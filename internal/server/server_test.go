package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/auth"
	"github.com/harpchad/td/internal/seed"
	"github.com/harpchad/td/internal/server"
	"github.com/harpchad/td/internal/store"
	"github.com/harpchad/td/internal/web"
)

// harness is a running server plus the credentials to talk to it.
type harness struct {
	*httptest.Server
	store    *store.Store
	srv      *server.Server
	token    string
	username string
	password string
	totp     string
	recovery []string
	now      time.Time
}

// testAccount is the credential every test logs in with.
const (
	testUsername = "chad"
	testPassword = "a long enough password"
)

// newServer starts an HTTP server over a scratch database loaded with
// testdata/seed.json, with its clock pinned to the fixture's, an account
// created, and a full-scope token minted. Phase 1's tests run through the
// same auth path everything else does.
func newServer(t *testing.T) *harness {
	t.Helper()

	d, err := seed.Load(filepath.Join("..", "..", "testdata", "seed.json"))
	if err != nil {
		t.Fatalf("load seed: %v", err)
	}
	now, loc, err := d.Clock()
	if err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(":memory:", store.Options{Location: loc})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Seed(context.Background(), d); err != nil {
		t.Fatal(err)
	}

	srv, err := server.New(st, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	srv.Now = func() time.Time { return now }

	// The browser UI is part of the server under test, so the web cases run
	// against the same handler the API cases do.
	if err := srv.AttachWeb(web.Load("", slog.New(slog.DiscardHandler)), ""); err != nil {
		t.Fatal(err)
	}

	h := &harness{store: st, srv: srv, now: now, username: testUsername, password: testPassword}

	// Light argon2 parameters here: the cost is the point only in the timing
	// test, which sets up its own account with the real ones.
	hash, err := auth.HashPassword(testPassword, testParams)
	if err != nil {
		t.Fatal(err)
	}
	secret, _, err := auth.NewTOTPSecret("td", testUsername)
	if err != nil {
		t.Fatal(err)
	}
	codes, hashes, err := auth.NewRecoveryCodes(3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAccount(context.Background(), testUsername, hash, secret, hashes, now); err != nil {
		t.Fatal(err)
	}
	h.recovery = codes
	if h.totp, err = auth.GenerateTOTP(secret, now); err != nil {
		t.Fatal(err)
	}

	tok, err := st.CreateToken(context.Background(), "test",
		"me", []string{api.ScopeRead, api.ScopeWrite, api.ScopeCapture}, now)
	if err != nil {
		t.Fatal(err)
	}
	h.token = tok.Secret

	h.Server = httptest.NewServer(srv.Handler())
	t.Cleanup(h.Close)
	return h
}

// testParams keep the suite fast where hashing cost is not what is under
// test. The timing assertion uses auth.DefaultParams.
var testParams = auth.Params{
	Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32,
}

// respMeta is what do returns instead of an *http.Response. Handing back a
// live response would leave every call site owning a body it does not read.
type respMeta struct {
	StatusCode int
	Header     http.Header
}

func do(t *testing.T, ts *harness, method, path string, body any) (respMeta, []byte) {
	t.Helper()
	return request(t, ts, method, path, body, map[string]string{
		"Authorization": "Bearer " + ts.token,
	})
}

// doAnon sends the same request with no credential.
func doAnon(t *testing.T, ts *harness, method, path string, body any) (respMeta, []byte) {
	t.Helper()
	return request(t, ts, method, path, body, nil)
}

func request(t *testing.T, ts *harness, method, path string, body any, headers map[string]string) (respMeta, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, ts.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Td-Client", api.Version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return respMeta{StatusCode: resp.StatusCode, Header: resp.Header}, out
}

func decodeInto(t *testing.T, body []byte, dst any) {
	t.Helper()
	if err := json.Unmarshal(body, dst); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
}

func TestListReturnsTheFixtureOrder(t *testing.T) {
	ts := newServer(t)
	resp, body := do(t, ts, http.MethodGet,
		"/api/v1/tasks?q=is%3Aopen+src%3Alocal+-is%3Ainbox+-is%3Asnoozed+-is%3Adeferred", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}

	var list api.TaskList
	decodeInto(t, body, &list)

	want := []int64{104, 102, 101, 114, 108, 106, 113, 103}
	if len(list.Tasks) != len(want) {
		t.Fatalf("got %d tasks, want %d", len(list.Tasks), len(want))
	}
	for i, n := range want {
		if list.Tasks[i].Num != n {
			t.Errorf("position %d = %d, want %d", i, list.Tasks[i].Num, n)
		}
	}
	if list.Total != len(want) {
		t.Errorf("total = %d, want %d", list.Total, len(want))
	}
}

// TestEveryResponseCarriesTheServerVersion covers the skew handshake. The
// container and the laptop update on different schedules, and the client
// warns off this header.
func TestEveryResponseCarriesTheServerVersion(t *testing.T) {
	ts := newServer(t)
	for _, path := range []string{"/healthz", "/api/v1/tasks", "/api/v1/people", "/api/v1/nope"} {
		resp, _ := do(t, ts, http.MethodGet, path, nil)
		if got := resp.Header.Get("X-Td-Server"); got != api.Version {
			t.Errorf("%s: X-Td-Server = %q, want %q", path, got, api.Version)
		}
	}
}

// TestEveryResponseCarriesTheServerClock covers the header a client renders
// relative date labels against. Without it, a client in another timezone
// labels a different set of tasks "Today" than the server bucketed there, and
// the two disagree about a list the server already ordered.
func TestEveryResponseCarriesTheServerClock(t *testing.T) {
	ts := newServer(t)
	resp, _ := do(t, ts, http.MethodGet, "/api/v1/tasks", nil)

	got := resp.Header.Get("X-Td-Now")
	if got == "" {
		t.Fatal("no X-Td-Now header")
	}
	at, err := time.Parse(time.RFC3339, got)
	if err != nil {
		t.Fatalf("X-Td-Now = %q, which does not parse: %v", got, err)
	}
	// The test server pins the fixture clock, so the header must report it
	// rather than the machine's wall clock.
	if want := "2026-08-03T10:30:00-05:00"; at.Format(time.RFC3339) != want {
		t.Errorf("X-Td-Now = %s, want the pinned %s", at.Format(time.RFC3339), want)
	}
}

func TestHealthzIsUnauthenticatedAndSaysNothing(t *testing.T) {
	ts := newServer(t)
	resp, body := do(t, ts, http.MethodGet, "/healthz", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if len(body) > 8 {
		t.Errorf("body = %q, health should carry no detail", body)
	}
}

// TestErrorStatusMapping checks each error code against the status the
// fixtures name.
func TestErrorStatusMapping(t *testing.T) {
	ts := newServer(t)

	tests := []struct {
		name   string
		method string
		path   string
		body   any
		status int
		code   string
	}{
		{
			name:   "a filter that does not parse is the user's typo",
			method: http.MethodGet, path: "/api/v1/tasks?q=foo%3Abar",
			status: http.StatusBadRequest, code: api.ErrBadRequest,
		},
		{
			name: "an unknown task", method: http.MethodGet, path: "/api/v1/tasks/9999",
			status: http.StatusNotFound, code: api.ErrNotFound,
		},
		{
			name:   "an illegal transition",
			method: http.MethodPatch, path: "/api/v1/tasks/111",
			body:   map[string]any{"status": "doing"},
			status: http.StatusConflict, code: api.ErrIllegalTransition,
		},
		{
			name:   "promoting an inbox task with neither a priority nor a due date",
			method: http.MethodPatch, path: "/api/v1/tasks/105",
			body:   map[string]any{"status": "todo"},
			status: http.StatusUnprocessableEntity, code: api.ErrInboxIncomplete,
		},
		{
			name:   "moving to waiting without saying who",
			method: http.MethodPatch, path: "/api/v1/tasks/102",
			body:   map[string]any{"status": "waiting"},
			status: http.StatusUnprocessableEntity, code: api.ErrWaitingNeedsPerson,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := do(t, ts, tc.method, tc.path, tc.body)
			if resp.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d: %s", resp.StatusCode, tc.status, body)
			}
			var apiErr api.Error
			decodeInto(t, body, &apiErr)
			if apiErr.Code != tc.code {
				t.Errorf("error = %q, want %q", apiErr.Code, tc.code)
			}
			if apiErr.Message == "" {
				t.Error("an error must say what to do about it")
			}
		})
	}
}

// TestIllegalTransitionBodyShape locks the 409 body the fixture spells out.
func TestIllegalTransitionBodyShape(t *testing.T) {
	ts := newServer(t)
	resp, body := do(t, ts, http.MethodPatch, "/api/v1/tasks/111",
		map[string]any{"status": "doing"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}
	var apiErr api.Error
	decodeInto(t, body, &apiErr)
	if apiErr.Code != "illegal_transition" || apiErr.From != "done" || apiErr.To != "doing" {
		t.Errorf("body = %+v, want illegal_transition done->doing", apiErr)
	}
	if apiErr.Message != "reopen to todo first" {
		t.Errorf("message = %q, want %q", apiErr.Message, "reopen to todo first")
	}
}

func TestCreateIsIdempotentOverHTTP(t *testing.T) {
	ts := newServer(t)
	in := map[string]any{"id": "01J0000000000000000000000A", "title": "sent twice"}

	resp, body := do(t, ts, http.MethodPost, "/api/v1/tasks", in)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}
	var first api.Task
	decodeInto(t, body, &first)

	_, body = do(t, ts, http.MethodPost, "/api/v1/tasks", in)
	var second api.Task
	decodeInto(t, body, &second)

	if first.Num != second.Num {
		t.Errorf("num %d then %d, a replay must not create a second row", first.Num, second.Num)
	}
}

// TestPatchTellsNullFromAbsent covers the presence rule: an absent key leaves
// a field alone and an explicit null clears it.
func TestPatchTellsNullFromAbsent(t *testing.T) {
	ts := newServer(t)

	_, body := do(t, ts, http.MethodPatch, "/api/v1/tasks/101", map[string]any{"title": "Renew the wildcard"})
	var kept api.Task
	decodeInto(t, body, &kept)
	if kept.DueAt == nil {
		t.Error("an absent due_at must leave the due date alone")
	}

	_, body = do(t, ts, http.MethodPatch, "/api/v1/tasks/101", map[string]any{"due_at": nil})
	var cleared api.Task
	decodeInto(t, body, &cleared)
	if cleared.DueAt != nil {
		t.Errorf("due_at = %v, an explicit null must clear it", *cleared.DueAt)
	}
}

func TestStaleIfMatchConflicts(t *testing.T) {
	ts := newServer(t)

	_, body := do(t, ts, http.MethodGet, "/api/v1/tasks/103", nil)
	var task api.Task
	decodeInto(t, body, &task)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPatch, ts.URL+"/api/v1/tasks/103",
		bytes.NewReader([]byte(`{"title":"renamed"}`)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+ts.token)
	req.Header.Set("If-Match", `"1999-01-01T00:00:00Z"`)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
}

// TestDeleteDrops covers the rule that DELETE never hard-deletes.
func TestDeleteDrops(t *testing.T) {
	ts := newServer(t)
	_, body := do(t, ts, http.MethodDelete, "/api/v1/tasks/103", nil)
	var task api.Task
	decodeInto(t, body, &task)
	if task.Status != api.StatusDropped {
		t.Errorf("status = %s, want dropped", task.Status)
	}

	resp, _ := do(t, ts, http.MethodGet, "/api/v1/tasks/103", nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("the row must still be there, got %d", resp.StatusCode)
	}
}

func TestCompleteReportsOpenChildren(t *testing.T) {
	ts := newServer(t)
	_, body := do(t, ts, http.MethodPost, "/api/v1/tasks/101/complete", nil)
	var res api.CompleteResult
	decodeInto(t, body, &res)
	if res.ChildrenOpen != 1 {
		t.Errorf("children_open = %d, want 1", res.ChildrenOpen)
	}
	if res.Task.Status != api.StatusDone {
		t.Errorf("status = %s, want done", res.Task.Status)
	}
}

func TestEventsAndUndoRoundTrip(t *testing.T) {
	ts := newServer(t)

	if _, body := do(t, ts, http.MethodPatch, "/api/v1/tasks/103",
		map[string]any{"title": "Order tires and an alignment"}); len(body) == 0 {
		t.Fatal("empty patch response")
	}

	_, body := do(t, ts, http.MethodGet, "/api/v1/events?since=0", nil)
	var events []api.Event
	decodeInto(t, body, &events)
	if len(events) != 1 || events[0].Kind != api.KindTaskUpdated {
		t.Fatalf("events = %+v, want one task.updated", events)
	}
	// Setting the harness up created an account and a token, both of which
	// wrote auth events. None of them belongs in the change feed.
	for _, e := range events {
		if strings.HasPrefix(e.Kind, "auth.") {
			t.Errorf("the change feed carries %s, which an agent has no business reading", e.Kind)
		}
	}

	_, body = do(t, ts, http.MethodPost, "/api/v1/undo", nil)
	var res api.UndoResult
	decodeInto(t, body, &res)
	if res.Task == nil || res.Task.Title != "Order tires" {
		t.Errorf("undo left %v, want the original title back", res.Task)
	}

	resp, _ := do(t, ts, http.MethodPost, "/api/v1/undo", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("a second undo with nothing left = %d, want 404", resp.StatusCode)
	}
}

// TestLimitTruncatesTheTopOfTheOrder covers what replaced pagination. limit
// takes the top N and total keeps reporting the full count, so a caller can
// always tell a slice from the whole answer.
func TestLimitTruncatesTheTopOfTheOrder(t *testing.T) {
	ts := newServer(t)

	_, body := do(t, ts, http.MethodGet, "/api/v1/tasks?q=is%3Aopen", nil)
	var all api.TaskList
	decodeInto(t, body, &all)
	if len(all.Tasks) != all.Total || all.Total < 4 {
		t.Fatalf("unfiltered read returned %d of %d", len(all.Tasks), all.Total)
	}

	_, body = do(t, ts, http.MethodGet, "/api/v1/tasks?q=is%3Aopen&limit=3", nil)
	var top api.TaskList
	decodeInto(t, body, &top)

	if len(top.Tasks) != 3 {
		t.Fatalf("limit=3 returned %d tasks", len(top.Tasks))
	}
	if top.Total != all.Total {
		t.Errorf("total = %d under a limit, want the untruncated %d", top.Total, all.Total)
	}
	for i := range top.Tasks {
		if top.Tasks[i].Num != all.Tasks[i].Num {
			t.Errorf("position %d = %d, want %d: limit must take the top of the same order",
				i, top.Tasks[i].Num, all.Tasks[i].Num)
		}
	}
}

// TestNoPaginationOnTheTaskList locks the decision that a filtered list is
// read whole. An unknown query parameter must not silently slice the result.
func TestNoPaginationOnTheTaskList(t *testing.T) {
	ts := newServer(t)

	_, body := do(t, ts, http.MethodGet, "/api/v1/tasks?q=is%3Aopen", nil)
	var all api.TaskList
	decodeInto(t, body, &all)

	if !bytes.Contains(body, []byte(`"total"`)) {
		t.Fatal("the list body should report a total")
	}
	if bytes.Contains(body, []byte(`"cursor"`)) {
		t.Error("the task list must not carry a cursor")
	}

	_, body = do(t, ts, http.MethodGet, "/api/v1/tasks?q=is%3Aopen&cursor=3", nil)
	var withCursor api.TaskList
	decodeInto(t, body, &withCursor)
	if len(withCursor.Tasks) != len(all.Tasks) {
		t.Errorf("a stray cursor parameter changed the result: %d vs %d",
			len(withCursor.Tasks), len(all.Tasks))
	}
}

func TestSavedFiltersShipWithDefaults(t *testing.T) {
	ts := newServer(t)
	_, body := do(t, ts, http.MethodGet, "/api/v1/filters", nil)
	var filters []api.SavedFilter
	decodeInto(t, body, &filters)

	byName := map[string]string{}
	for _, f := range filters {
		byName[f.Name] = f.Query
	}
	for _, want := range []string{"Today", "Inbox", "Waiting", "Overdue"} {
		if _, ok := byName[want]; !ok {
			t.Errorf("no default filter named %s", want)
		}
	}
	if byName["Today"] != "is:open src:local -is:inbox -is:snoozed -is:deferred" {
		t.Errorf("Today = %q, want the home view default", byName["Today"])
	}
}

// TestSavedFilterRejectsAQueryThatDoesNotParse keeps a broken filter out of a
// number key, where it would fail on every press instead of once on save.
func TestSavedFilterRejectsAQueryThatDoesNotParse(t *testing.T) {
	ts := newServer(t)
	resp, body := do(t, ts, http.MethodPost, "/api/v1/filters",
		map[string]any{"slot": 7, "name": "Broken", "query": "foo:bar"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, body)
	}
}

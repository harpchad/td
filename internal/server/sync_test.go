package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/client"
	"github.com/harpchad/td/internal/plugins/planner"
)

// The whole path, end to end: hand-written Graph fixtures through the real
// plugin, over HTTP with a scoped token, into a real server, and then the
// mirror is inspected. Nothing here contacts a Microsoft tenant, and nothing
// can: the Graph client is aimed at an httptest server serving files from
// testdata/plugins/planner.

// fakeGraph serves the Planner fixtures.
type fakeGraph struct {
	*httptest.Server
	tasksFile string
}

func newFakeGraph(t *testing.T) *fakeGraph {
	t.Helper()
	g := &fakeGraph{tasksFile: "tasks.json"}

	read := func(name string) []byte {
		body, err := os.ReadFile(filepath.Join("..", "..", "testdata", "plugins", "planner", name))
		if err != nil {
			t.Fatal(err)
		}
		return body
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1.0/planner/plans/{plan}/tasks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(read(g.tasksFile))
	})
	mux.HandleFunc("GET /v1.0/users/{id}", func(w http.ResponseWriter, r *http.Request) {
		var list planner.GraphUserList
		if err := json.Unmarshal(read("users.json"), &list); err != nil {
			t.Fatal(err)
		}
		for _, u := range list.Value {
			if u.ID == r.PathValue("id") {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(u)
				return
			}
		}
		http.Error(w, `{"error":{"code":"Request_ResourceNotFound"}}`, http.StatusNotFound)
	})

	g.Server = httptest.NewServer(mux)
	t.Cleanup(g.Close)
	return g
}

// pluginClient is a td client carrying a sync-scoped token, which is all a
// plugin ever gets.
func pluginClient(t *testing.T, ts *harness) *client.Client {
	t.Helper()
	tok, err := ts.store.CreateToken(t.Context(), "planner", "plugin:planner",
		[]string{api.ScopeSyncPrefix + planner.Source, api.ScopeRead}, ts.now)
	if err != nil {
		t.Fatal(err)
	}
	return client.New(client.Config{Server: ts.URL, Token: tok.Secret})
}

// TestPlannerMirrorsIntoTd is the phase's definition of done: a Planner
// mirror populates from fixtures and links back to the original item.
func TestPlannerMirrorsIntoTd(t *testing.T) {
	ts := newServer(t)
	graph := newFakeGraph(t)
	ctx := context.Background()

	// The Graph object ids are mapped onto people first, which is what
	// person_identity is for. Without this a first sync links nobody: an
	// unmapped identity whose name collides with somebody already known is
	// skipped rather than guessed at, because merging two different people is
	// worse than a missing link.
	for handle, objectID := range map[string]string{
		"stacey":   "8f3d2e11-0000-4a2b-9c3d-000000000001",
		"mikah":    "8f3d2e11-0000-4a2b-9c3d-000000000002",
		"brandiss": "8f3d2e11-0000-4a2b-9c3d-000000000003",
	} {
		person, err := ts.store.PersonByHandle(ctx, handle)
		if err != nil {
			t.Fatal(err)
		}
		if err := ts.store.LinkIdentity(ctx, person.ID, planner.Source, objectID); err != nil {
			t.Fatal(err)
		}
	}

	c := pluginClient(t, ts)
	plugin := planner.New(planner.Config{
		PlanIDs:    []string{"xqQg5FS2LkCp935s-FIFm2QAFkHM"},
		GraphToken: "graph-token",
		Endpoint:   graph.URL + "/v1.0",
	})

	res, err := planner.Run(ctx, plugin, c, ts.now, ts.now.Location())
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if res.Created != 3 {
		t.Fatalf("created %d, want the fixture's 3", res.Created)
	}

	mirrored, err := ts.store.List(ctx, "src:planner", ts.now)
	if err != nil {
		t.Fatal(err)
	}
	if len(mirrored) != 3 {
		t.Fatalf("%d mirrored tasks", len(mirrored))
	}

	byExternal := map[string]api.Task{}
	for _, task := range mirrored {
		if task.ExternalID == nil {
			t.Fatalf("task %d has no external id", task.Num)
		}
		byExternal[*task.ExternalID] = task
	}

	renew := byExternal["01_TASK_RENEW"]
	if renew.Title != "Renew the wildcard certificate" {
		t.Errorf("title = %q", renew.Title)
	}
	// Every mirrored task carries external_url, which is what makes one
	// keystroke open the real thing.
	if renew.ExternalURL == nil || !strings.Contains(*renew.ExternalURL, "01_TASK_RENEW") {
		t.Errorf("external_url = %v", renew.ExternalURL)
	}
	if renew.DueAt == nil || *renew.DueAt != "2026-08-04" {
		t.Errorf("due = %v", renew.DueAt)
	}
	// The Graph object ids resolved onto people, with names from the
	// directory rather than "unknown".
	roles := map[string]string{}
	for _, p := range renew.People {
		roles[p.Role] = p.Name
	}
	if roles[api.RoleAssignee] != "Stacey" {
		t.Errorf("assignee = %q, want the person the identity maps onto", roles[api.RoleAssignee])
	}
	if roles[api.RoleAssigner] != "Brandiss" {
		t.Errorf("assigner = %q, want the person the identity maps onto", roles[api.RoleAssigner])
	}

	// One Stacey, not two. This is the whole reason identities are mapped
	// rather than matched on a name.
	people, err := ts.store.People(ctx)
	if err != nil {
		t.Fatal(err)
	}
	staceys := 0
	for _, p := range people {
		if strings.HasPrefix(p.Name, "Stacey") {
			staceys++
		}
	}
	if staceys != 1 {
		t.Errorf("%d Staceys after a sync", staceys)
	}

	// The detail page puts the source on the first line.
	session := login(t, ts)
	_, html := page(t, ts, session, "/t/"+itoaTest(renew.Num))
	if !strings.Contains(html, *renew.ExternalURL) {
		t.Error("the detail page does not link back to Planner")
	}
}

// TestASecondRunAppliesOnlyWhatMoved is the fixture pair doing its job: one
// task changed, one is byte-identical, one disappeared, one is new.
func TestASecondRunAppliesOnlyWhatMoved(t *testing.T) {
	ts := newServer(t)
	graph := newFakeGraph(t)
	ctx := context.Background()

	c := pluginClient(t, ts)
	plugin := planner.New(planner.Config{
		PlanIDs:    []string{"xqQg5FS2LkCp935s-FIFm2QAFkHM"},
		GraphToken: "graph-token",
		Endpoint:   graph.URL + "/v1.0",
	})

	if _, err := planner.Run(ctx, plugin, c, ts.now, ts.now.Location()); err != nil {
		t.Fatal(err)
	}

	// Something local, on the task that is about to change upstream.
	mirrored, err := ts.store.List(ctx, "src:planner", ts.now)
	if err != nil {
		t.Fatal(err)
	}
	var renewID string
	for _, task := range mirrored {
		if task.ExternalID != nil && *task.ExternalID == "01_TASK_RENEW" {
			renewID = task.ID
		}
	}
	p1 := 1
	notes := "Vendor quoted three days."
	if _, err := ts.store.Patch(ctx, "me", renewID, api.TaskPatch{
		Priority: &p1, Notes: &notes,
		Tags:     &[]string{"certs"},
		Presence: map[string]bool{"priority": true},
	}, "", ts.now); err != nil {
		t.Fatal(err)
	}

	graph.tasksFile = "tasks_updated.json"
	res, err := planner.Run(ctx, plugin, c, ts.now, ts.now.Location())
	if err != nil {
		t.Fatal(err)
	}

	if res.Created != 1 {
		t.Errorf("created %d, want the one new task", res.Created)
	}
	if res.Updated != 1 {
		t.Errorf("updated %d, want the one whose etag moved", res.Updated)
	}
	if res.Unchanged != 1 {
		t.Errorf("unchanged %d, want the one that did not move", res.Unchanged)
	}
	if res.Gone != 1 {
		t.Errorf("gone %d, want the one that disappeared", res.Gone)
	}

	// The upstream change landed and nothing local went with it.
	after, err := ts.store.Get(ctx, renewID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Title != "Renew the wildcard certificate (staging first)" {
		t.Errorf("title = %q", after.Title)
	}
	if after.Status != api.StatusDoing {
		t.Errorf("status = %q, want doing for percentComplete 50", after.Status)
	}
	if after.Priority == nil || *after.Priority != 1 {
		t.Errorf("priority = %v; a sync overwrote it", after.Priority)
	}
	if after.Notes != notes {
		t.Errorf("notes = %q", after.Notes)
	}
	if len(after.Tags) != 1 || after.Tags[0] != "certs" {
		t.Errorf("tags = %v", after.Tags)
	}

	// The task that disappeared is marked, not deleted.
	all, err := ts.store.List(ctx, "src:planner", ts.now)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, task := range all {
		if task.ExternalID != nil && *task.ExternalID == "01_TASK_TIRES" {
			found = true
			if !task.UpstreamGone {
				t.Error("the task that disappeared is not marked upstream_gone")
			}
		}
	}
	if !found {
		t.Error("the task that disappeared was deleted")
	}

	// A completed one comes across as done rather than as something to do.
	for _, task := range all {
		if task.ExternalID != nil && *task.ExternalID == "01_TASK_ONBOARD" && task.Status != api.StatusDone {
			t.Errorf("a task at percentComplete 100 arrived as %q", task.Status)
		}
	}
}

// TestRunningTwiceWithNoUpstreamChangeWritesNothing. The plugin has no delta
// query, so it re-reads the whole plan every time and idempotence is the only
// thing standing between that and an event log full of noise.
func TestRunningTwiceWithNoUpstreamChangeWritesNothing(t *testing.T) {
	ts := newServer(t)
	graph := newFakeGraph(t)
	ctx := context.Background()

	c := pluginClient(t, ts)
	plugin := planner.New(planner.Config{
		PlanIDs:    []string{"xqQg5FS2LkCp935s-FIFm2QAFkHM"},
		GraphToken: "graph-token",
		Endpoint:   graph.URL + "/v1.0",
	})

	if _, err := planner.Run(ctx, plugin, c, ts.now, ts.now.Location()); err != nil {
		t.Fatal(err)
	}
	before, err := ts.store.Events(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}

	for range 3 {
		res, err := planner.Run(ctx, plugin, c, ts.now, ts.now.Location())
		if err != nil {
			t.Fatal(err)
		}
		if res.Created != 0 || res.Updated != 0 || res.Gone != 0 {
			t.Errorf("a repeat run did work: %+v", res)
		}
	}

	after, err := ts.store.Events(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("three no-op runs wrote %d events", len(after)-len(before))
	}
}

func itoaTest(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// TestExportRoundTripsOverTheAPI is the v1 done criterion end to end: the
// bytes a person actually gets from `td export --json` go back in and produce
// the same list.
func TestExportRoundTripsOverTheAPI(t *testing.T) {
	ts := newServer(t)

	resp, body := do(t, ts, http.MethodGet, "/api/v1/export", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("export = %d: %s", resp.StatusCode, body)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Error("the export is cacheable")
	}

	// A fresh server, and the same bytes back in.
	restored := newEmptyServer(t)
	var doc json.RawMessage = body
	resp, out := do(t, restored, http.MethodPost, "/api/v1/import", doc)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("import = %d: %s", resp.StatusCode, out)
	}

	const home = "?q=is%3Aopen+src%3Alocal+-is%3Ainbox+-is%3Asnoozed+-is%3Adeferred"
	_, before := do(t, ts, http.MethodGet, "/api/v1/tasks"+home, nil)
	_, after := do(t, restored, http.MethodGet, "/api/v1/tasks"+home, nil)

	var a, b api.TaskList
	decodeInto(t, before, &a)
	decodeInto(t, after, &b)
	if len(a.Tasks) != len(b.Tasks) {
		t.Fatalf("%d tasks before, %d after", len(a.Tasks), len(b.Tasks))
	}
	for i := range a.Tasks {
		if a.Tasks[i].Num != b.Tasks[i].Num || a.Tasks[i].Title != b.Tasks[i].Title {
			t.Errorf("position %d: %d %q before, %d %q after",
				i, a.Tasks[i].Num, a.Tasks[i].Title, b.Tasks[i].Num, b.Tasks[i].Title)
		}
	}

	// Importing twice is refused rather than doubling the list.
	resp, _ = do(t, restored, http.MethodPost, "/api/v1/import", doc)
	if resp.StatusCode == http.StatusOK {
		t.Error("a second import was accepted")
	}
}

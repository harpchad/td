package mcpsrv_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/mcpsrv"
	"github.com/harpchad/td/internal/seed"
	"github.com/harpchad/td/internal/store"
)

// These exercise the tools directly, with a principal injected the way the
// HTTP server injects one. internal/server covers the authentication in front
// of them; what is under test here is what each tool does once a caller is
// through the door.

type fixture struct {
	*httptest.Server
	store *store.Store
	now   time.Time
}

func serve(t *testing.T, scopes ...string) (*fixture, *mcp.ClientSession) {
	t.Helper()

	d, err := seed.Load(filepath.Join("..", "..", "testdata", "seed.json"))
	if err != nil {
		t.Fatal(err)
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

	srv := mcpsrv.New(st, func() time.Time { return now })
	handler := srv.Handler()

	f := &fixture{store: st, now: now}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := mcpsrv.WithPrincipal(r.Context(), &mcpsrv.Principal{
			Actor: "mcp:test", Scopes: scopes,
		})
		handler.ServeHTTP(w, r.WithContext(ctx))
	}))
	t.Cleanup(f.Close)

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(t.Context(),
		&mcp.StreamableClientTransport{Endpoint: f.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return f, session
}

func call(t *testing.T, session *mcp.ClientSession, name string, args map[string]any, out any) *mcp.CallToolResult {
	t.Helper()
	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if out != nil && !res.IsError {
		body, err := json.Marshal(res.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(body, out); err != nil {
			t.Fatalf("%s result: %v (%s)", name, err, body)
		}
	}
	return res
}

func text(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}

type taskEnvelope struct {
	Task struct {
		Num      int64    `json:"num"`
		ID       string   `json:"id"`
		Title    string   `json:"title"`
		Status   string   `json:"status"`
		Priority *int     `json:"priority"`
		DueAt    *string  `json:"due_at"`
		Tags     []string `json:"tags"`
		Notes    string   `json:"notes"`
	} `json:"task"`
}

type listEnvelope struct {
	Tasks []struct {
		Num   int64  `json:"num"`
		Title string `json:"title"`
	} `json:"tasks"`
	Total int `json:"total"`
}

// TestTheRevisionIsPinned covers what CLAUDE.md asks for by name. MCP
// authorization changed three times in a year, so which revision this code
// assumes is a fact in the source rather than a memory, and the README says
// the same number because that is where somebody deploying this looks first.
func TestTheRevisionIsPinned(t *testing.T) {
	if mcpsrv.Revision != "2026-07-28" {
		t.Errorf("Revision = %q", mcpsrv.Revision)
	}
	body, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), mcpsrv.Revision) {
		t.Errorf("the README does not name the MCP revision %s", mcpsrv.Revision)
	}
}

// TestWhatsNextIsTheDefaultOrder covers the tool an agent reaches for when
// asked "what should I do now". It has to agree with what the TUI and the web
// UI show, which means the same filter and the same comparator.
func TestWhatsNextIsTheDefaultOrder(t *testing.T) {
	_, session := serve(t, api.ScopeRead)

	var out listEnvelope
	if res := call(t, session, "whats_next", map[string]any{"limit": 3}, &out); res.IsError {
		t.Fatalf("whats_next: %s", text(res))
	}
	if len(out.Tasks) != 3 {
		t.Fatalf("%d tasks, want the limit of 3", len(out.Tasks))
	}
	// The home view's order from sort_cases.json: 104, 102, 101.
	want := []int64{104, 102, 101}
	for i, n := range want {
		if out.Tasks[i].Num != n {
			t.Errorf("position %d = %d, want %d", i, out.Tasks[i].Num, n)
		}
	}
	// Total reports the whole result, not the truncated page, or "3 of 3"
	// reads as inbox zero when eight things are waiting.
	if out.Total != 8 {
		t.Errorf("total = %d, want the full open list", out.Total)
	}
}

// TestCaptureReadsTheInlineGrammar covers the one-line capture path. It is
// the same parser quick-add uses, so an agent and a person typing the same
// string get the same task.
func TestCaptureReadsTheInlineGrammar(t *testing.T) {
	_, session := serve(t, api.ScopeRead, api.ScopeCapture)

	var out taskEnvelope
	res := call(t, session, "capture",
		map[string]any{"title": "renew the wildcard cert #certs p:2"}, &out)
	if res.IsError {
		t.Fatalf("capture: %s", text(res))
	}
	if out.Task.Title != "renew the wildcard cert" {
		t.Errorf("title = %q, want the tokens stripped out of it", out.Task.Title)
	}
	if out.Task.Priority == nil || *out.Task.Priority != 2 {
		t.Errorf("priority = %v", out.Task.Priority)
	}
	if len(out.Task.Tags) != 1 || out.Task.Tags[0] != "certs" {
		t.Errorf("tags = %v", out.Task.Tags)
	}
	// A priority given inline is what lets a task leave the inbox, so it did.
	if out.Task.Status != api.StatusTodo {
		t.Errorf("status = %s, want todo since a priority was given", out.Task.Status)
	}

	// Capture with nothing but tokens is refused rather than creating a task
	// with an empty title.
	if res := call(t, session, "capture", map[string]any{"title": "#certs @stacey"}, nil); !res.IsError {
		t.Error("a line with no title was accepted")
	}
}

// TestAddNoteAppends is the one that would be quietly destructive if it were
// wrong. A note is a running record and nobody backs it up.
func TestAddNoteAppends(t *testing.T) {
	f, session := serve(t, api.ScopeRead, api.ScopeWrite)

	// The fixture's tasks carry no notes, so the first note is what a second
	// one has to survive.
	if res := call(t, session, "add_note",
		map[string]any{"id": "101", "text": "Cert expires on the 4th."}, nil); res.IsError {
		t.Fatalf("add_note: %s", text(res))
	}
	before, err := f.store.GetByNum(t.Context(), 101)
	if err != nil {
		t.Fatal(err)
	}
	if before.Notes == "" {
		t.Fatal("the first note did not land")
	}

	var out taskEnvelope
	res := call(t, session, "add_note",
		map[string]any{"id": "101", "text": "Vendor quoted three days."}, &out)
	if res.IsError {
		t.Fatalf("add_note: %s", text(res))
	}
	if !strings.Contains(out.Task.Notes, before.Notes) {
		t.Error("the existing notes were replaced")
	}
	if !strings.Contains(out.Task.Notes, "Vendor quoted three days.") {
		t.Error("the new note is not there")
	}

	// An empty note is refused rather than appending two blank lines.
	if res := call(t, session, "add_note", map[string]any{"id": "101", "text": "  "}, nil); !res.IsError {
		t.Error("an empty note was accepted")
	}
}

// TestUpdateTaskClearsADateOnlyWhenAsked covers the argument that would
// otherwise be dangerous: an omitted field arrives as an empty string, and
// clearing a due date nobody mentioned is the worst kind of surprise.
func TestUpdateTaskClearsADateOnlyWhenAsked(t *testing.T) {
	f, session := serve(t, api.ScopeRead, api.ScopeWrite)

	before, err := f.store.GetByNum(t.Context(), 101)
	if err != nil {
		t.Fatal(err)
	}
	if before.DueAt == nil {
		t.Fatal("the fixture task has no due date, so this proves nothing")
	}

	// Changing only the title leaves the date alone.
	var out taskEnvelope
	res := call(t, session, "update_task",
		map[string]any{"id": "101", "title": "Renew the wildcard cert"}, &out)
	if res.IsError {
		t.Fatalf("update_task: %s", text(res))
	}
	if out.Task.DueAt == nil {
		t.Fatal("a title change cleared the due date")
	}

	// "none" clears it, explicitly. A fresh destination, because due_at is
	// omitted from the JSON once it is nil and decoding into the previous
	// value would leave the old pointer standing.
	var cleared taskEnvelope
	res = call(t, session, "update_task", map[string]any{"id": "101", "due_at": "none"}, &cleared)
	if res.IsError {
		t.Fatalf("update_task: %s", text(res))
	}
	if cleared.Task.DueAt != nil {
		t.Errorf("due_at = %v, want it cleared", *cleared.Task.DueAt)
	}
}

// TestRecentActivityReturnsAResumableCursor covers the change feed. The event
// log is what an MCP client polls instead of re-listing, so the cursor has to
// be usable as the next `since`.
func TestRecentActivityReturnsAResumableCursor(t *testing.T) {
	_, session := serve(t, api.ScopeRead, api.ScopeCapture)

	// The seed loads rows directly, so the log starts empty. One capture is
	// what gives the cursor something to point at.
	if res := call(t, session, "capture", map[string]any{"title": "the first thing"}, nil); res.IsError {
		t.Fatalf("capture: %s", text(res))
	}

	var first struct {
		Events []struct {
			Seq   int64  `json:"seq"`
			Actor string `json:"actor"`
			Kind  string `json:"kind"`
			Task  int64  `json:"task"`
		} `json:"events"`
		Cursor int64 `json:"cursor"`
	}
	if res := call(t, session, "recent_activity", map[string]any{}, &first); res.IsError {
		t.Fatalf("recent_activity: %s", text(res))
	}
	if first.Cursor == 0 {
		t.Fatal("no cursor came back")
	}

	if res := call(t, session, "capture", map[string]any{"title": "something new"}, nil); res.IsError {
		t.Fatalf("capture: %s", text(res))
	}

	var second struct {
		Events []struct {
			Seq   int64  `json:"seq"`
			Actor string `json:"actor"`
			Task  int64  `json:"task"`
		} `json:"events"`
		Cursor int64 `json:"cursor"`
	}
	if res := call(t, session, "recent_activity",
		map[string]any{"since": first.Cursor}, &second); res.IsError {
		t.Fatalf("recent_activity: %s", text(res))
	}
	if len(second.Events) != 1 {
		t.Fatalf("%d events since the cursor, want just the capture", len(second.Events))
	}
	if second.Events[0].Actor != "mcp:test" {
		t.Errorf("actor = %q, want the agent's", second.Events[0].Actor)
	}
	// Numbers, not ids: the rest of the tool output speaks in numbers and a
	// model asked to join two id spaces will get it wrong eventually.
	if second.Events[0].Task == 0 {
		t.Error("the event carries no task number")
	}
	if second.Cursor <= first.Cursor {
		t.Error("the cursor did not advance")
	}
}

// TestPersonAgendaIsTheSectionsInOrder covers what an agent reads before a
// 1:1. The order of the sections is the point: what they owe you, what you
// owe them, what is blocked on them.
func TestPersonAgendaIsTheSectionsInOrder(t *testing.T) {
	_, session := serve(t, api.ScopeRead)

	var out struct {
		Person struct {
			Handle string `json:"handle"`
			Name   string `json:"name"`
		} `json:"person"`
		Assigned []struct{ Num int64 } `json:"assigned"`
		Waiting  []struct{ Num int64 } `json:"waiting"`
	}
	// The leading @ is how people write it and how the filter grammar reads
	// it, so both forms have to work.
	for _, ref := range []string{"mikah", "@mikah"} {
		if res := call(t, session, "person_agenda", map[string]any{"person": ref}, &out); res.IsError {
			t.Fatalf("person_agenda(%s): %s", ref, text(res))
		}
		if out.Person.Handle != "mikah" {
			t.Errorf("handle = %q", out.Person.Handle)
		}
	}
	if len(out.Assigned) == 0 {
		t.Error("nothing assigned; the fixture has Mikah on 102")
	}
	if len(out.Waiting) == 0 {
		t.Error("nothing waiting; the fixture has 106 blocked on Mikah")
	}

	// An unknown handle is an error the model can read, not a silent empty
	// agenda that reads as "they owe you nothing".
	if res := call(t, session, "person_agenda", map[string]any{"person": "nobody"}, nil); !res.IsError {
		t.Error("an unknown handle returned an agenda")
	}
}

// TestABadFilterComesBackAsAToolError, not as a transport failure. A bad
// filter is the model's typo, and it needs the parser's message to fix it.
func TestABadFilterComesBackAsAToolError(t *testing.T) {
	_, session := serve(t, api.ScopeRead)

	res := call(t, session, "search_tasks", map[string]any{"query": "is:nonsense"}, nil)
	if !res.IsError {
		t.Fatal("a bad filter was accepted")
	}
	if !strings.Contains(text(res), "nonsense") {
		t.Errorf("the error does not name the problem: %s", text(res))
	}
}

// TestEveryToolRefusesWithoutItsScope is the whole scope story in one place.
// The everyday assistant gets read plus capture; write is for a token pasted
// deliberately.
func TestEveryToolRefusesWithoutItsScope(t *testing.T) {
	_, session := serve(t) // no scopes at all

	for _, tool := range []struct {
		name string
		args map[string]any
	}{
		{"search_tasks", map[string]any{"query": ""}},
		{"get_task", map[string]any{"id": "101"}},
		{"whats_next", map[string]any{}},
		{"list_people", map[string]any{}},
		{"person_agenda", map[string]any{"person": "mikah"}},
		{"recent_activity", map[string]any{}},
		{"capture", map[string]any{"title": "nope"}},
		{"create_task", map[string]any{"title": "nope"}},
		{"update_task", map[string]any{"id": "101", "title": "nope"}},
		{"complete_task", map[string]any{"id": "101"}},
		{"add_note", map[string]any{"id": "101", "text": "nope"}},
	} {
		res := call(t, session, tool.name, tool.args, nil)
		if !res.IsError {
			t.Errorf("%s ran with no scopes at all", tool.name)
			continue
		}
		if !strings.Contains(text(res), "scope") {
			t.Errorf("%s: the refusal does not say why: %s", tool.name, text(res))
		}
	}
}

// TestCompletingSaysWhatItDidNotDo covers the subtask rule from the model's
// side. The parent is the commitment and the server never cascades; a tool
// that reported nothing would leave an agent to decide for itself.
func TestCompletingSaysWhatItDidNotDo(t *testing.T) {
	_, session := serve(t, api.ScopeRead, api.ScopeWrite)

	// 101 has an open subtask in the fixture.
	res := call(t, session, "complete_task", map[string]any{"id": "101"}, nil)
	if res.IsError {
		t.Fatalf("complete_task: %s", text(res))
	}
	if !strings.Contains(text(res), "subtask") {
		t.Errorf("the result does not mention the open subtask: %s", text(res))
	}
}

// TestEveryResultCarriesItsDataAsText.
//
// The specification says a tool returning structured content SHOULD also
// return the serialized JSON in a TextContent block, and skipping it is not a
// theoretical incompatibility: a client reading only `content` saw
// "50 tasks match" with no tasks and reported that this server returns counts
// instead of results. From the other end that is exactly what it looked like.
func TestEveryResultCarriesItsDataAsText(t *testing.T) {
	_, session := serve(t, api.ScopeRead)

	for _, tc := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"search_tasks", map[string]any{"query": "is:open"}, `"tasks"`},
		{"whats_next", map[string]any{"limit": 3}, `"tasks"`},
		{"list_people", map[string]any{}, `"people"`},
		{"recent_activity", map[string]any{"limit": 3}, `"events"`},
	} {
		res := call(t, session, tc.tool, tc.args, nil)
		if res.IsError {
			t.Errorf("%s: %s", tc.tool, text(res))
			continue
		}
		body := text(res)
		if !strings.Contains(body, tc.want) {
			t.Errorf("%s returned no %s in its text content:\n  %s", tc.tool, tc.want, body)
		}
	}
}

// TestWhatsNextSaysWhichListItAnswered. Its zero was read as "you have no open
// tasks" when it meant "none on your own list", and the next call disagreed
// with it. A summary that names its filter cannot be misread that way.
func TestWhatsNextSaysWhichListItAnswered(t *testing.T) {
	_, session := serve(t, api.ScopeRead)

	res := call(t, session, "whats_next", map[string]any{"limit": 3}, nil)
	if res.IsError {
		t.Fatal(text(res))
	}
	body := text(res)
	if !strings.Contains(body, "src:local") {
		t.Errorf("whats_next does not say what it filtered on:\n  %s", body)
	}
}

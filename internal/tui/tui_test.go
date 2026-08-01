package tui_test

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

	tea "charm.land/bubbletea/v2"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/client"
	"github.com/harpchad/td/internal/seed"
	"github.com/harpchad/td/internal/tui"
)

// The TUI is driven through Update and read back through View, which is what
// makes it testable without a terminal. Every case here is a property the
// spec states about the terminal client.

func TestMain(m *testing.M) {
	// bubblezone needs a global manager before anything marks a region.
	zone.NewGlobal()
	code := m.Run()
	zone.Close()
	os.Exit(code)
}

// fakeServer answers the handful of routes the TUI calls, from the fixture
// data, so the tests exercise the real client and the real rendering.
type fakeServer struct {
	*httptest.Server
	tasks     []api.Task
	collapsed map[string]bool
	lastQuery string
	// view is the remembered filter, as the real /ui/filter stores it.
	view       api.ViewFilter
	viewWrites []string
	completed  []string
	undos      int
	created    []api.TaskCreate
	patched    []map[string]any
	snoozed    []api.SnoozeRequest
	linked     []personLink
	series     []map[string]any
}

type personLink struct{ person, role string }

func newFake(t *testing.T) *fakeServer {
	t.Helper()

	d, err := seed.Load(filepath.Join("..", "..", "testdata", "seed.json"))
	if err != nil {
		t.Fatal(err)
	}
	now, _, err := d.Clock()
	if err != nil {
		t.Fatal(err)
	}

	f := &fakeServer{collapsed: map[string]bool{}}
	f.tasks = fixtureTasks(d)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		f.lastQuery = r.URL.Query().Get("q")
		out := f.tasks
		// Just enough filtering for the tests that change the filter.
		if q := f.lastQuery; strings.HasPrefix(q, "#") {
			tag := strings.TrimPrefix(q, "#")
			var kept []api.Task
			for _, task := range out {
				for _, have := range task.Tags {
					if have == tag {
						kept = append(kept, task)
						break
					}
				}
			}
			out = kept
		}
		writeJSON(w, api.TaskList{Tasks: out, Total: len(out)})
	})
	mux.HandleFunc("GET /api/v1/filters", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, []api.SavedFilter{
			{Slot: 1, Name: "Today", Query: "is:open src:local -is:inbox"},
			{Slot: 3, Name: "Inbox", Query: "is:inbox"},
		})
	})
	mux.HandleFunc("GET /api/v1/ui/folds", func(w http.ResponseWriter, _ *http.Request) {
		ids := []string{}
		for id, on := range f.collapsed {
			if on {
				ids = append(ids, id)
			}
		}
		writeJSON(w, api.Folds{Collapsed: ids})
	})
	mux.HandleFunc("GET /api/v1/ui/filter", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, f.view)
	})
	mux.HandleFunc("PUT /api/v1/ui/filter", func(w http.ResponseWriter, r *http.Request) {
		var req api.ViewFilter
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.view = api.ViewFilter{Filter: req.Filter, Chosen: true}
		f.viewWrites = append(f.viewWrites, req.Filter)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /api/v1/ui/folds/{id}", func(w http.ResponseWriter, r *http.Request) {
		var req api.FoldRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.collapsed[r.PathValue("id")] = req.Collapsed
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /api/v1/tasks/{id}/complete", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		f.completed = append(f.completed, id)
		for i := range f.tasks {
			if f.tasks[i].ID == id {
				f.tasks[i].Status = api.StatusDone
				writeJSON(w, api.CompleteResult{Task: f.tasks[i], ChildrenOpen: f.tasks[i].ChildrenTotal - f.tasks[i].ChildrenDone})
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("POST /api/v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		var in api.TaskCreate
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.created = append(f.created, in)
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, api.Task{ID: in.ID, Num: 999, Title: in.Title, Status: api.StatusInbox})
	})
	mux.HandleFunc("PATCH /api/v1/tasks/{id}", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.patched = append(f.patched, body)
		for i := range f.tasks {
			if f.tasks[i].ID == r.PathValue("id") {
				writeJSON(w, f.tasks[i])
				return
			}
		}
		writeJSON(w, api.Task{})
	})
	mux.HandleFunc("POST /api/v1/tasks/{id}/snooze", func(w http.ResponseWriter, r *http.Request) {
		var req api.SnoozeRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.snoozed = append(f.snoozed, req)
		writeJSON(w, api.Task{ID: r.PathValue("id")})
	})
	mux.HandleFunc("GET /api/v1/people/{ref}", func(w http.ResponseWriter, r *http.Request) {
		ref := r.PathValue("ref")
		writeJSON(w, api.Person{ID: "person-" + ref, Handle: ref, Name: strings.ToUpper(ref[:1]) + ref[1:]})
	})
	mux.HandleFunc("POST /api/v1/tasks/{id}/people", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Person, Role string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.linked = append(f.linked, personLink{person: body.Person, role: body.Role})
		writeJSON(w, api.Task{ID: r.PathValue("id")})
	})
	mux.HandleFunc("POST /api/v1/series", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.series = append(f.series, body)
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, map[string]any{"series": body})
	})
	mux.HandleFunc("GET /api/v1/series/{id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"series": map[string]any{
			"id": r.PathValue("id"), "rrule": "FREQ=WEEKLY;BYDAY=MO",
			"mode": "fixed", "catchup": "skip",
		}})
	})
	mux.HandleFunc("PATCH /api/v1/series/{id}", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.series = append(f.series, body)
		writeJSON(w, map[string]any{"series": body})
	})
	mux.HandleFunc("POST /api/v1/undo", func(w http.ResponseWriter, _ *http.Request) {
		f.undos++
		writeJSON(w, api.UndoResult{Reversed: 1, Kind: api.KindTaskUpdated})
	})

	f.Server = httptest.NewServer(headers(mux, now))
	t.Cleanup(f.Close)
	return f
}

// headers stamps the version and clock headers the client reads, so the TUI
// renders relative dates against the fixture clock rather than the wall one.
func headers(next http.Handler, now time.Time) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Td-Server", api.Version)
		w.Header().Set("X-Td-Now", now.Format(time.RFC3339))
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// fixtureTasks builds the home view's tasks in the order sort_cases.json
// specifies, so the rendering tests read against a known list.
func fixtureTasks(d *seed.Data) []api.Task {
	byNum := map[int64]seed.Task{}
	for _, t := range d.Tasks {
		byNum[t.Num] = t
	}
	ids := map[int64]string{}
	for _, t := range d.Tasks {
		ids[t.Num] = "task-" + itoa(t.Num)
	}

	order := []int64{104, 102, 101, 114, 108, 106, 113, 103}
	out := make([]api.Task, 0, len(order))
	for _, num := range order {
		s := byNum[num]
		task := api.Task{
			ID: ids[num], Num: num, Title: s.Title, Status: s.Status,
			Priority: s.Priority, DueAt: s.DueAt, Tags: s.Tags,
			Source: s.Source, Notify: s.Notify, People: []api.TaskPerson{},
			Groups: []string{},
		}
		if task.Tags == nil {
			task.Tags = []string{}
		}
		if s.ParentNum != nil {
			pid := ids[*s.ParentNum]
			task.ParentID = &pid
		}
		for role, keys := range s.People {
			for _, key := range keys {
				task.People = append(task.People, api.TaskPerson{
					PersonID: key, Name: strings.ToUpper(key[:1]) + key[1:], Role: role,
				})
			}
		}
		out = append(out, task)
	}
	// 101 has one child, 113.
	for i := range out {
		if out[i].Num == 101 {
			out[i].ChildrenTotal, out[i].ChildrenDone = 1, 0
		}
	}
	return out
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// harness drives a model synchronously: every command it returns is run and
// its message fed back, until the model settles.
type harness struct {
	t     *testing.T
	model *tui.Model
}

func newHarness(t *testing.T, f *fakeServer, opts tui.Options) *harness {
	t.Helper()
	c := client.New(client.Config{Server: f.URL})
	m := tui.New(context.Background(), c, opts)

	h := &harness{t: t, model: m}
	h.run(m.Init())
	h.send(windowSize(80, 24))
	return h
}

// run executes a command tree and feeds every message back into the model.
func (h *harness) run(cmd tea.Cmd) {
	h.t.Helper()
	for range 20 {
		if cmd == nil {
			return
		}
		msg := cmd()
		if msg == nil {
			return
		}
		if batch, ok := msg.(tea.BatchMsg); ok {
			var next []tea.Cmd
			for _, c := range batch {
				if c == nil {
					continue
				}
				m := c()
				if m == nil {
					continue
				}
				_, follow := h.model.Update(m)
				if follow != nil {
					next = append(next, follow)
				}
			}
			cmd = tea.Batch(next...)
			continue
		}
		_, cmd = h.model.Update(msg)
	}
}

func (h *harness) send(msg tea.Msg) {
	h.t.Helper()
	_, cmd := h.model.Update(msg)
	h.run(cmd)
}

func (h *harness) key(k string) {
	h.t.Helper()
	h.send(keyPress(k))
}

// typeText sends a string one keystroke at a time, which is the only way a
// text input ever receives one.
func (h *harness) typeText(s string) {
	h.t.Helper()
	for _, r := range s {
		h.key(string(r))
	}
}

// keyPress builds the v2 key message. Keys are their own message type in
// Bubble Tea v2 rather than one message with a discriminator.
func keyPress(k string) tea.KeyPressMsg {
	switch k {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case " ":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	}
	r := []rune(k)[0]
	msg := tea.KeyPressMsg{Code: r, Text: k}
	if r >= 'A' && r <= 'Z' {
		msg.Code = r + 32
		msg.Mod = tea.ModShift
		msg.ShiftedCode = r
	}
	return msg
}

// windowSize is the resize message, named so the perf test reads clearly.
func windowSize(w, h int) tea.WindowSizeMsg {
	return tea.WindowSizeMsg{Width: w, Height: h}
}

func (h *harness) view() string {
	h.t.Helper()
	return h.model.View().Content
}

// The filter is a place you are. These cover the TUI half of that: it opens
// where you left it, and it tells the server when you move.

// TestTheTUIOpensOnTheRememberedFilter, so restarting td puts you back on the
// list you were reading rather than on Today.
func TestTheTUIOpensOnTheRememberedFilter(t *testing.T) {
	f := newFake(t)
	f.view = api.ViewFilter{Filter: "#certs", Chosen: true}

	h := newHarness(t, f, tui.Options{})
	_ = h.view()

	if f.lastQuery != "#certs" {
		t.Errorf("the TUI opened on %q, want the remembered %q", f.lastQuery, "#certs")
	}
}

// TestAFirstRunOpensOnSlotOne. Nothing remembered is not the same as an empty
// filter, and the difference is what the Chosen flag carries.
func TestAFirstRunOpensOnSlotOne(t *testing.T) {
	f := newFake(t)
	f.view = api.ViewFilter{Chosen: false}

	h := newHarness(t, f, tui.Options{})
	_ = h.view()

	if f.lastQuery == "" {
		t.Error("a first run sent an empty filter, want the slot 1 default")
	}
}

// TestAClearedFilterStaysCleared covers the other half of Chosen: an empty
// filter somebody chose is not a first run and must not become Today.
func TestAClearedFilterStaysCleared(t *testing.T) {
	f := newFake(t)
	f.view = api.ViewFilter{Filter: "", Chosen: true}

	h := newHarness(t, f, tui.Options{})
	_ = h.view()

	if f.lastQuery != "" {
		t.Errorf("a cleared filter opened on %q, want everything", f.lastQuery)
	}
}

// TestAFilterOnTheCommandLineWins, because ignoring your own argument would be
// absurd however recently something else was remembered.
func TestAFilterOnTheCommandLineWins(t *testing.T) {
	f := newFake(t)
	f.view = api.ViewFilter{Filter: "#certs", Chosen: true}

	h := newHarness(t, f, tui.Options{Filter: "is:inbox"})
	_ = h.view()

	if f.lastQuery != "is:inbox" {
		t.Errorf("the TUI opened on %q, want the -filter argument", f.lastQuery)
	}
}

// TestChangingTheFilterTellsTheServer, so the web UI and the next td agree.
func TestChangingTheFilterTellsTheServer(t *testing.T) {
	f := newFake(t)
	h := newHarness(t, f, tui.Options{})
	_ = h.view()

	// The bar opens prefilled with the current filter, so this clears it the
	// way a person would before typing a new one.
	h.key("/")
	for range 60 {
		h.send(tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	h.typeText("#certs")
	h.key("enter")
	_ = h.view()

	if len(f.viewWrites) == 0 || f.viewWrites[len(f.viewWrites)-1] != "#certs" {
		t.Errorf("filter writes = %v, want the last one to be %q", f.viewWrites, "#certs")
	}
}

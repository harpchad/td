package planner_test

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

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/plugins/planner"
	"github.com/harpchad/td/internal/sync"
)

// Everything here runs against testdata/plugins/planner, written by hand from
// Graph's published resource documentation. CLAUDE.md forbids pointing at a
// real tenant to capture a fixture, and nothing in this package can reach one:
// the Graph client is aimed at an httptest server.

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "plugins", "planner", name))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// graph serves the fixtures at the paths Graph serves them at, so the
// plugin's URL construction is exercised rather than assumed.
type graph struct {
	*httptest.Server
	tasksFile string
	calls     []string
	token     string
}

func newGraph(t *testing.T) *graph {
	t.Helper()
	g := &graph{tasksFile: "tasks.json"}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1.0/planner/plans/{plan}/tasks", func(w http.ResponseWriter, r *http.Request) {
		g.calls = append(g.calls, r.URL.Path)
		g.token = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture(t, g.tasksFile))
	})
	mux.HandleFunc("GET /v1.0/planner/plans/{plan}", func(w http.ResponseWriter, r *http.Request) {
		g.calls = append(g.calls, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture(t, "plan.json"))
	})
	mux.HandleFunc("GET /v1.0/users/{id}", func(w http.ResponseWriter, r *http.Request) {
		g.calls = append(g.calls, r.URL.Path)
		var list planner.GraphUserList
		if err := json.Unmarshal(fixture(t, "users.json"), &list); err != nil {
			t.Fatal(err)
		}
		for _, u := range list.Value {
			if u.ID == r.PathValue("id") {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(u)
				return
			}
		}
		// A user who has left the tenant. The plugin has to survive it.
		http.Error(w, `{"error":{"code":"Request_ResourceNotFound"}}`, http.StatusNotFound)
	})

	g.Server = httptest.NewServer(mux)
	t.Cleanup(g.Close)
	return g
}

func newClient(t *testing.T, g *graph) *planner.Client {
	t.Helper()
	return planner.New(planner.Config{
		PlanIDs:    []string{"xqQg5FS2LkCp935s-FIFm2QAFkHM"},
		GraphToken: "graph-token",
		Endpoint:   g.URL + "/v1.0",
	})
}

func chicago(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// TestTranslateReadsTheFixture covers the mapping from Graph onto the plugin
// contract.
func TestTranslateReadsTheFixture(t *testing.T) {
	g := newGraph(t)
	c := newClient(t, g)
	ctx := context.Background()

	tasks, err := c.Tasks(ctx, c.Config.PlanIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 3 {
		t.Fatalf("%d tasks, want the fixture's 3", len(tasks))
	}
	users, err := c.Users(ctx, planner.UserIDs(tasks))
	if err != nil {
		t.Fatal(err)
	}
	items := planner.Translate(tasks, users, planner.DefaultTaskURL, chicago(t), false)

	byID := map[string]sync.Item{}
	for _, item := range items {
		byID[item.ExternalID] = item
	}

	renew := byID["01_TASK_RENEW"]
	if renew.Title != "Renew the wildcard certificate" {
		t.Errorf("title = %q", renew.Title)
	}
	// percentComplete 0 is not started.
	if renew.Status != api.StatusTodo {
		t.Errorf("status = %q, want todo for percentComplete 0", renew.Status)
	}
	// Planner stores a due date as midnight UTC on the day the user picked.
	// Read as an instant in a zone west of UTC it becomes the previous
	// evening, so a task due the 4th would show as due the 3rd.
	if renew.DueAt == nil || *renew.DueAt != "2026-08-04" {
		t.Errorf("due = %v, want the calendar date Planner meant", renew.DueAt)
	}
	if renew.Rev == "" {
		t.Error("no etag, so nothing can tell a replay from a change")
	}
	if !strings.Contains(renew.URL, "01_TASK_RENEW") {
		t.Errorf("url = %q", renew.URL)
	}

	// The assignee and the person who put it on the board are both links, and
	// they carry the Graph object id rather than a name.
	roles := map[string]string{}
	for _, p := range renew.People {
		roles[p.Role] = p.SourceUser
	}
	if roles[api.RoleAssignee] != "8f3d2e11-0000-4a2b-9c3d-000000000001" {
		t.Errorf("assignee = %q", roles[api.RoleAssignee])
	}
	if roles[api.RoleAssigner] != "8f3d2e11-0000-4a2b-9c3d-000000000003" {
		t.Errorf("assigner = %q", roles[api.RoleAssigner])
	}
	for _, p := range renew.People {
		if p.Name == "" {
			t.Errorf("%s has no name, so the server cannot create the person", p.Role)
		}
	}

	// percentComplete between 1 and 99 is in progress.
	if byID["01_TASK_SOC2"].Status != api.StatusDoing {
		t.Errorf("status = %q for percentComplete 50", byID["01_TASK_SOC2"].Status)
	}
	// A task with no due date has none, rather than a zero one.
	if byID["01_TASK_TIRES"].DueAt != nil {
		t.Errorf("due = %v for a task with none", byID["01_TASK_TIRES"].DueAt)
	}
}

// TestPlannerPriorityIsNotTranslated is the field-ownership rule at its most
// important. Planner's priority is set by whoever made the card; td's is your
// answer to "what should I do next". Mapping one onto the other overwrites
// your answer every fifteen minutes.
func TestPlannerPriorityIsNotTranslated(t *testing.T) {
	g := newGraph(t)
	c := newClient(t, g)
	ctx := context.Background()

	tasks, err := c.Tasks(ctx, c.Config.PlanIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	// The fixture carries three distinct Planner priorities, so a mapping
	// would have something to show.
	seen := map[int]bool{}
	for _, task := range tasks {
		seen[task.Priority] = true
	}
	if len(seen) < 2 {
		t.Fatal("the fixture has one priority, so this proves nothing")
	}

	items := planner.Translate(tasks, nil, "", nil, false)
	body, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	// The contract's Item has no priority field at all, which is the
	// structural version of the rule.
	if strings.Contains(string(body), "priority") {
		t.Errorf("a translated item carries a priority: %s", body)
	}
}

// TestTranslationIsStable. Assignments is a map and Go randomises map
// iteration, so without an explicit sort two runs over an unchanged plan
// would produce different payloads and a replay would stop being provably a
// no-op.
func TestTranslationIsStable(t *testing.T) {
	g := newGraph(t)
	c := newClient(t, g)
	ctx := context.Background()

	tasks, err := c.Tasks(ctx, c.Config.PlanIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	users, err := c.Users(ctx, planner.UserIDs(tasks))
	if err != nil {
		t.Fatal(err)
	}

	first, err := json.Marshal(planner.Translate(tasks, users, planner.DefaultTaskURL, chicago(t), false))
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		again, err := json.Marshal(planner.Translate(tasks, users, planner.DefaultTaskURL, chicago(t), false))
		if err != nil {
			t.Fatal(err)
		}
		if string(again) != string(first) {
			t.Fatalf("two translations of the same input differ:\n%s\n%s", first, again)
		}
	}
}

// TestADepartedUserDoesNotStopTheSync. Losing a whole import over one
// colleague who left the tenant would be the wrong trade.
func TestADepartedUserDoesNotStopTheSync(t *testing.T) {
	g := newGraph(t)
	c := newClient(t, g)
	ctx := context.Background()

	users, err := c.Users(ctx, []string{
		"8f3d2e11-0000-4a2b-9c3d-000000000001",
		"00000000-dead-4000-8000-000000000000", // nobody
	})
	if err != nil {
		t.Fatalf("a 404 on one user failed the whole lookup: %v", err)
	}
	if len(users) != 1 {
		t.Errorf("%d users, want the one that exists", len(users))
	}
}

// api is a Poster that records what a run would send.
type recorder struct {
	batches  []sync.Request
	mirrored []string
	result   sync.Result
	err      error
}

func (r *recorder) Sync(_ context.Context, _ string, req sync.Request) (sync.Result, error) {
	if r.err != nil {
		return sync.Result{}, r.err
	}
	r.batches = append(r.batches, req)
	return r.result, nil
}

func (r *recorder) Mirrored(_ context.Context, _ string) ([]string, error) {
	return r.mirrored, nil
}

// TestGoneIsWhatDisappeared covers the subtraction. Planner has no tombstones
// and no delta query for tasks, so this is the only way to notice a deletion.
func TestGoneIsWhatDisappeared(t *testing.T) {
	g := newGraph(t)
	c := newClient(t, g)
	ctx := context.Background()

	rec := &recorder{mirrored: []string{"01_TASK_RENEW", "01_TASK_SOC2", "01_TASK_TIRES"}}

	// The second read of the plan: TIRES is gone, ONBOARD is new.
	g.tasksFile = "tasks_updated.json"
	if _, err := planner.Run(ctx, c, rec, time.Now(), chicago(t)); err != nil {
		t.Fatal(err)
	}

	var gone []string
	for _, batch := range rec.batches {
		gone = append(gone, batch.Gone...)
	}
	if len(gone) != 1 || gone[0] != "01_TASK_TIRES" {
		t.Errorf("gone = %v, want the one that disappeared", gone)
	}
}

// TestAnEmptyReadNeverMarksAnythingGone is the guard against the most
// destructive mistake this plugin can make. An expired token or a mistyped
// plan id produces zero tasks, and subtracting from nothing would mark every
// mirrored task upstream_gone in one pass.
func TestAnEmptyReadNeverMarksAnythingGone(t *testing.T) {
	g := newGraph(t)
	c := newClient(t, g)
	ctx := context.Background()

	// Serve an empty plan, which is what an expired token or a mistyped plan
	// id looks like from here.
	g.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"value":[]}`))
	})

	rec := &recorder{mirrored: []string{"01_TASK_RENEW", "01_TASK_SOC2", "01_TASK_TIRES"}}
	res, err := planner.Run(ctx, c, rec, time.Now(), chicago(t))
	if err != nil {
		t.Fatal(err)
	}
	if res.Gone != 0 {
		t.Errorf("an empty read reported %d gone", res.Gone)
	}
	for _, batch := range rec.batches {
		if len(batch.Gone) > 0 {
			t.Errorf("an empty read posted a gone list: %v", batch.Gone)
		}
	}
}

// TestTheGoneBatchGoesLast. Marking a task gone and then un-marking it in the
// same run would write two events for no change.
func TestTheGoneBatchGoesLast(t *testing.T) {
	g := newGraph(t)
	c := newClient(t, g)
	ctx := context.Background()

	g.tasksFile = "tasks_updated.json"
	rec := &recorder{mirrored: []string{"01_TASK_RENEW", "01_TASK_TIRES"}}
	if _, err := planner.Run(ctx, c, rec, time.Now(), chicago(t)); err != nil {
		t.Fatal(err)
	}
	if len(rec.batches) < 2 {
		t.Fatalf("%d batches, want the items then the gone list", len(rec.batches))
	}
	last := rec.batches[len(rec.batches)-1]
	if len(last.Items) != 0 || len(last.Gone) == 0 {
		t.Error("the last batch is not the gone list")
	}
	for _, batch := range rec.batches[:len(rec.batches)-1] {
		if len(batch.Gone) > 0 {
			t.Error("a gone list travelled with items")
		}
	}
}

// TestARunWithNoPlansIsAnError rather than a silent no-op that looks like it
// worked.
func TestARunWithNoPlansIsAnError(t *testing.T) {
	c := planner.New(planner.Config{})
	if _, err := planner.Run(context.Background(), c, &recorder{}, time.Now(), nil); err == nil {
		t.Error("a run with no plans configured succeeded")
	}
}

// TestTheGraphTokenIsSent, because a plugin that silently reads nothing looks
// exactly like a plan that is empty.
func TestTheGraphTokenIsSent(t *testing.T) {
	g := newGraph(t)
	c := newClient(t, g)

	if _, err := c.Tasks(context.Background(), c.Config.PlanIDs[0]); err != nil {
		t.Fatal(err)
	}
	if g.token != "graph-token" {
		t.Errorf("Authorization carried %q", g.token)
	}
}

// TestTheEmailTravels is what lets the server attach an identity to somebody
// td already knows without guessing on a name. Graph leaves displayName null
// on the identities inside a task, so both the name and the address come from
// the directory lookup.
func TestTheEmailTravels(t *testing.T) {
	g := newGraph(t)
	c := newClient(t, g)
	ctx := context.Background()

	tasks, err := c.Tasks(ctx, c.Config.PlanIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	users, err := c.Users(ctx, planner.UserIDs(tasks))
	if err != nil {
		t.Fatal(err)
	}
	items := planner.Translate(tasks, users, planner.DefaultTaskURL, chicago(t), false)

	var checked int
	for _, item := range items {
		for _, p := range item.People {
			if p.Email == "" {
				t.Errorf("%s on %s has no address, so the server can only guess",
					p.Role, item.ExternalID)
				continue
			}
			if !strings.Contains(p.Email, "@") {
				t.Errorf("email = %q", p.Email)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no person links at all, so this proves nothing")
	}
}

// TestUnresolvedIdentitiesAreCollectedAcrossBatches, so the report a person
// reads names each colleague once rather than once per batch.
func TestUnresolvedIdentitiesAreCollectedAcrossBatches(t *testing.T) {
	g := newGraph(t)
	c := newClient(t, g)
	ctx := context.Background()

	rec := &recorder{result: sync.Result{Unresolved: []sync.Unresolved{
		{
			Source: "planner", SourceUser: "8f3d2e11-0000-4a2b-9c3d-000000000001",
			Name: "Stacey Whitlock", Reason: "somebody already has that handle",
		},
	}}}
	res, err := planner.Run(ctx, c, rec, time.Now(), chicago(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Unresolved) != 1 {
		t.Errorf("unresolved = %+v, want one entry however many batches reported it", res.Unresolved)
	}
}

// TestRelinkDropsTheRevSoTheServerReappliesEverything. Idempotence has one
// cost: an item whose rev matches is not looked at, so a person link that
// could now be resolved is not resolved until something upstream happens to
// change. After mapping an identity you want the backfill now.
func TestRelinkDropsTheRevSoTheServerReappliesEverything(t *testing.T) {
	g := newGraph(t)
	c := newClient(t, g)
	ctx := context.Background()

	tasks, err := c.Tasks(ctx, c.Config.PlanIDs[0])
	if err != nil {
		t.Fatal(err)
	}

	ordinary := planner.Translate(tasks, nil, "", nil, false)
	for _, item := range ordinary {
		if item.Rev == "" {
			t.Fatalf("%s has no rev, so an ordinary run cannot skip it", item.ExternalID)
		}
	}

	relinked := planner.Translate(tasks, nil, "", nil, true)
	for _, item := range relinked {
		if item.Rev != "" {
			t.Errorf("%s kept its rev, so the server would skip it", item.ExternalID)
		}
	}
	// Everything else is untouched: relink changes what the server may skip,
	// not what it is told.
	if len(relinked) != len(ordinary) {
		t.Fatalf("%d items relinked, %d ordinary", len(relinked), len(ordinary))
	}
	for i := range ordinary {
		if relinked[i].ExternalID != ordinary[i].ExternalID ||
			relinked[i].Title != ordinary[i].Title ||
			relinked[i].Status != ordinary[i].Status {
			t.Errorf("relink changed item %d", i)
		}
	}
}

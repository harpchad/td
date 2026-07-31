package store_test

import (
	"context"
	"testing"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/sync"
)

// Field ownership is the core rule in section 8, and "my priority got wiped
// by a sync" kills trust in one incident. These tests exist so that cannot
// happen quietly.

func item(external, title, status string) sync.Item {
	return sync.Item{
		ExternalID: external, Title: title, Status: status,
		URL: "https://tasks.example.invalid/" + external, Rev: "1",
	}
}

// TestASyncNeverTouchesALocallyOwnedField is the one that matters. Everything
// local is set, a sync arrives claiming otherwise, and every one of them
// survives.
func TestASyncNeverTouchesALocallyOwnedField(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	if _, err := s.Sync(ctx, "plugin:planner", "planner", sync.Request{
		Items: []sync.Item{item("PLAN-1", "Renew the certificate", api.StatusTodo)},
	}, now); err != nil {
		t.Fatal(err)
	}

	mirrored, err := s.List(ctx, "src:planner", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(mirrored) != 1 {
		t.Fatalf("%d mirrored tasks", len(mirrored))
	}
	task := mirrored[0]

	// Everything a person owns, set by a person.
	p1, effort := 1, 3
	due := "2026-08-20"
	notes := "Vendor quoted three days. Ask Brandiss about the DNS record."
	snooze := "2026-08-05T09:00:00-05:00"
	if _, err := s.Patch(ctx, actor, task.ID, api.TaskPatch{
		Priority: &p1, Effort: &effort, Notes: &notes,
		SnoozeUntil: &snooze,
		Tags:        &[]string{"certs", "ops"},
		Presence: map[string]bool{
			"priority": true, "effort": true, "snooze_until": true,
		},
	}, "", now); err != nil {
		t.Fatal(err)
	}
	stacey, err := s.PersonByHandle(ctx, "stacey")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LinkPerson(ctx, actor, task.ID, stacey.ID, api.RoleInvolved, now); err != nil {
		t.Fatal(err)
	}

	// The upstream item now disagrees about all of it, and moves.
	if _, err := s.Sync(ctx, "plugin:planner", "planner", sync.Request{
		Items: []sync.Item{{
			ExternalID: "PLAN-1", Title: "Renew the certificate (staging first)",
			Status: api.StatusDoing, Rev: "2", DueAt: &due,
			URL: "https://tasks.example.invalid/PLAN-1",
		}},
	}, now); err != nil {
		t.Fatal(err)
	}

	after, err := s.Get(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Upstream fields moved.
	if after.Title != "Renew the certificate (staging first)" {
		t.Errorf("title = %q, want the upstream one", after.Title)
	}
	if after.Status != api.StatusDoing {
		t.Errorf("status = %q, want the upstream one", after.Status)
	}

	// Local fields did not.
	if after.Priority == nil || *after.Priority != 1 {
		t.Errorf("priority = %v, want 1: this is the field that kills trust", after.Priority)
	}
	if after.Notes != notes {
		t.Errorf("notes = %q", after.Notes)
	}
	if after.Effort == nil || *after.Effort != 3 {
		t.Errorf("effort = %v", after.Effort)
	}
	if after.SnoozeUntil == nil {
		t.Error("the snooze was cleared")
	}
	if len(after.Tags) != 2 {
		t.Errorf("tags = %v, want the two that were set locally", after.Tags)
	}
	var linked bool
	for _, p := range after.People {
		if p.PersonID == stacey.ID && p.Role == api.RoleInvolved {
			linked = true
		}
	}
	if !linked {
		t.Error("the person link was removed")
	}
}

// TestReplayingABatchIsANoOp covers idempotence, which is what lets a plugin
// always replay rather than having to track exactly what it sent.
func TestReplayingABatchIsANoOp(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	batch := sync.Request{Items: []sync.Item{
		item("PLAN-1", "Renew the certificate", api.StatusTodo),
		item("PLAN-2", "Upload SOC2 evidence", api.StatusDoing),
	}}

	first, err := s.Sync(ctx, "plugin:planner", "planner", batch, now)
	if err != nil {
		t.Fatal(err)
	}
	if first.Created != 2 {
		t.Fatalf("created %d, want 2", first.Created)
	}

	before, err := s.Events(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}

	// The same batch again, three times.
	for range 3 {
		again, err := s.Sync(ctx, "plugin:planner", "planner", batch, now)
		if err != nil {
			t.Fatal(err)
		}
		if again.Created != 0 || again.Updated != 0 {
			t.Errorf("a replay created %d and updated %d", again.Created, again.Updated)
		}
		if again.Unchanged != 2 {
			t.Errorf("unchanged = %d, want 2", again.Unchanged)
		}
	}

	// And it wrote nothing. An idempotent sync that still churns updated_at
	// and the event log is not idempotent in any way that matters.
	after, err := s.Events(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("a replay wrote %d events", len(after)-len(before))
	}
}

// TestARevThatMovesIsAnUpdate, so idempotence does not become inertia.
func TestARevThatMovesIsAnUpdate(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	if _, err := s.Sync(ctx, "plugin:planner", "planner", sync.Request{
		Items: []sync.Item{item("PLAN-1", "Renew the certificate", api.StatusTodo)},
	}, now); err != nil {
		t.Fatal(err)
	}

	moved := item("PLAN-1", "Renew the certificate (staging first)", api.StatusDoing)
	moved.Rev = "2"
	res, err := s.Sync(ctx, "plugin:planner", "planner", sync.Request{Items: []sync.Item{moved}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Updated != 1 {
		t.Errorf("updated = %d, want 1", res.Updated)
	}
}

// TestCompletingLocallySurvivesTheNextSync. Completing a mirrored task in td
// is a statement about your own work: you have done your part, whatever the
// ticket still says. A sync that reopened it every fifteen minutes would make
// the mirror an argument rather than a list.
func TestCompletingLocallySurvivesTheNextSync(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	if _, err := s.Sync(ctx, "plugin:planner", "planner", sync.Request{
		Items: []sync.Item{item("PLAN-1", "Renew the certificate", api.StatusTodo)},
	}, now); err != nil {
		t.Fatal(err)
	}
	mirrored, err := s.List(ctx, "src:planner", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Complete(ctx, actor, mirrored[0].ID, now); err != nil {
		t.Fatal(err)
	}

	// Upstream still thinks it is open, and says so with a new rev.
	still := item("PLAN-1", "Renew the certificate", api.StatusTodo)
	still.Rev = "2"
	if _, err := s.Sync(ctx, "plugin:planner", "planner",
		sync.Request{Items: []sync.Item{still}}, now); err != nil {
		t.Fatal(err)
	}

	after, err := s.Get(ctx, mirrored[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != api.StatusDone {
		t.Errorf("status = %q; a sync reopened something you finished", after.Status)
	}
}

// TestGoneIsMarkedNotDeleted. A ticket you can no longer see is not a ticket
// that never existed, and something in your notes probably refers to it.
func TestGoneIsMarkedNotDeleted(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	if _, err := s.Sync(ctx, "plugin:planner", "planner", sync.Request{
		Items: []sync.Item{item("PLAN-1", "Order tires", api.StatusTodo)},
	}, now); err != nil {
		t.Fatal(err)
	}
	mirrored, err := s.List(ctx, "src:planner", now)
	if err != nil {
		t.Fatal(err)
	}
	task := mirrored[0]

	notes := "Discount tire quoted 780 for the set."
	if _, err := s.Patch(ctx, actor, task.ID, api.TaskPatch{
		Notes: &notes, Presence: map[string]bool{},
	}, "", now); err != nil {
		t.Fatal(err)
	}

	res, err := s.Sync(ctx, "plugin:planner", "planner",
		sync.Request{Gone: []string{"PLAN-1"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Gone != 1 {
		t.Fatalf("gone = %d", res.Gone)
	}

	after, err := s.Get(ctx, task.ID)
	if err != nil {
		t.Fatalf("the task was deleted: %v", err)
	}
	if !after.UpstreamGone {
		t.Error("upstream_gone was not set")
	}
	if after.Num != task.Num {
		t.Error("the task lost its number")
	}
	if after.Notes != notes {
		t.Error("the local notes went with the upstream item")
	}

	// Marking it twice is not two events.
	again, err := s.Sync(ctx, "plugin:planner", "planner",
		sync.Request{Gone: []string{"PLAN-1"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if again.Gone != 0 {
		t.Errorf("marking an already-gone item counted %d", again.Gone)
	}
}

// TestAnItemThatComesBackIsBack. An upstream item can reappear, and the
// mirror should stop shouting about it.
func TestAnItemThatComesBackIsBack(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	if _, err := s.Sync(ctx, "plugin:planner", "planner", sync.Request{
		Items: []sync.Item{item("PLAN-1", "Order tires", api.StatusTodo)},
	}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Sync(ctx, "plugin:planner", "planner",
		sync.Request{Gone: []string{"PLAN-1"}}, now); err != nil {
		t.Fatal(err)
	}

	back := item("PLAN-1", "Order tires", api.StatusTodo)
	back.Rev = "2"
	if _, err := s.Sync(ctx, "plugin:planner", "planner",
		sync.Request{Items: []sync.Item{back}}, now); err != nil {
		t.Fatal(err)
	}

	mirrored, err := s.List(ctx, "src:planner", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(mirrored) != 1 {
		t.Fatalf("%d mirrored tasks", len(mirrored))
	}
	if mirrored[0].UpstreamGone {
		t.Error("an item that came back is still marked gone")
	}
}

// TestAMirrorNeverLandsInTheInbox. The inbox is for things you captured and
// have not sorted; a backlog arriving there would bury it.
func TestAMirrorNeverLandsInTheInbox(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	if _, err := s.Sync(ctx, "plugin:planner", "planner", sync.Request{
		Items: []sync.Item{
			item("PLAN-1", "Something", api.StatusInbox),
			item("PLAN-2", "Something else", ""),
		},
	}, now); err != nil {
		t.Fatal(err)
	}

	mirrored, err := s.List(ctx, "src:planner", now)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range mirrored {
		if task.Status == api.StatusInbox {
			t.Errorf("task %d landed in the inbox", task.Num)
		}
	}

	// And the home filter still excludes them, so your own list is not
	// buried on day one.
	home, err := s.List(ctx, "is:open src:local -is:inbox -is:snoozed -is:deferred", now)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range home {
		if task.Source == "planner" {
			t.Errorf("a mirrored task is in the home view: %d", task.Num)
		}
	}
}

// TestAnIdentityResolvesOntoOnePerson is what person_identity exists for:
// without it you get three Brandisses and the feature is worthless.
func TestAnIdentityResolvesOntoOnePerson(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	brandiss, err := s.PersonByHandle(ctx, "brandiss")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LinkIdentity(ctx, brandiss.ID, "planner", "graph-object-id-1"); err != nil {
		t.Fatal(err)
	}

	mapped := item("PLAN-1", "Renew the certificate", api.StatusTodo)
	mapped.People = []sync.ItemPerson{
		{Role: api.RoleAssignee, SourceUser: "graph-object-id-1", Name: "Brandiss Okafor"},
		// Never seen before, but named, so a person is created.
		{Role: api.RoleAssigner, SourceUser: "graph-object-id-9", Name: "Jordan Reyes"},
		// Never seen and unnamed: the link is skipped, and the task still
		// arrives.
		{Role: api.RoleInvolved, SourceUser: "graph-object-id-8"},
	}
	if _, err := s.Sync(ctx, "plugin:planner", "planner",
		sync.Request{Items: []sync.Item{mapped}}, now); err != nil {
		t.Fatal(err)
	}

	mirrored, err := s.List(ctx, "src:planner", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(mirrored) != 1 {
		t.Fatalf("%d mirrored tasks", len(mirrored))
	}

	roles := map[string]string{}
	for _, p := range mirrored[0].People {
		roles[p.Role] = p.PersonID
	}
	if roles[api.RoleAssignee] != brandiss.ID {
		t.Error("the mapped identity did not resolve onto the existing person")
	}
	if roles[api.RoleAssigner] == "" {
		t.Error("a named new identity did not create a person")
	}
	if roles[api.RoleInvolved] != "" {
		t.Error("an unnamed unknown identity created something")
	}

	// One Brandiss, not two.
	people, err := s.People(ctx)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, p := range people {
		if p.Name == "Brandiss" || p.Name == "Brandiss Okafor" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("%d Brandisses", count)
	}
}

// TestAPluginCannotInventAStatus. The state machine is closed, and a plugin
// bug should not become a database full of unknown statuses.
func TestAPluginCannotInventAStatus(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	if _, err := s.Sync(ctx, "plugin:planner", "planner", sync.Request{
		Items: []sync.Item{item("PLAN-1", "Something", "in-review")},
	}, now); err != nil {
		t.Fatal(err)
	}

	mirrored, err := s.List(ctx, "src:planner", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(mirrored) != 1 {
		t.Fatalf("%d mirrored tasks", len(mirrored))
	}
	if mirrored[0].Status != api.StatusTodo {
		t.Errorf("status = %q, want the fallback rather than an invented state", mirrored[0].Status)
	}
}

// TestSyncRefusesToWriteLocalTasks. A plugin with a sync scope must not be
// able to reach into the tasks you typed yourself.
func TestSyncRefusesToWriteLocalTasks(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	if _, err := s.Sync(ctx, "plugin:planner", "local", sync.Request{
		Items: []sync.Item{item("PLAN-1", "Something", api.StatusTodo)},
	}, now); err == nil {
		t.Error("a plugin wrote to the local source")
	}
	if _, err := s.Sync(ctx, "plugin:planner", "", sync.Request{}, now); err == nil {
		t.Error("a sync with no source was accepted")
	}
	if _, err := s.Sync(ctx, "plugin:planner", "planner", sync.Request{
		Items: []sync.Item{{Title: "no external id"}},
	}, now); err == nil {
		t.Error("an item with no external_id was accepted")
	}
}

// TestSyncIsAttributedToThePlugin, so a bad import is separable from your own
// work in the activity feed.
func TestSyncIsAttributedToThePlugin(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	before, err := s.Events(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Sync(ctx, "plugin:planner", "planner", sync.Request{
		Items: []sync.Item{item("PLAN-1", "Renew the certificate", api.StatusTodo)},
	}, now); err != nil {
		t.Fatal(err)
	}
	after, err := s.Events(ctx, int64(len(before)), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) == 0 {
		t.Fatal("the sync wrote no events")
	}
	for _, e := range after {
		if e.Actor != "plugin:planner" {
			t.Errorf("actor = %q, want plugin:planner", e.Actor)
		}
	}
}

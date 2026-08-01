package store_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/recur"
	"github.com/harpchad/td/internal/store"
	"github.com/harpchad/td/internal/sync"
)

// The one hard delete in td. What it keeps matters at least as much as what it
// removes: the whole reason it exists is that deleting the database file was
// the alternative, and that destroys the account, the tokens, and a Microsoft
// connection somebody just signed in for.

// TestResetKeepsEverythingThatIsNotATask.
func TestResetKeepsEverythingThatIsNotATask(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	// The things that are slow or annoying to rebuild.
	if _, err := s.CreateAccount(ctx, "chad", "hash", "SECRET", nil, now); err != nil {
		t.Fatal(err)
	}
	tok, err := s.CreateToken(ctx, "cli", "me", []string{api.ScopeRead}, now)
	if err != nil {
		t.Fatal(err)
	}
	brandiss, err := s.PersonByHandle(ctx, "brandiss")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LinkIdentity(ctx, brandiss.ID, "planner", "graph-object-id-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutSavedFilter(ctx, api.SavedFilter{Slot: 4, Name: "Truck", Query: "#truck"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePluginSettings(ctx, "planner", true,
		json.RawMessage(`{"plans":["PLAN-1"]}`), 15, now); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePluginCredential(ctx, "planner",
		json.RawMessage(`{"refresh_token":"keep-me"}`), now); err != nil {
		t.Fatal(err)
	}

	if _, err := s.ResetTasks(ctx, "", now); err != nil {
		t.Fatal(err)
	}

	if _, err := s.LookupToken(ctx, tok.Secret, now); err != nil {
		t.Errorf("the token went: %v", err)
	}
	if _, err := s.TheAccount(ctx); err != nil {
		t.Errorf("the account went: %v", err)
	}
	// Identity mappings especially. They are exactly what you do not want to
	// redo between two runs of the thing you are testing.
	if _, err := s.PersonByIdentity(ctx, "planner", "graph-object-id-1"); err != nil {
		t.Errorf("an identity mapping went: %v", err)
	}
	if people, err := s.People(ctx); err != nil || len(people) != 3 {
		t.Errorf("%d people left, want the fixture's 3", len(people))
	}
	if filters, err := s.SavedFilters(ctx); err != nil || len(filters) == 0 {
		t.Error("the saved filters went")
	}
	cfg, err := s.PluginConfigByName(ctx, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Connected() {
		t.Error("the Microsoft connection went, which is the thing this exists to avoid")
	}
	if !cfg.Enabled {
		t.Error("the plugin settings went")
	}
}

// TestResetRemovesTheTasksAndWhatHangsOffThem.
func TestResetRemovesTheTasksAndWhatHangsOffThem(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	task, err := s.GetByNum(ctx, 103)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddAttachment(ctx, actor, api.Attachment{
		TaskID: task.ID, SHA256: strings.Repeat("a", 64), Filename: "q.pdf", Bytes: 1,
	}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCollapsed(ctx, task.ID, true); err != nil {
		t.Fatal(err)
	}

	counts, err := s.ResetTasks(ctx, "", now)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Tasks == 0 {
		t.Fatal("nothing was removed")
	}
	// The fixture ships one of its own, so this is that plus the one added.
	if counts.Attachments != 2 {
		t.Errorf("attachments = %d, want the fixture's plus the one added", counts.Attachments)
	}

	left, err := s.List(ctx, "", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("%d tasks left", len(left))
	}
	// Search comes back empty too, which means the FTS index followed the
	// rows out rather than keeping ghosts.
	if found, err := s.List(ctx, "tires", now); err != nil || len(found) != 0 {
		t.Errorf("search still finds %d tasks", len(found))
	}
	// The attachment rows went with them. The files on disk wait for the
	// weekly sweep, which is what the sweep is for.
	if refs, err := s.ReferencedBlobs(ctx); err != nil || len(refs) != 0 {
		t.Errorf("%d blobs still referenced", len(refs))
	}
	// And a fresh task starts from one, since the numbers went with the rows.
	created, err := s.Create(ctx, actor, api.TaskCreate{Title: "After the reset"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if created.Num != 1 {
		t.Errorf("the next task is %d, want 1", created.Num)
	}
}

// TestResetBySourceLeavesYourOwnTasksAlone. This is the one that matters for
// testing a mirror: wipe what the plugin made, run it again, keep everything
// you typed yourself.
func TestResetBySourceLeavesYourOwnTasksAlone(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	if _, err := s.Sync(ctx, "plugin:planner", "planner", sync.Request{
		Items: []sync.Item{
			item("PLAN-1", "Renew the certificate", api.StatusTodo),
			item("PLAN-2", "Upload SOC2 evidence", api.StatusDoing),
		},
	}, now); err != nil {
		t.Fatal(err)
	}
	before, err := s.List(ctx, "src:local", now)
	if err != nil {
		t.Fatal(err)
	}

	counts, err := s.ResetTasks(ctx, "planner", now)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Tasks != 2 {
		t.Fatalf("removed %d, want the two mirrored ones", counts.Tasks)
	}

	if mirrored, err := s.List(ctx, "src:planner", now); err != nil || len(mirrored) != 0 {
		t.Errorf("%d mirrored tasks left", len(mirrored))
	}
	after, err := s.List(ctx, "src:local", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("%d local tasks before, %d after: a source reset took your own work", len(before), len(after))
	}

	// The recorded run describes a mirror that no longer exists, so it goes
	// too, whether or not there was anything left to delete.
	if err := s.RecordPluginRun(ctx, "planner", "2 created", nil, nil, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResetTasks(ctx, "planner", now); err != nil {
		t.Fatal(err)
	}
	cfg, err := s.PluginConfigByName(ctx, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LastResult != nil {
		t.Errorf("last_result = %q, describing a mirror that is gone", *cfg.LastResult)
	}
}

// TestResetLetsASeriesStartAgain rather than leaving it pointing at a task
// that no longer exists.
func TestResetLetsASeriesStartAgain(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	due := now.Format(recur.DateLayout)
	series, _, err := s.CreateSeries(ctx, actor, store.Series{
		RRule: "FREQ=DAILY", Mode: recur.ModeFixed, TZ: now.Location().String(),
		Template: api.TaskCreate{Title: "Feed the cat", DueAt: &due},
	}, now)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.ResetTasks(ctx, "", now); err != nil {
		t.Fatal(err)
	}

	after, err := s.Series(ctx, series.ID)
	if err != nil {
		t.Fatalf("the series went with the tasks: %v", err)
	}
	if after.CurrentTaskID != nil {
		t.Error("the series still points at a task that no longer exists")
	}
	// And it materializes a fresh instance rather than believing it already
	// fired for today.
	made, err := s.AdvanceSeries(ctx, actor, after, now.AddDate(0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	if len(made) == 0 {
		t.Error("the series never fired again after a reset")
	}
}

// TestResettingNothingIsNotAnError, so a script can run it unconditionally.
func TestResettingNothingIsNotAnError(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	counts, err := s.ResetTasks(ctx, "nosuchsource", now)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Tasks != 0 {
		t.Errorf("removed %d from a source with nothing in it", counts.Tasks)
	}
	if left, err := s.List(ctx, "", now); err != nil || len(left) == 0 {
		t.Error("an unknown source removed real tasks")
	}
}

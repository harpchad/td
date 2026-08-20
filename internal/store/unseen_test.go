package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/store"
)

// The new mark answers one question: what showed up while I was not looking.
// So the thing it must never do is mark what the owner typed themselves.
func TestWhatYouTypeYourselfIsNotNew(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	mine, err := s.Create(ctx, actor, api.TaskCreate{Title: "typed at a keyboard"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if mine.New {
		t.Error("a task the owner typed came back marked new")
	}

	// And no seeded task is marked either: a database that predates the mark
	// starts quiet rather than lighting up every row at once.
	got, err := s.List(ctx, "is:new", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("is:new returned %d tasks in a database nothing has arrived in", len(got))
	}
}

// TestWhatArrivesOnItsOwnIsNew covers the actors that are not the owner: an
// agent over MCP, a sync plugin. Anything that is not "me" is something
// filing a task while the owner is elsewhere.
func TestWhatArrivesOnItsOwnIsNew(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	for _, arrival := range []string{"mcp:claude", "plugin:jira", "plugin:mail"} {
		task, err := s.Create(ctx, arrival, api.TaskCreate{Title: "from " + arrival}, now)
		if err != nil {
			t.Fatal(err)
		}
		if !task.New {
			t.Errorf("a task created by %s came back unmarked", arrival)
		}

		// Get reads the mark too, not only List: the detail view is where the
		// mark gets cleared and it has to be able to see one first.
		again, err := s.Get(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !again.New {
			t.Errorf("Get lost the mark on the task from %s", arrival)
		}
	}

	got, err := s.List(ctx, "is:new", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("is:new returned %d tasks, want the 3 that arrived", len(got))
	}
}

// TestOpeningATaskClearsTheMark. Clearing one that carries no mark is not an
// error: the state asked for is the state that results either way.
func TestOpeningATaskClearsTheMark(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	task, err := s.Create(ctx, "mcp:claude", api.TaskCreate{Title: "filed by an agent"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSeen(ctx, task.ID); err != nil {
		t.Fatal(err)
	}

	after, err := s.Get(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.New {
		t.Error("the mark survived being seen")
	}
	if err := s.MarkSeen(ctx, task.ID); err != nil {
		t.Errorf("clearing an unmarked task: %v", err)
	}
}

// TestYourOwnEditClearsTheMarkAndASyncDoesNot is the distinction the whole
// feature rests on. Acting on a task is having seen it. Upstream rewriting a
// mirrored title is not the owner reading anything, and clearing the mark
// there would hide the arrival it was raised for.
func TestYourOwnEditClearsTheMarkAndASyncDoesNot(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	mine, err := s.Create(ctx, "mcp:claude", api.TaskCreate{Title: "one"}, now)
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := s.Create(ctx, "plugin:jira", api.TaskCreate{Title: "two"}, now)
	if err != nil {
		t.Fatal(err)
	}

	renamed := "one, edited by the owner"
	if _, err := s.Patch(ctx, actor, mine.ID, api.TaskPatch{Title: &renamed}, "", now); err != nil {
		t.Fatal(err)
	}
	upstream := "two, rewritten upstream"
	if _, err := s.Patch(ctx, "plugin:jira", theirs.ID, api.TaskPatch{Title: &upstream}, "", now); err != nil {
		t.Fatal(err)
	}

	edited, err := s.Get(ctx, mine.ID)
	if err != nil {
		t.Fatal(err)
	}
	if edited.New {
		t.Error("the owner edited a task and it stayed marked new")
	}
	synced, err := s.Get(ctx, theirs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !synced.New {
		t.Error("a sync rewriting a mirror cleared the mark the owner never saw")
	}
}

// TestTheNewMarkSurvivesARestore. Fold state is not exported and this could
// have gone the same way, but a restore is the moment you are least able to
// reconstruct what arrived overnight.
func TestTheNewMarkSurvivesARestore(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	arrived, err := s.Create(ctx, "plugin:jira", api.TaskCreate{Title: "filed overnight"}, now)
	if err != nil {
		t.Fatal(err)
	}

	out, err := s.Export(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var decoded store.Export
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}

	restored := fresh(t)
	if err := restored.Import(ctx, decoded); err != nil {
		t.Fatal(err)
	}
	back, err := restored.Get(ctx, arrived.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !back.New {
		t.Error("the restore lost the mark on a task that arrived before the backup")
	}
}

package store_test

import (
	"context"
	"strings"
	"testing"

	"github.com/harpchad/td/internal/api"
)

// TestUndoRestoresAFieldChange covers the base case and the requirement that
// undo writes its own event naming the seq it reversed.
func TestUndoRestoresAFieldChange(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	task, err := s.GetByNum(ctx, 103)
	if err != nil {
		t.Fatal(err)
	}
	original := task.Title

	renamed := "Order tires and an alignment"
	if _, err := s.Patch(ctx, actor, task.ID, api.TaskPatch{Title: &renamed}, "", now); err != nil {
		t.Fatal(err)
	}

	res, err := s.Undo(ctx, actor, now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Task == nil || res.Task.Title != original {
		t.Errorf("title = %v, want %q restored", res.Task, original)
	}

	events, err := s.Events(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	if last.Kind != api.KindUndo {
		t.Fatalf("last event kind = %s, want %s", last.Kind, api.KindUndo)
	}
	if last.Patch.UndoOf != res.Reversed {
		t.Errorf("undo_of = %d, want %d", last.Patch.UndoOf, res.Reversed)
	}
}

// TestUndoWalksBackwards covers the unbounded depth requirement and the rule
// that a second undo reverses the next-oldest eligible event rather than the
// undo itself.
func TestUndoWalksBackwards(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	task, err := s.GetByNum(ctx, 103)
	if err != nil {
		t.Fatal(err)
	}
	original := task.Title

	for _, title := range []string{"first rename", "second rename", "third rename"} {
		name := title
		if _, err := s.Patch(ctx, actor, task.ID, api.TaskPatch{Title: &name}, "", now); err != nil {
			t.Fatal(err)
		}
	}

	want := []string{"second rename", "first rename", original}
	for i, expected := range want {
		res, err := s.Undo(ctx, actor, now)
		if err != nil {
			t.Fatalf("undo %d: %v", i+1, err)
		}
		if res.Task == nil || res.Task.Title != expected {
			t.Fatalf("after undo %d title = %v, want %q", i+1, res.Task, expected)
		}
	}

	// Nothing eligible is left on this task, and the seeded database carries
	// no events of its own, so the next call has nothing to reverse.
	if _, err := s.Undo(ctx, actor, now); err == nil {
		t.Error("expected nothing_to_undo once the log is exhausted")
	} else {
		assertAPIError(t, err, api.ErrNothingToUndo)
	}
}

// TestUndoIsScopedToTheActor covers the scope rule: an actor may only reverse
// its own events, which is what keeps a bad MCP batch from undoing your work.
func TestUndoIsScopedToTheActor(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	task, err := s.GetByNum(ctx, 103)
	if err != nil {
		t.Fatal(err)
	}
	mine := "renamed by me"
	if _, err := s.Patch(ctx, actor, task.ID, api.TaskPatch{Title: &mine}, "", now); err != nil {
		t.Fatal(err)
	}
	theirs := "renamed by the agent"
	if _, err := s.Patch(ctx, "mcp:claude", task.ID, api.TaskPatch{Title: &theirs}, "", now); err != nil {
		t.Fatal(err)
	}

	// My undo reaches past the agent's event to my own.
	res, err := s.Undo(ctx, actor, now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Task == nil || res.Task.Title != "Order tires" {
		t.Errorf("title = %v, want my own change reversed", res.Task)
	}

	// The agent still has its own event to reverse.
	if _, err := s.Undo(ctx, "mcp:claude", now); err != nil {
		t.Fatalf("agent undo: %v", err)
	}
	// And a third actor has nothing.
	_, err = s.Undo(ctx, "plugin:jira", now)
	assertAPIError(t, err, api.ErrNothingToUndo)
}

// TestUndoOfACreateDropsTheTask covers the reversal of a create. Nothing in
// td hard-deletes, and the event log still refers to the row.
func TestUndoOfACreateDropsTheTask(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	created, err := s.Create(ctx, actor, api.TaskCreate{Title: "typed by mistake"}, now)
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.Undo(ctx, actor, now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != api.KindTaskCreated {
		t.Fatalf("reversed kind = %s, want %s", res.Kind, api.KindTaskCreated)
	}
	got, err := s.Get(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != api.StatusDropped {
		t.Errorf("status = %s, want dropped", got.Status)
	}
}

// TestUndoBypassesTheStateMachine covers the case that makes undo different
// from a forward edit: reversing a quick-complete out of the inbox means
// moving a done task back to inbox, which the machine forbids as a forward
// move and which is nonetheless the correct reversal.
func TestUndoBypassesTheStateMachine(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	task, err := s.GetByNum(ctx, 105)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != api.StatusInbox {
		t.Fatalf("fixture task 105 is %s, expected inbox", task.Status)
	}
	if _, err := s.Complete(ctx, actor, task.ID, now); err != nil {
		t.Fatal(err)
	}

	res, err := s.Undo(ctx, actor, now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Task == nil || res.Task.Status != api.StatusInbox {
		t.Fatalf("status = %v, want inbox restored", res.Task)
	}
	if res.Task.CompletedAt != nil {
		t.Errorf("completed_at = %v, want cleared by the reversal", *res.Task.CompletedAt)
	}
}

// TestUndoRestoresTags covers tag links, which live in another table and so
// have to be carried in the patch explicitly.
func TestUndoRestoresTags(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	task, err := s.GetByNum(ctx, 101)
	if err != nil {
		t.Fatal(err)
	}
	replaced := []string{"vpn"}
	got, err := s.Patch(ctx, actor, task.ID, api.TaskPatch{Tags: &replaced}, "", now)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.Tags, ",") != "vpn" {
		t.Fatalf("tags = %v, want [vpn]", got.Tags)
	}

	res, err := s.Undo(ctx, actor, now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Task == nil || strings.Join(res.Task.Tags, ",") != "certs,ops" {
		t.Errorf("tags = %v, want [certs ops] restored", res.Task)
	}
}

// TestUndoNeverReversesAnUndo covers the not_undoable list.
func TestUndoNeverReversesAnUndo(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	task, err := s.GetByNum(ctx, 103)
	if err != nil {
		t.Fatal(err)
	}
	renamed := "renamed once"
	if _, err := s.Patch(ctx, actor, task.ID, api.TaskPatch{Title: &renamed}, "", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Undo(ctx, actor, now); err != nil {
		t.Fatal(err)
	}
	_, err = s.Undo(ctx, actor, now)
	assertAPIError(t, err, api.ErrNothingToUndo)
}

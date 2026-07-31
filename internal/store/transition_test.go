package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/store"
)

const actor = "me"

type transitionFile struct {
	States      []string `json:"states"`
	Transitions []struct {
		From        string   `json:"from"`
		To          string   `json:"to"`
		Allowed     bool     `json:"allowed"`
		Requires    string   `json:"requires"`
		Clears      []string `json:"clears"`
		OnViolation struct {
			Status  int    `json:"status"`
			Error   string `json:"error"`
			Message string `json:"message"`
		} `json:"on_violation"`
	} `json:"transitions"`
}

func loadTransitions(t *testing.T) transitionFile {
	t.Helper()
	body, err := os.ReadFile(testdataPath("transition_cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f transitionFile
	if err := json.Unmarshal(body, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Transitions) == 0 {
		t.Fatal("no transitions loaded")
	}
	return f
}

func mikah(t *testing.T, s *store.Store) string {
	t.Helper()
	p, err := s.PersonByHandle(context.Background(), "mikah")
	if err != nil {
		t.Fatalf("person mikah: %v", err)
	}
	return p.ID
}

// inState builds a fresh task sitting in the requested status, walking there
// through legal moves so the row carries the side effects the state implies.
func inState(t *testing.T, s *store.Store, now time.Time, status string) api.Task {
	t.Helper()
	ctx := context.Background()
	p2 := 2

	create := api.TaskCreate{Title: "fixture " + status}
	if status != api.StatusInbox {
		create.Priority = &p2
	}
	task, err := s.Create(ctx, actor, create, now)
	if err != nil {
		t.Fatalf("create in %s: %v", status, err)
	}
	if task.Status == status {
		return task
	}

	move := func(to string, waitingOn *string) {
		t.Helper()
		patch := api.TaskPatch{Status: &to}
		if waitingOn != nil {
			patch.WaitingOn = waitingOn
			patch.Presence = map[string]bool{"waiting_on": true}
		}
		task, err = s.Patch(ctx, actor, task.ID, patch, "", now)
		if err != nil {
			t.Fatalf("setup move to %s: %v", to, err)
		}
	}

	switch status {
	case api.StatusTodo:
	case api.StatusDoing:
		move(api.StatusDoing, nil)
	case api.StatusWaiting:
		id := mikah(t, s)
		move(api.StatusWaiting, &id)
	case api.StatusDone:
		move(api.StatusDone, nil)
	case api.StatusDropped:
		move(api.StatusDropped, nil)
	default:
		t.Fatalf("cannot build a task in state %q", status)
	}
	return task
}

// TestTransitionTable runs the complete state machine out of
// testdata/transition_cases.json. Anything the table does not list is
// rejected, which the negative cases in the file cover directly.
func TestTransitionTable(t *testing.T) {
	f := loadTransitions(t)
	ctx := context.Background()

	for _, c := range f.Transitions {
		t.Run(c.From+"->"+c.To, func(t *testing.T) {
			s, now := seeded(t)
			task := inState(t, s, now, c.From)

			patch := api.TaskPatch{Status: &c.To, Presence: map[string]bool{}}
			// Any move into waiting needs to say who you are waiting on.
			if c.To == api.StatusWaiting {
				id := mikah(t, s)
				patch.WaitingOn = &id
				patch.Presence["waiting_on"] = true
			}

			// A transition with a precondition must fail before it is met.
			if strings.Contains(c.Requires, "priority") {
				_, err := s.Patch(ctx, actor, task.ID, patch, "", now)
				assertAPIError(t, err, c.OnViolation.Error)

				p1 := 1
				task, err = s.Patch(ctx, actor, task.ID,
					api.TaskPatch{Priority: &p1, Presence: map[string]bool{"priority": true}}, "", now)
				if err != nil {
					t.Fatalf("set priority: %v", err)
				}
			}
			if strings.Contains(c.Requires, "waiting_on") {
				bare := api.TaskPatch{Status: &c.To, Presence: map[string]bool{}}
				_, err := s.Patch(ctx, actor, task.ID, bare, "", now)
				assertAPIError(t, err, c.OnViolation.Error)
			}

			got, err := s.Patch(ctx, actor, task.ID, patch, "", now)
			if !c.Allowed {
				assertAPIError(t, err, api.ErrIllegalTransition)
				var apiErr *api.Error
				if errors.As(err, &apiErr) {
					if apiErr.From != c.From || apiErr.To != c.To {
						t.Errorf("error carries %s->%s, want %s->%s", apiErr.From, apiErr.To, c.From, c.To)
					}
				}
				// The task must not have moved.
				after, err := s.Get(ctx, task.ID)
				if err != nil {
					t.Fatal(err)
				}
				if after.Status != c.From {
					t.Errorf("status changed to %s on a rejected transition", after.Status)
				}
				return
			}

			if err != nil {
				t.Fatalf("transition %s->%s: %v", c.From, c.To, err)
			}
			if got.Status != c.To {
				t.Errorf("status = %s, want %s", got.Status, c.To)
			}
			for _, field := range c.Clears {
				if v := fieldOf(t, got, field); v != nil {
					t.Errorf("%s should be cleared by %s->%s, got %v", field, c.From, c.To, v)
				}
			}
		})
	}
}

func fieldOf(t *testing.T, task api.Task, name string) any {
	t.Helper()
	switch name {
	case "waiting_on":
		if task.WaitingOn == nil {
			return nil
		}
		return *task.WaitingOn
	case "waiting_since":
		if task.WaitingSince == nil {
			return nil
		}
		return *task.WaitingSince
	case "completed_at":
		if task.CompletedAt == nil {
			return nil
		}
		return *task.CompletedAt
	case "snooze_until":
		if task.SnoozeUntil == nil {
			return nil
		}
		return *task.SnoozeUntil
	}
	t.Fatalf("no such field %q", name)
	return nil
}

func assertAPIError(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error %s, got none", code)
	}
	var apiErr *api.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected an *api.Error %s, got %T: %v", code, err, err)
	}
	if apiErr.Code != code {
		t.Fatalf("error code = %s, want %s", apiErr.Code, code)
	}
}

// TestCompleteSideEffects covers the side_effects block: completing stamps
// completed_at, clears a snooze, and preserves notified_at, start_at, and
// notify.
func TestCompleteSideEffects(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	p2 := 2
	due := "2026-08-20"
	start := "2026-08-10"
	snooze := "2026-08-05T08:00:00-05:00"
	task, err := s.Create(ctx, actor, api.TaskCreate{
		Title: "with every date", Priority: &p2, DueAt: &due, StartAt: &start,
		Notify: api.NotifyOn,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Patch(ctx, actor, task.ID,
		api.TaskPatch{SnoozeUntil: &snooze, Presence: map[string]bool{"snooze_until": true}}, "", now); err != nil {
		t.Fatal(err)
	}
	// notified_at has no patch field: the scheduler owns it.
	if _, err := s.DB().ExecContext(ctx,
		`UPDATE task SET notified_at = ? WHERE id = ?`, "2026-08-04T13:00:00Z", task.ID); err != nil {
		t.Fatal(err)
	}

	res, err := s.Complete(ctx, actor, task.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	done := res.Task
	if done.CompletedAt == nil {
		t.Error("completed_at was not set")
	}
	if done.SnoozeUntil != nil {
		t.Errorf("snooze_until = %v, want cleared", *done.SnoozeUntil)
	}
	if done.NotifiedAt == nil || *done.NotifiedAt != "2026-08-04T13:00:00Z" {
		t.Errorf("notified_at = %v, want preserved", done.NotifiedAt)
	}
	if done.StartAt == nil || *done.StartAt != start {
		t.Errorf("start_at = %v, want preserved", done.StartAt)
	}
	if done.Notify != api.NotifyOn {
		t.Errorf("notify = %s, want preserved as on", done.Notify)
	}

	// Reopening clears completed_at and nothing else.
	todo := api.StatusTodo
	reopened, err := s.Patch(ctx, actor, task.ID, api.TaskPatch{Status: &todo}, "", now)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.CompletedAt != nil {
		t.Errorf("completed_at = %v after reopen, want cleared", *reopened.CompletedAt)
	}
}

// TestCompleteParentDoesNotCascade covers the fixture entry that a parent
// with open children answers with children_open and changes nothing else.
func TestCompleteParentDoesNotCascade(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	p2 := 2
	parent, err := s.Create(ctx, actor, api.TaskCreate{Title: "parent", Priority: &p2}, now)
	if err != nil {
		t.Fatal(err)
	}
	var children []api.Task
	for i := 0; i < 2; i++ {
		c, err := s.Create(ctx, actor, api.TaskCreate{
			Title: "child", Priority: &p2, ParentID: &parent.ID,
		}, now)
		if err != nil {
			t.Fatal(err)
		}
		children = append(children, c)
	}

	res, err := s.Complete(ctx, actor, parent.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if res.ChildrenOpen != 2 {
		t.Errorf("children_open = %d, want 2", res.ChildrenOpen)
	}
	for _, c := range children {
		got, err := s.Get(ctx, c.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != api.StatusTodo {
			t.Errorf("child %d moved to %s, the server must not cascade", got.Num, got.Status)
		}
	}

	// Completing the last child does not complete the parent either.
	for _, c := range children {
		if _, err := s.Complete(ctx, actor, c.ID, now); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Get(ctx, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ChildrenDone != 2 || got.ChildrenTotal != 2 {
		t.Errorf("parent badge = %d/%d, want 2/2", got.ChildrenDone, got.ChildrenTotal)
	}
}

// TestDropParentOrphansChildren covers the fixture entry that dropping a
// parent leaves the children alone and findable with is:orphan.
func TestDropParentOrphansChildren(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	parent, err := s.GetByNum(ctx, 101)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Drop(ctx, actor, parent.ID, now); err != nil {
		t.Fatal(err)
	}

	child, err := s.GetByNum(ctx, 113)
	if err != nil {
		t.Fatal(err)
	}
	if child.Status != api.StatusTodo {
		t.Errorf("child status = %s, dropping a parent must not drop the children", child.Status)
	}

	orphans, err := s.List(ctx, "is:orphan", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 1 || orphans[0].Num != 113 {
		t.Errorf("is:orphan = %v, want [113]", nums(orphans))
	}
}

// TestSnooze covers the snooze side effect: it sets the instant, leaves the
// status alone, and refuses on a done or dropped task with a conflict.
func TestSnooze(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	task, err := s.GetByNum(ctx, 102)
	if err != nil {
		t.Fatal(err)
	}
	until := "2026-08-06T08:00:00-05:00"
	got, err := s.Patch(ctx, actor, task.ID,
		api.TaskPatch{SnoozeUntil: &until, Presence: map[string]bool{"snooze_until": true}}, "", now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != api.StatusTodo {
		t.Errorf("status = %s, snoozing must not change it", got.Status)
	}
	if got.SnoozeUntil == nil || *got.SnoozeUntil != "2026-08-06T13:00:00Z" {
		t.Errorf("snooze_until = %v, want the instant stored as UTC", got.SnoozeUntil)
	}

	doneTask := inState(t, s, now, api.StatusDone)
	_, err = s.Patch(ctx, actor, doneTask.ID,
		api.TaskPatch{SnoozeUntil: &until, Presence: map[string]bool{"snooze_until": true}}, "", now)
	assertAPIError(t, err, api.ErrConflict)
}

// TestDueChangeClearsNotifiedAt covers the side effect that makes a task
// eligible for a new reminder.
func TestDueChangeClearsNotifiedAt(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	task, err := s.GetByNum(ctx, 102)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(ctx,
		`UPDATE task SET notified_at = ? WHERE id = ?`, "2026-08-03T13:00:00Z", task.ID); err != nil {
		t.Fatal(err)
	}

	due := "2026-08-09"
	got, err := s.Patch(ctx, actor, task.ID,
		api.TaskPatch{DueAt: &due, Presence: map[string]bool{"due_at": true}}, "", now)
	if err != nil {
		t.Fatal(err)
	}
	if got.NotifiedAt != nil {
		t.Errorf("notified_at = %v, want cleared by a due change", *got.NotifiedAt)
	}
}

// TestEveryMutationWritesAnEvent covers the rule that no mutation path skips
// the event log, which is what undo and the activity feed depend on.
func TestEveryMutationWritesAnEvent(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	p2 := 2
	task, err := s.Create(ctx, actor, api.TaskCreate{Title: "audited", Priority: &p2}, now)
	if err != nil {
		t.Fatal(err)
	}
	title := "audited, renamed"
	if _, err := s.Patch(ctx, actor, task.ID, api.TaskPatch{Title: &title}, "", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Complete(ctx, actor, task.ID, now); err != nil {
		t.Fatal(err)
	}

	events, err := s.Events(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range events {
		if e.TaskID == task.ID {
			kinds = append(kinds, e.Kind)
		}
	}
	want := []string{api.KindTaskCreated, api.KindTaskUpdated, api.KindTaskComplete}
	if len(kinds) != len(want) {
		t.Fatalf("kinds = %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Errorf("event %d = %s, want %s", i, kinds[i], want[i])
		}
	}
}

// TestIfMatchGuardsAStaleEdit covers the PATCH precondition that stops a slow
// TUI from clobbering a web edit.
func TestIfMatchGuardsAStaleEdit(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	task, err := s.GetByNum(ctx, 103)
	if err != nil {
		t.Fatal(err)
	}
	stale := task.UpdatedAt

	first := "edited by the web"
	later := now.Add(time.Minute)
	if _, err := s.Patch(ctx, actor, task.ID, api.TaskPatch{Title: &first}, stale, later); err != nil {
		t.Fatal(err)
	}

	second := "edited by the tui"
	_, err = s.Patch(ctx, actor, task.ID, api.TaskPatch{Title: &second}, stale, later.Add(time.Minute))
	assertAPIError(t, err, api.ErrConflict)
}

// TestSubtasksGoOneLevelDeep covers the API-enforced nesting limit.
func TestSubtasksGoOneLevelDeep(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	child, err := s.GetByNum(ctx, 113)
	if err != nil {
		t.Fatal(err)
	}
	p2 := 2
	_, err = s.Create(ctx, actor, api.TaskCreate{
		Title: "grandchild", Priority: &p2, ParentID: &child.ID,
	}, now)
	assertAPIError(t, err, api.ErrNestingTooDeep)
}

// TestSubtaskCopiesParentTags covers the one thing a subtask inherits, and
// only as a copy.
func TestSubtaskCopiesParentTags(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	parent, err := s.GetByNum(ctx, 101)
	if err != nil {
		t.Fatal(err)
	}
	p2 := 2
	child, err := s.Create(ctx, actor, api.TaskCreate{
		Title: "inherits tags", Priority: &p2, ParentID: &parent.ID,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(child.Tags, ",") != "certs,ops" {
		t.Errorf("child tags = %v, want the parent's [certs ops] copied", child.Tags)
	}

	// The copy is editable and editing it must not touch the parent.
	only := []string{"certs"}
	if _, err := s.Patch(ctx, actor, child.ID, api.TaskPatch{Tags: &only}, "", now); err != nil {
		t.Fatal(err)
	}
	again, err := s.Get(ctx, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(again.Tags, ",") != "certs,ops" {
		t.Errorf("parent tags = %v, editing a child must not rewrite the parent", again.Tags)
	}
}

// TestCreateIsIdempotent covers the client-supplied id contract that lets a
// plugin replay without duplicating rows.
func TestCreateIsIdempotent(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	id := store.NewID()
	in := api.TaskCreate{ID: id, Title: "sent twice"}
	first, err := s.Create(ctx, actor, in, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Create(ctx, actor, in, now)
	if err != nil {
		t.Fatal(err)
	}
	if first.Num != second.Num {
		t.Errorf("num %d then %d, a replay must not create a second row", first.Num, second.Num)
	}
}

// TestQuickAddLandsInInbox covers the capture rule: everything from quick-add
// lands in the inbox unless a priority or a due date came in with it.
func TestQuickAddLandsInInbox(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	bare, err := s.Create(ctx, actor, api.TaskCreate{Title: "call the dealer"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if bare.Status != api.StatusInbox {
		t.Errorf("status = %s, want inbox", bare.Status)
	}

	due := "2026-08-07"
	dated, err := s.Create(ctx, actor, api.TaskCreate{Title: "renew cert", DueAt: &due}, now)
	if err != nil {
		t.Fatal(err)
	}
	if dated.Status != api.StatusTodo {
		t.Errorf("status = %s, want todo when a due date came in with it", dated.Status)
	}
}

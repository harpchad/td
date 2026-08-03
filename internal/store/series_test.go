package store_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/recur"
	"github.com/harpchad/td/internal/store"
)

// The cases here come out of testdata/recurrence_cases.json. internal/recur
// already proves the arithmetic; what these prove is what the store does with
// it: how many task rows exist afterwards, and how many events say what
// happened to the occurrences that never became rows.

type recurFixture struct {
	Catchup []struct {
		Name                string   `json:"name"`
		RRule               string   `json:"rrule"`
		Dtstart             string   `json:"dtstart"`
		Mode                string   `json:"mode"`
		Catchup             string   `json:"catchup"`
		Now                 string   `json:"now"`
		ExpectNextDue       string   `json:"expect_next_due"`
		ExpectMissed        []string `json:"expect_missed"`
		ExpectOpenInstances int      `json:"expect_open_instances"`
		ExpectDueDates      []string `json:"expect_due_dates"`
		Note                string   `json:"note"`
	} `json:"catchup"`

	AfterCompletion []struct {
		Name          string `json:"name"`
		RRule         string `json:"rrule"`
		PreviousDue   string `json:"previous_due"`
		CompletedAt   string `json:"completed_at"`
		ExpectNextDue string `json:"expect_next_due"`
		Note          string `json:"note"`
	} `json:"after_completion_mode"`
}

func loadRecurFixture(t *testing.T) recurFixture {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "testdata", "recurrence_cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f recurFixture
	if err := json.Unmarshal(body, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func at(t *testing.T, s string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return parsed
}

// TestCatchupPolicies is the fixture's catch-up section run through the
// store. The skip case is the one that matters: six occurrences pass and the
// list still has one row, because a chore you already ignored once does not
// become six chores.
func TestCatchupPolicies(t *testing.T) {
	f := loadRecurFixture(t)
	if len(f.Catchup) != 2 {
		t.Fatalf("%d catchup cases, want 2", len(f.Catchup))
	}

	for _, c := range f.Catchup {
		t.Run(c.Name, func(t *testing.T) {
			s, _ := seeded(t)
			ctx := context.Background()

			start := at(t, c.Dtstart)
			due := start.Format(time.RFC3339)
			series, first, err := s.CreateSeries(ctx, actor, store.Series{
				RRule: c.RRule, Mode: c.Mode, Catchup: c.Catchup,
				TZ:       "America/Chicago",
				Template: api.TaskCreate{Title: "Stand-up notes", DueAt: &due},
			}, start)
			if err != nil {
				t.Fatal(err)
			}
			if first.SeriesID == nil || *first.SeriesID != series.ID {
				t.Fatalf("the first instance is not linked to its series: %+v", first.SeriesID)
			}

			// Reload so the walk starts from what was persisted, which is what
			// a scheduler restart would see.
			series, err = s.Series(ctx, series.ID)
			if err != nil {
				t.Fatal(err)
			}

			now := at(t, c.Now)
			made, err := s.AdvanceSeries(ctx, actor, series, now)
			if err != nil {
				t.Fatal(err)
			}

			switch c.Catchup {
			case recur.CatchupSkip:
				missed := missedDues(t, s, series.ID)
				if len(missed) != len(c.ExpectMissed) {
					t.Fatalf("%d recurrence.missed events, want %d: %v",
						len(missed), len(c.ExpectMissed), missed)
				}
				for i, want := range c.ExpectMissed {
					if missed[i] != want {
						t.Errorf("miss %d = %s, want %s", i, missed[i], want)
					}
				}
				if len(made) != 0 {
					t.Errorf("skip created %d instances while one was still open", len(made))
				}
				open := openInSeries(t, s, series.ID)
				if len(open) != c.ExpectOpenInstances {
					t.Errorf("%d open instances, want %d\n%s", len(open), c.ExpectOpenInstances, c.Note)
				}

				after, err := s.Series(ctx, series.ID)
				if err != nil {
					t.Fatal(err)
				}
				if after.NextAt == nil {
					t.Fatal("next_at was not rolled forward")
				}
				if got := at(t, *after.NextAt).In(start.Location()).Format(time.RFC3339); got != c.ExpectNextDue {
					t.Errorf("next due = %s, want %s", got, c.ExpectNextDue)
				}

			case recur.CatchupPile:
				if len(made) != c.ExpectOpenInstances {
					t.Fatalf("pile created %d instances, want %d", len(made), c.ExpectOpenInstances)
				}
				for i, want := range c.ExpectDueDates {
					got := at(t, *made[i].DueAt).In(start.Location()).Format(recur.DateLayout)
					if got != want {
						t.Errorf("instance %d due %s, want %s", i, got, want)
					}
				}
				if n := len(missedDues(t, s, series.ID)); n != 0 {
					t.Errorf("pile logged %d misses; nothing was missed, it was all created", n)
				}
			}
		})
	}
}

// TestAfterCompletionGeneratesOnCompletion covers the mode's whole point: the
// next instance appears when the task is completed, and its due date counts
// from the completion rather than from the previous due.
func TestAfterCompletionGeneratesOnCompletion(t *testing.T) {
	f := loadRecurFixture(t)

	for _, c := range f.AfterCompletion {
		if c.PreviousDue == "" {
			// Those cases pin arithmetic that internal/recur already covers.
			continue
		}
		t.Run(c.Name, func(t *testing.T) {
			s, _ := seeded(t)
			ctx := context.Background()

			completed := at(t, c.CompletedAt)
			series, first, err := s.CreateSeries(ctx, actor, store.Series{
				RRule: c.RRule, Mode: recur.ModeAfterCompletion,
				TZ:       "America/Chicago",
				Template: api.TaskCreate{Title: "Water the plants", DueAt: &c.PreviousDue},
			}, completed.AddDate(0, 0, -30))
			if err != nil {
				t.Fatal(err)
			}

			if _, err := s.Complete(ctx, actor, first.ID, completed); err != nil {
				t.Fatal(err)
			}

			open := openInSeries(t, s, series.ID)
			if len(open) != 1 {
				t.Fatalf("%d open instances after completion, want exactly one", len(open))
			}
			next := open[0]
			if next.ID == first.ID {
				t.Fatal("no new instance was generated")
			}
			if !next.DueIsDate {
				t.Error("a date-only template produced an instant")
			}
			if got := *next.DueAt; got != c.ExpectNextDue {
				t.Errorf("next due = %s, want %s\n%s", got, c.ExpectNextDue, c.Note)
			}
		})
	}
}

// TestFixedModeGeneratesNothingAtCompletion is the other half of the
// transition fixture: a fixed series waits for its rule, so completing an
// instance early leaves the list empty until the next occurrence.
func TestFixedModeGeneratesNothingAtCompletion(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	due := now.Format(time.RFC3339)
	series, first, err := s.CreateSeries(ctx, actor, store.Series{
		RRule: "FREQ=WEEKLY", Mode: recur.ModeFixed, TZ: "America/Chicago",
		Template: api.TaskCreate{Title: "Weekly review", DueAt: &due},
	}, now)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Complete(ctx, actor, first.ID, now); err != nil {
		t.Fatal(err)
	}
	if open := openInSeries(t, s, series.ID); len(open) != 0 {
		t.Fatalf("completing a fixed instance generated %d tasks; the scheduler does that", len(open))
	}

	// A week later the rule fires and the instance appears.
	made, err := s.AdvanceSeries(ctx, actor, mustSeries(t, s, series.ID), now.AddDate(0, 0, 7))
	if err != nil {
		t.Fatal(err)
	}
	if len(made) != 1 {
		t.Fatalf("the rule fired and produced %d instances, want 1", len(made))
	}
}

// TestReopeningLeavesTheGeneratedInstanceAlone pins the transition fixture's
// note. Reopening after the next instance exists leaves two open tasks, which
// is correct: undoing a completion is not the same as undoing a recurrence,
// and guessing which one was meant is worse than showing both.
func TestReopeningLeavesTheGeneratedInstanceAlone(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	due := now.Format(recur.DateLayout)
	series, first, err := s.CreateSeries(ctx, actor, store.Series{
		RRule: "FREQ=DAILY;INTERVAL=3", Mode: recur.ModeAfterCompletion,
		TZ:       "America/Chicago",
		Template: api.TaskCreate{Title: "Refill the humidifier", DueAt: &due},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Complete(ctx, actor, first.ID, now); err != nil {
		t.Fatal(err)
	}
	generated := openInSeries(t, s, series.ID)
	if len(generated) != 1 {
		t.Fatalf("%d instances generated, want 1", len(generated))
	}

	todo := api.StatusTodo
	if _, err := s.Patch(ctx, actor, first.ID, api.TaskPatch{Status: &todo}, "", now); err != nil {
		t.Fatal(err)
	}

	open := openInSeries(t, s, series.ID)
	if len(open) != 2 {
		t.Fatalf("%d open instances after reopening, want 2", len(open))
	}
	// And the generated one is untouched.
	still, err := s.Get(ctx, generated[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if still.UpdatedAt != generated[0].UpdatedAt {
		t.Error("reopening the completed instance modified the generated one")
	}
}

// TestEditingAnInstanceDoesNotEditTheSeries covers section 3's rule that
// editing an instance edits that instance. The series template is what the
// next instance is made from, and a one-off change to today's copy must not
// leak into it.
func TestEditingAnInstanceDoesNotEditTheSeries(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	due := now.Format(recur.DateLayout)
	series, first, err := s.CreateSeries(ctx, actor, store.Series{
		RRule: "FREQ=DAILY", Mode: recur.ModeFixed, TZ: "America/Chicago",
		Template: api.TaskCreate{Title: "Feed the cat", DueAt: &due},
	}, now)
	if err != nil {
		t.Fatal(err)
	}

	retitled := "Feed the cat twice"
	if _, err := s.Patch(ctx, actor, first.ID, api.TaskPatch{Title: &retitled}, "", now); err != nil {
		t.Fatal(err)
	}

	reloaded, err := s.Series(ctx, series.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Template.Title != "Feed the cat" {
		t.Errorf("the series template became %q; editing an instance edits that instance", reloaded.Template.Title)
	}

	// And the next instance is made from the template, not from the edit.
	if _, err := s.Complete(ctx, actor, first.ID, now); err != nil {
		t.Fatal(err)
	}
	made, err := s.AdvanceSeries(ctx, actor, reloaded, now.AddDate(0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	if len(made) != 1 || made[0].Title != "Feed the cat" {
		t.Errorf("next instance = %+v, want the template's title", made)
	}
}

// TestUpdateSeriesChangesFutureInstancesOnly is the explicit series edit that
// section 3 requires: it exists, it is separate, and it does not rewrite the
// instance already sitting in the list.
func TestUpdateSeriesChangesFutureInstancesOnly(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	due := now.Format(recur.DateLayout)
	series, first, err := s.CreateSeries(ctx, actor, store.Series{
		RRule: "FREQ=DAILY", Mode: recur.ModeFixed, TZ: "America/Chicago",
		Template: api.TaskCreate{Title: "Stretch", DueAt: &due},
	}, now)
	if err != nil {
		t.Fatal(err)
	}

	series.Template.Title = "Stretch for ten minutes"
	if _, err := s.UpdateSeries(ctx, series); err != nil {
		t.Fatal(err)
	}

	unchanged, err := s.Get(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Title != "Stretch" {
		t.Errorf("the open instance was rewritten to %q", unchanged.Title)
	}

	if _, err := s.Complete(ctx, actor, first.ID, now); err != nil {
		t.Fatal(err)
	}
	made, err := s.AdvanceSeries(ctx, actor, mustSeries(t, s, series.ID), now.AddDate(0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	if len(made) != 1 || made[0].Title != "Stretch for ten minutes" {
		t.Errorf("next instance = %+v, want the edited template", made)
	}
}

func mustSeries(t *testing.T, s *store.Store, id string) store.Series {
	t.Helper()
	series, err := s.Series(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return series
}

// missedDues returns the due dates of the recurrence.missed events for one
// series, oldest first.
func missedDues(t *testing.T, s *store.Store, seriesID string) []string {
	t.Helper()
	events, err := s.Events(context.Background(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range events {
		if e.Kind != api.KindRecurrenceMissed {
			continue
		}
		if id, _ := e.Patch.Meta["series"].(string); id != seriesID {
			continue
		}
		due, _ := e.Patch.Meta["due"].(string)
		out = append(out, due)
	}
	return out
}

// openInSeries returns the series' instances that are neither done nor
// dropped, oldest first.
func openInSeries(t *testing.T, s *store.Store, seriesID string) []api.Task {
	t.Helper()
	tasks, err := s.List(context.Background(), "is:open", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var out []api.Task
	for _, task := range tasks {
		if task.SeriesID != nil && *task.SeriesID == seriesID {
			out = append(out, task)
		}
	}
	return out
}

// TestRepeatingATaskDoesNotDuplicateIt.
//
// Reported by building the web form and looking at the result: repeating task
// 101 left 101 exactly as it was and created a second task with the same title
// and the same due date, one attached to the series and one not. CreateSeries
// materializes from the template, which is right for the API, where there is
// no task yet, and wrong from a task, where there already is one.
func TestRepeatingATaskDoesNotDuplicateIt(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	before, err := s.List(ctx, "is:open", now)
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.GetByNum(ctx, 101)
	if err != nil {
		t.Fatal(err)
	}

	series, out, err := s.RepeatTask(ctx, actor, task.ID, store.Series{
		RRule: "FREQ=WEEKLY;INTERVAL=2", TZ: now.Location().String(),
		Template: api.TaskCreate{Title: task.Title, DueAt: task.DueAt},
	}, now)
	if err != nil {
		t.Fatal(err)
	}

	after, err := s.List(ctx, "is:open", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("%d open tasks became %d; repeating a task created another one",
			len(before), len(after))
	}

	// The task in front of you is the instance now.
	if out.ID != task.ID {
		t.Errorf("the series adopted %s, not the task it was called on (%s)", out.ID, task.ID)
	}
	if out.SeriesID == nil || *out.SeriesID != series.ID {
		t.Errorf("task 101 is not attached to the new series")
	}
	if series.CurrentTaskID == nil || *series.CurrentTaskID != task.ID {
		t.Error("the series does not point back at the task")
	}

	// Adopting changes what comes next, not what the task is.
	if out.Title != task.Title || out.Status != task.Status {
		t.Errorf("the task changed: %q/%s became %q/%s",
			task.Title, task.Status, out.Title, out.Status)
	}
	if (out.DueAt == nil) != (task.DueAt == nil) ||
		(out.DueAt != nil && *out.DueAt != *task.DueAt) {
		t.Error("adopting the task moved its due date")
	}

	// Section 3: exactly one open instance at a time.
	instances, err := s.List(ctx, "is:open", now)
	if err != nil {
		t.Fatal(err)
	}
	open := 0
	for _, candidate := range instances {
		if candidate.SeriesID != nil && *candidate.SeriesID == series.ID {
			open++
		}
	}
	if open != 1 {
		t.Errorf("%d open instances of the series, want exactly 1", open)
	}
}

// TestATaskCanOnlyBelongToOneSeries. Attaching a second rule to an instance
// would give two schedules a claim on the same task.
func TestATaskCanOnlyBelongToOneSeries(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	task, err := s.GetByNum(ctx, 101)
	if err != nil {
		t.Fatal(err)
	}
	in := store.Series{
		RRule: "FREQ=WEEKLY", TZ: now.Location().String(),
		Template: api.TaskCreate{Title: task.Title, DueAt: task.DueAt},
	}
	if _, _, err := s.RepeatTask(ctx, actor, task.ID, in, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RepeatTask(ctx, actor, task.ID, in, now); err == nil {
		t.Error("a task was attached to a second series")
	}
}

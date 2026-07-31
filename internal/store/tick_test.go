package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/notify"
	"github.com/harpchad/td/internal/recur"
	"github.com/harpchad/td/internal/store"
)

// sweeper records what a sweep was asked to keep, so a test can assert on the
// keep set without a real directory.
type sweeper struct {
	keep  map[string]bool
	calls int
}

func (s *sweeper) Sweep(keep map[string]bool) (int, error) {
	s.keep = keep
	s.calls++
	return 0, nil
}

// TestTheTickFiresRecurrenceWithRemindersOff is the thing that would be easy
// to get wrong: recurrence used to live behind the reminder scheduler's early
// return, so turning push off would silently stop a repeating task from
// repeating.
func TestTheTickFiresRecurrenceWithRemindersOff(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	due := now.Format(recur.DateLayout)
	series, first, err := s.CreateSeries(ctx, actor, store.Series{
		RRule: "FREQ=DAILY", Mode: recur.ModeFixed, TZ: now.Location().String(),
		Template: api.TaskCreate{Title: "Morning pages", DueAt: &due},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Complete(ctx, actor, first.ID, now); err != nil {
		t.Fatal(err)
	}

	tomorrow := now.AddDate(0, 0, 1)
	sched := &notify.Scheduler{
		// No topic, so Policy.Enabled() is false.
		Policy: notify.DefaultPolicy, Store: s, Sender: &recorder{},
		BaseURL: "https://td.example.com", Loc: now.Location(),
		Log: discardLogger(), Now: func() time.Time { return tomorrow },
	}
	sched.Once(ctx)

	open := openInSeries(t, s, series.ID)
	if len(open) != 1 {
		t.Fatalf("%d instances after a tick with reminders off, want 1", len(open))
	}
}

// TestTheSweepKeepsEveryReferencedBlob covers the keep set the weekly orphan
// collection runs against. A bug here deletes live attachments, so it is
// asserted on directly rather than through the sweep's return value.
func TestTheSweepKeepsEveryReferencedBlob(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	// The fixture already attaches a file, so the assertions below are
	// relative to what was there.
	baseline, err := s.ReferencedBlobs(ctx)
	if err != nil {
		t.Fatal(err)
	}

	task, err := s.GetByNum(ctx, 103)
	if err != nil {
		t.Fatal(err)
	}
	live, err := s.AddAttachment(ctx, actor, api.Attachment{
		TaskID: task.ID, SHA256: digestOf("a"), Filename: "a.pdf", Bytes: 1,
	}, now)
	if err != nil {
		t.Fatal(err)
	}

	// A dropped task's attachment is still referenced. Dropping is not
	// deleting, and an undo has to find the file where it left it.
	dropped, err := s.GetByNum(ctx, 101)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddAttachment(ctx, actor, api.Attachment{
		TaskID: dropped.ID, SHA256: digestOf("b"), Filename: "b.pdf", Bytes: 1,
	}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Drop(ctx, actor, dropped.ID, now); err != nil {
		t.Fatal(err)
	}

	sw := &sweeper{}
	sched := &notify.Scheduler{
		Policy: notify.DefaultPolicy, Store: s, Sender: &recorder{}, Blobs: sw,
		BaseURL: "https://td.example.com", Loc: now.Location(),
		Log: discardLogger(), Now: func() time.Time { return now },
	}

	sched.Once(ctx)
	if sw.calls != 1 {
		t.Fatalf("%d sweeps on the first tick, want 1", sw.calls)
	}
	if len(sw.keep) != len(baseline)+2 {
		t.Fatalf("keep set has %d digests, want the fixture's %d plus 2", len(sw.keep), len(baseline))
	}
	if !sw.keep[digestOf("a")] || !sw.keep[digestOf("b")] {
		t.Errorf("keep set = %v, want both new digests", sw.keep)
	}
	for digest := range baseline {
		if !sw.keep[digest] {
			t.Errorf("the fixture's attachment %s fell out of the keep set", digest)
		}
	}

	// Weekly, not every minute.
	sched.Now = func() time.Time { return now.Add(time.Hour) }
	sched.Once(ctx)
	if sw.calls != 1 {
		t.Errorf("swept %d times within an hour, want 1", sw.calls)
	}
	sched.Now = func() time.Time { return now.Add(notify.SweepEvery + time.Minute) }
	sched.Once(ctx)
	if sw.calls != 2 {
		t.Errorf("%d sweeps after a week, want 2", sw.calls)
	}

	// Detaching drops it out of the keep set, which is what makes it
	// collectable on the next pass.
	if err := s.RemoveAttachment(ctx, actor, live.ID, now); err != nil {
		t.Fatal(err)
	}
	sched.Now = func() time.Time { return now.Add(2*notify.SweepEvery + time.Minute) }
	sched.Once(ctx)
	if sw.keep[digestOf("a")] {
		t.Error("a detached blob is still in the keep set")
	}
}

// digestOf builds a syntactically valid sha256 out of a marker character, so
// a test can name blobs without hashing anything.
func digestOf(marker string) string {
	out := make([]byte, 0, 64)
	for len(out) < 64 {
		out = append(out, marker[0])
	}
	return string(out)
}

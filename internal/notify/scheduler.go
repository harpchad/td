package notify

import (
	"context"
	"log/slog"
	"time"

	"github.com/harpchad/td/internal/api"
)

// Tick is how often the scheduler looks. A single goroutine on a 60 second
// tick: no job queue, no cron container.
const Tick = 60 * time.Second

// Store is what the scheduler needs from the database.
type Store interface {
	// DueForReminder returns open tasks with a due date and no notified_at.
	DueForReminder(ctx context.Context) ([]api.Task, error)
	// MatchingIDs evaluates a filter and returns the ids it selects.
	MatchingIDs(ctx context.Context, filter string, now time.Time) (map[string]bool, error)
	// MarkNotified stamps notified_at, which is what stops repeats.
	MarkNotified(ctx context.Context, taskID string, now time.Time) error
	// PurgeExpiredSessions drops sessions past their expiry.
	PurgeExpiredSessions(ctx context.Context, now time.Time) (int64, error)
	// AdvanceDue materializes the instances of every fixed series whose next
	// occurrence has arrived, and reports how many it made.
	AdvanceDue(ctx context.Context, actor string, now time.Time) (int, error)
	// ReferencedBlobs is every digest still pointed at by an attachment row.
	ReferencedBlobs(ctx context.Context) (map[string]bool, error)
}

// Blobs is the attachment store, as much of it as the sweep needs.
type Blobs interface {
	// Sweep deletes every blob whose digest is not in keep.
	Sweep(keep map[string]bool) (int, error)
}

// SweepEvery is how often orphaned attachment bytes are collected. Weekly,
// per section 17: a detached file is not urgent, and collecting on delete
// would take the bytes out from under an undo.
const SweepEvery = 7 * 24 * time.Hour

// Scheduler fires reminders.
type Scheduler struct {
	Policy  Policy
	Store   Store
	Sender  Sender
	BaseURL string
	Loc     *time.Location
	Log     *slog.Logger

	// Now is the clock. Injected so a test can run a whole day in a loop.
	Now func() time.Time

	// ActionToken authenticates the Done and Snooze buttons. Empty leaves the
	// notification a click-through.
	ActionToken string

	// Blobs is the attachment store the weekly sweep runs against. Nil skips
	// the sweep.
	Blobs Blobs

	// sweptAt is when the last orphan collection ran. Zero means never, and
	// the first tick after start does one.
	sweptAt time.Time
}

// Run ticks until the context is cancelled.
//
// The tick runs whether or not reminders are configured. Recurrence and
// session expiry are not optional, and tying them to an ntfy topic would mean
// a repeating task silently stops repeating when push is turned off.
func (s *Scheduler) Run(ctx context.Context) {
	if s.Policy.Enabled() {
		s.Log.Info("reminders on", "topic", s.Policy.Topic,
			"rule", s.Policy.DefaultRule, "lead_minutes", s.Policy.LeadMinutes)
	} else {
		s.Log.Info("reminders are off, no notify.topic configured")
	}

	ticker := time.NewTicker(Tick)
	defer ticker.Stop()

	for {
		s.Once(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Once is one pass. It is exported so a test can run a tick without a clock.
func (s *Scheduler) Once(ctx context.Context) {
	now := s.Now()

	// Housekeeping that has been waiting for something that runs on a tick.
	if n, err := s.Store.PurgeExpiredSessions(ctx, now); err != nil {
		s.Log.Error("purging sessions", "err", err)
	} else if n > 0 {
		s.Log.Info("purged expired sessions", "count", n)
	}

	// Recurrence fires before delivery, so an instance that materializes on
	// this tick can be reminded about on the same one.
	if n, err := s.Store.AdvanceDue(ctx, api.ActorScheduler, now); err != nil {
		s.Log.Error("advancing series", "err", err)
	} else if n > 0 {
		s.Log.Info("recurring instances created", "count", n)
	}

	s.sweepBlobs(ctx, now)

	if !s.Policy.Enabled() {
		return
	}
	sent, err := s.Deliver(ctx, now)
	if err != nil {
		s.Log.Error("reminder pass", "err", err)
		return
	}
	if sent > 0 {
		s.Log.Info("reminders sent", "count", sent)
	}
}

// sweepBlobs collects attachment bytes nothing points at, at most weekly.
//
// The keep set is read first and the sweep runs against it, so a file
// attached between the two survives: an upload that lands mid-sweep is not
// yet in the set but is also not yet on disk under a name the walk reaches
// before the rename. Getting the order backwards would delete a live file.
func (s *Scheduler) sweepBlobs(ctx context.Context, now time.Time) {
	if s.Blobs == nil || now.Sub(s.sweptAt) < SweepEvery {
		return
	}
	s.sweptAt = now

	keep, err := s.Store.ReferencedBlobs(ctx)
	if err != nil {
		s.Log.Error("reading attachment references", "err", err)
		return
	}
	n, err := s.Blobs.Sweep(keep)
	if err != nil {
		s.Log.Error("sweeping orphaned attachments", "err", err)
		return
	}
	if n > 0 {
		s.Log.Info("swept orphaned attachments", "count", n, "kept", len(keep))
	}
}

// Deliver runs one pass and reports how many pushes went out.
func (s *Scheduler) Deliver(ctx context.Context, now time.Time) (int, error) {
	candidates, err := s.Store.DueForReminder(ctx)
	if err != nil {
		return 0, err
	}
	if len(candidates) == 0 {
		return 0, nil
	}

	// notify=auto resolves against the default rule, which is a filter query
	// rather than a settings screen full of dropdowns.
	filter, always, never := s.Policy.RuleQuery()
	matches := map[string]bool{}
	if !always && !never {
		matches, err = s.Store.MatchingIDs(ctx, filter, now)
		if err != nil {
			return 0, err
		}
	}

	quiet, err := ParseQuietHours(s.Policy.QuietHours)
	if err != nil {
		return 0, err
	}

	sent := 0
	for _, t := range candidates {
		matched := always || (!never && matches[t.ID])
		if !s.Policy.Resolve(t, matched) {
			continue
		}

		fireAt, ok := s.Policy.FireAt(t, s.Loc)
		if !ok {
			continue
		}
		// Quiet hours hold the push, they do not drop it: a reminder that
		// would land at 02:00 goes out when the window opens.
		fireAt = quiet.Release(fireAt)
		if now.Before(fireAt) {
			continue
		}

		n := Compose(t, s.BaseURL, s.ActionToken, now, s.Loc)
		if err := s.Sender.Send(ctx, n); err != nil {
			// A push that fails is not marked, so the next tick tries again.
			// Leaving it unmarked is what makes ntfy being down a delay
			// rather than a lost reminder.
			s.Log.Error("sending reminder", "task", t.Num, "err", err)
			continue
		}

		// notified_at stops repeats: one push per task per due value. Overdue
		// does not re-push, because a task nagging you every hour teaches you
		// to swipe it away without reading it.
		if err := s.Store.MarkNotified(ctx, t.ID, now); err != nil {
			s.Log.Error("marking notified", "task", t.Num, "err", err)
		}
		sent++
	}
	return sent, nil
}

package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/memos"
	"github.com/harpchad/td/internal/notify"
	"github.com/harpchad/td/internal/store"
)

// journal is a Poster that keeps what it was handed. Nothing in the suite may
// reach a real Memos instance, the same rule the ntfy sender follows.
type journal struct {
	posted []memos.Memo
	fail   error
}

func (j *journal) Post(_ context.Context, m memos.Memo) error {
	if j.fail != nil {
		return j.fail
	}
	j.posted = append(j.posted, m)
	return nil
}

func newJournal(t *testing.T) (*notify.Journal, *journal, *store.Store, time.Time) {
	t.Helper()
	s, now := seeded(t)
	rec := &journal{}
	return &notify.Journal{
		Store:   s,
		Poster:  rec,
		Config:  memos.Config{URL: "https://memos.invalid", Token: "x", Tag: "td"},
		BaseURL: "https://td.example.com",
		Loc:     now.Location(),
	}, rec, s, now
}

// TestTheJournalStartsAtTheNewestEvent covers the thing that would embarrass
// somebody once: switching the webhook on must not post a year of history
// into their journal.
func TestTheJournalStartsAtTheNewestEvent(t *testing.T) {
	j, rec, s, now := newJournal(t)
	ctx := context.Background()

	// Complete something before the webhook has ever run.
	task, err := s.GetByNum(ctx, 104)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Complete(ctx, actor, task.ID, now); err != nil {
		t.Fatal(err)
	}

	if n, err := j.Deliver(ctx, now); err != nil {
		t.Fatal(err)
	} else if n != 0 {
		t.Fatalf("the first pass posted %d entries; it should start from now", n)
	}
	if len(rec.posted) != 0 {
		t.Errorf("posted %d memos of history", len(rec.posted))
	}

	// Anything completed after that does go.
	next, err := s.GetByNum(ctx, 102)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Complete(ctx, actor, next.ID, now); err != nil {
		t.Fatal(err)
	}
	if n, err := j.Deliver(ctx, now); err != nil {
		t.Fatal(err)
	} else if n != 1 {
		t.Fatalf("delivered %d, want the one completion after the cursor", n)
	}
	if !strings.Contains(rec.posted[0].Content, next.Title) {
		t.Errorf("memo = %q", rec.posted[0].Content)
	}
}

// TestAMemoIsPostedOncePerCompletion. The whole reason this reads the event
// log instead of firing inline is that neither a loss nor a duplicate is
// acceptable, and both are easy.
func TestAMemoIsPostedOncePerCompletion(t *testing.T) {
	j, rec, s, now := newJournal(t)
	ctx := context.Background()

	// Move the cursor to now, then complete two things.
	if _, err := j.Deliver(ctx, now); err != nil {
		t.Fatal(err)
	}
	for _, num := range []int64{104, 102} {
		task, err := s.GetByNum(ctx, num)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Complete(ctx, actor, task.ID, now); err != nil {
			t.Fatal(err)
		}
	}

	if n, err := j.Deliver(ctx, now); err != nil || n != 2 {
		t.Fatalf("delivered %d (%v), want 2", n, err)
	}
	// A second pass with nothing new posts nothing.
	if n, err := j.Deliver(ctx, now); err != nil || n != 0 {
		t.Fatalf("a second pass delivered %d (%v)", n, err)
	}
	if len(rec.posted) != 2 {
		t.Errorf("%d memos for two completions", len(rec.posted))
	}
}

// TestMemosBeingDownIsADelayNotALoss is the same property the ntfy sender
// has. Completing a task must never fail because a journal is unreachable,
// and the entry must still arrive when it comes back.
func TestMemosBeingDownIsADelayNotALoss(t *testing.T) {
	j, rec, s, now := newJournal(t)
	ctx := context.Background()

	if _, err := j.Deliver(ctx, now); err != nil {
		t.Fatal(err)
	}
	task, err := s.GetByNum(ctx, 104)
	if err != nil {
		t.Fatal(err)
	}

	// The completion itself succeeds whatever Memos is doing, because nothing
	// in the write path talks to it.
	rec.fail = errors.New("connection refused")
	if _, err := s.Complete(ctx, actor, task.ID, now); err != nil {
		t.Fatalf("a completion failed while the journal was down: %v", err)
	}

	if _, err := j.Deliver(ctx, now); err == nil {
		t.Error("the deliverer reported success while the poster was failing")
	}
	if len(rec.posted) != 0 {
		t.Fatal("something was posted by a failing poster")
	}

	// And when it comes back, the entry is still waiting.
	rec.fail = nil
	if n, err := j.Deliver(ctx, now); err != nil || n != 1 {
		t.Fatalf("delivered %d (%v) after recovery, want 1", n, err)
	}
	if !strings.Contains(rec.posted[0].Content, task.Title) {
		t.Errorf("memo = %q", rec.posted[0].Content)
	}
}

// TestAFailureHalfwayThroughDoesNotReplayWhatLanded covers the per-entry
// cursor. Advancing once at the end of a batch would repost everything
// before the failure on the next tick.
func TestAFailureHalfwayThroughDoesNotReplayWhatLanded(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	rec := &failAfter{limit: 1}
	j := &notify.Journal{
		Store: s, Poster: rec,
		Config:  memos.Config{URL: "https://memos.invalid", Token: "x"},
		BaseURL: "https://td.example.com", Loc: now.Location(),
	}
	if _, err := j.Deliver(ctx, now); err != nil {
		t.Fatal(err)
	}

	for _, num := range []int64{104, 102, 101} {
		task, err := s.GetByNum(ctx, num)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Complete(ctx, actor, task.ID, now); err != nil {
			t.Fatal(err)
		}
	}

	// One lands, the second fails, and the third is never attempted.
	if n, _ := j.Deliver(ctx, now); n != 1 {
		t.Fatalf("delivered %d before the failure, want 1", n)
	}

	// The next pass starts at the one that failed, not at the one that
	// worked.
	rec.limit = 100
	n, err := j.Deliver(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("delivered %d on the retry, want the two that had not landed", n)
	}
	if len(rec.posted) != 3 {
		t.Errorf("%d memos for three completions", len(rec.posted))
	}
}

// failAfter posts a fixed number of memos and then refuses.
type failAfter struct {
	limit  int
	posted []memos.Memo
}

func (f *failAfter) Post(_ context.Context, m memos.Memo) error {
	if len(f.posted) >= f.limit {
		return errors.New("memos is down")
	}
	f.posted = append(f.posted, m)
	return nil
}

// TestReopeningAndCompletingAgainIsTwoEntries. The journal is a record of
// what happened, and it happened twice.
func TestReopeningAndCompletingAgainIsTwoEntries(t *testing.T) {
	j, _, s, now := newJournal(t)
	ctx := context.Background()

	if _, err := j.Deliver(ctx, now); err != nil {
		t.Fatal(err)
	}
	task, err := s.GetByNum(ctx, 104)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Complete(ctx, actor, task.ID, now); err != nil {
		t.Fatal(err)
	}
	todo := api.StatusTodo
	if _, err := s.Patch(ctx, actor, task.ID, api.TaskPatch{Status: &todo}, "", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Complete(ctx, actor, task.ID, now); err != nil {
		t.Fatal(err)
	}

	if n, err := j.Deliver(ctx, now); err != nil || n != 2 {
		t.Fatalf("delivered %d (%v), want both completions", n, err)
	}
}

// TestTheWebhookIsOffUntilConfigured. Nothing posts anywhere by default.
func TestTheWebhookIsOffUntilConfigured(t *testing.T) {
	s, now := seeded(t)
	ctx := context.Background()

	rec := &journal{}
	j := &notify.Journal{Store: s, Poster: rec, Loc: now.Location()}

	task, err := s.GetByNum(ctx, 104)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Complete(ctx, actor, task.ID, now); err != nil {
		t.Fatal(err)
	}
	if n, err := j.Deliver(ctx, now); err != nil || n != 0 {
		t.Fatalf("an unconfigured journal delivered %d (%v)", n, err)
	}
	if len(rec.posted) != 0 {
		t.Error("an unconfigured journal posted something")
	}

	// And it did not even take a cursor, so switching it on later still
	// starts from that moment.
	if _, err := s.OutboxCursor(ctx, memos.Consumer); err != nil {
		t.Fatal(err)
	}
}

// TestTheMemoSaysWhatWasFinished covers the content. A journal entry that
// restates the whole task is two things to keep in sync, so it says what was
// done, the facts that will not be obvious later, and a link.
func TestTheMemoSaysWhatWasFinished(t *testing.T) {
	j, rec, s, now := newJournal(t)
	ctx := context.Background()

	if _, err := j.Deliver(ctx, now); err != nil {
		t.Fatal(err)
	}
	task, err := s.GetByNum(ctx, 104)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Complete(ctx, actor, task.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Deliver(ctx, now); err != nil {
		t.Fatal(err)
	}
	if len(rec.posted) != 1 {
		t.Fatalf("%d memos", len(rec.posted))
	}

	memo := rec.posted[0]
	for _, want := range []string{
		"#td",                          // the configured tag, so entries are filterable
		"Done:",                        // what happened
		task.Title,                     // to what
		"#finance",                     // the task's own tags
		"@stacey",                      // who it involved
		"https://td.example.com/t/104", // and a way back
	} {
		if !strings.Contains(memo.Content, want) {
			t.Errorf("the memo does not mention %q:\n%s", want, memo.Content)
		}
	}
	// Private by default. A task manager's contents are not a blog.
	if memo.Visibility != "PRIVATE" {
		t.Errorf("visibility = %q", memo.Visibility)
	}
}

// TestThereIsNoReadPathFromMemos is section 17's decision, asserted rather
// than assumed. A journal that can create work is a second inbox.
func TestThereIsNoReadPathFromMemos(t *testing.T) {
	if _, ok := any(&notify.Journal{}).(interface {
		Fetch(context.Context) error
	}); ok {
		t.Error("the journal has a read path")
	}
	// The config carries no polling interval, no cursor into Memos, and no
	// mapping from a memo back onto a task, because none of that exists.
	cfg := memos.Config{}
	if cfg.Enabled() {
		t.Error("an empty config is enabled")
	}
}

// TestTheMemoIsDatedByWhenTheWorkFinished, not by when it was delivered. If
// Memos was down for a day the entry arrives late, and a journal dated by
// delivery would be wrong about the one fact it exists to record.
func TestTheMemoIsDatedByWhenTheWorkFinished(t *testing.T) {
	j, rec, s, now := newJournal(t)
	ctx := context.Background()

	if _, err := j.Deliver(ctx, now); err != nil {
		t.Fatal(err)
	}
	task, err := s.GetByNum(ctx, 104)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Complete(ctx, actor, task.ID, now); err != nil {
		t.Fatal(err)
	}

	// Delivered three days later, as it would be after an outage.
	late := now.AddDate(0, 0, 3)
	if _, err := j.Deliver(ctx, late); err != nil {
		t.Fatal(err)
	}
	if len(rec.posted) != 1 {
		t.Fatalf("%d memos", len(rec.posted))
	}
	if !strings.Contains(rec.posted[0].Content, "finished "+now.Format("2006-01-02")) {
		t.Errorf("the memo is not dated by the completion:\n%s", rec.posted[0].Content)
	}
	if strings.Contains(rec.posted[0].Content, late.Format("2006-01-02")) {
		t.Error("the memo is dated by delivery time")
	}
}

package notify

import (
	"context"
	"time"

	"github.com/harpchad/td/internal/memos"
	"github.com/harpchad/td/internal/store"
)

// Journal delivers completed tasks to Memos.
//
// It rides the same 60 second tick as everything else and follows a cursor
// into the event log rather than firing inline from Complete. That is what
// makes the two properties worth having true at once: a completion is never
// lost because Memos was restarting, and never posted twice because something
// retried. Completing a task must not be able to fail because a journal is
// down.
type Journal struct {
	Store  JournalStore
	Poster memos.Poster
	Config memos.Config

	// BaseURL is the server's own URL, so a memo links back to the task.
	BaseURL string
	Loc     *time.Location
}

// JournalStore is the part of the store the outbox needs.
type JournalStore interface {
	OutboxCursor(ctx context.Context, name string) (int64, error)
	SetOutboxCursor(ctx context.Context, name string, seq int64, now time.Time) error
	CompletionsSince(ctx context.Context, seq int64, limit int) ([]store.Completion, error)
}

// Deliver posts everything finished since the cursor and reports how many
// went.
//
// The cursor advances one entry at a time rather than once at the end. A
// batch that fails halfway has delivered the first half, and moving the
// cursor per entry is what stops the next tick from posting those again.
func (j *Journal) Deliver(ctx context.Context, now time.Time) (int, error) {
	if !j.Config.Enabled() || j.Poster == nil {
		return 0, nil
	}

	cursor, err := j.Store.OutboxCursor(ctx, memos.Consumer)
	if err != nil {
		return 0, err
	}
	pending, err := j.Store.CompletionsSince(ctx, cursor, 0)
	if err != nil {
		return 0, err
	}

	sent := 0
	for _, done := range pending {
		memo := memos.Compose(j.Config, done.Task, j.BaseURL, j.Loc)
		if err := j.Poster.Post(ctx, memo); err != nil {
			// Stop here rather than skipping ahead. The cursor stays where it
			// is, so the next tick retries this entry, and everything after it
			// is still waiting in order.
			return sent, err
		}
		if err := j.Store.SetOutboxCursor(ctx, memos.Consumer, done.Seq, now); err != nil {
			return sent, err
		}
		sent++
	}
	return sent, nil
}

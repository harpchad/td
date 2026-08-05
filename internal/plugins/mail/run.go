package mail

import (
	"context"
	"time"

	"github.com/harpchad/td/internal/sync"
)

// Poster is what a run needs from the td API. An interface so the tests never
// need a running server, and so nothing in this package holds a store.
type Poster interface {
	// Sync posts one batch to POST /api/v1/sync/mail.
	Sync(ctx context.Context, source string, req sync.Request) (sync.Result, error)
	// Captured returns the external ids td already holds for this source,
	// which is how the plugin works out what is new.
	Captured(ctx context.Context, source string) ([]string, error)
}

// Run captures every flagged message td has not already seen.
//
// The subtraction is the whole plugin. Everything else here is plumbing.
//
// A mirror re-posts its whole window every run and lets the server work out
// what changed, which is right when upstream owns the fields. Here the person
// owns them the moment the task exists, so a second post would overwrite the
// title they fixed and the status they set. Subtracting first means a message
// is captured once, in its original state, and is then left alone forever.
//
// It also means unflagging does nothing, which is correct: the flag was how
// you told td about the task, not where the task lives. And no gone list is
// ever sent, so nothing this plugin captured is ever marked as vanished.
func Run(ctx context.Context, c *Client, api Poster, now time.Time, loc *time.Location) (sync.Result, error) {
	var total sync.Result

	messages, err := c.Flagged(ctx)
	if err != nil {
		return total, err
	}

	known, err := api.Captured(ctx, Source)
	if err != nil {
		return total, err
	}
	seen := make(map[string]bool, len(known))
	for _, id := range known {
		seen[id] = true
	}

	// Subtract before translating: a mailbox with two hundred old flags and
	// one new one should do one message of work, not two hundred.
	fresh := make([]GraphMessage, 0, len(messages))
	for _, m := range messages {
		if !m.Flagged() || seen[m.ID] {
			continue
		}
		// A mailbox can return the same message twice across folder queries
		// when a folder list overlaps, and posting a duplicate in one batch
		// would fail the unique constraint rather than being ignored.
		seen[m.ID] = true
		fresh = append(fresh, m)
	}

	items := Translate(fresh, loc)
	if len(items) == 0 {
		total.Cursor = now.UTC().Format(time.RFC3339)
		return total, nil
	}

	cursor := now.UTC().Format(time.RFC3339)
	for start := 0; start < len(items); start += BatchSize {
		end := min(start+BatchSize, len(items))
		res, err := api.Sync(ctx, Source, sync.Request{
			Cursor: cursor, Items: items[start:end],
			// Gone is never sent. A message that is no longer flagged is a
			// flag somebody cleared, not a task that never happened.
		})
		if err != nil {
			return total, err
		}
		total.Created += res.Created
		total.Updated += res.Updated
		total.Unresolved = append(total.Unresolved, res.Unresolved...)
		total.Cursor = res.Cursor
	}
	return total, nil
}

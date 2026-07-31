package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/harpchad/td/internal/api"
)

// Completion is one finished task, paired with the event sequence that
// recorded it. The seq is what the outbox cursor advances to, so a consumer
// that dies mid-batch resumes at the right place rather than replaying the
// whole log or skipping the rest of the batch.
type Completion struct {
	Seq  int64
	At   string
	Task api.Task
}

// OutboxCursor returns how far a consumer has delivered. An unknown consumer
// starts at the newest event rather than at zero: switching a webhook on
// should not post a year of history to somebody's journal.
func (s *Store) OutboxCursor(ctx context.Context, name string) (int64, error) {
	var seq int64
	err := s.db.QueryRowContext(ctx, `SELECT seq FROM outbox_cursor WHERE name = ?`, name).Scan(&seq)
	if err == nil {
		return seq, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}

	var newest int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT coalesce(max(seq), 0) FROM event`).Scan(&newest); err != nil {
		return 0, err
	}
	return newest, s.SetOutboxCursor(ctx, name, newest, time.Time{})
}

// SetOutboxCursor records delivery progress.
func (s *Store) SetOutboxCursor(ctx context.Context, name string, seq int64, now time.Time) error {
	var stamp any
	if !now.IsZero() {
		stamp = now.UTC().Format(time.RFC3339)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO outbox_cursor (name, seq, updated_at) VALUES (?,?,?)
		 ON CONFLICT(name) DO UPDATE SET seq = excluded.seq, updated_at = excluded.updated_at`,
		name, seq, stamp)
	return err
}

// CompletionsSince returns tasks completed after seq, oldest first.
//
// It reads the event log rather than the task table, because "completed since
// you last looked" is a question about history and completed_at alone cannot
// answer it: a task completed, reopened, and completed again is two entries in
// the journal and one row in the table.
func (s *Store) CompletionsSince(ctx context.Context, seq int64, limit int) ([]Completion, error) {
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT seq, at, task_id FROM event
		 WHERE seq > ? AND kind = ? AND task_id IS NOT NULL
		 ORDER BY seq LIMIT ?`, seq, api.KindTaskComplete, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type row struct {
		seq    int64
		at     string
		taskID string
	}
	var pending []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.seq, &r.at, &r.taskID); err != nil {
			return nil, err
		}
		pending = append(pending, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]Completion, 0, len(pending))
	for _, r := range pending {
		task, err := s.Get(ctx, r.taskID)
		if errors.Is(err, ErrNotFound) {
			// Nothing hard-deletes a task, so this should not happen. If it
			// does, the cursor still has to move past it or the outbox jams
			// on one row forever.
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, Completion{Seq: r.seq, At: r.at, Task: task})
	}
	return out, nil
}

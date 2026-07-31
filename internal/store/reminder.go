package store

import (
	"context"
	"time"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/query"
)

// DueForReminder returns the tasks a reminder could still fire for: open,
// with a due date, and never notified.
//
// Only due_at fires a push in v1. No morning digest, no waiting-on nags, no
// inbox threshold. The scheduler decides which of these are actually due;
// this is only the set worth looking at, which stays small because
// notified_at removes a task from it permanently for that due value.
func (s *Store) DueForReminder(ctx context.Context) ([]api.Task, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+taskColumns+` FROM task t
		WHERE t.status NOT IN ('done','dropped')
		  AND t.due_at IS NOT NULL
		  AND t.notified_at IS NULL
		  AND t.notify <> 'off'
		ORDER BY t.due_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []api.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, s.hydrate(ctx, out)
}

// MatchingIDs evaluates a filter and returns the ids it selects. The notify
// policy is a filter query, so resolving notify=auto is the same code path
// the filter bar uses.
func (s *Store) MatchingIDs(ctx context.Context, filter string, now time.Time) (map[string]bool, error) {
	node, err := query.ParseAt(filter, now.In(s.loc))
	if err != nil {
		return nil, err
	}
	where, err := buildWhere(node, newFilterContext(now, s.loc))
	if err != nil {
		return nil, err
	}

	rows, err := s.db.QueryContext(ctx, `SELECT t.id FROM task t WHERE `+where.sql, where.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// MarkNotified stamps notified_at. It writes no event: a reminder going out
// is not a change the user made, and undo has no business reversing one.
func (s *Store) MarkNotified(ctx context.Context, taskID string, now time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE task SET notified_at = ? WHERE id = ?`,
		now.UTC().Format(time.RFC3339), taskID)
	return err
}

// Snooze hides a task until an instant. It does not change the status, and it
// refuses on a done or dropped task.
func (s *Store) Snooze(ctx context.Context, actor, id string, until time.Time, now time.Time) (api.Task, error) {
	stamp := until.UTC().Format(time.RFC3339)
	return s.Patch(ctx, actor, id, api.TaskPatch{
		SnoozeUntil: &stamp,
		Presence:    map[string]bool{"snooze_until": true},
	}, "", now)
}

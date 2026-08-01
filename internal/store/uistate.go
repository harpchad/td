package store

import (
	"context"
	"database/sql"
	"errors"
)

// CollapsedTasks returns the ids of every folded parent.
//
// Fold state lives in ui_state rather than on the task row because it is view
// state: it generates no events, appears in no export, and syncs to no
// plugin. Keeping it off api.Task is what makes that structural rather than a
// rule someone has to remember.
func (s *Store) CollapsedTasks(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT task_id FROM ui_state WHERE collapsed = 1 ORDER BY task_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SetCollapsed folds or unfolds one task. It writes no event: an export or a
// sync must not be able to see that a row was folded on somebody's screen.
func (s *Store) SetCollapsed(ctx context.Context, taskID string, collapsed bool) error {
	if _, err := s.Get(ctx, taskID); err != nil {
		return err
	}
	if !collapsed {
		_, err := s.db.ExecContext(ctx, `DELETE FROM ui_state WHERE task_id = ?`, taskID)
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO ui_state (task_id, collapsed) VALUES (?, 1)
		 ON CONFLICT(task_id) DO UPDATE SET collapsed = 1`, taskID)
	return err
}

// CurrentFilter returns the filter last read, and whether anybody has ever
// chosen one.
//
// The bool is the whole point. A stored empty string is somebody clearing the
// box on purpose and has to be honoured, while no row at all is a first visit
// and opens on the saved filter in slot 1. Collapsing those two into "" would
// make clearing the filter impossible, which is the bug this was written for.
func (s *Store) CurrentFilter(ctx context.Context) (string, bool, error) {
	var filter string
	err := s.db.QueryRowContext(ctx, `SELECT filter FROM view_state WHERE id = 1`).Scan(&filter)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return filter, true, nil
}

// SetCurrentFilter remembers what is being read. Like fold state it writes no
// event and appears in no export: where you happen to be looking is not a
// thing that happened to your tasks.
func (s *Store) SetCurrentFilter(ctx context.Context, filter string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO view_state (id, filter) VALUES (1, ?)
		 ON CONFLICT(id) DO UPDATE SET filter = excluded.filter`, filter)
	return err
}

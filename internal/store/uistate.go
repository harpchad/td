package store

import "context"

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

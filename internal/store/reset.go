package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// ResetCounts is what a reset removed.
type ResetCounts struct {
	Tasks       int
	Events      int
	Attachments int
}

// ResetTasks deletes tasks and everything hanging off them.
//
// This is the one hard delete in td, and it exists under protest. Section 6 is
// explicit that nothing is ever hard deleted, because the activity feed is
// supposed to show what you abandoned, and every ordinary path honours that:
// dropping sets a status, a vanished upstream item sets a flag. But testing a
// sync means running it, looking at the result, and running it again from a
// known state, and the alternative was deleting the database file, which also
// destroys the account, the tokens, and the Microsoft connection somebody just
// signed in for.
//
// It is reachable only from the `tdd` command line, never over HTTP. A token
// cannot get here, which is the property that makes an operator-only wrecking
// tool acceptable at all.
//
// What it keeps, on purpose: the account, sessions and tokens; people, groups
// and their identity mappings; saved filters; and plugin configuration and
// credentials. Identity mappings especially. They are slow to rebuild by hand
// and they are exactly what you do not want to redo between two runs of the
// thing you are testing.
func (s *Store) ResetTasks(ctx context.Context, source string, now time.Time) (ResetCounts, error) {
	source = strings.TrimSpace(strings.ToLower(source))

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ResetCounts{}, err
	}
	defer func() { _ = tx.Rollback() }()

	// The ids first, so every dependent delete works from the same set and a
	// task created between two statements cannot be half removed.
	ids, err := taskIDsToReset(ctx, tx, source)
	if err != nil {
		return ResetCounts{}, err
	}

	// A mirror that is gone has no last run worth reporting, and leaving the
	// old counts up would describe a state that no longer exists. This runs
	// whether or not there was anything to delete: "reset this source" means
	// forget it, and a source whose tasks were already gone still has a stale
	// result sitting on the settings page.
	if source != "" && source != "local" {
		if _, err := tx.ExecContext(ctx,
			`UPDATE plugin_config
			 SET last_run_at = NULL, last_result = NULL, last_error = NULL,
			     last_unresolved = NULL, updated_at = ?
			 WHERE name = ?`, now.UTC().Format(time.RFC3339), source); err != nil {
			return ResetCounts{}, err
		}
	}

	out := ResetCounts{Tasks: len(ids)}
	if len(ids) == 0 {
		return out, tx.Commit()
	}
	in := "(" + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + ")"

	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM attachment WHERE task_id IN `+in, ids...).Scan(&out.Attachments); err != nil {
		return ResetCounts{}, err
	}
	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM event WHERE task_id IN `+in, ids...).Scan(&out.Events); err != nil {
		return ResetCounts{}, err
	}

	// A series points at the instance it most recently made. Clearing that and
	// the walk marker lets it materialize a fresh one on the next tick rather
	// than holding a reference to a task that no longer exists.
	if _, err := tx.ExecContext(ctx,
		`UPDATE series SET current_task_id = NULL, last_fired_at = NULL
		 WHERE current_task_id IN `+in, ids...); err != nil {
		return ResetCounts{}, err
	}

	// Children before parents, which the parent_id reference needs, and every
	// dependent row before the task itself.
	for _, stmt := range []string{
		`DELETE FROM task_tag WHERE task_id IN ` + in,
		`DELETE FROM task_person WHERE task_id IN ` + in,
		`DELETE FROM task_group WHERE task_id IN ` + in,
		`DELETE FROM attachment WHERE task_id IN ` + in,
		`DELETE FROM ui_state WHERE task_id IN ` + in,
		`DELETE FROM task_unseen WHERE task_id IN ` + in,
		`DELETE FROM event WHERE task_id IN ` + in,
		`UPDATE task SET parent_id = NULL WHERE parent_id IN ` + in,
		// The FTS index is kept in step by an AFTER DELETE trigger, so the
		// task row goes last and the index follows it.
		`DELETE FROM task WHERE id IN ` + in,
	} {
		if _, err := tx.ExecContext(ctx, stmt, ids...); err != nil {
			return ResetCounts{}, err
		}
	}

	// The outbox cursor points into an event log that just lost rows. Left
	// alone it would be a sequence number past anything that exists, which is
	// harmless, but resetting it to the newest surviving event keeps "start
	// from now" true rather than accidentally true.
	if _, err := tx.ExecContext(ctx,
		`UPDATE outbox_cursor SET seq = coalesce((SELECT max(seq) FROM event), 0)`); err != nil {
		return ResetCounts{}, err
	}

	return out, tx.Commit()
}

// taskIDsToReset collects the ids in a scope of its own.
//
// Its own function so the cursor closes before anything else runs on this
// transaction. The pool is one connection, so an open cursor and a second
// statement on the same transaction are a deadlock rather than a slowdown.
func taskIDsToReset(ctx context.Context, tx *sql.Tx, source string) ([]any, error) {
	query, args := `SELECT id FROM task`, []any{}
	if source != "" {
		query += ` WHERE source = ?`
		args = append(args, source)
	}

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var ids []any
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// CountTasks reports how many tasks a reset would remove, so the command can
// say what it is about to do before it does it.
func (s *Store) CountTasks(ctx context.Context, source string) (int, error) {
	source = strings.TrimSpace(strings.ToLower(source))
	query, args := `SELECT count(*) FROM task`, []any{}
	if source != "" {
		query += ` WHERE source = ?`
		args = append(args, source)
	}
	var n int
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&n)
	return n, err
}

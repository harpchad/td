package store

import (
	"context"
	"database/sql"
	"strings"
)

// ActorMe is the event-log actor for the owner at a keyboard. Every other
// actor is a machine acting on their behalf: mcp:<name> for an agent,
// plugin:<source> for a sync or capture plugin.
const ActorMe = "me"

// arrivedUnseen decides whether a task created by this actor is news.
//
// The test is against the owner rather than for a list of machines, so an
// actor nobody has thought of yet errs towards being marked. A new integration
// that quietly files tasks you never see is the failure this exists to catch.
func arrivedUnseen(actor string) bool { return strings.TrimSpace(actor) != ActorMe }

// markUnseen records that a task arrived on its own.
func markUnseen(ctx context.Context, tx *sql.Tx, taskID string) error {
	_, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO task_unseen (task_id) VALUES (?)`, taskID)
	return err
}

// clearUnseen drops the mark inside an open transaction.
func clearUnseen(ctx context.Context, tx *sql.Tx, taskID string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM task_unseen WHERE task_id = ?`, taskID)
	return err
}

// MarkSeen clears the new mark on a task. It is what opening a task does, and
// it is deliberately not what reading one over the API does: an agent calling
// get_task is not the owner looking at it.
//
// Clearing a task that was never marked is not an error. The state asked for
// is the state that results either way.
func (s *Store) MarkSeen(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM task_unseen WHERE task_id = ?`, id)
	return err
}

package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/harpchad/td/internal/api"
)

// Attachments returns a task's attachments, oldest first.
func (s *Store) Attachments(ctx context.Context, taskID string) ([]api.Attachment, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, task_id, sha256, coalesce(filename, ''), coalesce(bytes, 0),
		        coalesce(mime, ''), created_at
		 FROM attachment WHERE task_id = ? ORDER BY created_at, id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []api.Attachment{}
	for rows.Next() {
		var a api.Attachment
		if err := rows.Scan(&a.ID, &a.TaskID, &a.SHA256, &a.Filename,
			&a.Bytes, &a.Mime, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Attachment returns one attachment by id.
func (s *Store) Attachment(ctx context.Context, id string) (api.Attachment, error) {
	var a api.Attachment
	err := s.db.QueryRowContext(ctx,
		`SELECT id, task_id, sha256, coalesce(filename, ''), coalesce(bytes, 0),
		        coalesce(mime, ''), created_at
		 FROM attachment WHERE id = ?`, id).
		Scan(&a.ID, &a.TaskID, &a.SHA256, &a.Filename, &a.Bytes, &a.Mime, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return api.Attachment{}, ErrNotFound
	}
	return a, err
}

// AddAttachment records a blob against a task. The bytes are already in the
// blob store by the time this runs; this is the row that gives them a name
// and an owner.
func (s *Store) AddAttachment(ctx context.Context, actor string, a api.Attachment, now time.Time) (api.Attachment, error) {
	if _, err := s.Get(ctx, a.TaskID); err != nil {
		return api.Attachment{}, err
	}
	if a.ID == "" {
		a.ID = NewID()
	}
	a.CreatedAt = now.UTC().Format(time.RFC3339)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return api.Attachment{}, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `INSERT INTO attachment
		(id, task_id, sha256, filename, bytes, mime, created_at)
		VALUES (?,?,?,?,?,?,?)`,
		a.ID, a.TaskID, a.SHA256, a.Filename, a.Bytes, a.Mime, a.CreatedAt); err != nil {
		return api.Attachment{}, err
	}
	if err := appendEvent(ctx, tx, now, actor, a.TaskID, api.KindTaskAttached, api.Patch{
		Meta: map[string]any{
			"attachment": a.ID,
			"filename":   a.Filename,
			"bytes":      a.Bytes,
		},
	}); err != nil {
		return api.Attachment{}, err
	}
	if err := tx.Commit(); err != nil {
		return api.Attachment{}, err
	}
	return a, nil
}

// RemoveAttachment detaches a file from a task. The blob stays on disk: it
// may be referenced by another task, and the weekly sweep is what actually
// deletes bytes.
func (s *Store) RemoveAttachment(ctx context.Context, actor, id string, now time.Time) error {
	a, err := s.Attachment(ctx, id)
	if err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM attachment WHERE id = ?`, id); err != nil {
		return err
	}
	if err := appendEvent(ctx, tx, now, actor, a.TaskID, api.KindTaskDetached, api.Patch{
		Meta: map[string]any{"attachment": a.ID, "filename": a.Filename},
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// ReferencedBlobs is every digest still pointed at by a row. It is the keep
// set for the weekly sweep, so a bug here deletes live files: it must be the
// whole table with no filter on task status. A dropped task is not gone, and
// its attachment has to survive an undo.
func (s *Store) ReferencedBlobs(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT sha256 FROM attachment`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	keep := map[string]bool{}
	for rows.Next() {
		var digest string
		if err := rows.Scan(&digest); err != nil {
			return nil, err
		}
		keep[digest] = true
	}
	return keep, rows.Err()
}

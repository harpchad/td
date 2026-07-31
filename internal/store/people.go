package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/harpchad/td/internal/api"
)

// People returns every person, ordered by name.
func (s *Store) People(ctx context.Context) ([]api.Person, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, handle, name, coalesce(email, ''), notes FROM person ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []api.Person{}
	for rows.Next() {
		var p api.Person
		if err := rows.Scan(&p.ID, &p.Handle, &p.Name, &p.Email, &p.Notes); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PersonByHandle looks a person up by the @handle the filter grammar uses.
func (s *Store) PersonByHandle(ctx context.Context, handle string) (api.Person, error) {
	var p api.Person
	err := s.db.QueryRowContext(ctx,
		`SELECT id, handle, name, coalesce(email, ''), notes FROM person WHERE lower(handle) = lower(?)`,
		handle).Scan(&p.ID, &p.Handle, &p.Name, &p.Email, &p.Notes)
	if errors.Is(err, sql.ErrNoRows) {
		return api.Person{}, ErrNotFound
	}
	return p, err
}

// SavedFilters returns the saved filters bound to the number keys, ordered by
// slot. They live server-side so they follow you between clients.
func (s *Store) SavedFilters(ctx context.Context) ([]api.SavedFilter, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, coalesce(slot, 0), name, query FROM saved_filter ORDER BY slot`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []api.SavedFilter{}
	for rows.Next() {
		var f api.SavedFilter
		if err := rows.Scan(&f.ID, &f.Slot, &f.Name, &f.Query); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// PutSavedFilter creates or replaces the filter bound to a slot.
func (s *Store) PutSavedFilter(ctx context.Context, f api.SavedFilter) (api.SavedFilter, error) {
	if f.ID == "" {
		f.ID = NewID()
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM saved_filter WHERE slot = ?`, f.Slot); err != nil {
		return api.SavedFilter{}, err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO saved_filter (id, slot, name, query) VALUES (?,?,?,?)`,
		f.ID, f.Slot, f.Name, f.Query)
	return f, err
}

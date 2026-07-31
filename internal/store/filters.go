package store

import (
	"context"

	"github.com/harpchad/td/internal/api"
)

// SavedFilters returns the filters bound to the number keys, ordered by slot.
// They live server-side so they follow you between clients.
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

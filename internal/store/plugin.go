package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// PluginConfig is one sync plugin as the server holds it.
//
// Settings and Credential are raw JSON because the store has no business
// knowing what a Planner plan id is. Each plugin unmarshals its own shape,
// which is what keeps adding a second plugin from touching this file.
type PluginConfig struct {
	Name     string          `json:"name"`
	Enabled  bool            `json:"enabled"`
	Settings json.RawMessage `json:"settings"`
	// Credential never appears in any API response and never leaves the
	// database. It is separate from Settings so that stays true structurally
	// rather than by everybody remembering to strip a field.
	Credential json.RawMessage `json:"-"`

	IntervalMinutes int `json:"interval_minutes"`

	LastRunAt  *string `json:"last_run_at,omitempty"`
	LastResult *string `json:"last_result,omitempty"`
	LastError  *string `json:"last_error,omitempty"`
	// LastUnresolved is the identities the last run would not guess at, so
	// the settings page can offer to answer them.
	LastUnresolved json.RawMessage `json:"last_unresolved,omitempty"`
	UpdatedAt      string          `json:"updated_at"`
}

// Connected reports whether a credential has been stored, without saying
// anything about what it is. It is what the settings page shows.
func (p PluginConfig) Connected() bool {
	return len(p.Credential) > 0 && string(p.Credential) != "null"
}

// PluginConfigByName returns one plugin's configuration, or a zero-valued one
// that has never been saved. A plugin nobody has configured is not an error:
// it is the ordinary state before first use.
func (s *Store) PluginConfigByName(ctx context.Context, name string) (PluginConfig, error) {
	out := PluginConfig{Name: name, Settings: json.RawMessage("{}"), IntervalMinutes: 15}

	var settings string
	var credential, unresolved sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT enabled, settings, credential, interval_minutes,
		        last_run_at, last_result, last_error, last_unresolved, updated_at
		 FROM plugin_config WHERE name = ?`, name).
		Scan(&out.Enabled, &settings, &credential, &out.IntervalMinutes,
			&out.LastRunAt, &out.LastResult, &out.LastError, &unresolved, &out.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return PluginConfig{}, err
	}
	out.Settings = json.RawMessage(settings)
	if credential.Valid {
		out.Credential = json.RawMessage(credential.String)
	}
	if unresolved.Valid {
		out.LastUnresolved = json.RawMessage(unresolved.String)
	}
	return out, nil
}

// SavePluginSettings writes the parts a person edits. It deliberately does not
// touch the credential: a settings form that could blank a stored refresh
// token by omitting a field is a settings form that eventually will.
func (s *Store) SavePluginSettings(ctx context.Context, name string, enabled bool, settings json.RawMessage, interval int, now time.Time) error {
	if len(settings) == 0 {
		settings = json.RawMessage("{}")
	}
	if interval <= 0 {
		interval = 15
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO plugin_config
		(name, enabled, settings, interval_minutes, updated_at) VALUES (?,?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET
			enabled = excluded.enabled,
			settings = excluded.settings,
			interval_minutes = excluded.interval_minutes,
			updated_at = excluded.updated_at`,
		name, boolInt(enabled), string(settings), interval, now.UTC().Format(time.RFC3339))
	return err
}

// SavePluginCredential stores what a connect flow produced. Passing nil
// disconnects, which is the only way a credential is ever removed.
func (s *Store) SavePluginCredential(ctx context.Context, name string, credential json.RawMessage, now time.Time) error {
	var value any
	if len(credential) > 0 {
		value = string(credential)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO plugin_config
		(name, credential, updated_at) VALUES (?,?,?)
		ON CONFLICT(name) DO UPDATE SET
			credential = excluded.credential,
			updated_at = excluded.updated_at`,
		name, value, now.UTC().Format(time.RFC3339))
	return err
}

// RecordPluginRun stores what the last run did.
//
// A successful run clears the error rather than leaving the previous one
// standing, so what the settings page shows always describes now.
func (s *Store) RecordPluginRun(ctx context.Context, name, result string, unresolved json.RawMessage, runErr error, now time.Time) error {
	var message any
	if runErr != nil {
		message = runErr.Error()
	}
	var pending any
	if len(unresolved) > 0 && string(unresolved) != "null" {
		pending = string(unresolved)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO plugin_config
		(name, last_run_at, last_result, last_error, last_unresolved, updated_at)
		VALUES (?,?,?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET
			last_run_at = excluded.last_run_at,
			last_result = excluded.last_result,
			last_error = excluded.last_error,
			last_unresolved = excluded.last_unresolved,
			updated_at = excluded.updated_at`,
		name, now.UTC().Format(time.RFC3339), result, message, pending,
		now.UTC().Format(time.RFC3339))
	return err
}

// DuePlugins returns the enabled plugins whose interval has elapsed.
func (s *Store) DuePlugins(ctx context.Context, now time.Time) ([]PluginConfig, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT name FROM plugin_config WHERE enabled = 1 ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]PluginConfig, 0, len(names))
	for _, name := range names {
		cfg, err := s.PluginConfigByName(ctx, name)
		if err != nil {
			return nil, err
		}
		if !cfg.Connected() {
			// Enabled but never connected. The settings page says so; the
			// scheduler has nothing to do about it and must not spin trying.
			continue
		}
		if cfg.LastRunAt != nil {
			last, err := time.Parse(time.RFC3339, *cfg.LastRunAt)
			if err == nil && now.Sub(last) < time.Duration(cfg.IntervalMinutes)*time.Minute {
				continue
			}
		}
		out = append(out, cfg)
	}
	return out, nil
}

package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/auth"
)

// SessionLifetime is how long a browser session lasts. It refreshes on use,
// so thirty days of inactivity is what ends it.
const SessionLifetime = 30 * 24 * time.Hour

// sessionRefreshAfter is how stale a session has to be before use extends it.
// Writing on every request would mean a database write per page view for no
// benefit.
const sessionRefreshAfter = time.Hour

// Session is a logged-in browser.
type Session struct {
	ID        string
	AccountID string
	ExpiresAt string
}

// CreateSession issues a session and returns the opaque secret for the
// cookie. Only its hash is stored, so a database dump cannot be replayed as a
// logged-in browser.
func (s *Store) CreateSession(ctx context.Context, accountID, ip, userAgent string, now time.Time) (secret string, sess Session, err error) {
	secret, hash, err := auth.NewSessionSecret()
	if err != nil {
		return "", Session{}, err
	}

	sess = Session{
		ID:        NewID(),
		AccountID: accountID,
		ExpiresAt: now.Add(SessionLifetime).UTC().Format(time.RFC3339),
	}
	stamp := now.UTC().Format(time.RFC3339)
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO session (id, account_id, token_hash, created_at, expires_at, last_seen_at, ip, user_agent)
		 VALUES (?,?,?,?,?,?,?,?)`,
		sess.ID, accountID, hash, stamp, sess.ExpiresAt, stamp, ip, userAgent)
	if err != nil {
		return "", Session{}, err
	}
	return secret, sess, nil
}

// LookupSession resolves a cookie value to a live session, sliding its expiry
// forward. An expired session is deleted rather than reported, so the caller
// cannot tell an expired one from a forged one.
func (s *Store) LookupSession(ctx context.Context, secret string, now time.Time) (Session, error) {
	hash := auth.HashSecret(secret)

	var sess Session
	var lastSeen string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, account_id, expires_at, last_seen_at FROM session WHERE token_hash = ?`, hash).
		Scan(&sess.ID, &sess.AccountID, &sess.ExpiresAt, &lastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, err
	}

	expires, err := time.Parse(time.RFC3339, sess.ExpiresAt)
	if err != nil || !now.Before(expires) {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM session WHERE id = ?`, sess.ID)
		return Session{}, ErrNotFound
	}

	if seen, err := time.Parse(time.RFC3339, lastSeen); err == nil && now.Sub(seen) > sessionRefreshAfter {
		sess.ExpiresAt = now.Add(SessionLifetime).UTC().Format(time.RFC3339)
		_, err = s.db.ExecContext(ctx,
			`UPDATE session SET last_seen_at = ?, expires_at = ? WHERE id = ?`,
			now.UTC().Format(time.RFC3339), sess.ExpiresAt, sess.ID)
		if err != nil {
			return Session{}, err
		}
	}
	return sess, nil
}

// DeleteSession ends one session.
func (s *Store) DeleteSession(ctx context.Context, secret string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM session WHERE token_hash = ?`, auth.HashSecret(secret))
	return err
}

// PurgeExpiredSessions drops sessions that are past their expiry.
func (s *Store) PurgeExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM session WHERE expires_at <= ?`, now.UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CreateToken mints an API token and returns it with its secret populated.
// That is the only time the secret exists outside the caller's hands.
func (s *Store) CreateToken(ctx context.Context, name, actor string, scopes []string, now time.Time) (api.Token, error) {
	if strings.TrimSpace(name) == "" {
		return api.Token{}, errors.New("a token needs a name")
	}
	if err := ValidateActor(actor); err != nil {
		return api.Token{}, err
	}
	if err := ValidateScopes(scopes); err != nil {
		return api.Token{}, err
	}

	secret, hash, prefix, err := auth.NewToken()
	if err != nil {
		return api.Token{}, err
	}

	tok := api.Token{
		ID:        NewID(),
		Name:      name,
		Prefix:    prefix,
		Scopes:    scopes,
		Actor:     actor,
		CreatedAt: now.UTC().Format(time.RFC3339),
		Secret:    secret,
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return api.Token{}, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO api_token (id, name, token_hash, prefix, scopes, actor, created_at)
		 VALUES (?,?,?,?,?,?,?)`,
		tok.ID, tok.Name, hash, tok.Prefix, strings.Join(scopes, ","), actor, tok.CreatedAt); err != nil {
		return api.Token{}, err
	}
	if err := appendEvent(ctx, tx, now, "me", "", api.KindAuthTokenCreated,
		api.Patch{Meta: map[string]any{"name": name, "prefix": prefix, "scopes": scopes, "actor": actor}}); err != nil {
		return api.Token{}, err
	}
	if err := tx.Commit(); err != nil {
		return api.Token{}, err
	}
	return tok, nil
}

// LookupToken resolves a bearer secret to a live token and stamps its
// last-used time. A revoked token is not found.
func (s *Store) LookupToken(ctx context.Context, secret string, now time.Time) (api.Token, error) {
	var tok api.Token
	var scopes string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, prefix, scopes, actor, created_at, last_used_at, revoked_at
		 FROM api_token WHERE token_hash = ? AND revoked_at IS NULL`,
		auth.HashSecret(secret)).
		Scan(&tok.ID, &tok.Name, &tok.Prefix, &scopes, &tok.Actor,
			&tok.CreatedAt, &tok.LastUsedAt, &tok.RevokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return api.Token{}, ErrNotFound
	}
	if err != nil {
		return api.Token{}, err
	}
	tok.Scopes = splitScopes(scopes)

	stamp := now.UTC().Format(time.RFC3339)
	if _, err := s.db.ExecContext(ctx,
		`UPDATE api_token SET last_used_at = ? WHERE id = ?`, stamp, tok.ID); err != nil {
		return api.Token{}, err
	}
	tok.LastUsedAt = &stamp
	return tok, nil
}

// Tokens lists every token, revoked ones included, for the settings page.
// Secrets are never populated.
func (s *Store) Tokens(ctx context.Context) ([]api.Token, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, prefix, scopes, actor, created_at, last_used_at, revoked_at
		 FROM api_token ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []api.Token{}
	for rows.Next() {
		var tok api.Token
		var scopes string
		if err := rows.Scan(&tok.ID, &tok.Name, &tok.Prefix, &scopes, &tok.Actor,
			&tok.CreatedAt, &tok.LastUsedAt, &tok.RevokedAt); err != nil {
			return nil, err
		}
		tok.Scopes = splitScopes(scopes)
		out = append(out, tok)
	}
	return out, rows.Err()
}

// RevokeToken marks a token dead. It is not deleted, so the settings page can
// still show that it existed and the event log still refers to it.
func (s *Store) RevokeToken(ctx context.Context, id string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx,
		`UPDATE api_token SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`,
		now.UTC().Format(time.RFC3339), id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	if err := appendEvent(ctx, tx, now, "me", "", api.KindAuthTokenRevoked,
		api.Patch{Meta: map[string]any{"token_id": id}}); err != nil {
		return err
	}
	return tx.Commit()
}

func splitScopes(s string) []string {
	if s == "" {
		return []string{}
	}
	return strings.Split(s, ",")
}

// ValidateActor checks the shape the event log expects. Undo is scoped by
// actor, so a token that could claim an arbitrary string could reach into
// another actor's history.
func ValidateActor(actor string) error {
	if actor == "me" {
		return nil
	}
	prefix, name, ok := strings.Cut(actor, ":")
	if !ok || name == "" || (prefix != "mcp" && prefix != "plugin") {
		return errors.New(`actor must be "me", "mcp:<name>", or "plugin:<name>"`)
	}
	if strings.ContainsAny(name, " \t:") {
		return errors.New("actor name cannot contain spaces or colons")
	}
	return nil
}

// ValidateScopes checks each scope against the closed set.
func ValidateScopes(scopes []string) error {
	if len(scopes) == 0 {
		return errors.New("a token needs at least one scope")
	}
	for _, sc := range scopes {
		switch sc {
		case api.ScopeRead, api.ScopeWrite, api.ScopeCapture:
			continue
		}
		if strings.HasPrefix(sc, api.ScopeSyncPrefix) && len(sc) > len(api.ScopeSyncPrefix) {
			continue
		}
		return errors.New("unknown scope " + sc)
	}
	return nil
}

// RateLimitLogin records an attempt from ip and reports whether the caller
// has exceeded the allowance in the window. Old rows are pruned on the way
// through, so a sustained attempt does not grow the table without bound.
func (s *Store) RateLimitLogin(ctx context.Context, ip string, limit int, window time.Duration, now time.Time) (allowed bool, err error) {
	cutoff := now.Add(-window).UTC().Format(time.RFC3339)
	if _, err := s.db.ExecContext(ctx, `DELETE FROM login_attempt WHERE at < ?`, cutoff); err != nil {
		return false, err
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO login_attempt (id, at, ip) VALUES (?,?,?)`,
		NewID(), now.UTC().Format(time.RFC3339), ip); err != nil {
		return false, err
	}

	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM login_attempt WHERE ip = ? AND at >= ?`, ip, cutoff).Scan(&n); err != nil {
		return false, err
	}
	return n <= limit, nil
}

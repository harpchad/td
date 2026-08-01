package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/harpchad/td/internal/api"
	"github.com/harpchad/td/internal/auth"
	"github.com/harpchad/td/internal/oauth"
)

// Codes, refresh tokens, and client secrets are stored as their SHA-256,
// never in the clear, the same rule the session and token tables follow. They
// are high entropy random strings rather than passwords, so a password hash
// would only slow verification down; there is nothing to brute force. A
// database dump contains no usable credential, which is the property that
// matters.
func hashSecret(secret string) string { return auth.HashSecret(secret) }

// newSecret returns 256 bits of randomness in the same alphabet the session
// and API token secrets use, so everything a person might paste looks alike.
func newSecret() (string, error) {
	secret, _, err := auth.NewSessionSecret()
	return secret, err
}

// OAuthClient is a registered client.
type OAuthClient struct {
	ID           string   `json:"client_id"`
	Name         string   `json:"client_name"`
	RedirectURIs []string `json:"redirect_uris"`
	Scopes       []string `json:"scopes"`
	// Source is dcr or cimd. A CIMD client id is an https URL and its
	// metadata is refetched rather than trusted from the row forever.
	Source     string  `json:"source"`
	CreatedAt  string  `json:"created_at"`
	LastUsedAt *string `json:"last_used_at,omitempty"`
	// Public reports whether the client holds a secret. Public clients rely
	// on PKCE alone, which is what OAuth 2.1 expects of anything that cannot
	// keep one.
	Public bool `json:"public"`
}

// AllowsRedirect reports whether uri is one of the registered ones.
//
// Exact string comparison. A prefix or a hostname match is how open
// redirectors happen, and an authorization code delivered to an attacker's
// URL is the whole game.
func (c OAuthClient) AllowsRedirect(uri string) bool {
	for _, registered := range c.RedirectURIs {
		if registered == uri {
			return true
		}
	}
	return false
}

// RegisterClient stores a client and returns it with the secret, which is
// visible exactly once.
func (s *Store) RegisterClient(ctx context.Context, in OAuthClient, secret string, now time.Time) (OAuthClient, error) {
	if in.ID == "" {
		in.ID = NewID()
	}
	if strings.TrimSpace(in.Name) == "" {
		in.Name = "an unnamed client"
	}
	if len(in.RedirectURIs) == 0 {
		return OAuthClient{}, &api.Error{
			Code: api.ErrBadRequest, Message: "at least one redirect_uri is required",
		}
	}
	if in.Source == "" {
		in.Source = "dcr"
	}
	in.CreatedAt = now.UTC().Format(time.RFC3339)

	uris, err := json.Marshal(in.RedirectURIs)
	if err != nil {
		return OAuthClient{}, err
	}
	var hash any
	if secret != "" {
		hash = hashSecret(secret)
	}
	in.Public = secret == ""

	if _, err := s.db.ExecContext(ctx, `INSERT INTO oauth_client
		(id, secret_hash, name, redirect_uris, scopes, source, created_at)
		VALUES (?,?,?,?,?,?,?)`,
		in.ID, hash, in.Name, string(uris), strings.Join(in.Scopes, " "),
		in.Source, in.CreatedAt); err != nil {
		return OAuthClient{}, err
	}
	return in, nil
}

// OAuthClientByID returns one client.
func (s *Store) OAuthClientByID(ctx context.Context, id string) (OAuthClient, error) {
	var (
		out    OAuthClient
		hash   sql.NullString
		uris   string
		scopes string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, secret_hash, name, redirect_uris, scopes, source, created_at, last_used_at
		 FROM oauth_client WHERE id = ?`, id).
		Scan(&out.ID, &hash, &out.Name, &uris, &scopes, &out.Source, &out.CreatedAt, &out.LastUsedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return OAuthClient{}, ErrNotFound
	}
	if err != nil {
		return OAuthClient{}, err
	}
	if err := json.Unmarshal([]byte(uris), &out.RedirectURIs); err != nil {
		return OAuthClient{}, err
	}
	out.Scopes = strings.Fields(scopes)
	out.Public = !hash.Valid
	return out, nil
}

// AuthenticateClient checks a client secret. A public client authenticates by
// PKCE alone, which is what OAuth 2.1 expects of anything that cannot keep a
// secret, so an empty secret is correct for one and wrong for the other.
func (s *Store) AuthenticateClient(ctx context.Context, id, secret string) (OAuthClient, error) {
	client, err := s.OAuthClientByID(ctx, id)
	if err != nil {
		return OAuthClient{}, err
	}

	var stored sql.NullString
	if err := s.db.QueryRowContext(ctx,
		`SELECT secret_hash FROM oauth_client WHERE id = ?`, id).Scan(&stored); err != nil {
		return OAuthClient{}, err
	}
	if !stored.Valid {
		if secret != "" {
			return OAuthClient{}, &api.Error{
				Code: api.ErrUnauthorized, Message: "this client has no secret",
			}
		}
		return client, nil
	}
	if hashSecret(secret) != stored.String {
		return OAuthClient{}, &api.Error{
			Code: api.ErrUnauthorized, Message: "client authentication failed",
		}
	}
	return client, nil
}

// AuthCode is an issued authorization code.
type AuthCode struct {
	ClientID    string
	RedirectURI string
	Scopes      []string
	Resource    string
	Challenge   string
	ExpiresAt   time.Time
}

// CodeLifetime is how long an authorization code lives. Short on purpose: it
// is exchanged within a second of being issued by any client that works, and
// a long window is only useful to somebody who intercepted it.
const CodeLifetime = 60 * time.Second

// IssueCode stores a code and returns the secret half, which is returned to
// the client exactly once in the redirect.
func (s *Store) IssueCode(ctx context.Context, in AuthCode, now time.Time) (string, error) {
	secret, err := newSecret()
	if err != nil {
		return "", err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO oauth_code
		(code_hash, client_id, redirect_uri, scopes, resource, challenge, expires_at, created_at)
		VALUES (?,?,?,?,?,?,?,?)`,
		hashSecret(secret), in.ClientID, in.RedirectURI, strings.Join(in.Scopes, " "),
		in.Resource, in.Challenge,
		now.Add(CodeLifetime).UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339))
	if err != nil {
		return "", err
	}
	return secret, nil
}

// RedeemCode consumes an authorization code exactly once.
//
// The row is marked rather than deleted, so a replay is visible in the
// database instead of looking like a code that never existed. The redemption
// and the check are one statement, which is what makes "exactly once" true
// under two simultaneous exchanges rather than nearly true.
func (s *Store) RedeemCode(ctx context.Context, secret string, now time.Time) (AuthCode, error) {
	hash := hashSecret(secret)

	res, err := s.db.ExecContext(ctx,
		`UPDATE oauth_code SET redeemed_at = ? WHERE code_hash = ? AND redeemed_at IS NULL`,
		now.UTC().Format(time.RFC3339), hash)
	if err != nil {
		return AuthCode{}, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return AuthCode{}, err
	} else if n == 0 {
		return AuthCode{}, &api.Error{
			Code: api.ErrBadRequest, Message: "that code is not valid",
		}
	}

	var (
		out     AuthCode
		scopes  string
		expires string
	)
	if err := s.db.QueryRowContext(ctx,
		`SELECT client_id, redirect_uri, scopes, resource, challenge, expires_at
		 FROM oauth_code WHERE code_hash = ?`, hash).
		Scan(&out.ClientID, &out.RedirectURI, &scopes, &out.Resource,
			&out.Challenge, &expires); err != nil {
		return AuthCode{}, err
	}
	out.Scopes = strings.Fields(scopes)
	out.ExpiresAt, err = time.Parse(time.RFC3339, expires)
	if err != nil {
		return AuthCode{}, err
	}
	if now.After(out.ExpiresAt) {
		return AuthCode{}, &api.Error{
			Code: api.ErrBadRequest, Message: "that code has expired",
		}
	}
	return out, nil
}

// PurgeExpiredCodes drops codes past their window. Redeemed ones are kept for
// a while so a replay attempt is still visible.
func (s *Store) PurgeExpiredCodes(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM oauth_code WHERE expires_at < ?`,
		now.Add(-24*time.Hour).UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// OAuthGrant is a live grant, as the settings page lists it.
type OAuthGrant struct {
	ID         string   `json:"id"`
	ClientID   string   `json:"client_id"`
	ClientName string   `json:"client_name"`
	Scopes     []string `json:"scopes"`
	Resource   string   `json:"resource"`
	CreatedAt  string   `json:"created_at"`
	LastUsedAt *string  `json:"last_used_at,omitempty"`
	RevokedAt  *string  `json:"revoked_at,omitempty"`
}

// CreateGrant records a grant and returns its refresh token, visible once.
func (s *Store) CreateGrant(ctx context.Context, g OAuthGrant, now time.Time) (OAuthGrant, string, error) {
	if g.ID == "" {
		g.ID = NewID()
	}
	refresh, err := newSecret()
	if err != nil {
		return OAuthGrant{}, "", err
	}
	g.CreatedAt = now.UTC().Format(time.RFC3339)

	if _, err := s.db.ExecContext(ctx, `INSERT INTO oauth_grant
		(id, client_id, scopes, resource, refresh_token_hash, created_at)
		VALUES (?,?,?,?,?,?)`,
		g.ID, g.ClientID, strings.Join(g.Scopes, " "), g.Resource,
		hashSecret(refresh), g.CreatedAt); err != nil {
		return OAuthGrant{}, "", err
	}
	return g, refresh, nil
}

// GrantByRefreshToken looks up a live grant. A revoked one is not found,
// which is the point of the settings page's revoke button.
func (s *Store) GrantByRefreshToken(ctx context.Context, refresh string, now time.Time) (OAuthGrant, error) {
	var (
		out    OAuthGrant
		scopes string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT g.id, g.client_id, c.name, g.scopes, g.resource, g.created_at,
		        g.last_used_at, g.revoked_at
		 FROM oauth_grant g JOIN oauth_client c ON c.id = g.client_id
		 WHERE g.refresh_token_hash = ?`, hashSecret(refresh)).
		Scan(&out.ID, &out.ClientID, &out.ClientName, &scopes, &out.Resource,
			&out.CreatedAt, &out.LastUsedAt, &out.RevokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return OAuthGrant{}, ErrNotFound
	}
	if err != nil {
		return OAuthGrant{}, err
	}
	if out.RevokedAt != nil {
		return OAuthGrant{}, &api.Error{
			Code: api.ErrUnauthorized, Message: "that grant was revoked",
		}
	}
	out.Scopes = strings.Fields(scopes)

	if _, err := s.db.ExecContext(ctx,
		`UPDATE oauth_grant SET last_used_at = ? WHERE id = ?`,
		now.UTC().Format(time.RFC3339), out.ID); err != nil {
		return OAuthGrant{}, err
	}
	return out, nil
}

// RotateRefreshToken issues a new refresh token for a grant and invalidates
// the old one.
//
// OAuth 2.1 requires rotation for public clients. The old token stops working
// the moment the new one is issued, so a stolen refresh token is usable at
// most once and the theft shows up as the real client suddenly failing.
func (s *Store) RotateRefreshToken(ctx context.Context, grantID string) (string, error) {
	refresh, err := newSecret()
	if err != nil {
		return "", err
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE oauth_grant SET refresh_token_hash = ? WHERE id = ? AND revoked_at IS NULL`,
		hashSecret(refresh), grantID); err != nil {
		return "", err
	}
	return refresh, nil
}

// Grants lists every grant for the settings page, newest first. Revoked ones
// stay listed: "this was revoked on the 4th" is what you want to see.
func (s *Store) Grants(ctx context.Context) ([]OAuthGrant, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT g.id, g.client_id, c.name, g.scopes, g.resource, g.created_at,
		        g.last_used_at, g.revoked_at
		 FROM oauth_grant g JOIN oauth_client c ON c.id = g.client_id
		 ORDER BY g.created_at DESC, g.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []OAuthGrant{}
	for rows.Next() {
		var (
			g      OAuthGrant
			scopes string
		)
		if err := rows.Scan(&g.ID, &g.ClientID, &g.ClientName, &scopes, &g.Resource,
			&g.CreatedAt, &g.LastUsedAt, &g.RevokedAt); err != nil {
			return nil, err
		}
		g.Scopes = strings.Fields(scopes)
		out = append(out, g)
	}
	return out, rows.Err()
}

// RevokeGrant cuts off a client. claude.ai holds a refresh token for your
// task list, and this is the one place that stops it.
func (s *Store) RevokeGrant(ctx context.Context, id string, now time.Time) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE oauth_grant SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`,
		now.UTC().Format(time.RFC3339), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SigningKeys returns the live keys, the active one first.
//
// Two are kept so rotation is not a logout: the newest signs, and both
// verify until the older is dropped.
func (s *Store) SigningKeys(ctx context.Context) ([]oauth.Key, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT private_pem FROM oauth_key ORDER BY active DESC, created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []oauth.Key
	for rows.Next() {
		var pemText string
		if err := rows.Scan(&pemText); err != nil {
			return nil, err
		}
		key, err := oauth.ParseKey(pemText)
		if err != nil {
			return nil, err
		}
		out = append(out, key)
	}
	return out, rows.Err()
}

// EnsureSigningKeys generates keys on first start and returns the live set.
//
// Two from the beginning rather than one now and a second at the first
// rotation: a rotation path that has never run is a rotation path that does
// not work, and finding that out during an incident is the wrong time.
func (s *Store) EnsureSigningKeys(ctx context.Context, now time.Time) ([]oauth.Key, error) {
	keys, err := s.SigningKeys(ctx)
	if err != nil {
		return nil, err
	}
	for len(keys) < 2 {
		key, err := oauth.NewKey()
		if err != nil {
			return nil, err
		}
		text, err := oauth.MarshalKey(key)
		if err != nil {
			return nil, err
		}
		// The first one generated is the active signer; the second is the
		// standby that makes the next rotation a no-downtime change.
		active := len(keys) == 0
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO oauth_key (kid, private_pem, created_at, active) VALUES (?,?,?,?)`,
			key.Kid, text, now.UTC().Format(time.RFC3339), boolInt(active)); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, nil
}

// RotateSigningKey promotes the standby and generates a new standby. Tokens
// signed by the outgoing key keep verifying until it leaves the set.
func (s *Store) RotateSigningKey(ctx context.Context, now time.Time) error {
	keys, err := s.SigningKeys(ctx)
	if err != nil {
		return err
	}
	if len(keys) < 2 {
		if _, err := s.EnsureSigningKeys(ctx, now); err != nil {
			return err
		}
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// The oldest goes, the standby becomes active, and a new standby appears.
	if _, err := tx.ExecContext(ctx, `DELETE FROM oauth_key WHERE kid = ?`, keys[0].Kid); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE oauth_key SET active = 1 WHERE kid = ?`, keys[1].Kid); err != nil {
		return err
	}

	fresh, err := oauth.NewKey()
	if err != nil {
		return err
	}
	text, err := oauth.MarshalKey(fresh)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO oauth_key (kid, private_pem, created_at, active) VALUES (?,?,?,0)`,
		fresh.Kid, text, now.UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	return tx.Commit()
}

// CachedClientFresh returns a CIMD client whose metadata has not expired.
//
// The bool is false when there is no row or the cache is stale, which is the
// caller's cue to refetch. A stale row is deliberately not returned: the
// client's own document is the authority on its name and redirect URIs, and
// serving a name somebody has since changed onto a consent screen is exactly
// the mistake that makes consent meaningless.
func (s *Store) CachedClientFresh(ctx context.Context, id string, now time.Time) (OAuthClient, bool, error) {
	var expires sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT metadata_expires_at FROM oauth_client WHERE id = ? AND source = 'cimd'`, id).
		Scan(&expires)
	if errors.Is(err, sql.ErrNoRows) {
		return OAuthClient{}, false, nil
	}
	if err != nil {
		return OAuthClient{}, false, err
	}
	if !expires.Valid || !cacheStillGood(expires.String, now) {
		return OAuthClient{}, false, nil
	}

	client, err := s.OAuthClientByID(ctx, id)
	if err != nil {
		return OAuthClient{}, false, err
	}
	return client, true, nil
}

// cacheStillGood reports whether a cached document may still be used.
//
// A timestamp that will not parse counts as expired rather than as an error.
// The only consequence of refetching is one HTTP request, while trusting a
// row whose expiry cannot be read means serving a name onto a consent screen
// that the client may have changed.
func cacheStillGood(stamp string, now time.Time) bool {
	at, err := time.Parse(time.RFC3339, stamp)
	return err == nil && now.Before(at)
}

// SaveResolvedClient records a Client ID Metadata Document.
//
// An upsert rather than an insert, because a client id is a URL that stays the
// same while the document behind it changes. The row is a cache with a foreign
// key pointed at it, not a registration.
//
// It refuses to overwrite a client that arrived any other way. Otherwise
// anybody who could get a URL-shaped client id into the table could replace a
// registered client's redirect URIs with their own by serving a document.
func (s *Store) SaveResolvedClient(ctx context.Context, in OAuthClient, expiresAt, now time.Time) (OAuthClient, error) {
	var source string
	err := s.db.QueryRowContext(ctx, `SELECT source FROM oauth_client WHERE id = ?`, in.ID).Scan(&source)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return OAuthClient{}, err
	case source != "cimd":
		return OAuthClient{}, &api.Error{
			Code:    api.ErrBadRequest,
			Message: "that client id is already registered by another means",
		}
	}

	uris, err := json.Marshal(in.RedirectURIs)
	if err != nil {
		return OAuthClient{}, err
	}
	stamp := now.UTC().Format(time.RFC3339)
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO oauth_client
		   (id, secret_hash, name, redirect_uris, scopes, source,
		    created_at, metadata_fetched_at, metadata_expires_at)
		 VALUES (?, NULL, ?, ?, ?, 'cimd', ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   name = excluded.name,
		   redirect_uris = excluded.redirect_uris,
		   scopes = excluded.scopes,
		   metadata_fetched_at = excluded.metadata_fetched_at,
		   metadata_expires_at = excluded.metadata_expires_at`,
		in.ID, in.Name, string(uris), strings.Join(in.Scopes, " "),
		stamp, stamp, expiresAt.UTC().Format(time.RFC3339)); err != nil {
		return OAuthClient{}, err
	}
	return s.OAuthClientByID(ctx, in.ID)
}

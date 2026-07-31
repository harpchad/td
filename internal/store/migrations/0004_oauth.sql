-- Phase 9. td is its own OAuth 2.1 authorization server, because claude.ai's
-- custom connector UI takes a client id and secret and has no field for a
-- bearer token. There is one user and no external IdP, so /authorize reuses
-- the password and TOTP login that already exists.

-- A registered client. Rows arrive two ways: Dynamic Client Registration
-- (RFC 7591, deprecated as of the 2026-07-28 MCP revision but still the only
-- thing some clients speak), and Client ID Metadata Documents, where the
-- client id is a URL and the metadata is fetched from it.
CREATE TABLE oauth_client (
  id            TEXT PRIMARY KEY,
  secret_hash   TEXT,              -- NULL for a public client
  name          TEXT NOT NULL,
  redirect_uris TEXT NOT NULL,     -- JSON array, matched exactly
  scopes        TEXT NOT NULL,     -- space separated, the most it may ask for
  -- 'dcr' or 'cimd'. A CIMD client id is a https URL and its metadata is
  -- refetched rather than trusted from this row forever.
  source        TEXT NOT NULL DEFAULT 'dcr',
  created_at    TEXT NOT NULL,
  last_used_at  TEXT
);

-- An authorization code, single use and short lived.
--
-- The code itself is never stored: only its SHA-256, the same rule the
-- session and token tables follow. A database dump contains no usable
-- credential.
CREATE TABLE oauth_code (
  code_hash      TEXT PRIMARY KEY,
  client_id      TEXT NOT NULL REFERENCES oauth_client(id),
  redirect_uri   TEXT NOT NULL,
  scopes         TEXT NOT NULL,
  -- The resource this code will mint a token for. RFC 8707 makes it
  -- mandatory, and the token's audience is this value exactly.
  resource       TEXT NOT NULL,
  challenge      TEXT NOT NULL,    -- PKCE, S256 only
  expires_at     TEXT NOT NULL,
  created_at     TEXT NOT NULL,
  -- Set the moment the code is exchanged. A second exchange is refused, and
  -- the row stays so replay is visible rather than silent.
  redeemed_at    TEXT
);

-- A live grant: what the settings page lists next to the static tokens, with
-- the same revoke button. claude.ai holds a refresh token for your task list
-- and you want one place to cut it off.
CREATE TABLE oauth_grant (
  id                 TEXT PRIMARY KEY,
  client_id          TEXT NOT NULL REFERENCES oauth_client(id),
  scopes             TEXT NOT NULL,
  resource           TEXT NOT NULL,
  refresh_token_hash TEXT NOT NULL UNIQUE,
  created_at         TEXT NOT NULL,
  last_used_at       TEXT,
  expires_at         TEXT,
  revoked_at         TEXT
);
CREATE INDEX oauth_grant_client_idx ON oauth_grant(client_id);

-- Signing keys. Two are live at once so rotation does not invalidate every
-- session: the newest signs, and both verify until the older one is dropped.
CREATE TABLE oauth_key (
  kid         TEXT PRIMARY KEY,
  private_pem TEXT NOT NULL,
  created_at  TEXT NOT NULL,
  -- The key that signs. Exactly one row has this set.
  active      INTEGER NOT NULL DEFAULT 0
);

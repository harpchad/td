-- Phase 2. One account, its second factor, its recovery codes, browser
-- sessions, and API tokens.
--
-- Nothing here stores a credential in a form that can be used. The password
-- is an argon2id digest; every other secret is a SHA-256 of a 256-bit random
-- value. A dump of this database authenticates nobody.

CREATE TABLE account (
  id            TEXT PRIMARY KEY,
  username      TEXT UNIQUE NOT NULL,
  password_hash TEXT NOT NULL,
  totp_secret   TEXT NOT NULL,
  created_at    TEXT NOT NULL,

  -- Failures against the password and the TOTP step are counted separately,
  -- so four wrong passwords and four wrong codes do not add up to a lockout.
  failed_password INTEGER NOT NULL DEFAULT 0,
  failed_totp     INTEGER NOT NULL DEFAULT 0,
  locked_until    TEXT
);

CREATE TABLE recovery_code (
  id         TEXT PRIMARY KEY,
  account_id TEXT NOT NULL REFERENCES account(id),
  code_hash  TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL,
  -- Set the first time a code is accepted. A used code is spent, not deleted,
  -- so the settings page can show how many are left.
  used_at    TEXT
);
CREATE INDEX recovery_code_account_idx ON recovery_code(account_id);

CREATE TABLE session (
  id           TEXT PRIMARY KEY,
  account_id   TEXT NOT NULL REFERENCES account(id),
  token_hash   TEXT NOT NULL UNIQUE,
  created_at   TEXT NOT NULL,
  expires_at   TEXT NOT NULL,
  last_seen_at TEXT NOT NULL,
  ip           TEXT NOT NULL DEFAULT '',
  user_agent   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX session_expiry_idx ON session(expires_at);

CREATE TABLE api_token (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  token_hash TEXT NOT NULL UNIQUE,
  -- The leading fragment, so the settings page can tell two tokens apart
  -- without holding either of them.
  prefix     TEXT NOT NULL,
  -- Comma separated: read, write, capture, sync:<source>.
  scopes     TEXT NOT NULL,
  -- What the event log records for a mutation made with this token:
  -- 'me', 'mcp:<name>', or 'plugin:<source>'. Undo is already scoped by
  -- actor, so this is what keeps a bad agent batch separable from your work.
  actor      TEXT NOT NULL DEFAULT 'me',
  created_at   TEXT NOT NULL,
  last_used_at TEXT,
  revoked_at   TEXT
);
CREATE INDEX api_token_revoked_idx ON api_token(revoked_at);

-- Login attempts, for the per-IP rate limit. Rows older than the window are
-- pruned on every check, so this stays small even under a sustained attempt.
CREATE TABLE login_attempt (
  id TEXT PRIMARY KEY,
  at TEXT NOT NULL,
  ip TEXT NOT NULL
);
CREATE INDEX login_attempt_ip_idx ON login_attempt(ip, at);

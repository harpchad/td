-- Sync plugins configured on the server rather than on a laptop.
--
-- The Planner mirror started life as a client: it read Graph and posted to
-- POST /api/v1/sync/planner with a scoped token, which is the contract
-- section 8 defines for third parties and is still exactly how a third party
-- would do it. For the one first-party plugin it was the wrong place. A
-- mirror that only refreshes while somebody's laptop is open is not a mirror,
-- and the Memos webhook was already server-side on the same tick, so Planner
-- was the odd one out by accident rather than by decision.
--
-- Settings and credential are separate columns because they are different
-- kinds of thing. Settings is what the web UI edits and what an export could
-- safely carry; credential holds a Graph refresh token and is treated the way
-- every other credential in this schema is treated, which is that it never
-- leaves the database.
CREATE TABLE plugin_config (
  name       TEXT PRIMARY KEY,
  enabled    INTEGER NOT NULL DEFAULT 0,
  settings   TEXT NOT NULL DEFAULT '{}',
  credential TEXT,

  -- How often the scheduler runs it. Zero takes the plugin's own default.
  interval_minutes INTEGER NOT NULL DEFAULT 15,

  -- What the last run did, so the settings page can answer "is this working"
  -- without anybody reading a log. last_error is cleared on success, so a
  -- present value always describes the current state rather than history.
  last_run_at TEXT,
  last_result TEXT,
  last_error  TEXT,

  -- The upstream identities the last run would not guess at. Kept because
  -- the answer is a person's to give and the settings page is where they are:
  -- a list that only ever appeared in CLI output was a list nobody acted on.
  last_unresolved TEXT,

  updated_at TEXT NOT NULL
);

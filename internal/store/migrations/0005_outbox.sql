-- Phase 10. The outbound webhook reads the event log rather than firing
-- inline from Complete, so a completion is never lost to a webhook that was
-- down and never duplicated by a retry. One row per consumer, holding the
-- last event sequence it has delivered.
--
-- This is the same shape /events already exposes to MCP clients: the event
-- log is the change feed, and anything that wants to react to changes follows
-- a cursor into it.
CREATE TABLE outbox_cursor (
  name       TEXT PRIMARY KEY,
  seq        INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT
);

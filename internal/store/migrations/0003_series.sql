-- Phase 7. Series get the columns section 3 specifies plus the two the
-- catch-up policy needs, and attachments get a real blob reference.

-- last_fired_at is where the catch-up walk starts from. Without it a
-- scheduler restart cannot tell a missed occurrence from one that never
-- came due.
ALTER TABLE series ADD COLUMN last_fired_at TEXT;

-- The task a series most recently materialised, so "exactly one open
-- instance" is checkable rather than inferred.
ALTER TABLE series ADD COLUMN current_task_id TEXT REFERENCES task(id);

CREATE INDEX task_series_idx ON task(series_id);

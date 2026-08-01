-- View state, like ui_state in 0001: never exported, never synced, never an
-- event. Fold state is per task and lives there; the filter you are reading
-- is per client and lives here.
--
-- One row, enforced. A filter is a place you are, not a collection, and a
-- table that can hold two of them is a table that eventually does.
--
-- NULL filter and no row are different things. No row means nobody has ever
-- chosen, so home opens on the saved filter in slot 1. A row holding the empty
-- string means somebody cleared the box on purpose, and clearing has to stick
-- the same way setting does.
CREATE TABLE view_state (
  id     INTEGER PRIMARY KEY CHECK (id = 1),
  filter TEXT NOT NULL
);

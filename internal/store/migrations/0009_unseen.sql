-- View state, like ui_state in 0001: never synced and never an event.
--
-- A row here means a task arrived without the owner watching: a sync mirror,
-- a plugin capture, an agent over MCP. Anything the owner typed themselves is
-- born without one, because you do not need to be told about the thing you
-- just wrote.
--
-- Presence is the whole fact, which is why there is no column beyond the key.
-- The state is stored the way round rather than as a seen_at stamp on task so
-- that an empty table means "nothing new" instead of "everything is new": a
-- migration over an existing database, or a restore that predates this file,
-- highlights nothing rather than lighting up every row at once.
CREATE TABLE task_unseen (
  task_id TEXT PRIMARY KEY REFERENCES task(id)
);

-- Phase 1 schema. Follows BUILD-SPEC.md section 3, with three additions
-- noted in DECISIONS.md: person.handle and person_group.handle, which the
-- @person and grp: filter tokens need in order to have anything to match, and
-- the saved_filter table that section 6 describes but does not spell out.

CREATE TABLE task (
  id            TEXT PRIMARY KEY,
  num           INTEGER UNIQUE,
  title         TEXT NOT NULL,
  notes         TEXT NOT NULL DEFAULT '',
  status        TEXT NOT NULL,
  priority      INTEGER,
  due_at        TEXT,
  due_is_date   INTEGER NOT NULL DEFAULT 1,
  start_at      TEXT,
  snooze_until  TEXT,
  notify        TEXT NOT NULL DEFAULT 'auto',
  remind_before INTEGER,
  notified_at   TEXT,
  waiting_on    TEXT REFERENCES person(id),
  waiting_since TEXT,
  effort        INTEGER,
  parent_id     TEXT REFERENCES task(id),
  series_id     TEXT,
  source        TEXT NOT NULL DEFAULT 'local',
  external_id   TEXT,
  external_url  TEXT,
  external_rev  TEXT,
  upstream_gone INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL,
  completed_at  TEXT,
  UNIQUE (source, external_id),
  CHECK (status IN ('inbox','todo','doing','waiting','done','dropped')),
  CHECK (notify IN ('auto','on','off')),
  CHECK (priority IS NULL OR priority BETWEEN 1 AND 4)
);

CREATE INDEX task_status_idx ON task(status);
CREATE INDEX task_due_idx    ON task(due_at);
CREATE INDEX task_parent_idx ON task(parent_id);
CREATE INDEX task_source_idx ON task(source);

CREATE TABLE tag (
  id   TEXT PRIMARY KEY,
  name TEXT UNIQUE NOT NULL
);

CREATE TABLE task_tag (
  task_id TEXT NOT NULL REFERENCES task(id),
  tag_id  TEXT NOT NULL REFERENCES tag(id),
  PRIMARY KEY (task_id, tag_id)
);
CREATE INDEX task_tag_tag_idx ON task_tag(tag_id);

CREATE TABLE person (
  id     TEXT PRIMARY KEY,
  handle TEXT UNIQUE NOT NULL,
  name   TEXT NOT NULL,
  email  TEXT,
  notes  TEXT NOT NULL DEFAULT ''
);

CREATE TABLE person_identity (
  person_id   TEXT NOT NULL REFERENCES person(id),
  source      TEXT NOT NULL,
  external_id TEXT NOT NULL,
  PRIMARY KEY (source, external_id)
);

CREATE TABLE person_group (
  id     TEXT PRIMARY KEY,
  handle TEXT UNIQUE NOT NULL,
  name   TEXT NOT NULL
);

CREATE TABLE group_member (
  group_id  TEXT NOT NULL REFERENCES person_group(id),
  person_id TEXT NOT NULL REFERENCES person(id),
  PRIMARY KEY (group_id, person_id)
);
CREATE INDEX group_member_person_idx ON group_member(person_id);

CREATE TABLE task_person (
  task_id   TEXT NOT NULL REFERENCES task(id),
  person_id TEXT NOT NULL REFERENCES person(id),
  role      TEXT NOT NULL,
  PRIMARY KEY (task_id, person_id, role),
  CHECK (role IN ('assigner','assignee','involved'))
);
CREATE INDEX task_person_person_idx ON task_person(person_id);

CREATE TABLE task_group (
  task_id  TEXT NOT NULL REFERENCES task(id),
  group_id TEXT NOT NULL REFERENCES person_group(id),
  PRIMARY KEY (task_id, group_id)
);

CREATE TABLE series (
  id            TEXT PRIMARY KEY,
  rrule         TEXT NOT NULL,
  mode          TEXT NOT NULL,
  catchup       TEXT NOT NULL DEFAULT 'skip',
  anchor        TEXT NOT NULL,
  tz            TEXT NOT NULL,
  template_json TEXT NOT NULL,
  next_at       TEXT,
  ends_at       TEXT
);

-- View state. Never exported, never synced, never an event.
CREATE TABLE ui_state (
  task_id   TEXT PRIMARY KEY REFERENCES task(id),
  collapsed INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE attachment (
  id         TEXT PRIMARY KEY,
  task_id    TEXT NOT NULL REFERENCES task(id),
  sha256     TEXT NOT NULL,
  filename   TEXT,
  bytes      INTEGER,
  mime       TEXT,
  created_at TEXT NOT NULL
);
CREATE INDEX attachment_task_idx ON attachment(task_id);

-- Append-only history. Powers undo, the activity feed, and journal export.
CREATE TABLE event (
  seq        INTEGER PRIMARY KEY AUTOINCREMENT,
  at         TEXT NOT NULL,
  actor      TEXT NOT NULL,
  task_id    TEXT,
  kind       TEXT NOT NULL,
  patch_json TEXT NOT NULL
);
CREATE INDEX event_task_idx  ON event(task_id);
CREATE INDEX event_actor_idx ON event(actor, seq);

CREATE TABLE saved_filter (
  id    TEXT PRIMARY KEY,
  slot  INTEGER UNIQUE,
  name  TEXT NOT NULL,
  query TEXT NOT NULL,
  CHECK (slot IS NULL OR slot BETWEEN 1 AND 9)
);

CREATE VIRTUAL TABLE task_fts USING fts5(title, notes, content='task', content_rowid='rowid');

CREATE TRIGGER task_fts_ai AFTER INSERT ON task BEGIN
  INSERT INTO task_fts(rowid, title, notes) VALUES (new.rowid, new.title, new.notes);
END;

CREATE TRIGGER task_fts_ad AFTER DELETE ON task BEGIN
  INSERT INTO task_fts(task_fts, rowid, title, notes) VALUES ('delete', old.rowid, old.title, old.notes);
END;

CREATE TRIGGER task_fts_au AFTER UPDATE ON task BEGIN
  INSERT INTO task_fts(task_fts, rowid, title, notes) VALUES ('delete', old.rowid, old.title, old.notes);
  INSERT INTO task_fts(rowid, title, notes) VALUES (new.rowid, new.title, new.notes);
END;

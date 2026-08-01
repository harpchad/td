# Task Manager Build Spec

Working name: `td`. Single-user task manager with a server, a TUI, and a
web UI that behaves like the TUI.

---

## 1. Shape and constraints

One Go module, two binaries. `tdd` is the server: HTTP API, MCP
endpoint, web UI, SQLite. It builds for `linux/amd64` only and ships as
a container image, never installed on a workstation. `td` is the
client: TUI plus one-shot CLI commands, cross-compiled for whatever you
carry. The client talks to the server over HTTP only. It never opens
the database file.

Go builds both from one `cmd/` directory with no extra machinery, so
the split costs nothing. What it buys is worth more than the tidiness
of a single artifact. The client links no database code, no password
hashing, no MCP server, and cross-compiles to `darwin/arm64` with a
plain `go build`. And because the server only ever builds inside a
Docker stage, `CGO_ENABLED=1` is free there: you can use the mainstream
cgo SQLite driver with full FTS5 rather than picking a pure-Go driver
to keep client builds simple.

The boundary has to be enforced in the package layout or it erodes in a
month:

```text
cmd/td      client only
cmd/tdd     server only
internal/api      request and response types, API version constant
internal/query    filter grammar parser, sort comparator
internal/store    SQLite, migrations, FTS  <- cmd/tdd only
internal/tui      Bubble Tea             <- cmd/td only
```

`internal/query` is the important shared one. The client parses the
filter string locally to give you syntax highlighting and errors before
it sends anything, and the server parses the same string to build SQL.
Same parser, one grammar, no drift. If `internal/store` ever appears in
the client's import graph, the split has already failed.

Separate binaries do reintroduce version skew, which a single artifact
would not have solved either: the container and your laptop update on
different schedules regardless. Handle it explicitly. The client sends
`X-Td-Client` with its version, the server returns its own in every
response, and the client prints one warning line when the major
versions differ.

That the client never touches SQLite is the other decision worth
defending. If the TUI could read the database directly, it would only
work on the box holding the file, and you would maintain two code paths
for every query. One transport means the TUI, the web UI, the MCP
server, and the sync plugins all exercise the same API, so a bug shows
up everywhere at once instead of hiding in the path you use least.

Go plus SQLite keeps it cheap on the new 8 GB Docker host. A static Go
binary with an embedded database sits around 40 MB resident. A Node or
Rails equivalent would cost you four times that, and you have other
containers to feed.

**Non-goals.** Collaboration, sharing, permissions. Time tracking, since
Memos already holds the journal. Kanban boards, Gantt, burndown. Mobile
apps in v1: the web UI is the mobile client.

---

## 2. Architecture

```text
tdd (container, linux/amd64 only)
  http  :8080   web UI + REST + /mcp
  data  /data/td.db           SQLite, WAL
        /data/blobs/aa/bb/... content-addressed attachments
        /data/config.toml

td (Mac, laptop, wherever)
  TUI + CLI, config: server URL + token

plugins (separate processes or containers)
  poll Jira / monday / Planner, push into the API with a scoped token
```

Plugins are API clients, not linked code. This is the cheapest design
that works and it lets you write a plugin in PowerShell at 11pm without
recompiling the server. The server gives them one endpoint,
`POST /api/v1/sync/{source}`, that upserts by external ID. The plugin
owns its own schedule, credentials, and state. If a plugin dies, nothing
else notices.

The alternative, an in-process plugin interface over stdio JSON-RPC,
buys you lifecycle management and a plugin registry UI. Skip it until
you have three working plugins and are tired of managing their cron
entries.

---

## 3. Data model

```sql
-- core
CREATE TABLE task (
  id            TEXT PRIMARY KEY,        -- ULID, client-generated
  num           INTEGER UNIQUE,          -- short human ID, monotonic
  title         TEXT NOT NULL,
  notes         TEXT NOT NULL DEFAULT '',
  status        TEXT NOT NULL,           -- inbox|todo|doing|waiting|done|dropped
  priority      INTEGER,                 -- 1..4, NULL = none
  due_at        TEXT,                    -- RFC3339 or YYYY-MM-DD
  due_is_date   INTEGER NOT NULL DEFAULT 1,
  start_at      TEXT,                    -- hidden from default views before this
  snooze_until  TEXT,
  notify        TEXT NOT NULL DEFAULT 'auto',  -- auto|on|off
  remind_before INTEGER,                   -- minutes, NULL = use global lead
  notified_at   TEXT,                      -- last push sent, prevents repeats
  waiting_on    TEXT REFERENCES person(id),
  waiting_since TEXT,
  effort        INTEGER,                 -- minutes, optional
  parent_id     TEXT REFERENCES task(id),
  series_id     TEXT REFERENCES series(id),
  source        TEXT NOT NULL DEFAULT 'local',
  external_id   TEXT,
  external_url  TEXT,
  external_rev  TEXT,
  upstream_gone INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL,
  completed_at  TEXT,
  UNIQUE (source, external_id)
);

CREATE TABLE tag (id TEXT PRIMARY KEY, name TEXT UNIQUE NOT NULL);
CREATE TABLE task_tag (task_id TEXT, tag_id TEXT, PRIMARY KEY (task_id, tag_id));

CREATE TABLE person (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, email TEXT, notes TEXT
);
CREATE TABLE person_identity (          -- maps plugins to one person
  person_id TEXT, source TEXT, external_id TEXT,
  PRIMARY KEY (source, external_id)
);
CREATE TABLE person_group (id TEXT PRIMARY KEY, name TEXT NOT NULL);
CREATE TABLE group_member (group_id TEXT, person_id TEXT, PRIMARY KEY (group_id, person_id));

CREATE TABLE task_person (
  task_id TEXT, person_id TEXT,
  role TEXT NOT NULL,                    -- assigner|assignee|involved
  PRIMARY KEY (task_id, person_id, role)
);
CREATE TABLE task_group (task_id TEXT, group_id TEXT, PRIMARY KEY (task_id, group_id));

CREATE TABLE series (
  id TEXT PRIMARY KEY,
  rrule TEXT NOT NULL,                   -- RFC 5545
  mode TEXT NOT NULL,                    -- fixed|after_completion
  catchup TEXT NOT NULL DEFAULT 'skip',  -- skip|pile
  anchor TEXT NOT NULL,                  -- due|start
  tz TEXT NOT NULL,
  template_json TEXT NOT NULL,           -- title, tags, people, priority
  next_at TEXT,
  ends_at TEXT
);

-- view state. never exported, never synced, never an event
CREATE TABLE ui_state (
  task_id TEXT PRIMARY KEY REFERENCES task(id),
  collapsed INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE attachment (
  id TEXT PRIMARY KEY, task_id TEXT NOT NULL,
  sha256 TEXT NOT NULL, filename TEXT, bytes INTEGER,
  mime TEXT, created_at TEXT NOT NULL
);

-- append-only history: powers undo, activity feed, journal export
CREATE TABLE event (
  seq INTEGER PRIMARY KEY AUTOINCREMENT,
  at TEXT NOT NULL, actor TEXT NOT NULL,  -- 'me'|'mcp:claude'|'plugin:jira'
  task_id TEXT, kind TEXT NOT NULL, patch_json TEXT NOT NULL
);

CREATE VIRTUAL TABLE task_fts USING fts5(title, notes, content='task');
```

Notes on the shape:

- **ULIDs generated by the client.** Lets quick-add work before the
  server answers, and gives plugins a stable key to retry against
  without duplicating rows. `num` exists so you can type `td done 412`.
- **The event table is the important one.** It gives you undo for free,
  an activity feed, an "what did I close today" export into Memos, and a
  change cursor that MCP clients can poll instead of re-listing
  everything.
- **`parent_id` for subtasks, one level, enforced in the API.** A task
  with a parent cannot itself be a parent. Arbitrary nesting looks fine
  in a data model and is miserable in a flat list.
- **No `project` table.** Tags cover it. Add a `#proj/` namespace
  convention and a rule that a task can carry at most one `proj/` tag if
  you want project semantics later.

---

## 4. Task semantics

### Status

`inbox` is the triage bucket. Quick-add always lands there. A task
leaves `inbox` when it has, at minimum, a priority or a due date. The
home view hides `inbox` by default and shows a count in the top bar.
That count is the entire point: it nags without cluttering.

`waiting` needs the person link. "Waiting on Mikah since the 12th" is
the state you actually live in, and the derived view "waiting more than
7 days" is worth building on day one.

`dropped` is not `done`. Keep both, never hard-delete, and let the
activity feed show what you abandoned.

### Dates

Three fields, distinct meanings, no overlap:

- `due_at`: a commitment. Missing it means something.
- `start_at`: hide until this date. Defer, not schedule.
- `snooze_until`: temporary hide, cleared when it expires.

Support date-only and datetime due values with the `due_is_date` flag.
"Pay the mortgage on the 1st" is a date, not an instant, and treating it
as an instant breaks the moment you travel. Store timestamps as UTC
RFC3339. Store dates as `YYYY-MM-DD` with no zone.

Do not add a separate "do date" alongside due. Two date fields you have
to keep in sync is how task systems rot.

### Recurrence

Use RFC 5545 RRULE rather than inventing a syntax. `teambition/rrule-go`
handles it. The two modes you named map to `mode`:

- `fixed`: next instance comes from the rule regardless of completion.
- `after_completion`: next `due_at` is completion time plus the rule's
  interval.

Materialize exactly one open instance at a time. A `series` row plus one
live `task` row. Generating a year of instances up front makes every
query slower and every edit ambiguous.

The case you have not specified yet is catch-up. Daily fixed task, you
ignore it for three days: do you now owe three, or one? `catchup =
'skip'` rolls forward to the next occurrence and logs the misses as
events. `catchup = 'pile'` creates the backlog. Default to `skip`.
Anything that piles up silently gets ignored, and then the whole list
gets ignored.

Editing an instance edits that instance. Editing the series needs an
explicit action (`E` in the TUI). Never guess which one the user meant.

### Priority and default order

Priority is 1 through 4, where 1 is highest, plus unset. Resist a
computed urgency score in v1. Taskwarrior's urgency formula is clever
and nobody can predict what it will show them.

The default sort, when the user has not picked one:

1. Overdue, most overdue first
2. Due today
3. Priority ascending, unset last
4. Due date ascending, no due date last
5. Created ascending

Make that comparator one function shared by the API and both UIs.

### Subtasks

One level, and the rules have to be decided up front because every
task app that got them wrong got them wrong here:

- A subtask is a real task. It has its own status, priority, due date,
  people, and tags. It is not a checklist string.
- Subtasks do not inherit anything at creation, except tags, and only
  as a copy you can then edit. Live inheritance means editing a parent
  silently rewrites children.
- Completing the parent does not complete the children. It prompts:
  complete all, or leave them. Completing the last child does not
  complete the parent either. The parent is the commitment, the
  children are steps, and the two finish at different times more often
  than you would think.
- Subtasks never appear as top-level rows in the default list. They
  appear indented under a parent only when the parent is in the result
  set, and standalone when a filter matches them directly. The parent
  row shows `2/5` when it has children.
- Sort order and display order are not the same thing. The comparator
  orders parents. A subtask is then lifted out of its sorted position
  and drawn directly under its parent. In the home view fixture, 113
  sorts seventh and displays fourth.
- A parent folds. Collapsed hides its children and the `2/5` count
  becomes the only signal they exist, which is why that count is always
  drawn. `z` toggles the row under the cursor, `Z` toggles every parent
  in view.
- Fold state persists per task, in a `ui_state` table rather than on
  the task row. It is view state: it must not generate events, appear
  in an export, or sync to a plugin.
- A collapsed parent expands automatically when a filter matches one of
  its children directly. Search never hides a match. When the filter
  clears, the parent returns to its stored state.
- A subtask with its own due date generates its own reminders.
- `is:orphan` finds subtasks whose parent is done or dropped.

Do not let external plugins create subtasks in v1. Jira subtasks and
monday subitems map badly and the mirror is read-only anyway. Flatten
them and keep the link.

---

## 5. People

The person page is a first-class screen, not a filter preset. Open it
before a 1:1 and it shows, in this order: tasks assigned to them, tasks
you owe them, tasks you are waiting on them for with age, tasks where
they are involved, and their group's tasks. Add a free-text "agenda"
section per person that is just tasks tagged `agenda` scoped to that
person.

`person_identity` is what makes plugins useful. A Jira account ID, a
monday user ID, and an Entra object ID all resolve to one person row, so
"everything involving Brandiss" spans systems. Without it you get three
Brandisses and the feature is worthless. Unmatched external users go
into a pending list you can merge from.

Groups are static membership, not saved searches. Keep them dumb.

---

## 6. Filters and the query grammar

One grammar, used by the TUI filter bar, the web search box, the
`?q=` API parameter, and the MCP search tool. Terms are space separated
and AND by default. `-` negates.

```text
is:open is:done is:waiting is:inbox is:orphan
p:1 p:<=2
due:today due:<=friday due:overdue due:none
start:<=today
#vpn #proj/monday          tags
@mikah @mikah:waiting      person, optionally by role
grp:leadership
src:jira src:local
has:attachment has:notes has:sub
notify:on notify:off notify:auto
free text                  FTS5 over title and notes
```

Saved filters bind to number keys 1 through 9 in both UIs, and are
stored server-side so they follow you between clients. Ship four
defaults: Today, Inbox, Waiting, Overdue.

---

## 7. Capture

Quick-add has to be faster than opening the app, or you will not use it
and the whole thing dies.

- `td a "call the dealer about the alignment"` from any shell, exits
  immediately, queues locally if the server is unreachable.
- `a` key in the TUI and web, one line, Escape cancels.
- Inline syntax parsed on the way in, same tokens as the filter grammar:
  `a "renew wildcard cert" #certs @stacey p:2 due:friday`. Anything the
  parser does not recognize stays in the title.
- MCP `capture` tool, so an agent can drop something in the inbox
  mid-conversation.

Everything from quick-add lands in `inbox` unless a priority or due date
was given inline.

Triage is a dedicated mode, not a view. One task at a time, big, with
single-key actions: priority, due, tag, person, promote, drop, next.
Getting from 20 to 0 should take two minutes.

---

## 8. Sync plugins

Plugins are one-way importers first. Reading Jira, monday, and Planner
into one list is 90% of the value and 10% of the work. Write-back is the
other 90%.

**Field ownership is the core rule.** A mirrored task has upstream-owned
fields (title, status, external URL) and locally-owned fields (priority,
your tags, your people links, your notes, snooze). Sync overwrites the
first set and never touches the second. Write that table down per plugin
before writing code, because "my priority got wiped by a sync" kills
trust in one incident.

Plugin contract, over the API with a scoped token:

```http
POST /api/v1/sync/jira
{
  "cursor": "2026-07-31T04:00:00Z",
  "items": [
    {"external_id": "OPS-1421", "title": "...", "status": "todo",
     "url": "https://...", "rev": "17",
     "people": [{"role": "assigner", "source_user": "5b10a..."}],
     "due_at": "2026-08-04"}
  ],
  "gone": ["OPS-1390"]
}
```

The server upserts by `(source, external_id)`, applies the ownership
rules, marks `gone` items `upstream_gone = 1` rather than deleting them,
and returns a new cursor. Idempotent, so a plugin can always replay.

V1 is a read-only mirror, decided. Every mirrored task carries
`external_url`, and the detail view puts it on the first line so one
keystroke opens the real thing. That removes the callback path, the
action queue, and the whole class of failures where a remote write
half-succeeds and the two systems disagree about what happened.

If write-back arrives later, keep it to two actions: complete and
comment, through `POST /api/v1/tasks/{id}/actions`, handed to the
plugin's callback URL. Field-level bidirectional sync is a swamp.

An outbound plugin is worth building too: on `task.completed`, POST to
Memos so the journal fills itself. That is a webhook, not a plugin, and
it should be in v1.

---

## 9. API

REST, `/api/v1`, JSON. Nothing exotic.

```text
GET    /tasks?q=&sort=&limit=&cursor=
POST   /tasks
GET    /tasks/{id}
PATCH  /tasks/{id}
POST   /tasks/{id}/complete
POST   /tasks/{id}/attachments
DELETE /tasks/{id}                 -> status=dropped, never a hard delete
GET    /people, /people/{id}/tasks
GET    /filters, POST /filters
GET    /events?since={seq}         -> change feed, long-poll or SSE
POST   /undo                       -> reverses last event by actor
POST   /sync/{source}
```

Client-supplied IDs on `POST /tasks` make it idempotent. `PATCH` takes
`If-Match` against `updated_at` so a slow TUI does not clobber a web
edit.

Every mutation writes an event with an actor derived from the token.
That is what makes `/undo` and the audit view possible.

---

## 10. MCP

Serve MCP at `/mcp` in the same binary, over the same service layer.
Tools:

```text
search_tasks(query, limit)      the grammar from section 6
get_task(id)
capture(title, notes)           always lands in inbox
create_task(...)                requires a write-scoped token
update_task(id, patch)
complete_task(id)
add_note(id, text)
list_people() / person_agenda(person)
whats_next(limit)               default sort, top N, for "what should I do"
recent_activity(since)          reads the event log
```

### Authentication

Two paths, one verifier. Which one a client uses depends entirely on
what that client supports.

**Static bearer tokens** cover Claude Code, Claude Desktop, Cursor, and
anything else that lets you set a custom header. Same `td_` tokens as
the rest of the API, same scopes, same revocation page. This is the
path that works on day one and it is how the Memos setup already works.

**OAuth 2.1** is required, because td runs as a claude.ai custom
connector. That connector UI takes an OAuth client ID and secret and
has no field for a bearer token or a custom header, so a static token
cannot be used there at all. td is therefore an OAuth 2.1
authorization server as well as a resource server. This is not
optional and not a later nice-to-have.

Being its own authorization server is the right call here. There is one
user, no external IdP, and the authorize endpoint reuses the password
and TOTP login that already exists. What that means concretely:

```text
GET  /.well-known/oauth-protected-resource   RFC 9728, and its
                                             `resource` value must
                                             exactly match the MCP URL
GET  /.well-known/oauth-authorization-server AS metadata
GET  /authorize    auth code + PKCE, S256 only. Reject plain and
                   reject missing. Read the `resource` parameter.
POST /token        issue a signed JWT whose `aud` is that `resource`,
                   plus a refresh token
POST /register     DCR, or Client ID Metadata Documents on newer
                   clients
```

An unauthenticated request to `/mcp` returns 401 with
`WWW-Authenticate: Bearer resource_metadata="..."`. That header is the
whole discovery chain: without it the client never finds the
authorization server, and the usual symptom is the MCP endpoint seeing
traffic while the authorization server sees none.

Serving both roles from one binary behind one hostname is the simplest
arrangement and it removes a whole class of discovery failures. It puts
one requirement on the proxy: nginx-proxy-manager must pass
`/.well-known/*` through to td. It already intercepts
`/.well-known/acme-challenge/` for certificate issuance, and a rule
broad enough to swallow the rest of `/.well-known/` will break
discovery in a way that looks like an application bug. Check it before
debugging anything else.

Two more deployment facts. The redirect URI to allow is
`https://claude.ai/api/mcp/auth_callback`. And the MCP URL has to be
reachable from Anthropic's network, which it is, but it means this
endpoint is exposed to the internet by design rather than by accident.

Sign tokens with an asymmetric key and publish a JWKS endpoint from the
AS metadata. Keep two keys live so rotation does not invalidate every
session. The settings page lists OAuth grants next to the static
tokens, with the same revoke button, because claude.ai will be holding
a refresh token for your task list and you want one place to cut it
off.

On every request, validate signature, issuer, expiry, not-before,
scopes, and audience as an exact match against the resource. Audience
mismatch is the failure people hit, because it is what stops a token
minted for another server from being replayed at yours.

Two hard rules from the spec: never accept a token in a query string,
and do not implement `client_credentials`. A machine-to-machine grant
with no user in the loop is not supported.

Scopes map onto what already exists: `td:read`, `td:capture`,
`td:write`. Give the everyday assistant read plus capture, and keep
write for a token pasted deliberately. The consent screen lets you
grant less than a client asked for. Agent-created tasks carry
`actor = mcp:<name>` in the event log, so a bad batch is one `/undo`
loop away from gone.

Pin a spec revision in the code and say which one in the README. MCP
authorization has changed three times in a year, and the newest
revision landed on 2026-07-28: it made Protected Resource Metadata
mandatory, made Resource Indicators mandatory on clients, moved from
Dynamic Client Registration toward Client ID Metadata Documents, and
removed sessions and the initialization handshake. Check what the Go
SDK actually implements before choosing, because a spec that recent
will be ahead of its SDKs.

Prompt-injection matters here in a way it does not for your other
tools: a Jira description synced in from an external reporter becomes
text an agent reads. Do not let MCP tool output be treated as
instructions, and never auto-complete tasks based on synced content.

---

## 11. TUI

Bubble Tea plus Lipgloss. Layout, top to bottom:

```text
┌ td ─ Today ───────────────────────── inbox 1 ─ waiting 1 ─ overdue 1 ┐
│ filter: is:open src:local -is:inbox -is:snoozed                      │
├──────────────────────────────────────────────────────────────────────┤
│  [ ] 104 p1  Send Q3 numbers             @stacey #finance     Jul 30 │
│  [ ] 102 p2  Follow up on monday forms   @mikah #monday        Today │
│ ▾[ ] 101 p1  Renew wildcard cert         @stacey #certs   0/1  Aug 4 │
│    [ ] 113 p3  Draft cert renewal runbook  #certs              Aug 6 │
│ ▸[ ] 108 p2  Plan HR onboarding flow     @mikah #hr       2/5  Aug 7 │
├──────────────────────────────────────────────────────────────────────┤
│ a add  d done  z fold  w wait  / search  u undo  ? keys              │
└──────────────────────────────────────────────────────────────────────┘
```

Two chrome rows at the top, one at the bottom, everything else is list.
Detail opens as a full-screen replacement, not a split pane, since
splits stop working at 80 columns and on a phone.

Keymap is vim-flavored and identical in the web UI: `j/k` move, `g/G`
jump, `Enter` open, `Space` toggle done, `a` add, `e` edit, `d` done,
`x` drop, `w` waiting, `s` snooze, `p` priority, `t` tags, `@` people,
`/` search, `1-9` saved filters, `z` fold, `Z` fold all, `u` undo,
`?` help, `Esc` back.

### Mouse

The TUI takes mouse input, on by default. Not as a fallback for people
who cannot remember the keys: the same pointer affordances the web has,
in the terminal, so both clients behave the same way when your hand is
already on the trackpad.

What the pointer does:

- Click a row to select it. Double-click to open the detail view.
- Click the checkbox cell to toggle done. Click the fold cell to fold.
- Click a `#tag` or `@person` to filter by it. This is the one thing
  the mouse does better than the keyboard here, and it nearly justifies
  the feature on its own.
- Click a hint in the status line to run it. The bottom bar is a
  toolbar in both clients.
- Click the filter bar to edit it.
- Wheel scrolls the viewport without moving the selection, the same as
  every pager and the same as the web list.
- No right-click menu. Terminal emulators intercept the right button
  inconsistently, and the keyed menu already exists.

Three implementation rules:

- **Cell motion, not all motion.** All-motion reporting sends an event
  for every pointer move, which floods the event loop over SSH and buys
  only hover. There is no hover in the TUI by design.
- **Hit regions come from the renderer, not from column arithmetic.**
  Mark regions while drawing and test events against those marks.
  Recomputing columns in the input handler works until the first
  truncated title, then drifts silently. `lrstanley/bubblezone` does
  exactly this and requires alt-screen, which the TUI uses anyway.
- **Pin Bubble Tea v2 and use the declarative API.** v2 splits mouse
  events into separate click, release, wheel, and motion message types
  and moves mouse mode into a `View()` field rather than a program
  option. Training data is full of the v1 shape, so this is worth
  stating.

The cost is real and gets handled rather than discovered: capturing the
mouse takes the terminal's own text selection away from you. Most
emulators hand it back while shift is held. Say so in the `?` help, and
ship `--no-mouse` plus a `mouse = false` config key for the times that
is not good enough.

---

## 12. Web UI

Server-rendered Go templates plus htmx. No React, no build step, no
Tailwind. One hand-written stylesheet. The whole reason the JetBrains
dialog works is that it is plain rectangles and text, and that is easier
to hit with 400 lines of CSS than with a component library you keep
fighting.

Rules that produce the look:

- Monospace only. One family, two sizes at most. JetBrains Mono or IBM
  Plex Mono.
- Everything lands on a character grid. Horizontal spacing in `ch`
  units, vertical in a fixed row height. No fractional gaps.
- No gradients, no blur, no soft shadows, no depth.
- Border radius on containers, buttons, inputs, and rows is zero. The
  one exception is a control whose shape is its meaning: the pill track
  and round knob of a toggle. Rounding a rectangle is decoration.
  Rounding a knob is the affordance.
- Buttons are rectangles with a hard offset shadow, the way the
  reference image does it: solid block down and right, no blur. Pressed
  state moves the button into the shadow.
- Borders are 1px or 3px double rules, drawn with CSS borders rather
  than box-drawing characters, so text selection stays sane.
- Focus and selection are inverse video, never a glow or an outline.
  Two rules follow, both learned by shipping the bug. Form controls do
  not inherit `color`, so anything drawing itself with `currentColor`
  needs an explicit `color: inherit` or it vanishes the moment a row
  inverts. And de-emphasis is opacity, never a grey token: a fixed grey
  is legible on one background and invisible on the other, and each one
  costs an override per inverted context. Overdue is the single
  exception, and it gets a paired token rather than an opacity.
- Links are underlined. Always.
- One accent color for the primary action, matching the yellow "Accept
  All" treatment. Everything else is foreground, background, and two
  greys.
- Themes set colors and nothing else. See the theming rules below.
- The only animation is the caret. Instant state changes elsewhere.
- Every clickable thing shows its key hint. The bottom bar is a real
  status line, and it changes with context, exactly like the TUI.

### Control inventory

The reference dialogs settle the question of how far to take the
terminal metaphor. JetBrains did not draw `[x]` in ASCII for the cookie
categories. They used a real toggle, then stripped it: flat black track,
plain white knob, no travel animation, no drop shadow, no accent fill,
label in the same monospace at the same size as body text. The rule is
that the control keeps its native shape and gestures, and gives up its
material.

That rule runs in both directions. Where the web has a real control,
use it and strip it. Where the web has an affordance the terminal
lacks, take the affordance rather than pretending the browser is a
terminal: native checkboxes, hover, `<dialog>` for focus trapping and
Esc, `<input type="date">` on touch. What stays terminal is the visual
language and the keyboard model, not the widget implementations. Build every widget that way.

- **Toggle.** Pill track, circular knob, two states plus a locked state
  drawn with a grey knob (the "Necessary" treatment in the reference).
  Use toggles only for persistent settings: plugin enable, notification
  channels, theme, sync source on and off. Never for task state.
- **Task done.** A real `<input type="checkbox">` with `appearance:
  none`, square, 13px, 1px border, solid fill when checked, no
  checkmark glyph. Indeterminate means doing or waiting. The TUI draws
  `[ ]` because a terminal has no other option, and the web uses the
  native control so that label clicks, keyboard, and screen readers
  work without being reimplemented. Same control, two renderers, not
  the same glyph. Never a toggle in a list row: toggles are for
  settings.
- **Hover.** The web gets a hover state the TUI cannot have. Use it,
  once: the checkbox border thickens with an inset shadow so nothing
  moves on the grid. No row highlight, no transition.
- **Checkbox and radio.** Same treatment in forms. Square and round,
  1px outline, filled solid when selected.
- **Text input.** Underline only, no box. Block caret. On focus the
  field inverts.
- **Select.** No native dropdown. Open a bordered list panel anchored
  under the field, arrow keys move, Enter commits, same widget the TUI
  uses.
- **Priority.** A fixed 3ch column reading `p1` through `p4`, matching
  the `p:` token in the filter bar so the row teaches the grammar.
  Encode it in weight and value, never in hue: both color slots are
  already committed, amber to the one primary action per screen and red
  to overdue. The ramp is bold, normal, grey, faint, which works
  unchanged in a terminal. Unset priority renders blank.
- **Tag and person tokens.** No border, no fill, no padding. Dim is the
  entire treatment. `#certs` and `@stacey` keep their sigils, which
  already mark them as structured values and make the row read as the
  filter grammar. A box around every one turns a scannable list into a
  form.
- **Scrollbar.** A one-character gutter column with a block glyph, not
  the browser default. Overlay is fine on mobile.
- **Modal chrome.** Copy the reference exactly: double-rule border with
  the title inset into the top edge, breaking the line. That single
  detail does more for the terminal feel than anything else on the
  page, and it costs one pseudo-element.
- **Progress and counts.** Block characters, not arcs or bars with
  rounded caps.
- **Spinner.** There isn't one. Requests are local and fast. If
  something takes longer than 200ms, the status line says what it is
  doing in words.

Two accent rules that come out of the reference images. The primary
action is the only saturated element on the screen, and it stays filled
even at rest. Secondary actions are the same rectangle with an inverted
fill, so both read as buttons and the ranking is unmistakable. That is
how "Accept All" and "Accept Selected" differ in the second image
without a size or position change.

The visual system is not described here alone. `tokens.css` is the
system, `themes.css` holds the community palettes, and `mockup.html`
renders the home view, the control inventory, and the modal chrome
against real seed data. When this prose and those files disagree, the
files win, the same way `testdata/` wins over the prose elsewhere.

### Theming

Themes are user-selectable and the mechanism is nearly free, because
the token layer is already semantic. A theme sets around a dozen custom
properties and touches nothing structural. Ship Nord, Solarized Light,
Dracula, and Tokyo Night alongside the two built-ins, and read
additional themes from `$XDG_CONFIG_HOME/td/themes/*.css` so a palette
you like is a file drop rather than a pull request.

Three rules keep this from dissolving the design:

- **A theme sets colors only.** Never type, spacing, radius, shadow, or
  any structural token.
- **One accent.** A theme may replace the accent hue. It may not add a
  second one. One saturated value, one primary action per screen, in
  every theme.
- **Selection stays inverse video.** Dracula and Nord both define a
  "current line" background, and neither gets to use it here. Backing
  off inversion is what turns this into a normal web app with a
  monospace font.

Low-contrast palettes are why `--td-dim` is a token rather than a
constant. Nord and Solarized both need it above the 0.58 default or
task numbers and due dates fade out. Every theme file sets its own.

A theme must pass a contrast floor before it loads: 4.5:1 for ink on
paper, and 3:1 for ink at `--td-dim` on paper. Fail the check, log the
theme name, fall back to the built-in. This is a unit test, not a
runtime nicety, and it is the only thing standing between a dropped-in
palette and an unreadable list.

The TUI does not use theme files at all. It renders through the
terminal's own ANSI palette, mapping paper and ink to default
background and foreground and dim to bright black. If you run Tokyo
Night in your terminal, the TUI is already Tokyo Night, and a theme
file would only fight it.

Mobile: same grid, rows grow to a 44px minimum touch target, top bar
collapses to filter and inbox count, the bottom bar becomes the toolbar.
Long-press opens the action menu that `Enter` opens on desktop. Toggles
survive the shrink better than bracket checkboxes do, so on narrow
widths the settings screens keep toggles and the task list keeps
brackets with a widened tap area.

---

## 13. Notifications

ntfy, one topic, pushed from the server. The TUI and web UI never send
anything, so reminders work when nothing is running.

Per-task control is a tri-state, not a checkbox, because a checkbox
cannot express "whatever the default says". The task detail shows a
three-way control: `auto`, `on`, `off`. Default is `auto`, and `auto`
resolves against a policy you set once in settings.

The policy is a filter query. Section 6 already gave you a grammar, so
reuse it rather than inventing a settings screen full of dropdowns:

```toml
[notify]
topic         = "https://ntfy.example.com/td"
default_rule  = "p:<=2"        # auto-notify anything P1 or P2
lead_minutes  = 30             # before due_at
quiet_hours   = "22:00-06:00"  # hold until the window opens
date_only_at  = "08:00"        # when a date-only due fires
```

Set `default_rule = "*"` for always on, `""` for always off,
`"p:<=2 -#someday"` for anything in between. One expression covers
every case you listed, and it uses a language you already know from the
filter bar.

Firing rules:

- Only `due_at` fires a push in v1. No morning digest, no waiting-on
  nags, no inbox threshold. Add them later if the reminders alone are
  not enough, and add them as separate topics so you can mute one
  without muting the other.
- A date-only due fires at `date_only_at` on that day. A datetime due
  fires `lead_minutes` before.
- `notified_at` stops repeats. One push per task per due value. Change
  the due date and it becomes eligible again.
- Overdue does not re-push. A task nagging you every hour teaches you
  to swipe the notification away without reading it.
- Quiet hours hold the push, they do not drop it.
- ntfy actions carry the payload back: Done and Snooze 1h as action
  buttons, each hitting the API with a capture-scoped token, plus a
  click-through URL to the task in the web UI.

The scheduler is a single goroutine on a 60 second tick querying tasks
due within the window. No job queue, no cron container.

---

## 14. First run and empty states

An agent left alone will invent both of these, and they are the first
two things you will see.

### First run

`tdd` starts against an empty database and does not offer to create an
account over HTTP. Account creation is a command on the server:
`tdd account create`, which prompts for a password, prints the TOTP
enrolment URI, prints the recovery codes once, and exits. Until an
account exists, every route returns 503 with `no account configured`
and the login page says the same thing in one sentence.

Config resolves in this order: flags, then environment, then
`$XDG_CONFIG_HOME/td/config.toml`. Write a commented default file on
first start if none exists. The server refuses to start rather than
guessing when `base_url` is unset, because OAuth discovery, the ntfy
click-through, and the `resource` claim all depend on knowing its own
public URL.

Timezone comes from config, not from the container, and defaults to
`America/Chicago`. Every date-only comparison uses it. A container
running UTC while the user lives in Central is the single most likely
source of an off-by-one-day bug in this whole system.

### Empty, loading, error, offline

Every view needs four states and an agent will build one. Write them
as real content, not placeholders:

- **Empty is an invitation.** A list with no matches says what would
  put something there, and names the current filter. "Nothing matches
  `p:<=2 due:today`" beats "No results." An empty inbox is the one
  place to say something satisfied.
- **Loading is nothing.** Requests are local. If a response takes
  longer than 200ms the status line names what it is waiting on, in
  words. There is no spinner in this product.
- **Errors say what failed and what to do.** They do not apologize and
  they are never vague. A 409 on a stale edit says the task changed
  underneath you and offers to reload it.
- **Offline is a state, not an error.** The client shows it in the
  status line and keeps working from cache for reads. `td a` queues
  locally and flushes on reconnect. Everything else refuses with one
  line rather than pretending.

---

## 15. Auth, deploy, backup

It is going on the public internet behind nginx-proxy-manager, so the
auth surface is the part to get right first, not last.

Browser: one account, password hashed with argon2id, TOTP required at
enrollment rather than optional. Session cookie, `HttpOnly`,
`Secure`, `SameSite=Lax`, 30 day expiry with a sliding refresh.
Recovery codes generated once at setup, shown once, hashed at rest.
Lock the account for 15 minutes after 5 failed attempts, and count
failures against the password and the TOTP step separately.

Non-browser clients use bearer tokens and never touch the password
flow. Prefixed (`td_`), hashed at rest, scoped (`read`, `write`,
`capture`, `sync:planner`), individually revocable, with last-used
timestamps on a settings page. The TUI, each plugin, each MCP client,
and the ntfy action buttons each get their own.

Because it is public, a few things that would be optional on Tailscale
are not:

- No user enumeration and no registration route. One account, created
  by a CLI command on the server, no signup page at all.
- `/api` and `/mcp` return 401 with no body. The login page is the only
  route that renders anything to an unauthenticated request.
- Rate limit the login route at the app, not at the proxy. 10 attempts
  per IP per minute.
- HSTS, `Content-Security-Policy` with no inline script except the
  htmx bundle hash, `X-Frame-Options: DENY`.
- Attachment downloads go through the auth check every time. No
  guessable direct paths under a static file handler.
- Log every auth event to the `event` table with the source IP. You
  will want it the first time something looks odd.

SQLite in WAL mode, `busy_timeout=5000`, one writer. `VACUUM` weekly.

Backup: `litestream` to object storage is the right answer and costs
nothing at this size. Also ship `td export --json` and
`td export --markdown`, the second writing one file per task for
Obsidian. A task system you cannot get your data out of is a hostage
situation.

Container: multi-stage build, `CGO_ENABLED=1`, one volume at `/data`.
If you take the cgo SQLite driver, the runtime stage needs
`distroless/base` rather than `distroless/static`, since the binary is
no longer fully static. Health at `/healthz`, unauthenticated, no
detail in the body.

The client releases separately. Build `darwin/arm64`, `darwin/amd64`,
and `linux/amd64`, pure Go, no cgo, and let the version handshake warn
you when the laptop falls behind the container.

---

## 16. Build order

1. Schema, event log, task CRUD, filter grammar, `td` CLI one-shots.
   No UI. Prove the grammar against real data first.
2. Auth: account creation command, password, TOTP, tokens. Before
   anything is exposed, not after.
3. TUI: list, detail, add, complete, filters, undo.
4. Web UI at parity, plus the stylesheet that defines the look.
5. ntfy reminders and the `notify` tri-state.
6. People, groups, identity mapping, person page.
7. Recurrence, subtasks, triage mode, attachments.
8. MCP server with static bearer auth, read plus capture. This
   unblocks Claude Code immediately.
9. OAuth 2.1 authorization server. Required: the claude.ai connector
   cannot use a static token.
10. Memos completion webhook.
11. First sync plugin. Pick Planner over Jira: Planner has a cleaner
    Graph API.

Stop after step 5 and use it for two weeks before building step 6. Half
the requirements in this document will change.

---

## 17. Defaults chosen for you

These were open. Each has a default now, picked to be the version you
are least likely to regret. Override any of them.

1. **Recurrence catch-up: `skip`.** A missed daily task rolls to the
   next occurrence and logs the misses. Piling up backlog gets the
   whole list ignored.
2. **Memos integration: outbound webhook on `task.completed`.** The
   journal fills itself. No read path from Memos into tasks.
3. **Inbox never blocks the home view.** It shows a count in the top
   bar. Blocking modals in a tool you open fifty times a day get
   dismissed reflexively.
4. **Synced tasks are hidden from the default home filter.** Home is
   `is:open src:local -is:inbox -is:snoozed -is:deferred`, a saved
   filter rather than special-cased logic. A saved filter on `2` shows
   everything. Read-only
   mirrors of a Jira backlog will otherwise bury your own list on day
   one.
5. **Attachments cap at 25 MB per file.** Content-addressed, deduped,
   orphans collected weekly.

Still genuinely open: nothing blocking. It runs on the 8 GB Docker host.

One naming note. `td` is taken twice on PATH: Treasure Data's TD
Toolbelt (`gem install td`) and `td-cli`, a Python todo manager that
installs the same two letters. Neither is in coreutils and neither is
something you would install, so take the name. Do not take `~/.td/`,
which the Treasure Data toolbelt uses. Config goes to
`$XDG_CONFIG_HOME/td/config.toml`, falling back to `~/.config/td/`.
The server binary is `tdd`, which you type once in a Dockerfile
`ENTRYPOINT` and never again.

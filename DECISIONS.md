# Decisions

Every deviation from `BUILD-SPEC.md` and every ambiguity resolved
during the build. Newest first.

Format:

```text
## YYYY-MM-DD  short title
Phase: N
What the spec said, or did not say.
What was decided, and what was rejected.
How reversible it is.
```

Use a `FIXTURE-DISPUTE` heading when a `testdata/` case looks wrong.
Implement the fixture's behavior anyway and write the argument here.

---

## 2026-07-31  Editing a task is not in the build order, and the keymap now says so
Phase: 4

Section 11 gives the complete keymap, including `e` edit, `p` priority, and
`t` tags. Section 16's build order never schedules them. Phase 3 is "TUI:
list, detail, add, complete, filters, undo", and phase 4 is parity with
phase 3. Nothing later mentions editing a task.

Phase 3 papered over this by having those keys report "arrives in phase 4",
which was a guess. Phase 4 shipped without them, so the TUI was telling the
user something false, and the web help had guessed phase 5 for the same keys
so the two clients disagreed.

Both now say "not scheduled yet". The keys that section 16 does schedule keep
their phase: snooze with reminders in 5, people in 6, series in 7. A test
asserts `e` does not claim a schedule.

This is the gap being recorded rather than resolved. Deciding when editing
lands is a scope call, and section 16 says to stop after phase 5 and use the
thing for two weeks first, which is exactly the period that would settle it.

Reversible: it is a string until someone schedules the work.

## 2026-07-31  Solarized Light needed --td-dim raised to clear its own floor
Phase: 4

Section 12 sets a contrast floor a theme must pass before it loads: 4.5:1
for ink on paper, and 3:1 for ink at `--td-dim` on paper. It says that is a
unit test rather than a runtime nicety.

Written as specified, the test failed on a shipped theme. Solarized Light at
`--td-dim: 0.72` composites base01 over base3 to a grey that clears 2.92:1,
just under the 3:1 floor. Every other palette passes with margin: Nord
5.23:1, Dracula 6.12:1, Tokyo Night 5.09:1, and the two built-ins 4.05:1 and
5.49:1.

Raised it to 0.75 in `themes.css`. The arithmetic is in the comment beside
it: 0.74 is the first value that passes and 0.75 leaves a little.

This edits one of the three artifacts that outrank the prose, which is worth
being explicit about. It is not the artifact overruling the spec, though: the
floor and the palette are in the same file, and the file's own header says
`--td-dim` exists to be raised on low-contrast palettes and names Solarized
as one that needs it. 0.72 was an estimate that came up short. Nothing about
the palette's identity changed; one opacity did.

The alternative was to let the check reject Solarized Light at load, which is
the behavior the rule specifies for a failing theme. Rejected because section
12 also says to ship it, and a theme that ships and never loads is worse than
either.

Also worth recording: Solarized Light has the least headroom of any palette
here at 4.99:1 for ink on paper against a 4.5:1 floor. A future palette
tweak there is more likely than anywhere else to trip the check.

Reversible: one number.

## 2026-07-31  One stylesheet, assembled from the files that are the authority
Phase: 4

Section 12 says one hand-written stylesheet, and `CLAUDE.md` says
`tokens.css` and `themes.css` outrank the prose. Those pull slightly apart:
the system already lives in two files, and the app needs a third layer of
layout.

The browser fetches exactly one stylesheet, `/static/td.css`, assembled at
startup from tokens, themes, any user themes that cleared the floor, and an
app layer. The app layer introduces no colors, no radii, no shadows, and no
second type scale; everything in it is layout, and every value is a token.

`go:embed` cannot reach outside a package directory, so `internal/web/css`
mirrors the two root files. `TestEmbeddedCSSMatchesTheAuthority` compares
them byte for byte, which turns editing one and not the other into a build
failure rather than a drift nobody notices until the mockup and the app
disagree. `make sync-css` is the fix.

Rejected: making the root files the copies and the package files the
authority, which would move the artifact `CLAUDE.md` names out from under
the name it names it by.

Reversible: yes.

## 2026-07-31  No inline script at all, rather than an htmx hash
Phase: 4

Section 15 asks for a CSP with no inline script "except the htmx bundle
hash", which suggests inlining htmx and allowing its hash.

Nothing is inlined. htmx and the keymap are served as files, so
`script-src 'self'` covers both and the policy carries no hash at all. A
hash has to be recomputed whenever the file changes, and a stale one fails
in a way that looks like the script simply not running.

The first cut of the templates used `onchange="this.form.requestSubmit()"`
on the row checkbox and the theme radio, which would have forced
`unsafe-inline` back into the policy for two lines of convenience. A test
that scans every page for inline handlers and script bodies caught it; both
moved into a delegated listener in `td.js`.

Reversible: yes, and the test would fail first.

## 2026-07-31  The browser routes are not in openapi.yaml
Phase: 4

`openapi.yaml` is linted on every `make check` and a route without an entry
fails the build. The web UI adds a dozen routes.

None of them is documented there, and a test asserts that. `openapi.yaml`
describes the JSON API that clients, plugins, and MCP program against. The
server-rendered pages and their form posts are not an interface anything
integrates with, and documenting them would invite someone to try, then
constrain the markup as though it were a contract.

The route-coverage test now runs in both directions: every API route has an
entry, and no browser route does.

Reversible: yes.

## 2026-07-31  Every web action works with JavaScript off
Phase: 4

Section 12 specifies htmx, which implies the actions are JavaScript-driven.

Each action is a real form with a real action and method. htmx intercepts it
and swaps the list fragment; without htmx the form posts and the server
redirects. The `HX-Request` header is what picks between the two.

This is not primarily about supporting a browser with scripting off. It is
what makes the whole UI testable with an HTTP client, which is why there are
web tests at all rather than a note saying the browser UI is untested.

Reversible: yes, at the cost of those tests.

## 2026-07-31  A selected row is built flat, not flattened afterwards
Phase: 3

Section 12 says focus and selection are inverse video, never a glow or an
outline. The obvious way to do that is to render the row normally and then
invert it, stripping the inner escape sequences first so the inversion
applies to one run.

That is wrong twice, and the second one is what matters.

Inverting a string that already carries colors leaves the first inner reset
cancelling the inversion for the rest of the line, so the row inverts up to
its first tag and then stops. That is the visible bug.

The invisible one: `lrstanley/bubblezone` marks hit regions with escape
sequences embedded in the rendered string. Stripping a rendered row takes
every clickable region on it away, so the selected row would have been the
one row the mouse could not touch, and nothing about the screen would have
looked wrong. A test that clicked a tag on the selected row is what found it.

Rows are therefore built with a `paint` helper that applies a style or does
not, depending on whether the row is about to be inverted. Nothing is ever
stripped.

The consequence worth stating: a selected overdue row shows no red. Inverse
video is the selection treatment, and a second color inside an inverted run
is the thing the rule exists to prevent.

Reversible: yes, and the mouse test would catch a regression.

## 2026-07-31  Auth events, fold state, and what the TUI is allowed to see
Phase: 3

Fold state persists per task and section 3 says it must never be exported,
never synced, and never an event.

It lives in `ui_state`, reachable at `GET /api/v1/ui/folds` and
`POST /api/v1/ui/folds/{id}`, and it is deliberately not a field on
`api.Task`. Adding a `collapsed` field would have been one fewer round trip
and would have put view state inside the type that `td export --json`
serializes, where the rule against exporting it becomes something someone
has to remember. Keeping it off the type makes it structural.

Writing a fold emits no event, for the same reason: an export or a sync must
not be able to see that a row was folded on somebody's screen.

Reversible: yes, but doing so would put the export rule back at risk.

## 2026-07-31  Keys that are specified but not built say which phase
Phase: 3

Section 11 gives the complete keymap, and phase 3 builds list, detail, add,
complete, filters, and undo. That leaves `e`, `w`, `s`, `p`, `t`, `@`, and
`E` specified and inert.

Pressing one puts a line in the status bar naming what it would do and which
phase it lands in. `w wait` stays on the bottom bar in the position section
11 draws it.

A key that silently does nothing reads as a bug, and the person most likely
to press it is the one who just read the spec. Saying "phase 6" costs one
line and turns a apparent fault into a schedule.

Reversible: the entries come out of the keymap as each phase lands.

## 2026-07-31  OAuth 2.1 is in scope, and "no OAuth" means no federation
Phase: 1 (resolved for phase 9)

`CLAUDE.md` contradicts itself. Its "Do not build" list says "No
collaboration, no sharing, no second user, no OAuth". Four other statements
assume an OAuth authorization server exists: the v1 definition of done calls
the claude.ai connector "the end-to-end test for the OAuth work, and nothing
short of it counts"; two security assertions test `/authorize` PKCE handling
and exact audience matching; a third says "OAuth client registration is not
user registration and creates no account". `BUILD-SPEC.md` section 10 calls
it "not optional and not a later nice-to-have" and section 16 makes it step
9.

Built, as specced, in phase 9. Confirmed with the author.

The reading that makes the whole document consistent: "no OAuth" sits in a
list with "no collaboration, no sharing, no second user", and means no
third-party login and no user federation. Being your own authorization
server for a single account is a different thing, and it is the only way the
claude.ai connector works at all: that UI takes a client id and secret and
has no field for a bearer token.

Rejected: taking the prohibition literally, which would leave a v1 done
criterion permanently unmet and three security assertions testing code that
does not exist.

Reversible: it is a phase that either happens or does not. Deciding now
rather than at phase 9 matters because phase 2 designs the token and session
tables an authorization server later builds on.

## 2026-07-31  The account has a username
Phase: 1 (resolved for phase 2)

Section 15 says "one account" and section 14 has `tdd account create` prompt
for a password, saying nothing about a username. A security assertion in
`CLAUDE.md` then requires that "a login attempt with an unknown account and
one with a known account return the same status, the same body, and timings
within 50ms", which is only meaningful if an account can be named.

The account has a username. `tdd account create` prompts for one. Confirmed
with the author.

The assertion is what settles it: a test that distinguishes a known account
from an unknown one requires that accounts have names. Without a username
the login form is a bare password box, and that assertion becomes vacuous
rather than passing.

The implementation this commits to: an unknown username runs a dummy
argon2id verification with the same parameters as a real one, so the
response time does not reveal whether the account exists.

Reversible: dropping the field later is easy. Adding it later would mean a
migration plus re-enrolling.

## 2026-07-31  Auth events stay out of the change feed
Phase: 2

Section 15 says to log every auth event to the `event` table with the source
IP. Section 9 serves that table at `GET /events` as the change feed, and
section 10 has MCP's `recent_activity` read it.

Both, but not the same rows. Auth events go in the table, and `Events`
excludes anything with an `auth.` prefix. `tdd account log` reads them, on
the server, next to where the account is created.

The reason is who reads the feed. An MCP token is the least trusted
credential in the system and the one most likely to be handed to something
that summarizes what it sees. Login history and the IPs behind it are not a
change to anything and are no use to an agent, so putting them in the feed
would be a disclosure with no matching benefit. A read scope should mean
your tasks, not your security log.

This surfaced from a test rather than from reading: the phase 1 event
assertion started failing because setting up the harness created an account
and a token, and both showed up in the feed.

Rejected: a query parameter to include them, which needs a scope to guard it
and invents an admin role the spec does not have. Also rejected: a separate
table, which would cost the single ordered history that makes the event log
worth having.

Reversible: yes, it is one clause in one query.

## 2026-07-31  Both login factors arrive in one request
Phase: 2

Section 15 requires failures to be counted "against the password and the
TOTP step separately", which reads like two steps.

One request carries username, password, and either the authenticator code or
a recovery code. The handler verifies the password, and only then the second
factor, incrementing whichever counter the failure belongs to. Five of
either kind locks the account.

Every property the spec states holds. A two-step flow would add a
pending-login token, its storage, its expiry, and its own replay question,
and would buy nothing that is written down.

The one thing it gives up is telling the user which factor was wrong. That
is deliberate: the answer is the same status and the same body whatever
failed, which is what the no-enumeration assertion requires anyway.

Reversible: the second step could be added without changing the counters,
which are the part the spec pins.

## 2026-07-31  argon2id for the password, SHA-256 for everything else
Phase: 2

Section 15 says the password is argon2id and that tokens and recovery codes
are "hashed at rest". It does not say with what.

Passwords get argon2id at OWASP's 19 MiB, two iterations, one lane. Tokens,
session cookies, and recovery codes get SHA-256.

The difference is what the hash defends against. A slow KDF buys resistance
to guessing a low-entropy, human-chosen value. The others are 256 random
bits minted by the server, so there is nothing to guess, and a slow hash on
every API request would be a cost with no benefit: an MCP client polling the
change feed would pay 30ms of argon2 per poll to protect a secret that
cannot be brute forced.

The property the assertion actually wants holds either way, and is tested
directly: a database dump contains no usable credential.

Reversible: yes, though rehashing existing tokens would mean reissuing them.

## 2026-07-31  The health check is exempt from the no-account 503
Phase: 2

Section 14 says that until an account exists, "every route returns 503 with
`no account configured`". Section 15 separately specifies `/healthz` as
unauthenticated with no detail in the body, and the container uses it.

Everything answers 503 except `/healthz`, which answers 200.

A liveness probe reports whether the process is alive. An unconfigured
server is alive, and failing the probe would have the container restarted,
which does not create an account and does not help. The two statements are
about different things: one is the answer to a request for data, the other
is the answer to "are you running".

Reversible: one condition.

## 2026-07-31  A token carries the actor it writes to the event log
Phase: 2

Section 3 gives the event log actors of the form `me`, `mcp:claude`,
`plugin:jira`. Section 15 says each client gets its own token. Nothing says
how a token becomes an actor.

`api_token.actor` is set at creation and validated against those three
shapes. `tdd token create -name claude -actor mcp:claude -scopes read,capture`.

Deriving it from the token name was rejected: undo is already scoped by
actor, so a token that could claim an arbitrary actor string could reverse
another actor's work, and a name is a display label that should not carry
that weight. Section 10 wants a bad agent batch to be one `/undo` loop away
from gone, and that only holds if the agent's writes are separable.

Reversible: yes, it is a column.

## 2026-07-31  X-Forwarded-For is believed only from configured proxies
Phase: 2

Section 15 wants the login route rate limited at the app, ten attempts per
IP per minute, and notes the server sits behind nginx-proxy-manager. It does
not say how the client address is determined.

`-trusted-proxies` takes a CIDR list and defaults to empty. With nothing
trusted the immediate peer is the client. `X-Forwarded-For` is read only
when the peer is inside a trusted network.

Believing the header unconditionally would make the rate limit decorative:
a caller puts a fresh address in it on every attempt and never hits the
limit. The safe default is therefore to trust nothing, and the cost of that
default behind a proxy is that every request looks like the proxy and the
per-IP limit becomes one global limit. That fails toward locking out the
account holder rather than toward letting an attacker through, which is the
right direction, but it is not what anyone wants: the server logs a warning
at startup when it binds a non-loopback address with no trusted proxies
configured.

Reversible: it is configuration.

## 2026-07-31  A lockout expiring does not reset the failure counters
Phase: 2

Section 15 says five failed attempts lock the account for fifteen minutes.
It does not say what the counter does afterwards.

Only a completed login clears the counters. When the window expires the
account is usable again, but the counter still reads five, so the next
failure locks it immediately rather than granting four more tries.

The alternative turns the lockout into a rate limit of five attempts per
fifteen minutes, sustained forever. Since the legitimate user gets in on
the first try and clears the counter, the asymmetry costs them nothing.

Reversible: yes.

## 2026-07-31  The task list does not paginate
Phase: 1

Section 9 writes the list endpoint as `GET /tasks?q=&sort=&limit=&cursor=`.

`cursor` is not implemented and the endpoint returns the whole matched set.
`limit` stays, as a truncation to the top N rather than a page: it makes no
continuation promise, and `total` keeps reporting the untruncated count so a
caller can tell a slice from the answer. Section 10's `whats_next(limit)` is
what it exists for, and shipping 5,000 rows to an agent so it can take five
is worse than a cap.

This supersedes an earlier entry, made the same day, that shipped an
offset-based cursor and argued the instability was not yet worth fixing.
That argument stopped one step short. Ordering happens in Go, so a stable
cursor would have to encode sort position, and nothing needs one: a single
user reading a filtered list wants the list. The right move was to delete
the parameter, not to budget for repairing it.

What is left is the case that genuinely needs a cursor. `GET /events` is an
append-only log with a monotonic `seq`, so `since` is stable by
construction: a concurrent write appends past the cursor and can never shift
a row across it. Pagination lives there and nowhere else.

Reversible: adding a cursor back is easy, and would first require a reason
that the events feed does not already cover.

## 2026-07-31  The server can be pinned to the fixture clock, and tells clients its own
Phase: 1

`CLAUDE.md` says `make seed` loads `testdata/seed.json` "including the fixed
clock, so every fixture in that directory evaluates the way the case files
say it does". The first pass honored that in the test suite only: `make run`
served from the real wall clock, so the home view on a running server came
back in a different order than `sort_cases.json` specifies. That is correct
behavior for a real server and wrong against that sentence.

`tdd -now` pins the clock. It takes an RFC3339 instant, or `@<seed file>` to
lift both the instant and the timezone out of a fixture. `make run` passes
`-now @testdata/seed.json`; `make run-live` is the same server on the real
clock. Pinning logs a warning at startup, because every date predicate and
the entire sort order depend on it and a server left running that way
answers plausible nonsense.

Fixing that surfaced a second, real bug. The client rendered relative date
labels ("Today", overdue) from its own `time.Now()`, so a client and server
in different timezones disagree about which tasks are due today, on a list
the server already ordered. That breaks the v1 criterion that the TUI and
the web UI show the same list for the same filter, and it would have been
found in phase 3 or 4 rather than here.

Every response now carries `X-Td-Now` with the server's instant in its
configured zone, and the client renders against it. The server's zone is
authoritative because it is what the sort order already used. A missing or
unparseable header falls back to local time rather than failing.

Rejected: a `timezone` key in the client config, which is already there and
defaults to empty. It cannot be the answer, because the client would then
have to be configured correctly to agree with a list it did not compute.

Reversible: yes. The header is additive and an old client ignores it.

## 2026-07-31  Pure-Go SQLite driver instead of the cgo one
Phase: 1

Section 1 argues for the mainstream cgo SQLite driver, on the grounds that
the server only ever builds inside a Docker stage so `CGO_ENABLED=1` is free
there, and that a pure-Go driver would cost full FTS5.

Taken `modernc.org/sqlite` instead. Two reasons, one hard and one soft.

The hard one: `make check` builds `linux/amd64`, and CI is the same command
as the local one. With a cgo driver, `make check` on the Mac cannot build
the server for Linux without a C cross-toolchain, so the local and CI
definitions of passing would have to differ. `CLAUDE.md` says there is no
second definition of passing, and that outranks a driver preference.

The soft one: the premise about FTS5 no longer holds. `modernc.org/sqlite`
ships the full amalgamation with FTS5 compiled in. Verified before
committing to it: external-content FTS5 tables, prefix queries (`cert*`),
quoted phrase queries, and user-defined scalar functions all work.

Consequences: the runtime stage is `distroless/static` rather than
`distroless/base`, and the server cross-compiles like the client does.

Reversible: the driver sits behind `database/sql` and the SQL is portable
between the two. Swapping back is an import change plus a Dockerfile edit,
and `td_local_date` would need re-registering through the other driver's
hook.

## 2026-07-31  Filtering happens in SQL, ordering happens in Go
Phase: 1

Section 4 says the default comparator must be one function shared by the API
and both UIs. Section 9 gives the list endpoint a cursor. Those pull in
opposite directions: a shared Go comparator and a SQL `ORDER BY` cannot both
be the single source of order.

Filtering compiles to SQL. Ordering runs in Go, in `query.Sorter`, over the
full matched set, and the list endpoint returns that set whole.

Rejected: an `ORDER BY` mirroring the comparator, which is the drift the
"one function" rule exists to prevent, and would have to encode the
`td_local_date` bucketing in SQL a second time.

The cost is loading every matching row. Against the stated target of 5,000
tasks that is a few milliseconds and well inside the 25ms p95 budget. If it
stops being true, the fix is a materialized sort key, not a second
comparator.

Reversible: yes, and the comparator's own test would catch a divergence.

## 2026-07-31  Undo restores field values directly and does not re-run the state machine
Phase: 1

`transition_cases.json` gives a closed state machine and, separately, an
undo contract covering "status transitions". It does not say which wins when
they disagree, and they do: the fixture allows `inbox -> done` as a
quick-complete, and reversing it means moving a done task back to inbox,
which the same table forbids as a forward move.

Undo writes the `from` side of the event patch straight onto the row. It
does not go through `lookupTransition`.

The reasoning: the state machine constrains what the user may do next. Undo
is not a next move, it is the removal of a previous one, and a reversal that
cannot restore the state it came from is not an undo. Making the machine
symmetric instead would mean allowing `done -> inbox` as a forward move,
which contradicts the fixture directly.

Covered by `TestUndoBypassesTheStateMachine`.

Reversible: yes, but reversing it would break undo for any transition whose
inverse is not itself legal.

## 2026-07-31  Undoing a create drops the task
Phase: 1

The undo contract lists `create` as undoable. Section 9 says `DELETE` means
`status=dropped` and never a hard delete, and the event log holds a
`task_id` pointing at the row.

Undo of `task.created` sets the status to `dropped`. It does not delete.

Rejected: a hard delete, which would orphan the event rows that describe it
and break the one rule the whole history table rests on.

The visible cost is that undoing a mistyped `td a` leaves a dropped row
rather than nothing. `is:open` hides it, and the activity feed showing what
you abandoned is the stated reason `dropped` exists at all.

Reversible: yes.

## 2026-07-31  Moving to waiting always needs a person, not only from todo
Phase: 1

`transition_cases.json` puts `requires: waiting_on references an existing
person` on `todo -> waiting` and leaves `doing -> waiting` with no
`requires` clause at all. Section 4 says flatly that `waiting` needs the
person link.

Applied the requirement to both edges. The fixture is silent on
`doing -> waiting` rather than contradicting the prose, and silence is not
disagreement. A waiting task with nobody attached cannot answer the query
the state exists for, which is "waiting on Mikah since the 12th".

If this turns out to be wrong it becomes a `FIXTURE-DISPUTE`, and the
argument against would be that the fixture enumerates requirements
exhaustively. It does not read that way: the same file omits `clears` on
edges that plainly clear nothing.

Reversible: one field in the `transitions` table.

## 2026-07-31  Tag changes ride in the event patch under a pseudo-field
Phase: 1

The undo contract lists "tag and person links" as undoable. Tags live in
`task_tag`, so a field-level diff of the `task` row cannot see them.

A patch that changes tags carries a `tags` entry whose `from` and `to` are
the full tag lists. `reverseFields` routes that one name through `setTags`
rather than into the `UPDATE`. The event kind becomes `task.tagged` when
tags were the only thing that moved.

Person links have no mutation path yet: people are set at creation, nothing
in phase 1 edits them, and undoing a create drops the task, which covers the
only way they can currently appear. Phase 6 gets the same treatment when it
adds person editing. Recorded as deferred in `BUILD-LOG.md` rather than left
implicit.

Reversible: yes.

## 2026-07-31  person.handle and person_group.handle added to the schema
Phase: 1

Section 3 gives `person` the columns `id, name, email, notes` and
`person_group` the columns `id, name`. Section 6 then specifies `@mikah` and
`grp:leadership` as filter tokens. There is nothing in either table for
those to match: a name is display text and is neither unique nor typeable.

Added `handle TEXT UNIQUE NOT NULL` to both. `seed.json`'s person `key` and
group `key` load into it, which is evidently what those keys are.

Rejected: matching `@mikah` against `lower(name)`, which breaks on the first
person with a space in their name and cannot be made unique.

Reversible: a migration, and every saved filter naming a person or a group
would need rewriting. Cheap now, expensive later, which is why it is here
rather than deferred.

## 2026-07-31  Date comparison goes through a registered SQL function
Phase: 1

Section 14 says a container on UTC while the user lives in Central is the
likeliest source of an off-by-one-day bug, and the fixture's date rules all
resolve in `seed.timezone`. A due value is either a bare `YYYY-MM-DD` or an
RFC3339 instant, and a date predicate compares the calendar date either way.

`td_local_date(value)` is registered on the driver and does the reduction in
Go, through one implementation (`query.LocalDate`) shared with the
comparator. Date predicates stay inside the `WHERE` clause instead of
pulling every row into Go to be filtered.

The constraint this creates: `modernc.org/sqlite` registers scalar functions
per process, not per connection, so one process serves one timezone. That
matches the config file, which has one `timezone` key, and it is stated on
the `activeLoc` declaration so the next person meets it before it bites.

Rejected: a stored `due_date_local` column, which is correct until the
timezone changes and then silently is not. Also rejected: a fixed UTC offset
in SQL, which is wrong twice a year.

Reversible: yes, at the cost of moving date filtering into Go.

## 2026-07-31  The server refuses a non-loopback bind until phase 2
Phase: 1

Section 16 puts auth at step 2, "before anything is exposed, not after".
Phase 1 therefore has an API with no authentication in front of it, and
nothing in the spec says what stops it being started on `0.0.0.0`.

`tdd` refuses to bind anything but a loopback address. The container, which
has to bind `0.0.0.0` inside its own namespace to be reachable at all,
passes `-allow-unauthenticated-bind` and publishes its port to the host's
loopback only. The flag is long to type on purpose and greppable on purpose;
phase 2 deletes it.

Rejected: a plain default bind plus a note in the README, which is the
accident this exists to prevent. Also rejected: a `REVIEW:` marker, which
`CLAUDE.md` forbids on a default code path and which would not have stopped
anything anyway.

Reversible: the guard and the flag both go away in phase 2.

## 2026-07-31  internal/server holds the HTTP surface
Phase: 1

Section 1 lists the package layout and names no package for routing and JSON
encoding. Putting it in `cmd/tdd` would work and would make the handlers
untestable without starting a process.

Added `internal/server`. It is server-only, imports `internal/store`, and is
listed in the import-boundary test's forbidden set for `cmd/td` alongside
`internal/store` itself.

Reversible: trivially.

## 2026-07-31  Full weekday names resolve alongside the three-letter forms
Phase: 1

`filter_cases.json` lists the date vocabulary as `mon` through `sun` and
pins how they resolve. `BUILD-SPEC.md` sections 6 and 7 both write
`due:friday` in their own examples.

Both spellings resolve, by the same rule. Every fixture case is untouched
and still passes; this only adds spellings the fixture does not mention.

Reversible: one map.

## 2026-07-31  AST JSON shape for the nodes the fixture does not pin
Phase: 1

`filter_cases.json` gives four `ast_cases`, fixing the shape for `and`,
`or`, `not`, `is`, `tag`, `person`, `priority`, and `phrase`. It says
nothing about `due`, `start`, `src`, `has`, `notify`, `grp`, or a bare word.

Followed the pattern the pinned nodes establish: one key naming the
predicate. Dates render as `{"due": {"op": "<=", "value": "2026-08-07"}}`,
matching the `priority` shape, with the value already resolved to a calendar
date. Free text renders as `{"word": "cert"}`.

Reversible: yes, until something outside this repo parses the AST. Nothing
does.

## 2026-07-31  Date keywords resolve at parse time, not at evaluation time
Phase: 1

A parsed filter has to be reusable, and `due:tomorrow` means different dates
on different days. The alternative is carrying the keyword through the AST
and resolving during evaluation.

`ParseAt(query, now)` resolves keywords immediately and stores the calendar
date in the node. `Parse(query)` is the wall-clock form. The server passes
its configured timezone, which is what makes "today" mean the user's today
rather than the container's.

This makes an AST a snapshot rather than a standing query: a saved filter is
stored as text and re-parsed on each use, which is what `saved_filter` does.
The alternative was rejected because validation has to happen at parse time
regardless (`due:nextweek` is a parse error per the fixture), and splitting
validation from resolution means defining the vocabulary twice.

Reversible: yes, and it would matter if a filter were ever compiled once and
held for days.

## 2026-07-31  The capture parser is lenient where the filter parser is strict
Phase: 1

Section 7 says inline capture uses the same tokens as the filter grammar and
that "anything the parser does not recognize stays in the title". Section 6
says an unrecognized `key:value` is a parse error.

Both, in their own places. `query.ParseAt` refuses `foo:bar`;
`query.ParseCapture` puts it in the title. A filter with a typo should say
so. A captured thought should never be refused, and `foo:bar` inside a task
title is far more likely to be part of the thought.

`p:9` and `due:nextweek` follow the same rule in capture: they become title
text rather than errors.

Reversible: yes.

## 2026-07-31  Subtask tag inheritance happens only when no tags were given
Phase: 1

Section 4 says subtasks inherit nothing at creation except tags, "and only
as a copy you can then edit". It does not say what happens when the create
request already carries tags.

An explicit tag list wins. The copy only happens when the request supplies
none. Overwriting a caller's explicit list with the parent's would be
inheritance rather than a copy, which is the thing that sentence rules out.

Reversible: yes.

## 2026-07-31  Seeding writes no events and reuses fixed ids
Phase: 1

`make seed` loads a fixture that pins task numbers and `created_at` values.
Going through `Create` would assign its own, and would fill the event log
with fourteen creations that never happened.

`Store.Seed` writes rows directly and appends nothing to `event`. A seeded
database has an empty activity feed. ULIDs come from a counter-based entropy
source, so two loads of the same fixture produce the same ids and a diff of
two exports stays readable.

Covered by `TestSeedWritesNoEvents` and `TestSeedIsReproducible`.

Reversible: yes.

## 2026-07-31  is: vocabulary extended with todo, doing, and dropped
Phase: 1

Section 6 lists `is:open is:done is:waiting is:inbox is:orphan`, and the
fixture additionally uses `is:snoozed` and `is:deferred`. There was no
written way to ask for the three remaining statuses.

Added `is:todo`, `is:doing`, and `is:dropped`. No fixture case is affected;
this fills gaps in a list that was already open.

Reversible: yes.

## 2026-07-31  nothing_to_undo answers 404
Phase: 1

Section 9 gives `POST /undo` no failure modes. An empty log needs a status.

404: the request named a resource, the caller's newest reversible change,
and there is not one. 409 was rejected because nothing is in conflict, and
200 with an empty body was rejected because a client cannot tell it from
success.

Reversible: yes.

## 2026-07-31  Default saved filters ship five, not four
Phase: 1

Section 6 names four defaults: Today, Inbox, Waiting, Overdue. Section 17.4
separately says a saved filter on slot 2 shows everything, including the
synced mirrors the home view hides.

Shipped both: Today on 1, Everything on 2, then Inbox, Waiting, Overdue.
Section 17 is the more specific statement and pins a slot number, so it
wins.

Reversible: yes.

## 2026-07-31  Go toolchain pinned to 1.26.5
Phase: 1

`govulncheck` reported GO-2026-5856 against `crypto/tls` in go1.26.4, on a
path reachable from `http.Server.ListenAndServe`. `CLAUDE.md` says a finding
fails the build.

`toolchain go1.26.5` in `go.mod`. The finding clears. The Dockerfile pins
the same version so the container and CI cannot drift apart.

Reversible: it is a version bump.

## 2026-07-31  goconst is off in golangci-lint
Phase: 1

Every other linter in the enabled set earns its place. `goconst` fired
fourteen times, all of them on SQL column names (`notes`, `completed_at`) or
on the filter grammar's own keywords (`due`, `has`), where a named constant
is strictly harder to read than the literal it replaces. The rationale sits
on the line that disables it.

Reversible: one line.

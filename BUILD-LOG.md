# Build log

One entry per phase: what shipped, what was learned, what was
deferred. Narrative, not a changelog. Written as the phase closes,
not reconstructed later.

---

## Phase 1: schema, event log, task CRUD, filter grammar, CLI one-shots

2026-07-31

### What shipped

The whole of step 1 in section 16, plus the scaffolding the later phases
will not have to stop and build.

`internal/query` holds the grammar. A hand-written lexer and recursive
descent parser, the date vocabulary, the default sort comparator, the
display-order arrangement that lifts a subtask under its parent, and the
lenient capture parser quick-add uses. Both binaries import it, which is the
point: the client parses a filter locally to report a typo before sending
anything, and the server parses the same string to build SQL.

`internal/store` holds SQLite. The schema from section 3, migrations,
external-content FTS5, the AST-to-SQL compiler, task CRUD, the status state
machine, and the event log with undo on top of it.

`internal/server` serves `/api/v1`: tasks, people, saved filters, the change
feed, and undo. `internal/client` is the HTTP client, config resolution, and
the offline queue. `cmd/td` is the one-shot CLI, `cmd/tdd` is the server.

Around that: `make check` in seven steps with CI running the same command,
`openapi.yaml` with a lint that checks it against the mux, the import
boundary test, a Dockerfile, and a compose file.

### Where the fixtures were the whole value

All twenty query cases in `filter_cases.json`, both ordering cases in
`sort_cases.json`, and all twenty-five edges of the state machine passed
against an implementation written from the fixtures rather than adjusted to
fit them. Nothing in `testdata/` was edited, and there is no
`FIXTURE-DISPUTE`.

Two of the three traps the README warns about were live risks and got
handled by reading rather than by debugging. OR precedence falls out of a
correct recursive descent parser, so `is:open #monday | #finance` returned
four rows on the first run. Bucket-outranks-priority is a comparator that
reads its keys in the stated order, which is easy once written down and easy
to get wrong from a screenshot. The third, the DST wall clock, is recurrence
and belongs to phase 7.

The date keyword rule was the one that would have been silently wrong. A
bare weekday means the next occurrence strictly after today, *except* when
today is that weekday. The seed clock is a Monday and `mon` resolves to that
same Monday. Rolling forward a week there is exactly the wrong answer that
looks right, and it is why that fixture exists.

### What was learned

**The cgo argument in section 1 has expired.** It reasons that the server
only builds inside Docker, so cgo is free, so take the mainstream driver and
get FTS5. Both halves have moved: `modernc.org/sqlite` ships FTS5, and cgo
is not free once `make check` has to cross-compile `linux/amd64` from a Mac.
Verified the FTS5 claim with a throwaway program before committing to it
rather than trusting either the docs or memory.

**Two rules in the spec quietly conflict, and the conflict is real.** The
comparator must be one shared function, and the list endpoint has a cursor.
A shared Go comparator and a SQL `ORDER BY` cannot both be the source of
order. Filtering in SQL and ordering in Go resolves it and costs a full
result-set load per query, which is nothing at the stated scale.

**The cursor on `/tasks` was habit, not a requirement.** It shipped first as
an offset, with a note admitting a concurrent write could shift rows between
pages and arguing that was not yet worth fixing. That is the shape of an
argument that should have ended a step earlier. Ordering in Go means a
stable cursor has to encode sort position, and nothing wants one: a single
user reading a filtered list wants the list. It was removed the same day.
`limit` stays as a top-N truncation with `total` reporting the real count.
Pagination now exists only on `/events`, where a monotonic `seq` over an
append-only log makes `since` stable by construction. Worth remembering when
the next endpoint gets query parameters by reflex.

**Undo cannot be a forward move.** The state machine forbids `done ->
inbox`, and the fixture allows `inbox -> done` as a quick-complete. Undoing
that completion has to move a done task back to inbox. Undo therefore writes
prior values directly rather than routing through the machine. It took
writing the test to see that the two halves of one fixture file disagree
about this, and the disagreement is not a mistake in the file: they are
answering different questions.

**Two schema columns had to be invented before anything could match.**
Section 6 specifies `@mikah` and `grp:leadership`; section 3 gives neither
table anything to match them against. A `handle` column on both is the only
answer that is unique and typeable, and it had to be decided at the schema,
not worked around at the query.

**Date handling has exactly one correct place to live.** `query.LocalDate`
is shared by the comparator and by the `td_local_date` SQL function, so
there is one implementation of "what calendar date is this, here". Section
14 is right that a UTC container and a Central user is the likeliest bug in
the system, and the way to not have it is to make the question answerable in
one place.

### What was deferred, and why

- **Person-link undo.** The undo contract covers person links. Phase 1 has
  no path that edits them: people are set at creation, and undoing a create
  drops the task. Tags needed the pseudo-field treatment now because
  `PATCH` can change them; people get the same treatment in phase 6, when
  there is something to reverse.
- **`GET /events` as SSE or long-poll.** Section 9 offers either. Phase 1
  ships the plain paged read, which is what the MCP change cursor needs.
  The streaming form waits for a client that would use it.
- **`POST /api/v1/sync/{source}` and the people write endpoints.** Phases
  6 and 11.
- **Attachments.** The table, the count, and `has:attachment` all work; the
  upload route and the content-addressed blob store are phase 7.
- **`series`, `ui_state`, and `person_identity`** exist as tables with no
  code behind them yet. They are in the first migration because adding a
  column to a live database is more work than shipping it empty, and
  `recurrence_cases.json` already fixes the behavior phase 7 has to hit.
- **`sort=` on the list endpoint.** Section 9 lists it and section 4 says
  the default order applies "when the user has not picked one". Nothing can
  pick one yet, since choosing a sort is a UI affordance and there is no UI.
  It lands with the TUI in phase 3.
- **Performance measurement.** The targets are stated against 5,000 tasks
  and 20,000 events. The seed is fourteen tasks. A generator and a benchmark
  belong with the first phase that could plausibly miss a target.

### Notes for phase 2

Auth is the next step and it is the one that removes
`-allow-unauthenticated-bind` from `cmd/tdd` and from `docker-compose.yml`.
`Server.actor` returns a hardcoded `"me"` and is the seam the token identity
drops into; the event log and undo are already scoped by actor, so an
`mcp:claude` or `plugin:jira` token gets correct undo isolation for free.
There is a test covering that today, using those exact actor strings.

The security assertions in `CLAUDE.md` are phase 2's acceptance criteria,
and none of them are satisfied yet. That is expected at this point and is
what the loopback guard is standing in for in the meantime.

---

## Phase 2: auth

2026-07-31

### What shipped

Section 16 step 2, in full: account creation as a server-side command,
argon2id passwords, TOTP required at enrollment, one-time recovery codes,
browser sessions, scoped API tokens, lockout, and an app-level login rate
limit. Every route but the health check and the login pair now needs a
credential.

`internal/auth` holds the primitives with no HTTP and no SQL in them, so
each can be tested for the property it is supposed to have rather than
through a request. `internal/store` gained the account, session, token,
recovery code, and login attempt tables. `internal/server` gained the
middleware chain: security headers, then the no-account 503, then
authentication, then the mux.

`tdd account create` is the whole of first run. It prints the enrolment URI
and the recovery codes exactly once, because nothing can print them again.
`tdd token create` mints the credential the CLI uses. `tdd account log`
reads the auth history.

### The assertions

The eleven security assertions in `CLAUDE.md` that are not OAuth are now
eleven tests in `internal/server/security_test.go`, named after what they
assert. The two remaining ones, exact audience matching and the `/authorize`
PKCE rules, belong to the authorization server in phase 9.

Two were more interesting to write than to read.

**No enumeration.** The test measures the median of seven login attempts
against a known username and seven against an unknown one, and requires the
difference under 50ms. It also fails if either median comes in under a
millisecond, because that would mean no hashing happened and the test would
be passing for the wrong reason. Making it pass is one line: verify the
supplied password against a throwaway hash when the username is unknown.
Without it the unknown path returns in microseconds and answers the question
directly.

**Counters that do not pool.** "Failed TOTP attempts count separately from
failed passwords" only has a testable consequence in one place: four
failures of each kind must not lock the account, even though eight failures
happened. That is the test.

### What was learned

**The change feed is read by the least trusted thing in the system.** The
phase 1 event test started failing because creating an account and a token
wrote auth events, and they showed up in `GET /events`. The fix was not to
adjust the test. An MCP token is the credential most likely to be handed to
something that reads and summarizes, and login history with source IPs is
not a change to anything. Auth events stay in the table and out of the feed,
and `tdd account log` reads them on the server. A read scope should mean
your tasks, not your security log.

**Flags before subcommands, both ways.** The first cut dispatched
subcommands only when the first argument was not a flag, which meant
`tdd -db /data/td.db account create` fell through to the serve path and
died on the missing `-base-url`. That is exactly how the Makefile invokes
it. Parsing flags first and dispatching on the remaining arguments handles
both orders, which is what Go's flag package is shaped for.

**A hash is not one thing.** Section 15 says the password is argon2id and
that tokens and recovery codes are hashed, without saying with what. Using
argon2id for all of them would have put 30ms on every authenticated request
to protect 256 random bits that cannot be guessed. Passwords get the slow
KDF; server-minted secrets get SHA-256.

### What was deferred, and why

- **The login page.** There is a `POST /login` that issues the cookie, and
  no HTML. The web UI is phase 4 and owns the stylesheet that defines the
  look; a placeholder page now would either be thrown away or, worse, kept.
  The session mechanism is tested through the JSON endpoint.
- **The settings page.** Section 15 wants tokens listed with last-used
  timestamps and a revoke button. `GET /api/v1/tokens` and
  `DELETE /api/v1/tokens/{id}` are the routes behind it, and
  `tdd token list` reads the same data until there is a page.
- **TOTP replay inside a period.** A code stays valid for its window, so the
  same code can be used twice within 30 seconds. Nothing in the assertions
  covers it, and the fix is a last-used-counter column. Worth doing before
  the server is genuinely public.
- **Session purge on a schedule.** `PurgeExpiredSessions` exists and nothing
  calls it. It belongs with the scheduler goroutine in phase 5, which is the
  first thing in this design that runs on a tick.
- **Password change.** There is no route and no command. The spec says no
  password reset route, which is about the unauthenticated flow; changing a
  password while logged in is a different thing and is not asked for yet.

### Notes for phase 3

The TUI reads a token out of `config.toml` and sends it in the Authorization
header, which the client already does. `td whoami` reports what that
credential may do, which is the thing to show in the status line when a
write is refused.

`Server.actor` is gone: a mutation now writes the calling token's actor, so
an `mcp:claude` token's writes are already separable from your own and undo
is already scoped correctly. The test for that uses those exact strings.

---

## Phase 3: the TUI

2026-07-31

### What shipped

Section 16 step 3: list, detail, quick-add, complete, filters, and undo, plus
fold and the full mouse surface from section 11. `internal/tui` is
client-only and talks to the same HTTP API everything else does.

The layout is section 11's: two chrome rows at the top, one at the bottom,
everything else list, inside a box with the title inset into the top rule.
Detail is a full-screen replacement rather than a split pane. `td` with no
arguments opens it; the one-shot commands are unchanged.

Fold state moved server-side into `ui_state`, reachable at
`/api/v1/ui/folds`, so it follows you between the TUI and the web UI when
phase 4 lands.

### Checking the library before writing against it

`CLAUDE.md` says to read the current documentation for Bubble Tea v2 rather
than trusting training data. That was worth doing three times over.

The module moved from `github.com/charmbracelet/bubbletea/v2` to
`charm.land/bubbletea/v2`, and the old path does not resolve at all. Nothing
in the warning mentioned that, and no amount of care about the *API* would
have caught it.

The two things the warning did name both checked out. `View` is a struct,
not a string, and `MouseMode` and `AltScreen` are fields on it rather than
program options. Mouse input is four separate message types. A test asserts
the returned View carries `MouseModeCellMotion`, so a regression to the v1
shape fails rather than compiling into something that quietly reports all
motion.

### What was learned

**Stripping styles to invert a row also strips the mouse.** Selection is
inverse video, and the natural implementation renders the row, strips its
escape sequences, and inverts the result. bubblezone marks hit regions with
escape sequences inside the rendered string, so stripping takes them with it.
The selected row would have been the one row the mouse could not click, and
the screen would have looked completely correct. A test that clicks a tag on
the selected row is what caught it. Rows are now built flat when they are
about to be inverted, and nothing is ever stripped.

**A test can double-scan itself into failing.** `View` calls `zone.Scan` on
the composed frame. The first mouse tests scanned the returned frame again,
which consumes marks that are no longer there and leaves stale coordinates.
The rule is that the render scans and the test only reads.

**Columns beat a ragged edge.** The first cut right-aligned the whole
metadata group, which left the tags ragged while the dates lined up. Section
11 draws them as columns, and "everything lands on a character grid" is a
rule about exactly this. Title, tokens, child count, and due date each get a
fixed column now, and a test asserts the date column ends at the same cell on
every row that has a date.

**16ms is not close.** Keystroke to redraw measures 378µs mean and 762µs
worst on the fixture list, and 849µs mean at 200x60. The budget has about
twenty times the headroom it needs, which is what you would hope for from a
render that is string concatenation.

### What was deferred, and why

- **`e`, `p`, `t` edit paths.** Phase 4, with the web UI, because the two
  clients should get the same editing model at the same time rather than the
  TUI inventing one first.
- **`w`, `@`, and the person page.** Phase 6. `w wait` stays on the bottom
  bar in the position section 11 draws it, and pressing it says so.
- **`s` snooze.** Phase 5, with reminders, since snooze only means something
  once something fires.
- **Triage mode.** Phase 7. It is a mode rather than a view and wants the
  single-key actions that phases 4 to 6 add.
- **A live change feed.** The TUI reloads after its own writes and on `r`.
  `GET /events` exists and nothing subscribes to it. That is the phase 5
  scheduler's shape, and doing it now would mean inventing the polling loop
  twice.
- **Offline read cache.** Section 14 says reads fall back to cache. The TUI
  keeps showing the list it already has and says it is offline, which is the
  visible half. A cache that survives restart is a phase 4 concern once
  there is somewhere to put it.

### Notes for phase 4

The web UI is at parity with this, so the shapes to reuse are the row
columns, the empty states, and the keymap. `internal/query.Arrange` already
produces display order for both, and the fold endpoints are shared.

`tokens.css`, `themes.css`, and `mockup.html` are the authority for the web
look, and outrank the prose. The TUI deliberately reads none of them: it
renders through the terminal's own ANSI palette, so a theme file would only
fight the terminal.

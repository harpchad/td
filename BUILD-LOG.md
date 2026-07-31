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

---

## Phase 4: the web UI

2026-07-31

### What shipped

Section 16 step 4: the browser UI at parity with the TUI, plus the
stylesheet that defines the look. Server-rendered Go templates, vendored
htmx, one stylesheet, no build step.

Home, detail, help, settings, and the login page phase 2 deferred here.
Quick-add, complete, reopen, drop, undo, fold, filters, and the theme
picker. The keymap is the TUI's, key for key.

`tokens.css`, `themes.css`, and `mockup.html` were read first and treated as
the authority they are. The markup is the mockup's, class for class: a test
lists the classes tokens.css styles and fails if a page stops using one.

### The contrast floor found something

Section 12 says a theme has to clear 4.5:1 for ink on paper and 3:1 for ink
at `--td-dim`, and that this is a unit test rather than a runtime nicety.
Written as specified, it failed on a shipped palette.

Solarized Light at `--td-dim: 0.72` composites to 2.92:1, just under the
floor. Every other theme passes with room: Nord 5.23, Dracula 6.12, Tokyo
Night 5.09, the built-ins 4.05 and 5.49. The arithmetic was checked by hand
before touching anything, because a contrast test that is subtly wrong would
"find" problems that are not there.

Raised it to 0.75, which is the first passing value plus a little. That is
an edit to one of the three artifacts that outrank the prose, so it is in
`DECISIONS.md` with the numbers. The file's own header says `--td-dim`
exists to be raised on low-contrast palettes and names Solarized as one that
needs it; 0.72 was just short.

Worth carrying forward: Solarized Light has the least headroom of any
palette at 4.99:1 ink on paper against a 4.5:1 floor.

### What was learned

**A test wrote the CSP rule into the templates.** The first cut used
`onchange="this.form.requestSubmit()"` on the row checkbox and the theme
radio. Two attributes, entirely reasonable-looking, and they would have
forced `unsafe-inline` back into the policy that section 15 says must not
have it. The test that scans every page for inline handlers and script
bodies caught both. They moved into a delegated listener, and nothing is
inlined at all now, so the policy needs no htmx hash either.

**404 and 405 are different claims.** Registering `GET /` as the catch-all
made a POST to `/register` answer 405, which says "this path exists but not
for that verb" about a path that does not exist. The no-registration-route
assertion caught it. Registering the pattern for every method and answering
404 explicitly is the honest version.

**Secure cookies make curl a bad smoke test.** The session cookie carries
`Secure`, so curl will not store it over plain HTTP, and a shell check of
the web UI silently gets the login page every time. Browsers treat
`http://localhost` and `http://127.0.0.1` as trustworthy origins and do
store it, so this is a property of curl rather than a bug. Passing the
cookie explicitly is the workaround; it cost twenty minutes to notice.

**One stylesheet is a claim about the browser, not the source.** The system
already lives in two files and the app needs a layout layer. The browser
fetches exactly one, assembled at startup. The two authority files are
mirrored into the package because `go:embed` cannot reach outside it, and a
byte-for-byte test makes drift a build failure.

### What was deferred, and why

- **Editing from the web.** `e`, `p`, and `t` are on the help page marked
  phase 5. The list, detail, and capture paths are here; a full edit form is
  a bigger piece of design than it looks and belongs with the settings work
  that reminders bring.
- **Long-press action menu on touch.** Section 12 asks for it. The rows are
  44px targets and every action has a visible control, so the gap is a
  convenience rather than a hole.
- **The select widget.** Section 12 specifies a bordered list panel anchored
  under the field rather than a native dropdown. Nothing in phase 4 needs a
  select: the theme picker is radios, which is what "the picker is a list"
  asks for.
- **Live updates.** The page reloads after its own writes. `GET /events`
  exists and nothing subscribes. Same reason as the TUI: the polling loop
  should be written once, with the phase 5 scheduler.
- **A CSRF token.** The session cookie is `SameSite=Lax`, which blocks
  cross-site form posts, and every mutating route is a POST. Worth revisiting
  when the OAuth work in phase 9 adds a second way to hold a session.

### Notes for phase 5

Reminders need a scheduler goroutine on a 60 second tick, which is the first
thing in this design that runs on its own. Two things are waiting on it:
`PurgeExpiredSessions` from phase 2, which exists and is never called, and
the change-feed polling both UIs want.

The `notify` tri-state has a control inventory entry already: it is three
states, so it is radios rather than a toggle, and the toggle is reserved for
persistent settings like quiet hours.

---

## Phase 5: reminders, the notify tri-state, and editing

2026-07-31

### What shipped

Section 16 step 5, plus editing folded in on the author's call.

`internal/notify` holds the policy, the firing rules, the ntfy sender, and
the scheduler: a single goroutine on a 60 second tick, no job queue and no
cron container. The policy is the `[notify]` block of a `config.toml` that is
written commented on first start. `default_rule` is a filter query rather
than a settings screen, so "*", "" and "p:<=2 -#someday" all mean what they
look like.

Editing landed in all three clients, all through the `PATCH` route that has
existed since phase 1: `td edit`, `td note`, `td snooze`; the TUI's `e`, `p`,
`t`, `s`, and `N`; and a form on the web detail page with the notify
tri-state drawn as radios, because three states are not a toggle.

The scheduler also picked up `PurgeExpiredSessions`, which has existed since
phase 2 and had nothing to call it.

### The bug that mattered most

A `PATCH` that changed nothing deadlocked the entire server.

`Store.Patch` returned the task by calling `Get` on the no-op path while its
own transaction was still open. SQLite takes one writer so the pool is a
single connection, and `Get` waited forever for the connection the
transaction was holding. Every later request queued behind it. One no-op
write from any authenticated client stopped the server until restart.

It surfaced from a reminder test that patches a due date to the value it
already has. The editing work would have hit it constantly: a form submitted
with nothing changed is the most likely no-op patch there is, and the web
edit form submits every field every time.

Committed on its own, before the rest of the phase. An audit of the other
transactions found no second instance.

### What was learned

**A correct check of the wrong surface still passes.** Phase 4 built the
contrast floor section 12 specifies, which measures ink at `--td-dim`. It
never looked at `--td-grey`, because the rule says that token is not for text
at all, and the file then painted text with it anyway. The lesson generalises
past CSS: a test that encodes the rule as written will not notice the rule
being bypassed.

**The flag package and the way people type are different.** `td show 103
-json` printed the human form; `td note 103 -show` appended the literal
string "-show" as a note. Go's flag package stops at the first positional.
This is the third time it has bitten in this build, after `tdd -db X account
create` and the subcommand dispatch. There is one helper now that separates
flags from positionals wherever they appear.

**Input needed the server's clock too.** Phase 4 made rendering resolve
against `X-Td-Now`. Input was still local, so `td edit -due friday` typed on
a Friday laptop landed on a different day than the same word typed into the
web box against a Monday server. Two clients, one task, two answers. The
clients sync the clock off the health check before resolving anything.

**A test that cannot fail is worse than none.** The modal-contrast test
written in phase 4 asked whether a `.td-modal` rule mentioned a class, which
the `:focus` override satisfied by itself. Deleting the rule that mattered
left it green. It checks per property now, and was verified by deleting the
rule and watching it fail; on its first correct run it found a second
instance nobody had reported.

### What was deferred, and why

- **Reminder delivery against a real topic.** Every test uses a recording
  sender. `CLAUDE.md` says only the disposable dev topic may receive
  anything, and the interface makes reaching a real one structurally
  impossible rather than a rule to remember. Confirming a push actually
  arrives on a phone is the one thing the soak has to do by hand.
- **A catch-up bound.** A task whose fire time passed while the server was
  down fires once, late, whenever the server comes back. That is what "one
  push per task per due value" says. After a long outage it is a burst. Worth
  watching during the two weeks rather than guessing a window now.
- **TOTP replay inside a period**, still. Same as phase 2.
- **`td export --json`.** Still unscheduled, and still a v1 done criterion.
  It is the last one with no phase.

### Notes for the soak

Section 16 says to stop here and use it for two weeks before phase 6, because
half the requirements will change. This is that point.

Reminders are off until `notify.topic` is set in `config.toml`, which is
written on first start. The action buttons need a token: `tdd token create
-name ntfy -scopes write`, then `notify.action_token`. Without it the push is
a click-through, which is a reasonable way to start.

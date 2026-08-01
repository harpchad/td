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

---

## Phase 6: people, groups, identity mapping

2026-07-31

Built past the point section 16 says to stop at, on the author's
instruction. Recorded in `DECISIONS.md` rather than re-argued.

### What shipped

Person and group CRUD, identity mapping, person links on tasks with roles,
and the person page in all three clients: `td person`, `/p/{handle}` in the
browser, and the `w` and `@` keys in the TUI.

The page's sections are section 5's, in its order: assigned to them, what you
owe them, what you are waiting on with its age, the agenda, involved, then
their groups' tasks. The order is the point rather than the contents.

`person_identity` maps a Jira account, a monday user, and an Entra object
onto one person row. A test links three sources to one person and checks each
resolves back, because the failure this prevents is silent: without it you
get three Brandisses and every cross-system query is wrong in a way that
looks like missing data.

### What was learned

**The agenda needed no new storage.** Section 5 asks for a free-text agenda
section per person. It is `is:open #agenda @handle`, which is one line and
composes with everything else the grammar does. Anything else would have been
a second place tasks live.

**Person links needed the same treatment as tags.** They are in another table
so the field diff cannot see them, and the undo contract lists them. They
travel in the event patch under a pseudo-field and come back through
`setPeopleLinks`, exactly as tags do. The phase 1 note that this was deferred
"until there is something to reverse" turned out to be the right shape: the
mechanism was already there to copy.

**Every key in section 11's keymap is now implemented except one.** The
deferred-key test has a single subject left, `E` for editing a series, and
phase 7 takes it. The right move then is to delete the test rather than
invent a key to keep it alive.

### What was deferred

- **The pending list for unmatched external users.** Section 5 says unmatched
  external users go into a list you can merge from. Nothing produces
  unmatched users until a sync plugin runs, which is phase 11. The mapping
  side is built and tested; the queue is empty by construction.
- **Group editing in the clients.** The API creates groups and replaces
  membership. Neither UI has a screen for it, because groups are static and
  rarely change; `curl` or a future settings section covers it.

---

## Phase 7: recurrence, subtasks, triage mode, attachments

2026-07-31

### What shipped

`internal/recur` turns an RRULE into instants. It wraps `teambition/rrule-go`
rather than replacing it, and adds the one thing that library gets wrong for
this purpose: a local time inside a spring-forward gap. Go's `time.Date`
normalises 02:30 on a transition day backwards to 01:30 CST; the fixture
requires 03:30 CDT, and rrule-go inherits the standard library's behaviour
because it builds its results with `time.Date`. `ResolveLocal` detects the gap
by comparing the wall clock it asked for against the one it got back, and
`atWallClock` re-imposes the anchor's time of day on every occurrence. That is
what makes "09:00 every Monday" stay 09:00 across a DST change instead of
drifting an hour. All 15 cases in `testdata/recurrence_cases.json` pass,
including the two `dst_edges` and both catch-up policies.

`internal/store/series.go` is the storage side. A series is a rule plus the
`TaskCreate` its instances are made from, and exactly one instance is open at a
time. `after_completion` generates from `Patch` the moment a status lands on
done; `fixed` generates from the scheduler tick. Catch-up is the interesting
half: a fixed series materialises an occurrence only when it has no open
instance, so ignoring a daily chore for six days produces six
`recurrence.missed` events and still one row. `pile` materialises every
occurrence. `last_fired_at` is where the walk starts, which is what makes a
restart pick up exactly where it left off rather than firing twice or not at
all.

Attachments are content-addressed. `internal/blob` is a two-level sharded
directory named by SHA-256, with a 25 MB cap enforced while streaming rather
than from `Content-Length`, an atomic rename into place so a crash leaves a
temp file and never a truncated blob under a hash it does not have, and a weekly
sweep against the whole `attachment` table. Downloads go through an ordinary
`/api/v1` handler, so they inherit the same authentication as everything else;
there is no static file handler over the blob directory at all.

Triage is a dedicated mode in both clients. `T` in the TUI, `/triage` in the
browser. One task, large, with the same key letters they are everywhere else.
Priority `1` through `4` sets the priority and promotes in one keystroke,
because a priority is exactly what lets a task leave the inbox and pressing two
buttons that always go together is a wasted step.

`E` edits the series rather than the instance, which closes the last deferred
key in section 11's keymap. `td sub`, `td repeat`, and `td attach` are the CLI
half; the browser gets a subtask form and a file list on the detail page.

### What was learned

**The fixture's catch-up numbers have two readings and only one of them is a
rule.** Six missed and one open can mean "log five, materialise the sixth" or
"log all six, leave the one that is already there". The second makes "exactly
one open instance" structural instead of coincidental, and collapses the
restart path and the normal tick into the same loop. Written up in
`DECISIONS.md`.

**Promotion was checked against the wrong state.** Triage set a priority and
promoted in one request and got `inbox_incomplete` back, because
`applyTransition` was reading the task as it stood before the patch. The
fixture says "priority is set OR due_at is set" without saying when, and the
answer has to be "after", or every client sends two requests for one write.
Found by a UI feature, not by a test, which is the argument for building the
screens.

**The scheduler's early return was hiding a future bug.** `Run` bailed out
when no ntfy topic was configured. Recurrence now rides the same tick, so
turning off push would have silently stopped repeating tasks from repeating.
The tick always runs; only delivery is gated.

**`PatchTyped` only carried four fields.** The TUI had never needed priority
through it, so triage's one-keystroke promote sent a patch with no priority in
it and the server did exactly what it was told. Everything the client can set,
it can now set through the typed path.

### What was deferred

`td export --json` and its import are a v1 done-criterion with no phase in
section 16. They are not part of any of 1 through 9 and are still unbuilt.

The web triage screen has no keyboard bindings yet: the bottom bar advertises
`1-4`, `P`, `n`, `x` and `esc`, and only the buttons work. The TUI has the
keys; the browser has the buttons. Wiring the same letters into `td.js` is a
small job and belongs with whatever else touches that file next.

Attachments have no drag-and-drop and no upload progress. The form posts a
file and the page comes back. At 25 MB on a LAN that is fine, and anything
better needs script the CSP would have to be widened for.

---

## Phase 8: MCP server, static bearer auth, read plus capture

2026-07-31

### What shipped

`internal/mcpsrv` serves the Model Context Protocol at `POST /mcp`, over the
same store the REST API and the browser go through. All eleven tools from
section 10, no more: `search_tasks`, `get_task`, `capture`, `create_task`,
`update_task`, `complete_task`, `add_note`, `list_people`, `person_agenda`,
`whats_next`, `recent_activity`. A test asserts the count, so a twelfth tool
has to be a decision rather than a drift.

The revision is `2026-07-28`, pinned in `mcpsrv.Revision`, asserted by a test,
and named in the README. `CLAUDE.md` was right that this needed checking
against the current documentation rather than memory, and it was also right
that the SDK might be behind: it is not.
`github.com/modelcontextprotocol/go-sdk@v1.7.0` lists `2026-07-28` among its
supported versions, so the handler is `StreamableHTTPHandler` in `Stateless`
mode with `JSONResponse` set. Stateless is not a preference here: that
revision removed sessions and the initialize/initialized handshake, so `POST`
is the only method the endpoint answers and `GET` and `DELETE` return 405.

Authentication rides on whatever the HTTP layer already accepted. A `td_`
token in an `Authorization: Bearer` header works with the same scopes and the
same revoke button as everything else. An unauthenticated request answers 401
with `WWW-Authenticate: Bearer resource_metadata="...", scope="td:read"`, and
RFC 9728 Protected Resource Metadata is served unauthenticated at
`/.well-known/oauth-protected-resource` with `resource` set to the MCP URL
exactly. A valid credential missing a scope answers 403 with
`error="insufficient_scope"`, which is a different thing and tells a client
something different.

Read is the floor for reaching the protocol; each tool checks the scope it
needs. A client with read and capture can list all eleven tools and is told
which ones it may use, which is a better failure than seeing six.

### What was learned

**The 2026-07-28 revision makes this phase smaller, not bigger.** No session
store, no shared state between requests, no SSE, no handshake to get wrong.
The handler is built once at startup and every request is independent.

**A tool failure is a result, not a transport error.** A bad filter is the
model's typo and it needs the parser's message to fix it; returning a
JSON-RPC error would surface to the user as a broken server. Everything comes
back as `IsError` content with the same message a person would get.

**The injection defence is structural before it is textual.** Task content
goes back inside JSON string fields and is never interpolated into prose the
model reads as its own context, which is what actually stops a synced Jira
description from acting as a directive. The instructions block says so as
well, because a rule the model is told once is cheaper than a rule it has to
infer, but the shape of the output is what enforces it.

**`whats_next` had to report the untruncated total.** Returning `total: 3` for
a limit of 3 reads as inbox zero when eight things are open, and a model that
believes it will say so out loud.

### What was deferred

OAuth 2.1 is phase 9, which is where the authorization server, the JWKS with
two live keys, PKCE, and the consent screen land. Phase 8 ships the resource
server half: the PRM document and the 401 discovery chain already point at an
authorization server that does not exist yet, which is correct ordering but
means a claude.ai custom connector cannot complete a grant until phase 9.

MCP resources and prompts are not implemented. Section 10 lists tools and
nothing else, and a resource list is a second surface with its own injection
questions.

---

## Phase 9: OAuth 2.1 authorization server

2026-07-31

### What shipped

td is now its own authorization server as well as its own resource server.
`/.well-known/oauth-authorization-server`, `/.well-known/jwks.json`,
`/authorize`, `/token`, `/register`, and `/revoke`, all in the same binary
behind the same hostname, which is the arrangement that removes a whole class
of discovery failures.

`internal/oauth` is the token format and the PKCE arithmetic. The JWT
encoding is written by hand rather than imported: this code is both the only
issuer and the only verifier, it accepts exactly one algorithm, and the whole
family of algorithm-confusion bugs comes from being flexible about that.
`Verify` reads `alg` to refuse, never to dispatch, so `none` and `HS256`
against the public key are not rejected so much as unrepresentable. ES256, so
two live keys and a JWKS stay small.

The store holds clients, codes, grants, and keys. Codes are single use and
enforced with one UPDATE that both marks and checks, so "exactly once" is
true under two simultaneous exchanges rather than nearly true. Refresh tokens
rotate on every use. Every secret is stored as its SHA-256, the same rule the
session and token tables already follow.

The consent screen is a form on the same origin behind the ordinary login,
which is what makes this an authorization server without a second identity
system. It is checkboxes, because a screen you can only agree with is a
notification. Grants render on the settings page under the static tokens with
the same revoke button.

The end-to-end test does the sequence a claude.ai custom connector does:
register, authorize, consent to two of three scopes, exchange the code with
the verifier, then open an MCP session with the access token and call a tool.
BUILD-SPEC says nothing short of that counts, so that is the test.

### What was learned

**The two bearer shapes have to be told apart, not tried in turn.** A `td_`
token is opaque and an access token is a JWT with two dots. Trying both would
mean a failed database lookup on every OAuth request and a failed signature
check on every static-token one; the shape is enough.

**Section 15's "no registration route" needed its own carve-out asserted.**
The security test forbade `/register` outright, and section 15 says in the
same breath that OAuth client registration is not user registration. Removing
the path from the forbidden list would have weakened the test, so it was
replaced with an assertion that `/register` creates no account and that
neither the client id nor the client secret reaches the API.

**A missing PKCE method is the interesting case.** RFC 7636 says it defaults
to `plain`; OAuth 2.1 removes `plain`. Honouring the default would mean a
refusal that can be bypassed by leaving a parameter out, so absent is an
error.

**The login form needed a `next`, and `next` needed a guard.** `/authorize`
sends an unauthenticated browser to the login page and expects it back. An
absolute URL there would be an open redirect on the login form, which is the
exact thing the authorization server spends its own code avoiding, so only
same-origin paths are accepted and `//evil.example` is refused along with the
rest.

### What was deferred

Client ID Metadata Documents are advertised in the metadata but not
implemented: the `oauth_client.source` column distinguishes `dcr` from `cimd`
and nothing writes `cimd` yet. Fetching and caching a client's metadata
document is the remaining work, and Dynamic Client Registration covers every
client available to test against today.

Key rotation is implemented and tested but not scheduled. `RotateSigningKey`
promotes the standby and generates a new one; nothing calls it on a timer, so
rotation is currently an operator action with no command in front of it.

The consent screen does not show which client is asking beyond its
self-declared name. A client registers its own `client_name`, so the screen is
repeating a string an unauthenticated caller chose. That is worth stating on
the page and is not stated yet.

---

## Phase 10: the Memos completion webhook

2026-07-31

### What shipped

Completed tasks post themselves to Memos. `internal/memos` composes and
posts; `internal/notify.Journal` decides what to post and when.

The decision worth recording is that it follows a cursor into the event log
rather than firing inline from `Complete`. Two properties have to hold at
once. Completing a task must never fail because a journal is unreachable,
which means the post cannot be in the write path. And a memo must arrive
exactly once, which means a retry cannot duplicate it. A cursor gives both,
and the inline version gives neither.

The cursor advances per entry rather than per batch, so a batch that fails
halfway does not repost its first half on the next tick. An unknown consumer
starts at the newest event, so switching the webhook on does not dump a year
of history into somebody's journal.

### What was learned

**A memo has to be dated by the completion, not by the delivery.** If Memos
was down for a day the entry arrives a day late, and dating it by delivery
would make the journal wrong about the one thing it records. `Compose` reads
the completion time off the task and does not take a clock at all.

**"No read path" is worth asserting rather than assuming.** Section 17
decided it; the test now checks the type has no fetch and the config has no
polling interval and no mapping from a memo back to a task.

### What was deferred

Nothing from this phase. The webhook is one direction, one event kind, one
consumer, which is exactly what section 17 fixed.

---

## Phase 11: the Planner sync plugin

2026-07-31

### What shipped

`POST /api/v1/sync/{source}` and `internal/plugins/planner`, plus
`internal/sync`, which is where the field-ownership table lives as data.

Section 8 says to write that table down per plugin before writing code. It is
written down once, in the server, and the server enforces it. A plugin says
what it read upstream; it does not get to say what that is allowed to change.
A rule spread across plugins drifts a field at a time, and a plugin token is a
credential you paste into something else, so its blast radius should not
include your priorities. The contract's `Item` type has no priority field at
all, which makes the important half structural.

The plugin is a client. It reads Graph, translates, and posts over HTTP with a
`sync:planner` token, which reaches its own source and nothing else in the
API. Everything it writes is attributed to `plugin:planner` and is one undo
loop away.

The fixtures under `testdata/plugins/planner` are hand-written from Graph's
published `plannerTask`, `plannerPlan`, and `user` documentation. No tenant
was contacted and nothing in the package can reach one: the Graph client is
aimed at an httptest server in every test. `tasks.json` and
`tasks_updated.json` are the same plan minutes apart, arranged so one task
changed, one is byte-identical, one disappeared, and one is new.

Also shipped, because it is a v1 done criterion with no phase: `td export
--json`, `td export --markdown`, and `td import`. The JSON round-trips, and
that is a test rather than a claim.

### What was learned

**Idempotence has to be visible, not asserted.** The result carries an
`unchanged` count, and the test that matters counts events before and after
three replays rather than trusting the return value. An "idempotent" sync that
still churns `updated_at` and writes events is not idempotent in any way that
helps.

**The gone list is the dangerous part.** Planner has no delta query and no
tombstones, so "gone" is computed by subtraction from a read the plugin has to
assume was complete. An expired token returns zero tasks and looks exactly
like an emptied plan. Refusing to subtract from an empty read is the cheapest
guard against marking every mirrored task gone in one pass, and marking rather
than deleting is what makes the residual case recoverable.

**Map iteration would have broken replay.** Planner puts assignees in a map
keyed by user id, so two translations of an unchanged plan produced different
payloads until the ids were sorted. The test runs the translation twenty times
and compares bytes.

**A first sync links nobody, and that is correct.** An unmapped Graph id whose
name collides with somebody already known is skipped rather than guessed at,
because merging two different people is worse than a missing link. It is a
worse first-run experience than a name match and the right default; the
end-to-end test maps the identities first, the way a person would.

**Planner's due date is a date, not an instant.** It stores midnight UTC on
the day the user picked, and reading that as an instant anywhere west of UTC
turns Friday into Thursday.

### What was deferred

Write-back, permanently as far as v1 is concerned. Section 8 decided a
read-only mirror and gave the reason: it removes the callback path, the action
queue, and the whole class of failures where a remote write half-succeeds.

The Graph token is pasted or shelled out for. Device-code and client-credential
flows against Entra are real work and would need a real tenant to test, which
`CLAUDE.md` forbids.

No scheduler for the plugin. `td sync planner` is a command; putting it on a
timer is cron's job and does not need code here.

Markdown export is one-way. It writes frontmatter and notes for Obsidian and
nothing reads it back, because the JSON is the format that round-trips and two
import paths is one more than is worth maintaining.

---

## Phase 11, corrected: who an upstream identity belongs to

2026-08-01

### What changed

Asked whether "a first Planner sync links nobody" was a real problem. It was,
and worse than the phrase suggested. A probe against the seeded fixture: given
a board with Stacey, who td already knows, and Dana, who it does not, the sync
**dropped Stacey and created and linked Dana**.

The old resolution was: mapped identity, else create a person from the display
name, else skip. A handle collision took the skip. But a handle collision
means the person is somebody you already track, so the silent failure was
reserved for exactly the people who mattered, while strangers sailed through.

Now, in descending order of evidence: an existing mapping; an email matching a
person's exactly and uniquely, which records the mapping as a side effect; or
nobody holding that handle, so a new person is created with their address
kept. Names are never matched on. Everything left is returned in
`Result.Unresolved` with a reason, and `td sync planner` prints each with the
`td person map` command that fixes it.

`td sync planner -relink` was added alongside, because an unchanged revision
is skipped entirely and a person mapped after the fact would otherwise wait
for somebody to edit the card upstream.

### What was learned

**The silence was the bug, not the guessing.** Refusing to merge two people
called Stacey is right. Refusing without saying so is what made it invisible,
and the first version of that refusal did not even distinguish "somebody has
that handle" from "the plugin sent nothing", so neither could be reported.

**The linter caught a swallowed error class.** `CreatePerson` failing was read
as "handle taken", which would have reported a database fault as an ambiguous
name. `nilerr` flagged it. It now asks whether the handle is taken and treats
anything else as the failure it is.

**The end-to-end test was papering over it.** `TestPlannerMirrorsIntoTd`
mapped the three identities by hand before syncing, which made it pass while
demonstrating nothing about a first run. It now sets addresses on the fixture
people and maps nothing, so it proves the resolution rather than bypassing it.

### What is still true and worth knowing

A first sync against people with no `email` recorded reports everybody and you
map them once. That is the deliberate cost of never guessing on a name, and
filling in addresses removes it.

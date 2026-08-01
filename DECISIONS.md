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

## 2026-08-01  Selected text is styled, rather than left to the browser
Phase: 11, corrected

Reported: the device code disappeared while it was being dragged over, so it
could not be copied.

The stylesheet had no `::selection` rule at all, anywhere. Selection colours
were therefore whatever the browser chose, and on a themed page what it
chooses can land close enough to the text colour that the text vanishes under
the highlight. It was not specific to the device code; every selectable string
in the product had the same exposure, and the code only made it obvious
because it is large, bold, and the one thing somebody is definitely trying to
copy.

Decided: `::selection` is ink on paper inverted, the same idiom a selected row
already uses. The contrast is guaranteed rather than hoped for, because every
shipped theme has to clear 4.5:1 for ink on paper and contrast is symmetric,
so the swap clears it too. It lives in `tokens.css`, which is the authority for
the visual system, and reaches the embedded copy through `make sync-css`.

A test asserts the rule exists and sets both halves. A background with no
colour is exactly the failure mode: the highlight changes and the text does
not.

Reversible: four lines. The test fails without them.

## 2026-08-01  The connect panel renders two ways, and shipped rendering one
Phase: 11, corrected

The first deployment of the device code flow 404ed the moment somebody
pressed the button, with the device code sitting in the address bar.

Two mistakes compounding. The connect POST answered with a bare fragment, so
the response had no layout, and therefore no stylesheet and no htmx. Nothing
polled. And the poll form carried `hx-post` but no `action` and no `method`,
so the one control that still worked, the submit button, fell back to a native
GET at whatever URL the browser was on, which was the connect endpoint, which
only accepts POST.

The rule that was broken is already written down for the rest of this UI:
every action is a real form, and with the script off the form posts and the
server redirects. htmx is an enhancement on top of something that works
without it, and this was built the other way round.

Decided: `ConnectPanel` renders a whole page when the browser navigated to it
and a fragment when `HX-Request` is set. The form carries an explicit action
and method as well as the htmx attributes, so both paths reach the same POST.
`ConnectDone` redirects for a form and sets `HX-Redirect` for htmx. A GET into
the middle of the flow redirects back to Settings rather than 404ing, because
a refresh and a back button both land there and a device code is single use
anyway.

Also worth stating: the device code belongs in a form body and never in a
query string. It is short lived and single use, but a URL ends up in history
and in the Referer of anything the page links to.

Reversible: one branch and four template attributes. Both are pinned by tests
that fail with the original code.

## 2026-08-01  The Planner mirror moved onto the server
Phase: 11, corrected

Section 8 defines the plugin contract as "over the API with a scoped token",
and the first build took that literally: the mirror was a client, configured in
the client's config.toml, run from a laptop with a Graph token in a file.

That was wrong for the one first-party plugin, and the author said so. A mirror
that only refreshes while somebody's laptop is open is not a mirror. Worse, it
was already inconsistent: the Memos webhook runs on the server, on the
scheduler tick, configured server-side. Planner was the odd one out by accident
rather than by decision.

Decided: configuration lives in a `plugin_config` table so the browser can edit
it, the sync runs on the existing scheduler tick, and the Graph credential
lives on the server. `POST /api/v1/sync/{source}` is untouched and is still
exactly what a third party posts to; the first-party plugin simply no longer
needs to. Both paths call the same `store.Sync`, so the field-ownership rules
cannot differ between them.

The client loses `[planner]` from its config and its dependency on the plugin
package entirely, which is a smaller `cmd/td`. `td sync planner` stays as a
"run now" trigger that holds no credentials.

Rejected: a plugin marketplace or installer, which the do-not-build list
forbids and which this is not. One built-in mirror with a settings section is
not a marketplace.

Reversible: the client half was deleted rather than kept in parallel, so going
back means restoring it. The contract endpoint never moved, so nothing external
depends on the choice.

## 2026-08-01  Device code, not a client secret
Phase: 11, corrected

A scheduled sync needs a credential that renews itself, which a pasted
one-hour token is not.

Checked rather than assumed, because Microsoft's documentation currently
contradicts itself: the Graph permissions reference says Planner supports
delegated permissions only, while the plannerPlan endpoint pages list
Tasks.Read.All as an application permission. That looks like a per-endpoint
rollout in progress.

Decided: device code with a stored refresh token. It works whichever way that
lands, needs no client secret and no admin consent, and keeps td's model
intact: one user, and everything acts as them. One interactive sign-in, then
the refresh token renews on every run.

The refresh token is stored in the database in plaintext, which matches the
existing precedent: `oauth_key.private_pem` already is, and both are protected
by the same file permissions. It is excluded from `td export` and from every
API response, and the settings page shows only whether a credential exists and
whose it is.

Rejected: client credentials, which is simpler and might 403 on a tenant.
Rejected: supporting both, which is a third more surface for a fallback that
device code already covers.

Reversible: `internal/msgraph` is one file and one interface.

## 2026-08-01  Settings and credential are separate columns
Phase: 11, corrected

Decided: `plugin_config.settings` and `plugin_config.credential` are different
columns, and the save path writes only the first.

A settings form that could blank a stored refresh token by omitting a field is
a settings form that eventually will, and the failure looks like "Planner just
stopped working" rather than like a form bug. Separating them makes the save
path structurally incapable of it, and disconnect the only way a credential
goes.

## 2026-08-01  The pinned tools are installed locally and invoked by path
Phase: release

`make check` passed on this machine and failed in CI with six lint errors it
had never reported. `CLAUDE.md` says CI runs the same command and there is no
second definition of passing. There were two.

The cause: `make tools` ran `command -v golangci-lint >/dev/null || go install
...@v2.6.1`, so a machine that already had golangci-lint from Homebrew kept it.
This one had v2.12.2, which no longer reports `prealloc` on the six sites v2.6.1
does. The pin was written down and not enforced, which is the same as not
having one.

Decided: the tools install into `build/tools` with `GOBIN`, and every recipe
invokes them by absolute path. A missing tool is an error telling you to run
`make tools`, never a fallback to whatever is on `PATH`. `make tools` prints
the linter version it installed, so a mismatch is visible rather than
inferred.

The six findings were then real and were fixed rather than silenced: each one
allocates a slice whose length is known a line earlier.

Reversible: the Makefile. The rest of the change is six `make` calls.

## 2026-08-01  Publishing is a separate workflow from checking
Phase: release

`make check` is the one definition of passing, and CI runs it. Adding push
steps to that same workflow would have meant a pull request from a fork could
reach the registry.

Decided: `release.yml` calls `ci.yml` as a reusable workflow and publishes only
if it passes. It never runs on a pull request. A push to main gets a moving
`:main` tag plus the commit sha; a `v*` tag gets an immutable version, `:latest`,
and a GitHub release with the client binaries. The image is smoke-tested
before it is pushed rather than after.

Reversible: two files under `.github/workflows`.

## 2026-08-01  main.version had to exist before anything could be released
Phase: release

The Dockerfile was injecting `-ldflags "-X main.version=${VERSION}"` into a
variable nobody had declared. The Go linker discards `-X` on an unknown symbol
in silence, so every image reported the same number, and the CI step named
"The image starts and reports its version" was only proving that the binary
started.

Decided: `main.version` is declared in both commands and printed alongside the
API version, and both CI and release assert that the built artifact reports the
version it was built with rather than merely printing something. Two numbers,
because they answer different questions: which build this is, and what the skew
handshake compares.

The same check found that `td --version` and `td -h` had never worked. The
switch only saw non-dash arguments, so both fell through to the TUI and came
back as a flag parse error.

## 2026-08-01  The container could not create its own database
Phase: release

Found by the new release smoke test within a minute of writing it, which is
the argument for the test.

The runtime stage runs as `nonroot` and `/data` did not exist in the image, so
Docker created it root-owned and a fresh volume was unwritable:
`migrations table: unable to open database file`. Every deployment onto a new
volume would have failed on first start.

The old CI check could not have caught it. `docker run --rm td:ci -version`
exits before anything touches the store. The new one starts the container the
way a person would and waits for `/healthz`, which needs the binary, the
base-URL guard, and the store to have all come up.

Decided: the directory is created in the build stage and copied in with
`--chown=nonroot:nonroot`, because distroless has no shell and therefore no
`mkdir` in the final stage. Docker seeds a fresh named volume from the image,
so that ownership is what the volume gets.

## 2026-08-01  Employer references removed before publishing
Phase: release

`CLAUDE.md` says not to rewrite `BUILD-SPEC.md`. This edit was directed
explicitly, so it is recorded here rather than argued.

The repository was published public, and a scan before that turned up no
credentials anywhere in the tree or the history, but did find the author's
employer named in four files and two remarks about that employer's internal
tooling plans. None of it is secret and all of it is permanent once forked.
The name was replaced with "a real tenant" and the two remarks dropped; the
reasoning they supported ("Planner has a cleaner Graph API") stands on its own
without them.

The fixture first names were kept. They read as ordinary test data.

## 2026-08-01  A sync reports the people it will not guess at
Phase: 11, corrected

The first build resolved an upstream identity by looking for an existing
mapping, and failing that created a person from the display name. A handle
collision was swallowed and the link dropped.

That was backwards in the worst way, and a probe made it obvious. Given a
Planner board with Stacey, who td already knows, and Dana, who it does not:
the sync **dropped** Stacey and **created and linked** Dana. A handle
collision means the person is somebody you already track, which is exactly
when the link matters, so the failure mode was reserved for the cases that
mattered most. It was also silent, which is what made it a bug rather than a
trade-off.

Decided, in order of evidence:

1. An identity already mapped. Certain.
2. An email that matches a person's, exactly and uniquely. An address is an
   identity; a display name is not. The match records the mapping, so the next
   sync takes path 1.
3. Nobody holds that handle, so this is somebody new and a person is created
   for them with their address kept.

Anything left is returned in `Result.Unresolved`, one entry per identity
however many tasks it appeared on, with a reason. `td sync planner` prints
each with the `td person map` command that fixes it.

Rejected: matching on name, which merges two people irreversibly and cannot
be spotted by reading the list afterwards. Rejected: disambiguating into
`stacey2`, which is the three-Brandisses outcome section 5 exists to prevent.
Rejected: dropping the auto-create for unknown people, which would make a
first sync link nobody at all and turn a short report into thirty commands.

The residual cost is stated rather than hidden: a first sync against people
with no email recorded reports everybody, and you map them once. Filling in
`email` avoids it entirely.

Reversible: `resolveIdentity` in `internal/store/sync.go`, one function.

## 2026-08-01  -relink, because idempotence has one cost
Phase: 11, corrected

An item whose revision has not moved is not looked at, which is what makes a
replay free. It also means a person you map after the first sync is not
attached until somebody happens to edit that card upstream, which could be
never.

Decided: `td sync planner -relink` drops the revision from every item, so the
server cannot tell them from changes and re-applies the lot. It changes what
the server may skip, not what it is told.

Rejected: making the server re-resolve people on an unchanged item, which
would mean every sync walks every person link forever to catch a case that
happens once after a mapping.

Reversible: one flag and one line in `Translate`.

## 2026-07-31  The journal follows the event log rather than firing inline
Phase: 10

The obvious build posts to Memos from inside Complete.

Decided: an outbox cursor into the event log, delivered on the same tick as
everything else. Two properties have to hold at once and the inline version
gets both wrong. A completion must never fail because a journal is
unreachable, which means the post cannot be in the write path. And a memo must
be posted exactly once, which means a retry cannot be a duplicate. A cursor
gives both: the write path never touches Memos, and the cursor only advances
past an entry that landed.

The cursor advances per entry rather than once per batch. A batch that fails
halfway has delivered its first half, and moving the cursor at the end would
repost those on the next tick.

An unknown consumer starts at the newest event rather than at zero, so
switching the webhook on does not post a year of history into somebody's
journal.

Reversible: `internal/notify/journal.go` and one table.

## 2026-07-31  A memo is dated by the completion, not by the delivery
Phase: 10

Decided: `Compose` reads the completion time off the task and does not take a
clock. If Memos was down for a day the entry arrives a day late, and a journal
entry dated by when it was delivered would be wrong about the one fact it
exists to record.

Reversible: one line, and a test that sets the two apart by three days.

## 2026-07-31  Field ownership lives in the server, as data
Phase: 11

Section 8 says to write the ownership table down per plugin before writing
code. Taken literally that puts the table in each plugin.

Decided: the table is `internal/sync`'s `Upstream` and `Local` lists, and the
server enforces it. A plugin says what it read; it does not get to say what
that is allowed to change. Two reasons. A rule spread across plugins drifts a
field at a time, and the second plugin is where it drifts. And a plugin token
is a credential you paste into something else, so the blast radius of a
compromised or simply buggy one should not include your priorities.

The contract's `Item` type has no priority field at all, which makes the most
important half of the rule structural rather than enforced.

Rejected: letting a plugin declare its own ownership, which is how "my
priority got wiped by a sync" happens on the day somebody adds a plugin in a
hurry.

Reversible: two slices and one function.

## 2026-07-31  A completed mirror stays completed
Phase: 11

Status is upstream-owned, so the straightforward reading is that a sync
overwrites it every time.

Decided: a task you completed or dropped in td keeps that status whatever the
upstream item says. Completing a mirrored task is a statement about your own
work: you have done your part, and whether the ticket is still open is
somebody else's business. A sync that reopened it every fifteen minutes would
make the mirror an argument rather than a list, and you would stop trusting
the list.

The consequence is stated rather than hidden: the mirror and the source can
disagree about status, and when they do, td is right about you.

Reversible: `sync.LocalStatus`, one function.

## 2026-07-31  An empty read never marks anything gone
Phase: 11

Planner has no delta query and no tombstones, so "gone" is whatever td holds
for a source that a full read did not contain. That makes the read
authoritative, which makes a partial read destructive.

Decided: a read that returned zero tasks skips the gone pass entirely. An
expired token, a mistyped plan id, and a plan somebody genuinely emptied all
look identical from here, and only one of those should mark every mirrored
task `upstream_gone` in a single pass. Refusing to subtract from nothing is
the cheapest guard against the worst thing this plugin can do.

The residual risk is stated: a read that returned *some* tasks but not all of
them, which Graph should not do but could, would still mark the difference
gone. Marking rather than deleting is what makes that recoverable.

Reversible: one guard in `planner.Run`.

## 2026-07-31  Export refuses to merge
Phase: not scheduled, but a v1 done criterion

`td export --json` round-tripping through import is in section 13's list and
in the definition of done, and section 16 never gave it a phase.

Decided: import restores into an empty database and refuses one that already
holds tasks. Merging two task sets means deciding what a colliding number
means, and every answer to that quietly loses somebody's data. A restore is
into a fresh database, which is what a restore is.

The write path is direct SQL rather than going through `Create` and `Patch`.
Those apply defaults, run the state machine, and write events, all of which
would rewrite the history the export exists to preserve. Sequence numbers and
timestamps come back exactly as they left, because undo walks backwards
through the log and the MCP cursor points into it.

Fold state and every credential are absent from the document. Both are
structural: they are not fields on the exported types, so leaving them out is
not a rule anybody has to remember.

Reversible: `internal/store/export.go`, and the round-trip test would catch a
change to any of it.

## 2026-07-31  The JWT encoding is written here, not imported
Phase: 9

Section 10 says to sign tokens with an asymmetric key and publish a JWKS. The
obvious build takes a JWT library.

Decided: `internal/oauth` encodes and verifies the token itself, about 200
lines. This code is both the only issuer and the only verifier and it accepts
exactly one algorithm, and the entire family of algorithm-confusion bugs that
JWT libraries keep having comes from being flexible about that. `Verify` reads
`alg` to refuse, never to dispatch: there is no code path where a header field
selects a verification strategy, so `none`, `HS256` against the public key, and
every other variant of that trick are not merely rejected but unrepresentable.

ES256 rather than RS256, so the keys are 32 bytes, signatures are 64, and a
JWKS with two live keys stays uninteresting.

Rejected: `golang-jwt/jwt`, which is fine and widely used, and which would have
brought a general-purpose parser to a problem with one input format. The
dependency fence permits it; the reasoning above is why it was not taken.

Reversible: one package with a test that would fail loudly if a replacement
behaved differently.

## 2026-07-31  The audience is one string, compared exactly
Phase: 9

RFC 7519 allows `aud` to be an array, and most libraries expose a `contains`
style check for it.

Decided: `Claims.Audience` is a single string and the comparison is `!=`. td
issues tokens for exactly one resource. An array invites the check to be
written as "contains", which is exactly the shape that lets a token minted for
another server pass here, and the spec calls audience mismatch the failure
people actually hit. A prefix comparison would let
`https://td.example.com/mcp/evil` satisfy a check against the real resource;
a trailing slash would too. The test enumerates all of those.

Reversible in principle, but it should not be: this is the check the whole
resource-indicator mechanism exists for.

## 2026-07-31  A missing PKCE method is refused, not defaulted
Phase: 9

RFC 7636 says `code_challenge_method` defaults to `plain` when absent. OAuth
2.1 removes `plain`. Both cannot be honoured.

Decided: absent is an error. Inheriting the RFC's default would mean a client
that simply omits the parameter gets the mode this server exists to reject,
which is a refusal that can be bypassed by leaving something out.

Reversible: `CheckChallenge` in `internal/oauth/pkce.go`.

## 2026-07-31  The consent screen is checkboxes
Phase: 9

Section 10 says the consent screen lets you grant less than a client asked
for. It does not say how.

Decided: one checkbox per scope, all checked, with the write scope drawn
heavier than the others. A screen with only an Approve button is a
notification rather than a decision, and the whole reason to show one is that
the answer can be "some of that". The granted set is intersected against the
requested set on the server, so the form can only narrow.

Reversible: one template and twenty lines of handler.

## 2026-07-31  OAuth grants get the settings page, not a second one
Phase: 9

Decided: grants render in a table directly under the static tokens, same
columns, same revoke button, same page. Section 10 asks for exactly this and
the reason is worth restating: claude.ai will be holding a refresh token for
your task list, and the moment you want to cut it off is not the moment to go
looking for which of two screens has the button.

Reversible: one section in `settings.html`.

## 2026-07-31  MCP tool failures are results, not transport errors
Phase: 8

A tool can fail for two different reasons: the server is broken, or the model
asked for something that does not make sense. `search_tasks` with a filter that
does not parse is the second kind.

Decided: everything a tool can be asked for that it cannot do comes back as a
`CallToolResult` with `IsError` set and the same message a person would get at
the command line. A JSON-RPC error is reserved for the server actually
failing. The difference matters to whoever is watching: a JSON-RPC error
surfaces as "the td server is broken", and a typo in a filter is not that.

Reversible: `fail` in `internal/mcpsrv/mcpsrv.go`, one function.

## 2026-07-31  Read is the floor for /mcp, and each tool checks its own scope
Phase: 8

The middleware could have required write to reach `POST /mcp` at all, since
every other POST needs it.

Decided: the endpoint requires read, and every tool checks the scope it needs
on the way in. A client holding read and capture can list all eleven tools and
is told which of them it may call. The alternative, hiding the tools it cannot
use, produces a model that does not know the capability exists and a user who
cannot tell a missing scope from a missing feature.

Rejected: filtering the tool list per credential, for the reason above, and
because a tool list that changes shape depending on who is asking is a cache
invalidation problem nobody needs.

Reversible: `requiredScope` in `internal/server/auth.go` and `requireScope` in
`internal/mcpsrv`.

## 2026-07-31  Tool output is a projection, not api.Task
Phase: 8

The obvious build returns `api.Task` from every tool and lets the schema
generator describe it.

Decided: a `TaskView` projection. A model reading a list does not need
`updated_at`, `due_is_date`, `external_rev`, or `upstream_gone`, and every
field that goes over the wire is a field it can be confused by or waste tokens
on. The projection leads with `num`, because that is what a person types and
what the model should quote back, and it carries `id` alongside for the tools
that take one.

This is also the injection boundary. Task content lands inside JSON string
fields and is never interpolated into prose the model reads as its own
context, which is what actually stops a synced Jira description from acting as
a directive. The instructions block states the rule as well; the shape of the
output is what enforces it.

Reversible: `internal/mcpsrv/tools.go`, one type and one conversion.

## 2026-07-31  Promotion is checked against the state after the patch
Phase: 7

`testdata/transition_cases.json` says the inbox-to-todo edge requires
"priority is set OR due_at is set". It does not say when. The implementation
was reading the task as it stood before the patch, so a single request that
set a priority and promoted was refused with `inbox_incomplete`, and every
client had to send two.

Decided: the requirement is evaluated against the state the patch will leave
behind. A key present in the pending write wins over what the row holds,
including an explicit null, so promoting while clearing the priority is still
refused. That last case is why the check is not "before OR after".

Rejected: making the clients send two requests, which is the same write with
a worse failure mode; and having the transition table look at the request
body, which would put HTTP shapes inside the state machine.

Reversible: it is one function, `promotable` in `internal/store/mutate.go`,
and the fixture does not pin either reading.

## 2026-07-31  Catch-up skips against the open instance, not the clock
Phase: 7

The fixture's skip case is six missed occurrences, six `recurrence.missed`
events, and exactly one open instance. Two readings produce those numbers: log
five and materialise the sixth, or log all six and leave the instance that was
already sitting there.

Decided: the second. A fixed series materialises an occurrence only when it has
no open instance; every occurrence that arrives while one is still open is
logged and dropped. This makes "exactly one open instance at a time" a property
of the code rather than an outcome that happens to hold, and it makes the
normal tick and a restart catch-up the same code path.

The consequence worth stating: completing the instance on time means the next
occurrence materialises normally, and ignoring it means the same one row stays
in the list with a growing trail of `recurrence.missed` events behind it. That
is the intent of `skip`, and the events are what make the roll-forward visible
rather than silent.

Rejected: rolling the due date of the open instance forward, which rewrites a
task the user is looking at.

Reversible: `AdvanceSeries` in `internal/store/series.go`, one loop.

## 2026-07-31  Attachment bytes are never behind a static handler
Phase: 7

Section 15 requires the auth check on every attachment download and no
guessable direct path under a static file handler. The easy build is a
`http.FileServer` over `/data/blobs` and a middleware in front of it.

Decided: the download route is an ordinary `/api/v1` handler that opens the
blob by digest and copies it out. It inherits the same authentication as every
other route rather than a second copy of it, which is the only version of this
that cannot drift. The digest is validated as 64 lowercase hex characters
before it becomes a path, and the response is always
`Content-Disposition: attachment` so a stored HTML file cannot render in the
origin with a live session attached.

Orphaned bytes are collected weekly against the whole `attachment` table with
no filter on task status: a dropped task is not deleted, and its file has to
survive an undo.

Rejected: reference counting on detach, which deletes the bytes out from under
`/undo`.

Reversible: `internal/blob` is 150 lines and knows nothing about tasks.

## 2026-07-31  A small English front end to RRULE
Phase: 7

The spec says to use RFC 5545 rather than inventing a recurrence syntax, and
that is right: no invented syntax expresses "the last weekday of the month".
But nobody types `FREQ=WEEKLY;BYDAY=MO` at a prompt.

Decided: `query.ParseRecurrence` reads the shapes people say ("every monday",
"every 2 weeks", "monthly on the 1st") and passes anything that already looks
like a rule straight through. The stored form is always RRULE. An input it does
not recognise is an error naming the word it choked on, never a guess: a wrong
guess repeats the wrong thing for a year before anyone notices.

Rejected: a date-picker UI, which is a lot of surface for a rule you set once;
and accepting free text and doing the best it can, for the reason above.

Reversible: one file, and the RRULE passthrough means nothing is unreachable
without it.

## 2026-07-31  The scheduler tick runs with reminders off
Phase: 7

`Scheduler.Run` used to return immediately when no ntfy topic was configured.
Recurrence now rides on the same tick, and so does session expiry.

Decided: the tick always runs; only the delivery pass is gated on the policy.
Tying recurrence to a push configuration would mean turning off notifications
silently stops repeating tasks from repeating, which is the kind of bug nobody
finds for a month.

Reversible: three lines in `internal/notify/scheduler.go`.

## 2026-07-31  Building past the two-week soak, on the author's instruction
Phase: 6 onward

Section 16 says to stop after step 5 and use the thing for two weeks before
building step 6, because half the requirements will change. That was raised
twice and the author asked to continue through phase 9 anyway.

Recorded rather than re-argued. The risk the spec names is real and unchanged:
phases 6 to 9 are being built against requirements that have not been tested
against use, so anything they get wrong will be discovered later and cost
more. Nothing in the build is harder to reverse because of it.

## 2026-07-31  MCP: what the 2026-07-28 revision actually says
Phase: 8 and 9

`CLAUDE.md` says to read the current documentation rather than trusting
training data on this, and that was worth doing. What the spec says, checked
against modelcontextprotocol.io rather than memory:

- **Sessions and the initialization handshake are gone.** The
  `initialize`/`initialized` exchange and the `Mcp-Session-Id` header were
  retired. Every request carries its own protocol version in an
  `MCP-Protocol-Version` header, and the protocol is stateless
  request/response over `POST /mcp`. This is a large simplification: no
  session store, no shared state between instances, no SSE requirement.
- **Protected Resource Metadata is mandatory** for the resource server, at
  `/.well-known/oauth-protected-resource` (RFC 9728).
- **Resource Indicators are mandatory** (RFC 8707). The `resource` parameter
  goes in both the authorization request and the token request, and the
  server must validate that a token's audience is itself.
- **The 401 carries the discovery chain**:
  `WWW-Authenticate: Bearer resource_metadata="...", scope="..."`. Without
  that header the client never finds the authorization server.
- **403 with `error="insufficient_scope"`** is the runtime scope failure,
  distinct from 401.
- **Client ID Metadata Documents are SHOULD; Dynamic Client Registration is
  MAY and deprecated**, retained only for servers that lack CIMD.
- **RFC 9207 `iss`** should be returned in authorization responses, and
  advertised as `authorization_response_iss_parameter_supported`.

And the thing `CLAUDE.md` predicted would be a problem is not one:
`github.com/modelcontextprotocol/go-sdk` v1.7.0 lists `2026-07-28` among its
protocol versions and ships a `StreamableHTTPHandler` with typed tool
registration. The SDK is current with the spec, so phase 8 uses it rather
than hand-rolling JSON-RPC.

The revision is pinned in the README and in the code.

## 2026-07-31  Editing shipped as part of phase 5
Phase: 5

Section 16 never scheduled editing a task, which the previous entry recorded
as a gap. Confirmed with the author: it lands in phase 5 alongside reminders,
because section 16 also says to stop after phase 5 and use the thing for two
weeks, and two weeks without being able to change a due date is a thin
experiment.

All three clients, all through the `PATCH` route that already existed:

- `td edit`, `td note`, `td snooze`
- TUI `e`, `p`, `t`, `s`, and `N` for notes
- the web detail page's form

The TUI edits a task as one line in the capture grammar rather than as a
form. The row already reads as the grammar, so editing it as the grammar is
one thing to learn rather than two, and a token dropped from the line clears
the field it set. Notes are the only multi-line thing in the product, so they
are the only place with a textarea.

`N` for notes is an addition to section 11's keymap, which has no notes key.
Everything else uses the letter section 11 assigned.

Reversible: yes.

## 2026-07-31  Reminders are off until a topic is configured
Phase: 5

Section 13 specifies ntfy with one topic. `CLAUDE.md` says the dev topic is
disposable and that nothing may be sent anywhere else.

`notify.topic` defaults to empty, and empty means the scheduler logs once and
returns without sending. A server that has not been told where to push does
not guess.

The `Sender` is an interface with an HTTP implementation and a recording fake
that the tests use. That is not indirection for its own sake: a test that
could reach a real topic is one environment variable away from doing it, and
the interface makes that structurally impossible rather than a rule someone
has to remember.

Reversible: yes.

## 2026-07-31  A failed push is retried rather than marked
Phase: 5

Section 13 says `notified_at` stops repeats: one push per task per due value.
It does not say what happens when the push fails.

`notified_at` is stamped only after the send succeeds. ntfy being down is
therefore a delay rather than a lost reminder, and the next tick tries again.

The alternative, marking before sending, would make a single network blip
silently eat a reminder, which is the one failure this feature cannot afford:
you would not know it happened.

Reversible: yes.

## 2026-07-31  The ntfy action token is write-scoped, not capture-scoped
Phase: 5

Section 13 says the Done and Snooze buttons hit the API "with a
capture-scoped token".

They carry a write-scoped one. Capture is the narrow scope that creates an
inbox item; completing a task and snoozing one are writes, and the scope
mapping in phase 2 puts them there. Making the capture scope able to complete
tasks would weaken it everywhere it is used, including on the MCP token
section 10 wants to hold read plus capture.

The blast radius is controlled the way section 15 intends instead: the token
is its own, named, and revocable on its own, so cutting it off is one revoke.

Without a token configured the notification is a click-through with no
buttons, rather than buttons that answer 401.

Reversible: yes, and it would need the scope mapping to change with it.

## 2026-07-31  Date keywords resolve against the server's clock, on input too
Phase: 5

Phase 1 decided that date keywords resolve at parse time against a supplied
clock. Phase 4 made the server send `X-Td-Now` so clients render relative
labels against the server's day rather than their own.

Input was still using the local clock. `td edit 103 -due friday` on a laptop
whose today was a Friday resolved to that day; the same word typed into the
web box, against a server whose today was a Monday, resolved four days later.
Two clients, one task, two answers.

`Client.SyncClock` fetches the health check, which needs no credential and
carries the header like every other response, before anything is resolved.
Quick-add, edit, and snooze all use it. Offline, the local clock stands in,
which is the best available answer and travels with the queued capture
either way.

Reversible: yes, at the cost of the two clients disagreeing again.

## 2026-07-31  The CLI accepts flags in any position
Phase: 5

Go's flag package stops parsing at the first non-flag argument. Every
one-shot command therefore ignored flags written after the task reference,
which is where a person puts them: `td show 103 -json` printed the human
form, `td ls "is:open" -json` folded `-json` into the filter, and `td note
103 -show` appended the literal string "-show" as a note.

`parseArgs` walks the arguments, separates flags from positionals, and hands
the flag package the order it wants. Whether a flag consumes the next
argument comes from the FlagSet, so it stays correct as flags are added.

This is the third time this has bitten: `tdd -db X account create` had it in
phase 2, and the subcommand dispatch had it again. Worth remembering that the
flag package's rule and the way people type are simply different.

Reversible: yes.

## 2026-07-31  A modal inverts its surface, and the controls have to come with it
Phase: 4

Reported: the cursor is invisible in the login form's fields.

A modal repaints itself with `--td-surface` and `--td-surface-ink`, which are
the inverse of the page. `tokens.css` had `.td-modal` overrides for the
button, the toggle's track and knob, and the link. It had none for the input.
Nor does `mockup.html`, which shows inputs inside a modal and never overrides
them either, so both authority artifacts carry the same gap.

The result: an input inside a modal draws its underline and its caret from
`--td-ink`, which is dark in the light theme where the panel is also dark,
and light in the dark theme where the panel is also light. Invisible either
way, on the one screen that is nothing but fields.

Separately, the base `.td-input:focus` inverts the field to `--td-ink` and
leaves `caret-color` at `--td-ink`, so the caret is invisible while typing in
any field anywhere. The caret is the only animation in this product.

Both fixed. The general check is now a test: for every control class, any
property painted from `--td-ink` or `--td-paper` must have a `.td-modal` rule
resetting it. Controls drawing with `currentColor` or `inherit` need nothing,
which is what the "form controls do not inherit color" note in that file is
about.

Two things worth recording about the test. The first version asked only
whether a `.td-modal` rule mentioned the class, which the `:focus` override
satisfied on its own, so deleting the rule that mattered left it green. It
checks per property now, and was verified by deleting the rule and watching
it fail. And on its first correct run it found a second instance nobody had
reported: `.td-toggle:focus-visible` outlines in `--td-ink`, so focus was
invisible on the settings modal, which is the only place toggles live.

Reversible: no reason to.

## 2026-07-31  The due date is a column with a width, not a right-aligned run
Phase: 4

Reported: the last two rows overlap into the date column when they have no
date.

`.td-due` had `margin-left: auto`, which right-aligns it, and no width. A row
whose date renders as an em dash is one character wide where a row reading
"Aug 10" is six, so its tags ran five characters further right and the tag
column went ragged.

`min-width: 8ch` and `text-align: right`: the 2ch gutter plus the six
characters of the longest date. Now it is a column.

Everything lands on a character grid is the rule this follows, and a column
that is only as wide as its content is not on a grid.

Reversible: yes.

## 2026-07-31  The CSP silently dropped every inline style, and the markup relied on them
Phase: 4

Section 15 asks for a CSP with no inline script. The policy written in phase
2 also carries `style-src 'self'`, which additionally forbids inline `style`
attributes. The templates used them for layout: `display: contents` on the
form wrappers, `display: none` on the drop control, and a handful of margins.

The browser drops those silently. Nothing errors, the page renders, and every
control that was hidden or laid out by one is simply wrong. The visible
symptom was a "drop" button on every row, and the status bar's undo rendering
as a grey default button. Neither shows up in a test that reads markup,
because the markup is correct.

Every inline style is now a class in the app layer, and a test fails on any
`style=` attribute in any rendered page. That is the same test the inline
script check should have been: written in phase 4, it looked for `<script>`
bodies and `on*=` handlers and never thought about the other half of the
policy.

Worth stating the general shape, because it will recur: a Content-Security
Policy failure is invisible from the server. The only thing that catches it
is asserting the negative in the markup, which is cheap, or opening the page,
which is not repeatable.

Reversible: no reason to.

## 2026-07-31  The asset cache key is a hash of the assets
Phase: 4

The stylesheet and scripts are served with `max-age=31536000, immutable` and
a `?v=` carrying `api.Version`.

Those two do not go together. The API version moves when the API changes,
which is unrelated to the CSS, and might not move for a year. A returning
browser would hold a stale stylesheet for as long as the cache entry lived.

The key is now a twelve character hash over the CSS and both scripts,
computed once at assembly. Same inputs, same key, so a redeploy that changes
nothing busts nothing.

Reversible: yes.

## 2026-07-31  tokens.css had drifted from mockup.html on the due-date gutter
Phase: 4

Reported: the tags run into the due date.

`mockup.html` has `.td-due { padding-left: 2ch }` and has since the
beginning. `tokens.css` had the same rule without the padding, so the only
separation was the row's 1ch flex gap, and a long tag list ended up against
the date.

Copied the mockup's value across. That is the third place the two authority
artifacts have disagreed in this phase, after the status bar grey and this;
in every case the mockup was right and tokens.css had the drift. Worth
remembering which one to trust when they differ: the mockup is the rendered
result, so a mistake in it is visible, and a mistake in tokens.css is not.

Reversible: yes.

## 2026-07-31  tokens.css painted de-emphasis with a fixed grey, against its own rule
Phase: 4

`tokens.css` opens by saying "De-emphasis is opacity, never a grey token. A
fixed grey is correct on one background and invisible on the other." Four
rules in the same file then paint text with `--td-grey` or
`--td-grey-faint`: the status bar, the counts, a completed row's title, the
disabled button, and the input placeholder.

`mockup.html`, which is the other authority for the look, uses
`opacity: var(--td-dim)` for the same status bar. So the two artifacts
disagreed, and the one that matched the stated rule was right.

Measured, on `--td-grey` against `--td-paper`:

    nord              1.69:1
    solarized-light   2.48:1
    tokyo-night       2.76:1
    dracula           3.03:1
    light             3.66:1
    dark              5.49:1

Nord at 1.69:1 is not text. That is what the reported unreadable footer was.
For comparison, ink at `--td-dim` clears 3:1 on every palette, which is the
floor section 12 sets and the reason the token exists.

All text de-emphasis now uses opacity. `--td-grey` and `--td-grey-faint` are
left for fills that are not text: the locked toggle knob and the scrollbar
thumb. Two tests enforce it, one in each direction.

Worth noting what the existing contrast test did not catch: it checks ink at
`--td-dim`, which is the rule as written, and never looked at the grey token
because the rule says the grey token is not for this. A correct check of the
wrong surface.

Reversible: yes, and the tests would fail first.

## 2026-07-31  The default theme follows the system
Phase: 4

Section 12 ships light and dark built-ins and a picker. It does not say what
a first visit gets.

It was light, which means a browser set to dark got a light page until
somebody went to settings. The default is now "auto": the page carries no
`data-theme` attribute at all, and a generated
`@media (prefers-color-scheme: dark)` rule scoped to `:root:not([data-theme])`
applies the dark palette. An explicit pick still wins, because the attribute
beats the media query.

The media query is generated from `tokens.css`'s own dark block at assembly
time rather than written out. There is one dark palette in this system and
this makes the rule quote it instead of copying it.

Rejected: detecting the preference in JavaScript, which flashes the wrong
palette on every page load and needs a script to render a colour.

Reversible: yes.

## 2026-07-31  Full width, and quick-add at the top
Phase: 4

Two layout calls, both reversed after looking at the thing on a real screen.

The app was capped at `120ch` and centred, which on a wide display puts a
third of the screen into empty gutters either side. Removed. A list is
scanned down its title column, and that column starts at the left edge
whatever the width is.

Quick-add was pinned above the status bar at the bottom, which put it a
screen away from the list on anything taller than the content, and the Add
button overflowed its bar: a `.td-btn` is four pixels taller than a row plus
a four pixel hard shadow, so it drew over the input beside it. It now sits
directly under the filter bar, in a row sized to hold a button.

Neither of these is in the spec either way. Both were mine and both were
wrong.

Reversible: yes.

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

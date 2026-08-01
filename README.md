# td

Single-user task manager with a server, a TUI, and a web UI that behaves like
the TUI.

`BUILD-SPEC.md` is what is being built. `CLAUDE.md` is how. `testdata/` is
the oracle: when the code and a fixture disagree, the code is wrong.

**Status: all 11 phases built.** Schema, event log, task CRUD, the filter
grammar, the CLI one-shots, authentication, the TUI, the web UI, editing,
ntfy reminders, people, recurrence, subtasks, triage, attachments, MCP,
OAuth 2.1, the Memos journal webhook, and the Planner mirror. Plus
`td export`/`td import`, which section 13 asks for and section 16 never
scheduled.

Section 16 says to stop after phase 5 and use it for two weeks before phase
6, on the grounds that half the requirements will change. That was raised and
overruled; see `DECISIONS.md`.

## Install

The server is a container image:

```sh
docker pull ghcr.io/harpchad/td:latest     # or :main for the tip
```

The client is a single binary with no cgo. Take the one for your machine from
the [releases](https://github.com/harpchad/td/releases), check it against
`SHA256SUMS`, and put it on your PATH as `td`.

They release separately on purpose and exchange versions on every request; the
client warns once when the major versions differ. `td --version` and
`tdd -version` each print their build and the API version they speak.

## Build and check

```sh
make tools     # install the pinned gofumpt, golangci-lint, govulncheck
make check     # the one definition of passing; CI runs this exact command
```

`make check` runs, in order: gofumpt, golangci-lint, the tests, cross-builds
for darwin/arm64 and linux/amd64 with `CGO_ENABLED=0`, govulncheck, the
import boundary test, and the `openapi.yaml` schema lint.

CI runs that exact command, and then builds the image and starts it. Release
is a separate workflow that runs the same `make check` before publishing
anything: a push to `main` gets `ghcr.io/harpchad/td:main`, and a `v*` tag
gets an immutable image plus a GitHub release with the three client binaries
and their checksums.

## Run it

```sh
make seed      # load testdata/seed.json into build/dev.db
make account   # create the one account, print TOTP and recovery codes
make token     # mint a token for the CLI
make run       # serve on 127.0.0.1:8080, pinned to the fixture's clock
make run-live  # the same server on the real clock
```

There is no signup page and no route that creates an account. Until
`tdd account create` has been run, every route but `/healthz` answers 503.

`make run` passes `-now @testdata/seed.json`, so a filter typed at the
running server returns what the case files say it should. It logs a warning
while pinned, because every date predicate and the whole sort order depend
on it.

If 8080 is taken, `make run PORT=8099`. Point the client at the same place
with `export TD_SERVER=http://127.0.0.1:8099`, or set `server` in
`config.toml`. For the container, `TD_PORT=8099 docker compose up` moves the
host side and leaves the container on 8080.

Or with the container:

```sh
docker compose up
```

The compose file publishes to the host's loopback only and passes
`-allow-unauthenticated-bind`, because phase 1 has no auth in front of the
API. Phase 2 removes both.

## Use it

```sh
go build -o build/td ./cmd/td
export TD_SERVER=http://127.0.0.1:8080   # match `make run PORT=...`
export TD_TOKEN=td_...        # from `make token`, or set it in config.toml

td a "call the dealer about the alignment"
td a 'renew wildcard cert #certs @stacey p:2 due:friday'
td ls 'is:open src:local -is:inbox -is:snoozed -is:deferred'
td show 101
td done 104
td edit 103 -priority 1 -due friday -tags certs,ops
td note 103 "Discount tire quoted 780."   # appends; -replace or -show
td snooze 103 2h                          # or a date: friday
td undo
td whoami                     # which credential is in use, and what it may do

td sub 103 "call the dealer"              # a subtask under 103
td repeat 103 "every 2 weeks"             # or: every monday, monthly on the 1st
td repeat 103                             # show the rule without changing it
td attach 103 quote.pdf                   # -list, -get <id>, -rm <id>
td triage                                 # work the inbox one task at a time
```

Flags work before or after the task number, and date keywords resolve against
the server's clock rather than the laptop's, so `friday` means the same day
whichever client typed it.

## The TUI

`td` with no arguments opens it. `td 'is:waiting'` opens it on a filter.

```text
j k      move            a  add            z  fold      / search
g G      top, bottom     d  done           Z  fold all  1-9 saved filters
enter    detail          x  drop           u  undo      ?  keys
space    toggle done     r  reload         esc back     q  quit
```

The mouse is on by default and does what the web pointer does: click a row to
select, double-click to open, click the checkbox to toggle, click the fold
cell to fold, click a `#tag` or `@person` to filter by it, click a hint in
the bottom bar to run it. The wheel scrolls without moving the selection.

Capturing the mouse takes the terminal's own text selection away. Most
emulators hand it back while shift is held. `td --no-mouse` turns it off, as
does `mouse = false` in `config.toml`.

The TUI reads no theme file. It renders through the terminal's own ANSI
palette, so if you run Tokyo Night in your terminal, the TUI is already Tokyo
Night.

`td a` exits immediately whether or not the server is reachable. If it is
not, the capture queues locally and `td flush` sends it. Every command takes
`--json`.

Config lives in `$XDG_CONFIG_HOME/td/config.toml`, falling back to
`~/.config/td/`. A commented default is written on first run. `TD_SERVER`,
`TD_TOKEN`, and `TD_TIMEZONE` override it.

## Auth

One account. Password with argon2id, TOTP required at enrollment, ten
one-time recovery codes. Five failed password attempts lock the account for
fifteen minutes, and failed TOTP attempts are counted separately. The login
route is limited to ten attempts per IP per minute.

Browsers get a session cookie (`HttpOnly`, `Secure`, `SameSite=Lax`).
Everything else gets a `td_` bearer token in the Authorization header, never
in a query string. Tokens are scoped (`read`, `write`, `capture`,
`sync:<source>`), individually revocable, and carry the actor their writes
appear under in the event log:

```sh
tdd token create -name tui     -scopes read,write,capture
tdd token create -name claude  -actor mcp:claude    -scopes read,capture
tdd token create -name planner -actor plugin:planner -scopes sync:planner
tdd token list
tdd token revoke <id>
tdd account log                # the auth history, with source IPs
```

Two deployment notes. `-base-url` is required and the server refuses to
start without it. And behind a reverse proxy, set `-trusted-proxies` to the
proxy's CIDR: with nothing trusted, `X-Forwarded-For` is ignored, every
request looks like it came from the proxy, and the per-IP login limit
becomes one global limit.

## The web UI

Open the server's base URL in a browser. Server-rendered Go templates plus a
vendored htmx, one stylesheet, no build step. The keymap is the TUI's, key
for key, and every action is a real form: with JavaScript off the forms post
and the server redirects.

`tokens.css` and `themes.css` at the repository root are the visual system
and outrank anything written about it, including this file. They are mirrored
into `internal/web/css` because `go:embed` cannot reach outside a package;
a test fails the build if the two drift, and `make sync-css` fixes it.

Themes ship as Light, Dark, Nord, Solarized Light, Dracula, and Tokyo Night.
Drop a `[data-theme="name"]` block into `$XDG_CONFIG_HOME/td/themes/*.css` to
add one. A palette that does not clear 4.5:1 for ink on paper, or 3:1 for ink
at `--td-dim`, is logged and skipped rather than loaded unreadable.

## Reminders

Off until you set a topic. `config.toml` is written commented on first start
at `$XDG_CONFIG_HOME/td/config.toml`:

```toml
[notify]
topic        = "https://ntfy.example.com/td"
default_rule = "p:<=2"        # resolves notify = "auto"; "*" always, "" never
lead_minutes = 30             # before a datetime due
quiet_hours  = "22:00-06:00"  # holds a push, does not drop it
date_only_at = "08:00"        # when a date-only due fires
action_token = ""             # tdd token create -name ntfy -scopes write
```

Only `due_at` fires. One push per task per due value: changing the due date
makes it eligible again, and an overdue task is never pushed twice. Per task,
`notify` is `auto`, `on`, or `off` — three states, because a checkbox cannot
express "whatever the default says".

Without an action token the push is a click-through. With one it carries Done
and Snooze 1h buttons.

## Filter grammar

Terms are space separated and AND by default. `-` negates, `|` is OR, and
parentheses override precedence. `-` binds tightest, then AND, then `|`.

```text
is:open is:done is:todo is:doing is:waiting is:inbox is:dropped
is:orphan is:snoozed is:deferred
p:1 p:<=2
due:today due:<=friday due:overdue due:none
start:<=today
#vpn #proj/monday               tags
@mikah @mikah:waiting           person, optionally by role
grp:leadership
src:jira src:local
has:attachment has:notes has:sub
notify:on notify:off notify:auto
"exact phrase"                  FTS5 phrase over title and notes
free text                       FTS5 prefix match, so cert finds certs
```

Dates accept `today`, `tomorrow`, `yesterday`, weekday names, `eow`, `eom`,
`+3d` / `+2w` / `+1m`, and `YYYY-MM-DD`. A bare weekday means its next
occurrence strictly after today, except when today already is that weekday.
Everything resolves in the configured timezone, not the container's.

## Layout

```text
cmd/td            client only
cmd/tdd           server only
internal/api      request and response types, API version constant
internal/query    filter grammar, sort comparator, capture parser
internal/auth     argon2id, TOTP, tokens, recovery codes  <- cmd/tdd only
internal/tui      Bubble Tea v2                           <- cmd/td only
internal/web      templates, htmx, the stylesheet         <- cmd/tdd only
internal/notify   reminder policy, ntfy, the scheduler    <- cmd/tdd only
internal/store    SQLite, migrations, FTS      <- cmd/tdd only
internal/server   HTTP surface                 <- cmd/tdd only
internal/client   HTTP client, config, offline queue
internal/seed     testdata/seed.json loader
```

`internal/query` is the important shared one. One grammar, one parser, no
drift between what the client validates and what the server executes.
`internal/boundary` walks the real import graph and fails the build if
`internal/store` ever reaches `cmd/td`.

## MCP

td serves the Model Context Protocol at `POST /mcp`, in the same binary and
over the same service layer everything else uses.

**The revision is `2026-07-28`**, pinned in `internal/mcpsrv.Revision` and
asserted by a test. That revision removed sessions and the
`initialize`/`initialized` handshake, so the protocol is stateless
request/response and `POST` is the only method the endpoint answers; `GET`
and `DELETE` were the session transport. It also made RFC 9728 Protected
Resource Metadata mandatory, which td serves unauthenticated at
`/.well-known/oauth-protected-resource`.

Authentication is whatever the HTTP layer already accepts. A `td_` token in
an `Authorization: Bearer` header works today, with the same scopes and the
same revoke button as the rest of the API:

```jsonc
// ~/.claude.json, or any client that lets you set a header
{
  "mcpServers": {
    "td": {
      "type": "http",
      "url": "https://td.example.com/mcp",
      "headers": { "Authorization": "Bearer td_..." }
    }
  }
}
```

Mint one with `tdd token create -name claude -actor mcp:claude -scopes read,capture`.
Give the everyday assistant read plus capture and keep write for a token you
paste deliberately: capture drops things in the inbox, and everything that
lands there gets sorted by you. Agent mutations carry `actor = mcp:<name>` in
the event log, so a bad batch is one `td undo` loop away from gone.

An unauthenticated request answers 401 with
`WWW-Authenticate: Bearer resource_metadata="...", scope="td:read"`. That
header is the whole of client discovery. A valid credential missing a scope
answers 403 with `error="insufficient_scope"` instead, which tells a client
to ask for more scope rather than to start the authorization dance again.

**One deployment gotcha, because it looks like an application bug.** The
reverse proxy has to pass `/.well-known/*` through to td.
nginx-proxy-manager already intercepts `/.well-known/acme-challenge/` for
certificate issuance, and a rule broad enough to swallow the rest of
`/.well-known/` breaks discovery entirely. Check that before debugging
anything else.

Tool output is data, never instructions. A Jira description synced in from an
external reporter becomes text an agent reads, so nothing is interpolated
into prose the model takes as its own context, and no tool completes or
transitions a task on the strength of content that came from somewhere else.
The server states that rule in its instructions block as well as enforcing it
in the shape of the output.

## OAuth 2.1

td is its own authorization server as well as its own resource server.

That is not gold plating. claude.ai's custom connector UI takes an OAuth
client id and secret and has no field for a bearer token or a custom header,
so a static token cannot be used there at all. There is one user and no
external identity provider, so `/authorize` reuses the password and TOTP
login that already exists and the consent screen is a form on the same
origin.

```text
GET  /.well-known/oauth-protected-resource   RFC 9728. resource is the MCP URL
GET  /.well-known/oauth-authorization-server RFC 8414
GET  /.well-known/jwks.json                  two live keys
GET  /authorize    authorization code, PKCE S256 only
POST /token        an ES256 JWT whose aud is the resource, plus a refresh token
POST /register     Dynamic Client Registration
POST /revoke       RFC 7009
```

Add it to claude.ai as a custom connector pointing at `https://<host>/mcp`.
The redirect URI to allow is `https://claude.ai/api/mcp/auth_callback`. Scopes
are `td:read`, `td:capture`, and `td:write`, and the consent screen is
checkboxes rather than one Approve button, so you can grant less than was
asked for.

Grants appear on the settings page next to the static tokens with the same
revoke button. Revoking cuts the client off immediately, including the
refresh token it is holding.

What is enforced, each of it a test:

- PKCE `S256` only. `plain` is refused, and so is a missing
  `code_challenge_method`: RFC 7636 says the default is `plain`, and
  inheriting that default would hand a client the mode this server exists to
  reject.
- `resource` is required on the authorization request and must equal the MCP
  URL exactly. The token's `aud` is that value, compared as an exact string.
  An audience mismatch is the failure people hit, and it is what stops a token
  minted for another server from being replayed here.
- `redirect_uri` is matched exactly against what the client registered. An
  unregistered one renders an error rather than redirecting anywhere.
- Refresh tokens rotate on every use. A refresh may narrow the scopes and
  never widen them.
- `client_credentials` is not implemented and is not advertised. Every token
  here acts as the one account holder, and that needs a person in the loop.
- No token is ever accepted in a query string.
- Signing keys are ES256 and two are live at once, so rotating does not
  invalidate every session. `tdd` generates both on first start.

The JWT encoding is written by hand in `internal/oauth` rather than taken
from a library. This code is both the only issuer and the only verifier, it
accepts exactly one algorithm, and the whole family of algorithm-confusion
bugs that JWT libraries keep having comes from being flexible about that:
`alg` is read to refuse, never to dispatch.

`/register` is client registration, not user registration. It creates no
account and grants no access; a registered client still has to send its user
through `/authorize`. There is still no signup page and no password reset
route.

## The journal

Completed tasks post themselves to Memos, so the journal fills itself. One
direction only: nothing in Memos can create or change a task, because a
journal that makes work is a second inbox and there is already one inbox.

```toml
[memos]
url        = "https://memos.example.com"
token      = ""              # a Memos access token
visibility = "PRIVATE"       # a task manager's contents are not a blog
tag        = "td"            # prepended, so the entries are filterable
```

Off until you set a URL. It follows a cursor into the event log rather than
firing inline from the completion, which is what makes both of the properties
worth having true at once: a completion is never lost because Memos was
restarting, and never posted twice because something retried. Completing a
task cannot fail because a journal is down.

Switching it on starts from that moment rather than posting a year of
history. A memo is dated by when the work finished, not by when it was
delivered, so an entry that arrives after an outage is still right about the
one fact it exists to record.

## Sync plugins

One way. A mirrored task carries `external_url` and the detail view puts it
on the first line, so one keystroke opens the real thing. That removes the
callback path, the action queue, and the whole class of failures where a
remote write half-succeeds and the two systems disagree about what happened.

```sh
td sync planner -n     # read and translate, post nothing
td sync planner        # mirror it
```

```toml
[planner]
plans       = ["xqQg5FS2LkCp935s-FIFm2QAFkHM"]
graph_token = ""       # Tasks.Read and User.ReadBasic.All
# graph_token_command = "az account get-access-token --resource https://graph.microsoft.com --query accessToken -o tsv"
```

Mint the token with
`tdd token create -name planner -actor plugin:planner -scopes sync:planner,read`.
The scope is per source, so a Planner token cannot write the Jira mirror and
reaches nothing else in the API. Everything it writes is attributed to
`plugin:planner` and is one `td undo` loop away from gone.

**Field ownership is the core rule**, and it is enforced by the server rather
than by each plugin:

| Upstream owns, overwritten every sync | Yours, never touched |
| --- | --- |
| title, status, due date | priority, notes, tags |
| external URL and revision | person links, snooze, start |
| | notify, effort, parent, series |

One exception runs the other way: a task you completed or dropped in td keeps
that status whatever the ticket says. Completing a mirrored task is a
statement about your own work, and a sync that reopened it every fifteen
minutes would make the mirror an argument rather than a list.

Planner's own priority is deliberately not mapped. It is set by whoever made
the card; td's priority is your answer to "what should I do next", and those
are different questions.

### Who is who

An upstream account has to be attached to a person, or the mirror arrives with
nobody on it. A sync resolves that three ways, in descending order of
evidence: an identity already mapped, an email that matches a person's exactly
and uniquely, or nobody holding that handle yet, in which case a new person is
created.

It never matches on a name. Two people called Stacey is ordinary, and merging
them is not something you notice afterwards by looking at the list. Anything
it will not place is **reported**, once per person, with the command that
fixes it:

```text
td: 3 upstream people could not be matched to anybody, so those links are missing:
  Stacey Whitlock <stacey@example.invalid>
      somebody already has that handle, so this was not guessed at
      td person map <handle> planner 8f3d2e11-0000-4a2b-9c3d-000000000001
```

Mapping is permanent and the next sync takes the certain path. Filling in
`email` on your people is the cheaper route: the match then happens on the
first sync and records the mapping for you.

Because an item whose revision has not moved is skipped entirely, a person you
map after the fact is not backfilled until something upstream changes.
`td sync planner -relink` re-applies everything and does it now.

Items that disappear upstream are marked `upstream_gone`, never deleted: a
ticket you can no longer see is not a ticket that never existed, and
something in your notes probably refers to it. Planner has no delta query, so
"gone" is computed by subtraction from a full read, and a read that returned
nothing never marks anything gone. An expired token looks exactly like an
emptied plan, and only one of those should delete your mirror.

## Backup

```sh
td export --json -o td-$(date +%F).json    # goes back in
td export --markdown -o vault/             # one file per task, for Obsidian
td import td-2026-07-31.json               # into an empty database
```

The JSON round-trips: export, restore into a fresh database, export again,
and the two files are byte-identical. That is a test, because a backup you
have never restored is a hypothesis.

It carries tasks, people, groups, identities, saved filters, series,
attachment rows, and the whole event log with its sequence numbers intact, so
undo and the MCP change cursor still work after a restore. It carries no
credentials at all, so a file copied to object storage cannot log anybody in,
and no fold state, which is view state.

Import refuses a database that already has tasks. Merging two task sets means
deciding what a colliding number means, and there is no answer to that which
is not somebody's data quietly disappearing.

`litestream` to object storage is still the right continuous answer. This is
the one you can read.

## API

`openapi.yaml` is the reference and is validated on every `make check`,
including a test that fails if a route exists without a matching entry.

The client sends `X-Td-Client` with its version and the server returns
`X-Td-Server` on every response. The client prints one warning line when the
major versions differ.

Every response also carries `X-Td-Now`, the server's clock in its configured
timezone. Clients render relative dates against it rather than against their
own wall clock, so a client in another zone does not label a different set of
tasks "Today" than the server bucketed there.

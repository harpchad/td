# td

Single-user task manager with a server, a TUI, and a web UI that behaves like
the TUI.

`BUILD-SPEC.md` is what is being built. `CLAUDE.md` is how. `testdata/` is
the oracle: when the code and a fixture disagree, the code is wrong.

**Status: phase 6 of 11.** Schema, event log, task CRUD, the filter grammar,
the CLI one-shots, authentication, the TUI, the web UI, editing, ntfy
reminders, and people.

Section 16 says to stop after phase 5 and use it for two weeks before phase
6, on the grounds that half the requirements will change. That was raised and
overruled; see `DECISIONS.md`.

## Build and check

```sh
make tools     # install the pinned gofumpt, golangci-lint, govulncheck
make check     # the one definition of passing; CI runs this exact command
```

`make check` runs, in order: gofumpt, golangci-lint, the tests, cross-builds
for darwin/arm64 and linux/amd64 with `CGO_ENABLED=0`, govulncheck, the
import boundary test, and the `openapi.yaml` schema lint.

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

# td

Single-user task manager with a server, a TUI, and a web UI that behaves like
the TUI.

`BUILD-SPEC.md` is what is being built. `CLAUDE.md` is how. `testdata/` is
the oracle: when the code and a fixture disagree, the code is wrong.

**Status: phase 1 of 11.** Schema, event log, task CRUD, the filter grammar,
and the CLI one-shots. No authentication yet, which is why the server
refuses to bind anything but a loopback address. No TUI and no web UI yet.

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
make seed      # load testdata/seed.json into build/dev.db, fixed clock and all
make run       # serve on 127.0.0.1:8080
```

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
export TD_SERVER=http://127.0.0.1:8080

td a "call the dealer about the alignment"
td a 'renew wildcard cert #certs @stacey p:2 due:friday'
td ls 'is:open src:local -is:inbox -is:snoozed -is:deferred'
td show 101
td done 104
td undo
```

`td a` exits immediately whether or not the server is reachable. If it is
not, the capture queues locally and `td flush` sends it. Every command takes
`--json`.

Config lives in `$XDG_CONFIG_HOME/td/config.toml`, falling back to
`~/.config/td/`. A commented default is written on first run. `TD_SERVER`,
`TD_TOKEN`, and `TD_TIMEZONE` override it.

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

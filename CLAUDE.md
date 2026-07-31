# CLAUDE.md

Working rules for building `td`. Read `BUILD-SPEC.md` first. It is the
source of truth for what to build. This file is the source of truth for
how to work.

Three artifacts outrank the prose in `BUILD-SPEC.md` when they
disagree with it:

- `testdata/` for behavior
- `tokens.css` and `themes.css` for the visual system
- `mockup.html` for what the home view, the controls, and the modal
  chrome actually look like

Prose describes intent. Those files are the intent, executed.

You have free run. Nobody reviews a pull request before you continue.
That trust is the reason the fences below are absolute rather than
advisory: they are the only thing standing between an unattended agent
and a codebase that works but is not the thing that was specified.

---

## The one rule that outranks the rest

`testdata/` is the oracle. If the implementation disagrees with a case
in `testdata/`, the implementation is wrong.

Do not edit a case to make a test pass. Do not add a special case to
satisfy one fixture. If you believe a fixture is genuinely incorrect,
stop work on that component. Write the argument in `DECISIONS.md` under
a `FIXTURE-DISPUTE` heading, implement the behavior the fixture
specifies anyway, and continue. The dispute gets read later.

The expected values in `testdata/` were computed independently of any
implementation. Two of them were computed wrong on the first pass and
corrected. Assume the remaining ones are right.

---

## Scope fence

These are categorical. No exceptions, no case-by-case judgment.

- No ORM. Write SQL.
- No web framework. `net/http` and the standard library.
- No JavaScript framework and no frontend build step. Server-rendered
  templates plus htmx, loaded from a vendored file.
- No CSS framework. One hand-written stylesheet.
- `cmd/td` builds with `CGO_ENABLED=0` and cross-compiles to
  `darwin/arm64`. Verify this in CI on every commit.
- `internal/store` must never appear in the import graph of `cmd/td`.
  Enforce it with a test that walks the graph and fails the build.

If a dependency you want would violate one of these, you do not want
that dependency.

## Dependencies

Take what you want inside the fence. Three conditions:

1. Pin an exact version. Never a branch, never `latest`.
2. Add a line to `DEPENDENCIES.md`: what it is, what it replaced, and
   what you considered instead.
3. `govulncheck` runs in CI and a finding fails the build.

Run `go mod tidy` before every commit.

## Never touch production

Do not use credentials for NCM's Planner, monday.com, or Jira. Sync
plugin work uses recorded HTTP fixtures committed under
`testdata/plugins/`. If you need a response shape you do not have,
write the fixture by hand from the vendor's published API
documentation.

The ntfy topic in the dev environment is disposable. Do not send to any
other topic.

---

## Phase protocol

Phases are listed in `BUILD-SPEC.md` section 16. For each one:

1. Read the relevant `testdata/` cases first.
2. Write the tests from those cases before the implementation.
3. Implement until `make check` passes.
4. Commit. Conventional commits, phase number in the body.
5. Append to `BUILD-LOG.md`: what shipped, what you learned, what you
   deferred.

Do not start a phase before the previous phase's tests pass.

Never block waiting for a human. When the spec is ambiguous, choose the
option that is easier to reverse later, record it in `DECISIONS.md`
with the alternatives you rejected, and keep moving. Prefix anything
you are unsure about with `REVIEW:` so it can be found in one grep.

## make check

One command. It runs, in order:

1. `gofumpt -l .` with any output failing the build
2. `golangci-lint run`
3. `go test ./...`
4. `go build` for `darwin/arm64` and `linux/amd64`, client with
   `CGO_ENABLED=0`
5. `govulncheck ./...`
6. the import boundary test
7. `openapi.yaml` schema lint (phase 1 onward)

CI runs the same command. There is no second definition of passing.

## Files you maintain

- `DECISIONS.md` for every deviation from the spec and every ambiguity
  you resolved. Newest first. This is the file that gets read.
- `DEPENDENCIES.md` for every third-party module.
- `BUILD-LOG.md` for narrative progress, one entry per phase.

Do not rewrite `BUILD-SPEC.md`. If the spec is wrong, note it in
`DECISIONS.md`.

---

## Definition of done, per phase

A phase is done when all of these hold:

- `make check` passes
- every `testdata/` case covering that phase's components passes
- every new exported function has a doc comment
- the phase's entry exists in `BUILD-LOG.md`
- no `REVIEW:` marker sits on a code path that runs by default

## Definition of done, v1

- `td a "something"` from a cold shell creates a task in the inbox
- the TUI and the web UI show the same list in the same order for the
  same filter
- a reminder arrives on the phone for a task due in five minutes
- the server survives `docker compose down` and `up` with data intact
- `td export --json` round-trips through import with no loss
- a Planner mirror populates from fixtures and links back to the
  original item
- td connects as a claude.ai custom connector and a tool call
  round-trips. This is the end-to-end test for the OAuth work, and
  nothing short of it counts.

---

## Security assertions

The server is internet-facing. Each of these is a test, not a review
item.

- Any `/api/v1/*` request without a valid token returns 401 with an
  empty body.
- A login attempt with an unknown account and one with a known account
  return the same status, the same body, and timings within 50ms.
- Five failed password attempts lock the account for 15 minutes.
- Failed TOTP attempts count separately from failed passwords.
- The session cookie carries `HttpOnly`, `Secure`, and
  `SameSite=Lax`.
- No route serves an attachment without checking auth first.
- A database dump contains no usable token and no plaintext password.
- Each recovery code works exactly once.
- The response carries HSTS, `X-Frame-Options: DENY`, and a CSP with no
  `unsafe-inline`.
- There is no registration route and no password reset route. OAuth
  client registration is not user registration and creates no account.
- A token whose audience does not exactly match the resource is
  rejected.
- `/authorize` rejects PKCE `plain` and rejects a missing challenge.
- No endpoint accepts a token in a query string.

## Check the spec revision, do not trust training data

Two moving targets will be wrong in whatever you remember about them.
Read the current documentation before writing either one.

- **MCP authorization.** It changed three times in a year and the
  newest revision landed on 2026-07-28. Protected Resource Metadata and
  Resource Indicators are mandatory, Client ID Metadata Documents are
  replacing Dynamic Client Registration, and sessions and the
  initialization handshake are gone. Pin a revision, name it in the
  README, and check what the Go SDK implements before choosing.
- **Bubble Tea v2.** Mouse events are separate message types and mouse
  mode is a `View()` field, not a program option. Most example code in
  circulation is v1.

## Performance targets

Measured against 5,000 tasks and 20,000 events. Treat a miss as a bug
with an issue, not as a reason to stop.

- List query with a filter: under 25ms server-side at p95
- Full-text search: under 50ms at p95
- TUI keystroke to redraw on a cached list: under 16ms
- Cold start to serving: under 200ms
- Container resident memory at idle: under 100MB

## Do not build

The spec is deliberately small. Adding to it is the most likely way
this goes wrong.

No write-back to source systems. No mobile app. No collaboration, no
sharing, no second user, no OAuth. No time tracking. No kanban board,
no calendar view, no Gantt. No plugin marketplace or plugin installer
UI. No AI features inside the product. No notification channels beyond
ntfy. No theme editor, no theme gallery, no live preview: themes are
files, and the picker is a list.

If you finish early, write tests.

---

## Environment

`docker compose up` starts the server with a scratch database.
`make seed` loads `testdata/seed.json`, including the fixed clock, so
every fixture in that directory evaluates the way the case files say it
does. `TD_NTFY_TOPIC` points at the disposable topic. `make check`
needs no network beyond the module proxy.

# Dependencies

Every third-party module, why it is here, and what was considered
instead. Exact versions only, never a branch or `latest`.

Format:

```text
## module@version
Phase: N
Used for: ...
Considered instead: ...
```

---

## modernc.org/sqlite@v1.55.0
Phase: 1
Used for: the database. Schema, migrations, FTS5 full-text search, and the
`td_local_date` scalar function every date predicate goes through.
Considered instead: `github.com/mattn/go-sqlite3`, which section 1 of the
spec recommends. It needs cgo, and `make check` builds `linux/amd64` from a
Mac, so taking it would mean CI and the local run disagreeing about what
passing means. The spec's other argument for it, that a pure-Go driver costs
full FTS5, no longer holds: this driver ships the amalgamation with FTS5
compiled in. External-content tables, prefix queries, phrase queries, and
user-defined functions were all verified before committing to it. The full
argument is in `DECISIONS.md`.
Binary impact: server only. The import-boundary test fails the build if it
ever reaches `cmd/td`.

## github.com/oklog/ulid/v2@v2.1.2
Phase: 1
Used for: task, person, tag, and attachment ids. Clients generate their own
so quick-add returns before the server answers and a replayed capture cannot
create a second row.
Considered instead: `github.com/google/uuid`. UUIDv4 does not sort, and a
lexicographically sortable id is what lets the seed loader produce a stable
ordering and lets a future keyset cursor exist at all. UUIDv7 would work and
would have meant taking a larger module for one function.
Binary impact: both. Pure Go, no cgo.

## github.com/BurntSushi/toml@v1.5.0
Phase: 1
Used for: reading `$XDG_CONFIG_HOME/td/config.toml`. Section 14 fixes the
format, and sections 13 and 15 both specify config in TOML.
Considered instead: hand-parsing a two-key file, which stops being tempting
the moment the `[notify]` table from section 13 lands. Also considered
`github.com/pelletier/go-toml/v2`, which is faster and larger; config is
read once at startup, so speed is not the axis that matters here.
Binary impact: both.

## github.com/getkin/kin-openapi@v0.145.0
Phase: 1
Used for: the `openapi.yaml` schema lint in `make check`, and the test that
checks the document against the mux so a route cannot be added without a
matching entry.
Considered instead: `github.com/pb33f/libopenapi`, which supports OpenAPI
3.1 where this one stops at 3.0. Nothing in the API needs 3.1, and this
validates against the meta-schema without a network call. Also considered
shelling out to `spectral`, which would put Node in the build.
Binary impact: neither. Test-only, and it does not appear in either
binary's import graph.

## golang.org/x/crypto@v0.54.0
Phase: 2
Used for: argon2id password hashing, which section 15 names directly.
Considered instead: nothing seriously. `golang.org/x/crypto/argon2` is the
reference implementation and argon2id is what the spec asks for. `bcrypt`
would have been the fallback if this were unavailable; it is weaker against
GPU attack and the spec did not ask for it.
Binary impact: server only. The import boundary test fails the build if
argon2 ever reaches `cmd/td`, which holds a bearer token and hashes nothing.

## github.com/pquerna/otp@v1.5.0
Phase: 2
Used for: TOTP generation and validation, and the otpauth:// enrolment URI
`tdd account create` prints.
Considered instead: implementing RFC 6238 by hand, which is about forty
lines and is a bad place to be clever. This library is the one everything
else in Go uses, has no dependencies beyond a barcode package, and gets the
skew window and the URI format right.
Binary impact: server only, and on the forbidden list for `cmd/td`.

## golang.org/x/term@v0.45.0
Phase: 2
Used for: reading the password without echo in `tdd account create`.
Considered instead: reading a plain line, which prints the password to the
terminal and into the scrollback. The command falls back to a plain read
when stdin is not a terminal, which is what makes it testable.
Binary impact: server only.

---

## Tools

Pinned in the `Makefile` and installed by `make tools`. Not modules of this
project, but they define what passing means, so they are versioned here too.

- `mvdan.cc/gofumpt@v0.9.1` for formatting
- `golangci-lint@v2.6.1` for linting, configured in `.golangci.yml`
- `golang.org/x/vuln/cmd/govulncheck@v1.1.4` for vulnerability scanning

The Go toolchain itself is pinned in `go.mod` (`toolchain go1.26.5`) and in
the `Dockerfile`. See `DECISIONS.md` for why that version specifically.

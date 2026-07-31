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

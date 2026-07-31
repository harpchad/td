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

# testdata

The oracle for `td`. Every file here describes behavior that was
decided deliberately, and several cases encode a choice that a
reasonable implementation would otherwise get wrong.

Read `CLAUDE.md` before using these. The short version: when the code
and a fixture disagree, the code is wrong.

## Files

- `seed.json` sets a fixed clock (2026-08-03T10:30:00-05:00, a Monday,
  America/Chicago) and 14 tasks, 3 people, and 1 group. Every other
  file evaluates against it. Load with `make seed`.
- `filter_cases.json` holds the grammar EBNF, precedence rules, date
  keyword vocabulary, 20 query cases with expected task numbers, 4 AST
  cases, and 6 parse errors with their exact messages.
- `recurrence_cases.json` holds fixed-mode and after-completion cases,
  catch-up behavior, and the DST edges. Values came from
  python-dateutil and IANA tzdata rather than from arithmetic on paper.
- `sort_cases.json` defines the six-key comparator and two ordering
  cases with per-row reasoning.
- `transition_cases.json` is the complete status state machine, the
  field side effects, and the undo contract.

## Traps worth knowing about

Three cases exist specifically because the obvious implementation fails
them.

**OR precedence.** `is:open #monday | #finance` parses as
`(is:open AND #monday) OR #finance`, so a completed task carrying
`#finance` matches. Applying `is:open` across the whole expression is
the natural mistake and returns three rows instead of four.

**DST wall clock.** A weekly task at 09:00 on 2026-10-26 recurs at
09:00 on 2026-11-02, with the UTC offset moving from -05:00 to -06:00.
Adding 168 hours produces 08:00 and passes every other weekly test.

**Bucket outranks priority.** A P4 due today sorts above a P1 due
tomorrow. This looks like a bug in a screenshot and is not one.

## Adding cases

New cases are welcome and new files are not. Compute expected values
independently before writing them down, and say in the case note how
they were derived.

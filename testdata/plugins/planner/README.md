# Planner fixtures

Written by hand from Microsoft Graph's published `plannerTask`,
`plannerPlan`, and `user` resource documentation. No live tenant was
contacted and no NCM credentials exist anywhere in this repository.

The identifiers are structurally realistic and semantically fake. The
GUIDs are made up; the base64-looking plan and task ids follow Graph's
shape so the plugin's parsing is exercised the way it will be in
production.

`tasks.json` is the first read. `tasks_updated.json` is the same plan a
few minutes later and is what the idempotence and field-ownership tests
run against:

- `01_TASK_RENEW` changed title and `percentComplete`, and its
  `@odata.etag` moved. It must update.
- `01_TASK_SOC2` is byte-identical with the same etag. It must not
  produce a write or an event.
- `01_TASK_TIRES` is absent, so it appears in `gone` and the mirror is
  marked `upstream_gone` rather than deleted.
- `01_TASK_ONBOARD` is new in the second read.

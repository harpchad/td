# Mail fixtures

Written by hand from Microsoft Graph's published `message`, `followupFlag`,
`recipient` and `emailAddress` resource documentation. No live mailbox was
contacted and no live credentials exist anywhere in this repository.

The addresses are `example.invalid`, which is reserved and cannot resolve. The
message ids follow Graph's shape, which is a long opaque base64-ish string,
so the plugin's handling of them is exercised the way it will be in
production. They are made up.

`flagged.json` is one page of `GET /me/messages?$filter=flag/flagStatus eq
'flagged'`. It covers, in order:

- `AAMkAG_RENEWAL` a plain flagged message with no due date on the flag.
- `AAMkAG_CONTRACT` a flag carrying `dueDateTime`, which becomes the task's
  due date. Graph returns that as UTC and it has to land as a local calendar
  date, so this one is 04:00Z and must not become the previous day.
- `AAMkAG_NOSUBJECT` an empty subject, which is legal in mail and must not
  produce a task with no title.
- `AAMkAG_COMPLETE` `flagStatus: complete`, which the filter would normally
  exclude. It is here so the plugin's own check is tested rather than trusted
  to the query string.

`flagged_page2.json` is the continuation `flagged.json` points at with
`@odata.nextLink`, holding `AAMkAG_INVOICE`. Paging is not optional: a
mailbox with more flags than a page would silently capture only the first
page without it.

`flagged_later.json` is the same mailbox on a later run. `AAMkAG_RENEWAL` is
still flagged and must not be captured a second time; `AAMkAG_LATER` is new.

# Shared view contract fixtures

One JSON file per scenario under `testdata/views/`. The Go runner
(`internal/repository/views_contract_test.go`, `TestViewContractFixtures`)
and the frontend Vitest runner both load these files, so the local view
builders cannot drift from the server views.

Each fixture: `name`, `now` (RFC 3339 instant pinned via SetClock),
`timezone` (owner/server location), `evening_starts_at` (HH:MM),
`seed` (areas, projects, headings, tags, tasks, schedules — omitted
fields take the DB defaults), and `expect`. Rows compare IN ORDER.

Expect keys: `inbox`, `anytime`, `someday`, `logbook`, `trash`
(flat task lists, flattened in view traversal order), `today`
(Today/This Evening/Overdue/Earlier sections in view order),
`upcoming` (overdue rows, then date groups, then earlier rows — the
runner calls Upcoming with the default from, so dates start tomorrow),
`project:<id>` / `area:<id>` / `tag:<id>` (detail listings), and
`counts`.

Row shape: `{"task": id, "entry": schedule id or null}` plus
`"section": today|evening|overdue|earlier` for section rows or
`"date": YYYY-MM-DD` for grouped rows (logbook/trash/upcoming dates).

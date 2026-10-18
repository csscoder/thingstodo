# Web UI and Sync Correctness Against the Hardened API — Design

Date: 2026-10-18
Status: draft, owner answered Q1–Q4 on 2026-10-18; awaiting approval of the document

## Intent

Projects 1 and 2 (`docs/review-and-roadmap.md` §7) reworked the server: validation at the boundary, atomic and idempotent writes, the cross-origin session guard, view rules in the server timezone, completion that runs once. The built-in web UI was only kept compiling. The owner has not re-checked the UI since and wants it re-verified across every task lifecycle against the new API.

A re-check on 2026-10-18 at `bbcfe46` found that the UI is not just "secondary and slightly off". Some of it is broken by the new server, and some old bugs are now more exposed:

- Every write from `npm run dev` gets 403 (cross-origin session guard vs Vite's proxy). [probe]
- After a first load or a 410, Trash is empty: `/sync/full` omits trashed tasks and `fullSync` clears the local table first. [code]
- The server now rejects payloads the UI produces (font size 15/17, deadline before when, end time past midnight, a synthetic schedule with `when_date: ''`). The sync queue keeps a rejected change forever, re-sends it every 30 s, and stops pulling that entity. [probe]
- Re-sent creates of attachments, checklist items, reminders and schedules fail with "internal error" (UNIQUE), so a lost response leaves a stuck entry. [probe]

The project makes the web UI correct against the current server and makes it agree with what Hermes and widgets see. The server stays the source of truth; the UI keeps working offline from its IndexedDB mirror.

Evidence: the two audit reports from this session (summarised in Appendix A). Baseline at `bbcfe46`: `go test ./...` passes; frontend `npm test` 75/75; `tsc -b` clean; `npm run lint` 0 errors, 1 warning.

## Goals

1. **The UI works against the current server, in dev and in production.** No write fails because of how the UI or a proxy forwards requests.
2. **No change gets stuck.** A change the server rejects is shown to the owner, who can discard or retry it. One failure never drops a sibling change. Producers stop sending invalid data.
3. **Sync is lossless in both directions:** full sync carries everything the views need (including Trash); retries never duplicate; no server change is lost on the client while a local change is pending.
4. **One "today".** The UI computes dates, groups and cutoffs in the owner's timezone, the same one the server uses.
5. **Same lists everywhere.** Local view builders return the same membership, order and counts as the server views for the same data, enforced by shared contract fixtures.
6. **The listed UI bugs are fixed:** H1–H4, M1–M6, the remaining L items in scope, and the new bugs from the re-check (Appendix A).
7. **Tests cover the high-risk frontend modules** (roadmap §5.2), and a real-browser pass covers every lifecycle in §Acceptance.

## Non-goals

- Replacing the local-first architecture: the UI keeps computing views from IndexedDB so it works offline. It does not switch to calling `/api/views/*` (designer's call; see Decisions).
- Server inconsistencies the UI does not hit (record them as roadmap follow-ups): deadline < when accepted on task update but rejected on create; sync schedule create allowing a duplicate timeless date; an `end_time`-only schedule update storing end < start; `PATCH /projects/{id}` with `area_id: null` returning 500.
- Making every change-log write transactional. Only writes this spec rewrites change.
- Logbook/Trash pagination and batching the N+1 Dexie queries in `enrichTask(s)`. Single user, not measured as slow. Add when a list is measurably slow.
- Resolving a tag name that was created on another device before the local create was pushed (Appendix A, compat #9). With Goal 2, the failing change surfaces in the queue UI; the owner discards it. Known limitation.
- OIDC changes. `email_verified` and `OIDC_ALLOWED_EMAIL` stay as Project 1 set them.
- Field-level last-write-wins for sync push (owner decision Q3: later). Recording `device_id` in the change log goes with it.
- Multi-user, API versioning, priority levels.

## Anti-goals

- Weakening the cross-origin session guard to make dev work.
- Silently discarding the owner's edits. Every discarded change is visible and owner-initiated.
- A second copy of view rules that can drift from the server without a failing test.
- Disabling, skipping or loosening tests.

## Constraints

- TDD for every behavior change: a failing test first (Go: `go test`, frontend: Vitest with `fake-indexeddb` and `msw`, both already installed).
- Every API change updates `docs/api.md`, `GET /api/openapi.yaml` and keeps the OpenAPI drift test green (`CLAUDE.md`).
- New dev dependency approved by the owner (2026-10-18): `@playwright/test` (latest 1.64.1, npm registry, checked 2026-10-18; 1.64.0 released 2026-10-07). No other new dependency.
- The CLI `ttd`, Hermes via `/mcp`, widgets and `API_KEY` clients keep working; their requests use Bearer tokens and are not affected by the session guard.
- Branch: `csscoder/review-and-test-coverage` (owner instruction).

## Design

Work goes in phases; each phase leaves the suite green. Phase 0 comes first because nothing can be verified in `npm run dev` until writes work.

### Phase 0 — Compatibility breakers

**P0.1 Session guard recognises same-origin requests behind proxies.** `crossOriginSession` (`internal/middleware/auth.go`) compares `Origin` with `Host`. Vite's proxy shorthand `'/api': 'http://localhost:2999'` sets `changeOrigin: true` (`frontend/node_modules/vite/dist/node/chunks/config.js:21918-21921`, vite 7.3.1, read 2026-10-18), so `Host` becomes `localhost:2999` while `Origin` stays `:5173`. Reverse proxies that rewrite `Host` (nginx with `proxy_set_header Host $proxy_host`) do the same in production.

- Change: if `Sec-Fetch-Site` is present, it decides: `same-origin` → same-origin; `none` → same-origin (user-initiated navigation); any other value → cross-origin. Only when it is absent, fall back to the current `Origin`/`Host` comparison.
- Why this is safe: browsers set `Sec-Fetch-Site` on every request and scripts cannot set or override it (forbidden header). Go 1.27's own `net/http.CrossOriginProtection` uses the same rule: "Cross-origin requests are currently detected with the Sec-Fetch-Site header, available in all browsers since 2023, or by comparing the hostname of the Origin header with the Host header" (`go doc net/http.CrossOriginProtection`, go1.27.0, read 2026-10-18).
- Keep our guard rather than adopting `http.CrossOriginProtection`: the stdlib handler always allows GET/HEAD/OPTIONS, while our guard rejects cross-origin session requests on every method. CORS never sends `Access-Control-Allow-Credentials` (`internal/middleware/cors.go`), so this is defence in depth, not a known hole.
- Also set `changeOrigin: false` for `/api` in `frontend/vite.config.ts` so dev mirrors a same-host deployment (designer's call).
- README deployment section: one line that the proxy must forward requests from a current browser (any proxy works because of `Sec-Fetch-Site`); recommend preserving `Host`.

**P0.2 Full sync includes trashed tasks.** `/api/sync/full` (`internal/handler/sync.go`, `Full`) calls `tasks.List(model.TaskFilters{})`, which adds `t.deleted_at IS NULL`. Return trashed tasks too (open, completed and trashed: everything the Trash and Logbook views need), not purged ones. How (a filter flag or a second query) is the builder's call. Check projects, areas and other entities for the same omission. Read all tables and the returned cursor in one read transaction, so a write between reading a table and reading the cursor is neither in the snapshot nor skipped by the next pull (or take the cursor before the reads; pull re-applies full snapshots, so overlap is harmless).

**P0.3 Idempotent creates for attachments, checklist items, reminders and schedules** in sync push, matching the task/project/area/tag behavior from `91b5079`: a create whose id already exists for the same parent is `applied` with the current seq, not "internal error"; an id that exists under a different parent is a conflict error. REST creates of the same entities get the same rule if they accept a client id.

**P0.4 Font size.** The settings slider (`frontend/src/pages/SettingsView.tsx:273-279`, range 14–18, step 1) predates the server rule; `validateSettingsPatch` (`internal/handler/validate.go:116-117`) allows only 12, 14, 16, 18, 20 (introduced in `c02b597`). Change the server rule to an integer range 12–20 (designer's call: the UI is the older contract, and 12 and 20 stay valid for existing data). Update `docs/api.md` and OpenAPI.

**P0.5 Push in batches.** `sync/push.ts` sends the whole queue in one POST; the server caps bodies at 10 MiB. Send at most 100 changes per request (designer's call) and stop on the first failed request. On 413, halve the batch and retry; a single change that still gets 413 becomes a per-entry permanent error with the client-side code `TOO_LARGE` (P1.2). Requests go one at a time in queue order and the next waits for the previous result, so per-entity FIFO (P1.2) holds even when one entity's changes span several requests.

### Phase 1 — Sync engine

**P1.1 H2: schedules survive full sync.** `fullSync` writes rows without `_serverSeq`, so the first pulled schedule `create` deletes every other local schedule of that task as if it were a temp row (`frontend/src/sync/pull.ts:126-136, 213-230`). Stamp every row written by `fullSync` with the full-sync cursor. Temp-row cleanup applies only to rows created locally and not yet confirmed.

**P1.1a Full sync keeps pending local edits.** `fullSync` clears the entity tables but keeps the queue (`sync/pull.ts:183-234`), so after a 410 the local version of every entity with a pending or stuck change disappears. After writing the snapshot, re-apply every queue entry in queue order to the local rows (create inserts, update overlays its fields, delete removes), and store the full-sync row of each such entity as its skipped server snapshot (for Discard, P1.2). Test: a stuck task update and a pending child create both survive a 410 full sync and stay visible.

**P1.2 H3: the queue is per change, and failures are classified.**
- Remove queue entries by queue id, not entity id. Results correlate to changes by position; the server must return results in request order (document it; add a test if not already guaranteed).
- Add a machine code to each per-change result: `SyncPushResult.code` ∈ `VALIDATION`, `NOT_FOUND`, `CONFLICT`, `INTERNAL`. Permanent: `VALIDATION`, `NOT_FOUND`, `CONFLICT`. Transient: `INTERNAL`. Map every existing per-change error string to a code; the `duplicate timeless date` path through `syncFirstScheduleDate` (`sync.go:605`) currently reports "internal error" and must become `VALIDATION` with the message.
- Permanent failures stop being retried automatically. The queue entry keeps the error and blocks pull for that entity as now (so local edits are not overwritten silently), until the owner acts.
- **Per-entity FIFO** (designer's call): while an entity has a stuck entry, later queued changes for the same entity are held, not pushed, so a later Retry can never overwrite a newer change. Changes for other entities keep flowing, except a child change (schedule, reminder, checklist item, attachment, task tag) whose parent's create is still queued or stuck: it is held until the parent create is applied, so a transient parent failure does not turn the child into a permanent `NOT_FOUND`. Batches (P0.5) preserve queue order.
- **Skipped server changes are kept, not lost.** `pullChanges` skips entities with pending entries but still advances the global cursor (`sync/pull.ts:91-97, 160-182`), so those change-log entries never come again. Store the latest skipped snapshot per entity in a small Dexie table (snapshots are full entity state, so the latest is enough). When the entity's queue drains by **success**, drop it: the push writes a newer change-log entry that the same cycle's pull delivers. When it drains by **Discard**, apply the stored snapshot (or delete the local row if the latest skipped entry was a delete). Every applied push must write a change-log entry so the next pull returns the server state.
- UI: the existing sync status banner opens a list of stuck changes: entity, short description, server message. Actions: **Retry** (re-queue) and **Discard** (delete the entry and restore the server state as above). Discarding a create also discards its queued children and removes the local row: before that, show every queue entry the Discard will remove (including children without an error) and require explicit confirmation. Test the confirmation and that cancelling removes nothing. Design of the list is the builder's call; keep it inside the existing settings/sync UI patterns.
- Transient failures and network errors keep the current 30 s retry. After 5 consecutive `INTERNAL` failures of the same entry, it also appears in the stuck-changes list with Retry and Discard (auto-retry continues), so an unmapped server error never leaves the owner without a way out.
- `TOO_LARGE` (P0.5) is a client-side permanent code outside `SyncPushResult.code`; the queue and the stuck-changes list treat it like the server's permanent codes.

**P1.3 Producers stop sending invalid data.**
- `QuickEntry.tsx:111-121`: block saving when deadline < when, with an inline message, as the server does on create.
- `ScheduleEditor.tsx:29-35`: when start + default gap passes 23:59, leave `end_time` empty instead of clamping to an end ≤ start.
- `hooks/queries.ts:877-879`: do not create a synthetic schedule with `when_date: ''`; when there is no date, the time change is rejected in the UI or applied only after a date is chosen (builder's call which).

**P1.4 H4: first full sync can fail.** `AppLayout.tsx:44-62` has no try/catch. On failure: if the local DB already has data, start the sync engine and work offline with the banner showing the error; if it is empty, show an error screen with **Retry**. Never leave the "Syncing" overlay up.

**P1.5 Conflicts stay client-wins** (owner decision 2026-10-18). Sync push keeps applying the client write; `conflict_resolved` stays a label. Document this in `docs/api.md`: a queued web edit made offline overwrites a newer write from Hermes or another device. Field-level last-write-wins is a follow-up (roadmap).

**P1.6 Don't drop fields after status.** `applyTaskChange` returns early on the first `status` or `deleted_at` field (roadmap §3.4). Apply every field of the change; the order and atomicity (one transaction per change) is the builder's call.

**P1.7 H1: one write path for multi-drag.** `AppDndContext.tsx:224-275` writes locally (→ sync push) and also calls REST `bulkAction`. Drop the REST call; local mutations plus the queue are the only path. The server's run-once completion already prevents duplicate repeat instances, but double writes still race and the REST promise has no `.catch`.

**P1.8 SSE.**
- `useSSE.ts`: reconnect with backoff after `onerror` (today it reconnects only on `online` or tab visibility).
- Handle `task_purged` (remove the local row).
- Ignore events whose `seq` the client already has, to avoid the extra refetch from its own pushes.

**P1.9 Logout clears local data.** `queries.ts:954-963` only clears TanStack Query. Also delete the Dexie database (all tables, including `syncQueue`), stop the sync engine and close SSE. If the queue is non-empty, warn before logging out that unsynced changes will be lost.

### Phase 2 — One timezone, one set of view rules

**P2.1 The UI learns the owner's timezone** (owner decision Q1: the server `TZ`). Add `timezone` (IANA name from `config.Location`, e.g. `Europe/Moscow`) to `GET /api/auth/config`. The UI stores it and computes every "today", date bucket, completed-today bound, Logbook/Trash group and review cutoff in that zone (`Intl.DateTimeFormat` with `timeZone`, no new dependency). Without `TZ`, `config.Location` is `time.Local`, whose name is `"Local"` (not IANA, rejected by `Intl`): resolve the zone name from the `/etc/localtime` link target, else report `UTC` (designer's call). The server must use the same zone it reports.

Replace every UTC `toISOString().slice(0, 10)` site (`AppDndContext.tsx:227, 516`; `TaskDetail.tsx:252`; `ScheduleEditor.tsx:175, 185, 310`; `SearchOverlay.tsx:62`; `localQueries.ts` completed-today, Logbook, Trash, review cutoff) with one shared helper. "Today" re-evaluates when the date changes in that zone (a tab left open overnight shows the new day without a write).

**P2.2 Exact reminders.** The datetime-local input is naive. Interpret it in the owner's timezone and send it with an explicit offset; prefill by converting the stored UTC value back to that zone. Today the prefill receives `…Z`, which `datetime-local` rejects, so the field is empty and Save does nothing.

**P2.3 Local views match server views.** Make the local builders in `frontend/src/hooks/localQueries.ts` match `internal/repository/views.go` and `view_predicates.go`:

| View | Change in the UI |
|---|---|
| Today, Upcoming, project, area, heading, tag lists | Order by the same sort-order columns as the server (fixes M1 drag snap-back) |
| Today rows and Evening split | Owner decision Q2: one row per open today schedule entry, Evening split per entry (as the server). Dragging any row of a task moves the whole task (`sort_order_today` is per task). A task in Today by membership with no open today entry shows one row without a time. |
| Displayed time on Today/Earlier rows | Today's entry time, or the first open past entry for Earlier, computed from local schedules (not the stale `first_schedule_time` snapshot) |
| Completed today, Logbook, Trash groups, review cutoff | Owner's timezone (P2.1) |
| Anytime/Someday grouping | Exclude tasks whose project is not open or whose area does not exist, as the server does |

Server-side fixes in this phase:
- `GetCounts` Anytime/Someday must exclude tasks of non-open projects, so counts equal views (today they disagree on the server itself).
- The Today LEFT JOIN on `task_schedules` (`views.go:193, 226`) has no `completed` filter, so completed entries of today produce rows. Join only open entries (owner decision Q2: completed time blocks of today are hidden); membership does not change.

**P2.4 Shared contract fixtures.** Add JSON fixtures: a seed dataset (areas, projects, headings, tags with nesting, task tags, tasks, schedules), a fixed "now" and timezone, and per view the expected rows in order and the expected counts. A row is identified by task id plus, where a view has one row per schedule entry (Today, Upcoming), the section and `schedule_entry_id`. Cover tag lists as well as the built-in views. Include cases near midnight in a non-UTC zone, multi-schedule tasks, deadline-only tasks, tasks in closed projects, overdue and Earlier. A Go test loads each fixture into a test DB and checks the server views; a Vitest test loads it into `fake-indexeddb` and checks the local builders. Fixture location and loader shape are the builder's call; one copy, read by both.

### Phase 3 — Remaining UI bugs

- **M3** `lib/date-parser.ts`: typing today's date yields today, not next year; invalid days ("feb 31") are rejected, not rolled over. Remove the midnight-pinned clock that hid it; test at several times of day.
- **M4** `lib/parse-tags.ts`: Unicode tags (`#покупки`; use the `u` flag with letter/number classes); `$project` matches on a word boundary.
- **M5** Area and tag delete, task/area/project/tag reorders (`queries.ts:514, 575-587, 593, 637-655`; `AppDndContext.tsx:592-600`): call REST first and update Dexie only on success; on failure show the error and keep the local state. These operations need a connection; offline, show that instead of diverging. Handle area delete 409 `HAS_PROJECTS`. Empty Trash releases the "purging" state in a `finally`.
- **M6** `sync/reminders.ts` and `missedRemindersEmitter.ts` have no callers. Delete them (designer's call: server push and the `reminder_fired` SSE toast deliver reminders).
- Local `syncFirstScheduleDate` (`db/mutations.ts:79-92`) moves the first entry like the server does, also for multi-entry tasks.
- `ProjectView.tsx:87` dialog: tasks go to Trash, not "permanently deleted". Local `deleteProject`/`useDeleteArea` move child tasks to Trash locally to match.
- `SearchOverlay.tsx:36`: trim the query; do not search on whitespace.
- `ScheduleEditor.tsx:116`: remove the unused `eslint-disable` (the one lint warning).

### Phase 4 — Tests

Write the RED test for each item above in its phase. In addition, bring these untested modules under test (roadmap §5.2): `sync/engine.ts` (status, 410 → full sync), `pull.ts` `fullSync`, `push.ts` partial failure on the same entity, `AppDndContext` drop semantics (Inbox drop clears project/area/when; project drop inherits area; sidebar drop restores trashed and reopens completed), `db/mutations.ts` cascades, `parse-tags` + `useResolveTags`, `filter-tasks`, `sort-position`, `useBulkAction`, `useSSE`. Use the existing Vitest + `fake-indexeddb` + `msw` setup for unit and integration tests, and the Playwright suite (Acceptance 3) for the browser lifecycles; fix the unhandled `GET /api/saved-filters?view=today` MSW warning in `TodayView.test.tsx`.

## Acceptance

1. `go test ./...`, `go vet ./...`, `golangci-lint run`, frontend `npm test`, `npm run lint` (0 warnings) and `tsc -b` pass.
2. The contract fixtures pass on both sides.
3. A real-browser pass, run twice: against `npm run dev` (Vite proxy) and against the embedded build served by the Go binary, with `AUTH_MODE=builtin` and `TZ=Europe/Moscow`. Each lifecycle ends with a reload and a check that the server (via `GET /api/views/*` with a token) shows the same lists:
   - login, first load, logout (local data cleared);
   - create in Inbox and via Quick Entry with `#tag` (Cyrillic), `$Project`, date and time text;
   - schedule: today, this evening, someday, a future date, multiple time blocks, a late-evening time block;
   - deadline, reminder (exact and relative), checklist, link attachment, file attachment;
   - move to area, project, heading; drag reorder in Today and a project (order survives reload);
   - complete, cancel, won't do, reopen; complete a repeating task (fixed and after-completion: one next instance);
   - multi-select drag to Completed and to Today;
   - delete task, restore from Trash, empty Trash; delete a project and an area (tasks land in Trash);
   - settings: font size 14–18, evening start, default reminder;
   - offline: go offline, edit, come back online: changes push; a server-rejected change appears in the stuck-changes list and can be discarded;
   - SSE: a change made via the API appears without reload; restart the server and the stream reconnects.
   The pass is a committed Playwright suite (owner decision Q4) under `frontend/e2e/` (location is the builder's call), run against a throwaway server with a fresh DB. It runs in CI against the embedded build as a new job in `.github/workflows/ci.yml`; the dev-proxy run is a local script (`npm run e2e:dev`), because CI does not run Vite's dev server. Offline is simulated with the browser context's offline mode; the API side uses a named token.
4. `docs/api.md`, OpenAPI (drift test green) and README updated; roadmap §7 Project 3 points to this spec and plan.

## Decisions made by the designer

- One spec for the UI and the server changes it needs; server changes are limited to what the UI hits.
- Keep local-first view computation; enforce parity with shared fixtures rather than calling `/api/views/*` (keeps offline work).
- Keep our session guard, add `Sec-Fetch-Site`; do not adopt `http.CrossOriginProtection` (it allows cross-origin session GETs).
- `vite.config.ts` `/api` proxy uses `changeOrigin: false`.
- Font size: server accepts integers 12–20.
- Push batches of 100 changes.
- Permanent per-change errors stop auto-retrying and wait for Retry/Discard; transient ones retry every 30 s.
- Per-entity FIFO in the sync queue, child changes held behind a pending parent create; skipped server snapshots stored per entity and applied on Discard; full sync re-applies the queue.
- Push batches shrink by half on 413.
- Timezone name falls back to the `/etc/localtime` target, else `UTC`.
- Area/tag delete and reorders are online-only REST operations.
- Delete the dead missed-reminders pipeline.
- Logbook/Trash pagination and N+1 batching deferred until measured.

## Decisions made by the owner (2026-10-18)

- **Q1** The UI uses the server `TZ` (exposed in `/api/auth/config`).
- **Q2** Today shows one row per open today schedule entry; completed time blocks of today are hidden.
- **Q3** Conflicts stay client-wins for now; field-level LWW is a follow-up.
- **Q4** The browser pass is a committed Playwright suite (new dev dependency).

## Left to the builder

Fixture format and location; the stuck-changes UI layout; how `/sync/full` fetches trashed tasks; transaction boundaries inside `applyTaskChange`; the reconnect backoff values; the exact code mapping of existing per-change error strings; task order inside each phase.

## Appendix A — Re-check findings (2026-10-18, `bbcfe46`)

Confidence: [probe] reproduced against a scratch server with the UI's payloads; [code] read; [inferred].

| Item | Status | Where | Conf |
|---|---|---|---|
| Writes 403 under Vite dev proxy / Host-rewriting proxies | new | `middleware/auth.go:149, 174-195`; `vite.config.ts:45-47` | probe |
| Full sync omits trashed tasks | pre-existing | `handler/sync.go:190`; `repository/tasks.go:327-328`; `sync/pull.ts:200` | code |
| Re-sent child creates → "internal error" | pre-existing | `sync.go:1169, 1282, 1371, 1440` | probe |
| `font_size` 15/17 rejected | new | `validate.go:116-117` | probe |
| Deadline < when from Quick Entry rejected, stuck | new | `QuickEntry.tsx:111-121`; `sync.go:650-651` | probe |
| Late-evening time block end ≤ start, stuck | new | `ScheduleEditor.tsx:29-35, 212-220`; `sync.go:1166-1167, 1229-1230` | probe |
| Synthetic schedule `when_date: ''`, stuck | rejection new | `queries.ts:877-879`; `sync.go:1157` | probe |
| Exact reminder shifted by TZ; prefill empty | new | `TaskDetail.tsx:1116-1178`; `validate.go:44-54` | probe/code |
| Push > 10 MiB → 413, push and pull stop | new | `sync/push.ts:101`; `middleware/bodylimit.go:9` | code |
| H1 double write (no duplicate instances any more) | changed | `AppDndContext.tsx:224-275` | probe |
| H2 schedules deleted after full sync | present | `sync/pull.ts:126-136, 213-230` | code |
| H3 queue by entity id, no discard, permanent errors retried forever | present, more exposed | `sync/push.ts:106-125`; `pull.ts:91-97` | code |
| H4 overlay forever on first-sync failure | present | `AppLayout.tsx:44-62` | code |
| M1 local sort ignores sort_order | present | `localQueries.ts:328-334, 428-437` | code |
| M2 UTC "today" sites | present, gap wider | see P2.1 | code |
| M3 date parser year/overflow | present | `date-parser.ts:113-130` | code |
| M4 ASCII-only tags, `$project` prefix | present | `parse-tags.ts:6, 32, 50` | code |
| M5 delete/reorder outside queue; purging lock | present | see Phase 3 | code |
| M6 missed-reminder pipeline dead | present | `sync/reminders.ts`, `missedRemindersEmitter.ts` | code |
| Today badge vs list; deadline-only double count | fixed (`bbcfe46`, `0e05ecf`) | `localQueries.ts` | code |
| Logout leaves IndexedDB/queue | present | `queries.ts:954-963` | code |
| SSE no reconnect after transient drop | new finding | `useSSE.ts` | code |
| Stale/wrong time on Today rows | new finding | `localQueries.ts:218-229` | code |
| Server counts ≠ server Anytime/Someday views (closed projects) | new finding | `views.go:486-551` | code |
| Sync push emits SSE to the sender too; `task_purged` unhandled | new | `useSSE.ts:194-196` | probe |

Verified OK [probe]: builtin login and cookies over HTTP; SSE via Vite and embedded; task create/update/complete/reopen/delete/restore via sync; repeat completion once (sync and bulk in either order); move semantics; project/area/tag creates and updates; schedule, checklist, reminder, link attachment creates; settings except font size; repeat rule PUT; bulk actions; reorders; uploads; saved filters; search; pull and full-sync shapes; error envelope; CLI unaffected.

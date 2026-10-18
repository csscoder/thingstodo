# ThingsToDo — Review Findings and AI-First Roadmap

Date: 2026-10-16. Branch `csscoder/review-and-test-coverage`.

This document records the full review of the project (backend, frontend, tests, MCP), the comparison with Things 3, and the work that follows from it. It is the entry point; each project gets its own spec and plan under `docs/superpowers/`.

Confidence markers used below:
- **[probe]**: reproduced against a throwaway server built from this tree (`AUTH_MODE=builtin`, `API_KEY=k`, `TZ=Europe/Moscow`).
- **[code]**: verified by reading code, not executed.
- **[inferred]**: follows from code or docs, not verified.

---

## 1. Product direction (owner's intent)

- **AI-first.** The owner talks to **Hermes Agent** in natural language ("do X on date Y", plus context). Hermes creates and manages tasks through the API/MCP. Hermes will also answer "what do I have today" and reschedule or complete tasks.
- **Widgets read slices.** Separate small widget apps read data over the API: tasks by area, by date, by priority, and so on. Even the built-in lists (Today, Upcoming, …) are conceptually widgets over the API.
- **The built-in web UI is secondary.**
- **Deployment:** Docker on the owner's internet-facing VPS.
- **Reference model:** Things 3 (culturedcode.com/things/support). It is used to validate ideas and semantics, not as a spec to copy.

Consequence: the API is the product. Every business rule must live on the server and be reachable by a machine client, with stable contracts, safe credentials and predictable errors.

---

## 2. Current state (facts from the repo)

- The project is far past "Phase 1 — Scaffolding" (as `CLAUDE.md` claims). Version 0.13.2, local-first sync, CLI `ttd`, MCP server in `mcp/` (TypeScript, about 650 lines, 44 tools).
- **Backend:** Go + chi, SQLite (WAL + FTS5), 28 migrations, single-tenant (one user).
- **Auth:** `AUTH_MODE` ∈ `builtin` (default) | `proxy` | `oidc` | `none`. A single static `API_KEY` (Bearer) authenticates as the first user with full access.
- **Frontend:** React 19 + Vite, Dexie (IndexedDB) mirror, sync queue. The UI does **not** call `/api/views/*`; every view is computed locally in `frontend/src/hooks/localQueries.ts` (1196 lines).
- **MCP:** stdio only, Bearer API key, thin wrapper around REST.
- **No OpenAPI, no API versioning, no CORS, no idempotency header.**

---

## 3. Review findings — backend

### 3.1 Security

| # | Sev | Finding | Where | Conf |
|---|---|---|---|---|
| S1 | Critical | Attachment path traversal. JSON `POST /tasks/{id}/attachments` with `{"type":"file","url":"../thingstodo.db"}` is accepted. `GET /attachments/{id}/file` streams the DB; `DELETE` runs `os.Remove` on the path. Same via `sync/push`. | `attachments.go:52-64,163,190`; `sync.go:1024-1053` | [probe] |
| S2 | Critical (oidc) | JWT middleware accepts any HS256 token signed with `JWT_SECRET`: no alg restriction, no `exp` required, no `sub` required. `GET /api/auth/oidc/login` hands anonymous callers such a token (`oidc_state` cookie). Replaying it as `token=` opens the API. | `middleware/auth.go:75-91`; `oidc.go:59-72,208-214` | [probe] |
| S3 | Critical (oidc) | OIDC callback links **any** IdP email to the single user (renames it). `email_verified` unchecked. README promises a 403 that does not exist. | `oidc.go:149-172` | [code] |
| S4 | High | One shared admin API key: no scopes, no read-only, no expiry, no revocation. `GET /user/settings` returns `ntfy_access_token` to any key holder. | `middleware/auth.go`, `user_settings.go` | [code] |
| S5 | Low | Link attachments store any URL, including `javascript:`. React 19 blocks it in `href`, but widgets may render raw. | `attachments.go:60-63` | [code] |
| S6 | Medium | 500 responses leak raw SQLite error text. | all handlers via `helpers.go` | [code] |
| S7 | Medium | No request body size limit (except multipart). | `helpers.go:18-21` | [code] |
| S8 | Medium | Cookies lack `Secure`; no brute-force protection on password login. | `auth.go:23-66`, `oidc.go` | [code] |
| — | High | No panic recovery; sync push panics on wrong JSON types and drops the connection mid-batch (partial apply). | `sync.go:350…`, `router.go:30` | [probe] |
| — | Low | `AUTH_MODE=proxy` trusts `Remote-User`; with the port reachable directly it is an open door. `none` has no auth. | docs | [code] |

### 3.2 Robustness for machine writers

- **No date/time/enum validation** [probe]. `when_date:"tomorrow"` is stored and shows in Upcoming. `start_time:"9:00"` sorts after `"18:00"` and lands in This Evening (string compare).
- **Non-atomic writes** [probe]. Task create inserts the row, then tags, then schedule as separate statements. An unknown `tag_ids` gives 500 with the task already created; a retry duplicates it. `PATCH` duplicate-timeless returns 400 after `when_date` was already written (task and schedule disagree).
- **FTS injection/500s** [probe]. Search goes to `MATCH` unescaped: `c++`, `email:`, `milk-2` all return 500 with raw SQLite text. `/api/tasks?search=` is fully raw.
- **Unknown FK ids → 500** on checklist, repeat, reminders, schedules for a nonexistent task.
- **Bulk API** [probe]: `set_priority` reads `params.priority`; the MCP tool hint `{high_priority:true}` silently **clears** priority. `affected` counts nonexistent ids. `set_when ""` creates a phantom schedule with `when_date=''`. `move_project` nulls `area_id` when absent and leaves `heading_id` stale.
- **PATCH `/move`** nulls every absent FK; sending only `project_id` wipes `area_id`.
- **Error format inconsistent:** handlers `{error, code}` JSON; middleware `text/plain`; unknown `/api/*` → chi plain-text 404.
- **Hard deletes:** `DELETE /projects/{id}` and `DELETE /areas/{id}` hard-delete child tasks (no trash, no undo). Undocumented.
- **User settings:** the first `PATCH` for a user with no row is silently lost (`user_settings.go:162-172`).
- **Schedules reorder** ignores the `{id}` task scope; it can reorder another task's schedules.

### 3.3 Domain logic

- **Recurrence `after_completion` is not implemented** [code]: the mode is never read; the next instance always uses the original `when_date`. `HandleAfterCompletion` is dead code. Things treats this as a separate, core mode.
- **Exact reminders in the documented format never fire** [code]: the parser accepts only zone-less layouts, so `…Z`/offset values are skipped every minute. Create does not validate `exact_at`. "morning" is hardcoded to 09:00 and settings are read and discarded.
- **Repeat duplicates** [code]: after completion the rule moves to the new task; reopening the old one yields two open recurring tasks. Completion has no `WHERE status='open'` guard and `createNextInstance` has no idempotency check, so any double completion (UI double path, agent retry) spawns two next instances.
- **UTC vs local "today"** [code]: SQLite `datetime('now')` is UTC; Go "today" is the container's local date. Near midnight in non-UTC zones, items completed today drop out of Today and Logbook groups shift by a day.
- **View inconsistencies** [probe]: a deadline-only standalone task appears in Inbox **and** Anytime; `counts.today` disagrees with the Today view; Anytime count includes tasks in non-open projects; Upcoming includes today and uses `from` instead of today for overdue.
- **Tasks that vanish from every view** [code]: an open task with `when_date < today` and no uncompleted past schedule (for example created via API without a schedule) is in no list.
- **Data model notes:** priority is boolean only; `tasks.when_date` is a denormalized copy of the first `task_schedules` row (up to 12 schedules per task); "Evening" is a string compare of `start_time` against `evening_starts_at`; `task.area_id` is not kept consistent with its project's area; projects require an area.

### 3.4 Change feed, sync, SSE (what widgets would rely on)

- `SyncPrimary` rewrites `tasks.when_date` with **no change-log entry**, so a widget polling `/sync/pull` misses date changes made via schedules.
- Change-log writes are best-effort, outside the transaction. `user_id`/`device_id` are never recorded.
- 410 `CURSOR_EXPIRED` is off by one (`sync.go:112`) [probe].
- `conflict_resolved` is only a label: the client write is still applied (client-wins), so a stale web edit can overwrite a newer agent edit.
- `applyTaskChange` returns early on the first `status`/`deleted_at` field; other fields in the same change are dropped.
- Sync push emits no SSE. SSE events often omit the task body; the checklist-update event sends the item id as the task id; no `Last-Event-ID` replay; events carry no sequence number.
- `/api/tasks`, `/search`, `/sync/full` are unbounded (no pagination). Only Logbook and Trash paginate.

### 3.5 MCP gaps

- `update_task` cannot send `null`, so an agent **cannot clear** `when_date`, `deadline` or `project`.
- `search_tasks` requires an FTS query: no filter-only listing ("all tasks in area X"), and raw FTS input can 500.
- The `bulk_action` `set_priority` hint is wrong (see 3.2).
- `list_areas` claims nested projects; the API returns flat areas.
- No tools for reminders, repeat rules or settings.
- stdio only: a remote agent needs a local process.

### 3.6 docs/api.md vs code (code is truth)

- `/api/events` is auth-required (docs: no auth). Undocumented events `connected`, `saved_filter_changed`.
- `GET /api/push/vapid-key` returns `public_key`, docs say `key`.
- `POST /tasks` returns full TaskDetail, not the list-item shape. Client-supplied `id` undocumented. Default reminder auto-added from settings, undocumented.
- `project_name`/`area_name` are always `null`.
- Bulk `toggle_priority`/`toggle_tags` undocumented; `set_when.when_time` documented but ignored.
- Saved filters: docs omit the `/api` prefix.
- Sync `fields` is a JSON-array string (docs: comma-separated); push supports more entity types than documented.

---

## 4. Review findings — frontend

| # | Sev | Finding | Where |
|---|---|---|---|
| H1 | High | Multi-drag to Completed runs both a local mutation (→ sync push) and REST `bulkAction`. Each repeating task spawns **two** next instances. Same double write for other drop targets; `.then()` without `.catch`. | `AppDndContext.tsx:227-273` |
| H2 | High | After a full sync, rows have no `_serverSeq`; when a new schedule arrives, pull deletes all existing local schedules of that task as if they were temp rows. Dates disappear from Today/Upcoming until the next full sync. | `sync/pull.ts:126-136,213-230` |
| H3 | High | Push queue removes entries by entity id: one success also drops a failed sibling. A permanently failing entry is retried forever, keeps status `error`, and pull skips that entity. No UI to discard. | `sync/push.ts:109-121`, `pull.ts:91-96` |
| H4 | High | First full sync failure (offline or 5xx) leaves the full-screen "Syncing" overlay forever; the sync engine never starts. | `AppLayout.tsx:45-63` |
| M1 | Medium | Drag-reorder snaps back in Today/Project/Heading/Area/Tag lists: local views sort by priority, time, title and ignore `sort_order`. Server Today sorts by `sort_order_today`, so UI and API disagree. | `localQueries.ts:318-324,414-423` |
| M2 | Medium | "Today" computed in UTC at several sites. In UTC+3 at 00:30, dropping on Today sets yesterday. Logbook/Trash group by UTC date. | `AppDndContext.tsx:227,516`, `TaskDetail.tsx:252`, `ScheduleEditor.tsx:175,185,310`, `SearchOverlay.tsx:62`, `localQueries.ts:468,762,788` |
| M3 | Medium | Date parser: typing today's date ("oct 16") yields next year; "feb 31" becomes Mar 3. The test pins time to midnight and hides it. | `lib/date-parser.ts:113-128` |
| M4 | Medium | `#tag` parsing is ASCII-only: `#покупки` is not recognized. `$project` is a prefix match without word boundary. | `lib/parse-tags.ts:6,32,50` |
| M5 | Medium | Area/tag delete removes the local row before REST; offline → divergence, nothing queued. Reorders bypass the queue. Empty Trash can stick in "purging". | `queries.ts:575-649`, `TrashView.tsx:23-28` |
| M6 | Medium | Missed-reminder pipeline is dead code; `MissedRemindersToast` never fires. | `sync/reminders.ts`, `missedRemindersEmitter.ts` |
| L | Low | Today badge ≠ Today list; deadline-only tasks counted twice; logout leaves IndexedDB and queue on device; per-task N+1 Dexie queries; no pagination in Logbook/Trash. | `localQueries.ts` |

### 4.1 Business logic that lives only in the browser

These rules are invisible to Hermes and widgets today:

- Natural-language date parsing (`lib/date-parser.ts`). The Go CLI has a separate parser with different semantics ("next week" = +7 days vs next Monday).
- Time parsing (`lib/time-parser.ts`).
- Title tokens `#tag` and `$Project` with tag auto-create (`lib/parse-tags.ts`, `useResolveTags.ts`).
- **Saved-filter evaluation** (`lib/filter-tasks.ts`): filters are stored on the server but only run in the browser. An agent cannot "run" a saved filter.
- Fractional sort positions (`lib/sort-position.ts`).
- Drag-and-drop semantics (Inbox drop clears project/area/when; project drop inherits area; sidebar drop restores trashed and reopens completed).
- Schedule rules (next free date, no duplicate untimed dates, synthetic placeholder schedule).
- Upcoming date buckets; recurrence and reminder labels.

Re-implemented with differences (UI vs server): Today order, one row per task vs one row per schedule entry, evening split, "today" clock (browser vs `time.Now()` in the container), review cutoff.

Already on the server: recurrence generation, reminder firing and push, schedule cleanup on completion.

---

## 5. Tests

### 5.1 State at review time (commit `32dbd08`)

- `go test ./...` and `go vet ./...` **failed from a clean clone**: `internal/frontend/embed.go: pattern dist: no matching files` (CI stubs `dist`).
- With a stub `dist`: all pass, vet clean, `-race` clean. Coverage 35.2% per package, 40.2% cross-package.
- Frontend: 12 test files, 71 tests. Page tests mock `localQueries` entirely, so view logic is untested.

### 5.2 Highest-risk untested areas

Backend (cross-package line coverage):
- `scheduler/scheduler.go` 2.2% — reminder firing, recurrence instance creation, fixed-rule cron.
- `push/*` ~3% — sender, ntfy, dispatcher.
- `handler/oidc.go` 0%; `attachments.go` 0.9%; `schedules.go` 0.9%; `repeat_rules.go` 1.5%; `saved_filters.go` 1.8%; `reminders.go` 2.9%; `user_settings.go` 3.7%; `views.go` 13.8%; `sync.go` 25.6%.
- `middleware/auth.go` 26.1% (proxy path only).
- `repository`: saved_filters 2.7%, reminders 9.7%, user_settings 11.8%, repeat_rules 14.0%, schedules 21.7%, views 52.7%.

Frontend (by risk):
1. `localQueries` view builders (only counts tested).
2. Sync engine, `fullSync`, partial-failure push.
3. `AppDndContext` (none).
4. `db/mutations.ts` cascades and derived flags.
5. `parse-tags` + `useResolveTags` (none).
6. `filter-tasks` (none).
7. `date-parser` (misses the today case).
8. `sort-position` (none).
9. `useBulkAction`, empty trash, REST-first mutations (none).
10. `useSSE`, reminders (none).

### 5.3 Testing policy for all projects

- TDD: a failing test that reproduces the behavior first (RED), then the fix (GREEN).
- Coverage grows in the project that changes the area: server rules in project 2, API surface in project 3, frontend in project 4.
- Never skip, delete or weaken a test to get green.

---

## 6. Comparison with Things 3

Source: about 35 culturedcode.com support articles, Mac/iOS release notes and the Things 3.23 blog post, checked 2026-10-16 (Things for Mac 3.24.1, URL scheme v2).

### 6.1 Where this app diverges from Things

| Topic | Things 3 | ThingsToDo today | Decision needed |
|---|---|---|---|
| When model | One When state per to-do: Inbox, On Date (Today if ≤ today, Upcoming if future), Anytime, Someday. This Evening is a flag valid only when the date is today. | Up to 12 schedules per task; `when_date` mirrors the first; Evening derived from `start_time >= evening_starts_at` by string compare. | Keep multi-schedule (an extension), but define one canonical derivation of "effective list" on the server. |
| Today | Start date, deadline or repeat rule matches today; overdue starts and deadlines stay in Today. Order is manual. | Server orders by `sort_order_today`; UI by priority/time/title. Tasks with past `when_date` and no open past schedule vanish. | Server rule becomes canonical; no task may vanish. |
| Upcoming | Starts **tomorrow**; next 7 days individually, then larger ranges. | Includes today; overdue relative to `from`. | Align to tomorrow-onward or document the difference. |
| Anytime | Active items incl. those with deadlines; Today items shown with a star; loose items first, then grouped by parent. | Deadline-only standalone task also appears in Inbox. | One list per task (except the Today/Anytime overlap). |
| Inbox | Unprocessed items with no parent and no date. | Same rule, but deadline-only tasks also count. | Clarify. |
| Repeating | Template + copies. **After completion** offsets from the completion date; **fixed** creates copies regardless. Since 3.23: complete early, Make Exception / Update Rule, Create Next Copy, Pause/Resume/Stop. | `after_completion` behaves as fixed; reopen duplicates; double completion spawns two copies. | Implement after-completion; make completion idempotent. |
| Reminders | A time on the start date; deadlines cannot have reminders. | Six reminder types incl. relative and `exact`; `exact` with timezone never fires. | Keep richer model; fix exact parsing and timezone. |
| Status | Open, Completed, Canceled. Completed ≠ Logged (logging interval). | Adds `wont_do`. | Keep `wont_do` as an extension; document mapping. |
| Projects / areas | A to-do's parent: project, area or none. A project's area is optional. Headings only in projects. | Projects require an area; `task.area_id` can drift from its project's area. | Decide whether a project may have no area; keep area derived from project. |
| Tags | Nested; tags on projects/areas are **inherited** by children (filtering and search include inherited tags); multi-tag filter is AND. | Nested (no cycle check); no inheritance; `/tags/{id}/tasks` ignores child tags; `tag_ids` filter is OR. | Decide inheritance and AND/OR semantics for widget queries. |
| Delete | To-do/project to Trash (Mac); area: children to Trash. | Deleting a project or area **hard-deletes** child tasks. | Soft-delete children to Trash. |
| Priority | None in Things. | Boolean `high_priority`. | Owner wants priority slices; decide boolean vs levels. |

### 6.2 What Things' automation lacks — and our API must provide

Things' URL scheme, Shortcuts and AppleScript are the prior art for an external program driving a task manager. Their gaps are exactly what an AI-first API must not have:

1. **No read API** in the URL scheme (`show`/`search` only drive the UI). → We need rich, filterable, paginated reads.
2. **No delete** via URL. → Delete and restore with Trash semantics.
3. **Areas and tags cannot be created** via URL/JSON; unknown tags are silently dropped. → Explicit errors, or opt-in auto-create.
4. **Repeat rules are not automatable.** → Full repeat-rule CRUD in REST and MCP.
5. **Partial ID return** (JSON returns only top-level IDs). → Return every created id.
6. **No idempotency key.** → Client-supplied id plus an idempotency header for retries.
7. **Name-based targeting is first-match** (`list`, `area`, `heading`). → Name→id resolution with explicit ambiguity errors.
8. **Inconsistent semantics across endpoints** (array vs comma string, three project-completion rules). → One consistent schema, published as OpenAPI.
9. **English-only natural-language dates** in the URL scheme. → Server-side date parsing that handles English and Russian, with timezone input.
10. **Rate limits** (250 items / 10 s). → Bulk endpoints with explicit limits.

---

## 7. Roadmap

Three projects, in order. Each goes through brainstorming → spec → plan → implementation with review. Status: Projects 1, 2 and 3 are done; the original Projects 2 and 3 were merged and executed as Project 2 (the 2026-10-17 agent-widget-api plan); the frontend project was renumbered to 3.

### Project 1 — Security hardening and runnable tests (DONE)

- **Spec:** `docs/superpowers/specs/2026-10-16-security-hardening-design.md` (approved 2026-10-16; Droid review `REVIEW_APPROVED`, 3/3 rounds).
- **Plan:** `docs/superpowers/plans/2026-10-16-security-hardening.md` (9 tasks).
- **Scope:** S1–S8 + the panic/no-recover hole, README warning for `proxy`/`none`, clean-clone `go test`, frontend tests in CI, auth middleware tests for all modes.

| Task | Content | Status (from `git log`) |
|---|---|---|
| 1 | Clean-clone `go test`; frontend tests in CI | done — `6107faf` |
| 2 | Generic 5xx bodies, sanitized sync errors | done — `8a33208` |
| 3 | Recover middleware, checked type assertions in sync | done — `d9fdb7c` |
| 4 | 10 MiB request body limit (413) | done — `843ed3c` |
| 5 | JWT: `HS256` only, `exp` and `sub` required; auth middleware tests | done — `6a01bdc`, `99f4a64` |
| 6 | OIDC: `OIDC_ALLOWED_EMAIL`, `email_verified`, userinfo `sub` match, state token type; fake-IdP tests | done — `a889ac5` |
| 7 | `Secure` cookies; per-IP login limiter (10 failures / 15 min → 429) | done — `db26a5b` |
| 8 | Attachments: links only over JSON/sync, `os.Root` file access, script-scheme URL denylist | done — `e335c4c`, `9458dbb` |
| 9 | README/`.env.example`/CHANGELOG; replay audit attacks on a local server | done — `6187e86` |

Verified on 2026-10-16 at `843ed3c`: `go test ./...` passes from this tree without a built frontend; `go vet ./...` clean. Frontend `npm test`: 71/71 at plan time.

Key decisions (designer's calls, accepted by the owner with the spec):
- `OIDC_ALLOWED_EMAIL` required in oidc mode (breaking config change on purpose).
- OIDC userinfo fallback kept, guarded by a `sub` match (Authelia ≥ 4.39 puts email only in userinfo).
- Login limiter per client IP; `X-Forwarded-For` trusted only from loopback/private peers.
- Link URLs: scheme denylist (`javascript`, `data`, `vbscript`, `file`), so `obsidian://`, `siyuan://` keep working.
- Links the server now rejects stay stuck in the web UI's sync queue until project 4 fixes H3.

### Project 2 — The server is the single source of business rules, exposed for AI and widgets (DONE)

Merges the original Projects 2 and 3, which were executed together as one plan: [docs/superpowers/plans/2026-10-17-agent-widget-api.md](superpowers/plans/2026-10-17-agent-widget-api.md) (spec: [docs/superpowers/specs/2026-10-17-agent-widget-api-design.md](superpowers/specs/2026-10-17-agent-widget-api-design.md), 21 tasks, branch `csscoder/review-and-test-coverage`). It delivered named revocable tokens, CORS, boundary validation, atomic writes, escaped FTS, the widget query API (`GET /api/tasks` filters, sort, cursor pagination), idempotent creates, the single JSON error envelope, the OpenAPI contract at `GET /api/openapi.yaml` with a drift test, and the MCP server served from the Go binary at `/mcp`.

Goal: Hermes, widgets and the web UI see the same lists, dates and filters — and an external program can do everything safely and predictably. The original scopes of both merged projects follow for the record.

#### Original Project 2 scope

To do:
1. **Canonical view rules on the server** for Inbox, Today (+ This Evening, Overdue, Earlier), Upcoming, Anytime, Someday, Logbook, Trash, counts. Resolve the inconsistencies in 3.3 and 6.1 (no vanishing tasks; one list per task; counts equal views).
2. **Timezone as input.** Every date-relative request takes the owner's timezone (setting plus per-request override). Fix UTC vs local skew in completed-today, Logbook grouping, Review cutoff.
3. **Server-side natural-language dates and times** (one parser shared by REST, MCP, CLI; English and Russian). Fix the year-rollover and overflow bugs while porting.
4. **Server-side title tokens** (`#tag` with Unicode, `$Project` with word boundary) as an optional parse mode on create.
5. **Saved filters evaluated on the server** (`GET /api/tasks?filter_id=` or `/api/filters/{id}/tasks`), using the semantics from `lib/filter-tasks.ts`.
6. **Recurrence:** implement `after_completion`; make completion idempotent (`WHERE status='open'`, spawn only when a row changed); fix reopen duplicates; consider Things 3.23 controls (complete early, pause/resume, create next copy).
7. **Reminders:** parse `exact_at` with offsets; honor settings instead of hardcoded 09:00; validate values.
8. **Change feed reliable:** log every write including `SyncPrimary`; log inside the transaction; record `device_id`; fix 410 off-by-one; real LWW in `conflict_resolved`; do not drop fields after `status`; emit SSE for sync push with full bodies and a sequence number.
9. **Data consistency:** derive `task.area_id` from the project; soft-delete children on project/area delete; schedules reorder scoped to its task; user-settings first PATCH not lost.
10. **Tests:** contract fixtures per view (the same fixtures later drive the web UI); scheduler and reminder firing; recurrence modes; views edge cases near midnight in non-UTC zones.

Decisions for the owner (in project 2 brainstorming): tag inheritance and AND/OR filter semantics; whether Upcoming starts tomorrow; whether a project may have no area; priority boolean vs levels.

#### Original Project 3 scope

Goal: an external program (Hermes via MCP, widget apps, scripts) can do everything safely and predictably.

To do:
1. **Credentials:** multiple named API tokens with scopes (at least read-only vs read-write), expiry and revocation; hide `ntfy_access_token` from tokens without a settings scope.
2. **CORS** with an allowlist of widget origins; SSE auth that works for browser widgets (EventSource cannot send `Authorization`).
3. **Input validation** at the boundary: ISO dates/times, enums, ranges, FK existence → 400/404/422 with field-level details, never 500.
4. **Atomic writes:** task create/update in one transaction; no half-created tasks.
5. **Escape FTS input**; search never 500s.
6. **Query API for widget slices:** filter-only listing by area, project, heading, tag(s), date range, deadline range, priority, status, updated-since; sort selection; cursor pagination everywhere (incl. `/api/tasks`, `/search`, `/sync/full`); include `project_name`/`area_name`.
7. **Idempotency:** client-supplied ids on every create and an `Idempotency-Key` header; bulk create; return all created ids.
8. **Name→id resolution** with explicit ambiguity errors (today only in the CLI).
9. **Consistent error envelope** everywhere, including middleware and unknown routes.
10. **OpenAPI** spec generated or maintained alongside `docs/api.md`; API versioning (`/api/v1` or a version header).
11. **New MCP:** clear fields with `null`; filter-only listing; correct bulk params; reminders, repeat rules and settings tools; tool descriptions written for an LLM; consider a remote HTTP transport so Hermes on another host needs no local process.
12. **Docs:** reconcile `docs/api.md` with code (section 3.6).
13. **Tests:** handler tests per endpoint for validation and error shapes; MCP tool tests against a test server.

### Project 3 — Built-in UI bugs and frontend tests (DONE)

- **Spec:** `docs/superpowers/specs/2026-10-18-web-ui-sync-correctness-design.md` (approved 2026-10-18). **Plan:** [docs/superpowers/plans/2026-10-18-web-ui-sync-correctness.md](superpowers/plans/2026-10-18-web-ui-sync-correctness.md) (30 tasks, all executed on branch `csscoder/review-and-test-coverage`, implementation HEAD `9d8a396`). A re-check on 2026-10-18 found the UI also breaks on server changes from Project 2 (403 under the Vite proxy, stuck sync entries, Trash lost on full sync); the spec covers those server fixes and the sync-push items Project 2 deferred here.

Goal: the secondary UI is correct and stops diverging from the server.

Delivered (scope as planned):
1. Fix H1 (single write path for multi-drag; relies on idempotent completion from project 2).
2. Fix H2 (`_serverSeq` in `fullSync` or an explicit local-only marker).
3. Fix H3 (remove queue entries by queue id; discard/retry UI for stuck entries; this also releases links rejected by project 1).
4. Fix H4 (error handling and retry for the first full sync).
5. Fix M1–M6 and the L items.
6. Make the UI read the server's canonical rules (call `/api/views/*` or share the project 2 contract fixtures in tests).
7. Logout clears IndexedDB and the queue.
8. Tests for the modules ranked in 5.2 (frontend list).

Verified on 2026-10-18 at `9d8a396`: `go test ./...` — 429 tests pass, 0 failures, 11 packages; `go vet ./...` clean; `golangci-lint` (v2.15.0, default linters, no repo config) — 0 issues; frontend `npm test` — 218/218 across 32 files; `npm run lint` clean; `npx tsc -b` clean; `make build` succeeds; Playwright e2e — 13/13 in both modes (`npm run e2e` against the built binary, 1.1m; `npm run e2e:dev` against the Vite dev server, 1.9m).

Follow-ups deferred to Project 4. Sync and server leftovers are already captured in the Project 4 backlog below (field-level LWW + `device_id`, the four server inconsistencies from this spec's Non-goals, Logbook/Trash pagination and the `enrichTask(s)` N+1, cross-device tag creation). Additional UI findings from the implementation ledger:
- Discarding a stuck change can leave local rows in phantom containers when no skipped snapshot exists to restore from.
- Quick entry with a when-date can transiently show duplicate Today rows until sync settles.
- Failed offline reorders give no feedback (silent drag failure).
- `useLocalAreaDetail`'s `completed_tasks` returns all completed tasks of the area; the server's area detail returns completed-today only.
- Review-cutoff mixing: Today's review cutoff compares a UTC `updated_at` slice against the owner-zone date cutoff (hours-bounded parity gap).
- Silent drag-reorder feedback: offline/failed sidebar and task reorders keep the previous order but give no user-visible error (no toast surface exists yet).

### Backlog — candidates for Project 4 (NOT SCHEDULED)

Items the specs above deferred or excluded. Project 4 is chosen after Project 3 ships, from what actually hurts when the owner uses Hermes and widgets; this list is the pool, not the scope.

Sync and server leftovers:
- Field-level last-write-wins for `/sync/push` instead of client-wins, plus `device_id` in the change log (Project 3, owner decision Q3).
- Transactional change-log writes everywhere, not only in rewritten paths (Projects 2 and 3 non-goals).
- Server inconsistencies the UI does not hit (Project 3 non-goals): deadline < when accepted on task update but rejected on create; sync schedule create allows a duplicate timeless date; an `end_time`-only schedule update stores end < start; `PATCH /projects/{id}` with `area_id: null` returns 500.
- Logbook/Trash pagination and batching the N+1 Dexie queries in `enrichTask(s)`; add when measurably slow (Project 3).
- A tag created on another device before the local create was pushed is not resolved by name (Project 3, known limitation).

Things 3 parity (section 6.1):
- Tag inheritance from projects/areas; AND vs OR semantics for multi-tag filters.
- A project without an area.
- Priority levels instead of boolean `high_priority`.
- Things 3.23 repeat controls: complete early, Pause/Resume/Stop, Create Next Copy, Make Exception / Update Rule.

Access (owner declined in Project 2; revisit only on need):
- Token scopes (read-only vs read-write).
- CORS origin allowlist.
- API versioning (`/api/v1` or a version header).

Server-side logic Hermes does itself today (Project 2 non-goals):
- Natural-language dates and times (English and Russian), one parser for REST, MCP and CLI.
- Title tokens `#tag` (Unicode) and `$Project` (word boundary) on create.
- Saved-filter evaluation on the server.
- Name→id resolution with explicit ambiguity errors.

Outside the review entirely:
- The widget apps themselves.
- VPS operations: backups, monitoring, upgrade procedure.

---

## 8. Housekeeping

- ~~`CLAUDE.md` says "Current Phase: Phase 1 - Scaffolding" and references a missing plan file.~~ Resolved: CLAUDE.md now points to this file for the current phase.
- Seven older plans/specs from 2026-03 under `docs/superpowers/` are deleted in the working tree but not committed. They were not deleted by this review. The owner decides whether to commit or restore them.

## 9. Source reports

Full audit reports with file:line references, copied from the review session:
- `docs/review/backend-audit.md` — routes, data model, view SQL, auth, MCP, API qualities, bugs, coverage.
- `docs/review/frontend-audit.md` — structure, backend usage, bugs, browser-only logic, coverage ranking.
- `docs/review/things-reference.md` — Things 3 data model, list semantics and automation APIs, with source URLs.

Line numbers refer to commit `32dbd08`.

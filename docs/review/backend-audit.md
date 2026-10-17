# ThingsToDo Go backend audit (AI-first readiness)

Repo: `/Volumes/ExtendSSD/orca/workspaces/36_MyTODO/marakihau`, branch `csscoder/review-and-test-coverage`, commit `32dbd08`. Date 2026-10-16. Read-only; `git status --short` clean. Probes ran against a throwaway server built from this tree, scratch DB in scratchpad (`AUTH_MODE=builtin`, `API_KEY=k`, `TZ=Europe/Moscow`).

Labels: **[probe]** reproduced on the running server; **[code]** verified by reading code only.

## 0. Top findings

| # | Sev | Finding | Where | Conf |
|---|---|---|---|---|
| 1 | Critical (oidc) | JWT middleware accepts any HS256 token signed with `JWT_SECRET`, even one without `sub`. `GET /api/auth/oidc/login` hands anonymous callers exactly such a token (the `oidc_state` cookie = `{state,exp}` signed with `JWT_SECRET`); replaying it as `Cookie: token=<oidc_state>` opens all data endpoints. | middleware/auth.go:75-91; oidc.go:59-72,208-214 | [probe] sub-less JWT accepted (GET /api/tasks 200); [code] issuance |
| 2 | Critical (oidc) | OIDC callback links any IdP email to the single existing user (overwrites username). Docs promise 403; no 403 path exists. `email_verified` unchecked. | oidc.go:149-172 | [code] |
| 3 | High | Arbitrary file read/delete via attachments. JSON POST attachment with `type:"file"` + `url:"../thingstodo.db"` accepted; download streams any readable file, delete `os.Remove`s it. Same via sync push. | attachments.go:52-64,163,190; sync.go:1024-1053 | [probe] downloaded DB; deleted sacrificial file |
| 4 | High | Non-atomic writes: task create inserts row, then tags, then schedule as separate statements. Bad `tag_ids` → 500 but task already created; retries duplicate. PATCH dup-timeless returns 400 after `when_date` already written. | tasks.go:192-213,265-283 | [probe] |
| 5 | High | No date/time format validation. `when_date:"tomorrow"` stored, shown in Upcoming. `start_time:"9:00"` sorts ≥ `"18:00"` → filed under Evening. | tasks.go:88-125; schedules.go:42-102; views.go:137,175 | [probe] |
| 6 | High | Sync push panics on wrong JSON types (unchecked `v.(string)`); no recover middleware. Numeric `when_date` drops the connection; earlier batch changes already applied, client gets nothing. | sync.go:350,359,…,442,601; router.go:30 | [probe] curl exit 52, panic in log |
| 7 | High | FTS injection/500s. Search passed to MATCH unescaped. `c++`, `email:`, `milk-2` all 500 with raw SQLite text. `/api/tasks?search=` fully raw. | search.go:21-31,60; tasks.go:89-92 | [probe] |
| 8 | Med | `exact` reminders in documented format never fire: parser accepts only zone-less layouts, so `...Z`/offset is skipped every minute. Create doesn't validate `exact_at`. | scheduler.go:244-258; reminders.go:46-49 | [code] |
| 9 | Med | `after_completion` recurrence not implemented. recurrence pkg never reads `Mode`; `createNextInstance` always uses original `when_date`. `HandleAfterCompletion` is dead. | scheduler.go:85-101,140 | [code] |
| 10 | Med | Change feed unreliable for widgets: `SyncPrimary` rewrites `tasks.when_date` with no change-log entry; log writes best-effort, outside tx; `user_id`/`device_id` never recorded; 410 check off by one; sync push emits no SSE. | schedules.go:185-204; changelog_helper.go:7-22; sync.go:112,277-298 | [probe] 410 off-by-one; counts zero |

## 1. Route inventory vs docs/api.md

Routes registered in internal/router/router.go:85-265. All except /health, /api/auth/login|logout|config and /api/auth/oidc/* sit behind mw.Auth (router.go:103-113). Only global middleware is mw.Logger (router.go:30): no Recoverer, CORS, rate-limit, request-ID, or body-size limit. Every route in docs/api.md exists in code; discrepancies below (code is truth).

Auth routes:
- POST /api/auth/login (auth.go:23) — no rate limit/lockout; cookie lacks Secure flag (auth.go:59-66).
- DELETE /api/auth/logout, GET /api/auth/config — match.
- GET /api/auth/oidc/login, /callback (oidc mode only) — docs' "403 on email mismatch" absent; links instead.
- GET /api/auth/me — proxy/none echo raw identity.

Events:
- GET /api/events — docs say "No auth required" but it is INSIDE the protected group (router.go:116). Undocumented events: connected, saved_filter_changed. reminder_fired sends reminder_value, not description (scheduler.go:373-378). task_updated often lacks the task object (reminders/checklist/attachments/repeat). Checklist Update broadcasts the checklist-item id as "id" (checklist.go:67). bulk_change "type" is the action name.

Tasks:
- GET /api/tasks — no pagination/limit/sort params. project_name/area_name ALWAYS null (tasks.go:107-131 never sets them). Returns all statuses unless status= given. has_deadline accepts only "true". search is raw FTS. Undocumented fields: has_reminders, first_reminder_*, schedule_entry_id, first_schedule_completed, past_schedule_count, has_actionable_schedules, all_today_schedules_completed, first_schedule_end_time.
- POST /api/tasks — undocumented client-supplied `id`. Response is TaskDetail (nested checklist/attachments/schedules/reminders), NOT list-item shape as docs claim. Unknown tag/project/area/heading → 500 with partial write. No heading∈project check; area_id not derived from project. Auto-adds a default reminder from settings (undocumented).
- GET /api/tasks/{id} — also returns reminders (undocumented); no project_name.
- PATCH /api/tasks/{id} — `status` silently ignored. Empty title allowed. Magic key `updated_at` bumps timestamp. Documented 400 "deadline cannot be before the when date" is undocumented. Unknown keys ignored but logged as changed fields.
- DELETE /{id} — 204 for nonexistent; still logs delete. 
- DELETE /{id}/purge — does not require trash; 204 for nonexistent; leaves attachment files on disk.
- PATCH /{id}/complete|cancel|wontdo — work on trashed tasks; re-complete overwrites completed_at; schedule cleanup runs before existence check, outside tx (tasks.go:206). wontdo sets no timestamp.
- PATCH /{id}/reopen — recurring rule already moved to new instance → two open copies.
- PATCH /{id}/move — absent project_id/area_id/heading_id set to NULL (tasks.go:303-310); sending only project_id wipes area_id; when_date here does NOT sync task_schedules.
- PATCH /tasks/reorder — unknown ids silently ignored.
- POST /tasks/bulk — undocumented actions toggle_priority, toggle_tags. move_project reads area_id (absent→NULL), leaves heading_id stale. set_when.when_time documented but ignored. set_priority reads params.priority (number); MCP tool hint {high_priority:true} silently CLEARS priority [probe]. affected counts nonexistent ids [probe affected:2 for 1 real]. set_when "" creates phantom schedule when_date='' [probe]. Handler runs HandleTaskDone for trashed ids too (tasks.go:386-390).

Checklist/attachments:
- POST checklist, repeat, reminders, schedules on unknown task → 500 (FK).
- PATCH /checklist/{id} SSE sends item id as task id. DELETE checklist/attachment: no SSE.
- POST attachment JSON accepts type:"file" + arbitrary url (traversal); link URL scheme unvalidated (javascript: stored); orphan file remains if DB insert fails (attachments.go:98-123).
- GET /attachments/{id}/file — path traversal; Content-Disposition interpolates raw filename; no X-Content-Type-Options.

Repeat/schedules/reminders:
- PUT /tasks/{id}/repeat — unknown task 500; if follow-up when_date update fails returns 500 after rule saved; every<1 coerced to 1.
- POST schedules — no format/end>start/existence checks; 12-entry limit is TOCTOU.
- PATCH /tasks/{id}/schedules/reorder — {id} does not scope items; can reorder other tasks' schedules.
- PATCH /schedules/{id} — accepts completed (undocumented); no someday/start-requires-end checks.
- POST reminders — value 0-99 not enforced; exact_at not validated.
- No PATCH reminder route (matches docs).

Push:
- GET /api/push/vapid-key — response key is `public_key`; docs say `key` (push_subscriptions.go:73-75).
- POST /api/push/test — error codes NOT_CONFIGURED/SEND_FAILED undocumented.

Projects/areas/tags:
- PATCH /projects/{id} — invalid status or area_id:null → DB constraint 500; area change does not update tasks.area_id; empty title allowed.
- DELETE /projects/{id} — HARD-deletes all project tasks (no trash), undocumented; 204 for nonexistent.
- PATCH /projects/{id}/complete — leaves open tasks open.
- DELETE /headings/{id} — tasks heading_id nulled by FK, no change-log entry.
- GET /api/areas — project_count/task_count/standalone_task_count are omitempty → zeros omitted (models.go:29-31).
- DELETE /areas/{id} — HARD-deletes standalone area tasks, undocumented.
- GET /api/tags — task_count omitempty, counts completed tasks too.
- POST /tags — undocumented lowercasing + reserved names siyuan/obsidian → 403 RESERVED.
- PATCH /tags/{id} — no parent-cycle check.
- GET /tags/{id}/tasks — open tasks only; ignores child tags.

Views/settings/filters/search:
- Upcoming `from` unvalidated; Upcoming includes today.
- GET /user/settings — returns secret ntfy_access_token; review_include_recurring undocumented.
- PATCH /user/settings — documented enums/ranges unvalidated; first PATCH for a user with no row is SILENTLY LOST (UPDATE 0 rows then GetOrCreate inserts defaults; user_settings.go:162-172).
- saved-filters — docs omit /api prefix; config opaque (no JSON validation); FK to users → 500 in proxy mode.
- GET /api/search — results always tags:[]; includes completed; FTS 500s.

Sync (see §5): fields is JSON-array string (docs say comma-separated); 410 off-by-one; push supports schedule/checklistItem(camelCase, pull emits checklist_item)/attachment/reminder/heading beyond docs' 4; conflict_resolved still applies client write; no batch limit; no SSE; /sync/full task shape differs from pull snapshot shape.

Error format: handlers use {error, code} JSON (helpers.go:14-16). Middleware uses http.Error → body with text/plain content-type (auth.go:31,39,50,61,70). Unknown /api/* paths return plain-text "404 page not found". 500s leak raw SQLite errors (err.Error()) throughout.

## 2. Data model and views

Tables (final schema, 28 migrations in internal/database/migrations):
- areas(id, title UNIQUE[006], sort_order, created_at, updated_at)
- projects(id, title UNIQUE-global, notes, area_id NOT NULL→areas ON DELETE RESTRICT[017], status CHECK open|completed|canceled, when_date, deadline, sort_order, ...) + project_tags
- headings(id, title, project_id→projects CASCADE, sort_order)
- tasks(id, title, notes, status CHECK open|completed|canceled|wont_do, when_date TEXT, when_evening INT [dead since 022], deadline TEXT, project_id/area_id/heading_id SET NULL, sort_order_today/_project/_heading REAL, completed_at, canceled_at, deleted_at[004], high_priority INT[008], created_at, updated_at)
- tasks_fts FTS5(title,notes) with sync triggers[002]
- task_tags, tags(id, title UNIQUE, parent_tag_id, sort_order, color[010])
- checklist_items(task_id CASCADE, completed, sort_order)
- attachments(task_id CASCADE, type CHECK file|link, title, url, mime_type, file_size, sort_order)
- repeat_rules(task_id UNIQUE CASCADE, frequency, interval_value, mode, day_constraints, pattern JSON[016])
- task_schedules(task_id CASCADE, when_date NOT NULL, start_time, end_time, sort_order, completed[021])[019]
- reminders(task_id CASCADE, type CHECK, value, exact_at), reminder_log UNIQUE(reminder_id,schedule_id,fire_at)[024]
- users(username UNIQUE, password_hash), user_settings(user_id PK, NO FK[009], 22 cols), push_subscriptions(→users), saved_filters(→users, view CHECK)
- change_log(seq AUTOINCREMENT, entity, entity_id, action, fields JSON, snapshot JSON, user_id, device_id, created_at)[027], purged after 90 days (scheduler.go:69-78)

Task fields:
- Single-tenant: no table except settings/saved_filters/push_subscriptions has user_id. Every authenticated identity sees/edits all data.
- Priority: boolean high_priority only; no levels.
- when/start date: tasks.when_date = ISO date | 'someday' | NULL, a denormalized copy of the first task_schedules row by sort_order (SyncPrimary, schedules.go:185-204). Up to 12 schedule rows/task; only first may be 'someday' (enforced on POST only).
- Evening: no flag; "This Evening" = today's schedule row has start_time >= settings.evening_starts_at by STRING compare (views.go:137,175).
- Deadline: ISO date string, compared lexicographically vs when_date (tasks.go:98,141,161).
- Area/project: projects require area; task.area_id NOT kept consistent with the project's area (not derived on create, not propagated on project area change, nulled by bulk move_project). Migration 014 backfilled once.
- Tags: color, parent_tag_id (hierarchy, no cycle check), lowercased, reserved siyuan/obsidian.
- Repeat: one rule/task. On complete/cancel/wontdo HandleTaskDone creates a new task copied from original with when_date=engine.Next(original.when_date), deadline dropped, checklist reset, attachments shared (same file on disk), reminders copied (non-exact if setting on); the rule MOVES to the new task (scheduler.go:134-203). Hourly cron reprocesses fixed rules whose task is completed.
- Schedules: multi-date; CleanupOnTaskDone completes past/today uncompleted and deletes future/someday uncompleted on done.
- Reminders: types at_start|on_day|minutes_before|hours_before|days_before|exact. 1-min cron joins reminders×schedules(completed=0) for open tasks, fires ±1min, dedup via reminder_log. "morning" hardcoded 09:00; settings read and discarded (scheduler.go:211-216).
- Timestamps: SQLite datetime('now') = UTC; Go "today" = local TZ (bug B12).

View rules (repository/views.go; all filter deleted_at IS NULL; "open"=status='open'):
- Inbox (:19-100): project_id IS NULL AND area_id IS NULL AND when_date IS NULL AND open; order sort_order_today. review = open non-deleted tasks with date(updated_at) < date('now','-Nd'), N=review_after_days, EXCLUDING inbox ids and (unless review_include_recurring) tasks with a repeat rule.
- Today (:102-285): "Today" section = open AND (when_date=today with no completed-blocking, OR deadline=today, OR a schedule row dated today and uncompleted) AND schedule start_time IS NULL OR < evening_starts_at; "This Evening" = same but start_time >= evening_starts_at (string compare). Overdue = open AND deadline < today. Earlier = open AND when_date<today AND !='someday' AND (deadline IS NULL OR >=today) AND has an uncompleted past schedule row. Completed = status in (completed,canceled,wont_do) AND COALESCE(completed_at,canceled_at,updated_at) >= today (UTC date string >= local today).
- Upcoming (:287-407): JOIN schedules(completed=0) WHERE open AND schedule.when_date >= from AND != 'someday'; grouped by schedule_date (one row per date, each task gets schedule_date). overdue = open AND deadline < from (NOT today — uses `from`). earlier = same as Today's earlier but relative to from.
- Anytime (:409-542): per area → open projects with tasks where when_date IS NULL; standalone tasks per area where project IS NULL AND when_date IS NULL. no_area: projects with area_id IS NULL and open; standalone no-area tasks where project IS NULL AND area IS NULL AND when_date IS NULL AND deadline IS NOT NULL (:569).
- Someday (:413): same as Anytime but when_date='someday' (and standalone-no-area requires only when_date='someday', no deadline condition).
- Logbook (:584-647): status in (completed,canceled,wont_do), ordered by COALESCE(completed_at,canceled_at,updated_at) DESC, LIMIT/OFFSET, grouped by that date. Total = count (ignores limit).
- Trash (:649-708): deleted_at IS NOT NULL, order deleted_at DESC, grouped by deleted_at date.
- Counts (:871-903): inbox/today/overdue/anytime/someday/logbook/trash via one query + a review subquery. today uses when_date=today OR deadline=today (NOT schedule rows), so it disagrees with the Today view. anytime count = when_date IS NULL AND (project OR area OR deadline NOT NULL) — includes tasks in non-open projects that the Anytime view hides. A deadline-only standalone task is counted in BOTH inbox and anytime [probe: inbox includes deadline-only, anytime no_area includes it, counts inbox:5 anytime:1].

View inconsistencies (AI-first impact): a deadline-only standalone task shows in Inbox AND Anytime [probe]; counts.today differs from Today view; Anytime count includes non-open-project tasks; Upcoming.overdue uses `from` not today; Upcoming includes today.

## 3. Auth

AUTH_MODE (config.go:44) ∈ {builtin (default), proxy, oidc, none}. JWT_SECRET required for builtin/oidc (config.go:63-65). Middleware internal/middleware/auth.go:

- API key: if API_KEY set and mode != "none", an Authorization: Bearer <key> header is constant-time compared (auth.go:28). On match → authenticates as userRepo.GetFirst() (first/admin user). Wrong key present → immediate 401, no cookie fallback. This is the non-interactive path for Hermes and widgets. [probe] works; wrong key → 401 {error,code}.
- builtin: JWT in httpOnly cookie "token", HS256 verified with JWT_SECRET. **No algorithm restriction in jwt.Parse (auth.go:75-77) and no exp/claims validation beyond jwtToken.Valid.** sub is taken as-is; a token with no sub still passes the middleware (then Me returns 401 "user not found", but data endpoints accept it — [probe] GET /api/tasks returned 200 with a sub-less token). Combined with the oidc_state leak this is an auth bypass (top finding 1).
- proxy: trusts AUTH_PROXY_HEADER (default Remote-User) as the raw user ID. That value is used directly as users FK for saved_filters/settings → saved-filters 500 in proxy mode; settings rows diverge from the API-key (GetFirst) identity.
- none: dev only; authenticates as GetFirst.
- oidc: see findings 1,2.

Non-interactive auth for agents/widgets: **only one shared API key (API_KEY env), no per-client or scoped tokens, no expiry, no revocation.** It authenticates as the admin user with full read/write, and GET /user/settings exposes ntfy_access_token to any key holder. For "separate small widget apps" reading slices, there is no read-only or scoped credential — every widget would hold the master key.

CORS: **none anywhere** [probe: OPTIONS preflight returns 405, no Access-Control-* headers; grep for cors/Access-Control in internal/cmd = 0 hits]. Browser widgets on another origin cannot call the API, and EventSource cannot send the Authorization header anyway (so SSE needs cookie auth from the same origin). Cookies lack Secure and are SameSite=Lax.

## 4. MCP

An MCP server EXISTS and is implemented: mcp/ (TypeScript, @modelcontextprotocol/sdk ^1.0.0, stdio transport). Not committed to history as a separate feature — present in the initial commit; node_modules gitignored, dist not built. Config via THINGSTODO_URL + THINGSTODO_API_KEY (Bearer). Spec: docs/superpowers/specs/2026-03-22-thingstodo-mcp-server-design.md; plan: .../plans/2026-03-22-thingstodo-mcp-server.md.

Implemented (44 tools, matching the spec): views (get_today/upcoming/inbox/anytime/someday/logbook), tasks (search_tasks, get_task, create/update/complete/cancel/wontdo/reopen/delete/purge/restore), bulk_action, projects (list/get/create/update/delete), areas (list/get/create/update/delete), tags (list/create/update/delete), headings (list/create/update/delete), checklist (add/update/delete), attachments (add_link, delete_attachment), schedules (add/update/delete). Thin client.ts wraps fetch with Bearer + error unwrap.

Excluded per spec (YAGNI): file uploads, reordering, settings, auth mgmt, SSE/sync, reminders, repeat rules.

MCP quality gaps for AI-first:
- update_task params are all optional strings via zod; there is no way to send JSON null, so an agent CANNOT clear when_date/deadline/project (the REST PATCH supports null via Raw, but the MCP tool never sends it).
- bulk_action description tells the model to pass {high_priority:true} for set_priority, but the backend reads params.priority → following the tool hint silently clears priority [probe].
- search_tasks requires a `search` arg (FTS), so there is no filter-only listing (e.g. "all tasks in area X") — and raw FTS input 500s.
- list_areas description claims nested projects; GET /api/areas returns flat areas only.
- No reminders/repeat/settings tools, so an agent cannot manage recurrence or notifications it is expected to own.
- stdio transport only — no HTTP/SSE MCP endpoint, so remote agents need a local process.

## 5. API qualities for machine clients

- Filtering: GET /api/tasks supports status, project_id, area_id, heading_id, tag_ids(csv, OR semantics), when_date, when_before, when_after, has_deadline, search. No created/updated range, no deadline range, no sort selection. GET /api/projects: area_id, status. Others: none.
- Pagination: ONLY logbook/trash (limit/offset). GET /api/tasks, /search, /sync/full return everything unbounded — a growing DB will produce large unpaged payloads.
- Idempotency: create endpoints accept a client-supplied `id`; sync create is idempotent by id (checks existing). REST create is NOT (no existing check → duplicate titles blocked for projects/areas/tags by UNIQUE, but tasks duplicate freely). No Idempotency-Key header [grep: 0].
- Bulk: POST /tasks/bulk (max 100), plus sync push batches (no size limit). No bulk create.
- Error format: inconsistent — handler JSON {error,code} vs middleware text/plain vs chi plain-text 404. Codes not enumerated in docs; 500 leaks SQL.
- Validation: minimal. Required-title only for task/project/area/tag/checklist; required when_date/url for schedule/attachment. No date/time/enum/range/format validation (findings 5,8; settings enums; reminder value; status). FK violations surface as 500.
- Partial update (PATCH null vs absent): implemented via decodeJSONWithRaw + a Raw map; for task when_date/deadline/project_id/area_id/heading_id, explicit null clears and absent is untouched (tasks.go:234-257). But title/notes/high_priority use pointer-presence only (so null title is treated as absent). Settings and tags use the same Raw trick for nullable fields. Good overall, but uneven.
- Resolve by name vs id: API is id-only. Name→id resolution exists only client-side in the CLI (internal/cli/resolve.go). An agent must list then map names itself.
- Change feed / sync / SSE: SSE at /api/events (no Last-Event-ID replay, events carry no seq). Change-log sync: /sync/pull (since+limit, 410 CURSOR_EXPIRED), /sync/full (snapshot+cursor), /sync/push (LWW by updated_at). Caveats: SyncPrimary when_date writes bypass the log (schedules.go:185-204), so a widget polling pull misses date changes made via the schedule path; sync push emits no SSE; log writes are best-effort outside the tx (changelog_helper.go:7-22); user_id/device_id never populated [probe COUNT=0]; conflict_resolved still applies the client write (contradicts docs); 410 fires when oldest==since+1 (off by one, sync.go:112) [probe].
- OpenAPI/Swagger: none [grep: 0]. Only the hand-written docs/api.md.
- Versioning: none — base path /api, no /v1 [grep: 0], no version header.

## 6. Bugs and risks (verified)

Security:
- S1 [probe] Path traversal read+delete via attachment url (finding 3). attachments.go:52-64 only defaults empty type; Download attachments.go:190 and Delete :163 join raw url; sync.go:1024-1053 same.
- S2 [code] JWT auth bypass + no alg restriction (finding 1). auth.go:75-91.
- S3 [code] OIDC account linking/takeover, no email_verified (finding 2). oidc.go:149-172.
- S4 [code] Single shared API key = admin, no scoping/expiry/revocation; ntfy_access_token readable by any key holder (user_settings.go GetOrCreate / models.go:228).
- S5 [code] Link attachments store arbitrary url incl. javascript: — rendered by the SPA could XSS; no scheme allowlist (attachments.go:60-63).
- S6 [code] 500 responses leak raw SQLite error text (err.Error()) across all handlers.
- S7 [code] No request body size limit except multipart upload; JSON bodies unbounded (helpers.go:18-21).
- S8 [code] Cookies lack Secure; SameSite=Lax (auth.go:59-66, oidc.go).

Correctness / concurrency:
- B1 [probe] No recover middleware → sync push panic on type mismatch drops the connection mid-batch; partial apply (sync.go:350 etc; router.go:30).
- B2 [probe] Non-atomic create: task row persists when tag/schedule step fails → 500 + orphan/duplicate (tasks.go:192-213).
- B3 [probe] bulk set_when "" and absent → phantom schedule when_date='' (tasks.go:572-577 via bulkNilIfEmpty only guards column, syncFirstScheduleDate gets &"").
- B4 [probe] bulk affected counts nonexistent/deleted ids (tasks.go:564).
- B5 [code] bulk move_project nulls area when area_id absent, leaves heading_id stale; PATCH move nulls all absent FKs.
- B6 [probe] No date/time validation → bad values pollute views (finding 5).
- B7 [probe] FTS 500 on c++, email:, trailing/embedded punctuation; raw search in /api/tasks (finding 7).
- B8 [code] exact reminders with Z/offset never fire (finding 8).
- B9 [code] after_completion == fixed; mode ignored (finding 9).
- B10 [probe] 410 cursor off-by-one (sync.go:112): returns Gone when oldest==since+1, forcing an unnecessary full sync.
- B11 [code] conflict_resolved still writes client data (sync.go:420-421,555-562) — "server was newer" is a label only; actual LWW is not enforced.
- B12 [code] UTC vs local-date skew: Today "completed today", Logbook grouping, Review staleness use COALESCE(...)>=local-today against UTC timestamps; near midnight in non-UTC TZ, completed items drop out of Today / shift logbook day.
- B13 [code] Reorder reads GetByID per item after commit (N+1) and re-logs; schedules reorder endpoint ignores the task scope ({id}) so it can move another task's schedules (schedules.go:187-204 + handler).
- B14 [code] user_settings first PATCH lost when no row exists (UPDATE 0 rows, then defaults inserted) (user_settings.go:162-172).
- B15 [code] DELETE project/area hard-deletes child tasks with no trash and (for tasks) emits only delete change-log entries, so an undo is impossible.
- B16 [code] SSE events frequently omit the task body and the checklist-update event mislabels id; a widget using events alone cannot refresh reliably.
- B17 [code] Repeat reopen duplicates: after done the rule is on the new task; reopening the old one yields two open recurring tasks.
- B18 [code] PATCH /tasks dup-timeless returns 400 but tasks.when_date already updated before the schedule sync fails (tasks.go:265-283) — task and its schedule disagree [probe: 400 returned, task.when_date=2030-01-02 while schedule unchanged].

## 7. Test coverage

IMPORTANT: `go test ./...` and `go vet ./...` FAIL out of the box from the repo root:
`internal/frontend/embed.go:5:12: pattern dist: no matching files found` (the embedded SPA dist/ is gitignored and absent). This breaks cmd/server, internal/cli, internal/frontend, internal/router (setup failed). CI works around it by `mkdir -p internal/frontend/dist && echo '<!doctype html>' > .../index.html` (.github/workflows/ci.yml). To audit I used a build overlay supplying a stub dist/index.html (no files changed).

Default `go test ./... -cover` (no dist, as a fresh clone sees it):
- cmd/server, internal/cli, internal/frontend, internal/router: FAIL [setup failed]
- cmd/ttd 0.0%, internal/config 0.0%, internal/database 64.3%, internal/handler 23.1%, internal/middleware 0.0%, internal/model 0.0%, internal/push 0.0%, internal/recurrence 55.6%, internal/repository 43.4%, internal/scheduler 0.0%, internal/sse 96.6%, internal/testutil 0.0% → overall FAIL.

With overlay, `go test ./... -cover`: all pass; per-package cli 40.9%, database 64.3%, handler 23.1%, recurrence 55.6%, repository 43.4%, router 83.8%, sse 96.6%; total 35.2%. go vet: clean (overlay). go test -race: all pass (no data races reported).

Cross-package `-coverpkg=./internal/...` (real reach of the suite, total 40.2%); per-file lines covered:
- scheduler/scheduler.go 2.2% — reminders, recurrence instance creation, fixed-rule cron, fireReminder, computeFireAt all UNTESTED.
- push/* ~3% — sender, ntfy, dispatcher UNTESTED.
- handler: oidc.go 0.0%, attachments.go 0.9%, schedules.go 0.9%, repeat_rules.go 1.5%, saved_filters.go 1.8%, push_subscriptions.go 2.2%, reminders.go 2.9%, user_settings.go 3.7%, events.go 4.2%, search.go 9.1%, views.go 13.8%, sync.go 25.6%, tasks.go 51.1%, projects.go 54.0%, areas.go 46.4%, tags.go 49.3%, checklist.go 65.0%.
- repository: push_subscriptions 1.9%, saved_filters 2.7%, reminders 9.7%, user_settings 11.8%, repeat_rules 14.0%, schedules 21.7%, views 52.7%, tasks 61.7%, search 86.8%, changelog 80.5%.
- middleware/auth.go 26.1% (proxy path only — the JWT/API-key branches largely untested).
- config, model(behavioral), cmd/* essentially 0%.

Tested areas (have _test.go): handler tasks/tasks_bulk/projects/areas/tags/checklist/sync/sync_integration; repository tasks/projects/areas/tags/checklist/headings/attachments/views/search/changelog/users; database/schema; recurrence engine; sse broker; cli app/config/dateparse.

Highest-value UNTESTED surfaces (ordered by AI-first risk): auth middleware (all 4 modes, JWT alg/exp), oidc handler, saved filters, schedules (handler+repo, the when_date source of truth), reminders (repo + scheduler firing math incl. exact/timezone), repeat/recurrence mode semantics + createNextInstance, push (sender/ntfy/dispatcher), search escaping, view edge cases (today/upcoming/counts consistency), user_settings update. sync has handler tests but not the panic/type-mismatch or conflict paths.

## 8. AI-first readiness summary

Blocking before an agent + public widgets:
1. Credentials: add scoped/read-only tokens (or at least per-client keys with revocation); stop returning ntfy_access_token to every key holder; the current single admin key is unsuitable for multiple widgets.
2. Security: fix attachment path traversal (allowlist stored names), restrict JWT alg + validate claims, fix OIDC linking, add a recover middleware, cap body size, stop leaking SQL in 500s, add a URL scheme allowlist for link attachments.
3. Robustness for machine writers: validate date/time/enum formats; make task create atomic (tx); return structured validation errors instead of 500 on FK/format; escape FTS input.
4. Machine ergonomics: CORS for widget origins; pagination + updated-since filter on /api/tasks; name→id resolution or a filter-only list; an OpenAPI spec; fix the change feed (log when_date syncs, record device_id, fix 410 off-by-one) so widgets can sync reliably.
5. Correctness: implement after_completion recurrence or document it as unsupported; fix exact-reminder parsing; reconcile counts vs views; UTC/local date handling.
6. MCP: let update_task clear fields (null), fix the set_priority hint, add reminders/repeat tools, consider an HTTP transport.
7. Docs: reconcile docs/api.md with code (events auth, vapid-key field name, response shapes, undocumented actions/fields, hard-delete semantics).

Verification note: findings labeled [probe] were reproduced on a running server built from this exact tree; [code] findings were read, not executed. No repository files were modified.

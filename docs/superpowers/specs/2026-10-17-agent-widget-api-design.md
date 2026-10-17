# API and MCP for Hermes and Widgets, on Correct Data — Design

Date: 2026-10-17
Status: approved by owner 2026-10-17

## Intent

ThingsToDo is AI-first. The owner talks to **Hermes Agent**, which runs on the owner's local machine. The ThingsToDo server runs on a remote VPS in Docker. Hermes reads and writes tasks through MCP and the REST API. Hermes does its own reasoning: it turns "on Friday" into a date, builds "what do I have today" from raw data, and decides what to show. The server's job is to hold **correct data** and give **complete, predictable access** to it.

**Widgets** are separate mini SPAs (HTML pages) on their own origins. Each widget gets an API token and calls the REST API directly. The widget builds its own view of the data. Widgets need the same full rights as Hermes.

The built-in web UI is secondary and must keep working; its own bugs are project 3 (previously project 4).

This spec merges projects 2 and 3 of `docs/review-and-roadmap.md`. The owner confirmed that Hermes does the reasoning, so server-side natural-language dates, title-token parsing and saved-filter evaluation are dropped.

## Goals

1. **Access.**
   - Many named API tokens with full rights, each revocable on its own.
   - Open CORS for token requests, so widgets on any origin work.
2. **MCP inside the Go server** at `/mcp` over Streamable HTTP, so Hermes connects by URL with a token. It replaces the TypeScript server in `mcp/`.
3. **Query API.** Hermes and widgets can fetch any slice of tasks with filters, sorting and pagination, without full-text search.
4. **Safe writes.**
   - Inputs are validated at the boundary, and errors are structured, never a 500 for bad input.
   - Writes are atomic.
   - Retries do not duplicate.
   - Deletes are recoverable.
5. **Correct data.** The rules that change data without Hermes are right:
   - recurrence (both modes);
   - completion runs once;
   - reminders fire on time;
   - "today" means the owner's timezone;
   - views are consistent;
   - the change feed records the writes this spec touches (E5), so widgets and pulls see them.
6. **A machine-readable contract:** an OpenAPI document that tests keep in sync with the router.

## Non-goals

- Token scopes or read-only tokens. All tokens have full rights (owner decision).
- An origin allowlist for CORS (owner decision).
- Priority levels. Priority stays the boolean `high_priority` (designer's call; levels can come later).
- Server-side natural-language dates, `#tag`/`$project` title parsing, saved-filter evaluation, name→id resolution. Hermes does these itself.
- Web UI changes, except what is required so it does not break.
- Sync push semantics move to project 3 (frontend), because only the web UI uses `/sync/push` and changing them changes UI sync behavior. That covers:
  - `conflict_resolved` client-wins;
  - `applyTaskChange` dropping other fields of a change that carries `status` or `deleted_at` (`handler/sync.go:515-582`);
  - the frontend queue bugs.

  This spec changes the sync path only where stated: the completion guard (D3), project and area deletes (D5), area derivation (D5), and SSE emission (E5).
- Making every change-log write transactional. Only the writes this spec rewrites (D3, D5, E2, E5) become transactional. Other repositories keep best-effort logging (`changelog_helper.go`) until the frontend project reworks sync.
- Tag inheritance from projects/areas (Things behavior); API versioning (`/v1`); changing the timestamp format.
- Multi-user support.

## Anti-goals

- Breaking the web UI, the CLI `ttd`, or existing API clients that use `API_KEY`.
- Tokens in URLs, in logs, or stored in plaintext.
- A second implementation of business logic for MCP. MCP tools must go through the same code path as REST.
- Disabling or weakening tests.

## Constraints

- Single user, single SQLite DB. Server timezone comes from `TZ` (already loaded into `config.Location`).
- TDD: a failing test first for every behavior change.
- `docs/api.md` must be updated for every API change (`CLAUDE.md`).
- New dependency approved by the owner: `github.com/modelcontextprotocol/go-sdk`.
- **Go toolchain upgrade (approved by the owner 2026-10-17):** move `go.mod` (`go 1.27.0`), `Dockerfile` (`golang:1.27-alpine`) and CI (`go-version: "1.27"`) to Go 1.27, and use go-sdk v1.8.0.
  - Why: go-sdk v1.5.0+ requires Go ≥ 1.25; v1.8.0 (2026-09-14) declares `go 1.25.0`.
  - Go 1.24 no longer receives security fixes: supported releases are go1.27.2 and go1.26.9 (go.dev/dl, checked 2026-10-17).
  - Do the upgrade as its own first step, with the full test suite green before any feature work.

## Design

### A. Access

#### A1. Named API tokens

- **Table `api_tokens`:** `id`, `name` (unique, non-empty), `token_hash` (SHA-256 hex of the token), `prefix` (first 8 characters of the token, for display), `created_at`, `last_used_at`, `revoked_at` (NULL while active).
- **Token format:** `ttd_` followed by 32 random bytes from `crypto/rand`, base64url without padding.
  - The plaintext is returned only once, in the create response.
  - Only the hash is stored.
- **Auth middleware:** a `Bearer` value is accepted if it equals the env `API_KEY` (existing behavior, constant-time compare) or if its SHA-256 matches an active row.
  - The match authenticates as the single user, the same as the API key does today.
  - `last_used_at` is updated at most once per minute per token, so reads don't write on every request.
  - A wrong `Bearer` value still gets 401. There is no fallback to the cookie.
  - Per `AUTH_MODE`: tokens and the env key work in `builtin`, `oidc` and `proxy`, as the env key does today. In `none`, Bearer is ignored, as today (`middleware/auth.go:28`).
- **Auth method in the request context:**
  - `session`: a cookie JWT, the `proxy` header, or `none` mode.
  - `master_key`: the env `API_KEY`.
  - `token`: a per-client token.
- **Management endpoints:**
  - `GET /api/tokens` lists id, name, prefix, created_at, last_used_at and revoked_at, never the hash.
  - `POST /api/tokens {name}` returns `{id, name, token}`.
    - A client-supplied `id` is not accepted; the D3 retry rule does not apply to tokens.
    - A duplicate `name` gets 409 `CONFLICT`.
    - A lost create response is recovered by revoking and creating a new token.
  - `DELETE /api/tokens/{id}` revokes.
  - **These endpoints accept only `session` or `master_key`.** A request authenticated with `token` gets 403. A leaked widget token therefore cannot mint new tokens or revoke others.
- **CLI:** `ttd token create <name>`, `ttd token list`, `ttd token revoke <id>`. They run through the CLI's existing API-key client.
- **No expiry.** Revocation is the control (YAGNI; add `expires_at` when needed).
- **Risk the owner accepts:** a widget token is visible in the widget's source and gives full data access. Mitigation is revocation per token.

#### A2. CORS

- A middleware on `/api/*` and `/mcp` sets:
  - `Access-Control-Allow-Origin: *`
  - `Access-Control-Allow-Methods: GET, POST, PUT, PATCH, DELETE, OPTIONS`
  - `Access-Control-Allow-Headers: Authorization, Content-Type, Mcp-Session-Id, Mcp-Protocol-Version, Last-Event-ID`
  - `Access-Control-Expose-Headers: Mcp-Session-Id`
  - `Access-Control-Max-Age: 600`
- No `Allow-Credentials`, so cross-origin scripts cannot read responses to cookie-authenticated requests.
- **Cross-origin requests must use a Bearer.** CORS alone does not stop a browser from sending a `SameSite=Lax` cookie: a same-site page can send it with `credentials: include`, and a simple `text/plain` POST needs no preflight. So the auth middleware rejects with 403 `FORBIDDEN` any request that:
  - carries an `Origin` header whose host differs from the request `Host`, and
  - would authenticate as `session` (cookie or proxy header).

  Requests without `Origin` (CLI, Hermes, curl) and same-origin requests (the web UI) are unaffected. Public routes (`/api/auth/*`, `/api/openapi.yaml`, `/health`) are exempt.
- A preflight `OPTIONS` request returns 204 before auth runs.
- **CORS is off in `AUTH_MODE=none`.** That mode has no auth, so `*` would let any website the owner visits read and write the data.

#### A3. Live updates for widgets

- `/api/events` (SSE) already accepts `Authorization: Bearer`.
- The browser's `EventSource` cannot send headers. Widgets read the stream with `fetch()` and a `ReadableStream` instead. Document this with a minimal snippet in `docs/api.md`.
- Tokens in query strings are not supported (anti-goal).

### B. MCP in the Go server

- **Transport:** `POST/GET /mcp`, served by `mcp.NewStreamableHTTPHandler` with `Stateless: true`. Stateless is required for MCP revision 2026-07-28; clients negotiate older revisions automatically.
- **Auth:** the same middleware and tokens as REST, plus CORS from A2.
- **Hermes config** (documented in README):

  ```yaml
  mcp_servers:
    thingstodo:
      url: https://<host>/mcp
      headers:
        Authorization: "Bearer ttd_…"
  ```

  This is from the Hermes Agent MCP docs as summarized by Perplexity on 2026-10-17. Verify it against the installed Hermes version.
- **One code path.** Each tool builds an in-process HTTP request (method, path, JSON body) and dispatches it to the same chi router behind the auth middleware, carrying the caller's auth context.
  - The tool result is the response JSON.
  - A non-2xx response becomes an MCP tool error, with the API error body as its text.
  - MCP therefore inherits validation, side effects (SSE, recurrence, default reminders) and errors from REST, with no duplicated logic.
- **Tools:** every REST capability an agent needs. Each tool has a description written for an LLM: what it does, the units and formats it expects, and that `null` clears a field.
  - Minimum set: everything the current TypeScript server exposes (44 tools, `mcp/src/tools/*.ts`), plus:
    - the task query from C1;
    - repeat rules (get, set, delete);
    - reminders (list, add, delete);
    - user settings (get, update);
    - views counts.
  - Fixes from the review:
    - `update_task` accepts `null` to clear `when_date`, `deadline`, `project_id`, `area_id` and `heading_id`;
    - `bulk_action` documents the real params;
    - `list_areas` describes what it returns;
    - every create tool accepts an optional `id` and describes the retry rule from D3 (repeat with the same `id` after a lost response).
- **The TypeScript server is removed.** Delete `mcp/`, update README (MCP section) and `CHANGELOG.md`.

### C. Query API

#### C1. `GET /api/tasks`: filters, sort, pagination

New and changed query params. All are optional and combined with AND.

| Param | Meaning |
|---|---|
| `status` | comma list of `open,completed,canceled,wont_do`. Default: every status, as today. Applies independently of `list`. |
| `list` | `inbox`, `today`, `upcoming`, `anytime`, `someday`, `logbook`, `trash`. A task matches when it appears in that view under the E3 predicates. Views may overlap, and each task is returned once. Example: a task with a project, no when date and an overdue deadline is in Today (Overdue) and Anytime. Every query excludes trashed tasks (`deleted_at IS NULL`), except `list=trash`, which selects only them (`deleted_at IS NOT NULL`). |
| `project_id`, `area_id`, `heading_id` | as today. `area_id` matches the stored `tasks.area_id`, which D5 keeps equal to the project's area. |
| `tag_ids` + `tag_mode` | `tag_mode=any` (default, today's behavior) or `all`. |
| `when_date`, `when_before`, `when_after` | as today, validated as dates. |
| `deadline_before`, `deadline_after` | inclusive date bounds. |
| `has_deadline` | as today (`true` or `false`), validated. |
| `has_when` | `true` or `false` (new). |
| `high_priority` | `true` or `false`. |
| `updated_since`, `created_since` | UTC timestamp, inclusive. |
| `search` | FTS, escaped (D4). |
| `sort` | `manual` (default), `when_date`, `deadline`, `created_at`, `updated_at`, `title`; prefix `-` for descending. `manual` means `sort_order_heading` when `heading_id` is given, `sort_order_project` when `project_id` is given, otherwise `sort_order_today`, then `created_at` (today's default). Ties break by `id`. |
| `limit` | 1–500. Pagination is opt-in: without `limit` or `cursor`, all matching tasks are returned, as today, so existing clients such as the CLI resolver (`internal/cli/resolve.go:46`) keep working. MCP tools always send a `limit`. |
| `cursor` | opaque string from the previous response; requires the same filters and sort. |

- **Response:** `{"tasks": [...], "next_cursor": "…" | null}`. `next_cursor` is set only when `limit` was given and more rows exist. It encodes the last row's sort key and id.
- Responses fill `project_name` and `area_name`, which are always `null` today.
- Unknown params or invalid values get 400 `VALIDATION` with `field`.
- **`/api/search` gets cursor pagination** and keeps its response shape: `{"results": [...]}` with the existing `SearchResult` items, read by `frontend/src/components/SearchOverlay.tsx:37`.
  - Adds `next_cursor`.
  - Keeps the default `limit` of 20.
  - Keeps the order: rank first, then `id` as the unique tie-breaker. The cursor encodes both.
- **`/sync/full` is not paginated.** The web UI calls it once without `limit` (`frontend/src/sync/pull.ts:181`), and sync semantics belong to the frontend project.

#### C2. Views stay

`/api/views/*` stay as ready-made slices for widgets, and E3 fixes their rules.
- Each `/api/views/counts` value equals the number of **distinct task ids** in the corresponding view, excluding the Completed section.
- Today and Upcoming may return one row per schedule entry, and Today's sections can overlap (Overdue and Earlier, or Today and This Evening for a task with two schedules). A task therefore counts once even when it appears on several rows.

### D. Safe writes

#### D1. Validation at the boundary

- **Dates:**
  - `when_date` is `YYYY-MM-DD` and a real calendar date, or `someday`.
  - `deadline` is a real date.
  - `deadline >= when_date` (existing rule).
- **Times:** `start_time` and `end_time` are `HH:MM`, 00:00–23:59, and `end_time > start_time` when both are set.
- **Enums:**
  - task `status`, project `status`;
  - reminder `type`; reminder `value` in range (0–99 as documented);
  - recurrence pattern fields;
  - settings enums and ranges.
- **Exact reminders:** `exact_at` is RFC 3339 with an offset (`2026-10-20T09:00:00+03:00` or `Z`). A naive local `YYYY-MM-DDTHH:MM[:SS]` is also accepted and read in the server timezone. The value is stored normalized to UTC RFC 3339.
- **Referenced ids** (`project_id`, `area_id`, `heading_id`, `tag_ids`, the parent task of checklist, schedule, reminder and repeat) must exist, otherwise 422 `VALIDATION` with `field`. A `heading_id` must belong to the given or current `project_id`.
- **Titles:** an empty title is rejected on update as well as on create.

#### D2. One error envelope

- Every API error body is `{"error": "<message>", "code": "<CODE>", "field": "<name, optional>"}` with `Content-Type: application/json`. That covers handlers, the auth middleware (which today writes `text/plain` through `http.Error`), and unknown `/api/*` routes and methods (today chi plain text).
- **Codes:** `VALIDATION` (400, 422), `UNAUTHORIZED` (401), `FORBIDDEN` (403), `NOT_FOUND` (404), `METHOD_NOT_ALLOWED` (405, with the `Allow` header kept), `CONFLICT` (409), `PAYLOAD_TOO_LARGE` (413), `RATE_LIMITED` (429), `INTERNAL` (500). Keep any existing code values that clients match on: check `frontend/src` and `internal/cli` before renaming any.
- Document the list in `docs/api.md` and OpenAPI.

#### D3. Atomic writes and idempotent retries

- **Atomic task writes.** Task create and update run in one transaction: row, tags, first schedule, default reminder, and the change-log entries. If any step fails, nothing is written.
- **Retries without duplicates.** Every create endpoint accepts a client-supplied `id`, already the case for tasks.
  - A repeated `POST` with an `id` that already exists returns 200 with the existing entity and does not modify it.
  - This is the retry mechanism for Hermes. No `Idempotency-Key` table (designer's call: the client id already gives idempotency).
  - Ids must match the nanoid format the app uses: 8–21 characters of `[A-Za-z0-9_-]`. Anything else gets 400.
- **Completion runs once.**
  - The guard is `WHERE status = 'open'`. It lives in the repository: `TaskRepository.Complete`, `Cancel`, `WontDo`, and the status actions of `BulkAction` (today `repository/tasks.go:472` has no guard).
  - Each method reports whether a row changed, and every caller runs `HandleTaskDone` only for changed rows. The callers are the REST endpoints, bulk (`handler/tasks.go:386-389`) and the sync push status mapping.
  - Calling `complete` on a completed task returns 200 with the task and spawns nothing.
  - **The status change and the next instance commit together.** For a task with a repeat rule, all of this runs in one transaction:
    - the status update;
    - schedule cleanup;
    - creating the next instance (E2);
    - moving the rule;
    - the change-log entries.

    If any step fails, the whole transaction rolls back, the task stays open, and the caller gets 500 `INTERNAL`. A retry then works normally.
  - Because every path goes through the guard, this also neutralizes the web UI's double completion (frontend H1, via `/tasks/bulk` plus `/sync/push`).

#### D4. Search never 500s

FTS input is tokenized and each token is quoted as an FTS5 string, so operators and punctuation are literal: `c++`, `email:`, `milk-2`. This applies to `/api/search` and the `search` param.

`/api/search` keeps prefix matching (today `ftsPrefix`, `repository/search.go:19-30`): each quoted token keeps its `*` prefix suffix (`"milk"*`). Tests assert the expected tasks are found, not only status 200. A prefix such as `mil` still finds "milk", and `c++` matches a task titled "c++ notes".

#### D5. Recoverable deletes and precise moves

- **An area that still has projects cannot be deleted.** REST keeps 409 `HAS_PROJECTS` (`handler/areas.go:118`), and sync returns the same as a per-change error. `projects.area_id` is `NOT NULL ... ON DELETE RESTRICT`, migration 017.
- **Project and area deletes send child tasks to Trash** (`deleted_at`). For an area this means its standalone tasks, since it has no projects by the rule above. Today REST hard-deletes them (`DeleteWithTasks`). The sync path (`handler/sync.go:736`) deletes only the row, and the foreign key makes the tasks loose with no change-log entries.
  - Both paths call one repository method, which writes a change-log entry per trashed task, so the web UI receives the change on pull.
  - The project or area row itself is deleted, and its tasks lose that reference.
  - Restoring a task from Trash brings it back as a loose task.
- `PATCH /tasks/{id}/move` changes only the keys present in the body. Today an absent key is set to NULL.
  - **One exception, for move and update alike:** when the project changes and no `heading_id` is given, a heading that does not belong to the new project is cleared.
  - A `when_date` in a move goes through the same first-schedule sync as update (E5).
- **A task's area follows its project.** Whenever a task has a `project_id`, its `area_id` is that project's area. A client-supplied `area_id` that disagrees is overwritten, not rejected.
  - The server enforces this on task create, update, move, bulk and sync create/update.
  - When a project's area changes, the same transaction updates its tasks' `area_id` and writes a change-log entry per changed task.
  - A one-time backfill fixes existing rows. It runs at startup in Go through the repository, not as a SQL migration, so each changed task gets a change-log entry and synced clients receive it on pull. It is idempotent: once the rows agree, it changes nothing.
- **User settings:** the first `PATCH /user/settings` for a user with no row is saved. Upsert instead of UPDATE-then-create (`user_settings.go:162-172`).
- **Bulk:**
  - `set_priority` reads `params.high_priority` (boolean) and keeps accepting the legacy numeric `params.priority`.
  - `affected` counts only rows that changed.
  - `set_when` with an empty value clears the date without creating a phantom schedule.
  - `move_project` sets the area from the target project and clears a heading that does not belong to it.
- `PATCH /tasks/{id}/schedules/reorder` only reorders schedules of task `{id}`.

### E. Correct data

#### E1. The owner's timezone everywhere

- "Today" and every date bound are computed with `time.Now().In(cfg.Location)`, never `time.Now()` alone and never SQLite `date('now')`. This affects `repository/views.go`, scheduler, review cutoff and counts.
- Stored timestamps stay UTC `YYYY-MM-DD HH:MM:SS` (format unchanged).
- Comparisons such as "completed today" compute the UTC bounds of the local day in Go and compare timestamps against them.
- Logbook and Trash group by the local date.
- The API docs state that timestamps are UTC and that dates are in the server timezone (`TZ`).

#### E2. Recurrence

- **`after_completion`:** run the existing pattern calculator with the completion date (local) as the base, instead of the original `when_date`.
  - Weekly every 1, no `on` days, due Wednesday, done Saturday: the next instance is the following Saturday (Things semantics).
  - With `on` days (e.g. `on: [wed]`), the calculator returns the next listed weekday after the completion date.
- **`fixed`:** advance from the original `when_date`, as today.
  - If the result is before today, keep advancing until it is today or later, so a late completion never spawns an already-overdue instance.
  - Only one instance is created.
- Remove the dead `HandleAfterCompletion`. `HandleTaskDone` picks the base date by mode.
- **Attachment copies stay independent (data-loss fix).**
  - Instances copy file attachments as new rows pointing to the same stored file (`scheduler.go:176-185`).
  - Today deleting any one of them calls `DeleteByURL` (`handler/attachments.go:187-196`, `repository/attachments.go:127`). That removes every row sharing the file and the file itself, so deleting an attachment on an old instance destroys it on the new one.
  - Required:
    - every attachment delete path (REST and sync) deletes only the row with the given `id`;
    - the stored file is removed only when `CountByURL` reports no remaining rows;
    - the row delete and the count run in one transaction; the file removal follows the commit.
  - Test: create a recurring task with a file attachment, complete it, delete the original's attachment. The new instance's attachment still downloads. Deleting the last copy removes the file.
- **Fix two calculator bugs** in `internal/recurrence/calculators.go`:
  - **Monthly types skip short months.** `from.AddDate(0, every, 0)` from Jan 31 lands in March (`MonthlyDOMCalculator.Next`; `monthly_dow` and `monthly_workday` share the pattern). Compute the target month from the first day of `from`'s month, then pick the day.
  - **`yearly_date` Feb 29 loops forever.** When `every` never reaches a leap year (e.g. every 4 from 2030), the loop never ends. Remove it: a non-leap target year clamps to Feb 28.
    - The same clamp applies in `CurrentPeriod`, which today returns an error for Feb 29 in a non-leap year (`calculators.go:431-432`).
    - `FirstOnOrAfter` (`handler/repeat_rules.go:73-76`, used when a rule is set on an undated task) still never returns a date before its input (`recurrence/engine.go:54-76`).
    - So in a non-leap year it yields Feb 28 of the current year only when the input is on or before Feb 28. A later input moves on to the next occurrence. Tests cover inputs before, on and after Feb 28.
  - Tests cover month ends (Jan 31 → Feb 28/29, Mar 31) for all three monthly types, and Feb 29 with `every` of 1 and 4.
- Idempotent via D3. Instance creation (task, checklist, attachments, reminders, rule move) runs in one transaction.
- Tests cover both modes, a missed fixed date, and double completion.

#### E3. Views are consistent

Following Things semantics:
- A task with a deadline but no project, area or when date is in **Inbox, not Anytime**. It still appears in Today (deadline today) or Overdue (deadline passed), as today. Counts follow. Tests cover deadlines yesterday, today and tomorrow.
- **Today's "Earlier" section** includes every open task with `when_date < today`, not `someday`, regardless of schedule rows.
- **A task with a `when_date` and no schedule rows** is treated as having one uncompleted schedule on its `when_date`, the same synthetic entry the web UI uses. So a future date without a schedule row appears in Upcoming (today `views.go:312-313` requires a schedule JOIN).
- **Placement:** every open, non-trashed task is in at least one of Inbox, Today (any section), Upcoming, Anytime and Someday. The single documented exception is a task dated today whose today schedules are all completed and that has no future schedule: its work for the date is done, so it is in no list until it is completed or rescheduled. A fixture pins this.
- `/views/counts` uses the same predicates as the views. Today count = distinct task ids across the Today, This Evening, Overdue and Earlier sections (C2).
- **Upcoming** starts tomorrow by default (`from` still overrides). Its Overdue and Earlier sections stay, so the response shape is unchanged, but both are relative to today, not `from`. Today's tasks therefore never land in Upcoming's Earlier.
- **Evening** compares times as validated `HH:MM` (D1), so the string compare becomes correct. No schema change.
  - A data migration zero-pads existing `H:MM` values in `task_schedules.start_time` and `end_time` (e.g. `9:00` → `09:00`).
  - Values that do not parse are left unchanged and logged.
- These predicates live in one place in `repository/views.go`, and `list=` (C1) uses the same predicates.

#### E4. Reminders

- Exact reminders parse the stored RFC 3339 form (D1). Existing naive rows are read in the server timezone.
- "Morning" stays the constant 09:00. No setting exists for it (`model/models.go:209-232`).
  - Remove the dead settings read in `scheduler.go:211-216`.
  - Correct `docs/api.md:474`, which implies such a setting.
- **Local wall-clock times.** Fire times are built with `time.Date(y, m, d, hh, mm, 0, 0, loc)` from the local date and time. Today the code adds hours to local midnight (`scheduler.go:284,309,327,330`), which is wrong on DST days. Relative types (minutes, hours or days before) subtract from that anchor.
- **Timeliness.** A reminder never fires before its `fireAt`. It fires on the first scheduler tick at or after `fireAt`, so at most one tick (1 minute) late. Today the window extends one minute past now, so a 09:00 reminder can fire at 08:59.
- **Tests:**
  - fire-time math for every reminder type;
  - the dedup log;
  - a non-UTC timezone;
  - DST start and end days in a DST zone (e.g. `Europe/Berlin`);
  - a tick just before, at and just after `fireAt`.

#### E5. Change feed and events

- **`tasks.when_date` always equals the first schedule row**, on every write path, in both directions:
  - **Schedule → task:** schedule create, update, delete and reorder, through REST and through sync (`handler/sync.go:956,1007,1016` today skip it), call `SyncPrimary`.
  - **Task → schedule:** task update, move and bulk `set_when` go through `syncFirstScheduleDate`.
- `SyncPrimary` (`schedules.go:185-204`) writes a change-log entry for the task when it rewrites `when_date`. The schedule write, the derived date and its change-log entries commit in one transaction.
- Within the transactions introduced in D3 and E2, change-log rows are written in the same transaction.
- **Fix the 410 off-by-one** (`sync.go:112`): `CURSOR_EXPIRED` only when `oldest > since + 1`.
- Sync push broadcasts SSE events for applied changes, like REST does.
- Checklist update events carry `task_id` and the item `id` correctly.
- Every SSE event that reports a change to one entity carries `entity`, `id` and `seq` (the change-log seq when one exists), so a widget can refetch exactly what changed.
  - `bulk_change` keeps its `ids` array and has no `seq`.
  - `connected` and `reminder_fired` are not entity changes and are exempt.

### F. Contract

- **OpenAPI.** `docs/openapi.yaml` is a hand-written OpenAPI 3.1 file covering every `/api` route, the auth scheme, the error envelope, and the request and response shapes. It is served at `GET /api/openapi.yaml` without auth, and is embedded in the binary.
- **Drift test.** A router test walks the chi routes (`chi.Walk`) of a `builtin`-mode router. It fails if any `/api` route is missing from `openapi.yaml`, or if the file lists a route that is neither registered nor in an explicit list of mode-conditional routes. Today that list is `GET /api/auth/oidc/login` and `GET /api/auth/oidc/callback`, registered only in oidc mode.
- **`docs/api.md`** stays the human guide. Reconcile it with code using the discrepancy list in `docs/review-and-roadmap.md` §3.6.

## Tests (summary)

Each item above starts with a failing test, using the existing conventions: Go `testing`, `internal/testutil`, and the handler and repository test patterns.

| Area | Tests |
|---|---|
| Tokens | create returns the plaintext once and stores a hash; list never shows the hash; a revoked token gets 401; token management with a per-client token gets 403 and with a cookie or the env key works; the env `API_KEY` still works |
| CORS | preflight 204 with the headers, without auth; no `Allow-Credentials`; a token GET from another origin carries `Allow-Origin: *`; no CORS headers in `none` mode; a cross-origin `text/plain` POST with only a session cookie gets 403; a same-origin cookie request works |
| MCP | the go-sdk in-memory or `httptest` client lists tools, creates a task, clears a date with `null`, and gets a tool error from a 422; unauthenticated `/mcp` gets 401 |
| Query | every filter, `tag_mode=all`, sort directions, a cursor across pages with no duplicates or gaps, no `limit` returns everything, `list=trash`, invalid params get 400 |
| Validation | every rule in D1, with the `field` in the error |
| Envelope | middleware 401, unknown route 404 and wrong method 405 are all JSON |
| Atomic and idempotent | a create with a bad tag writes nothing; a repeated POST with the same id returns 200 and no duplicate; double complete (REST, bulk, sync) spawns one instance; a failure while creating the next instance leaves the task open |
| Search | `c++`, `email:`, `milk-2` return 200 |
| Deletes and moves | a project delete through REST and through sync puts its tasks in Trash, with change-log entries; an area with projects gets 409; a move changes only present keys; the bulk fixes; a task's area follows its project on every write path and on project area change; the first settings PATCH is saved |
| Timezone | with `TZ=Europe/Moscow` and a clock just after local midnight, "completed today", Logbook grouping and Today membership are right (inject the clock) |
| Recurrence | both modes, missed fixed date, transactional instance creation, month ends, Feb 29 |
| Views | the four E3 rules; counts equal distinct tasks in views, including a task with two schedules and one in both Overdue and Earlier; a legacy `9:00` start time is not Evening after migration |
| Reminders | fire-time math per type, RFC 3339 with offset, morning setting |
| Change feed | a `SyncPrimary` date change appears in `/sync/pull`; schedule CRUD via REST and sync keeps `when_date` equal to the first schedule, and move with `when_date` updates the schedule; project area change and backfill emit task entries; 410 boundary; sync push emits SSE |
| MCP retry | a create tool called twice with the same `id` creates one entity |
| Search | prefix and literal-punctuation queries return the expected tasks; `/api/search` keeps `results` and pages by rank with stable ties |
| OpenAPI | the drift test |

## Docs to update

- `docs/api.md`: tokens, CORS, SSE via `fetch`, query params, pagination, validation rules, error codes, idempotent create, delete semantics, timezone note, MCP endpoint.
- `docs/openapi.yaml` (new).
- `README.md`: Hermes MCP config; the `mcp/` section removed; token management.
- `CHANGELOG.md`.
- `docs/review-and-roadmap.md`: project 1 marked done; projects 2 and 3 merged into this one; the frontend project renumbered to 3.
- `CLAUDE.md`: replace the stale "Current Phase: Phase 1" line (shared file, owner approval via this spec).

## Calls made (designer's, not the owner's)

- Priority stays boolean.
- Token management is allowed only with a session cookie or the env `API_KEY`, never with a per-client token.
- Tokens don't expire; revocation is the control. `last_used_at` is updated at most once per minute.
- SSE for widgets uses `fetch()` streaming; no tokens in URLs.
- MCP tools dispatch in-process to the REST router (one code path).
- The TypeScript MCP server is deleted.
- Idempotent create by client-supplied `id`, no `Idempotency-Key` table.
- Things semantics for views: deadline-only loose tasks stay in Inbox; Upcoming starts tomorrow; overdue start dates stay in Today ("Earlier").
- `after_completion` advances from the completion date.
- Project and area deletes send tasks to Trash.
- Timestamp format unchanged, documented as UTC.
- `after_completion` runs the pattern calculator from the completion date; `fixed` rolls a past next date forward to today or later.
- `list=` means "appears in that view"; counts count distinct tasks.
- `/sync/full` stays unpaginated. `/api/tasks` pagination is opt-in, so the CLI and other existing clients keep working.
- Cross-origin requests authenticated only by a session get 403, as CSRF protection for open CORS.
- A task's area is derived from its project. A conflicting client value is overwritten, not rejected.
- Repeat creation and completion commit in one transaction.
- Legacy `H:MM` times are zero-padded by migration.
- Feb 29 yearly rules clamp to Feb 28 in non-leap years.
- Reminder "morning" stays 09:00; no new setting.
- CORS is off in `AUTH_MODE=none`. Token management also accepts `proxy` sessions.
- **The web UI's lists keep their own local rules until the frontend project.** For example, a deadline-only loose task shows in Anytime in the UI but in Inbox for Hermes and widgets. This drift is accepted because the UI is secondary.
- Deleting a project or area in the web UI now sends its tasks to Trash, instead of making them loose. That matches Things.
- OpenAPI is hand-written, with a drift test, instead of a generator dependency.

## Left to the builder

- Package layout for tokens, CORS and MCP (follow existing `internal/handler`, `internal/middleware`, `internal/repository` patterns).
- Cursor encoding.
- The exact list and names of MCP tools beyond the minimum set.
- How to inject the clock for timezone tests.
- The order of the work, as long as each step keeps all tests green.

## Verification

- `go test ./... -race`, `go vet ./...`, `golangci-lint run`.
- `cd frontend && npm test && npm run lint`: the web UI still passes.
- Against a locally built server:
  - create a token and call the API from another origin (preflight plus GET);
  - connect an MCP client to `/mcp` with the token, list tools, create and complete a recurring task, and confirm exactly one next instance;
  - run the D4 search inputs.

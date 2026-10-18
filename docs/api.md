# ThingsToDo API Contract

Base URL: `/api`

All endpoints return JSON. All mutations broadcast SSE events. Authentication via httpOnly JWT cookie (builtin/oidc mode), proxy header (proxy mode), or API key via `Authorization: Bearer <key>` header.

An OpenAPI 3.1 contract of every route below is served at `GET /api/openapi.yaml` (no auth). For agent integration over MCP, see the `/mcp` endpoint (README documents it fully).

---

## Common Patterns

### Error Response
```json
{
  "error": "Human-readable error message",
  "code": "ERROR_CODE",
  "field": "offending request field (optional)"
}
```

Every error response — including auth middleware rejections, unknown `/api` routes (404) and unsupported methods on known `/api` routes (405, which keeps the `Allow` header chi computes) — uses this single JSON envelope with `Content-Type: application/json`. The `field` property is omitted unless the error is about a specific request field.

`5xx` responses always carry the generic body `{ "error": "internal error", "code": "INTERNAL" }`; the underlying cause is logged server-side and never included in the response.

Error codes:

| Code | Status | Meaning |
| --- | --- | --- |
| `UNAUTHORIZED` | 401 | Missing/invalid session cookie, proxy header, API key, or credentials |
| `FORBIDDEN` | 403 | Authenticated but not allowed (e.g. OIDC email not permitted, or a named API token calling `/api/tokens`) |
| `NOT_FOUND` | 404 | Unknown `/api` route, or entity does not exist |
| `METHOD_NOT_ALLOWED` | 405 | Method not registered for a known `/api` route; `Allow` header lists supported methods |
| `BAD_REQUEST` | 400 | Malformed JSON body or invalid request parameters |
| `VALIDATION` | 400/422 | Request field failed validation; 400 for malformed values, 422 for referenced ids that do not exist (carries `field`) |
| `RESERVED` | 403 | Reserved tag name |
| `DUPLICATE_NAME` | 409 | Project/area/tag title or API token name already exists |
| `HAS_PROJECTS` | 409 | Area still contains projects |
| `CONFLICT` | 409 | Client-supplied `id` already stored under another parent task (child-entity creates) |
| `CURSOR_EXPIRED` | 410 | Sync cursor purged; perform a full sync |
| `BODY_TOO_LARGE` | 413 | Request body over the size cap |
| `LIMIT_REACHED` | 422 | Per-view saved filter cap reached |
| `RATE_LIMITED` | 429 | Too many failed login attempts |
| `FILE_TOO_LARGE` | 400 | Uploaded file over `MAX_UPLOAD_SIZE` |
| `NOT_CONFIGURED` | 400 | Notification provider not configured |
| `INTERNAL` | 5xx | Generic server error; details only in server logs |

### Request Body Limit
JSON request bodies are capped at 10 MiB (10485760 bytes), regardless of `Content-Type`. Larger bodies are rejected before decoding with **413 Payload Too Large**:

```json
{ "error": "request body too large", "code": "BODY_TOO_LARGE" }
```

Exception: the route `POST /api/tasks/:id/attachments` is exempt from this middleware-level cap regardless of `Content-Type` (the exemption is keyed on route and method, not Content-Type). Multipart uploads there are capped by `MAX_UPLOAD_SIZE` instead (see below); JSON link-creates on that route remain capped at 10 MiB by the handler itself and return 413 `BODY_TOO_LARGE` beyond that.

### Duplicate Name Errors
`POST` and `PATCH` for projects, areas, and tags return **409 Conflict** with code `DUPLICATE_NAME` if the title already exists, as does `POST /api/tokens` for a duplicate token name. A retried create that carries the `id` of an already-stored entity is not a duplicate-name error — it replays 200 (see Idempotent Creates).

### Idempotent Creates
Every create endpoint that accepts a body `id` — tasks, projects, areas, tags, headings, checklist items, schedules, reminders and link attachments — is idempotent by that id. Validation runs first; if it passes and the `id` already exists in the target table, the endpoint returns **200** with the stored entity (the same shape a GET of that entity returns). Nothing is modified on the replay: no row is written, no SSE event is broadcast and no change-log entry is recorded, so retried creates from agents, widgets or shortcuts never duplicate. A body `id` that does not exist yet is created normally with **201**.

For the four task children (checklist items, schedules, reminders and link attachments) the replay must also target the same task: a stored `id` posted under a different task in the URL returns **409 Conflict** with code `CONFLICT` instead of the stored row.

For schedules, the replay check runs after the field validation but before the per-task entry limits (max 12 entries, only-first-`someday`, duplicate timeless date): a retried create of an entry that already exists replays 200 even when the task is now at those limits.

`POST /api/tokens` is the exception: token creates are **not** idempotent. A body `id` is ignored, every call mints a new token, and the server always generates its own id.

### Pagination (where applicable)
Query params: `?limit=50&offset=0`

### Sort Order
All entities with `sort_order` use REAL (float) for fractional positioning. New items default to max(sort_order) + 1024. Insert-between uses (before + after) / 2.

### IDs
All entity IDs are nanoid strings (8-10 chars). Task IDs double as permalink slugs.

### Timestamps and Timezones
All timestamps (`created_at`, `updated_at`, `completed_at`, `canceled_at`, `deleted_at`, reminder `exact_at`) are stored and returned as UTC. Calendar-day boundaries are computed in the server timezone set by the `TZ` environment variable: "Today" (including This Evening and the Completed-today section), Logbook and Trash date grouping, and the review staleness cutoff all use the local calendar date of that zone, with stored UTC timestamps compared against the UTC instants of local midnight. `when_date`, `deadline` and schedule dates are plain local calendar dates and are matched literally.

### Validation Rules
Mutation endpoints validate request fields at the API boundary, before any write reaches the database. Every validation error uses the error envelope above with code `VALIDATION` and the offending `field`:

- **Malformed values → 400** `VALIDATION`: dates that are not real `YYYY-MM-DD` calendar dates (`2026-02-31` is rejected), `when_date` values that are neither a date nor `someday`, times not in `HH:MM` 24-hour form (`9:00`, `24:00`), client-supplied `id` values outside `^[A-Za-z0-9_-]{8,21}$`, empty titles on create/update, and `end_time` earlier than or equal to `start_time`.
- **Reminder, repeat, settings and status fields → 400** `VALIDATION`: reminder `type` outside the documented set, reminder `value` outside 0-99, `exact` reminders without a parseable `exact_at`; repeat patterns with an unknown `type`, `every` below 1, an unknown `mode`, or invalid weekday/ordinal/day/month values; user settings fields outside their documented enums and ranges (`sort_areas`, `sort_tags`, `evening_starts_at`, `time_format`, `font_size`, `default_reminder_type`, `default_reminder_value`, `notification_provider`); project `status` outside `open|completed|canceled`.
- **`exact_at` normalization**: reminder `exact_at` accepts RFC 3339 with a numeric offset or `Z`, or a naive `YYYY-MM-DDTHH:MM[:SS]` timestamp read in the server timezone, and is stored and returned as UTC RFC 3339 (`2026-10-20T09:00:00+03:00` → `2026-10-20T06:00:00Z`).
- **Referenced ids that do not exist → 422** `VALIDATION`: `project_id`, `area_id`, `heading_id`, `tag_ids`, `parent_tag_id` and `task_id` (URL or body) must reference an existing row. A task create with both `project_id` and `heading_id` requires the heading to belong to that project.
- Client ids apply to every create endpoint that accepts a body `id`: tasks, projects, areas, tags, headings, checklist items, schedules, reminders, link attachments, and sync creates.
- Bulk `set_when`/`set_deadline` validate their date params the same way (an empty string clears the date and stays allowed); `PATCH /api/tasks/:id/move` validates `when_date` and its reference params.
- `deadline >= when_date` is enforced on task create and update (including cross-field updates resolved against the stored task).

**Sync** (`POST /api/sync/push`) applies the same rules per change: a violation produces a result with `status: "error"`, `error: "<field>: <message>"` and a machine-readable `code`, and the rest of the batch continues.

---

## Authentication

### API Key (builtin mode)

Set the `API_KEY` environment variable to a secret string. Then pass it via the `Authorization` header:

```
Authorization: Bearer <your-api-key>
```

The API key authenticates as the first (admin) user in the database. This is useful for programmatic access (e.g., iOS Shortcuts, scripts) where cookie-based auth isn't practical.

If the header is present but the key is wrong, the request is rejected with 401 immediately (no fallback to cookie auth).

### Named API Tokens (all modes except `none`)

Named tokens created via `POST /api/tokens` (see [API Tokens](#api-tokens)) authenticate the same way as the master key:

```
Authorization: Bearer ttd_<token>
```

Precedence: the master `API_KEY` is checked first, then the session (cookie or proxy header), then named tokens. A valid session wins over a presented Bearer token; a Bearer value that matches nothing (wrong master key, unknown or revoked token) rejects the request with 401 immediately, with no cookie fallback. In `none` mode Bearer headers are ignored entirely. A named token authenticates as the first (admin) user, updates its `last_used_at` (at most one write per minute per token) and cannot call the `/api/tokens` management endpoints (403 `FORBIDDEN`).

### CORS (all modes except `none`)

The `/api` and `/mcp` endpoints send these headers on every response:

```
Access-Control-Allow-Origin: *
Access-Control-Allow-Methods: GET, POST, PUT, PATCH, DELETE, OPTIONS
Access-Control-Allow-Headers: Authorization, Content-Type, Mcp-Session-Id, Mcp-Protocol-Version, Last-Event-ID
Access-Control-Expose-Headers: Mcp-Session-Id
Access-Control-Max-Age: 600
```

`Access-Control-Allow-Credentials` is never sent, so cross-origin scripts cannot read responses to cookie-authenticated requests. Preflight `OPTIONS` requests are answered with **204** before authentication runs. Widgets on any origin therefore work with a Bearer token (master key or named token).

CORS is **off** in `AUTH_MODE=none`: that mode has no auth, and a wildcard policy would let any website read and write the data.

**Cross-origin session guard.** A request that would authenticate as a session (cookie or proxy header) and carries an `Origin` header naming a different host than the request `Host` is rejected with **403 Forbidden** (`FORBIDDEN`), regardless of method — CORS alone cannot stop a browser from attaching a `SameSite=Lax` cookie to a simple `text/plain` POST, which needs no preflight, and a cross-origin session GET leaks data too. Browsers always attach `Origin` to cross-origin requests, so genuine cross-origin browser requests cannot slip past the guard by omitting it. Requests without `Origin` (CLI, curl, agents) and same-origin requests (the web UI) are unaffected, and Bearer-authenticated requests are exempt. Default ports (80 for `http`, 443 for `https`) are stripped on both sides before comparing, so `https://app.example:443` matches `Host: app.example`.

### GET /api/auth/config
**Mode: all** (public, no authentication required)

Returns the current auth mode so the login page can render the appropriate UI, plus the server's timezone so clients compute "today" in the owner's zone. `timezone` is the IANA name of the server's zone, resolved from the `TZ` env var, else the `/etc/localtime` symlink, else `UTC`.

Response (200):
```json
{ "auth_mode": "builtin|proxy|oidc|none", "timezone": "Europe/Moscow" }
```

### POST /api/auth/login
**Mode: builtin only** (returns 404 in proxy/oidc modes)

Request:
```json
{ "username": "string", "password": "string" }
```

Response (200): Sets httpOnly JWT cookie
```json
{ "user": { "id": "string", "username": "string" } }
```

Failed logins are rate-limited per client IP: after 10 consecutive failures within a 15-minute window, all further login attempts from that IP are rejected until the window expires.

Response (429):
```json
{ "error": "too many failed login attempts", "code": "RATE_LIMITED" }
```

### DELETE /api/auth/logout
**Mode: builtin, oidc**

Response (200): Clears JWT cookie (local logout only, no IdP redirect)
```json
{ "ok": true }
```

### GET /api/auth/me
Response (200):
```json
{
  "user": { "id": "string", "username": "string" },
  "auth_mode": "builtin|proxy|oidc|none"
}
```

### GET /api/auth/oidc/login
**Mode: oidc only**

Redirects the browser to the configured OIDC provider's authorization endpoint. Sets a signed `oidc_state` cookie for CSRF protection. Not a JSON API — browser navigation only.

### GET /api/auth/oidc/callback
**Mode: oidc only**

OIDC redirect URI. Verifies state, exchanges authorization code for tokens, and only accepts the email configured in `OIDC_ALLOWED_EMAIL` when the provider asserts it is verified (`email_verified: true`, taken from the ID token, or from userinfo when the ID token carries no email and its `sub` matches the ID token's). Provisions/links the user on first login and issues the app JWT cookie. Redirects to `/inbox` on success.

Returns `403` `{"error":"account not allowed","code":"FORBIDDEN"}` when the email is not the configured allowed email, is not asserted as verified, or userinfo does not match the ID token subject.

Required env vars: `OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`, `OIDC_REDIRECT_URI`, `OIDC_ALLOWED_EMAIL`
```

---

## API Tokens

Named, revocable API tokens for programmatic access. A token value is `ttd_` followed by 43 base64url characters of crypto-random entropy (32 bytes); only its SHA-256 hash is stored, and the plaintext is returned exactly once — at creation. Token names are unique.

All three endpoints reject requests that authenticated with a named API token: **403** `{"error":"token authentication cannot manage tokens","code":"FORBIDDEN"}`. Authenticate with a session or the master `API_KEY`.

### GET /api/tokens
**Auth: session or API key** (403 with a named token)

Response (200):
```json
{
  "tokens": [
    {
      "id": "string",
      "name": "string",
      "prefix": "first 8 characters of the token value",
      "created_at": "timestamp",
      "last_used_at": "timestamp or null (updated at most once per minute per token)",
      "revoked_at": "timestamp or null"
    }
  ]
}
```

The hash and the token value are never returned after creation.

### POST /api/tokens
**Auth: session or API key** (403 with a named token)

Request:
```json
{ "name": "string" }
```

Creates are **not** idempotent: a body `id` is ignored — the server always generates its own id, and every POST mints a new token.

Response (201):
```json
{ "id": "string", "name": "string", "prefix": "string", "token": "ttd_..." }
```

`token` is the only time the plaintext value is ever returned; it cannot be shown again. Errors: 409 `DUPLICATE_NAME` if the name already exists; 400 `VALIDATION` (`field: "name"`) for a missing name.

### DELETE /api/tokens/:id
**Auth: session or API key** (403 with a named token)

Marks the token revoked. Response: **204 No Content**. Revoking an already-revoked token is a no-op (204); an unknown id returns 404 `NOT_FOUND`. A revoked token stops authenticating immediately (Lookup skips revoked rows).

---

## Tasks

### GET /api/tasks
Query params (all optional, combined with AND). Unknown params or invalid
values get 400 `VALIDATION` with `field`.

| Param | Meaning |
|---|---|
| `status` | Comma list of `open,completed,canceled,wont_do`. Default: every status. Applies independently of `list`. |
| `list` | `inbox`, `today`, `upcoming`, `anytime`, `someday`, `logbook`, `trash`. A task matches when it appears in that view (same predicates as `/api/views/*`). Every query excludes trashed tasks (`deleted_at IS NULL`) except `list=trash`, which selects only them (`deleted_at IS NOT NULL`), including tasks with `status=open`. `today` is resolved in the server timezone. |
| `project_id`, `area_id`, `heading_id` | Exact id match. `area_id` matches the stored `tasks.area_id`, which is kept equal to the project's area. |
| `tag_ids` | Comma list of tag ids. With `tag_mode=any` (default) a task matching any tag returns; with `tag_mode=all` it must carry every listed tag. |
| `when_date`, `when_before`, `when_after` | `when_date` is `YYYY-MM-DD` or `someday`; the bounds are dates, `someday` never matches a bound. |
| `deadline_before`, `deadline_after` | Inclusive date bounds. |
| `has_deadline`, `has_when`, `high_priority` | `true` or `false`. `has_when` counts `someday` as a when date. |
| `updated_since`, `created_since` | Inclusive UTC timestamp: `YYYY-MM-DD HH:MM:SS` or RFC 3339 (normalized to UTC). |
| `search` | FTS over title and notes; each whitespace token is quoted as an FTS5 string (punctuation is literal, tokens are ANDed) with **no** prefix matching. See `/api/search`. |
| `sort` | `manual` (default), `when_date`, `deadline`, `created_at`, `updated_at`, `title`; prefix `-` for descending. `manual` orders by `sort_order_heading` when `heading_id` is given, `sort_order_project` when `project_id` is given, otherwise `sort_order_today`. NULL `when_date`/`deadline` keys sort first ascending and last descending. Every sort tie-breaks by `id` ascending. |
| `limit` | 1–500. Pagination is opt-in: without `limit`, all matching tasks return (existing callers such as the CLI keep working). An explicit `limit=0` is a 400. |
| `cursor` | Opaque string from the previous response's `next_cursor`; requires the same filters and sort. |

Response (200):
```json
{
  "tasks": [
    {
      "id": "string",
      "title": "string",
      "notes": "string",
      "status": "open|completed|canceled|wont_do",
      "when_date": "string|null",
      "high_priority": false,
      "deadline": "string|null",
      "project_id": "string|null",
      "project_name": "string|null",
      "area_id": "string|null",
      "area_name": "string|null",
      "heading_id": "string|null",
      "sort_order_today": 0.0,
      "sort_order_project": 0.0,
      "sort_order_heading": 0.0,
      "completed_at": "string|null",
      "canceled_at": "string|null",
      "deleted_at": "string|null",
      "created_at": "string",
      "updated_at": "string",
      "tags": [{ "id": "string", "title": "string", "color": "string|null" }],
      "checklist_count": 0,
      "checklist_done": 0,
      "has_notes": false,
      "has_links": false,
      "has_files": false,
      "has_repeat_rule": false,
      "first_schedule_time": "string|null",
      "first_schedule_end_time": "string|null"
    }
  ],
  "next_cursor": "string (present only when limit was given and more rows exist; omitted otherwise)"
}
```

`project_name` and `area_name` are always filled (null when the task has no
project or area).

### POST /api/tasks
Request:
```json
{
  "id": "string (optional, client-supplied; a known id replays 200 — see Idempotent Creates)",
  "title": "string (required)",
  "notes": "string",
  "when_date": "string|null",
  "high_priority": false,
  "deadline": "string|null",
  "project_id": "string|null",
  "area_id": "string|null",
  "heading_id": "string|null",
  "tag_ids": ["string"]
}
```

When the user settings carry a `default_reminder_type`, that reminder is added atomically with the task.

Response (201): the full task detail — the same shape as `GET /api/tasks/:id`, with nested checklist, attachments, tags, repeat_rule and schedules (not the list-item shape).

### GET /api/tasks/:id
Response (200): Full task object with nested checklist, attachments, tags, repeat_rule
```json
{
  "id": "string",
  "title": "string",
  "notes": "string",
  "status": "open|completed|canceled|wont_do",
  "when_date": "string|null",
  "high_priority": false,
  "deadline": "string|null",
  "project_id": "string|null",
  "project": { "id": "string", "title": "string" },
  "area_id": "string|null",
  "area": { "id": "string", "title": "string" },
  "heading_id": "string|null",
  "heading": { "id": "string", "title": "string" },
  "sort_order_today": 0.0,
  "sort_order_project": 0.0,
  "sort_order_heading": 0.0,
  "completed_at": "string|null",
  "canceled_at": "string|null",
  "deleted_at": "string|null",
  "created_at": "string",
  "updated_at": "string",
  "tags": [{ "id": "string", "title": "string", "color": "string|null" }],
  "checklist": [
    {
      "id": "string",
      "title": "string",
      "completed": false,
      "sort_order": 0.0
    }
  ],
  "attachments": [
    {
      "id": "string",
      "type": "file|link",
      "title": "string",
      "url": "string",
      "mime_type": "string",
      "file_size": 0,
      "sort_order": 0.0,
      "created_at": "string"
    }
  ],
  "repeat_rule": {
    "id": "string",
    "frequency": "daily|weekly|monthly|yearly",
    "interval_value": 1,
    "mode": "fixed|after_completion",
    "day_constraints": ["mon", "wed", "fri"]
  },
  "schedules": [
    {
      "id": "string",
      "task_id": "string",
      "when_date": "string",
      "start_time": "string|null",
      "end_time": "string|null",
      "sort_order": 0.0,
      "created_at": "string"
    }
  ]
}
```

### PATCH /api/tasks/:id
Request: Partial update (any subset of task fields; absent keys are untouched, explicit null clears)
```json
{
  "title": "string",
  "notes": "string",
  "when_date": "string|null",
  "high_priority": false,
  "deadline": "string|null",
  "project_id": "string|null",
  "area_id": "string|null",
  "heading_id": "string|null",
  "tag_ids": ["string"]
}
```

Container derivation (applies whenever the request includes `project_id`, `area_id` or `heading_id`): a task in a project stores that project's area as `area_id` — a sent `area_id` is only honored on project-less tasks; and when `project_id` changes without a `heading_id`, a heading that does not belong to the new project is cleared. A sent `heading_id` must belong to the effective project — the sent one, or the task's stored project when none is sent — otherwise 422 VALIDATION on `heading_id` (same as create).

Response (200): Updated full task object

### DELETE /api/tasks/:id
Soft delete (moves to trash).

Response (204): No content

### DELETE /api/tasks/:id/purge
Permanently deletes the task.

Response (204): No content

### PATCH /api/tasks/:id/complete
Finishes an open task in one guarded, transactional step: sets status=completed with completed_at, completes past and today's uncompleted schedule entries, deletes future (after today) and someday uncompleted entries, and — when a repeat rule exists — creates the next instance and moves the rule to it. Only a task with status=open transitions; a repeated call is an idempotent success (200 with the current task, nothing spawned).
Response (200): Updated task with status=completed, completed_at set

### PATCH /api/tasks/:id/cancel
Same guarded finishing path as complete (status=canceled, canceled_at set, schedule cleanup and repeat-instance creation are atomic with the status change).
Response (200): Updated task with status=canceled, canceled_at set

### PATCH /api/tasks/:id/wontdo
Same guarded finishing path as complete (status=wont_do, schedule cleanup and repeat-instance creation are atomic with the status change).
Response (200): Updated task with status=wont_do

### PATCH /api/tasks/:id/reopen
Response (200): Updated task with status=open

### PATCH /api/tasks/:id/restore
Restores a soft-deleted task from trash.

Response (200): Updated task with deleted_at cleared

### PATCH /api/tasks/:id/review
Marks a task as reviewed by bumping its `updated_at` timestamp. Used by the review feature to dismiss tasks from the review queue.

Response (200): Updated task

### PATCH /api/tasks/:id/move
Move task to a different project, area, heading, or date. Precise: only keys present in the body are applied — absent keys keep their stored values, explicit null clears.

Request:
```json
{
  "project_id": "string|null",
  "area_id": "string|null",
  "heading_id": "string|null",
  "when_date": "string|null"
}
```

- `area_id` follows the resolved project (a sent `area_id` only applies to project-less tasks).
- When `project_id` changes and no `heading_id` is sent, a heading that does not belong to the new project is cleared.
- A sent `heading_id` must belong to the effective project (the sent one, or the task's stored one when none is sent); otherwise 422 VALIDATION on `heading_id`.
- A sent `when_date` also syncs the first schedule entry (same as PATCH).

Response (200): Updated task

### PATCH /api/tasks/reorder
Request:
```json
{
  "items": [
    { "id": "string", "sort_field": "sort_order_today|sort_order_project|sort_order_heading", "sort_order": 0.0 }
  ]
}
```

Response (200):
```json
{ "ok": true }
```

### POST /api/tasks/bulk
Perform an action on multiple tasks in a single transaction.

Request:
```json
{
  "task_ids": ["string"],
  "action": "complete|cancel|wontdo|delete|set_when|set_deadline|set_priority|toggle_priority|move_project|add_tags|remove_tags|toggle_tags|mark_reviewed",
  "params": {}
}
```

| Action | Params |
|--------|--------|
| set_when | `when_date` (string, empty = clear) |
| set_deadline | `deadline` (string) |
| set_priority | `high_priority` (boolean) or legacy `priority` (number, 1 = high); `high_priority` wins when both are sent |
| toggle_priority | _(none)_ — flips each task's `high_priority` |
| move_project | `project_id` (string or null), `area_id` (string or null, empty = null); the area is derived from the target project and a heading not in it is cleared |
| add_tags | `tag_ids` (string[]) |
| remove_tags | `tag_ids` (string[]) |
| toggle_tags | `tag_ids` (string[]) — adds the tag where absent, removes it where present |
| mark_reviewed | _(none)_ — bumps `updated_at` to now |

Limits: max 100 tasks per request.

Response (200):
```json
{ "ok": true, "affected": 3 }
```

The status actions `complete`, `cancel` and `wontdo` run the one guarded finishing path per task (same as the single-task endpoints). `affected` counts only tasks that actually transitioned from open; already-finished and unknown ids do not count. For every other action, `affected` counts one per task a statement of the action actually changed; unknown ids contribute 0.

`set_when` runs the same first-schedule sync as `PATCH /api/tasks/:id`: each task's first schedule entry moves to the new date, and an empty `when_date` nulls `tasks.when_date` and removes the task's schedule entries. The whole action is one transaction — when any task's move would create a duplicate timeless schedule, the request fails with 400 `VALIDATION` and nothing is applied.

SSE: Broadcasts `bulk_change` event with `{ type, entity: "task", ids }`.

---

## Checklist Items

### GET /api/tasks/:id/checklist
Response (200):
```json
{
  "items": [
    { "id": "string", "title": "string", "completed": false, "sort_order": 0.0 }
  ]
}
```

### POST /api/tasks/:id/checklist
Request:
```json
{ "title": "string (required)" }
```

Response (201): Checklist item object

### PATCH /api/checklist/:id
Request:
```json
{ "title": "string", "completed": true, "sort_order": 0.0 }
```

Response (200): Updated checklist item

### DELETE /api/checklist/:id
Response (204): No content

---

## Attachments

### GET /api/tasks/:id/attachments
Response (200):
```json
{
  "attachments": [
    {
      "id": "string",
      "type": "file|link",
      "title": "string",
      "url": "string",
      "mime_type": "string",
      "file_size": 0,
      "sort_order": 0.0,
      "created_at": "string"
    }
  ]
}
```

### POST /api/tasks/:id/attachments
**For files:** multipart/form-data with `file` field (capped by `MAX_UPLOAD_SIZE`, default 25 MiB). This is the only way to create `file` attachments.
**For links:** JSON accepts only `link` attachments (`type` optional, defaults to `"link"`); any other `type` is rejected with 400 `VALIDATION`:
```json
{ "type": "link", "title": "string", "url": "string" }
```
The URL is required and trimmed; `javascript:`, `data:`, `vbscript:` and `file:` schemes (case-insensitive) are rejected with 400 `VALIDATION`. The same rules apply to attachment creates via `POST /api/sync/push` (violations yield a per-change `error` result).
(JSON bodies on this route are capped at 10 MiB and return 413 `BODY_TOO_LARGE` beyond that.)

Response (201): Attachment object

### PATCH /api/attachments/:id
Request:
```json
{ "title": "string", "sort_order": 0.0 }
```

Response (200): Updated attachment

### DELETE /api/attachments/:id
Deletes exactly the attachment row with the given id. Recurring instances copy file attachments as rows sharing one stored file; the stored file is removed only when no other attachment row still references it.

Response (204): No content
Response (404): Unknown attachment id (`NOT_FOUND`)

### GET /api/attachments/:id/file
Response: Binary file stream with appropriate Content-Type

---

## Reminders

### GET /api/tasks/:id/reminders
Response (200):
```json
{
  "items": [
    {
      "id": "string",
      "task_id": "string",
      "type": "at_start|on_day|minutes_before|hours_before|days_before|exact",
      "value": 0,
      "exact_at": "2025-03-15T14:30:00Z or null",
      "created_at": "datetime"
    }
  ]
}
```

Type+value encoding:
| type | value | exact_at | meaning |
|------|-------|----------|---------|
| `at_start` | 0 | null | At schedule `start_time`, or 09:00 server-local when the schedule has no start time |
| `on_day` | 0 | null | 09:00 server-local on `when_date` |
| `minutes_before` | 0-99 | null | N minutes before schedule `start_time` (09:00 server-local fallback) |
| `hours_before` | 0-99 | null | N hours before schedule `start_time` (09:00 server-local fallback) |
| `days_before` | 0-99 | null | N days before `when_date` at 09:00 server-local |
| `exact` | 0 | ISO datetime | Fires at the exact specified date/time |

### POST /api/tasks/:id/reminders
Request:
```json
{
  "type": "at_start|on_day|minutes_before|hours_before|days_before|exact",
  "value": 15,
  "exact_at": "optional, for exact type only"
}
```
Response (201): Created reminder object

Errors (400 `VALIDATION` with `field`): unknown `type`; `value` outside 0-99; `exact` without `exact_at`; unparseable `exact_at` (see the normalization rule above — `exact_at` is stored and returned as UTC RFC 3339).

### DELETE /api/reminders/:id
Response (204): No content

---

## Push Subscriptions

### GET /api/push/vapid-key
Response (200):
```json
{ "public_key": "base64url-encoded VAPID public key" }
```

### POST /api/push/subscribe
Request:
```json
{
  "endpoint": "https://push-service.example.com/...",
  "p256dh": "base64url key",
  "auth": "base64url key"
}
```
Response (201): Created subscription object

### DELETE /api/push/subscribe
Request:
```json
{
  "endpoint": "https://push-service.example.com/..."
}
```
Response (204): No content

### POST /api/push/test
Sends a test notification using the user's currently configured provider (webpush or ntfy).

Request: `{}` (empty body)

Response (200):
```json
{ "ok": true }
```
Response (400): If no notification provider is configured

---

## Projects

### GET /api/projects
Query params: `area_id`, `status` (open|completed|canceled)

Response (200):
```json
{
  "projects": [
    {
      "id": "string",
      "title": "string",
      "notes": "string",
      "area_id": "string (required)",
      "area": { "id": "string", "title": "string" },
      "status": "open|completed|canceled",
      "when_date": "string|null",
      "deadline": "string|null",
      "sort_order": 0.0,
      "task_count": 0,
      "completed_task_count": 0,
      "tags": [{ "id": "string", "title": "string", "color": "string|null" }],
      "created_at": "string",
      "updated_at": "string"
    }
  ]
}
```

### POST /api/projects
Request:
```json
{
  "title": "string (required)",
  "notes": "string",
  "area_id": "string (required)",
  "when_date": "string|null",
  "deadline": "string|null",
  "tag_ids": ["string"]
}
```

Response (201): Full project object

### GET /api/projects/:id
Response (200): Project with nested headings and tasks
```json
{
  "id": "string",
  "title": "string",
  "notes": "string",
  "area_id": "string (required)",
  "area": { "id": "string", "title": "string" },
  "status": "open|completed|canceled",
  "when_date": "string|null",
  "deadline": "string|null",
  "sort_order": 0.0,
  "task_count": 0,
  "completed_task_count": 0,
  "tags": [{ "id": "string", "title": "string", "color": "string|null" }],
  "created_at": "string",
  "updated_at": "string",
  "headings": [
    {
      "id": "string",
      "title": "string",
      "sort_order": 0.0,
      "tasks": [/* task objects */]
    }
  ],
  "tasks_without_heading": [/* task objects */],
  "completed_tasks": [/* completed/canceled/wont_do task objects */]
}
```

### PATCH /api/projects/:id
Request: Partial update (`title`, `notes`, `area_id`, `status`, `when_date`, `deadline`, `tag_ids`)
Response (200): Updated project

Errors (400 `VALIDATION` with `field`): `status` outside `open|completed|canceled` (date, id and title rules are shared with other endpoints).

### DELETE /api/projects/:id

Deletes the project in one transaction and sends its child tasks to Trash instead of destroying them: each task gets `deleted_at` set (status unchanged) and `project_id`, `heading_id`, `area_id` cleared, so restoring it yields a loose open task. A task change-log entry is written per trashed child (plus one per heading and one for the project) so sync clients see the soft-deletes. Unknown ids are an idempotent success.

Response (204): No content

### PATCH /api/projects/:id/complete
Response (200): Updated project with status=completed

### PATCH /api/projects/reorder
Request:
```json
{
  "items": [
    { "id": "string", "sort_order": 0.0 }
  ]
}
```

Response (200):
```json
{ "ok": true }
```

---

## Headings

### GET /api/projects/:id/headings
Response (200):
```json
{
  "headings": [
    { "id": "string", "title": "string", "project_id": "string", "sort_order": 0.0 }
  ]
}
```

### POST /api/projects/:id/headings
Request:
```json
{ "title": "string (required)" }
```

Response (201): Heading object

### PATCH /api/headings/:id
Request:
```json
{ "title": "string", "sort_order": 0.0 }
```

Response (200): Updated heading

### DELETE /api/headings/:id
Response (204): No content

### PATCH /api/headings/reorder
Request:
```json
{
  "items": [
    { "id": "string", "sort_order": 0.0 }
  ]
}
```

Response (200):
```json
{ "ok": true }
```

---

## Areas

### GET /api/areas
Response (200):
```json
{
  "areas": [
    {
      "id": "string",
      "title": "string",
      "sort_order": 0.0,
      "project_count": 0,
      "task_count": 0,
      "standalone_task_count": 0,
      "created_at": "string",
      "updated_at": "string"
    }
  ]
}
```

### POST /api/areas
Request:
```json
{ "title": "string (required)" }
```

Response (201): Full area detail (same shape as GET /api/areas/:id; a new area has empty `projects`, `tasks` and `completed_tasks` collections)

### GET /api/areas/:id
Response (200): Area with nested projects and tasks
```json
{
  "id": "string",
  "title": "string",
  "sort_order": 0.0,
  "created_at": "string",
  "updated_at": "string",
  "projects": [/* project objects */],
  "tasks": [/* standalone tasks in this area (no project) */],
  "completed_tasks": [/* completed standalone tasks */]
}
```

### PATCH /api/areas/:id
Request:
```json
{ "title": "string", "sort_order": 0.0 }
```

Response (200): Updated area

### PATCH /api/areas/reorder
Request:
```json
{
  "items": [
    { "id": "string", "sort_order": 0.0 }
  ]
}
```

Response (200):
```json
{ "ok": true }
```

### DELETE /api/areas/:id

Deletes the area in one transaction, but only while it holds no projects; its standalone tasks (no project) are sent to Trash instead of being destroyed: each gets `deleted_at` set (status unchanged) and `area_id` cleared, so restoring it yields a loose open task. A task change-log entry is written per trashed task (plus one for the area). Unknown ids are an idempotent success.

Response (204): No content

Response (409): Area still has projects
```json
{ "error": "area still has projects", "code": "HAS_PROJECTS" }
```

---

## Tags

### GET /api/tags
Response (200):
```json
{
  "tags": [
    {
      "id": "string",
      "title": "string",
      "color": "string|null",
      "parent_tag_id": "string|null",
      "sort_order": 0.0,
      "task_count": 0
    }
  ]
}
```

### POST /api/tags
Request:
```json
{ "title": "string (required)", "parent_tag_id": "string|null" }
```

Response (201): Tag object

### PATCH /api/tags/:id
Request:
```json
{ "title": "string", "color": "string|null", "parent_tag_id": "string|null", "sort_order": 0.0 }
```

Response (200): Updated tag

### DELETE /api/tags/:id
Response (204): No content

### PATCH /api/tags/reorder
Request:
```json
{
  "items": [
    { "id": "string", "sort_order": 0.0 }
  ]
}
```

Response (200):
```json
{ "ok": true }
```

### GET /api/tags/:id/tasks
Response (200): Tasks with this tag
```json
{ "tasks": [/* task objects */] }
```

---

## Repeat Rules

### GET /api/tasks/:id/repeat
Response (200):
```json
{
  "repeat_rule": {
    "id": "string",
    "pattern": { /* RecurrencePattern, see below */ },
    "frequency": "daily",
    "interval_value": 1,
    "mode": "fixed",
    "day_constraints": []
  }
}
```
Response (200, no rule): `{ "repeat_rule": null }`

The `pattern` field is the canonical recurrence definition. The flat fields (`frequency`, `interval_value`, `mode`, `day_constraints`) are deprecated but still populated for backwards compatibility.

### PUT /api/tasks/:id/repeat
Request (new pattern format — preferred):
```json
{
  "pattern": {
    "type": "weekly",
    "every": 2,
    "mode": "fixed",
    "on": ["mon", "wed", "fri"]
  }
}
```

Request (legacy format — still accepted):
```json
{
  "frequency": "daily|weekly|monthly|yearly",
  "interval_value": 1,
  "mode": "fixed|after_completion",
  "day_constraints": ["mon", "wed", "fri"]
}
```

Response (200): Repeat rule object

Errors (400 `VALIDATION` with `field`): unknown pattern `type`; `every` below 1; `mode` outside `fixed|after_completion`; weekly `on` values outside the DayOfWeek set; `monthly_dom` `day` outside -30..31; invalid `ordinal` or `weekday`; `monthly_workday` `workday_position` outside `first|last`; `month` outside 1-12.

### DELETE /api/tasks/:id/repeat
Response (204): No content

### RecurrencePattern Types

All patterns share: `type` (string), `every` (int, interval), `mode` ("fixed" | "after_completion")

| Type | Extra Fields | Example |
|------|-------------|---------|
| `daily` | — | `{"type":"daily","every":3,"mode":"fixed"}` |
| `daily_weekday` | — | `{"type":"daily_weekday","every":1,"mode":"fixed"}` |
| `daily_weekend` | — | `{"type":"daily_weekend","every":1,"mode":"fixed"}` |
| `weekly` | `on`: DayOfWeek[] | `{"type":"weekly","every":1,"mode":"fixed","on":["mon","wed"]}` |
| `monthly_dom` | `day`: int (1-31, 0=last, negative=last-N, null=use when_date) | `{"type":"monthly_dom","every":1,"mode":"fixed","day":15}` |
| `monthly_dow` | `ordinal`: string, `weekday`: string | `{"type":"monthly_dow","every":1,"mode":"fixed","ordinal":"first","weekday":"monday"}` |
| `monthly_workday` | `workday_position`: "first"\|"last" | `{"type":"monthly_workday","every":1,"mode":"fixed","workday_position":"first"}` |
| `yearly_date` | `month`: 1-12, `day`: 1-31 | `{"type":"yearly_date","every":1,"mode":"fixed","month":3,"day":15}` |
| `yearly_dow` | `month`: 1-12, `ordinal`: string, `weekday`: string | `{"type":"yearly_dow","every":1,"mode":"fixed","month":11,"ordinal":"fourth","weekday":"thursday"}` |

**DayOfWeek values**: mon, tue, wed, thu, fri, sat, sun
**Ordinal values**: first, second, third, fourth, last
**Weekday values** (full): monday, tuesday, wednesday, thursday, friday, saturday, sunday

---

## Schedules

Multi-date scheduling entries for a task. Each task can have up to 12 schedule entries. The first entry's `when_date` is synced to `tasks.when_date` as a denormalized cache. Only the first entry can be set to `'someday'`.

### GET /api/tasks/:id/schedules
Response (200):
```json
{
  "items": [
    {
      "id": "string",
      "task_id": "string",
      "when_date": "string",
      "start_time": "string|null",
      "end_time": "string|null",
      "sort_order": 0.0,
      "created_at": "string"
    }
  ]
}
```

### POST /api/tasks/:id/schedules
Request:
```json
{
  "when_date": "string (required)",
  "start_time": "string|null (HH:MM 24h)",
  "end_time": "string|null (HH:MM 24h, required when start_time is set)"
}
```

Response (201): Schedule entry object

Validation:
- Maximum 12 schedule entries per task
- Only the first entry can have `when_date = 'someday'`
- `start_time` requires `end_time`
- `when_date` must be `YYYY-MM-DD` or `someday` (400 `when_date`); `start_time`/`end_time` must be `HH:MM` 24h (400 `start_time`/`end_time`); `end_time` must be after `start_time` (400 `end_time`)
- Unknown `task_id` → 422 `VALIDATION` `task_id`
- A create whose `id` already exists replays 200 before the entry limits above are applied (see Idempotent Creates); a replay under a different task in the URL returns 409 `CONFLICT`

SSE: broadcasts `task_updated`

### PATCH /api/schedules/:id
Request: Partial update
```json
{
  "when_date": "string",
  "start_time": "string|null",
  "end_time": "string|null",
  "sort_order": 0.0
}
```

Response (200): Updated schedule entry

SSE: broadcasts `task_updated`

### DELETE /api/schedules/:id
Response (204): No content

SSE: broadcasts `task_updated`

### PATCH /api/tasks/:id/schedules/reorder
Request (array of items):
```json
[
  { "id": "string", "sort_order": 0.0 }
]
```

Response (204): No content

Every `id` must be a schedule entry of the task in the URL. A foreign or unknown id rejects the whole request with 400 `VALIDATION` (field `items`) before anything is applied.

SSE: broadcasts `task_updated`

---

## User Settings

### GET /api/user/settings
Response (200):
```json
{
  "play_complete_sound": true,
  "show_count_main": true,
  "show_count_projects": true,
  "show_count_tags": true,
  "review_after_days": 7,
  "sort_areas": "manual",
  "sort_tags": "manual",
  "evening_starts_at": "18:00",
  "default_time_gap": 60,
  "show_time_badge": true,
  "time_format": "12h",
  "font_size": 16,
  "default_reminder_type": "minutes_before",
  "default_reminder_value": 15,
  "copy_reminders_to_recurring": true,
  "notification_provider": "webpush",
  "ntfy_server_url": "https://ntfy.sh",
  "ntfy_topic": "thingstodo",
  "ntfy_access_token": "",
  "base_url": "",
  "privacy_mode": false,
  "review_include_recurring": true
}
```

### PATCH /api/user/settings
Request: Partial update (any subset of fields)
```json
{
  "play_complete_sound": true,
  "show_count_main": true,
  "show_count_projects": true,
  "show_count_tags": true,
  "review_after_days": 7,
  "sort_areas": "manual|a-z|z-a",
  "sort_tags": "manual|a-z|z-a",
  "evening_starts_at": "HH:MM (24h, default 18:00)",
  "default_time_gap": "integer (minutes, default 60)",
  "show_time_badge": "boolean (default true)",
  "time_format": "12h|24h (default 12h)",
  "font_size": "integer 12-20 (default 16)",
  "default_reminder_type": "at_start|on_day|minutes_before|hours_before|days_before|null (default null)",
  "default_reminder_value": "integer 0-99 (default 0)",
  "copy_reminders_to_recurring": "boolean (default true)",
  "notification_provider": "webpush|ntfy|none (default webpush)",
  "ntfy_server_url": "string (default https://ntfy.sh)",
  "ntfy_topic": "string (default thingstodo)",
  "ntfy_access_token": "string (optional, Bearer token for authenticated ntfy servers)",
  "base_url": "string (optional, e.g. https://tasks.example.com, for ntfy click-through links)",
  "privacy_mode": "boolean (default false)",
  "review_include_recurring": "boolean (default true; includes recurring tasks in the Review section)"
}
```

Response (200): Updated settings object

The settings row is upserted on first save: a PATCH sent before any GET still persists (missing fields take their defaults), and the response is the merged row.

Errors (400 `VALIDATION` with `field`): a value outside the enums and ranges documented above for `sort_areas`, `sort_tags`, `evening_starts_at`, `time_format`, `font_size`, `default_reminder_type`, `default_reminder_value` or `notification_provider`. Omitted fields are not validated; `default_reminder_type: null` clears the value.

---

## View Endpoints

Pre-structured data for each smart list. Complex grouping/filtering happens server-side.

All views share one set of membership predicates, and `/views/counts` counts `COUNT(DISTINCT id)` with the same predicates, so a count always equals the distinct tasks of its view. "Today" is resolved in the server timezone.

Placement rule: every open, non-trashed task appears in at least one of Inbox, Today (any section), Upcoming, Anytime and Someday. The single documented exception: a task dated today whose schedule rows all exist and are completed appears in **no** list — its work for the date is done; it comes back when it is rescheduled or its schedules reopen. A task with a `when_date` and no `task_schedules` rows counts as one uncompleted schedule on that `when_date`.

### GET /api/views/inbox
Open tasks with no project, no area and no `when_date`. A deadline-only "loose" task (deadline set, nothing else) stays in Inbox — it is never in Anytime — and still appears in Today (deadline today) or Overdue (deadline passed).
```json
{
  "tasks": [/* task objects ordered by sort_order_today */],
  "review": [/* tasks not updated in review_after_days, excluding inbox tasks */]
}
```

### GET /api/views/today
Four open sections plus the day's completed log. Sections may overlap: a task with a day and an evening schedule appears in both Today and This Evening, and a task with a passed `when_date` and a passed deadline appears in both Overdue and Earlier.

- **Today / This Evening**: open tasks due today (`deadline` = today) or with an uncompleted schedule entry for today; a rowless task dated today counts as one uncompleted entry. The day section holds entries with no `start_time` or one before the user's `evening_starts_at`; This Evening holds entries at or after it (times are zero-padded `HH:MM`).
- **Overdue**: open tasks with `deadline < today`.
- **Earlier**: open tasks with `when_date < today`, not `someday`, regardless of schedule rows — a rowless past task included.
- **Completed**: tasks completed, canceled or marked won't-do during the local calendar day.
```json
{
  "sections": [
    {
      "title": "Today",
      "groups": [
        {
          "project": { "id": "string", "title": "string" } | null,
          "tasks": [/* task objects */]
        }
      ]
    },
    {
      "title": "This Evening",
      "groups": [
        {
          "project": { "id": "string", "title": "string" } | null,
          "tasks": [/* task objects */]
        }
      ]
    }
  ],
  "overdue": [/* open tasks with deadline < today */],
  "earlier": [/* open tasks with when_date < today, not someday */],
  "completed": [/* tasks completed today */]
}
```

### GET /api/views/upcoming
Query params: `from` (ISO date, default **tomorrow** in the server timezone; an explicit `from` is still honored)

Date groups list one entry per uncompleted schedule row on or after `from`; a task with a `when_date` and no schedule rows appears once under its `when_date`. Each task in the response includes a `schedule_date` field indicating which date it appears under.

The **Overdue** and **Earlier** sections stay in the response and are both relative to **today**, never to `from`, so a task dated today never lands in Upcoming at all with the default `from`.

```json
{
  "overdue": [/* open tasks with deadline < today */],
  "dates": [
    {
      "date": "2024-03-15",
      "tasks": [/* task objects, each with extra "schedule_date" field */]
    }
  ],
  "earlier": [/* open tasks with when_date < today, not someday */]
}
```

### GET /api/views/anytime
Open tasks with no `when_date` that belong to a project or an area. Loose tasks (no project, no area, no `when_date`) are Inbox only, so `no_area.standalone_tasks` is always empty here.
```json
{
  "areas": [
    {
      "area": { "id": "string", "title": "string" },
      "projects": [
        {
          "project": { "id": "string", "title": "string" },
          "tasks": [/* task objects */]
        }
      ],
      "standalone_tasks": [/* tasks in area but no project */]
    }
  ],
  "no_area": {
    "projects": [/* projects with no area */],
    "standalone_tasks": []
  }
}
```

### GET /api/views/someday
Same structure as anytime, filtered to open tasks with `when_date = 'someday'` (loose someday tasks included under `no_area.standalone_tasks`).
```json
{
  "areas": [/* same structure as anytime */],
  "no_area": {/* same structure as anytime */}
}
```

### GET /api/views/logbook
Query params: `limit` (default 50), `offset` (default 0)

Tasks with status `completed`, `canceled` or `wont_do`, not trashed, grouped by completion date in the server timezone. `total` is the distinct task count.
```json
{
  "groups": [
    {
      "date": "2024-03-15",
      "tasks": [/* completed/canceled/wont_do task objects */]
    }
  ],
  "total": 150
}
```

### GET /api/views/trash
Query params: `limit` (default 50), `offset` (default 0)

Soft-deleted tasks (any status), grouped by deletion date in the server timezone. `total` is the distinct task count.
```json
{
  "groups": [
    {
      "date": "2024-03-15",
      "tasks": [/* soft-deleted task objects */]
    }
  ],
  "total": 150
}
```

### GET /api/views/counts
One `COUNT(DISTINCT id)` per view using the same predicates as the views above; completed tasks are excluded from the open lists. `today` counts the distinct tasks across the Today, This Evening, Overdue and Earlier sections; `overdue` matches the Overdue section of Today/Upcoming.
Response (200):
```json
{
  "inbox": 5,
  "today": 3,
  "overdue": 1,
  "review": 2,
  "anytime": 12,
  "someday": 4,
  "logbook": 50,
  "trash": 2
}
```

---

## Search

### GET /api/search
Query params: `q` (required), `limit` (default 20, max 500), `cursor`
(optional, from the previous response).

The query is made safe (never a 500): every whitespace token is quoted as an
FTS5 string with any `"` doubled, so operators and punctuation are literal —
`c++`, `email:`, `milk-2` match as plain words. Tokens are ANDed. Each token
is also a **prefix** match: `mil` finds "milk". The `unicode61` tokenizer
case-folds, so `покуп` finds "Покупки на неделю".

Results are ordered by `rank`, then `id` as the unique tie-breaker; the
cursor encodes both. Pagination follows the same opt-in rules as
`GET /api/tasks`, except that the default limit is 20.

Response (200):
```json
{
  "results": [
    {
      "task": {/* task object, same shape as GET /api/tasks items */},
      "title_snippet": "string with <mark>highlights</mark>",
      "notes_snippet": "string with <mark>highlights</mark>",
      "rank": -1.5
    }
  ],
  "next_cursor": "string (present only when more rows exist; omitted otherwise)"
}
```

---

## SSE Events

### GET /api/events
**Auth required** (Bearer token; the browser session works same-origin). Response: text/event-stream

On connect the server sends:
```
event: connected
data: {"client_id": "string"}
```

#### Reading the stream from a widget (SSE via fetch)

The browser's `EventSource` cannot send headers, so cross-origin widgets
authenticate by reading the stream with `fetch()` and a `ReadableStream`
instead. Tokens in query strings are not supported.

```js
const resp = await fetch("/api/events", {
  headers: { Authorization: "Bearer ttd_<token>" },
});
const reader = resp.body
  .pipeThrough(new TextDecoderStream())
  .getReader();
for (;;) {
  const { value, done } = await reader.read();
  if (done) break;
  // value is one chunk of the text/event-stream: parse "event:" and
  // "data:" lines and refetch what changed.
  console.log(value);
}
```

Every **single-entity** change event carries an envelope: the changed
`entity`, its `id`, and `seq` — the latest change-log sequence number at
broadcast time, read after the write, so it is at least the write's own
change-log entry. `seq` is omitted when it cannot be read. `bulk_change`,
`connected` and `reminder_fired` keep their own shapes and carry no `seq`.

Event types:
```
event: task_created
data: {"entity": "task", "id": "string", "seq": 42, "task": {/* task object */}}

event: task_updated
data: {"entity": "task", "id": "string", "seq": 42, "task": {/* task object, when loaded */}}

event: task_deleted
data: {"entity": "task", "id": "string", "seq": 42}

event: task_purged
data: {"entity": "task", "id": "string", "seq": 42}

event: project_updated
data: {"entity": "project", "id": "string", "seq": 42, "project": {/* project object, when loaded */}}

event: area_updated
data: {"entity": "area", "id": "string", "seq": 42, "area": {/* area object */}}

event: tag_updated
data: {"entity": "tag", "id": "string", "seq": 42, "tag": {/* tag object */}}

event: saved_filter_changed
data: {"entity": "saved_filter", "id": "string", "seq": 42, "view": "today|upcoming|anytime|someday|logbook"}

event: bulk_change
data: {"type": "reorder|move|delete", "entity": "task|project|heading|area|tag", "ids": ["string"]}

event: reminder_fired
data: {"task_id": "string", "task_title": "string", "reminder_type": "string", "description": "string"}
```

Checklist item writes broadcast a `task_updated` event whose envelope
identifies the item, with the owning task as context:
```
event: task_updated
data: {"entity": "checklist_item", "id": "<item id>", "task_id": "<owning task id>", "seq": 42}
```

Schedule, reminder, attachment and repeat-rule writes broadcast
`task_updated` with `{"entity": "task", "id": "<task id>", "seq": N}`.
Heading writes broadcast `project_updated` with the heading's project.
Task mutations applied through `POST /api/sync/push` broadcast the same
single-entity task events as their REST equivalents.

---

## Saved Filters

### GET /api/saved-filters?view={view}

Returns all saved filters for the current user for a specific view, sorted A-Z by name.

Query params: `view` — one of `today`, `upcoming`, `anytime`, `someday`, `logbook` (required)

Response (200):
```json
{
  "saved_filters": [
    {
      "id": "abc1234567",
      "view": "today",
      "name": "High priority",
      "config": "{\"areas\":[],\"projects\":[],\"tags\":[],\"highPriority\":true,\"plannedDate\":null,\"deadline\":null,\"search\":\"\"}",
      "created_at": "2026-02-26T12:00:00"
    }
  ]
}
```

### POST /api/saved-filters

Creates a new saved filter. Maximum 10 per view.

Request:
```json
{
  "view": "today",
  "name": "High priority",
  "config": "{\"areas\":[],\"projects\":[],\"tags\":[],\"highPriority\":true,\"plannedDate\":null,\"deadline\":null,\"search\":\"\"}"
}
```

Response (201): the created SavedFilter object.

Error (422): `LIMIT_REACHED` — maximum 10 saved filters per view.

SSE: broadcasts `saved_filter_changed` with `{ "view": "<view>" }`.

### DELETE /api/saved-filters/{id}

Deletes a saved filter. Only the owning user can delete it.

Response: 204 No Content.

Error (404): `NOT_FOUND`.

SSE: broadcasts `saved_filter_changed` with `{ "view": "<view>" }`.

---

## Sync

Endpoints for local-first synchronization. All endpoints require authentication.

### GET /api/sync/pull
Returns changes to the database since a given sequence number.

Query params:
- `since` (int64, default 0): Return changes after this sequence number
- `limit` (int, default 500, max 1000): Maximum number of changes to return

Response (200):
```json
{
  "changes": [
    {
      "seq": 42,
      "entity": "task",
      "entity_id": "abc12345",
      "action": "create|update|delete",
      "fields": "title,notes",
      "snapshot": "{...full entity JSON...}",
      "user_id": "user123",
      "device_id": "device456",
      "created_at": "2026-03-15T10:30:00Z"
    }
  ],
  "cursor": 42,
  "has_more": false
}
```

Response (410 Gone): Cursor has been purged, perform a full sync
```json
{
  "error": "cursor expired: change log has been purged, perform a full sync",
  "code": "CURSOR_EXPIRED"
}
```

| Field | Description |
|-------|-------------|
| `seq` | Monotonically increasing sequence number (cursor) |
| `entity` | Entity type: task, project, area, tag, heading, checklist_item, attachment, schedule, reminder, repeat_rule |
| `entity_id` | ID of the entity that changed |
| `action` | Type of change: create, update, or delete |
| `fields` | JSON-encoded string array of the fields that changed (for updates), e.g. `["title","notes"]`; null for create/delete |
| `snapshot` | Full entity state as JSON string, always populated: the complete entity for create and update, and for soft deletes (trash) the entity as of deletion; hard deletes and purges log `{"id": "..."}` (a repeat-rule delete also carries `task_id`) |
| `user_id` | User ID (omitted if empty) |
| `device_id` | Device ID of origin (omitted if empty) |
| `created_at` | ISO 8601 timestamp when change was recorded |

### GET /api/sync/full
Returns the complete current state of all entities, used for initial sync or when cursor has expired.

The `tasks` array includes trashed (soft-deleted, `deleted_at` non-null) tasks so clients can populate their Trash view from the initial snapshot; purged tasks are excluded. The `cursor` is taken before the snapshot reads: a write that commits during the snapshot may appear both in the snapshot and in a subsequent pull from this cursor (pull re-applies full snapshots, so the overlap is harmless), but no write can be skipped.

Response (200):
```json
{
  "tasks": [/* array of task objects */],
  "projects": [/* array of project objects */],
  "areas": [/* array of area objects */],
  "tags": [/* array of tag objects */],
  "headings": [/* array of heading objects */],
  "checklist": [/* array of checklist item objects */],
  "attachments": [/* array of attachment objects */],
  "schedules": [/* array of schedule objects */],
  "reminders": [/* array of reminder objects */],
  "repeat_rules": [/* array of repeat rule objects */],
  "cursor": 1000
}
```

### POST /api/sync/push
Applies changes from a client device to the server. Uses last-write-wins (LWW) conflict resolution per entity based on `updated_at` timestamps. A task update pushing `status` to `completed`, `canceled` or `wont_do` runs the same guarded finishing path as the single-task endpoints: idempotent on re-push, one repeat instance per task.

Project and area `delete` changes run the same delete-to-Trash semantics as `DELETE /api/projects/:id` and `DELETE /api/areas/:id`: child/standalone tasks are trashed with a change-log entry each. An area delete that still has projects produces a per-change result with `status: "error"` and an error containing `HAS_PROJECTS`, and nothing is deleted.

Child-entity creates (checklistItem, attachment, schedule, reminder) are idempotent by id: a re-pushed create with the same id and the same `task_id` reports `applied` without writing; the same id under a different `task_id` is a per-change error containing `already exists`.

Request:
```json
{
  "device_id": "string (required)",
  "changes": [
    {
      "entity": "task|project|area|tag|heading|checklistItem|attachment|schedule|reminder",
      "entity_id": "string",
      "action": "create|update|delete",
      "data": { "title": "string", "notes": "string", ... },
      "fields": ["title", "notes"],
      "client_updated_at": "2026-03-15T10:30:00Z or 2026-03-15 10:30:00"
    }
  ]
}
```

Response (200):
```json
{
  "results": [
    {
      "entity": "task",
      "entity_id": "abc12345",
      "status": "applied|conflict_resolved|error",
      "seq": 1001,
      "error": "optional error message",
      "code": "VALIDATION|NOT_FOUND|CONFLICT|INTERNAL"
    }
  ]
}
```

`results` is positional: `results[i]` is the outcome of `changes[i]`, in request order, even when some changes fail.

| Status | Meaning |
|--------|---------|
| `applied` | Change was applied without conflict |
| `conflict_resolved` | Change conflicted with server state (server was newer), but was still recorded for audit |
| `error` | Change could not be applied; check `error` and `code` fields for details. Internal failures report the generic `internal error`; user-facing messages (validation, not found, duplicate name, unsupported entity/action) keep their own text. Boundary validation failures use `"<field>: <message>"` (e.g. `when_date: must be YYYY-MM-DD or 'someday'`, or `when_date: duplicate timeless date`) and do not fail the batch |

**Error codes:** every `error` result also carries a machine-readable `code` (absent on `applied` and `conflict_resolved`):

| Code | Meaning | Client handling |
|------|---------|-----------------|
| `VALIDATION` | The change breaks a boundary rule: bad field value or type, unsupported entity type or action, missing `task_id`, or a `when_date` that would duplicate another timeless schedule of the same task (`when_date: duplicate timeless date`) | Permanent — the same change will fail again; fix or drop it |
| `NOT_FOUND` | The change's target or a referenced entity (task, project, area, tag, heading) does not exist | Permanent — refresh state and re-resolve the id |
| `CONFLICT` | Collision with server state: duplicate project/area/tag name, deleting an area that still has projects (`HAS_PROJECTS`), or a child-entity id re-used under another parent | Permanent — resolve locally before re-pushing |
| `INTERNAL` | Unexpected server failure; `error` is the generic `internal error` | Transient — safe to retry later |

**Supported entities:** task, project, area, tag, heading, checklistItem, attachment, schedule, reminder

**Conflict resolution:** For task, project, and area updates, if the server's `updated_at` is strictly newer than the client's `client_updated_at`, the status is marked as `conflict_resolved`. The client's write is still applied — conflicts are client-wins: a queued offline edit overwrites a newer server write; `conflict_resolved` is informational. The change is recorded in the change log either way.

---

## Health

### GET /health
**No auth required.**

Response (200):
```json
{ "status": "ok" }
```

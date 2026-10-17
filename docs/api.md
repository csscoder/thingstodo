# ThingsToDo API Contract

Base URL: `/api`

All endpoints return JSON. All mutations broadcast SSE events. Authentication via httpOnly JWT cookie (builtin/oidc mode), proxy header (proxy mode), or API key via `Authorization: Bearer <key>` header.

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
| `FORBIDDEN` | 403 | Authenticated but not allowed (e.g. OIDC email not permitted) |
| `NOT_FOUND` | 404 | Unknown `/api` route, or entity does not exist |
| `METHOD_NOT_ALLOWED` | 405 | Method not registered for a known `/api` route; `Allow` header lists supported methods |
| `BAD_REQUEST` | 400 | Malformed JSON body or invalid request parameters |
| `VALIDATION` | 400/422 | Request field failed validation; 400 for malformed values, 422 for referenced ids that do not exist (carries `field`) |
| `RESERVED` | 403 | Reserved tag name |
| `DUPLICATE_NAME` | 409 | Project/area/tag title already exists |
| `HAS_PROJECTS` | 409 | Area still contains projects |
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
`POST` and `PATCH` for projects, areas, and tags return **409 Conflict** with code `DUPLICATE_NAME` if the title already exists.

### Pagination (where applicable)
Query params: `?limit=50&offset=0`

### Sort Order
All entities with `sort_order` use REAL (float) for fractional positioning. New items default to max(sort_order) + 1024. Insert-between uses (before + after) / 2.

### IDs
All entity IDs are nanoid strings (8-10 chars). Task IDs double as permalink slugs.

### Validation Rules
Mutation endpoints validate request fields at the API boundary, before any write reaches the database. Every validation error uses the error envelope above with code `VALIDATION` and the offending `field`:

- **Malformed values → 400** `VALIDATION`: dates that are not real `YYYY-MM-DD` calendar dates (`2026-02-31` is rejected), `when_date` values that are neither a date nor `someday`, times not in `HH:MM` 24-hour form (`9:00`, `24:00`), client-supplied `id` values outside `^[A-Za-z0-9_-]{8,21}$`, empty titles on create/update, and `end_time` earlier than or equal to `start_time`.
- **Reminder, repeat, settings and status fields → 400** `VALIDATION`: reminder `type` outside the documented set, reminder `value` outside 0-99, `exact` reminders without a parseable `exact_at`; repeat patterns with an unknown `type`, `every` below 1, an unknown `mode`, or invalid weekday/ordinal/day/month values; user settings fields outside their documented enums and ranges (`sort_areas`, `sort_tags`, `evening_starts_at`, `time_format`, `font_size`, `default_reminder_type`, `default_reminder_value`, `notification_provider`); project `status` outside `open|completed|canceled`.
- **`exact_at` normalization**: reminder `exact_at` accepts RFC 3339 with a numeric offset or `Z`, or a naive `YYYY-MM-DDTHH:MM[:SS]` timestamp read in the server timezone, and is stored and returned as UTC RFC 3339 (`2026-10-20T09:00:00+03:00` → `2026-10-20T06:00:00Z`).
- **Referenced ids that do not exist → 422** `VALIDATION`: `project_id`, `area_id`, `heading_id`, `tag_ids`, `parent_tag_id` and `task_id` (URL or body) must reference an existing row. A task create with both `project_id` and `heading_id` requires the heading to belong to that project.
- Client ids apply to every create endpoint that accepts a body `id`: tasks, projects, areas, tags, headings, checklist items, schedules, reminders, link attachments, and sync creates.
- Bulk `set_when`/`set_deadline` validate their date params the same way (an empty string clears the date and stays allowed); `PATCH /api/tasks/:id/move` validates `when_date` and its reference params.
- `deadline >= when_date` is enforced on task create and update (including cross-field updates resolved against the stored task).

**Sync** (`POST /api/sync/push`) applies the same rules per change: a violation produces a result with `status: "error"` and `error: "<field>: <message>"`, and the rest of the batch continues.

---

## Authentication

### API Key (builtin mode)

Set the `API_KEY` environment variable to a secret string. Then pass it via the `Authorization` header:

```
Authorization: Bearer <your-api-key>
```

The API key authenticates as the first (admin) user in the database. This is useful for programmatic access (e.g., iOS Shortcuts, scripts) where cookie-based auth isn't practical.

If the header is present but the key is wrong, the request is rejected with 401 immediately (no fallback to cookie auth).

### GET /api/auth/config
**Mode: all** (public, no authentication required)

Returns the current auth mode so the login page can render the appropriate UI.

Response (200):
```json
{ "auth_mode": "builtin|proxy|oidc|none" }
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

## Tasks

### GET /api/tasks
Query params: `status`, `project_id`, `area_id`, `heading_id`, `tag_ids` (comma-separated), `when_date`, `when_before`, `when_after`, `has_deadline`, `search`

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
  ]
}
```

### POST /api/tasks
Request:
```json
{
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

Response (201): Full task object (same shape as list item)

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
Request: Partial update (any subset of task fields)
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

Response (200): Updated full task object

### DELETE /api/tasks/:id
Soft delete (moves to trash).

Response (204): No content

### DELETE /api/tasks/:id/purge
Permanently deletes the task.

Response (204): No content

### PATCH /api/tasks/:id/complete
Automatically cleans up schedule entries: completes past and today's uncompleted entries, deletes future (after today) and someday uncompleted entries.
Response (200): Updated task with status=completed, completed_at set

### PATCH /api/tasks/:id/cancel
Automatically cleans up schedule entries (same as complete).
Response (200): Updated task with status=canceled, canceled_at set

### PATCH /api/tasks/:id/wontdo
Automatically cleans up schedule entries (same as complete).
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
Move task to a different project, area, or heading.

Request:
```json
{
  "project_id": "string|null",
  "area_id": "string|null",
  "heading_id": "string|null",
  "when_date": "string|null"
}
```

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
  "action": "complete|cancel|wontdo|delete|set_when|set_deadline|set_priority|move_project|add_tags|remove_tags|mark_reviewed",
  "params": {}
}
```

| Action | Params |
|--------|--------|
| set_when | `when_date` (string), `when_time` (string, optional) |
| set_deadline | `deadline` (string) |
| set_priority | `priority` (number, 0 or 1) |
| move_project | `project_id` (string or null) |
| add_tags | `tag_ids` (string[]) |
| remove_tags | `tag_ids` (string[]) |
| mark_reviewed | _(none)_ — bumps `updated_at` to now |

Limits: max 100 tasks per request.

Response (200):
```json
{ "ok": true, "affected": 3 }
```

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
Response (204): No content

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
| `at_start` | 0 | null | At schedule start_time |
| `on_day` | 0 | null | Morning of when_date |
| `minutes_before` | 0-99 | null | N min before start_time |
| `hours_before` | 0-99 | null | N hours before start_time |
| `days_before` | 0-99 | null | N days before when_date at morning time |
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
{ "key": "base64url-encoded VAPID public key" }
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

Response (201): Area object

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
  "privacy_mode": false
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
  "font_size": "integer (12|14|16|18|20, default 16)",
  "default_reminder_type": "at_start|on_day|minutes_before|hours_before|days_before|null (default null)",
  "default_reminder_value": "integer 0-99 (default 0)",
  "copy_reminders_to_recurring": "boolean (default true)",
  "notification_provider": "webpush|ntfy|none (default webpush)",
  "ntfy_server_url": "string (default https://ntfy.sh)",
  "ntfy_topic": "string (default thingstodo)",
  "ntfy_access_token": "string (optional, Bearer token for authenticated ntfy servers)",
  "base_url": "string (optional, e.g. https://tasks.example.com, for ntfy click-through links)",
  "privacy_mode": "boolean (default false)"
}
```

Response (200): Updated settings object

Errors (400 `VALIDATION` with `field`): a value outside the enums and ranges documented above for `sort_areas`, `sort_tags`, `evening_starts_at`, `time_format`, `font_size`, `default_reminder_type`, `default_reminder_value` or `notification_provider`. Omitted fields are not validated; `default_reminder_type: null` clears the value.

---

## View Endpoints

Pre-structured data for each smart list. Complex grouping/filtering happens server-side.

### GET /api/views/inbox
Tasks with no project, no area, no when_date, status=open.
```json
{
  "tasks": [/* task objects ordered by sort_order_today */],
  "review": [/* tasks not updated in review_after_days, excluding inbox tasks */]
}
```

### GET /api/views/today
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
  "overdue": [/* tasks with deadline < today */],
  "earlier": [/* past-dated open tasks without overdue deadlines */],
  "completed": [/* tasks completed today */]
}
```

### GET /api/views/upcoming
Query params: `from` (ISO date, default today)

Tasks with multiple schedule entries appear once per date. Each task in the response includes a `schedule_date` field indicating which date it appears under.

```json
{
  "overdue": [/* tasks with deadline < today */],
  "dates": [
    {
      "date": "2024-03-15",
      "tasks": [/* task objects, each with extra "schedule_date" field */]
    }
  ],
  "earlier": [/* past-dated open tasks */]
}
```

### GET /api/views/anytime
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
    "standalone_tasks": [/* tasks with no area, no project, but with when_date or not inbox */]
  }
}
```

### GET /api/views/someday
Same structure as anytime but filtered to someday-deferred items.
```json
{
  "areas": [/* same structure as anytime */],
  "no_area": {/* same structure as anytime */}
}
```

### GET /api/views/logbook
Query params: `limit` (default 50), `offset` (default 0)
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
Query params: `q` (required, FTS5 query string), `limit` (default 20)

Response (200):
```json
{
  "results": [
    {
      "task": {/* task object */},
      "title_snippet": "string with <mark>highlights</mark>",
      "notes_snippet": "string with <mark>highlights</mark>",
      "rank": -1.5
    }
  ]
}
```

---

## SSE Events

### GET /api/events
**No auth required.** Response: text/event-stream

Event types:
```
event: task_created
data: {"id": "string", "task": {/* task object */}}

event: task_updated
data: {"id": "string", "task": {/* task object */}}

event: task_deleted
data: {"id": "string"}

event: task_purged
data: {"id": "string"}

event: project_updated
data: {"id": "string", "project": {/* project object */}}

event: area_updated
data: {"id": "string", "area": {/* area object */}}

event: tag_updated
data: {"id": "string", "tag": {/* tag object */}}

event: bulk_change
data: {"type": "reorder|move|delete", "entity": "task|project|heading|area|tag", "ids": ["string"]}

event: reminder_fired
data: {"task_id": "string", "task_title": "string", "reminder_type": "string", "description": "string"}
```

---

## Saved Filters

### GET /saved-filters?view={view}

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

### POST /saved-filters

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

### DELETE /saved-filters/{id}

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
| `fields` | Comma-separated list of fields that changed (for updates); null for create/delete |
| `snapshot` | Full entity state as JSON string (always populated for create/update, empty string for delete) |
| `user_id` | User ID (omitted if empty) |
| `device_id` | Device ID of origin (omitted if empty) |
| `created_at` | ISO 8601 timestamp when change was recorded |

### GET /api/sync/full
Returns the complete current state of all entities, used for initial sync or when cursor has expired.

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
Applies changes from a client device to the server. Uses last-write-wins (LWW) conflict resolution per entity based on `updated_at` timestamps.

Request:
```json
{
  "device_id": "string (required)",
  "changes": [
    {
      "entity": "task|project|area|tag",
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
      "error": "optional error message"
    }
  ]
}
```

| Status | Meaning |
|--------|---------|
| `applied` | Change was applied without conflict |
| `conflict_resolved` | Change conflicted with server state (server was newer), but was still recorded for audit |
| `error` | Change could not be applied; check `error` field for details. Internal failures report the generic `internal error`; user-facing messages (validation, not found, duplicate name, unsupported entity/action) keep their own text. Boundary validation failures use `"<field>: <message>"` (e.g. `when_date: must be YYYY-MM-DD or 'someday'`) and do not fail the batch |

**Supported entities:** task, project, area, tag

**Conflict resolution:** For task, project, and area updates, if the server's `updated_at` is strictly newer than the client's `client_updated_at`, the status is marked as `conflict_resolved`. The client's change is still recorded in the change log. The client is responsible for pulling the latest state and reapplying their changes if needed.

---

## Health

### GET /health
**No auth required.**

Response (200):
```json
{ "status": "ok" }
```

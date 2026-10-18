# Web UI and Sync Correctness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the built-in web UI work correctly against the hardened API, lose no change in sync, agree with the server on "today" and on every list, and prove it with unit tests, shared contract fixtures and a Playwright suite.

**Architecture:** Server fixes first (session guard, full sync, idempotent child creates, per-change error codes, timezone in `/api/auth/config`, two view fixes). Then the frontend sync engine is reworked around per-entry queue state (`pending`/`stuck`), per-entity FIFO and stored skipped server snapshots. Local view builders stay in IndexedDB and are forced into parity with the server by JSON fixtures read by both Go and Vitest tests. A committed Playwright suite drives every lifecycle against a real server.

**Tech Stack:** Go 1.27 + chi + SQLite; React 19, Vite 7.3.1, Dexie 4, TanStack Query 5, Zustand 5, Vitest 4 + fake-indexeddb + msw; new dev dependency `@playwright/test` 1.64.1.

**Spec:** `docs/superpowers/specs/2026-10-18-web-ui-sync-correctness-design.md` (approved 2026-10-18). Section ids below (P0.1 … P3) refer to it.

## Global Constraints

- TDD for every behavior change: a failing test first, then the fix. Never skip, delete or weaken a test.
- Every API change updates `docs/api.md`, `internal/.../openapi.yaml` (served at `GET /api/openapi.yaml`) and keeps the OpenAPI drift test green.
- New dependency: only `@playwright/test` 1.64.1 (dev). No other new dependency, Go or npm.
- Session guard: never weaken it. Bearer requests stay exempt; cookie and proxy sessions stay guarded on every method.
- Conflicts stay client-wins (owner decision Q3). No LWW, no `device_id` plumbing.
- "Today" in the UI is the server `TZ` (owner decision Q1). Today shows one row per open today schedule entry; completed time blocks of today are hidden (Q2).
- Batch size `100`; 413 halves the batch; client code `TOO_LARGE`; `INTERNAL` becomes visible in the stuck list after `5` consecutive failures; transient retry stays 30 s.
- Server error codes per change: `VALIDATION`, `NOT_FOUND`, `CONFLICT` (permanent), `INTERNAL` (transient).
- Font size: server accepts integers 12–20.
- Branch `csscoder/review-and-test-coverage`; commit per task; commit subject in the repo's `type: subject` form, no attribution trailers.
- Commands: `go test ./...`, `go vet ./...`, `golangci-lint run`, `cd frontend && npm test`, `npm run lint`, `npx tsc -b`.

## Review Focus

1. **A stuck change whose entity is then edited again** — the owner expects the newer edit to win after Retry, never the older one. Pinned in Task 9 (`holds later changes of a stuck entity`).
2. **410 while the queue holds a stuck create plus children** — the owner expects the task and its schedule to stay visible. Pinned in Task 10 (`fullSync re-applies a stuck create and its pending child`).
3. **Late evening near midnight in Europe/Moscow while the browser is in UTC** — "today" lists must match the server. Pinned in Task 18 (`ownerToday uses the owner zone, not the browser zone`) and fixture `midnight-moscow.json` (Tasks 21–22).
4. **Discard of a create that has children without errors** — the owner must see every entry that will be removed and can cancel. Pinned in Task 11 (`cancel removes nothing`).
5. **Proxy that rewrites Host plus a browser that still sends `Sec-Fetch-Site: cross-site`** — the guard must still reject. Pinned in Task 1 (`cross-site wins over matching Host`).

---

## File Map

Server:
- `internal/middleware/auth.go` — `crossOriginSession` reads `Sec-Fetch-Site` first.
- `internal/handler/sync.go` — `Full` (trashed tasks, one read transaction), child-create idempotency, `SyncPushResult.Code`, `syncFail`, `applyTaskChange` applies all fields.
- `internal/model/models.go` — `TaskFilters.IncludeDeleted`.
- `internal/repository/tasks.go` — honor `IncludeDeleted`.
- `internal/handler/validate.go` — font size range.
- `internal/config/config.go` — `TimeZoneName`, `zoneName`.
- `internal/handler/auth.go` — `timezone` in `AuthConfig`.
- `internal/repository/views.go` — Today join on open entries; Counts exclude non-open projects.
- `internal/repository/views_contract_test.go` — fixture runner (new).
- `testdata/views/*.json` — shared fixtures (new).
- `docs/api.md`, OpenAPI file, `README.md`.

Frontend:
- `frontend/vite.config.ts` — `changeOrigin: false`.
- `frontend/src/db/schema.ts`, `frontend/src/db/index.ts` — queue entry state, `skippedSnapshots` table (Dexie version 4).
- `frontend/src/sync/push.ts` — batching, classification, FIFO.
- `frontend/src/sync/pull.ts` — skipped snapshots, `fullSync` stamping and replay.
- `frontend/src/sync/replay.ts` — apply a queue entry to local rows (new).
- `frontend/src/sync/queueActions.ts` — list stuck, retry, discard (new).
- `frontend/src/sync/engine.ts`, `frontend/src/sync/status.ts` — stuck count.
- `frontend/src/components/StuckChangesDialog.tsx` (new), `SyncStatus.tsx`.
- `frontend/src/components/AppLayout.tsx`, `SyncingOverlay.tsx` — first-sync failure.
- `frontend/src/components/AppDndContext.tsx`, `QuickEntry.tsx`, `ScheduleEditor.tsx`, `TaskDetail.tsx`, `SearchOverlay.tsx`; `frontend/src/pages/ProjectView.tsx`, `TrashView.tsx`.
- `frontend/src/hooks/queries.ts`, `useSSE.ts`, `localQueries.ts`.
- `frontend/src/lib/owner-time.ts` (new), `date-parser.ts`, `parse-tags.ts`.
- Delete: `frontend/src/sync/reminders.ts`, `missedRemindersEmitter.ts`, `components/MissedRemindersToast.tsx`.
- `frontend/playwright.config.ts`, `frontend/e2e/*` (new); `.github/workflows/ci.yml`.

---

## Phase 0 — Compatibility breakers

### Task 1: Session guard trusts `Sec-Fetch-Site` (P0.1)

**Files:**
- Modify: `internal/middleware/auth.go` (`crossOriginSession`, ~line 174)
- Modify: `frontend/vite.config.ts:45-47`, `README.md` (deployment section)
- Test: `internal/middleware/cors_test.go`

**Interfaces:**
- Produces: `crossOriginSession(r *http.Request) bool` (unchanged signature).

- [ ] **Step 1: Write the failing test** `TestAuth_CrossOriginSession_SecFetchSite` in `cors_test.go`, reusing `guardRequest`, `runAuth`, `assertStatusBody`, `proxyCfg`:

```go
// Host rewritten by a proxy (Vite changeOrigin, nginx $proxy_host):
// Origin names :5173, Host is localhost:2999, browser says same-origin.
r := guardRequest("http://localhost:5173")
r.Host = "localhost:2999"
r.Header.Set("Sec-Fetch-Site", "same-origin")
assertStatusBody(t, runAuth(proxyCfg, firstUserStub, userExistsStub, nil, nil, r), http.StatusOK, "proxyuser")

r = guardRequest("")
r.Header.Set("Sec-Fetch-Site", "none")
assertStatusBody(t, runAuth(proxyCfg, firstUserStub, userExistsStub, nil, nil, r), http.StatusOK, "proxyuser")

// cross-site wins over matching Host
for _, v := range []string{"cross-site", "same-site"} {
    r = guardRequest("https://app.example")
    r.Header.Set("Sec-Fetch-Site", v)
    assertStatusBody(t, runAuth(proxyCfg, firstUserStub, userExistsStub, nil, nil, r), http.StatusForbidden, crossOriginForbiddenEnvelope)
}
```

- [ ] **Step 2: Run** `go test ./internal/middleware -run SecFetchSite -v` — Expected: FAIL (first case 403).
- [ ] **Step 3: Implement** in `crossOriginSession`: if `r.Header.Get("Sec-Fetch-Site")` is non-empty, return `false` for `same-origin` or `none`, `true` for any other value; otherwise keep the existing Origin/Host comparison. Update the doc comment with the `go doc net/http.CrossOriginProtection` citation from the spec.
- [ ] **Step 4: Run** `go test ./internal/middleware -v` — Expected: PASS, including `TestAuth_CrossOriginSessionForbidden`.
- [ ] **Step 5:** In `frontend/vite.config.ts` replace `'/api': 'http://localhost:2999'` with `'/api': { target: 'http://localhost:2999', changeOrigin: false }`. Add to README deployment: "Any reverse proxy works with current browsers (the server trusts `Sec-Fetch-Site`); preserving `Host` is still recommended."
- [ ] **Step 6: Commit** `fix: accept same-origin sessions behind Host-rewriting proxies`

### Task 2: Full sync returns trashed tasks with a consistent cursor (P0.2)

**Files:**
- Modify: `internal/model/models.go` (`TaskFilters`), `internal/repository/tasks.go:~315-330` (`List`), `internal/handler/sync.go:189-…` (`Full`)
- Test: `internal/handler/sync_integration_test.go`

**Interfaces:**
- Produces: `model.TaskFilters.IncludeDeleted bool` — when true and `List == nil`, `List` adds no `deleted_at` condition.

- [ ] **Step 1: Write the failing test** `TestSyncFullIncludesTrashedTasks`: create two tasks via `POST /api/tasks`, soft-delete one through `/api/sync/push` (`fields: ["deleted_at"]`), `GET /api/sync/full`; assert both ids are in `tasks` and the trashed one has non-null `deleted_at`.
- [ ] **Step 2: Run** `go test ./internal/handler -run TestSyncFullIncludesTrashedTasks -v` — Expected: FAIL (one task).
- [ ] **Step 3: Implement** `IncludeDeleted`; `Full` calls `tasks.List(model.TaskFilters{IncludeDeleted: true})`. Read the cursor **before** the table reads (spec allows this; pull re-applies full snapshots, so overlap is harmless). Check projects/areas/tags/headings/children lists for a `deleted_at IS NULL` filter that hides rows the Trash view needs; trashed projects/areas are not shown in Trash today, so only fix tasks unless a test proves otherwise.
- [ ] **Step 4: Add test** `TestSyncFullCursorEqualsLatestSeq`: with no concurrent writes, `cursor` equals the latest change-log seq. The before-the-reads ordering is not testable without a hook; state it in a comment above the cursor read.
- [ ] **Step 5: Run** `go test ./internal/handler -run TestSyncFull -v` — Expected: PASS.
- [ ] **Step 6:** Document in `docs/api.md` (`GET /api/sync/full`): trashed tasks included; cursor taken before the snapshot.
- [ ] **Step 7: Commit** `fix: include trashed tasks in full sync`

### Task 3: Idempotent child creates in sync push (P0.3)

**Files:**
- Modify: `internal/handler/sync.go` — `applyScheduleChange`, `applyChecklistChange`, `applyAttachmentChange`, `applyReminderChange` create branches
- Test: `internal/handler/sync_test.go`

**Interfaces:**
- Consumes: existing repository `GetByID` (or equivalent) per entity; add `GetByID(id string)` to a repository only if missing.

- [ ] **Step 1: Write the failing table test** `TestSyncPushChildCreateIsIdempotent` with cases `schedule`, `checklistItem`, `attachment` (type `link`, url `https://example.com`), `reminder` (`type: "at_start"`): push the same create twice for a task created via REST; assert both results `status == "applied"` and the second has no `error`; assert one row exists. Second table `TestSyncPushChildCreateOtherParentConflicts`: push the same id with a different `task_id`; assert `status == "error"`, `code == "CONFLICT"` (the `code` field arrives in Task 5; for now assert `error` contains `"already exists"`).
- [ ] **Step 2: Run** `go test ./internal/handler -run ChildCreate -v` — Expected: FAIL (`internal error`).
- [ ] **Step 3: Implement** at the top of each create branch, after id validation: load the existing row by id; same parent → `Status = "applied"` and return; different parent → `syncFieldError(result, "id", "already exists for another task")`.
- [ ] **Step 4:** Check REST creates of the same four entities: if they accept a client `id`, add the same rule (`200` with the existing body for same parent, `409 CONFLICT` for another parent) with one test each in the entity's handler test file; if they do not accept one, skip.
- [ ] **Step 5: Run** `go test ./internal/handler -v` — Expected: PASS.
- [ ] **Step 6: Commit** `fix: make sync child creates idempotent`

### Task 4: Font size range 12–20 (P0.4)

**Files:**
- Modify: `internal/handler/validate.go:94,116-117`, `docs/api.md`, OpenAPI file
- Test: `internal/handler/validate_test.go` (or `user_settings_test.go`, whichever holds settings validation tests)

- [ ] **Step 1: Write the failing test** `TestSettingsFontSizeRange`: PATCH `/api/user/settings` with `font_size` 12, 15, 17, 20 → 200; 11 and 21 → 400 with `field == "font_size"` and message `"must be an integer from 12 to 20"`.
- [ ] **Step 2: Run** — Expected: FAIL on 15.
- [ ] **Step 3: Implement**: replace the `fontSizes` set with `12 <= v && v <= 20`; message exactly `must be an integer from 12 to 20`. Update `docs/api.md` and the OpenAPI schema (`minimum: 12`, `maximum: 20`).
- [ ] **Step 4: Run** `go test ./...` — Expected: PASS (drift test included).
- [ ] **Step 5: Commit** `fix: accept every font size from 12 to 20`

### Task 5: Per-change error codes and ordered results (P1.2 server part)

**Files:**
- Modify: `internal/handler/sync.go` (`SyncPushResult`, `syncErrorMessage`, `syncFieldError`, `fieldTypeError`, every `result.Status = "error"` site)
- Modify: `docs/api.md` (sync push), OpenAPI file
- Test: `internal/handler/sync_test.go`

**Interfaces:**
- Produces: `SyncPushResult.Code string \`json:"code,omitempty"\`` ∈ `VALIDATION | NOT_FOUND | CONFLICT | INTERNAL`.
- Produces: `func syncFail(result SyncPushResult, err error) SyncPushResult` — sets `Status = "error"`, `Error = syncErrorMessage(err)`, `Code = syncErrorCode(err)`.
- Produces: `func syncErrorCode(err error) string` — duplicate project/area/tag name and `ErrHasProjects` → `CONFLICT`; error text containing `duplicate timeless date` → `VALIDATION`; not-found sentinels (`sql.ErrNoRows`, `ErrAttachmentNotFound`, any `… not found`) → `NOT_FOUND`; else `INTERNAL`.
- `syncFieldError`: `Code = "NOT_FOUND"` when `msg` ends with `not found`, else `VALIDATION`. `fieldTypeError`, `unsupported entity type`, `unsupported action`, `task_id is required` → `VALIDATION`.
- `syncErrorMessage`: `duplicate timeless date` returns `when_date: duplicate timeless date` instead of `internal error`.

- [ ] **Step 1: Write the failing tests**:
  - `TestSyncPushResultCodes` table: invalid `when_date: "tomorrow"` → `VALIDATION`; schedule create for missing `task_id` → `NOT_FOUND`; duplicate project name → `CONFLICT`; `unsupported entity type` → `VALIDATION`.
  - `TestSyncPushDuplicateTimelessIsValidation`: task with an untimed schedule today plus a second untimed schedule tomorrow; push a task update `when_date` = tomorrow's date that hits `syncFirstScheduleDate`; assert `code == "VALIDATION"` and `error == "when_date: duplicate timeless date"`.
  - `TestSyncPushResultsKeepRequestOrder`: push 5 mixed changes (2 failing); assert `results[i].entity_id == changes[i].entity_id` for all i.
- [ ] **Step 2: Run** `go test ./internal/handler -run 'ResultCodes|DuplicateTimeless|RequestOrder' -v` — Expected: FAIL.
- [ ] **Step 3: Implement** the interfaces above; replace every `result.Status = "error"; result.Error = syncErrorMessage(err)` pair with `return syncFail(result, err)`. Update Task 3's conflict assertion to `code == "CONFLICT"`.
- [ ] **Step 4: Document** in `docs/api.md`: `code` per result, permanent vs transient meaning, results in request order, and (P1.5) "conflicts are client-wins: a queued offline edit overwrites a newer server write; `conflict_resolved` is informational". OpenAPI: add `code` enum to the push result schema.
- [ ] **Step 5: Run** `go test ./...` — Expected: PASS.
- [ ] **Step 6: Commit** `feat: classify sync push errors with machine codes`

### Task 6: Task changes apply every field (P1.6)

**Files:**
- Modify: `internal/handler/sync.go:440-625` (`applyTaskChange` update branch)
- Test: `internal/handler/sync_test.go`

- [ ] **Step 1: Write the failing tests**:
  - `TestSyncPushStatusChangeKeepsOtherFields`: update with `fields: ["title","status"]`, `data: {title: "Renamed", status: "completed"}` → task is completed **and** titled `Renamed`.
  - `TestSyncPushRestoreKeepsOtherFields`: `fields: ["deleted_at","notes"]`, `deleted_at: null`, `notes: "n"` on a trashed task → restored and notes `n`.
  - `TestSyncPushFieldsThenStatusIsAtomic`: invalid `when_date` plus `status: completed` → result `VALIDATION` and the task is still open.
- [ ] **Step 2: Run** — Expected: FAIL.
- [ ] **Step 3: Implement**: collect `status` and `deleted_at` instead of returning; validate and apply the plain fields first (`tasks.Update`), then the restore/delete, then the status transition (`FinishTask` / `Reopen`); any validation failure returns before writing. Broadcast one event per change (the most significant: deleted > updated).
- [ ] **Step 4: Run** `go test ./internal/handler -v` — Expected: PASS.
- [ ] **Step 5: Commit** `fix: apply every field of a sync task change`

---

## Phase 1 — Sync engine (frontend)

### Task 7: Queue entry state and skipped-snapshot table (schema)

**Files:**
- Modify: `frontend/src/db/schema.ts`, `frontend/src/db/index.ts`
- Test: `frontend/src/db/__tests__/mutations.test.ts` (or new `schema.test.ts`)

**Interfaces:**
- Produces in `schema.ts`:

```ts
export type SyncErrorCode = 'VALIDATION' | 'NOT_FOUND' | 'CONFLICT' | 'INTERNAL' | 'TOO_LARGE'
export interface SyncQueueEntry {
  // existing fields …
  state?: 'pending' | 'stuck'      // undefined === 'pending'
  errorCode?: SyncErrorCode
  errorMessage?: string
  internalFailures?: number         // consecutive INTERNAL results
}
export interface SkippedSnapshot {
  entity: SyncEntity
  entityId: string
  action: 'create' | 'update' | 'delete'
  snapshot: Record<string, unknown>
  seq: number
}
```

- Produces in `index.ts`: `skippedSnapshots!: Table<SkippedSnapshot, [SyncEntity, string]>`; `this.version(4).stores({ …v3, skippedSnapshots: '[entity+entityId]' })`.

- [ ] **Step 1: Write the failing test** `stores and replaces a skipped snapshot per entity`: put two snapshots for `['task','t1']` with seq 3 then 5; `get(['task','t1'])` returns seq 5; count is 1.
- [ ] **Step 2: Run** `cd frontend && npx vitest run src/db` — Expected: FAIL (no table).
- [ ] **Step 3: Implement** the types and version 4.
- [ ] **Step 4: Run** — Expected: PASS; `npx tsc -b` clean.
- [ ] **Step 5: Commit** `feat: add queue entry state and skipped snapshots to local db`

### Task 8: Push removes by queue id and classifies results (P1.2)

**Files:**
- Modify: `frontend/src/sync/push.ts`
- Test: `frontend/src/sync/__tests__/push.test.ts`

**Interfaces:**
- Consumes: Task 7 types; server `code` (Task 5).
- Produces:
  - `export function isPermanent(code: SyncErrorCode | undefined): boolean` — true for `VALIDATION`, `NOT_FOUND`, `CONFLICT`, `TOO_LARGE`.
  - `export const INTERNAL_STUCK_THRESHOLD = 5`
  - `PushResult` gains `code?: SyncErrorCode`.
  - `PushSummary` gains `stuckCount: number`.
  - Result `i` belongs to sent entry `i` (positional).
  - Success (`applied`/`conflict_resolved`) → delete the entry by its `id` and delete `skippedSnapshots[[entity, entityId]]` when no other queue entry for that entity remains.
  - Permanent error → `state: 'stuck'`, `errorCode`, `errorMessage`.
  - `INTERNAL` or missing code → `internalFailures += 1`, keep `state` pending; when it reaches 5 also set `state: 'stuck'` but keep it in push selection (auto-retry continues for `INTERNAL` stuck entries only).

- [ ] **Step 1: Write the failing tests**:
  - `removes only the succeeded entry when siblings share an entity`: queue `task-1` update A (applied) and `task-1` update B (`error`, `VALIDATION`); after push the queue holds only B with `state: 'stuck'`.
  - `marks permanent errors stuck and stops sending them`: second `pushChanges` call does not include the stuck entry in the POST body.
  - `INTERNAL errors stay pending and become visible after 5`: five pushes with `INTERNAL` → `internalFailures === 5`, `state === 'stuck'`, and the sixth push still sends it.
  - `success clears the skipped snapshot of a drained entity`.
- [ ] **Step 2: Run** `npx vitest run src/sync/__tests__/push.test.ts` — Expected: FAIL.
- [ ] **Step 3: Implement** per the Interfaces. Drop the `unsupported entity type` special case (the server now codes it `VALIDATION`, so it becomes stuck and visible).
- [ ] **Step 4: Run** — Expected: PASS.
- [ ] **Step 5: Commit** `fix: track sync queue results per entry`

### Task 9: Batching, 413 halving, per-entity FIFO and parent hold (P0.5, P1.2)

**Files:**
- Modify: `frontend/src/sync/push.ts`
- Test: `frontend/src/sync/__tests__/push.test.ts`

**Interfaces:**
- Produces:
  - `export const PUSH_BATCH_SIZE = 100`
  - `export function selectPushable(entries: SyncQueueEntry[]): SyncQueueEntry[]` — input in queue order (`id` ascending). Skips: a permanently stuck entry; every later entry of an entity that has an earlier stuck entry (any code except `INTERNAL`); a child entry (`schedule`, `reminder`, `checklistItem`, `attachment` by `data.task_id`; `heading` by `data.project_id`) whose parent's queued `create` is stuck or is not itself selected earlier in this selection. A child may share a request with its parent create only when the create comes first.
  - Requests are sent one at a time; the next waits for the previous result; stop on the first non-2xx (network error, 401, 403, 5xx). On `ApiError.status === 413`: retry the same entries with half the batch size; a batch of one that still gets 413 → that entry `state: 'stuck'`, `errorCode: 'TOO_LARGE'`, `errorMessage: 'change is larger than the 10 MiB request limit'`, then continue with the next entries.

- [ ] **Step 1: Write the failing tests**:
  - `sends at most 100 changes per request`: 250 entries → 3 POSTs of 100, 100, 50 in queue order.
  - `halves the batch on 413`: mock first POST rejects `new ApiError(413, '')`, then accept; assert second POST had 50 changes.
  - `marks a single oversized change TOO_LARGE`: batch of 1 rejected twice with 413 → entry stuck with `TOO_LARGE`, next entry still sent.
  - `holds later changes of a stuck entity`: entries [A1 stuck VALIDATION, A2 pending, B1 pending] → POST contains only B1.
  - `holds a child behind a stuck parent create`: [task T create stuck, schedule S create (task_id T)] → nothing sent; after T applied in a later push, S is sent.
  - `sends parent create and child in one request in order`: [T create, S create] both sent, T first.
- [ ] **Step 2: Run** — Expected: FAIL.
- [ ] **Step 3: Implement** `selectPushable` and the batching loop.
- [ ] **Step 4: Run** — Expected: PASS.
- [ ] **Step 5: Commit** `fix: push the sync queue in ordered batches`

### Task 10: Pull keeps skipped snapshots; full sync stamps rows and replays the queue (P1.1, P1.1a, P1.2)

**Files:**
- Create: `frontend/src/sync/replay.ts`
- Modify: `frontend/src/sync/pull.ts`
- Test: `frontend/src/sync/__tests__/pull.test.ts`, new `frontend/src/sync/__tests__/fullSync.test.ts`

**Interfaces:**
- Produces in `replay.ts`: `export async function applyQueueEntryLocally(entry: SyncQueueEntry): Promise<void>` — create → `put` `{...entry.data, id: entry.entityId, _syncStatus: 'pending'}`; update → `update(entityId, pick(entry.data, entry.fields))`; delete → mirror what `db/mutations.ts` does locally for that entity (soft delete for tasks, row delete otherwise). Uses the same queue-entity → table map as `pull.ts` (export it from `pull.ts` as `QUEUE_ENTITY_TABLE`).
- Produces in `pull.ts`: `applyChange` stores `{entity, entityId, action, snapshot, seq}` in `skippedSnapshots` when it skips a pending entity; `export async function restoreFromSkippedSnapshot(entity: SyncEntity, entityId: string): Promise<void>` — applies the stored snapshot (delete action with no `deleted_at` → delete the row), removes the record; no record → leaves the row.
- `fullSync`: every `withMeta` call passes `response.cursor` as `seq`; after the bulk puts, for each queue entry in `id` order call `applyQueueEntryLocally`, and for each entity touched store its full-sync row (if any) as its skipped snapshot with `seq = response.cursor`, `action: 'update'` (or `'delete'` when the row is absent).

- [ ] **Step 1: Write the failing tests**:
  - `pull.test.ts` `stores a skipped snapshot when the entity has a pending entry`.
  - `pull.test.ts` `restoreFromSkippedSnapshot applies the latest snapshot` and `… deletes the row when the latest skipped change was a hard delete`.
  - `fullSync.test.ts` `stamps every row with the full-sync cursor`: mock `/sync/full` with one task and two schedules, cursor 42 → every row has `_serverSeq === 42`.
  - `fullSync.test.ts` `a later schedule create keeps confirmed schedules` (H2): after the full sync, apply a pulled schedule `create` for the same task → the two earlier schedules remain.
  - `fullSync.test.ts` `fullSync re-applies a stuck create and its pending child`: queue [task T create stuck, schedule S create]; full sync response without T → T and S exist locally afterwards.
  - `fullSync.test.ts` `fullSync re-applies a stuck update over the server row and keeps the server row as the skipped snapshot`.
- [ ] **Step 2: Run** `npx vitest run src/sync` — Expected: FAIL.
- [ ] **Step 3: Implement** per the Interfaces.
- [ ] **Step 4: Run** — Expected: PASS.
- [ ] **Step 5: Commit** `fix: keep pending edits and server snapshots across full sync`

### Task 11: Stuck-changes list with Retry and Discard (P1.2 UI)

**Files:**
- Create: `frontend/src/sync/queueActions.ts`, `frontend/src/components/StuckChangesDialog.tsx`
- Modify: `frontend/src/sync/status.ts` (add `stuckCount`), `frontend/src/sync/engine.ts` (set `stuckCount` after each push), `frontend/src/components/SyncStatus.tsx`
- Test: `frontend/src/sync/__tests__/queueActions.test.ts`, `frontend/src/components/__tests__/StuckChangesDialog.test.tsx`

**Interfaces:**
- Consumes: Task 7–10.
- Produces in `queueActions.ts`:
  - `listStuckEntries(): Promise<SyncQueueEntry[]>` — `state === 'stuck'`, queue order.
  - `entriesRemovedByDiscard(entryId: number): Promise<SyncQueueEntry[]>` — the entry, plus all later entries of the same entity, plus, if it is a `create`, every queued entry whose `data.task_id` / `data.project_id` names the created entity (transitively).
  - `discardEntry(entryId: number): Promise<void>` — deletes exactly `entriesRemovedByDiscard(entryId)`; if the discarded set contains the entity's `create`, deletes the local row (and children rows from the set); otherwise `restoreFromSkippedSnapshot(entity, entityId)`; then `syncNow()`.
  - `retryEntry(entryId: number): Promise<void>` — clears `state`, `errorCode`, `errorMessage`, `internalFailures`; then `syncNow()`.
- `useSyncStore` gains `stuckCount: number` and `setStuckCount(n: number)`.
- `SyncStatus`: when `stuckCount > 0`, label `${stuckCount} stuck` and the click opens `StuckChangesDialog` instead of `syncNow()`.
- `StuckChangesDialog`: one row per stuck entry: entity, title or id (task title from `localDb.tasks` when available), `errorMessage`; buttons **Retry** and **Discard**. Discard opens `ConfirmDialog` listing every entry from `entriesRemovedByDiscard` (one line each: action + entity + title/id); confirm label `Discard N change(s)`.

- [ ] **Step 1: Write the failing tests**:
  - `queueActions.test.ts`: `discard of an update restores the skipped snapshot`; `discard of a create removes its children and the local rows`; `retry clears the stuck state`; `entriesRemovedByDiscard includes later entries of the same entity`.
  - `StuckChangesDialog.test.tsx`: `lists stuck entries with their server message`; `discard asks for confirmation listing every affected entry`; `cancel removes nothing` (queue count unchanged after cancel).
- [ ] **Step 2: Run** — Expected: FAIL.
- [ ] **Step 3: Implement** per the Interfaces, using `ConfirmDialog` and existing Radix Dialog patterns.
- [ ] **Step 4: Run** `npx vitest run src/sync src/components` — Expected: PASS.
- [ ] **Step 5: Commit** `feat: show stuck sync changes with retry and discard`

### Task 12: Producers stop sending invalid data (P1.3)

**Files:**
- Modify: `frontend/src/components/QuickEntry.tsx:111-121`, `frontend/src/components/ScheduleEditor.tsx:29-35`, `frontend/src/hooks/queries.ts:872-895`
- Test: `frontend/src/components/__tests__/QuickEntry.test.tsx` (new), `frontend/src/components/__tests__/ScheduleEditor.test.ts` (new, for the exported helper), `frontend/src/hooks/__tests__/queries.test.tsx`

**Interfaces:**
- Produces: `export function defaultEndTime(start: string, gapMinutes: number): string | null` in `ScheduleEditor.tsx` (replaces the clamping `addMinutes` use) — returns `null` when `start + gap` passes `23:59`.

- [ ] **Step 1: Write the failing tests**:
  - `QuickEntry` `blocks saving when the deadline is before the when date`: type a title, set when `2026-10-20`, deadline `2026-10-19`, submit → no task in `localDb.tasks`, inline text `Deadline can't be before the start date` visible.
  - `defaultEndTime('23:30', 60) === null`; `defaultEndTime('22:30', 60) === '23:30'`; `defaultEndTime('23:00', 59) === '23:59'`.
  - `useUpdateTaskSchedule` `does not create a synthetic schedule without a date`: task with no `when_date` and no schedules; `mutate({ start_time: '09:00' })` → no schedule row and no queue entry; the hook returns an error `Pick a date before setting a time`.
- [ ] **Step 2: Run** — Expected: FAIL.
- [ ] **Step 3: Implement**: QuickEntry validation with the exact inline copy above; `defaultEndTime`; in the synthetic-schedule branch throw `new Error('Pick a date before setting a time')` when `when_date` is empty (TaskDetail surfaces mutation errors as it does today).
- [ ] **Step 4: Run** — Expected: PASS.
- [ ] **Step 5: Commit** `fix: stop the UI from queueing changes the server rejects`

### Task 13: First full sync can fail (H4, P1.4)

**Files:**
- Modify: `frontend/src/components/AppLayout.tsx:44-62`, `frontend/src/components/SyncingOverlay.tsx`
- Test: `frontend/src/components/__tests__/AppLayout.test.tsx` (new)

**Interfaces:**
- `SyncingOverlay` props: `{ show: boolean; error?: string | null; onRetry?: () => void }` — with `error`, shows `Couldn't load your data: {error}` and a **Retry** button.

- [ ] **Step 1: Write the failing tests** (msw: `/api/auth/me` 200, `/api/sync/full` 500):
  - `empty local db: shows the error screen with Retry` — overlay text `Couldn't load your data` visible; clicking Retry with `/sync/full` now 200 hides it.
  - `local data present: starts offline without the overlay` — seed one task; `/sync/full` never called; `startSyncEngine` called (spy).
- [ ] **Step 2: Run** — Expected: FAIL (overlay stuck).
- [ ] **Step 3: Implement** try/catch around `fullSync()`; keep `initialSyncing` true only while loading; store the error for the overlay; Retry re-runs `init`.
- [ ] **Step 4: Run** — Expected: PASS.
- [ ] **Step 5: Commit** `fix: recover when the first full sync fails`

### Task 14: One write path for multi-drag; drop-semantics tests (H1, P1.7)

**Files:**
- Modify: `frontend/src/components/AppDndContext.tsx:224-275`
- Test: `frontend/src/components/__tests__/AppDndContext.test.tsx` (new)

**Interfaces:**
- Produces (for testability): `export async function applySidebarDrop(target: SidebarDropTarget, taskIds: string[]): Promise<void>` extracted from the drop handler; uses only local mutations (`db/mutations.ts`) — no `bulkAction` import remains in the file.

- [ ] **Step 1: Write the failing tests** (fake-indexeddb, seeded tasks):
  - `multi-drop to Completed queues one update per task and calls no REST endpoint` (spy on `api.post` for `/tasks/bulk`).
  - `Inbox drop clears project, area, heading and when_date`.
  - `project drop sets project_id and inherits the project's area_id`.
  - `sidebar drop on a list restores a trashed task and reopens a completed one`.
- [ ] **Step 2: Run** — Expected: FAIL (`bulkAction` called; helper missing).
- [ ] **Step 3: Implement** the extraction; remove the REST call and its `.then`.
- [ ] **Step 4: Run** — Expected: PASS.
- [ ] **Step 5: Commit** `fix: write multi-drag drops through the sync queue only`

### Task 15: SSE reconnect, `task_purged`, own-seq filter (P1.8)

**Files:**
- Modify: `frontend/src/hooks/useSSE.ts`
- Test: `frontend/src/hooks/__tests__/useSSE.test.tsx` (new; fake `EventSource` class on `globalThis`)

**Interfaces:**
- Produces: `export function nextBackoffMs(attempt: number): number` — `min(30000, 1000 * 2 ** attempt)`.
- Seq filter: read `cursor` from `localDb.syncMeta`; ignore an event whose payload `seq` ≤ cursor.

- [ ] **Step 1: Write the failing tests**:
  - `reconnects after an error with backoff` (fake timers: after `onerror`, a new `EventSource` is created after 1000 ms, then 2000 ms on the next error; reset to attempt 0 after `open`).
  - `task_purged removes the local task`.
  - `ignores events at or below the local cursor` (no `invalidateQueries` call).
  - `nextBackoffMs(0) === 1000`, `nextBackoffMs(10) === 30000`.
- [ ] **Step 2: Run** — Expected: FAIL.
- [ ] **Step 3: Implement**; keep the existing `online`/`visibilitychange` reconnects.
- [ ] **Step 4: Run** — Expected: PASS.
- [ ] **Step 5: Commit** `fix: reconnect SSE and handle purged tasks`

### Task 16: Logout clears local data (P1.9)

**Files:**
- Create: `frontend/src/sync/clearLocalData.ts`
- Modify: `frontend/src/hooks/queries.ts:954-963` (`useLogout`), `frontend/src/hooks/useSSE.ts` (export `closeSSE`) and the logout button's component (find with `grep -rn useLogout frontend/src`)
- Test: `frontend/src/hooks/__tests__/queries.test.tsx`

**Interfaces:**
- Produces: `export async function clearLocalData(): Promise<void>` in new `frontend/src/sync/clearLocalData.ts` (not in `db/index.ts`, which `engine.ts` imports) — `stopSyncEngine()`, closes SSE (export `closeSSE()` from `useSSE.ts` that closes the current source), `localDb.delete()` then `localDb.open()`; removes the `thingstodo_timezone` localStorage key (Task 20).
- Before logout, when `syncQueue.count() > 0`, the UI shows `ConfirmDialog` with `You have N unsynced change(s). Logging out will lose them.` and confirm label `Log out anyway`.

- [ ] **Step 1: Write the failing tests**: `logout clears every Dexie table including the queue`; `logout with a pending queue asks for confirmation first` (component test of the logout control).
- [ ] **Step 2: Run** — Expected: FAIL.
- [ ] **Step 3: Implement**.
- [ ] **Step 4: Run** — Expected: PASS.
- [ ] **Step 5: Commit** `fix: clear local data on logout`

---

## Phase 2 — One timezone, one set of view rules

### Task 17: Server reports the owner's timezone (P2.1 server)

**Files:**
- Modify: `internal/config/config.go` (add `TimeZoneName string` to `Config`, set in load), `internal/handler/auth.go:125` (`AuthConfig`), `docs/api.md`, OpenAPI file
- Test: `internal/config/config_test.go`, `internal/handler/auth_test.go`

**Interfaces:**
- Produces: `func zoneName(loc *time.Location, localtimeTarget func() (string, error)) string` — `loc.String()` when it is not `"Local"`; else the IANA name after `zoneinfo/` in the `/etc/localtime` symlink target; else `"UTC"`. In load: `cfg.TimeZoneName = zoneName(cfg.Location, func() (string, error) { return os.Readlink("/etc/localtime") })`.
- `GET /api/auth/config` → `{"auth_mode": "...", "timezone": "<IANA>"}`.

- [ ] **Step 1: Write the failing tests**: `TestZoneName` table — `Europe/Moscow` location → `Europe/Moscow`; `time.Local` with target `/usr/share/zoneinfo/Asia/Tokyo` → `Asia/Tokyo`; `time.Local` with target error → `UTC`. `TestAuthConfigIncludesTimezone` → body has `timezone == cfg.TimeZoneName`.
- [ ] **Step 2: Run** `go test ./internal/config ./internal/handler -run 'ZoneName|AuthConfig' -v` — Expected: FAIL.
- [ ] **Step 3: Implement**. When `TZ` is unset and the name resolves from `/etc/localtime`, also set `cfg.Location` via `time.LoadLocation(name)` so the server uses the zone it reports.
- [ ] **Step 4: Update** `docs/api.md` and OpenAPI; run `go test ./...` — Expected: PASS.
- [ ] **Step 5: Commit** `feat: expose the server timezone in auth config`

### Task 18: Owner-time helpers (P2.1 frontend)

**Files:**
- Create: `frontend/src/lib/owner-time.ts`
- Test: `frontend/src/lib/__tests__/owner-time.test.ts`

**Interfaces:**
- Produces:

```ts
export function setOwnerTimeZone(tz: string): void      // also caches in localStorage 'thingstodo_timezone'
export function getOwnerTimeZone(): string              // cached value, else Intl browser zone
export function ownerToday(now?: Date): string          // YYYY-MM-DD in the owner zone
export function ownerDateOf(isoUtc: string): string     // date of a UTC timestamp ('YYYY-MM-DD HH:MM:SS' or RFC3339) in the owner zone
export function addDays(date: string, n: number): string
export function ownerDayBoundsUtc(date: string): { start: string; end: string } // RFC3339 UTC bounds [start, end)
export function ownerLocalInputToIso(local: string): string // 'YYYY-MM-DDTHH:mm' in owner zone → RFC3339 with offset
export function isoToOwnerLocalInput(iso: string): string   // inverse, for <input type="datetime-local">
export async function loadOwnerTimeZone(): Promise<void>     // GET /api/auth/config → setOwnerTimeZone; ignores network errors
export function useOwnerToday(): string                      // re-renders when the owner date changes (timer to next owner midnight)
```

Use `Intl.DateTimeFormat(..., { timeZone, ... }).formatToParts` for zone math; no library.

- [ ] **Step 1: Write the failing tests** (set `process.env.TZ = 'UTC'` for the test file):
  - `ownerToday uses the owner zone, not the browser zone`: zone `Europe/Moscow`, now `2026-10-18T21:30:00Z` → `2026-10-19`.
  - `ownerDateOf('2026-10-18 21:30:00') === '2026-10-19'` in Moscow.
  - `ownerDayBoundsUtc('2026-10-19')` → `{ start: '2026-10-18T21:00:00Z', end: '2026-10-19T21:00:00Z' }`.
  - `ownerLocalInputToIso('2026-10-18T21:30') === '2026-10-18T21:30:00+03:00'`; `isoToOwnerLocalInput('2026-10-18T18:30:00Z') === '2026-10-18T21:30'`.
  - `addDays('2026-02-28', 1) === '2026-03-01'`.
  - `useOwnerToday re-renders at owner midnight` (fake timers).
- [ ] **Step 2: Run** — Expected: FAIL.
- [ ] **Step 3: Implement**.
- [ ] **Step 4: Run** — Expected: PASS.
- [ ] **Step 5: Commit** `feat: add owner timezone date helpers`

### Task 19: Use owner time everywhere (P2.1 sites, P2.2)

**Files:**
- Modify: `frontend/src/components/AppLayout.tsx` (call `loadOwnerTimeZone()` before `fullSync`), `AppDndContext.tsx:227,516`, `TaskDetail.tsx:252` and `:1116-1178` (exact reminder), `ScheduleEditor.tsx:175,185,310`, `SearchOverlay.tsx:62`, `frontend/src/hooks/localQueries.ts` (`todayString`, `tomorrowString`, review cutoff `:299`, `:1029`, completed-today `:467-474`, Logbook `:781`, Trash `:807`), `frontend/src/lib/date-parser.ts` (its "today")
- Test: `frontend/src/hooks/__tests__/localQueries.test.tsx`, `frontend/src/components/__tests__/TaskDetail.reminder.test.tsx` (new)

- [ ] **Step 1: Write the failing tests**:
  - `localQueries` `completed today uses the owner day` (Moscow, now `2026-10-18T22:00:00Z`, a task completed at `2026-10-18 21:30:00` UTC appears in Today's completed group; one completed at `2026-10-18 20:00:00` does not).
  - `logbook groups by owner date` (same timestamps → groups `2026-10-19` and `2026-10-18`).
  - `TaskDetail` `exact reminder is sent with the owner offset` (input `2026-10-20T09:00` → queued reminder `exact_at === '2026-10-20T09:00:00+03:00'`) and `prefills a stored UTC value in owner time`.
- [ ] **Step 2: Run** — Expected: FAIL.
- [ ] **Step 3: Implement**: replace every listed site with the helpers; live queries use `useOwnerToday()` as a dependency so lists roll over at midnight. `grep -rn "toISOString().s" frontend/src` must return no date-only uses afterwards (timestamps for `updated_at` stay).
- [ ] **Step 4: Run** `npm test` — Expected: PASS.
- [ ] **Step 5: Commit** `fix: compute dates in the owner timezone`

### Task 20: Server view fixes (P2.3 server)

**Files:**
- Modify: `internal/repository/views.go:193,226` (Today joins), `:955-…` (`Counts`)
- Test: `internal/repository/views_test.go`

- [ ] **Step 1: Write the failing tests**:
  - `TestTodayHidesCompletedTimeBlocks`: open task with today entries 09:00 (completed) and 19:00 (open) → Today has one row, in Evening, entry 19:00.
  - `TestTodayTaskWithOnlyCompletedEntryShowsOnce`: open task in Today by membership whose only today entry is completed → one row with no start time.
  - `TestCountsExcludeTasksOfClosedProjects`: task in a completed project with no date → `counts.anytime` excludes it and equals `len(Anytime())`; same for Someday.
- [ ] **Step 2: Run** `go test ./internal/repository -run 'Today|Counts' -v` — Expected: FAIL.
- [ ] **Step 3: Implement**: add `AND ts.completed = 0` to both Today `LEFT JOIN` conditions; Counts Anytime/Someday use the same project/area conditions as `buildAnytimeView`.
- [ ] **Step 4: Run** `go test ./...` — Expected: PASS.
- [ ] **Step 5: Commit** `fix: hide completed time blocks in Today and align counts`

### Task 21: Shared contract fixtures and the Go runner (P2.4)

**Files:**
- Create: `testdata/views/README.md` (format, 10 lines), `testdata/views/basic.json`, `testdata/views/midnight-moscow.json`, `testdata/views/multi-schedule.json`, `testdata/views/projects-and-tags.json`
- Create: `internal/repository/views_contract_test.go`

**Interfaces:**
- Fixture format (one JSON object per file):

```json
{
  "name": "midnight-moscow",
  "now": "2026-10-18T21:30:00Z",
  "timezone": "Europe/Moscow",
  "evening_starts_at": "18:00",
  "seed": {
    "areas": [{"id": "a1", "title": "Home"}],
    "projects": [{"id": "p1", "title": "P", "area_id": "a1", "status": "open"}],
    "headings": [], "tags": [{"id": "g1", "title": "x", "parent_tag_id": null}],
    "tasks": [{"id": "t1", "title": "T", "status": "open", "when_date": "2026-10-19", "project_id": null, "area_id": null,
               "sort_order_today": 1, "tag_ids": ["g1"], "completed_at": null, "deleted_at": null}],
    "schedules": [{"id": "s1", "task_id": "t1", "when_date": "2026-10-19", "start_time": null, "end_time": null, "completed": false, "sort_order": 0}]
  },
  "expect": {
    "inbox": [], "anytime": [], "someday": [], "logbook": [], "trash": [],
    "today": [{"task": "t1", "section": "today", "entry": "s1"}],
    "upcoming": [{"task": "t3", "date": "2026-10-20", "entry": "s3"}],
    "project:p1": [{"task": "t2"}], "area:a1": [], "tag:g1": [{"task": "t1"}],
    "counts": {"inbox": 0, "today": 1, "overdue": 0, "anytime": 0, "someday": 0, "logbook": 0, "trash": 0}
  }
}
```

  `section` ∈ `today | evening | overdue | earlier`. Seed fields omitted take the DB defaults. Rows compare in order.
- Go runner `TestViewContractFixtures`: for each `testdata/views/*.json` (path `../../testdata/views` from the package), `t.Run(name)`: fresh `testutil.SetupTestDB`, insert seed rows with SQL, `ViewRepository` with `SetClock(fixed now)` and `loc` from `timezone`, compare each `expect` key with the matching repository method (`Inbox`, `Today(evening_starts_at)`, `Upcoming(today)`, `Anytime`, `Someday`, `Logbook(1000,0)`, `Trash(1000,0)`, `Counts`; `project:`/`area:`/`tag:` via the repository methods behind `GET /api/projects/{id}`, `/api/areas/{id}`, `/api/tags/{id}/tasks`).

- [ ] **Step 1: Write** the four fixtures covering: near-midnight Moscow; multi-schedule (two today entries, one evening, one completed); deadline-only loose task; task in a completed project; overdue and Earlier; nested tags; project with headings and manual `sort_order_project`. Expectations come from the spec rules (Q2, P2.3), not from running the code.
- [ ] **Step 2: Write** the runner; run `go test ./internal/repository -run TestViewContractFixtures -v` — Expected: PASS after Task 20; any FAIL is a server bug against the spec: fix it in this task with a focused test, or, if the fixture contradicts the spec, fix the fixture and note why in the commit body.
- [ ] **Step 3: Commit** `test: add shared view contract fixtures`

### Task 22: Local Today/Upcoming match the server (P2.3, fixtures)

**Files:**
- Modify: `frontend/src/hooks/localQueries.ts` (`useLocalToday` `:347-500`, `useLocalUpcoming` `:505-610`, `enrichTasks` time selection `:218-229`)
- Create: `frontend/src/hooks/__tests__/viewsContract.test.ts`
- Modify: `frontend/src/components/AppDndContext.tsx` (Today reorder: any row moves the whole task)

**Interfaces:**
- Produces in the test file: `async function loadFixture(name: string): Promise<Fixture>` reading `../../../../testdata/views/<name>.json` via `node:fs`; `seedLocalDb(fixture.seed)`; pins time with `vi.setSystemTime(fixture.now)` and `setOwnerTimeZone(fixture.timezone)`; renders each local hook with `renderHook` and maps results to the fixture row shape.
- Local Today: one row per **open** today schedule entry (`schedule_entry_id` set), Evening per entry (`start_time >= evening_starts_at`), tasks in Today by membership with no open today entry → one row without time; order `sort_order_today`, then `start_time` (nulls first, as `views.go:196`). Upcoming order within a date: `start_time` (nulls first), then `sort_order_today`. Displayed time = today's entry time (Today) or the first open past entry (Earlier).

- [ ] **Step 1: Write** `viewsContract.test.ts` with `test.each(fixtureNames)('%s: today and upcoming match', …)` comparing `today` and `upcoming` only.
- [ ] **Step 2: Run** `npx vitest run src/hooks/__tests__/viewsContract.test.ts` — Expected: FAIL.
- [ ] **Step 3: Implement** the local changes; update the Today drag handler so reordering any row of a task writes that task's `sort_order_today`.
- [ ] **Step 4: Run** `npm test` — Expected: PASS (existing `localQueries.test.tsx` updated only where it pinned the old one-row-per-task rule; note each changed assertion in the commit body).
- [ ] **Step 5: Commit** `fix: local Today and Upcoming follow the server rules`

### Task 23: Local lists, Anytime/Someday, Logbook/Trash and counts match (P2.3, M1)

**Files:**
- Modify: `frontend/src/hooks/localQueries.ts` (`sortTasks` callers `:328-334`, `useLocalAnytime`, `useLocalSomeday`, `useLocalProjectTasks`, `useLocalAreaTasks`, `useLocalTagTasks`, `useLocalProjectDetail`, `useLocalAreaDetail`, `useLocalLogbook`, `useLocalTrash`, `useLocalViewCounts`)
- Modify: `frontend/src/hooks/__tests__/viewsContract.test.ts`

- [ ] **Step 1: Extend** the contract test to every remaining `expect` key (`inbox`, `anytime`, `someday`, `logbook`, `trash`, `project:*`, `area:*`, `tag:*`, `counts`).
- [ ] **Step 2: Run** — Expected: FAIL.
- [ ] **Step 3: Implement**: order project/heading lists by `sort_order_project`, area lists by the server's area sort column, tag lists as the server; Anytime/Someday exclude tasks whose project is not `open` or whose area is missing; Logbook/Trash group by `ownerDateOf`.
- [ ] **Step 4: Add test** `drag reorder in a project survives re-render` in `localQueries.test.tsx` (set `sort_order_project` of two tasks swapped → order follows it).
- [ ] **Step 5: Run** `npm test` — Expected: PASS.
- [ ] **Step 6: Commit** `fix: local lists and counts follow the server rules`

---

## Phase 3 — Remaining UI bugs

### Task 24: Date parser today and overflow (M3)

**Files:**
- Modify: `frontend/src/lib/date-parser.ts:113-130`
- Test: `frontend/src/lib/__tests__/date-parser.test.ts`

- [ ] **Step 1: Write the failing tests** with `vi.setSystemTime` at `09:00`, `15:00` and `23:30` owner time on 2026-10-16 (Moscow): `"oct 16"` → `2026-10-16`; `"oct 15"` → `2027-10-15`; `"feb 31"` → `null`; `"2/30"` → `null`. Remove the midnight pin at the top of the file.
- [ ] **Step 2: Run** — Expected: FAIL.
- [ ] **Step 3: Implement**: compare against `ownerToday()` by date string (not timestamps); reject when the constructed date's month/day differ from the input.
- [ ] **Step 4: Run** — Expected: PASS.
- [ ] **Step 5: Commit** `fix: parse today's date and reject impossible days`

### Task 25: Unicode tags and `$project` word boundary (M4)

**Files:**
- Modify: `frontend/src/lib/parse-tags.ts:6,32,50`
- Test: `frontend/src/lib/__tests__/parse-tags.test.ts` (new), `frontend/src/hooks/__tests__/useResolveTags.test.tsx` (new)

- [ ] **Step 1: Write the failing tests**: `#покупки` and `#café-2` parse as tags; `email@x.com#anchor` handling stays as today (assert current behavior first); `$Work` matches project `Work` but `$Work` does not match `Workshop` when `Work` exists, and `$Wor` matches nothing; `useResolveTags` auto-creates `покупки` once and reuses an existing tag case-insensitively.
- [ ] **Step 2: Run** — Expected: FAIL.
- [ ] **Step 3: Implement**: tag regex `/#([\p{L}\p{N}_-]+)/gu`; project match requires the name to end at a non-letter/number boundary (`(?![\p{L}\p{N}])`, `u` flag).
- [ ] **Step 4: Run** — Expected: PASS.
- [ ] **Step 5: Commit** `fix: parse Unicode tags and exact project tokens`

### Task 26: Online-only REST operations and Empty Trash (M5)

**Files:**
- Modify: `frontend/src/hooks/queries.ts:510-530,575-600,637-665` (`useReorderProjects`, `useDeleteArea`, `useReorderAreas`, `useDeleteTag`, `useReorderTags`), `frontend/src/components/AppDndContext.tsx:592-600` (task reorder), `frontend/src/pages/TrashView.tsx:19-29`
- Test: `frontend/src/hooks/__tests__/queries.test.tsx`, `frontend/src/pages/__tests__/TrashView.test.tsx` (new)

**Interfaces:**
- Pattern for each: call REST first; on success update Dexie; on `ApiError` keep Dexie unchanged and surface `error.message` through the existing toast/alert mechanism; when `!navigator.onLine` reject immediately with `new Error('This needs a connection')` without touching Dexie.
- `useDeleteArea` on 409 `HAS_PROJECTS` shows `Move or delete this area's projects first`.

- [ ] **Step 1: Write the failing tests**: `delete area keeps the local row when the server returns 409`; `delete tag offline leaves the tag and reports "This needs a connection"`; `task reorder failure keeps the previous order`; `Empty Trash releases the purging state after a failure`.
- [ ] **Step 2: Run** — Expected: FAIL.
- [ ] **Step 3: Implement**; Empty Trash resets its `purging` state in `finally`.
- [ ] **Step 4: Run** — Expected: PASS.
- [ ] **Step 5: Commit** `fix: make area, tag and reorder operations server-first`

### Task 27: Small fixes and dead code (M6, P3 bullets)

**Files:**
- Delete: `frontend/src/sync/reminders.ts`, `frontend/src/sync/missedRemindersEmitter.ts`, `frontend/src/components/MissedRemindersToast.tsx`
- Modify: `frontend/src/db/mutations.ts:79-92` (`syncFirstScheduleDate`), `frontend/src/db/mutations.ts:360-365` and `frontend/src/hooks/queries.ts:504,575` (local project/area delete), `frontend/src/pages/ProjectView.tsx:87`, `frontend/src/components/SearchOverlay.tsx:36`, `frontend/src/components/ScheduleEditor.tsx:116`
- Test: `frontend/src/db/__tests__/mutations.test.ts`, `frontend/src/components/__tests__/SearchOverlay.test.tsx` (new)

- [ ] **Step 1: Write the failing tests**: `syncFirstScheduleDate moves the first entry of a multi-entry task`; `deleting a project locally moves its tasks to Trash and clears project_id and area_id` (as the server, `projects.go:164`); `deleting an area locally moves its loose tasks to Trash`; `whitespace search sends no request`.
- [ ] **Step 2: Run** — Expected: FAIL.
- [ ] **Step 3: Implement**; dialog copy in `ProjectView.tsx`: `Delete this project? Its tasks move to Trash.`; delete the three dead files (confirm `grep -rn "reminders'\|missedReminders\|MissedRemindersToast" frontend/src` is empty); remove the unused `eslint-disable` at `ScheduleEditor.tsx:116`.
- [ ] **Step 4: Run** `npm test && npm run lint` — Expected: PASS, 0 warnings.
- [ ] **Step 5: Commit** `fix: align local deletes with the server and drop dead reminder code`

---

## Phase 4 — Tests and acceptance

### Task 28: Remaining unit coverage (Phase 4)

**Files:**
- Test (new): `frontend/src/lib/__tests__/filter-tasks.test.ts`, `frontend/src/lib/__tests__/sort-position.test.ts`, `frontend/src/hooks/__tests__/useBulkAction.test.tsx`, `frontend/src/sync/__tests__/engine.test.ts`
- Modify: `frontend/src/db/__tests__/mutations.test.ts`, `frontend/src/test/mocks/handlers.ts`

- [ ] **Step 1: Write tests** pinning current intended behavior (each must fail if its module's core branch is broken — verify by temporarily inverting one condition locally, then restore):
  - `filter-tasks`: AND across filter kinds, tag filter semantics as implemented, priority and date-range filters.
  - `sort-position`: between two values, before first, after last, equal neighbours.
  - `useBulkAction`: each action writes through local mutations and the queue.
  - `engine`: `410 on pull runs fullSync`; `status is error when stuck entries exist and idle otherwise`; `stuckCount` set after push.
  - `mutations`: cascades for task delete (children), project status change, `first_schedule_*` flags.
  - Add an msw handler for `GET /api/saved-filters` returning `[]` so `TodayView.test.tsx` logs no unhandled-request warning.
- [ ] **Step 2: Run** `npm test` — Expected: PASS with no MSW warnings in the output.
- [ ] **Step 3: Commit** `test: cover filters, sort positions, bulk actions and the sync engine`

### Task 29: Playwright lifecycle suite and CI job (Acceptance 3)

**Files:**
- Modify: `frontend/vite.config.ts` (proxy target from `VITE_API_TARGET`, default `http://localhost:2999`, keeping `changeOrigin: false`)
- Modify: `frontend/package.json` (devDependency `"@playwright/test": "1.64.1"`, scripts `"e2e": "playwright test"`, `"e2e:dev": "E2E_TARGET=dev playwright test"`)
- Create: `frontend/playwright.config.ts`, `frontend/e2e/global-setup.ts`, `frontend/e2e/helpers.ts`, `frontend/e2e/lifecycle.spec.ts`, `frontend/e2e/sync.spec.ts`
- Modify: `.github/workflows/ci.yml` (new job `e2e`), `.gitignore` (`frontend/test-results/`, `frontend/playwright-report/`)

**Interfaces:**
- `global-setup.ts`: fresh temp dir; starts the server binary (`bin/thingstodo`, built by `make build` beforehand) with `PORT=3999`, `DB_PATH=<tmp>/e2e.db`, `AUTH_MODE=builtin`, `LOGIN_PASSWORD=e2e-pass`, `JWT_SECRET=<random>`, `TZ=Europe/Moscow`; waits for `GET /api/auth/config` 200; logs in as `admin`, creates a named token via `POST /api/tokens` `{name: "e2e"}`, stores `baseURL`, token and the server process for teardown. With `E2E_TARGET=dev` it also starts `npm run dev -- --port 5199` with the Vite proxy pointed at `3999` (env `VITE_API_TARGET`, read in `vite.config.ts` with default `http://localhost:2999`) and uses `http://localhost:5199` as `baseURL`.
- `helpers.ts`: `login(page)`, `apiView(request, name): Promise<unknown>` (GET `/api/views/<name>` with the token), `apiPatchTask(request, id, body)`, `restartServer()`.
- Every test ends with `page.reload()` and asserts the UI list equals `apiView(...)` ids for that list.

- [ ] **Step 1:** `npm install -D @playwright/test@1.64.1` and `npx playwright install chromium`; add config (`projects: [{ name: 'chromium' }]`, `workers: 1`, `globalSetup`).
- [ ] **Step 2: Write** `lifecycle.spec.ts` tests, one per Acceptance 3 bullet except offline/SSE: `login, first load, logout clears local data`; `quick entry with #покупки, $Project, date and time`; `schedules: today, evening, someday, future, two time blocks, 23:30 block`; `deadline, exact and relative reminders, checklist, link and file attachments`; `move to area, project, heading; reorder survives reload`; `complete, cancel, won't do, reopen; repeating task spawns one instance (fixed and after completion)`; `multi-select drag to Completed and Today`; `trash: delete, restore, empty; delete project and area send tasks to Trash`; `settings: font size 14 to 18, evening start, default reminder`.
- [ ] **Step 3: Write** `sync.spec.ts`: `offline edit pushes on reconnect` (`context.setOffline`); `server-rejected change appears in stuck list and can be discarded` (create the rejection by changing the server via token so a queued change becomes invalid, e.g. delete the target project via API while offline, then edit a task into it); `SSE: API change appears without reload`; `SSE reconnects after server restart`.
- [ ] **Step 4: Run** `make build && cd frontend && npm run e2e` — Expected: all pass. Then `npm run e2e:dev` — Expected: all pass (this run is the one that proves Task 1 under the Vite proxy).
- [ ] **Step 5: CI job** `e2e` in `ci.yml`: checkout, setup-go 1.27, setup-node 22, `cd frontend && npm ci`, `npx playwright install --with-deps chromium`, `make build`, `cd frontend && npm run e2e`; upload `playwright-report` on failure.
- [ ] **Step 6: Commit** `test: add Playwright lifecycle suite and CI job`

### Task 30: Docs, roadmap and final verification (Acceptance 1, 4)

**Files:**
- Modify: `docs/review-and-roadmap.md` (§7 Project 3: link this plan, status; add follow-ups: field-level LWW + `device_id`; the four server inconsistencies from the spec's Non-goals; Logbook/Trash pagination and N+1), `docs/api.md` (check every API change of Tasks 2, 4, 5, 17 is present), `README.md` (Task 1 note present; e2e commands)

- [ ] **Step 1: Run** `go test ./... && go vet ./... && golangci-lint run` — Expected: all pass.
- [ ] **Step 2: Run** `cd frontend && npm test && npm run lint && npx tsc -b` — Expected: pass, lint 0 warnings.
- [ ] **Step 3: Run** `make build && cd frontend && npm run e2e && npm run e2e:dev` — Expected: pass.
- [ ] **Step 4: Update** the docs listed above; record the verification results (test counts) in the roadmap entry.
- [ ] **Step 5: Commit** `docs: close project 3 in the roadmap`

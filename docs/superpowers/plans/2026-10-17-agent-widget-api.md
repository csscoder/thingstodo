# Agent and Widget API Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give Hermes (via MCP) and widget SPAs (via REST) complete, safe access to correct task data: named tokens, open CORS, MCP inside the Go server, a filterable task query, validated atomic idempotent writes, and correct recurrence, reminders, timezone, views and change feed.

**Architecture:** Changes stay in the handler → repository → SQLite layers. Three cross-cutting additions:
- a small transaction helper (`repository.InTx` plus `WithTx` on the repositories that must commit together);
- one task-finishing path, `Scheduler.FinishTask`, used by REST, bulk and sync;
- an MCP package that dispatches every tool in-process to the same chi router.

New middleware covers CORS and the cross-origin session guard. A hand-written OpenAPI file is checked against the router by a drift test.

**Tech Stack:** Go 1.27, chi v5.2.5, golang-jwt v5.3.1, modernc SQLite, `github.com/modelcontextprotocol/go-sdk` v1.8.0, React 19 + Vitest (unchanged, must stay green).

**Spec:** `docs/superpowers/specs/2026-10-17-agent-widget-api-design.md`. Read it before starting; section letters (A1, D3, E2…) below refer to it.

## Global Constraints

- **Dependencies:**
  - Go 1.27 everywhere: `go.mod` `go 1.27.0`, `Dockerfile` `golang:1.27-alpine`, CI `go-version: "1.27"`.
  - The only new dependency is `github.com/modelcontextprotocol/go-sdk@v1.8.0`.
- **TDD:** every behavior change starts with a test that fails for the stated reason. Never skip, delete or weaken an existing test. If a test fails for a bug outside this plan, stop and ask the owner.
- **Error body:** `{"error": "...", "code": "...", "field": "..."}`, where `field` is optional, with `Content-Type: application/json`.
- **Error codes:**
  - Keep the existing codes `BAD_REQUEST`, `BODY_TOO_LARGE`, `HAS_PROJECTS`, `RESERVED`, `CURSOR_EXPIRED` and `RATE_LIMITED` unchanged.
  - New codes: `METHOD_NOT_ALLOWED`, `FORBIDDEN` and `CONFLICT`.
  - The spec's `PAYLOAD_TOO_LARGE` is satisfied by the existing `BODY_TOO_LARGE` (D2: keep codes clients match).
- **Generic 5xx body:** exactly `{"error":"internal error","code":"INTERNAL"}`.
- **Client ids:** `^[A-Za-z0-9_-]{8,21}$`.
- **Token format:** `ttd_` followed by base64url (no padding) of 32 bytes from `crypto/rand`. Store `sha256` hex only. `prefix` is the first 8 characters of the token.
- **CORS header values:** exactly as in spec A2.
- **Stored formats:**
  - Timestamps stay UTC `YYYY-MM-DD HH:MM:SS`.
  - Dates are `YYYY-MM-DD`.
  - Times are `HH:MM`.
  - `exact_at` is stored as UTC RFC 3339 (`2026-10-20T06:00:00Z`).
- **"Today"** is always `now().In(cfg.Location)`, never bare `time.Now()` and never SQLite `date('now')`.
- **Docs:** every endpoint behavior change updates `docs/api.md` in the same task (`CLAUDE.md`).
- **Commits:** a short `type: subject` line, no trailers.
- **Working directory:** run Go commands from the repo root and frontend commands in `frontend/`.

## Review Focus

1. **The web UI's offline completion still works.** `/sync/push` with `status: completed` on a recurring task returns a success result and spawns exactly one instance. It is pinned by `TestSyncPush_CompleteRecurringSpawnsOnce` in Task 8.
2. **Hermes clears a date through MCP.** `update_task` with `when_date: null` clears the date and deletes its schedules. It is pinned by `TestMCP_UpdateTaskClearsWhenDate` in Task 20.
3. **The startup backfill on a real, inconsistent DB.** It fixes mismatched `area_id` rows, logs one change per fixed task, and a second run changes nothing. It is pinned by `TestBackfillAreas_IdempotentAndLogged` in Task 11.
4. **A browser widget's preflight with lowercase headers.** `Access-Control-Request-Headers: authorization,content-type` gets 204 with the allow headers. It is pinned by `TestCORS_PreflightLowercaseHeaders` in Task 18.
5. **A Cyrillic search for a Russian-speaking owner.** `покуп` finds "Покупки на неделю" in both `/api/search` and `/api/tasks?search=`. It is pinned by `TestSearch_CyrillicPrefix` in Task 16.

---

### Task 1: Go 1.27 toolchain

**Files:** Modify `go.mod`, `go.sum`, `Dockerfile:11`, `.github/workflows/ci.yml`, `.devcontainer/*` (only if it pins Go).

- [ ] **Step 1: Bump the Go version.** Set `go 1.27.0` in `go.mod`, `golang:1.27-alpine` in `Dockerfile`, and `go-version: "1.27"` in both CI jobs. Run `go mod tidy`.
- [ ] **Step 2: Upgrade the lint tooling.** golangci-lint v1.64 cannot lint a Go 1.27 module.
  - Switch CI to `golangci/golangci-lint-action@v8` with the newest golangci-lint v2.x release that is at least two weeks old and whose release notes list Go 1.27 support.
  - The repo has no `.golangci.yml`, so v2 defaults apply. If v2 reports findings that v1 did not, fix them in this task.
  - Do not add a config that disables linters.
- [ ] **Step 3: Verify.**
  - Run `go test ./... -race && go vet ./... && go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@<chosen> run`. Expected: all pass.
  - Run `docker build .`. Expected: success.
- [ ] **Step 4: Commit.** `build: move to Go 1.27`

### Task 2: One JSON error envelope (D2)

**Files:**
- Create: `internal/middleware/errors.go`
- Modify: `internal/middleware/auth.go`, `internal/handler/helpers.go`, `internal/router/router.go`
- Test: `internal/middleware/errors_test.go`, `internal/router/router_test.go`

**Interfaces:**
- Produces:
  - `mw.WriteError(w http.ResponseWriter, status int, msg, code, field string)`: JSON envelope; `field` is omitted when empty; for status ≥ 500 it logs `msg` and writes the generic body.
  - `handler.writeError(w, status, msg, code)` stays and delegates with `field=""`.
  - `handler.writeFieldError(w, status, msg, code, field string)`.

- [ ] **Step 1: Write failing tests.**
  - `TestAuth_ErrorsAreJSON`: a missing cookie gets 401 with `Content-Type: application/json` and code `UNAUTHORIZED`.
  - `TestRouter_UnknownAPIRouteIsJSON404`: `GET /api/nope` gets 404, JSON, code `NOT_FOUND`.
  - `TestRouter_WrongMethodIsJSON405`: `PUT /api/tasks` gets 405, JSON, code `METHOD_NOT_ALLOWED`, and the `Allow` header is non-empty.
  - `TestWriteError_FieldIncluded`: `writeFieldError(..., 400, "bad", "VALIDATION", "when_date")` gives body `{"error":"bad","code":"VALIDATION","field":"when_date"}`.
- [ ] **Step 2: Run** `go test ./internal/middleware/ ./internal/router/ ./internal/handler/ -run 'ErrorsAreJSON|JSON404|JSON405|FieldIncluded'`. Expected: FAIL.
- [ ] **Step 3: Implement.**
  - Replace every `http.Error` in `auth.go` with `mw.WriteError`, keeping each current code.
  - On the `/api` sub-router, set `r.NotFound` and `r.MethodNotAllowed`. For 405, keep the `Allow` header chi sets.
  - Leave the SPA fallback (`/*`) untouched.
- [ ] **Step 4: Run** `go test ./...`. Expected: PASS.
- [ ] **Step 5: Docs and commit.** List all codes in `docs/api.md`. Commit: `fix: return JSON error envelope from middleware and unknown routes`.

### Task 3: Transaction helper and atomic task writes (D3)

**Files:**
- Create: `internal/repository/tx.go`
- Modify:
  - `internal/repository/{tasks,schedules,checklist,attachments,reminders,repeat_rules,projects,areas,changelog,changelog_helper}.go`
  - `internal/handler/tasks.go`
- Test: `internal/repository/tx_test.go`, `internal/handler/tasks_test.go`

**Interfaces:**
- Produces:
  - `type DBTX interface { Exec(query string, args ...any) (sql.Result, error); Query(query string, args ...any) (*sql.Rows, error); QueryRow(query string, args ...any) *sql.Row }`
  - `func InTx(db *sql.DB, fn func(tx *sql.Tx) error) error`: commits on nil, rolls back on error or panic (re-panics).
  - For `Task`, `Schedule`, `Checklist`, `Attachment`, `Reminder`, `RepeatRule`, `Project`, `Area` and `ChangeLog` repositories:
    - the `db` field becomes `DBTX`;
    - a `conn *sql.DB` field is kept;
    - each gets `WithTx(tx *sql.Tx) *XRepository`, a shallow copy using `tx`. Its `changeLog` is `changeLog.WithTx(tx)`, and it is nil-safe.
  - `func (r *TaskRepository) Conn() *sql.DB`.
  - `logChange(...)` now returns `error`. Existing call sites may keep ignoring it; code inside `InTx` must return it.

- [ ] **Step 1: Write failing tests.**
  - `TestInTx_RollsBackOnError`: insert an area inside `InTx`, return an error; the area does not exist.
  - `TestInTx_RollsBackOnPanic`: same, but panic; the test recovers.
  - `TestTaskCreate_BadTagWritesNothing`: `POST /api/tasks {"title":"x","tag_ids":["nope0000"]}` gets non-2xx. Then `SELECT COUNT(*) FROM tasks` = 0 and `change_log` has no task row.
  - `TestTaskCreate_DuplicateTimelessScheduleRollsBack`: an update that fails `syncFirstScheduleDate` leaves `tasks.when_date` unchanged. This is the audit's B18 probe.
- [ ] **Step 2: Run them.** Expected: FAIL. Today the task row survives, and `Create` ignores the `syncFirstScheduleDate` error at `tasks.go:212`.
- [ ] **Step 3: Implement.**
  - `TaskRepository.Create` and `Update` run row, tags, first schedule and change log through one `InTx`, returning any error. Stop ignoring the `syncFirstScheduleDate` error.
  - `TaskHandler.Create` creates the default reminder in the same transaction: `InTx(h.repo.Conn(), …)` with `h.repo.WithTx(tx)` and `h.reminderRepo.WithTx(tx)`.
  - Until Task 4 maps it to 422, a failed tag insert may surface as 500.
- [ ] **Step 4: Run** `go test ./... -race`. Expected: PASS.
- [ ] **Step 5: Commit.** `fix: make task create and update atomic`

### Task 4: Validation for tasks, schedules, checklist and titles (D1)

**Files:**
- Create: `internal/handler/validate.go`
- Modify: `internal/handler/{tasks,schedules,checklist,projects,areas,tags,headings,sync}.go`, `internal/repository/tx.go` (add `Exists`)
- Test: `internal/handler/validate_test.go`, `internal/handler/tasks_test.go`, a new `internal/handler/schedules_test.go`

**Interfaces:**
- Produces, in `validate.go`:
  - `isDate(s string) bool`: `time.Parse("2006-01-02")` and the result formats back to `s`, so `2026-02-31` is rejected.
  - `isWhenDate(s string) bool`: `isDate` or `"someday"`.
  - `isClock(s string) bool`: `^([01]\d|2[0-3]):[0-5]\d$`.
  - `isClientID(s string) bool`: the Global Constraints regex.
- Produces, in `repository/tx.go`: `func Exists(db DBTX, table, id string) (bool, error)`. `table` must be one of `tasks`, `projects`, `areas`, `headings`, `tags`; anything else returns an error.
- Rule: malformed values get 400 `VALIDATION`; referenced ids that do not exist get 422 `VALIDATION`. Both carry `field`.

- [ ] **Step 1: Write failing tests.** Each asserts the status, `code == "VALIDATION"` and the exact `field`.
  - `TestValidators`: table test over `isDate` (`2026-02-29` false, `2028-02-29` true, `2026-13-01` false), `isClock` (`9:00` false, `09:00` true, `24:00` false), `isClientID`.
  - `TestTaskCreate_RejectsBadWhenDate`: `"tomorrow"` → 400 `when_date`.
  - `TestTaskCreate_RejectsUnknownProject`: → 422 `project_id`. Also `TestTaskCreate_RejectsUnknownTag` → 422 `tag_ids`.
  - `TestTaskCreate_RejectsHeadingFromOtherProject`: → 422 `heading_id`.
  - `TestTaskUpdate_RejectsEmptyTitle`: → 400 `title`. Same for project, area, tag, heading and checklist update.
  - `TestTaskCreate_RejectsBadClientID`: `"id":"a b"` → 400 `id`.
  - `TestScheduleCreate_RejectsBadTimes`: `start_time:"9:00"` → 400 `start_time`; `end_time` ≤ `start_time` → 400 `end_time`.
  - `TestScheduleCreate_UnknownTaskIs422`: today this is 500 (FK); now 422 `task_id`. Same for checklist create.
  - `TestSyncPush_BadWhenDateIsChangeError`: sync task create with `when_date:"tomorrow"` gets a per-change `error` containing `when_date`. The batch continues.
- [ ] **Step 2: Run them.** Expected: FAIL.
- [ ] **Step 3: Implement.**
  - Call the validators at the top of each handler. Bulk `set_when` and `move` validate their date params the same way.
  - In sync, return `SyncPushResult{Error: "<field>: <msg>"}` for the same rules.
- [ ] **Step 4: Run** `go test ./... -race && (cd frontend && npm test)`. Expected: PASS.
- [ ] **Step 5: Docs and commit.** Update `docs/api.md` validation rules. Commit: `feat: validate dates, times, ids and references at the API boundary`.

### Task 5: Validation for reminders, repeat rules, settings and status enums (D1, E4 storage)

**Files:**
- Modify: `internal/handler/{reminders,repeat_rules,user_settings,projects,sync}.go`, `internal/handler/validate.go`
- Test: `internal/handler/reminders_test.go`, `repeat_rules_test.go`, `user_settings_test.go` (new files)

**Interfaces:**
- Produces: `normalizeExactAt(s string, loc *time.Location) (string, error)`.
  - It accepts RFC 3339 with an offset or `Z`, or a naive `2006-01-02T15:04[:05]` read in `loc`.
  - It returns UTC RFC 3339, e.g. `2026-10-20T09:00:00+03:00` → `2026-10-20T06:00:00Z`.
- Consumes: `isClock`, `isDate`, `writeFieldError` (Tasks 2 and 4).

- [ ] **Step 1: Write failing tests.**
  - `TestNormalizeExactAt`: covers offset, `Z`, naive in `Europe/Moscow`, and garbage (error).
  - `TestReminderCreate_Validates`:
    - unknown `type` → 400 `type`;
    - `value` 100 → 400 `value`;
    - `exact` without `exact_at` → 400 `exact_at`;
    - a valid offset value is stored normalized (read it back through `GET /api/tasks/{id}/reminders`).
  - `TestRepeatUpsert_Validates`:
    - unknown `type` → 400 `type`;
    - `every` 0 → 400 `every`;
    - unknown `mode` → 400 `mode`;
    - weekly `on:["xyz"]` → 400 `on`;
    - `monthly_dom` with `day` 32 → 400 `day`;
    - `month` 13 → 400 `month`.
  - `TestSettingsPatch_Validates`. Each enum and range documented in `docs/api.md` under user settings gets one rejecting case, e.g. `evening_starts_at:"7pm"` → 400 `evening_starts_at`, and `time_format:"x"` → 400 `time_format`.
  - `TestProjectUpdate_RejectsUnknownStatus`: today a DB constraint gives 500; now 400 `status`.
- [ ] **Step 2: Run them.** Expected: FAIL.
- [ ] **Step 3: Implement.** The handler `ReminderHandler` needs the location, so pass `cfg.Location` in `router.go`. Sync reminder create applies the same rules.
- [ ] **Step 4: Run** `go test ./... -race`. Expected: PASS.
- [ ] **Step 5: Docs and commit.** Commit: `feat: validate reminders, repeat rules, settings and status`.

### Task 6: Idempotent create by client id (D3)

**Files:**
- Modify: the create handlers in `internal/handler/{tasks,projects,areas,tags,headings,checklist,schedules,reminders,attachments}.go`, plus a `GetByID` in any repository that lacks one.
- Test: one test per handler file.

**Interfaces:**
- Produces: on every create endpoint, a body `id` that already exists in the target table returns **200** with the stored entity. Nothing is modified, and no SSE or change-log entry is written. A new id still gets 201.

- [ ] **Step 1: Write failing tests.**
  - `TestCreate_RepeatWithSameIDReturnsExisting`: a table test over tasks, projects, areas, tags, headings, checklist items, schedules, reminders and link attachments.
    - First POST → 201.
    - Second POST with the same `id` and a different title → 200, with the first title.
    - The row count is 1.
- [ ] **Step 2: Run it.** Expected: FAIL. Today the result is 500, 409 or a duplicate, depending on the table.
- [ ] **Step 3: Implement.** In each handler, after validation: if `input.ID != ""` and `GetByID` finds it, `writeJSON(w, 200, existing)` and return.
- [ ] **Step 4: Run** `go test ./... -race`. Expected: PASS.
- [ ] **Step 5: Docs and commit.** Commit: `feat: make creates idempotent by client-supplied id`.

### Task 7: Recurrence calculator fixes (E2)

**Files:** Modify `internal/recurrence/calculators.go`. Test: `internal/recurrence/engine_test.go`.

- [ ] **Step 1: Write failing tests.**
  - `TestMonthlyDOM_FromJan31`:
    - `day=31`, every 1, from `2026-01-31` → `2026-02-28`;
    - from `2028-01-31` → `2028-02-29`;
    - from `2026-02-28` with `day=31` → `2026-03-31`.
  - `TestMonthlyDOW_FromJan31` and `TestMonthlyWorkday_FromJan31`: from `2026-01-31` the next result is in February 2026.
  - `TestYearlyFeb29_EveryFour`: from `2030-02-28` with `month=2, day=29`, every 4 → `2034-02-28`. It must return; guard it with a 1 s `time.AfterFunc` that fails the test.
  - `TestYearlyFeb29_EveryOne`: from `2027-03-01` → `2028-02-29`.
  - `TestFirstOnOrAfter_Feb29NonLeap`. With `month=2, day=29`, from `2026-02-10` → `2026-02-28`; from `2026-02-28` → `2026-02-28`; from `2026-03-01` → `2027-02-28`.
- [ ] **Step 2: Run** `go test ./internal/recurrence/`. Expected: FAIL. One test hangs until the guard fires.
- [ ] **Step 3: Implement.**
  - Monthly types compute the target month as `time.Date(from.Year(), from.Month()+every, 1, …)`, then pick the day.
  - In `yearly_date`, remove the leap loop and clamp with `clampToLastDay`.
  - `CurrentPeriod` clamps instead of returning an error.
- [ ] **Step 4: Run.** Expected: PASS.
- [ ] **Step 5: Commit.** `fix: recurrence month-end skips and Feb 29 infinite loop`.

### Task 8: One finishing path: completion guard and transactional next instance (D3, E2)

**Files:**
- Modify:
  - `internal/scheduler/scheduler.go`
  - `internal/repository/tasks.go` (`Complete`/`Cancel`/`WontDo` become one guarded method)
  - `internal/repository/schedules.go`
  - `internal/handler/tasks.go`, `internal/handler/sync.go`
- Test: `internal/scheduler/scheduler_test.go` (new, `package scheduler`), `internal/handler/tasks_test.go`, `internal/handler/sync_test.go`

**Interfaces:**
- Produces:
  - `func (r *TaskRepository) SetDoneStatus(id, status string) (changed bool, err error)`:
    - `status` is one of `completed`, `canceled`, `wont_do`;
    - SQL `UPDATE … WHERE id = ? AND status = 'open'`;
    - sets `completed_at` or `canceled_at` as today;
    - logs the change.
  - `func (s *Scheduler) FinishTask(taskID, status string) (task *model.TaskDetail, changed bool, err error)`:
    - one `InTx` that calls `SetDoneStatus`, then schedule cleanup (`CleanupOnTaskDone` with local today), then, if a rule exists, the next instance and the rule move;
    - returns `(nil, false, nil)` for an unknown task.
  - `func (s *Scheduler) SetClock(now func() time.Time)` sets the scheduler's clock; the default is `time.Now`. Later tasks use it.
  - An unexported `testHookAfterInstance func() error`, called inside the transaction after the instance is created. A test sets it to force a rollback.
- Removes: `HandleTaskDone`, `HandleAfterCompletion`, `TaskRepository.Complete/Cancel/WontDo`, and both handler-level `cleanupSchedules`.
- **Base date for the next instance:**
  - `after_completion`: `engine.Next(localDate(now), pattern)`.
  - `fixed`: `engine.Next(original.when_date, pattern)`, repeated while the result is before local today. With no `when_date`, use local today.

- [ ] **Step 1: Write failing tests.**
  - `TestFinishTask_DoubleCompleteSpawnsOnce`: two calls create one instance, and the second returns `changed=false`.
  - `TestFinishTask_AfterCompletionUsesCompletionDate`:
    - weekly every 1, no `on`, `when_date` Wed `2026-10-14`, clock Sat `2026-10-17` → new instance `2026-10-24`;
    - with `on:[wed]` → `2026-10-21`.
  - `TestFinishTask_FixedRollsForward`: daily, `when_date` `2026-10-01`, clock `2026-10-17` → `2026-10-17`.
  - `TestFinishTask_InstanceFailureRollsBack`: the hook returns an error. The task is still `open`, no new task exists, the rule is still on the original, and `FinishTask` returns the error.
  - `TestComplete_RepeatedRESTCallIs200NoSpawn`: the handler test calls `PATCH /complete` twice → 200 both times, one instance.
  - `TestBulkComplete_AffectedCountsChangedOnly`: one open id, one already completed, one unknown → `affected: 1`.
  - `TestSyncPush_CompleteRecurringSpawnsOnce` (Review Focus 1): push `status: completed` twice in two batches → success results both times, one instance.
- [ ] **Step 2: Run** `go test ./internal/scheduler/ ./internal/handler/`. Expected: FAIL.
- [ ] **Step 3: Implement.**
  - `createNextInstance` takes the `tx`-bound repositories. Use `WithTx` for task, checklist, attachment, reminder, rule and schedule, and return every error.
  - REST `Complete`, `Cancel` and `WontDo`, the bulk status actions (loop `FinishTask`), and the sync status mapping all call `FinishTask`.
  - `processFixedRules` runs `createNextInstance` inside its own `InTx`.
- [ ] **Step 4: Run** `go test ./... -race && (cd frontend && npm test)`. Expected: PASS.
- [ ] **Step 5: Commit.** `fix: finish tasks once and create the next instance atomically`.

### Task 9: Independent attachment copies (E2 data-loss fix)

**Files:**
- Modify: `internal/repository/attachments.go`, `internal/handler/attachments.go`, `internal/handler/sync.go` (attachment delete)
- Test: `internal/handler/attachments_test.go`

**Interfaces:**
- Produces: `func (r *AttachmentRepository) DeleteOne(id string) (orphanedFile string, err error)`.
  - In one `InTx` it deletes the row by `id`, logs the change, and, for a `file` row, runs `CountByURL`.
  - It returns the stored file name only when no rows remain, otherwise `""`.
  - The caller removes `orphanedFile` through `os.Root` after the call returns.
- Removes: `DeleteByURL`.

- [ ] **Step 1: Write failing tests.**
  - `TestDeleteAttachment_KeepsSharedFile`:
    - upload a file to a recurring task, complete it (Task 8), delete the original's attachment;
    - the new instance's `GET /api/attachments/{id}/file` → 200, and the file exists on disk.
    - Deleting the copy then removes the file.
  - `TestSyncDeleteAttachment_KeepsSharedFile`: the same scenario through `/sync/push`.
- [ ] **Step 2: Run them.** Expected: FAIL. Today the copy returns 404.
- [ ] **Step 3: Implement** `DeleteOne` and switch both delete paths to it.
- [ ] **Step 4: Run** `go test ./... -race`. Expected: PASS.
- [ ] **Step 5: Commit.** `fix: deleting one attachment copy no longer deletes the shared file`.

### Task 10: Project and area deletes go to Trash (D5)

**Files:**
- Modify: `internal/repository/{projects,areas}.go`, `internal/handler/{projects,areas,sync}.go`
- Test: `internal/handler/projects_test.go`, `areas_test.go`, `sync_test.go`

**Interfaces:**
- Produces: `ErrHasProjects` (repository sentinel) and two methods.
  - `func (r *ProjectRepository) DeleteToTrash(id string) error`. In one `InTx`, for each child task:
    - set `deleted_at` = now (UTC) and clear `project_id`, `heading_id` and `area_id`;
    - log a task change.
    - Then delete the headings and the project and log the project deletion.
  - `func (r *AreaRepository) DeleteToTrash(id string) error`:
    - returns `ErrHasProjects` when projects remain;
    - otherwise trashes its standalone tasks (clearing `area_id`), logs them, and deletes the area.
- Removes: `DeleteWithTasks` on both repositories.

- [ ] **Step 1: Write failing tests.**
  - `TestDeleteProject_TasksGoToTrash`: after REST delete, `GET /api/views/trash` lists both child tasks. `project_id` is null and `change_log` has a task entry per child.
  - `TestDeleteArea_WithProjectsIs409`: → 409 `HAS_PROJECTS` (keep this) and nothing is deleted.
  - `TestDeleteArea_StandaloneTasksGoToTrash`: the area's standalone tasks are trashed and logged.
  - `TestSyncDeleteProject_TasksGoToTrash`: the same as the first test through `/sync/push`.
  - `TestSyncDeleteArea_WithProjectsIsChangeError`: the per-change error contains `HAS_PROJECTS`.
  - `TestRestoreTrashedProjectTask_IsLoose`: restore → task open, no project, no area.
- [ ] **Step 2: Run them.** Expected: FAIL.
- [ ] **Step 3: Implement** the two methods. Point REST and sync deletes at them.
- [ ] **Step 4: Run** `go test ./... -race && (cd frontend && npm test)`. Expected: PASS.
- [ ] **Step 5: Docs and commit.** Document the delete semantics. Commit: `fix: project and area deletes send tasks to Trash`.

### Task 11: A task's area follows its project; precise moves; backfill (D5)

**Files:**
- Modify:
  - `internal/repository/{tasks,projects}.go`
  - `internal/model/models.go` (`MoveTaskInput.Raw`)
  - `internal/handler/tasks.go` (Move uses `decodeJSONWithRaw`)
  - `cmd/server/main.go`
- Test: `internal/repository/tasks_test.go`, `internal/handler/tasks_test.go`

**Interfaces:**
- Produces:
  - `func normalizeParents(db DBTX, projectID, areaID, headingID **string, projectChanged, headingGiven bool) error`, unexported, in `tasks.go`:
    - when `*projectID` is non-nil, it sets `*areaID` to that project's area;
    - when the project changed and no heading was given, it clears a heading that does not belong to the new project.
  - `func (r *TaskRepository) BackfillAreasFromProjects() (changed int, err error)`: one `InTx` that updates every task whose `area_id` differs from its project's area, logging each. `main.go` calls it after `database.Open`, logs the count, and does not exit on error.
- Applies `normalizeParents` in `Create`, `Update`, `Move`, `BulkAction move_project`, and sync task create/update. Sync goes through `Create` and `Update`; check that it does.
- `ProjectRepository.Update`: when `area_id` changes, the same `InTx` updates its tasks' `area_id` and logs each.
- `Move` sets only keys present in `Raw`. A `when_date` in a move goes through `syncFirstScheduleDate`.

- [ ] **Step 1: Write failing tests.**
  - `TestTaskCreate_AreaFollowsProject`: create with `project_id`=P (area A) and `area_id`=B → stored area A.
  - `TestMove_OnlyPresentKeys`: a task in project P with heading H; move `{"when_date":"2026-11-01"}` → project and heading unchanged, and the first schedule is `2026-11-01`.
  - `TestMove_ProjectChangeClearsForeignHeading`: `{"project_id":"P2"}` → heading cleared, area = P2's area. The same holds for `PATCH /api/tasks/{id}`.
  - `TestProjectAreaChange_UpdatesTasks`: change P's area → its tasks' `area_id` updated, one change-log entry each.
  - `TestBackfillAreas_IdempotentAndLogged` (Review Focus 3): corrupt two rows via SQL, run the backfill → 2 changed and 2 log entries; run again → 0.
- [ ] **Step 2: Run them.** Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./... -race && (cd frontend && npm test)`. Expected: PASS.
- [ ] **Step 5: Docs and commit.** Commit: `fix: derive task area from project and make moves precise`.

### Task 12: Bulk fixes, settings upsert, scoped schedule reorder (D5)

**Files:**
- Modify: `internal/repository/{tasks,user_settings,schedules}.go`, `internal/handler/{tasks,schedules}.go`
- Test: `internal/handler/tasks_bulk_test.go`, `user_settings_test.go`, `schedules_test.go`

- [ ] **Step 1: Write failing tests.**
  - `TestBulkSetPriority_HighPriorityParam`: `{"high_priority":true}` sets it; the legacy `{"priority":1}` still sets it.
  - `TestBulkAffected_IgnoresUnknownIDs`: a non-status action over 1 real and 1 unknown id → `affected: 1`.
  - `TestBulkSetWhen_EmptyClears`: `when_date:""` → `when_date` null and no schedule rows (today a phantom `''` row is created).
  - `TestBulkMoveProject_SetsAreaClearsHeading`.
  - `TestSettingsFirstPatchSaved`: with no `user_settings` row, the first PATCH `{"review_after_days":3}` → the response and a later GET show 3.
  - `TestScheduleReorder_ScopedToTask`: reordering with another task's schedule id → that schedule is unchanged and the result is 400 `VALIDATION`, field `items`.
- [ ] **Step 2: Run them.** Expected: FAIL.
- [ ] **Step 3: Implement.**
  - Settings use `INSERT … ON CONFLICT(user_id) DO UPDATE`.
  - Bulk `affected` is the sum of `RowsAffected`.
  - Reorder rejects ids not belonging to `{id}`.
- [ ] **Step 4: Run** `go test ./... -race`. Expected: PASS.
- [ ] **Step 5: Docs and commit.** Commit: `fix: bulk params and counts, first settings save, scoped schedule reorder`.

### Task 13: Schedule ↔ when_date consistency, change feed, SSE envelope (E5)

**Files:**
- Modify:
  - `internal/repository/schedules.go`
  - `internal/handler/{schedules,sync,checklist}.go`
  - `internal/sse/broker.go`
  - every handler that broadcasts a single-entity change
  - `internal/router/router.go`
- Test: `internal/handler/sync_test.go`, `internal/handler/schedules_test.go`, `internal/sse/broker_test.go`

**Interfaces:**
- Produces:
  - `func (b *Broker) SetSeqSource(f func() (int64, error))`. The router wires it to `changeLogRepo.GetLatestSeq`.
  - `func (b *Broker) BroadcastChange(eventType, entity, id string, data map[string]any)` broadcasts `data` plus `entity`, `id` and `seq`. `seq` is the latest change-log seq read after the write, so it is at least the write's own entry. On error it is omitted.
  - `bulk_change`, `connected` and `reminder_fired` keep their shapes.
  - `func (r *ScheduleRepository) SyncPrimary(taskID string) error` now logs a task change when it rewrites `when_date`. Schedule create, update, delete and reorder call it inside the same `InTx` as the schedule write, through REST and through sync.

- [ ] **Step 1: Write failing tests.**
  - `TestSyncScheduleCreate_UpdatesWhenDate`: sync create of a first schedule on `2026-11-02` → `tasks.when_date` = `2026-11-02`, and `/sync/pull` returns a task entry for it. Also add update and delete variants.
  - `TestSchedulePrimaryChange_InPull`: a REST schedule date change of the first entry produces a task change in `/sync/pull`.
  - `TestPull_CursorBoundary`: oldest seq = `since+1` → 200; oldest = `since+2` → 410.
  - `TestSyncPush_EmitsSSE`: subscribe to the broker; a sync task update emits `task_updated` with `entity:"task"`, `id` and `seq`.
  - `TestChecklistUpdate_EventIDs`: the event carries the item's `task_id` and the item `id`.
- [ ] **Step 2: Run them.** Expected: FAIL.
- [ ] **Step 3: Implement.**
  - Fix `sync.go` so the 410 check reads `oldest > since+1`.
  - Switch single-entity broadcasts to `BroadcastChange`.
- [ ] **Step 4: Run** `go test ./... -race && (cd frontend && npm test)`. Expected: PASS. The UI's SSE hook only triggers a sync, so extra fields are harmless.
- [ ] **Step 5: Docs and commit.** Document the SSE event shape. Commit: `fix: keep when_date in step with schedules and complete the change feed`.

### Task 14: The owner's timezone everywhere (E1)

**Files:**
- Modify: `internal/repository/views.go`, `internal/router/router.go`, `internal/scheduler/scheduler.go`, `internal/handler/views.go`
- Test: `internal/repository/views_test.go`

**Interfaces:**
- Produces:
  - `NewViewRepository(db *sql.DB, loc *time.Location) *ViewRepository`.
  - `func (r *ViewRepository) SetClock(now func() time.Time)`.
  - An unexported helper `localDayUTCBounds(day string, loc *time.Location) (startUTC, endUTC string)`, returning `YYYY-MM-DD HH:MM:SS` strings.
  - Every `time.Now().Format("2006-01-02")` in `views.go` and `scheduler.go` becomes `r.now().In(r.loc)` / `s.now().In(s.loc)`.
  - Every SQL `date('now', …)` (review cutoff) gets a Go-computed parameter.

- [ ] **Step 1: Write failing tests.** Use `Europe/Moscow` with the clock at `2026-10-17 00:30` local (`2026-10-16 21:30` UTC).
  - `TestToday_CompletedTodayLocal`: a task completed at `2026-10-16 21:10:00` UTC (00:10 local) is in Today's Completed section.
  - `TestToday_UsesLocalDate`: a task with `when_date` `2026-10-17` is in Today, not Upcoming.
  - `TestLogbook_GroupsByLocalDate`: that completion is grouped under `2026-10-17`.
  - `TestInbox_ReviewCutoffLocal`: the review boundary uses the local date.
- [ ] **Step 2: Run them.** Expected: FAIL.
- [ ] **Step 3: Implement.** In `router.go`, pass `cfg.Location`.
- [ ] **Step 4: Run** `go test ./... -race`. Expected: PASS.
- [ ] **Step 5: Docs and commit.** Document that timestamps are UTC and dates use `TZ`. Commit: `fix: compute today and date bounds in the server timezone`.

### Task 15: Consistent views and shared list predicates (E3, C2)

**Files:**
- Create: `internal/repository/view_predicates.go`, `internal/database/migrations/029_zero_pad_schedule_times.sql`
- Modify: `internal/repository/views.go`
- Test: `internal/repository/views_test.go`, `internal/database/schema_test.go`

**Interfaces:**
- Produces: `func listCondition(list, today, eveningStartsAt string) (cond string, args []any, err error)`.
  - `cond` is a SQL boolean expression over alias `t` (tasks) meaning "task appears in view `list`". The views are `inbox`, `today` (all four sections), `upcoming`, `anytime`, `someday`, `logbook` and `trash`.
  - An unknown `list` returns an error.
  - The view builders and `Counts` use it for membership. Task 16 uses it for `list=`.
- **Rules** (spec E3):
  - deadline-only loose → Inbox, not Anytime;
  - Earlier = open, `when_date < today`, not `someday`, regardless of schedules;
  - a task with a `when_date` and no schedule rows counts as one uncompleted schedule on `when_date`;
  - Upcoming defaults `from` to tomorrow, and its Overdue and Earlier are relative to today;
  - counts = `COUNT(DISTINCT t.id)` per view, Completed excluded.
- The migration zero-pads `task_schedules.start_time` and `end_time` matching `GLOB '[0-9]:[0-5][0-9]'`. Values that still fail `HH:MM` are listed by a Go startup check that logs them, with no exit.

- [ ] **Step 1: Write failing tests.**
  - `TestViews_DeadlineOnlyLooseTask`: with deadlines yesterday, today and tomorrow → Inbox always, never Anytime; Overdue / Today / neither respectively.
  - `TestViews_PastDateWithoutSchedulesInEarlier`.
  - `TestViews_FutureDateWithoutScheduleInUpcoming`.
  - `TestViews_TodayAllSchedulesDoneInNoList`: pins the one documented exception.
  - `TestViews_EveryOpenTaskPlaced`: a fixture of each task shape. Every open non-trashed task except the exception is in at least one list.
  - `TestCounts_DistinctTasks`: a task with a day and an evening schedule today, and a task in both Overdue and Earlier, each count once. Every count equals the distinct ids of its view.
  - `TestUpcoming_StartsTomorrowAndEarlierRelativeToToday`.
  - `TestMigration029_ZeroPads`: `9:00` → `09:00`, `09:30` unchanged, `junk` unchanged.
  - `TestToday_Legacy900NotEvening`: after the migration, a `9:00` schedule is in Today, not This Evening.
- [ ] **Step 2: Run them.** Expected: FAIL.
- [ ] **Step 3: Implement.** Keep each view's response shape unchanged.
- [ ] **Step 4: Run** `go test ./... -race && (cd frontend && npm test)`. Expected: PASS.
- [ ] **Step 5: Docs and commit.** Document each view's rule. Commit: `fix: consistent view rules and counts`.

### Task 16: Task query API and safe search (C1, D4)

**Files:**
- Modify:
  - `internal/model/models.go` (`TaskFilters`)
  - `internal/handler/tasks.go`, `internal/handler/search.go`
  - `internal/repository/tasks.go`, `internal/repository/search.go`
- Create: `internal/repository/cursor.go`
- Test: `internal/handler/tasks_query_test.go` (new), `internal/repository/search_test.go`

**Interfaces:**
- Produces:
  - `TaskFilters` gains:
    - `Statuses []string`, `List *string`, `TagMode string` (`any` | `all`);
    - `DeadlineBefore`, `DeadlineAfter`, `UpdatedSince`, `CreatedSince *string`;
    - `HasWhen`, `HighPriority *bool`;
    - `Sort string`, `Limit int` (0 = unlimited), `Cursor string`.
  - `func (r *TaskRepository) List(f model.TaskFilters) (tasks []model.TaskListItem, nextCursor string, err error)`. The return arity changes; update the callers.
  - `encodeCursor(sortKey any, id string) string` / `decodeCursor(s string) (sortKey string, id string, err error)`: base64url JSON. They are shared with search.
  - `func ftsQuery(input string) string`. Split the input on whitespace and quote each token as an FTS5 string, doubling any `"`. With `prefix` true it appends `*`. All tokens are joined with spaces (AND).
  - `/api/search` uses it with prefix; the `search` param on `/api/tasks` uses it without.
  - `SearchRepository.Search(query string, limit int, cursor string) ([]model.SearchResult, string, error)`, ordered by `rank, id`.

- [ ] **Step 1: Write failing tests.**
  - `TestTaskQuery_Filters`: a table test with one case per param in spec C1, each with the exact expected ids. Include `tag_mode=all`, `has_when=false`, `high_priority=true`, `deadline_before`, `updated_since`, `status=open,completed`, `list=today` and `list=trash`. `list=trash` returns only trashed tasks, including with `status=open`.
  - `TestTaskQuery_Sort`: `sort=-deadline`, `sort=title`, and `manual` with `project_id` (orders by `sort_order_project`).
  - `TestTaskQuery_CursorPages`: with 7 tasks and `limit=3` → pages 3, 3, 1, no duplicates or gaps, and the last `next_cursor` is null.
  - `TestTaskQuery_NoLimitReturnsAll`: 600 tasks without `limit` → 600 and `next_cursor` null.
  - `TestTaskQuery_InvalidParams`: `limit=0`, `limit=501`, `sort=nope`, `list=nope`, `tag_mode=x`, `has_when=maybe` and an unknown param `foo=1` → 400 `VALIDATION` with `field`.
  - `TestTaskQuery_FillsNames`: `project_name` and `area_name` are set.
  - `TestSearch_LiteralPunctuation`: `c++` finds "c++ notes"; `email:` and `milk-2` → 200 with their expected matches.
  - `TestSearch_Prefix`: `mil` finds "milk".
  - `TestSearch_CyrillicPrefix` (Review Focus 5): `покуп` finds "Покупки на неделю" in both endpoints. The `search` param on `/api/tasks` uses the full word `покупки`.
  - `TestSearch_PagesByRankStable`: equal-rank results page without duplicates, and the response keeps the `results` key.
- [ ] **Step 2: Run them.** Expected: FAIL.
- [ ] **Step 3: Implement.**
  - `tasks_fts` uses the default `unicode61` tokenizer, since migration 002 sets none. It case-folds Cyrillic, so no migration is expected. If `TestSearch_CyrillicPrefix` still fails for a tokenizer reason, stop and report rather than changing the FTS schema.
  - `list=` uses `listCondition` from Task 15.
- [ ] **Step 4: Run** `go test ./... -race && (cd frontend && npm test)`. Expected: PASS. The CLI resolver still works because there is no `limit`.
- [ ] **Step 5: Docs and commit.** Commit: `feat: filterable paginated task query and safe search`.

### Task 17: Named API tokens (A1)

**Files:**
- Create:
  - `internal/database/migrations/030_api_tokens.sql`
  - `internal/repository/api_tokens.go`
  - `internal/handler/api_tokens.go`
- Modify: `internal/middleware/auth.go`, `internal/router/router.go`, `internal/cli/app.go`, `internal/cli/client.go` (if it needs a DELETE helper)
- Test: `internal/repository/api_tokens_test.go`, `internal/middleware/auth_test.go`, `internal/handler/api_tokens_test.go`, `internal/cli/app_test.go`

**Interfaces:**
- Produces:
  - Table `api_tokens(id TEXT PK, name TEXT NOT NULL UNIQUE, token_hash TEXT NOT NULL UNIQUE, prefix TEXT NOT NULL, created_at, last_used_at, revoked_at)`.
  - `const (AuthSession = "session"; AuthMasterKey = "master_key"; AuthToken = "token")` and `mw.AuthMethodKey contextKey`. `mw.AuthMethod(ctx) string`.
  - `func (r *APITokenRepository) Create(name string) (id, token string, err error)`.
  - `List() ([]model.APIToken, error)` returns no hash.
  - `Revoke(id string) error`, and `Lookup(token string) (id string, ok bool, err error)`, which checks active rows only.
  - `TouchLastUsed(id string)` updates at most once per minute per id, using an in-memory last-write map.
  - The `mw.Auth` signature gains a `TokenLookupFunc func(token string) (id string, ok bool)`. In `none` mode, Bearer is ignored.
  - Routes: `GET`/`POST /api/tokens`, `DELETE /api/tokens/{id}`. Each handler rejects `AuthToken` with 403 `FORBIDDEN`.
  - CLI: `ttd token create <name>` (prints the token once), `ttd token list`, `ttd token revoke <id>`.

- [ ] **Step 1: Write failing tests.**
  - `TestTokenCreate_PlaintextOnceHashStored`: the response has `token` starting `ttd_`; the DB row has `sha256(token)` and no plaintext; `GET` shows `id`, `name` and `prefix`, but not the hash or the token.
  - `TestTokenAuth_ValidRevokedWrong`:
    - a valid token → 200 on `/api/tasks`;
    - after revoke → 401;
    - a wrong Bearer → 401 with no cookie fallback.
  - `TestTokenManage_ForbiddenWithToken`: `POST /api/tokens` with a token → 403; with the env `API_KEY` → 201; with a proxy session → 201.
  - `TestTokenCreate_DuplicateNameIs409`, and `TestTokenCreate_IgnoresClientID`: a body `id` is not used.
  - `TestTokenAuth_NoneModeIgnoresBearer`.
  - `TestTokenLastUsed_Throttled`: two requests within a minute leave one write (inject the clock).
  - `TestCLI_TokenCommands`: against a test server, `create` prints the token, `list` prints the id, and `revoke` succeeds.
- [ ] **Step 2: Run them.** Expected: FAIL.
- [ ] **Step 3: Implement.** Log no token values anywhere: check `mw.Logger` does not log headers.
- [ ] **Step 4: Run** `go test ./... -race`. Expected: PASS.
- [ ] **Step 5: Docs and commit.** Document the token API in `docs/api.md` and `README.md`. Commit: `feat: named revocable API tokens`.

### Task 18: CORS and the cross-origin session guard (A2, A3)

**Files:**
- Create: `internal/middleware/cors.go`
- Modify: `internal/middleware/auth.go`, `internal/router/router.go`
- Test: `internal/middleware/cors_test.go`, `internal/router/router_test.go`

**Interfaces:**
- Produces: `mw.CORS(enabled bool) func(http.Handler) http.Handler`.
  - It sets the spec A2 headers on every response.
  - It answers `OPTIONS` with 204 before auth.
  - With `enabled=false` it adds nothing. The router passes `cfg.AuthMode != "none"`.
  - It is mounted on `/api` and `/mcp`.
- The auth middleware rejects with 403 `FORBIDDEN` when the method would be `AuthSession` and the request has an `Origin` whose host differs from `r.Host`. Public routes are outside the auth group and stay exempt.

- [ ] **Step 1: Write failing tests.**
  - `TestCORS_Preflight`: `OPTIONS /api/tasks` with an `Origin` → 204, every A2 header, and no `Access-Control-Allow-Credentials`.
  - `TestCORS_PreflightLowercaseHeaders` (Review Focus 4).
  - `TestCORS_TokenGetCrossOrigin`: `Allow-Origin: *` on a 200.
  - `TestCORS_OffInNoneMode`: no CORS headers.
  - `TestAuth_CrossOriginSessionForbidden`: a proxy-session `text/plain` POST with `Origin: https://evil.example` → 403. With `Origin` equal to `Host` → 2xx. With no `Origin` → 2xx. A Bearer with a foreign `Origin` → 2xx.
- [ ] **Step 2: Run them.** Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./... -race && (cd frontend && npm test)`. Expected: PASS.
- [ ] **Step 5: Docs and commit.** In `docs/api.md`, document CORS and SSE via `fetch` with a short snippet that reads `/api/events` with `Authorization`. Commit: `feat: open CORS for token requests with cross-origin session guard`.

### Task 19: Reminders fire at the right local time (E4)

**Files:** Modify `internal/scheduler/scheduler.go`. Test: `internal/scheduler/scheduler_test.go`.

**Interfaces:**
- Produces:
  - `computeFireAt(p model.PendingReminder, loc *time.Location) time.Time` loses the `morningTime` parameter, and morning is the constant `09:00`. The anchor is built with `time.Date(y, m, d, hh, mm, 0, 0, loc)`, and relative types subtract from it.
  - `func (s *Scheduler) remindersDue(now time.Time) []dueReminder`, unexported and testable. It selects reminders with `lastTick < fireAt <= now`, where `lastTick` is the previous tick time (initially `now - 1 minute`).
  - Exact reminders parse RFC 3339 first, then the legacy naive layouts in `loc`.
- Removes: the dead settings read (`scheduler.go:211-216`).

- [ ] **Step 1: Write failing tests.**
  - `TestComputeFireAt_AllTypes`: a table test with exact expected times in `Europe/Moscow`.
  - `TestComputeFireAt_DST`: in `Europe/Berlin`, `at_start` 09:00 on `2026-03-29` and on `2026-10-25` → 09:00 local on both days.
  - `TestRemindersDue_NeverEarly`: with `fireAt` 09:00, a tick at 08:59:30 → not due; at 09:00:00 → due; at 09:00:40 with the previous tick at 08:59:40 → due once. The next tick → not due again (dedup).
  - `TestExactReminder_RFC3339Fires`: stored `2026-10-20T06:00:00Z` fires at 09:00 Moscow.
- [ ] **Step 2: Run them.** Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./... -race`. Expected: PASS.
- [ ] **Step 5: Docs and commit.** Fix `docs/api.md:474` (no morning setting). Commit: `fix: reminders use local wall-clock time and never fire early`.

### Task 20: MCP server inside the Go binary (B)

**Files:**
- Create:
  - `internal/mcpserver/server.go`
  - `internal/mcpserver/dispatch.go`
  - `internal/mcpserver/tools_tasks.go`, `tools_lists.go`, `tools_meta.go` (projects, areas, tags, headings), `tools_details.go` (checklist, attachments, schedules, reminders, repeat), `tools_misc.go` (views, settings, bulk, search)
- Modify: `go.mod`, `internal/router/router.go`
- Delete: `mcp/`
- Test: `internal/mcpserver/server_test.go`

**Interfaces:**
- Consumes: the full router built in `router.New`.
- Produces:
  - `func New(api http.Handler) http.Handler` returns `mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{Stateless: true})`. The router mounts it at `/mcp` behind CORS and `mw.Auth`.
  - `func call(ctx context.Context, api http.Handler, hdr http.Header, method, path string, body any) (json.RawMessage, error)`:
    - builds an in-process request with the caller's `Authorization` header, taken from the go-sdk request's `Extra.Header`. Verify that field in v1.8.0 before writing tools; if it does not exist, stop and report.
    - serves it via `httptest.NewRecorder`;
    - returns the body for 2xx, otherwise an error whose text is the API error body.
  - A tool handler error becomes an MCP tool error result (`IsError: true`).
- **Tools:**
  - Port all 44 tools in `mcp/src/tools/*.ts` with the same names. Add:
    - `query_tasks` (the C1 params; always sends `limit`, default 50);
    - `get_repeat_rule`, `set_repeat_rule`, `delete_repeat_rule`;
    - `list_reminders`, `add_reminder`, `delete_reminder`;
    - `get_settings`, `update_settings`;
    - `get_counts`.
  - Descriptions state formats (`YYYY-MM-DD`, `HH:MM`, RFC 3339), that `null` clears a field, and the retry-with-same-`id` rule on every create tool.
  - Nullable fields in `update_task` are typed so JSON `null` reaches the API. Use `*string` fields with an explicit-presence map, or `json.RawMessage`. Absent and null must differ.

- [ ] **Step 1: Add the dependency.** `go get github.com/modelcontextprotocol/go-sdk@v1.8.0`. Confirm `go.mod` stays at `go 1.27.0`.
- [ ] **Step 2: Write failing tests.** Use the go-sdk in-memory transports, or an `httptest` server with an MCP client over Streamable HTTP, against a full `router.New` with a token.
  - `TestMCP_ListsTools`: includes every ported name plus the new ones.
  - `TestMCP_CreateAndQueryTask`.
  - `TestMCP_UpdateTaskClearsWhenDate` (Review Focus 2): the date and schedules are cleared.
  - `TestMCP_ValidationIsToolError`: `create_task` with `when_date:"tomorrow"` → `IsError`, and the text contains `VALIDATION`.
  - `TestMCP_CreateRetryWithSameID`: two calls → one task.
  - `TestMCP_CompleteRecurringOnce`.
  - `TestMCP_Unauthenticated401`: a raw HTTP POST to `/mcp` without a token → 401.
- [ ] **Step 3: Run** `go test ./internal/mcpserver/`. Expected: FAIL.
- [ ] **Step 4: Implement.** Delete `mcp/`. Remove the MCP section that builds the TS server from README, replace it with the Hermes config from spec B, and update `CHANGELOG.md`.
- [ ] **Step 5: Run** `go test ./... -race && go vet ./...`. Expected: PASS.
- [ ] **Step 6: Commit.** `feat: serve MCP over Streamable HTTP from the Go server`.

### Task 21: OpenAPI contract, drift test, docs and end-to-end verification (F)

**Files:**
- Create: `docs/openapi.yaml`, `docs/openapi.go` (package `docs`, embeds the YAML), `internal/router/openapi_test.go`
- Modify:
  - `internal/router/router.go`
  - `docs/api.md`, `README.md`, `CHANGELOG.md`, `docs/review-and-roadmap.md`, `CLAUDE.md`
  - `Makefile` (copy step, if needed)

**Interfaces:**
- Produces:
  - `docs.OpenAPI []byte` (import `github.com/collinjanssen/thingstodo/docs`), embedded with `//go:embed openapi.yaml` in `docs/openapi.go`. `go:embed` only reaches files in its own package directory, so the file stays at `docs/openapi.yaml` as the spec requires.
  - `GET /api/openapi.yaml`, public, with `Content-Type: application/yaml`.

- [ ] **Step 1: Write failing tests.**
  - `TestOpenAPI_MatchesRouter`:
    - walks the `builtin`-mode router with `chi.Walk`;
    - collects `METHOD path` for `/api/*`, normalizing `{id}`;
    - parses the YAML paths: a minimal line scan of `paths:` keys and method keys, because a YAML library would be a new dependency. If parsing that way proves unreliable, stop and ask before adding one.
    - Both sets are equal, except the explicit mode-conditional list `GET /api/auth/oidc/login` and `GET /api/auth/oidc/callback`, which must be in the YAML.
  - `TestOpenAPI_Served`: → 200 without auth.
- [ ] **Step 2: Run them.** Expected: FAIL.
- [ ] **Step 3: Write the OpenAPI document.** OpenAPI 3.1 covering every route, the `bearerAuth` scheme, the error envelope schema, and request and response schemas for each route. Base it on the handlers and the updated `docs/api.md`.
- [ ] **Step 4: Reconcile the docs.**
  - `docs/api.md`: fix every item in `docs/review-and-roadmap.md` §3.6.
  - `docs/review-and-roadmap.md`: project 1 done, projects 2+3 merged into this one, the frontend project renumbered 3.
  - `CLAUDE.md`: replace the "Current Phase" lines with a pointer to `docs/review-and-roadmap.md`.
  - `CHANGELOG.md`: a summary entry.
- [ ] **Step 5: Verify end to end.**
  - Run `go test ./... -race && go vet ./... && golangci-lint run && (cd frontend && npm ci && npm test && npm run lint)`. Expected: all pass.
  - Build and run a local server (`AUTH_MODE=builtin`, a `JWT_SECRET`, an `API_KEY`, `TZ=Europe/Moscow`), then:
    1. `ttd token create widget` → a token.
    2. `curl -i -X OPTIONS -H 'Origin: http://localhost:8080' -H 'Access-Control-Request-Method: GET' -H 'Access-Control-Request-Headers: authorization' localhost:2999/api/tasks` → 204 with the A2 headers. Then a `GET` with the token and that `Origin` → 200 with `Access-Control-Allow-Origin: *`.
    3. Connect an MCP client, e.g. `npx @modelcontextprotocol/inspector` run outside the repo, to `http://localhost:2999/mcp` with `Authorization: Bearer <token>`. List tools; create a daily recurring task; complete it; `query_tasks` shows exactly one new open instance.
    4. `curl '/api/search?q=c%2B%2B'`, `email:` and `milk-2` → 200 with results.
  - Record the commands and their outputs in the final report.
- [ ] **Step 6: Commit.** `docs: OpenAPI contract with drift test; reconcile API docs`.

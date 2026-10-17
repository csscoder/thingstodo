# Frontend audit — ThingsToDo web UI (read-only)

Date: 2026-10-16. Scope: `frontend/` (React 19 + Vite, TanStack Query, Zustand, Dexie local-first sync).
Branch: `csscoder/review-and-test-coverage` (single commit `32dbd08`). No source files modified.

Legend: **verified** = read in code; **inferred** = follows from code read but not executed; backend claims are labelled.

---

## 1. Test and lint run

`frontend/node_modules` does not exist (and no root `node_modules`). Per instructions nothing was installed, so
`npm test` (`vitest run`) and `npm run lint` (`eslint .`) were **not run**. Results: **unverified**.

Configured scripts: `test` = `vitest run`, `test:coverage` = `vitest run --coverage`, `lint` = `eslint .`.
Vitest: jsdom, globals, setup `src/test/setup.ts`, MSW + `fake-indexeddb` available.

12 test files exist (~1.3k lines): `lib/date-parser`, `db/mutations`, `sync/push`, `sync/pull`, `hooks/queries`,
`hooks/localQueries` (one test), pages `Inbox/Today/Project`, components `Sidebar/TaskItem/ThemeToggle`.

---

## 2. Structure map

| Layer | Files | Role |
|---|---|---|
| API clients | `src/api/*.ts` (client, tasks, projects, areas, tags, checklist, attachments, repeat, schedules, reminders, search, savedFilters, settings, auth, push, views, types) | Thin `fetch` wrappers on `/api`. `client.ts:14` throws `ApiError(0,'offline')` when `navigator.onLine` is false. |
| Local DB | `src/db/index.ts` (Dexie v3, 14 tables), `db/schema.ts`, `db/mutations.ts` (838 lines), `db/attachmentCache.ts` | IndexedDB mirror of server entities + `syncQueue` + `syncMeta(cursor)` + file cache + `seenReminders`. All task/project/area/tag/checklist/schedule/heading/reminder writes go through `mutations.ts` → local put + queue entry + fire-and-forget `syncNow()`. |
| Sync engine | `src/sync/engine.ts`, `pull.ts`, `push.ts`, `status.ts`, `reminders.ts`, `missedRemindersEmitter.ts` | Push queue → `POST /sync/push`, then `GET /sync/pull?since=cursor` paginated; `GET /sync/full` on first load or 410. 30 s interval + online + visibility triggers. |
| Read hooks | `src/hooks/localQueries.ts` (1196 lines) | `useLiveQuery` builders for **every** view (Inbox, Today, Upcoming, Anytime, Someday, Logbook, Trash, counts, project/area/tag detail, task detail). |
| Write/legacy hooks | `src/hooks/queries.ts` (1031 lines) | TanStack mutations wrapping local mutations; a few still call REST directly. Server view query hooks (`useInbox`…`useViewCounts`, `:93-147`) are **unused by any page**. |
| Other hooks | `useSSE.ts`, `useBulkAction.ts`, `useResolveTags.ts`, `useKeyboardShortcuts.ts`, `useDetailShortcuts.ts`, swipe/pull-to-refresh/theme | SSE triggers `syncNow()` and TanStack invalidation. |
| Stores | `stores/app.ts` (UI state: selection, expanded/departing task, modals), `stores/filters.ts` (filter state), `sync/status.ts` | Zustand. |
| Lib | `date-parser`, `time-parser`, `parse-tags`, `filter-tasks`, `sort-position`, `format-*`, `reserved-tags`, `tag-colors`, `push-registration`, `sounds` | Pure helpers; most business-relevant logic lives here and in `localQueries`. |
| Pages | `pages/*View.tsx` | All read from `useLocal*` hooks. |
| Big components | `TaskDetail.tsx` (1225), `Sidebar.tsx` (1288), `AppDndContext.tsx` (634), `FilterBar.tsx`, `QuickEntry.tsx`, `ScheduleEditor.tsx`, `SortableTaskItem.tsx` | DnD and schedule editing contain business rules. |

---

## 3. How the frontend talks to the backend

**Views are computed locally from IndexedDB. The `/api/views/*` endpoints are not used by the UI** (verified: every page imports
`useLocal*`; `grep` finds no page/component calling `useInbox/useToday/...`). The only consumer of a server view hook is
`hooks/__tests__/queries.test.tsx` (tests the dead `useInbox`).

Endpoints the running UI actually calls:

- Sync: `POST /sync/push`, `GET /sync/pull?since=&limit=500`, `GET /sync/full` (`sync/push.ts:101`, `sync/pull.ts:163,181`).
- SSE: `GET /api/events` (`useSSE.ts:84`) — only used as a "something changed → syncNow()" signal + reminder toasts.
- Direct REST (bypassing the queue, fail when offline):
  - `PATCH /tasks/reorder` (`AppDndContext.tsx:595`), `POST /tasks/bulk` (`AppDndContext.tsx:232-272`, `useKeyboardShortcuts.ts:352`),
    `DELETE /tasks/{id}/purge` (`TrashView.tsx:24`).
  - `PATCH /projects/{id}` + `/projects/reorder` (`AppDndContext.tsx:374,377`, `queries.ts:515`), `/areas/reorder` (`AppDndContext.tsx:302`, `queries.ts:594`),
    `/tags/reorder` (`AppDndContext.tsx:326`, `queries.ts:656`), `DELETE /areas/{id}` (`queries.ts:580`), `DELETE /tags/{id}` (`queries.ts:642`).
  - `PUT|DELETE /tasks/{id}/repeat` (`queries.ts:800,821`), `POST /tasks/{id}/attachments` multipart (`queries.ts:739`),
    `PATCH /attachments/{id}` (`queries.ts:776`), `PATCH /tasks/{id}/schedules/reorder` (`queries.ts:914`).
  - `GET /search` (`SearchOverlay`), `/saved-filters` CRUD, `/user/settings`, `/auth/login|logout|me`, `/push/*`, `GET /attachments/{id}/file`.
- Everything else (task CRUD/complete/cancel/wontdo/reopen/restore/review, checklist, schedules, reminders, headings,
  project/area/tag create/update, link attachments) goes through the **sync queue**, not the REST endpoints.

Consequence for AI-first: business behaviour attached to REST endpoints is only exercised by the UI through
`/sync/push` field diffs. Backend `sync.go` maps `status`/`deleted_at` field changes to `Complete/Cancel/WontDo/Reopen/Delete/Restore`
(verified `internal/handler/sync.go:484-563`), so recurrence (`scheduler.HandleTaskDone`) and schedule cleanup do run on the server.

---

## 4. Bugs and risks (verified in code unless marked)

Severity: H = data corruption/loss or broken core flow; M = wrong result in a common case; L = edge case / hygiene.

### H1. Multi-drag to "Completed" creates duplicate recurring instances
`AppDndContext.tsx:270-273`: for a multi-selection dropped on the Completed target, the code runs **both**
`localMutations.completeTask(id)` (→ queue → `/sync/push` status=completed → backend `HandleTaskDone`) **and**
`bulkAction({action:'complete'})` (→ backend `handler/tasks.go:386-389` `HandleTaskDone`).
Backend `TaskRepository.Complete` has no `WHERE status='open'` guard (`repository/tasks.go:364-366`) and
`scheduler.createNextInstance` has no idempotency check (`scheduler/scheduler.go:134-163`) — **verified**.
Scenario: select 3 repeating tasks, drag to Completed → each spawns two next instances.
The same double-path (local mutation + REST bulk) exists for set_when/someday/anytime/project/tag/trash (`:227-268`); harmless
for idempotent fields but doubles writes and change-log entries. `bulkAction(...).then()` has no `.catch` → unhandled rejection offline.
Related (inferred): any re-push of a queued `status=completed` change after a lost response also double-spawns.

### H2. Pull deletes server-backed schedules after a full sync
`sync/pull.ts:126-136` deletes every local schedule of the task without `_serverSeq` when a remote schedule `create` arrives
(meant to drop the temp schedule from `mutations.ts:61-77`). But `fullSync` writes all rows **without** `_serverSeq`
(`pull.ts:213-219`, `withMeta` called without `seq` on every table, `:221-230`).
Scenario: after first load / cursor expiry, task has 2 schedule entries; another device or the AI agent adds a 3rd → this
client deletes the 2 existing entries locally (server still has them) until the next full sync. Today/Upcoming then hide those dates.

### H3. Push queue: sibling failures dropped, poison entries retried forever
`sync/push.ts:109-121` removes queue entries by `entityId` only. If entity X has `[create OK, update ERROR]` in one batch, the failed
update is deleted together with the create (reported once in the status, then lost).
Conversely an entry that always errors (e.g. `task not found` after the agent purged the task via API) is never removed
(only `unsupported entity type` is dropped), is re-sent every 30 s, keeps sync status `error`, and `pull.ts:91-96` skips every server
change for that entity while the cursor advances (`:172-176`). There is no UI to discard a stuck entry.
Note: this is not general data loss — once a push succeeds, the server logs a full snapshot that repairs the local copy.

### H4. Initial full sync failure locks the app behind an overlay
`AppLayout.tsx:45-63`: `init()` has no try/catch. First launch while offline (`client.ts:14` throws) or a 5xx from `/sync/full` →
`setInitialSyncing(false)` never runs, the full-screen `SyncingOverlay` (`z-50`, `AppLayout.tsx:102`) stays forever and
`startSyncEngine()` is never called (no retry when online returns). The effect also runs before `useMe()` resolves.
StrictMode double-mount starts two concurrent `fullSync()` calls (inferred, dev only).

### M1. Drag-reorder has no visible effect in Today, Project, Heading, Area, Tag lists
`SortableTaskList` writes `sort_order_today|project|heading` (`TodayView.tsx:126`, `ProjectView.tsx:64,78`, `AreaView.tsx:100`), but
the data source re-sorts by `high_priority desc, start_time, title` and ignores sort_order:
`useLocalToday` `localQueries.ts:414-423`; `sortTasks` `:318-324` used by project/area/tag/heading (`:1029,1046,1071,1108,1117,1192`).
Scenario: drag task B above task A in Today → it snaps back. Also `calculatePosition` (`lib/sort-position.ts:9-19`) takes midpoints
between neighbours whose sort_order is not monotonic in the displayed order, producing arbitrary positions.
Server Today orders by `sort_order_today, start_time` (`repository/views.go:138`, verified) → UI and API disagree on order.
Inbox/Anytime/Someday do sort by `sort_order_today` (`:281`, `:635`) and work.

### M2. "Today" computed in UTC at several sites; elsewhere local time
`todayString()` (`localQueries.ts:55-61`) uses local date, but these use `toISOString()` (UTC):
`AppDndContext.tsx:227,516` (drop on Today), `TaskDetail.tsx:252`, `ScheduleEditor.tsx:175,185,310`, `SearchOverlay.tsx:62`.
Scenario (owner in UTC+3): at 00:30 local, dropping a task on Today sets `when_date` = yesterday → task lands in "Earlier", not Today.
In UTC-5 after 19:00 it sets tomorrow → task disappears into Upcoming. ScheduleEditor's "add next date" and past/today highlighting are off by one in the same window.
Also: completed-today filter compares UTC ISO timestamps with a local date (`localQueries.ts:468`), and Logbook/Trash group by
UTC date `ts.slice(0,10)` (`:762`, `:788`) → items completed after local midnight in UTC+X show under the previous day.
Review cutoff mixes local `setDate` with `toISOString().slice(0,10)` (`:287-289`, `:986-988`).

### M3. Natural-date parser rolls today's date into next year
`lib/date-parser.ts:116` and `:128`: candidate is local midnight, compared with `isBefore(candidate, new Date())` (current time).
Typing "oct 16" or "10/16" on Oct 16 returns 2027-10-16. The test pins system time to exact midnight (`date-parser.test.ts:13`,
`new Date(2026, 1, 15)`), which hides the bug. `"feb 31"` → `new Date(y,1,31)` overflows to Mar 3 and passes `isValid` (`:113-114`).

### M4. Tag token parsing is ASCII-only
`lib/parse-tags.ts:6,32`: `/#([\w-]+)/g` without the `u` flag and `\p{L}`. `#покупки` is not parsed — it stays in the title and no tag is
created. Relevant for a Russian-speaking owner. `$project` matching is a case-insensitive prefix match with no word boundary
(`:50`): with projects "Home" and "Homework", `$Homeworkout` resolves to "Homework" and leaves "out" in the title.

### M5. Direct-REST mutations that change local state first (offline divergence)
- `useDeleteArea` / `useDeleteTag` (`queries.ts:575-587`, `:637-649`) delete from Dexie, then call REST. Offline: local row gone, server row
  remains, nothing queued, no cascade to tasks. Anytime/Someday then show the raw area id as the title (`localQueries.ts:679`, `:689`); task
  TagRefs keep the deleted tag.
- Reorders (`queries.ts:514,593,655,913`; `AppDndContext.tsx:593`) write Dexie without a queue entry, then REST. Offline → rejected promise
  (no catch at `AppDndContext.tsx:302,326,595`), local order persists until an unrelated server change overwrites it.
- DnD area/tag/project reorder (`AppDndContext.tsx:284-389`) patches only the TanStack cache, which the sidebar no longer reads
  (it uses `useLocal*`) → no optimistic feedback; project area move is server-only.
- `TrashView.tsx:23-28`: `Promise.all` over `purgeTask` without try/finally. One 404 (task never pushed) or offline → `purging` stays
  true, button disabled, unhandled rejection; remaining tasks partly purged.
- Keyboard bulk delete (`useKeyboardShortcuts.ts:347-362`) is REST-only with no catch; offline, `departingTaskIds` is never cleared.

### M6. Missed-reminder pipeline is dead code
`checkMissedReminders`, `scheduleNearTermReminders`, `clearScheduledReminders` (`sync/reminders.ts`) and `emitMissedReminders`
(`missedRemindersEmitter.ts:11`) have no callers. `MissedRemindersToast` subscribes (`MissedRemindersToast.tsx:14`) but never fires.
`checkMissedReminders` also expects `entityId/data` fields while pull returns `entity_id/snapshot` (`reminders.ts:4-10` vs `pull.ts:32-40`),
so wiring it in as-is would not work. Reminders only surface via the SSE `reminder_fired` toast (`useSSE.ts:169-175`) and server push.

### L1. Sidebar Today badge ≠ Today list
`useLocalViewCounts` (`localQueries.ts:969-973,1001`) counts `when_date=today OR deadline=today` + overdue, ignoring tasks pulled into Today
by a schedule entry and including tasks whose today schedules are all completed; it omits "Earlier". Server `/views/counts` has the same
formula (`repository/views.go:877`, verified), so both are consistent with each other but not with the Today list.

### L2. Inbox and Anytime both include deadline-only tasks
Inbox = open, no project/area/when (`localQueries.ts:274-280`); Anytime adds `|| !!t.deadline` (`:601`). A task with only a deadline
appears in both and is counted twice. Same in server counts (`views.go:876,879`) — verify intent.

### L3. Tasks that vanish from every view
Open task with `when_date < today` and no uncompleted past schedule (e.g. all entries completed, or created via API without a schedule)
is excluded from Today (`:350-365`), Earlier (`:449-456` requires `pastCount>0`), Upcoming (needs schedule ≥ today), Inbox and Anytime
(both require `!when_date`). Server Earlier has the same EXISTS condition (`views.go:237`). Only search / project views show it.

### L4. Misc
- `createProject` defaults `area_id: ''` and `area: {id:'', title:''}` (`mutations.ts:292-293`); backend sync create only skips `nil`
  (`sync.go:600`) → **inferred**: a project without an area is stored with
  `area_id = ''` (FK/empty-string semantics not verified).
- `useLocalProjectDetail` returns `area: {id: project.area_id, title: project.area_id}` when area is missing (`localQueries.ts:1128`).
- `useLocalTask` synthesizes a `synth-<id>` schedule when none exist (`:834-845`); update/delete paths special-case the prefix
  (`queries.ts:875,899`) — correct but fragile, and duplicates server `syncFirstScheduleDate` (`mutations.ts:45-93`).
- `queueChange` triggers `syncNow()` per mutation, but `syncNow` is a no-op while a sync is running (`engine.ts:26`); a change made during a
  sync waits for the next 30 s tick or SSE event → up to 30 s before the agent/other clients see it.
- Logout (`queries.ts:954-963`) clears only the TanStack cache; IndexedDB data and the pending `syncQueue` stay on the device. Data model is
  single-tenant (tasks have no `user_id`), so this is a privacy-on-shared-device issue rather than a cross-user leak.
- Performance: `enrichTask(s)` runs ~6 Dexie queries per task (`localQueries.ts:121-246`); Logbook/Trash load and enrich every row
  (`:742-758`, `:779-785`, no pagination); Today "completed" does a full-table scan (`:463-470`); `useLocalTags` scans all open tasks once
  per tag (`:927-933`). Grows linearly with history.
- (Backend, inferred) `sync.go:419-421` labels server-newer updates `conflict_resolved` but still applies the client value → a stale web edit
  replayed from the queue overwrites a newer agent edit (client-wins LWW).
- (Backend, verified) `applyTaskChange` returns early on the first `status`/`deleted_at` field (`sync.go:484-563`); other fields in the same
  change are dropped. Today no UI path sends status together with other user fields, but an API/agent client using `/sync/push` could.

---

## 5. Business logic living in the frontend (relevant for API / AI clients)

### 5a. Logic duplicated with the backend but divergent (the UI is not a spec for API clients)

The server already has `/api/views/*`; the UI ignores them and re-implements each view in `hooks/localQueries.ts`. Known differences:

| Concern | Frontend (`localQueries.ts`) | Backend (`repository/views.go`) |
|---|---|---|
| Today order | priority desc, start_time, title (`:414-423`) | `sort_order_today, ts.start_time` (`:138,176`) |
| Today rows | one row per task (`seenIds`, `:345-391`) | one row per today schedule entry (LEFT JOIN, `:128`) |
| Today evening split | first uncompleted today entry with `start_time >= eveningStartsAt` (`:403-411`) | per-entry `ts.start_time >= ?` (`:175`) |
| Project/area/tag/heading order | priority/time/title (`sortTasks :318`) | inferred: sort_order columns (not re-checked) |
| Completed today | local-date string vs UTC timestamp (`:468`) | not compared |
| "Today" date | browser local date (`:55`) | `time.Now()` in server/container TZ (`views.go:103,872`) |
| Review cutoff | `updated_at.slice(0,10) < local-minus-N → UTC date` (`:287-299`) | `date(updated_at) < date('now','-N days')` UTC (`:75`) |
| Schedule flags | `computeScheduleFlags :83-115` (all_today_schedules_completed = **all** entries completed) | `populateActionableScheduleFlags`, `populatePastScheduleCounts` |
| Counts | `useLocalViewCounts :950-1010` | `views.go:872-900` (same formula, verified) |
| Denormalized fields | `enrichTask(s) :121-246`, cascades `mutations.ts:314-331,396-406,463-477,515-568` | SQL subqueries/JOINs |

Recommendation: for the AI agent the timezone must become an API input (header or `tz` param) — server `time.Now()` and the browser
disagree whenever the container is not in the owner's TZ. Pick one canonical implementation (server) and either make the web UI call
`/api/views/*` or add contract tests that run the same fixtures through both.

### 5b. Logic with no server equivalent (would be missing for API clients)

| Logic | Location | Notes |
|---|---|---|
| Natural-language date parsing ("tomorrow", "fri", "in 3 weeks", "jan 15", "10/16") | `lib/date-parser.ts:59-136`, used by `DateInput.tsx:54` | REST accepts only ISO/`someday`. Go CLI has its own parser (`internal/cli/dateparse.go:15`) with different semantics: "next week" = +7 days there vs next Monday here. Has bug M3. |
| Time parsing ("9", "2pm", "21:30") | `lib/time-parser.ts:7-36`, `TimeInput.tsx:42`, `SettingsView.tsx:111` | |
| Title token parsing `#tag`, `$Project/Area` + tag auto-create | `lib/parse-tags.ts:27-73`, `hooks/useResolveTags.ts:18-75`, `QuickEntry.tsx:100-122` | Reserved tags skipped (`lib/reserved-tags.ts`). Has bug M4. CLI has `parseInlineAdd` (`cli/dateparse.go:62`) — separate implementation. |
| Saved-filter evaluation | `lib/filter-tasks.ts:28-195`, `stores/filters.ts` | Filters are stored server-side (`/saved-filters`) but only evaluated in the browser: presets today/thisWeek (Mon start)/thisMonth/next7/next30/overdue/noDate, ranges, OR within areas/projects/tags, AND across. An agent cannot "run" a saved filter. |
| Fractional sort positioning | `lib/sort-position.ts:9-19`, `AppDndContext.tsx:79-87` (`calculateEntityPosition`) | Midpoint / +1024 / half of first. No rebalancing. |
| Drag-and-drop semantics | `AppDndContext.tsx:391-569` | Inbox drop clears project+area+when; project drop inherits project's area; area drop clears project; any sidebar drop restores trashed and reopens completed tasks; tag drop only adds. |
| Bulk actions (local path) | `hooks/useBulkAction.ts:9-92` | toggle_priority / add/remove/toggle_tags computed client-side then sent as field diffs. Server `/tasks/bulk` exists with its own implementation (`repository/tasks.go:459+`); param names differ (`set_priority` reads `priority` on server, UI local path uses `high_priority`). |
| Schedule rules | `ScheduleEditor.tsx:166-186` (next free date, skip dates with timeless entries, 30-day window), `:189-196,257-262` (no duplicate timeless dates), `queries.ts:836-869` (materialize first entry from when_date before adding, sort_order max+1024) | Server does not enforce duplicate-date rule (inferred, not checked). |
| First-schedule sync on when_date change | `mutations.ts:45-93` | Mirrors server `syncFirstScheduleDate` (local optimistic copy). |
| Completion side rules in UI | `TaskDetail.tsx:252-262` (confirm when actionable schedules remain) | UI-only prompt. |
| Task → view routing | `SearchOverlay.tsx:55-66` | project → area → someday → today/upcoming → inbox. |
| "Review" semantics | `queries.ts:450`, `useBulkAction.ts:88` | Reviewing = bump `updated_at`; server has `/tasks/{id}/review` and handles `updated_at` in Raw (`repository/tasks.go:259-261`). |
| Upcoming grouping | `UpcomingView.tsx:25-70` (`groupDateSections`: days / rest-of-month / month buckets) | Presentation, but an agent/widget would need the same buckets. |
| Labels | `lib/format-repeat.ts`, `lib/format-reminder.ts`, `lib/format-date.ts` | Human-readable recurrence/reminder text; useful for an agent's replies. |

Already on the server (no need to move): recurrence generation (`internal/scheduler`, `internal/recurrence`), reminder firing and
push (`internal/push`, scheduler), schedule cleanup on completion (`sync.go:1214`), denormalized counts in REST/view responses.

---

## 6. Test coverage — untested modules ranked by risk

Page tests mock `hooks/localQueries` entirely (`hooks/__mocks__/localQueries.ts`, `vi.mock` in Inbox/Today/Project tests), so they test
rendering only, not view logic.

| Rank | Module | Current coverage | Why it matters |
|---|---|---|---|
| 1 | `hooks/localQueries.ts` view builders (Today, Upcoming, Anytime/Someday, Logbook, Trash, project/area/tag detail, enrich, schedule flags) | only `useLocalViewCounts` (1 test) | All user-visible view semantics; divergent from server (5a); bugs M1, M2, L1-L3. |
| 2 | `sync/engine.ts`, `sync/pull.ts` `fullSync` + schedule dedup, `sync/push.ts` partial-failure handling | pull/push happy paths + one skip test; no fullSync, no engine, no mixed results per entity | H2, H3, 410 fallback, status transitions. |
| 3 | `components/AppDndContext.tsx` | none | H1 double write, M1/M2, drop semantics. |
| 4 | `db/mutations.ts` cascades & derived flags (`syncFirstScheduleDate`, project/area/tag cascades, checklist/attachment/reminder flags, tag_ids→tags) | 6 basic CRUD tests | Denormalized data correctness. |
| 5 | `lib/parse-tags.ts` + `hooks/useResolveTags.ts` | none | M4; tag auto-creation. |
| 6 | `lib/filter-tasks.ts` | none | Saved-filter semantics (week start, ranges, noDate). |
| 7 | `lib/date-parser.ts` | 15 tests, but time pinned to midnight | M3 hidden; no test for "today's date", invalid day overflow. |
| 8 | `lib/sort-position.ts`, `calculateEntityPosition` | none | Ordering. |
| 9 | `hooks/useBulkAction.ts`, `TrashView` empty-trash, `queries.ts` REST-first mutations (delete area/tag, repeat rule, reorders) | none | M5. |
| 10 | `hooks/useSSE.ts`, `sync/reminders.ts` | none | Reminder toasts; dead pipeline (M6). |
| 11 | `lib/time-parser.ts`, `format-repeat.ts`, `format-reminder.ts` | none | Low risk, cheap to cover. |

`hooks/__tests__/queries.test.tsx` spends a test on `useInbox`, which no page uses.

---

## 7. Suggested next steps (for the owner, not done)

1. Fix H1 (drop the REST `bulkAction` calls in the multi-drag path, or make backend completion idempotent: `UPDATE ... WHERE status='open'`
   and only call `HandleTaskDone` when a row changed). Backend idempotency also protects agent retries.
2. Fix H2 (set `_serverSeq` in `fullSync` or mark the temp schedule explicitly, e.g. `_localOnly: true`).
3. Fix H3 (remove queue entries by queue id/result index, not entity id; add discard/retry UI for stuck entries).
4. Wrap `AppLayout` init in try/finally with retry on `online` (H4).
5. Decide the canonical view implementation (server), make "today" timezone-aware in the API, and add shared fixtures/contract tests
   so web, CLI and agent agree (section 5a).
6. Move or expose for agents: natural-date parsing, title-token parsing, saved-filter evaluation (e.g. `GET /api/tasks?filter_id=`),
   sort-position rules. Unify with `internal/cli/dateparse.go`.

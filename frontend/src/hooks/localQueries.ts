/**
 * localQueries.ts
 *
 * Reactive Dexie live-query hooks that read from IndexedDB.
 * These mirror the shapes returned by the server view hooks in queries.ts
 * and will eventually replace TanStack Query reads for local-first operation.
 *
 * useLiveQuery returns `undefined` while loading — callers should treat
 * `undefined` the same as `isLoading: true`.
 */

import { useLiveQuery } from 'dexie-react-hooks'
import { localDb } from '../db/index'
import { addDays, ownerDateOf, ownerToday, useOwnerToday } from '../lib/owner-time'
import type {
  LocalTask,
  LocalProject,
  LocalArea,
  LocalTag,
  LocalChecklistItem,
  LocalAttachment,
  LocalSchedule,
  LocalReminder,
  LocalRepeatRule,
} from '../db/schema'
import type {
  Task,
  InboxView,
  TodayView,
  TodayViewGroup,
  UpcomingView,
  AnytimeView,
  AnytimeViewProjectGroup,
  SomedayView,
  LogbookView,
  TrashView,
  ViewCounts,
  TaskDetail,
  ProjectDetail,
  HeadingWithTasks,
  AreaDetail,
  Project,
  Area,
  Tag,
} from '../api/types'

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Returns today's date as a YYYY-MM-DD string in the owner timezone. */
function todayString(): string {
  return ownerToday()
}

/** Strip sync metadata fields so callers get plain server-shaped types. */
function stripSyncMeta<T extends object>(
  obj: T,
): Omit<T, '_syncStatus' | '_localUpdatedAt' | '_serverSeq'> {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const { _syncStatus, _localUpdatedAt, _serverSeq, ...rest } = obj as any
  void _syncStatus
  void _localUpdatedAt
  void _serverSeq
  return rest
}

function taskToPlain(t: LocalTask): Task {
  return stripSyncMeta(t) as Task
}

/**
 * Compute schedule flags on a plain Task from its local schedule entries.
 * Mirrors the server's populateActionableScheduleFlags / populatePastScheduleCounts.
 */
async function computeScheduleFlags(
  plain: Task,
  today: string = todayString(),
  preloaded?: LocalSchedule[],
): Promise<void> {
  const allSchedules =
    preloaded ??
    (await localDb.schedules.where('task_id').equals(plain.id).toArray())

  if (allSchedules.length === 0) return

  // has_actionable_schedules: uncompleted entries not for today (or someday)
  plain.has_actionable_schedules = allSchedules.some(
    (s) => !s.completed && (s.when_date !== today || s.when_date === 'someday'),
  )

  // all_today_schedules_completed: all entries are completed
  const total = allSchedules.length
  const completed = allSchedules.filter((s) => s.completed).length
  plain.all_today_schedules_completed = total > 0 && completed === total

  // first_schedule_completed: the displayed entry is completed
  if (plain.schedule_entry_id) {
    const entry = allSchedules.find((s) => s.id === plain.schedule_entry_id)
    if (entry) plain.first_schedule_completed = entry.completed
  } else if (plain.first_schedule_time) {
    const sorted = [...allSchedules].sort((a, b) => (a.sort_order ?? 0) - (b.sort_order ?? 0))
    if (sorted.length > 0) plain.first_schedule_completed = sorted[0].completed
  }

  // past_schedule_count: uncompleted entries with when_date < today
  plain.past_schedule_count = allSchedules.filter(
    (s) => !s.completed && s.when_date < today && s.when_date !== 'someday',
  ).length
}

/** start_time ASC with nulls first (SQLite ASC NULL ordering). */
function timeNullsFirst(a: string | null, b: string | null): number {
  if (a === b) return 0
  if (a === null) return -1
  if (b === null) return 1
  return a < b ? -1 : 1
}

/** How a row's displayed schedule time is chosen. The server picks these
 * per view query (views.go); the local builders mirror each mode. */
type ScheduleTimePick =
  /** This row's own schedule entry — Today/Evening rows and Upcoming date
   * rows. A null entry is the LEFT JOIN fallback row: force no time. */
  | { kind: 'row-entry'; entry: LocalSchedule | null }
  /** First schedule row by sort_order, any completion, any date — the
   * generic lists and Overdue. No entry id is exposed there. */
  | { kind: 'first-any' }
  /** First OPEN past entry (when_date < today, never someday) — Earlier. */
  | { kind: 'first-open-past' }

/** Set first_schedule_time / first_schedule_end_time / schedule_entry_id
 * from the task's schedule rows according to the view's selection rule.
 * A null pick keeps the generic default used by the other local views. */
function applyScheduleTimePick(
  plain: Task,
  rows: LocalSchedule[],
  today: string,
  pick: ScheduleTimePick | null,
): void {
  if (pick?.kind === 'row-entry') {
    plain.first_schedule_time = pick.entry?.start_time ?? null
    plain.first_schedule_end_time = pick.entry?.end_time ?? null
    plain.schedule_entry_id = pick.entry?.id ?? null
    return
  }
  if (pick?.kind === 'first-any') {
    const first = [...rows].sort((a, b) => (a.sort_order ?? 0) - (b.sort_order ?? 0))[0]
    plain.first_schedule_time = first?.start_time ?? null
    plain.first_schedule_end_time = first?.end_time ?? null
    plain.schedule_entry_id = null
    return
  }
  if (pick?.kind === 'first-open-past') {
    const first = rows
      .filter((s) => !s.completed && s.when_date < today && s.when_date !== 'someday')
      .sort((a, b) => (a.sort_order ?? 0) - (b.sort_order ?? 0))[0]
    plain.first_schedule_time = first?.start_time ?? null
    plain.first_schedule_end_time = first?.end_time ?? null
    plain.schedule_entry_id = first?.id ?? null
    return
  }
  // Generic default: first uncompleted entry by sort_order.
  if (!plain.first_schedule_time) {
    const first = rows
      .filter((s) => !s.completed)
      .sort((a, b) => (a.sort_order ?? 0) - (b.sort_order ?? 0))[0]
    if (first?.start_time) {
      plain.first_schedule_time = first.start_time
      plain.first_schedule_end_time = first.end_time ?? null
      plain.schedule_entry_id = first.id
    }
  }
}

/**
 * Enrich a task with denormalized project_name, area_name, schedule time, and flags.
 * The server does this via SQL JOINs; we do it by looking up related entities.
 */
async function enrichTask(
  t: LocalTask,
  today: string = todayString(),
  pick?: ScheduleTimePick,
): Promise<Task> {
  const plain = taskToPlain(t)

  // Resolve project/area names
  if (t.project_id && !plain.project_name) {
    const project = await localDb.projects.get(t.project_id)
    if (project) plain.project_name = project.title
  }
  if (t.area_id && !plain.area_name) {
    const area = await localDb.areas.get(t.area_id)
    if (area) plain.area_name = area.title
  }

  // Schedule time selection: view-specific where a pick is given, else
  // the generic first-uncompleted-by-sort_order default.
  const schedules = await localDb.schedules.where('task_id').equals(t.id).toArray()
  applyScheduleTimePick(plain, schedules, today, pick ?? null)

  // Resolve reminder fields
  const reminders = await localDb.reminders
    .where('task_id')
    .equals(t.id)
    .sortBy('created_at')
  plain.has_reminders = reminders.length > 0
  if (reminders.length > 0) {
    const first = reminders[0]
    plain.first_reminder_type = first.type ?? null
    plain.first_reminder_value = first.value ?? null
    plain.first_reminder_exact_at = first.exact_at ?? null
  }

  // Re-derive boolean flags from actual data
  plain.has_notes = !!plain.notes
  const attachments = await localDb.attachments.where('task_id').equals(t.id).toArray()
  plain.has_links = attachments.some((a) => a.type === 'link')
  plain.has_files = attachments.some((a) => a.type === 'file')
  const repeatRules = await localDb.repeatRules.where('task_id').equals(t.id).count()
  plain.has_repeat_rule = repeatRules > 0
  const checklistItems = await localDb.checklistItems.where('task_id').equals(t.id).toArray()
  plain.checklist_count = checklistItems.length
  plain.checklist_done = checklistItems.filter((c) => c.completed).length

  await computeScheduleFlags(plain, today, schedules)
  return plain
}

/**
 * Batch-enrich tasks, caching project/area lookups.
 */
async function enrichTasks(tasks: LocalTask[], today: string = todayString()): Promise<Task[]> {
  // Collect unique project/area IDs
  const projectIds = new Set<string>()
  const areaIds = new Set<string>()
  for (const t of tasks) {
    if (t.project_id && !t.project_name) projectIds.add(t.project_id)
    if (t.area_id && !t.area_name) areaIds.add(t.area_id)
  }

  const projectNames = new Map<string, string>()
  const areaNames = new Map<string, string>()
  for (const pid of projectIds) {
    const p = await localDb.projects.get(pid)
    if (p) projectNames.set(pid, p.title)
  }
  for (const aid of areaIds) {
    const a = await localDb.areas.get(aid)
    if (a) areaNames.set(aid, a.title)
  }

  const result: Task[] = []
  for (const t of tasks) {
    const plain = taskToPlain(t)
    if (t.project_id && !plain.project_name) {
      plain.project_name = projectNames.get(t.project_id) ?? null
    }
    if (t.area_id && !plain.area_name) {
      plain.area_name = areaNames.get(t.area_id) ?? null
    }
    const schedules = await localDb.schedules.where('task_id').equals(t.id).toArray()
    applyScheduleTimePick(plain, schedules, today, null)
    // Resolve reminder fields
    const reminders = await localDb.reminders
      .where('task_id')
      .equals(t.id)
      .sortBy('created_at')
    plain.has_reminders = reminders.length > 0
    if (reminders.length > 0) {
      const first = reminders[0]
      plain.first_reminder_type = first.type ?? null
      plain.first_reminder_value = first.value ?? null
      plain.first_reminder_exact_at = first.exact_at ?? null
    }
    // Re-derive boolean flags from actual data
    plain.has_notes = !!plain.notes
    const attachments = await localDb.attachments.where('task_id').equals(t.id).toArray()
    plain.has_links = attachments.some((a) => a.type === 'link')
    plain.has_files = attachments.some((a) => a.type === 'file')
    const repeatRules = await localDb.repeatRules.where('task_id').equals(t.id).count()
    plain.has_repeat_rule = repeatRules > 0
    const checklistItems = await localDb.checklistItems.where('task_id').equals(t.id).toArray()
    plain.checklist_count = checklistItems.length
    plain.checklist_done = checklistItems.filter((c) => c.completed).length
    await computeScheduleFlags(plain, today, schedules)
    result.push(plain)
  }
  return result
}

function projectToPlain(p: LocalProject): Project {
  return stripSyncMeta(p) as Project
}

function areaToPlain(a: LocalArea): Area {
  return stripSyncMeta(a) as Area
}

function tagToPlain(t: LocalTag): Tag {
  return stripSyncMeta(t) as Tag
}

// ---------------------------------------------------------------------------
// View Hooks
// ---------------------------------------------------------------------------

/**
 * Inbox: open tasks with no project_id, no area_id, no when_date, not deleted.
 * Review: open tasks not updated in `reviewAfterDays`, excluding inbox tasks.
 * When `reviewIncludeRecurring` is false, recurring tasks are excluded from review.
 */
export function useLocalInbox(reviewAfterDays?: number | null, reviewIncludeRecurring = true): InboxView | undefined {
  // Owner-zone today: re-runs at owner midnight so the review cutoff rolls over
  const today = useOwnerToday()
  return useLiveQuery(async () => {
    const tasks = await localDb.tasks
      .where('status')
      .equals('open')
      .filter(
        (t) =>
          !t.project_id &&
          !t.area_id &&
          !t.when_date &&
          !t.deleted_at,
      )
      .sortBy('sort_order_today')

    const inboxIds = new Set(tasks.map((t) => t.id))

    let review: Task[] = []
    if (reviewAfterDays && reviewAfterDays > 0) {
      const cutoffStr = addDays(today, -reviewAfterDays)

      const stale = await localDb.tasks
        .where('status')
        .equals('open')
        .filter(
          (t) =>
            !t.deleted_at &&
            !inboxIds.has(t.id) &&
            (t.updated_at ?? '').slice(0, 10) < cutoffStr &&
            (reviewIncludeRecurring || !t.has_repeat_rule),
        )
        .toArray()

      stale.sort((a, b) => (a.updated_at ?? '').localeCompare(b.updated_at ?? ''))
      review = await enrichTasks(stale, today)
    }

    return {
      tasks: await enrichTasks(tasks, today),
      review,
    } satisfies InboxView
  }, [reviewAfterDays, reviewIncludeRecurring, today])
}

/** Sort by a REAL sort_order column ASC, the server's ORDER BY for the
 *  project/heading lists (sort_order_project) and the area/tag lists
 *  (sort_order_today). */
function bySortOrder(key: 'sort_order_project' | 'sort_order_today') {
  return (a: LocalTask, b: LocalTask): number => (a[key] ?? 0) - (b[key] ?? 0)
}

/** Terminal-timestamp sort for completed lists: the server's
 *  COALESCE(completed_at, canceled_at, updated_at) DESC. */
function byTerminalTsDesc(a: LocalTask, b: LocalTask): number {
  const ts = (t: LocalTask): string => t.completed_at ?? t.canceled_at ?? t.updated_at ?? ''
  return ts(b).localeCompare(ts(a))
}

/** One display row of the Today view: the task plus the open schedule
 *  entry the row shows (null for the timeless membership row). */
interface TodayRow {
  task: LocalTask
  entry: LocalSchedule | null
  startTime: string | null
}

/** All open, non-deleted tasks plus their schedule rows grouped by task. */
async function loadOpenTasksWithSchedules(): Promise<{
  openTasks: LocalTask[]
  rowsByTask: Map<string, LocalSchedule[]>
}> {
  const [openTasks, allSchedules] = await Promise.all([
    localDb.tasks.filter((t) => t.status === 'open' && !t.deleted_at).toArray(),
    localDb.schedules.toArray(),
  ])
  const rowsByTask = new Map<string, LocalSchedule[]>()
  for (const s of allSchedules) {
    const rows = rowsByTask.get(s.task_id)
    if (rows) rows.push(s)
    else rowsByTask.set(s.task_id, [s])
  }
  return { openTasks, rowsByTask }
}

/** Overdue: open tasks with deadline < today, oldest deadline first. */
function collectOverdueTasks(openTasks: LocalTask[], today: string): LocalTask[] {
  return openTasks
    .filter((t) => !!t.deadline && t.deadline < today)
    .sort((a, b) => (a.deadline ?? '').localeCompare(b.deadline ?? ''))
}

/** Earlier: open tasks with when_date < today, never someday, regardless
 *  of schedule rows (E3) — by date, then sort_order_today. */
function collectEarlierTasks(openTasks: LocalTask[], today: string): LocalTask[] {
  return openTasks
    .filter((t) => !!t.when_date && t.when_date !== 'someday' && t.when_date < today)
    .sort(
      (a, b) =>
        (a.when_date ?? '').localeCompare(b.when_date ?? '') ||
        (a.sort_order_today ?? 0) - (b.sort_order_today ?? 0),
    )
}

/** Sort Today rows like the server: sort_order_today ASC, then the row's
 *  start_time ASC with timeless rows first (SQL NULLs first). */
function sortTodayRows(rows: TodayRow[]): TodayRow[] {
  return rows.sort((a, b) =>
    (a.task.sort_order_today ?? 0) - (b.task.sort_order_today ?? 0) ||
    timeNullsFirst(a.startTime, b.startTime),
  )
}

/** Enrich Today rows (each showing its own entry) and group them by
 *  project in first-appearance order — the server's groupByProject. */
async function todayRowsToGroups(rows: TodayRow[], today: string): Promise<TodayViewGroup[]> {
  const order: string[] = []
  const buckets = new Map<string, TodayRow[]>()
  for (const row of rows) {
    const key = row.task.project_id ?? ''
    if (!buckets.has(key)) {
      buckets.set(key, [])
      order.push(key)
    }
    buckets.get(key)!.push(row)
  }

  const groups: TodayViewGroup[] = []
  for (const key of order) {
    const project = key ? await localDb.projects.get(key) : undefined
    const tasks: Task[] = []
    for (const row of buckets.get(key)!) {
      tasks.push(await enrichTask(row.task, today, { kind: 'row-entry', entry: row.entry }))
    }
    groups.push({ project: project ? { id: project.id, title: project.title } : null, tasks })
  }
  return groups
}

/**
 * Today: mirrors the server rules in internal/repository/views.go —
 * one row per OPEN today schedule entry (an entry at or after eveningStartsAt
 * joins This Evening), plus one timeless row for a member with no open today
 * entries (deadline today, or a rowless task dated today). Sections order by
 * sort_order_today then start_time (nulls first). Overdue (deadline < today)
 * and Earlier (when_date < today, never someday, regardless of schedule
 * rows) follow, and Earlier shows the first OPEN past entry.
 */
export function useLocalToday(eveningStartsAt = '18:00'): TodayView | undefined {
  // Owner-zone today: re-runs at owner midnight so Today rolls over
  const today = useOwnerToday()
  return useLiveQuery(async () => {
    const { openTasks, rowsByTask } = await loadOpenTasksWithSchedules()

    // Membership (view_predicates.go todaySectionsCond): deadline today, an
    // open schedule entry for today, or a rowless task dated today (the E3
    // synthetic entry). A dated task whose rows all exist and are completed
    // matches nothing — the documented no-list exception.
    const isTodayMember = (t: LocalTask): boolean => {
      const rows = rowsByTask.get(t.id) ?? []
      return (
        t.deadline === today ||
        rows.some((s) => s.when_date === today && !s.completed) ||
        (t.when_date === today && rows.length === 0)
      )
    }

    // One row per OPEN today entry (the LEFT JOIN): an entry at or after
    // eveningStartsAt joins This Evening, a timeless or earlier one joins
    // Today. A member with no open today entries at all keeps one timeless
    // row (the NULL join row).
    const dayRows: TodayRow[] = []
    const eveningRows: TodayRow[] = []
    for (const t of openTasks) {
      if (!isTodayMember(t)) continue
      const openToday = (rowsByTask.get(t.id) ?? []).filter(
        (s) => s.when_date === today && !s.completed,
      )
      for (const entry of openToday) {
        const row: TodayRow = { task: t, entry, startTime: entry.start_time ?? null }
        if (entry.start_time !== null && entry.start_time >= eveningStartsAt) {
          eveningRows.push(row)
        } else {
          dayRows.push(row)
        }
      }
      if (openToday.length === 0) {
        dayRows.push({ task: t, entry: null, startTime: null })
      }
    }

    // ORDER BY sort_order_today ASC, start_time ASC (nulls first)
    const sortedTodayRows = sortTodayRows(dayRows)
    const sortedEveningRows = sortTodayRows(eveningRows)

    const overdueTasks = collectOverdueTasks(openTasks, today)
    const earlierTasks = collectEarlierTasks(openTasks, today)

    // Completed today (owner-zone calendar day of the terminal timestamp)
    const completedTasks = await localDb.tasks
      .filter(
        (t) =>
          !t.deleted_at &&
          (t.status === 'completed' || t.status === 'canceled' || t.status === 'wont_do') &&
          ownerDateOf(t.completed_at ?? t.canceled_at ?? t.updated_at ?? '') === today,
      )
      .toArray()
    completedTasks.sort(byTerminalTsDesc)

    return {
      sections: [
        { title: 'Today', groups: await todayRowsToGroups(sortedTodayRows, today) },
        { title: 'This Evening', groups: await todayRowsToGroups(sortedEveningRows, today) },
      ],
      overdue: await Promise.all(
        overdueTasks.map((t) => enrichTask(t, today, { kind: 'first-any' })),
      ),
      earlier: await Promise.all(
        earlierTasks.map((t) => enrichTask(t, today, { kind: 'first-open-past' })),
      ),
      completed: await enrichTasks(completedTasks, today),
    } satisfies TodayView
  }, [eveningStartsAt, today])
}

/**
 * Upcoming: mirrors the server rules in internal/repository/view_predicates.go.
 * Date groups start TOMORROW (the server's default from) — a today-task lives
 * in the Today view. A task appears once per uncompleted schedule entry dated
 * on or after tomorrow (never someday); a task with a when_date and NO
 * schedule rows counts as one uncompleted schedule on that when_date (the
 * synthetic entry of E3). Overdue (deadline < today) and Earlier (when_date
 * < today, not someday, regardless of schedule rows) are both relative to
 * today, never to tomorrow.
 */
export function useLocalUpcoming(): UpcomingView | undefined {
  // Owner-zone today: re-runs at owner midnight so the date groups roll over
  const today = useOwnerToday()
  return useLiveQuery(async () => {
    // E3: the date groups start tomorrow (the server's default from);
    // Overdue and Earlier stay relative to today, never to from.
    const from = addDays(today, 1)

    const { openTasks, rowsByTask } = await loadOpenTasksWithSchedules()

    // One row per OPEN entry dated on or after from, never someday — the
    // LEFT JOIN. A task with a when_date and NO schedule rows at all
    // appears once under its when_date (the E3 synthetic entry); a task
    // whose rows all exist and are completed matches nothing here.
    const rows: Array<{
      task: LocalTask
      entry: LocalSchedule | null
      date: string
      startTime: string | null
    }> = []
    for (const t of openTasks) {
      const all = rowsByTask.get(t.id) ?? []
      const future = all.filter(
        (s) => s.when_date >= from && s.when_date !== 'someday' && !s.completed,
      )
      for (const entry of future) {
        rows.push({ task: t, entry, date: entry.when_date, startTime: entry.start_time ?? null })
      }
      if (
        future.length === 0 &&
        all.length === 0 &&
        t.when_date !== null &&
        t.when_date !== 'someday' &&
        t.when_date >= from
      ) {
        rows.push({ task: t, entry: null, date: t.when_date, startTime: null })
      }
    }

    // ORDER BY schedule_date ASC, start_time ASC (nulls first), sort_order_today ASC
    rows.sort(
      (a, b) =>
        a.date.localeCompare(b.date) ||
        timeNullsFirst(a.startTime, b.startTime) ||
        (a.task.sort_order_today ?? 0) - (b.task.sort_order_today ?? 0),
    )

    // Group under the entry's date (or the task's when_date for the
    // synthetic row) in encounter order, which is chronological.
    const dateMap = new Map<string, Task[]>()
    const dateOrder: string[] = []
    for (const row of rows) {
      if (!dateMap.has(row.date)) {
        dateMap.set(row.date, [])
        dateOrder.push(row.date)
      }
      dateMap
        .get(row.date)!
        .push(await enrichTask(row.task, today, { kind: 'row-entry', entry: row.entry }))
    }

    const overdueTasks = collectOverdueTasks(openTasks, today)
    const earlierTasks = collectEarlierTasks(openTasks, today)

    return {
      overdue: await Promise.all(
        overdueTasks.map((t) => enrichTask(t, today, { kind: 'first-any' })),
      ),
      dates: dateOrder.map((date) => ({ date, tasks: dateMap.get(date) ?? [] })),
      earlier: await Promise.all(
        earlierTasks.map((t) => enrichTask(t, today, { kind: 'first-open-past' })),
      ),
    } satisfies UpcomingView
  }, [today])
}

/**
 * Anytime: open tasks with no when_date owned by a project or an area
 * (a deadline-only loose task is Inbox, never Anytime), not deleted.
 * Returns an AnytimeView grouped by area → project.
 */
export function useLocalAnytime(): AnytimeView | undefined {
  // Owner-zone today: schedule flags re-derive at owner midnight
  const today = useOwnerToday()
  return useLiveQuery(async () => {
    const tasks = await localDb.tasks
      .where('status')
      .equals('open')
      .filter(
        (t) =>
          !t.when_date &&
          !t.deleted_at &&
          (!!t.project_id || !!t.area_id),
      )
      .toArray()

    return await buildAnytimeShape(tasks, today) as AnytimeView
  }, [today])
}

/**
 * Someday: tasks where when_date === 'someday', not deleted.
 * Returns a SomedayView grouped by area → project (same shape as AnytimeView).
 */
export function useLocalSomeday(): SomedayView | undefined {
  // Owner-zone today: schedule flags re-derive at owner midnight
  const today = useOwnerToday()
  return useLiveQuery(async () => {
    const tasks = await localDb.tasks
      .where('when_date')
      .equals('someday')
      .filter((t) => t.status === 'open' && !t.deleted_at)
      .toArray()

    return await buildAnytimeShape(tasks, today) as unknown as SomedayView
  }, [today])
}

/**
 * Shared builder for the Anytime / Someday view shape. Mirrors the server's
 * buildAnytimeView (views.go): areas in sort_order, each listing its open
 * projects in sort_order and then its standalone tasks; no-area projects
 * follow, then no-area standalone tasks. A task lists only where the server
 * would place it — under its project (which must be open and whose area, if
 * any, must exist) or, without a project, under its own existing area. Tasks
 * of closed or missing projects and of missing areas appear in no group;
 * only Someday keeps loose tasks (the anytime predicate requires a project
 * or an area, so its loose bucket is always empty). Within a group tasks
 * order by sort_order_today, and empty groups are omitted.
 */
async function buildAnytimeShape(
  rawTasks: LocalTask[],
  today: string,
): Promise<AnytimeView> {
  rawTasks.sort(bySortOrder('sort_order_today'))

  const areas = await localDb.areas.orderBy('sort_order').toArray()
  const areaExists = new Set(areas.map((a) => a.id))
  const openProjects = (await localDb.projects.where('status').equals('open').toArray())
    .sort((a, b) => (a.sort_order ?? 0) - (b.sort_order ?? 0))
  const projectById = new Map(openProjects.map((p) => [p.id, p]))

  const byProject = new Map<string, LocalTask[]>()
  const standaloneByArea = new Map<string, LocalTask[]>()
  const loose: LocalTask[] = []
  for (const t of rawTasks) {
    if (t.project_id) {
      const project = projectById.get(t.project_id)
      // A closed or missing project lists nothing; a project whose area_id
      // dangles sits in no area and not under no-area either.
      if (!project || (project.area_id && !areaExists.has(project.area_id))) continue
      const bucket = byProject.get(project.id)
      if (bucket) bucket.push(t)
      else byProject.set(project.id, [t])
    } else if (t.area_id) {
      if (!areaExists.has(t.area_id)) continue
      const bucket = standaloneByArea.get(t.area_id)
      if (bucket) bucket.push(t)
      else standaloneByArea.set(t.area_id, [t])
    } else {
      loose.push(t)
    }
  }

  const projectGroup = async (id: string): Promise<AnytimeViewProjectGroup> => {
    const project = projectById.get(id)!
    return {
      project: { id: project.id, title: project.title },
      tasks: await enrichTasks(byProject.get(id) ?? [], today),
    }
  }

  const view: AnytimeView = { areas: [], no_area: { projects: [], standalone_tasks: [] } }
  for (const area of areas) {
    const projects: AnytimeViewProjectGroup[] = []
    for (const p of openProjects.filter((p) => p.area_id === area.id)) {
      if ((byProject.get(p.id) ?? []).length === 0) continue
      projects.push(await projectGroup(p.id))
    }
    const standaloneTasks = await enrichTasks(standaloneByArea.get(area.id) ?? [], today)
    if (projects.length === 0 && standaloneTasks.length === 0) continue
    view.areas.push({
      area: { id: area.id, title: area.title },
      projects,
      standalone_tasks: standaloneTasks,
    })
  }
  for (const p of openProjects.filter((p) => !p.area_id)) {
    if ((byProject.get(p.id) ?? []).length === 0) continue
    view.no_area.projects.push(await projectGroup(p.id))
  }
  view.no_area.standalone_tasks = await enrichTasks(loose, today)
  return view
}

/**
 * Logbook: completed or canceled tasks, sorted by completed_at / canceled_at desc.
 * Returns a LogbookView grouped by date.
 */
export function useLocalLogbook(): LogbookView | undefined {
  // Owner-zone today: schedule flags re-derive at owner midnight
  const today = useOwnerToday()
  return useLiveQuery(async () => {
    const allDone = await localDb.tasks
      .filter(
        (t) =>
          !t.deleted_at &&
          (t.status === 'completed' || t.status === 'canceled' || t.status === 'wont_do'),
      )
      .toArray()

    // Sort by the most recent terminal timestamp desc
    allDone.sort((a, b) => {
      const aTs = a.completed_at ?? a.canceled_at ?? a.updated_at ?? ''
      const bTs = b.completed_at ?? b.canceled_at ?? b.updated_at ?? ''
      return bTs.localeCompare(aTs)
    })

    // Group by owner-zone calendar date of completion
    const enriched = await enrichTasks(allDone, today)
    const dateMap = new Map<string, Task[]>()
    for (const t of enriched) {
      const ts = t.completed_at ?? t.canceled_at ?? t.updated_at ?? ''
      const dateKey = ts ? ownerDateOf(ts) : 'unknown'
      if (!dateMap.has(dateKey)) dateMap.set(dateKey, [])
      dateMap.get(dateKey)!.push(t)
    }

    const groups = [...dateMap.entries()].map(([date, groupTasks]) => ({ date, tasks: groupTasks }))

    return { groups, total: allDone.length } satisfies LogbookView
  }, [today])
}

/**
 * Trash: tasks with deleted_at set, sorted by deleted_at desc.
 * Returns a TrashView (same shape as LogbookView).
 */
export function useLocalTrash(): TrashView | undefined {
  // Owner-zone today: schedule flags re-derive at owner midnight
  const today = useOwnerToday()
  return useLiveQuery(async () => {
    const deleted = await localDb.tasks
      .filter((t) => !!t.deleted_at)
      .toArray()

    deleted.sort((a, b) => (b.deleted_at ?? '').localeCompare(a.deleted_at ?? ''))

    const enriched = await enrichTasks(deleted, today)
    const dateMap = new Map<string, Task[]>()
    for (const t of enriched) {
      const dateKey = ownerDateOf(t.deleted_at ?? '')
      if (!dateMap.has(dateKey)) dateMap.set(dateKey, [])
      dateMap.get(dateKey)!.push(t)
    }

    const groups = [...dateMap.entries()].map(([date, groupTasks]) => ({ date, tasks: groupTasks }))

    return { groups, total: deleted.length } satisfies LogbookView
  }, [today])
}

// ---------------------------------------------------------------------------
// Entity Hooks
// ---------------------------------------------------------------------------

/**
 * Single task with all related data (checklist, attachments, schedules,
 * reminders, repeat rule). Returns a TaskDetail shape.
 */
export function useLocalTask(id: string): TaskDetail | undefined {
  return useLiveQuery(async () => {
    if (!id) return undefined

    const task = await localDb.tasks.get(id)
    if (!task) return undefined

    const [checklist, attachments, schedules, reminders, repeatRuleArr] = await Promise.all([
      localDb.checklistItems.where('task_id').equals(id).sortBy('sort_order'),
      localDb.attachments.where('task_id').equals(id).sortBy('sort_order'),
      localDb.schedules.where('task_id').equals(id).sortBy('sort_order'),
      localDb.reminders.where('task_id').equals(id).toArray(),
      localDb.repeatRules.where('task_id').equals(id).toArray(),
    ])

    const repeatRule = repeatRuleArr.length > 0 ? repeatRuleArr[0] : null

    // Resolve project / area / heading names for the detail view
    const [project, area, heading] = await Promise.all([
      task.project_id ? localDb.projects.get(task.project_id) : Promise.resolve(undefined),
      task.area_id ? localDb.areas.get(task.area_id) : Promise.resolve(undefined),
      task.heading_id ? localDb.headings.get(task.heading_id) : Promise.resolve(undefined),
    ])

    // If task has when_date but no schedule entries locally, synthesize one
    // so the ScheduleEditor has something to display.
    let resolvedSchedules = schedules.map((s: LocalSchedule) => stripSyncMeta(s))
    if (resolvedSchedules.length === 0 && task.when_date) {
      resolvedSchedules = [{
        id: `synth-${id}`,
        task_id: id,
        when_date: task.when_date,
        start_time: null,
        end_time: null,
        completed: false,
        sort_order: 0,
        created_at: task.created_at,
      }]
    }

    return {
      ...taskToPlain(task),
      project: project ? { id: project.id, title: project.title } : null,
      area: area ? { id: area.id, title: area.title } : null,
      heading: heading ? { id: heading.id, title: heading.title } : null,
      checklist: checklist.map((c: LocalChecklistItem) => stripSyncMeta(c)),
      attachments: attachments.map((a: LocalAttachment) => stripSyncMeta(a)),
      schedules: resolvedSchedules,
      reminders: reminders.map((r: LocalReminder) => stripSyncMeta(r)),
      repeat_rule: repeatRule
        ? (stripSyncMeta(repeatRule as LocalRepeatRule) as ReturnType<typeof stripSyncMeta>)
        : null,
    } as TaskDetail
  }, [id])
}

/**
 * All non-deleted (open) projects sorted by sort_order.
 */
export function useLocalProjects(): Project[] | undefined {
  return useLiveQuery(async () => {
    const projects = await localDb.projects
      .where('status')
      .equals('open')
      .sortBy('sort_order')

    // Compute task counts live from tasks table
    const result: Project[] = []
    for (const p of projects) {
      const tasks = await localDb.tasks
        .where('project_id')
        .equals(p.id)
        .filter((t: LocalTask) => !t.deleted_at)
        .toArray()
      const taskCount = tasks.length
      const completedCount = tasks.filter(
        (t: LocalTask) => t.status === 'completed' || t.status === 'canceled' || t.status === 'wont_do',
      ).length
      result.push({
        ...projectToPlain(p),
        task_count: taskCount,
        completed_task_count: completedCount,
      })
    }
    return result
  })
}

/**
 * All areas sorted by sort_order.
 */
export function useLocalAreas(): Area[] | undefined {
  return useLiveQuery(async () => {
    const areas = await localDb.areas.orderBy('sort_order').toArray()

    const result: Area[] = []
    for (const a of areas) {
      // standalone_task_count: open tasks in this area with no project
      const count = await localDb.tasks
        .where('area_id')
        .equals(a.id)
        .filter((t: LocalTask) => t.status === 'open' && !t.deleted_at && !t.project_id)
        .count()
      result.push({
        ...areaToPlain(a),
        standalone_task_count: count,
      })
    }
    return result
  })
}

/**
 * All tags sorted by sort_order.
 */
export function useLocalTags(): Tag[] | undefined {
  return useLiveQuery(async () => {
    const tags = await localDb.tags.orderBy('sort_order').toArray()

    const result: Tag[] = []
    for (const tag of tags) {
      // Count open, non-deleted tasks that reference this tag
      const count = await localDb.tasks
        .where('status')
        .equals('open')
        .filter((t: LocalTask) => !t.deleted_at && t.tags?.some((tr) => tr.id === tag.id))
        .count()
      result.push({
        ...tagToPlain(tag),
        task_count: count,
      })
    }
    return result
  })
}

// ---------------------------------------------------------------------------
// Aggregate Hook
// ---------------------------------------------------------------------------

/**
 * View counts for sidebar badges — mirrors ViewCounts from the server.
 */
export function useLocalViewCounts(reviewAfterDays?: number | null, reviewIncludeRecurring = true): ViewCounts | undefined {
  // Owner-zone today: re-runs at owner midnight so the badges roll over
  const today = useOwnerToday()
  return useLiveQuery(async () => {
    // Read the whole task table once so the badge query reruns on any task
    // mutation, including changes only visible through JS filters. The
    // schedule rows join the same way for the today badge; projects and
    // areas feed the Anytime/Someday ownership check.
    const [tasks, schedules, projects, areas] = await Promise.all([
      localDb.tasks.toArray(),
      localDb.schedules.toArray(),
      localDb.projects.toArray(),
      localDb.areas.toArray(),
    ])
    const openTasks = tasks.filter((t) => t.status === 'open' && !t.deleted_at)
    const inboxTasks = openTasks.filter((t) => !t.project_id && !t.area_id && !t.when_date)

    const inboxCount = inboxTasks.length
    const logbookCount = tasks.filter(
      (t) =>
        !t.deleted_at &&
        (t.status === 'completed' || t.status === 'canceled' || t.status === 'wont_do'),
    ).length
    const trashCount = tasks.filter((t) => !!t.deleted_at).length

    // Today mirrors the server's today view (view_predicates.go
    // listCondition "today"): the union of the Today/This Evening sections
    // (deadline today, an uncompleted schedule row today, or a rowless task
    // dated today), Overdue (deadline < today) and Earlier (when_date <
    // today, not someday, regardless of schedule rows). The documented
    // exception — dated today with every schedule row completed — appears
    // in no list and does not count.
    const rowsByTask = new Map<string, LocalSchedule[]>()
    for (const s of schedules) {
      const rows = rowsByTask.get(s.task_id)
      if (rows) rows.push(s)
      else rowsByTask.set(s.task_id, [s])
    }
    const inTodayView = (t: LocalTask): boolean => {
      const rows = rowsByTask.get(t.id) ?? []
      const todaySection =
        t.deadline === today ||
        rows.some((s) => s.when_date === today && !s.completed) ||
        (t.when_date === today && rows.length === 0)
      const overdue = !!t.deadline && t.deadline < today
      const earlier = !!t.when_date && t.when_date !== 'someday' && t.when_date < today
      return todaySection || overdue || earlier
    }
    const todayCount = openTasks.filter(inTodayView).length

    // Anytime/Someday mirror the views' ownership (anytimeOwnershipCond) so
    // counts equal the views: a task of an open project whose area exists
    // (or that has none), or a standalone task of an existing area. Tasks of
    // closed or missing projects and of missing areas appear in no group and
    // do not count; loose tasks count only for someday.
    const openProjectsById = new Map(
      projects.filter((p) => p.status === 'open').map((p) => [p.id, p] as const),
    )
    const areaIds = new Set(areas.map((a) => a.id))
    const ownedByOpenProjectOrArea = (t: LocalTask): boolean => {
      if (t.project_id) {
        const p = openProjectsById.get(t.project_id)
        return !!p && (!p.area_id || areaIds.has(p.area_id))
      }
      return !!t.area_id && areaIds.has(t.area_id)
    }
    const anytimeCount = openTasks.filter(
      (t) => !t.when_date && ownedByOpenProjectOrArea(t),
    ).length
    const somedayCount = openTasks.filter(
      (t) =>
        t.when_date === 'someday' &&
        (ownedByOpenProjectOrArea(t) || (!t.project_id && !t.area_id)),
    ).length

    // Overdue: open tasks with deadline < today, not deleted
    const overdueCount = openTasks.filter((t) => !!t.deadline && t.deadline < today).length

    // Review: open tasks not updated in reviewAfterDays, excluding inbox tasks
    let reviewCount = 0
    if (reviewAfterDays && reviewAfterDays > 0) {
      const cutoffStr = addDays(today, -reviewAfterDays)
      const inboxIds = new Set(inboxTasks.map((t) => t.id))

      reviewCount = openTasks.filter(
        (t) =>
          !inboxIds.has(t.id) &&
          (t.updated_at ?? '').slice(0, 10) < cutoffStr &&
          (reviewIncludeRecurring || !t.has_repeat_rule),
      ).length
    }

    return {
      inbox: inboxCount,
      today: todayCount,
      overdue: overdueCount,
      review: reviewCount,
      anytime: anytimeCount,
      someday: somedayCount,
      logbook: logbookCount,
      trash: trashCount,
    } satisfies ViewCounts
  }, [reviewAfterDays, reviewIncludeRecurring, today])
}

// ---------------------------------------------------------------------------
// Project / Area / Tag Task Hooks
// ---------------------------------------------------------------------------

/**
 * Open tasks for a specific project, ordered by sort_order_project like the
 * server's getTaskListItems / getTaskListItemsNoHeading.
 */
export function useLocalProjectTasks(projectId: string): Task[] | undefined {
  // Owner-zone today: schedule flags re-derive at owner midnight
  const today = useOwnerToday()
  return useLiveQuery(async () => {
    if (!projectId) return []

    const tasks = await localDb.tasks
      .where('project_id')
      .equals(projectId)
      .filter((t) => t.status === 'open' && !t.deleted_at)
      .toArray()

    tasks.sort(bySortOrder('sort_order_project'))
    return enrichTasks(tasks, today)
  }, [projectId, today])
}

/**
 * Open standalone tasks for a specific area, ordered by sort_order_today
 * like the server's AreaRepository.GetByID.
 */
export function useLocalAreaTasks(areaId: string): Task[] | undefined {
  // Owner-zone today: schedule flags re-derive at owner midnight
  const today = useOwnerToday()
  return useLiveQuery(async () => {
    if (!areaId) return []

    const tasks = await localDb.tasks
      .where('area_id')
      .equals(areaId)
      .filter((t) => t.status === 'open' && !t.deleted_at && !t.project_id)
      .toArray()

    tasks.sort(bySortOrder('sort_order_today'))
    return enrichTasks(tasks, today)
  }, [areaId, today])
}

/**
 * Open tasks for a specific tag, ordered by sort_order_today like the
 * server's GetTasksByTag.
 * Tags are stored as a TagRef[] array on each Task — there is no separate
 * task_tags join table in the local schema. We scan all open tasks and
 * filter by tag id match.
 */
export function useLocalTagTasks(tagId: string): Task[] | undefined {
  // Owner-zone today: schedule flags re-derive at owner midnight
  const today = useOwnerToday()
  return useLiveQuery(async () => {
    if (!tagId) return []

    const tasks = await localDb.tasks
      .where('status')
      .equals('open')
      .filter(
        (t) =>
          !t.deleted_at &&
          Array.isArray(t.tags) &&
          t.tags.some((tag) => tag.id === tagId),
      )
      .toArray()

    tasks.sort(bySortOrder('sort_order_today'))
    return enrichTasks(tasks, today)
  }, [tagId, today])
}

// ---------------------------------------------------------------------------
// Detail Hooks (Project / Area with full sub-resources)
// ---------------------------------------------------------------------------

/**
 * Full project detail with headings, tasks without heading, and completed tasks.
 * Mirrors the ProjectDetail shape from the server API.
 */
export function useLocalProjectDetail(projectId: string): ProjectDetail | undefined {
  // Owner-zone today: schedule flags re-derive at owner midnight
  const today = useOwnerToday()
  return useLiveQuery(async () => {
    if (!projectId) return undefined

    const project = await localDb.projects.get(projectId)
    if (!project) return undefined

    const [allTasks, headings] = await Promise.all([
      localDb.tasks
        .where('project_id')
        .equals(projectId)
        .filter((t) => !t.deleted_at)
        .toArray(),
      localDb.headings
        .where('project_id')
        .equals(projectId)
        .sortBy('sort_order'),
    ])

    const openTasks = allTasks
      .filter((t: LocalTask) => t.status === 'open')
      .sort(bySortOrder('sort_order_project'))
    const completedTasks = allTasks
      .filter(
        (t: LocalTask) => t.status === 'completed' || t.status === 'canceled' || t.status === 'wont_do',
      )
      .sort(byTerminalTsDesc)

    // Split open tasks by heading
    const tasksWithoutHeading = await enrichTasks(
      openTasks.filter((t: LocalTask) => !t.heading_id),
      today,
    )

    const headingsWithTasks: HeadingWithTasks[] = []
    for (const h of headings) {
      const hTasks = openTasks.filter((t: LocalTask) => t.heading_id === h.id)
      headingsWithTasks.push({
        ...stripSyncMeta(h),
        tasks: await enrichTasks(hTasks, today),
      })
    }

    // Resolve area for the project
    const area = project.area_id
      ? await localDb.areas.get(project.area_id)
      : undefined

    return {
      ...projectToPlain(project),
      area: area ? { id: area.id, title: area.title } : { id: project.area_id, title: project.area_id },
      task_count: openTasks.length + completedTasks.length,
      completed_task_count: completedTasks.length,
      headings: headingsWithTasks,
      tasks_without_heading: tasksWithoutHeading,
      completed_tasks: await enrichTasks(completedTasks, today),
    } as ProjectDetail
  }, [projectId, today])
}

/**
 * Full area detail with projects, tasks, and completed tasks.
 * Mirrors the AreaDetail shape from the server API.
 */
export function useLocalAreaDetail(areaId: string): AreaDetail | undefined {
  // Owner-zone today: schedule flags re-derive at owner midnight
  const today = useOwnerToday()
  return useLiveQuery(async () => {
    if (!areaId) return undefined

    const area = await localDb.areas.get(areaId)
    if (!area) return undefined

    const [projects, allTasks] = await Promise.all([
      localDb.projects
        .where('area_id')
        .equals(areaId)
        .filter((p: LocalProject) => p.status === 'open')
        .sortBy('sort_order'),
      localDb.tasks
        .where('area_id')
        .equals(areaId)
        .filter((t: LocalTask) => !t.deleted_at && !t.project_id)
        .toArray(),
    ])

    const openTasks = allTasks
      .filter((t: LocalTask) => t.status === 'open')
      .sort(bySortOrder('sort_order_today'))
    const completedTasks = allTasks.filter(
      (t: LocalTask) => t.status === 'completed' || t.status === 'canceled' || t.status === 'wont_do',
    )

    // Get task counts per project
    const projectsWithCounts = await Promise.all(
      projects.map(async (p: LocalProject) => {
        const pTasks = await localDb.tasks
          .where('project_id')
          .equals(p.id)
          .filter((t: LocalTask) => !t.deleted_at)
          .toArray()
        const openCount = pTasks.filter((t: LocalTask) => t.status === 'open').length
        const completedCount = pTasks.length - openCount
        return {
          ...projectToPlain(p),
          task_count: pTasks.length,
          completed_task_count: completedCount,
        }
      }),
    )

    return {
      ...areaToPlain(area),
      project_count: projects.length,
      task_count: openTasks.length + completedTasks.length,
      standalone_task_count: openTasks.length,
      projects: projectsWithCounts,
      tasks: await enrichTasks(openTasks, today),
      completed_tasks: await enrichTasks(completedTasks, today),
    } as AreaDetail
  }, [areaId, today])
}

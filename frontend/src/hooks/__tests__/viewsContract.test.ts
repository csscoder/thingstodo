import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { renderHook, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import {
  useLocalAnytime,
  useLocalAreaDetail,
  useLocalInbox,
  useLocalLogbook,
  useLocalProjectDetail,
  useLocalSomeday,
  useLocalTagTasks,
  useLocalToday,
  useLocalTrash,
  useLocalUpcoming,
  useLocalViewCounts,
} from '../localQueries'
import { localDb } from '../../db'
import { setOwnerTimeZone } from '../../lib/owner-time'
import type {
  LocalArea,
  LocalHeading,
  LocalProject,
  LocalSchedule,
  LocalTag,
  LocalTask,
} from '../../db/schema'
import type {
  AnytimeView,
  AreaDetail,
  LogbookView,
  ProjectDetail,
  ProjectStatus,
  Task,
  TodayView,
  UpcomingView,
} from '../../api/types'

// The shared view contract fixtures (testdata/views) are the one source of
// view truth: the Go contract test runs them against the repositories, this
// Vitest twin runs the same fixtures against the local Dexie hooks. Rows
// compare in order, exactly like views_contract_test.go.

const FIXTURE_NAMES = ['basic', 'midnight-moscow', 'multi-schedule', 'projects-and-tags']
const FIXTURES_DIR = resolve(dirname(fileURLToPath(import.meta.url)), '../../../../testdata/views')
const SEED_TS = '2026-10-01 00:00:00'

/** Fixture row grammar: task id plus, depending on the view, a section
 *  (today/evening/overdue/earlier), a date group, and the schedule entry
 *  the row displays (absent key === null entry). */
interface FixtureRow {
  task: string
  section?: string
  date?: string
  entry?: string | null
}

interface FixtureTask {
  id: string
  title: string
  status?: string
  when_date?: string | null
  deadline?: string | null
  project_id?: string | null
  area_id?: string | null
  heading_id?: string | null
  sort_order_today?: number
  sort_order_project?: number
  completed_at?: string | null
  canceled_at?: string | null
  deleted_at?: string | null
  tag_ids?: string[]
}

interface FixtureCounts {
  inbox: number
  today: number
  overdue: number
  anytime: number
  someday: number
  logbook: number
  trash: number
}

interface Fixture {
  name: string
  now: string
  timezone: string
  evening_starts_at: string
  seed: {
    areas: Array<{ id: string; title: string; sort_order?: number }>
    projects: Array<{ id: string; title: string; area_id?: string | null; status?: string; sort_order?: number }>
    headings: Array<{ id: string; title: string; project_id: string; sort_order?: number }>
    tags: Array<{ id: string; title: string; parent_tag_id?: string | null; sort_order?: number }>
    tasks: FixtureTask[]
    schedules: Array<{
      id: string
      task_id: string
      when_date: string
      start_time?: string | null
      end_time?: string | null
      completed?: boolean
      sort_order?: number
    }>
  }
  expect: {
    today?: FixtureRow[]
    upcoming?: FixtureRow[]
    inbox?: FixtureRow[]
    anytime?: FixtureRow[]
    someday?: FixtureRow[]
    logbook?: FixtureRow[]
    trash?: FixtureRow[]
    counts?: FixtureCounts
  } & Record<string, unknown>
}

async function loadFixture(name: string): Promise<Fixture> {
  return JSON.parse(readFileSync(resolve(FIXTURES_DIR, `${name}.json`), 'utf8')) as Fixture
}

async function clearLocalDb(): Promise<void> {
  await Promise.all([
    localDb.tasks.clear(),
    localDb.schedules.clear(),
    localDb.projects.clear(),
    localDb.areas.clear(),
    localDb.headings.clear(),
    localDb.tags.clear(),
  ])
}

/** Insert a fixture seed into the local Dexie tables. Fields the fixture
 *  omits take the same defaults the Go seeder gives the SQL rows (status
 *  'open', sort_order 0, NULL refs). */
async function seedLocalDb(seed: Fixture['seed']): Promise<void> {
  const syncMeta = { _syncStatus: 'synced' as const, _localUpdatedAt: SEED_TS }

  await localDb.areas.bulkPut(seed.areas.map((a): LocalArea => ({
    id: a.id,
    title: a.title,
    sort_order: a.sort_order ?? 0,
    project_count: 0,
    task_count: 0,
    standalone_task_count: 0,
    created_at: SEED_TS,
    updated_at: SEED_TS,
    ...syncMeta,
  })))

  await localDb.projects.bulkPut(seed.projects.map((p): LocalProject => ({
    id: p.id,
    title: p.title,
    notes: '',
    area_id: p.area_id ?? null,
    // The embedded area ref is unused by the local view hooks.
    area: { id: p.area_id ?? '', title: '' },
    status: (p.status ?? 'open') as ProjectStatus,
    when_date: null,
    deadline: null,
    sort_order: p.sort_order ?? 0,
    task_count: 0,
    completed_task_count: 0,
    tags: [],
    created_at: SEED_TS,
    updated_at: SEED_TS,
    ...syncMeta,
  })))

  await localDb.headings.bulkPut(seed.headings.map((h): LocalHeading => ({
    id: h.id,
    title: h.title,
    project_id: h.project_id,
    sort_order: h.sort_order ?? 0,
    ...syncMeta,
  })))

  await localDb.tags.bulkPut(seed.tags.map((g): LocalTag => ({
    id: g.id,
    title: g.title,
    color: null,
    parent_tag_id: g.parent_tag_id ?? null,
    sort_order: g.sort_order ?? 0,
    task_count: 0,
    ...syncMeta,
  })))

  const tagTitles = new Map(seed.tags.map((g) => [g.id, g.title]))
  await localDb.tasks.bulkPut(seed.tasks.map((t): LocalTask => ({
    id: t.id,
    title: t.title,
    notes: '',
    status: (t.status ?? 'open') as LocalTask['status'],
    when_date: t.when_date ?? null,
    high_priority: false,
    deadline: t.deadline ?? null,
    project_id: t.project_id ?? null,
    area_id: t.area_id ?? null,
    heading_id: t.heading_id ?? null,
    sort_order_today: t.sort_order_today ?? 0,
    sort_order_project: t.sort_order_project ?? 0,
    sort_order_heading: 0,
    completed_at: t.completed_at ?? null,
    canceled_at: t.canceled_at ?? null,
    deleted_at: t.deleted_at ?? null,
    created_at: SEED_TS,
    updated_at: SEED_TS,
    tags: (t.tag_ids ?? []).map((id) => ({ id, title: tagTitles.get(id) ?? id, color: null })),
    checklist_count: 0,
    checklist_done: 0,
    has_notes: false,
    has_links: false,
    has_files: false,
    has_repeat_rule: false,
    has_reminders: false,
    first_reminder_type: null,
    first_reminder_value: null,
    first_reminder_exact_at: null,
    project_name: null,
    area_name: null,
    first_schedule_time: null,
    first_schedule_end_time: null,
    schedule_entry_id: null,
    ...syncMeta,
  })))

  await localDb.schedules.bulkPut(seed.schedules.map((s): LocalSchedule => ({
    id: s.id,
    task_id: s.task_id,
    when_date: s.when_date,
    start_time: s.start_time ?? null,
    end_time: s.end_time ?? null,
    completed: s.completed ?? false,
    sort_order: s.sort_order ?? 0,
    created_at: SEED_TS,
    ...syncMeta,
  })))
}

/** Map a hook task to the fixture row grammar; the entry key appears only
 *  when the row displays one (null entry === absent key). */
function rowOf(t: Task, section?: string, date?: string): FixtureRow {
  const row: FixtureRow = { task: t.id }
  if (section !== undefined) row.section = section
  if (date !== undefined) row.date = date
  if (t.schedule_entry_id) row.entry = t.schedule_entry_id
  return row
}

/** Normalized row for ordered comparison: [task, section, date, entry]. */
function norm(r: FixtureRow): [string, string, string, string | null] {
  return [r.task, r.section ?? '', r.date ?? '', r.entry ?? null]
}

function todayRows(view: TodayView): FixtureRow[] {
  const rows: FixtureRow[] = []
  const pushAll = (tasks: Task[], section: string) => {
    for (const t of tasks) rows.push(rowOf(t, section))
  }
  for (const g of view.sections[0]?.groups ?? []) pushAll(g.tasks, 'today')
  for (const g of view.sections[1]?.groups ?? []) pushAll(g.tasks, 'evening')
  pushAll(view.overdue, 'overdue')
  pushAll(view.earlier, 'earlier')
  return rows
}

function upcomingRows(view: UpcomingView): FixtureRow[] {
  const rows: FixtureRow[] = view.overdue.map((t) => rowOf(t, 'overdue'))
  for (const dg of view.dates) {
    for (const t of dg.tasks) rows.push(rowOf(t, undefined, dg.date))
  }
  rows.push(...view.earlier.map((t) => rowOf(t, 'earlier')))
  return rows
}

/** Flatten Anytime/Someday in display order, like the Go runner's
 *  vcFlattenAnytime: each area's projects then standalone tasks, then the
 *  no-area projects and no-area standalone tasks. */
function flattenAnytime(view: AnytimeView): FixtureRow[] {
  const rows: FixtureRow[] = []
  for (const a of view.areas) {
    for (const p of a.projects) rows.push(...p.tasks.map((t) => rowOf(t)))
    rows.push(...a.standalone_tasks.map((t) => rowOf(t)))
  }
  for (const p of view.no_area.projects) rows.push(...p.tasks.map((t) => rowOf(t)))
  rows.push(...view.no_area.standalone_tasks.map((t) => rowOf(t)))
  return rows
}

/** Date-grouped listings (logbook, trash): like the Go vcGroupRows. */
function groupRows(view: LogbookView): FixtureRow[] {
  const rows: FixtureRow[] = []
  for (const g of view.groups) {
    for (const t of g.tasks) rows.push(rowOf(t, undefined, g.date))
  }
  return rows
}

/** Project detail in the Go runner's flatten order: headings' tasks, then
 *  tasks without heading, then completed tasks. */
function projectDetailRows(view: ProjectDetail): FixtureRow[] {
  const rows: FixtureRow[] = []
  for (const h of view.headings) rows.push(...h.tasks.map((t) => rowOf(t)))
  rows.push(...view.tasks_without_heading.map((t) => rowOf(t)))
  rows.push(...view.completed_tasks.map((t) => rowOf(t)))
  return rows
}

/** Area detail: standalone tasks, then completed standalone tasks. */
function areaDetailRows(view: AreaDetail): FixtureRow[] {
  return [...view.tasks, ...view.completed_tasks].map((t) => rowOf(t))
}

function expectRows(key: string, name: string, want: FixtureRow[], got: FixtureRow[]): void {
  expect(
    got.map(norm),
    `${name} ${key}: want ${JSON.stringify(want)} got ${JSON.stringify(got)}`,
  ).toEqual(want.map(norm))
}

beforeEach(async () => {
  await clearLocalDb()
})

afterEach(async () => {
  await clearLocalDb()
  vi.useRealTimers()
  // Reset the owner zone so later files keep agreeing with the clock.
  setOwnerTimeZone(Intl.DateTimeFormat().resolvedOptions().timeZone)
  localStorage.removeItem('thingstodo_timezone')
})

test.each(FIXTURE_NAMES)('%s: local views match the shared fixtures', async (name) => {
  const fx = await loadFixture(name)

  vi.useFakeTimers({ shouldAdvanceTime: true })
  vi.setSystemTime(new Date(fx.now))
  setOwnerTimeZone(fx.timezone)
  await seedLocalDb(fx.seed)

  const today = renderHook(() => useLocalToday(fx.evening_starts_at))
  const upcoming = renderHook(() => useLocalUpcoming())

  await waitFor(() => {
    expect(today.result.current).toBeDefined()
  })
  await waitFor(() => {
    expect(upcoming.result.current).toBeDefined()
  })

  expectRows('today', name, fx.expect.today ?? [], todayRows(today.result.current!))
  expectRows('upcoming', name, fx.expect.upcoming ?? [], upcomingRows(upcoming.result.current!))

  // Every remaining expect key maps to one local hook, exactly like the Go
  // runner maps it to one repository call. The hook mounts get slower as
  // more live queries pile up under fake timers, so the waits allow 5s.
  const WAIT: Parameters<typeof waitFor>[1] = { timeout: 5000 }
  for (const key of Object.keys(fx.expect).sort()) {
    if (key === 'today' || key === 'upcoming') continue

    if (key === 'counts') {
      const counts = renderHook(() => useLocalViewCounts())
      await waitFor(() => {
        expect(counts.result.current).toBeDefined()
      }, WAIT)
      const got = counts.result.current!
      expect(
        {
          inbox: got.inbox,
          today: got.today,
          overdue: got.overdue,
          anytime: got.anytime,
          someday: got.someday,
          logbook: got.logbook,
          trash: got.trash,
        },
        `${name} counts`,
      ).toEqual(fx.expect.counts)
      continue
    }

    const want = fx.expect[key] as FixtureRow[]
    let got: FixtureRow[]
    if (key === 'inbox') {
      const inbox = renderHook(() => useLocalInbox())
      await waitFor(() => {
        expect(inbox.result.current).toBeDefined()
      }, WAIT)
      got = inbox.result.current!.tasks.map((t) => rowOf(t))
    } else if (key === 'anytime' || key === 'someday') {
      const view = renderHook(() => (key === 'anytime' ? useLocalAnytime() : useLocalSomeday()))
      await waitFor(() => {
        expect(view.result.current).toBeDefined()
      }, WAIT)
      got = flattenAnytime(view.result.current!)
    } else if (key === 'logbook' || key === 'trash') {
      const view = renderHook(() => (key === 'logbook' ? useLocalLogbook() : useLocalTrash()))
      await waitFor(() => {
        expect(view.result.current).toBeDefined()
      }, WAIT)
      got = groupRows(view.result.current!)
    } else if (key.startsWith('project:')) {
      const detail = renderHook(() => useLocalProjectDetail(key.slice('project:'.length)))
      await waitFor(() => {
        expect(detail.result.current).toBeDefined()
      }, WAIT)
      got = projectDetailRows(detail.result.current!)
    } else if (key.startsWith('area:')) {
      const detail = renderHook(() => useLocalAreaDetail(key.slice('area:'.length)))
      await waitFor(() => {
        expect(detail.result.current).toBeDefined()
      }, WAIT)
      got = areaDetailRows(detail.result.current!)
    } else if (key.startsWith('tag:')) {
      const tasks = renderHook(() => useLocalTagTasks(key.slice('tag:'.length)))
      await waitFor(() => {
        expect(tasks.result.current).toBeDefined()
      }, WAIT)
      got = tasks.result.current!.map((t) => rowOf(t))
    } else {
      throw new Error(`${name}: unknown expect key ${key}`)
    }
    expectRows(key, name, want, got)
  }
})

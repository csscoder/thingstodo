import { renderHook, waitFor, act } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { useLocalUpcoming, useLocalViewCounts } from '../localQueries'
import { localDb } from '../../db'
import type { LocalSchedule, LocalTask } from '../../db/schema'

function formatLocalDate(date: Date): string {
  const year = date.getFullYear()
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  return `${year}-${month}-${day}`
}

function addDays(date: Date, days: number): Date {
  const d = new Date(date)
  d.setDate(d.getDate() + days)
  return d
}

function makeTask(overrides: Partial<LocalTask> = {}): LocalTask {
  const now = new Date().toISOString()
  return {
    id: 'task-1',
    title: 'Test task',
    status: 'open',
    notes: '',
    when_date: null,
    high_priority: false,
    deadline: null,
    project_id: 'project-1',
    area_id: null,
    heading_id: null,
    sort_order_today: 0,
    sort_order_project: 0,
    sort_order_heading: 0,
    completed_at: null,
    canceled_at: null,
    deleted_at: null,
    created_at: now,
    updated_at: now,
    tags: [],
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
    project_name: 'Project',
    area_name: null,
    first_schedule_time: null,
    first_schedule_end_time: null,
    first_schedule_completed: undefined,
    schedule_entry_id: null,
    past_schedule_count: undefined,
    has_actionable_schedules: undefined,
    all_today_schedules_completed: undefined,
    _syncStatus: 'synced',
    _localUpdatedAt: now,
    ...overrides,
  }
}

function makeSchedule(overrides: Partial<LocalSchedule> = {}): LocalSchedule {
  const now = new Date().toISOString()
  return {
    id: 'schedule-1',
    task_id: 'task-1',
    when_date: '2024-01-01',
    start_time: null,
    end_time: null,
    completed: false,
    sort_order: 0,
    created_at: now,
    _syncStatus: 'synced',
    _localUpdatedAt: now,
    ...overrides,
  }
}

describe('useLocalUpcoming', () => {
  beforeEach(async () => {
    await localDb.tasks.clear()
    await localDb.schedules.clear()
  })

  afterEach(async () => {
    await localDb.tasks.clear()
    await localDb.schedules.clear()
  })

  it('TestUpcomingLocal_DatesStartTomorrow', async () => {
    const today = formatLocalDate(new Date())
    const tomorrow = formatLocalDate(addDays(new Date(), 1))

    // Both tasks have an uncompleted schedule entry on their date; the
    // today-task belongs to the Today view, never to Upcoming dates.
    await localDb.tasks.put(makeTask({ id: 'today-task', when_date: today }))
    await localDb.tasks.put(makeTask({ id: 'tomorrow-task', when_date: tomorrow }))
    await localDb.schedules.put(makeSchedule({ id: 'sch-today', task_id: 'today-task', when_date: today }))
    await localDb.schedules.put(makeSchedule({ id: 'sch-tomorrow', task_id: 'tomorrow-task', when_date: tomorrow }))

    const { result } = renderHook(() => useLocalUpcoming())

    await waitFor(() => {
      expect(result.current).toBeDefined()
    })

    const dateStrings = result.current!.dates.map((d) => d.date)
    expect(dateStrings).not.toContain(today)
    expect(dateStrings).toContain(tomorrow)
    expect(
      result.current!.dates.find((d) => d.date === tomorrow)?.tasks.map((t) => t.id),
    ).toEqual(['tomorrow-task'])
  })

  it('TestUpcomingLocal_WhenDateWithoutScheduleAppears', async () => {
    const tomorrow = formatLocalDate(addDays(new Date(), 1))

    // A rowless task dated tomorrow appears once under tomorrow (E3).
    await localDb.tasks.put(makeTask({ id: 'rowless-task', when_date: tomorrow }))
    // A task whose only schedule row is completed has no synthetic entry
    // and no uncompleted entry, so it appears nowhere in dates.
    await localDb.tasks.put(makeTask({ id: 'completed-row-task', when_date: tomorrow }))
    await localDb.schedules.put(makeSchedule({
      id: 'sch-done',
      task_id: 'completed-row-task',
      when_date: tomorrow,
      completed: true,
    }))

    const { result } = renderHook(() => useLocalUpcoming())

    await waitFor(() => {
      expect(result.current).toBeDefined()
    })

    expect(result.current!.dates).toHaveLength(1)
    expect(result.current!.dates[0].date).toBe(tomorrow)
    expect(result.current!.dates[0].tasks.map((t) => t.id)).toEqual(['rowless-task'])
  })

  it('TestUpcomingLocal_EarlierIgnoresSchedules', async () => {
    const yesterday = formatLocalDate(addDays(new Date(), -1))

    // Rowless past task, all-completed past rows, and a passed deadline:
    // all three are Earlier regardless of schedules.
    await localDb.tasks.put(makeTask({ id: 'rowless-earlier', when_date: yesterday }))
    await localDb.tasks.put(makeTask({ id: 'all-completed-earlier', when_date: yesterday }))
    await localDb.schedules.put(makeSchedule({
      id: 'sch-past-done',
      task_id: 'all-completed-earlier',
      when_date: yesterday,
      completed: true,
    }))
    await localDb.tasks.put(makeTask({ id: 'overdue-earlier', when_date: yesterday, deadline: yesterday }))
    // Someday never counts as a past date.
    await localDb.tasks.put(makeTask({ id: 'someday-task', when_date: 'someday' }))

    const { result } = renderHook(() => useLocalUpcoming())

    await waitFor(() => {
      expect(result.current).toBeDefined()
    })

    const earlierIds = result.current!.earlier.map((t) => t.id)
    expect(earlierIds).toContain('rowless-earlier')
    expect(earlierIds).toContain('all-completed-earlier')
    expect(earlierIds).toContain('overdue-earlier')
    expect(earlierIds).not.toContain('someday-task')
    // The overdue task appears in both Overdue and Earlier.
    expect(result.current!.overdue.map((t) => t.id)).toContain('overdue-earlier')
    // Dates stay empty: nothing is scheduled from tomorrow on.
    expect(result.current!.dates).toEqual([])
  })
})

describe('useLocalViewCounts', () => {
  beforeEach(async () => {
    await localDb.tasks.clear()
    await localDb.schedules.clear()
  })

  afterEach(async () => {
    await localDb.tasks.clear()
    await localDb.schedules.clear()
  })

  it('reacts to review and overdue changes driven by non-indexed fields', async () => {
    const today = new Date()
    const yesterday = new Date(today)
    yesterday.setDate(today.getDate() - 1)
    const tenDaysAgo = new Date(today)
    tenDaysAgo.setDate(today.getDate() - 10)

    await localDb.tasks.put(makeTask({
      updated_at: tenDaysAgo.toISOString(),
      deadline: formatLocalDate(yesterday),
    }))

    const { result } = renderHook(() => useLocalViewCounts(7, true))

    await waitFor(() => {
      expect(result.current).toMatchObject({
        inbox: 0,
        today: 1,
        overdue: 1,
        review: 1,
        anytime: 1,
        someday: 0,
        logbook: 0,
        trash: 0,
      })
    })

    await act(async () => {
      await localDb.tasks.update('task-1', { updated_at: new Date().toISOString() })
    })

    await waitFor(() => {
      expect(result.current?.review).toBe(0)
      expect(result.current?.overdue).toBe(1)
      expect(result.current?.today).toBe(1)
    })

    await act(async () => {
      await localDb.tasks.update('task-1', {
        status: 'completed',
        completed_at: new Date().toISOString(),
      })
    })

    await waitFor(() => {
      expect(result.current).toMatchObject({
        today: 0,
        overdue: 0,
        review: 0,
        logbook: 1,
      })
    })
  })

  // Mirrors the server's today view (view_predicates.go listCondition
  // "today"): the union of the Today/This Evening sections, Overdue and
  // Earlier — each task counted once.
  it('TestTodayCount_MatchesServerRules', async () => {
    const today = formatLocalDate(new Date())
    const yesterday = formatLocalDate(addDays(new Date(), -1))
    const tomorrow = formatLocalDate(addDays(new Date(), 1))

    // Earlier: open, when_date < today, not someday — in the Today view.
    await localDb.tasks.put(makeTask({ id: 'earlier-task', when_date: yesterday }))
    // Multi-schedule: the primary when_date is tomorrow, but an
    // uncompleted schedule row lands today — Today section via the row.
    await localDb.tasks.put(makeTask({ id: 'multi-today', when_date: tomorrow }))
    await localDb.schedules.put(
      makeSchedule({ id: 'sch-multi-today', task_id: 'multi-today', when_date: today }),
    )
    await localDb.schedules.put(
      makeSchedule({ id: 'sch-multi-tomorrow', task_id: 'multi-today', when_date: tomorrow }),
    )
    // No-list exception: dated today, its only schedule row completed —
    // open, but the server puts it in no list, so the badge skips it.
    await localDb.tasks.put(makeTask({ id: 'no-list-exception', when_date: today }))
    await localDb.schedules.put(
      makeSchedule({ id: 'sch-done-today', task_id: 'no-list-exception', when_date: today, completed: true }),
    )
    // Overdue belongs to the Today union too.
    await localDb.tasks.put(makeTask({ id: 'overdue-task', deadline: yesterday }))

    const { result } = renderHook(() => useLocalViewCounts())

    await waitFor(() => {
      expect(result.current).toBeDefined()
    })

    expect(result.current!.today).toBe(3) // earlier + multi-schedule-today + overdue
    expect(result.current!.overdue).toBe(1)
  })
})

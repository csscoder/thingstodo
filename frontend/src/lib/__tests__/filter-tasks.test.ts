import { describe, expect, test } from 'vitest'
import { filterTasks, hasFilters } from '../filter-tasks'
import type { Task } from '../../api/types'
import type { FilterState } from '../../stores/filters'

function localDate(offsetDays = 0): string {
  const d = new Date()
  d.setDate(d.getDate() + offsetDays)
  const month = String(d.getMonth() + 1).padStart(2, '0')
  const day = String(d.getDate()).padStart(2, '0')
  return `${d.getFullYear()}-${month}-${day}`
}

function makeTask(overrides: Partial<Task> = {}): Task {
  return {
    id: 'task-1',
    title: 'Task',
    notes: '',
    status: 'open',
    when_date: null,
    high_priority: false,
    deadline: null,
    project_id: null,
    area_id: null,
    heading_id: null,
    sort_order_today: 0,
    sort_order_project: 0,
    sort_order_heading: 0,
    completed_at: null,
    canceled_at: null,
    deleted_at: null,
    created_at: '2026-01-01T00:00:00.000Z',
    updated_at: '2026-01-01T00:00:00.000Z',
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
    project_name: null,
    area_name: null,
    first_schedule_time: null,
    first_schedule_end_time: null,
    schedule_entry_id: null,
    ...overrides,
  }
}

const noFilters: FilterState = {
  areas: [],
  projects: [],
  tags: [],
  highPriority: false,
  plannedDate: null,
  deadline: null,
  search: '',
}

describe('filterTasks', () => {
  test('empty filter state returns every task', () => {
    const tasks = [makeTask({ id: 'a' }), makeTask({ id: 'b' })]
    expect(filterTasks(tasks, noFilters)).toEqual(tasks)
  })

  test('different filter kinds AND together', () => {
    const tasks = [
      makeTask({ id: 'match', area_id: 'area-1', tags: [{ id: 'tag-1', title: 'T1', color: null }], high_priority: true, when_date: localDate(0), deadline: localDate(1) }),
      makeTask({ id: 'wrong-area', area_id: 'area-2', tags: [{ id: 'tag-1', title: 'T1', color: null }], high_priority: true, when_date: localDate(0), deadline: localDate(1) }),
      makeTask({ id: 'wrong-tag', area_id: 'area-1', tags: [{ id: 'tag-2', title: 'T2', color: null }], high_priority: true, when_date: localDate(0), deadline: localDate(1) }),
      makeTask({ id: 'wrong-priority', area_id: 'area-1', tags: [{ id: 'tag-1', title: 'T1', color: null }], high_priority: false, when_date: localDate(0), deadline: localDate(1) }),
      makeTask({ id: 'wrong-when', area_id: 'area-1', tags: [{ id: 'tag-1', title: 'T1', color: null }], high_priority: true, when_date: localDate(5), deadline: localDate(1) }),
      makeTask({ id: 'wrong-deadline', area_id: 'area-1', tags: [{ id: 'tag-1', title: 'T1', color: null }], high_priority: true, when_date: localDate(0), deadline: localDate(9) }),
    ]

    const result = filterTasks(tasks, {
      ...noFilters,
      areas: ['area-1'],
      tags: ['tag-1'],
      highPriority: true,
      plannedDate: { type: 'preset', preset: 'today' },
      deadline: { type: 'range', start: localDate(0), end: localDate(2) },
    })

    expect(result.map((t) => t.id)).toEqual(['match'])
  })

  test('area filter is OR within the kind and excludes area-less tasks', () => {
    const tasks = [
      makeTask({ id: 'a1', area_id: 'area-1' }),
      makeTask({ id: 'a2', area_id: 'area-2' }),
      makeTask({ id: 'a3', area_id: 'area-3' }),
      makeTask({ id: 'loose', area_id: null }),
    ]
    const result = filterTasks(tasks, { ...noFilters, areas: ['area-1', 'area-2'] })
    expect(result.map((t) => t.id)).toEqual(['a1', 'a2'])
  })

  test('project filter excludes project-less tasks', () => {
    const tasks = [
      makeTask({ id: 'p1', project_id: 'proj-1' }),
      makeTask({ id: 'none', project_id: null }),
    ]
    const result = filterTasks(tasks, { ...noFilters, projects: ['proj-1'] })
    expect(result.map((t) => t.id)).toEqual(['p1'])
  })

  test('tag filter is OR within the kind: at least one selected tag matches', () => {
    const tasks = [
      makeTask({ id: 'one-of-two', tags: [{ id: 'tag-2', title: 'T2', color: null }, { id: 'tag-9', title: 'T9', color: null }] }),
      makeTask({ id: 'none-selected', tags: [{ id: 'tag-9', title: 'T9', color: null }] }),
      makeTask({ id: 'untagged', tags: [] }),
    ]
    const result = filterTasks(tasks, { ...noFilters, tags: ['tag-1', 'tag-2'] })
    expect(result.map((t) => t.id)).toEqual(['one-of-two'])
  })

  test('high priority filter keeps only high_priority tasks', () => {
    const tasks = [makeTask({ id: 'high', high_priority: true }), makeTask({ id: 'low' })]
    const result = filterTasks(tasks, { ...noFilters, highPriority: true })
    expect(result.map((t) => t.id)).toEqual(['high'])
  })

  test('search matches title and notes case-insensitively', () => {
    const tasks = [
      makeTask({ id: 'title', title: 'Buy GROCERIES' }),
      makeTask({ id: 'notes', notes: 'pick up groceries later' }),
      makeTask({ id: 'none', title: 'Mow lawn', notes: 'weekend' }),
    ]
    const result = filterTasks(tasks, { ...noFilters, search: 'GROCERIES' })
    expect(result.map((t) => t.id)).toEqual(['title', 'notes'])
  })

  test('plannedDate preset today matches only today on when_date', () => {
    const tasks = [
      makeTask({ id: 'today', when_date: localDate(0) }),
      makeTask({ id: 'tomorrow', when_date: localDate(1) }),
      makeTask({ id: 'undated', when_date: null }),
    ]
    const result = filterTasks(tasks, { ...noFilters, plannedDate: { type: 'preset', preset: 'today' } })
    expect(result.map((t) => t.id)).toEqual(['today'])
  })

  test('plannedDate preset overdue matches only past dates', () => {
    const tasks = [
      makeTask({ id: 'past', when_date: localDate(-3) }),
      makeTask({ id: 'today', when_date: localDate(0) }),
      makeTask({ id: 'future', when_date: localDate(3) }),
    ]
    const result = filterTasks(tasks, { ...noFilters, plannedDate: { type: 'preset', preset: 'overdue' } })
    expect(result.map((t) => t.id)).toEqual(['past'])
  })

  test('plannedDate preset noDate matches null and empty when_date', () => {
    const tasks = [
      makeTask({ id: 'null', when_date: null }),
      makeTask({ id: 'empty', when_date: '' }),
      makeTask({ id: 'dated', when_date: localDate(0) }),
    ]
    const result = filterTasks(tasks, { ...noFilters, plannedDate: { type: 'preset', preset: 'noDate' } })
    expect(result.map((t) => t.id)).toEqual(['null', 'empty'])
  })

  test('plannedDate range is inclusive of both bounds and ignores undated tasks', () => {
    const tasks = [
      makeTask({ id: 'start', when_date: '2026-03-01' }),
      makeTask({ id: 'inside', when_date: '2026-03-15' }),
      makeTask({ id: 'end', when_date: '2026-03-31' }),
      makeTask({ id: 'before', when_date: '2026-02-28' }),
      makeTask({ id: 'after', when_date: '2026-04-01' }),
      makeTask({ id: 'undated', when_date: null }),
    ]
    const result = filterTasks(tasks, {
      ...noFilters,
      plannedDate: { type: 'range', start: '2026-03-01', end: '2026-03-31' },
    })
    expect(result.map((t) => t.id)).toEqual(['start', 'inside', 'end'])
  })

  test('plannedDate range with only a start bound keeps everything from it', () => {
    const tasks = [
      makeTask({ id: 'before', when_date: '2026-02-28' }),
      makeTask({ id: 'on', when_date: '2026-03-01' }),
      makeTask({ id: 'later', when_date: '2026-12-01' }),
    ]
    const result = filterTasks(tasks, { ...noFilters, plannedDate: { type: 'range', start: '2026-03-01' } })
    expect(result.map((t) => t.id)).toEqual(['on', 'later'])
  })

  test('plannedDate specific matches exactly one date', () => {
    const tasks = [makeTask({ id: 'a', when_date: '2026-03-05' }), makeTask({ id: 'b', when_date: '2026-03-06' })]
    const result = filterTasks(tasks, { ...noFilters, plannedDate: { type: 'specific', date: '2026-03-05' } })
    expect(result.map((t) => t.id)).toEqual(['a'])
  })

  test('deadline filter applies to the deadline field, not when_date', () => {
    const tasks = [
      makeTask({ id: 'deadline-today', deadline: localDate(0), when_date: localDate(9) }),
      makeTask({ id: 'when-today', deadline: localDate(9), when_date: localDate(0) }),
    ]
    const result = filterTasks(tasks, { ...noFilters, deadline: { type: 'preset', preset: 'today' } })
    expect(result.map((t) => t.id)).toEqual(['deadline-today'])
  })
})

describe('hasFilters', () => {
  test('false for the empty filter state', () => {
    expect(hasFilters(noFilters)).toBe(false)
  })

  test('true when any single kind is active', () => {
    expect(hasFilters({ ...noFilters, areas: ['a'] })).toBe(true)
    expect(hasFilters({ ...noFilters, projects: ['p'] })).toBe(true)
    expect(hasFilters({ ...noFilters, tags: ['t'] })).toBe(true)
    expect(hasFilters({ ...noFilters, highPriority: true })).toBe(true)
    expect(hasFilters({ ...noFilters, plannedDate: { type: 'preset', preset: 'today' } })).toBe(true)
    expect(hasFilters({ ...noFilters, deadline: { type: 'preset', preset: 'today' } })).toBe(true)
    expect(hasFilters({ ...noFilters, search: 'x' })).toBe(true)
  })
})

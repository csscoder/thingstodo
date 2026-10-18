import { describe, it, expect, vi, beforeEach } from 'vitest'
import { renderHook, waitFor, act } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { useInbox, useCreateTask, useCompleteTask, useUpdateTaskSchedule, useLogout, useDeleteArea, useDeleteTag } from '../queries'
import { applyTaskReorder } from '../../components/AppDndContext'
import { localDb } from '../../db'
import { server } from '../../test/mocks/server'
import { http, HttpResponse } from 'msw'
import type { Task } from '../../api/types'

// Mock audio to avoid AudioContext errors in jsdom
vi.mock('../../lib/sounds', () => ({
  playCompleteSound: vi.fn(),
  playReviewSound: vi.fn(),
}))

// Mock sync engine to avoid network calls in tests
vi.mock('../../sync/engine', () => ({
  syncNow: vi.fn().mockResolvedValue(undefined),
  stopSyncEngine: vi.fn(),
}))

function createWrapper() {
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0 },
      mutations: { retry: false },
    },
  })

  function Wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  }

  return { Wrapper, queryClient }
}

describe('useInbox', () => {
  it('fetches inbox data', async () => {
    const { Wrapper } = createWrapper()

    const { result } = renderHook(() => useInbox(), { wrapper: Wrapper })

    await waitFor(() => {
      expect(result.current.isSuccess).toBe(true)
    })

    expect(result.current.data?.tasks).toHaveLength(2)
    expect(result.current.data?.tasks[0].title).toBe('Buy groceries')
  })
})

describe('useCreateTask', () => {
  beforeEach(async () => {
    await localDb.tasks.clear()
    await localDb.syncQueue.clear()
  })

  it('writes task to local IndexedDB', async () => {
    const { Wrapper } = createWrapper()

    const { result } = renderHook(() => useCreateTask(), { wrapper: Wrapper })

    await act(async () => {
      result.current.mutate({ title: 'New task' })
    })

    await waitFor(() => {
      expect(result.current.isSuccess).toBe(true)
    })

    // Verify task was written to IndexedDB
    const tasks = await localDb.tasks.toArray()
    expect(tasks).toHaveLength(1)
    expect(tasks[0].title).toBe('New task')
    expect(tasks[0]._syncStatus).toBe('pending')
  })
})

describe('useCompleteTask', () => {
  beforeEach(async () => {
    vi.clearAllMocks()
    await localDb.tasks.clear()
    await localDb.syncQueue.clear()
  })

  it('writes completed status to local IndexedDB', async () => {
    const { Wrapper } = createWrapper()

    // Seed a task in IndexedDB
    await localDb.tasks.put({
      id: 'task-1',
      title: 'Buy groceries',
      status: 'open',
      notes: '',
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
      created_at: '2026-01-01T00:00:00Z',
      updated_at: '2026-01-01T00:00:00Z',
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
      first_schedule_completed: undefined,
      schedule_entry_id: null,
      past_schedule_count: undefined,
      has_actionable_schedules: undefined,
      all_today_schedules_completed: undefined,
      _syncStatus: 'synced',
      _localUpdatedAt: '2026-01-01T00:00:00Z',
    })

    const { result } = renderHook(() => useCompleteTask(), { wrapper: Wrapper })

    await act(async () => {
      result.current.mutate('task-1')
    })

    await waitFor(() => {
      expect(result.current.isSuccess).toBe(true)
    })

    // Verify task was marked completed in IndexedDB
    const task = await localDb.tasks.get('task-1')
    expect(task?.status).toBe('completed')
    expect(task?.completed_at).not.toBeNull()
    expect(task?._syncStatus).toBe('pending')
  })

  it('queues a sync entry for the completed task', async () => {
    const { Wrapper } = createWrapper()

    await localDb.tasks.put({
      id: 'task-2',
      title: 'Test task',
      status: 'open',
      notes: '',
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
      created_at: '2026-01-01T00:00:00Z',
      updated_at: '2026-01-01T00:00:00Z',
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
      first_schedule_completed: undefined,
      schedule_entry_id: null,
      past_schedule_count: undefined,
      has_actionable_schedules: undefined,
      all_today_schedules_completed: undefined,
      _syncStatus: 'synced',
      _localUpdatedAt: '2026-01-01T00:00:00Z',
    })

    const { result } = renderHook(() => useCompleteTask(), { wrapper: Wrapper })

    await act(async () => {
      result.current.mutate('task-2')
    })

    await waitFor(() => {
      expect(result.current.isSuccess).toBe(true)
    })

    // Verify a sync queue entry was created
    const queue = await localDb.syncQueue.toArray()
    const entry = queue.find((e) => e.entityId === 'task-2')
    expect(entry).toBeDefined()
    expect(entry?.entity).toBe('task')
    expect(entry?.action).toBe('update')
  })
})

describe('useUpdateTaskSchedule', () => {
  const localTaskBase = {
    title: 'Schedule task',
    status: 'open',
    notes: '',
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
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
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
    first_schedule_completed: undefined,
    schedule_entry_id: null,
    past_schedule_count: undefined,
    has_actionable_schedules: undefined,
    all_today_schedules_completed: undefined,
    _syncStatus: 'synced',
    _localUpdatedAt: '2026-01-01T00:00:00Z',
  }

  beforeEach(async () => {
    vi.clearAllMocks()
    await localDb.tasks.clear()
    await localDb.syncQueue.clear()
    await localDb.schedules.clear()
  })

  it('does not create a synthetic schedule without a date', async () => {
    const { Wrapper } = createWrapper()

    // Task with no when_date and no schedule rows
    await localDb.tasks.put({ ...localTaskBase, id: 'task-3' })

    const { result } = renderHook(() => useUpdateTaskSchedule('task-3'), { wrapper: Wrapper })

    await act(async () => {
      result.current.mutate({ id: 'synth-task-3', data: { start_time: '09:00' } })
    })

    await waitFor(() => {
      expect(result.current.isError).toBe(true)
    })
    expect(result.current.error).toEqual(new Error('Pick a date before setting a time'))

    // Nothing may be written or queued
    const schedules = await localDb.schedules.toArray()
    expect(schedules).toHaveLength(0)
    const queue = await localDb.syncQueue.toArray()
    expect(queue).toHaveLength(0)
  })
})

const localAreaBase = {
  title: 'Work',
  sort_order: 0,
  project_count: 1,
  task_count: 0,
  standalone_task_count: 0,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
  _syncStatus: 'synced' as const,
  _localUpdatedAt: '2026-01-01T00:00:00Z',
}

describe('useDeleteArea', () => {
  beforeEach(async () => {
    vi.clearAllMocks()
    await localDb.areas.clear()
  })

  it('delete area keeps the local row when the server returns 409', async () => {
    server.use(
      http.delete('/api/areas/:id', () =>
        HttpResponse.json({ error: 'area still has projects', code: 'HAS_PROJECTS' }, { status: 409 })),
    )
    await localDb.areas.put({ ...localAreaBase, id: 'area-409' })

    const { Wrapper } = createWrapper()
    const { result } = renderHook(() => useDeleteArea(), { wrapper: Wrapper })

    await act(async () => {
      result.current.mutate('area-409')
    })

    await waitFor(() => {
      expect(result.current.isError).toBe(true)
    })
    expect(result.current.error).toEqual(new Error("Move or delete this area's projects first"))

    // Dexie was never touched
    const area = await localDb.areas.get('area-409')
    expect(area?.title).toBe('Work')
  })
})

describe('useDeleteTag', () => {
  beforeEach(async () => {
    vi.clearAllMocks()
    await localDb.tags.clear()
  })

  it('delete tag offline leaves the tag and reports "This needs a connection"', async () => {
    await localDb.tags.put({
      id: 'tag-1',
      title: 'urgent',
      color: null,
      parent_tag_id: null,
      sort_order: 0,
      task_count: 2,
      _syncStatus: 'synced',
      _localUpdatedAt: '2026-01-01T00:00:00Z',
    })

    const onLineDesc = Object.getOwnPropertyDescriptor(Navigator.prototype, 'onLine')
    Object.defineProperty(navigator, 'onLine', { configurable: true, get: () => false })
    try {
      const { Wrapper } = createWrapper()
      const { result } = renderHook(() => useDeleteTag(), { wrapper: Wrapper })

      await act(async () => {
        result.current.mutate('tag-1')
      })

      await waitFor(() => {
        expect(result.current.isError).toBe(true)
      })
      expect(result.current.error).toEqual(new Error('This needs a connection'))

      // Dexie was never touched
      const tag = await localDb.tags.get('tag-1')
      expect(tag?.title).toBe('urgent')
    } finally {
      // Remove the instance-level stub so Navigator.prototype's real getter applies again
      delete (navigator as { onLine?: boolean }).onLine
      if (onLineDesc) Object.defineProperty(Navigator.prototype, 'onLine', onLineDesc)
    }
  })
})

describe('applyTaskReorder', () => {
  const reorderTaskBase = {
    title: 'Reorder task',
    status: 'open',
    notes: '',
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
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
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
    first_schedule_completed: undefined,
    schedule_entry_id: null,
    past_schedule_count: undefined,
    has_actionable_schedules: undefined,
    all_today_schedules_completed: undefined,
    _syncStatus: 'synced' as const,
    _localUpdatedAt: '2026-01-01T00:00:00Z',
  }

  beforeEach(async () => {
    vi.clearAllMocks()
    await localDb.tasks.clear()
  })

  it('task reorder failure keeps the previous order', async () => {
    server.use(
      http.patch('/api/tasks/reorder', () => new HttpResponse(null, { status: 500 })),
    )
    await localDb.tasks.put({ ...reorderTaskBase, id: 'first', title: 'First', sort_order_today: 0 })
    await localDb.tasks.put({ ...reorderTaskBase, id: 'second', title: 'Second', sort_order_today: 1024 })
    const tasks = (await localDb.tasks.toArray()) as unknown as Task[]

    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    // Move 'second' above 'first'
    await applyTaskReorder(queryClient, tasks, 'sort_order_today', 'second', 'first')

    // Server rejected the reorder: local order is unchanged
    const second = await localDb.tasks.get('second')
    expect(second?.sort_order_today).toBe(1024)
    const first = await localDb.tasks.get('first')
    expect(first?.sort_order_today).toBe(0)
  })
})

describe('useLogout', () => {
  const originalLocation = Object.getOwnPropertyDescriptor(window, 'location')

  beforeEach(async () => {
    vi.clearAllMocks()
    await localDb.tasks.clear()
    await localDb.syncQueue.clear()
    await localDb.syncMeta.clear()
    localStorage.setItem('thingstodo_timezone', 'Europe/Berlin')
    server.use(http.delete('/api/auth/logout', () => new HttpResponse(null, { status: 204 })))
    // jsdom-safe navigation stub: capture href assignments without navigating
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: { href: 'http://localhost/' },
    })
  })

  afterEach(() => {
    if (originalLocation) Object.defineProperty(window, 'location', originalLocation)
  })

  it('logout clears every Dexie table including the queue', async () => {
    const { Wrapper, queryClient } = createWrapper()
    const clearSpy = vi.spyOn(queryClient, 'clear')

    await localDb.tasks.put({
      id: 'logout-task',
      title: 'Pending local edit',
      status: 'open',
      notes: '',
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
      created_at: '2026-01-01T00:00:00Z',
      updated_at: '2026-01-01T00:00:00Z',
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
      first_schedule_completed: undefined,
      schedule_entry_id: null,
      past_schedule_count: undefined,
      has_actionable_schedules: undefined,
      all_today_schedules_completed: undefined,
      _syncStatus: 'pending',
      _localUpdatedAt: '2026-01-01T00:00:00Z',
    })
    await localDb.syncQueue.bulkPut([
      {
        entity: 'task',
        entityId: 'logout-task',
        action: 'update',
        data: { title: 'Pending local edit' },
        clientUpdatedAt: '2026-01-01T00:00:00Z',
        createdAt: '2026-01-01T00:00:00Z',
      },
      {
        entity: 'project',
        entityId: 'logout-proj',
        action: 'create',
        data: { title: 'Local project' },
        clientUpdatedAt: '2026-01-01T00:00:00Z',
        createdAt: '2026-01-01T00:00:00Z',
      },
    ])
    await localDb.syncMeta.put({ key: 'cursor', value: 42 })

    const { result } = renderHook(() => useLogout(), { wrapper: Wrapper })

    await act(async () => {
      result.current.mutate()
    })

    await waitFor(() => {
      expect(result.current.isSuccess).toBe(true)
    })

    // Every Dexie table is dropped and recreated empty
    await waitFor(() => expect(localDb.tasks.count()).resolves.toBe(0))
    await expect(localDb.syncQueue.count()).resolves.toBe(0)
    await expect(localDb.syncMeta.count()).resolves.toBe(0)

    // Local device state is removed
    expect(localStorage.getItem('thingstodo_timezone')).toBeNull()

    // Redirect happens and the query cache is cleared
    expect((window.location as unknown as { href: string }).href).toBe('/login')
    expect(clearSpy).toHaveBeenCalled()
  })
})

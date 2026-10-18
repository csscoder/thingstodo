import { beforeEach, describe, expect, test, vi } from 'vitest'
import { act, renderHook, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { useBulkAction } from '../useBulkAction'
import { useAppStore } from '../../stores/app'
import { localDb } from '../../db/index'
import type { BulkActionRequest } from '../../api/types'
import type { LocalTask } from '../../db/schema'

// Mock syncNow so mutations do not call the API
vi.mock('../../sync/engine', () => ({
  syncNow: vi.fn().mockResolvedValue(undefined),
}))

function makeTask(overrides: Partial<LocalTask> = {}): LocalTask {
  const now = new Date().toISOString()
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
    created_at: now,
    updated_at: '2020-01-01T00:00:00.000Z',
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
    _localUpdatedAt: now,
    ...overrides,
  }
}

async function clearDb() {
  await Promise.all(localDb.tables.map((t) => t.clear()))
}

async function seedTask(overrides: Partial<LocalTask> = {}): Promise<void> {
  await localDb.tasks.put(makeTask(overrides))
}

function createWrapper() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return function Wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  }
}

async function runBulk(data: BulkActionRequest) {
  const { result } = renderHook(() => useBulkAction(), { wrapper: createWrapper() })
  await act(async () => {
    await result.current.mutateAsync(data)
  })
}

async function taskAfter(): Promise<LocalTask | undefined> {
  return localDb.tasks.get('task-1')
}

async function queueForTask(): Promise<Array<{ action: string; data: Record<string, unknown> }>> {
  const entries = await localDb.syncQueue.toArray()
  return entries
    .filter((e) => e.entity === 'task' && e.entityId === 'task-1')
    .map((e) => ({ action: e.action, data: e.data ?? {} }))
}

describe('useBulkAction', () => {
  beforeEach(async () => {
    await clearDb()
    vi.clearAllMocks()
    useAppStore.setState({
      selectedTaskIds: new Set(),
      selectionSection: null,
      lastSelectedTaskId: null,
      departingTaskIds: new Set(),
    })
  })

  test.each([
    ['complete', { action: 'complete' as const }, { status: 'completed', completed_at: expect.any(String) }],
    ['cancel', { action: 'cancel' as const }, { status: 'canceled', canceled_at: expect.any(String) }],
    ['wontdo', { action: 'wontdo' as const }, { status: 'wont_do', canceled_at: expect.any(String) }],
    ['delete', { action: 'delete' as const }, { deleted_at: expect.any(String) }],
    ['set_when', { action: 'set_when' as const, params: { when_date: '2026-03-05' } }, { when_date: '2026-03-05' }],
    ['set_deadline', { action: 'set_deadline' as const, params: { deadline: '2026-04-01' } }, { deadline: '2026-04-01' }],
    ['set_priority', { action: 'set_priority' as const, params: { high_priority: true } }, { high_priority: true }],
  ])('%s writes through local mutations and queues a sync update', async (_name, request, expected) => {
    await seedTask()
    await runBulk({ task_ids: ['task-1'], ...request } as BulkActionRequest)

    await expect(taskAfter()).resolves.toMatchObject(expected)
    const queue = await queueForTask()
    expect(queue.some((e) => e.action === 'update')).toBe(true)
  })

  test('toggle_priority flips the current value per task', async () => {
    await seedTask({ id: 'task-on', high_priority: true })
    await seedTask({ id: 'task-off', high_priority: false })
    await runBulk({ task_ids: ['task-on', 'task-off'], action: 'toggle_priority' })

    expect((await localDb.tasks.get('task-on'))?.high_priority).toBe(false)
    expect((await localDb.tasks.get('task-off'))?.high_priority).toBe(true)
    const queue = await localDb.syncQueue.toArray()
    expect(queue.filter((e) => e.entity === 'task' && e.action === 'update')).toHaveLength(2)
  })

  test('move_project sets both project and area', async () => {
    await seedTask({ project_id: 'p-old', area_id: 'a-old' })
    await runBulk({ task_ids: ['task-1'], action: 'move_project', params: { project_id: 'p-new', area_id: 'a-new' } })

    const task = await taskAfter()
    expect(task?.project_id).toBe('p-new')
    expect(task?.area_id).toBe('a-new')
  })

  test('add_tags merges without duplicates, unknown tags keep a placeholder ref', async () => {
    await seedTask({ tags: [{ id: 'tag-1', title: 'One', color: null }] })
    await runBulk({ task_ids: ['task-1'], action: 'add_tags', params: { tag_ids: ['tag-2', 'tag-1'] } })

    const task = await taskAfter()
    expect(task?.tags.map((t) => t.id)).toEqual(['tag-1', 'tag-2'])
    expect(task?.tags[1]).toEqual({ id: 'tag-2', title: '', color: null })
    const queue = await queueForTask()
    expect(queue.some((e) => e.data.tag_ids)).toBe(true)
  })

  test('remove_tags drops only the listed tags', async () => {
    await seedTask({ tags: [
      { id: 'tag-1', title: 'One', color: null },
      { id: 'tag-2', title: 'Two', color: null },
    ] })
    await runBulk({ task_ids: ['task-1'], action: 'remove_tags', params: { tag_ids: ['tag-1'] } })

    const task = await taskAfter()
    expect(task?.tags.map((t) => t.id)).toEqual(['tag-2'])
  })

  test('toggle_tags adds missing and removes present tags', async () => {
    await seedTask({ tags: [{ id: 'tag-1', title: 'One', color: null }] })
    await runBulk({ task_ids: ['task-1'], action: 'toggle_tags', params: { tag_ids: ['tag-1', 'tag-3'] } })

    const task = await taskAfter()
    expect(task?.tags.map((t) => t.id)).toEqual(['tag-3'])
  })

  test('mark_reviewed bumps updated_at', async () => {
    await seedTask({ updated_at: '2020-01-01T00:00:00.000Z' })
    await runBulk({ task_ids: ['task-1'], action: 'mark_reviewed' })

    const task = await taskAfter()
    expect(task && task.updated_at > '2020-01-01T00:00:00.000Z').toBe(true)
  })

  test('destructive actions mark tasks departing and clear the selection', async () => {
    await seedTask({ id: 'task-1' })
    await seedTask({ id: 'task-2' })
    useAppStore.setState({
      selectedTaskIds: new Set(['task-1', 'task-2']),
      selectionSection: 'today',
      lastSelectedTaskId: 'task-2',
    })

    await runBulk({ task_ids: ['task-1', 'task-2'], action: 'complete' })
    await waitFor(() => {
      const { selectedTaskIds, selectionSection, lastSelectedTaskId } = useAppStore.getState()
      expect(selectedTaskIds.size).toBe(0)
      expect(selectionSection).toBeNull()
      expect(lastSelectedTaskId).toBeNull()
    })
    // Both tasks were marked departing during the run (cleared after 800ms)
    expect([...useAppStore.getState().departingTaskIds].sort()).toEqual(['task-1', 'task-2'])
  })

  test('destructive action on one of two selected tasks keeps the other selected', async () => {
    await seedTask({ id: 'task-1' })
    await seedTask({ id: 'task-2' })
    useAppStore.setState({
      selectedTaskIds: new Set(['task-1', 'task-2']),
      selectionSection: 'today',
      lastSelectedTaskId: 'task-2',
    })

    await runBulk({ task_ids: ['task-1'], action: 'delete' })
    await waitFor(() => {
      const { selectedTaskIds, selectionSection } = useAppStore.getState()
      expect([...selectedTaskIds]).toEqual(['task-2'])
      expect(selectionSection).toBe('today')
    })
  })

  test('non-destructive actions keep the selection', async () => {
    await seedTask()
    useAppStore.setState({ selectedTaskIds: new Set(['task-1']), selectionSection: 'today' })

    await runBulk({ task_ids: ['task-1'], action: 'set_priority', params: { high_priority: true } })

    const { selectedTaskIds, selectionSection } = useAppStore.getState()
    expect([...selectedTaskIds]).toEqual(['task-1'])
    expect(selectionSection).toBe('today')
  })
})

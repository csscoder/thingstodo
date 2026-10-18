import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { QueryClient } from '@tanstack/react-query'
import { api } from '../../api/client'
import { localDb } from '../../db/index'
import { createProject, createTask } from '../../db/mutations'
import { applySidebarDrop, applyTaskReorder } from '../AppDndContext'
import { ownerToday } from '../../lib/owner-time'
import { reorderTasks } from '../../api/tasks'
import type { Task } from '../../api/types'

// Mock the sync engine so queued changes never hit the network
vi.mock('../../sync/engine', () => ({
  syncNow: vi.fn().mockResolvedValue(undefined),
}))

vi.mock('../../api/tasks', () => ({
  reorderTasks: vi.fn().mockResolvedValue({ updated: 1 }),
}))

async function clearDb() {
  await Promise.all(localDb.tables.map((t) => t.clear()))
}

describe('applySidebarDrop (multi-drag drop semantics)', () => {
  beforeEach(async () => {
    await clearDb()
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('multi-drop to Completed queues one update per task and calls no REST endpoint', async () => {
    const postSpy = vi.spyOn(api, 'post')
    const ids = await Promise.all([
      createTask({ title: 'A' }),
      createTask({ title: 'B' }),
      createTask({ title: 'C' }),
    ])
    // Drop the seeded 'create' entries so only the drop's queue writes remain
    await localDb.syncQueue.clear()

    await applySidebarDrop({ kind: 'completed' }, ids)

    expect(postSpy).not.toHaveBeenCalled()
    const queue = await localDb.syncQueue.toArray()
    expect(queue).toHaveLength(ids.length)
    for (const id of ids) {
      const entries = queue.filter((e) => e.entityId === id)
      expect(entries).toHaveLength(1)
      expect(entries[0].entity).toBe('task')
      expect(entries[0].action).toBe('update')
      const task = await localDb.tasks.get(id)
      expect(task?.status).toBe('completed')
      expect(task?.completed_at).toBeTruthy()
    }
  })

  test('Inbox drop clears project, area, heading and when_date', async () => {
    const id = await createTask({
      title: 'Full task',
      project_id: 'proj-seed',
      area_id: 'area-seed',
      heading_id: 'heading-seed',
      when_date: '2026-01-15',
    })
    await localDb.syncQueue.clear()

    await applySidebarDrop({ kind: 'inbox' }, [id])

    const task = await localDb.tasks.get(id)
    expect(task?.project_id).toBeNull()
    expect(task?.area_id).toBeNull()
    expect(task?.heading_id).toBeNull()
    expect(task?.when_date).toBeNull()
  })

  test("project drop sets project_id and inherits the project's area_id", async () => {
    const projectId = await createProject({ title: 'Website', area_id: 'area-1' })
    const taskId = await createTask({ title: 'Landing page' })
    await localDb.syncQueue.clear()

    await applySidebarDrop({ kind: 'project', id: projectId }, [taskId])

    const task = await localDb.tasks.get(taskId)
    expect(task?.project_id).toBe(projectId)
    expect(task?.area_id).toBe('area-1')
  })

  test('sidebar drop on a list restores a trashed task and reopens a completed one', async () => {
    const trashedId = await createTask({
      title: 'Trashed',
      project_id: 'proj-seed',
      deleted_at: '2026-01-01T00:00:00.000Z',
    })
    const completedId = await createTask({
      title: 'Done',
      status: 'completed',
      completed_at: '2026-01-01T00:00:00.000Z',
    })
    await localDb.syncQueue.clear()

    await applySidebarDrop({ kind: 'inbox' }, [trashedId])
    await applySidebarDrop({ kind: 'today' }, [completedId])

    const restored = await localDb.tasks.get(trashedId)
    expect(restored?.deleted_at).toBeNull()
    expect(restored?.project_id).toBeNull()

    const reopened = await localDb.tasks.get(completedId)
    expect(reopened?.status).toBe('open')
    expect(reopened?.completed_at).toBeNull()
    expect(reopened?.when_date).toBe(ownerToday())
  })
})

describe('applyTaskReorder (same-list index mapping)', () => {
  const now = '2026-01-01T00:00:00.000Z'
  let tasks: Task[]
  let queryClient: QueryClient

  function task(id: string, order: number): Task {
    return {
      id,
      title: id.toUpperCase(),
      status: 'open',
      sort_order_today: order,
      sort_order_project: order,
      sort_order_heading: order,
    } as unknown as Task
  }

  /** Run a reorder of activeId onto overId and return the new sort_order sent to the server. */
  async function reorderAndGetOrder(activeId: string, overId: string): Promise<number> {
    vi.mocked(reorderTasks).mockClear()
    await applyTaskReorder(queryClient, tasks, 'sort_order_today', activeId, overId)
    expect(reorderTasks).toHaveBeenCalledTimes(1)
    const arg = vi.mocked(reorderTasks).mock.calls[0][0]
    expect(arg).toHaveLength(1)
    expect(arg[0].id).toBe(activeId)
    return arg[0].sort_order
  }

  beforeEach(async () => {
    await clearDb()
    // [A, B, C] with well-separated orders
    tasks = [task('a', 1000), task('b', 2000), task('c', 3000)]
    await localDb.tasks.bulkAdd(
      tasks.map((t) => ({ ...t, _syncStatus: 'synced', _localUpdatedAt: now }) as never),
    )
    queryClient = new QueryClient()
  })

  test('moving A down one (onto B) lands A between B and C', async () => {
    const order = await reorderAndGetOrder('a', 'b')
    expect(order).toBeGreaterThan(2000)
    expect(order).toBeLessThan(3000)
  })

  test('moving A down two (onto C) lands A after C', async () => {
    const order = await reorderAndGetOrder('a', 'c')
    expect(order).toBeGreaterThan(3000)
  })

  test('moving C up one (onto B) lands C between A and B', async () => {
    const order = await reorderAndGetOrder('c', 'b')
    expect(order).toBeGreaterThan(1000)
    expect(order).toBeLessThan(2000)
  })

  test('moving C up two (onto A) lands C before A', async () => {
    const order = await reorderAndGetOrder('c', 'a')
    expect(order).toBeLessThan(1000)
  })
})

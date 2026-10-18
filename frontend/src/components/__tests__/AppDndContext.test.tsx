import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { api } from '../../api/client'
import { localDb } from '../../db/index'
import { createProject, createTask } from '../../db/mutations'
import { applySidebarDrop } from '../AppDndContext'
import { ownerToday } from '../../lib/owner-time'

// Mock the sync engine so queued changes never hit the network
vi.mock('../../sync/engine', () => ({
  syncNow: vi.fn().mockResolvedValue(undefined),
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

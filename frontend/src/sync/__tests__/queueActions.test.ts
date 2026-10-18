import { beforeEach, describe, expect, test, vi } from 'vitest'
import { localDb } from '../../db/index'
import { mockTask } from '../../test/mocks/data'
import type { LocalSchedule, LocalTask, SyncQueueEntry } from '../../db/schema'

// syncNow is the engine's job; here it only needs to be observable.
vi.mock('../engine', () => ({
  syncNow: vi.fn().mockResolvedValue(undefined),
}))

import { syncNow } from '../engine'
import {
  discardEntry,
  entriesRemovedByDiscard,
  listStuckEntries,
  retryEntry,
} from '../queueActions'

const mockSyncNow = vi.mocked(syncNow)

async function clearDb() {
  await Promise.all(localDb.tables.map((t) => t.clear()))
}

async function addQueueEntry(
  overrides: Partial<Omit<SyncQueueEntry, 'id'>> = {},
): Promise<SyncQueueEntry> {
  const entry: Omit<SyncQueueEntry, 'id'> = {
    entity: 'task',
    entityId: 'task-1',
    action: 'update',
    data: { title: 'Test task' },
    clientUpdatedAt: new Date().toISOString(),
    createdAt: new Date().toISOString(),
    ...overrides,
  }
  const id = await localDb.syncQueue.add(entry as SyncQueueEntry)
  return { ...entry, id }
}

function seedTask(overrides: Partial<LocalTask> = {}): LocalTask {
  return {
    ...mockTask,
    _syncStatus: 'pending',
    _localUpdatedAt: '2026-01-01T00:00:00.000Z',
    ...overrides,
  }
}

function seedSchedule(overrides: Partial<LocalSchedule> = {}): LocalSchedule {
  return {
    id: 'sched-1',
    task_id: 'task-1',
    when_date: '2026-01-01',
    start_time: null,
    end_time: null,
    completed: false,
    sort_order: 1024,
    created_at: '2026-01-01 00:00:00',
    _syncStatus: 'pending',
    _localUpdatedAt: '2026-01-01T00:00:00.000Z',
    ...overrides,
  }
}

describe('queueActions', () => {
  beforeEach(async () => {
    await clearDb()
    mockSyncNow.mockReset()
    mockSyncNow.mockResolvedValue(undefined)
  })

  test('listStuckEntries returns only stuck entries in queue order', async () => {
    const healthy = await addQueueEntry({ entityId: 'task-healthy' })
    const firstStuck = await addQueueEntry({
      entityId: 'task-first',
      state: 'stuck',
      errorCode: 'CONFLICT',
      errorMessage: 'conflict',
    })
    const secondStuck = await addQueueEntry({
      entityId: 'task-later',
      state: 'stuck',
      errorCode: 'VALIDATION',
      errorMessage: 'invalid field title',
    })

    const stuck = await listStuckEntries()

    expect(stuck.map((entry) => entry.id)).toEqual([firstStuck.id, secondStuck.id])
    expect(stuck.every((entry) => entry.state === 'stuck')).toBe(true)
    expect(stuck.map((entry) => entry.id)).not.toContain(healthy.id)
  })

  test('discard of an update restores the skipped snapshot', async () => {
    await localDb.tasks.put(seedTask({ id: 'task-1', title: 'Local edit' }))
    const stuck = await addQueueEntry({
      entityId: 'task-1',
      action: 'update',
      fields: ['title'],
      data: { title: 'Local edit' },
      state: 'stuck',
      errorCode: 'CONFLICT',
      errorMessage: 'conflict with newer revision',
    })
    // A later edit of the same entity is discarded with the stuck one.
    await addQueueEntry({
      entityId: 'task-1',
      action: 'update',
      fields: ['notes'],
      data: { notes: 'later edit' },
    })
    await localDb.skippedSnapshots.put({
      entity: 'task',
      entityId: 'task-1',
      action: 'update',
      snapshot: { ...seedTask({ id: 'task-1', title: 'Server title' }) },
      seq: 12,
    })

    await discardEntry(stuck.id!)

    // The stuck entry and its later sibling are gone from the queue.
    expect(await localDb.syncQueue.count()).toBe(0)
    // The local row is back to the server version, marked synced.
    const task = await localDb.tasks.get('task-1')
    expect(task?.title).toBe('Server title')
    expect(task?._syncStatus).toBe('synced')
    expect(task?._serverSeq).toBe(12)
    // The snapshot record is consumed.
    expect(await localDb.skippedSnapshots.get(['task', 'task-1'])).toBeUndefined()
    expect(mockSyncNow).toHaveBeenCalledTimes(1)
  })

  test('discard of a create removes its children and the local rows', async () => {
    // The child is queued BEFORE the create: children match at any position.
    await addQueueEntry({
      entity: 'schedule',
      entityId: 'sched-1',
      action: 'create',
      data: { task_id: 'task-9', when_date: '2026-01-01' },
    })
    const stuck = await addQueueEntry({
      entityId: 'task-9',
      action: 'create',
      data: { title: 'New local task' },
      state: 'stuck',
      errorCode: 'VALIDATION',
      errorMessage: 'invalid field title',
    })
    await localDb.tasks.put(seedTask({ id: 'task-9', title: 'New local task' }))
    await localDb.schedules.put(seedSchedule({ id: 'sched-1', task_id: 'task-9' }))
    // fullSync stores a delete snapshot for entities unknown to the server.
    await localDb.skippedSnapshots.put({
      entity: 'task',
      entityId: 'task-9',
      action: 'delete',
      snapshot: {},
      seq: 5,
    })

    await discardEntry(stuck.id!)

    expect(await localDb.syncQueue.count()).toBe(0)
    expect(await localDb.tasks.get('task-9')).toBeUndefined()
    expect(await localDb.schedules.get('sched-1')).toBeUndefined()
    expect(await localDb.skippedSnapshots.get(['task', 'task-9'])).toBeUndefined()
    expect(mockSyncNow).toHaveBeenCalledTimes(1)
  })

  test('discard is atomic: a mid-discard failure rolls everything back', async () => {
    await addQueueEntry({
      entity: 'schedule',
      entityId: 'sched-1',
      action: 'create',
      data: { task_id: 'task-9', when_date: '2026-01-01' },
    })
    const stuck = await addQueueEntry({
      entityId: 'task-9',
      action: 'create',
      data: { title: 'New local task' },
      state: 'stuck',
      errorCode: 'VALIDATION',
      errorMessage: 'invalid field title',
    })
    await localDb.tasks.put(seedTask({ id: 'task-9', title: 'New local task' }))
    await localDb.schedules.put(seedSchedule({ id: 'sched-1', task_id: 'task-9' }))
    await localDb.skippedSnapshots.put({
      entity: 'task',
      entityId: 'task-9',
      action: 'delete',
      snapshot: {},
      seq: 5,
    })

    // The local-row delete for the task fails mid-discard, after the queue
    // entries and the schedule row are already gone.
    const deleteSpy = vi
      .spyOn(localDb.tasks, 'delete')
      .mockRejectedValueOnce(new Error('indexeddb is gone'))

    await expect(discardEntry(stuck.id!)).rejects.toThrow('indexeddb is gone')
    deleteSpy.mockRestore()

    // Nothing was committed: queue, rows, and snapshot are all intact.
    expect(await localDb.syncQueue.count()).toBe(2)
    expect(await localDb.tasks.get('task-9')).toBeDefined()
    expect(await localDb.schedules.get('sched-1')).toBeDefined()
    expect(await localDb.skippedSnapshots.get(['task', 'task-9'])).toBeDefined()
    expect(mockSyncNow).not.toHaveBeenCalled()
  })

  test('retry clears the stuck state', async () => {
    const stuck = await addQueueEntry({
      entityId: 'task-3',
      state: 'stuck',
      errorCode: 'VALIDATION',
      errorMessage: 'invalid field title',
      internalFailures: 2,
    })

    await retryEntry(stuck.id!)

    const entry = await localDb.syncQueue.get(stuck.id!)
    expect(entry).toBeDefined()
    // The stuck markers are gone from the stored object, not just undefined.
    expect('state' in entry!).toBe(false)
    expect('errorCode' in entry!).toBe(false)
    expect('errorMessage' in entry!).toBe(false)
    expect('internalFailures' in entry!).toBe(false)
    expect(mockSyncNow).toHaveBeenCalledTimes(1)
  })

  test('entriesRemovedByDiscard includes later entries of the same entity', async () => {
    const earlier = await addQueueEntry({
      entityId: 'task-a',
      action: 'update',
      fields: ['notes'],
      data: { notes: 'earlier' },
    })
    const stuck = await addQueueEntry({
      entityId: 'task-a',
      state: 'stuck',
      errorCode: 'CONFLICT',
      errorMessage: 'conflict',
    })
    const later = await addQueueEntry({
      entityId: 'task-a',
      action: 'update',
      fields: ['title'],
      data: { title: 'later' },
    })
    const other = await addQueueEntry({ entityId: 'task-b' })

    const removed = await entriesRemovedByDiscard(stuck.id!)

    // The stuck entry and the later task-a entry only.
    expect(removed.map((entry) => entry.id)).toEqual([stuck.id, later.id])
    const removedIds = new Set(removed.map((entry) => entry.id))
    expect(removedIds.has(earlier.id!)).toBe(false)
    expect(removedIds.has(other.id!)).toBe(false)
    // Pure query: the queue itself is untouched.
    expect(await localDb.syncQueue.count()).toBe(4)
  })
})

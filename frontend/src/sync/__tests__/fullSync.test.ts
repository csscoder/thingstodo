import { beforeEach, describe, expect, test, vi } from 'vitest'
import { localDb } from '../../db/index'
import { fullSync, pullChanges } from '../pull'
import type { SyncQueueEntry } from '../../db/schema'

// Mock the API client
vi.mock('../../api/client', () => ({
  api: {
    get: vi.fn(),
    post: vi.fn(),
  },
}))

import { api } from '../../api/client'

const mockGet = vi.mocked(api.get)

async function clearDb() {
  await Promise.all(localDb.tables.map((t) => t.clear()))
}

const nowIso = () => new Date().toISOString()

function makeFullResponse(overrides: Record<string, unknown> = {}) {
  return {
    tasks: [] as Record<string, unknown>[],
    projects: [] as Record<string, unknown>[],
    areas: [] as Record<string, unknown>[],
    tags: [] as Record<string, unknown>[],
    cursor: 42,
    ...overrides,
  }
}

function makeChange(
  entity: string,
  entity_id: string,
  action: 'create' | 'update' | 'delete',
  data: Record<string, unknown>,
  seq: number,
) {
  return {
    seq,
    entity,
    entity_id,
    action,
    snapshot: JSON.stringify({ id: entity_id, ...data }),
    fields: null,
    created_at: nowIso(),
  }
}

async function addQueueEntry(overrides: Partial<Omit<SyncQueueEntry, 'id'>> = {}) {
  const entry: Omit<SyncQueueEntry, 'id'> = {
    entity: 'task',
    entityId: 'task-1',
    action: 'update',
    data: {},
    clientUpdatedAt: nowIso(),
    createdAt: nowIso(),
    ...overrides,
  }
  await localDb.syncQueue.add(entry as SyncQueueEntry)
}

describe('fullSync', () => {
  beforeEach(async () => {
    await clearDb()
    vi.clearAllMocks()
  })

  test('stamps every row with the full-sync cursor', async () => {
    mockGet.mockResolvedValueOnce(
      makeFullResponse({
        tasks: [{ id: 'task-1', title: 'Server task', status: 'open' }],
        schedules: [
          { id: 'sched-1', task_id: 'task-1', when_date: '2026-01-01', sort_order: 0 },
          { id: 'sched-2', task_id: 'task-1', when_date: '2026-01-02', sort_order: 1 },
        ],
      }),
    )

    await fullSync()

    expect((await localDb.tasks.get('task-1'))?._serverSeq).toBe(42)
    expect((await localDb.schedules.get('sched-1'))?._serverSeq).toBe(42)
    expect((await localDb.schedules.get('sched-2'))?._serverSeq).toBe(42)
    expect((await localDb.syncMeta.get('cursor'))?.value).toBe(42)
  })

  test('a later schedule create keeps confirmed schedules', async () => {
    mockGet.mockResolvedValueOnce(
      makeFullResponse({
        tasks: [{ id: 'task-1', title: 'Server task', status: 'open' }],
        schedules: [
          { id: 'sched-1', task_id: 'task-1', when_date: '2026-01-01', sort_order: 0 },
          { id: 'sched-2', task_id: 'task-1', when_date: '2026-01-02', sort_order: 1 },
        ],
      }),
    )
    mockGet.mockResolvedValueOnce({
      changes: [
        makeChange('schedule', 'sched-3', 'create', { task_id: 'task-1', when_date: '2026-01-03', sort_order: 2 }, 43),
      ],
      cursor: 43,
      has_more: false,
    })

    await fullSync()
    await pullChanges()

    const ids = (await localDb.schedules.toArray()).map((s) => s.id).sort()
    expect(ids).toEqual(['sched-1', 'sched-2', 'sched-3'])
  })

  test('fullSync re-applies a stuck create and its pending child', async () => {
    await addQueueEntry({
      entityId: 'task-stuck',
      action: 'create',
      data: { title: 'Local only task', status: 'open' },
      state: 'stuck',
      errorCode: 'VALIDATION',
      errorMessage: 'invalid field title',
    })
    await addQueueEntry({
      entity: 'schedule',
      entityId: 'sched-pending',
      action: 'create',
      data: { task_id: 'task-stuck', when_date: '2026-01-01', sort_order: 0 },
    })

    // The server has neither entity
    mockGet.mockResolvedValueOnce(makeFullResponse())

    await fullSync()

    const task = await localDb.tasks.get('task-stuck')
    expect(task?.title).toBe('Local only task')
    expect(task?._syncStatus).toBe('pending')

    const sched = await localDb.schedules.get('sched-pending')
    expect(sched?.task_id).toBe('task-stuck')
    expect(sched?._syncStatus).toBe('pending')

    // Both remain queued, so Discard can fall back to a delete
    expect((await localDb.skippedSnapshots.get(['task', 'task-stuck']))?.action).toBe('delete')
    expect((await localDb.skippedSnapshots.get(['schedule', 'sched-pending']))?.action).toBe('delete')
  })

  test('fullSync re-applies a stuck update over the server row and keeps the server row as the skipped snapshot', async () => {
    await addQueueEntry({
      entityId: 'task-1',
      action: 'update',
      fields: ['title'],
      data: { title: 'Local edit' },
      state: 'stuck',
      errorCode: 'CONFLICT',
      errorMessage: 'conflict',
    })

    mockGet.mockResolvedValueOnce(
      makeFullResponse({
        tasks: [{ id: 'task-1', title: 'Server title', status: 'open' }],
      }),
    )

    await fullSync()

    const task = await localDb.tasks.get('task-1')
    expect(task?.title).toBe('Local edit')
    expect(task?._syncStatus).toBe('pending')

    const snap = await localDb.skippedSnapshots.get(['task', 'task-1'])
    expect(snap?.action).toBe('update')
    expect(snap?.seq).toBe(42)
    expect(snap?.snapshot).toMatchObject({ id: 'task-1', title: 'Server title' })
  })
})

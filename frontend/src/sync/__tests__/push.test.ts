import { beforeEach, describe, expect, test, vi } from 'vitest'
import { localDb } from '../../db/index'
import { formatPushFailures, pushChanges } from '../push'
import type { SyncQueueEntry } from '../../db/schema'

// Mock the API client
vi.mock('../../api/client', () => ({
  api: {
    post: vi.fn(),
  },
}))

import { api } from '../../api/client'

const mockPost = vi.mocked(api.post)

async function clearDb() {
  await Promise.all(localDb.tables.map((t) => t.clear()))
}

async function addQueueEntry(
  overrides: Partial<Omit<SyncQueueEntry, 'id'>> = {},
): Promise<void> {
  const entry: Omit<SyncQueueEntry, 'id'> = {
    entity: 'task',
    entityId: 'task-abc',
    action: 'create',
    data: { title: 'Test task' },
    clientUpdatedAt: new Date().toISOString(),
    createdAt: new Date().toISOString(),
    ...overrides,
  }
  await localDb.syncQueue.add(entry as SyncQueueEntry)
}

describe('pushChanges', () => {
  beforeEach(async () => {
    await clearDb()
    vi.clearAllMocks()
  })

  test('returns 0 and does not call API when queue is empty', async () => {
    const summary = await pushChanges('device-1')
    expect(summary).toEqual({ processedCount: 0, failures: [], stuckCount: 0 })
    expect(mockPost).not.toHaveBeenCalled()
  })

  test('sends queued entries and clears queue on success', async () => {
    await addQueueEntry({ entityId: 'task-1' })
    await addQueueEntry({ entityId: 'task-2', action: 'update' })

    mockPost.mockResolvedValueOnce({
      results: [
        { entity_id: 'task-1', status: 'applied' },
        { entity_id: 'task-2', status: 'applied' },
      ],
    })

    const summary = await pushChanges('device-1')

    expect(summary).toEqual({ processedCount: 2, failures: [], stuckCount: 0 })
    expect(mockPost).toHaveBeenCalledWith('/sync/push', expect.objectContaining({
      device_id: 'device-1',
    }))

    const remaining = await localDb.syncQueue.toArray()
    expect(remaining).toHaveLength(0)
  })

  test('keeps failed entries in queue', async () => {
    await addQueueEntry({ entityId: 'task-ok' })
    await addQueueEntry({ entityId: 'task-fail' })

    mockPost.mockResolvedValueOnce({
      results: [
        { entity: 'task', entity_id: 'task-ok', status: 'applied' },
        { entity: 'task', entity_id: 'task-fail', status: 'error', error: 'conflict' },
      ],
    })

    const summary = await pushChanges('device-1')

    expect(summary.processedCount).toBe(1)
    expect(summary.failures).toEqual([
      {
        entity: 'task',
        entityId: 'task-fail',
        error: 'conflict',
        count: 1,
      },
    ])
    const remaining = await localDb.syncQueue.toArray()
    expect(remaining).toHaveLength(1)
    expect(remaining[0].entityId).toBe('task-fail')
  })

  test('groups repeated failures into a readable message', () => {
    const message = formatPushFailures([
      { entity: 'task', entityId: 'task-1', error: 'constraint failed', count: 2 },
      { entity: 'task', entityId: 'task-2', error: 'task not found', count: 1 },
    ])

    expect(message).toContain('3 sync changes failed.')
    expect(message).toContain('task task-1 (2x): constraint failed')
    expect(message).toContain('task task-2: task not found')
  })

  test('sends correct change payload shape', async () => {
    await addQueueEntry({
      entityId: 'task-xyz',
      entity: 'task',
      action: 'update',
      fields: ['title', 'status'],
      data: { title: 'Updated', status: 'completed' },
    })

    mockPost.mockResolvedValueOnce({
      results: [{ entity_id: 'task-xyz', status: 'applied' }],
    })

    await pushChanges('my-device')

    const callArgs = mockPost.mock.calls[0]
    const body = callArgs[1] as { device_id: string; changes: unknown[] }
    expect(body.changes).toHaveLength(1)
    const change = body.changes[0] as Record<string, unknown>
    expect(change.entity).toBe('task')
    expect(change.entity_id).toBe('task-xyz')
    expect(change.action).toBe('update')
    expect(change.fields).toEqual(['title', 'status'])
  })

  test('removes only the succeeded entry when siblings share an entity', async () => {
    await addQueueEntry({
      entityId: 'task-1',
      action: 'update',
      fields: ['title'],
      data: { title: 'A' },
      createdAt: '2024-01-01T00:00:00.000Z',
    })
    await addQueueEntry({
      entityId: 'task-1',
      action: 'update',
      fields: ['notes'],
      data: { notes: 'B' },
      createdAt: '2024-01-01T00:00:01.000Z',
    })
    await localDb.skippedSnapshots.put({
      entity: 'task',
      entityId: 'task-1',
      action: 'update',
      snapshot: { title: 'Old title' },
      seq: 3,
    })

    mockPost.mockResolvedValueOnce({
      results: [
        { entity: 'task', entity_id: 'task-1', status: 'applied' },
        { entity: 'task', entity_id: 'task-1', status: 'error', code: 'VALIDATION', error: 'invalid field notes' },
      ],
    })

    const summary = await pushChanges('device-1')

    expect(summary.processedCount).toBe(1)
    expect(summary.stuckCount).toBe(1)

    const remaining = await localDb.syncQueue.toArray()
    expect(remaining).toHaveLength(1)
    expect(remaining[0].fields).toEqual(['notes'])
    expect(remaining[0].state).toBe('stuck')
    expect(remaining[0].errorCode).toBe('VALIDATION')
    expect(remaining[0].errorMessage).toBe('invalid field notes')

    // The stuck sibling still references the entity, so its snapshot is kept.
    expect(await localDb.skippedSnapshots.get(['task', 'task-1'])).toBeDefined()
  })

  test('marks permanent errors stuck and stops sending them', async () => {
    await addQueueEntry({ entityId: 'task-fail', createdAt: '2024-01-01T00:00:00.000Z' })

    mockPost.mockResolvedValueOnce({
      results: [
        { entity: 'task', entity_id: 'task-fail', status: 'error', code: 'CONFLICT', error: 'conflict' },
      ],
    })

    const first = await pushChanges('device-1')
    expect(first.stuckCount).toBe(1)

    await addQueueEntry({ entityId: 'task-new', createdAt: '2024-01-01T00:00:02.000Z' })

    mockPost.mockResolvedValueOnce({
      results: [{ entity: 'task', entity_id: 'task-new', status: 'applied' }],
    })

    const second = await pushChanges('device-1')

    expect(mockPost).toHaveBeenCalledTimes(2)
    const body = mockPost.mock.calls[1][1] as { changes: Array<{ entity_id: string }> }
    expect(body.changes.map((change) => change.entity_id)).toEqual(['task-new'])

    const remaining = await localDb.syncQueue.toArray()
    expect(remaining).toHaveLength(1)
    expect(remaining[0].entityId).toBe('task-fail')
    expect(remaining[0].state).toBe('stuck')
    expect(remaining[0].errorCode).toBe('CONFLICT')
    expect(second.processedCount).toBe(1)
    expect(second.stuckCount).toBe(1)
  })

  test('INTERNAL errors stay pending and become visible after 5', async () => {
    await addQueueEntry({ entityId: 'task-internal' })

    const internalResult = () => ({
      entity: 'task',
      entity_id: 'task-internal',
      status: 'error',
      code: 'INTERNAL',
      error: 'scheduler not available',
    })

    for (let i = 0; i < 4; i++) {
      mockPost.mockResolvedValueOnce({ results: [internalResult()] })
      await pushChanges('device-1')
    }

    let entry = (await localDb.syncQueue.toArray())[0]
    expect(entry.internalFailures).toBe(4)
    expect(entry.state ?? 'pending').toBe('pending')

    mockPost.mockResolvedValueOnce({ results: [internalResult()] })
    await pushChanges('device-1')

    entry = (await localDb.syncQueue.toArray())[0]
    expect(entry.internalFailures).toBe(5)
    expect(entry.state).toBe('stuck')

    // INTERNAL-stuck entries are still auto-retried: the sixth push sends it.
    mockPost.mockResolvedValueOnce({ results: [internalResult()] })
    const summary = await pushChanges('device-1')

    expect(mockPost).toHaveBeenCalledTimes(6)
    const body = mockPost.mock.calls[5][1] as { changes: Array<{ entity_id: string }> }
    expect(body.changes.map((change) => change.entity_id)).toEqual(['task-internal'])

    entry = (await localDb.syncQueue.toArray())[0]
    expect(entry.internalFailures).toBe(6)
    expect(entry.state).toBe('stuck')
    expect(summary.stuckCount).toBe(1)
  })

  test('success clears the skipped snapshot of a drained entity', async () => {
    await addQueueEntry({ entityId: 'task-1' })
    await localDb.skippedSnapshots.put({
      entity: 'task',
      entityId: 'task-1',
      action: 'update',
      snapshot: { title: 'Old title' },
      seq: 3,
    })
    await localDb.skippedSnapshots.put({
      entity: 'task',
      entityId: 'task-other',
      action: 'update',
      snapshot: { title: 'Other' },
      seq: 4,
    })

    mockPost.mockResolvedValueOnce({
      results: [{ entity: 'task', entity_id: 'task-1', status: 'applied' }],
    })

    await pushChanges('device-1')

    expect(await localDb.skippedSnapshots.get(['task', 'task-1'])).toBeUndefined()
    // Snapshots of other entities are untouched.
    expect(await localDb.skippedSnapshots.get(['task', 'task-other'])).toBeDefined()
    expect(await localDb.syncQueue.count()).toBe(0)
  })
})

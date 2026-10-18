import { beforeEach, describe, expect, test, vi } from 'vitest'
import { http, HttpResponse } from 'msw'
import { localDb } from '../../db/index'
import { server } from '../../test/mocks/server'
import { useSyncStore } from '../status'
import { syncNow } from '../engine'
import type { LocalTask, SyncQueueEntry } from '../../db/schema'

async function clearDb() {
  await Promise.all(localDb.tables.map((t) => t.clear()))
}

function resetStore() {
  useSyncStore.setState({
    status: 'idle',
    lastSyncAt: null,
    pendingCount: 0,
    error: null,
    failedCount: 0,
    stuckCount: 0,
  })
}

function makeLocalTask(overrides: Partial<LocalTask> = {}): LocalTask {
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

async function addQueueEntry(overrides: Partial<Omit<SyncQueueEntry, 'id'>> = {}): Promise<void> {
  const entry: Omit<SyncQueueEntry, 'id'> = {
    entity: 'task',
    entityId: 'task-1',
    action: 'create',
    data: { title: 'Task' },
    clientUpdatedAt: new Date().toISOString(),
    createdAt: new Date().toISOString(),
    ...overrides,
  }
  await localDb.syncQueue.add(entry as SyncQueueEntry)
}

const emptyPull = () =>
  http.get('/api/sync/pull', () =>
    HttpResponse.json({ changes: [], cursor: 1, has_more: false }),
  )

const emptyPush = () =>
  http.post('/api/sync/push', () => HttpResponse.json({ results: [] }))

describe('syncNow', () => {
  beforeEach(async () => {
    await clearDb()
    resetStore()
  })

  test('410 on pull falls back to a full sync', async () => {
    await localDb.tasks.put(makeLocalTask({ id: 'local-only' }))
    await localDb.syncMeta.put({ key: 'cursor', value: 3 })

    const fullSync = vi.fn(() =>
      HttpResponse.json({
        tasks: [{ id: 'server-task', title: 'From server', status: 'open' }],
        projects: [],
        areas: [],
        tags: [],
        cursor: 9,
      }),
    )
    server.use(
      emptyPush(),
      http.get('/api/sync/pull', () =>
        HttpResponse.json({ error: 'cursor expired' }, { status: 410 }),
      ),
      http.get('/api/sync/full', fullSync),
    )

    await syncNow()

    expect(fullSync).toHaveBeenCalledTimes(1)
    // The full sync replaced the local table with the server's rows
    expect(await localDb.tasks.get('local-only')).toBeUndefined()
    const serverTask = await localDb.tasks.get('server-task')
    expect(serverTask?.title).toBe('From server')
    expect(serverTask?._syncStatus).toBe('synced')
    expect(serverTask?._serverSeq).toBe(9)
    // The full-sync cursor is saved
    expect((await localDb.syncMeta.get('cursor'))?.value).toBe(9)
    expect(useSyncStore.getState().status).toBe('idle')
  })

  test('a non-410 pull error surfaces as error status without a full sync', async () => {
    const fullSync = vi.fn(() => HttpResponse.json({ tasks: [], cursor: 1 }))
    server.use(
      emptyPush(),
      http.get('/api/sync/pull', () =>
        HttpResponse.json({ error: 'boom' }, { status: 500 }),
      ),
      http.get('/api/sync/full', fullSync),
    )

    await syncNow()

    expect(fullSync).not.toHaveBeenCalled()
    const state = useSyncStore.getState()
    expect(state.status).toBe('error')
    expect(state.error).toBeTruthy()
  })

  test('status is error after a push with failures and idle after a clean sync', async () => {
    // Clean sync: empty queue and empty pull
    server.use(emptyPush(), emptyPull())
    await syncNow()
    expect(useSyncStore.getState().status).toBe('idle')
    expect(useSyncStore.getState().lastSyncAt).not.toBeNull()

    resetStore()

    // Failing push: one permanent error result
    await addQueueEntry({ entityId: 'task-broken' })
    server.use(
      http.post('/api/sync/push', () =>
        HttpResponse.json({
          results: [
            {
              entity: 'task',
              entity_id: 'task-broken',
              status: 'error',
              code: 'VALIDATION',
              error: 'invalid field title',
            },
          ],
        }),
      ),
      emptyPull(),
    )

    await syncNow()

    const state = useSyncStore.getState()
    expect(state.status).toBe('error')
    expect(state.error).toContain('task task-broken')
    expect(state.failedCount).toBe(1)
  })

  test('stuck entries surface via stuckCount while status stays idle', async () => {
    await addQueueEntry({
      entityId: 'task-stuck',
      state: 'stuck',
      errorCode: 'VALIDATION',
      errorMessage: 'invalid field title',
    })
    const push = vi.fn(() => HttpResponse.json({ results: [] }))
    server.use(http.post('/api/sync/push', push), emptyPull())

    await syncNow()

    // A permanently stuck entry is not resent: no push request happens
    expect(push).not.toHaveBeenCalled()
    const state = useSyncStore.getState()
    expect(state.stuckCount).toBe(1)
    expect(state.status).toBe('idle')
    expect(state.error).toBeNull()
  })
})

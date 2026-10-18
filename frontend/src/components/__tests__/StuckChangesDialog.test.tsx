import { beforeEach, describe, expect, test, vi } from 'vitest'
import { render, screen, waitFor } from '../../test/test-utils'
import { localDb } from '../../db/index'
import { mockTask } from '../../test/mocks/data'
import type { LocalTask, SyncQueueEntry } from '../../db/schema'
import { StuckChangesDialog } from '../StuckChangesDialog'

// The dialog's actions run the real queueActions against the fake DB; only
// the engine round-trip is stubbed out.
vi.mock('../../sync/engine', () => ({
  syncNow: vi.fn().mockResolvedValue(undefined),
}))

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

async function seedStuckCreateWithChild() {
  await addQueueEntry({
    entityId: 'task-9',
    action: 'create',
    data: { title: 'New local task' },
    state: 'stuck',
    errorCode: 'VALIDATION',
    errorMessage: 'invalid field title',
  })
  await addQueueEntry({
    entity: 'schedule',
    entityId: 'sched-1',
    action: 'create',
    data: { task_id: 'task-9', when_date: '2026-01-01' },
  })
}

describe('StuckChangesDialog', () => {
  beforeEach(async () => {
    await clearDb()
    vi.clearAllMocks()
  })

  test('lists stuck entries with their server message', async () => {
    await localDb.tasks.put(seedTask({ id: 'task-1', title: 'Pay rent' }))
    await addQueueEntry({
      entityId: 'task-1',
      action: 'update',
      fields: ['title'],
      data: { title: 'Pay rent!' },
      state: 'stuck',
      errorCode: 'VALIDATION',
      errorMessage: 'server rejected title',
    })

    render(<StuckChangesDialog open onClose={() => {}} />)

    // The task title comes from the local task row, not the queue payload.
    expect(await screen.findByText('Pay rent')).toBeInTheDocument()
    expect(screen.getByText('Task')).toBeInTheDocument()
    expect(screen.getByText('server rejected title')).toBeInTheDocument()
  })

  test('discard asks for confirmation listing every affected entry', async () => {
    await seedStuckCreateWithChild()

    const { user } = render(<StuckChangesDialog open onClose={() => {}} />)

    // Only the stuck entry gets a row; its queued child does not.
    const discardButtons = await screen.findAllByRole('button', { name: 'Discard' })
    expect(discardButtons).toHaveLength(1)
    await user.click(discardButtons[0])

    expect(await screen.findByText('Discard 2 change(s)')).toBeInTheDocument()
    // The affected entries are listed one per line in the confirmation.
    expect(screen.getByText(/create Task New local task/)).toBeInTheDocument()
    expect(screen.getByText(/create Schedule sched-1/)).toBeInTheDocument()
  })

  test('cancel removes nothing', async () => {
    await seedStuckCreateWithChild()

    const { user } = render(<StuckChangesDialog open onClose={() => {}} />)

    const discardButtons = await screen.findAllByRole('button', { name: 'Discard' })
    await user.click(discardButtons[0])
    await screen.findByText('Discard 2 change(s)')

    expect(await localDb.syncQueue.count()).toBe(2)

    await user.click(screen.getByRole('button', { name: 'Cancel' }))

    expect(await localDb.syncQueue.count()).toBe(2)
    await waitFor(() => {
      expect(screen.queryByText('Discard 2 change(s)')).not.toBeInTheDocument()
    })
    // The stuck list is still there, untouched.
    expect(await screen.findByText('invalid field title')).toBeInTheDocument()
  })
})

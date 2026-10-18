import { beforeEach, describe, expect, test, vi } from 'vitest'
import { http, HttpResponse } from 'msw'
import { render, screen, waitFor } from '../../test/test-utils'
import { AppLayout } from '../AppLayout'
import { localDb } from '../../db/index'
import { mockTask } from '../../test/mocks/data'
import { server } from '../../test/mocks/server'
import { startSyncEngine } from '../../sync/engine'

// jsdom has no EventSource — stub the SSE hook (AppLayout imports it directly)
vi.mock('../../hooks/useSSE', () => ({
  useSSE: vi.fn(),
}))

// Keep real timers and sync traffic out of the test; assert on the spies
vi.mock('../../sync/engine', () => ({
  syncNow: vi.fn().mockResolvedValue(undefined),
  getOrCreateDeviceId: vi.fn(() => 'dev-test'),
  startSyncEngine: vi.fn(),
  stopSyncEngine: vi.fn(),
}))

const emptyFullSync = { tasks: [], projects: [], areas: [], tags: [], cursor: 0 }

async function clearDb() {
  await Promise.all(localDb.tables.map((t) => t.clear()))
}

describe('AppLayout first sync', () => {
  beforeEach(async () => {
    vi.clearAllMocks()
    await clearDb()
  })

  test('empty local db: shows the error screen with Retry when the first full sync fails', async () => {
    server.use(
      http.get('/api/sync/full', () => new HttpResponse('sync unavailable', { status: 500 })),
    )

    const { user } = render(<AppLayout />)

    await waitFor(() => {
      expect(screen.getByText(/Couldn't load your data/)).toBeInTheDocument()
    })
    expect(screen.queryByText('Syncing your tasks…')).not.toBeInTheDocument()
    expect(startSyncEngine).not.toHaveBeenCalled()

    // The server recovers; Retry hides the overlay and starts the engine
    server.use(
      http.get('/api/sync/full', () => HttpResponse.json(emptyFullSync)),
    )

    await user.click(screen.getByRole('button', { name: 'Retry' }))

    // Retry re-runs init: transient spinner, then the overlay is fully gone
    await waitFor(() => {
      expect(screen.queryByText(/Couldn't load your data/)).not.toBeInTheDocument()
      expect(screen.queryByText('Syncing your tasks…')).not.toBeInTheDocument()
    })
    await waitFor(() => {
      expect(startSyncEngine).toHaveBeenCalled()
    })
  })

  test('local data present: starts offline without the overlay and never calls /sync/full', async () => {
    let fullSyncRequested = false
    server.use(
      http.get('/api/sync/full', () => {
        fullSyncRequested = true
        return HttpResponse.json(emptyFullSync)
      }),
    )

    await localDb.tasks.put({
      ...mockTask,
      _syncStatus: 'synced',
      _localUpdatedAt: '2026-01-01T00:00:00.000Z',
    } as Parameters<typeof localDb.tasks.put>[0])

    render(<AppLayout />)

    await waitFor(() => {
      expect(startSyncEngine).toHaveBeenCalled()
    })
    expect(fullSyncRequested).toBe(false)
    expect(screen.queryByText('Syncing your tasks…')).not.toBeInTheDocument()
    expect(screen.queryByText(/Couldn't load your data/)).not.toBeInTheDocument()
  })
})

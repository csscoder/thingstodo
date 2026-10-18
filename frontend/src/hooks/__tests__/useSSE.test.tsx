import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook, act } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { useSSE } from '../useSSE'
import * as useSSEModule from '../useSSE'
import { localDb } from '../../db/index'
import { syncNow } from '../../sync/engine'

// nextBackoffMs does not exist yet in the RED phase; resolve it through the
// module namespace so the other tests still report individual failures.
const nextBackoffMs = useSSEModule.nextBackoffMs as (attempt: number) => number

// Mock sync engine to avoid network calls in tests
vi.mock('../../sync/engine', () => ({
  syncNow: vi.fn().mockResolvedValue(undefined),
}))

const syncNowMock = vi.mocked(syncNow)

// jsdom has no EventSource — install a fake the tests can drive.
class FakeEventSource {
  static instances: FakeEventSource[] = []
  url: string
  onopen: (() => void) | null = null
  onerror: (() => void) | null = null
  closed = false
  private listeners = new Map<string, Set<(e: { data: string }) => void>>()

  constructor(url: string) {
    this.url = url
    FakeEventSource.instances.push(this)
  }

  addEventListener(type: string, listener: (e: { data: string }) => void): void {
    if (!this.listeners.has(type)) this.listeners.set(type, new Set())
    this.listeners.get(type)!.add(listener)
  }

  close(): void {
    this.closed = true
  }

  emit(type: string, payload: unknown): void {
    const listeners = this.listeners.get(type)
    if (!listeners) return
    for (const listener of listeners) {
      listener({ data: JSON.stringify(payload) })
    }
  }

  simulateOpen(): void {
    this.onopen?.()
  }

  simulateError(): void {
    this.onerror?.()
  }
}

let queryClient: QueryClient

function renderSSE() {
  function Wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  }
  return renderHook(() => useSSE(), { wrapper: Wrapper })
}

async function settle(ms = 20) {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, ms))
  })
}

beforeEach(async () => {
  FakeEventSource.instances.length = 0
  vi.stubGlobal('EventSource', FakeEventSource)
  queryClient = new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0 },
      mutations: { retry: false },
    },
  })
  await Promise.all(localDb.tables.map((t) => t.clear()))
  vi.clearAllMocks()
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('useSSE reconnect', () => {
  it('reconnects after an error with backoff and resets the attempt after open', () => {
    vi.useFakeTimers()
    try {
      renderSSE()
      expect(FakeEventSource.instances).toHaveLength(1)

      // First error: reconnect after 1000 ms
      act(() => FakeEventSource.instances[0].simulateError())
      expect(FakeEventSource.instances[0].closed).toBe(true)
      expect(FakeEventSource.instances).toHaveLength(1)
      act(() => vi.advanceTimersByTime(999))
      expect(FakeEventSource.instances).toHaveLength(1)
      act(() => vi.advanceTimersByTime(1))
      expect(FakeEventSource.instances).toHaveLength(2)

      // Second consecutive error: back off to 2000 ms
      act(() => FakeEventSource.instances[1].simulateError())
      act(() => vi.advanceTimersByTime(1999))
      expect(FakeEventSource.instances).toHaveLength(2)
      act(() => vi.advanceTimersByTime(1))
      expect(FakeEventSource.instances).toHaveLength(3)

      // Successful open resets the attempt: next error waits 1000 ms again
      act(() => FakeEventSource.instances[2].simulateOpen())
      act(() => FakeEventSource.instances[2].simulateError())
      act(() => vi.advanceTimersByTime(999))
      expect(FakeEventSource.instances).toHaveLength(3)
      act(() => vi.advanceTimersByTime(1))
      expect(FakeEventSource.instances).toHaveLength(4)
      expect(FakeEventSource.instances[3].closed).toBe(false)
    } finally {
      vi.useRealTimers()
    }
  })

  it('does not leak a pending reconnect timer after unmount', () => {
    vi.useFakeTimers()
    try {
      renderSSE()
      act(() => FakeEventSource.instances[0].simulateError())
      act(() => vi.advanceTimersByTime(1000))
      expect(FakeEventSource.instances).toHaveLength(2)
      act(() => FakeEventSource.instances[1].simulateError())
    } finally {
      vi.useRealTimers()
    }
    // Unmount (RTL cleanup) closes the source and clears the timer; advancing
    // real time must not create anything new.
    expect(FakeEventSource.instances).toHaveLength(2)
  })
})

describe('useSSE task_purged', () => {
  it('removes the local task and drops queued changes for it', async () => {
    const now = new Date().toISOString()
    await localDb.tasks.put({
      id: 'purge1',
      title: 'Purge me',
      status: 'open',
      _syncStatus: 'synced',
      _localUpdatedAt: now,
    } as Parameters<typeof localDb.tasks.put>[0])
    await localDb.syncQueue.put({
      entity: 'task',
      entityId: 'purge1',
      action: 'update',
      data: { title: 'Purge me' },
      clientUpdatedAt: now,
      createdAt: now,
    })

    renderSSE()
    act(() => FakeEventSource.instances[0].emit('task_purged', { entity: 'task', id: 'purge1', seq: 7 }))
    await settle()

    expect(await localDb.tasks.get('purge1')).toBeUndefined()
    expect(await localDb.syncQueue.where('entityId').equals('purge1').count()).toBe(0)
    expect(syncNowMock).toHaveBeenCalled()
  })
})

describe('useSSE own-seq filter', () => {
  it('ignores events at or below the local cursor', async () => {
    await localDb.syncMeta.put({ key: 'cursor', value: 10 })
    const invalidateSpy = vi.spyOn(queryClient, 'invalidateQueries')

    renderSSE()
    const source = FakeEventSource.instances[0]

    // seq == cursor: already applied locally — ignore entirely
    act(() => source.emit('task_updated', { entity: 'task', id: 't1', seq: 10 }))
    await settle()
    expect(invalidateSpy).not.toHaveBeenCalled()
    expect(syncNowMock).not.toHaveBeenCalled()

    // seq above the cursor passes through
    act(() => source.emit('task_updated', { entity: 'task', id: 't1', seq: 11 }))
    await settle()
    expect(invalidateSpy).toHaveBeenCalled()
    expect(syncNowMock).toHaveBeenCalled()

    // events without a seq pass through
    invalidateSpy.mockClear()
    syncNowMock.mockClear()
    act(() => source.emit('tag_updated', {}))
    await settle()
    expect(syncNowMock).toHaveBeenCalled()
  })
})

describe('nextBackoffMs', () => {
  it('grows exponentially and caps at 30 s', () => {
    expect(nextBackoffMs(0)).toBe(1000)
    expect(nextBackoffMs(1)).toBe(2000)
    expect(nextBackoffMs(2)).toBe(4000)
    expect(nextBackoffMs(5)).toBe(30000)
    expect(nextBackoffMs(10)).toBe(30000)
    expect(nextBackoffMs(100)).toBe(30000)
  })
})

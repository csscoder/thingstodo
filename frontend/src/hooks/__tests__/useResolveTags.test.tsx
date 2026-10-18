import { describe, it, expect, vi, beforeEach } from 'vitest'
import { renderHook, act } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { useResolveTags } from '../useResolveTags'
import { localDb } from '../../db'
import type { LocalTag } from '../../db/schema'

// Mock audio to avoid AudioContext errors in jsdom
vi.mock('../../lib/sounds', () => ({
  playCompleteSound: vi.fn(),
  playReviewSound: vi.fn(),
}))

// Mock sync engine to avoid network calls in tests
vi.mock('../../sync/engine', () => ({
  syncNow: vi.fn().mockResolvedValue(undefined),
  stopSyncEngine: vi.fn(),
}))

function createWrapper() {
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0 },
      mutations: { retry: false },
    },
  })

  function Wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  }

  return { Wrapper }
}

function makeTag(overrides: Partial<LocalTag> = {}): LocalTag {
  return {
    id: 'tag-existing',
    title: 'Покупки',
    color: null,
    parent_tag_id: null,
    sort_order: 1024,
    task_count: 0,
    _syncStatus: 'synced',
    _localUpdatedAt: '2026-01-01T00:00:00Z',
    ...overrides,
  }
}

async function renderSettledResolver() {
  const { Wrapper } = createWrapper()
  const rendered = renderHook(() => useResolveTags(), { wrapper: Wrapper })
  // Let the Dexie live queries (tags/projects/areas) settle so the
  // resolve callback sees the seeded rows.
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 20))
  })
  return rendered
}

describe('useResolveTags', () => {
  beforeEach(async () => {
    vi.clearAllMocks()
    await localDb.tags.clear()
    await localDb.syncQueue.clear()
  })

  it('auto-creates a Unicode tag once', async () => {
    const { result } = await renderSettledResolver()

    let resolved: Awaited<ReturnType<typeof result.current>> | undefined
    await act(async () => {
      resolved = await result.current('Купить молоко #покупки')
    })

    expect(resolved?.title).toBe('Купить молоко')
    expect(resolved?.tagIds).toHaveLength(1)

    const tags = await localDb.tags.toArray()
    expect(tags).toHaveLength(1)
    expect(tags[0].title).toBe('покупки')
    expect(tags[0].id).toBe(resolved?.tagIds[0])

    // Created exactly once: one pending create in the sync queue
    const queue = await localDb.syncQueue.toArray()
    const tagCreates = queue.filter((e) => e.entity === 'tag' && e.action === 'create')
    expect(tagCreates).toHaveLength(1)
  })

  it('reuses an existing tag case-insensitively', async () => {
    await localDb.tags.put(makeTag({ id: 'tag-existing', title: 'Покупки' }))
    const { result } = await renderSettledResolver()

    let resolved: Awaited<ReturnType<typeof result.current>> | undefined
    await act(async () => {
      resolved = await result.current('Купить молоко #покупки')
    })

    expect(resolved?.tagIds).toEqual(['tag-existing'])

    // No new row and no sync entry
    const tags = await localDb.tags.toArray()
    expect(tags).toHaveLength(1)
    const queue = await localDb.syncQueue.toArray()
    expect(queue).toHaveLength(0)
  })
})

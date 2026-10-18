process.env.TZ = 'UTC'

// Imports are hoisted above the TZ pin, but neither the module under test nor
// any of its imports do date math at module scope, so the pin lands before the
// first Date call inside a test body. It only affects the *browser zone*
// fallback (getOwnerTimeZone), which must differ from Europe/Moscow.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook, act } from '@testing-library/react'
import {
  setOwnerTimeZone,
  getOwnerTimeZone,
  ownerToday,
  ownerDateOf,
  addDays,
  ownerDayBoundsUtc,
  ownerLocalInputToIso,
  isoToOwnerLocalInput,
  loadOwnerTimeZone,
  useOwnerToday,
} from '../owner-time'
import { api } from '../../api/client'

vi.mock('../../api/client', () => ({
  api: { get: vi.fn() },
}))

const mockGet = vi.mocked(api.get)

afterEach(() => {
  vi.useRealTimers()
  vi.clearAllMocks()
  localStorage.clear()
})

// These fallback tests must run before any setOwnerTimeZone call: the module
// cache can only be primed by setOwnerTimeZone and has no reset hook.
describe('getOwnerTimeZone', () => {
  it('falls back to the browser zone when nothing is cached', () => {
    expect(getOwnerTimeZone()).toBe('UTC')
  })

  it('prefers the localStorage value over the browser zone', () => {
    localStorage.setItem('thingstodo_timezone', 'Europe/Berlin')
    expect(getOwnerTimeZone()).toBe('Europe/Berlin')
  })
})

describe('setOwnerTimeZone', () => {
  it('caches the zone in memory and localStorage', () => {
    setOwnerTimeZone('Europe/Moscow')
    expect(getOwnerTimeZone()).toBe('Europe/Moscow')
    expect(localStorage.getItem('thingstodo_timezone')).toBe('Europe/Moscow')
  })
})

describe('owner-zone date math (owner Europe/Moscow, browser TZ pinned to UTC)', () => {
  beforeEach(() => {
    setOwnerTimeZone('Europe/Moscow')
  })

  it('ownerToday uses the owner zone, not the browser zone', () => {
    // 2026-10-18 21:30 UTC is already 2026-10-19 00:30 in Moscow.
    expect(ownerToday(new Date('2026-10-18T21:30:00Z'))).toBe('2026-10-19')
  })

  it("ownerDateOf('2026-10-18 21:30:00') === '2026-10-19'", () => {
    expect(ownerDateOf('2026-10-18 21:30:00')).toBe('2026-10-19')
  })

  it('ownerDateOf also accepts RFC3339 timestamps', () => {
    expect(ownerDateOf('2026-10-18T18:30:00Z')).toBe('2026-10-18')
    expect(ownerDateOf('2026-10-18T21:30:00+00:00')).toBe('2026-10-19')
  })

  it('addDays crosses month and year boundaries', () => {
    expect(addDays('2026-02-28', 1)).toBe('2026-03-01')
    expect(addDays('2026-03-01', -1)).toBe('2026-02-28')
    expect(addDays('2028-02-28', 1)).toBe('2028-02-29')
    expect(addDays('2026-12-31', 1)).toBe('2027-01-01')
  })

  it('ownerDayBoundsUtc returns UTC bounds of the owner-zone day', () => {
    expect(ownerDayBoundsUtc('2026-10-19')).toEqual({
      start: '2026-10-18T21:00:00Z',
      end: '2026-10-19T21:00:00Z',
    })
  })

  it('ownerDayBoundsUtc spans a DST transition correctly', () => {
    // US DST ends 2026-11-01 02:00 -> 01:00, so that day is 25 hours long.
    setOwnerTimeZone('America/New_York')
    expect(ownerDayBoundsUtc('2026-11-01')).toEqual({
      start: '2026-11-01T04:00:00Z',
      end: '2026-11-02T05:00:00Z',
    })
  })

  it("ownerLocalInputToIso('2026-10-18T21:30') === '2026-10-18T21:30:00+03:00'", () => {
    expect(ownerLocalInputToIso('2026-10-18T21:30')).toBe('2026-10-18T21:30:00+03:00')
  })

  it('ownerLocalInputToIso picks the offset in effect at that wall time', () => {
    setOwnerTimeZone('America/New_York')
    expect(ownerLocalInputToIso('2026-10-31T12:00')).toBe('2026-10-31T12:00:00-04:00')
    expect(ownerLocalInputToIso('2026-11-01T12:00')).toBe('2026-11-01T12:00:00-05:00')
  })

  it("isoToOwnerLocalInput('2026-10-18T18:30:00Z') === '2026-10-18T21:30'", () => {
    expect(isoToOwnerLocalInput('2026-10-18T18:30:00Z')).toBe('2026-10-18T21:30')
  })
})

describe('loadOwnerTimeZone', () => {
  it('fetches /api/auth/config and caches the reported timezone', async () => {
    mockGet.mockResolvedValueOnce({ auth_mode: 'jwt', timezone: 'Europe/Moscow' })
    await loadOwnerTimeZone()
    expect(mockGet).toHaveBeenCalledWith('/auth/config')
    expect(localStorage.getItem('thingstodo_timezone')).toBe('Europe/Moscow')
  })

  it('is a no-op when the config has no timezone field', async () => {
    mockGet.mockResolvedValueOnce({ auth_mode: 'none' })
    await loadOwnerTimeZone()
    expect(localStorage.getItem('thingstodo_timezone')).toBeNull()
  })

  it('never throws when the request fails', async () => {
    mockGet.mockRejectedValueOnce(new Error('offline'))
    await expect(loadOwnerTimeZone()).resolves.toBeUndefined()
    expect(localStorage.getItem('thingstodo_timezone')).toBeNull()
  })
})

describe('useOwnerToday', () => {
  it('re-renders at owner midnight', () => {
    vi.useFakeTimers()
    setOwnerTimeZone('Europe/Moscow')
    // One second before Moscow midnight (21:00 UTC).
    vi.setSystemTime(new Date('2026-10-18T20:59:59Z'))
    const { result } = renderHook(() => useOwnerToday())
    expect(result.current).toBe('2026-10-18')
    act(() => {
      vi.advanceTimersByTime(2000)
    })
    expect(result.current).toBe('2026-10-19')
  })
})

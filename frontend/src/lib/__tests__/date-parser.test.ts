process.env.TZ = 'UTC'

// Imports are hoisted above the TZ pin, but no module-scope date math runs
// before a test body, so the pin lands first (convention from owner-time.test.ts).
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { parseNaturalDate } from '../date-parser'
import { setOwnerTimeZone } from '../owner-time'
import { format, addDays, addMonths, startOfMonth, nextMonday, nextFriday } from 'date-fns'

function fmt(d: Date) {
  return format(d, 'yyyy-MM-dd')
}

describe('parseNaturalDate', () => {
  beforeEach(() => {
    // Fix "today" to 2026-02-15 (a Sunday) at 09:00, not midnight.
    vi.useFakeTimers()
    vi.setSystemTime(new Date(2026, 1, 15, 9, 0, 0))
    setOwnerTimeZone('Europe/Moscow')
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  const today = new Date(2026, 1, 15)

  it('returns null for empty input', () => {
    expect(parseNaturalDate('')).toBeNull()
    expect(parseNaturalDate('  ')).toBeNull()
  })

  it('returns null for unrecognized input', () => {
    expect(parseNaturalDate('gobbledygook')).toBeNull()
    expect(parseNaturalDate('asdf1234')).toBeNull()
  })

  it('parses "today"', () => {
    expect(parseNaturalDate('today')).toEqual({
      date: fmt(today),
    })
  })

  it('parses "tomorrow"', () => {
    expect(parseNaturalDate('tomorrow')).toEqual({
      date: fmt(addDays(today, 1)),
    })
  })

  it('parses "this evening" and "tonight" as today', () => {
    const expected = { date: fmt(today) }
    expect(parseNaturalDate('this evening')).toEqual(expected)
    expect(parseNaturalDate('tonight')).toEqual(expected)
  })

  it('parses "next week" as next Monday', () => {
    expect(parseNaturalDate('next week')).toEqual({
      date: fmt(nextMonday(today)),
    })
  })

  it('parses "next month" as 1st of next month', () => {
    expect(parseNaturalDate('next month')).toEqual({
      date: fmt(startOfMonth(addMonths(today, 1))),
    })
  })

  it('parses "in N days"', () => {
    expect(parseNaturalDate('in 3 days')).toEqual({
      date: fmt(addDays(today, 3)),
    })
    expect(parseNaturalDate('in 1 day')).toEqual({
      date: fmt(addDays(today, 1)),
    })
  })

  it('parses "in N weeks"', () => {
    expect(parseNaturalDate('in 2 weeks')).toEqual({
      date: fmt(addDays(today, 14)),
    })
  })

  it('parses "in N months"', () => {
    expect(parseNaturalDate('in 3 months')).toEqual({
      date: fmt(addMonths(today, 3)),
    })
  })

  it('parses day names', () => {
    expect(parseNaturalDate('friday')).toEqual({
      date: fmt(nextFriday(today)),
    })
    expect(parseNaturalDate('fri')).toEqual({
      date: fmt(nextFriday(today)),
    })
    expect(parseNaturalDate('monday')).toEqual({
      date: fmt(nextMonday(today)),
    })
  })

  it('parses "month day" pattern', () => {
    // March 5 is in the future from Feb 15
    expect(parseNaturalDate('mar 5')).toEqual({
      date: '2026-03-05',
    })
  })

  it('rolls "month day" to next year if in the past', () => {
    // Jan 10 has already passed in 2026
    expect(parseNaturalDate('jan 10')).toEqual({
      date: '2027-01-10',
    })
  })

  it('parses ISO date format', () => {
    expect(parseNaturalDate('2026-06-01')).toEqual({
      date: '2026-06-01',
    })
  })

  it('is case-insensitive', () => {
    expect(parseNaturalDate('TODAY')).toEqual({
      date: fmt(today),
    })
    expect(parseNaturalDate('Tomorrow')).toEqual({
      date: fmt(addDays(today, 1)),
    })
  })
})

describe('parseNaturalDate today and overflow (owner Europe/Moscow, browser TZ UTC)', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    setOwnerTimeZone('Europe/Moscow')
  })

  afterEach(() => {
    vi.useRealTimers()
    localStorage.clear()
  })

  // 09:00, 15:00 and 23:30 owner time on 2026-10-16 in Moscow (UTC+3)
  // are 06:00Z, 12:00Z and 20:30Z.
  for (const utcTime of ['T06:00:00Z', 'T12:00:00Z', 'T20:30:00Z']) {
    it(`compares by owner date and rejects impossible days at ${utcTime}`, () => {
      vi.setSystemTime(new Date(`2026-10-16${utcTime}`))
      expect(parseNaturalDate('oct 16')).toEqual({ date: '2026-10-16' })
      expect(parseNaturalDate('oct 15')).toEqual({ date: '2027-10-15' })
      expect(parseNaturalDate('feb 31')).toBeNull()
      expect(parseNaturalDate('2/30')).toBeNull()
    })
  }
})

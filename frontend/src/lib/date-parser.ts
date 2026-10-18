import {
  addDays,
  addMonths,
  nextMonday,
  nextFriday,
  nextThursday,
  nextWednesday,
  nextTuesday,
  nextSaturday,
  nextSunday,
  format,
  startOfMonth,
  parse,
  isValid,
  addYears,
} from 'date-fns'
import { ownerToday } from './owner-time'

export interface ParsedDate {
  date: string // ISO date string YYYY-MM-DD
}

const DAY_FINDERS: Record<string, (d: Date) => Date> = {
  monday: nextMonday,
  mon: nextMonday,
  tuesday: nextTuesday,
  tue: nextTuesday,
  wednesday: nextWednesday,
  wed: nextWednesday,
  thursday: nextThursday,
  thu: nextThursday,
  friday: nextFriday,
  fri: nextFriday,
  saturday: nextSaturday,
  sat: nextSaturday,
  sunday: nextSunday,
  sun: nextSunday,
}

const MONTH_NAMES: Record<string, number> = {
  jan: 0, january: 0,
  feb: 1, february: 1,
  mar: 2, march: 2,
  apr: 3, april: 3,
  may: 4,
  jun: 5, june: 5,
  jul: 6, july: 6,
  aug: 7, august: 7,
  sep: 8, september: 8,
  oct: 9, october: 9,
  nov: 10, november: 10,
  dec: 11, december: 11,
}

function formatDate(d: Date): string {
  return format(d, 'yyyy-MM-dd')
}

export function parseNaturalDate(input: string): ParsedDate | null {
  const text = input.trim().toLowerCase()
  if (!text) return null

  // Anchor on the owner-zone date string, parsed to a local-midnight Date,
  // so every date below is pure calendar math in the owner's "today".
  const [year, month, day] = ownerToday().split('-').map(Number)
  const today = new Date(year, month - 1, day)

  // "today"
  if (text === 'today' || text === 'this evening' || text === 'tonight') {
    return { date: formatDate(today) }
  }

  // "tomorrow"
  if (text === 'tomorrow') {
    return { date: formatDate(addDays(today, 1)) }
  }

  // "next week"
  if (text === 'next week') {
    return { date: formatDate(nextMonday(today)) }
  }

  // "next month"
  if (text === 'next month') {
    return { date: formatDate(startOfMonth(addMonths(today, 1))) }
  }

  // "in N days/weeks/months"
  const inMatch = text.match(/^in\s+(\d+)\s+(day|days|week|weeks|month|months)$/)
  if (inMatch) {
    const n = parseInt(inMatch[1], 10)
    const unit = inMatch[2]
    if (unit.startsWith('day')) {
      return { date: formatDate(addDays(today, n)) }
    }
    if (unit.startsWith('week')) {
      return { date: formatDate(addDays(today, n * 7)) }
    }
    if (unit.startsWith('month')) {
      return { date: formatDate(addMonths(today, n)) }
    }
  }

  // Day name: "friday", "mon", etc.
  const dayFinder = DAY_FINDERS[text]
  if (dayFinder) {
    return { date: formatDate(dayFinder(today)) }
  }

  // "month day" pattern: "jan 15", "december 3"
  const monthDayMatch = text.match(/^([a-z]+)\s+(\d{1,2})$/)
  if (monthDayMatch) {
    const monthNum = MONTH_NAMES[monthDayMatch[1]]
    if (monthNum !== undefined) {
      const day = parseInt(monthDayMatch[2], 10)
      let candidate = new Date(today.getFullYear(), monthNum, day)
      // Reject impossible days ("feb 31") instead of letting them roll over.
      if (candidate.getMonth() !== monthNum || candidate.getDate() !== day) return null
      // If the date has passed this year, use next year
      if (formatDate(candidate) < ownerToday()) {
        candidate = addYears(candidate, 1)
      }
      return { date: formatDate(candidate) }
    }
  }

  // Try parsing as a date string directly (YYYY-MM-DD, MM/DD, etc.)
  for (const fmt of ['yyyy-MM-dd', 'MM/dd', 'M/d']) {
    const parsed = parse(text, fmt, today)
    // Strict round-trip: a rolled-over date ("2/30" -> Mar 2) formats back
    // differently, so impossible days are rejected.
    if (isValid(parsed) && format(parsed, fmt) === text) {
      // For short formats without year, advance if in the past
      if (!text.includes('-') && formatDate(parsed) < ownerToday()) {
        return { date: formatDate(addYears(parsed, 1)) }
      }
      return { date: formatDate(parsed) }
    }
  }

  return null
}

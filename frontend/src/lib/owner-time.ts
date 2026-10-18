import { useEffect, useState } from 'react'
import { api } from '../api/client'

/**
 * Owner-timezone date helpers.
 *
 * Every date the UI shows or sends is computed in the *owner* timezone — the
 * server's TZ, exposed as `timezone` in GET /api/auth/config — so "today"
 * always matches what the server's views call today, whatever the browser's
 * local zone is. All zone math goes through Intl.DateTimeFormat#formatToParts;
 * no date library. The module never reads process.env and never uses the
 * local zone except as the final fallback in getOwnerTimeZone.
 */

const TIMEZONE_KEY = 'thingstodo_timezone'

/** Zone primed by setOwnerTimeZone this session; null until then. */
let cachedTimeZone: string | null = null

export function setOwnerTimeZone(tz: string): void {
  cachedTimeZone = tz
  try {
    localStorage.setItem(TIMEZONE_KEY, tz)
  } catch {
    // Storage unavailable (private mode): the in-memory cache still applies.
  }
}

export function getOwnerTimeZone(): string {
  if (cachedTimeZone) return cachedTimeZone
  try {
    const stored = localStorage.getItem(TIMEZONE_KEY)
    if (stored) return stored
  } catch {
    // Storage unavailable: fall through to the browser zone.
  }
  return Intl.DateTimeFormat().resolvedOptions().timeZone
}

interface WallTime {
  year: number
  month: number
  day: number
  hour: number
  minute: number
  second: number
}

const formatterCache = new Map<string, Intl.DateTimeFormat>()

function zoneFormatter(tz: string): Intl.DateTimeFormat {
  let fmt = formatterCache.get(tz)
  if (!fmt) {
    fmt = new Intl.DateTimeFormat('en-US', {
      timeZone: tz,
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
      hour12: false,
    })
    formatterCache.set(tz, fmt)
  }
  return fmt
}

/** Wall-clock reading of an instant in tz. */
function wallTimeInZone(ms: number, tz: string): WallTime {
  const parts = zoneFormatter(tz).formatToParts(new Date(ms))
  const byType: Record<string, string> = {}
  for (const part of parts) byType[part.type] = part.value
  return {
    year: Number(byType.year),
    month: Number(byType.month),
    day: Number(byType.day),
    // Some ICU builds render midnight as hour '24' with hour12: false.
    hour: Number(byType.hour) % 24,
    minute: Number(byType.minute),
    second: Number(byType.second),
  }
}

/** Offset of tz at an instant: wall time minus UTC, in milliseconds. */
function zoneOffsetMs(ms: number, tz: string): number {
  const wall = wallTimeInZone(ms, tz)
  return (
    Date.UTC(wall.year, wall.month - 1, wall.day, wall.hour, wall.minute, wall.second) - ms
  )
}

/**
 * UTC instant of a wall-clock time in tz.
 *
 * DST probing by fixed-point iteration: guess the offset, re-evaluate it at
 * the guessed instant, and repeat until the instant stops moving (at most a
 * few rounds — real IANA zones converge in <= 3 because a day's offset only
 * changes once and never by 24h). Days whose two midnights sit on different
 * sides of a DST shift (ownerDayBoundsUtc probes each midnight separately)
 * and datetime-local inputs near a shift (ownerLocalInputToIso) therefore
 * pick up the correct offset. Skipped or ambiguous wall times converge to
 * one of the two bracketing instants.
 */
function wallTimeToUtcMs(wall: WallTime, tz: string): number {
  const wallAsUtc = Date.UTC(
    wall.year,
    wall.month - 1,
    wall.day,
    wall.hour,
    wall.minute,
    wall.second,
  )
  let utc = wallAsUtc - zoneOffsetMs(wallAsUtc, tz)
  for (let i = 0; i < 3; i++) {
    const next = wallAsUtc - zoneOffsetMs(utc, tz)
    if (next === utc) break
    utc = next
  }
  return utc
}

function pad2(n: number): string {
  return String(n).padStart(2, '0')
}

function wallDate(wall: WallTime): string {
  return `${String(wall.year).padStart(4, '0')}-${pad2(wall.month)}-${pad2(wall.day)}`
}

function midnightWall(date: string): WallTime {
  const [year, month, day] = date.split('-').map(Number)
  return { year, month, day, hour: 0, minute: 0, second: 0 }
}

function utcMsToRfc3339(ms: number): string {
  return `${new Date(ms).toISOString().slice(0, 19)}Z`
}

/** Parse 'YYYY-MM-DD HH:MM:SS' (SQLite UTC, space form) or RFC3339. */
function parseUtcMs(isoUtc: string): number {
  const sqlite = isoUtc.match(/^(\d{4})-(\d{2})-(\d{2}) (\d{2}):(\d{2}):(\d{2})$/)
  return new Date(
    sqlite
      ? `${sqlite[1]}-${sqlite[2]}-${sqlite[3]}T${sqlite[4]}:${sqlite[5]}:${sqlite[6]}Z`
      : isoUtc,
  ).getTime()
}

/** YYYY-MM-DD of an instant in the owner zone. */
function ownerDateOfInstant(ms: number): string {
  return wallDate(wallTimeInZone(ms, getOwnerTimeZone()))
}

export function ownerToday(now: Date = new Date()): string {
  return ownerDateOfInstant(now.getTime())
}

/** Owner-zone date of a UTC timestamp. Unparseable input is returned as-is. */
export function ownerDateOf(isoUtc: string): string {
  const ms = parseUtcMs(isoUtc)
  return Number.isNaN(ms) ? isoUtc : ownerDateOfInstant(ms)
}

/** Pure date-string arithmetic (UTC), independent of any timezone. */
export function addDays(date: string, n: number): string {
  const [year, month, day] = date.split('-').map(Number)
  return new Date(Date.UTC(year, month - 1, day + n)).toISOString().slice(0, 10)
}

/** RFC3339 UTC bounds [start, end) of one owner-zone calendar day. */
export function ownerDayBoundsUtc(date: string): { start: string; end: string } {
  const tz = getOwnerTimeZone()
  return {
    start: utcMsToRfc3339(wallTimeToUtcMs(midnightWall(date), tz)),
    end: utcMsToRfc3339(wallTimeToUtcMs(midnightWall(addDays(date, 1)), tz)),
  }
}

function formatOffset(offsetMs: number): string {
  const sign = offsetMs < 0 ? '-' : '+'
  const totalMinutes = Math.round(Math.abs(offsetMs) / 60000)
  return `${sign}${pad2(Math.floor(totalMinutes / 60))}:${pad2(totalMinutes % 60)}`
}

/**
 * 'YYYY-MM-DDTHH:mm' (datetime-local value) read in the owner zone ->
 * RFC3339 with the zone's numeric offset at that wall time. Malformed or
 * empty input is returned as-is so cleared form fields stay empty.
 */
export function ownerLocalInputToIso(local: string): string {
  const m = local.match(/^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})$/)
  if (!m) return local
  const tz = getOwnerTimeZone()
  const utc = wallTimeToUtcMs(
    {
      year: Number(m[1]),
      month: Number(m[2]),
      day: Number(m[3]),
      hour: Number(m[4]),
      minute: Number(m[5]),
      second: 0,
    },
    tz,
  )
  return `${local}:00${formatOffset(zoneOffsetMs(utc, tz))}`
}

/** Inverse of ownerLocalInputToIso; accepts SQLite or RFC3339 timestamps. */
export function isoToOwnerLocalInput(iso: string): string {
  const ms = parseUtcMs(iso)
  if (Number.isNaN(ms)) return iso
  const wall = wallTimeInZone(ms, getOwnerTimeZone())
  return `${wallDate(wall)}T${pad2(wall.hour)}:${pad2(wall.minute)}`
}

interface AuthConfig {
  timezone?: string
}

/** Adopt the server's timezone; any failure is silently ignored. */
export async function loadOwnerTimeZone(): Promise<void> {
  try {
    const config = await api.get<AuthConfig>('/auth/config')
    if (typeof config?.timezone === 'string' && config.timezone) {
      setOwnerTimeZone(config.timezone)
    }
  } catch {
    // Offline, or a server that does not report a timezone yet: keep the
    // cached / localStorage / browser zone from getOwnerTimeZone().
  }
}

/**
 * Today's date in the owner zone. Re-renders when the owner-zone date
 * changes: a timeout armed for just past the next owner midnight fires,
 * recomputes, and re-arms itself. Cleared on unmount and re-armed whenever
 * the owner zone changes.
 */
export function useOwnerToday(): string {
  const [today, setToday] = useState(() => ownerToday())
  const zone = getOwnerTimeZone()

  useEffect(() => {
    const schedule = () => {
      const now = new Date()
      const { end } = ownerDayBoundsUtc(ownerToday(now))
      // +50ms buffer so the owner wall clock has certainly rolled over.
      const delay = Math.max(0, Date.parse(end) - now.getTime() + 50)
      return setTimeout(() => {
        setToday(ownerToday())
        schedule()
      }, delay)
    }
    const timer = schedule()
    return () => clearTimeout(timer)
  }, [zone])

  return today
}

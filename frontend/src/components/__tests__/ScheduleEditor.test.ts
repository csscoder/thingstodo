import { describe, it, expect } from 'vitest'
import { defaultEndTime } from '../ScheduleEditor'

describe('defaultEndTime', () => {
  it('returns null when the default gap would pass 23:59', () => {
    expect(defaultEndTime('23:30', 60)).toBeNull()
  })

  it('returns the end time when it fits in the same day', () => {
    expect(defaultEndTime('22:30', 60)).toBe('23:30')
  })

  it('returns 23:59 when the gap lands on the last minute of the day', () => {
    expect(defaultEndTime('23:00', 59)).toBe('23:59')
  })
})

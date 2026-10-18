import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import { TaskDetail } from '../TaskDetail'
import { render } from '../../test/test-utils'
import { localDb } from '../../db'
import type { LocalReminder, LocalTask } from '../../db/schema'
import { setOwnerTimeZone } from '../../lib/owner-time'

// Keep sync traffic out of the test; assert on the local queue instead
vi.mock('../../sync/engine', () => ({
  syncNow: vi.fn().mockResolvedValue(undefined),
}))

function makeTask(overrides: Partial<LocalTask> = {}): LocalTask {
  const now = new Date().toISOString()
  return {
    id: 'task-1',
    title: 'Test task',
    status: 'open',
    notes: '',
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

describe('TaskDetail exact reminders (owner timezone)', () => {
  beforeEach(async () => {
    await Promise.all(localDb.tables.map((t) => t.clear()))
    setOwnerTimeZone('Europe/Moscow')
    await localDb.tasks.put(makeTask({ id: 'task-1' }))
  })

  afterEach(() => {
    // Reset the owner zone to the browser zone for any later test in this file
    setOwnerTimeZone(Intl.DateTimeFormat().resolvedOptions().timeZone)
    localStorage.removeItem('thingstodo_timezone')
  })

  it('exact reminder is sent with the owner offset', async () => {
    const { user } = render(<TaskDetail taskId="task-1" isModal />)

    await user.click(await screen.findByRole('button', { name: 'Add reminder' }))
    await user.selectOptions(await screen.findByRole('combobox'), 'exact')

    const input = document.querySelector('input[type=datetime-local]')
    expect(input).not.toBeNull()
    fireEvent.change(input!, { target: { value: '2026-10-20T09:00' } })

    await user.click(screen.getByRole('button', { name: 'Save' }))

    // 09:00 Moscow wall time carries the +03:00 offset at that instant
    await waitFor(async () => {
      const queue = await localDb.syncQueue.toArray()
      const create = queue.find((e) => e.entity === 'reminder' && e.action === 'create')
      expect(create?.data.exact_at).toBe('2026-10-20T09:00:00+03:00')
    })
  })

  it('prefills a stored UTC value in owner time', async () => {
    await localDb.reminders.put({
      id: 'rem-1',
      task_id: 'task-1',
      type: 'exact',
      value: 0,
      exact_at: '2026-10-20T06:00:00Z',
      created_at: '2026-01-01T00:00:00.000Z',
      _syncStatus: 'synced',
      _localUpdatedAt: '2026-01-01T00:00:00.000Z',
    } as LocalReminder)

    const { user } = render(<TaskDetail taskId="task-1" isModal />)

    // Open the edit row via the reminder's label button
    const removeButton = await screen.findByRole('button', { name: 'Remove reminder' })
    const row = removeButton.closest('div')!
    const labelButton = within(row)
      .getAllByRole('button')
      .find((b) => b !== removeButton)!
    await user.click(labelButton)

    // 06:00Z is 09:00 in Moscow — datetime-local rejects the stored Z form
    const input = document.querySelector('input[type=datetime-local]') as HTMLInputElement
    expect(input).not.toBeNull()
    expect(input.value).toBe('2026-10-20T09:00')
  })
})

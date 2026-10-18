import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '../../test/test-utils'
import { QuickEntry } from '../QuickEntry'
import { useAppStore } from '../../stores/app'
import { localDb } from '../../db'

// Mock sync engine to avoid network calls in tests
vi.mock('../../sync/engine', () => ({
  syncNow: vi.fn().mockResolvedValue(undefined),
}))

describe('QuickEntry', () => {
  beforeEach(async () => {
    useAppStore.getState().closeQuickEntry()
    await localDb.tasks.clear()
    await localDb.syncQueue.clear()
  })

  it('blocks saving when the deadline is before the when date', async () => {
    useAppStore.setState({ quickEntryOpen: true, quickEntryInitialValue: '' })
    const { user } = render(<QuickEntry />)

    await user.type(screen.getByPlaceholderText('New task...'), 'Test task')

    // Set when date to 2026-10-20
    await user.click(screen.getByRole('button', { name: 'Set when date' }))
    await user.type(screen.getByPlaceholderText('When...'), '2026-10-20{Enter}')

    // Set deadline to 2026-10-19
    await user.click(screen.getByRole('button', { name: 'Set deadline' }))
    await user.type(screen.getByPlaceholderText('Deadline...'), '2026-10-19{Enter}')

    // Submit from the title input
    await user.click(screen.getByPlaceholderText('New task...'))
    await user.keyboard('{Enter}')

    expect(await screen.findByText("Deadline can't be before the start date")).toBeInTheDocument()

    // No task may be queued or stored
    const tasks = await localDb.tasks.toArray()
    expect(tasks).toHaveLength(0)
    const queue = await localDb.syncQueue.toArray()
    expect(queue).toHaveLength(0)
  })
})

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '../../test/test-utils'
import userEvent from '@testing-library/user-event'
import { TrashView } from '../TrashView'
import { mockTask } from '../../test/mocks/data'
import { server } from '../../test/mocks/server'
import { http, HttpResponse } from 'msw'

// Use the auto-mock for all localQueries hooks
vi.mock('../../hooks/localQueries')
import { useLocalTrash } from '../../hooks/localQueries'
const mockUseLocalTrash = vi.mocked(useLocalTrash)

describe('TrashView', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockUseLocalTrash.mockReturnValue(undefined)
  })

  it('Empty Trash releases the purging state after a failure', async () => {
    server.use(
      http.delete('/api/tasks/:id/purge', () => new HttpResponse(null, { status: 500 })),
    )
    mockUseLocalTrash.mockReturnValue({
      groups: [{ date: '2026-10-17', tasks: [{ ...mockTask, deleted_at: '2026-10-17T10:00:00Z' }] }],
      total: 1,
    })

    const user = userEvent.setup()
    render(<TrashView />)

    await user.click(screen.getByRole('button', { name: 'Empty trash' }))
    await user.click(screen.getByRole('button', { name: 'Empty Trash' }))

    // purging disables the button; a failed purge must release it
    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Empty trash' })).toBeEnabled()
    })
  })
})

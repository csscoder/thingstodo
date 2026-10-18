import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '../../test/test-utils'
import { Sidebar } from '../Sidebar'
import { mockArea, mockProject, mockTag, mockViewCounts } from '../../test/mocks/data'
import { server } from '../../test/mocks/server'
import { http, HttpResponse } from 'msw'
import { localDb } from '../../db'

// Use the auto-mock for all localQueries hooks
vi.mock('../../hooks/localQueries')
import { useLocalViewCounts, useLocalAreas, useLocalProjects, useLocalTags } from '../../hooks/localQueries'

describe('Sidebar', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(useLocalViewCounts).mockReturnValue(mockViewCounts)
    vi.mocked(useLocalAreas).mockReturnValue([mockArea])
    vi.mocked(useLocalProjects).mockReturnValue([mockProject])
    vi.mocked(useLocalTags).mockReturnValue([mockTag])
  })

  it('renders all smart list nav items', async () => {
    render(<Sidebar />)

    await waitFor(() => {
      expect(screen.getByText('Inbox')).toBeInTheDocument()
      expect(screen.getByText('Today')).toBeInTheDocument()
      expect(screen.getByText('Upcoming')).toBeInTheDocument()
      expect(screen.getByText('Anytime')).toBeInTheDocument()
      expect(screen.getByText('Someday')).toBeInTheDocument()
      expect(screen.getByText('Completed')).toBeInTheDocument()
    })
  })

  it('renders the app title', () => {
    render(<Sidebar />)
    expect(screen.getByText('ThingsToDo')).toBeInTheDocument()
  })

  it('renders plus menu button', () => {
    render(<Sidebar />)
    expect(screen.getByLabelText('New item')).toBeInTheDocument()
  })

  it('renders Areas & Projects section', async () => {
    render(<Sidebar />)
    await waitFor(() => {
      expect(screen.getByText(/Areas/)).toBeInTheDocument()
    })
  })

  it('renders projects from API', async () => {
    render(<Sidebar />)
    await waitFor(() => {
      expect(screen.getByText('Website Redesign')).toBeInTheDocument()
    })
  })

  it('renders areas from API', async () => {
    render(<Sidebar />)
    await waitFor(() => {
      expect(screen.getByText('Work')).toBeInTheDocument()
    })
  })

  it('smart list items are navigation links', async () => {
    render(<Sidebar />, { routerProps: { initialEntries: ['/inbox'] } })

    await waitFor(() => {
      const inboxLink = screen.getByText('Inbox').closest('a')
      expect(inboxLink).toHaveAttribute('href', '/inbox')

      const todayLink = screen.getByText('Today').closest('a')
      expect(todayLink).toHaveAttribute('href', '/today')
    })
  })

  it('logout with a pending queue asks for confirmation first', async () => {
    await localDb.syncQueue.clear()
    await localDb.syncQueue.put({
      entity: 'task',
      entityId: 'task-1',
      action: 'update',
      data: { title: 'Unsynced edit' },
      clientUpdatedAt: '2026-01-01T00:00:00Z',
      createdAt: '2026-01-01T00:00:00Z',
    })

    let logoutCalls = 0
    server.use(
      http.get('/api/auth/me', () =>
        HttpResponse.json({ user: { id: 'user-1', username: 'testuser' }, auth_mode: 'builtin' }),
      ),
      http.delete('/api/auth/logout', () => {
        logoutCalls++
        return new HttpResponse(null, { status: 204 })
      }),
    )

    // jsdom-safe navigation stub: capture href assignments without navigating
    const originalLocation = Object.getOwnPropertyDescriptor(window, 'location')
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: { href: 'http://localhost/' },
    })

    try {
      const { user } = render(<Sidebar />)

      const logoutButton = await screen.findByRole('button', { name: 'Log out' })
      await user.click(logoutButton)

      // Confirmation shown before anything is sent or cleared
      expect(
        await screen.findByText('You have 1 unsynced change(s). Logging out will lose them.'),
      ).toBeInTheDocument()
      expect(screen.getByRole('button', { name: 'Log out anyway' })).toBeInTheDocument()
      expect(logoutCalls).toBe(0)

      await user.click(screen.getByRole('button', { name: 'Log out anyway' }))

      // Confirming proceeds with the logout and wipes local data
      await waitFor(() => expect(logoutCalls).toBe(1))
      await waitFor(() => expect(localDb.syncQueue.count()).resolves.toBe(0))
      expect((window.location as unknown as { href: string }).href).toBe('/login')
    } finally {
      if (originalLocation) Object.defineProperty(window, 'location', originalLocation)
    }
  })
})

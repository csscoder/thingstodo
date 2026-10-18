import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { http, HttpResponse } from 'msw'
import { fireEvent } from '@testing-library/react'
import { act, render, screen } from '../../test/test-utils'
import { SearchOverlay } from '../SearchOverlay'
import { useAppStore } from '../../stores/app'
import { server } from '../../test/mocks/server'

describe('SearchOverlay', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    useAppStore.setState({ searchOpen: true })
  })

  afterEach(() => {
    vi.useRealTimers()
    useAppStore.setState({ searchOpen: false })
  })

  test('whitespace search sends no request', async () => {
    let calls = 0
    server.use(
      http.get('/api/search', () => {
        calls++
        return HttpResponse.json({ results: [] })
      }),
    )

    render(<SearchOverlay />)
    const input = screen.getByPlaceholderText('Search tasks...')

    // Whitespace-only query: debounced, but never sent
    fireEvent.change(input, { target: { value: '   ' } })
    await act(async () => {
      vi.advanceTimersByTime(400)
    })
    expect(calls).toBe(0)

    // Control: a real query through the same path does send one request
    fireEvent.change(input, { target: { value: 'hello' } })
    await act(async () => {
      vi.advanceTimersByTime(400)
    })
    expect(calls).toBe(1)
  })
})

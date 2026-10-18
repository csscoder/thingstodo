import { test, expect } from '@playwright/test'
import {
  apiFetch,
  apiView,
  closeTaskModal,
  expectSynced,
  expectUiMatchesView,
  forceSync,
  login,
  openTask,
  quickAdd,
  restartServer,
  taskRow,
  viewTaskIds,
} from './helpers'

test.describe('sync', () => {
  test('offline edit pushes on reconnect', async ({ page, context, request }) => {
    await login(page)

    await context.setOffline(true)
    await expect(page.locator('button[aria-label="Offline"]')).toBeVisible()

    // Quick entry works offline (local-first); the change queues locally.
    await quickAdd(page, 'Offline edit')
    await expect(taskRow(page, 'Offline edit').first()).toBeVisible()

    // Reconnect: the queued create pushes to the server.
    await context.setOffline(false)
    await expectSynced(page)
    const inbox = await apiView(request, 'inbox')
    expect(viewTaskIds('inbox', inbox)).toContain(
      (inbox.tasks as Array<{ id: string; title: string }>).find((t) => t.title === 'Offline edit')!.id,
    )

    await expectUiMatchesView(page, request, 'inbox')
  })

  test('server-rejected change appears in stuck list and can be discarded', async ({
    page,
    context,
    request,
  }) => {
    await login(page)
    // A tag the queued edit will reference; the server loses it while offline.
    const tag = await apiFetch(request, 'POST', '/api/tags', { title: 'stucktag' })
    await forceSync(page)
    await quickAdd(page, 'Stuck task')
    await expectSynced(page)
    const inbox = await apiView(request, 'inbox')
    const taskId = (inbox.tasks as Array<{ id: string; title: string }>).find(
      (t) => t.title === 'Stuck task',
    )!.id

    // Go offline and delete the target tag behind the UI's back.
    await context.setOffline(true)
    await apiFetch(request, 'DELETE', `/api/tags/${tag.id}`)
    await expect(page.locator('button[aria-label="Offline"]')).toBeVisible()

    // Tag the task with the (now deleted) tag through the real picker.
    await openTask(page, 'Stuck task')
    await page.locator('.task-detail-modal').getByRole('button', { name: 'Add tag' }).click()
    const tagSearch = page.getByPlaceholder('Search or create…')
    await tagSearch.fill('stucktag')
    await page
      .locator('.task-detail-modal')
      .getByRole('button', { name: 'stucktag', exact: true })
      .click()
    await page.keyboard.press('Escape')
    await closeTaskModal(page)
    await expect(page.locator('.task-detail-modal')).toHaveCount(0)

    // Reconnect: the server rejects the tag reference permanently.
    await context.setOffline(false)
    await expect(page.locator('button[aria-label="1 stuck"]')).toBeVisible({ timeout: 30_000 })

    // The stuck list shows the change and Discard clears it.
    await page.locator('button[aria-label="1 stuck"]').click()
    const dialog = page.locator('div[role="dialog"]')
    await expect(dialog.getByRole('heading', { name: 'Stuck changes' })).toBeVisible()
    await expect(dialog.getByText('Stuck task')).toBeVisible()
    await dialog.getByRole('button', { name: 'Discard' }).click()
    await dialog.getByRole('button', { name: /Discard 1 change/ }).click()
    await expect(dialog.getByText('No stuck changes.')).toBeVisible()
    await dialog.getByRole('button', { name: 'Close' }).click()
    await expectSynced(page)

    // The task is still on the server, in the inbox, without the tag.
    const after = await apiView(request, 'inbox')
    expect(viewTaskIds('inbox', after)).toContain(taskId)
    const detail = await apiFetch(request, 'GET', `/api/tasks/${taskId}`)
    expect(detail.tags).toEqual([])

    await expectUiMatchesView(page, request, 'inbox')
  })

  test('SSE: API change appears without reload', async ({ page, request }) => {
    await login(page)
    await apiFetch(request, 'POST', '/api/tasks', { title: 'SSE one' })
    await expect(taskRow(page, 'SSE one').first()).toBeVisible({ timeout: 20_000 })

    await expectUiMatchesView(page, request, 'inbox')
  })

  test('SSE reconnects after server restart', async ({ page, request }) => {
    await login(page)
    await apiFetch(request, 'POST', '/api/tasks', { title: 'SSE two' })
    await expect(taskRow(page, 'SSE two').first()).toBeVisible({ timeout: 20_000 })

    await restartServer()

    // A change after the restart still arrives without a reload.
    await apiFetch(request, 'POST', '/api/tasks', { title: 'SSE three' })
    await expect(taskRow(page, 'SSE three').first()).toBeVisible({ timeout: 30_000 })

    await expectUiMatchesView(page, request, 'inbox')
  })
})

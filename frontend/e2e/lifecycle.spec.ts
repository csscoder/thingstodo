/* eslint-disable @typescript-eslint/no-explicit-any -- API responses are dynamic JSON, typed loosely in specs on purpose */
import { writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test, expect, type APIRequestContext, type Page } from '@playwright/test'
import {
  apiFetch,
  apiPatchTask,
  apiView,
  closeTaskModal,
  dragBy,
  dragHandle,
  expectSynced,
  expectUiMatchesView,
  forceSync,
  login,
  openTask,
  quickAdd,
  serverDateOffset,
  serverToday,
  sidebarLink,
  taskRow,
  uiTaskIds,
  viewTaskIds,
} from './helpers'

/** Find a task id by title in an API view response. */
function idByTitle(data: any, title: string): string {
  const hit = viewTaskIdsDeep(data).find((t: any) => t.title === title)
  if (!hit) throw new Error(`task "${title}" not found in view`)
  return hit.id
}

/** All task objects in a view response (any nesting). */
function viewTaskIdsDeep(data: any): any[] {
  const out: any[] = []
  const walk = (node: unknown) => {
    if (Array.isArray(node)) {
      node.forEach(walk)
      return
    }
    if (node && typeof node === 'object') {
      if ('id' in node && 'title' in node && 'status' in node) out.push(node)
      Object.values(node).forEach(walk)
    }
  }
  walk(data)
  return out
}


/** Type a time into the modal's Start/End field (opens it if needed).
 *  Retries: SSE-driven refetches remount the schedule rows mid-interaction. */
async function setTime(page: import('@playwright/test').Page, which: 'Start' | 'End', value: string): Promise<void> {
  const modal = page.locator('.task-detail-modal')
  for (let attempt = 0; ; attempt++) {
    const input = modal.getByPlaceholder('e.g. 9am')
    try {
      const open = await input.waitFor({ state: 'visible', timeout: 1500 }).then(() => true).catch(() => false)
      if (!open) {
        await modal.getByRole('button', { name: which, exact: true }).click({ timeout: 3000 })
      }
      await input.fill(value, { timeout: 3000 })
      await input.press('Enter', { timeout: 3000 })
      return
    } catch (err) {
      if (attempt >= 5) throw err
      await page.waitForTimeout(400)
    }
  }
}

/** GET /api/tasks/:id. */
async function apiTask(request: APIRequestContext, id: string): Promise<any> {
  return apiFetch(request, 'GET', `/api/tasks/${id}`)
}

/** Tasks matching a title via the search endpoint. */
async function apiSearchTasks(request: APIRequestContext, title: string): Promise<any[]> {
  const res = await apiFetch(
    request,
    'GET',
    `/api/tasks?search=${encodeURIComponent(title)}`,
  )
  return res.tasks
}

/** Create an area + project (+ optional heading) over the API and wait for the
 *  UI to know about them (SSE pull). */
async function apiCreateProject(
  request: APIRequestContext,
  page: Page,
  areaTitle: string,
  projectTitle: string,
  headingTitle?: string,
): Promise<{ areaId: string; projectId: string; headingId?: string }> {
  const area = await apiFetch(request, 'POST', '/api/areas', { title: areaTitle })
  const project = await apiFetch(request, 'POST', '/api/projects', {
    title: projectTitle,
    area_id: area.id,
  })
  let headingId: string | undefined
  if (headingTitle) {
    const heading = await apiFetch(
      request,
      'POST',
      `/api/projects/${project.id}/headings`,
      { title: headingTitle },
    )
    headingId = heading.id
  }
  // Wait until the sidebar (local Dexie) knows the project. The SSE event
  // drives the pull, but forcing a sync round makes it deterministic.
  await forceSync(page)
  await expect(page.locator('aside').getByText(projectTitle, { exact: true })).toBeVisible()
  return { areaId: area.id, projectId: project.id, headingId }
}

test.describe('lifecycle', () => {
  test('login, first load, logout clears local data', async ({ page, request }) => {
    // Seed a task over the API: the first load must pull it in.
    await apiFetch(request, 'POST', '/api/tasks', { title: 'First load task' })
    await login(page)
    await expect(taskRow(page, 'First load task').first()).toBeVisible()

    // A reload keeps the session.
    await page.reload()
    await expect(page.getByRole('heading', { name: 'Inbox' })).toBeVisible()
    await expect(taskRow(page, 'First load task').first()).toBeVisible()

    // Log out: the local database is wiped and we land on the login page.
    await page.getByRole('button', { name: 'Settings' }).hover()
    await page.getByRole('button', { name: 'Log out' }).click()
    await page.waitForURL('**/login')
    // clearLocalData drops and recreates the db; the tasks store must be empty.
    const localTaskCount = await page.evaluate(
      () =>
        new Promise<number>((resolve) => {
          const req = indexedDB.open('ThingsToDo')
          req.onsuccess = () => {
            const db = req.result
            try {
              const count = db.transaction('tasks', 'readonly').objectStore('tasks').count()
              count.onsuccess = () => resolve(count.result)
              count.onerror = () => resolve(-1)
            } catch {
              resolve(-2)
            }
          }
          req.onerror = () => resolve(-3)
        }),
    )
    expect(localTaskCount).toBe(0)

    // Logging back in works from the empty local state.
    await login(page)

    await expectUiMatchesView(page, request, 'inbox')
  })

  test('quick entry with #покупки, $Project, date and time', async ({ page, request }) => {
    await login(page)
    await apiCreateProject(request, page, 'Дом', 'Ремонт')

    await page.keyboard.press('q')
    const input = page.getByPlaceholder('New task...')
    await input.fill('Купить хлеб #покупки $Ремонт')

    // When date: today.
    await page.getByRole('button', { name: 'Set when date' }).click()
    const when = page.getByPlaceholder('When...')
    await when.fill('today')
    await when.press('Enter')

    // Deadline: tomorrow.
    await page.getByRole('button', { name: 'Set deadline' }).click()
    const deadline = page.getByPlaceholder('Deadline...')
    await deadline.fill('tomorrow')
    await deadline.press('Enter')

    await input.press('Enter')
    // The modal closes once the local create has committed — waiting for it
    // prevents navigating away mid-mutation.
    await expect(page.locator('.quick-entry-modal')).toHaveCount(0)
    await page.goto('/today')
    await expect(taskRow(page, 'Купить хлеб').first()).toBeVisible()
    await expectSynced(page)

    const today = await apiView(request, 'today')
    const id = idByTitle(today, 'Купить хлеб')
    const detail = await apiTask(request, id)
    expect(detail.when_date).toBe(serverToday())
    expect(detail.deadline).toBe(serverDateOffset(1))
    expect(detail.project?.title).toBe('Ремонт')
    expect(detail.tags.map((t: any) => t.title)).toContain('покупки')

    // A time block on the today schedule.
    await openTask(page, 'Купить хлеб')
    await setTime(page, 'Start', '18:00')
    await expect(page.locator('.task-detail-modal').getByText('6:00 PM')).toBeVisible()
    await setTime(page, 'End', '19:00')
    await closeTaskModal(page)
    await expectSynced(page)

    const withTime = await apiTask(request, id)
    expect(withTime.schedules[0].start_time).toBe('18:00')
    expect(withTime.schedules[0].end_time).toBe('19:00')

    await expectUiMatchesView(page, request, 'today')
  })

  test('schedules: today, evening, someday, future, two time blocks, 23:30 block', async ({
    page,
    request,
  }) => {
    await login(page)
    await quickAdd(page, 'Sched main')

    // Today 10:00-11:00 (day block) + today 19:00-20:00 (evening block)
    // + tomorrow (future): three schedule entries on one task.
    await openTask(page, 'Sched main')
    const modal = page.locator('.task-detail-modal')
    const when = modal.getByPlaceholder('When...')
    await when.fill('today')
    await when.press('Enter')
    await setTime(page, 'Start', '10:00')
    await setTime(page, 'End', '11:00')
    await expect(modal.getByText('10:00 AM')).toBeVisible()

    // Second entry: today 19:00-20:00 — lands in This Evening.
    await modal.getByRole('button', { name: 'Add date' }).click()
    let entryWhen = modal.getByPlaceholder('When...')
    await entryWhen.fill('today')
    await entryWhen.press('Enter')
    await setTime(page, 'Start', '19:00')
    await setTime(page, 'End', '20:00')
    await expect(modal.getByText('7:00 PM')).toBeVisible()

    // Third entry: tomorrow, timeless — the future schedule.
    await modal.getByRole('button', { name: 'Add date' }).click()
    entryWhen = modal.getByPlaceholder('When...')
    await entryWhen.fill('tomorrow')
    await entryWhen.press('Enter')
    await closeTaskModal(page)
    await expectSynced(page)

    // A someday task through quick entry.
    await page.keyboard.press('q')
    const input = page.getByPlaceholder('New task...')
    await input.fill('Sched someday')
    await page.getByRole('button', { name: 'Set when date' }).click()
    const qWhen = page.getByPlaceholder('When...')
    await qWhen.fill('someday')
    await qWhen.press('Enter')
    await input.press('Enter')
    await expectSynced(page)

    // A 23:30 block today.
    await quickAdd(page, 'Sched late')
    await openTask(page, 'Sched late')
    const late = page.locator('.task-detail-modal')
    const lateWhen = late.getByPlaceholder('When...')
    await lateWhen.fill('today')
    await lateWhen.press('Enter')
    await setTime(page, 'Start', '23:30')
    await setTime(page, 'End', '23:45')
    await closeTaskModal(page)
    await expectSynced(page)

    // The today view: 'Sched main' in Today and This Evening, 'Sched late' in This Evening.
    await page.goto('/today')
    await expect(page.getByRole('heading', { name: 'This Evening' })).toBeVisible()
    const today = await apiView(request, 'today')
    const evening = today.sections.find((s: any) => s.title === 'This Evening')
    const day = today.sections.find((s: any) => s.title === 'Today')
    const eveningIds = evening.groups.flatMap((g: any) => g.tasks.map((t: any) => t.id))
    const dayIds = day.groups.flatMap((g: any) => g.tasks.map((t: any) => t.id))
    const mainId = idByTitle(today, 'Sched main')
    const lateId = idByTitle(today, 'Sched late')
    expect(dayIds).toContain(mainId)
    expect(eveningIds).toContain(mainId)
    expect(eveningIds).toContain(lateId)

    // The someday task sits in the someday view.
    const someday = await apiView(request, 'someday')
    expect(viewTaskIds('someday', someday)).toContain(idByTitle(someday, 'Sched someday'))

    // The future entry shows up under tomorrow in upcoming.
    const upcoming = await apiView(request, 'upcoming')
    expect(viewTaskIds('upcoming', upcoming)).toContain(mainId)

    // Schedule rows on the server carry the times we typed.
    const detail = await apiTask(request, mainId)
    const byStart = (a: any, b: any) => (a.start_time ?? '').localeCompare(b.start_time ?? '')
    const times = [...detail.schedules]
      .filter((s: any) => s.when_date === serverToday())
      .sort(byStart)
      .map((s: any) => [s.start_time, s.end_time])
    expect(times).toEqual([
      ['10:00', '11:00'],
      ['19:00', '20:00'],
    ])

    await expectUiMatchesView(page, request, 'today')
  })

  test('deadline, exact and relative reminders, checklist, link and file attachments', async ({
    page,
    request,
  }) => {
    await login(page)
    await quickAdd(page, 'Full featured')
    await openTask(page, 'Full featured')
    const modal = page.locator('.task-detail-modal')

    // Deadline.
    const deadline = modal.getByPlaceholder('Deadline...')
    await deadline.fill('tomorrow')
    await deadline.press('Enter')

    // Relative reminder: 15 minutes before.
    await modal.getByRole('button', { name: 'Add reminder' }).first().click()
    await modal.getByRole('button', { name: 'Save' }).click()

    // Exact reminder tomorrow 09:00 owner-local (06:00Z in Europe/Moscow).
    await modal.getByRole('button', { name: 'Add reminder' }).first().click()
    await modal.getByRole('combobox').selectOption('exact')
    await modal.locator('input[type="datetime-local"]').fill(`${serverDateOffset(1)}T09:00`)
    await modal.getByRole('button', { name: 'Save' }).click()

    // Checklist: two items, first one done.
    const addItem = modal.getByPlaceholder('Add item...')
    await addItem.fill('Alpha')
    await addItem.press('Enter')
    await addItem.fill('Beta')
    await addItem.press('Enter')
    await expect(modal.getByText('Alpha')).toBeVisible()
    await modal.locator('.group\\/item', { hasText: 'Alpha' }).getByRole('checkbox').click()

    // Link attachment.
    await modal.getByRole('button', { name: 'Add link' }).click()
    await modal.getByPlaceholder('URL...').fill('https://example.com/e2e')
    await modal.getByPlaceholder('Title...').fill('Example link')
    await modal.getByPlaceholder('URL...').press('Enter')

    // File attachment.
    const filePath = join(tmpdir(), `e2e-upload-${Date.now()}.txt`)
    writeFileSync(filePath, 'e2e attachment payload')
    await modal.locator('input[type="file"]').setInputFiles(filePath)
    await expect(modal.getByText('e2e-upload')).toBeVisible()

    await closeTaskModal(page)
    await expectSynced(page)

    const inbox = await apiView(request, 'inbox')
    const detail = await apiTask(request, idByTitle(inbox, 'Full featured'))
    expect(detail.deadline).toBe(serverDateOffset(1))
    const rel = detail.reminders.find((r: any) => r.type === 'minutes_before')
    expect(rel?.value).toBe(15)
    const exact = detail.reminders.find((r: any) => r.type === 'exact')
    expect(exact?.exact_at).toContain('T06:00:00Z')
    expect(detail.checklist.map((c: any) => [c.title, c.completed])).toEqual([
      ['Alpha', true],
      ['Beta', false],
    ])
    const kinds = detail.attachments.map((a: any) => [a.type, a.title]).sort()
    expect(kinds).toEqual([
      ['file', expect.stringContaining('e2e-upload')],
      ['link', 'Example link'],
    ])

    await expectUiMatchesView(page, request, 'inbox')
  })

  test('move to area, project, heading; reorder survives reload', async ({ page, request }) => {
    await login(page)
    const { areaId, projectId, headingId } = await apiCreateProject(
      request,
      page,
      'Work',
      'Website',
      'Phase 1',
    )
    expect(headingId).toBeTruthy()

    await quickAdd(page, 'Move area')
    await quickAdd(page, 'Move project')
    await quickAdd(page, 'Move heading')
    await quickAdd(page, 'Reorder last')
    await quickAdd(page, 'Reorder first')
    await expectSynced(page)

    // Move to area: drag onto the sidebar area.
    await dragBy(page, dragHandle(page, 'Move area'), page.locator('aside').getByText('Work', { exact: true }))
    await expectSynced(page)
    // Move to project: drag onto the sidebar project.
    await dragBy(page, dragHandle(page, 'Move project'), page.locator('aside').getByText('Website', { exact: true }))
    await expectSynced(page)

    // Move to heading: no heading picker exists in the UI — move via the API.
    const inbox = await apiView(request, 'inbox')
    const headingTaskId = idByTitle(inbox, 'Move heading')
    await apiPatchTask(request, headingTaskId, { project_id: projectId, heading_id: headingId })
    await expectSynced(page) // pull the heading move into the local state

    // Reorder in the inbox: drag 'Reorder first' above 'Reorder last'.
    await dragBy(page, dragHandle(page, 'Reorder first'), taskRow(page, 'Reorder last').first())
    await expectSynced(page)

    const after = await apiView(request, 'inbox')
    const detailArea = await apiTask(request, (await apiSearchTasks(request, 'Move area'))[0].id)
    const detailProject = await apiTask(request, (await apiSearchTasks(request, 'Move project'))[0].id)
    const detailHeading = await apiTask(request, headingTaskId)
    expect(detailArea.area?.id).toBe(areaId)
    expect(detailArea.project_id).toBeNull()
    expect(detailProject.project?.id).toBe(projectId)
    expect(detailHeading.heading?.id).toBe(headingId)

    // Order survives a reload: UI sequence equals the API sequence.
    const apiOrder = viewTaskIds('inbox', after)
    await page.reload()
    await expect(page.locator('main p', { hasText: 'Loading...' })).toHaveCount(0, { timeout: 20_000 })
    const uiOrder = (await uiTaskIds(page)).filter((id) => apiOrder.includes(id))
    expect(uiOrder).toEqual(apiOrder)

    await expectUiMatchesView(page, request, 'inbox')
  })

  test("complete, cancel, won't do, reopen; repeating task spawns one instance (fixed and after completion)", async ({
    page,
    request,
  }) => {
    await login(page)
    await quickAdd(page, 'Stat done')
    await quickAdd(page, 'Stat cancel')
    await quickAdd(page, 'Stat wont')
    await quickAdd(page, 'Stat repeat fixed')
    await quickAdd(page, 'Stat repeat after')
    await expectSynced(page)

    // Complete from the row checkbox.
    await taskRow(page, 'Stat done').first().getByRole('checkbox').click()
    await expect(taskRow(page, 'Stat done')).toHaveCount(0)

    // Cancel and won't do from the detail modal.
    await openTask(page, 'Stat cancel')
    await page.getByRole('button', { name: 'Cancel task' }).click()
    await expect(page.locator('.task-detail-modal')).toHaveCount(0)
    await openTask(page, 'Stat wont')
    await page.getByRole('button', { name: "Won't do task" }).click()
    await expect(page.locator('.task-detail-modal')).toHaveCount(0)
    await expectSynced(page)

    // Reopen the completed task from the logbook.
    await sidebarLink(page, 'Completed').click()
    await expect(page.getByRole('heading', { name: 'Completed' })).toBeVisible()
    await taskRow(page, 'Stat done').first().getByRole('checkbox').click()
    await sidebarLink(page, 'Inbox').click()
    await expect(taskRow(page, 'Stat done').first()).toBeVisible()

    // Fixed daily repeat: complete spawns exactly one instance tomorrow.
    for (const [title, mode] of [
      ['Stat repeat fixed', 'Fixed schedule'],
      ['Stat repeat after', 'After completion'],
    ] as const) {
      await page.goto('/inbox')
      await openTask(page, title)
      const modal = page.locator('.task-detail-modal')
      const when = modal.getByPlaceholder('When...')
      await when.fill('today')
      await when.press('Enter')
      await closeTaskModal(page)
      await expectSynced(page)

      // The task now lives in Today; open it there and set the repeat rule.
      await page.goto('/today')
      await openTask(page, title)
      await modal.getByRole('button', { name: 'Repeat' }).click()
      await page.getByRole('combobox').nth(1).selectOption(mode)
      await page.getByRole('button', { name: 'Save' }).click()
      await closeTaskModal(page)
      await expectSynced(page)

      // Complete it (row checkbox; today-only schedule needs no confirm).
      await taskRow(page, title).first().getByRole('checkbox').click()
      await expect(taskRow(page, title)).toHaveCount(0)
      await expectSynced(page)

      const hits = await apiSearchTasks(request, title)
      expect(hits).toHaveLength(2)
      const spawned = hits.find((t) => t.status === 'open')
      const original = hits.find((t) => t.status !== 'open')
      expect(original?.status).toBe('completed')
      expect(spawned?.when_date).toBe(serverDateOffset(1))
    }

    await expectUiMatchesView(page, request, 'inbox')
  })

  test('multi-select drag to Completed and Today', async ({ page, request }) => {
    await login(page)
    await quickAdd(page, 'Multi one')
    await quickAdd(page, 'Multi two')
    await quickAdd(page, 'Multi three')
    await expectSynced(page)

    // Cmd-click two tasks, drag the selection to Completed.
    await taskRow(page, 'Multi one').click({ modifiers: ['Meta'] })
    await taskRow(page, 'Multi two').click({ modifiers: ['Meta'] })
    await dragBy(page, dragHandle(page, 'Multi one'), sidebarLink(page, 'Completed'))
    await expectSynced(page)

    const afterBatch = await apiSearchTasks(request, 'Multi')
    const statusOf = (title: string) => afterBatch.find((t) => t.title === title)?.status
    expect(statusOf('Multi one')).toBe('completed')
    expect(statusOf('Multi two')).toBe('completed')

    // A second selection dragged to Today moves it to today's list.
    await page.keyboard.press('Escape') // clear the previous selection
    await taskRow(page, 'Multi three').click({ modifiers: ['Meta'] })
    await dragBy(page, dragHandle(page, 'Multi three'), sidebarLink(page, 'Today'))
    await expectSynced(page)

    const today = await apiView(request, 'today')
    expect(viewTaskIds('today', today)).toContain(idByTitle(today, 'Multi three'))

    await expectUiMatchesView(page, request, 'inbox')
  })

  test('trash: delete, restore, empty; delete project and area send tasks to Trash', async ({ page, request }) => {
    await login(page)
    await quickAdd(page, 'Trash me')
    await quickAdd(page, 'Trash filler')
    await expectSynced(page)

    // Delete through the detail modal.
    await openTask(page, 'Trash me')
    await page.getByRole('button', { name: 'Delete task' }).click()
    await expect(page.locator('.task-detail-modal')).toHaveCount(0)
    await openTask(page, 'Trash filler')
    await page.getByRole('button', { name: 'Delete task' }).click()
    await expectSynced(page)

    // Both are in the trash view.
    await sidebarLink(page, 'Trash').click()
    await expect(page.getByText('Trash me')).toBeVisible()
    let trash = await apiView(request, 'trash')
    expect(viewTaskIds('trash', trash)).toContain(idByTitle(trash, 'Trash me'))

    // Restore by dragging back onto the Inbox sidebar target.
    await dragBy(page, dragHandle(page, 'Trash me'), sidebarLink(page, 'Inbox'))
    await expectSynced(page)
    const inbox = await apiView(request, 'inbox')
    expect(viewTaskIds('inbox', inbox)).toContain(idByTitle(inbox, 'Trash me'))

    // Empty the trash.
    await sidebarLink(page, 'Trash').click()
    await page.getByRole('button', { name: 'Empty trash' }).click()
    await page.getByRole('button', { name: 'Empty Trash', exact: true }).click()
    await expect(page.getByText('Trash is empty.')).toBeVisible()

    // Deleting a project sends its tasks to Trash.
    const { projectId } = await apiCreateProject(request, page, 'Trash area', 'Trash project')
    await apiFetch(request, 'POST', '/api/tasks', {
      title: 'Project orphan',
      project_id: projectId,
    })
    await expectSynced(page) // pull the task into the local project view
    await sidebarLink(page, 'Trash project').click()
    const projectDelete = page
      .getByRole('heading', { name: 'Trash project' })
      .locator('..')
      .getByRole('button')
    await projectDelete.click()
    await page.getByRole('button', { name: 'Delete', exact: true }).click()
    await expectSynced(page)

    // Deleting an area sends its standalone tasks to Trash.
    const area = await apiFetch(request, 'POST', '/api/areas', { title: 'Trash area 2' })
    await apiFetch(request, 'POST', '/api/tasks', { title: 'Area orphan', area_id: area.id })
    await expectSynced(page)
    await page.locator('aside').getByText('Trash area 2', { exact: true }).click()
    await expect(page.getByRole('heading', { name: 'Trash area 2' })).toBeVisible()
    const areaDelete = page
      .getByRole('heading', { name: 'Trash area 2' })
      .locator('..')
      .getByRole('button')
    await areaDelete.click()
    await page.getByRole('button', { name: 'Delete', exact: true }).click()
    await expectSynced(page)

    trash = await apiView(request, 'trash')
    const trashIds = viewTaskIds('trash', trash)
    expect(trashIds).toContain(idByTitle(trash, 'Project orphan'))
    expect(trashIds).toContain(idByTitle(trash, 'Area orphan'))

    await expectUiMatchesView(page, request, 'trash')
  })

  test('settings: font size 14 to 18, evening start, default reminder', async ({
    page,
    request,
  }) => {
    await login(page)
    await page.getByRole('button', { name: 'Settings' }).click()
    await expect(page.getByRole('heading', { name: 'Settings' })).toBeVisible()

    // Font size: drag the slider to 18, then back to 14.
    const slider = page.locator('input[type="range"]')
    const box = await slider.boundingBox()
    expect(box).toBeTruthy()
    await slider.click({ position: { x: box!.width - 2, y: box!.height / 2 } })
    await expect(page.getByText('18px')).toBeVisible()
    expect(await page.evaluate(() => document.documentElement.style.fontSize)).toBe('18px')
    await slider.click({ position: { x: 2, y: box!.height / 2 } })
    await expect(page.getByText('14px')).toBeVisible()
    expect(await page.evaluate(() => document.documentElement.style.fontSize)).toBe('14px')

    // Evening start: 20:00.
    const evening = page.getByText('Evening starts at').locator('..').locator('input')
    await evening.fill('20:00')
    await evening.press('Enter')
    await expect(evening).toHaveValue('8:00 PM')

    // Default reminder: minutes before, 30.
    await page.getByRole('button', { name: 'Notifications' }).click()
    await page.getByRole('combobox').selectOption('minutes_before')
    await page.locator('input[type="number"]').fill('30')
    await expectSynced(page)

    const settings = await apiFetch(request, 'GET', '/api/user/settings')
    expect(settings.font_size).toBe(14)
    expect(settings.evening_starts_at).toBe('20:00')
    expect(settings.default_reminder_type).toBe('minutes_before')
    expect(settings.default_reminder_value).toBe(30)

    // Values survive a reload.
    await page.reload()
    await expect(page.getByText('14px')).toBeVisible()
    await expect(evening).toHaveValue('8:00 PM')

    await page.goto('/inbox')
    await expectUiMatchesView(page, request, 'inbox')
  })
})

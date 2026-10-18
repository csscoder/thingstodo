/* eslint-disable @typescript-eslint/no-explicit-any -- API responses are dynamic JSON, typed loosely in specs on purpose */
import { spawn, type ChildProcess } from 'node:child_process'
import { randomBytes } from 'node:crypto'
import { mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { expect, type APIRequestContext, type Locator, type Page } from '@playwright/test'

/** The backend under test (the Vite dev proxy forwards /api here in e2e:dev). */
export const API_BASE = process.env.E2E_API_BASE ?? 'http://localhost:3999'
const SERVER_BIN = join(fileURLToPath(new URL('.', import.meta.url)), '..', '..', 'bin', 'thingstodo')

// The builtin user seeded from LOGIN_PASSWORD (cmd/server/main.go).
export const LOGIN_USERNAME = 'admin'
export const LOGIN_PASSWORD = 'e2e-pass'

function authHeaders(): Record<string, string> {
  const token = process.env.E2E_API_TOKEN
  if (!token) throw new Error('E2E_API_TOKEN not set — global-setup did not run')
  return { Authorization: `Bearer ${token}` }
}

// ---------------------------------------------------------------------------
// Server lifecycle (shared by global-setup and restartServer)
// ---------------------------------------------------------------------------

async function waitForServer(timeoutMs = 30_000): Promise<void> {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    try {
      const res = await fetch(`${API_BASE}/api/auth/config`)
      if (res.status === 200) return
    } catch {
      // not up yet
    }
    await new Promise((r) => setTimeout(r, 250))
  }
  throw new Error(`server at ${API_BASE} did not become ready`)
}

/** Start the e2e server; records its pid in the pid file for teardown. */
export async function startServer(dbPath: string, jwtSecret: string): Promise<ChildProcess> {
  const child = spawn(SERVER_BIN, [], {
    env: {
      ...process.env,
      PORT: '3999',
      DB_PATH: dbPath,
      AUTH_MODE: 'builtin',
      LOGIN_PASSWORD: LOGIN_PASSWORD,
      JWT_SECRET: jwtSecret,
      TZ: 'Europe/Moscow',
    },
    stdio: 'ignore',
    detached: true,
  })
  child.unref()
  writeFileSync(process.env.E2E_PID_FILE!, String(child.pid))
  await waitForServer()
  return child
}

/** Kill the current e2e server and start a fresh process on the same DB. */
export async function restartServer(): Promise<void> {
  const pidFile = process.env.E2E_PID_FILE
  if (!pidFile) throw new Error('E2E_PID_FILE not set — global-setup did not run')
  let pid: number | undefined
  try {
    pid = Number(readFileSync(pidFile, 'utf8').trim())
  } catch {
    // no pid recorded — nothing to kill
  }
  if (pid) {
    try {
      process.kill(pid, 'SIGKILL')
    } catch {
      // already dead
    }
    const deadline = Date.now() + 10_000
    while (Date.now() < deadline) {
      try {
        process.kill(pid, 0)
        await new Promise((r) => setTimeout(r, 100))
      } catch {
        break
      }
    }
  }
  await startServer(process.env.E2E_DB_PATH!, process.env.E2E_JWT_SECRET!)
}

// ---------------------------------------------------------------------------
// API helpers
// ---------------------------------------------------------------------------

/** GET /api/views/<name> with the e2e token. */
export async function apiView(request: APIRequestContext, name: string): Promise<any> {
  const res = await request.get(`${API_BASE}/api/views/${name}`, { headers: authHeaders() })
  expect(res.status()).toBe(200)
  return res.json()
}

/** PATCH /api/tasks/:id with the e2e token. */
export async function apiPatchTask(
  request: APIRequestContext,
  id: string,
  body: Record<string, unknown>,
): Promise<any> {
  const res = await request.patch(`${API_BASE}/api/tasks/${id}`, {
    headers: { ...authHeaders(), 'Content-Type': 'application/json' },
    data: body,
  })
  expect(res.status()).toBe(200)
  return res.json()
}

/** Arbitrary authenticated API call from specs (create task/project/area…). */
export async function apiFetch(
  request: APIRequestContext,
  method: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE',
  path: string,
  data?: Record<string, unknown>,
): Promise<any> {
  const res = await request.fetch(`${API_BASE}${path}`, {
    method,
    headers: { ...authHeaders(), 'Content-Type': 'application/json' },
    data: data as never,
  })
  expect(res.status()).toBeLessThan(300)
  if (res.status() === 204) return null
  return res.json()
}

// ---------------------------------------------------------------------------
// UI helpers
// ---------------------------------------------------------------------------

/** Log in through the real login form and wait for the first sync to settle. */
export async function login(page: Page): Promise<void> {
  await page.goto('/login')
  await page.locator('input[type="text"]').fill(LOGIN_USERNAME)
  await page.locator('input[type="password"]').fill(LOGIN_PASSWORD)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await page.waitForURL('**/inbox')
  await expect(page.getByRole('heading', { name: 'Inbox' })).toBeVisible()
  await expectSynced(page)
}

/** The local sync cursor, or -1 when no sync has completed yet. */
function localCursor(page: Page): Promise<number> {
  return page.evaluate(
    () =>
      new Promise<number>((resolve) => {
        const req = indexedDB.open('ThingsToDo')
        req.onsuccess = () => {
          try {
            const g = req.result.transaction('syncMeta', 'readonly').objectStore('syncMeta').get('cursor')
            g.onsuccess = () => resolve(g.result ? (g.result.value as number) : -1)
            g.onerror = () => resolve(-1)
          } catch {
            resolve(-1)
          }
        }
        req.onerror = () => resolve(-1)
      }),
  )
}

/**
 * Wait until a sync round has completed (the cursor record exists — the
 * "Synced" label alone is the idle default before the first sync) and the
 * indicator is not in an error/offline/stuck state. Clicking the indicator
 * first forces a fresh round so queued changes are pushed now instead of on
 * the 30 s interval.
 */
export async function expectSynced(page: Page, timeout = 30_000): Promise<void> {
  await forceSync(page)
  // Give the click's syncNow a beat to flip the label to "Syncing…" so the
  // wait below observes the new round, not the previous idle state.
  await page.waitForTimeout(150)
  await expect.poll(() => localCursor(page), { timeout }).toBeGreaterThan(-1)
  await expect(page.locator('button[aria-label="Synced"]')).toBeVisible({ timeout })
}

/**
 * Click the sync indicator to trigger an immediate sync round (the 30 s
 * interval and SSE events are the only other triggers). Never clicked while
 * stuck changes exist: that opens the stuck dialog instead.
 */
export async function forceSync(page: Page): Promise<void> {
  await page
    .getByRole('button', {
      name: /^(Synced|Syncing…|\d+ pending|Offline|Sync error|\d+ blocked|\d+ failed)$/,
    })
    .click()
}

/** Task ids rendered in the main area, in document order.
 *  Exit-animation ghost rows (kept in the DOM at height 0) are skipped. */
export async function uiTaskIds(page: Page): Promise<string[]> {
  return page.locator('main [data-task-id]').evaluateAll((els) =>
    els
      .filter((el) => (el as HTMLElement).offsetHeight > 1)
      .map((el) => (el as HTMLElement).dataset.taskId!),
  )
}

/** Task ids the API view response contains, in response order. */
export function viewTaskIds(view: string, data: any): string[] {
  const ids: string[] = []
  const pushArr = (arr: any[] | undefined) => {
    if (Array.isArray(arr)) ids.push(...arr.map((t) => t.id as string))
  }
  switch (view) {
    case 'inbox':
      pushArr(data.tasks)
      pushArr(data.review)
      break
    case 'today':
      for (const s of data.sections ?? []) for (const g of s.groups ?? []) pushArr(g.tasks)
      pushArr(data.overdue)
      pushArr(data.earlier)
      break
    case 'upcoming':
      pushArr(data.overdue)
      pushArr(data.earlier)
      for (const d of data.dates ?? []) pushArr(d.tasks)
      break
    case 'anytime':
    case 'someday':
      for (const a of data.areas ?? []) {
        for (const p of a.projects ?? []) pushArr(p.tasks)
        pushArr(a.standalone_tasks)
      }
      for (const p of data.no_area?.projects ?? []) pushArr(p.tasks)
      pushArr(data.no_area?.standalone_tasks)
      break
    case 'logbook':
    case 'trash':
      for (const g of data.groups ?? []) pushArr(g.tasks)
      break
    default:
      throw new Error(`unknown view ${view}`)
  }
  return ids
}

/** Reload and assert the visible UI list equals the API view's task ids.
 *  Polled: SSE-driven pulls converge the local state shortly after load. */
export async function expectUiMatchesView(
  page: Page,
  request: APIRequestContext,
  view: string,
): Promise<void> {
  await expectSynced(page) // settle: local state matches the server first
  const api = await apiView(request, view)
  const expected = [...new Set(viewTaskIds(view, api))].sort()
  await page.goto(`/${view}`)
  await page.reload()
  await expect(page.locator('main p', { hasText: 'Loading...' })).toHaveCount(0, { timeout: 20_000 })
  await expect
    .poll(
      async () => [...new Set(await uiTaskIds(page))].sort(),
      { timeout: 20_000 },
    )
    .toEqual(expected)
}

/** Open the quick entry (q hotkey), type a title, submit. */
export async function quickAdd(page: Page, title: string): Promise<void> {
  await page.keyboard.press('q')
  const input = page.getByPlaceholder('New task...')
  await expect(input).toBeVisible()
  await input.fill(title)
  await input.press('Enter')
  await expect(taskRow(page, title).first()).toBeVisible()
}

/** The row of the task with this title in the main area (visible rows only —
 *  exit animations briefly keep removed rows in the DOM). */
export function taskRow(page: Page, title: string): Locator {
  return page.locator(`main [data-task-id]`).filter({ hasText: title, visible: true })
}

/** The drag handle of a task row (visible on hover, always hit-testable). */
export function dragHandle(page: Page, title: string): Locator {
  return taskRow(page, title).locator('button').first()
}

/** A sidebar smart-list link by its label (Inbox, Today, Completed, Trash…). */
export function sidebarLink(page: Page, label: string): Locator {
  return page.locator('aside').getByRole('link', { name: label })
}

/** Pointer drag from one element to another (dnd-kit PointerSensor). */
export async function dragBy(page: Page, source: Locator, target: Locator): Promise<void> {
  const s = await source.boundingBox()
  const t = await target.boundingBox()
  if (!s || !t) throw new Error('drag source/target has no box')
  const sx = s.x + s.width / 2
  const sy = s.y + s.height / 2
  const tx = t.x + t.width / 2
  const ty = t.y + t.height / 2
  await page.mouse.move(sx, sy)
  await page.mouse.down()
  const steps = 15
  for (let i = 1; i <= steps; i++) {
    await page.mouse.move(
      sx + ((tx - sx) * i) / steps,
      sy + ((ty - sy) * i) / steps,
    )
  }
  await page.mouse.up()
}

/** Open a task's detail modal (double-click its row).
 *  Retries: framer-motion exit/layout animations leave shifting and
 *  duplicate boxes behind, so a click can land on a neighbouring row. */
export async function openTask(page: Page, title: string): Promise<void> {
  const h2 = page.locator('.task-detail-modal h2')
  for (let attempt = 0; ; attempt++) {
    try {
      await taskRow(page, title).first().dblclick({ force: true, timeout: 5000 })
      await expect(h2).toHaveText(title, { timeout: 3000 })
      return
    } catch (err) {
      if (attempt >= 3) throw err
      await page.keyboard.press('Escape').catch(() => {})
    }
  }
}

/** Close the task detail modal (no-op when already closed). */
export async function closeTaskModal(page: Page): Promise<void> {
  const modal = page.locator('.task-detail-modal')
  if (await modal.count()) {
    await modal.locator('button[aria-label="Close"]').click()
  }
  await expect(modal).toHaveCount(0)
}

/** Server-timezone today computed in Node (matches the server's TZ env). */
export function serverToday(): string {
  return new Intl.DateTimeFormat('en-CA', { timeZone: 'Europe/Moscow' }).format(new Date())
}

/** Server-timezone date N days from today. */
export function serverDateOffset(days: number): string {
  const d = new Date(Date.now() + days * 86_400_000)
  return new Intl.DateTimeFormat('en-CA', { timeZone: 'Europe/Moscow' }).format(d)
}

/** New server env (temp db + random secret); used by global-setup. */
export function newServerEnv(): { dbPath: string; jwtSecret: string; pidFile: string } {
  const dir = mkdtempSync(join(tmpdir(), 'thingstodo-e2e-'))
  return {
    dbPath: join(dir, 'e2e.db'),
    jwtSecret: randomBytes(24).toString('hex'),
    pidFile: join(dir, 'server.pid'),
  }
}

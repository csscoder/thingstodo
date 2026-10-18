import { spawn, type ChildProcess } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import {
  API_BASE,
  LOGIN_PASSWORD,
  LOGIN_USERNAME,
  newServerEnv,
  startServer,
} from './helpers'

const FRONTEND_DIR = join(fileURLToPath(new URL('.', import.meta.url)), '..')

async function waitForUrl(url: string, timeoutMs = 60_000): Promise<void> {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    try {
      const res = await fetch(url)
      if (res.ok) return
    } catch {
      // not up yet
    }
    await new Promise((r) => setTimeout(r, 250))
  }
  throw new Error(`${url} did not become ready`)
}

function killPid(pid: number | undefined): void {
  if (!pid) return
  try {
    process.kill(pid, 'SIGKILL')
  } catch {
    // already dead
  }
}

async function readPidFile(pidFile: string): Promise<number | undefined> {
  try {
    return Number(readFileSync(pidFile, 'utf8').trim())
  } catch {
    return undefined
  }
}

export default async function globalSetup(): Promise<() => Promise<void>> {
  const env = newServerEnv()
  process.env.E2E_DB_PATH = env.dbPath
  process.env.E2E_JWT_SECRET = env.jwtSecret
  process.env.E2E_PID_FILE = env.pidFile

  await startServer(env.dbPath, env.jwtSecret)

  // Log in as the seeded builtin user and mint the e2e API token.
  const loginRes = await fetch(`${API_BASE}/api/auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username: LOGIN_USERNAME, password: LOGIN_PASSWORD }),
  })
  if (!loginRes.ok) throw new Error(`e2e login failed: ${loginRes.status}`)
  const cookie = (loginRes.headers.get('set-cookie') ?? '').split(';')[0]
  const tokenRes = await fetch(`${API_BASE}/api/tokens`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', cookie },
    body: JSON.stringify({ name: 'e2e' }),
  })
  if (tokenRes.status !== 201) throw new Error(`e2e token creation failed: ${tokenRes.status}`)
  const { token } = (await tokenRes.json()) as { token: string }
  process.env.E2E_API_TOKEN = token

  // In e2e:dev mode, serve the UI through the Vite dev server proxy (Task 1).
  let vite: ChildProcess | null = null
  if (process.env.E2E_TARGET === 'dev') {
    vite = spawn('npm', ['run', 'dev', '--', '--port', '5199', '--strictPort'], {
      cwd: FRONTEND_DIR,
      env: { ...process.env, VITE_API_TARGET: API_BASE },
      stdio: 'ignore',
      detached: true,
    })
    vite.unref()
    await waitForUrl('http://localhost:5199/')
  }

  return async () => {
    killPid(await readPidFile(env.pidFile))
    if (vite) killPid(vite.pid)
  }
}

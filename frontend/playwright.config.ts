import { defineConfig } from '@playwright/test'

// E2E_TARGET=dev runs the UI through the Vite dev server proxy (Task 1);
// the default serves the SPA embedded in the Go binary.
const dev = process.env.E2E_TARGET === 'dev'

export default defineConfig({
  testDir: './e2e',
  globalSetup: './e2e/global-setup',
  workers: 1,
  timeout: 60_000,
  expect: { timeout: 15_000 },
  reporter: [
    ['list'],
    ['html', { outputDir: 'playwright-report', open: 'never' }],
  ],
  use: {
    baseURL: dev ? 'http://localhost:5199' : 'http://localhost:3999',
    // Match the server's TZ=Europe/Moscow so datetime-local input and
    // exact_at/deadline assertions are runner-location independent.
    timezoneId: 'Europe/Moscow',
    trace: 'retain-on-failure',
  },
  projects: [{ name: 'chromium' }],
  outputDir: './test-results',
})

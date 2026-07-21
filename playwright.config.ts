import { defineConfig, devices } from '@playwright/test'

export default defineConfig({
  testDir: './tests/e2e',
  outputDir: process.env.E2E_OUTPUT_DIR ?? 'test-results',
  // Password hashing is intentionally expensive and this suite exercises several
  // complete sign-in/change-password flows on CPU-constrained CI containers.
  timeout: 120_000,
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  // The disposable suite deliberately verifies initial empty state and then mutates
  // one isolated database. Retrying a single stateful test would invalidate that proof.
  retries: 0,
  workers: 1,
  reporter: process.env.CI ? [['github'], ['list']] : 'list',
  use: {
    baseURL: process.env.E2E_BASE_URL ?? 'http://127.0.0.1:8080',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    video: 'off',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
})

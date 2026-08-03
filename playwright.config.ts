import { defineConfig, devices } from '@playwright/test'

const chromiumSPKI = process.env.E2E_CHROMIUM_SPKI_LIST
if (chromiumSPKI && !/^[A-Za-z0-9+/]{43}=$/.test(chromiumSPKI)) {
  throw new Error('E2E_CHROMIUM_SPKI_LIST must be one base64-encoded SHA-256 digest')
}

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
    launchOptions: chromiumSPKI
      ? { args: [`--ignore-certificate-errors-spki-list=${chromiumSPKI}`] }
      : undefined,
  },
  projects: [
    {
      name: 'chromium',
      grepInvert: /@phase4-mobile|@phase5-mobile|@phase6/,
      use: { ...devices['Desktop Chrome'] },
    },
    {
      name: 'mobile',
      grep: /@phase4-mobile|@phase5-mobile/,
      use: { ...devices['Pixel 7'] },
    },
    {
      name: 'phase6',
      grep: /@phase6(?!-mobile)/,
      use: { ...devices['Desktop Chrome'] },
    },
    {
      name: 'phase6-mobile',
      grep: /@phase6-mobile/,
      use: { ...devices['Pixel 7'] },
    },
  ],
})

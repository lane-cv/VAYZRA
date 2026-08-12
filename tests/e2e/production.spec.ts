import { expect, test } from '@playwright/test'
import { login } from './helpers'

const httpBaseURL = process.env.E2E_PHASE6_HTTP_BASE_URL
const expectedHostname = process.env.E2E_PHASE6_HOSTNAME
const forbiddenMaintenanceDetail = /commit|database|dependency|endpoint|exception|manifest|request.?id|schema|trace|version/i

function requireEnvironment(name: string, value: string | undefined): string {
  if (!value) throw new Error(`${name} is required by the disposable Phase 6 harness`)
  return value
}

test('@phase6 public edge enforces TLS and privacy', async ({ page, request, baseURL }) => {
  const httpOrigin = requireEnvironment('E2E_PHASE6_HTTP_BASE_URL', httpBaseURL)
  const hostname = requireEnvironment('E2E_PHASE6_HOSTNAME', expectedHostname)
  expect(new URL(baseURL!).protocol).toBe('https:')
  expect(new URL(baseURL!).hostname).toBe(hostname)

  const redirect = await request.get(`${httpOrigin}/api/v1/health/live?phase6=redirect`, { maxRedirects: 0 })
  expect(redirect.status()).toBe(308)
  expect(redirect.headers().location).toBe(`https://${hostname}/api/v1/health/live?phase6=redirect`)

  const edge = await request.get('/')
  expect(edge.status()).toBe(200)
  const headers = edge.headers()
  expect(headers['content-security-policy']).toContain("default-src 'self'")
  expect(headers['x-content-type-options']).toBe('nosniff')
  expect(headers['x-frame-options']).toBe('DENY')
  expect(headers['referrer-policy']).toBe('no-referrer')
  expect(headers['permissions-policy']).toContain('camera=()')
  expect(headers.server).toBeUndefined()

  for (const path of ['/internal/metrics', '/internal/readiness']) {
    const response = await request.get(path)
    expect(response.status(), path).toBe(404)
    expect(await response.text()).not.toMatch(forbiddenMaintenanceDetail)
  }

  const normalOversize = await request.post('/api/v1/auth/login', {
    data: Buffer.alloc(2 * 1024 * 1024 + 1, 0x61),
    headers: { 'Content-Type': 'application/octet-stream' },
  })
  expect(normalOversize.status()).toBe(413)
  const uploadOversize = await request.put('/api/v1/admin/uploads/00000000-0000-4000-8000-000000000000/parts/1', {
    data: Buffer.alloc(9 * 1024 * 1024 + 1, 0x61),
    headers: { 'Content-Type': 'application/octet-stream' },
  })
  expect(uploadOversize.status()).toBe(413)

  await page.goto('/')
  await expect(page.locator('html')).toBeVisible()
})

test('@phase6 maintenance mode is static and fail-closed', async ({ request }) => {
  for (const path of ['/', '/api/v1/health/live', '/internal/readiness']) {
    const response = await request.get(path)
    expect(response.status(), path).toBe(503)
    expect(response.headers()['retry-after']).toBe('300')
    expect(response.headers()['cache-control']).toBe('no-store')
    expect(response.headers().server).toBeUndefined()
    expect(await response.text()).not.toMatch(forbiddenMaintenanceDetail)
  }
  const write = await request.post('/api/v1/auth/logout-others', { data: {} })
  expect(write.status()).toBe(503)
  expect(await write.text()).not.toMatch(forbiddenMaintenanceDetail)
})

test('@phase6 seeds an accepted durable write', async ({ page }) => {
  const marker = requireEnvironment('E2E_PHASE6_DURABLE_MARKER', process.env.E2E_PHASE6_DURABLE_MARKER)
  const adminPassword = requireEnvironment('E2E_ADMIN_PASSWORD', process.env.E2E_ADMIN_PASSWORD)
  await login(page, 'admin', adminPassword)
  await page.goto('/admin/settings')
  await page.getByLabel('站点公告').fill(marker)
  await page.getByRole('button', { name: '保存设置' }).click()
  await expect(
    page.getByRole('status').filter({ hasText: '系统设置已保存' }),
  ).toContainText('系统设置已保存')
})

test('@phase6 production restart preserves durable data', async ({ page }) => {
  const marker = requireEnvironment('E2E_PHASE6_DURABLE_MARKER', process.env.E2E_PHASE6_DURABLE_MARKER)
  const adminPassword = requireEnvironment('E2E_ADMIN_PASSWORD', process.env.E2E_ADMIN_PASSWORD)
  await login(page, 'admin', adminPassword)
  await page.goto('/admin')
  await expect(page.getByTestId('backup-summary')).toBeVisible()
  await page.goto('/admin/settings')
  await expect(page.getByLabel('站点公告')).toHaveValue(marker)
})

test('@phase6-mobile production edge remains usable', async ({ page }) => {
  await page.goto('/')
  await expect(page.locator('html')).toBeVisible()
  await expect(page.locator('body')).not.toHaveCSS('overflow-x', 'scroll')
})

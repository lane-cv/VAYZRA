import { expect, test } from '@playwright/test'

function required(name: string): string {
  const value = process.env[name]
  if (!value) throw new Error(`${name} is required by the disposable Phase 6 harness`)
  return value
}

async function loginAdmin(page: import('@playwright/test').Page): Promise<void> {
  await page.goto('/login')
  await page.getByLabel('账号').fill('admin')
  await page.getByLabel('密码').fill(required('E2E_ADMIN_PASSWORD'))
  await page.getByRole('button', { name: '登录' }).click()
  await expect(page).not.toHaveURL(/login/)
}

test('@phase6 successful release exposes the second safe version', async ({ page, request }) => {
  await loginAdmin(page)
  await page.goto('/admin')
  await expect(page.getByTestId('release-version')).toHaveText(required('E2E_PHASE6_EXPECTED_VERSION'))
  await page.goto('/admin/settings')
  await expect(page.getByLabel('站点公告')).toHaveValue(required('E2E_PHASE6_DURABLE_MARKER'))
  expect((await request.get('/api/v1/health/live')).status()).toBe(200)
  expect((await request.get('/internal/readiness')).status()).toBe(404)
})

test('@phase6 failed release restores the previous compatible image', async ({ page, request }) => {
  await loginAdmin(page)
  await page.goto('/admin')
  await expect(page.getByTestId('release-version')).toHaveText(required('E2E_PHASE6_PREVIOUS_VERSION'))
  await page.goto('/admin/settings')
  await expect(page.getByLabel('站点公告')).toHaveValue(required('E2E_PHASE6_ACCEPTED_WRITE_MARKER'))
  expect((await request.get('/api/v1/health/live')).status()).toBe(200)
  expect((await request.get('/internal/readiness')).status()).toBe(404)
})

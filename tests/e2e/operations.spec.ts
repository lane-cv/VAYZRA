import { randomUUID } from 'node:crypto'
import {
  expect,
  test,
  type APIResponse,
  type Locator,
  type Response,
} from '@playwright/test'
import {
  changePassword,
  createStudentAPI,
  login,
} from './helpers'

const adminPassword = process.env.E2E_ADMIN_PASSWORD
const studentPassword = process.env.E2E_STUDENT_PASSWORD
const studentNewPassword = process.env.E2E_STUDENT_NEW_PASSWORD
const safeSettingsKeys = [
  'aiErrorCriticalPercent',
  'aiErrorWarningPercent',
  'auditRetentionDays',
  'authorizationDenialCriticalCount',
  'authorizationDenialWarningCount',
  'backupFilesystemCriticalPercent',
  'backupFilesystemWarningPercent',
  'backupHour',
  'backupMinute',
  'backupTimezone',
  'diskCriticalPercent',
  'diskWarningPercent',
  'infrastructure',
  'localBackupAgeCriticalHours',
  'localBackupAgeWarningHours',
  'loginFailureCriticalCount',
  'loginFailureWarningCount',
  'operationalSampleRetentionDays',
  'processingFailureCriticalCount',
  'processingFailureWarningCount',
  'processingQueueCritical',
  'processingQueueWarning',
  'siteAnnouncement',
  'siteName',
  'softDeleteRetentionDays',
  'updatedAt',
  'version',
].sort()
const infrastructureKeys = [
  'application_database',
  'redis_security',
  'object_store',
  'ai_encryption',
  'internal_metrics',
  'host_metrics_ingestion',
  'alert_webhook',
  'local_backup',
  'remote_backup',
]
const forbiddenOperationsField = /authorization|body|content|cookie|credential|object.?key|password|query|repository.?path|secret|token|url/i
const forbiddenAuditMetadata = /authorization|body|content|cookie|credential|filename|\bip\b|object.?key|password|prompt|query|secret|token|url/i

type SettingsEnvelope = {
  data: Record<string, unknown> & { version: number }
}

type AlertEnvelope = {
  data: {
    id: string
    state: string
  }
}

type AlertListEnvelope = {
  data: Array<{
    id: string
    state: string
  }>
}

test.beforeAll(() => {
  if (!adminPassword || !studentPassword || !studentNewPassword) {
    throw new Error('Phase 5 E2E admin and student credentials are required.')
  }
})

test.describe.configure({ mode: 'serial' })

function expectSecretFreeKeys(value: unknown): void {
  if (Array.isArray(value)) {
    for (const item of value) expectSecretFreeKeys(item)
    return
  }
  if (!value || typeof value !== 'object') return
  for (const [key, item] of Object.entries(value)) {
    expect(key).not.toMatch(forbiddenOperationsField)
    expectSecretFreeKeys(item)
  }
}

async function responseJSON<T>(response: APIResponse | Response): Promise<T> {
  expect(
    response.ok(),
    `request failed with ${response.status()} ${response.statusText()}`,
  ).toBe(true)
  return response.json() as Promise<T>
}

async function expectVerticalOrder(sections: Locator[]): Promise<void> {
  const positions = await Promise.all(sections.map(async (section) => {
    await expect(section).toBeVisible()
    return (await section.boundingBox())?.y
  }))
  expect(positions.every((position) => position !== undefined)).toBe(true)
  for (let index = 1; index < positions.length; index += 1) {
    expect(positions[index]!).toBeGreaterThan(positions[index - 1]!)
  }
}

test('@phase5 teacher manages operations without exposing secrets', async ({ page }) => {
  const dashboardResponse = page.waitForResponse((response) =>
    response.request().method() === 'GET'
    && response.url().endsWith('/api/v1/admin/operations/dashboard'))
  await login(page, 'admin', adminPassword!)
  await expect(page.getByRole('heading', { name: '运行仪表盘' })).toBeVisible()
  expectSecretFreeKeys(await (await dashboardResponse).json())
  await expect(page.getByTestId('dashboard-observed-at')).toHaveAttribute('datetime', /.+/)
  await expect(page.getByTestId('alert-summary')).toContainText('当前告警')
  await expect(page.getByTestId('backup-summary')).toContainText('备份与恢复')
  await expect(page.getByRole('heading', { name: '服务健康' })).toBeVisible()
  await expect(page.getByRole('region', { name: '运行摘要' })).toBeVisible()

  const settingsResponse = page.waitForResponse((response) =>
    response.request().method() === 'GET'
    && response.url().endsWith('/api/v1/admin/operations/settings'))
  await page.goto('/admin/settings')
  await expect(page.getByRole('heading', { name: '系统设置' })).toBeVisible()
  const initialSettings = await responseJSON<SettingsEnvelope>(await settingsResponse)
  expect(Object.keys(initialSettings.data).sort()).toEqual(safeSettingsKeys)
  expectSecretFreeKeys(initialSettings)
  const infrastructure = initialSettings.data.infrastructure as Array<Record<string, unknown>>
  expect(infrastructure.map((status) => status.key)).toEqual(infrastructureKeys)
  expect(infrastructure.every((status) => (
    Object.keys(status).sort().join(',') === 'configured,key,lastValidatedAt'
    && typeof status.configured === 'boolean'
    && (status.lastValidatedAt === null || typeof status.lastValidatedAt === 'string')
  ))).toBe(true)
  await expect(page.locator('input[type="password"]')).toHaveCount(0)
  const infrastructureSection = page.getByTestId('infrastructure-status')
  await expect(infrastructureSection).toBeVisible()
  await expect(infrastructureSection.getByTestId('infrastructure-row')).toHaveCount(9)
  await expect(infrastructureSection.locator('input, [title], [data-config], [data-source], [data-url], [data-path]')).toHaveCount(0)
  await expect(page.getByText(new RegExp(`^版本 ${initialSettings.data.version} ·`))).toBeVisible()

  const announcement = `Phase 5 acceptance ${randomUUID()}`
  await page.getByLabel('站点公告').fill(announcement)
  const updateResponse = page.waitForResponse((response) =>
    response.request().method() === 'PUT'
    && response.url().endsWith('/api/v1/admin/operations/settings'))
  const updateRequest = page.waitForRequest((request) =>
    request.method() === 'PUT'
    && request.url().endsWith('/api/v1/admin/operations/settings'))
  await page.getByRole('button', { name: '保存设置' }).click()
  const updatePayload = (await updateRequest).postDataJSON() as Record<string, unknown>
  expect(updatePayload).not.toHaveProperty('infrastructure')
  expect(updatePayload).not.toHaveProperty('updatedAt')
  const updatedSettings = await responseJSON<SettingsEnvelope>(await updateResponse)
  expect(updatedSettings.data.version).toBe(initialSettings.data.version + 1)
  expect(updatedSettings.data.siteAnnouncement).toBe(announcement)
  expect(Object.keys(updatedSettings.data).sort()).toEqual(safeSettingsKeys)
  expect((updatedSettings.data.infrastructure as Array<Record<string, unknown>>).map((status) => status.key)).toEqual(infrastructureKeys)
  expectSecretFreeKeys(updatedSettings)
  await expect(page.getByRole('status')).toContainText('系统设置已保存')
  await expect(page.getByText(new RegExp(`^版本 ${updatedSettings.data.version} ·`))).toBeVisible()

  await page.goto('/admin/audit')
  await page.getByTestId('audit-action').fill('operations.settings_updated')
  await page.getByTestId('audit-target-type').fill('system_settings')
  await page.getByTestId('audit-outcome').selectOption('succeeded')
  const auditResponse = page.waitForResponse((response) => {
    const url = new URL(response.url())
    return response.request().method() === 'GET'
      && url.pathname === '/api/v1/admin/operations/audit'
      && url.searchParams.get('action') === 'operations.settings_updated'
      && url.searchParams.get('targetType') === 'system_settings'
      && url.searchParams.get('outcome') === 'succeeded'
  })
  await page.getByRole('button', { name: '应用筛选' }).click()
  const auditPayload = await (await auditResponse).json()
  expectSecretFreeKeys(auditPayload)
  const auditRecord = page.getByTestId('audit-record').first()
  await expect(auditRecord).toBeVisible()
  await expect(auditRecord).toContainText('operations.settings_updated')
  await expect(auditRecord).not.toContainText(forbiddenAuditMetadata)

  await page.goto('/admin/alerts')
  await page.getByLabel('状态').selectOption('open')
  const filteredAlerts = page.waitForResponse((response) => {
    const url = new URL(response.url())
    return response.request().method() === 'GET'
      && url.pathname === '/api/v1/admin/operations/alerts'
      && url.searchParams.get('state') === 'open'
  })
  await page.getByRole('button', { name: '应用筛选' }).click()
  const openAlerts = await responseJSON<AlertListEnvelope>(await filteredAlerts)
  expectSecretFreeKeys(openAlerts)
  expect(openAlerts.data.some((alert) => alert.state === 'open')).toBe(true)
  const alertCard = page.getByTestId('alert-card').first()
  await expect(alertCard).toBeVisible()
  const alertID = await alertCard.getAttribute('data-id')
  expect(alertID).toMatch(/^[0-9a-f-]{36}$/)
  await alertCard.getByRole('button', { name: '确认告警' }).click()
  await expect(page.getByRole('dialog', { name: '确认这条告警？' })).toBeVisible()
  const acknowledgement = page.waitForResponse((response) =>
    response.request().method() === 'POST'
    && response.url().endsWith(`/api/v1/admin/operations/alerts/${alertID}/acknowledge`))
  await page.getByTestId('confirm-acknowledge').click()
  const acknowledged = await responseJSON<AlertEnvelope>(await acknowledgement)
  expect(acknowledged.data).toMatchObject({ id: alertID, state: 'acknowledged' })
  await page.getByLabel('状态').selectOption('acknowledged')
  const acknowledgedAlerts = page.waitForResponse((response) => {
    const url = new URL(response.url())
    return response.request().method() === 'GET'
      && url.pathname === '/api/v1/admin/operations/alerts'
      && url.searchParams.get('state') === 'acknowledged'
  })
  await page.getByRole('button', { name: '应用筛选' }).click()
  await acknowledgedAlerts
  const acknowledgedCard = page.locator(`[data-id="${alertID}"]`)
  await expect(acknowledgedCard).toContainText('已确认')
  await expect(acknowledgedCard).not.toContainText('已解决')
})

test('@phase5 student cannot access operations', async ({ page, request }) => {
  const suffix = randomUUID().slice(0, 8)
  await login(page, 'admin', adminPassword!)
  const student = await createStudentAPI(
    page,
    `phase5-ops-${suffix}`,
    'Phase 5 运维权限学生',
    studentPassword!,
  )
  await page.context().clearCookies()
  await login(page, student.username, studentPassword!)
  if (new URL(page.url()).pathname === '/change-password') {
    await changePassword(page, studentPassword!, `${studentNewPassword!} ops`)
  }

  for (const route of [
    '/admin',
    '/admin/settings',
    '/admin/audit',
    '/admin/alerts',
    '/admin/backups',
  ]) {
    await page.goto(route)
    await expect(page).toHaveURL(/\/student$/)
  }

  for (const route of [
    '/api/v1/admin/operations/dashboard',
    '/api/v1/admin/operations/settings',
    '/api/v1/admin/operations/audit',
    '/api/v1/admin/operations/alerts',
    '/api/v1/admin/operations/backups',
    '/api/v1/admin/operations/backups/53000000-0000-4000-8000-000000000001',
  ]) {
    expect((await page.request.get(route)).status(), route).toBe(403)
  }
  expect((await request.get('/internal/metrics')).status()).toBe(404)
})

test('@phase5-mobile operations remain usable on mobile', async ({ page }) => {
  await login(page, 'admin', adminPassword!)
  await expect(page.getByRole('heading', { name: '运行仪表盘' })).toBeVisible()
  await expectVerticalOrder([
    page.locator('[data-mobile-section="alerts"]'),
    page.locator('[data-mobile-section="backup"]'),
    page.locator('[data-mobile-section="services"]'),
    page.locator('[data-mobile-section="summaries"]'),
  ])
  const viewportWidth = await page.evaluate(() => document.documentElement.clientWidth)
  const overflowWidth = await page.evaluate(() => document.documentElement.scrollWidth)
  expect(overflowWidth).toBeLessThanOrEqual(viewportWidth)

  const menu = page.getByRole('button', { name: '打开导航' })
  await menu.click()
  await expect(menu).toHaveAttribute('aria-expanded', 'true')
  await expect(page.getByRole('link', { name: '仪表盘' })).toBeFocused()
  const settingsLink = page.getByRole('link', { name: '系统设置' })
  await settingsLink.click()
  await expect(page.getByRole('heading', { name: '系统设置' })).toBeVisible()
  const saveSettings = page.getByRole('button', { name: '保存设置' })
  await expect(saveSettings).toBeVisible()
  await saveSettings.focus()
  await expect(saveSettings).toBeFocused()

  await menu.click()
  await page.getByRole('link', { name: '告警中心' }).click()
  await expect(page.getByRole('heading', { name: '告警中心' })).toBeVisible()
  const stateFilter = page.getByLabel('状态')
  await expect(stateFilter).toBeVisible()
  await stateFilter.focus()
  await expect(stateFilter).toBeFocused()
  const alertCard = page.getByTestId('alert-card').first()
  await expect(alertCard).toBeVisible()
  await expect(alertCard.getByText('首次发现')).toBeVisible()
  await expect(alertCard.getByText('最近观测')).toBeVisible()
  await alertCard.focus()
  await expect(alertCard).toBeFocused()
})

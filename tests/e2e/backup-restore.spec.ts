import { expect, test, type APIResponse, type Response } from '@playwright/test'
import { csrfHeader, login } from './helpers'

const adminPassword = process.env.E2E_ADMIN_PASSWORD

type BackupRun = {
  id: string
  state: string
  trigger: string
}

type BackupListEnvelope = {
  data: BackupRun[]
}

type BackupDetailEnvelope = {
  data: BackupRun & {
    artifacts: Array<{ repository: 'local' | 'remote' }>
    restoreVerifications: Array<{ state: string }>
  }
}

test.beforeAll(() => {
  if (!adminPassword) throw new Error('Phase 5 E2E admin credentials are required.')
})

async function responseJSON<T>(response: APIResponse | Response): Promise<T> {
  expect(
    response.ok(),
    `request failed with ${response.status()} ${response.statusText()}`,
  ).toBe(true)
  return response.json() as Promise<T>
}

async function listBackups(page: Parameters<typeof csrfHeader>[0]): Promise<BackupListEnvelope> {
  return responseJSON<BackupListEnvelope>(
    await page.request.get('/api/v1/admin/operations/backups?limit=25'),
  )
}

test('@phase5 manual backup is idempotent and recovery evidence is read-only', async ({ page }) => {
  test.setTimeout(180_000)
  await login(page, 'admin', adminPassword!)
  await page.goto('/admin/backups')
  await expect(page.getByRole('heading', { name: '备份与恢复记录' })).toBeVisible()
  await page.getByRole('button', { name: '创建手动备份' }).click()
  await expect(page.getByRole('dialog', { name: '创建手动备份' })).toBeVisible()
  const queuedResponse = page.waitForResponse((response) =>
    response.request().method() === 'POST'
    && response.url().endsWith('/api/v1/admin/operations/backups'))
  await page.getByRole('button', { name: '确认创建' }).click()
  const firstResponse = await queuedResponse
  const idempotencyKey = firstResponse.request().headers()['idempotency-key']
  if (!idempotencyKey) throw new Error('manual backup request omitted its idempotency key')
  expect(idempotencyKey).toMatch(/^[A-Za-z0-9._:-]{8,128}$/)
  const first = await responseJSON<{ data: BackupRun }>(firstResponse)
  await expect(page.getByRole('status')).toContainText('手动备份已加入队列')

  const headers = {
    ...(await csrfHeader(page)),
    'Idempotency-Key': idempotencyKey,
  }
  const retried = await responseJSON<{ data: BackupRun }>(
    await page.request.post('/api/v1/admin/operations/backups', {
      data: {},
      headers,
    }),
  )
  expect(retried.data.id).toBe(first.data.id)
  expect(retried.data.trigger).toBe('manual')

  const history = await listBackups(page)
  expect(history.data.filter((run) => run.id === first.data.id)).toHaveLength(1)

  let evidenceIndex = -1
  let evidence: BackupDetailEnvelope['data'] | undefined
  for (const [index, run] of history.data.entries()) {
    const detail = await responseJSON<BackupDetailEnvelope>(
      await page.request.get(`/api/v1/admin/operations/backups/${run.id}`),
    )
    if (detail.data.restoreVerifications.some((verification) => verification.state === 'succeeded')) {
      evidenceIndex = index
      evidence = detail.data
      break
    }
  }
  expect(evidenceIndex, 'the Phase 5 fixture must provide successful restore verification evidence').toBeGreaterThanOrEqual(0)
  expect(evidence?.artifacts.some((artifact) => artifact.repository === 'local')).toBe(true)
  expect(evidence?.artifacts.some((artifact) => artifact.repository === 'remote')).toBe(true)

  await page.goto('/admin/backups')
  await expect(page.getByRole('heading', { name: '备份与恢复记录' })).toBeVisible()
  const cards = page.getByTestId('backup-card')
  await expect(cards).toHaveCount(history.data.length)
  await cards.nth(evidenceIndex).getByRole('button', { name: '查看恢复证据' }).click()
  const detailPanel = page.getByTestId('backup-detail')
  await expect(detailPanel).toContainText('本地')
  await expect(detailPanel).toContainText('远端')
  await expect(detailPanel.getByRole('heading', { name: '恢复演练成功' })).toBeVisible()
  await expect(detailPanel).toContainText('会话已撤销')
  await expect(detailPanel).toContainText(/恢复目标耗时：\d+ 秒/)
  await expect(page.getByRole('button', { name: /开始恢复|执行恢复|恢复备份|覆盖当前数据/ })).toHaveCount(0)
  await expect(page.getByRole('link', { name: /开始恢复|执行恢复|恢复备份|覆盖当前数据/ })).toHaveCount(0)
})

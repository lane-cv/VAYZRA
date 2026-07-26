import { randomUUID } from 'node:crypto'
import { expect, test } from '@playwright/test'
import {
  apiJSON,
  configureAIProvider,
  createStudentAPI,
  login,
  type AIProvider,
} from './helpers'

const adminPassword = process.env.E2E_ADMIN_PASSWORD
const studentPassword = process.env.E2E_STUDENT_PASSWORD
const providerBaseURL = process.env.E2E_AI_PROVIDER_BASE_URL ?? 'http://fake-ai:8090/v1'
const providerKey = process.env.E2E_AI_PROVIDER_KEY ?? 'e2e-provider-key'
const providerKeys = ['id', 'name', 'baseUrl', 'protocolMode', 'active', 'hasKey', 'keyUpdatedAt', 'version'].sort()

function safeProviderEnvelope(payload: unknown, secret: string): boolean {
  if (!payload || typeof payload !== 'object' || Array.isArray(payload)) return false
  const envelope = payload as Record<string, unknown>
  if (Object.keys(envelope).length !== 1 || !('data' in envelope)) return false
  if (!envelope.data || typeof envelope.data !== 'object' || Array.isArray(envelope.data)) return false
  const data = envelope.data as Record<string, unknown>
  if (Object.keys(data).sort().join('|') !== providerKeys.join('|')) return false
  const normalized = Object.keys(data).map((key) => key.toLowerCase().replace(/[-_]/g, ''))
  if (normalized.some((key) => ['apikey', 'encryptedapikey', 'ciphertext', 'objectkey'].includes(key))) return false
  return !Object.values(data).some((value) => typeof value === 'string' && value.includes(secret))
}

test.beforeAll(() => {
  if (!adminPassword || !studentPassword) {
    throw new Error('Phase 4 E2E admin and student credentials are required.')
  }
})

test.describe.configure({ mode: 'serial' })

test('admin creates, tests, and switches a provider without secret readback', async ({ page }) => {
  const suffix = randomUUID().slice(0, 8)
  const name = `浏览器供应商-${suffix}`
  await login(page, 'admin', adminPassword!)
  await page.goto('/admin/ai')
  await page.getByLabel('新建供应商').click()
  await page.getByLabel('供应商名称').fill(name)
  await page.getByLabel('供应商地址').fill(providerBaseURL)
  await page.getByLabel('协议模式').selectOption('responses')
  await page.getByLabel('API Key').fill(providerKey)
  const savedResponse = page.waitForResponse((response) =>
    response.request().method() === 'POST' && response.url().endsWith('/api/v1/admin/ai/providers'))
  await page.getByRole('button', { name: '保存供应商' }).click()
  await expect(page.getByRole('status')).toContainText('已安全保存')
  const savedPayload = await (await savedResponse).json()
  expect(safeProviderEnvelope(savedPayload, providerKey)).toBe(true)

  await page.reload()
  const card = page.locator('article').filter({ hasText: name })
  await expect(card).toContainText('Responses')
  await expect(card).toContainText('已安全保存')
  await card.getByLabel(`编辑 ${name}`).click()
  await expect(page.getByLabel('API Key')).toHaveCount(0)

  await page.getByLabel('协议模式').selectOption('chat_completions')
  const updateResponse = page.waitForResponse((response) =>
    response.request().method() === 'PUT' && /\/api\/v1\/admin\/ai\/providers\/[^/]+$/.test(response.url()))
  await page.getByRole('button', { name: '保存供应商' }).click()
  expect((await updateResponse).status()).toBe(200)
  await expect(card).toContainText('Chat Completions')

  page.once('dialog', async (dialog) => {
    expect(dialog.message()).toContain('少量费用')
    await dialog.accept()
  })
  const tested = page.waitForResponse((response) =>
    response.request().method() === 'POST' && /\/providers\/[^/]+\/test$/.test(response.url()))
  await card.getByLabel(`测试 ${name} 的连接`).click()
  expect((await tested).status()).toBe(200)
  await expect(page.getByRole('status')).toContainText('连接成功')

  const provider = (await apiJSON<AIProvider[]>(page, 'GET', '/api/v1/admin/ai/providers'))
    .find((item) => item.name === name)
  expect(provider).toBeTruthy()
  expect(provider!.hasKey).toBe(true)
})

test('admin saves text/vision routing, subject prompts, global/student limits, and usage filters', async ({ page }) => {
  const suffix = randomUUID().slice(0, 8)
  await login(page, 'admin', adminPassword!)
  const student = await createStudentAPI(page, `ai-config-${suffix}`, '额度验收学生', studentPassword!)
  await configureAIProvider(page, 'responses')
  await configureAIProvider(page, 'chat_completions')
  const providers = await apiJSON<AIProvider[]>(page, 'GET', '/api/v1/admin/ai/providers')
  const responsesProvider = providers.find((provider) => provider.name.includes('E2E AI responses'))!

  await page.goto('/admin/ai')
  const responsesCard = page.locator('article').filter({ hasText: responsesProvider.name })
  page.once('dialog', async (dialog) => {
    expect(dialog.message()).toContain('设为当前供应商')
    await dialog.accept()
  })
  const activationResponse = page.waitForResponse((response) =>
    response.request().method() === 'PUT' && response.url().endsWith('/api/v1/admin/ai/active-provider'))
  await responsesCard.getByLabel(`启用 ${responsesProvider.name}`).click()
  expect((await activationResponse).status()).toBe(200)
  await expect(responsesCard).toContainText('当前使用')

  await page.getByRole('tab', { name: '模型路由' }).click()
  await page.getByLabel('模型供应商').selectOption(responsesProvider.id)
  await expect(page.getByLabel('文本上游模型')).toHaveValue(/fixture-text-/)
  await expect(page.getByLabel('视觉上游模型')).toHaveValue(/fixture-vision-/)
  await page.getByLabel('文本输入价格').fill('0.001234')
  const textRoute = page.waitForResponse((response) =>
    response.request().method() === 'PUT' && response.url().includes('/models/'))
  await page.getByLabel('保存文本模型路由').click()
  expect((await textRoute).status()).toBe(200)
  await expect(page.getByRole('status')).toContainText('文本模型路由已保存')
  const visionValue = `fixture-vision-ui-${suffix}`
  await page.getByLabel('视觉上游模型').fill(visionValue)
  const visionRoute = page.waitForResponse((response) =>
    response.request().method() === 'PUT' && response.url().includes('/models/'))
  await page.getByLabel('保存视觉模型路由').click()
  expect((await visionRoute).status()).toBe(200)
  await expect(page.getByRole('status')).toContainText('视觉模型路由已保存')
  await page.reload()
  await page.getByRole('tab', { name: '模型路由' }).click()
  await page.getByLabel('模型供应商').selectOption(responsesProvider.id)
  await expect(page.getByLabel('文本输入价格')).toHaveValue('0.001234')
  await expect(page.getByLabel('视觉上游模型')).toHaveValue(visionValue)

  await page.getByRole('tab', { name: '提示词' }).click()
  await page.getByLabel('数学提示词').fill('数学验收提示词：逐步推导并检查结果。')
  await page.getByLabel('保存数学提示词').click()
  await expect(page.getByRole('status')).toContainText('数学提示词已保存')
  await page.getByLabel('物理提示词').fill('物理验收提示词：说明系统、定律和单位。')
  await page.getByLabel('保存物理提示词').click()
  await expect(page.getByRole('status')).toContainText('物理提示词已保存')

  await page.getByRole('tab', { name: '额度策略' }).click()
  await page.getByLabel('全局每日请求模式').selectOption('limit')
  await page.getByLabel('全局每日请求上限').fill('40')
  await page.getByLabel('全局每月请求模式').selectOption('limit')
  await page.getByLabel('全局每月请求上限').fill('400')
  await page.getByLabel('全局每日 Token模式').selectOption('limit')
  await page.getByLabel('全局每日 Token上限').fill('40000')
  await page.getByLabel('全局每月 Token模式').selectOption('limit')
  await page.getByLabel('全局每月 Token上限').fill('400000')
  await page.getByLabel('保存全局额度').click()
  await expect(page.getByRole('status')).toContainText('全局额度已保存')

  await page.getByLabel('搜索学生').fill(student.username)
  await page.getByLabel(`选择学生 ${student.username}`).click()
  await page.getByLabel('学生每日请求模式').selectOption('limit')
  await page.getByLabel('学生每日请求上限').fill('4')
  await page.getByLabel('保存学生额度').click()
  await expect(page.getByRole('status')).toContainText('学生额度已保存')

  await page.goto('/admin/ai-usage')
  await expect(page.getByRole('region', { name: '用量概览' })).toBeVisible()
  const filterResponse = page.waitForResponse((response) => {
    const url = new URL(response.url())
    return response.request().method() === 'GET'
      && url.pathname === '/api/v1/admin/ai/usage/summary'
      && url.searchParams.get('studentId') === student.id
      && url.searchParams.get('status') === 'failed'
  })
  await page.locator('select[name="studentId"]').selectOption(student.id)
  await page.locator('select[name="status"]').selectOption('failed')
  expect((await filterResponse).status()).toBe(200)
  await expect(page.getByRole('heading', { name: '用量结果' })).toBeVisible()
})

test('@phase4-mobile admin usage cards remain readable without horizontal-only controls', async ({ page }) => {
  await login(page, 'admin', adminPassword!)
  await page.goto('/admin/ai-usage')
  await expect(page.getByRole('heading', { name: '用量统计' })).toBeVisible()
  await expect(page.getByRole('region', { name: '用量概览' })).toBeVisible()
  const viewportWidth = await page.evaluate(() => document.documentElement.clientWidth)
  const overflowWidth = await page.evaluate(() => document.documentElement.scrollWidth)
  expect(overflowWidth).toBeLessThanOrEqual(viewportWidth)
  await expect(page.locator('.desktop-table')).toBeHidden()
  await expect(page.locator('form[aria-label="用量筛选"] select').first()).toBeVisible()
})

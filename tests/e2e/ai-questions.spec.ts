import { randomUUID } from 'node:crypto'
import { join } from 'node:path'
import { expect, test, type Browser, type Page } from '@playwright/test'
import {
  apiJSON,
  changePassword,
  configureAIProvider,
  createStudentAPI,
  csrfHeader,
  login,
  providerHitCounts,
  uploadAIFixture,
  waitForAIFile,
  waitForRunStatus,
  type AIRun,
} from './helpers'

const adminPassword = process.env.E2E_ADMIN_PASSWORD
const studentPassword = process.env.E2E_STUDENT_PASSWORD
const studentNewPassword = process.env.E2E_STUDENT_NEW_PASSWORD
const fixtureDir = process.env.E2E_FIXTURE_DIR ?? 'tests/fixtures/teaching/generated'

type AIThreadMutation = {
  thread?: { id: string; title: string; subject: 'math' | 'physics' }
  message?: { id: string }
  run: AIRun
  eventsUrl: string
}

test.beforeAll(() => {
  if (!adminPassword || !studentPassword || !studentNewPassword) {
    throw new Error('Phase 4 E2E credentials are required.')
  }
})

test.describe.configure({ mode: 'serial' })

async function provisionStudent(browser: Browser, label: string, protocol: 'chat_completions' | 'responses' = 'responses') {
  const suffix = `${label}-${randomUUID().slice(0, 8)}`
  const adminContext = await browser.newContext()
  const studentContext = await browser.newContext()
  const admin = await adminContext.newPage()
  await login(admin, 'admin', adminPassword!)
  await configureAIProvider(admin, protocol)
  const student = await createStudentAPI(admin, `ai-${suffix}`, `AI验收-${label}`, studentPassword!)
  const page = await studentContext.newPage()
  await login(page, student.username, studentPassword!)
  await changePassword(page, studentPassword!, `${studentNewPassword!}-${suffix}`)
  return { adminContext, studentContext, admin, page, student }
}

async function createAIThread(
  page: Page,
  title: string,
  body: string,
  attachments: Array<{ fileVersionId: string; sortPosition: number }> = [],
  subject: 'math' | 'physics' = 'math',
): Promise<AIThreadMutation> {
  return apiJSON<AIThreadMutation>(page, 'POST', '/api/v1/student/ai/threads', {
    title,
    subject,
    body,
    attachments,
  }, { 'Idempotency-Key': randomUUID() })
}

async function putStudentLimits(
  admin: Page,
  studentId: string,
  limits: {
    dailyRequests: { mode: 'disabled' | 'inherit' | 'limit'; value?: number }
    monthlyRequests: { mode: 'disabled' | 'inherit' | 'limit'; value?: number }
    dailyTokens: { mode: 'disabled' | 'inherit' | 'limit'; value?: number }
    monthlyTokens: { mode: 'disabled' | 'inherit' | 'limit'; value?: number }
  },
) {
  const current = await apiJSON<{
    students: Record<string, { version: number }>
  }>(admin, 'GET', '/api/v1/admin/ai/limits')
  return apiJSON(admin, 'PUT', `/api/v1/admin/ai/limits/students/${studentId}`, {
    ...limits,
    expectedVersion: current.students[studentId]?.version ?? 0,
  })
}

const inheritedLimits = {
  dailyRequests: { mode: 'inherit' as const },
  monthlyRequests: { mode: 'inherit' as const },
  dailyTokens: { mode: 'inherit' as const },
  monthlyTokens: { mode: 'inherit' as const },
}

test('unified teacher and AI flow streams safe math, follows up, and deduplicates submission', async ({ browser }) => {
  test.setTimeout(300_000)
  const setup = await provisionStudent(browser, 'unified')
  const { adminContext, studentContext, page } = setup
  try {
    await page.goto('/student/questions')
    await expect(page.getByText('还没有符合条件的问题。')).toBeVisible()

    await page.goto('/student/questions/new')
    await page.getByRole('radio', { name: /老师答疑/ }).check()
    await page.getByLabel('问题标题').fill('合成老师问题')
    await page.getByLabel('问题描述').fill('这是一条不包含真实学生数据的老师答疑验收问题。')
    await page.getByRole('button', { name: '提交问题' }).click()
    await expect(page).toHaveURL(/\/student\/questions\/teacher\/[0-9a-f-]+$/)

    const before = await providerHitCounts(page)
    await page.goto('/student/questions/new')
    await page.getByRole('radio', { name: /AI 答疑/ }).check()
    await page.getByRole('radio', { name: '数学', exact: true }).check()
    await page.getByLabel('问题标题').fill('合成数学问题')
    await page.getByLabel('问题描述').fill('[case:success] 求解 x+1=3，并检查结果。')

    let committed: AIThreadMutation | undefined
    const idempotencyKeys: string[] = []
    await page.route('**/api/v1/student/ai/threads', async (route) => {
      if (route.request().method() !== 'POST') {
        await route.continue()
        return
      }
      idempotencyKeys.push(route.request().headers()['idempotency-key'] ?? '')
      if (idempotencyKeys.length === 1) {
        const response = await route.fetch()
        expect(response.status()).toBe(201)
        committed = ((await response.json()) as { data: AIThreadMutation }).data
        await route.abort('failed')
        return
      }
      await route.continue()
    })
    await page.getByRole('button', { name: '提交问题' }).click()
    await expect(page.getByRole('alert')).toContainText('网络连接异常')
    const retryResponse = page.waitForResponse((response) =>
      response.request().method() === 'POST' && response.url().endsWith('/api/v1/student/ai/threads'))
    await page.getByRole('button', { name: '提交问题' }).click()
    const retried = ((await (await retryResponse).json()) as { data: AIThreadMutation }).data
    await page.unroute('**/api/v1/student/ai/threads')

    expect(idempotencyKeys).toHaveLength(2)
    expect(idempotencyKeys[0]).toBeTruthy()
    expect(new Set(idempotencyKeys).size).toBe(1)
    expect(retried.thread?.id).toBe(committed?.thread?.id)
    expect(retried.run.id).toBe(committed?.run.id)
    await waitForRunStatus(page, retried.run.id, 'succeeded')
    const afterDuplicate = await providerHitCounts(page)
    expect((afterDuplicate['responses.success'] ?? 0) - (before['responses.success'] ?? 0)).toBe(1)
    await page.reload()
    await expect(page.getByLabel('生成状态：回答完成')).toBeVisible()
    await expect(page.locator('.final-answer .katex')).toBeVisible()
    await expect(page.locator('.final-answer script, .final-answer iframe, .final-answer img, .final-answer a')).toHaveCount(0)

    await page.getByLabel('AI 追问内容').fill('请用代入法继续检查这个合成答案。')
    const followup = page.waitForResponse((response) =>
      response.request().method() === 'POST' && /\/student\/ai\/threads\/[^/]+\/messages$/.test(response.url()))
    await page.getByRole('button', { name: '提交追问' }).click()
    const followupMutation = ((await (await followup).json()) as { data: AIThreadMutation }).data
    await waitForRunStatus(page, followupMutation.run.id, 'succeeded')
    await page.reload()
    await expect(page.getByLabel('AI 答疑对话')).toContainText('请用代入法继续检查')
    await expect(page.getByLabel('AI 答疑对话').getByText('AI 助教')).toHaveCount(2)

    await page.getByRole('link', { name: '← 返回答疑中心' }).click()
    const teacher = page.getByRole('link', { name: /合成老师问题/ })
    const ai = page.getByRole('link', { name: /合成数学问题/ })
    await expect(teacher).toContainText('老师')
    await expect(ai).toContainText('AI')

    const after = await providerHitCounts(page)
    expect((after['responses.success'] ?? 0) - (before['responses.success'] ?? 0)).toBe(2)
  } finally {
    await studentContext.close()
    await adminContext.close()
  }
})

test('vision and extracted PDF/DOCX route to the intended models', async ({ browser }) => {
  test.setTimeout(420_000)
  const setup = await provisionStudent(browser, 'attachments', 'chat_completions')
  const { adminContext, studentContext, admin, page, student } = setup
  try {
    const fixtures = [
      ['question.png', 'image/png', 'vision'] as const,
      ['question.pdf', 'application/pdf', 'text'] as const,
      ['lesson.docx', 'application/vnd.openxmlformats-officedocument.wordprocessingml.document', 'text'] as const,
    ]
    const runIDs: Array<{ id: string; modality: 'vision' | 'text' }> = []
    for (const [name, mime, modality] of fixtures) {
      const upload = await uploadAIFixture(page, join(fixtureDir, name), mime)
      const status = await waitForAIFile(page, upload.fileVersionId)
      expect(status.processingState).toBe('ready')
      if (modality === 'text') expect(status.previewAvailable).toBe(true)
      const mutation = await createAIThread(
        page,
        `合成附件-${name}`,
        `[case:success] 只分析这个合成 ${name} 附件。`,
        [{ fileVersionId: upload.fileVersionId, sortPosition: 0 }],
        'physics',
      )
      await waitForRunStatus(page, mutation.run.id, 'succeeded')
      runIDs.push({ id: mutation.run.id, modality })
    }

    const usage = await apiJSON<Array<{ id: string; studentId: string; modelLabel: string }>>(
      admin,
      'GET',
      `/api/v1/admin/ai/usage/runs?studentId=${student.id}&limit=100`,
    )
    for (const run of runIDs) {
      const row = usage.find((item) => item.id === run.id)
      expect(row?.modelLabel).toContain(run.modality === 'vision' ? 'fixture-vision-' : 'fixture-text-')
    }
  } finally {
    await studentContext.close()
    await adminContext.close()
  }
})

test('refresh and explicit reconnect resume one run without another provider request', async ({ browser }) => {
  test.setTimeout(240_000)
  const setup = await provisionStudent(browser, 'resume')
  const { adminContext, studentContext, page } = setup
  try {
    const before = await providerHitCounts(page)
    const mutation = await createAIThread(page, '合成恢复问题', '[case:slow-first-byte] 验证刷新恢复同一次运行。')
    await page.goto(`/student/questions/ai/${mutation.thread!.id}`)
    await expect(page.getByLabel(/生成状态：/)).toBeVisible()
    await page.reload()
    await expect(page.getByRole('heading', { name: '合成恢复问题' })).toBeVisible()

    let interrupted = false
    await page.route(`**/api/v1/student/ai/runs/${mutation.run.id}/events*`, async (route) => {
      if (!interrupted) {
        interrupted = true
        await route.abort('failed')
        return
      }
      await route.continue()
    })
    await page.reload()
    await expect(page.getByLabel('生成状态：连接中断')).toBeVisible()
    await page.getByLabel('重新连接回答').click()
    await waitForRunStatus(page, mutation.run.id, 'succeeded')
    await page.unroute(`**/api/v1/student/ai/runs/${mutation.run.id}/events*`)
    const after = await providerHitCounts(page)
    expect((after['responses.slow-first-byte'] ?? 0) - (before['responses.slow-first-byte'] ?? 0)).toBe(1)
  } finally {
    await studentContext.close()
    await adminContext.close()
  }
})

test('provider failures, cancellation, retry, busy, attachment, context, and quota branches remain explicit', async ({ browser }) => {
  test.setTimeout(600_000)
  const setup = await provisionStudent(browser, 'failures')
  const { adminContext, studentContext, admin, page, student } = setup
  try {
    for (const [marker, expected] of [
      ['429', /rate|429/i],
      ['500', /upstream|500/i],
      ['disconnect-after-delta', /interrupt|stream/i],
    ] as const) {
      const mutation = await createAIThread(page, `合成失败-${marker}`, `[case:${marker}] 触发确定性失败。`)
      const failed = await waitForRunStatus(page, mutation.run.id, 'failed')
      expect(failed.errorCode).toMatch(expected)
    }

    const estimatedMutation = await createAIThread(page, '合成缺失用量', '[case:no-usage] 验证安全估算。')
    const estimated = await waitForRunStatus(page, estimatedMutation.run.id, 'succeeded')
    expect(estimated.usage?.source).toBe('estimated')

    const settlementStudent = await createStudentAPI(
      admin,
      `ai-settlement-${randomUUID().slice(0, 8)}`,
      '额度结算验收学生',
      studentPassword!,
    )
    const settlementContext = await browser.newContext()
    const settlementPage = await settlementContext.newPage()
    try {
      await login(settlementPage, settlementStudent.username, studentPassword!)
      await changePassword(
        settlementPage,
        studentPassword!,
        `${studentNewPassword!}-settlement-${randomUUID().slice(0, 8)}`,
      )
      await putStudentLimits(admin, settlementStudent.id, {
        ...inheritedLimits,
        dailyRequests: { mode: 'limit', value: 1 },
      })
      const cancellable = await createAIThread(
        settlementPage,
        '合成取消重试',
        '[case:slow-first-byte] 取消后释放硬额度。',
      )
      await settlementPage.goto(`/student/questions/ai/${cancellable.thread!.id}`)
      await settlementPage.getByLabel('停止生成').click()
      const cancelled = await waitForRunStatus(settlementPage, cancellable.run.id, 'cancelled')
      expect(cancelled.usage).toBeUndefined()
      const retryResponsePromise = settlementPage.waitForResponse((response) =>
        response.request().method() === 'POST' && /\/runs\/[^/]+\/retries$/.test(response.url()))
      await settlementPage.getByLabel('重试生成').click()
      const retryResponse = await retryResponsePromise
      const retried = ((await retryResponse.json()) as { data: AIThreadMutation }).data
      expect(retried.run.id).not.toBe(cancellable.run.id)
      const succeededRetry = await waitForRunStatus(settlementPage, retried.run.id, 'succeeded')
      expect(succeededRetry.usage?.source).toBe('provider')

      const exhausted = await settlementPage.request.post('/api/v1/student/ai/threads', {
        headers: { ...(await csrfHeader(settlementPage)), 'Idempotency-Key': randomUUID() },
        data: { title: '合成结算后限额', subject: 'math', body: '成功重试只结算一次。', attachments: [] },
      })
      expect(exhausted.status()).toBe(429)
      expect((await exhausted.json()).error.code).toBe('QUOTA_EXCEEDED')
      const settledRuns = await apiJSON<Array<{ id: string; usageSource: string }>>(
        admin,
        'GET',
        `/api/v1/admin/ai/usage/runs?studentId=${settlementStudent.id}&limit=100`,
      )
      expect(settledRuns.filter((run) => run.id === cancellable.run.id)).toHaveLength(1)
      expect(settledRuns.filter((run) => run.id === retried.run.id)).toEqual([
        expect.objectContaining({ usageSource: 'upstream' }),
      ])
    } finally {
      await settlementContext.close()
    }

    const busy = await createAIThread(page, '合成并发限制', '[case:slow-first-byte] 保持一次运行占用。')
    const busyResponse = await page.request.post(`/api/v1/student/ai/threads/${busy.thread!.id}/messages`, {
      headers: { ...(await csrfHeader(page)), 'Idempotency-Key': randomUUID() },
      data: { body: '并发追问应被拒绝', attachments: [] },
    })
    expect(busyResponse.status()).toBe(409)
    expect((await busyResponse.json()).error.code).toBe('AI_BUSY')
    await apiJSON(page, 'POST', `/api/v1/student/ai/runs/${busy.run.id}/cancel`, {})

    const pending = await uploadAIFixture(
      page,
      join(fixtureDir, 'lesson.docx'),
      'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
    )
    const pendingResponse = await page.request.post('/api/v1/student/ai/threads', {
      headers: { ...(await csrfHeader(page)), 'Idempotency-Key': randomUUID() },
      data: {
        title: '合成待处理附件',
        subject: 'math',
        body: '附件未完成 ai_text 时不能开始。',
        attachments: [{ fileVersionId: pending.fileVersionId, sortPosition: 0 }],
      },
    })
    expect(pendingResponse.status()).toBe(409)
    expect((await pendingResponse.json()).error.code).toBe('ATTACHMENT_NOT_READY')

    const providers = await apiJSON<Array<AIProviderWithVersion>>(admin, 'GET', '/api/v1/admin/ai/providers')
    const active = providers.find((provider) => provider.active)!
    const models = await apiJSON<Array<AIModelWithVersion>>(
      admin,
      'GET',
      `/api/v1/admin/ai/providers/${active.id}/models`,
    )
    const textModel = models.find((model) => model.modality === 'text')!
    await apiJSON(admin, 'PUT', `/api/v1/admin/ai/providers/${active.id}/models/${textModel.id}`, {
      ...modelWrite(textModel),
      contextTokens: 16,
      maxOutputTokens: 8,
      expectedVersion: textModel.version,
    })
    const contextResponse = await page.request.post('/api/v1/student/ai/threads', {
      headers: { ...(await csrfHeader(page)), 'Idempotency-Key': randomUUID() },
      data: {
        title: '合成上下文过长',
        subject: 'math',
        body: '很长的合成问题 '.repeat(100),
        attachments: [],
      },
    })
    expect(contextResponse.status()).toBe(422)
    expect((await contextResponse.json()).error.code).toBe('CONTEXT_TOO_LARGE')

    await putStudentLimits(admin, student.id, {
      ...inheritedLimits,
      dailyRequests: { mode: 'disabled' },
    })
    const disabled = await page.request.post('/api/v1/student/ai/threads', {
      headers: { ...(await csrfHeader(page)), 'Idempotency-Key': randomUUID() },
      data: { title: '合成停用', subject: 'math', body: 'AI 应停用。', attachments: [] },
    })
    expect(disabled.status()).toBe(503)
    expect((await disabled.json()).error.code).toBe('AI_DISABLED')

    await putStudentLimits(admin, student.id, {
      ...inheritedLimits,
      dailyRequests: { mode: 'limit', value: 1 },
      dailyTokens: { mode: 'limit', value: 1 },
    })
    const quota = await page.request.post('/api/v1/student/ai/threads', {
      headers: { ...(await csrfHeader(page)), 'Idempotency-Key': randomUUID() },
      data: { title: '合成额度不足', subject: 'math', body: 'Token 预留超过额度。', attachments: [] },
    })
    expect(quota.status()).toBe(429)
    expect((await quota.json()).error.code).toBe('QUOTA_EXCEEDED')

    const summary = await apiJSON<{
      succeeded: number
      failed: number
      unknownUsage: number
    }>(admin, 'GET', `/api/v1/admin/ai/usage/summary?studentId=${student.id}`)
    expect(summary.succeeded).toBeGreaterThanOrEqual(1)
    expect(summary.failed).toBeGreaterThanOrEqual(4)
    expect(summary.unknownUsage).toBeGreaterThanOrEqual(4)
  } finally {
    await studentContext.close()
    await adminContext.close()
  }
})

type AIProviderWithVersion = { id: string; active: boolean }
type AIModelWithVersion = {
  id: string
  upstreamModelId: string
  modality: 'text' | 'vision'
  contextTokens: number
  maxOutputTokens: number
  imageQuotaTokens: number
  inputPriceMicroUsd: number
  outputPriceMicroUsd: number
  connectTimeoutMs: number
  responseHeaderTimeoutMs: number
  idleStreamTimeoutMs: number
  totalTimeoutMs: number
  enabled: boolean
  version: number
}

function modelWrite(model: AIModelWithVersion) {
  return {
    upstreamModelId: model.upstreamModelId,
    modality: model.modality,
    contextTokens: model.contextTokens,
    maxOutputTokens: model.maxOutputTokens,
    imageQuotaTokens: model.imageQuotaTokens,
    inputPriceMicroUsd: model.inputPriceMicroUsd,
    outputPriceMicroUsd: model.outputPriceMicroUsd,
    connectTimeoutMs: model.connectTimeoutMs,
    responseHeaderTimeoutMs: model.responseHeaderTimeoutMs,
    idleStreamTimeoutMs: model.idleStreamTimeoutMs,
    totalTimeoutMs: model.totalTimeoutMs,
    enabled: model.enabled,
    clearQuotaBlock: false,
  }
}

test('@phase4-mobile unified list, channel cards, detail back focus, and streaming status are accessible', async ({ browser }) => {
  test.setTimeout(240_000)
  const setup = await provisionStudent(browser, 'mobile')
  const { adminContext, studentContext, page } = setup
  try {
    const mutation = await createAIThread(page, '合成移动问题', '[case:slow-first-byte] 移动端状态播报。')
    await page.goto('/student/questions')
    const link = page.getByRole('link', { name: /合成移动问题/ })
    await link.focus()
    await link.press('Enter')
    await expect(page.getByLabel(/生成状态：/)).toBeVisible()
    await expect(page.locator('[aria-live="polite"]')).toBeAttached()
    await page.getByRole('link', { name: '← 返回答疑中心' }).press('Enter')
    await expect(link).toBeFocused()

    await page.getByRole('link', { name: '提出新问题' }).click()
    await expect(page.getByRole('radio', { name: /AI 答疑/ })).toBeVisible()
    await expect(page.getByRole('radio', { name: /老师答疑/ })).toBeVisible()
    const viewportWidth = await page.evaluate(() => document.documentElement.clientWidth)
    const overflowWidth = await page.evaluate(() => document.documentElement.scrollWidth)
    expect(overflowWidth).toBeLessThanOrEqual(viewportWidth)
    await apiJSON(page, 'POST', `/api/v1/student/ai/runs/${mutation.run.id}/cancel`, {})
  } finally {
    await studentContext.close()
    await adminContext.close()
  }
})

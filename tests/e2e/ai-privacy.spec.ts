import { randomUUID } from 'node:crypto'
import { join } from 'node:path'
import { expect, test, type APIResponse } from '@playwright/test'
import {
  apiJSON,
  changePassword,
  configureAIProvider,
  createStudentAPI,
  csrfHeader,
  login,
  uploadAIFixture,
  waitForAIFile,
  waitForRunStatus,
  type AIRun,
} from './helpers'

const adminPassword = process.env.E2E_ADMIN_PASSWORD
const studentPassword = process.env.E2E_STUDENT_PASSWORD
const studentNewPassword = process.env.E2E_STUDENT_NEW_PASSWORD
const fixtureDir = process.env.E2E_FIXTURE_DIR ?? 'tests/fixtures/teaching/generated'

type AIMutation = {
  thread: { id: string }
  message: { id: string }
  run: AIRun
  eventsUrl: string
}

function hasRestrictedSerialization(serialized: string, secret: string, teacherNote: string): boolean {
  if (serialized.includes(secret) || serialized.includes(teacherNote)) return true
  return /(api[-_]?key|encrypted[-_]?api[-_]?key|cipher[-_]?text|object[-_]?key)/i.test(serialized)
}

test.beforeAll(() => {
  if (!adminPassword || !studentPassword || !studentNewPassword) {
    throw new Error('Phase 4 E2E credentials are required.')
  }
})

test('two students receive uniform 404s and AI data never enters teacher queue or student serialization', async ({ browser }) => {
  test.setTimeout(360_000)
  const suffix = randomUUID().slice(0, 8)
  const syntheticSecret = process.env.E2E_AI_PROVIDER_KEY ?? 'e2e-provider-key'
  const syntheticTeacherNote = `仅教师合成备注-${suffix}`
  const adminContext = await browser.newContext()
  const studentAContext = await browser.newContext()
  const studentBContext = await browser.newContext()
  const admin = await adminContext.newPage()
  const pageA = await studentAContext.newPage()
  const pageB = await studentBContext.newPage()
  try {
    await login(admin, 'admin', adminPassword!)
    await configureAIProvider(admin, 'responses')
    const studentA = await createStudentAPI(admin, `privacy-a-${suffix}`, '隐私验收甲', studentPassword!)
    const studentB = await createStudentAPI(admin, `privacy-b-${suffix}`, '隐私验收乙', studentPassword!)
    await login(pageA, studentA.username, studentPassword!)
    await changePassword(pageA, studentPassword!, `${studentNewPassword!}-privacy-a-${suffix}`)
    await login(pageB, studentB.username, studentPassword!)
    await changePassword(pageB, studentPassword!, `${studentNewPassword!}-privacy-b-${suffix}`)

    const uploaded = await uploadAIFixture(pageA, join(fixtureDir, 'question.png'), 'image/png')
    await waitForAIFile(pageA, uploaded.fileVersionId)
    const ai = await apiJSON<AIMutation>(pageA, 'POST', '/api/v1/student/ai/threads', {
      title: `合成私有 AI 问题-${suffix}`,
      subject: 'physics',
      body: '[case:success] 仅用于隔离验收。',
      attachments: [{ fileVersionId: uploaded.fileVersionId, sortPosition: 0 }],
    }, { 'Idempotency-Key': randomUUID() })
    await waitForRunStatus(pageA, ai.run.id, 'succeeded')

    const teacher = await apiJSON<{
      thread: { id: string }
    }>(pageA, 'POST', '/api/v1/student/questions', {
      title: `合成老师隐私问题-${suffix}`,
      body: '仅用于教师备注隔离验收。',
      attachments: [],
    }, { 'Idempotency-Key': randomUUID() })
    await apiJSON(admin, 'POST', `/api/v1/admin/questions/${teacher.thread.id}/notes`, {
      body: syntheticTeacherNote,
    })

    const foreignPaths = [
      `/api/v1/student/ai/threads/${ai.thread.id}`,
      `/api/v1/student/ai/runs/${ai.run.id}/events?afterSequence=0`,
      `/api/v1/ai-question-files/${uploaded.fileVersionId}/status`,
      `/api/v1/ai-question-files/${uploaded.fileVersionId}/preview`,
      `/api/v1/student/ai/threads/${randomUUID()}`,
      `/api/v1/student/ai/runs/${randomUUID()}/events?afterSequence=0`,
      `/api/v1/ai-question-files/${randomUUID()}/preview`,
    ]
    let canonical404: { status: number; code: string; message: string } | undefined
    for (const path of foreignPaths) {
      const response = await pageB.request.get(path, { timeout: 5_000 })
      const shape = await notFoundShape(response)
      canonical404 ??= shape
      expect(shape, path).toEqual(canonical404)
    }
    expect(await apiJSON<unknown[]>(pageB, 'GET', '/api/v1/student/ai/threads?limit=100')).toEqual([])
    expect(JSON.stringify(await apiJSON(pageB, 'GET', '/api/v1/student/questions?limit=100'))).not.toContain(ai.thread.id)

    for (const path of [
      '/api/v1/admin/ai/providers',
      '/api/v1/admin/ai/prompts',
      '/api/v1/admin/ai/limits',
      '/api/v1/admin/ai/usage/summary',
      '/api/v1/admin/ai/usage/runs',
    ]) {
      expect((await pageA.request.get(path)).status(), path).toBe(403)
    }
    const forbiddenMutation = await pageA.request.put('/api/v1/admin/ai/limits/global', {
      headers: await csrfHeader(pageA),
      data: {
        dailyRequests: { mode: 'disabled' },
        monthlyRequests: { mode: 'disabled' },
        dailyTokens: { mode: 'disabled' },
        monthlyTokens: { mode: 'disabled' },
        expectedVersion: 1,
      },
    })
    expect(forbiddenMutation.status()).toBe(403)
    await pageA.goto('/admin/ai')
    await expect(pageA).toHaveURL(/\/student$/)
    await pageA.goto('/admin/ai-usage')
    await expect(pageA).toHaveURL(/\/student$/)

    const teacherQueue = await apiJSON<Array<{ id: string; title: string }>>(
      admin,
      'GET',
      '/api/v1/admin/questions?limit=100',
    )
    expect(teacherQueue.some((item) => item.id === ai.thread.id)).toBe(false)
    expect(teacherQueue.some((item) => item.title === `合成私有 AI 问题-${suffix}`)).toBe(false)
    await admin.goto('/admin/questions')
    await expect(admin.getByText(`合成私有 AI 问题-${suffix}`, { exact: true })).toHaveCount(0)

    let leaked = false
    const inspections: Promise<void>[] = []
    pageA.on('response', (response) => {
      const contentType = response.headers()['content-type'] ?? ''
      if (!contentType.includes('json') && !contentType.includes('html')) return
      inspections.push(response.body().then((body) => {
        const serialized = body.toString('utf8')
        leaked ||= hasRestrictedSerialization(serialized, syntheticSecret, syntheticTeacherNote)
      }).catch(() => undefined))
    })
    await pageA.goto(`/student/questions/teacher/${teacher.thread.id}`)
    await expect(pageA.getByRole('heading', { name: `合成老师隐私问题-${suffix}` })).toBeVisible()
    await pageA.goto(`/student/questions/ai/${ai.thread.id}`)
    await expect(pageA.getByRole('heading', { name: `合成私有 AI 问题-${suffix}` })).toBeVisible()
    await Promise.all(inspections)
    const html = await pageA.content()
    leaked ||= hasRestrictedSerialization(html, syntheticSecret, syntheticTeacherNote)
    expect(leaked).toBe(false)
  } finally {
    await studentBContext.close()
    await studentAContext.close()
    await adminContext.close()
  }
})

async function notFoundShape(response: APIResponse) {
  expect(response.status()).toBe(404)
  const payload = await response.json() as {
    error: { code: string; message: string }
  }
  return {
    status: response.status(),
    code: payload.error.code,
    message: payload.error.message,
  }
}

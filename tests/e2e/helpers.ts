import { createHash, randomUUID } from 'node:crypto'
import { readFile } from 'node:fs/promises'
import { basename } from 'node:path'
import { expect, type APIResponse, type Page } from '@playwright/test'

export async function login(page: Page, username: string, password: string) {
  await page.goto('/login')
  await page.getByLabel('账号').fill(username)
  await page.getByLabel('密码').fill(password)
  await page.getByRole('button', { name: '登录' }).click()
  await expect(page).not.toHaveURL(/login/)
}

export async function changePassword(page: Page, currentPassword: string, newPassword: string) {
  await expect(page).toHaveURL(/change-password/)
  await page.getByLabel('当前密码').fill(currentPassword)
  await page.getByLabel('新密码', { exact: true }).fill(newPassword)
  await page.getByLabel('确认新密码').fill(newPassword)
  await page.getByRole('button', { name: '保存新密码' }).click()
  await expect(page).not.toHaveURL(/change-password/, { timeout: 15_000 })
}

export async function createStudent(page: Page, username: string, displayName: string, temporaryPassword: string) {
  await page.getByRole('button', { name: '创建学生' }).click()
  await page.getByLabel('学生账号').fill(username)
  await page.getByLabel('学生姓名').fill(displayName)
  await page.getByLabel('临时密码').fill(temporaryPassword)
  await page.getByRole('button', { name: '创建学生', exact: true }).last().click()
  await expect(page.getByText(username, { exact: true })).toBeVisible()
}

export async function csrfHeader(page: Page): Promise<Record<string, string>> {
  const cookies = await page.context().cookies()
  const csrf = cookies.find((cookie) => cookie.name === 'hl_csrf')?.value
  if (!csrf) throw new Error('missing CSRF cookie')
  return { 'X-CSRF-Token': csrf, Origin: new URL(page.url()).origin }
}

export async function apiJSON<T>(page: Page, method: string, path: string, data?: unknown, headers: Record<string, string> = {}): Promise<T> {
  const response = await page.request.fetch(path, { method, data, headers: { ...(await csrfHeader(page)), ...headers } })
  await expect(response, `${method} ${path}`).toBeOK()
  if (response.status() === 204) return undefined as T
  return (await response.json()).data as T
}

export type StudentRecord = { id: string; username: string; displayName: string; status: 'active' | 'disabled' }
export async function createStudentAPI(page: Page, username: string, displayName: string, temporaryPassword: string) {
  return apiJSON<StudentRecord>(page, 'POST', '/api/v1/admin/students', { username, displayName, temporaryPassword })
}

type CatalogNode = { id: string; kind: string; name: string }
export type Draft = { lessonId: string; chapterId: string; title: string; summary: string; bodyMarkdown: string; sortKey: number; lockVersion: number; audience: { mode: 'all' | 'selected'; userIds: string[] }; externalVideos: unknown[] }
export async function createTeachingPath(page: Page, suffix: string) {
  let parentId = ''
  const names = [`高一-${suffix}`, `上学期-${suffix}`, `物理-${suffix}`, `力学-${suffix}`]
  for (const [index, kind] of ['grade', 'term', 'subject', 'chapter'].entries()) {
    const node = await apiJSON<CatalogNode>(page, 'POST', `/api/v1/admin/catalog/${kind}`, { parentId, name: names[index], description: '', sortKey: 10 })
    parentId = node.id
  }
  const draft = await apiJSON<Draft>(page, 'POST', '/api/v1/admin/lessons', { chapterId: parentId, title: `牛顿定律-${suffix}` })
  return { chapterId: parentId, draft, names }
}

export async function createTeachingPathUI(page: Page, suffix: string) {
  await page.goto('/admin/teaching')
  const names = [`高一-${suffix}`, `上学期-${suffix}`, `物理-${suffix}`, `力学-${suffix}`, `牛顿定律-${suffix}`]
  const kinds = ['年级', '学期', '学科', '章节', '课程']
  for (let index = 0; index < names.length; index += 1) {
    if (index > 0) await page.getByRole('treeitem', { name: names[index - 1], exact: true }).click()
    await page.getByRole('button', { name: `创建${kinds[index]}`, exact: true }).last().click()
    await page.getByLabel(index === 4 ? '课程名称' : '目录名称').fill(names[index])
    await page.getByRole('button', { name: '确认', exact: true }).click()
    await expect(page.getByRole('treeitem', { name: names[index], exact: true })).toBeVisible()
  }
  await page.getByRole('treeitem', { name: names[4], exact: true }).click()
  await page.getByRole('link', { name: `编辑课程 ${names[4]}` }).click()
  await expect(page).toHaveURL(/\/admin\/teaching\/lessons\/[^/]+$/)
  const lessonId = new URL(page.url()).pathname.split('/').pop()!
  const detail = await apiJSON<{ draft: Draft }>(page, 'GET', `/api/v1/admin/lessons/${lessonId}`)
  return { lessonId, draft: detail.draft, names }
}

export async function saveDraft(page: Page, draft: Draft, patch: Partial<Omit<Draft, 'lessonId' | 'chapterId' | 'lockVersion'>>) {
  return apiJSON<Draft>(page, 'PUT', `/api/v1/admin/lessons/${draft.lessonId}/draft`, {
    title: patch.title ?? draft.title,
    summary: patch.summary ?? draft.summary,
    bodyMarkdown: patch.bodyMarkdown ?? draft.bodyMarkdown,
    sortKey: patch.sortKey ?? draft.sortKey,
    audience: patch.audience ?? draft.audience,
    externalVideos: patch.externalVideos ?? draft.externalVideos,
  }, { 'If-Match': String(draft.lockVersion) })
}

export async function publishDraft(page: Page, draft: Draft) {
  return apiJSON<{ id: string; lessonId: string; version: number }>(page, 'POST', `/api/v1/admin/lessons/${draft.lessonId}/publish`, undefined, { 'If-Match': String(draft.lockVersion) })
}

export type UploadedFile = { fileId: string; fileVersionId: string; processingState: string }
export async function uploadFixture(page: Page, path: string, declaredMime: string): Promise<UploadedFile> {
  const bytes = await readFile(path)
  const sha256 = createHash('sha256').update(bytes).digest('hex')
  const session = await apiJSON<{ id: string }>(page, 'POST', '/api/v1/admin/uploads', { displayName: basename(path), declaredMime, expectedSize: bytes.length, expectedSha256: sha256 })
  const partSize = 8 * 1024 * 1024
  for (let offset = 0, number = 1; offset < bytes.length; offset += partSize, number += 1) {
    const part = bytes.subarray(offset, Math.min(offset + partSize, bytes.length))
    const response = await page.request.put(`/api/v1/admin/uploads/${session.id}/parts/${number}`, { data: part, headers: { ...(await csrfHeader(page)), 'Content-Type': 'application/octet-stream', 'X-Part-SHA256': createHash('sha256').update(part).digest('hex') } })
    await expect(response, `upload part ${number}`).toBeOK()
  }
  return apiJSON<UploadedFile>(page, 'POST', `/api/v1/admin/uploads/${session.id}/complete`, {})
}

export async function waitForFileState(page: Page, fileId: string, accepted: string[], timeout = 120_000) {
  const deadline = Date.now() + timeout
  let last: { versions: Array<{ id: string; processingState: string; previewState?: string; browserPlayable: boolean; failureCategory?: string }> } | undefined
  while (Date.now() < deadline) {
    last = await apiJSON<typeof last>(page, 'GET', `/api/v1/admin/files/${fileId}`)
    if (last?.versions[0] && accepted.includes(last.versions[0].processingState)) return last
    await new Promise((resolve) => setTimeout(resolve, 500))
  }
  throw new Error(`file ${fileId} did not reach ${accepted.join('/')} (last=${JSON.stringify(last)})`)
}

export async function uploadQuestionFixture(page: Page, role: 'student' | 'admin', path: string, declaredMime: string): Promise<UploadedFile> {
  const bytes = await readFile(path)
  const sha256 = createHash('sha256').update(bytes).digest('hex')
  const prefix = role === 'admin' ? '/api/v1/admin/question-uploads' : '/api/v1/student/question-uploads'
  const session = await apiJSON<{ id: string }>(page, 'POST', prefix, { displayName: basename(path), declaredMime, expectedSize: bytes.length, expectedSha256: sha256 })
  const partSize = 8 * 1024 * 1024
  for (let offset = 0, number = 1; offset < bytes.length; offset += partSize, number += 1) {
    const part = bytes.subarray(offset, Math.min(offset + partSize, bytes.length))
    const response = await page.request.put(`${prefix}/${session.id}/parts/${number}`, { data: part, headers: { ...(await csrfHeader(page)), 'Content-Type': 'application/octet-stream', 'X-Part-SHA256': createHash('sha256').update(part).digest('hex') } })
    await expect(response, `question upload part ${number}`).toBeOK()
  }
  return apiJSON<UploadedFile>(page, 'POST', `${prefix}/${session.id}/complete`, {})
}

export async function waitForQuestionFile(page: Page, fileVersionId: string, timeout = 120_000) {
  const deadline = Date.now() + timeout
  let last: { processingState: string; previewAvailable: boolean } | undefined
  while (Date.now() < deadline) {
    const response = await page.request.get(`/api/v1/question-files/${fileVersionId}/status`)
    if (response.ok()) {
      last = await response.json() as { processingState: string; previewAvailable: boolean }
      if (last.processingState === 'ready') return last
      if (last.processingState === 'rejected' || last.processingState === 'failed') break
    }
    await new Promise((resolve) => setTimeout(resolve, 500))
  }
  throw new Error(`question file ${fileVersionId} did not become ready (last=${JSON.stringify(last)})`)
}

export type AIProtocolMode = 'chat_completions' | 'responses'
export type AIRunStatus = 'queued' | 'streaming' | 'succeeded' | 'failed' | 'cancelled'
export type AIRun = {
  id: string
  status: AIRunStatus
  attemptNo: number
  lastSequence: number
  errorCode?: string
  usage?: {
    inputTokens: number
    outputTokens: number
    costMicroUSD: string
    source: 'provider' | 'estimated'
  }
  createdAt: string
  updatedAt: string
}
export type AIFileStatus = {
  fileVersionId: string
  processingState: string
  failureCategory?: string
  detectedMime?: string
  size: number
  previewAvailable: boolean
}
export type AIProvider = {
  id: string
  name: string
  baseUrl: string
  protocolMode: AIProtocolMode
  active: boolean
  hasKey: boolean
  keyUpdatedAt: string
  version: number
}
export type AIModel = {
  id: string
  providerId: string
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

const fakeProviderBaseURL = process.env.E2E_AI_PROVIDER_BASE_URL ?? 'http://fake-ai:8090/v1'
const fakeProviderKey = process.env.E2E_AI_PROVIDER_KEY ?? 'e2e-provider-key'

/**
 * Install a complete, production-shaped AI configuration through the real
 * admin endpoints. UI-specific provider/configuration behavior is covered in
 * ai-admin.spec.ts; workflow tests use this helper to avoid coupling every
 * scenario to the tabbed form.
 */
export async function configureAIProvider(page: Page, mode: AIProtocolMode): Promise<void> {
  const suffix = randomUUID().slice(0, 8)
  const provider = await apiJSON<AIProvider>(page, 'POST', '/api/v1/admin/ai/providers', {
    name: `E2E AI ${mode} ${suffix}`,
    baseUrl: fakeProviderBaseURL,
    protocolMode: mode,
    apiKey: fakeProviderKey,
  }, { 'Idempotency-Key': randomUUID() })

  const modelInput = (modality: 'text' | 'vision', upstreamModelId: string) => ({
    upstreamModelId,
    modality,
    contextTokens: 16_384,
    maxOutputTokens: 2_048,
    imageQuotaTokens: 1_024,
    inputPriceMicroUsd: 1_000,
    outputPriceMicroUsd: 2_000,
    connectTimeoutMs: 1_000,
    responseHeaderTimeoutMs: 45_000,
    idleStreamTimeoutMs: 45_000,
    totalTimeoutMs: 90_000,
    enabled: true,
    clearQuotaBlock: false,
    expectedVersion: 0,
  })
  await apiJSON<AIModel>(
    page,
    'PUT',
    `/api/v1/admin/ai/providers/${provider.id}/models/${randomUUID()}`,
    modelInput('text', `fixture-text-${suffix}`),
  )
  await apiJSON<AIModel>(
    page,
    'PUT',
    `/api/v1/admin/ai/providers/${provider.id}/models/${randomUUID()}`,
    modelInput('vision', `fixture-vision-${suffix}`),
  )

  const prompts = await apiJSON<Array<{ subject: 'math' | 'physics'; version: number; active: boolean }>>(
    page,
    'GET',
    '/api/v1/admin/ai/prompts',
  )
  for (const subject of ['math', 'physics'] as const) {
    const current = prompts.find((prompt) => prompt.subject === subject && prompt.active)
    await apiJSON(page, 'PUT', `/api/v1/admin/ai/prompts/${subject}`, {
      body: subject === 'math'
        ? '你是数学助教。给出安全、清晰、可核对的推导。'
        : '你是物理助教。说明系统、已知量、定律和单位。',
      expectedVersion: current?.version ?? 0,
    })
  }
  const limits = await apiJSON<{ global: { version: number } }>(page, 'GET', '/api/v1/admin/ai/limits')
  await apiJSON(page, 'PUT', '/api/v1/admin/ai/limits/global', {
    dailyRequests: { mode: 'limit', value: 1_000 },
    monthlyRequests: { mode: 'limit', value: 10_000 },
    dailyTokens: { mode: 'limit', value: 1_000_000 },
    monthlyTokens: { mode: 'limit', value: 10_000_000 },
    expectedVersion: limits.global.version,
  })
  await apiJSON<AIProvider>(page, 'PUT', '/api/v1/admin/ai/active-provider', {
    providerId: provider.id,
    expectedVersion: provider.version,
  })
}

export async function uploadAIFixture(page: Page, path: string, declaredMime: string): Promise<UploadedFile> {
  const bytes = await readFile(path)
  const sha256 = createHash('sha256').update(bytes).digest('hex')
  const prefix = '/api/v1/student/ai-uploads'
  const session = await apiJSON<{ id: string }>(page, 'POST', prefix, {
    displayName: basename(path),
    declaredMime,
    expectedSize: bytes.length,
    expectedSha256: sha256,
  })
  const partSize = 8 * 1024 * 1024
  for (let offset = 0, number = 1; offset < bytes.length; offset += partSize, number += 1) {
    const part = bytes.subarray(offset, Math.min(offset + partSize, bytes.length))
    const response = await page.request.put(`${prefix}/${session.id}/parts/${number}`, {
      data: part,
      headers: {
        ...(await csrfHeader(page)),
        'Content-Type': 'application/octet-stream',
        'X-Part-SHA256': createHash('sha256').update(part).digest('hex'),
      },
    })
    await expect(response, `AI upload part ${number}`).toBeOK()
  }
  return apiJSON<UploadedFile>(page, 'POST', `${prefix}/${session.id}/complete`, {})
}

export async function waitForAIFile(page: Page, fileVersionId: string, timeout = 120_000): Promise<AIFileStatus> {
  const deadline = Date.now() + timeout
  let last: AIFileStatus | undefined
  while (Date.now() < deadline) {
    const response = await page.request.get(`/api/v1/ai-question-files/${fileVersionId}/status`)
    if (response.ok()) {
      last = await response.json() as AIFileStatus
      const isImage = last.detectedMime?.startsWith('image/') ?? false
      // Non-image AI attachments are usable only after the worker published
      // their private ai_text preview; "ready" alone proves only clean scan.
      if (last.processingState === 'ready' && (isImage || last.previewAvailable)) return last
      if (last.processingState === 'rejected' || last.processingState === 'failed') break
    }
    await new Promise((resolve) => setTimeout(resolve, 500))
  }
  throw new Error(`AI file ${fileVersionId} did not become ready (last=${JSON.stringify(last)})`)
}

export async function waitForAIFileState(
  page: Page,
  fileVersionId: string,
  accepted: string[],
  timeout = 20_000,
): Promise<AIFileStatus> {
  const deadline = Date.now() + timeout
  let last: AIFileStatus | undefined
  while (Date.now() < deadline) {
    const response = await page.request.get(`/api/v1/ai-question-files/${fileVersionId}/status`)
    await expect(response, 'owner pre-bind AI file status').toBeOK()
    last = await response.json() as AIFileStatus
    if (accepted.includes(last.processingState)) return last
    await new Promise((resolve) => setTimeout(resolve, 200))
  }
  throw new Error(`AI file did not reach the requested synthetic state (state=${last?.processingState ?? 'unknown'})`)
}

/**
 * Task 3 supplies an internal, runner-only processing controller. It holds or
 * releases the disposable worker without exposing Docker or a production test
 * endpoint to the browser application.
 */
export async function setAIProcessingHeld(page: Page, held: boolean): Promise<void> {
  const controlURL = process.env.E2E_AI_PROCESSING_CONTROL_URL
  if (!controlURL) throw new Error('E2E_AI_PROCESSING_CONTROL_URL is required for deterministic pending-file acceptance')
  const response = await page.request.post(`${controlURL.replace(/\/$/, '')}/${held ? 'hold' : 'release'}`, {
    data: {},
  })
  await expect(response, `AI processing ${held ? 'hold' : 'release'}`).toBeOK()
}

export async function waitForRunStatus(
  page: Page,
  runId: string,
  status: AIRunStatus,
  timeout = 120_000,
): Promise<AIRun> {
  const deadline = Date.now() + timeout
  let last: AIRun | undefined
  while (Date.now() < deadline) {
    const threadResponse = await page.request.get('/api/v1/student/ai/threads?limit=100')
    await expect(threadResponse).toBeOK()
    const threads = (await threadResponse.json()).data as Array<{ id: string }>
    for (const thread of threads) {
      const detail = await apiJSON<{ activeRun?: AIRun }>(
        page,
        'GET',
        `/api/v1/student/ai/threads/${thread.id}?limit=100`,
      )
      if (detail.activeRun?.id !== runId) continue
      last = detail.activeRun
      if (last.status === status) return last
    }
    await new Promise((resolve) => setTimeout(resolve, 300))
  }
  throw new Error(`AI run ${runId} did not reach ${status} (last=${JSON.stringify(last)})`)
}

export async function providerHitCounts(page: Page): Promise<Record<string, number>> {
  const countsURL = process.env.E2E_AI_PROVIDER_COUNTS_URL ?? 'http://fake-ai:8090/test/counts'
  const response = await page.request.get(countsURL)
  await expect(response, 'fake AI provider aggregate counts').toBeOK()
  const payload = await response.json() as Record<string, unknown>
  const counts: Record<string, number> = {}
  for (const [label, value] of Object.entries(payload)) {
    if (!/^(chat_completions|responses)\.[a-z0-9-]+$/.test(label) || !Number.isSafeInteger(value) || (value as number) < 0) {
      throw new Error('fake AI provider returned non-aggregate counts')
    }
    counts[label] = value as number
  }
  return counts
}

export type NotificationRecord = { id: string; kind: string; targetId: string; targetPath: string; readAt?: string }
export async function waitForNotifications(page: Page, predicate: (items: NotificationRecord[]) => boolean, timeout = 20_000) {
  const deadline = Date.now() + timeout
  let items: NotificationRecord[] = []
  while (Date.now() < deadline) {
    items = await apiJSON<NotificationRecord[]>(page, 'GET', '/api/v1/notifications?limit=100')
    if (predicate(items)) return items
    await new Promise((resolve) => setTimeout(resolve, 300))
  }
  throw new Error(`notifications did not reach expected state (count=${items.length})`)
}

export function expectStatus(response: APIResponse, status: number) { expect(response.status()).toBe(status) }

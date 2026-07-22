import { randomUUID } from 'node:crypto'
import { readFile } from 'node:fs/promises'
import { join } from 'node:path'
import { expect, test, type Page } from '@playwright/test'
import { apiJSON, changePassword, createStudentAPI, login, waitForNotifications } from './helpers'

const adminPassword = process.env.E2E_ADMIN_PASSWORD
const studentPassword = process.env.E2E_STUDENT_PASSWORD
const studentNewPassword = process.env.E2E_STUDENT_NEW_PASSWORD
const fixtureDir = process.env.E2E_FIXTURE_DIR ?? 'tests/fixtures/teaching/generated'

type Thread = { id: string; title: string; status: 'pending'|'in_progress'|'waiting_student'|'completed'; version: number }
type Message = { id: string; body: string; attachments: Array<{ fileVersionId: string }> }
type Detail = { thread: Thread; messages: Message[] }
type Mutation = { thread: Thread; message: Message }

test.beforeAll(() => {
  if (!adminPassword || !studentPassword || !studentNewPassword) throw new Error('Phase 3 E2E credentials are required.')
})

function mutationResponse(page: Page, role: 'student'|'admin', questionId?: string) {
  const prefix = role === 'student' ? '/api/v1/student/questions' : '/api/v1/admin/questions'
  return page.waitForResponse((response) => response.request().method() === 'POST'
    && (questionId ? response.url().includes(`${prefix}/${questionId}/messages`) : response.url().endsWith(prefix)))
}

async function confirmStatus(page: Page, testId: 'claim'|'complete'|'reopen', expectedStatus: string) {
  page.once('dialog', (dialog) => dialog.accept())
  const responsePromise = page.waitForResponse((response) => response.request().method() === 'POST' && /\/status$/.test(response.url()))
  await page.getByTestId(testId).click()
  expect((await responsePromise).status()).toBe(200)
  await expect(page.getByLabel('问题详情').getByText(expectedStatus, { exact: true })).toBeVisible()
}

test('UI covers attachments, retry, Q&A lifecycle, privacy, disabling, and responsive keyboard layouts', async ({ browser }) => {
  test.setTimeout(420_000)
  const suffix = `${Date.now()}-${Math.floor(Math.random() * 10_000)}`
  const adminContext = await browser.newContext()
  const studentAContext = await browser.newContext()
  const studentBContext = await browser.newContext()
  const admin = await adminContext.newPage()
  await login(admin, 'admin', adminPassword!)
  const studentA = await createStudentAPI(admin, `qa-a-${suffix}`, '答疑学生甲', studentPassword!)
  const studentB = await createStudentAPI(admin, `qa-b-${suffix}`, '答疑学生乙', studentPassword!)
  const pageA = await studentAContext.newPage()
  await login(pageA, studentA.username, studentPassword!)
  await changePassword(pageA, studentPassword!, `${studentNewPassword!} qa-a`)
  const pageB = await studentBContext.newPage()
  await login(pageB, studentB.username, studentPassword!)
  await changePassword(pageB, studentPassword!, `${studentNewPassword!} qa-b`)

  const title = `动量题-${suffix}`
  const body = '请解释碰撞后的动量守恒。'
  await pageA.goto('/student/questions/new')
  const uploader = pageA.getByRole('region', { name: '添加附件（最多 20 个，合计不超过 100 MB）' })
  const fileInput = uploader.locator('input[type="file"]')
  await fileInput.setInputFiles({ name: 'question.png', mimeType: 'image/png', buffer: await readFile(join(fixtureDir, 'question.png')) })
  const browserPNG = await fileInput.evaluate(async (input: HTMLInputElement) => {
    const file = input.files?.[0]
    if (!file) return { size: 0, type: '', signature: '' }
    const bytes = new Uint8Array(await file.slice(0, 8).arrayBuffer())
    return { size: file.size, type: file.type, signature: [...bytes].map((value) => value.toString(16).padStart(2, '0')).join('') }
  })
  expect(browserPNG.size).toBeGreaterThan(0)
  expect(browserPNG).toMatchObject({ type: 'image/png', signature: '89504e470d0a1a0a' })
  await expect(uploader.locator('li').filter({ hasText: 'question.png' })).toContainText('已就绪', { timeout: 120_000 })
  await fileInput.setInputFiles({ name: 'question.pdf', mimeType: 'application/pdf', buffer: await readFile(join(fixtureDir, 'question.pdf')) })
  await expect(uploader.locator('li').filter({ hasText: 'question.pdf' })).toContainText('已就绪', { timeout: 120_000 })
  await pageA.getByLabel('问题标题').fill(title)
  await pageA.getByLabel('问题描述').fill(body)
  const createResponsePromise = mutationResponse(pageA, 'student')
  await pageA.getByRole('button', { name: '提交问题' }).evaluate((button: HTMLButtonElement) => { button.click(); button.click() })
  const createResponse = await createResponsePromise
  expect(createResponse.status()).toBe(201)
  const created = (await createResponse.json()).data as Mutation
  await expect(pageA).toHaveURL(new RegExp(`/student/questions/${created.thread.id}$`))
  await expect(pageA.getByLabel('问答消息')).toContainText(body)
  await expect(pageA.getByLabel('问答消息').locator('article')).toHaveCount(1)

  let failedOnce = false
  await pageA.route(`**/api/v1/student/questions/${created.thread.id}`, async (route) => {
    if (!failedOnce) { failedOnce = true; await route.abort(); return }
    await route.continue()
  })
  await pageA.reload()
  await expect(pageA.getByRole('alert')).toBeVisible()
  const retryResponse = pageA.waitForResponse((response) => response.request().method() === 'GET' && response.url().endsWith(`/api/v1/student/questions/${created.thread.id}`))
  await pageA.getByLabel('重试加载问题').click()
  expect((await retryResponse).status()).toBe(200)
  await expect(pageA.getByRole('heading', { name: title })).toBeVisible()
  await pageA.unroute(`**/api/v1/student/questions/${created.thread.id}`)

  const detailA = await apiJSON<Detail>(pageA, 'GET', `/api/v1/student/questions/${created.thread.id}`)
  expect(detailA.messages[0].attachments).toHaveLength(2)
  const [image, pdf] = detailA.messages[0].attachments
  expect((await pageA.request.get(`/api/v1/question-files/${image.fileVersionId}/preview`)).status()).toBe(200)
  expect((await pageA.request.get(`/api/v1/question-files/${pdf.fileVersionId}/download`)).status()).toBe(200)

  const foreignPaths = [
    `/api/v1/student/questions/${created.thread.id}`,
    `/api/v1/student/questions/${created.thread.id}/messages?limit=50`,
    `/api/v1/student/questions/${created.thread.id}/messages/${detailA.messages[0].id}`,
    `/api/v1/question-files/${image.fileVersionId}/status`,
    `/api/v1/question-files/${pdf.fileVersionId}/download`,
    `/api/v1/student/questions/${randomUUID()}`,
    `/api/v1/student/questions/${randomUUID()}/messages/${randomUUID()}`,
    `/api/v1/question-files/${randomUUID()}/preview`,
  ]
  for (const path of foreignPaths) expect((await pageB.request.get(path)).status(), path).toBe(404)
  expect(await apiJSON<Thread[]>(pageB, 'GET', '/api/v1/student/questions?limit=50')).toEqual([])
  expect((await apiJSON<unknown[]>(pageB, 'GET', '/api/v1/notifications?limit=50')).some((item) => JSON.stringify(item).includes(created.thread.id))).toBe(false)

  const adminNotifications = await waitForNotifications(admin, (items) => items.filter((item) => item.kind === 'qa_created' && item.targetId === created.thread.id).length === 1)
  expect(adminNotifications.filter((item) => item.kind === 'qa_created' && item.targetId === created.thread.id)).toHaveLength(1)

  await admin.setViewportSize({ width: 1440, height: 900 })
  await admin.goto('/admin/questions')
  await admin.getByLabel('状态').selectOption('pending')
  await admin.getByLabel('学生 ID').fill(studentA.id)
  const filteredResponse = admin.waitForResponse((response) => response.request().method() === 'GET' && response.url().includes('/api/v1/admin/questions?') && response.url().includes(`studentId=${studentA.id}`))
  await admin.getByTestId('apply-filters').click()
  expect((await filteredResponse).status()).toBe(200)
  const questionLink = admin.getByRole('link', { name: new RegExp(title) })
  await expect(questionLink).toBeVisible()
  await questionLink.click()
  await expect(admin.getByRole('heading', { name: title })).toBeVisible()
  const listBox = await admin.getByRole('heading', { name: '问题队列' }).boundingBox()
  const detailBox = await admin.getByLabel('问题详情').boundingBox()
  expect(listBox).not.toBeNull(); expect(detailBox).not.toBeNull(); expect(detailBox!.x).toBeGreaterThan(listBox!.x)

  await admin.getByTestId('claim').focus()
  await expect(admin.getByTestId('claim')).toBeFocused()
  admin.once('dialog', (dialog) => dialog.accept())
  const claimResponse = admin.waitForResponse((response) => response.request().method() === 'POST' && /\/status$/.test(response.url()))
  await admin.getByTestId('claim').press('Enter')
  expect((await claimResponse).status()).toBe(200)
  await expect(admin.getByLabel('问题详情').getByText('处理中', { exact: true })).toBeVisible()

  const replyForm = admin.getByTestId('reply-form')
  await replyForm.getByLabel('回复内容').fill('先选系统，再检查外力冲量。')
  const replyResponsePromise = mutationResponse(admin, 'admin', created.thread.id)
  await replyForm.getByRole('button', { name: '发送回复', exact: true }).click()
  expect((await replyResponsePromise).status()).toBe(201)
  await expect(admin.getByTestId('timeline')).toContainText('先选系统')
  await admin.getByLabel('新增私密备注').fill('仅教师：下次复习冲量定理。')
  const noteResponse = admin.waitForResponse((response) => response.request().method() === 'POST' && response.url().endsWith(`/api/v1/admin/questions/${created.thread.id}/notes`))
  await admin.getByRole('button', { name: '保存备注' }).click()
  expect((await noteResponse).status()).toBe(201)
  await expect(admin.getByText('仅教师：下次复习冲量定理。')).toBeVisible()

  const studentNotifications = await waitForNotifications(pageA, (items) => items.filter((item) => item.kind === 'qa_replied' && item.targetId === created.thread.id).length === 1)
  expect(studentNotifications.filter((item) => item.kind === 'qa_replied' && item.targetId === created.thread.id)).toHaveLength(1)
  expect(JSON.stringify(await (await pageA.request.get(`/api/v1/student/questions/${created.thread.id}`)).json())).not.toContain('仅教师')

  await pageA.reload()
  await pageA.getByLabel('追问内容').fill('如果有摩擦，系统怎么选？')
  const followResponsePromise = mutationResponse(pageA, 'student', created.thread.id)
  await pageA.getByRole('button', { name: '提交追问' }).evaluate((button: HTMLButtonElement) => { button.click(); button.click() })
  expect((await followResponsePromise).status()).toBe(201)
  await expect(pageA.getByLabel('问答消息')).toContainText('如果有摩擦，系统怎么选？')
  await pageA.reload()
  await expect(pageA.getByLabel('问答消息')).toContainText('如果有摩擦，系统怎么选？')
  await waitForNotifications(admin, (items) => items.filter((item) => item.kind === 'qa_followed_up' && item.targetId === created.thread.id).length === 1)

  await admin.reload()
  await confirmStatus(admin, 'complete', '已完成')
  await confirmStatus(admin, 'reopen', '处理中')
  await admin.getByTestId('reply-form').getByLabel('回复内容').fill('把接触双方都纳入系统即可。')
  const secondReplyPromise = mutationResponse(admin, 'admin', created.thread.id)
  await admin.getByTestId('reply-form').getByRole('button', { name: '发送回复', exact: true }).click()
  expect((await secondReplyPromise).status()).toBe(201)
  await expect(admin.getByLabel('问题详情').getByText('等待学生回复', { exact: true })).toBeVisible()

  await admin.setViewportSize({ width: 390, height: 844 })
  await expect(admin.getByRole('heading', { name: '问题队列' })).toBeHidden()
  await expect(admin.getByRole('link', { name: '← 返回问题队列' })).toBeVisible()
  await pageA.setViewportSize({ width: 390, height: 844 })
  await pageA.reload()
  await expect(pageA.getByLabel('问答消息')).toContainText('把接触双方都纳入系统即可。')
  await pageA.getByLabel('追问内容').focus()
  await expect(pageA.getByLabel('追问内容')).toBeFocused()
  await pageA.getByLabel('打开导航').press('Enter')
  await expect(pageA.getByRole('navigation', { name: '主导航' })).toBeVisible()
  await pageA.keyboard.press('Escape')
  await expect(pageA.getByLabel('打开导航')).toBeFocused()

  await pageA.goto('about:blank')
  await admin.setViewportSize({ width: 1440, height: 900 })
  await admin.goto('/admin/students')
  const disableButton = admin.getByRole('button', { name: `禁用 ${studentA.username}` })
  await expect(disableButton).toBeVisible()
  await disableButton.click()
  const disableResponse = admin.waitForResponse((response) => response.request().method() === 'POST' && response.url().endsWith(`/api/v1/admin/students/${studentA.id}/status`))
  await admin.getByRole('button', { name: `确认禁用 ${studentA.username}` }).click()
  expect((await disableResponse).status()).toBe(204)
  const disabledStudentRow = admin.getByRole('row').filter({ hasText: studentA.username })
  await expect(disabledStudentRow.getByText('已停用', { exact: true })).toBeVisible()
  for (const path of ['/api/v1/auth/me', `/api/v1/student/questions/${created.thread.id}`, '/api/v1/notifications?limit=20', `/api/v1/question-files/${image.fileVersionId}/preview`]) {
    expect((await studentAContext.request.get(path)).status(), path).toBe(401)
  }
  await Promise.all([pageB.goto('about:blank'), admin.goto('about:blank')])
})

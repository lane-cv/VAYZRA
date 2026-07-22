import { join } from 'node:path'
import { expect, test, type Page } from '@playwright/test'
import { apiJSON, changePassword, createStudentAPI, login, uploadQuestionFixture, waitForNotifications, waitForQuestionFile } from './helpers'

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

async function postQuestion(page: Page, path: string, data: unknown, key: string) {
  return apiJSON<Mutation>(page, 'POST', path, data, { 'Idempotency-Key': key })
}

test('attachments, immutable Q&A, privacy, idempotency, disabling, and keyboard layouts', async ({ browser }) => {
  test.setTimeout(360_000)
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

    const image = await uploadQuestionFixture(pageA, 'student', join(fixtureDir, 'question.png'), 'image/png')
    const pdf = await uploadQuestionFixture(pageA, 'student', join(fixtureDir, 'question.pdf'), 'application/pdf')
    await waitForQuestionFile(pageA, image.fileVersionId)
    await waitForQuestionFile(pageA, pdf.fileVersionId)
    const createKey = `question-create-${suffix}`
    const input = { title: `动量题-${suffix}`, body: '请解释碰撞后的动量守恒。', attachments: [image, pdf].map((item, sortPosition) => ({ fileVersionId: item.fileVersionId, sortPosition })) }
    const created = await postQuestion(pageA, '/api/v1/student/questions', input, createKey)
    const replayed = await postQuestion(pageA, '/api/v1/student/questions', input, createKey)
    expect(replayed).toEqual(created)
    let detailA = await apiJSON<Detail>(pageA, 'GET', `/api/v1/student/questions/${created.thread.id}`)
    expect(detailA.messages).toHaveLength(1)
    expect(detailA.messages[0].attachments).toHaveLength(2)
    expect((await pageA.request.get(`/api/v1/question-files/${image.fileVersionId}/preview`)).status()).toBe(200)
    expect((await pageA.request.get(`/api/v1/question-files/${pdf.fileVersionId}/download`)).status()).toBe(200)

    for (const path of [
      `/api/v1/student/questions/${created.thread.id}`,
      `/api/v1/student/questions/${created.thread.id}/messages?limit=50`,
      `/api/v1/question-files/${image.fileVersionId}/status`,
      `/api/v1/question-files/${pdf.fileVersionId}/download`,
    ]) expect((await pageB.request.get(path)).status(), path).toBe(404)
    expect(await apiJSON<Thread[]>(pageB, 'GET', '/api/v1/student/questions?limit=50')).toEqual([])
    expect((await apiJSON<unknown[]>(pageB, 'GET', '/api/v1/notifications?limit=50')).some((item) => JSON.stringify(item).includes(created.thread.id))).toBe(false)

    let adminNotifications = await waitForNotifications(admin, (items) => items.filter((item) => item.kind === 'qa_created' && item.targetId === created.thread.id).length === 1)
    expect(adminNotifications.filter((item) => item.kind === 'qa_created' && item.targetId === created.thread.id)).toHaveLength(1)
    const filtered = await apiJSON<Array<Thread & { studentId: string }>>(admin, 'GET', `/api/v1/admin/questions?status=pending&studentId=${studentA.id}&limit=20`)
    expect(filtered.map((thread) => thread.id)).toContain(created.thread.id)

    await admin.setViewportSize({ width: 1440, height: 900 })
    await admin.goto(`/admin/questions/${created.thread.id}`)
    await expect(admin.getByRole('heading', { name: input.title })).toBeVisible()
    await expect(admin.getByTestId('timeline')).toContainText(input.body)
    admin.once('dialog', (dialog) => dialog.accept())
    await admin.getByTestId('claim').focus()
    await expect(admin.getByTestId('claim')).toBeFocused()
    await admin.getByTestId('claim').press('Enter')
    await expect(admin.getByLabel('问题详情').getByText('处理中', { exact: true })).toBeVisible()
    const replyForm = admin.getByTestId('reply-form')
    const replyInput = replyForm.getByLabel('回复内容')
    const replyButton = replyForm.getByRole('button', { name: '发送回复', exact: true })
    await replyInput.focus()
    await replyInput.pressSequentially('先选系统，再检查外力冲量。')
    await expect(replyInput).toHaveValue('先选系统，再检查外力冲量。')
    await expect(replyButton).toBeEnabled()
    const [replyResponse] = await Promise.all([
      admin.waitForResponse((response) => response.request().method() === 'POST' && /\/api\/v1\/admin\/questions\/[^/]+\/messages\/?(?:\?.*)?$/.test(response.url()), { timeout: 15_000 }),
      replyButton.click(),
    ])
    expect(replyResponse.status()).toBe(201)
    await expect(admin.getByTestId('timeline')).toContainText('先选系统')
    await admin.getByLabel('新增私密备注').fill('仅教师：下次复习冲量定理。')
    await admin.getByRole('button', { name: '保存备注' }).click()
    await expect(admin.getByText('仅教师：下次复习冲量定理。')).toBeVisible()

    const studentNotifications = await waitForNotifications(pageA, (items) => items.filter((item) => item.kind === 'qa_replied' && item.targetId === created.thread.id).length === 1)
    expect(studentNotifications.filter((item) => item.kind === 'qa_replied' && item.targetId === created.thread.id)).toHaveLength(1)
    const rawStudentDetail = await pageA.request.get(`/api/v1/student/questions/${created.thread.id}`)
    expect(JSON.stringify(await rawStudentDetail.json())).not.toContain('仅教师')
    const followKey = `question-follow-${suffix}`
    const follow = await postQuestion(pageA, `/api/v1/student/questions/${created.thread.id}/messages`, { body: '如果有摩擦，系统怎么选？', attachments: [] }, followKey)
    const followReplay = await postQuestion(pageA, `/api/v1/student/questions/${created.thread.id}/messages`, { body: '如果有摩擦，系统怎么选？', attachments: [] }, followKey)
    expect(followReplay).toEqual(follow)
    expect(follow.thread.status).toBe('pending')
    detailA = await apiJSON<Detail>(pageA, 'GET', `/api/v1/student/questions/${created.thread.id}`)
    expect(detailA.messages.map((message) => message.body)).toEqual([input.body, '先选系统，再检查外力冲量。', '如果有摩擦，系统怎么选？'])
    adminNotifications = await waitForNotifications(admin, (items) => items.filter((item) => item.kind === 'qa_followed_up' && item.targetId === created.thread.id).length === 1)
    expect(adminNotifications.filter((item) => item.kind === 'qa_followed_up' && item.targetId === created.thread.id)).toHaveLength(1)

    let current = (await apiJSON<{ thread: Thread } & Record<string, unknown>>(admin, 'GET', `/api/v1/admin/questions/${created.thread.id}`)).thread
    current = await apiJSON<Thread>(admin, 'POST', `/api/v1/admin/questions/${current.id}/status`, { status: 'completed', expectedVersion: current.version })
    current = await apiJSON<Thread>(admin, 'POST', `/api/v1/admin/questions/${current.id}/status`, { status: 'in_progress', expectedVersion: current.version })
    const secondReply = await postQuestion(admin, `/api/v1/admin/questions/${current.id}/messages`, { body: '把接触双方都纳入系统即可。', attachments: [], expectedVersion: current.version }, `question-reply-2-${suffix}`)
    expect(secondReply.thread.status).toBe('waiting_student')

    await pageA.setViewportSize({ width: 390, height: 844 })
    await pageA.goto(`/student/questions/${created.thread.id}`)
    await expect(pageA.getByRole('heading', { name: input.title })).toBeVisible()
    await expect(pageA.getByLabel('问答消息')).toContainText('把接触双方都纳入系统即可。')
    await pageA.getByLabel('追问内容').focus()
    await expect(pageA.getByLabel('追问内容')).toBeFocused()
    await pageA.getByLabel('打开导航').press('Enter')
    await expect(pageA.getByRole('navigation', { name: '主导航' })).toBeVisible()
    await pageA.keyboard.press('Escape')
    await expect(pageA.getByLabel('打开导航')).toBeFocused()

    // Unmount the application and stop notification polling before the
    // administrator invalidates this session. Cookies remain in the context.
    await pageA.goto('about:blank')
    await apiJSON<void>(admin, 'POST', `/api/v1/admin/students/${studentA.id}/status`, { status: 'disabled' })
    for (const path of [
      '/api/v1/auth/me',
      `/api/v1/student/questions/${created.thread.id}`,
      '/api/v1/notifications?limit=20',
      `/api/v1/question-files/${image.fileVersionId}/preview`,
    ]) expect((await studentAContext.request.get(path)).status(), path).toBe(401)
    await Promise.all([pageB.goto('about:blank'), admin.goto('about:blank')])
  // Playwright's browser fixture owns these pages and contexts. Closing them
  // individually can hang Chromium after the disabled session cancels polling.
})

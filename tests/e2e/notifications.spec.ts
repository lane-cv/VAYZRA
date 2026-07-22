import { expect, test } from '@playwright/test'
import { apiJSON, changePassword, createStudentAPI, createTeachingPathUI, login, waitForNotifications } from './helpers'

const adminPassword = process.env.E2E_ADMIN_PASSWORD
const studentPassword = process.env.E2E_STUDENT_PASSWORD
const studentNewPassword = process.env.E2E_STUDENT_NEW_PASSWORD

test.beforeAll(() => {
  if (!adminPassword || !studentPassword || !studentNewPassword) throw new Error('Phase 3 E2E credentials are required.')
})

test('lesson audience notifications and durable read state survive reloads', async ({ browser }) => {
  test.setTimeout(240_000)
  const suffix = `${Date.now()}-${Math.floor(Math.random() * 10_000)}`
  const adminContext = await browser.newContext()
  const studentAContext = await browser.newContext()
  const studentBContext = await browser.newContext()
  const admin = await adminContext.newPage()
    await login(admin, 'admin', adminPassword!)
    const studentA = await createStudentAPI(admin, `notify-a-${suffix}`, '通知学生甲', studentPassword!)
    const studentB = await createStudentAPI(admin, `notify-b-${suffix}`, '通知学生乙', studentPassword!)
    const pageA = await studentAContext.newPage()
    await login(pageA, studentA.username, studentPassword!)
    await changePassword(pageA, studentPassword!, `${studentNewPassword!} notify-a`)
    const pageB = await studentBContext.newPage()
    await login(pageB, studentB.username, studentPassword!)
    await changePassword(pageB, studentPassword!, `${studentNewPassword!} notify-b`)

    const allPath = await createTeachingPathUI(admin, `notify-all-${suffix}`)
    await admin.getByLabel('课程正文', { exact: true }).fill('# 全体课程')
    await expect(admin.getByText('已保存', { exact: true })).toBeVisible()
    await admin.getByRole('button', { name: '发布课程' }).click()
    const allPublish = admin.waitForResponse((response) => response.request().method() === 'POST' && response.url().endsWith(`/api/v1/admin/lessons/${allPath.lessonId}/publish`))
    await admin.getByRole('button', { name: '确认发布课程' }).click()
    expect((await allPublish).status()).toBe(201)
    await expect(admin.getByText('发布成功：第 1 版')).toBeVisible()

    const selectedPath = await createTeachingPathUI(admin, `notify-selected-${suffix}`)
    await admin.getByLabel('课程正文', { exact: true }).fill('# 定向课程')
    await admin.getByLabel('指定学生').check()
    await admin.getByLabel(`选择学生 ${studentA.username}`).check()
    await expect(admin.getByText('已保存', { exact: true })).toBeVisible()
    await admin.getByRole('button', { name: '发布课程' }).click()
    const selectedPublish = admin.waitForResponse((response) => response.request().method() === 'POST' && response.url().endsWith(`/api/v1/admin/lessons/${selectedPath.lessonId}/publish`))
    await admin.getByRole('button', { name: '确认发布课程' }).click()
    expect((await selectedPublish).status()).toBe(201)
    await expect(admin.getByText('发布成功：第 1 版')).toBeVisible()

    const notificationsA = await waitForNotifications(pageA, (items) => items.filter((item) => item.kind === 'lesson_published' && [allPath.lessonId, selectedPath.lessonId].includes(item.targetId)).length === 2)
    const notificationsB = await waitForNotifications(pageB, (items) => items.filter((item) => item.kind === 'lesson_published' && item.targetId === allPath.lessonId).length === 1)
    expect(notificationsA.filter((item) => item.kind === 'lesson_published' && [allPath.lessonId, selectedPath.lessonId].includes(item.targetId))).toHaveLength(2)
    expect(notificationsB.filter((item) => item.targetId === selectedPath.lessonId)).toHaveLength(0)

    await pageA.goto('/notifications')
    await expect(pageA.getByRole('heading', { name: '通知中心' })).toBeVisible()
    await expect(pageA.getByLabel('未读消息 2')).toBeVisible()
    const markOneResponse = pageA.waitForResponse((response) => response.request().method() === 'POST' && /\/api\/v1\/notifications\/[^/]+\/read$/.test(response.url()))
    await pageA.getByRole('button', { name: '标为已读', exact: true }).first().click()
    expect((await markOneResponse).status()).toBe(200)
    await expect(pageA.getByLabel('未读消息 1')).toBeVisible({ timeout: 15_000 })
    await pageA.reload()
    await expect(pageA.getByLabel('未读消息 1')).toBeVisible()
    expect((await apiJSON<{ count: number }>(pageA, 'GET', '/api/v1/notifications/unread-count')).count).toBe(1)

    await pageB.goto('/notifications')
    await expect(pageB.getByLabel('未读消息 1')).toBeVisible()
    const markAllResponse = pageB.waitForResponse((response) => response.request().method() === 'POST' && response.url().endsWith('/api/v1/notifications/read-all'))
    await pageB.getByRole('button', { name: '全部标为已读' }).click()
    expect((await markAllResponse).status()).toBe(200)
    await expect(pageB.getByLabel('未读消息 0')).toBeVisible({ timeout: 15_000 })
    await pageB.reload()
    await expect(pageB.getByLabel('未读消息 0')).toBeVisible()
    expect((await apiJSON<{ count: number }>(pageB, 'GET', '/api/v1/notifications/unread-count')).count).toBe(0)
    // Close pages first so their polling intervals and in-flight list requests are
    // cancelled before disposing the isolated authenticated contexts.
  await Promise.all([pageB.close(), pageA.close(), admin.close()])
  // The Playwright browser fixture owns the three empty contexts and closes
  // them together. Per-context disposal is unreliable after polling pages have
  // been explicitly closed in Chromium, while browser teardown remains bounded.
})

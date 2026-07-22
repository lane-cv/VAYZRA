import { expect, test } from '@playwright/test'
import { apiJSON, changePassword, createStudentAPI, createTeachingPathUI, login, type Draft } from './helpers'

const adminPassword = process.env.E2E_ADMIN_PASSWORD
const studentPassword = process.env.E2E_STUDENT_PASSWORD
const studentNewPassword = process.env.E2E_STUDENT_NEW_PASSWORD

test.beforeAll(() => {
  if (!adminPassword || !studentPassword || !studentNewPassword) throw new Error('Phase 2 E2E credentials are required.')
})

test('teacher UI publishes immutable, audience-bound lessons and revokes access', async ({ browser }) => {
  test.setTimeout(180_000)
  const suffix = `${Date.now()}-${Math.floor(Math.random() * 10_000)}`
  const admin = await browser.newContext()
  const studentAContext = await browser.newContext()
  const studentBContext = await browser.newContext()
  try {
    const adminPage = await admin.newPage()
    await login(adminPage, 'admin', adminPassword!)
    expect(await apiJSON<unknown[]>(adminPage, 'GET', '/api/v1/admin/catalog?limit=200')).toEqual([])
    const studentA = await createStudentAPI(adminPage, `learner-a-${suffix}`, '学生甲', studentPassword!)
    const studentB = await createStudentAPI(adminPage, `learner-b-${suffix}`, '学生乙', studentPassword!)

    const path = await createTeachingPathUI(adminPage, suffix)
    await adminPage.getByLabel('课程摘要').fill('从惯性理解运动')
    await adminPage.getByLabel('课程正文', { exact: true }).fill('# 第一版\n\n由 $F=ma$ 得到运动规律。')
    await expect(adminPage.getByLabel('课程正文预览')).toContainText('第一版')
    await expect(adminPage.locator('.katex')).toBeVisible()
    await expect(adminPage.getByText('已保存', { exact: true })).toBeVisible()
    await expect(adminPage.getByLabel('全部启用学生')).toBeChecked()
    await adminPage.getByRole('button', { name: '发布课程' }).click()
    await adminPage.getByRole('button', { name: '确认发布课程' }).click()
    await expect(adminPage.getByText('发布成功：第 1 版')).toBeVisible()
    let draft = (await apiJSON<{ draft: Draft }>(adminPage, 'GET', `/api/v1/admin/lessons/${path.lessonId}`)).draft

    const studentAPage = await studentAContext.newPage()
    await login(studentAPage, studentA.username, studentPassword!)
    await changePassword(studentAPage, studentPassword!, `${studentNewPassword!} A`)
    const studentBPage = await studentBContext.newPage()
    await login(studentBPage, studentB.username, studentPassword!)
    await changePassword(studentBPage, studentPassword!, `${studentNewPassword!} B`)
    expect(await apiJSON<{ version: number; bodyMarkdown: string }>(studentAPage, 'GET', `/api/v1/student/lessons/${draft.lessonId}`)).toMatchObject({ version: 1, bodyMarkdown: expect.stringContaining('第一版') })

    const staleAdminPage = await admin.newPage()
    await staleAdminPage.goto(`/admin/teaching/lessons/${draft.lessonId}`)
    await expect(staleAdminPage.getByText('已保存', { exact: true })).toBeVisible()
    await adminPage.getByLabel('课程正文', { exact: true }).fill('# 第二版\n\n新内容仍满足 $F=ma$。')
    await expect(adminPage.getByText('已保存', { exact: true })).toBeVisible()
    await staleAdminPage.getByLabel('课程摘要').fill('过期页面写入')
    await expect(staleAdminPage.getByRole('alert')).toContainText('草稿已在其他页面更新')
    const reloadResponse = staleAdminPage.waitForResponse((response) => response.request().method() === 'GET' && response.url().endsWith(`/api/v1/admin/lessons/${draft.lessonId}`))
    await staleAdminPage.getByRole('button', { name: '重新加载服务器草稿' }).click()
    expect((await reloadResponse).status()).toBe(200)
    await expect(staleAdminPage.getByLabel('课程正文', { exact: true })).toHaveValue(/第二版/, { timeout: 15_000 })
    draft = (await apiJSON<{ draft: Draft }>(adminPage, 'GET', `/api/v1/admin/lessons/${draft.lessonId}`)).draft
    expect(await apiJSON<{ version: number; bodyMarkdown: string }>(studentAPage, 'GET', `/api/v1/student/lessons/${draft.lessonId}`)).toMatchObject({ version: 1, bodyMarkdown: expect.stringContaining('第一版') })
    await adminPage.getByRole('button', { name: '发布课程' }).click()
    const secondPublishResponse = adminPage.waitForResponse((response) => response.request().method() === 'POST' && response.url().endsWith(`/api/v1/admin/lessons/${draft.lessonId}/publish`))
    await adminPage.getByRole('button', { name: '确认发布课程' }).click()
    expect((await secondPublishResponse).status()).toBe(201)
    await expect(adminPage.getByText('发布成功：第 2 版')).toBeVisible()
    expect(await apiJSON<{ version: number }>(studentAPage, 'GET', `/api/v1/student/lessons/${draft.lessonId}`)).toMatchObject({ version: 2 })

    const selectedPath = await createTeachingPathUI(adminPage, `selected-${suffix}`)
    await adminPage.getByLabel('课程正文', { exact: true }).fill('# 定向课程\n\n仅学生甲可见。')
    await adminPage.getByLabel('指定学生').check()
    await adminPage.getByLabel(`选择学生 ${studentA.username}`).check()
    await expect(adminPage.getByText('已保存', { exact: true })).toBeVisible()
    const selectedDraft = (await apiJSON<{ draft: Draft }>(adminPage, 'GET', `/api/v1/admin/lessons/${selectedPath.draft.lessonId}`)).draft
    await adminPage.getByRole('button', { name: '发布课程' }).click()
    await adminPage.getByRole('button', { name: '确认发布课程' }).click()
    await expect(adminPage.getByText('发布成功：第 1 版')).toBeVisible()
    await expect(await studentAPage.request.get(`/api/v1/student/lessons/${selectedDraft.lessonId}`)).toBeOK()
    expect((await studentBPage.request.get(`/api/v1/student/lessons/${selectedDraft.lessonId}`)).status()).toBe(404)

    await apiJSON<void>(adminPage, 'POST', `/api/v1/admin/lessons/${draft.lessonId}/withdraw`)
    expect((await studentAPage.request.get(`/api/v1/student/lessons/${draft.lessonId}`)).status()).toBe(404)
    await apiJSON<void>(adminPage, 'POST', `/api/v1/admin/students/${studentA.id}/status`, { status: 'disabled' })
    expect((await studentAPage.request.get('/api/v1/auth/me')).status()).toBe(401)
  } finally {
    await studentBContext.close()
    await studentAContext.close()
    await admin.close()
  }
})

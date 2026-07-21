import { expect, test } from '@playwright/test'
import { apiJSON, changePassword, createStudentAPI, createTeachingPath, login, publishDraft, saveDraft } from './helpers'

const adminPassword = process.env.E2E_ADMIN_PASSWORD
const studentPassword = process.env.E2E_STUDENT_PASSWORD
const studentNewPassword = process.env.E2E_STUDENT_NEW_PASSWORD

test.beforeAll(() => {
  if (!adminPassword || !studentPassword || !studentNewPassword) throw new Error('Phase 2 E2E credentials are required.')
})

test('desktop/mobile learning navigation, search, recent lesson, and position work', async ({ browser }) => {
  const suffix = `${Date.now()}-${Math.floor(Math.random() * 10_000)}`
  const admin = await browser.newContext()
  const desktop = await browser.newContext()
  const mobile = await browser.newContext({ viewport: { width: 390, height: 844 } })
  try {
    const adminPage = await admin.newPage()
    await login(adminPage, 'admin', adminPassword!)
    const student = await createStudentAPI(adminPage, `learning-${suffix}`, '学习学生', studentPassword!)
    const path = await createTeachingPath(adminPage, `learning-${suffix}`)
    const longBody = ['# 动量守恒', ...Array.from({ length: 80 }, (_, index) => `## 例题 ${index + 1}\n\n由 $p=mv$ 分析碰撞。`)].join('\n\n')
    const draft = await saveDraft(adminPage, path.draft, { title: `动量课程-${suffix}`, summary: '动量与碰撞', bodyMarkdown: longBody, audience: { mode: 'selected', userIds: [student.id] } })
    await publishDraft(adminPage, draft)

    const desktopPage = await desktop.newPage()
    await login(desktopPage, student.username, studentPassword!)
    await changePassword(desktopPage, studentPassword!, `${studentNewPassword!} learning`)
    await desktopPage.goto('/student/learning')
    await desktopPage.getByLabel('搜索课程').fill('动量课程')
    await expect(desktopPage.getByRole('link', { name: new RegExp(`动量课程-${suffix}`) })).toBeVisible()
    await desktopPage.getByRole('link', { name: new RegExp(`动量课程-${suffix}`) }).click()
    await expect(desktopPage.getByRole('heading', { name: `动量课程-${suffix}` })).toBeVisible()
    const progress = desktopPage.waitForResponse((response) => response.url().endsWith('/api/v1/student/progress') && response.request().method() === 'POST')
    await desktopPage.locator('[data-reader-scroll]').evaluate((element) => { element.scrollTop = Math.max(1, element.scrollHeight / 2); element.dispatchEvent(new Event('scroll')) })
    await progress
    const position = await apiJSON<{ viewed: boolean; scrollRatio: number }>(desktopPage, 'GET', `/api/v1/student/lessons/${draft.lessonId}/position`)
    expect(position.viewed).toBe(true)
    expect(position.scrollRatio).toBeGreaterThan(0)
    await desktopPage.goto('/student')
    await expect(desktopPage.getByRole('heading', { name: '最近学习' })).toBeVisible()
    await expect(desktopPage.getByRole('link', { name: new RegExp(`动量课程-${suffix}.*阅读到`) })).toBeVisible()

    const mobilePage = await mobile.newPage()
    await login(mobilePage, student.username, `${studentNewPassword!} learning`)
    await mobilePage.goto(`/student/learning/${draft.lessonId}`)
    const trigger = mobilePage.getByRole('button', { name: '打开课程目录' })
    await expect(trigger).toBeVisible()
    await trigger.click()
    await expect(mobilePage.getByRole('dialog', { name: '课程目录' })).toHaveAttribute('aria-modal', 'true')
    await mobilePage.keyboard.press('Escape')
    await expect(trigger).toBeFocused()
  } finally {
    await mobile.close()
    await desktop.close()
    await admin.close()
  }
})

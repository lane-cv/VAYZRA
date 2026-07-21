import { createHash } from 'node:crypto'
import { readFile } from 'node:fs/promises'
import { join } from 'node:path'
import { expect, test } from '@playwright/test'
import { apiJSON, changePassword, createStudentAPI, createTeachingPath, csrfHeader, login, publishDraft, saveDraft, uploadFixture, waitForFileState } from './helpers'

const adminPassword = process.env.E2E_ADMIN_PASSWORD
const studentPassword = process.env.E2E_STUDENT_PASSWORD
const studentNewPassword = process.env.E2E_STUDENT_NEW_PASSWORD
const fixtureDir = process.env.E2E_FIXTURE_DIR ?? 'tests/fixtures/teaching/generated'

test.beforeAll(() => {
  if (!adminPassword || !studentPassword || !studentNewPassword) throw new Error('Phase 2 E2E credentials are required.')
})

test('processing, policy, Range, replacement, and rollback stay fail-closed', async ({ browser }) => {
  test.setTimeout(300_000)
  const suffix = `${Date.now()}-${Math.floor(Math.random() * 10_000)}`
  const admin = await browser.newContext()
  const studentContext = await browser.newContext()
  try {
    const adminPage = await admin.newPage()
    await login(adminPage, 'admin', adminPassword!)
    const student = await createStudentAPI(adminPage, `files-${suffix}`, '文件学生', studentPassword!)
    const path = await createTeachingPath(adminPage, `files-${suffix}`)
    let draft = await saveDraft(adminPage, path.draft, { bodyMarkdown: '# 文件课程\n\n安全文件策略。', audience: { mode: 'selected', userIds: [student.id] } })

    const document = await uploadFixture(adminPage, join(fixtureDir, 'lesson.docx'), 'application/vnd.openxmlformats-officedocument.wordprocessingml.document')
    const documentDetail = await waitForFileState(adminPage, document.fileId, ['ready'])
    expect(documentDetail!.versions[0].previewState).toBe('ready')
    const video = await uploadFixture(adminPage, join(fixtureDir, 'lesson.mp4'), 'video/mp4')
    const videoDetail = await waitForFileState(adminPage, video.fileId, ['ready'])
    expect(videoDetail!.versions[0].browserPlayable).toBe(true)

    await apiJSON(adminPage, 'PUT', `/api/v1/admin/lessons/${draft.lessonId}/files`, { expectedVersion: draft.lockVersion, files: [
      { fileVersionId: document.fileVersionId, policy: 'preview', displayName: '课程讲义.docx', description: '', sortPosition: 10 },
      { fileVersionId: video.fileVersionId, policy: 'download', displayName: '实验视频.mp4', description: '', sortPosition: 20 },
    ] })
    draft = (await apiJSON<{ draft: typeof draft }>(adminPage, 'GET', `/api/v1/admin/lessons/${draft.lessonId}`)).draft
    await publishDraft(adminPage, draft)

    const studentPage = await studentContext.newPage()
    await login(studentPage, student.username, studentPassword!)
    await changePassword(studentPage, studentPassword!, `${studentNewPassword!} files`)
    const lesson = await apiJSON<{ files: Array<{ fileVersionId: string; policy: string }> }>(studentPage, 'GET', `/api/v1/student/lessons/${draft.lessonId}`)
    expect(lesson.files).toEqual(expect.arrayContaining([
      expect.objectContaining({ fileVersionId: document.fileVersionId, policy: 'preview' }),
      expect.objectContaining({ fileVersionId: video.fileVersionId, policy: 'download' }),
    ]))
    expect((await studentPage.request.get(`/api/v1/files/${document.fileVersionId}/download`)).status()).toBe(404)
    expect((await studentPage.request.get(`/api/v1/files/${document.fileVersionId}/preview`)).status()).toBe(200)
    await studentPage.goto(`/student/learning/${draft.lessonId}`)
    const media = studentPage.locator(`video[src$="/${video.fileVersionId}/preview"]`)
    await expect(media).toBeVisible()
    const range = await studentPage.request.get(`/api/v1/files/${video.fileVersionId}/preview`, { headers: { Range: 'bytes=1024-2047' } })
    expect(range.status()).toBe(206)
    expect(range.headers()['content-range']).toMatch(/^bytes [1-9]\d*-\d+\//)
    expect((await studentPage.request.get(`/api/v1/files/${video.fileVersionId}/download`)).status()).toBe(200)

    const unsupported = await uploadFixture(adminPage, join(fixtureDir, 'unsupported.mkv'), 'video/x-matroska')
    await waitForFileState(adminPage, unsupported.fileId, ['ready'])
    const rejectedBinding = await adminPage.request.put(`/api/v1/admin/lessons/${draft.lessonId}/files`, { headers: await csrfHeader(adminPage), data: { expectedVersion: draft.lockVersion, files: [{ fileVersionId: unsupported.fileVersionId, policy: 'preview', displayName: 'unsupported.mkv', description: '', sortPosition: 30 }] } })
    expect(rejectedBinding.status()).toBe(409)

    for (const [name, mime] of [
      ['archive.zip', 'application/zip'],
      ['macro.docm', 'application/vnd.ms-word.document.macroEnabled.12'],
    ] as const) {
      const bytes = await readFile(join(fixtureDir, name))
      const response = await adminPage.request.post('/api/v1/admin/uploads', { headers: await csrfHeader(adminPage), data: {
        displayName: name,
        declaredMime: mime,
        expectedSize: bytes.length,
        expectedSha256: createHash('sha256').update(bytes).digest('hex'),
      } })
      expect(response.status()).toBe(400)
    }

    for (const [name, mime] of [
      ['eicar.txt', 'text/plain'], ['mismatch.pdf', 'application/pdf'],
    ] as const) {
      const uploaded = await uploadFixture(adminPage, join(fixtureDir, name), mime)
      const detail = await waitForFileState(adminPage, uploaded.fileId, ['rejected', 'failed'])
      expect(detail!.versions[0].processingState).toBe('rejected')
      expect(detail!.versions[0].failureCategory).toBeTruthy()
    }

    const replacement = await uploadFixture(adminPage, join(fixtureDir, 'replacement.docx'), 'application/vnd.openxmlformats-officedocument.wordprocessingml.document')
    await waitForFileState(adminPage, replacement.fileId, ['ready'])
    await apiJSON<void>(adminPage, 'POST', `/api/v1/admin/files/${document.fileId}/replace`, { uploadedVersionId: replacement.fileVersionId })
    const afterReplace = await apiJSON<{ versions: Array<{ id: string }> }>(adminPage, 'GET', `/api/v1/admin/files/${document.fileId}`)
    expect(afterReplace.versions[0].id).not.toBe(document.fileVersionId)
    expect((await apiJSON<{ files: Array<{ fileVersionId: string }> }>(studentPage, 'GET', `/api/v1/student/lessons/${draft.lessonId}`)).files[0].fileVersionId).toBe(document.fileVersionId)
    await apiJSON<void>(adminPage, 'POST', `/api/v1/admin/files/${document.fileId}/rollback`, { lessonId: draft.lessonId, fileVersionId: document.fileVersionId })
    expect((await apiJSON<{ files: Array<{ fileVersionId: string }> }>(studentPage, 'GET', `/api/v1/student/lessons/${draft.lessonId}`)).files[0].fileVersionId).toBe(document.fileVersionId)
  } finally {
    await studentContext.close()
    await admin.close()
  }
})

test('multipart upload resumes from IndexedDB after a browser reload', async ({ page }) => {
  test.setTimeout(180_000)
  const path = await createTeachingPathAfterLogin(page)
  await page.goto(`/admin/teaching/lessons/${path.draft.lessonId}`)
  await page.getByLabel('允许下载').check()
  let release!: () => void
  const blocked = new Promise<void>((resolve) => { release = resolve })
  const secondPart = '**/api/v1/admin/uploads/*/parts/2'
  await page.route(secondPart, async (route) => { await blocked; await route.continue().catch(() => undefined) })
  const firstPart = page.waitForResponse((response) => response.url().includes('/parts/1') && response.ok())
  await page.locator('input[type="file"]').setInputFiles(join(fixtureDir, 'resume.pdf'))
  await firstPart
  await page.reload()
  release()
  await page.unroute(secondPart)
  await page.getByLabel('允许下载').check()
  await page.locator('input[type="file"]').setInputFiles(join(fixtureDir, 'resume.pdf'))
  await expect(page.getByText('文件已进入文件中心')).toBeVisible({ timeout: 120_000 })
})

async function createTeachingPathAfterLogin(page: import('@playwright/test').Page) {
  await login(page, 'admin', adminPassword!)
  const suffix = `resume-${Date.now()}`
  const path = await createTeachingPath(page, suffix)
  path.draft = await saveDraft(page, path.draft, { bodyMarkdown: '# 续传测试' })
  return path
}

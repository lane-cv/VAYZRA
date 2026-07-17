import { expect, test } from '@playwright/test'
import { changePassword, createStudent, csrfHeader, login } from './helpers'

const adminPassword = process.env.E2E_ADMIN_PASSWORD
const studentPassword = process.env.E2E_STUDENT_PASSWORD
const studentNewPassword = process.env.E2E_STUDENT_NEW_PASSWORD

test.beforeAll(() => {
  if (!adminPassword || !studentPassword || !studentNewPassword) {
    throw new Error('Set E2E_ADMIN_PASSWORD, E2E_STUDENT_PASSWORD, and E2E_STUDENT_NEW_PASSWORD.')
  }
})

test('teacher creates student and student is isolated from admin APIs', async ({ browser }) => {
  const suffix = `${Date.now()}${Math.floor(Math.random() * 10000)}`
  const username = `e2e-${suffix}`
  const admin = await browser.newContext()
  const student = await browser.newContext()
  try {
    const adminPage = await admin.newPage()
    await login(adminPage, 'admin', adminPassword!)
    await expect(adminPage).toHaveURL(/admin/)
    await adminPage.goto('/admin/students')
    await createStudent(adminPage, username, '林同学', studentPassword!)

    const studentPage = await student.newPage()
    await login(studentPage, username, studentPassword!)
    await expect(studentPage).toHaveURL(/change-password/)
    await changePassword(studentPage, studentPassword!, studentNewPassword!)
    const denied = await studentPage.request.get('/api/v1/admin/students')
    expect(denied.status()).toBe(403)
    await expect(studentPage.getByText('学生管理')).toHaveCount(0)

    const students = await adminPage.request.get('/api/v1/admin/students')
    const record = (await students.json()).data.find((item: { username: string }) => item.username === username)
    expect(record).toBeTruthy()
    const disabled = await adminPage.request.post(`/api/v1/admin/students/${record.id}/status`, {
      data: { status: 'disabled' }, headers: await csrfHeader(adminPage),
    })
    expect(disabled.status()).toBe(204)
    expect((await studentPage.request.get('/api/v1/auth/me')).status()).toBe(401)
  } finally {
    await student.close()
    await admin.close()
  }
})

test('authentication errors are generic and mutation defenses expose request IDs', async ({ request }) => {
  const origin = process.env.E2E_BASE_URL ?? 'http://127.0.0.1:8080'
  const requestOptions = { headers: { Origin: origin }, data: { username: 'missing-e2e', password: 'wrong password 123' } }
  const unknown = await request.post('/api/v1/auth/login', requestOptions)
  const wrong = await request.post('/api/v1/auth/login', { headers: { Origin: origin }, data: { username: 'admin', password: 'wrong password 123' } })
  expect(unknown.status()).toBe(401)
  const unknownError = (await unknown.json()).error
  const wrongError = (await wrong.json()).error
  expect({ code: unknownError.code, message: unknownError.message }).toEqual({ code: wrongError.code, message: wrongError.message })
  expect(JSON.stringify(unknownError)).not.toContain('hl_session')

  const crossOrigin = await request.post('/api/v1/auth/login', {
    data: { username: 'admin', password: 'wrong password 123' }, headers: { Origin: 'https://evil.example' },
  })
  expect(crossOrigin.status()).toBe(403)
  expect(crossOrigin.headers()['x-request-id']).toBeTruthy()
  expect((await crossOrigin.json()).error.requestId).toBeTruthy()
})

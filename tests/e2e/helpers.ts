import { expect, type Page } from '@playwright/test'

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
  await expect(page).not.toHaveURL(/change-password/);
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

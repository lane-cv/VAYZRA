import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { routerKey } from 'vue-router'
import ChangePasswordView from './ChangePasswordView.vue'
const mountView = () => {
  const replace = vi.fn()
  return { wrapper: mount(ChangePasswordView, { global: { plugins: [createPinia()], provide: { [routerKey as symbol]: { replace } } } }), replace }
}
const mountWrapper = () => mountView().wrapper
describe('ChangePasswordView', () => {
  beforeEach(() => { vi.restoreAllMocks(); document.cookie = 'hl_csrf=csrf-value; path=/' })
  it('requires matching new-password confirmation before submitting', async () => {
    const wrapper = mountWrapper(); await wrapper.get('input[aria-label="当前密码"]').setValue('old password'); await wrapper.get('input[aria-label="新密码"]').setValue('new password'); await wrapper.get('input[aria-label="确认新密码"]').setValue('other password'); await wrapper.get('form').trigger('submit'); expect(wrapper.text()).toContain('两次输入的新密码不一致')
  })
  it('shows request ID and clears all passwords after a failed submission', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify({ error: { code: 'invalid_credentials', message: '用户名或密码错误', requestId: 'req-3' } }), { status: 401 })); const wrapper = mountWrapper(); await wrapper.get('input[aria-label="当前密码"]').setValue('old password'); await wrapper.get('input[aria-label="新密码"]').setValue('new password'); await wrapper.get('input[aria-label="确认新密码"]').setValue('new password'); await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(wrapper.text()).toContain('支持编号：req-3'); expect((wrapper.get('input[aria-label="当前密码"]').element as HTMLInputElement).value).toBe(''); expect((wrapper.get('input[aria-label="新密码"]').element as HTMLInputElement).value).toBe(''); expect((wrapper.get('input[aria-label="确认新密码"]').element as HTMLInputElement).value).toBe('')
  })
  it('logs out through the session action and redirects to login', async () => {
    const { wrapper, replace } = mountView()
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(null, { status: 204 }))
    await wrapper.get('button[type="button"]').trigger('click'); await flushPromises()
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/auth/logout', expect.objectContaining({ method: 'POST' }))
    expect(replace).toHaveBeenCalledWith('/login')
  })
  it('keeps logout safe after an API failure without exposing password values', async () => {
    const { wrapper, replace } = mountView()
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify({ error: { message: 'server secret' } }), { status: 500 }))
    await wrapper.get('input[aria-label="当前密码"]').setValue('private-current'); await wrapper.get('input[aria-label="新密码"]').setValue('private-new')
    await wrapper.get('button[type="button"]').trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain('已清除本机登录状态')
    expect(wrapper.text()).not.toContain('private-current'); expect(wrapper.text()).not.toContain('private-new')
    expect(replace).toHaveBeenCalledWith('/login')
  })
})
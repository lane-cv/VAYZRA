import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { routerKey } from 'vue-router'
import ChangePasswordView from './ChangePasswordView.vue'
const mountView = () => mount(ChangePasswordView, { global: { plugins: [createPinia()], provide: { [routerKey as symbol]: { replace: vi.fn() } } } })
describe('ChangePasswordView', () => {
  beforeEach(() => vi.restoreAllMocks())
  it('requires matching new-password confirmation before submitting', async () => {
    const wrapper = mountView(); await wrapper.get('input[aria-label="当前密码"]').setValue('old password'); await wrapper.get('input[aria-label="新密码"]').setValue('new password'); await wrapper.get('input[aria-label="确认新密码"]').setValue('other password'); await wrapper.get('form').trigger('submit'); expect(wrapper.text()).toContain('两次输入的新密码不一致')
  })
  it('shows request ID and clears all passwords after a failed submission', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify({ error: { code: 'invalid_credentials', message: '用户名或密码错误', requestId: 'req-3' } }), { status: 401 })); const wrapper = mountView(); await wrapper.get('input[aria-label="当前密码"]').setValue('old password'); await wrapper.get('input[aria-label="新密码"]').setValue('new password'); await wrapper.get('input[aria-label="确认新密码"]').setValue('new password'); await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(wrapper.text()).toContain('支持编号：req-3'); expect((wrapper.get('input[aria-label="当前密码"]').element as HTMLInputElement).value).toBe(''); expect((wrapper.get('input[aria-label="新密码"]').element as HTMLInputElement).value).toBe(''); expect((wrapper.get('input[aria-label="确认新密码"]').element as HTMLInputElement).value).toBe('')
  })
})
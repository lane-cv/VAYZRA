import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { routerKey } from 'vue-router'
import LoginView from './LoginView.vue'
const mountView = () => mount(LoginView, { global: { plugins: [createPinia()], provide: { [routerKey as symbol]: { replace: vi.fn() } } } })
const apiError = (code: string, requestId: string) => new Response(JSON.stringify({ error: { code, message: '用户名或密码错误', requestId } }), { status: 401 })
describe('LoginView', () => {
  beforeEach(() => vi.restoreAllMocks())
  afterEach(() => vi.unstubAllGlobals())
  it('labels required account and password fields', () => { const wrapper = mountView(); expect(wrapper.find('input[aria-label="账号"]').exists()).toBe(true); expect(wrapper.find('input[aria-label="密码"]').exists()).toBe(true) })
  it('shows a generic error and clears the password after a failed Enter submission', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(apiError('invalid_credentials', 'req-1')); const wrapper = mountView(); await wrapper.get('input[aria-label="账号"]').setValue('student01'); await wrapper.get('input[aria-label="密码"]').setValue('wrong'); await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(wrapper.text()).toContain('账号或密码不正确，请重试'); expect((wrapper.get('input[aria-label="密码"]').element as HTMLInputElement).value).toBe('')
  })
  it('disables submission while login is pending', async () => {
    vi.spyOn(globalThis, 'fetch').mockReturnValue(new Promise(() => undefined)); const wrapper = mountView(); await wrapper.get('input[aria-label="账号"]').setValue('student01'); await wrapper.get('input[aria-label="密码"]').setValue('password'); await wrapper.get('form').trigger('submit'); expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeDefined()
  })
  it('loads a refreshable accessible CAPTCHA challenge when required', async () => {
    const createObjectURL = vi.fn(() => 'blob:captcha'); Object.defineProperty(URL, 'createObjectURL', { value: createObjectURL, configurable: true }); Object.defineProperty(URL, 'revokeObjectURL', { value: vi.fn(), configurable: true }); vi.spyOn(globalThis, 'fetch').mockImplementation((url) => Promise.resolve(String(url).includes('/auth/login') ? apiError('login_challenge_required', 'req-2') : new Response(new Blob(['png']), { headers: { 'X-Challenge-ID': 'challenge-1' } }))); const wrapper = mountView(); await wrapper.get('input[aria-label="账号"]').setValue('student01'); await wrapper.get('input[aria-label="密码"]').setValue('password'); await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(wrapper.get('img[alt="登录验证码"]').attributes('src')).toBe('blob:captcha'); expect(wrapper.find('input[aria-label="验证码答案"]').exists()).toBe(true); await wrapper.get('button[aria-label="刷新验证码"]').trigger('click'); expect(fetch).toHaveBeenCalledTimes(3)
  })
})
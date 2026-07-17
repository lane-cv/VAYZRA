import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { routerKey } from 'vue-router'
import LoginView from './LoginView.vue'
const mountView = () => mount(LoginView, { global: { plugins: [createPinia()], provide: { [routerKey as symbol]: { replace: vi.fn() } } } })
const apiError = (code: string, requestId: string) => new Response(JSON.stringify({ error: { code, message: '用户名或密码错误', requestId } }), { status: 401 })
function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  return { promise: new Promise<T>((res, rej) => { resolve = res; reject = rej }), resolve, reject }
}
const challengeResponse = (id: string, image = id) => new Response(new Blob([image]), { headers: { 'X-Challenge-ID': id } })
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
  it('keeps the latest CAPTCHA refresh when responses resolve out of order', async () => {
    const first = deferred<Response>(); const second = deferred<Response>()
    const createObjectURL = vi.fn(() => ['blob:latest', 'blob:stale'][createObjectURL.mock.calls.length - 1])
    const revokeObjectURL = vi.fn()
    Object.defineProperty(URL, 'createObjectURL', { value: createObjectURL, configurable: true })
    Object.defineProperty(URL, 'revokeObjectURL', { value: revokeObjectURL, configurable: true })
    let challengeCalls = 0
    vi.spyOn(globalThis, 'fetch').mockImplementation(() => (++challengeCalls === 1 ? first.promise : second.promise))
    const wrapper = mountView()
    const firstRefresh = (wrapper.vm as any).refreshChallenge()
    const secondRefresh = (wrapper.vm as any).refreshChallenge()
    second.resolve(challengeResponse('latest')); await secondRefresh
    first.resolve(challengeResponse('stale')); await firstRefresh
    expect(wrapper.get('img[alt="登录验证码"]').attributes('src')).toBe('blob:latest')
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:stale')
  })
  it('clears the CAPTCHA ID and image when a refresh fails', async () => {
    Object.defineProperty(URL, 'createObjectURL', { value: vi.fn(() => 'blob:initial'), configurable: true })
    Object.defineProperty(URL, 'revokeObjectURL', { value: vi.fn(), configurable: true })
    let challengeCalls = 0
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockImplementation((url) => Promise.resolve(String(url).includes('/auth/login') ? apiError('login_challenge_required', 'req-4') : ++challengeCalls === 1 ? challengeResponse('initial') : new Response(null, { status: 500 })))
    const wrapper = mountView()
    await wrapper.get('input[aria-label="账号"]').setValue('student01'); await wrapper.get('input[aria-label="密码"]').setValue('password'); await wrapper.get('form').trigger('submit'); await flushPromises()
    await wrapper.get('button[aria-label="刷新验证码"]').trigger('click'); await flushPromises()
    expect(wrapper.find('img[alt="登录验证码"]').exists()).toBe(false)
    await wrapper.get('input[aria-label="密码"]').setValue('password'); await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(fetchMock.mock.calls.filter((call) => String(call[0]).includes('/auth/login')).pop()?.[1]).toEqual(expect.objectContaining({ body: JSON.stringify({ username: 'student01', password: 'password' }) }))
  })
  it('aborts a pending CAPTCHA refresh and revokes its image on unmount', async () => {
    const pendingRefresh = deferred<Response>()
    Object.defineProperty(URL, 'createObjectURL', { value: vi.fn(() => 'blob:initial'), configurable: true })
    const revokeObjectURL = vi.fn(); Object.defineProperty(URL, 'revokeObjectURL', { value: revokeObjectURL, configurable: true })
    let challengeCalls = 0
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockImplementation((url) => Promise.resolve(String(url).includes('/auth/login') ? apiError('login_challenge_required', 'req-5') : ++challengeCalls === 1 ? challengeResponse('initial') : pendingRefresh.promise))
    const wrapper = mountView()
    await wrapper.get('input[aria-label="账号"]').setValue('student01'); await wrapper.get('input[aria-label="密码"]').setValue('password'); await wrapper.get('form').trigger('submit'); await flushPromises()
    await wrapper.get('button[aria-label="刷新验证码"]').trigger('click')
    const signal = (fetchMock.mock.calls[fetchMock.mock.calls.length - 1]?.[1] as RequestInit).signal as AbortSignal
    wrapper.unmount()
    expect(signal.aborted).toBe(true)
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:initial')
  })
})
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { reactive } from 'vue'
import { routeLocationKey, routerKey } from 'vue-router'
import ConsoleLayout from './ConsoleLayout.vue'
import { useSessionStore } from '../stores/session'
import { useNotificationStore } from '../stores/notifications'
import { useAIRunStore } from '../stores/aiRuns'

function mountLayout(role: 'admin' | 'student' = 'student') {
  const pinia = createPinia()
  const session = useSessionStore(pinia)
  session.setUser({ id: 'u1', username: role === 'admin' ? 'teacher' : 'student01', displayName: role === 'admin' ? '张老师' : '林同学', role, mustChangePassword: false })
  const notifications = useNotificationStore(pinia)
  const aiRuns = useAIRunStore(pinia)
  notifications.start = vi.fn()
  notifications.stop = vi.fn()
  const route = reactive({ fullPath: '/student' })
  const replace = vi.fn()
  const wrapper = mount(ConsoleLayout, { attachTo: document.body, global: { plugins: [pinia], provide: { [routerKey as symbol]: { replace }, [routeLocationKey as symbol]: route }, stubs: { RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' }, RouterView: true, ApplicationUpdateBadge: true } } })
  return { wrapper, route, notifications, aiRuns, session }
}

describe('ConsoleLayout drawer', () => {
  beforeEach(() => {
    localStorage.clear()
    document.documentElement.removeAttribute('data-theme')
    vi.stubGlobal('matchMedia', vi.fn((query: string) => ({ matches: query.includes('max-width'), addEventListener: vi.fn(), removeEventListener: vi.fn() })))
  })
  afterEach(() => vi.unstubAllGlobals())
  it('exposes and controls the mobile navigation drawer', async () => {
    const { wrapper } = mountLayout()
    const trigger = wrapper.get('button[aria-label="打开导航"]')
    expect(trigger.attributes('aria-expanded')).toBe('false')
    expect(trigger.attributes('aria-controls')).toBe('console-navigation')
    expect(wrapper.findAll('#console-navigation')).toHaveLength(1)
    expect(wrapper.get('nav').attributes('id')).toBe('console-navigation')
    expect(wrapper.get('nav').attributes('aria-label')).toBe('主导航')
    expect(document.getElementById(trigger.attributes('aria-controls')!)).toBe(wrapper.get('nav').element)
    expect(wrapper.get('aside').attributes('aria-hidden')).toBe('true')
    await trigger.trigger('click'); await flushPromises()
    expect(document.activeElement).toBe(wrapper.get('nav a').element)
    expect(trigger.attributes('aria-expanded')).toBe('true')
    expect(wrapper.get('aside').attributes('aria-hidden')).toBeUndefined()
  })
  it('makes the page inert and locks scrolling while the mobile drawer is open', async () => {
    document.body.style.overflow = 'clip'
    const { wrapper } = mountLayout()
    const trigger = wrapper.get('button[aria-label="打开导航"]')

    expect(wrapper.get('.content-wrap').attributes('inert')).toBeUndefined()
    await trigger.trigger('click')
    await flushPromises()
    expect(wrapper.get('.content-wrap').attributes('inert')).toBeDefined()
    expect(document.body.style.overflow).toBe('hidden')

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await flushPromises()
    expect(wrapper.get('.content-wrap').attributes('inert')).toBeUndefined()
    expect(document.body.style.overflow).toBe('clip')
    wrapper.unmount()
    document.body.style.overflow = ''
  })
  it('traps forward and reverse Tab focus inside the open mobile drawer', async () => {
    const { wrapper } = mountLayout()
    await wrapper.get('button[aria-label="打开导航"]').trigger('click')
    await flushPromises()
    const first = wrapper.get('nav a')
    const last = wrapper.get('aside button[aria-label="切换至深色模式"]')

    ;(last.element as HTMLElement).focus()
    const forward = new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true })
    document.dispatchEvent(forward)
    expect(forward.defaultPrevented).toBe(true)
    expect(document.activeElement).toBe(first.element)

    ;(first.element as HTMLElement).focus()
    const reverse = new KeyboardEvent('keydown', { key: 'Tab', shiftKey: true, bubbles: true, cancelable: true })
    document.dispatchEvent(reverse)
    expect(reverse.defaultPrevented).toBe(true)
    expect(document.activeElement).toBe(last.element)
    wrapper.unmount()
  })
  it('links teachers to student management', () => {
    const { wrapper } = mountLayout('admin')
    expect(wrapper.findAll('application-update-badge-stub')).toHaveLength(1)
    expect(wrapper.get('a[href="/admin/students"]').text()).toContain('学生管理')
    expect(wrapper.get('a[href="/admin/teaching"]').text()).toContain('教学管理')
    expect(wrapper.get('a[href="/admin/files"]').text()).toContain('文件中心')
    expect(wrapper.get('a[href="/admin/questions"]').text()).toContain('问题答疑')
    expect(wrapper.get('a[href="/admin/ai"]').text()).toContain('AI 管理')
    expect(wrapper.get('a[href="/admin/ai-usage"]').text()).toContain('用量统计')
    expect(wrapper.findAll('a[href="/admin/ai-usage"]')).toHaveLength(1)
    expect(wrapper.get('a[href="/admin/settings"]').text()).toContain('系统设置')
    expect(wrapper.findAll('a[href="/admin/settings"]')).toHaveLength(1)
    expect(wrapper.get('a[href="/admin/backups"]').text()).toContain('备份记录')
    expect(wrapper.findAll('a[href="/admin/backups"]')).toHaveLength(1)
    expect(wrapper.get('a[href="/admin/alerts"]').text()).toContain('告警中心')
    expect(wrapper.findAll('a[href="/admin/alerts"]')).toHaveLength(1)
    expect(wrapper.find('a[href="/admin/audit"]').exists()).toBe(false)
    expect(wrapper.get('a[href="/notifications"]').text()).toContain('通知中心')
    expect(wrapper.find('a[href="/student/questions"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('即将开放')
  })
  it('never renders teaching administration for students', () => {
    const { wrapper } = mountLayout('student')
    expect(wrapper.find('application-update-badge-stub').exists()).toBe(false)
    expect(wrapper.find('a[href="/admin/teaching"]').exists()).toBe(false)
    expect(wrapper.get('a[href="/student/learning"]').text()).toContain('课程学习')
    expect(wrapper.get('a[href="/student/questions"]').text()).toContain('答疑中心')
    expect(wrapper.findAll('a[href="/student/questions"]')).toHaveLength(1)
    expect(wrapper.text()).not.toContain('AI 答疑')
    expect(wrapper.text()).not.toContain('老师答疑')
    expect(wrapper.get('a[href="/notifications"]').text()).toContain('通知中心')
    expect(wrapper.find('a[href="/student/notifications"]').exists()).toBe(false)
    expect(wrapper.find('a[href="/admin/questions"]').exists()).toBe(false)
    expect(wrapper.find('a[href="/admin/ai"]').exists()).toBe(false)
    expect(wrapper.find('a[href="/admin/ai-usage"]').exists()).toBe(false)
    expect(wrapper.find('a[href="/admin/settings"]').exists()).toBe(false)
    expect(wrapper.find('a[href="/admin/backups"]').exists()).toBe(false)
    expect(wrapper.find('a[href="/admin/alerts"]').exists()).toBe(false)
  })
  it('persists desktop collapse state and keeps navigation names available to assistive technology', async () => {
    vi.stubGlobal('matchMedia', vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() })))
    const { wrapper } = mountLayout('admin')
    const toggle = wrapper.get('button[aria-label="收起侧栏"]')
    await toggle.trigger('click')
    expect(wrapper.classes()).toContain('sidebar-collapsed')
    expect(localStorage.getItem('happylearn-sidebar-collapsed')).toBe('true')
    expect(wrapper.get('a[href="/admin/students"]').attributes('aria-label')).toBe('学生管理')
    wrapper.unmount()
    const next = mountLayout('admin').wrapper
    expect(next.classes()).toContain('sidebar-collapsed')
    expect(next.find('button[aria-label="展开侧栏"]').exists()).toBe(true)
  })
  it('persists the selected color theme and reflects it on the document root', async () => {
    vi.stubGlobal('matchMedia', vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() })))
    const { wrapper } = mountLayout()
    expect(document.documentElement.dataset.theme).toBe('light')
    await wrapper.get('button[aria-label="切换至深色模式"]').trigger('click')
    expect(document.documentElement.dataset.theme).toBe('dark')
    expect(localStorage.getItem('happylearn-theme')).toBe('dark')
    expect(wrapper.find('button[aria-label="切换至浅色模式"]').exists()).toBe(true)
  })
  it('closes for Escape and route changes, restoring focus to the trigger', async () => {
    const { wrapper, route } = mountLayout(); const trigger = wrapper.get('button[aria-label="打开导航"]')
    await trigger.trigger('click'); await flushPromises(); document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' })); await flushPromises()
    expect(trigger.attributes('aria-expanded')).toBe('false'); expect(document.activeElement).toBe(trigger.element)
    await trigger.trigger('click'); route.fullPath = '/student/next'; await flushPromises()
    expect(trigger.attributes('aria-expanded')).toBe('false')
  })
  it('owns one polling lifecycle and stops it before logout clears the session', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(null, { status: 204 })))
    const { wrapper, notifications, aiRuns, session } = mountLayout('student')
    const clearAI = vi.spyOn(aiRuns, 'clearAll')
    const clear = vi.spyOn(session, 'clear')
    expect(notifications.start).toHaveBeenCalledOnce(); expect(notifications.start).toHaveBeenCalledWith('u1')
    await wrapper.get('.logout-button').trigger('click'); await flushPromises()
    expect(notifications.stop).toHaveBeenCalled(); expect(clearAI).toHaveBeenCalled(); expect(clearAI.mock.invocationCallOrder[0]).toBeLessThan(clear.mock.invocationCallOrder[0]); expect(vi.mocked(notifications.stop).mock.invocationCallOrder[0]).toBeLessThan(clear.mock.invocationCallOrder[0]); const callsBeforeUnmount = vi.mocked(notifications.stop).mock.calls.length; wrapper.unmount(); expect(notifications.stop).toHaveBeenCalledTimes(callsBeforeUnmount + 1)
  })

  it('clears in-memory AI subscriptions before a different account renders', async () => {
    const { wrapper, aiRuns, session } = mountLayout('student')
    const clearAI = vi.spyOn(aiRuns, 'clearAll')
    session.setUser({ id: 'u2', username: 'student02', displayName: '周同学', role: 'student', mustChangePassword: false })
    await flushPromises()
    expect(clearAI).toHaveBeenCalledOnce()
    wrapper.unmount()
  })
})

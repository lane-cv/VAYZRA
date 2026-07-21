import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { reactive } from 'vue'
import { routeLocationKey, routerKey } from 'vue-router'
import ConsoleLayout from './ConsoleLayout.vue'
import { useSessionStore } from '../stores/session'

function mountLayout(role: 'admin' | 'student' = 'student') {
  const pinia = createPinia()
  useSessionStore(pinia).setUser({ id: 'u1', username: role === 'admin' ? 'teacher' : 'student01', displayName: role === 'admin' ? '张老师' : '林同学', role, mustChangePassword: false })
  const route = reactive({ fullPath: '/student' })
  const replace = vi.fn()
  const wrapper = mount(ConsoleLayout, { attachTo: document.body, global: { plugins: [pinia], provide: { [routerKey as symbol]: { replace }, [routeLocationKey as symbol]: route }, stubs: { RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' }, RouterView: true } } })
  return { wrapper, route }
}

describe('ConsoleLayout drawer', () => {
  beforeEach(() => vi.stubGlobal('matchMedia', vi.fn(() => ({ matches: true, addEventListener: vi.fn(), removeEventListener: vi.fn() }))))
  afterEach(() => vi.unstubAllGlobals())
  it('exposes and controls the mobile navigation drawer', async () => {
    const { wrapper } = mountLayout()
    const trigger = wrapper.get('button[aria-label="打开导航"]')
    expect(trigger.attributes('aria-expanded')).toBe('false')
    expect(trigger.attributes('aria-controls')).toBe('console-navigation')
    expect(wrapper.get('aside').attributes('aria-hidden')).toBe('true')
    await trigger.trigger('click'); await flushPromises()
    expect(document.activeElement).toBe(wrapper.get('nav a').element)
    expect(trigger.attributes('aria-expanded')).toBe('true')
    expect(wrapper.get('aside').attributes('aria-hidden')).toBeUndefined()
  })
  it('links teachers to student management', () => {
    const { wrapper } = mountLayout('admin')
    expect(wrapper.get('a[href="/admin/students"]').text()).toContain('学生管理')
    expect(wrapper.get('a[href="/admin/teaching"]').text()).toContain('教学管理')
    expect(wrapper.get('a[href="/admin/files"]').text()).toContain('文件中心')
    expect(wrapper.text()).not.toContain('即将开放')
  })
  it('never renders teaching administration for students', () => {
    const { wrapper } = mountLayout('student')
    expect(wrapper.find('a[href="/admin/teaching"]').exists()).toBe(false)
    expect(wrapper.get('a[href="/student/learning"]').text()).toContain('课程学习')
  })
  it('closes for Escape and route changes, restoring focus to the trigger', async () => {
    const { wrapper, route } = mountLayout(); const trigger = wrapper.get('button[aria-label="打开导航"]')
    await trigger.trigger('click'); await flushPromises(); document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' })); await flushPromises()
    expect(trigger.attributes('aria-expanded')).toBe('false'); expect(document.activeElement).toBe(trigger.element)
    await trigger.trigger('click'); route.fullPath = '/student/next'; await flushPromises()
    expect(trigger.attributes('aria-expanded')).toBe('false')
  })
})

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { reactive } from 'vue'
import { routeLocationKey, routerKey } from 'vue-router'
import ConsoleLayout from './ConsoleLayout.vue'

function mountLayout() {
  const route = reactive({ fullPath: '/student' })
  const replace = vi.fn()
  const wrapper = mount(ConsoleLayout, { attachTo: document.body, global: { plugins: [createPinia()], provide: { [routerKey as symbol]: { replace }, [routeLocationKey as symbol]: route }, stubs: { RouterLink: { template: '<a href="#"><slot /></a>' }, RouterView: true } } })
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
  it('closes for Escape and route changes, restoring focus to the trigger', async () => {
    const { wrapper, route } = mountLayout(); const trigger = wrapper.get('button[aria-label="打开导航"]')
    await trigger.trigger('click'); await flushPromises(); document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' })); await flushPromises()
    expect(trigger.attributes('aria-expanded')).toBe('false'); expect(document.activeElement).toBe(trigger.element)
    await trigger.trigger('click'); route.fullPath = '/student/next'; await flushPromises()
    expect(trigger.attributes('aria-expanded')).toBe('false')
  })
})
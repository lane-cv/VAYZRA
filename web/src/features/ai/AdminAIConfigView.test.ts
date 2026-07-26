import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import AdminAIConfigView from './AdminAIConfigView.vue'
import { useSessionStore } from '../../stores/session'

vi.mock('./adminApi', () => ({
  listProviders: vi.fn().mockResolvedValue([]),
  listModels: vi.fn().mockResolvedValue([]),
  listPrompts: vi.fn().mockResolvedValue([]),
  readLimits: vi.fn().mockResolvedValue({
    global: {
      dailyRequests: { mode: 'disabled' },
      monthlyRequests: { mode: 'disabled' },
      dailyTokens: { mode: 'disabled' },
      monthlyTokens: { mode: 'disabled' },
      version: 1,
    },
    students: {},
  }),
  createProvider: vi.fn(),
  updateProvider: vi.fn(),
  activateProvider: vi.fn(),
  testProvider: vi.fn(),
  putModel: vi.fn(),
  putPrompt: vi.fn(),
  putGlobalLimits: vi.fn(),
  putStudentLimits: vi.fn(),
}))

function mountView(role: 'admin' | 'student') {
  const pinia = createPinia()
  useSessionStore(pinia).setUser({
    id: 'user-1',
    username: role === 'admin' ? 'teacher' : 'student',
    displayName: role === 'admin' ? '张老师' : '林同学',
    role,
    mustChangePassword: false,
  })
  return mount(AdminAIConfigView, { global: { plugins: [pinia] } })
}

describe('AdminAIConfigView', () => {
  beforeEach(() => vi.clearAllMocks())

  it('gives admins four accessible configuration tabs', async () => {
    const wrapper = mountView('admin')
    await flushPromises()
    expect(wrapper.get('h1').text()).toBe('AI 管理')
    expect(wrapper.findAll('[role="tab"]').map((tab) => tab.text())).toEqual([
      '供应商配置',
      '模型路由',
      '提示词',
      '额度策略',
    ])
    await wrapper.get('[role="tab"][aria-controls="model-routing-panel"]').trigger('click')
    expect(wrapper.get('#model-routing-panel').attributes('role')).toBe('tabpanel')
  })

  it('does not expose configuration content to students even when mounted directly', () => {
    const wrapper = mountView('student')
    expect(wrapper.text()).toContain('无权访问 AI 管理')
    expect(wrapper.find('[role="tablist"]').exists()).toBe(false)
  })
})

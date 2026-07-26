import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { APIError } from '../../api/client'
import AILimitsEditor from './AILimitsEditor.vue'
import * as api from './adminApi'

vi.mock('./adminApi', async (importOriginal) => {
  const original = await importOriginal<typeof import('./adminApi')>()
  return {
    ...original,
    readLimits: vi.fn(),
    listAIConfigStudents: vi.fn(),
    putGlobalLimits: vi.fn(),
    putStudentLimits: vi.fn(),
  }
})

const globalLimits: api.LimitView = {
  dailyRequests: { mode: 'limit', value: 10 },
  monthlyRequests: { mode: 'disabled' },
  dailyTokens: { mode: 'limit', value: 1000 },
  monthlyTokens: { mode: 'disabled' },
  version: 5,
}
const studentLimits: api.LimitView = {
  dailyRequests: { mode: 'inherit' },
  monthlyRequests: { mode: 'disabled' },
  dailyTokens: { mode: 'limit', value: 300 },
  monthlyTokens: { mode: 'inherit' },
  version: 2,
}

describe('AILimitsEditor', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(api.readLimits).mockResolvedValue({
      global: globalLimits,
      students: { 'student-1': studentLimits },
    })
    vi.mocked(api.listAIConfigStudents).mockResolvedValue({
      items: [{
        id: 'student-1',
        username: 'lin01',
        displayName: '林同学',
      }],
    })
  })

  it('uses explicit disabled, inherit, and limit selectors with numeric values', async () => {
    vi.mocked(api.putGlobalLimits).mockResolvedValue({ ...globalLimits, version: 6 })
    const wrapper = mount(AILimitsEditor)
    await flushPromises()
    const globalPanel = wrapper.get('[data-scope="global"]')
    expect(globalPanel.findAll('select option').map((option) => option.attributes('value'))).toEqual(
      expect.arrayContaining(['disabled', 'limit']),
    )
    expect(globalPanel.find('option[value="inherit"]').exists()).toBe(false)
    await globalPanel.get('select[aria-label="全局每日请求模式"]').setValue('limit')
    await globalPanel.get('input[aria-label="全局每日请求上限"]').setValue('12')
    await globalPanel.get('button[aria-label="保存全局额度"]').trigger('click')
    await flushPromises()
    expect(api.putGlobalLimits).toHaveBeenCalledWith(expect.objectContaining({
      dailyRequests: { mode: 'limit', value: 12 },
      expectedVersion: 5,
    }))
  })

  it('searches students and preserves explicit inheritance semantics', async () => {
    vi.mocked(api.putStudentLimits).mockResolvedValue({ ...studentLimits, version: 3 })
    const wrapper = mount(AILimitsEditor)
    await flushPromises()
    await wrapper.get('input[aria-label="搜索学生"]').setValue('lin')
    expect(wrapper.get('button[aria-label="选择学生 lin01"]').isVisible()).toBe(true)
    await wrapper.get('button[aria-label="选择学生 lin01"]').trigger('click')
    const panel = wrapper.get('[data-scope="student"]')
    expect(panel.get('select[aria-label="学生每日请求模式"]').element).toHaveProperty('value', 'inherit')
    await panel.get('button[aria-label="保存学生额度"]').trigger('click')
    await flushPromises()
    expect(api.putStudentLimits).toHaveBeenCalledWith('student-1', expect.objectContaining({
      dailyRequests: { mode: 'inherit' },
      monthlyRequests: { mode: 'disabled' },
      dailyTokens: { mode: 'limit', value: 300 },
      expectedVersion: 2,
    }))
  })

  it('preserves a global conflict and rebases the form on the latest limits', async () => {
    const latestGlobal: api.LimitView = {
      ...globalLimits,
      dailyRequests: { mode: 'limit', value: 99 },
      version: 6,
    }
    vi.mocked(api.readLimits)
      .mockResolvedValueOnce({ global: globalLimits, students: {} })
      .mockResolvedValue({ global: latestGlobal, students: {} })
    vi.mocked(api.putGlobalLimits)
      .mockRejectedValueOnce(new APIError(409, 'config_conflict', '配置已更新', 'req-limits-conflict'))
      .mockResolvedValue({ ...latestGlobal, version: 7 })
    const wrapper = mount(AILimitsEditor)
    await flushPromises()
    await wrapper.get('input[aria-label="全局每日请求上限"]').setValue('12')
    await wrapper.get('button[aria-label="保存全局额度"]').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('支持编号：req-limits-conflict')
    expect(wrapper.get('input[aria-label="全局每日请求上限"]').element).toHaveProperty('value', '99')

    await wrapper.get('button[aria-label="保存全局额度"]').trigger('click')
    await flushPromises()
    expect(api.putGlobalLimits).toHaveBeenLastCalledWith(expect.objectContaining({
      dailyRequests: { mode: 'limit', value: 99 },
      expectedVersion: 6,
    }))
  })
})

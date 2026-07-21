import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import AudiencePicker from './AudiencePicker.vue'

function response(data: unknown, nextCursor?: string) {
  return new Response(JSON.stringify({ data, meta: nextCursor ? { nextCursor } : {} }))
}

describe('AudiencePicker', () => {
  beforeEach(() => vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response([
    { id: 'u1', username: 'student01', displayName: '林同学', status: 'active', mustChangePassword: false, createdAt: '2026-01-01T00:00:00Z' },
    { id: 'u2', username: 'student02', displayName: '陈同学', status: 'disabled', mustChangePassword: false, createdAt: '2026-01-01T00:00:00Z' },
  ]))))
  afterEach(() => vi.unstubAllGlobals())

  it('switches to selected students, searches locally, and excludes disabled accounts', async () => {
    const wrapper = mount(AudiencePicker, { props: { modelValue: { mode: 'all', userIds: [] } } })
    await flushPromises()
    await wrapper.get('input[value="selected"]').setValue(true)
    await wrapper.setProps({ modelValue: { mode: 'selected', userIds: [] } })
    await wrapper.get('input[aria-label="搜索学生"]').setValue('林')
    expect(wrapper.text()).toContain('林同学')
    expect(wrapper.text()).not.toContain('陈同学')
    await wrapper.get('input[aria-label="选择学生 student01"]').setValue(true)
    const updates = wrapper.emitted('update:modelValue') ?? []
    const update = updates[updates.length - 1]?.[0]
    expect(update).toEqual({ mode: 'selected', userIds: ['u1'] })
    await wrapper.setProps({ modelValue: { mode: 'selected', userIds: ['u1'] } })
    expect(wrapper.text()).toContain('已选择 1 人')
  })

  it('surfaces selected disabled accounts as publication blockers', async () => {
    const wrapper = mount(AudiencePicker, { props: { modelValue: { mode: 'selected', userIds: ['u2'] } } })
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('陈同学已被禁用')
    const validations = wrapper.emitted('validationChange') ?? []
    expect(validations[validations.length - 1]?.[0]).toEqual(['陈同学已被禁用，请从受众中移除'])
  })
})

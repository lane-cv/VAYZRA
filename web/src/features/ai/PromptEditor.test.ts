import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { APIError } from '../../api/client'
import PromptEditor from './PromptEditor.vue'
import * as api from './adminApi'

vi.mock('./adminApi', async (importOriginal) => {
  const original = await importOriginal<typeof import('./adminApi')>()
  return { ...original, listPrompts: vi.fn(), putPrompt: vi.fn() }
})

const prompts: api.PromptView[] = [
  { id: 'math-id', subject: 'math', version: 2, body: '数学提示词', active: true },
  { id: 'physics-id', subject: 'physics', version: 4, body: '物理提示词', active: true },
]

describe('PromptEditor', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(api.listPrompts).mockResolvedValue(prompts)
  })

  it('edits math and physics independently using their current versions', async () => {
    vi.mocked(api.putPrompt).mockResolvedValue({ ...prompts[0], version: 3, body: '新版数学提示词' })
    const wrapper = mount(PromptEditor)
    await flushPromises()
    expect(wrapper.text()).toContain('数学 · 版本 2')
    expect(wrapper.text()).toContain('物理 · 版本 4')
    await wrapper.get('textarea[aria-label="数学提示词"]').setValue('新版数学提示词')
    await wrapper.get('button[aria-label="保存数学提示词"]').trigger('click')
    await flushPromises()
    expect(api.putPrompt).toHaveBeenCalledWith('math', {
      body: '新版数学提示词',
      expectedVersion: 2,
    })
  })

  it('reloads on optimistic conflicts and displays the support request ID', async () => {
    const latest = [
      { ...prompts[0], version: 3, body: '其他管理员的新数学提示词' },
      prompts[1],
    ]
    vi.mocked(api.listPrompts)
      .mockResolvedValueOnce(prompts)
      .mockResolvedValue(latest)
    vi.mocked(api.putPrompt)
      .mockRejectedValueOnce(new APIError(409, 'config_conflict', '配置已更新', 'req-prompt'))
      .mockResolvedValue({ ...latest[0], version: 4 })
    const wrapper = mount(PromptEditor)
    await flushPromises()
    await wrapper.get('textarea[aria-label="数学提示词"]').setValue('我的过期提示词')
    await wrapper.get('button[aria-label="保存数学提示词"]').trigger('click')
    await flushPromises()
    expect(api.listPrompts).toHaveBeenCalledTimes(2)
    expect(wrapper.text()).toContain('支持编号：req-prompt')
    expect(wrapper.get('textarea[aria-label="数学提示词"]').element).toHaveProperty(
      'value',
      latest[0].body,
    )
    await wrapper.get('button[aria-label="保存数学提示词"]').trigger('click')
    await flushPromises()
    expect(api.putPrompt).toHaveBeenLastCalledWith('math', {
      body: latest[0].body,
      expectedVersion: 3,
    })
  })
})

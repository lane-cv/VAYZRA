import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { APIError } from '../../api/client'
import ProviderEditor from './ProviderEditor.vue'
import * as api from './adminApi'

vi.mock('./adminApi', async (importOriginal) => {
  const original = await importOriginal<typeof import('./adminApi')>()
  return {
    ...original,
    listProviders: vi.fn(),
    createProvider: vi.fn(),
    updateProvider: vi.fn(),
    activateProvider: vi.fn(),
    testProvider: vi.fn(),
  }
})

const provider: api.ProviderView = {
  id: '11111111-1111-4111-8111-111111111111',
  name: '主供应商',
  baseUrl: 'https://provider.example/v1',
  protocolMode: 'responses',
  active: false,
  hasKey: true,
  keyUpdatedAt: '2026-07-27T01:00:00Z',
  version: 3,
}

describe('ProviderEditor', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(api.listProviders).mockResolvedValue([provider])
    vi.stubGlobal('confirm', vi.fn(() => true))
  })

  it('requires HTTPS and a key for create, supports both protocols, and clears the secret after success', async () => {
    vi.mocked(api.createProvider).mockResolvedValue({ ...provider, id: 'new-provider' })
    const wrapper = mount(ProviderEditor)
    await flushPromises()
    await wrapper.get('button[aria-label="新建供应商"]').trigger('click')
    await wrapper.get('input[aria-label="供应商名称"]').setValue('新供应商')
    await wrapper.get('input[aria-label="供应商地址"]').setValue('http://provider.example')
    await wrapper.get('select[aria-label="协议模式"]').setValue('chat_completions')
    await wrapper.get('form').trigger('submit')
    expect(wrapper.text()).toContain('请使用 HTTPS 地址')
    expect(api.createProvider).not.toHaveBeenCalled()
    await wrapper.get('input[aria-label="供应商地址"]').setValue('https://provider.example/v1')
    await wrapper.get('form').trigger('submit')
    expect(wrapper.text()).toContain('新建供应商必须填写 API Key')
    await wrapper.get('input[aria-label="API Key"]').setValue('top-secret')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.createProvider).toHaveBeenCalledWith(expect.objectContaining({
      protocolMode: 'chat_completions',
      apiKey: 'top-secret',
    }))
    expect(wrapper.html()).not.toContain('top-secret')
    expect(wrapper.text()).toContain('已安全保存')
  })

  it('never fills a saved key while editing and only sends a replacement after explicit opt-in', async () => {
    vi.mocked(api.updateProvider).mockResolvedValue({ ...provider, version: 4 })
    const wrapper = mount(ProviderEditor)
    await flushPromises()
    await wrapper.get(`button[aria-label="编辑 ${provider.name}"]`).trigger('click')
    expect(wrapper.find('input[aria-label="API Key"]').exists()).toBe(false)
    expect(wrapper.html()).not.toContain('top-secret')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.updateProvider).toHaveBeenLastCalledWith(provider.id, expect.objectContaining({
      expectedVersion: 3,
      apiKey: undefined,
    }))
    await wrapper.get('button[aria-label="替换 API Key"]').trigger('click')
    await wrapper.get('input[aria-label="API Key"]').setValue('replacement-secret')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.updateProvider).toHaveBeenLastCalledWith(provider.id, expect.objectContaining({
      apiKey: 'replacement-secret',
    }))
    expect(wrapper.html()).not.toContain('replacement-secret')
  })

  it('confirms activation and cost-bearing connection tests', async () => {
    vi.mocked(api.activateProvider).mockResolvedValue({ ...provider, active: true, version: 4 })
    vi.mocked(api.testProvider).mockResolvedValue({
      ok: true,
      protocol: 'responses',
      latencyMs: 18,
    })
    const wrapper = mount(ProviderEditor)
    await flushPromises()
    await wrapper.get(`button[aria-label="启用 ${provider.name}"]`).trigger('click')
    await flushPromises()
    expect(confirm).toHaveBeenCalledWith(expect.stringContaining('设为当前供应商'))
    expect(api.activateProvider).toHaveBeenCalledWith(provider.id, provider.version)
    await wrapper.get(`button[aria-label="测试 ${provider.name} 的连接"]`).trigger('click')
    await flushPromises()
    expect(confirm).toHaveBeenCalledWith(expect.stringContaining('可能产生少量费用'))
    expect(wrapper.text()).toContain('连接成功')
  })

  it('shows request IDs and reloads stale versions after a conflict', async () => {
    vi.mocked(api.activateProvider).mockRejectedValue(
      new APIError(409, 'config_conflict', '配置已更新', 'req-conflict'),
    )
    const wrapper = mount(ProviderEditor)
    await flushPromises()
    await wrapper.get(`button[aria-label="启用 ${provider.name}"]`).trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('支持编号：req-conflict')
    expect(api.listProviders).toHaveBeenCalledTimes(2)
  })
})

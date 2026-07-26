import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import ModelRoutingEditor, {
  dollarsPerMillionToMicroUSD,
  microUSDToDollarsPerMillion,
} from './ModelRoutingEditor.vue'
import * as api from './adminApi'

vi.mock('./adminApi', async (importOriginal) => {
  const original = await importOriginal<typeof import('./adminApi')>()
  return { ...original, listProviders: vi.fn(), listModels: vi.fn(), putModel: vi.fn() }
})

const provider = {
  id: '11111111-1111-4111-8111-111111111111',
  name: 'Provider',
  baseUrl: 'https://provider.example',
  protocolMode: 'responses' as const,
  active: true,
  hasKey: true,
  keyUpdatedAt: '2026-07-27T00:00:00Z',
  version: 1,
}

describe('ModelRoutingEditor', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(api.listProviders).mockResolvedValue([provider])
    vi.mocked(api.listModels).mockResolvedValue([])
  })

  it('converts dollar prices exactly without floating point arithmetic', () => {
    expect(dollarsPerMillionToMicroUSD('0.123456')).toBe(123456)
    expect(dollarsPerMillionToMicroUSD('9007199254.740991')).toBe(9007199254740991)
    expect(dollarsPerMillionToMicroUSD('0.1234567')).toBeNull()
    expect(microUSDToDollarsPerMillion(123456)).toBe('0.123456')
  })

  it('configures separate text and vision routes with validated bounds and exact prices', async () => {
    vi.mocked(api.putModel).mockImplementation(async (_providerId, modelId, input) => ({
      id: modelId,
      providerId: provider.id,
      ...input,
      quotaBlockedAt: undefined,
      quotaBlockReason: undefined,
      version: 1,
    }))
    const wrapper = mount(ModelRoutingEditor)
    await flushPromises()
    expect(wrapper.findAll('fieldset').map((field) => field.get('legend').text())).toEqual([
      '文本模型路由',
      '视觉模型路由',
    ])
    const text = wrapper.get('[data-modality="text"]')
    await text.get('input[aria-label="文本模型 UUID"]').setValue('22222222-2222-4222-8222-222222222222')
    await text.get('input[aria-label="文本上游模型"]').setValue('text-model')
    await text.get('input[aria-label="文本上下文 Token"]').setValue('100')
    await text.get('input[aria-label="文本最大输出 Token"]').setValue('101')
    await text.get('input[aria-label="文本图片配额 Token"]').setValue('1')
    await text.get('input[aria-label="文本输入价格"]').setValue('0.123456')
    await text.get('input[aria-label="文本输出价格"]').setValue('2')
    await text.get('input[aria-label="文本连接超时"]').setValue('700')
    await text.get('input[aria-label="文本响应头超时"]').setValue('8000')
    await text.get('input[aria-label="文本流空闲超时"]').setValue('9000')
    await text.get('input[aria-label="文本总超时"]').setValue('10000')
    await text.get('button[type="submit"]').trigger('click')
    expect(wrapper.text()).toContain('最大输出 Token 不能超过上下文 Token')
    await text.get('input[aria-label="文本最大输出 Token"]').setValue('50')
    await text.get('button[type="submit"]').trigger('click')
    await flushPromises()
    expect(api.putModel).toHaveBeenCalledWith(
      provider.id,
      '22222222-2222-4222-8222-222222222222',
      expect.objectContaining({
        modality: 'text',
        inputPriceMicroUsd: 123456,
        outputPriceMicroUsd: 2000000,
        connectTimeoutMs: 700,
        responseHeaderTimeoutMs: 8000,
        idleStreamTimeoutMs: 9000,
        totalTimeoutMs: 10000,
      }),
    )
  })
})

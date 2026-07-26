import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import AdminAIUsageView from './AdminAIUsageView.vue'
import { useSessionStore } from '../../stores/session'
import { APIError } from '../../api/client'
import {
  listAIConfigStudents,
  listModels,
  listProviders,
  listUsageRuns,
  readUsageSummary,
} from './adminApi'

vi.mock('./adminApi', async (importOriginal) => {
  const original = await importOriginal<typeof import('./adminApi')>()
  return {
    ...original,
    listAIConfigStudents: vi.fn(),
    listModels: vi.fn(),
    listProviders: vi.fn(),
    listUsageRuns: vi.fn(),
    readUsageSummary: vi.fn(),
  }
})

const summary = {
  requests: 3,
  succeeded: 1,
  failed: 2,
  inputTokens: 21,
  outputTokens: 21,
  costMicroUSD: '9007199254741010',
  unknownUsage: 1,
  averageFirstByteMs: 120,
  averageTotalMs: 900,
}
const run = {
  id: 'run-1',
  studentId: '11111111-1111-4111-8111-111111111111',
  studentUsername: 'lin01',
  studentDisplayName: '林同学',
  modelId: '22222222-2222-4222-8222-222222222222',
  modelLabel: 'math-text',
  status: 'succeeded' as const,
  inputTokens: 13,
  outputTokens: 21,
  usageSource: 'upstream' as const,
  costMicroUSD: '19',
  firstByteMs: 120,
  totalMs: 900,
  createdAt: '2026-07-26T08:00:00Z',
}

function mountView(role: 'admin' | 'student' = 'admin') {
  const pinia = createPinia()
  useSessionStore(pinia).setUser({
    id: role === 'admin' ? 'admin-1' : 'student-1',
    username: role,
    displayName: role === 'admin' ? '张老师' : '林同学',
    role,
    mustChangePassword: false,
  })
  return mount(AdminAIUsageView, { attachTo: document.body, global: { plugins: [pinia] } })
}

describe('AdminAIUsageView', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(listAIConfigStudents).mockResolvedValue({
      items: [{ id: run.studentId, username: 'lin01', displayName: '林同学' }],
    })
    vi.mocked(listProviders).mockResolvedValue([{
      id: 'provider-1',
      name: '主供应商',
      baseUrl: 'https://example.invalid/v1',
      protocolMode: 'responses',
      active: true,
      hasKey: true,
      keyUpdatedAt: '2026-07-01T00:00:00Z',
      version: 1,
    }])
    vi.mocked(listModels).mockResolvedValue([{
      id: run.modelId,
      providerId: 'provider-1',
      upstreamModelId: 'math-text',
      modality: 'text',
      contextTokens: 1000,
      maxOutputTokens: 100,
      imageQuotaTokens: 100,
      inputPriceMicroUsd: 1,
      outputPriceMicroUsd: 1,
      connectTimeoutMs: 1000,
      responseHeaderTimeoutMs: 2000,
      idleStreamTimeoutMs: 2000,
      totalTimeoutMs: 3000,
      enabled: true,
      version: 1,
    }])
    vi.mocked(readUsageSummary).mockResolvedValue(summary)
    vi.mocked(listUsageRuns).mockResolvedValue({ items: [run], nextCursor: 'equal-time-cursor' })
  })

  it('is admin-only even when mounted directly', async () => {
    const wrapper = mountView('student')
    await flushPromises()
    expect(wrapper.text()).toContain('无权访问用量统计')
    expect(readUsageSummary).not.toHaveBeenCalled()
    expect(listUsageRuns).not.toHaveBeenCalled()
  })

  it('loads summaries and applies Shanghai date, student, model and status filters', async () => {
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.get('h1').text()).toBe('用量统计')
    expect(wrapper.text()).toContain('请求数')
    expect(wrapper.text()).toContain('$9,007,199,254.741010')

    await wrapper.get('input[name="fromDate"]').setValue('2026-07-01')
    await wrapper.get('input[name="toDate"]').setValue('2026-07-26')
    await wrapper.get('select[name="studentId"]').setValue(run.studentId)
    await wrapper.get('select[name="modelId"]').setValue(run.modelId)
    await wrapper.get('select[name="status"]').setValue('failed')
    await flushPromises()

    const expected = {
      studentId: run.studentId,
      modelId: run.modelId,
      status: 'failed',
      from: '2026-06-30T16:00:00.000Z',
      to: '2026-07-26T15:59:59.999Z',
      limit: 25,
    }
    expect(readUsageSummary).toHaveBeenLastCalledWith(expected, expect.any(AbortSignal))
    expect(listUsageRuns).toHaveBeenLastCalledWith(expected, expect.any(AbortSignal))
  })

  it('aborts stale replacement requests and appends the exact opaque cursor', async () => {
    const pending = new Promise<typeof summary>(() => undefined)
    vi.mocked(readUsageSummary).mockReturnValueOnce(pending)
    vi.mocked(listUsageRuns).mockReturnValueOnce(new Promise(() => undefined))
    const wrapper = mountView()
    await flushPromises()
    const firstSignal = vi.mocked(readUsageSummary).mock.calls[0][1]!

    await wrapper.get('select[name="status"]').setValue('cancelled')
    await flushPromises()
    expect(firstSignal.aborted).toBe(true)

    vi.mocked(listUsageRuns).mockResolvedValueOnce({
      items: [{ ...run, id: 'run-2' }],
    })
    await wrapper.get('button[data-action="load-more"]').trigger('click')
    await flushPromises()
    expect(listUsageRuns).toHaveBeenLastCalledWith(
      expect.objectContaining({ cursor: 'equal-time-cursor', limit: 25 }),
      expect.any(AbortSignal),
    )
    expect(wrapper.text()).toContain('run-1')
    expect(wrapper.text()).toContain('run-2')
  })

  it('shows empty and retryable error states with a request ID and restores focus', async () => {
    vi.mocked(readUsageSummary).mockRejectedValueOnce(
      new APIError(503, 'unavailable', '统计服务暂不可用', 'req-usage-1'),
    )
    vi.mocked(listUsageRuns).mockRejectedValueOnce(
      new APIError(503, 'unavailable', '统计服务暂不可用', 'req-usage-1'),
    )
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('统计服务暂不可用')
    expect(wrapper.get('[role="alert"]').text()).toContain('req-usage-1')

    vi.mocked(readUsageSummary).mockResolvedValueOnce({ ...summary, requests: 0 })
    vi.mocked(listUsageRuns).mockResolvedValueOnce({ items: [] })
    await wrapper.get('button[data-action="retry"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('当前筛选条件下暂无运行记录')
    expect(document.activeElement).toBe(wrapper.get('#usage-results-title').element)
  })

  it('reports filter metadata loading errors without issuing partial usage requests', async () => {
    vi.mocked(listProviders).mockRejectedValueOnce(
      new APIError(503, 'unavailable', '模型筛选加载失败', 'req-options-1'),
    )
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('模型筛选加载失败')
    expect(wrapper.get('[role="alert"]').text()).toContain('req-options-1')
    expect(readUsageSummary).not.toHaveBeenCalled()
    expect(listUsageRuns).not.toHaveBeenCalled()
  })

  it('keeps loaded rows visible and retries an append failure with the same cursor', async () => {
    const wrapper = mountView()
    await flushPromises()
    vi.mocked(listUsageRuns).mockRejectedValueOnce(
      new APIError(503, 'unavailable', '下一页加载失败', 'req-page-1'),
    )
    await wrapper.get('button[data-action="load-more"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('run-1')
    expect(wrapper.get('[role="alert"]').text()).toContain('req-page-1')

    vi.mocked(listUsageRuns).mockResolvedValueOnce({ items: [{ ...run, id: 'run-2' }] })
    await wrapper.get('button[data-action="retry-more"]').trigger('click')
    await flushPromises()
    expect(listUsageRuns).toHaveBeenLastCalledWith(
      expect.objectContaining({ cursor: 'equal-time-cursor' }),
      expect.any(AbortSignal),
    )
    expect(wrapper.text()).toContain('run-1')
    expect(wrapper.text()).toContain('run-2')
  })
})

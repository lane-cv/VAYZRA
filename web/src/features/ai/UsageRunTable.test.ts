import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import UsageRunTable from './UsageRunTable.vue'
import type { UsageRun } from './adminApi'

const at = '2026-07-26T08:00:00Z'
const rows: UsageRun[] = [
  {
    id: 'run-success',
    studentId: 'student-1',
    studentUsername: 'lin01',
    studentDisplayName: '林同学',
    modelId: 'model-1',
    modelLabel: 'math-text',
    status: 'succeeded',
    inputTokens: 13,
    outputTokens: 21,
    usageSource: 'upstream',
    costMicroUSD: '9007199254740993',
    firstByteMs: 120,
    totalMs: 900,
    createdAt: at,
  },
  {
    id: 'run-failed',
    studentId: 'student-2',
    studentUsername: 'zhou02',
    studentDisplayName: '周同学',
    modelId: 'model-2',
    modelLabel: 'physics-vision',
    status: 'failed',
    inputTokens: 8,
    outputTokens: 0,
    usageSource: 'estimated',
    costMicroUSD: '17',
    errorCategory: 'quota_estimation_anomaly',
    createdAt: at,
  },
  {
    id: 'run-cancelled',
    studentId: 'student-3',
    studentUsername: 'wu03',
    studentDisplayName: '吴同学',
    modelId: 'model-3',
    modelLabel: 'fallback',
    status: 'cancelled',
    inputTokens: 0,
    outputTokens: 0,
    usageSource: 'unknown',
    costMicroUSD: '0',
    createdAt: at,
  },
]

describe('UsageRunTable', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('renders a desktop table at 900px without a duplicate accessible mobile copy', () => {
    const matchMedia = vi.fn(() => ({
      matches: false,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }))
    vi.stubGlobal('matchMedia', matchMedia)
    const wrapper = mount(UsageRunTable, { props: { items: rows } })

    expect(matchMedia).toHaveBeenCalledWith('(max-width: 899px)')
    expect(wrapper.get('table').attributes('aria-label')).toBe('AI 用量运行记录')
    expect(wrapper.findAll('tbody tr')).toHaveLength(3)
    expect(wrapper.find('.mobile-cards').exists()).toBe(false)
    expect(wrapper.text()).toContain('成功')
    expect(wrapper.text()).toContain('失败')
    expect(wrapper.text()).toContain('已取消')
    expect(wrapper.text()).toContain('供应商')
    expect(wrapper.text()).toContain('估算')
    expect(wrapper.text()).toContain('未知')
    expect(wrapper.text()).toContain('额度估算异常')
    expect(wrapper.text()).toContain('$9,007,199,254.740993')
    expect(wrapper.find('a[href*="student-1"]').exists()).toBe(false)
    expect(wrapper.findAll('a')).toHaveLength(0)
    expect(wrapper.html()).not.toContain('prompt')
    expect(wrapper.html()).not.toContain('messageBody')

  })

  it('renders labelled cards below 900px with the same rows and no duplicate table', () => {
    vi.stubGlobal('matchMedia', vi.fn(() => ({
      matches: true,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    })))
    const wrapper = mount(UsageRunTable, { props: { items: rows } })
    expect(wrapper.find('table').exists()).toBe(false)
    expect(wrapper.findAll('.mobile-run-card')).toHaveLength(3)
    expect(wrapper.findAll('.mobile-run-card code').map((node) => node.text())).toEqual([
      'run-success',
      'run-failed',
      'run-cancelled',
    ])
    const card = wrapper.findAll('.mobile-run-card')[1]
    expect(card.text()).toContain('学生：周同学（zhou02）')
    expect(card.text()).toContain('模型：physics-vision')
    expect(card.text()).toContain('运行 ID：run-failed')
  })

  it('formats integer micro-USD without floating point precision loss', async () => {
    const module = await import('./UsageRunTable.vue')
    expect(module.formatMicroUSD('9007199254740993')).toBe('$9,007,199,254.740993')
    expect(module.formatMicroUSD('19')).toBe('$0.000019')
    expect(module.formatMicroUSD('1000000')).toBe('$1.000000')
    expect(module.formatMicroUSD('not-an-integer')).toBe('未知')
  })
})

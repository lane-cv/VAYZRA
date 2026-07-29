import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { APIError } from '../../api/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AdminHomeView from './AdminHomeView.vue'
import type { OperationsDashboard } from '../operations/types'

const api = vi.hoisted(() => ({ readDashboard: vi.fn() }))
vi.mock('../operations/api', async (original) => ({
  ...(await original<typeof import('../operations/api')>()),
  readDashboard: api.readDashboard,
}))

const observedAt = '2026-07-30T08:00:00Z'
const dashboard: OperationsDashboard = {
  observedAt,
  students: { state: 'healthy', observedAt, active: 28, disabled: 2 },
  questions: { state: 'healthy', observedAt, waiting: 3, oldestWaitSeconds: 7200 },
  ai: {
    state: 'healthy',
    observedAt,
    requests: 40,
    successRatePercent: 97.5,
    firstByteLatencyMilliseconds: 450,
    totalLatencyMilliseconds: 1800,
    dailyCostMicroUSD: 123456,
  },
  storage: {
    state: 'healthy',
    observedAt,
    usedBytes: 512 * 1024 * 1024,
    capacityBytes: 1024 * 1024 * 1024,
    warningPercent: 75,
  },
  services: [
    { service: 'app', state: 'healthy', observedAt, latencyMilliseconds: 12 },
    { service: 'caddy', state: 'healthy', observedAt, latencyMilliseconds: 8 },
    { service: 'postgres', state: 'healthy', observedAt, latencyMilliseconds: 4 },
    { service: 'redis', state: 'healthy', observedAt, latencyMilliseconds: 2 },
    { service: 'object_store', state: 'healthy', observedAt, latencyMilliseconds: 10 },
    { service: 'worker', state: 'healthy', observedAt, latencyMilliseconds: 15 },
  ],
  queues: [
    { queue: 'processing', state: 'healthy', observedAt, queued: 1, streaming: 0, failed: 0, expired: 0 },
    { queue: 'ai', state: 'healthy', observedAt, queued: 0, streaming: 2, failed: 1, expired: 0 },
    { queue: 'outbox', state: 'empty', observedAt, queued: 0, streaming: 0, failed: 0, expired: 0 },
  ],
  backup: {
    state: 'healthy',
    observedAt,
    local: { state: 'succeeded', completedAt: '2026-07-30T02:00:00Z' },
    remote: { state: 'succeeded', completedAt: '2026-07-30T02:05:00Z' },
    restore: { state: 'succeeded', completedAt: '2026-07-29T06:00:00Z', rtoSeconds: 70 },
  },
  alerts: { state: 'degraded', observedAt, openWarning: 2, openCritical: 1 },
  recentAuditState: 'healthy',
  recentAudit: [
    { category: 'backup', outcome: 'succeeded', occurredAt: '2026-07-30T07:59:00Z' },
    { category: 'authorization', outcome: 'denied', occurredAt: '2026-07-30T07:58:00Z' },
  ],
}

const mounted: VueWrapper[] = []
function mountView() {
  const wrapper = mount(AdminHomeView, {
    attachTo: document.body,
    global: {
      stubs: {
        RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' },
      },
    },
  })
  mounted.push(wrapper)
  return wrapper
}

describe('AdminHomeView operations dashboard', () => {
  beforeEach(() => {
    api.readDashboard.mockReset()
    api.readDashboard.mockResolvedValue(structuredClone(dashboard))
  })

  afterEach(() => {
    for (const wrapper of mounted.splice(0)) wrapper.unmount()
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('renders healthy summaries, text statuses, observation times, recovery evidence, and safe audit activity', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[data-testid="dashboard-observed-at"]').attributes('datetime')).toBe(observedAt)
    expect(wrapper.get('[data-testid="student-summary"]').text()).toContain('28')
    expect(wrapper.get('[data-testid="service-app"]').text()).toContain('正常')
    expect(wrapper.get('[data-testid="backup-summary"]').text()).toContain('本地恢复点')
    expect(wrapper.get('[data-testid="backup-summary"]').text()).toContain('远端恢复点')
    expect(wrapper.get('[data-testid="backup-summary"]').text()).toContain('70 秒')
    expect(wrapper.get('[data-testid="alert-summary"]').text()).toMatch(/严重[\s\S]*1[\s\S]*警告[\s\S]*2/)
    expect(wrapper.findAll('[data-testid="recent-audit"]')).toHaveLength(2)
    expect(wrapper.text()).toContain('备份 · 成功')
    expect(wrapper.text()).toContain('授权 · 已拒绝')
    expect(wrapper.text()).not.toMatch(/actor|target|metadata|request|password|webhook|authorization:/i)
    expect(wrapper.get('a[href="/admin/alerts"]').text()).toContain('告警')
    expect(wrapper.get('a[href="/admin/backups"]').text()).toContain('备份')
    expect(wrapper.get('a[href="/admin/audit"]').text()).toContain('审计')
  })

  it('keeps alerts, backup, service health, and queues before summaries in the mobile DOM order', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.findAll('[data-mobile-section]').map((section) => section.attributes('data-mobile-section'))).toEqual([
      'alerts',
      'backup',
      'services',
      'queues',
      'summaries',
    ])
  })

  it('labels degraded, stale, unavailable, timeout, and empty data explicitly with observation time', async () => {
    api.readDashboard.mockResolvedValueOnce({
      ...structuredClone(dashboard),
      students: { ...dashboard.students, state: 'stale' },
      questions: { ...dashboard.questions, state: 'unavailable', observedAt: undefined },
      ai: { ...dashboard.ai, state: 'timeout', observedAt: undefined },
      storage: { ...dashboard.storage, state: 'degraded' },
      services: [{ service: 'app', state: 'empty', latencyMilliseconds: 0 }],
    })
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[data-testid="student-summary"]').text()).toContain('数据陈旧')
    expect(wrapper.get('[data-testid="student-summary"]').text()).toContain('观测于')
    expect(wrapper.get('[data-testid="question-summary"]').text()).toContain('未知（不可用）')
    expect(wrapper.get('[data-testid="ai-summary"]').text()).toContain('未知（超时）')
    expect(wrapper.get('[data-testid="storage-summary"]').text()).toContain('需关注')
    expect(wrapper.get('[data-testid="service-app"]').text()).toContain('暂无数据')
  })

  it('never presents zero values as observed health when a metric is unknown or empty', async () => {
    api.readDashboard.mockResolvedValueOnce({
      ...structuredClone(dashboard),
      students: { state: 'unavailable', active: 0, disabled: 0 },
      questions: { state: 'empty', waiting: 0, oldestWaitSeconds: 0 },
      ai: {
        ...dashboard.ai,
        state: 'timeout',
        observedAt: undefined,
        requests: 0,
        successRatePercent: 0,
      },
      storage: {
        state: 'unavailable',
        usedBytes: 0,
        capacityBytes: 0,
        warningPercent: 0,
      },
      services: [
        { service: 'app', state: 'unavailable', latencyMilliseconds: 0 },
        { service: 'redis', state: 'timeout', latencyMilliseconds: 0 },
        { service: 'worker', state: 'empty', observedAt, latencyMilliseconds: 0 },
      ],
      queues: [
        { queue: 'processing', state: 'empty', observedAt, queued: 0, streaming: 0, failed: 0, expired: 0 },
      ],
      alerts: { state: 'unavailable', openWarning: 0, openCritical: 0 },
    })
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[data-testid="student-summary"] strong').text()).toBe('—')
    expect(wrapper.get('[data-testid="question-summary"] strong').text()).toBe('—')
    expect(wrapper.get('[data-testid="ai-summary"] strong').text()).toBe('—')
    expect(wrapper.get('[data-testid="storage-summary"] strong').text()).toBe('—')
    expect(wrapper.text()).not.toMatch(/NaN|Infinity/)
    expect(wrapper.get('[data-testid="storage-summary"]').text()).not.toContain('0%')
    expect(wrapper.get('[data-testid="alert-summary"]').findAll('.alert-counts strong').map((item) => item.text())).toEqual(['—', '—'])
    for (const service of ['app', 'redis', 'worker']) {
      expect(wrapper.get(`[data-testid="service-${service}"]`).text()).not.toContain('0 ms')
      expect(wrapper.get(`[data-testid="service-${service}"]`).text()).toContain('延迟 —')
    }
    expect(wrapper.get('.queues').text()).toContain('运行指标 —')
  })

  it('surfaces backup and recent-audit dependency state and observation time instead of empty evidence', async () => {
    api.readDashboard.mockResolvedValueOnce({
      ...structuredClone(dashboard),
      storage: { ...dashboard.storage, state: 'stale' },
      queues: [{ ...dashboard.queues[0], state: 'stale' }],
      backup: {
        state: 'unavailable',
        local: { state: 'empty' },
        remote: { state: 'empty' },
        restore: { state: 'empty', rtoSeconds: 0 },
      },
      recentAuditState: 'timeout',
      recentAudit: [],
    })
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[data-testid="backup-state"]').text()).toContain('未知（不可用）')
    expect(wrapper.get('[data-testid="backup-summary"]').text()).not.toContain('暂无记录')
    expect(wrapper.get('[data-testid="backup-summary"]').findAll('.recovery-points strong').map((item) => item.text())).toEqual(['—', '—', '—'])
    expect(wrapper.get('[data-testid="recent-audit-state"]').text()).toContain('未知（超时）')
    expect(wrapper.get('.audit').text()).not.toContain('暂无安全活动摘要')
    expect(wrapper.get('[data-testid="storage-summary"] time').attributes('datetime')).toBe(observedAt)
    expect(wrapper.get('.queues time').attributes('datetime')).toBe(observedAt)
  })

  it('shows failed restore completion without inventing an RTO value', async () => {
    const failedAt = '2026-07-30T06:30:00Z'
    api.readDashboard.mockResolvedValueOnce({
      ...structuredClone(dashboard),
      backup: {
        ...dashboard.backup,
        state: 'stale',
        restore: { state: 'failed', completedAt: failedAt, rtoSeconds: 0 },
      },
    })
    const wrapper = mountView()
    await flushPromises()

    const restore = wrapper.get('[data-testid="restore-evidence"]')
    expect(restore.text()).toContain('失败')
    expect(restore.get('time').attributes('datetime')).toBe(failedAt)
    expect(restore.text()).not.toContain('RTO 0 秒')
    expect(wrapper.get('[data-testid="backup-state"] time').attributes('datetime')).toBe(observedAt)
  })

  it('polls every 60 seconds only while visible and refreshes immediately on resume', async () => {
    vi.useFakeTimers()
    let visibility: DocumentVisibilityState = 'visible'
    vi.spyOn(document, 'visibilityState', 'get').mockImplementation(() => visibility)
    mountView()
    await flushPromises()
    expect(api.readDashboard).toHaveBeenCalledTimes(1)

    await vi.advanceTimersByTimeAsync(60_000)
    await flushPromises()
    expect(api.readDashboard).toHaveBeenCalledTimes(2)

    visibility = 'hidden'
    document.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(120_000)
    await flushPromises()
    expect(api.readDashboard).toHaveBeenCalledTimes(2)

    visibility = 'visible'
    document.dispatchEvent(new Event('visibilitychange'))
    await flushPromises()
    expect(api.readDashboard).toHaveBeenCalledTimes(3)
  })

  it('shows a request-aware error and restores keyboard focus after retry', async () => {
    api.readDashboard
      .mockRejectedValueOnce(new APIError(503, 'internal_error', '监控暂不可用', 'req-dashboard'))
      .mockResolvedValueOnce(structuredClone(dashboard))
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[role="alert"]').text()).toContain('req-dashboard')
    await wrapper.get('[data-testid="dashboard-retry"]').trigger('click')
    await flushPromises()
    expect(document.activeElement).toBe(wrapper.get('[data-testid="dashboard-title"]').element)
  })

  it('aborts an in-flight dashboard request when unmounted', async () => {
    let signal: AbortSignal | undefined
    api.readDashboard.mockImplementationOnce((input?: AbortSignal) => {
      signal = input
      return new Promise(() => {})
    })
    const wrapper = mountView()
    await flushPromises()
    expect(signal?.aborted).toBe(false)

    wrapper.unmount()
    expect(signal?.aborted).toBe(true)
  })
})

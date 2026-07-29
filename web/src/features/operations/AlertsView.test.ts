import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { APIError } from '../../api/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AlertsView from './AlertsView.vue'
import type { AlertPage, OperationalAlert } from './types'

const api = vi.hoisted(() => ({
  listAlerts: vi.fn(),
  acknowledgeAlert: vi.fn(),
}))
vi.mock('./api', async (original) => ({
  ...(await original<typeof import('./api')>()),
  listAlerts: api.listAlerts,
  acknowledgeAlert: api.acknowledgeAlert,
}))

const firstObservedAt = '2026-07-30T07:00:00Z'
const lastObservedAt = '2026-07-30T08:00:00Z'
const critical: OperationalAlert = {
  id: '10000000-0000-4000-8000-000000000001',
  dedupeKey: 'backup_local_age',
  category: 'backup',
  severity: 'critical',
  state: 'open',
  firstObservedAt,
  lastObservedAt,
  currentValue: 108001,
  thresholdValue: 108000,
  summary: 'Verified local backup is overdue',
}
const warning: OperationalAlert = {
  ...critical,
  id: '10000000-0000-4000-8000-000000000002',
  dedupeKey: 'filesystem_root_usage',
  category: 'storage',
  severity: 'warning',
  currentValue: 80,
  thresholdValue: 75,
  summary: 'Root filesystem usage is high',
}
const resolved: OperationalAlert = {
  ...warning,
  state: 'resolved',
  resolvedAt: '2026-07-30T08:05:00Z',
}

const mounted: VueWrapper[] = []
function mountView() {
  const wrapper = mount(AlertsView, {
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

describe('AlertsView', () => {
  beforeEach(() => {
    document.cookie = 'hl_csrf=csrf; path=/'
    api.listAlerts.mockReset()
    api.acknowledgeAlert.mockReset()
    api.listAlerts.mockResolvedValue({ items: [critical, warning], next: null } satisfies AlertPage)
  })

  afterEach(() => {
    for (const wrapper of mounted.splice(0)) wrapper.unmount()
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('renders text severity/state and a bounded timeline without webhook material', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.findAll('[data-testid="alert-card"]').map((item) => item.attributes('data-id'))).toEqual([
      critical.id,
      warning.id,
    ])
    expect(wrapper.get(`[data-id="${critical.id}"]`).text()).toContain('严重')
    expect(wrapper.get(`[data-id="${critical.id}"]`).text()).toContain('未处理')
    expect(wrapper.get(`[data-id="${critical.id}"]`).text()).toContain('首次发现')
    expect(wrapper.get(`[data-id="${critical.id}"]`).text()).toContain('最近观测')
    expect(wrapper.text()).not.toMatch(/webhook|authorization|delivery body|https?:\/\//i)
  })

  it('applies exact state, severity, and category filters and aborts the superseded request', async () => {
    let firstSignal: AbortSignal | undefined
    api.listAlerts.mockReset()
    api.listAlerts
      .mockImplementationOnce((_filters, signal?: AbortSignal) => {
        firstSignal = signal
        return new Promise(() => {})
      })
      .mockResolvedValueOnce({ items: [], next: null })
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-testid="alert-state-filter"]').setValue('open')
    await wrapper.get('[data-testid="alert-severity-filter"]').setValue('critical')
    await wrapper.get('[data-testid="alert-category-filter"]').setValue('backup')
    await wrapper.get('[data-testid="alert-filters"]').trigger('submit')
    await flushPromises()

    expect(firstSignal?.aborted).toBe(true)
    expect(api.listAlerts).toHaveBeenLastCalledWith({
      state: 'open',
      severity: 'critical',
      category: 'backup',
      limit: 50,
    }, expect.any(AbortSignal))
  })

  it('appends stable keyset pages without duplicating an equal-time alert', async () => {
    const third = { ...warning, id: '10000000-0000-4000-8000-000000000003' }
    api.listAlerts
      .mockResolvedValueOnce({ items: [critical, warning], next: 'opaque-cursor' })
      .mockResolvedValueOnce({ items: [warning, third], next: null })
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-testid="alerts-load-more"]').trigger('click')
    await flushPromises()

    expect(api.listAlerts).toHaveBeenLastCalledWith(
      { before: 'opaque-cursor', limit: 50 },
      expect.any(AbortSignal),
    )
    expect(wrapper.findAll('[data-testid="alert-card"]').map((item) => item.attributes('data-id'))).toEqual([
      critical.id,
      warning.id,
      third.id,
    ])
  })

  it('requires confirmation before acknowledgement and updates the alert in place', async () => {
    const acknowledged: OperationalAlert = {
      ...critical,
      state: 'acknowledged',
      acknowledgedBy: '20000000-0000-4000-8000-000000000001',
      acknowledgedAt: '2026-07-30T08:01:00Z',
    }
    api.acknowledgeAlert.mockResolvedValueOnce(acknowledged)
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get(`[data-acknowledge="${critical.id}"]`).trigger('click')
    expect(api.acknowledgeAlert).not.toHaveBeenCalled()
    expect(wrapper.get('[data-testid="acknowledge-dialog"]').text()).toContain(critical.summary)
    await wrapper.get('[data-testid="confirm-acknowledge"]').trigger('click')
    await flushPromises()

    expect(api.acknowledgeAlert).toHaveBeenCalledWith(critical.id)
    expect(wrapper.get(`[data-id="${critical.id}"]`).text()).toContain('已确认')
    expect(document.activeElement).toBe(wrapper.get(`[data-id="${critical.id}"]`).element)
  })

  it('shows a live resolution without removing other loaded alerts', async () => {
    vi.useFakeTimers()
    api.listAlerts
      .mockResolvedValueOnce({ items: [warning, critical], next: null })
      .mockResolvedValueOnce({ items: [resolved], next: null })
    const wrapper = mountView()
    await flushPromises()

    await vi.advanceTimersByTimeAsync(60_000)
    await flushPromises()

    expect(wrapper.get(`[data-id="${warning.id}"]`).text()).toContain('已解决')
    expect(wrapper.find(`[data-id="${critical.id}"]`).exists()).toBe(true)
  })

  it('focuses the result heading after retry and aborts remaining requests on unmount', async () => {
    api.listAlerts
      .mockRejectedValueOnce(new APIError(503, 'internal_error', '告警加载失败', 'req-alerts'))
      .mockResolvedValueOnce({ items: [critical], next: null })
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[role="alert"]').text()).toContain('req-alerts')
    await wrapper.get('[data-testid="alerts-retry"]').trigger('click')
    await flushPromises()
    expect(document.activeElement).toBe(wrapper.get('[data-testid="alerts-results-title"]').element)

    let signal: AbortSignal | undefined
    api.listAlerts.mockImplementationOnce((_filters, input?: AbortSignal) => {
      signal = input
      return new Promise(() => {})
    })
    await wrapper.get('[data-testid="alert-filters"]').trigger('submit')
    await flushPromises()
    wrapper.unmount()
    expect(signal?.aborted).toBe(true)
  })
})

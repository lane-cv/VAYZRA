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
function deferred<T>() {
  let reject!: (reason?: unknown) => void
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done, fail) => {
    resolve = done
    reject = fail
  })
  return { promise, reject, resolve }
}

function orderedAlert(position: number): OperationalAlert {
  return {
    ...warning,
    id: `30000000-0000-4000-8000-${String(position).padStart(12, '0')}`,
    dedupeKey: `ordered_alert_${position}`,
    lastObservedAt: new Date(Date.parse(lastObservedAt) - position * 60_000).toISOString(),
    summary: `Ordered alert ${position}`,
  }
}

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

  it('keeps the tail cursor across a head poll so three keyset pages remain reachable', async () => {
    vi.useFakeTimers()
    const third = {
      ...warning,
      id: '10000000-0000-4000-8000-000000000003',
      summary: 'Third page alert',
    }
    api.listAlerts
      .mockResolvedValueOnce({ items: [critical], next: 'page-2' })
      .mockResolvedValueOnce({ items: [warning], next: 'page-3' })
      .mockResolvedValueOnce({ items: [critical], next: 'page-2' })
      .mockResolvedValueOnce({ items: [warning], next: 'page-3' })
      .mockResolvedValueOnce({ items: [third], next: null })
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="alerts-load-more"]').trigger('click')
    await flushPromises()

    await vi.advanceTimersByTimeAsync(60_000)
    await flushPromises()
    await wrapper.get('[data-testid="alerts-load-more"]').trigger('click')
    await flushPromises()

    expect(api.listAlerts.mock.calls.map(([filters]) => filters)).toEqual([
      { limit: 50 },
      { before: 'page-2', limit: 50 },
      { limit: 50 },
      { before: 'page-2', limit: 50 },
      { before: 'page-3', limit: 50 },
    ])
    expect(wrapper.findAll('[data-testid="alert-card"]').map((item) => item.attributes('data-id'))).toEqual([
      critical.id,
      warning.id,
      third.id,
    ])
  })

  it('does not start load-more while a head refresh is in flight', async () => {
    vi.useFakeTimers()
    const refreshing = deferred<AlertPage>()
    const third = {
      ...warning,
      id: '10000000-0000-4000-8000-000000000003',
      summary: 'Third page alert',
    }
    api.listAlerts
      .mockResolvedValueOnce({ items: [critical], next: 'page-2' })
      .mockResolvedValueOnce({ items: [warning], next: 'page-3' })
      .mockImplementationOnce(() => refreshing.promise)
      .mockResolvedValueOnce({ items: [warning], next: 'page-3' })
      .mockResolvedValueOnce({ items: [third], next: null })
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="alerts-load-more"]').trigger('click')
    await flushPromises()

    await vi.advanceTimersByTimeAsync(60_000)
    await flushPromises()
    await wrapper.get('[data-testid="alerts-load-more"]').trigger('click')
    await flushPromises()
    expect(api.listAlerts).toHaveBeenCalledTimes(3)

    refreshing.resolve({ items: [critical], next: 'page-2' })
    await flushPromises()
    await wrapper.get('[data-testid="alerts-load-more"]').trigger('click')
    await flushPromises()
    expect(api.listAlerts).toHaveBeenCalledTimes(5)
    expect(api.listAlerts.mock.calls[4]?.[0]).toEqual({ before: 'page-3', limit: 50 })
  })

  it('atomically rebuilds every loaded page when a new alert shifts a full head page', async () => {
    vi.useFakeTimers()
    const originalHead = Array.from({ length: 50 }, (_, index) => orderedAlert(index + 1))
    const originalTail = [orderedAlert(51)]
    const refreshedHead = Array.from({ length: 50 }, (_, index) => orderedAlert(index))
    const refreshedTail = [orderedAlert(50), orderedAlert(51)]
    api.listAlerts
      .mockResolvedValueOnce({ items: originalHead, next: 'initial-page-2' })
      .mockResolvedValueOnce({ items: originalHead, next: 'initial-page-2' })
      .mockResolvedValueOnce({ items: originalTail, next: null })
      .mockResolvedValueOnce({ items: refreshedHead, next: 'refreshed-page-2' })
      .mockResolvedValueOnce({ items: refreshedTail, next: null })
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="alert-state-filter"]').setValue('open')
    await wrapper.get('[data-testid="alert-filters"]').trigger('submit')
    await flushPromises()
    await wrapper.get('[data-testid="alerts-load-more"]').trigger('click')
    await flushPromises()

    await vi.advanceTimersByTimeAsync(60_000)
    await flushPromises()

    expect(api.listAlerts.mock.calls.slice(3).map(([request]) => request)).toEqual([
      { state: 'open', limit: 50 },
      { state: 'open', before: 'refreshed-page-2', limit: 50 },
    ])
    expect(wrapper.findAll('[data-testid="alert-card"]').map((item) => item.attributes('data-id'))).toEqual(
      Array.from({ length: 52 }, (_, index) => orderedAlert(index).id),
    )
  })

  it('uses server order when an alert from a loaded tail page moves into the refreshed head', async () => {
    vi.useFakeTimers()
    const tailWarning = { ...warning, lastObservedAt: '2026-07-30T07:30:00Z' }
    const promotedWarning = { ...warning, lastObservedAt: '2026-07-30T09:00:00Z' }
    const third = {
      ...warning,
      id: '10000000-0000-4000-8000-000000000003',
      lastObservedAt: '2026-07-30T07:00:00Z',
    }
    api.listAlerts
      .mockResolvedValueOnce({ items: [critical], next: 'initial-tail' })
      .mockResolvedValueOnce({ items: [tailWarning, third], next: null })
      .mockResolvedValueOnce({ items: [promotedWarning, critical], next: 'refreshed-tail' })
      .mockResolvedValueOnce({ items: [third], next: null })
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="alerts-load-more"]').trigger('click')
    await flushPromises()

    await vi.advanceTimersByTimeAsync(60_000)
    await flushPromises()

    expect(wrapper.findAll('[data-testid="alert-card"]').map((item) => item.attributes('data-id'))).toEqual([
      warning.id,
      critical.id,
      third.id,
    ])
    expect(wrapper.find(`[data-id="${warning.id}"] time[datetime="${promotedWarning.lastObservedAt}"]`).exists()).toBe(true)
  })

  it('removes a resolved alert from a loaded tail page under an open filter rebuild', async () => {
    vi.useFakeTimers()
    const head = orderedAlert(1)
    const tail = orderedAlert(2)
    api.listAlerts
      .mockResolvedValueOnce({ items: [head], next: 'initial-tail' })
      .mockResolvedValueOnce({ items: [head], next: 'initial-tail' })
      .mockResolvedValueOnce({ items: [tail], next: null })
      .mockResolvedValueOnce({ items: [head], next: 'refreshed-tail' })
      .mockResolvedValueOnce({ items: [], next: null })
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="alert-state-filter"]').setValue('open')
    await wrapper.get('[data-testid="alert-filters"]').trigger('submit')
    await flushPromises()
    await wrapper.get('[data-testid="alerts-load-more"]').trigger('click')
    await flushPromises()

    await vi.advanceTimersByTimeAsync(60_000)
    await flushPromises()

    expect(wrapper.find(`[data-id="${tail.id}"]`).exists()).toBe(false)
    expect(wrapper.findAll('[data-testid="alert-card"]').map((item) => item.attributes('data-id'))).toEqual([
      head.id,
    ])
  })

  it('keeps the previous complete list when a later page of a live rebuild fails', async () => {
    vi.useFakeTimers()
    const head = orderedAlert(1)
    const tail = orderedAlert(2)
    api.listAlerts
      .mockResolvedValueOnce({ items: [head], next: 'initial-tail' })
      .mockResolvedValueOnce({ items: [tail], next: null })
      .mockResolvedValueOnce({ items: [orderedAlert(0)], next: 'refreshed-tail' })
      .mockRejectedValueOnce(new APIError(503, 'internal_error', '刷新失败', 'req-refresh'))
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="alerts-load-more"]').trigger('click')
    await flushPromises()

    await vi.advanceTimersByTimeAsync(60_000)
    await flushPromises()

    expect(wrapper.findAll('[data-testid="alert-card"]').map((item) => item.attributes('data-id'))).toEqual([
      head.id,
      tail.id,
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

  it('gives the acknowledgement dialog an accessible name, focuses it, and restores the trigger on cancel', async () => {
    const wrapper = mountView()
    await flushPromises()
    const trigger = wrapper.get(`[data-acknowledge="${critical.id}"]`)

    await trigger.trigger('click')
    await flushPromises()
    const dialog = wrapper.get('[data-testid="acknowledge-dialog"]')
    expect(dialog.attributes('aria-labelledby')).toBe('acknowledge-dialog-title')
    expect(wrapper.get('#acknowledge-dialog-title').text()).toContain('确认这条告警')
    expect(document.activeElement).toBe(wrapper.get('[data-testid="cancel-acknowledge"]').element)

    await wrapper.get('[data-testid="cancel-acknowledge"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="acknowledge-dialog"]').exists()).toBe(false)
    expect(document.activeElement).toBe(trigger.element)
  })

  it('keeps the dialog open and focuses mutation feedback after an acknowledgement error', async () => {
    api.acknowledgeAlert.mockRejectedValueOnce(
      new APIError(500, 'internal_error', '确认失败', 'req-acknowledge'),
    )
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get(`[data-acknowledge="${critical.id}"]`).trigger('click')
    await wrapper.get('[data-testid="confirm-acknowledge"]').trigger('click')
    await flushPromises()

    const feedback = wrapper.get('[data-testid="acknowledge-error"]')
    expect(feedback.text()).toContain('req-acknowledge')
    expect(document.activeElement).toBe(feedback.element)
    expect(wrapper.get('[data-testid="acknowledge-dialog"]').attributes('open')).toBeDefined()
  })

  it('removes a locally acknowledged alert from the active open filter', async () => {
    const acknowledged: OperationalAlert = {
      ...critical,
      state: 'acknowledged',
      acknowledgedBy: '20000000-0000-4000-8000-000000000001',
      acknowledgedAt: '2026-07-30T08:01:00Z',
    }
    api.listAlerts.mockResolvedValue({ items: [critical], next: null })
    api.acknowledgeAlert.mockResolvedValueOnce(acknowledged)
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="alert-state-filter"]').setValue('open')
    await wrapper.get('[data-testid="alert-filters"]').trigger('submit')
    await flushPromises()

    await wrapper.get(`[data-acknowledge="${critical.id}"]`).trigger('click')
    await wrapper.get('[data-testid="confirm-acknowledge"]').trigger('click')
    await flushPromises()

    expect(wrapper.find(`[data-id="${critical.id}"]`).exists()).toBe(false)
    expect(document.activeElement).toBe(wrapper.get('[data-testid="alerts-results-title"]').element)
  })

  it('removes live transitions that no longer match state, severity, or category filters', async () => {
    vi.useFakeTimers()
    const transitioned: OperationalAlert = {
      ...warning,
      state: 'resolved',
      severity: 'critical',
      category: 'backup',
      resolvedAt: '2026-07-30T08:05:00Z',
    }
    api.listAlerts
      .mockResolvedValueOnce({ items: [warning], next: null })
      .mockResolvedValueOnce({ items: [warning], next: null })
      .mockResolvedValueOnce({ items: [transitioned], next: null })
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="alert-state-filter"]').setValue('open')
    await wrapper.get('[data-testid="alert-severity-filter"]').setValue('warning')
    await wrapper.get('[data-testid="alert-category-filter"]').setValue('storage')
    await wrapper.get('[data-testid="alert-filters"]').trigger('submit')
    await flushPromises()

    await vi.advanceTimersByTimeAsync(60_000)
    await flushPromises()

    expect(wrapper.find(`[data-id="${warning.id}"]`).exists()).toBe(false)
  })

  it('removes an old head alert that disappears from a filtered live response', async () => {
    vi.useFakeTimers()
    api.listAlerts
      .mockResolvedValueOnce({ items: [critical], next: null })
      .mockResolvedValueOnce({ items: [critical], next: null })
      .mockResolvedValueOnce({ items: [], next: null })
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="alert-state-filter"]').setValue('open')
    await wrapper.get('[data-testid="alert-filters"]').trigger('submit')
    await flushPromises()

    await vi.advanceTimersByTimeAsync(60_000)
    await flushPromises()

    expect(wrapper.find(`[data-id="${critical.id}"]`).exists()).toBe(false)
  })

  it.each([
    [409, 'alert_already_resolved'],
    [404, 'alert_not_found'],
  ])('refreshes a stale alert after acknowledge HTTP %i', async (status, code) => {
    api.listAlerts
      .mockResolvedValueOnce({ items: [critical], next: null })
      .mockResolvedValueOnce({ items: [], next: null })
    api.acknowledgeAlert.mockRejectedValueOnce(new APIError(status, code, '状态已变化', 'req-mutation'))
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get(`[data-acknowledge="${critical.id}"]`).trigger('click')
    await wrapper.get('[data-testid="confirm-acknowledge"]').trigger('click')
    await flushPromises()

    expect(api.listAlerts).toHaveBeenCalledTimes(2)
    expect(wrapper.find(`[data-id="${critical.id}"]`).exists()).toBe(false)
  })

  it('does not let an older in-flight poll roll an acknowledged alert back to open', async () => {
    vi.useFakeTimers()
    const polling = deferred<AlertPage>()
    const acknowledged: OperationalAlert = {
      ...critical,
      state: 'acknowledged',
      acknowledgedBy: '20000000-0000-4000-8000-000000000001',
      acknowledgedAt: '2026-07-30T08:01:00Z',
    }
    api.listAlerts
      .mockResolvedValueOnce({ items: [critical], next: null })
      .mockImplementationOnce(() => polling.promise)
    api.acknowledgeAlert.mockResolvedValueOnce(acknowledged)
    const wrapper = mountView()
    await flushPromises()

    await vi.advanceTimersByTimeAsync(60_000)
    await flushPromises()
    await wrapper.get(`[data-acknowledge="${critical.id}"]`).trigger('click')
    await wrapper.get('[data-testid="confirm-acknowledge"]').trigger('click')
    await flushPromises()
    polling.resolve({ items: [critical], next: null })
    await flushPromises()

    expect(wrapper.get(`[data-id="${critical.id}"]`).text()).toContain('已确认')
  })

  it('does not start or apply a visibility refresh triggered after acknowledgement begins', async () => {
    const mutation = deferred<OperationalAlert>()
    const polling = deferred<AlertPage>()
    const acknowledged: OperationalAlert = {
      ...critical,
      state: 'acknowledged',
      acknowledgedBy: '20000000-0000-4000-8000-000000000001',
      acknowledgedAt: '2026-07-30T08:01:00Z',
    }
    api.listAlerts
      .mockResolvedValueOnce({ items: [critical], next: null })
      .mockImplementationOnce(() => polling.promise)
    api.acknowledgeAlert.mockImplementationOnce(() => mutation.promise)
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get(`[data-acknowledge="${critical.id}"]`).trigger('click')
    await wrapper.get('[data-testid="confirm-acknowledge"]').trigger('click')
    document.dispatchEvent(new Event('visibilitychange'))
    await flushPromises()

    expect(api.listAlerts).toHaveBeenCalledTimes(1)
    mutation.resolve(acknowledged)
    await flushPromises()
    polling.resolve({ items: [critical], next: null })
    await flushPromises()
    expect(wrapper.get(`[data-id="${critical.id}"]`).text()).toContain('已确认')
  })

  it('invalidates a mutation-period visibility response after acknowledgement fails', async () => {
    const mutation = deferred<OperationalAlert>()
    const polling = deferred<AlertPage>()
    api.listAlerts
      .mockResolvedValueOnce({ items: [critical], next: null })
      .mockImplementationOnce(() => polling.promise)
    api.acknowledgeAlert.mockImplementationOnce(() => mutation.promise)
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get(`[data-acknowledge="${critical.id}"]`).trigger('click')
    await wrapper.get('[data-testid="confirm-acknowledge"]').trigger('click')
    document.dispatchEvent(new Event('visibilitychange'))
    await flushPromises()
    mutation.reject(new APIError(500, 'internal_error', '确认失败', 'req-acknowledge'))
    await flushPromises()
    polling.resolve({ items: [resolved], next: null })
    await flushPromises()

    expect(api.listAlerts).toHaveBeenCalledTimes(1)
    expect(wrapper.get(`[data-id="${critical.id}"]`).text()).toContain('未处理')
    expect(wrapper.get('[data-testid="acknowledge-error"]').text()).toContain('req-acknowledge')
  })

  it('shows a live resolution without removing other loaded alerts', async () => {
    vi.useFakeTimers()
    api.listAlerts
      .mockResolvedValueOnce({ items: [warning, critical], next: null })
      .mockResolvedValueOnce({ items: [resolved, critical], next: null })
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

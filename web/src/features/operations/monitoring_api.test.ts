import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  acknowledgeAlert,
  listAlerts,
  readDashboard,
} from './api'
import type { OperationalAlert, OperationsDashboard } from './types'

const observedAt = '2026-07-30T08:00:00Z'
const dashboard: OperationsDashboard = {
  observedAt,
  students: { state: 'healthy', observedAt, active: 1, disabled: 0 },
  questions: { state: 'empty', observedAt, waiting: 0, oldestWaitSeconds: 0 },
  ai: {
    state: 'healthy',
    observedAt,
    requests: 20,
    successRatePercent: 100,
    firstByteLatencyMilliseconds: 1,
    totalLatencyMilliseconds: 2,
    dailyCostMicroUSD: 3,
  },
  storage: { state: 'healthy', observedAt, usedBytes: 4, capacityBytes: 8, warningPercent: 75 },
  services: [{ service: 'app', state: 'healthy', observedAt, latencyMilliseconds: 1 }],
  queues: [{ queue: 'ai', state: 'healthy', observedAt, queued: 0, streaming: 1, failed: 0, expired: 0 }],
  backup: {
    state: 'healthy',
    observedAt,
    local: { state: 'succeeded', completedAt: observedAt },
    remote: { state: 'empty' },
    restore: { state: 'succeeded', completedAt: observedAt, rtoSeconds: 70 },
  },
  alerts: { state: 'healthy', observedAt, openWarning: 0, openCritical: 0 },
  recentAuditState: 'healthy',
  recentAudit: [{ category: 'operations', outcome: 'succeeded', occurredAt: observedAt }],
}
const alert: OperationalAlert = {
  id: '10000000-0000-4000-8000-000000000001',
  dedupeKey: 'backup_local_age',
  category: 'backup',
  severity: 'critical',
  state: 'open',
  firstObservedAt: '2026-07-30T07:00:00Z',
  lastObservedAt: observedAt,
  currentValue: 108001,
  thresholdValue: 108000,
  summary: 'Verified local backup is overdue',
}

describe('operations monitoring API', () => {
  beforeEach(() => {
    document.cookie = 'hl_csrf=csrf; path=/'
    vi.stubGlobal('fetch', vi.fn())
  })
  afterEach(() => vi.unstubAllGlobals())

  it('reads the bounded dashboard DTO from the canonical route', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({ data: dashboard })))
    await expect(readDashboard()).resolves.toStrictEqual(dashboard)
    expect(vi.mocked(fetch).mock.calls[0][0]).toBe('/api/v1/admin/operations/dashboard')
  })

  it('serializes alert keyset filters in stable server order', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({
      data: [alert],
      meta: { next: 'opaque_cursor' },
    })))
    await expect(listAlerts({
      state: 'open',
      severity: 'critical',
      category: 'backup',
      before: 'older_cursor',
      limit: 50,
    })).resolves.toStrictEqual({ items: [alert], next: 'opaque_cursor' })
    expect(vi.mocked(fetch).mock.calls[0][0]).toBe(
      '/api/v1/admin/operations/alerts?state=open&severity=critical&category=backup&before=older_cursor&limit=50',
    )
  })

  it('acknowledges with the backend empty-body contract', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({
      data: {
        ...alert,
        state: 'acknowledged',
        acknowledgedBy: '20000000-0000-4000-8000-000000000001',
        acknowledgedAt: observedAt,
      },
    })))
    await expect(acknowledgeAlert(alert.id)).resolves.toMatchObject({ state: 'acknowledged' })
    const [url, init] = vi.mocked(fetch).mock.calls[0]
    expect(url).toBe(`/api/v1/admin/operations/alerts/${alert.id}/acknowledge`)
    expect(init?.method).toBe('POST')
    expect(init?.body).toBeUndefined()
  })

  it('rejects unknown monitoring fields so webhook and internal alert data cannot reach the UI', async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(new Response(JSON.stringify({
        data: { ...dashboard, webhookUrl: 'https://secret.example.test/hook' },
      })))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        data: [{ ...alert, deliveryBody: 'secret', consecutiveFailures: 2 }],
        meta: {},
      })))
    await expect(readDashboard()).rejects.toMatchObject({ code: 'invalid_response' })
    await expect(listAlerts({})).rejects.toMatchObject({ code: 'invalid_response' })
  })

  it('rejects malformed states, counters, times, cursors, filters, and non-canonical alert IDs', async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(new Response(JSON.stringify({
        data: { ...dashboard, students: { ...dashboard.students, state: 'unknown' } },
      })))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        data: { ...dashboard, alerts: { ...dashboard.alerts, openCritical: -1 } },
      })))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        data: [{ ...alert, lastObservedAt: 'not-a-time' }],
        meta: {},
      })))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        data: [alert],
        meta: { next: 1 },
      })))
    await expect(readDashboard()).rejects.toMatchObject({ code: 'invalid_response' })
    await expect(readDashboard()).rejects.toMatchObject({ code: 'invalid_response' })
    await expect(listAlerts({})).rejects.toMatchObject({ code: 'invalid_response' })
    await expect(listAlerts({})).rejects.toMatchObject({ code: 'invalid_response' })
    await expect(listAlerts({ limit: 0 })).rejects.toMatchObject({ code: 'invalid_response' })
    await expect(listAlerts({ category: 'private' as never })).rejects.toMatchObject({ code: 'invalid_response' })
    await expect(acknowledgeAlert('NOT-A-UUID')).rejects.toMatchObject({ code: 'invalid_response' })
  })
})

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  listAudit,
  readSettings,
  saveSettings,
} from './api'
import type { OperationsSettings } from './types'

const settings: OperationsSettings = {
  version: 7,
  siteName: 'HappyLearn',
  siteAnnouncement: '期中复习周',
  softDeleteRetentionDays: 30,
  auditRetentionDays: 365,
  operationalSampleRetentionDays: 7,
  backupHour: 2,
  backupMinute: 30,
  backupTimezone: 'Asia/Shanghai',
  diskWarningPercent: 75,
  diskCriticalPercent: 90,
  aiErrorWarningPercent: 10,
  aiErrorCriticalPercent: 25,
  processingQueueWarning: 20,
  processingQueueCritical: 50,
  updatedAt: '2026-07-28T01:02:03Z',
}

describe('operations API', () => {
  beforeEach(() => {
    document.cookie = 'hl_csrf=csrf; path=/'
    vi.stubGlobal('fetch', vi.fn())
  })

  afterEach(() => vi.unstubAllGlobals())

  it('reads the exact 16-field settings DTO from the canonical URL', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({
      data: { ...settings, ignoredServerField: 'must-not-escape-the-parser' },
    })))

    await expect(readSettings()).resolves.toStrictEqual(settings)
    expect(Object.keys(await readSettingsFrom(settings))).toHaveLength(16)
    expect(vi.mocked(fetch).mock.calls[0][0]).toBe('/api/v1/admin/operations/settings')
  })

  it('rejects malformed settings instead of admitting a partial DTO', async () => {
    const partial: Partial<OperationsSettings> = { ...settings }
    delete partial.updatedAt
    vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({ data: partial })))

    await expect(readSettings()).rejects.toMatchObject({ code: 'invalid_response' })
  })

  it('rejects settings and audit records with invalid timestamps', async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(new Response(JSON.stringify({
        data: { ...settings, updatedAt: 'not-a-time' },
      })))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        data: [{
          id: 1,
          action: 'operations.settings_updated',
          targetType: 'system_settings',
          metadata: {},
          occurredAt: 'not-a-time',
        }],
        meta: {},
      })))

    await expect(readSettings()).rejects.toMatchObject({ code: 'invalid_response' })
    await expect(listAudit({})).rejects.toMatchObject({ code: 'invalid_response' })
  })

  it('saves the complete settings object including updatedAt with PUT', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({ data: settings })))

    await expect(saveSettings(settings)).resolves.toStrictEqual(settings)
    const [url, init] = vi.mocked(fetch).mock.calls[0]
    expect(url).toBe('/api/v1/admin/operations/settings')
    expect(init?.method).toBe('PUT')
    expect(JSON.parse(String(init?.body))).toStrictEqual(settings)
  })

  it('serializes only non-empty audit filters in stable server order', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({
      data: [],
      meta: { nextBeforeId: 41 },
    })))

    await expect(listAudit({
      action: 'settings.updated',
      targetType: 'system_settings',
      outcome: 'success',
      actorId: '11111111-1111-4111-8111-111111111111',
      from: '2026-07-01T00:00:00Z',
      to: '2026-07-28T00:00:00Z',
      beforeId: 42,
      limit: 50,
    })).resolves.toEqual({ items: [], nextBeforeId: 41 })
    expect(vi.mocked(fetch).mock.calls[0][0]).toBe(
      '/api/v1/admin/operations/audit?action=settings.updated&targetType=system_settings&outcome=success&actorId=11111111-1111-4111-8111-111111111111&from=2026-07-01T00%3A00%3A00Z&to=2026-07-28T00%3A00%3A00Z&beforeId=42&limit=50',
    )

    vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({ data: [], meta: {} })))
    await listAudit({ action: '', actorId: '', from: '', limit: undefined })
    expect(vi.mocked(fetch).mock.calls[1][0]).toBe('/api/v1/admin/operations/audit')
  })

  it('projects audit responses onto safe fields and allowlisted metadata only', async () => {
    const secret = 'raw-request-payload-secret'
    vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({
      data: [{
        id: 44,
        actorId: '11111111-1111-4111-8111-111111111111',
        action: 'settings.updated',
        targetType: 'system_settings',
        targetId: 'global',
        occurredAt: '2026-07-28T01:02:03Z',
        requestId: secret,
        ip: secret,
        metadata: {
          status: 'active',
          count: 3,
          provider_id: '22222222-2222-4222-8222-222222222222',
          raw_payload: secret,
          api_key: secret,
        },
      }],
      meta: { nextBeforeId: 40 },
    })))

    const page = await listAudit({})
    expect(page).toStrictEqual({
      items: [{
        id: 44,
        actorId: '11111111-1111-4111-8111-111111111111',
        action: 'settings.updated',
        targetType: 'system_settings',
        targetId: 'global',
        occurredAt: '2026-07-28T01:02:03Z',
        metadata: {
          status: 'active',
          count: 3,
          provider_id: '22222222-2222-4222-8222-222222222222',
        },
      }],
      nextBeforeId: 40,
    })
    expect(JSON.stringify(page)).not.toContain(secret)
  })

  it('redacts invalid values even when they use a public audit field name', async () => {
    const secret = 'secret-hidden-in-public-field'
    vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({
      data: [{
        id: 45,
        actorId: secret,
        action: 'operations.settings_updated',
        targetType: 'system_settings',
        targetId: secret,
        occurredAt: '2026-07-28T01:02:03Z',
        metadata: {
          status: secret,
          reason: secret,
          provider_id: secret,
          file_purpose: secret,
          count: -1,
        },
      }],
      meta: {},
    })))

    const page = await listAudit({})
    expect(page.items[0]).toMatchObject({ actorId: '', targetId: '', metadata: {} })
    expect(JSON.stringify(page)).not.toContain(secret)
  })
})

async function readSettingsFrom(value: OperationsSettings): Promise<OperationsSettings> {
  vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({ data: value })))
  return readSettings()
}

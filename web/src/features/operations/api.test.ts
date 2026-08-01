import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  listAudit,
  readSettings,
  saveSettings,
} from './api'
import type { OperationsSettings, OperationsSettingsUpdate } from './types'

const infrastructure: OperationsSettings['infrastructure'] = [
  { key: 'application_database', configured: true, lastValidatedAt: '2026-07-28T01:01:01Z' },
  { key: 'redis_security', configured: true, lastValidatedAt: '2026-07-28T01:01:01Z' },
  { key: 'object_store', configured: true, lastValidatedAt: '2026-07-28T01:01:01Z' },
  { key: 'ai_encryption', configured: true, lastValidatedAt: '2026-07-28T01:01:01Z' },
  { key: 'internal_metrics', configured: true, lastValidatedAt: '2026-07-28T01:01:01Z' },
  { key: 'host_metrics_ingestion', configured: true, lastValidatedAt: '2026-07-28T01:01:01Z' },
  { key: 'alert_webhook', configured: false, lastValidatedAt: '2026-07-28T01:01:01Z' },
  { key: 'local_backup', configured: true, lastValidatedAt: '2026-07-28T01:01:02Z' },
  { key: 'remote_backup', configured: false, lastValidatedAt: null },
]

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
  backupFilesystemWarningPercent: 75,
  backupFilesystemCriticalPercent: 90,
  localBackupAgeWarningHours: 25,
  localBackupAgeCriticalHours: 30,
  aiErrorWarningPercent: 10,
  aiErrorCriticalPercent: 25,
  processingQueueWarning: 20,
  processingQueueCritical: 50,
  processingFailureWarningCount: 5,
  processingFailureCriticalCount: 20,
  loginFailureWarningCount: 20,
  loginFailureCriticalCount: 100,
  authorizationDenialWarningCount: 50,
  authorizationDenialCriticalCount: 200,
  infrastructure,
  updatedAt: '2026-07-28T01:02:03Z',
}

const update: OperationsSettingsUpdate = Object.fromEntries(
  Object.entries(settings).filter(([key]) => key !== 'infrastructure' && key !== 'updatedAt'),
) as OperationsSettingsUpdate

describe('operations API', () => {
  beforeEach(() => {
    document.cookie = 'hl_csrf=csrf; path=/'
    vi.stubGlobal('fetch', vi.fn())
  })

  afterEach(() => vi.unstubAllGlobals())

  it('reads the exact safe settings DTO and stable infrastructure rows from the canonical URL', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({
      data: { ...settings, ignoredServerField: 'must-not-escape-the-parser' },
    })))

    await expect(readSettings()).resolves.toStrictEqual(settings)
    expect(Object.keys(await readSettingsFrom(settings))).toHaveLength(27)
    expect((await readSettingsFrom(settings)).infrastructure).toStrictEqual(infrastructure)
    expect(Object.keys(infrastructure[0])).toStrictEqual(['key', 'configured', 'lastValidatedAt'])
    expect(vi.mocked(fetch).mock.calls[0][0]).toBe('/api/v1/admin/operations/settings')
  })

  it('rejects malformed settings instead of admitting a partial DTO', async () => {
    const partial: Partial<OperationsSettings> = { ...settings }
    delete partial.updatedAt
    vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({ data: partial })))

    await expect(readSettings()).rejects.toMatchObject({ code: 'invalid_response' })
  })

  it('rejects missing, reordered, duplicated, unknown, and metadata-bearing infrastructure rows', async () => {
    const invalidInfrastructure: unknown[] = [
      infrastructure.slice(0, -1),
      [...infrastructure].reverse(),
      [...infrastructure.slice(0, -1), infrastructure[0]],
      infrastructure.map((row, index) => index === 0 ? { ...row, key: 'database_url' } : row),
      infrastructure.map((row, index) => index === 0 ? { ...row, configured: 'yes' } : row),
      infrastructure.map((row, index) => index === 0 ? { ...row, lastValidatedAt: 'not-a-time' } : row),
      infrastructure.map((row, index) => index === 0 ? { ...row, source: '/run/secrets/database' } : row),
    ]
    for (const value of invalidInfrastructure) {
      vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({
        data: { ...settings, infrastructure: value },
      })))
      await expect(readSettings()).rejects.toMatchObject({ code: 'invalid_response' })
    }
  })

  it('rejects settings responses outside every server constraint', async () => {
    const invalid: Array<[string, Partial<OperationsSettings>]> = [
      ['zero version', { version: 0 }],
      ['unsafe version', { version: Number.MAX_SAFE_INTEGER + 1 }],
      ['empty site name', { siteName: '' }],
      ['long site name', { siteName: '学'.repeat(81) }],
      ['invalid Unicode site name', { siteName: '\ud800' }],
      ['long announcement', { siteAnnouncement: '公'.repeat(1001) }],
      ['soft-delete retention', { softDeleteRetentionDays: 29 }],
      ['audit retention', { auditRetentionDays: 2556 }],
      ['sample retention', { operationalSampleRetentionDays: 31 }],
      ['backup hour', { backupHour: 24 }],
      ['backup minute', { backupMinute: 60 }],
      ['backup timezone', { backupTimezone: 'UTC' as 'Asia/Shanghai' }],
      ['disk warning', { diskWarningPercent: 100 }],
      ['disk threshold order', { diskWarningPercent: 90, diskCriticalPercent: 90 }],
      ['disk critical ceiling', { diskCriticalPercent: 101 }],
      ['backup filesystem warning', { backupFilesystemWarningPercent: 0 }],
      ['backup filesystem threshold order', { backupFilesystemWarningPercent: 90, backupFilesystemCriticalPercent: 90 }],
      ['backup filesystem critical ceiling', { backupFilesystemCriticalPercent: 101 }],
      ['local backup age warning', { localBackupAgeWarningHours: 0 }],
      ['local backup age threshold order', { localBackupAgeWarningHours: 30, localBackupAgeCriticalHours: 30 }],
      ['local backup age int32 ceiling', { localBackupAgeCriticalHours: 2_147_483_648 }],
      ['AI warning', { aiErrorWarningPercent: 0 }],
      ['AI threshold order', { aiErrorWarningPercent: 25, aiErrorCriticalPercent: 25 }],
      ['AI critical ceiling', { aiErrorCriticalPercent: 101 }],
      ['queue warning', { processingQueueWarning: 0 }],
      ['queue threshold order', { processingQueueWarning: 50, processingQueueCritical: 50 }],
      ['unsafe queue integer', { processingQueueCritical: Number.MAX_SAFE_INTEGER + 1 }],
      ['queue int32 ceiling', { processingQueueCritical: 2_147_483_648 }],
      ['processing failure warning', { processingFailureWarningCount: 0 }],
      ['processing failure threshold order', { processingFailureWarningCount: 20, processingFailureCriticalCount: 20 }],
      ['processing failure int32 ceiling', { processingFailureCriticalCount: 2_147_483_648 }],
      ['login failure warning', { loginFailureWarningCount: 0 }],
      ['login failure threshold order', { loginFailureWarningCount: 100, loginFailureCriticalCount: 100 }],
      ['login failure int32 ceiling', { loginFailureCriticalCount: 2_147_483_648 }],
      ['authorization denial warning', { authorizationDenialWarningCount: 0 }],
      ['authorization denial threshold order', { authorizationDenialWarningCount: 200, authorizationDenialCriticalCount: 200 }],
      ['authorization denial int32 ceiling', { authorizationDenialCriticalCount: 2_147_483_648 }],
    ]
    for (const [label, override] of invalid) {
      vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({
        data: { ...settings, ...override },
      })))
      await expect(readSettings(), label).rejects.toMatchObject({ code: 'invalid_response' })
    }
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

  it('accepts only real RFC3339Nano timestamps with strict offsets and shapes', async () => {
    const invalid = [
      '2026-07-28',
      '2026-02-30T01:02:03Z',
      '2026-07-28 01:02:03Z',
      '2026-07-28T01:02:03z',
      '2026-07-28T01:02:03+24:00',
      '2026-07-28T01:02:03+08',
      '2026-07-28T01:02:60Z',
      '2026-07-28T01:02:03.1234567890Z',
    ]
    for (const updatedAt of invalid) {
      vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({
        data: { ...settings, updatedAt },
      })))
      await expect(readSettings()).rejects.toMatchObject({ code: 'invalid_response' })
    }

    for (const updatedAt of [
      '2026-07-28T01:02:03.123456789Z',
      '2026-07-28T01:02:03+08:00',
      '2024-02-29T23:59:59.1-05:30',
    ]) {
      vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({
        data: { ...settings, updatedAt },
      })))
      await expect(readSettings()).resolves.toMatchObject({ updatedAt })
    }

    vi.mocked(fetch)
      .mockResolvedValueOnce(new Response(JSON.stringify({
        data: [{
          id: 1,
          action: 'operations.settings_updated',
          targetType: 'system_settings',
          metadata: {},
          occurredAt: '2025-02-29T01:02:03Z',
        }],
        meta: {},
      })))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        data: [{
          id: 1,
          action: 'operations.settings_updated',
          targetType: 'system_settings',
          metadata: {},
          occurredAt: '2026-07-28T01:02:03.123456789+08:00',
        }],
        meta: {},
      })))
    await expect(listAudit({})).rejects.toMatchObject({ code: 'invalid_response' })
    await expect(listAudit({})).resolves.toMatchObject({
      items: [expect.objectContaining({ occurredAt: '2026-07-28T01:02:03.123456789+08:00' })],
    })
  })

  it('saves only the separate editable update shape with PUT', async () => {
    vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({ data: settings })))

    await expect(saveSettings(update)).resolves.toStrictEqual(settings)
    const [url, init] = vi.mocked(fetch).mock.calls[0]
    expect(url).toBe('/api/v1/admin/operations/settings')
    expect(init?.method).toBe('PUT')
    expect(JSON.parse(String(init?.body))).toStrictEqual(update)
    expect(String(init?.body)).not.toContain('infrastructure')
    expect(String(init?.body)).not.toContain('updatedAt')
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

  it('fails closed on audit IDs and cursors outside the exact JavaScript integer range', async () => {
    vi.mocked(fetch)
      .mockResolvedValueOnce(new Response(JSON.stringify({
        data: [{
          id: Number.MAX_SAFE_INTEGER + 1,
          action: 'operations.settings_updated',
          targetType: 'system_settings',
          metadata: {},
          occurredAt: '2026-07-28T01:02:03Z',
        }],
        meta: {},
      })))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        data: [],
        meta: { nextBeforeId: Number.MAX_SAFE_INTEGER + 1 },
      })))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        data: [{
          id: Number.MAX_SAFE_INTEGER,
          action: 'operations.settings_updated',
          targetType: 'system_settings',
          metadata: {},
          occurredAt: '2026-07-28T01:02:03Z',
        }],
        meta: { nextBeforeId: Number.MAX_SAFE_INTEGER },
      })))

    await expect(listAudit({})).rejects.toMatchObject({ code: 'invalid_response' })
    await expect(listAudit({})).rejects.toMatchObject({ code: 'invalid_response' })
    await expect(listAudit({})).resolves.toMatchObject({
      items: [expect.objectContaining({ id: Number.MAX_SAFE_INTEGER })],
      nextBeforeId: Number.MAX_SAFE_INTEGER,
    })
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

  it('accepts canonical non-nil UUIDs of any version and redacts noncanonical IDs', async () => {
    const actorV7 = '01890f3e-e7b2-7cc1-98f2-5e17f0b6c701'
    const targetV7 = '01890f3e-e7b2-7cc2-08f2-5e17f0b6c702'
    const providerV7 = '01890f3e-e7b2-7cc3-f8f2-5e17f0b6c703'
    const modelV7 = '01890f3e-e7b2-7cc4-18f2-5e17f0b6c704'
    const nilUUID = '00000000-0000-0000-0000-000000000000'
    vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({
      data: [
        {
          id: 46,
          actorId: actorV7,
          action: 'operations.settings_updated',
          targetType: 'system_settings',
          targetId: targetV7,
          occurredAt: '2026-07-28T01:02:03Z',
          metadata: { provider_id: providerV7, model_id: modelV7 },
        },
        {
          id: 47,
          actorId: nilUUID,
          action: 'operations.settings_updated',
          targetType: 'system_settings',
          targetId: nilUUID,
          occurredAt: '2026-07-28T01:02:03Z',
          metadata: { provider_id: nilUUID, model_id: nilUUID },
        },
        {
          id: 48,
          actorId: actorV7.toUpperCase(),
          action: 'operations.settings_updated',
          targetType: 'system_settings',
          targetId: targetV7.replace(/-/g, ''),
          occurredAt: '2026-07-28T01:02:03Z',
          metadata: { provider_id: providerV7.toUpperCase(), model_id: modelV7.replace(/-/g, '') },
        },
      ],
      meta: {},
    })))

    const page = await listAudit({})
    expect(page.items[0]).toMatchObject({
      actorId: actorV7,
      targetId: targetV7,
      metadata: { provider_id: providerV7, model_id: modelV7 },
    })
    expect(page.items.slice(1)).toEqual([
      expect.objectContaining({ actorId: '', targetId: '', metadata: {} }),
      expect.objectContaining({ actorId: '', targetId: '', metadata: {} }),
    ])
  })

  it('enforces server integer bounds without losing precision', async () => {
    const metadata = [
      { version: 0 },
      { version: '0' },
      { version: Number.MAX_SAFE_INTEGER + 1 },
      { version: '9223372036854775808' },
      { count: 1_000_000_001 },
      { count: '1000000001' },
      { version: '9999999999999999999999999999999999999999' },
      { count: '0000000001' },
      { version: '+1' },
      { count: '1.0' },
      { version: 1 },
      { version: '9223372036854775807' },
      { count: 0 },
      { count: '1000000000' },
      { version: '1' },
      { version: Number.MAX_SAFE_INTEGER },
      { count: '0' },
      { count: 1_000_000_000 },
    ]
    vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({
      data: metadata.map((value, index) => ({
        id: index + 1,
        action: 'operations.settings_updated',
        targetType: 'system_settings',
        metadata: value,
        occurredAt: '2026-07-28T01:02:03Z',
      })),
      meta: {},
    })))

    const page = await listAudit({})
    expect(page.items.map((item) => item.metadata)).toStrictEqual([
      {}, {}, {}, {}, {}, {}, {}, {}, {}, {},
      { version: 1 },
      { version: '9223372036854775807' },
      { count: 0 },
      { count: '1000000000' },
      { version: '1' },
      { version: Number.MAX_SAFE_INTEGER },
      { count: '0' },
      { count: 1_000_000_000 },
    ])
  })
})

async function readSettingsFrom(value: OperationsSettings): Promise<OperationsSettings> {
  vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({ data: value })))
  return readSettings()
}

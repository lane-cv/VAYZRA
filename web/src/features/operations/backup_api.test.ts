import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  listBackups,
  queueBackup,
  readBackup,
} from './api'

const run = {
  id: '11111111-1111-4111-8111-111111111111',
  trigger: 'manual',
  state: 'succeeded',
  requestedAt: '2026-07-28T01:02:03Z',
  startedAt: '2026-07-28T01:02:04Z',
  finishedAt: '2026-07-28T01:04:03Z',
  logicalBytes: 4096,
  storedBytes: 2048,
  localExpiresAt: '2026-08-04T01:04:03Z',
}
const artifact = {
  kind: 'database_dump',
  repository: 'local',
  sizeBytes: 1024,
  verifiedAt: '2026-07-28T01:04:01Z',
  expiresAt: '2026-08-04T01:04:01Z',
}
const verification = {
  id: '22222222-2222-4222-8222-222222222222',
  state: 'succeeded',
  startedAt: '2026-07-28T02:00:00Z',
  finishedAt: '2026-07-28T02:02:00Z',
  restoredMigrationVersion: 20,
  databaseRowCounts: { users: 3, sessions: 0 },
  checkedObjectCount: 2,
  missingObjectCount: 0,
  unexpectedObjectCount: 0,
  sessionRevocationVerified: true,
  rtoSeconds: 120,
}

const pendingVerification = {
  id: verification.id,
  databaseRowCounts: {},
  checkedObjectCount: 0,
  missingObjectCount: 0,
  unexpectedObjectCount: 0,
  sessionRevocationVerified: false,
}

function response(data: unknown, meta?: unknown, status = 200): Response {
  return new Response(JSON.stringify(
    meta === undefined ? { data } : { data, meta },
  ), { status, headers: { 'Content-Type': 'application/json' } })
}

function detailWithVerification(restoreVerification: unknown): unknown {
  return {
    ...run,
    artifacts: [artifact],
    restoreVerifications: [restoreVerification],
  }
}

function withoutVerificationField(field: string): Record<string, unknown> {
  const result: Record<string, unknown> = { ...verification }
  delete result[field]
  return result
}

afterEach(() => {
  vi.unstubAllGlobals()
  document.cookie = 'hl_csrf=; Max-Age=0; path=/'
})

describe('backup operations API', () => {
  it('strictly parses keyset pages and sends a complete continuation cursor', async () => {
    vi.stubGlobal('fetch', vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({
        data: [run],
        meta: {
          nextBeforeRequestedAt: '2026-07-28T01:02:03Z',
          nextBeforeId: run.id,
        },
      }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        data: [],
        meta: {},
      }), { status: 200, headers: { 'Content-Type': 'application/json' } })))

    const first = await listBackups({ limit: 25 })
    expect(first.items).toEqual([run])
    expect(first.next).toEqual({
      requestedAt: '2026-07-28T01:02:03Z',
      id: run.id,
    })
    await listBackups({ limit: 25, before: first.next! })
    expect(vi.mocked(fetch).mock.calls[1][0]).toBe(
      '/api/v1/admin/operations/backups?beforeRequestedAt=2026-07-28T01%3A02%3A03Z&beforeId=11111111-1111-4111-8111-111111111111&limit=25',
    )
  })

  it('parses safe detail evidence without accepting hashes, paths, or credentials', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response({
      ...run,
      artifacts: [artifact],
      restoreVerifications: [verification],
    })))

    const detail = await readBackup(run.id)
    expect(detail.artifacts[0]).toMatchObject({ repository: 'local', sizeBytes: 1024 })
    expect(detail.restoreVerifications[0]).toMatchObject({
      state: 'succeeded',
      databaseRowCounts: { users: 3, sessions: 0 },
      sessionRevocationVerified: true,
    })
    expect(JSON.stringify(detail)).not.toMatch(/sha256|password|repositoryPath|objectKey/i)
  })

  it.each([
    ['queued', {
      ...pendingVerification,
      state: 'queued',
    }],
    ['restoring', {
      ...pendingVerification,
      state: 'restoring',
      startedAt: '2026-07-28T02:00:00Z',
    }],
    ['checking', {
      ...pendingVerification,
      state: 'checking',
      startedAt: '2026-07-28T02:00:00Z',
    }],
    ['failed without success-only evidence', {
      ...pendingVerification,
      state: 'failed',
      startedAt: '2026-07-28T02:00:00Z',
      finishedAt: '2026-07-28T02:00:00Z',
      missingObjectCount: 2,
      errorCategory: 'reference_check',
    }],
  ])('accepts migration-consistent %s restore evidence', async (_name, restoreVerification) => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response(
      detailWithVerification(restoreVerification),
    )))
    const detail = await readBackup(run.id)
    expect(detail.restoreVerifications).toEqual([restoreVerification])
  })

  it.each([
    ['migration version zero', {
      ...verification,
      restoredMigrationVersion: 0,
    }],
    ['unknown row-count table', {
      ...verification,
      databaseRowCounts: { credentials: 1 },
    }],
    ['negative row count', {
      ...verification,
      databaseRowCounts: { users: -1 },
    }],
    ['fractional row count', {
      ...verification,
      databaseRowCounts: { users: 1.5 },
    }],
    ['unsafe row count', {
      ...verification,
      databaseRowCounts: { users: Number.MAX_SAFE_INTEGER + 1 },
    }],
    ['queued with a start time', {
      ...pendingVerification,
      state: 'queued',
      startedAt: '2026-07-28T02:00:00Z',
    }],
    ['queued with a finish time', {
      ...pendingVerification,
      state: 'queued',
      finishedAt: '2026-07-28T02:00:00Z',
    }],
    ['restoring without a start time', {
      ...pendingVerification,
      state: 'restoring',
    }],
    ['restoring with a finish time', {
      ...pendingVerification,
      state: 'restoring',
      startedAt: '2026-07-28T02:00:00Z',
      finishedAt: '2026-07-28T02:01:00Z',
    }],
    ['checking without a start time', {
      ...pendingVerification,
      state: 'checking',
    }],
    ['checking with a finish time', {
      ...pendingVerification,
      state: 'checking',
      startedAt: '2026-07-28T02:00:00Z',
      finishedAt: '2026-07-28T02:01:00Z',
    }],
    ['succeeded without a start time', withoutVerificationField('startedAt')],
    ['succeeded without a finish time', withoutVerificationField('finishedAt')],
    ['failed without a start time', {
      ...pendingVerification,
      state: 'failed',
      finishedAt: '2026-07-28T02:01:00Z',
    }],
    ['failed without a finish time', {
      ...pendingVerification,
      state: 'failed',
      startedAt: '2026-07-28T02:00:00Z',
    }],
    ['succeeded with a finish time one nanosecond before its start', {
      ...verification,
      startedAt: '2026-07-28T02:00:00.000000002Z',
      finishedAt: '2026-07-28T02:00:00.000000001Z',
    }],
    ['failed with a finish time before its start', {
      ...pendingVerification,
      state: 'failed',
      startedAt: '2026-07-28T02:00:01Z',
      finishedAt: '2026-07-28T02:00:00Z',
    }],
    ['succeeded without a migration version', withoutVerificationField('restoredMigrationVersion')],
    ['succeeded with empty row counts', {
      ...verification,
      databaseRowCounts: {},
    }],
    ['succeeded with missing objects', {
      ...verification,
      missingObjectCount: 1,
    }],
    ['succeeded without session revocation evidence', {
      ...verification,
      sessionRevocationVerified: false,
    }],
    ['succeeded without an RTO', withoutVerificationField('rtoSeconds')],
  ])('rejects migration-inconsistent restore evidence: %s', async (_name, restoreVerification) => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response(
      detailWithVerification(restoreVerification),
    )))
    await expect(readBackup(run.id)).rejects.toMatchObject({
      code: 'invalid_response',
    })
  })

  it.each([
    ['detail root', {
      ...run,
      repositoryPath: '/private/backup',
      artifacts: [artifact],
      restoreVerifications: [verification],
    }],
    ['artifact', {
      ...run,
      artifacts: [{ ...artifact, sha256: 'private-hash' }],
      restoreVerifications: [verification],
    }],
    ['restore verification', {
      ...run,
      artifacts: [artifact],
      restoreVerifications: [{ ...verification, errorTraceId: 'private-trace' }],
    }],
  ])('rejects a forbidden field nested at %s', async (_name, data) => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response(data)))
    await expect(readBackup(run.id)).rejects.toMatchObject({
      code: 'invalid_response',
    })
  })

  it.each([
    ['null', null],
    ['array', []],
    ['string', ''],
    ['unknown key', { unexpected: 'cursor' }],
    ['partial cursor', { nextBeforeRequestedAt: run.requestedAt }],
    ['offset cursor', {
      nextBeforeRequestedAt: '2026-07-28T09:02:03+08:00',
      nextBeforeId: run.id,
    }],
    ['non-canonical fractional cursor', {
      nextBeforeRequestedAt: '2026-07-28T01:02:03.120Z',
      nextBeforeId: run.id,
    }],
  ])('rejects %s list metadata', async (_name, meta) => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response([run], meta)))
    await expect(listBackups({})).rejects.toMatchObject({
      code: 'invalid_response',
    })
  })

  it.each([
    '2026-07-28T09:02:03+08:00',
    '2026-07-28T01:02:03.120Z',
  ])('rejects non-canonical input cursor %s before accessing the network', async (requestedAt) => {
    const fetch = vi.fn()
    vi.stubGlobal('fetch', fetch)
    await expect(listBackups({
      before: {
        requestedAt,
        id: run.id,
      },
    })).rejects.toMatchObject({ code: 'invalid_response' })
    expect(fetch).not.toHaveBeenCalled()
  })

  it.each([
    ['summary error category', { ...run, errorCategory: 'password' }],
    ['nullable summary timestamp', { ...run, startedAt: null }],
    ['nullable summary integer', { ...run, storedBytes: null }],
  ])('rejects %s', async (_name, item) => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response([item], {})))
    await expect(listBackups({})).rejects.toMatchObject({
      code: 'invalid_response',
    })
  })

  it.each([
    ['restore error category', { ...verification, errorCategory: 'password' }],
    ['nullable restore timestamp', { ...verification, finishedAt: null }],
    ['nullable restore integer', { ...verification, rtoSeconds: null }],
  ])('rejects %s', async (_name, restoreVerification) => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response({
      ...run,
      artifacts: [artifact],
      restoreVerifications: [restoreVerification],
    })))
    await expect(readBackup(run.id)).rejects.toMatchObject({
      code: 'invalid_response',
    })
  })

  it.each([
    ['unknown state', { ...run, state: 'complete' }],
    ['unsafe integer', { ...run, storedBytes: Number.MAX_SAFE_INTEGER + 1 }],
    ['unknown field', { ...run, repositoryPath: '/private/backup' }],
  ] as Array<[string, Record<string, unknown>, Record<string, unknown>?]>)(
    'rejects %s',
    async (_name, item, meta = {}) => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({
      data: [item],
      meta,
    }), { status: 200, headers: { 'Content-Type': 'application/json' } })))
    await expect(listBackups({})).rejects.toMatchObject({
      code: 'invalid_response',
    })
    },
  )

  it('queues an empty manual request with the caller idempotency key', async () => {
    document.cookie = 'hl_csrf=csrf-token; path=/'
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({
      data: { ...run, state: 'queued' },
    }), { status: 202, headers: { 'Content-Type': 'application/json' } })))
    await queueBackup('33333333-3333-4333-8333-333333333333')
    const [, init] = vi.mocked(fetch).mock.calls[0]
    expect(init?.method).toBe('POST')
    expect(new Headers(init?.headers).get('Idempotency-Key')).toBe(
      '33333333-3333-4333-8333-333333333333',
    )
    expect(init?.body).toBe('{}')
  })
})

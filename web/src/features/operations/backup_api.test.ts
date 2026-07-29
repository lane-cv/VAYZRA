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

function response(data: unknown, meta?: unknown, status = 200): Response {
  return new Response(JSON.stringify(
    meta === undefined ? { data } : { data, meta },
  ), { status, headers: { 'Content-Type': 'application/json' } })
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

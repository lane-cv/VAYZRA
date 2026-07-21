import { describe, expect, it, vi } from 'vitest'
import { createUploadManager, sha256File, UPLOAD_PART_SIZE, type UploadSessionStore, type UploadTransport } from './uploadManager'

class TestFile {
  readonly size: number
  readonly type = 'application/octet-stream'
  readonly lastModified = 123
  constructor(readonly name: string, private readonly bytes: Uint8Array) { this.size = bytes.byteLength }
  arrayBuffer(): never { throw new Error('whole-file arrayBuffer must not be used') }
  slice(start?: number, end?: number) {
    const part = this.bytes.slice(start ?? 0, end ?? this.size)
    return new Blob([part])
  }
}

class MemorySessionStore implements UploadSessionStore {
  values = new Map<string, string>()
  get(key: string) { return Promise.resolve(this.values.get(key)) }
  set(key: string, value: string) { this.values.set(key, value); return Promise.resolve() }
  delete(key: string) { this.values.delete(key); return Promise.resolve() }
}

describe('uploadManager', () => {
  it('hashes incrementally without reading the whole file', async () => {
    const file = new TestFile('abc.txt', new TextEncoder().encode('abc'))
    await expect(sha256File(file, 2)).resolves.toBe('ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad')
  })

  it('resumes completed 8 MiB parts, uploads at most two concurrently, and retries transient failures', async () => {
    const bytes = new Uint8Array(UPLOAD_PART_SIZE * 3 + 1)
    bytes[bytes.length - 1] = 7
    const file = new TestFile('lesson.bin', bytes)
    const sessions = new MemorySessionStore()
    sessions.values.set(`lesson.bin:${bytes.length}:123`, 'session-1')
    let active = 0
    let maximum = 0
    const attempts = new Map<number, number>()
    const putPart = vi.fn(async (_id: string, number: number, body: Blob, sha256: string) => {
      active += 1
      maximum = Math.max(maximum, active)
      attempts.set(number, (attempts.get(number) ?? 0) + 1)
      if (number === 2 && attempts.get(number) === 1) { active -= 1; throw new Error('temporary') }
      await new Promise((resolve) => setTimeout(resolve, 10))
      active -= 1
      expect(body.size).toBe(number === 4 ? 1 : UPLOAD_PART_SIZE)
      expect(sha256).toMatch(/^[a-f0-9]{64}$/)
      return { number, size: body.size, sha256 }
    })
    const transport: UploadTransport = {
      create: vi.fn(),
      status: vi.fn().mockResolvedValue({ id: 'session-1', displayName: file.name, declaredMime: file.type, expectedSize: file.size, expectedSha256: '', state: 'open', expiresAt: '2026-07-22T00:00:00Z', parts: [{ number: 1, size: UPLOAD_PART_SIZE, sha256: 'done' }] }),
      putPart,
      complete: vi.fn().mockResolvedValue({ fileId: 'file-1', fileVersionId: 'version-1', processingState: 'pending' }),
      cancel: vi.fn(),
    }
    const manager = createUploadManager({ transport, sessions, sleep: async () => {}, random: () => 0 })
    const completed = await manager.start(file)

    expect(transport.create).not.toHaveBeenCalled()
    expect(putPart.mock.calls.map((call) => call[1]).sort()).toEqual([2, 2, 3, 4])
    expect(maximum).toBe(2)
    expect(completed).toEqual({ fileId: 'file-1', fileVersionId: 'version-1', processingState: 'pending' })
    expect(sessions.values.size).toBe(0)
  }, 15_000)

  it('persists only the opaque session id and cancels it explicitly', async () => {
    const file = new TestFile('small.bin', new Uint8Array([1, 2, 3]))
    const sessions = new MemorySessionStore()
    let release!: () => void
    const transport: UploadTransport = {
      create: vi.fn().mockResolvedValue({ id: 'opaque-session', displayName: file.name, declaredMime: file.type, expectedSize: file.size, expectedSha256: '', state: 'open', expiresAt: '2026-07-22T00:00:00Z', parts: [] }),
      status: vi.fn(),
      putPart: vi.fn(() => new Promise<{ number: number; size: number; sha256: string }>((resolve) => { release = () => resolve({ number: 1, size: 3, sha256: 'hash' }) })),
      complete: vi.fn(),
      cancel: vi.fn().mockResolvedValue(undefined),
    }
    const manager = createUploadManager({ transport, sessions })
    const running = manager.start(file)
    await vi.waitFor(() => expect(transport.putPart).toHaveBeenCalled())
    expect([...sessions.values.values()]).toEqual(['opaque-session'])
    await manager.cancel()
    release()
    await running
    expect(transport.cancel).toHaveBeenCalledWith('opaque-session')
    expect(sessions.values.size).toBe(0)
  })

  it('cleans a session that is created after cancellation was requested', async () => {
    const file = new TestFile('race.bin', new Uint8Array([1]))
    const sessions = new MemorySessionStore()
    let resolveCreate!: (session: Awaited<ReturnType<UploadTransport['create']>>) => void
    const transport: UploadTransport = {
      create: vi.fn(() => new Promise<Awaited<ReturnType<UploadTransport['create']>>>((resolve) => { resolveCreate = resolve })),
      status: vi.fn(), putPart: vi.fn(), complete: vi.fn(), cancel: vi.fn().mockResolvedValue(undefined),
    }
    const manager = createUploadManager({ transport, sessions })
    const running = manager.start(file)
    await vi.waitFor(() => expect(transport.create).toHaveBeenCalled())
    await manager.cancel()
    resolveCreate({ id: 'late-session', displayName: file.name, declaredMime: file.type, expectedSize: file.size, expectedSha256: '', state: 'open', expiresAt: '2026-07-22T00:00:00Z', parts: [] })
    await running
    expect(transport.cancel).toHaveBeenCalledWith('late-session')
    expect(sessions.values.size).toBe(0)
    expect(transport.putPart).not.toHaveBeenCalled()
  })

  it('waits for a paused setup flow to exit before resuming the same session', async () => {
    const file = new TestFile('pause.bin', new Uint8Array([9]))
    const sessions = new MemorySessionStore()
    let resolveCreate!: (session: Awaited<ReturnType<UploadTransport['create']>>) => void
    const session = { id: 'pause-session', displayName: file.name, declaredMime: file.type, expectedSize: file.size, expectedSha256: '', state: 'open' as const, expiresAt: '2026-07-22T00:00:00Z', parts: [] }
    const transport: UploadTransport = {
      create: vi.fn(() => new Promise<Awaited<ReturnType<UploadTransport['create']>>>((resolve) => { resolveCreate = resolve })),
      status: vi.fn().mockResolvedValue(session),
      putPart: vi.fn().mockResolvedValue({ number: 1, size: 1, sha256: 'hash' }),
      complete: vi.fn().mockResolvedValue({ fileId: 'f1', fileVersionId: 'v1', processingState: 'pending_scan' }),
      cancel: vi.fn(),
    }
    const manager = createUploadManager({ transport, sessions })
    const first = manager.start(file)
    await vi.waitFor(() => expect(transport.create).toHaveBeenCalled())
    manager.pause()
    const resumed = manager.resume()
    resolveCreate(session)
    await first
    await resumed
    expect(transport.create).toHaveBeenCalledTimes(1)
    expect(transport.status).toHaveBeenCalledTimes(1)
    expect(transport.putPart).toHaveBeenCalledTimes(1)
    expect(transport.complete).toHaveBeenCalledTimes(1)
  })

  it('recovers an idempotently completed session instead of creating a duplicate file', async () => {
    const file = new TestFile('completed.bin', new Uint8Array([4]))
    const sessions = new MemorySessionStore()
    sessions.values.set('completed.bin:1:123', 'completed-session')
    const transport: UploadTransport = {
      create: vi.fn(),
      status: vi.fn().mockResolvedValue({ id: 'completed-session', displayName: file.name, declaredMime: file.type, expectedSize: file.size, expectedSha256: '', state: 'completed', expiresAt: '2026-07-22T00:00:00Z', parts: [{ number: 1, size: 1, sha256: 'hash' }] }),
      putPart: vi.fn(),
      complete: vi.fn().mockResolvedValue({ fileId: 'existing-file', fileVersionId: 'existing-version', processingState: 'pending_scan' }),
      cancel: vi.fn(),
    }
    const manager = createUploadManager({ transport, sessions })
    await expect(manager.start(file)).resolves.toMatchObject({ fileId: 'existing-file' })
    expect(transport.create).not.toHaveBeenCalled()
    expect(transport.putPart).not.toHaveBeenCalled()
    expect(transport.complete).toHaveBeenCalledWith('completed-session')
  })
})

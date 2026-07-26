import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { StreamCallbacks } from '../features/ai/types'

const stream = vi.hoisted(() => ({ subscribe: vi.fn() }))
vi.mock('../features/ai/eventStream', () => ({ subscribeRun: stream.subscribe }))
import { useAIRunStore } from './aiRuns'

type Pending = {
  runId: string
  after: number
  callbacks: StreamCallbacks
  signal: AbortSignal
  resolve: () => void
  reject: (error: unknown) => void
}

describe('useAIRunStore', () => {
  let pending: Pending[]
  beforeEach(() => {
    vi.useFakeTimers()
    setActivePinia(createPinia())
    pending = []
    stream.subscribe.mockReset().mockImplementation(
      (runId: string, after: number, callbacks: StreamCallbacks, signal: AbortSignal) =>
        new Promise<void>((resolve, reject) => pending.push({ runId, after, callbacks, signal, resolve, reject })),
    )
  })

  it('owns one subscription per run, resumes from the last sequence, and deduplicates events', async () => {
    const store = useAIRunStore()
    store.start('run-1', 2)
    store.start('run-1', 0)
    expect(pending).toHaveLength(1)
    expect(pending[0].after).toBe(2)

    pending[0].callbacks.onEvent({ sequence: 3, kind: 'delta', delta: '甲' })
    pending[0].callbacks.onEvent({ sequence: 3, kind: 'delta', delta: '重复' })
    expect(store.runs['run-1']).toMatchObject({ lastSequence: 3, text: '甲' })

    pending[0].resolve()
    await Promise.resolve()
    await vi.advanceTimersByTimeAsync(500)
    expect(pending[1].after).toBe(3)
  })

  it('backs off without overlap, caps at five seconds, and resets after an event', async () => {
    const store = useAIRunStore()
    store.start('run-1', 0)
    for (const delay of [500, 1000, 2000, 5000, 5000]) {
      pending[pending.length - 1].reject(new Error('offline'))
      await Promise.resolve()
      await vi.advanceTimersByTimeAsync(delay - 1)
      expect(pending).toHaveLength(stream.subscribe.mock.calls.length)
      const before = pending.length
      await vi.advanceTimersByTimeAsync(1)
      expect(pending).toHaveLength(before + 1)
    }
    pending[pending.length - 1].callbacks.onEvent({ sequence: 1, kind: 'delta', delta: '恢复' })
    pending[pending.length - 1].reject(new Error('again'))
    await Promise.resolve()
    const before = pending.length
    await vi.advanceTimersByTimeAsync(499)
    expect(pending).toHaveLength(before)
    await vi.advanceTimersByTimeAsync(1)
    expect(pending).toHaveLength(before + 1)
  })

  it('stops terminal runs and clears abort controllers and reconnect timers', async () => {
    const store = useAIRunStore()
    store.start('run-1', 0)
    pending[0].callbacks.onRequestId?.('request-safe')
    pending[0].callbacks.onEvent({ sequence: 1, kind: 'status', status: 'succeeded' })
    pending[0].resolve()
    await vi.runAllTimersAsync()
    expect(stream.subscribe).toHaveBeenCalledOnce()
    expect(store.runs['run-1']).toMatchObject({ status: 'succeeded', requestId: 'request-safe' })

    store.start('run-2', 0)
    const active = pending[pending.length - 1]
    active.reject(new Error('offline'))
    await Promise.resolve()
    store.start('run-3', 0)
    const inFlight = pending[pending.length - 1]
    store.clearAll()
    expect(inFlight.signal.aborted).toBe(true)
    await vi.runAllTimersAsync()
    expect(store.runs).toEqual({})
    expect(vi.getTimerCount()).toBe(0)
  })
})

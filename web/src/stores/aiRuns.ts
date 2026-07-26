import { defineStore } from 'pinia'
import { reactive } from 'vue'
import { subscribeRun } from '../features/ai/eventStream'
import type { AIRunStatus, StreamEvent } from '../features/ai/types'
import { APIError } from '../api/client'

export type AIRunLiveState = {
  id: string
  status: AIRunStatus
  lastSequence: number
  text: string
  errorCode?: string
  requestId?: string
  subscriptionErrorCode?: string
}

type Subscription = {
  controller?: AbortController
  timer?: ReturnType<typeof setTimeout>
  generation: number
  retryIndex: number
}

const TERMINAL = new Set<AIRunStatus>(['succeeded', 'failed', 'cancelled'])
const RECONNECT_DELAYS = [500, 1000, 2000, 5000] as const

export const useAIRunStore = defineStore('ai-runs', () => {
  const runs = reactive<Record<string, AIRunLiveState>>({})
  const subscriptions = new Map<string, Subscription>()
  let lastRunId = ''

  function ensure(runId: string, afterSequence = 0): AIRunLiveState {
    const existing = runs[runId]
    if (existing) {
      existing.lastSequence = Math.max(existing.lastSequence, afterSequence)
      return existing
    }
    return runs[runId] = {
      id: runId,
      status: 'queued',
      lastSequence: afterSequence,
      text: '',
    }
  }

  function start(runId: string, afterSequence: number): void {
    if (!runId || !Number.isSafeInteger(afterSequence) || afterSequence < 0) return
    lastRunId = runId
    const state = ensure(runId, afterSequence)
    if (TERMINAL.has(state.status) || subscriptions.has(runId)) return
    const subscription: Subscription = { generation: 1, retryIndex: 0 }
    subscriptions.set(runId, subscription)
    connect(runId, subscription, subscription.generation)
  }

  function connect(runId: string, subscription: Subscription, generation: number): void {
    if (subscriptions.get(runId) !== subscription || subscription.generation !== generation) return
    const state = runs[runId]
    if (!state || TERMINAL.has(state.status) || subscription.controller) return
    const controller = new AbortController()
    subscription.controller = controller
    void subscribeRun(
      runId,
      state.lastSequence,
      {
        onEvent(event) {
          if (subscriptions.get(runId) !== subscription || subscription.generation !== generation) return
          subscription.retryIndex = 0
          state.subscriptionErrorCode = undefined
          applyFor(runId, event)
        },
        onRequestId(requestId) {
          if (runs[runId] && subscriptions.get(runId) === subscription) runs[runId].requestId = requestId
        },
      },
      controller.signal,
    ).then(
      () => settled(runId, subscription, generation, controller),
      (error: unknown) => settled(runId, subscription, generation, controller, error),
    )
  }

  function settled(
    runId: string,
    subscription: Subscription,
    generation: number,
    controller: AbortController,
    error?: unknown,
  ): void {
    if (subscription.controller === controller) subscription.controller = undefined
    if (
      controller.signal.aborted
      || subscriptions.get(runId) !== subscription
      || subscription.generation !== generation
      || TERMINAL.has(runs[runId]?.status)
    ) return
    if (permanentStreamError(error)) {
      const state = runs[runId]
      if (state) {
        state.subscriptionErrorCode = safeStreamErrorCode(error)
        if (error instanceof APIError && error.requestId) state.requestId = error.requestId
      }
      stopConnection(runId, false)
      return
    }
    if (subscription.timer) return
    const delay = RECONNECT_DELAYS[Math.min(subscription.retryIndex, RECONNECT_DELAYS.length - 1)]
    subscription.retryIndex += 1
    subscription.timer = setTimeout(() => {
      subscription.timer = undefined
      connect(runId, subscription, generation)
    }, delay)
  }

  function applyFor(runId: string, event: StreamEvent): void {
    const state = ensure(runId)
    if (event.sequence <= state.lastSequence) return
    state.lastSequence = event.sequence
    if (event.kind === 'delta') {
      state.status = 'streaming'
      state.text += event.delta ?? ''
    }
    if (event.kind === 'status' && event.status) state.status = event.status
    if (event.kind === 'error') state.status = 'failed'
    if (event.errorCode) state.errorCode = event.errorCode
    if (TERMINAL.has(state.status)) stopConnection(runId, false)
  }

  function apply(event: StreamEvent): void {
    const active = subscriptions.has(lastRunId)
      ? lastRunId
      : [...subscriptions.keys()][0]
    if (active) applyFor(active, event)
  }

  function stopConnection(runId: string, removeState: boolean): void {
    const subscription = subscriptions.get(runId)
    if (subscription) {
      subscriptions.delete(runId)
      subscription.generation += 1
      if (subscription.timer) clearTimeout(subscription.timer)
      subscription.timer = undefined
      subscription.controller?.abort()
      subscription.controller = undefined
    }
    if (removeState) delete runs[runId]
  }

  function stopSubscription(runId: string): void {
    stopConnection(runId, false)
  }

  function retrySubscription(runId: string): void {
    const state = ensure(runId)
    stopConnection(runId, false)
    state.subscriptionErrorCode = undefined
    if (TERMINAL.has(state.status)) state.status = 'queued'
    start(runId, state.lastSequence)
  }

  function clearAll(): void {
    for (const runId of [...subscriptions.keys()]) stopConnection(runId, false)
    for (const runId of Object.keys(runs)) delete runs[runId]
    lastRunId = ''
  }

  function seed(
    runId: string,
    status: AIRunStatus,
    lastSequence: number,
    text?: string,
    errorCode?: string,
  ): AIRunLiveState {
    const state = ensure(runId, lastSequence)
    state.status = status
    if (text !== undefined) state.text = text
    state.errorCode = errorCode
    return state
  }

  return { runs, start, stopSubscription, apply, retrySubscription, clearAll, seed }
})

function permanentStreamError(error: unknown): boolean {
  if (!(error instanceof APIError)) return false
  if (error.code === 'network_error') return false
  if (error.status === 408 || error.status === 429 || error.status >= 500) return false
  return true
}

function safeStreamErrorCode(error: unknown): string {
  if (!(error instanceof APIError)) return 'stream_unavailable'
  const safe = new Set(['not_found', 'forbidden', 'invalid_stream', 'unauthorized'])
  return safe.has(error.code) ? error.code : 'stream_unavailable'
}

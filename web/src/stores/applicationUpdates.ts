import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import { APIError } from '../api/client'
import {
  applyApplicationUpdate,
  checkForUpdates,
  readUpdateStatus,
  rollbackApplicationUpdate,
} from '../features/operations/api'
import type { ApplicationUpdateStatus, UpdateState } from '../features/operations/types'

type UpdateAction = 'check' | 'update' | 'rollback'
type RetryAction = UpdateAction | 'status'

const STEADY_CHECK_MILLISECONDS = 5 * 60 * 1000
const OPERATION_POLL_MILLISECONDS = 1000
const MAXIMUM_POLL_FAILURES = 30
const transientStates = new Set<UpdateState>(['checking', 'updating'])

export const useApplicationUpdateStore = defineStore('application-updates', () => {
  const status = ref<ApplicationUpdateStatus>()
  const initializing = ref(false)
  const checking = ref(false)
  const operation = ref<Exclude<UpdateAction, 'check'>>()
  const retryAction = ref<RetryAction>('check')
  const error = ref('')
  const waitingForService = ref(false)
  const pollExhausted = ref(false)

  let consumers = 0
  let automaticConsumers = 0
  let generation = 0
  let steadyTimer: number | undefined
  let pollTimer: number | undefined
  let pollFailures = 0
  let statusController: AbortController | undefined
  let initializePromise: Promise<void> | undefined

  const isTransient = computed(() => status.value ? transientStates.has(status.value.state) : false)
  const busy = computed(() => initializing.value || checking.value || operation.value !== undefined || isTransient.value)
  const retryBusy = computed(() => (
    initializing.value || checking.value || operation.value !== undefined || waitingForService.value
  ))
  const currentVersion = computed(() => status.value?.currentVersion || '—')
  const hasUpdate = computed(() => status.value?.updateAvailable === true)
  const progress = computed(() => status.value?.progress ?? 0)
  const displayMessage = computed(() => {
    if (waitingForService.value) return '更新服务暂时不可达，正在等待服务恢复…'
    if ((initializing.value || checking.value) && !status.value) return '正在检查远程版本…'
    return status.value?.message || '尚未获取更新状态'
  })
  const retryLabel = computed(() => {
    if (retryAction.value === 'status') return '重新读取状态'
    if (retryAction.value === 'rollback') return '重试回滚'
    if (retryAction.value === 'update') return '重试更新'
    return '重试检查'
  })

  function failureMessage(reason: unknown, fallback: string): string {
    if (reason instanceof APIError && (reason.status === 404 || reason.code === 'updates_disabled')) {
      return '当前部署未启用在线更新'
    }
    if (reason instanceof APIError) {
      const message = reason.message || fallback
      return reason.requestId ? `${message}（支持编号：${reason.requestId}）` : message
    }
    return fallback
  }

  function clearPollTimer() {
    if (pollTimer === undefined) return
    window.clearTimeout(pollTimer)
    pollTimer = undefined
  }

  function clearSteadyTimer() {
    if (steadyTimer === undefined) return
    window.clearInterval(steadyTimer)
    steadyTimer = undefined
  }

  function schedulePoll() {
    clearPollTimer()
    if (consumers === 0) return
    pollTimer = window.setTimeout(() => { void pollOperation() }, OPERATION_POLL_MILLISECONDS)
  }

  function scheduleSteadyChecks() {
    clearSteadyTimer()
    if (automaticConsumers === 0) return
    steadyTimer = window.setInterval(() => { void refresh(false) }, STEADY_CHECK_MILLISECONDS)
  }

  function acceptStatus(next: ApplicationUpdateStatus) {
    status.value = next
    waitingForService.value = false
    pollExhausted.value = false
    pollFailures = 0
    if (transientStates.has(next.state)) {
      schedulePoll()
      return
    }
    clearPollTimer()
    operation.value = undefined
    error.value = next.state === 'failed' ? (next.message || '在线更新未能完成') : ''
  }

  async function pollOperation() {
    if (consumers === 0) return
    pollTimer = undefined
    const currentGeneration = generation
    try {
      const next = await readUpdateStatus()
      if (currentGeneration !== generation || consumers === 0) return
      acceptStatus(next)
    } catch (reason) {
      if (currentGeneration !== generation || consumers === 0) return
      pollFailures++
      waitingForService.value = true
      if (pollFailures < MAXIMUM_POLL_FAILURES) {
        schedulePoll()
        return
      }
      operation.value = undefined
      waitingForService.value = false
      pollExhausted.value = true
      retryAction.value = 'status'
      error.value = failureMessage(reason, '更新状态读取失败，请稍后重试')
    }
  }

  async function refresh(force = false) {
    if (busy.value || status.value?.enabled === false || status.value?.state === 'disabled') return
    checking.value = true
    retryAction.value = 'check'
    error.value = ''
    waitingForService.value = false
    pollExhausted.value = false
    const currentGeneration = generation
    try {
      const next = await checkForUpdates(force)
      if (currentGeneration !== generation || consumers === 0) return
      acceptStatus(next)
    } catch (reason) {
      if (currentGeneration !== generation || consumers === 0) return
      error.value = failureMessage(reason, '远程版本检查失败，请稍后重试')
    } finally {
      if (currentGeneration === generation) checking.value = false
    }
  }

  async function readPersistedStatus(): Promise<boolean> {
    if (initializing.value) {
      await initializePromise
      return false
    }
    initializing.value = true
    retryAction.value = 'status'
    waitingForService.value = false
    pollExhausted.value = false
    statusController?.abort()
    const controller = new AbortController()
    const currentGeneration = generation
    statusController = controller
    try {
      const next = await readUpdateStatus(controller.signal)
      if (currentGeneration !== generation || controller.signal.aborted || consumers === 0) return false
      acceptStatus(next)
      return true
    } catch (reason) {
      if (currentGeneration !== generation || controller.signal.aborted || consumers === 0) return false
      error.value = failureMessage(reason, '更新状态读取失败，请稍后重试')
      return false
    } finally {
      if (currentGeneration === generation) initializing.value = false
      if (statusController === controller) statusController = undefined
    }
  }

  function initialize() {
    if (initializePromise) return initializePromise
    const job = (async () => {
      const loaded = await readPersistedStatus()
      if (!loaded) return
      if (consumers === 0 || automaticConsumers === 0) return
      if (!status.value || status.value.enabled === false || status.value.state === 'disabled' || isTransient.value) return
      await refresh(false)
    })().finally(() => {
      if (initializePromise === job) initializePromise = undefined
    })
    initializePromise = job
    return job
  }

  async function runAction(action: Exclude<UpdateAction, 'check'>, confirmAction = true) {
    if (busy.value || !status.value) return
    if (action === 'update') {
      if (!status.value.updateAvailable || status.value.dirty) return
      if (confirmAction && !window.confirm(`确定更新到 v${status.value.latestVersion || '最新版本'} 吗？更新期间服务可能短暂断开。`)) return
    } else {
      if (!status.value.canRollback || !status.value.previousVersion) return
      if (confirmAction && !window.confirm(`确定回滚到 v${status.value.previousVersion} 吗？回滚会替换当前应用版本，请确认已了解影响。`)) return
    }
    operation.value = action
    retryAction.value = action
    error.value = ''
    waitingForService.value = false
    pollExhausted.value = false
    pollFailures = 0
    const currentGeneration = generation
    try {
      const next = action === 'update'
        ? await applyApplicationUpdate()
        : await rollbackApplicationUpdate()
      if (currentGeneration !== generation || consumers === 0) return
      acceptStatus(next)
    } catch (reason) {
      if (currentGeneration !== generation || consumers === 0) return
      operation.value = undefined
      retryAction.value = 'status'
      error.value = failureMessage(
        reason,
        action === 'update' ? '应用更新失败，请稍后重试' : '版本回滚失败，请稍后重试',
      )
    }
  }

  function retry() {
    if (retryAction.value === 'status') return readPersistedStatus()
    if (retryAction.value === 'check') return refresh(true)
    return runAction(retryAction.value, false)
  }

  function connect(automatic = true) {
    consumers++
    if (!automatic) return
    automaticConsumers++
    if (automaticConsumers === 1) {
      scheduleSteadyChecks()
      void initialize()
    }
  }

  function disconnect(automatic = true) {
    consumers = Math.max(0, consumers - 1)
    if (automatic) automaticConsumers = Math.max(0, automaticConsumers - 1)
    if (automaticConsumers === 0) clearSteadyTimer()
    if (consumers > 0) return
    generation++
    statusController?.abort()
    statusController = undefined
    initializePromise = undefined
    clearPollTimer()
    clearSteadyTimer()
    initializing.value = false
    checking.value = false
    operation.value = undefined
    waitingForService.value = false
  }

  function reloadPage() {
    window.location.reload()
  }

  return {
    status,
    initializing,
    checking,
    operation,
    retryAction,
    error,
    waitingForService,
    pollExhausted,
    isTransient,
    busy,
    retryBusy,
    currentVersion,
    hasUpdate,
    progress,
    displayMessage,
    retryLabel,
    connect,
    disconnect,
    refresh,
    runAction,
    retry,
    reloadPage,
  }
})

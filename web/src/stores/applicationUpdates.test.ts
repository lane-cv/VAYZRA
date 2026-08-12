import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { APIError } from '../api/client'
import {
  applyApplicationUpdate,
  checkForUpdates,
  readUpdateStatus,
  rollbackApplicationUpdate,
} from '../features/operations/api'
import type { ApplicationUpdateStatus } from '../features/operations/types'
import { useApplicationUpdateStore } from './applicationUpdates'

vi.mock('../features/operations/api', () => ({
  applyApplicationUpdate: vi.fn(),
  checkForUpdates: vi.fn(),
  readUpdateStatus: vi.fn(),
  rollbackApplicationUpdate: vi.fn(),
}))

const available: ApplicationUpdateStatus = {
  enabled: true,
  state: 'available',
  strategy: 'github-release',
  repository: 'lane-cv/VAYZRA',
  ref: 'master',
  channel: 'stable',
  currentVersion: '0.1.0',
  latestVersion: '0.2.0',
  currentCommit: '1111111111111111111111111111111111111111',
  latestCommit: '2222222222222222222222222222222222222222',
  releaseName: 'HappyLearn 0.2.0',
  releaseNotes: '新增远程更新。',
  releaseURL: 'https://github.com/lane-cv/VAYZRA/releases/tag/v0.2.0',
  publishedAt: '2026-08-12T01:02:03Z',
  updateAvailable: true,
  dirty: false,
  canRollback: false,
  previousVersion: '',
  phase: 'complete',
  progress: 0,
  message: '发现新版本 0.2.0',
  startedAt: null,
  finishedAt: '2026-08-12T01:02:04Z',
}

describe('application update store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.useFakeTimers()
    vi.resetAllMocks()
    vi.mocked(checkForUpdates).mockResolvedValue(available)
    vi.mocked(readUpdateStatus).mockResolvedValue(available)
    vi.mocked(applyApplicationUpdate).mockResolvedValue(available)
    vi.mocked(rollbackApplicationUpdate).mockResolvedValue(available)
  })

  afterEach(() => {
    useApplicationUpdateStore().disconnect()
    vi.clearAllTimers()
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it('keeps known data and does not substitute a remote check when a later persisted-status read fails', async () => {
    vi.mocked(readUpdateStatus)
      .mockResolvedValueOnce(available)
      .mockRejectedValueOnce(new APIError(503, 'status_unavailable', '状态服务不可达', 'req-status'))
    const store = useApplicationUpdateStore()

    store.connect()
    await vi.waitFor(() => expect(checkForUpdates).toHaveBeenCalledTimes(1))
    store.disconnect()
    vi.mocked(checkForUpdates).mockReturnValueOnce(new Promise(() => {}))
    store.connect()
    expect(store.initializing).toBe(true)
    await vi.waitFor(() => expect(store.initializing).toBe(false))
    await Promise.resolve()

    expect(store.status).toEqual(available)
    expect(store.error).toContain('状态服务不可达')
    expect(checkForUpdates).toHaveBeenCalledTimes(1)
  })

  it('re-reads persisted status after polling is exhausted without replaying the mutation', async () => {
    const updating: ApplicationUpdateStatus = {
      ...available, state: 'updating', phase: 'switching', progress: 70, message: '正在切换服务',
    }
    const success: ApplicationUpdateStatus = {
      ...available, state: 'success', updateAvailable: false, currentVersion: '0.2.0',
      phase: 'complete', progress: 100, message: '更新完成',
    }
    vi.stubGlobal('confirm', vi.fn().mockReturnValue(true))
    vi.mocked(applyApplicationUpdate).mockResolvedValueOnce(updating)
    vi.mocked(readUpdateStatus).mockRejectedValue(
      new APIError(503, 'status_unavailable', '状态服务不可达', 'req-poll'),
    )
    const store = useApplicationUpdateStore()
    store.connect(false)
    await store.refresh(true)
    await store.runAction('update')

    await vi.advanceTimersByTimeAsync(30_000)
    expect(store.pollExhausted).toBe(true)
    expect(store.retryLabel).toBe('重新读取状态')
    expect(readUpdateStatus).toHaveBeenCalledTimes(30)

    vi.mocked(readUpdateStatus).mockResolvedValueOnce(success)
    await store.retry()
    expect(store.status).toEqual(success)
    expect(applyApplicationUpdate).toHaveBeenCalledTimes(1)
    expect(readUpdateStatus).toHaveBeenCalledTimes(31)
  })

  it('reconciles authoritative status after a mutation request fails without replaying the mutation', async () => {
    const success: ApplicationUpdateStatus = {
      ...available, state: 'success', updateAvailable: false, currentVersion: '0.2.0',
      phase: 'complete', progress: 100, message: '服务端确认更新完成',
    }
    vi.stubGlobal('confirm', vi.fn().mockReturnValue(true))
    vi.mocked(applyApplicationUpdate).mockRejectedValueOnce(
      new APIError(503, 'network_error', '更新请求结果未知', 'req-mutation'),
    )
    vi.mocked(readUpdateStatus).mockResolvedValueOnce(success)
    const store = useApplicationUpdateStore()
    store.connect(false)
    await store.refresh(true)
    await store.runAction('update')

    expect(store.error).toContain('更新请求结果未知')
    expect(store.retryLabel).toBe('重新读取状态')
    await store.retry()

    expect(store.status).toEqual(success)
    expect(applyApplicationUpdate).toHaveBeenCalledTimes(1)
    expect(readUpdateStatus).toHaveBeenCalledTimes(1)
  })

  it('accepts a persisted terminal status before its background check finishes', async () => {
    vi.mocked(checkForUpdates).mockReturnValueOnce(new Promise(() => {}))
    const store = useApplicationUpdateStore()
    store.connect()

    await vi.waitFor(() => expect(checkForUpdates).toHaveBeenCalledTimes(1))
    expect(store.status).toEqual(available)
    expect(store.checking).toBe(true)
  })

  it('accepts a disabled persisted status and never checks the remote release', async () => {
    const disabled: ApplicationUpdateStatus = {
      ...available,
      enabled: false,
      state: 'disabled',
      updateAvailable: false,
      message: '当前部署未启用在线更新',
    }
    vi.mocked(readUpdateStatus).mockResolvedValueOnce(disabled)
    const store = useApplicationUpdateStore()
    store.connect()

    await vi.waitFor(() => expect(store.status).toEqual(disabled))
    await vi.advanceTimersByTimeAsync(10 * 60 * 1000)
    expect(checkForUpdates).not.toHaveBeenCalled()
  })

  it('owns exactly one steady timer and one initialization for duplicate automatic consumers', async () => {
    vi.mocked(readUpdateStatus).mockReturnValueOnce(new Promise(() => {}))
    const store = useApplicationUpdateStore()
    store.connect()
    store.connect()

    expect(readUpdateStatus).toHaveBeenCalledTimes(1)
    expect(vi.getTimerCount()).toBe(1)
    store.disconnect()
    expect(vi.getTimerCount()).toBe(1)
    store.disconnect()
    expect(vi.getTimerCount()).toBe(0)
  })

  it('treats a persisted transient state as globally busy', async () => {
    const updating: ApplicationUpdateStatus = {
      ...available, state: 'updating', phase: 'building', progress: 40, message: '正在构建服务',
    }
    vi.mocked(readUpdateStatus)
      .mockResolvedValueOnce(updating)
      .mockReturnValueOnce(new Promise(() => {}))
    const store = useApplicationUpdateStore()
    store.connect()
    await vi.waitFor(() => expect(store.status).toEqual(updating))

    expect(store.busy).toBe(true)
    await store.refresh(true)
    expect(checkForUpdates).not.toHaveBeenCalled()
  })
})

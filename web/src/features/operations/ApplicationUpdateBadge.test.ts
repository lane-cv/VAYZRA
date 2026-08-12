import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { APIError } from '../../api/client'
import ApplicationUpdateBadge from './ApplicationUpdateBadge.vue'
import {
  applyApplicationUpdate,
  checkForUpdates,
  readUpdateStatus,
  rollbackApplicationUpdate,
} from './api'
import type { ApplicationUpdateStatus } from './types'

vi.mock('./api', () => ({
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
  releaseNotes: '新增远程更新。\n保留可恢复发布。',
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

const mountedWrappers: Array<{ unmount(): void }> = []

function updateStatus(overrides: Partial<ApplicationUpdateStatus>): ApplicationUpdateStatus {
  return { ...available, ...overrides }
}

async function mountBadge() {
  const wrapper = mount(ApplicationUpdateBadge, {
    attachTo: document.body,
    global: { plugins: [createPinia()] },
  })
  mountedWrappers.push(wrapper)
  await flushPromises()
  return wrapper
}

describe('ApplicationUpdateBadge', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.clearAllMocks()
    vi.mocked(checkForUpdates).mockResolvedValue(available)
    vi.mocked(readUpdateStatus).mockResolvedValue(available)
    vi.mocked(applyApplicationUpdate).mockResolvedValue(available)
    vi.mocked(rollbackApplicationUpdate).mockResolvedValue(available)
  })

  afterEach(() => {
    for (const wrapper of mountedWrappers.splice(0)) wrapper.unmount()
    vi.unstubAllGlobals()
    vi.useRealTimers()
  })

  it('opens an accessible version dropdown with release notes and a safe external link', async () => {
    const wrapper = await mountBadge()
    const trigger = wrapper.get('[data-testid="application-update-trigger"]')
    expect(trigger.text()).toContain('v0.1.0')
    expect(trigger.attributes('aria-expanded')).toBe('false')
    expect(wrapper.get('[data-testid="update-available-indicator"]').attributes('aria-label')).toBe('有新版本')

    await trigger.trigger('click')
    const panel = wrapper.get('[role="dialog"]')
    expect(trigger.attributes('aria-expanded')).toBe('true')
    expect(panel.attributes('data-placement')).toBe('bottom-start')
    expect(panel.classes()).toContain('version-panel-below')
    expect(panel.findAll('.version-overview strong')[1].text()).toBe('v0.2.0')
    expect(panel.text()).toContain('HappyLearn 0.2.0')
    expect(panel.text()).toContain('新增远程更新。')
    expect(panel.text()).toContain('保留可恢复发布。')
    expect(panel.get('a').attributes()).toMatchObject({
      href: available.releaseURL,
      target: '_blank',
      rel: 'noopener noreferrer',
    })
  })

  it('applies an update once and short-polls progress until the operation succeeds', async () => {
    vi.stubGlobal('confirm', vi.fn().mockReturnValue(true))
    vi.mocked(applyApplicationUpdate).mockResolvedValueOnce(updateStatus({
      state: 'updating', phase: 'preparing', progress: 5, message: '准备更新',
    }))
    vi.mocked(readUpdateStatus)
      .mockResolvedValueOnce(available)
      .mockResolvedValueOnce(updateStatus({
        state: 'updating', phase: 'building', progress: 55, message: '正在构建',
      }))
      .mockResolvedValueOnce(updateStatus({
        state: 'success', updateAvailable: false, currentVersion: '0.2.0',
        phase: 'complete', progress: 100, message: '更新完成',
      }))

    const wrapper = await mountBadge()
    await wrapper.get('[data-testid="application-update-trigger"]').trigger('click')
    await wrapper.get('[data-testid="apply-update"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[role="progressbar"]').attributes('aria-valuenow')).toBe('5')

    await vi.advanceTimersByTimeAsync(1000)
    await flushPromises()
    expect(wrapper.get('[role="progressbar"]').attributes('aria-valuenow')).toBe('55')
    await vi.advanceTimersByTimeAsync(1000)
    await flushPromises()
    expect(wrapper.get('[role="status"]').text()).toContain('更新完成')
    expect(wrapper.get('[data-testid="reload-application"]').text()).toBe('重新加载页面')
    expect(vi.mocked(applyApplicationUpdate)).toHaveBeenCalledTimes(1)
  })

  it('keeps polling across a temporary disconnect while the service restarts', async () => {
    vi.stubGlobal('confirm', vi.fn().mockReturnValue(true))
    vi.mocked(applyApplicationUpdate).mockResolvedValueOnce(updateStatus({
      state: 'updating', phase: 'switching', progress: 70, message: '正在切换服务',
    }))
    vi.mocked(readUpdateStatus)
      .mockResolvedValueOnce(available)
      .mockRejectedValueOnce(new APIError(0, 'network_error', '网络连接异常', ''))
      .mockResolvedValueOnce(updateStatus({
        state: 'success', updateAvailable: false, currentVersion: '0.2.0',
        phase: 'complete', progress: 100, message: '服务已恢复',
      }))

    const wrapper = await mountBadge()
    await wrapper.get('[data-testid="application-update-trigger"]').trigger('click')
    await wrapper.get('[data-testid="apply-update"]').trigger('click')
    await flushPromises()
    await vi.advanceTimersByTimeAsync(1000)
    await flushPromises()
    expect(wrapper.get('[role="status"]').text()).toContain('等待服务恢复')
    await vi.advanceTimersByTimeAsync(1000)
    await flushPromises()
    expect(wrapper.text()).toContain('服务已恢复')
  })

  it('reconciles status instead of replaying an update request after its result is unknown', async () => {
    vi.stubGlobal('confirm', vi.fn().mockReturnValue(true))
    vi.mocked(applyApplicationUpdate).mockRejectedValueOnce(
      new APIError(500, 'update_failed', '构建镜像失败', 'req-update'),
    )
    vi.mocked(readUpdateStatus)
      .mockResolvedValueOnce(available)
      .mockResolvedValueOnce(updateStatus({
        state: 'success', updateAvailable: false, currentVersion: '0.2.0',
        phase: 'complete', progress: 100, message: '服务端确认更新完成',
      }))

    const wrapper = await mountBadge()
    await wrapper.get('[data-testid="application-update-trigger"]').trigger('click')
    await wrapper.get('[data-testid="apply-update"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('构建镜像失败')
    const retry = wrapper.get('[data-testid="retry-update"]')
    expect(retry.text()).toBe('重新读取状态')
    await retry.trigger('click')
    await flushPromises()
    expect(wrapper.get('[role="status"]').text()).toContain('服务端确认更新完成')
    expect(vi.mocked(applyApplicationUpdate)).toHaveBeenCalledTimes(1)
    expect(vi.mocked(readUpdateStatus)).toHaveBeenCalledTimes(2)
  })

  it('requires explicit confirmation before rolling back to the displayed previous version', async () => {
    vi.mocked(checkForUpdates).mockResolvedValueOnce(updateStatus({
      state: 'current', updateAvailable: false, currentVersion: '0.2.0', latestVersion: '0.2.0',
      canRollback: true, previousVersion: '0.1.0', message: '已是最新版本',
    }))
    vi.mocked(rollbackApplicationUpdate).mockResolvedValueOnce(updateStatus({
      state: 'success', updateAvailable: false, currentVersion: '0.1.0', latestVersion: '0.2.0',
      canRollback: false, previousVersion: '', phase: 'complete', progress: 100, message: '回滚完成',
    }))
    vi.stubGlobal('confirm', vi.fn().mockReturnValueOnce(false).mockReturnValueOnce(true))

    const wrapper = await mountBadge()
    await wrapper.get('[data-testid="application-update-trigger"]').trigger('click')
    const rollback = wrapper.get('[data-testid="rollback-update"]')
    expect(rollback.text()).toContain('v0.1.0')
    await rollback.trigger('click')
    expect(vi.mocked(rollbackApplicationUpdate)).not.toHaveBeenCalled()
    await rollback.trigger('click')
    await flushPromises()
    expect(vi.mocked(confirm)).toHaveBeenLastCalledWith(expect.stringContaining('v0.1.0'))
    expect(wrapper.get('[role="status"]').text()).toContain('回滚完成')
  })

  it('resumes short polling when the persisted status is already updating on mount', async () => {
    vi.mocked(readUpdateStatus)
      .mockResolvedValueOnce(updateStatus({
        state: 'updating', phase: 'switching', progress: 72, message: '正在恢复更新进度',
      }))
      .mockResolvedValueOnce(updateStatus({
        state: 'success', updateAvailable: false, currentVersion: '0.2.0',
        phase: 'complete', progress: 100, message: '重载后更新完成',
      }))

    const wrapper = await mountBadge()
    expect(checkForUpdates).not.toHaveBeenCalled()
    await wrapper.get('[data-testid="application-update-trigger"]').trigger('click')
    expect(wrapper.get('[role="progressbar"]').attributes('aria-valuenow')).toBe('72')

    await vi.advanceTimersByTimeAsync(1000)
    await flushPromises()
    expect(wrapper.get('[role="status"]').text()).toContain('重载后更新完成')
    expect(vi.mocked(readUpdateStatus)).toHaveBeenCalledTimes(2)
  })

  it('does not run the steady check while a recovered operation remains transient', async () => {
    vi.mocked(readUpdateStatus)
      .mockResolvedValueOnce(updateStatus({
        state: 'updating', phase: 'building', progress: 48, message: '构建仍在进行',
      }))
      .mockReturnValueOnce(new Promise(() => {}))

    const wrapper = await mountBadge()
    await vi.advanceTimersByTimeAsync(5 * 60 * 1000)
    expect(checkForUpdates).not.toHaveBeenCalled()
    expect(vi.mocked(readUpdateStatus)).toHaveBeenCalledTimes(2)
    wrapper.unmount()
  })

  it('closes the dropdown with Escape and stops scheduled checks after unmount', async () => {
    const wrapper = await mountBadge()
    const trigger = wrapper.get('[data-testid="application-update-trigger"]')
    await trigger.trigger('click')
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await flushPromises()
    expect(trigger.attributes('aria-expanded')).toBe('false')
    expect(document.activeElement).toBe(trigger.element)

    wrapper.unmount()
    await vi.advanceTimersByTimeAsync(5 * 60 * 1000)
    expect(vi.mocked(checkForUpdates)).toHaveBeenCalledTimes(1)
  })
})

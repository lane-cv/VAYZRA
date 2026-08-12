import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { defineComponent } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { APIError } from '../../api/client'
import { useSessionStore } from '../../stores/session'
import ApplicationUpdateBadge from './ApplicationUpdateBadge.vue'
import SystemSettingsView from './SystemSettingsView.vue'
import {
  applyApplicationUpdate,
  checkForUpdates,
  readSettings,
  readUpdateStatus,
  rollbackApplicationUpdate,
  saveSettings,
} from './api'
import type { ApplicationUpdateStatus, OperationsSettings } from './types'

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

vi.mock('./api', () => ({
  readSettings: vi.fn(),
  saveSettings: vi.fn(),
  checkForUpdates: vi.fn(),
  readUpdateStatus: vi.fn(),
  applyApplicationUpdate: vi.fn(),
  rollbackApplicationUpdate: vi.fn(),
}))

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
const availableUpdate: ApplicationUpdateStatus = {
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
  releaseNotes: '新增远程更新与回滚状态。',
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

async function mountSettings(role: 'admin' | 'student' = 'admin') {
  const pinia = createPinia()
  useSessionStore(pinia).setUser({
    id: role === 'admin' ? 'admin-1' : 'student-1',
    username: role,
    displayName: role === 'admin' ? '张老师' : '林同学',
    role,
    mustChangePassword: false,
  })
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/settings', component: SystemSettingsView },
      { path: '/other', component: { template: '<p>other</p>' } },
      { path: '/admin/audit', component: { template: '<p>audit</p>' } },
    ],
  })
  await router.push('/settings')
  await router.isReady()
  const wrapper = mount({ template: '<RouterView />' }, { attachTo: document.body, global: { plugins: [pinia, router] } })
  mountedWrappers.push(wrapper)
  return { wrapper, router }
}

describe('SystemSettingsView', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(readSettings).mockResolvedValue({ ...settings })
    vi.mocked(saveSettings).mockResolvedValue({ ...settings, version: 8 })
  })
  afterEach(() => {
    for (const wrapper of mountedWrappers.splice(0)) wrapper.unmount()
    vi.unstubAllGlobals()
    vi.useRealTimers()
  })

  it('renders loading and explicit grouped server-range fields', async () => {
    let resolve!: (value: OperationsSettings) => void
    vi.mocked(readSettings).mockReturnValueOnce(new Promise((done) => { resolve = done }))
    const { wrapper } = await mountSettings()
    expect(wrapper.get('[role="status"]').text()).toContain('正在加载')
    resolve(settings)
    await flushPromises()
    expect(wrapper.findAll('fieldset').map((field) => field.get('legend').text())).toEqual([
      '站点信息',
      '数据保留',
      '备份计划',
      '磁盘告警',
      '备份存储告警',
      '本地备份时效告警',
      'AI 错误率告警',
      '处理队列告警',
      '处理失败告警',
      '登录失败告警',
      '授权拒绝告警',
    ])
    expect(wrapper.get('[data-testid="site-name"]').attributes('maxlength')).toBe('80')
    expect(wrapper.get('[data-testid="soft-delete-retention"]').attributes()).toMatchObject({ min: '30', max: '365' })
    expect(wrapper.get('[data-testid="audit-retention"]').attributes()).toMatchObject({ min: '365', max: '2555' })
    expect(wrapper.get('[data-testid="sample-retention"]').attributes()).toMatchObject({ min: '1', max: '30' })
    expect(wrapper.get('[data-testid="backup-timezone"]').attributes('readonly')).toBeDefined()
  })

  it('renders a read-only Chinese infrastructure section with only status and validation time', async () => {
    const { wrapper } = await mountSettings()
    await flushPromises()
    const section = wrapper.get('[data-testid="infrastructure-status"]')
    expect(section.get('h2').text()).toBe('基础设施配置状态')
    expect(section.findAll('[data-testid="infrastructure-row"]')).toHaveLength(9)
    expect(section.text()).toContain('应用数据库')
    expect(section.text()).toContain('已配置')
    expect(section.text()).toContain('未配置')
    expect(section.text()).toContain('尚无验证记录')
    expect(section.findAll('input')).toHaveLength(0)
    expect(section.findAll('[title], [data-config], [data-source], [data-url], [data-path]')).toHaveLength(0)
    expect(section.html()).not.toContain('/run/secrets')
  })

  it('keeps checks manual on the settings page and renders the complete release summary on demand', async () => {
    vi.mocked(checkForUpdates).mockResolvedValueOnce(availableUpdate)
    const { wrapper } = await mountSettings()
    await flushPromises()
    const section = wrapper.get('[data-testid="application-updates"]')
    expect(checkForUpdates).not.toHaveBeenCalled()
    expect(section.text()).toContain('点击“立即检查”获取远程版本信息')

    await section.get('[data-testid="check-updates"]').trigger('click')
    await flushPromises()
    expect(section.text()).toContain('当前版本')
    expect(section.text()).toContain('v0.1.0')
    expect(section.text()).toContain('最新版本')
    expect(section.text()).toContain('v0.2.0')
    expect(section.text()).toContain('新增远程更新与回滚状态。')
    expect(section.get('a').attributes()).toMatchObject({
      href: availableUpdate.releaseURL,
      target: '_blank',
      rel: 'noopener noreferrer',
    })
  })

  it('preserves the last known update summary when a later manual check fails', async () => {
    vi.mocked(checkForUpdates)
      .mockResolvedValueOnce({
        ...availableUpdate,
        state: 'current',
        updateAvailable: false,
        currentVersion: '0.2.0',
        latestVersion: '0.2.0',
        message: '已是最新版本',
      })
      .mockRejectedValueOnce(new APIError(502, 'update_check_failed', '远程检查暂不可用', 'req-check'))
      .mockResolvedValueOnce({
        ...availableUpdate,
        state: 'available',
        message: '重新检查成功',
      })

    const { wrapper } = await mountSettings()
    await flushPromises()
    const section = wrapper.get('[data-testid="application-updates"]')
    await section.get('[data-testid="check-updates"]').trigger('click')
    await flushPromises()
    expect(section.text()).toContain('已是最新版本')

    await section.get('[data-testid="check-updates"]').trigger('click')
    await flushPromises()
    expect(section.get('[role="alert"]').text()).toContain('远程检查暂不可用')
    expect(section.text()).toContain('已是最新版本')
    await section.get('[data-testid="retry-update"]').trigger('click')
    await flushPromises()
    expect(section.get('[role="status"]').text()).toContain('重新检查成功')
  })

  it('short-polls update progress to a terminal state without starting a second action', async () => {
    vi.useFakeTimers()
    vi.stubGlobal('confirm', vi.fn().mockReturnValue(true))
    vi.mocked(checkForUpdates).mockResolvedValueOnce(availableUpdate)
    vi.mocked(applyApplicationUpdate).mockResolvedValueOnce({
      ...availableUpdate, state: 'updating', phase: 'building', progress: 40, message: '正在构建服务',
    })
    vi.mocked(readUpdateStatus).mockResolvedValueOnce({
      ...availableUpdate, state: 'success', updateAvailable: false,
      currentVersion: '0.2.0', phase: 'complete', progress: 100, message: '更新完成',
    })

    const { wrapper } = await mountSettings()
    await flushPromises()
    const section = wrapper.get('[data-testid="application-updates"]')
    await section.get('[data-testid="check-updates"]').trigger('click')
    await flushPromises()
    await section.get('[data-testid="apply-update"]').trigger('click')
    await section.get('[data-testid="apply-update"]').trigger('click')
    await flushPromises()
    expect(section.get('[role="progressbar"]').attributes('aria-valuenow')).toBe('40')
    await vi.advanceTimersByTimeAsync(1000)
    await flushPromises()
    expect(section.get('[role="status"]').text()).toContain('更新完成')
    expect(section.get('[data-testid="reload-application"]').text()).toBe('重新加载页面')
    expect(vi.mocked(applyApplicationUpdate)).toHaveBeenCalledTimes(1)
  })

  it('shares one globally busy OTA action between the badge and settings page', async () => {
    vi.stubGlobal('confirm', vi.fn().mockReturnValue(true))
    vi.mocked(readUpdateStatus).mockResolvedValue(availableUpdate)
    vi.mocked(checkForUpdates).mockResolvedValue(availableUpdate)
    vi.mocked(applyApplicationUpdate).mockReturnValue(new Promise(() => {}))

    const pinia = createPinia()
    useSessionStore(pinia).setUser({
      id: 'admin-1', username: 'admin', displayName: '张老师', role: 'admin', mustChangePassword: false,
    })
    const Host = defineComponent({
      components: { ApplicationUpdateBadge, SystemSettingsView },
      template: '<ApplicationUpdateBadge /><SystemSettingsView />',
    })
    const wrapper = mount(Host, { attachTo: document.body, global: { plugins: [pinia] } })
    mountedWrappers.push(wrapper)
    await flushPromises()

    const settingsSection = wrapper.get('[data-testid="application-updates"]')
    await settingsSection.get('[data-testid="check-updates"]').trigger('click')
    await flushPromises()
    await wrapper.get('[data-testid="application-update-trigger"]').trigger('click')
    const badge = wrapper.get('.application-update-badge')
    await settingsSection.get('[data-testid="apply-update"]').trigger('click')
    await wrapper.get('[data-testid="application-update-trigger"]').trigger('click')

    expect(settingsSection.get('[data-testid="apply-update"]').attributes('disabled')).toBeDefined()
    expect(badge.get('[data-testid="apply-update"]').attributes('disabled')).toBeDefined()
    expect(vi.mocked(applyApplicationUpdate)).toHaveBeenCalledTimes(1)
  })

  it('re-reads authoritative status after an apply error instead of offering to replay it', async () => {
    vi.stubGlobal('confirm', vi.fn().mockReturnValue(true))
    vi.mocked(checkForUpdates).mockResolvedValueOnce(availableUpdate)
    vi.mocked(applyApplicationUpdate).mockRejectedValueOnce(
      new APIError(503, 'network_error', '更新请求结果未知', 'req-apply'),
    )
    vi.mocked(readUpdateStatus).mockResolvedValueOnce({
      ...availableUpdate, state: 'success', updateAvailable: false,
      currentVersion: '0.2.0', phase: 'complete', progress: 100, message: '服务端确认更新完成',
    })

    const { wrapper } = await mountSettings()
    await flushPromises()
    const section = wrapper.get('[data-testid="application-updates"]')
    await section.get('[data-testid="check-updates"]').trigger('click')
    await flushPromises()
    await section.get('[data-testid="apply-update"]').trigger('click')
    await flushPromises()

    const retry = section.get('[data-testid="retry-update"]')
    expect(retry.text()).toBe('重新读取状态')
    await retry.trigger('click')
    await flushPromises()
    expect(section.get('[role="status"]').text()).toContain('服务端确认更新完成')
    expect(vi.mocked(applyApplicationUpdate)).toHaveBeenCalledTimes(1)
    expect(vi.mocked(readUpdateStatus)).toHaveBeenCalledTimes(1)
  })

  it('does not request rollback until the administrator confirms the previous version', async () => {
    vi.mocked(checkForUpdates).mockResolvedValueOnce({
      ...availableUpdate, state: 'current', updateAvailable: false,
      currentVersion: '0.2.0', latestVersion: '0.2.0',
      canRollback: true, previousVersion: '0.1.0', message: '已是最新版本',
    })
    vi.mocked(rollbackApplicationUpdate).mockResolvedValueOnce({
      ...availableUpdate, state: 'success', updateAvailable: false,
      currentVersion: '0.1.0', canRollback: false, previousVersion: '',
      phase: 'complete', progress: 100, message: '已回滚到 0.1.0',
    })
    vi.stubGlobal('confirm', vi.fn().mockReturnValueOnce(false).mockReturnValueOnce(true))

    const { wrapper } = await mountSettings()
    await flushPromises()
    const section = wrapper.get('[data-testid="application-updates"]')
    await section.get('[data-testid="check-updates"]').trigger('click')
    await flushPromises()
    const rollback = section.get('[data-testid="rollback-update"]')
    await rollback.trigger('click')
    expect(vi.mocked(rollbackApplicationUpdate)).not.toHaveBeenCalled()
    await rollback.trigger('click')
    await flushPromises()
    expect(vi.mocked(confirm)).toHaveBeenLastCalledWith(expect.stringContaining('v0.1.0'))
    expect(section.get('[role="status"]').text()).toContain('已回滚到 0.1.0')
  })

  it('validates ordered thresholds before saving', async () => {
    const { wrapper } = await mountSettings()
    await flushPromises()
    await wrapper.get('[data-testid="disk-warning"]').setValue('95')
    await wrapper.get('[data-testid="disk-critical"]').setValue('90')
    await wrapper.get('form').trigger('submit')
    expect(wrapper.get('[role="alert"]').text()).toContain('磁盘严重阈值必须高于警告阈值')
    expect(saveSettings).not.toHaveBeenCalled()
  })

  it.each([
    {
      name: 'backup filesystem percent lower bound',
      values: [['backup-filesystem-warning', '0']],
      message: '备份存储阈值须为 1–100 的整数',
    },
    {
      name: 'backup filesystem percent upper bound',
      values: [['backup-filesystem-critical', '101']],
      message: '备份存储阈值须为 1–100 的整数',
    },
    {
      name: 'backup filesystem percent ordering',
      values: [['backup-filesystem-critical', '75']],
      message: '备份存储严重阈值必须高于警告阈值',
    },
    {
      name: 'local backup age hour lower bound',
      values: [['local-backup-age-warning', '0']],
      message: '本地备份时效阈值须为有效正整数',
    },
    {
      name: 'local backup age hour upper bound',
      values: [['local-backup-age-critical', '2147483648']],
      message: '本地备份时效阈值须为有效正整数',
    },
    {
      name: 'local backup age hour ordering',
      values: [['local-backup-age-critical', '25']],
      message: '本地备份时效严重阈值必须高于警告阈值',
    },
    {
      name: 'processing failure count lower bound',
      values: [['processing-failure-warning', '0']],
      message: '处理失败阈值须为有效正整数',
    },
    {
      name: 'processing failure count upper bound',
      values: [['processing-failure-critical', '2147483648']],
      message: '处理失败阈值须为有效正整数',
    },
    {
      name: 'processing failure count ordering',
      values: [['processing-failure-critical', '5']],
      message: '处理失败严重阈值必须高于警告阈值',
    },
    {
      name: 'login failure count lower bound',
      values: [['login-failure-warning', '0']],
      message: '登录失败阈值须为有效正整数',
    },
    {
      name: 'login failure count upper bound',
      values: [['login-failure-critical', '2147483648']],
      message: '登录失败阈值须为有效正整数',
    },
    {
      name: 'login failure count ordering',
      values: [['login-failure-critical', '20']],
      message: '登录失败严重阈值必须高于警告阈值',
    },
    {
      name: 'authorization denial count lower bound',
      values: [['authorization-denial-warning', '0']],
      message: '授权拒绝阈值须为有效正整数',
    },
    {
      name: 'authorization denial count upper bound',
      values: [['authorization-denial-critical', '2147483648']],
      message: '授权拒绝阈值须为有效正整数',
    },
    {
      name: 'authorization denial count ordering',
      values: [['authorization-denial-critical', '50']],
      message: '授权拒绝严重阈值必须高于警告阈值',
    },
  ])('rejects $name before saving', async ({ values, message }) => {
    const { wrapper } = await mountSettings()
    await flushPromises()
    for (const [testID, value] of values) {
      await wrapper.get(`[data-testid="${testID}"]`).setValue(value)
    }
    await wrapper.get('form').trigger('submit')
    expect(wrapper.get('[role="alert"]').text()).toContain(message)
    expect(saveSettings).not.toHaveBeenCalled()
  })

  it('edits and saves every newly persisted threshold group', async () => {
    const { wrapper } = await mountSettings()
    await flushPromises()
    await wrapper.get('[data-testid="backup-filesystem-warning"]').setValue('76')
    await wrapper.get('[data-testid="backup-filesystem-critical"]').setValue('91')
    await wrapper.get('[data-testid="local-backup-age-warning"]').setValue('26')
    await wrapper.get('[data-testid="local-backup-age-critical"]').setValue('31')
    await wrapper.get('[data-testid="processing-failure-warning"]').setValue('6')
    await wrapper.get('[data-testid="processing-failure-critical"]').setValue('21')
    await wrapper.get('[data-testid="login-failure-warning"]').setValue('21')
    await wrapper.get('[data-testid="login-failure-critical"]').setValue('101')
    await wrapper.get('[data-testid="authorization-denial-warning"]').setValue('51')
    await wrapper.get('[data-testid="authorization-denial-critical"]').setValue('201')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(saveSettings).toHaveBeenCalledWith(expect.objectContaining({
      backupFilesystemWarningPercent: 76,
      backupFilesystemCriticalPercent: 91,
      localBackupAgeWarningHours: 26,
      localBackupAgeCriticalHours: 31,
      processingFailureWarningCount: 6,
      processingFailureCriticalCount: 21,
      loginFailureWarningCount: 21,
      loginFailureCriticalCount: 101,
      authorizationDenialWarningCount: 51,
      authorizationDenialCriticalCount: 201,
    }))
  })

  it('warns on browser and router navigation while dirty, then clears dirty state after save', async () => {
    const { wrapper, router } = await mountSettings()
    await flushPromises()
    await wrapper.get('[data-testid="site-announcement"]').setValue('新的公告')
    const beforeUnload = new Event('beforeunload', { cancelable: true })
    window.dispatchEvent(beforeUnload)
    expect(beforeUnload.defaultPrevented).toBe(true)

    vi.stubGlobal('confirm', vi.fn().mockReturnValueOnce(false).mockReturnValueOnce(true))
    await router.push('/other')
    expect(router.currentRoute.value.path).toBe('/settings')
    await router.push('/other')
    expect(router.currentRoute.value.path).toBe('/other')

    expect(vi.mocked(confirm)).toHaveBeenCalledTimes(2)
  })

  it('clears dirty state after saving the separate editable update shape', async () => {
    const { wrapper } = await mountSettings()
    await flushPromises()
    await wrapper.get('[data-testid="site-announcement"]').setValue('保存后的公告')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(saveSettings).toHaveBeenCalledWith(expect.objectContaining({
      version: 7,
      siteAnnouncement: '保存后的公告',
    }))
    const payload = vi.mocked(saveSettings).mock.calls[0][0]
    expect(payload).not.toHaveProperty('updatedAt')
    expect(payload).not.toHaveProperty('infrastructure')
    const cleanUnload = new Event('beforeunload', { cancelable: true })
    window.dispatchEvent(cleanUnload)
    expect(cleanUnload.defaultPrevented).toBe(false)
  })

  it('does not mark editable settings dirty when a save response refreshes infrastructure status', async () => {
    vi.mocked(saveSettings).mockResolvedValueOnce({
      ...settings,
      version: 8,
      infrastructure: infrastructure.map((row) => row.key === 'remote_backup'
        ? { ...row, configured: true, lastValidatedAt: '2026-07-28T02:03:04Z' }
        : row),
    })
    const { wrapper } = await mountSettings()
    await flushPromises()
    await wrapper.get('[data-testid="site-announcement"]').setValue('刷新配置状态')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(wrapper.get('[data-testid="infrastructure-status"]').text()).toContain('远程备份已配置')
    const cleanUnload = new Event('beforeunload', { cancelable: true })
    window.dispatchEvent(cleanUnload)
    expect(cleanUnload.defaultPrevented).toBe(false)
  })

  it('locks every editable group during a slow save and preserves the submitted state', async () => {
    let resolveSave!: (value: OperationsSettings) => void
    vi.mocked(saveSettings).mockReturnValueOnce(new Promise((resolve) => { resolveSave = resolve }))
    const { wrapper } = await mountSettings()
    await flushPromises()
    const announcement = wrapper.get<HTMLInputElement>('[data-testid="site-announcement"]')
    await announcement.setValue('等待保存的公告')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(wrapper.findAll('fieldset')).toHaveLength(11)
    expect(wrapper.findAll('fieldset').every((group) => group.attributes('disabled') !== undefined)).toBe(true)
    expect(announcement.element.matches(':disabled')).toBe(true)
    expect(saveSettings).toHaveBeenCalledWith(expect.objectContaining({ siteAnnouncement: '等待保存的公告' }))

    resolveSave({ ...settings, version: 8, siteAnnouncement: '等待保存的公告' })
    await flushPromises()
    expect(wrapper.get<HTMLInputElement>('[data-testid="site-announcement"]').element.value).toBe('等待保存的公告')
    expect(wrapper.findAll('fieldset').some((group) => group.attributes('disabled') !== undefined)).toBe(false)
  })

  it('offers a conflict reload and shows the request ID without overwriting edits', async () => {
    vi.mocked(saveSettings).mockRejectedValueOnce(
      new APIError(409, 'settings_conflict', '设置已被其他管理员更新', 'req-settings-409'),
    )
    vi.mocked(readSettings)
      .mockResolvedValueOnce({ ...settings })
      .mockResolvedValueOnce({ ...settings, version: 8, siteName: '服务端新名称' })
    const { wrapper } = await mountSettings()
    await flushPromises()
    await wrapper.get('[data-testid="site-name"]').setValue('本地编辑')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('req-settings-409')
    expect(wrapper.get<HTMLInputElement>('[data-testid="site-name"]').element.value).toBe('本地编辑')
    await wrapper.get('[data-testid="reload-conflict"]').trigger('click')
    await flushPromises()
    expect(wrapper.get<HTMLInputElement>('[data-testid="site-name"]').element.value).toBe('服务端新名称')
    expect(document.activeElement).toBe(wrapper.get('#operations-settings-title').element)
  })

  it('keeps conflict reload retryable after a failed reload, then focuses the successful result', async () => {
    vi.mocked(saveSettings).mockRejectedValueOnce(
      new APIError(409, 'settings_conflict', '设置已被其他管理员更新', 'req-conflict'),
    )
    vi.mocked(readSettings)
      .mockResolvedValueOnce({ ...settings })
      .mockRejectedValueOnce(new APIError(500, 'internal_error', '重新加载失败', 'req-reload-failed'))
      .mockResolvedValueOnce({ ...settings, version: 8, siteName: '重试后的服务端名称' })
    const { wrapper } = await mountSettings()
    await flushPromises()
    await wrapper.get('[data-testid="site-name"]').setValue('需要保留的本地编辑')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    await wrapper.get('[data-testid="reload-conflict"]').trigger('click')
    await flushPromises()
    const failedAlert = wrapper.get('[role="alert"]')
    expect(failedAlert.text()).toContain('req-reload-failed')
    expect(document.activeElement).toBe(failedAlert.element)
    expect(wrapper.get<HTMLInputElement>('[data-testid="site-name"]').element.value).toBe('需要保留的本地编辑')
    expect(wrapper.get('[data-testid="reload-conflict"]').attributes('disabled')).toBeUndefined()

    await wrapper.get('[data-testid="reload-conflict"]').trigger('click')
    await flushPromises()
    expect(wrapper.get<HTMLInputElement>('[data-testid="site-name"]').element.value).toBe('重试后的服务端名称')
    expect(document.activeElement).toBe(wrapper.get('#operations-settings-title').element)
  })

  it('aborts an unfinished read on unmount and focuses retryable request-ID errors', async () => {
    let capturedSignal: AbortSignal | undefined
    vi.mocked(readSettings).mockImplementationOnce((signal) => {
      capturedSignal = signal
      return new Promise(() => {})
    })
    const { wrapper } = await mountSettings()
    wrapper.unmount()
    expect(capturedSignal?.aborted).toBe(true)

    vi.mocked(readSettings).mockRejectedValueOnce(
      new APIError(500, 'internal_error', '服务暂不可用', 'req-settings-load'),
    )
    const mounted = await mountSettings()
    await flushPromises()
    const alert = mounted.wrapper.get('[role="alert"]')
    expect(alert.text()).toContain('req-settings-load')
    expect(document.activeElement).toBe(alert.element)
  })

  it('does not expose settings when mounted for a student', async () => {
    const { wrapper } = await mountSettings('student')
    expect(wrapper.text()).toContain('无权访问系统设置')
    expect(wrapper.find('form').exists()).toBe(false)
    expect(readSettings).not.toHaveBeenCalled()
  })
})

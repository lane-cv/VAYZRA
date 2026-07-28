import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { APIError } from '../../api/client'
import { useSessionStore } from '../../stores/session'
import SystemSettingsView from './SystemSettingsView.vue'
import { readSettings, saveSettings } from './api'
import type { OperationsSettings } from './types'

vi.mock('./api', () => ({
  readSettings: vi.fn(),
  saveSettings: vi.fn(),
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
  aiErrorWarningPercent: 10,
  aiErrorCriticalPercent: 25,
  processingQueueWarning: 20,
  processingQueueCritical: 50,
  updatedAt: '2026-07-28T01:02:03Z',
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
      'AI 错误率告警',
      '处理队列告警',
    ])
    expect(wrapper.get('[data-testid="site-name"]').attributes('maxlength')).toBe('80')
    expect(wrapper.get('[data-testid="soft-delete-retention"]').attributes()).toMatchObject({ min: '30', max: '365' })
    expect(wrapper.get('[data-testid="audit-retention"]').attributes()).toMatchObject({ min: '365', max: '2555' })
    expect(wrapper.get('[data-testid="sample-retention"]').attributes()).toMatchObject({ min: '1', max: '30' })
    expect(wrapper.get('[data-testid="backup-timezone"]').attributes('readonly')).toBeDefined()
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

  it('clears dirty state after saving the complete object', async () => {
    const { wrapper } = await mountSettings()
    await flushPromises()
    await wrapper.get('[data-testid="site-announcement"]').setValue('保存后的公告')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(saveSettings).toHaveBeenCalledWith(expect.objectContaining({
      version: 7,
      siteAnnouncement: '保存后的公告',
      updatedAt: settings.updatedAt,
    }))
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

    expect(wrapper.findAll('fieldset')).toHaveLength(6)
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

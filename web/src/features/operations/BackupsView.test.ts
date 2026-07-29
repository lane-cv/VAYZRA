import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { APIError } from '../../api/client'
import { useSessionStore } from '../../stores/session'
import BackupsView from './BackupsView.vue'
import {
  listBackups,
  queueBackup,
  readBackup,
} from './api'
import type { BackupRun, BackupRunDetail } from './types'

vi.mock('./api', () => ({
  listBackups: vi.fn(),
  queueBackup: vi.fn(),
  readBackup: vi.fn(),
}))

const first: BackupRun = {
  id: '11111111-1111-4111-8111-111111111111',
  trigger: 'manual',
  state: 'degraded',
  requestedAt: '2026-07-28T01:02:03Z',
  startedAt: '2026-07-28T01:02:04Z',
  finishedAt: '2026-07-28T01:04:03Z',
  logicalBytes: 4096,
  storedBytes: 2048,
  localExpiresAt: '2026-08-04T01:04:03Z',
}
const second: BackupRun = {
  id: '22222222-2222-4222-8222-222222222222',
  trigger: 'scheduled',
  state: 'succeeded',
  requestedAt: '2026-07-27T01:02:03Z',
}
const detail: BackupRunDetail = {
  ...first,
  artifacts: [
    {
      kind: 'database_dump',
      repository: 'local',
      sizeBytes: 1024,
      verifiedAt: '2026-07-28T01:04:01Z',
      expiresAt: '2026-08-04T01:04:01Z',
    },
    {
      kind: 'manifest',
      repository: 'remote',
      sizeBytes: 512,
      verifiedAt: '2026-07-28T01:04:02Z',
      expiresAt: '2026-08-27T01:04:02Z',
    },
  ],
  restoreVerifications: [{
    id: '33333333-3333-4333-8333-333333333333',
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
  }],
}
const wrappers: Array<{ unmount(): void }> = []

function mountBackups(role: 'admin' | 'student' = 'admin') {
  const pinia = createPinia()
  useSessionStore(pinia).setUser({
    id: role === 'admin' ? 'admin-1' : 'student-1',
    username: role,
    displayName: role === 'admin' ? '张老师' : '林同学',
    role,
    mustChangePassword: false,
  })
  const wrapper = mount(BackupsView, {
    attachTo: document.body,
    global: {
      plugins: [pinia],
      stubs: {
        RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' },
      },
    },
  })
  wrappers.push(wrapper)
  return wrapper
}

describe('BackupsView', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(listBackups).mockResolvedValue({
      items: [first],
      next: { requestedAt: second.requestedAt, id: second.id },
    })
    vi.mocked(readBackup).mockResolvedValue(detail)
    vi.mocked(queueBackup).mockResolvedValue({ ...first, state: 'queued' })
    vi.stubGlobal('crypto', { randomUUID: vi.fn(() => '44444444-4444-4444-8444-444444444444') })
  })
  afterEach(() => {
    for (const wrapper of wrappers.splice(0)) wrapper.unmount()
    vi.unstubAllGlobals()
  })

  it('renders degraded recovery-point cards, appends keyset pages, and keeps no sensitive fields', async () => {
    const wrapper = mountBackups()
    expect(wrapper.get('[role="status"]').text()).toContain('正在加载')
    await flushPromises()
    expect(wrapper.get('[data-testid="backup-card"]').text()).toContain('远端副本异常')
    expect(wrapper.get('[data-testid="backup-card"]').text()).toContain('本地恢复点可用')
    vi.mocked(listBackups).mockResolvedValueOnce({ items: [second], next: null })
    await wrapper.get('[data-testid="load-more"]').trigger('click')
    await flushPromises()
    expect(wrapper.findAll('[data-testid="backup-card"]')).toHaveLength(2)
    expect(listBackups).toHaveBeenLastCalledWith({
      limit: 25,
      before: { requestedAt: second.requestedAt, id: second.id },
    }, expect.any(AbortSignal))
    expect(wrapper.text()).not.toMatch(/repositoryPath|password|sha256|objectKey/i)
  })

  it('shows local/remote artifacts and the latest restore result in detail', async () => {
    const wrapper = mountBackups()
    await flushPromises()
    await wrapper.get('[data-testid="open-detail"]').trigger('click')
    await flushPromises()
    expect(readBackup).toHaveBeenCalledWith(first.id, expect.any(AbortSignal))
    const panel = wrapper.get('[data-testid="backup-detail"]')
    expect(panel.text()).toContain('本地')
    expect(panel.text()).toContain('远端')
    expect(panel.text()).toContain('恢复演练成功')
    expect(panel.text()).toContain('120 秒')
    expect(panel.text()).toContain('会话已撤销')
    expect(panel.text()).toContain('数据库迁移版本：20')
    expect(panel.text()).toContain('固定表行数合计：3')
    expect(panel.text()).not.toMatch(/hash|路径|凭据|password/i)
  })

  it('uses a native modal with explicit focus, Escape close, and opener return', async () => {
    const wrapper = mountBackups()
    await flushPromises()
    const opener = wrapper.get('[data-testid="queue-open"]')
    await opener.trigger('click')
    await flushPromises()
    const dialog = wrapper.get('dialog[open]')
    const cancel = wrapper.get('[data-testid="queue-cancel"]')
    expect(document.activeElement).toBe(cancel.element)

    await dialog.trigger('keydown', { key: 'Escape' })
    await flushPromises()
    expect(wrapper.find('dialog').exists()).toBe(false)
    expect(document.activeElement).toBe(opener.element)

    await opener.trigger('click')
    await flushPromises()
    await wrapper.get('[data-testid="queue-cancel"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('dialog').exists()).toBe(false)
    expect(document.activeElement).toBe(opener.element)
  })

  it('uses one idempotency key through a failed manual request retry', async () => {
    vi.mocked(queueBackup)
      .mockRejectedValueOnce(new APIError(503, 'backup_unavailable', '暂不可用', 'req-queue'))
      .mockResolvedValueOnce({ ...first, state: 'queued' })
    const wrapper = mountBackups()
    await flushPromises()
    await wrapper.get('[data-testid="queue-open"]').trigger('click')
    expect(wrapper.get('[role="dialog"]').text()).toContain('短暂维护窗口')
    await wrapper.get('[data-testid="queue-confirm"]').trigger('click')
    await flushPromises()
    const queueAlert = wrapper.get('[role="alert"]')
    expect(queueAlert.text()).toContain('req-queue')
    expect(document.activeElement).toBe(queueAlert.element)
    await wrapper.get('[data-testid="queue-retry"]').trigger('click')
    await flushPromises()
    expect(queueBackup).toHaveBeenCalledTimes(2)
    expect(queueBackup).toHaveBeenNthCalledWith(1, '44444444-4444-4444-8444-444444444444')
    expect(queueBackup).toHaveBeenNthCalledWith(2, '44444444-4444-4444-8444-444444444444')
    expect(crypto.randomUUID).toHaveBeenCalledOnce()
    const notice = wrapper.get('[data-testid="queue-notice"]')
    expect(notice.text()).toContain('已加入队列')
    expect(document.activeElement).toBe(notice.element)
  })

  it('renders an explicit empty state and retryable focused load errors', async () => {
    vi.mocked(listBackups).mockResolvedValueOnce({ items: [], next: null })
    const empty = mountBackups()
    await flushPromises()
    expect(empty.text()).toContain('还没有备份记录')

    vi.mocked(listBackups).mockRejectedValueOnce(
      new APIError(503, 'backup_unavailable', '加载失败', 'req-load'),
    )
    const failed = mountBackups()
    await flushPromises()
    const alert = failed.get('[role="alert"]')
    expect(alert.text()).toContain('req-load')
    expect(document.activeElement).toBe(alert.element)
    vi.mocked(listBackups).mockResolvedValueOnce({ items: [first], next: null })
    await failed.get('[data-testid="retry-load"]').trigger('click')
    await flushPromises()
    expect(failed.find('[role="alert"]').exists()).toBe(false)
  })

  it('aborts an initial list read on unmount and denies students', async () => {
    let listSignal: AbortSignal | undefined
    vi.mocked(listBackups).mockImplementationOnce((_filter, signal) => {
      listSignal = signal
      return new Promise(() => {})
    })
    const loading = mountBackups()
    loading.unmount()
    expect(listSignal?.aborted).toBe(true)

    const student = mountBackups('student')
    expect(student.text()).toContain('无权访问备份历史')
    expect(student.find('[data-testid="queue-open"]').exists()).toBe(false)
    expect(listBackups).not.toHaveBeenCalledTimes(2)
  })

  it('aborts a pending detail read on unmount', async () => {
    let detailSignal: AbortSignal | undefined
    vi.mocked(readBackup).mockImplementationOnce((_id, signal) => {
      detailSignal = signal
      return new Promise(() => {})
    })
    const wrapper = mountBackups()
    await flushPromises()
    await wrapper.get('[data-testid="open-detail"]').trigger('click')
    expect(detailSignal?.aborted).toBe(false)
    wrapper.unmount()
    expect(detailSignal?.aborted).toBe(true)
  })

  it('keeps responsive backup records as semantic cards for the browser viewport gate', async () => {
    vi.mocked(listBackups).mockResolvedValueOnce({
      items: [first, second],
      next: null,
    })
    const wrapper = mountBackups()
    await flushPromises()
    const grid = wrapper.get('[aria-label="备份记录"]')
    expect(grid.classes()).toContain('backup-grid')
    const cards = wrapper.findAll('[data-testid="backup-card"]')
    expect(cards).toHaveLength(2)
    expect(cards.every((card) => card.element.tagName === 'ARTICLE')).toBe(true)
    expect(cards.every((card) => card.get('button').text() === '查看恢复证据')).toBe(true)
  })
})

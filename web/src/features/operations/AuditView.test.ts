import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { APIError } from '../../api/client'
import { useSessionStore } from '../../stores/session'
import AuditView from './AuditView.vue'
import { listAudit } from './api'
import type { AuditPage, AuditRecord } from './types'

vi.mock('./api', () => ({ listAudit: vi.fn() }))

const first: AuditRecord = {
  id: 50,
  actorId: '11111111-1111-4111-8111-111111111111',
  action: 'settings.updated',
  targetType: 'system_settings',
  targetId: 'global',
  metadata: { status: 'active', count: 2 },
  occurredAt: '2026-07-28T01:02:03Z',
}
const second: AuditRecord = {
  id: 49,
  actorId: '',
  action: 'backup.completed',
  targetType: 'backup',
  targetId: '',
  metadata: { status: 'succeeded' },
  occurredAt: '2026-07-28T00:02:03Z',
}

function page(items: AuditRecord[], nextBeforeId: number | null = null): AuditPage {
  return { items, nextBeforeId }
}

function mountAudit(role: 'admin' | 'student' = 'admin') {
  const pinia = createPinia()
  useSessionStore(pinia).setUser({
    id: role === 'admin' ? 'admin-1' : 'student-1',
    username: role,
    displayName: role === 'admin' ? '张老师' : '林同学',
    role,
    mustChangePassword: false,
  })
  return mount(AuditView, {
    attachTo: document.body,
    global: {
      plugins: [pinia],
      stubs: { RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' } },
    },
  })
}

describe('AuditView', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(listAudit).mockResolvedValue(page([first]))
  })

  it('submits all six filters without empty values', async () => {
    const wrapper = mountAudit()
    await flushPromises()
    await wrapper.get('[data-testid="audit-action"]').setValue('settings.updated')
    await wrapper.get('[data-testid="audit-target-type"]').setValue('system_settings')
    await wrapper.get('[data-testid="audit-outcome"]').setValue('attempted')
    await wrapper.get('[data-testid="audit-actor"]').setValue('11111111-1111-4111-8111-111111111111')
    await wrapper.get('[data-testid="audit-from"]').setValue('2026-07-01T00:00:00Z')
    await wrapper.get('[data-testid="audit-to"]').setValue('2026-07-28T00:00:00Z')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(listAudit).toHaveBeenLastCalledWith({
      action: 'settings.updated',
      targetType: 'system_settings',
      outcome: 'attempted',
      actorId: '11111111-1111-4111-8111-111111111111',
      from: '2026-07-01T00:00:00Z',
      to: '2026-07-28T00:00:00Z',
      limit: 50,
    }, expect.any(AbortSignal))
  })

  it('merges keyset pages stably without duplicates', async () => {
    vi.mocked(listAudit)
      .mockResolvedValueOnce(page([first], 49))
      .mockResolvedValueOnce(page([first, second]))
    const wrapper = mountAudit()
    await flushPromises()
    await wrapper.get('[data-testid="audit-load-more"]').trigger('click')
    await flushPromises()
    expect(wrapper.findAll('[data-testid="audit-record"]').map((item) => item.attributes('data-id'))).toEqual(['50', '49'])
    expect(listAudit).toHaveBeenLastCalledWith({ beforeId: 49, limit: 50 }, expect.any(AbortSignal))
  })

  it('ignores an old append after filters replace the result set', async () => {
    let resolveAppend!: (value: AuditPage) => void
    let resolveReplace!: (value: AuditPage) => void
    const replacement = { ...second, id: 80, action: 'audit.filtered' }
    vi.mocked(listAudit)
      .mockResolvedValueOnce(page([first], 49))
      .mockReturnValueOnce(new Promise((resolve) => { resolveAppend = resolve }))
      .mockReturnValueOnce(new Promise((resolve) => { resolveReplace = resolve }))
    const wrapper = mountAudit()
    await flushPromises()
    await wrapper.get('[data-testid="audit-load-more"]').trigger('click')
    await wrapper.get('[data-testid="audit-action"]').setValue('audit.filtered')
    await wrapper.get('form').trigger('submit')
    expect(wrapper.findAll('[data-testid="audit-record"]')).toHaveLength(0)
    resolveReplace(page([replacement]))
    await flushPromises()
    resolveAppend(page([second]))
    await flushPromises()
    expect(wrapper.findAll('[data-testid="audit-record"]').map((item) => item.attributes('data-id'))).toEqual(['80'])
  })

  it('renders only named safe metadata and never raw metadata payloads', async () => {
    const secret = 'request-body-secret'
    vi.mocked(listAudit).mockResolvedValueOnce(page([{
      ...first,
      metadata: { status: 'active', count: 2, raw_payload: secret } as AuditRecord['metadata'],
    }]))
    const wrapper = mountAudit()
    await flushPromises()
    expect(wrapper.text()).toContain('状态：active')
    expect(wrapper.text()).toContain('数量：2')
    expect(wrapper.text()).not.toContain(secret)
    expect(wrapper.text()).not.toContain('raw_payload')
  })

  it('shows request IDs and restores keyboard focus after retry', async () => {
    vi.mocked(listAudit)
      .mockRejectedValueOnce(new APIError(500, 'internal_error', '审计记录暂不可用', 'req-audit'))
      .mockResolvedValueOnce(page([]))
    const wrapper = mountAudit()
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('req-audit')
    await wrapper.get('[data-testid="audit-retry"]').trigger('click')
    await flushPromises()
    expect(document.activeElement).toBe(wrapper.get('[data-testid="audit-results-title"]').element)
    expect(wrapper.text()).toContain('暂无审计记录')
  })

  it('uses native filter, list, article, and time semantics that survive mobile layout', async () => {
    const wrapper = mountAudit()
    await flushPromises()
    expect(wrapper.get('fieldset').get('legend').text()).toBe('筛选审计记录')
    expect(wrapper.get('ol').attributes('aria-labelledby')).toBe('audit-results-title')
    expect(wrapper.find('li article').exists()).toBe(true)
    expect(wrapper.get('time').attributes('datetime')).toBe(first.occurredAt)
  })

  it('does not load or expose audit content to students', () => {
    const wrapper = mountAudit('student')
    expect(wrapper.text()).toContain('无权访问审计日志')
    expect(wrapper.find('form').exists()).toBe(false)
    expect(listAudit).not.toHaveBeenCalled()
  })
})

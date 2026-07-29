import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import AdminHomeView from './AdminHomeView.vue'

describe('AdminHomeView operations entry points', () => {
  it('links the dashboard to settings, audit, and backups without exposing sensitive detail', () => {
    const wrapper = mount(AdminHomeView, {
      global: {
        stubs: {
          RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' },
        },
      },
    })
    expect(wrapper.get('a[href="/admin/settings"]').text()).toContain('系统设置')
    expect(wrapper.get('a[href="/admin/audit"]').text()).toContain('审计日志')
    expect(wrapper.get('a[href="/admin/backups"]').text()).toContain('备份记录')
    expect(wrapper.text()).not.toContain('metadata')
    expect(wrapper.text()).not.toMatch(/password|repositoryPath|objectKey/i)
  })
})

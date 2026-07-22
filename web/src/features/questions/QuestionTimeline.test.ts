import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import QuestionTimeline from './QuestionTimeline.vue'

describe('QuestionTimeline', () => {
  it('renders hostile message bodies only as text with preserved whitespace', () => {
    const body = '<img src=x onerror=alert(1)>\nnext'
    const wrapper = mount(QuestionTimeline, { props: { messages: [{ id: 'm1', senderRole: 'student', kind: 'initial', body, createdAt: '2026-07-22T00:00:00Z', attachments: [] }] } })
    expect(wrapper.text()).toContain(body)
    expect(wrapper.find('img').exists()).toBe(false)
    expect(wrapper.get('.message-body').attributes('style')).toContain('pre-wrap')
  })

  it('sorts chronologically and offers same-origin preview only when the server marks it available', () => {
    const wrapper = mount(QuestionTimeline, { props: { messages: [
      { id: 'm2', senderRole: 'admin', kind: 'admin_reply', body: 'later', createdAt: '2026-07-22T02:00:00Z', attachments: [
        { fileVersionId: 'v/2', sortPosition: 0, displayName: 'reply.pdf', previewAvailable: true },
        { fileVersionId: 'v/3', sortPosition: 1, displayName: 'archive.zip', previewAvailable: false },
      ] },
      { id: 'm1', senderRole: 'student', kind: 'initial', body: 'earlier', createdAt: '2026-07-22T01:00:00Z', attachments: [] },
    ] } })
    expect(wrapper.findAll('article')[0].text()).toContain('earlier')
    expect(wrapper.findAll('a')).toHaveLength(3)
    expect(wrapper.get('a[aria-label="预览 reply.pdf"]').attributes()).toMatchObject({ href: '/api/v1/question-files/v%2F2/preview', target: '_blank', rel: 'noopener' })
    expect(wrapper.get('a[aria-label="下载 reply.pdf"]').attributes('href')).toBe('/api/v1/question-files/v%2F2/download')
    expect(wrapper.find('a[aria-label="预览 archive.zip"]').exists()).toBe(false)
    expect(wrapper.get('a[aria-label="下载 archive.zip"]').attributes('href')).toBe('/api/v1/question-files/v%2F3/download')
  })
  it('uses the correct speaker and bubble perspective for an admin viewer',()=>{const wrapper=mount(QuestionTimeline,{props:{viewerRole:'admin',messages:[{id:'s',senderRole:'student',kind:'initial',body:'student',createdAt:'2026-01-01',attachments:[]},{id:'a',senderRole:'admin',kind:'admin_reply',body:'admin',createdAt:'2026-01-02',attachments:[]}]}});const articles=wrapper.findAll('article');expect(articles[0].text()).toContain('学生');expect(articles[0].classes()).toContain('other');expect(articles[1].text()).toContain('我（老师）');expect(articles[1].classes()).toContain('mine')})
})

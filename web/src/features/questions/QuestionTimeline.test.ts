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

  it('sorts chronologically and builds same-origin attachment links from the version id', () => {
    const wrapper = mount(QuestionTimeline, { props: { messages: [
      { id: 'm2', senderRole: 'admin', kind: 'admin_reply', body: 'later', createdAt: '2026-07-22T02:00:00Z', attachments: [{ fileVersionId: 'v/2', sortPosition: 0, displayName: 'reply.pdf' }] },
      { id: 'm1', senderRole: 'student', kind: 'initial', body: 'earlier', createdAt: '2026-07-22T01:00:00Z', attachments: [] },
    ] } })
    expect(wrapper.findAll('article')[0].text()).toContain('earlier')
    expect(wrapper.findAll('a')[1].attributes('href')).toBe('/api/v1/question-files/v%2F2/download')
  })
})

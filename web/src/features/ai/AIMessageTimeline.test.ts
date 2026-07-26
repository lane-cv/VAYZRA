import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import AIMessageTimeline from './AIMessageTimeline.vue'

describe('AIMessageTimeline', () => {
  it('renders streaming output as inert text instead of HTML, Markdown, links, or KaTeX', () => {
    const hostile = '<img src=x onerror=alert(1)> [资料](https://evil.test) $x^2$'
    const wrapper = mount(AIMessageTimeline, { props: { messages: [], streamingText: hostile } })
    const live = wrapper.get('[data-testid="streaming-answer"]')
    expect(live.text()).toBe(hostile)
    expect(live.find('img').exists()).toBe(false)
    expect(live.find('a').exists()).toBe(false)
    expect(live.find('.katex').exists()).toBe(false)
    expect(live.element.innerHTML).not.toContain('<img')
  })
})

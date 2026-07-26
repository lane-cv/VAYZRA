import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
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

  it('announces the latest accumulated text once per throttle window and clears its timer on unmount', async () => {
    vi.useFakeTimers()
    const wrapper = mount(AIMessageTimeline, { props: { messages: [], streamingText: '一' } })
    await wrapper.setProps({ streamingText: '一二' })
    await wrapper.setProps({ streamingText: '一二三' })
    await wrapper.setProps({ streamingText: '' })
    await vi.advanceTimersByTimeAsync(500)
    expect(wrapper.get('[aria-live="polite"]').text()).toBe('一二三')
    await wrapper.setProps({ streamingText: '一二三四' })
    expect(vi.getTimerCount()).toBe(1)
    wrapper.unmount()
    expect(vi.getTimerCount()).toBe(0)
    vi.useRealTimers()
  })
})

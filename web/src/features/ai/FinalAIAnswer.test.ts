import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import FinalAIAnswer from './FinalAIAnswer.vue'

describe('FinalAIAnswer', () => {
  it('renders final Markdown and KaTeX while removing active content and unsafe attributes', () => {
    const wrapper = mount(FinalAIAnswer, {
      props: { source: '$x^2$ [安全](https://example.com) <img src=x onerror=alert(1)> [坏](javascript:alert(1))' },
    })
    expect(wrapper.find('.katex').exists()).toBe(true)
    expect(wrapper.find('a[href="https://example.com"]').attributes()).toMatchObject({
      rel: 'noopener noreferrer',
      referrerpolicy: 'no-referrer',
    })
    expect(wrapper.find('img').exists()).toBe(false)
    expect(wrapper.html().toLowerCase()).not.toContain('javascript:')
    expect(wrapper.html().toLowerCase()).not.toContain('onerror')
  })
})

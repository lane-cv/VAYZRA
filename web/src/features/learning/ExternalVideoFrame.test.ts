import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import ExternalVideoFrame from './ExternalVideoFrame.vue'

describe('ExternalVideoFrame', () => {
  it('embeds HTTPS video without same-origin authority and provides a safe fallback', () => {
    const wrapper = mount(ExternalVideoFrame, { props: { video: { id: 'v1', url: 'https://video.example/watch/1', title: '实验演示', description: '观察运动', sortKey: 10 } } })
    const frame = wrapper.get('iframe')
    expect(frame.attributes('sandbox')).toContain('allow-scripts')
    expect(frame.attributes('sandbox')).not.toContain('allow-same-origin')
    expect(frame.attributes('referrerpolicy')).toBe('no-referrer')
    expect(frame.attributes('allow')).toBe('fullscreen; picture-in-picture')
    const link = wrapper.get('a')
    expect(link.attributes('target')).toBe('_blank')
    expect(link.attributes('rel')).toContain('noopener')
  })
})

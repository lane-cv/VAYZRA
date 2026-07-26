import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import AIRunStatusCard from './AIRunStatusCard.vue'

describe('AIRunStatusCard', () => {
  it.each([
    ['queued', '等待生成'],
    ['streaming', '正在生成'],
    ['succeeded', '回答完成'],
    ['failed', '生成失败'],
    ['cancelled', '已停止'],
  ] as const)('shows the %s state without provider details', (status, label) => {
    const wrapper = mount(AIRunStatusCard, {
      props: { run: { id: 'r1', status, attemptNo: 1, lastSequence: 0, errorCode: status === 'failed' ? 'PROVIDER_UNAVAILABLE' : undefined } },
    })
    expect(wrapper.text()).toContain(label)
    expect(wrapper.text()).not.toContain('upstream')
  })

  it('only offers explicit cancellation for active runs and exposes safe support metadata', async () => {
    const wrapper = mount(AIRunStatusCard, {
      props: { run: { id: 'r1', status: 'streaming', attemptNo: 1, lastSequence: 2 }, requestId: 'req-123' },
    })
    expect(wrapper.text()).toContain('支持编号：req-123')
    await wrapper.get('button').trigger('click')
    expect(wrapper.emitted('cancel')).toHaveLength(1)
  })
})

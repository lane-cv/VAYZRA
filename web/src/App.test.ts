import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import App from './App.vue'

describe('App', () => {
  it('renders the authenticated route outlet', () => {
    const wrapper = mount(App, { global: { stubs: { RouterView: { template: '<main>HappyLearn</main>' } } } })
    expect(wrapper.text()).toContain('HappyLearn')
  })
})
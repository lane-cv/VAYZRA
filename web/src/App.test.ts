import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import App from './App.vue'

describe('App', () => {
  it('renders the product name', () => {
    expect(mount(App).text()).toContain('HappyLearn')
  })
})

/// <reference types="vite/client" />
import { mount } from '@vue/test-utils'
import { afterEach, describe, expect, it } from 'vitest'
import App from './App.vue'
import appSource from './App.vue?raw'

function contrastRatio(foreground: string, background: string): number {
  const luminance = (hex: string) => {
    const channels = hex.trim().replace('#', '').match(/.{2}/g)
    if (!channels || channels.length !== 3) throw new Error(`Expected a six-digit color, received ${hex}`)
    const [red, green, blue] = channels.map((channel) => {
      const value = Number.parseInt(channel, 16) / 255
      return value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4
    })
    return 0.2126 * red + 0.7152 * green + 0.0722 * blue
  }
  const first = luminance(foreground)
  const second = luminance(background)
  return (Math.max(first, second) + 0.05) / (Math.min(first, second) + 0.05)
}

describe('App', () => {
  afterEach(() => document.documentElement.removeAttribute('data-theme'))

  it('renders the authenticated route outlet', () => {
    const wrapper = mount(App, { global: { stubs: { RouterView: { template: '<main>HappyLearn</main>' } } } })
    expect(wrapper.text()).toContain('HappyLearn')
  })

  it('keeps dark primary actions and semantic foregrounds at WCAG AA contrast', () => {
    const darkBlock = appSource.match(/:root\[data-theme="dark"\]\s*{([^}]+)}/)?.[1] ?? ''
    const tokenValue = (name: string) => (
      darkBlock.match(new RegExp(`${name}:\\s*(#[0-9a-f]{6})`, 'i'))?.[1] ?? ''
    )
    const primary = tokenValue('--hl-primary')
    const onPrimary = tokenValue('--hl-on-primary')
    const surface = tokenValue('--hl-surface-solid')

    expect(contrastRatio(primary, '#ffffff')).toBeGreaterThanOrEqual(4.5)
    expect(onPrimary.trim()).not.toBe('')
    expect(contrastRatio(onPrimary, primary)).toBeGreaterThanOrEqual(4.5)
    for (const token of ['--hl-success', '--hl-warning', '--hl-danger']) {
      const foreground = tokenValue(token)
      expect(foreground.trim(), token).not.toBe('')
      expect(contrastRatio(foreground, surface), token).toBeGreaterThanOrEqual(4.5)
    }
  })

  it('provides a non-overridable focus-visible ring for every interactive control family', () => {
    const focusRule = appSource.match(/([^{}]+:focus-visible[^{}]*)\{\s*outline:\s*3px[^}]+}/)?.[0] ?? ''
    for (const selector of ['button', 'input', 'textarea', 'select', 'a[href]', '[tabindex]']) {
      expect(focusRule, selector).toContain(selector)
    }
    expect(focusRule).toContain('!important')
    expect(focusRule).toContain('var(--hl-focus-ring)')
  })
})

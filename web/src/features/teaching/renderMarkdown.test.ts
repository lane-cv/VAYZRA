import { describe, expect, it } from 'vitest'
import { renderMarkdown } from './renderMarkdown'

describe('renderMarkdown', () => {
  it.each([
    ['<img src=x onerror=alert(1)>', 'onerror'],
    ['[x](javascript:alert(1))', 'javascript:'],
    ['<script>alert(1)</script>', '<script'],
    ['\\href{javascript:alert(1)}{x}', 'javascript:'],
  ])('removes active content from %s', (source, forbidden) => {
    expect(renderMarkdown(source).toLowerCase()).not.toContain(forbidden)
  })

  it('renders inline and display math with KaTeX trust disabled', () => {
    const html = renderMarkdown('$x^2$\n\n$$F=ma$$')
    expect(html.match(/class="katex/g)?.length).toBeGreaterThanOrEqual(2)
  })

  it('hardens external links', () => {
    const html = renderMarkdown('[资料](https://example.com/reference)')
    expect(html).toContain('rel="noopener noreferrer"')
    expect(html).toContain('referrerpolicy="no-referrer"')
  })

  it('rejects oversized source before parsing', () => {
    expect(() => renderMarkdown('x'.repeat(200_001))).toThrow('课程正文过长')
  })
})

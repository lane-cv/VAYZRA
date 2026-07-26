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
    expect(html).not.toContain('target=')
  })

  it('uses a narrow final-answer whitelist without images or unsafe external attributes', () => {
    const html = renderMarkdown('<img src="https://example.com/x" style="display:none" onerror="alert(1)">\n\n[资料](https://example.com)')
    expect(html).not.toContain('<img')
    expect(html).not.toContain('style=')
    expect(html).not.toContain('onerror')
  })

  it('rejects oversized source before parsing', () => {
    expect(() => renderMarkdown('x'.repeat(200_001))).toThrow('课程正文过长')
  })
})

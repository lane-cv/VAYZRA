import DOMPurify from 'dompurify'
import katex from 'katex'
import MarkdownIt from 'markdown-it'

const MAX_SOURCE_LENGTH = 200_000
const unsafeLatexLink = /\\(?:href|url)\s*\{[^{}]*\}/giu
const latexDimension = /(-?(?:\d+(?:\.\d+)?|\.\d+))(em|ex|px|pt|mu|cm|mm|in)\b/giu
const rawHTMLTag = /<\/?[A-Za-z!][^>]*>/gu
const unsafeProtocol = /\b(?:javascript|vbscript|data)\s*:/giu

const markdown = new MarkdownIt({ html: false, linkify: true, breaks: true })

markdown.inline.ruler.before('escape', 'math_inline', (state, silent) => {
  if (state.src[state.pos] !== '$' || state.src[state.pos + 1] === '$') return false
  const end = state.src.indexOf('$', state.pos + 1)
  if (end < 0) return false
  if (!silent) {
    const token = state.push('math_inline', '', 0)
    token.content = state.src.slice(state.pos + 1, end)
  }
  state.pos = end + 1
  return true
})

markdown.block.ruler.before('fence', 'math_block', (state, startLine, _endLine, silent) => {
  const start = state.bMarks[startLine] + state.tShift[startLine]
  const max = state.eMarks[startLine]
  const firstLine = state.src.slice(start, max)
  if (!firstLine.startsWith('$$')) return false
  let content = firstLine.slice(2)
  let nextLine = startLine
  if (content.endsWith('$$')) content = content.slice(0, -2)
  else {
    let closed = false
    for (nextLine = startLine + 1; nextLine < state.lineMax; nextLine += 1) {
      const lineStart = state.bMarks[nextLine] + state.tShift[nextLine]
      const line = state.src.slice(lineStart, state.eMarks[nextLine])
      if (line.endsWith('$$')) { content += `\n${line.slice(0, -2)}`; closed = true; break }
      content += `\n${line}`
    }
    if (!closed) return false
  }
  if (silent) return true
  const token = state.push('math_block', '', 0)
  token.block = true
  token.content = content.trim()
  token.map = [startLine, nextLine + 1]
  state.line = nextLine + 1
  return true
})

markdown.renderer.rules.math_inline = (tokens, index) => renderMath(tokens[index].content, false)
markdown.renderer.rules.math_block = (tokens, index) => `<div class="math-block">${renderMath(tokens[index].content, true)}</div>`

function renderMath(source: string, displayMode: boolean): string {
  const safeSource = source
    .replace(unsafeLatexLink, '\\text{blocked link}')
    .replace(latexDimension, (original, amount: string, unit: string) =>
      Math.abs(Number(amount)) <= 100 ? original : `0${unit}`)
  return katex.renderToString(safeSource, {
    displayMode, throwOnError: false, trust: false, strict: 'error', maxSize: 100,
  })
}

const katexStyleProperties = new Set([
  'height', 'vertical-align', 'top', 'margin-right', 'margin-left',
  'border-bottom-width', 'width', 'min-width',
])
const katexLength = /^(-?(?:\d+(?:\.\d+)?|\.\d+))(?:em|ex|px|%)?$/u

function hardenOutput(html: string): string {
  const template = document.createElement('template')
  template.innerHTML = html
  for (const link of template.content.querySelectorAll('a[href]')) {
    const href = link.getAttribute('href') ?? ''
    if (!/^https?:\/\//iu.test(href)) continue
    link.setAttribute('rel', 'noopener noreferrer')
    link.setAttribute('referrerpolicy', 'no-referrer')
  }
  for (const element of template.content.querySelectorAll<HTMLElement>('[style]')) {
    if (!element.closest('.katex') || !safeKatexStyle(element.getAttribute('style') ?? '')) {
      element.removeAttribute('style')
    }
  }
  return template.innerHTML
}

function safeKatexStyle(style: string): boolean {
  const declarations = style.split(';').filter(Boolean)
  return declarations.length > 0 && declarations.every((declaration) => {
    const separator = declaration.indexOf(':')
    if (separator <= 0) return false
    const property = declaration.slice(0, separator).trim().toLowerCase()
    const value = declaration.slice(separator + 1).trim().toLowerCase()
    const length = katexLength.exec(value)
    return katexStyleProperties.has(property) && length !== null && Math.abs(Number(length[1])) <= 100
  })
}

export function renderMarkdown(source: string): string {
  if (source.length > MAX_SOURCE_LENGTH) throw new Error('课程正文过长')
  const normalized = source.replace(rawHTMLTag, '').replace(unsafeProtocol, '').replace(unsafeLatexLink, '\\text{blocked link}')
  const rendered = markdown.render(normalized)
  const sanitized = DOMPurify.sanitize(rendered, {
    USE_PROFILES: { html: true, mathMl: true, svg: true },
    ADD_ATTR: ['aria-hidden', 'referrerpolicy'],
    FORBID_TAGS: ['form', 'iframe', 'object', 'embed', 'style', 'img', 'audio', 'video', 'source'],
    FORBID_ATTR: ['target', 'src', 'srcset', 'onerror', 'onclick', 'onload'],
  })
  return hardenOutput(sanitized)
}

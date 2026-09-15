// @vitest-environment jsdom
import { readFileSync, readdirSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { createMeta, updateMeta } from './meta'

const dirname =
  (import.meta as { dirname?: string }).dirname ?? resolve(new URL('.', import.meta.url).pathname)
const CSS = resolve(dirname, '../styles/components/meta.css')
const THEMES = resolve(dirname, '../styles/themes')

function token(themeText: string, name: string): string {
  const match = themeText.match(new RegExp(`--${name}\\s*:\\s*([^;]+);`))
  if (!match) throw new Error(`no --${name} in theme`)
  return match[1].trim()
}

function luminance(hex: string): number {
  const channels = hex
    .replace('#', '')
    .match(/../g)!
    .map((c) => parseInt(c, 16) / 255)
    .map((c) => (c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4))
  return 0.2126 * channels[0] + 0.7152 * channels[1] + 0.0722 * channels[2]
}

function contrast(a: string, b: string): number {
  const [x, y] = [luminance(a), luminance(b)]
  const [hi, lo] = x > y ? [x, y] : [y, x]
  return (hi + 0.05) / (lo + 0.05)
}

/** The declaration block of the first rule whose selector is exactly `selector`. */
function ruleFor(cssText: string, selector: string): string {
  // Strip comments first: meta.css documents nearly every rule with one
  // directly above it (the file-header comment above .ui-meta, the "safety
  // question" comment above .ui-meta__part[data-emphasis='strong']), and a
  // comment immediately preceding a rule has no '{' of its own to split on,
  // so it merges into that rule's "head" and the selector never matches
  // exactly.
  const withoutComments = cssText.replace(/\/\*[\s\S]*?\*\//g, '')
  for (const block of withoutComments.split('}')) {
    const [head, body] = block.split('{')
    if (
      head
        ?.trim()
        .split(/\s*,\s*/)
        .includes(selector) &&
      body
    )
      return body
  }
  throw new Error(`no rule for ${selector}`)
}

describe('createMeta — the DOM contract', () => {
  it('renders one ui-meta with a part per fact and a hidden separator between them', () => {
    const el = createMeta(['dev@staging', 'repos/nocx'])
    expect(el.tagName).toBe('SPAN')
    expect(el.className).toBe('ui-meta')
    expect(el.dataset.tone).toBe('muted')
    const parts = el.querySelectorAll('.ui-meta__part')
    expect(Array.from(parts).map((p) => p.textContent)).toEqual(['dev@staging', 'repos/nocx'])
    const seps = el.querySelectorAll('.ui-meta__sep')
    expect(seps).toHaveLength(1)
    expect(seps[0].getAttribute('aria-hidden')).toBe('true')
    expect(el.textContent).toBe('dev@staging · repos/nocx')
  })

  it('a single part carries no separator', () => {
    const el = createMeta(['repos/nocx'])
    expect(el.querySelectorAll('.ui-meta__sep')).toHaveLength(0)
  })

  it.each(['muted', 'dim', 'danger', 'accent'] as const)('states tone %s on data-tone', (tone) => {
    expect(createMeta(['x'], { tone }).dataset.tone).toBe(tone)
  })

  it('marks a strong part, and only that part', () => {
    const el = createMeta([{ text: 'dev@staging', emphasis: 'strong' }, 'repos/nocx'])
    const [host, cwd] = Array.from(el.querySelectorAll<HTMLElement>('.ui-meta__part'))
    expect(host.dataset.emphasis).toBe('strong')
    expect(cwd.hasAttribute('data-emphasis')).toBe(false)
  })

  it('carries the duration column and the title when asked, and neither when not', () => {
    const col = createMeta(['84ms'], { column: 'duration', title: 'Started 19:32:23' })
    expect(col.dataset.column).toBe('duration')
    expect(col.title).toBe('Started 19:32:23')
    const plain = createMeta(['84ms'])
    expect(plain.hasAttribute('data-column')).toBe(false)
    expect(plain.hasAttribute('title')).toBe(false)
  })

  it('carries the sm size when asked, and not otherwise (spec 2026-09-15 §4)', () => {
    const sm = createMeta(['Exit 1'], { size: 'sm' })
    expect(sm.dataset.size).toBe('sm')
    const plain = createMeta(['Exit 1'])
    expect(plain.hasAttribute('data-size')).toBe(false)
  })
})

describe('updateMeta — the same element, restated', () => {
  it('replaces the parts and every option, removing ones no longer asked for', () => {
    const el = createMeta(['3s'], { tone: 'accent', column: 'duration', title: 'running' })
    updateMeta(el, ['exit 1'], { tone: 'danger' })
    expect(el.textContent).toBe('exit 1')
    expect(el.dataset.tone).toBe('danger')
    expect(el.hasAttribute('data-column')).toBe(false)
    expect(el.hasAttribute('title')).toBe(false)
    expect(el.className).toBe('ui-meta')
  })
})

describe('meta.css — tokens only, and legible where the terminal screen puts it', () => {
  const css = readFileSync(CSS, 'utf8')
  const themes = readdirSync(THEMES).filter((f) => f.endsWith('.css'))

  it('one line, the small register, tabular figures', () => {
    const base = ruleFor(css, '.ui-meta')
    expect(base).toContain('font-size: var(--font-size-2xs)')
    expect(base).toContain('font-variant-numeric: tabular-nums')
    expect(base).toContain('white-space: nowrap')
    expect(base).toContain('text-overflow: ellipsis')
  })

  it.each([
    ['muted', 'color-text-muted'],
    ['dim', 'color-text-dim'],
    ['danger', 'color-danger'],
    ['accent', 'color-accent'],
  ])('tone %s paints --%s', (tone, tok) => {
    expect(ruleFor(css, `.ui-meta[data-tone='${tone}']`)).toContain(`color: var(--${tok})`)
  })

  it('a strong part reads at normal text colour', () => {
    expect(ruleFor(css, ".ui-meta__part[data-emphasis='strong']")).toContain(
      'color: var(--color-text)',
    )
  })

  it('the sm size reads in the mono face at --font-size-sm (spec 2026-09-15 §4)', () => {
    const sm = ruleFor(css, ".ui-meta[data-size='sm']")
    expect(sm).toContain('font-family: var(--font-family-mono)')
    expect(sm).toContain('font-size: var(--font-size-sm)')
  })

  it.each(themes)('%s: muted and dim reach 4.5:1 on the terminal ground', (file) => {
    const text = readFileSync(resolve(THEMES, file), 'utf8')
    const ground = token(text, 'terminal-background')
    expect(contrast(token(text, 'color-text-muted'), ground)).toBeGreaterThanOrEqual(4.5)
    expect(contrast(token(text, 'color-text-dim'), ground)).toBeGreaterThanOrEqual(4.5)
  })

  it.each(['tokyo-night.css', 'light.css'])(
    '%s: danger and accent reach 4.5:1 on the terminal ground',
    (file) => {
      const text = readFileSync(resolve(THEMES, file), 'utf8')
      const ground = token(text, 'terminal-background')
      expect(contrast(token(text, 'color-danger'), ground)).toBeGreaterThanOrEqual(4.5)
      expect(contrast(token(text, 'color-accent'), ground)).toBeGreaterThanOrEqual(4.5)
    },
  )
})

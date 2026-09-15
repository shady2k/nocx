// @vitest-environment jsdom
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { createPromptContext, updatePromptContext } from './prompt-context'

const dirname =
  (import.meta as { dirname?: string }).dirname ?? resolve(new URL('.', import.meta.url).pathname)
const CSS = resolve(dirname, '../styles/components/prompt-context.css')

/** The declaration block of the first rule whose selector is exactly `selector`. */
function ruleFor(cssText: string, selector: string): string {
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

describe('createPromptContext — the DOM contract', () => {
  it('renders the path alone when there is no host and no branch', () => {
    const el = createPromptContext({ path: '~/repos/nocx' })
    expect(el.tagName).toBe('SPAN')
    expect(el.className).toBe('ui-prompt-context')
    expect(el.dataset.tone).toBe('normal')
    expect(el.querySelector(':scope > [data-part="host"]')).toBeNull()
    expect(el.querySelector(':scope > .ui-prompt-context__colon')).toBeNull()
    const path = el.querySelector<HTMLElement>(':scope > [data-part="path"]')
    expect(path?.textContent).toBe('~/repos/nocx')
    expect(el.textContent).toBe('~/repos/nocx')
  })

  it('a host reads muted, then a hidden colon, then the path — one element with one where', () => {
    const el = createPromptContext({ host: 'dev@staging', path: '~/repos/nocx' })
    const host = el.querySelector<HTMLElement>(':scope > [data-part="host"]')
    const colon = el.querySelector<HTMLElement>(':scope > .ui-prompt-context__colon')
    expect(host?.textContent).toBe('dev@staging')
    expect(host?.hasAttribute('data-emphasis')).toBe(false)
    expect(colon?.textContent).toBe(':')
    expect(colon?.getAttribute('aria-hidden')).toBe('true')
    expect(el.textContent).toBe('dev@staging:~/repos/nocx')
  })

  it('hostStrong marks the host for normal text colour — the composer’s safety question', () => {
    const el = createPromptContext({ host: 'dev@staging', hostStrong: true, path: '~' })
    const host = el.querySelector<HTMLElement>(':scope > [data-part="host"]')
    expect(host?.dataset.emphasis).toBe('strong')
  })

  it('a branch adds "on", the git-branch glyph and the name, after the path', () => {
    const el = createPromptContext({ path: '~/repos/nocx', branch: 'main' })
    const [pathPart, onPart, icon, branchPart] = Array.from(el.children)
    expect(pathPart.getAttribute('data-part')).toBe('path')
    expect(onPart.getAttribute('data-part')).toBe('on')
    expect(onPart.textContent).toBe('on')
    expect(icon.tagName.toLowerCase()).toBe('svg')
    expect(icon.getAttribute('aria-hidden')).toBe('true')
    expect(branchPart.getAttribute('data-part')).toBe('branch')
    expect(branchPart.textContent).toBe('main')
    expect(el.textContent).toBe('~/repos/nocxonmain')
  })

  it('no branch known draws no "on", no glyph and no branch part', () => {
    const el = createPromptContext({ path: '~' })
    expect(el.querySelector('[data-part="on"]')).toBeNull()
    expect(el.querySelector('svg')).toBeNull()
    expect(el.querySelector('[data-part="branch"]')).toBeNull()
  })

  it('dim tone is stated on the root, for the composer while unfocused', () => {
    expect(createPromptContext({ path: '~' }, { tone: 'dim' }).dataset.tone).toBe('dim')
    expect(createPromptContext({ path: '~' }, { tone: 'normal' }).dataset.tone).toBe('normal')
  })
})

describe('updatePromptContext — the same element, restated in place', () => {
  it('replaces every fact and every option, dropping ones no longer given', () => {
    const el = createPromptContext(
      { host: 'dev@staging', hostStrong: true, path: '~/a', branch: 'main' },
      { tone: 'dim' },
    )
    updatePromptContext(el, { path: '/srv/other' }, { tone: 'normal' })
    expect(el.textContent).toBe('/srv/other')
    expect(el.dataset.tone).toBe('normal')
    expect(el.querySelector('[data-part="host"]')).toBeNull()
    expect(el.querySelector('[data-part="branch"]')).toBeNull()
    expect(el.querySelector('svg')).toBeNull()
    expect(el.className).toBe('ui-prompt-context')
  })
})

describe('prompt-context.css — tokens only, mono, one line', () => {
  const css = readFileSync(CSS, 'utf8')

  it('one line, ellipsis, the mono face at the command row’s own size (round 18)', () => {
    const base = ruleFor(css, '.ui-prompt-context')
    expect(base).toContain('font-family: var(--font-family-mono)')
    // --font-size-terminal, not --font-size-sm: the mockups draw the prompt
    // line and the command line as one register (round 18, mockup pass) —
    // --font-size-sm read as "a tiny accent ~" against the terminal's own
    // 14px in the owner's screenshot.
    expect(base).toContain('font-size: var(--font-size-terminal)')
    expect(base).toContain('white-space: nowrap')
    expect(base).toContain('text-overflow: ellipsis')
  })

  it('host is muted, and strong reads at normal text colour', () => {
    expect(ruleFor(css, ".ui-prompt-context__part[data-part='host']")).toContain(
      'color: var(--color-text-muted)',
    )
    expect(
      ruleFor(css, ".ui-prompt-context__part[data-part='host'][data-emphasis='strong']"),
    ).toContain('color: var(--color-text)')
  })

  it('the path and the branch read in the accent colour', () => {
    expect(ruleFor(css, ".ui-prompt-context__part[data-part='path']")).toContain(
      'color: var(--color-accent)',
    )
    expect(ruleFor(css, ".ui-prompt-context__part[data-part='branch']")).toContain(
      'color: var(--color-accent)',
    )
  })

  it('the colon and "on" are muted, like the host', () => {
    expect(ruleFor(css, '.ui-prompt-context__colon')).toContain('color: var(--color-text-muted)')
    expect(ruleFor(css, ".ui-prompt-context__part[data-part='on']")).toContain(
      'color: var(--color-text-muted)',
    )
  })

  it('"on" and the glyph carry their own gaps — the parts are bare adjacent spans with no whitespace text node between them (round 18)', () => {
    // Found by reading a screenshot, not by textContent: with the branch
    // known the rendered line read "~/repoonmain" with no visible space
    // around "on" or the glyph, because DOM-adjacency contributes nothing
    // and no whitespace text node sits between the parts (fill(),
    // prompt-context.ts). A textContent assertion cannot see this — an SVG
    // icon contributes no text either — so this checks the CSS gap exists.
    expect(ruleFor(css, ".ui-prompt-context__part[data-part='on']")).toContain('margin-inline')
    expect(ruleFor(css, '.ui-prompt-context svg')).toContain('margin-right')
  })

  it('the branch glyph is sized off the icon-size token, not a literal', () => {
    expect(ruleFor(css, '.ui-prompt-context svg')).toContain('width: var(--icon-size-sm)')
  })

  it('dim steps the whole line down without a second colour per part', () => {
    expect(ruleFor(css, ".ui-prompt-context[data-tone='dim']")).toContain('opacity')
  })
})

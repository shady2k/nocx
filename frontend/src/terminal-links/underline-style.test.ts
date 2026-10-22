// The armed link's LOOK is a stylesheet fact (nocx-1ebrs.48), and jsdom
// computes neither cursors nor text-decoration — so this pins the shipped
// cascade the way painter/pointer-shape.test.ts does: read the stylesheet,
// find the rule, assert the declaration.
//
// Two halves, one behaviour. The LIVE grid's underline is the painter's
// `.term-grid-link` overlay and the hand is the link tracker's
// `data-link-hover` attribute; the FROZEN scrollback's underline is the
// armed `.term-link` anchor. The owner's wording is "dotted while armed",
// so both halves must declare dotted — a solid rule on either surface is
// the two halves of one terminal disagreeing about one row as it scrolls
// from the live grid into a frozen block.
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { LINK_HOVER_ATTR } from './live-hover'

/** Every top-level rule of one stylesheet, comments stripped. */
function rulesOf(path: string): Array<{ selector: string; body: string }> {
  const source = readFileSync(resolve(import.meta.dirname ?? '.', path), 'utf8').replace(
    /\/\*[\s\S]*?\*\//g,
    '',
  )
  const out: Array<{ selector: string; body: string }> = []
  for (const match of source.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
    out.push({ selector: match[1].trim(), body: match[2] })
  }
  return out
}

const style = (): string => resolve(import.meta.dirname ?? '.', '..', 'style.css')
const declaration = (body: string, prop: string): string | null => {
  const declared = new RegExp(`(?:^|;)\\s*${prop}\\s*:\\s*([^;]+)`).exec(body)
  return declared ? declared[1].trim() : null
}

describe('the live grid\u2019s armed-link cascade (nocx-1ebrs.48)', () => {
  it('draws the overlay\u2019s line dotted, as chrome above the rows', () => {
    const rule = rulesOf(style()).find((r) => r.selector === '.term-grid-link')
    expect(rule).toBeDefined()
    const border = declaration(rule!.body, 'border-bottom')
    expect(border).toContain('dotted')
    expect(declaration(rule!.body, 'position')).toBe('absolute')
    // The overlay must never take the click it advertises: the click belongs
    // to the surface's own armed handler.
    expect(declaration(rule!.body, 'pointer-events')).toBe('none')
  })

  it('shows the hand while an armed link is under the pointer, after the pointer\u2019s own rules', () => {
    const selector = `.term-grid[${LINK_HOVER_ATTR}='true']`
    const all = rulesOf(style())
    const rule = all.find((r) => r.selector === selector)
    expect(rule).toBeDefined()
    expect(declaration(rule!.body, 'cursor')).toBe('pointer')
    // Equal specificity with `.term-grid[data-pointer='program']` decides by
    // source order, so the hand rule has to come after it — an armed link
    // under a mouse-reporting program still opens on ⌘+click.
    const program = all.findIndex((r) => r.selector === ".term-grid[data-pointer='program']")
    expect(all.findIndex((r) => r.selector === selector)).toBeGreaterThan(program)
  })
})

describe('the frozen scrollback\u2019s armed-link cascade (nocx-1ebrs.48)', () => {
  it('underlines the armed anchor dotted, matching the live grid', () => {
    const css = rulesOf(
      resolve(import.meta.dirname ?? '.', '..', 'styles', 'components', 'terminal-link.css'),
    )
    const rule = css.find((r) => r.selector === '.links-armed .term-link')
    expect(rule).toBeDefined()
    expect(declaration(rule!.body, 'text-decoration-style')).toBe('dotted')
  })
})

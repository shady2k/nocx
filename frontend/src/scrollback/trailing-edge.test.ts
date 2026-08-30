// THE SCROLLBAR IS THE PANE'S TRAILING EDGE, NOT AN OBJECT STANDING NEAR IT
// (nocx-mvbne).
//
// The activity bar sits on the window's trailing edge now. `.pane` carries
// `--pane-inline-padding: 10px`, so the scroller — and with it the stable
// gutter the bar is drawn in — used to stop 10px short of the pane's border
// edge. That put canvas on BOTH sides of the thumb: content, gutter, thumb,
// 10px of canvas, the rail's divider. A bar with a margin either side reads
// as a free-standing stripe, which is what the eye kept catching on; a bar
// flush against the surface it borders reads as that surface's edge and
// disappears. Orca is the reference — same bar, no gap, quiet.
//
// This is geometry, not colour. Tinting the thumb down to `--color-divider`
// was tried on paper first and rejected: in tokyo-night that is #2a2b3d
// against a #2b3049 thumb, so the tint moves nearly nothing while the
// floating stays.
//
// The three facts below are one contract, and the middle one is why the fix
// is on `.scrollback-layout` rather than on the scroller itself:
//
//   1. the layout cancels the pane's trailing padding, so the gutter ends on
//      the pane's border edge;
//   2. `.scrollback-area` carries NO padding of its own — `usableViewport`
//      reads its `clientWidth` as the grid width (terminal-content.ts, the
//      invariant nocx-vydj bought), and padding counts in `clientWidth`, so a
//      padded scroller fits a grid 10px wider than the box it is drawn in and
//      the last columns are cut mid-glyph by `.xterm-inner`'s overflow;
//   3. nothing INSIDE the scroller re-inserts a trailing inset, because the
//      live region and the frozen blocks share that content box and a grid
//      fitted to `clientWidth` must be able to fill it.
//
// jsdom computes no cascade, so this reads the shipped stylesheets the way
// `cmd-output-wrap.test.ts` does.
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

type Rule = { selectors: string[]; body: string }

const HERE = import.meta.dirname ?? '.'
const STYLE_ENTRY = resolve(HERE, '..', 'style.css')
const BASE_ENTRY = resolve(HERE, '..', 'styles/base.css')

/** Top-level rules only, comments stripped. An at-rule block is skipped
 *  whole: a declaration that only holds at some viewport width does not
 *  hold. Lifted from cmd-output-wrap.test.ts. */
function topLevelRules(css: string): Rule[] {
  const rules: Rule[] = []
  const source = css.replace(/\/\*[\s\S]*?\*\//g, '')
  let depth = 0
  let head = ''
  let body = ''
  for (const ch of source) {
    if (ch === '{') {
      depth++
      if (depth === 1) {
        body = ''
        continue
      }
    } else if (ch === '}') {
      depth--
      if (depth === 0) {
        const selector = head.trim()
        if (!selector.startsWith('@')) {
          rules.push({ selectors: selector.split(',').map((s) => s.trim()), body })
        }
        head = ''
        continue
      }
    }
    if (depth === 0) head += ch
    else body += ch
  }
  return rules
}

const RULES: Rule[] = [
  ...topLevelRules(readFileSync(BASE_ENTRY, 'utf8')),
  ...topLevelRules(readFileSync(STYLE_ENTRY, 'utf8')),
]

/** Every declaration the shipped cascade gives a bare class, later rules
 *  winning — the order the browser resolves them in. */
function shippedValue(className: string, property: string): string | null {
  let found: string | null = null
  const pattern = new RegExp(`(?:^|;)\\s*${property}\\s*:\\s*([^;]+)`)
  for (const rule of RULES) {
    if (!rule.selectors.includes(`.${className}`)) continue
    const m = rule.body.match(pattern)
    if (m) found = m[1].trim()
  }
  return found
}

describe('the scrollback scrollbar lands on the pane edge (nocx-mvbne)', () => {
  it('the pane still states its inline breathing room as one token', () => {
    // The cancellation below is written against this token rather than
    // against a second copy of the number: two 10s that must agree is the
    // dead twin the pane's own comment warns about.
    expect(shippedValue('pane', '--pane-inline-padding')).toBe('10px')
    expect(shippedValue('pane', 'padding')).toBe('0 var(--pane-inline-padding)')
  })

  it('the scrollback layout cancels that padding on the trailing side only', () => {
    // Trailing side only: the LEADING inset is what keeps the first column
    // off the pane's edge, and nothing about the scrollbar asks for it back.
    const marginRight = shippedValue('scrollback-layout', 'margin-right')
    expect(marginRight).toBe('calc(-1 * var(--pane-inline-padding))')
    expect(shippedValue('scrollback-layout', 'margin-left')).toBeNull()
  })

  it('the scroller takes no padding, because its clientWidth is the grid width', () => {
    for (const property of ['padding', 'padding-right', 'padding-inline', 'padding-inline-end']) {
      expect(shippedValue('scrollback-area', property)).toBeNull()
    }
  })

  it('the gutter stays reserved, so the trailing edge never moves', () => {
    // Without this the bar's arrival and departure would move the very edge
    // the fix aligns to, and `refitIfResized` would see a width that
    // alternates with the row count.
    expect(shippedValue('scrollback-area', 'scrollbar-gutter')).toBe('stable')
  })

  it('nothing inside the scroller re-inserts a trailing inset', () => {
    // The live region is a descendant of the scroller, not a sibling of it
    // (scrollback/controller.ts builds `.scrollback-area > .scrollback-inner
    // > .xterm-live-container`), so blocks and the running grid share one
    // content box. An inset on either would put the frozen column and the
    // live column on different right edges.
    for (const className of ['scrollback-inner', 'xterm-live-container']) {
      for (const property of ['padding-right', 'margin-right', 'padding-inline-end']) {
        expect(shippedValue(className, property)).toBeNull()
      }
    }
  })

  it('the composer keeps its breathing room on both sides', () => {
    // The summon stack is absolutely positioned against the pane, so it is
    // unaffected by the layout's margin — and it must stay that way: a
    // composer flush against the rail is a text field with no edge.
    const stack = RULES.find((r) => r.selectors.includes('.nocx-summon-stack'))
    expect(stack).toBeDefined()
    expect(stack!.body).toMatch(/left\s*:\s*var\(--pane-inline-padding\)/)
    expect(stack!.body).toMatch(/right\s*:\s*var\(--pane-inline-padding\)/)
  })
})

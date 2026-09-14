// THE SCROLLBAR IS THE PANE'S TRAILING EDGE, NOT AN OBJECT STANDING NEAR IT
// (nocx-mvbne).
//
// The activity bar sits on the window's trailing edge now. `.pane` used to
// carry `--pane-inline-padding` as its own inline padding, so the scroller —
// and with it the stable gutter the bar is drawn in — stopped short of the
// pane's border edge, cancelled back to flush with a negative margin on
// `.scrollback-layout`. Rows now carry the gutter instead (nocx-9bpeq.8), so
// the scrollbar is flush for a simpler reason: the pane pads nothing, the
// scroller pads nothing, and there is nothing left to cancel.
//
// This is geometry, not colour. Tinting the thumb down to `--color-divider`
// was tried on paper first and rejected: in tokyo-night that is #2a2b3d
// against a #2b3049 thumb, so the tint moves nearly nothing while the
// floating stays.
//
// The contract is now three facts, and the fourth is the fit that depends on
// them:
//
//   1. the pane insets nothing — the gutter it used to own moved into the
//      rows, so there is no padding left for `.scrollback-layout` to cancel;
//   2. `.scrollback-area` carries NO padding of its own — its `clientWidth`
//      is the ROW width, not the grid width, now that a row insets itself;
//   3. every child of `.scrollback-inner` (a block, the separator, the
//      restore boundary, the live region) states the same
//      `padding-inline: var(--pane-inline-padding)` as a border-box, so the
//      frozen column and the live column share one content box; a block
//      nested in a turn is not a child of the stack and gets none;
//   4. `usableViewport` (terminal-content.ts) fits the grid to that content
//      box — the scroller's `clientWidth` minus the live row's own computed
//      inline padding — which is what keeps the grid from being `2 ×
//      gutter` wider than the box it is drawn in (the nocx-vydj defect,
//      returned if this drifts).
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
const TOKENS_ENTRY = resolve(HERE, '..', 'styles/tokens.css')
const COMPOSER_ENTRY = resolve(HERE, '..', 'styles/surfaces/composer.css')

/** Top-level rules only, comments stripped. An at-rule block is skipped
 *  whole. Lifted from cmd-output-wrap.test.ts. */
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
  ...topLevelRules(readFileSync(TOKENS_ENTRY, 'utf8')),
  ...topLevelRules(readFileSync(BASE_ENTRY, 'utf8')),
  ...topLevelRules(readFileSync(STYLE_ENTRY, 'utf8')),
  // The composer's own rules moved out of style.css (nocx-9bpeq.7); the
  // gutter this suite is about moved with them.
  ...topLevelRules(readFileSync(COMPOSER_ENTRY, 'utf8')),
]

/** Every declaration the shipped cascade gives `selector` exactly, later
 *  rules winning. */
function shipped(selector: string, property: string): string | null {
  let found: string | null = null
  const pattern = new RegExp(`(?:^|;)\\s*${property}\\s*:\\s*([^;]+)`)
  for (const rule of RULES) {
    if (!rule.selectors.includes(selector)) continue
    const m = rule.body.match(pattern)
    if (m) found = m[1].trim()
  }
  return found
}

const INLINE = [
  'padding',
  'padding-inline',
  'padding-left',
  'padding-right',
  'padding-inline-start',
  'padding-inline-end',
]

describe('rows are full width, and the gutter lives in them (nocx-9bpeq.8)', () => {
  it('the gutter is one token on the spacing scale', () => {
    expect(shipped(':root', '--pane-inline-padding')).toBe('var(--space-4)')
  })

  it('the pane insets nothing, so the scroller and its scrollbar reach the pane edge', () => {
    for (const property of INLINE) expect(shipped('.pane', property)).toBeNull()
    // Nothing is left to cancel, so no cancellation may survive: a negative
    // margin with no padding to pay for it pushes the scroller past the pane.
    expect(shipped('.scrollback-layout', 'margin-right')).toBeNull()
    expect(shipped('.scrollback-layout', 'margin-left')).toBeNull()
  })

  it('the scroller takes no padding, because its clientWidth is where the fit starts', () => {
    for (const property of INLINE) expect(shipped('.scrollback-area', property)).toBeNull()
    expect(shipped('.scrollback-area', 'scrollbar-gutter')).toBe('stable')
  })

  it('every row of the ledger carries the same inset, as a border-box', () => {
    // One rule for every child of the stack — blocks, separator, restore
    // boundary and the live region — so a frozen column and the live column
    // cannot sit on different edges, and a block nested in a turn (not a
    // child of the stack) gets none.
    expect(shipped('.scrollback-inner > *', 'padding-inline')).toBe('var(--pane-inline-padding)')
    expect(shipped('.scrollback-inner > *', 'box-sizing')).toBe('border-box')
    // Every one of these is a direct child of `.scrollback-inner` at equal
    // specificity to that row rule (one class each): `.cmd-block` (every
    // kind), the live region and `.scrollback-restore-boundary` ("Previous
    // session" / "New shell"). A `padding`/`padding-inline`/`padding-left`/
    // `padding-right` declared on any of them, later in the cascade, wins
    // outright and resets the row's inline inset to 0 — the defect a review
    // found on `.scrollback-restore-boundary` (nocx-9bpeq.8): its own
    // `padding: var(--space-2) 0` shorthand pulled the boundary text back to
    // the pane's edge while every other row kept the gutter.
    for (const selector of [
      '.cmd-block',
      '.xterm-live-container',
      '.scrollback-restore-boundary',
    ]) {
      for (const property of INLINE) expect(shipped(selector, property)).toBeNull()
    }
    expect(shipped('.scrollback-restore-boundary', 'padding-block')).toBe('var(--space-2)')
  })

  it('the running region states only its block padding, never resetting the inset', () => {
    // A `padding` shorthand here (0,2,0) would override the row inset (0,1,0)
    // with 0 and put the running grid on a different edge from the block it
    // freezes into.
    expect(shipped('.xterm-live-container.live-running', 'padding')).toBeNull()
    expect(shipped('.xterm-live-container.live-running', 'padding-block')).toBe(
      'var(--cmd-output-pad-top) var(--cmd-output-pad-bottom)',
    )
  })

  it('the summon stack is full width and its answers wear the row inset', () => {
    expect(shipped('.nocx-summon-stack', 'left')).toBe('0')
    expect(shipped('.nocx-summon-stack', 'right')).toBe('0')
    expect(shipped('.nocx-summon-answers > *', 'padding-inline')).toBe('var(--pane-inline-padding)')
    expect(shipped('.nocx-summon-answers > *', 'box-sizing')).toBe('border-box')
  })

  it('the composer carries the gutter itself', () => {
    expect(shipped('.nocx-editor', 'padding')).toBe('10px var(--pane-inline-padding) 12px')
  })
})

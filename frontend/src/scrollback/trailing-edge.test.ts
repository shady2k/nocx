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
//      frozen rows share one content box; a block nested in a turn is not a
//      child of the stack and gets none. The ACTIVE body opts out of that
//      gutter on purpose (ADR-3kqrem7v): an edge-to-edge grid is what makes a
//      buffer switch cost no columns;
//   4. `usableViewport` (terminal-content.ts) fits the grid to the scroller's
//      `clientWidth`, which is the active row's content box now that an
//      active row inset is zero — the nocx-vydj defect returns if the two
//      drift apart.
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
// The composer's card margin — its own gutter now (nocx-9bpeq.23 round 2) —
// lives in ComposerFrame's own stylesheet, not the surface that places it.
const COMPOSER_FRAME_ENTRY = resolve(HERE, '..', 'styles/components/composer-frame.css')

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
  // The composer's gutter moved again in round 2 (nocx-9bpeq.23): off
  // `.nocx-editor`'s own padding and onto ComposerFrame's card margin.
  ...topLevelRules(readFileSync(COMPOSER_FRAME_ENTRY, 'utf8')),
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
  it('the gutter is one terminal cell, and it scales with the font', () => {
    // ADR-92zzg2y2: one cell wide on the sides, half a row top and bottom.
    // The renderer publishes the cell metric on `.pane`
    // (controller.cellMetricsChanged), so the token is declared there — a
    // `:root` declaration could not see the published value, because a custom
    // property's `var()` is resolved where it is declared.
    expect(shipped('.pane', '--pane-inline-padding')).toBe('var(--term-cell-width, 1ch)')
    expect(shipped('.pane', '--pane-block-padding')).toBe(
      'calc(var(--term-cell-height, 1.2em) / 2)',
    )
    // …and nowhere else, so there is no second owner of the number.
    expect(shipped(':root', '--pane-inline-padding')).toBeNull()
    expect(shipped(':root', '--pane-block-padding')).toBeNull()
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
    expect(shipped('.scrollback-inner > :not(.cell-metric-probe)', 'padding-inline')).toBe(
      'var(--pane-inline-padding)',
    )
    expect(shipped('.scrollback-inner > :not(.cell-metric-probe)', 'box-sizing')).toBe('border-box')
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
      '.terminal-live-container',
      '.scrollback-restore-boundary',
    ]) {
      for (const property of INLINE) expect(shipped(selector, property)).toBeNull()
    }
    expect(shipped('.scrollback-restore-boundary', 'padding-block')).toBe('var(--space-2)')
  })

  it('the running block wears the row gutter, and its body adds no inset of its own', () => {
    // ADR-2yzsq8mk: a normal command — running or finished — is one column
    // with the transcript's gutter, so the running block states NO inline
    // inset of its own and wears the stack's row rule exactly as the frozen
    // block does. ADR-3kqrem7v zeroed it here; that zero is what moved apt's
    // rows 24px sideways at the instant the card froze.
    expect(shipped('.cmd-block.cmd-block-running', 'padding-inline')).toBeNull()
    // Inside the block nothing adds a second inset: the body pays no padding
    // at all (its shorthand, and the vertical rhythm arrives with the frozen
    // card), and the live region pays none either. Either one would shift
    // every column of the grid off the width the PTY was told.
    expect(shipped('.terminal-live-container.live-running', 'padding')).toBe('0')
    expect(shipped('.cmd-block.cmd-block-running > .cmd-output', 'padding')).toBe('0')
    expect(shipped('.cmd-block.cmd-block-running > .cmd-output', 'padding-inline')).toBeNull()
  })

  it('no text touches the pane’s top or bottom edge, in any mode', () => {
    // ADR-92zzg2y2: the margin is on the BOX the scroller sits in, so a
    // scrolling transcript cannot carry it away with the content — and there
    // is no mode exception left to cancel it.
    expect(shipped('.scrollback-layout', 'padding-block')).toBe('var(--pane-block-padding)')
    expect(shipped('.scrollback-layout.inner-pane-program', 'padding-block')).toBeNull()
  })

  it('a filled pane hangs from the top gutter, and a transcript from the bottom', () => {
    // WHY THE STACK IS BOTTOM-ANCHORED AT ALL: a short transcript hangs from
    // the scroller's bottom edge, right above the composer, the way output
    // leaves a terminal (owner, 2026-09-15). The alternate screen has no
    // composer above it and its live box is sized from the scroller less the
    // one running header, floored to whole rows — so the pair is a few pixels
    // short of the scroller, and the bottom anchor spent that remainder as a
    // BAND ABOVE THE PINNED HEADER (nocx-4y82c.2: the header sat about a row
    // below a block's first row). Nothing hangs from the bottom in that mode,
    // so nothing is anchored there and the header starts on the gutter.
    expect(shipped('.scrollback-inner', 'margin-top')).toBe('auto')
    expect(shipped('.scrollback-inner.inner-fullscreen-mode', 'margin-top')).toBe('0')
  })
  it('a full-screen program wears the same gutter and no extra bottom margin', () => {
    // ADR-92zzg2y2: the alternate screen is not an exception. The row rule
    // above gives its live container the same one-cell gutter, and nothing
    // adds a margin the fit would have to pay for.
    expect(
      shipped('.scrollback-inner > .terminal-live-container.live-fullscreen', 'padding-inline'),
    ).toBeNull()
    expect(
      shipped('.scrollback-inner > .terminal-live-container.live-fullscreen', 'margin-bottom'),
    ).toBeNull()
  })

  it('the summon stack is full width and its answers wear the row inset', () => {
    expect(shipped('.nocx-summon-stack', 'left')).toBe('0')
    expect(shipped('.nocx-summon-stack', 'right')).toBe('0')
    expect(shipped('.nocx-summon-answers > *', 'padding-inline')).toBe('var(--pane-inline-padding)')
    expect(shipped('.nocx-summon-answers > *', 'box-sizing')).toBe('border-box')
  })

  it('the composer is a card with its OWN fixed inset, not a row wearing the ledger gutter', () => {
    // Superseded by the mockup decision record's ComposerFrame (§4,
    // nocx-9bpeq.23 round 2): the composer stopped being a full-width row
    // carrying `--pane-inline-padding` like a block or the live region —
    // `.nocx-editor` itself states no padding at all now (composer.css).
    // It is an inset CARD instead, and the card's own margin —
    // `--terminal-card-margin` (spec §3, equal to the existing --space-3,
    // so no new token was spent on it) — is where its gutter lives now,
    // deliberately narrower than the ledger's 24px row inset (the mockup's
    // own "10-12 px external gutter" for the card, not the row's).
    expect(shipped('.nocx-editor', 'padding')).toBeNull()
    // AND THE CARD DOES NOT FOLLOW THE PANE'S ONE-CELL GUTTER
    // (ADR-my0wd0za). ADR-92zzg2y2 carried the composer along on a
    // consequence the owner never decided: at a one-cell gutter the card
    // spanned the pane and paid the gutter as its inline padding, so it
    // stopped being an inset card. The geometry from before that change is
    // restored as fixed spacing-scale numbers — the old 24px gutter
    // (`--space-6`) less the card's own 12px padding and its 1px border for
    // the margin, and the 12px padding itself — so the card is the same card
    // at every font scale and nothing in it reads the cell metric.
    expect(shipped('.ui-composer-frame', 'margin')?.replace(/\s+/g, ' ')).toBe(
      '0 calc(var(--space-6) - var(--terminal-card-padding) - 1px) var(--terminal-card-margin)',
    )
    expect(shipped('.ui-composer-frame', 'padding')?.replace(/\s+/g, ' ')).toBe(
      'var(--terminal-card-padding)',
    )
    expect(shipped('.ui-composer-frame', 'padding') ?? '').not.toContain('--pane-inline-padding')
  })
})

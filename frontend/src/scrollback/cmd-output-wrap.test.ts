// The frozen-block wrap defect (nocx-juau): `.cmd-output` declared
// `white-space: pre-wrap` + `overflow-wrap: break-word` + `overflow-x: auto`.
// The first two fold a line as wide as the terminal — a status bar, a
// full-width rule, a boxed panel — and the third can never engage, because a
// wrapped line never overflows. Every box-drawing character after the fold
// lands in the wrong column, so the frozen block stops matching what the live
// terminal showed.
//
// jsdom computes no layout and `getComputedStyle` only sees injected styles,
// not the shipped stylesheet, so this pins the mechanism family the way the
// scroll-chain test pins its chain: read the real `src/style.css` and assert
// what the cascade resolves for `.cmd-output`. The contract is three facts:
// a term-line never wraps (white-space: pre, not pre-wrap), an unbroken run
// is never broken anywhere (no overflow-wrap), and a line wider than the
// block is reached by horizontal scrolling (overflow-x: auto).
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

type Rule = { selectors: string[]; body: string }
const STYLE_ENTRY = resolve(import.meta.dirname ?? '.', '..', 'style.css')
/** Top-level rules only, comments stripped. An at-rule block is skipped whole:
 *  a declaration that only holds at some viewport width does not hold. */
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

const RULES: Rule[] = topLevelRules(readFileSync(STYLE_ENTRY, 'utf8'))

/** The value the shipped cascade gives `property` for an element carrying
 *  `classes`, or null. Every matching bare-class rule contributes, later rules
 *  winning, which is how the cascade resolves it in the browser. */
function shippedValue(classes: readonly string[], property: string): string | null {
  let found: string | null = null
  const pattern = new RegExp(`(?:^|;)\\s*${property}\\s*:\\s*([^;]+)`)
  for (const rule of RULES) {
    if (!rule.selectors.some((s) => /^\.[\w-]+$/.test(s) && classes.includes(s.slice(1)))) {
      continue
    }
    const m = rule.body.match(pattern)
    if (m) found = m[1].trim()
  }
  return found
}

describe('a frozen block preserves the column alignment the live output had (nocx-juau)', () => {
  it('keeps every term-line on one row: .cmd-output white-space is pre, not pre-wrap', () => {
    // pre-wrap folds a full-width line at the block's edge; `pre` never
    // wraps, so the box-drawing columns survive.
    expect(shippedValue(['cmd-output'], 'white-space')).toBe('pre')
  })

  it('never breaks an unbroken run: .cmd-output declares no overflow-wrap', () => {
    // overflow-wrap: break-word is what folded a status bar — a run of box
    // characters with no break opportunity — at an arbitrary point. Absent
    // (or the initial `normal`) is the contract.
    const value = shippedValue(['cmd-output'], 'overflow-wrap')
    expect(value === null || value === 'normal').toBe(true)
  })

  it('reaches the far end of a wide line by scrolling: .cmd-output keeps overflow-x: auto', () => {
    expect(shippedValue(['cmd-output'], 'overflow-x')).toBe('auto')
  })
})

describe('frozen output lays out on the terminal cell metric (nocx-yy9g)', () => {
  it('corrects every character advance to the published cell width: .term-line carries the delta as letter-spacing', () => {
    // The renderer's real cell width (snapped to device pixels) is published
    // as --term-cell-delta on the scrollback container; the block applies it
    // per character, so N columns advance exactly N × cellWidth. Without
    // this, the DOM lays the same font out at its natural fractional advance
    // and a block that fitted the pane while live drifts wider (or narrower)
    // once frozen.
    expect(shippedValue(['term-line'], 'letter-spacing')).toBe('var(--term-cell-delta, 0px)')
  })

  it('declares the hidden probe the publisher measures with the block\u2019s own font', () => {
    // The probe must inherit the block's font chain (--font-family-mono,
    // --font-size-terminal) by living inside the scrollback container, or
    // the measured natural advance would be for a different font than the
    // one the block actually renders.
    const probe = RULES.find((r) => r.selectors.includes('.cell-metric-probe'))
    expect(probe).toBeDefined()
    expect(probe!.body).toMatch(/font-family\s*:\s*var\(--font-family-mono\)/)
    expect(probe!.body).toMatch(/visibility\s*:\s*hidden/)
  })
})

describe('the ask kind body wraps prose but keeps frozen output frozen (nocx-ex636)', () => {
  it('wraps prose: .cmd-output-ask resolves white-space pre-wrap, not the base pre', () => {
    // The base .cmd-output rule is pre (frozen rows, nocx-juau); the ask
    // modifier must come later in the cascade and override for the ask
    // kind only.
    expect(shippedValue(['cmd-output', 'cmd-output-ask'], 'white-space')).toBe('pre-wrap')
  })

  it('never runs off the right edge: .cmd-output-ask breaks unbroken runs and scrolls nowhere', () => {
    expect(shippedValue(['cmd-output', 'cmd-output-ask'], 'overflow-wrap')).toBe('break-word')
    expect(shippedValue(['cmd-output', 'cmd-output-ask'], 'overflow-x')).toBe('visible')
  })

  it('the frozen contract survives untouched: bare .cmd-output is still pre + auto', () => {
    // The modifier must not leak onto command blocks.
    expect(shippedValue(['cmd-output'], 'white-space')).toBe('pre')
    expect(shippedValue(['cmd-output'], 'overflow-x')).toBe('auto')
  })

  it('terminal output inside an answer keeps the old grammar: .cmd-output-code is pre + auto', () => {
    // A fenced block the model returns is the one case where the command
    // rules are the right rules — reached through the kind, never by
    // accident.
    expect(shippedValue(['cmd-output-code'], 'white-space')).toBe('pre')
    expect(shippedValue(['cmd-output-code'], 'overflow-x')).toBe('auto')
  })
})

describe('the Ask token is a gutter, not text in the input (nocx-ex636)', () => {
  it('the gutter clears CM6 chrome: no panel, no divider — a sigil, not a line-number rail', () => {
    // The token moved out of `.cm-content` because a control inside the
    // element carrying role="textbox" becomes part of the line's text. What
    // it moved INTO is a gutter, and CM6's default gutter is dressed as a
    // rail: a filled column with a divider down the side.
    const gutters = RULES.find((r) => r.selectors.includes('.nocx-editor .cm-gutters'))
    expect(gutters).toBeDefined()
    expect(gutters!.body).toMatch(/background\s*:\s*transparent/)
    expect(gutters!.body).toMatch(/border-right\s*:\s*none/)
  })

  it('the chip declares no trailing margin of its own, so the gap has one owner', () => {
    // The gap between the token and the text belongs to the gutter cell.
    const indicator = RULES.find((r) => r.selectors.includes('.nocx-editor-target-indicator'))
    expect(indicator).toBeDefined()
    expect(indicator!.body).not.toMatch(/margin-right/)
    const cell = RULES.find((r) =>
      r.selectors.includes('.nocx-editor .nocx-editor-target-gutter .cm-gutterElement'),
    )
    expect(cell).toBeDefined()
    expect(cell!.body).toMatch(/padding\s*:\s*0 6px 0 0/)
  })

  it('no line carries a hanging indent any more: the gutter aligns every line by construction', () => {
    // The widget needed `padding-left` + a negative `text-indent` computed
    // from a measured token width. A gutter reserves the column on every
    // line, so those rules are gone and must not creep back.
    const line = RULES.find((r) => r.selectors.includes('.nocx-editor .cm-line'))
    expect(line).toBeDefined()
    expect(line!.body).not.toMatch(/nocx-target-token-width/)
    expect(RULES.some((r) => r.selectors.some((sel) => sel.includes('cm-widgetBuffer')))).toBe(
      false,
    )
  })
})

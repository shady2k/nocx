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

const RULES: Rule[] = [
  ...topLevelRules(readFileSync(STYLE_ENTRY, 'utf8')),
  // The Ask token's own appearance moved into the kit with the indicator
  // (nocx-4ff.7): the base identity lives in styles/components/. The
  // gutter that PLACES it stays in style.css — both are read here, the
  // same way the cascade would see them.
  ...topLevelRules(
    readFileSync(
      resolve(import.meta.dirname ?? '.', '..', 'styles/components/mode-indicator.css'),
      'utf8',
    ),
  ),
]

/** The element the cascade is asked about. Attributes are part of it because
 *  a selector like `.term-cell[data-cols='2']` is what actually ships the
 *  two-column width, and a matcher that only looked at bare classes would
 *  silently skip it — the same shape of blindness that let nocx-juau come
 *  back unnoticed, where a compound selector overrode the rule the test was
 *  reading and the test never saw it. */
interface ShippedElement {
  classes: readonly string[]
  attrs?: Readonly<Record<string, string>>
}

/** Does one simple selector — `.cls` or `[attr='v']` — match the element? */
function matchesSimple(el: ShippedElement, simple: string): boolean {
  if (simple.startsWith('.')) return el.classes.includes(simple.slice(1))
  const attr = /^\[([\w-]+)(?:\s*=\s*(?:'([^']*)'|"([^"]*)"|([^\]]*)))?\]$/.exec(simple)
  if (attr === null) return false
  const name = attr[1]
  const value = attr[2] ?? attr[3] ?? attr[4]
  const have = el.attrs?.[name]
  if (have === undefined) return false
  return value === undefined || have === value.trim()
}

/** Split a compound selector (`.a.b[c='d']`) into its simple parts, or null
 *  when it uses anything this matcher does not model — a descendant
 *  combinator, an element name, a pseudo. Returning null rather than
 *  guessing keeps an unmodelled selector from silently "matching". */
function simpleParts(selector: string): string[] | null {
  const parts = selector.match(/\.[\w-]+|\[[^\]]*\]/g)
  if (parts === null) return null
  return parts.join('') === selector ? parts : null
}

/** The value the shipped cascade gives `property` for `el`, or null. Every
 *  matching rule contributes, later rules winning, which is how the cascade
 *  resolves it in the browser. Specificity is not modelled: within one
 *  stylesheet read in order, later-and-more-specific is how these rules are
 *  actually written, and a rule this matcher cannot parse contributes
 *  nothing rather than everything. */
function shippedValue(el: ShippedElement, property: string): string | null {
  let found: string | null = null
  const pattern = new RegExp(`(?:^|;)\\s*${property}\\s*:\\s*([^;]+)`)
  for (const rule of RULES) {
    const hit = rule.selectors.some((s) => {
      const parts = simpleParts(s)
      return parts !== null && parts.every((part) => matchesSimple(el, part))
    })
    if (!hit) continue
    const m = rule.body.match(pattern)
    if (m) found = m[1].trim()
  }
  return found
}

describe('a frozen block preserves the column alignment the live output had (nocx-juau)', () => {
  it('keeps every term-line on one row: .cmd-output white-space is pre, not pre-wrap', () => {
    // pre-wrap folds a full-width line at the block's edge; `pre` never
    // wraps, so the box-drawing columns survive.
    expect(shippedValue({ classes: ['cmd-output'] }, 'white-space')).toBe('pre')
  })

  it('never breaks an unbroken run: .cmd-output declares no overflow-wrap', () => {
    // overflow-wrap: break-word is what folded a status bar — a run of box
    // characters with no break opportunity — at an arbitrary point. Absent
    // (or the initial `normal`) is the contract.
    const value = shippedValue({ classes: ['cmd-output'] }, 'overflow-wrap')
    expect(value === null || value === 'normal').toBe(true)
  })

  it('reaches the far end of a wide line by scrolling: .cmd-output keeps overflow-x: auto', () => {
    expect(shippedValue({ classes: ['cmd-output'] }, 'overflow-x')).toBe('auto')
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
    expect(shippedValue({ classes: ['term-line'] }, 'letter-spacing')).toBe(
      'var(--term-cell-delta, 0px)',
    )
  })

  // Коробка ячейки поставляется в CSS, а не живёт в инлайне (nocx-ec18).
  it('ships a fixed-width box for a cell that cannot lay itself out', () => {
    expect(shippedValue({ classes: ['term-cell'] }, 'display')).toBe('inline-block')
    expect(shippedValue({ classes: ['term-cell'] }, 'width')).toBe('var(--term-cell-width, 1ch)')
    expect(shippedValue({ classes: ['term-cell'] }, 'letter-spacing')).toBe('0')
    // НИ overflow, НИ clip-path: коробка задаёт продвижку, а не прячет
    // краску. overflow увёл бы базовую линию к нижнему краю margin-бокса;
    // клип отрезал бы хвост глифу, попавшему в устаревшую коробку после
    // смены метрики. Оба утверждения — про ОТСУТСТВИЕ правила, потому что
    // добавить их обратно легко и незаметно.
    expect(shippedValue({ classes: ['term-cell'] }, 'overflow')).toBe(null)
    expect(shippedValue({ classes: ['term-cell'] }, 'clip-path')).toBe(null)
    expect(shippedValue({ classes: ['term-cell'], attrs: { 'data-cols': '2' } }, 'width')).toBe(
      'calc(var(--term-cell-width, 1ch) * 2)',
    )
  })

  it('shapes a frozen row cell by cell, the way the grid draws it', () => {
    // Иначе DOM волен сшить `->` или `ffi` не так, как xterm рисует их по
    // ячейкам, и предпосылка «ASCII всегда ложится» перестаёт быть верной.
    expect(shippedValue({ classes: ['term-line'] }, 'font-variant-ligatures')).toBe('none')
    expect(shippedValue({ classes: ['term-line'] }, 'font-kerning')).toBe('none')
  })

  it('declares the hidden probe the publisher measures with the block\u2019s own font', () => {
    // The probe must carry the block's font chain (--font-family-mono,
    // --font-size-terminal) EXPLICITLY, or the measured natural advance
    // would be for a different font than the one the block actually renders.
    // It does not inherit it: the scrollback container has no font-family at
    // all and `.term-line` declares only a size — believing the inheritance
    // story is how a second probe came to be written against the UI font
    // (nocx-ec18).
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
    expect(shippedValue({ classes: ['cmd-output', 'cmd-output-ask'] }, 'white-space')).toBe(
      'pre-wrap',
    )
  })

  it('never runs off the right edge: .cmd-output-ask breaks unbroken runs and scrolls nowhere', () => {
    expect(shippedValue({ classes: ['cmd-output', 'cmd-output-ask'] }, 'overflow-wrap')).toBe(
      'break-word',
    )
    expect(shippedValue({ classes: ['cmd-output', 'cmd-output-ask'] }, 'overflow-x')).toBe(
      'visible',
    )
  })

  it('the frozen contract survives untouched: bare .cmd-output is still pre + auto', () => {
    // The modifier must not leak onto command blocks.
    expect(shippedValue({ classes: ['cmd-output'] }, 'white-space')).toBe('pre')
    expect(shippedValue({ classes: ['cmd-output'] }, 'overflow-x')).toBe('auto')
  })

  it('terminal output inside an answer keeps the old grammar: .cmd-output-code is pre + auto', () => {
    // A fenced block the model returns is the one case where the command
    // rules are the right rules — reached through the kind, never by
    // accident.
    expect(shippedValue({ classes: ['cmd-output-code'] }, 'white-space')).toBe('pre')
    expect(shippedValue({ classes: ['cmd-output-code'] }, 'overflow-x')).toBe('auto')
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
    const indicator = RULES.find((r) => r.selectors.includes('.ui-mode-indicator'))
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
    //
    // The buffer rule is checked by what it MAY SAY rather than by its
    // absence. This assertion used to be "no rule mentions cm-widgetBuffer at
    // all", and that forbade one legitimate declaration along with the hack:
    // CM6 draws that image beside every inline widget, and its shipped
    // `vertical-align: text-top` grows the line box with some fonts — which
    // is a pixel of height the composer gained whenever a completion ghost
    // appeared, on macOS only (style.css says why `top` fixes it). What must
    // not come back is the ALIGNMENT arithmetic: a width, an indent, a
    // padding or a margin on the buffer is the old token widget returning
    // through the same door.
    const line = RULES.find((r) => r.selectors.includes('.nocx-editor .cm-line'))
    expect(line).toBeDefined()
    expect(line!.body).not.toMatch(/nocx-target-token-width/)
    const buffers = RULES.filter((r) => r.selectors.some((sel) => sel.includes('cm-widgetBuffer')))
    for (const rule of buffers) {
      expect(rule.body).not.toMatch(/nocx-target-token-width/)
      expect(rule.body).not.toMatch(/(^|[\s;])(width|text-indent|padding|margin)\s*:/)
    }
  })
})

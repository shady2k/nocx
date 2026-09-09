// The translated element must not clip (nocx-hg0dg).
//
// The live region hides the shell's echo of the command by TRANSLATING
// `.xterm-inner` upward (ScrollbackController._applyEchoShift). That element
// also carried `overflow: hidden`, added as "defence-in-depth against
// artifacts leaking below it". Against a transform it is not redundant
// defence: a clip applies in the element's OWN box, before the translation
// moves anything, so the bottom row was cut there and the shift then slid the
// survivors up — leaving the region one row shorter than the height it had
// just been sized to.
//
// Measured in the owner's pane, omp running and its input line unreachable:
// the outer viewport occupied [259, 772) and this element, after a one-row
// shift, [240, 753). Their overlap is 494px — 26 rows of the 27 the box was
// sized for. The row lost was the one being typed into.
//
// jsdom computes no layout, so this pins the mechanism the way the frozen-wrap
// test does: read the shipped stylesheet and assert the rule. The contract is
// one fact — clipping happens outside the translated element, once.
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

type Rule = { selectors: string[]; body: string }

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

const RULES = topLevelRules(
  readFileSync(resolve(import.meta.dirname ?? '.', '..', 'style.css'), 'utf8'),
)

/** What the shipped cascade resolves `property` to for one exact selector. */
function declared(selector: string, property: string): string | null {
  let found: string | null = null
  const pattern = new RegExp(`(?:^|;)\\s*${property}\\s*:\\s*([^;]+)`)
  for (const rule of RULES) {
    if (!rule.selectors.includes(selector)) continue
    const m = rule.body.match(pattern)
    if (m) found = m[1].trim()
  }
  return found
}

describe('the live region clips outside the element it translates (nocx-hg0dg)', () => {
  it('does not clip on .xterm-inner, which the echo shift transforms', () => {
    // Neither `hidden` nor any other clipping value: `clip`, `scroll` and
    // `auto` all establish the same box, and `auto` would additionally put a
    // scrollbar inside the grid.
    expect(declared('.xterm-inner', 'overflow')).toBeNull()
  })

  it('clips on the two ancestors that are never transformed', () => {
    // The row must still not leak below the region — that is what these are
    // for, and the reason the inner clip could be removed rather than moved.
    expect(declared('.xterm-live-container', 'overflow')).toBe('hidden')
    expect(declared('.xterm-live-viewport', 'overflow')).toBe('hidden')
  })
})

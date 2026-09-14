import { describe, expect, it } from 'vitest'
import { scanSource } from './check-glyph-icons.mjs'

/**
 * The glyph-icons checker's own tests (nocx-9bpeq.5).
 *
 * A character standing in for an icon — ⋮ × ✕ ⚠ or an emoji — renders, passes
 * every other gate and looks like a second icon vocabulary beside ui/icons. The
 * rule: such a string, used as an element's TEXT, is a violation. Both directions
 * are asserted; a rule that reported a multiplication sign in a title, or a
 * constant compared against a program's screen, would be turned off.
 */

describe('must trip', () => {
  it('JSX text that is a glyph', () => {
    const hits = scanSource('s.tsx', 'export const A = () => <button>✕</button>')
    expect(hits.map((h) => h.glyph)).toEqual(['✕'])
  })

  it('a string literal child of a JSX element', () => {
    const hits = scanSource('s.tsx', "export const A = () => <b>{'\\u00d7'}</b>")
    expect(hits.map((h) => h.glyph)).toEqual(['×'])
  })

  it('a textContent assignment, in a .ts module, with an emoji at the start', () => {
    const hits = scanSource(
      's.ts',
      'export function f(el: HTMLElement, x: string) { el.textContent = `📁 ${x}` }',
    )
    expect(hits.map((h) => h.glyph)).toEqual(['📁'])
  })

  it('an innerText assignment of a vertical ellipsis', () => {
    const hits = scanSource(
      's.ts',
      "export function f(el: HTMLElement) { el.innerText = '\\u22EE' }",
    )
    expect(hits.map((h) => h.glyph)).toEqual(['⋮'])
  })

  it('a textContent: property in an object literal', () => {
    const hits = scanSource('s.ts', "export const o = { textContent: '⚠ broken' }")
    expect(hits.map((h) => h.glyph)).toEqual(['⚠'])
  })
})

describe('must stay silent', () => {
  it('a glyph in a title attribute (a multiplication sign)', () => {
    expect(
      scanSource('s.tsx', 'export const A = (n: number) => <b title={`x ×${n}`}>x</b>'),
    ).toEqual([])
  })

  it('a constant compared against a screen', () => {
    expect(
      scanSource('s.ts', "const IDLE = '✳'\nexport const idle = (s: string) => s === IDLE"),
    ).toEqual([])
  })

  it('a regular expression', () => {
    expect(scanSource('s.ts', 'export const r = /^[>❯›»$#%λ⯈▶]{1,2}$/u')).toEqual([])
  })

  it('an icon component', () => {
    expect(
      scanSource(
        's.tsx',
        "import { CloseIcon } from './ui/icons'\nexport const A = () => <b><CloseIcon /></b>",
      ),
    ).toEqual([])
  })

  it('a comment', () => {
    expect(scanSource('s.ts', '// the old ✕ button\nexport const a = 1')).toEqual([])
  })
})

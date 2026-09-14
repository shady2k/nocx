// @vitest-environment node
// css-tree is loaded via createRequire and has no type declarations;
// every call to it touches a value typed `any`, so no-unsafe-* must
// be disabled at the file level for this test.
/* eslint-disable @typescript-eslint/no-unsafe-assignment,
                      @typescript-eslint/no-unsafe-call,
                      @typescript-eslint/no-unsafe-member-access */
/**
 * Icon sizes and the pane gutter are tokens (nocx-9bpeq.2, spec §8).
 *
 * Structural rather than computed: jsdom does not resolve var() from a
 * stylesheet, so "the token reaches the element" is asserted in a browser by the
 * terminal-screen end-to-end check (nocx-9bpeq.9). What this pins is that the
 * component cannot size a glyph any other way.
 */
import { describe, it, expect } from 'vitest'
import { createRequire } from 'node:module'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const css = createRequire(import.meta.url)('css-tree')
const dirname =
  (import.meta as { dirname?: string }).dirname ?? resolve(new URL('.', import.meta.url).pathname)
const read = (p: string): string => readFileSync(resolve(dirname, p), 'utf8')

function declarations(source: string): { selector: string; property: string; value: string }[] {
  const out: { selector: string; property: string; value: string }[] = []
  css.walk(css.parse(source), {
    visit: 'Rule',
    enter(rule: {
      prelude: unknown
      block: { children: { forEach(fn: (d: unknown) => void): void } }
    }) {
      const selector = css.generate(rule.prelude) as string
      rule.block.children.forEach((d: unknown) => {
        const n = d as { type: string; property: string; value: unknown }
        if (n.type !== 'Declaration') return
        out.push({
          selector,
          property: n.property,
          value: (css.generate(n.value) as string).trim(),
        })
      })
    },
  })
  return out
}

describe('icon and gutter tokens', () => {
  const tokens = declarations(read('styles/tokens.css'))
  const valueOf = (name: string): string | undefined =>
    tokens.find((d) => d.property === name)?.value

  it('declares the three icon sizes the spec names', () => {
    expect(valueOf('--icon-size-sm')).toBe('14px')
    expect(valueOf('--icon-size-md')).toBe('16px')
    expect(valueOf('--icon-size-lg')).toBe('20px')
  })

  it('declares the pane gutter in the token layer and nowhere else', () => {
    // 10px until nocx-9bpeq.8 changes it: that change moves xterm's column count.
    expect(valueOf('--pane-inline-padding')).toBe('10px')
    expect(
      declarations(read('styles/base.css')).some((d) => d.property === '--pane-inline-padding'),
    ).toBe(false)
  })

  it('sizes every IconButton glyph through an icon-size token', () => {
    const glyph = declarations(read('styles/components/icon-button.css')).filter(
      (d) => /svg$/.test(d.selector) && (d.property === 'width' || d.property === 'height'),
    )
    expect(glyph.length).toBeGreaterThan(0)
    for (const d of glyph) {
      expect(d.value, `${d.selector} ${d.property}`).toMatch(/^var\(--icon-size-(sm|md|lg)\)$/)
    }
  })
})

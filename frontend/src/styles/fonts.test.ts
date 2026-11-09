// THE PRIVATE-USE FALLBACK IS WIRED ONCE AND CARRIES ITS LICENCE (nocx-15b13).
//
// A local omp drew its icons as empty boxes because it asks for Nerd Font
// private-use codepoints and no font the pane rendered with had them; the same
// tool over ssh fell back to Unicode and drew fine. Measured first: the bytes
// arrive intact even when a codepoint is split across writes (e2e writes them
// one byte per write, and the emulator port has the same assertion in Go), so
// the gap was the face, not the decoding.
//
// Three facts keep the answer from drifting, and they are read off the shipped
// files because jsdom computes no cascade:
//
//   1. the terminal's chain is the app's mono stack plus the fallback — one
//      owner, composed, so a theme that names its own family still gets it;
//   2. the fallback's face is scoped to the private use area, so it can never
//      take a Latin, box-drawing or emoji codepoint from the ledger's font;
//   3. the font file and its licence travel together.
import { describe, expect, it } from 'vitest'
import { existsSync, readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { createHash } from 'node:crypto'

const HERE = import.meta.dirname ?? '.'
const FONTS_CSS = readFileSync(resolve(HERE, 'fonts.css'), 'utf8')
const STYLE_CSS = readFileSync(resolve(HERE, '..', 'style.css'), 'utf8')
const FONT_DIR = resolve(HERE, '..', '..', 'public', 'fonts', 'nerd-symbols')

/** The @font-face body for a family, comments stripped. */
function faceOf(css: string, family: string): string {
  const source = css.replace(/\/\*[\s\S]*?\*\//g, '')
  const at = source.indexOf(`font-family: '${family}'`)
  if (at < 0) return ''
  const open = source.lastIndexOf('@font-face', at)
  const close = source.indexOf('}', at)
  return open < 0 || close < 0 ? '' : source.slice(open, close)
}

describe('the private-use symbols fallback (nocx-15b13)', () => {
  it('is the terminal chain’s last text family, composed once', () => {
    expect(FONTS_CSS).toMatch(
      /--font-family-terminal:\s*var\(--font-family-mono\),\s*'Nerd Symbols Mono'/,
    )
    // The terminal surfaces read that token, not the bare mono stack: the live
    // grid, the frozen body, the history pages and the measuring probe.
    for (const selector of ['.term-grid', '.cmd-output', '.live-history', '.cell-metric-probe']) {
      const rule = new RegExp(
        `${selector.replace('.', '\\.')}\\s*\\{[^}]*font-family: var\\(--font-family-terminal\\)`,
      )
      expect(STYLE_CSS, selector).toMatch(rule)
    }
  })

  it('is scoped to the private use area and nothing else', () => {
    const face = faceOf(FONTS_CSS, 'Nerd Symbols Mono')
    expect(face).not.toBe('')
    expect(face).toContain('unicode-range: U+E000-F8FF, U+F0000-FFFFD')
  })

  it('ships its font file with the MIT licence that covers it', () => {
    const font = resolve(FONT_DIR, 'SymbolsNerdFontMono-Regular.woff2')
    const licence = resolve(FONT_DIR, 'LICENSE-FONT.txt')
    expect(existsSync(font)).toBe(true)
    expect(existsSync(licence)).toBe(true)
    // The licence is the Nerd Fonts MIT text, and the file is the glyph source
    // this test's siblings assert on — pinned by hash so a swap cannot slip in.
    expect(readFileSync(licence, 'utf8')).toContain('The MIT License (MIT)')
    expect(createHash('sha256').update(readFileSync(font)).digest('hex')).toBe(
      'ef2eba8ec11f6ca7179436bb40a892512178b6a84fa99731da820351b96b3714',
    )
  })
})

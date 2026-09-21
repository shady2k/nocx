import { describe, it, expect } from 'vitest'
import { runsFromSGR } from './sgr-read'
import { serializeRange, DEFAULT_SNAPSHOT, attrsToStyle, paletteToRGB } from './serializer'
import { BufferLine, lineWith, XTERM_CM_P16, XTERM_CM_P256, XTERM_CM_RGB } from './test-helpers'

const S = DEFAULT_SNAPSHOT

describe('runsFromSGR', () => {
  it('splits a row into runs and resolves the colour against the CURRENT palette', () => {
    const runs = runsFromSGR(S, 'plain \u001b[31mred')
    expect(runs.map((r) => r.chars)).toEqual(['plain ', 'red'])
    expect(runs[0].attrs.fg).toBeNull()
    expect(runs[1].attrs.fg).toBe('#f7768e')
  })

  it('reads the three colour forms sgr.ts writes', () => {
    // Against the palette rather than a hex typed here: the assertion is
    // that 91 means index 9, and a theme whose bright red equals its red
    // must not make that look wrong.
    expect(runsFromSGR(S, '\u001b[91mx')[0].attrs.fg).toBe(paletteToRGB(S, 9))
    expect(runsFromSGR(S, '\u001b[31mx')[0].attrs.fg).toBe(paletteToRGB(S, 1))
    expect(runsFromSGR(S, '\u001b[38;5;208mx')[0].attrs.fg).toBe('rgb(255,135,0)')
    expect(runsFromSGR(S, '\u001b[38;2;255;136;0mx')[0].attrs.fg).toBe('rgb(255,136,0)')
    expect(runsFromSGR(S, '\u001b[44mx')[0].attrs.bg).toBe('#7aa2f7')
  })

  it('applies parameters in order, because SGR is instructions and not a set', () => {
    // "reset, then red" is red; "red, then reset" is not.
    expect(runsFromSGR(S, '\u001b[0;31mx')[0].attrs.fg).toBe('#f7768e')
    expect(runsFromSGR(S, '\u001b[31;0mx')[0].attrs.fg).toBeNull()
  })

  it('turns attributes off as well as on', () => {
    const on = runsFromSGR(S, '\u001b[1;4mx')[0].attrs
    expect([on.bold, on.underline]).toEqual([true, true])
    const off = runsFromSGR(S, '\u001b[1;4mx\u001b[24my')[1].attrs
    expect([off.bold, off.underline]).toEqual([true, false])
  })

  it('ignores a parameter it does not model rather than losing the text', () => {
    const runs = runsFromSGR(S, '\u001b[73mstill here')
    expect(runs.map((r) => r.chars).join('')).toBe('still here')
  })

  it('sets nothing for half a colour, and keeps the text', () => {
    const runs = runsFromSGR(S, '\u001b[38;5mtruncated')
    expect(runs[0].attrs.fg).toBeNull()
    expect(runs[0].chars).toBe('truncated')
  })

  it('treats an empty parameter list as a reset, like a terminal does', () => {
    const runs = runsFromSGR(S, '\u001b[31mred\u001b[mplain')
    expect(runs[1].attrs.fg).toBeNull()
  })
})

// THE round trip, and the reason both halves live beside each other: what a
// restored block draws must be what the live block drew. Serialize real cells
// to SGR (the capture path), read them back (the restore path), and render
// both through the ONE attribute-to-style mapping.
describe('a stored body renders as the live rows did', () => {
  const styled = (runs: { chars: string; attrs: Parameters<typeof attrsToStyle>[1] }[]) =>
    runs
      .map((r) => {
        const style = attrsToStyle(S, r.attrs)
        return style ? `<span style="${style}">${r.chars}</span>` : r.chars
      })
      .join('')

  it('round-trips a coloured row through capture and restore', () => {
    const lines = [
      lineWith(
        { chars: 'o', fg: 2, fgMode: XTERM_CM_P16, bgMode: 0 },
        { chars: 'k', fg: 2, fgMode: XTERM_CM_P16, bgMode: 0 },
        { chars: '!', fgMode: 0, bgMode: 0 },
      ),
    ]
    const getLine = (y: number) => lines[y]

    const stored = '\u001b[32mok\u001b[39m!'
    const restored = styled(runsFromSGR(S, stored))
    const live = serializeRange(S, getLine, 0, 0)

    // The live path wraps each logical row in a term-line; the comparison is
    // of what is INSIDE it, which is what the two paths both produce.
    expect(`<span class="term-line">${restored}</span>`).toBe(live)
  })

  it('round-trips a 256-palette and a 24-bit row', () => {
    const lines = [
      lineWith(
        { chars: 'a', fg: 208, fgMode: XTERM_CM_P256, bgMode: 0 },
        { chars: 'b', fg: 0xff8800, fgMode: XTERM_CM_RGB, bgMode: 0 },
      ),
    ]
    const getLine = (y: number) => lines[y]
    const restored = styled(runsFromSGR(S, '\u001b[38;5;208ma\u001b[38;2;255;136;0mb\u001b[0m'))
    expect(`<span class="term-line">${restored}</span>`).toBe(serializeRange(S, getLine, 0, 0))
  })

  it('round-trips a multi-row body, joined the way the walk joins it', () => {
    const lines = [new BufferLine('first', false), new BufferLine('second', false)]
    const getLine = (y: number) => lines[y]
    const stored = 'first\nsecond'
    const rows = stored.split('\n').map((row) => styled(runsFromSGR(S, row)))
    expect(rows.map((r) => `<span class="term-line">${r}</span>`).join('')).toBe(
      serializeRange(S, getLine, 0, 1),
    )
  })
})

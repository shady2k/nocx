import { describe, it, expect } from 'vitest'
import { runsFromSGR } from './sgr-read'
import { DEFAULT_SNAPSHOT, paletteToRGB } from './serializer'

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

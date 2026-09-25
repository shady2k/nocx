// Wire-style tests (nocx-zg3k3.2.4): the painter's merge key over
// emulator.Style, and its CSS resolution.
//
// styleEquals is what rule 2 compares — a colour is equal when its KIND and
// the fields that kind names agree, because the frame schema promises the
// other fields ride the wire zeroed and are meaningless. Two runs must not
// split because two zeroed fields happen to differ.

// @vitest-environment jsdom

import { describe, it, expect } from 'vitest'
import { DEFAULT_SNAPSHOT } from '../scrollback/serializer'
import { colorEquals, faceOf, resolveInk, styleEquals } from './style'
import { DEFAULT_COLOR, styleOf } from './fixtures'

const rgb = (r: number, g: number, b: number) => ({
  kind: 2 as const,
  palette: 0,
  rgb: { r, g, b },
})
const paletteOf = (palette: number) => ({ kind: 1 as const, palette, rgb: { r: 0, g: 0, b: 0 } })

describe('styleEquals — the merge key (ADR-0009 rule 2)', () => {
  it('splits runs whose foreground differs', () => {
    expect(styleEquals(styleOf({ foreground: rgb(1, 2, 3) }), styleOf())).toBe(false)
  })

  it('ignores the fields the colour kind does not name', () => {
    const a = styleOf({ foreground: paletteOf(5) })
    const b = styleOf({ foreground: { kind: 1, palette: 5, rgb: { r: 99, g: 99, b: 99 } } })
    expect(styleEquals(a, b)).toBe(true)
  })

  it('compares palette indexes by index, rgb colours by channel', () => {
    expect(colorEquals(paletteOf(5), paletteOf(5))).toBe(true)
    expect(colorEquals(paletteOf(5), paletteOf(6))).toBe(false)
    expect(colorEquals(rgb(1, 2, 3), rgb(1, 2, 3))).toBe(true)
    expect(colorEquals(rgb(1, 2, 3), rgb(1, 2, 4))).toBe(false)
    expect(colorEquals(paletteOf(0), DEFAULT_COLOR)).toBe(false)
  })

  it('splits runs on attributes and underline shape, and joins on everything equal', () => {
    expect(styleEquals(styleOf({ attributes: 1 }), styleOf())).toBe(false)
    expect(styleEquals(styleOf({ underline: 2 }), styleOf({ underline: 1 }))).toBe(false)
    const bold = styleOf({ attributes: 1, foreground: rgb(9, 9, 9), underline: 4 })
    const boldAgain = styleOf({ attributes: 1, foreground: rgb(9, 9, 9), underline: 4 })
    expect(styleEquals(bold, boldAgain)).toBe(true)
  })
})

describe('faceOf — what the measuring authority is asked with', () => {
  it('reads bold and italic from the attribute bitset', () => {
    expect(faceOf(styleOf({ attributes: 1 }))).toEqual({ bold: true, italic: false })
    expect(faceOf(styleOf({ attributes: 2 }))).toEqual({ bold: false, italic: true })
    expect(faceOf(styleOf({ attributes: 3 }))).toEqual({ bold: true, italic: true })
    expect(faceOf(styleOf())).toEqual({ bold: false, italic: false })
  })
})

describe('resolveInk — the wire vocabulary to CSS', () => {
  it('paints nothing for a default cell: it inherits the surface (nocx-6w4z)', () => {
    expect(resolveInk(styleOf(), DEFAULT_SNAPSHOT)).toEqual({ css: '', hasBackground: false })
  })

  it('resolves exact rgb and palette colours', () => {
    expect(resolveInk(styleOf({ foreground: rgb(10, 20, 30) }), DEFAULT_SNAPSHOT).css).toContain(
      'color:rgb(10, 20, 30)',
    )
    expect(resolveInk(styleOf({ foreground: paletteOf(1) }), DEFAULT_SNAPSHOT).css).toContain(
      'color:#f7768e',
    )
    const bg = resolveInk(styleOf({ background: rgb(0, 1, 2) }), DEFAULT_SNAPSHOT)
    expect(bg.css).toContain('background:rgb(0, 1, 2)')
    expect(bg.hasBackground).toBe(true)
  })
  it('swaps the resolved colours on inverse: an inverse cell asks for both', () => {
    const ink = resolveInk(
      styleOf({ foreground: rgb(255, 0, 0), attributes: 16 }),
      DEFAULT_SNAPSHOT,
    )
    // The swapped-in foreground is the theme's own background colour — an
    // explicit ask, painted as such, exactly as the frozen path's
    // attrsToStyle resolves an inverse cell (nocx-6w4z omits what a cell
    // never asked for; this one asked).
    expect(ink.css).toContain('background:rgb(255, 0, 0)')
    expect(ink.css).toContain(`color:${DEFAULT_SNAPSHOT.defaultBg}`)
    expect(ink.hasBackground).toBe(true)
  })

  it('hides the ink for invisible without losing the background', () => {
    const ink = resolveInk(styleOf({ background: rgb(0, 1, 2), attributes: 32 }), DEFAULT_SNAPSHOT)
    expect(ink.css).toContain('color:transparent')
    expect(ink.css).toContain('background:rgb(0, 1, 2)')
  })

  it('carries the attribute asks the program made', () => {
    expect(resolveInk(styleOf({ attributes: 1 }), DEFAULT_SNAPSHOT).css).toContain(
      'font-weight:bold',
    )
    expect(resolveInk(styleOf({ attributes: 2 }), DEFAULT_SNAPSHOT).css).toContain(
      'font-style:italic',
    )
    expect(resolveInk(styleOf({ attributes: 4 }), DEFAULT_SNAPSHOT).css).toContain('opacity:0.5')
  })

  it('maps the underline shapes the wire enumerates', () => {
    expect(resolveInk(styleOf({ underline: 1 }), DEFAULT_SNAPSHOT).css).toContain(
      'text-decoration:underline',
    )
    expect(resolveInk(styleOf({ underline: 2 }), DEFAULT_SNAPSHOT).css).toContain(
      'text-decoration:underline double',
    )
    expect(resolveInk(styleOf({ underline: 3 }), DEFAULT_SNAPSHOT).css).toContain(
      'text-decoration:underline wavy',
    )
    expect(resolveInk(styleOf({ underline: 5 }), DEFAULT_SNAPSHOT).css).toContain(
      'text-decoration:underline dashed',
    )
    const both = resolveInk(styleOf({ underline: 1, attributes: 64 }), DEFAULT_SNAPSHOT)
    expect(both.css).toContain('text-decoration:underline line-through')
  })

  it('colours the decoration when the wire names an underline colour', () => {
    const ink = resolveInk(
      styleOf({ underline: 1, underlineColor: rgb(1, 2, 3) }),
      DEFAULT_SNAPSHOT,
    )
    expect(ink.css).toContain('text-decoration:underline')
    expect(ink.css).toContain('text-decoration-color:rgb(1, 2, 3)')
  })
})

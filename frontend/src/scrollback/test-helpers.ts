// Test helper: minimal terminal line/cell fixture for serializer tests.
// Implements the structural contract needed by cellAttrs and serializeLine.

import type { TerminalBufferLine, TerminalCell } from '../terminal-types'

interface MockCellData {
  chars: string
  width: number
  fg: number
  fgMode: number
  bg: number
  bgMode: number
  bold: boolean
  italic: boolean
  dim: boolean
  underline: boolean
  inverse: boolean
  blink: boolean
  strikethrough: boolean
  overline: boolean
}

function defaultCell(ch: string, width: number = 1): MockCellData {
  return {
    chars: ch,
    width,
    fg: 0,
    fgMode: 0, // default (inherit) — no color spans for plain text
    bg: 0,
    bgMode: 0, // default
    bold: false,
    italic: false,
    dim: false,
    underline: false,
    inverse: false,
    blink: false,
    strikethrough: false,
    overline: false,
  }
}

// The colour-mode flags xterm's getFgColorMode()/getBgColorMode() actually
// return — the raw attribute bits (CM_P16/CM_P256/CM_RGB), measured from a
// real 5.5.0 buffer (nocx-07o7). The mock models the real contract: a
// fixture with one of these values is a cell exactly as xterm hands it over.
export const XTERM_CM_P16 = 0x01000000
export const XTERM_CM_P256 = 0x02000000
export const XTERM_CM_RGB = 0x03000000

/** Create a mock TerminalCell from MockCellData. */
function toBufferCell(d: MockCellData) {
  return {
    getChars: () => d.chars,
    getWidth: () => d.width,
    getCode: () => d.chars.codePointAt(0) ?? 0,
    getFgColor: () => d.fg,
    getFgColorMode: () => d.fgMode,
    getBgColor: () => d.bg,
    getBgColorMode: () => d.bgMode,
    isBold: () => (d.bold ? 1 : 0),
    isItalic: () => (d.italic ? 1 : 0),
    isDim: () => (d.dim ? 1 : 0),
    isUnderline: () => (d.underline ? 1 : 0),
    isBlink: () => (d.blink ? 1 : 0),
    isInverse: () => (d.inverse ? 1 : 0),
    isInvisible: () => 0,
    isStrikethrough: () => (d.strikethrough ? 1 : 0),
    isOverline: () => (d.overline ? 1 : 0),
    // xterm's getFgColorMode() returns the raw attribute bits, not small
    // integers — model that here so a fixture cell IS an xterm cell.
    isFgRGB: () => d.fgMode === XTERM_CM_RGB,
    isBgRGB: () => d.bgMode === XTERM_CM_RGB,
    isFgPalette: () => d.fgMode === XTERM_CM_P16 || d.fgMode === XTERM_CM_P256,
    isBgPalette: () => d.bgMode === XTERM_CM_P16 || d.bgMode === XTERM_CM_P256,
    isFgDefault: () => d.fgMode === 0,
    isBgDefault: () => d.bgMode === 0,
    isAttributeDefault: () => false,
  }
}

/**
 * Minimal TerminalBufferLine mock.
 */
export class BufferLine implements TerminalBufferLine {
  private _cells: MockCellData[]
  readonly isWrapped: boolean
  readonly length: number

  constructor(content: string | MockCellData[], isWrapped = false) {
    if (typeof content === 'string') {
      this._cells = [...content].map((ch) => defaultCell(ch, 1))
    } else {
      this._cells = content
    }
    this.isWrapped = isWrapped
    this.length = this._cells.length
  }

  getCell(x: number): TerminalCell | undefined {
    if (x < 0 || x >= this._cells.length) return undefined
    return toBufferCell(this._cells[x])
  }

  translateToString(trimRight?: boolean, startCol?: number, endCol?: number): string {
    const s = startCol ?? 0
    const e = endCol ?? this.length
    const result = this._cells
      .slice(s, e)
      .map((c) => c.chars)
      .join('')
    return trimRight ? result.replace(/ +$/, '') : result
  }
}

/**
 * Builder for constructing styled lines.
 */
export function lineWith(
  ...specs: Array<{
    chars: string
    bold?: boolean
    fg?: number
    bg?: number
    fgMode?: number
    bgMode?: number
    italic?: boolean
    dim?: boolean
    underline?: boolean
    inverse?: boolean
    width?: number
  }>
): BufferLine {
  const cells: MockCellData[] = specs.map((s) => ({
    chars: s.chars,
    width: s.width ?? 1,
    fg: s.fg ?? 7,
    fgMode: s.fgMode ?? XTERM_CM_P16,
    bg: s.bg ?? 0,
    bgMode: s.bgMode ?? XTERM_CM_P16,
    bold: s.bold ?? false,
    italic: s.italic ?? false,
    dim: s.dim ?? false,
    underline: s.underline ?? false,
    inverse: s.inverse ?? false,
    blink: false,
    strikethrough: false,
    overline: false,
  }))
  return new BufferLine(cells)
}

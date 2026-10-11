// SGR emission for a captured block body (nocx-2f0f, design §3).
//
// THE DURABLE BODY KEEPS COLOUR AS SGR, not as inline CSS, and the reason is
// the theme: CSS bakes in the palette that was current when the block ran, so
// a restored block would sit in the old colours while every live block around
// it repainted. SGR names the colour the way the program named it and leaves
// resolving it to whoever draws.
//
// It is therefore the RAW cell colour that matters here — mode plus value —
// and not `CellAttrs`, whose fg/bg are already resolved against a
// TerminalSnapshot. That resolution belongs to the HTML path and must not
// happen on this one.
import type { TerminalBufferLine } from '../terminal-types'

/** A cell colour as xterm reports it: a palette index (mode 1) or a packed
 *  0xRRGGBB (mode 2). Null is not a third mode — it is the terminal's own
 *  default, which SGR writes as 39/49 rather than as any particular value.
 *
 *  NOT exported: nothing outside this module names it yet, and the dead-export
 *  ratchet is right about that. It goes public when the restore path needs to
 *  read one back. */
interface SGRColor {
  mode: 1 | 2
  color: number
}

export interface SGRAttrs {
  fg: SGRColor | null
  bg: SGRColor | null
  bold: boolean
  dim: boolean
  italic: boolean
  underline: boolean
  blink: boolean
  inverse: boolean
  strikethrough: boolean
  overline: boolean
}

export function emptySGR(): SGRAttrs {
  return {
    fg: null,
    bg: null,
    bold: false,
    dim: false,
    italic: false,
    underline: false,
    blink: false,
    inverse: false,
    strikethrough: false,
    overline: false,
  }
}

// xterm's raw colour-mode bits, the same values serializer.ts normalizes
// (nocx-07o7: the packing is R in bits 16-23, and reading it the other way
// round turned every orange frozen block blue).
const CM_MASK = 0x03000000
const CM_P16 = 0x01000000
const CM_P256 = 0x02000000
const CM_RGB = 0x03000000

function colorOf(color: number, rawMode: number): SGRColor | null {
  switch (rawMode & CM_MASK) {
    case CM_P16:
    case CM_P256:
      return { mode: 1, color }
    case CM_RGB:
      return { mode: 2, color }
    default:
      return null
  }
}

/** The raw SGR attributes of one cell. A cell that is not there is the empty
 *  set, which is what the walk needs at the end of a short line. */
export function cellSGRAttrs(line: TerminalBufferLine, cellIdx: number): SGRAttrs {
  const cell = line.getCell(cellIdx)
  if (!cell) return emptySGR()
  return {
    fg: colorOf(cell.getFgColor(), cell.getFgColorMode()),
    bg: colorOf(cell.getBgColor(), cell.getBgColorMode()),
    bold: cell.isBold() !== 0,
    dim: cell.isDim() !== 0,
    italic: cell.isItalic() !== 0,
    underline: cell.isUnderline() !== 0,
    blink: cell.isBlink() !== 0,
    inverse: cell.isInverse() !== 0,
    strikethrough: cell.isStrikethrough() !== 0,
    overline: cell.isOverline() !== 0,
  }
}

export function sgrEqual(a: SGRAttrs, b: SGRAttrs): boolean {
  return (
    (a.fg?.mode ?? 0) === (b.fg?.mode ?? 0) &&
    (a.fg?.color ?? -1) === (b.fg?.color ?? -1) &&
    (a.bg?.mode ?? 0) === (b.bg?.mode ?? 0) &&
    (a.bg?.color ?? -1) === (b.bg?.color ?? -1) &&
    a.bold === b.bold &&
    a.dim === b.dim &&
    a.italic === b.italic &&
    a.underline === b.underline &&
    a.blink === b.blink &&
    a.inverse === b.inverse &&
    a.strikethrough === b.strikethrough &&
    a.overline === b.overline
  )
}

/** The attribute flags and the SGR parameter that turns each one on. */
const FLAGS: Array<[keyof SGRAttrs, number]> = [
  ['bold', 1],
  ['dim', 2],
  ['italic', 3],
  ['underline', 4],
  ['blink', 5],
  ['inverse', 7],
  ['strikethrough', 9],
  ['overline', 53],
]

function colorParams(c: SGRColor | null, base: 30 | 40): string {
  const ext = base === 30 ? 38 : 48
  const dflt = base === 30 ? 39 : 49
  if (c === null) return String(dflt)
  if (c.mode === 2) {
    const r = (c.color >> 16) & 0xff
    const g = (c.color >> 8) & 0xff
    const b = c.color & 0xff
    return `${ext};2;${r};${g};${b}`
  }
  // The eight standard colours and their bright twins have short forms, and
  // they are what a reader expects to see: `\x1b[31m` is red to anyone who
  // has ever looked at a terminal, `\x1b[38;5;1m` is red to a parser.
  if (c.color < 8) return String(base + c.color)
  if (c.color < 16) return String(base + 60 + (c.color - 8))
  return `${ext};5;${c.color}`
}

function sameColor(a: SGRColor | null, b: SGRColor | null): boolean {
  return (a?.mode ?? 0) === (b?.mode ?? 0) && (a?.color ?? -1) === (b?.color ?? -1)
}

/**
 * The sequence that turns `prev` into `next`, or '' when they are the same.
 *
 * RESET-AND-REOPEN when an attribute goes off. SGR has an off code for each
 * one, but using them means up to eight parameters to undo a run and leaves a
 * reader that meets an unknown code in a state we did not intend. `0` plus
 * what is still on is shorter in the common case and has exactly one reading.
 */
export function sgrParams(prev: SGRAttrs, next: SGRAttrs): string {
  if (sgrEqual(prev, next)) return ''
  const turnedOff = FLAGS.some(([k]) => prev[k] === true && next[k] === false)
  const params: string[] = []
  if (turnedOff) {
    params.push('0')
    for (const [k, code] of FLAGS) if (next[k] === true) params.push(String(code))
    if (next.fg !== null) params.push(colorParams(next.fg, 30))
    if (next.bg !== null) params.push(colorParams(next.bg, 40))
  } else {
    for (const [k, code] of FLAGS) {
      if (prev[k] === false && next[k] === true) params.push(String(code))
    }
    if (!sameColor(prev.fg, next.fg)) params.push(colorParams(next.fg, 30))
    if (!sameColor(prev.bg, next.bg)) params.push(colorParams(next.bg, 40))
  }
  if (params.length === 0) return ''
  return `\x1b[${params.join(';')}m`
}

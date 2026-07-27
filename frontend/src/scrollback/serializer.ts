// DOM scrollback: xterm buffer → HTML serializer.
// Iterates IBufferLine cells, maps 256-color + RGB + attributes into inline
// styles, and merges adjacent cells with identical attributes into a single
// run. Output is an HTML fragment string — assigned via innerHTML on the
// frozen block output element.
//
// THEME SNAPSHOT: all colour decisions flow through a TerminalSnapshot that
// is captured once at block-freeze time. This ensures frozen output is never
// recoloured by a later theme change. The snapshot carries the 16-entry ANSI
// palette and the default foreground/background used for mode-0 (inherit)
// cells — critical for correct inverse-video fallback.

import type { IBufferLine, ITheme } from '@xterm/xterm'

// ── Theme snapshot types ──────────────────────────────────────────────────

/** 16-element ANSI palette tuple (indices 0–15). MUST match ITheme keys:
 *  black, red, green, yellow, blue, magenta, cyan, white,
 *  brightBlack, brightRed, brightGreen, brightYellow, brightBlue,
 *  brightMagenta, brightCyan, brightWhite. */
export type AnsiPalette = readonly [
  string,
  string,
  string,
  string,
  string,
  string,
  string,
  string,
  string,
  string,
  string,
  string,
  string,
  string,
  string,
  string,
]

/** Frozen theme snapshot: ANSI palette + default foreground/background.
 *  Captured once at block-freeze time so frozen output is immune to later
 *  theme changes. */
export interface TerminalSnapshot {
  palette: AnsiPalette
  defaultFg: string
  defaultBg: string
}

// ── Built-in default snapshot (Tokyo Night) ──────────────────────────────
// This is the fallback when no live theme snapshot was captured at freeze
// time.  It MUST stay byte-for-byte consistent with fromITheme applied to
// the DEFAULT_TERMINAL_THEME in renderers/theme-adapter.ts — if that object
// changes, this one must change too.  The test suite cross-checks both.

export const DEFAULT_SNAPSHOT: TerminalSnapshot = {
  palette: [
    '#1a1b26', // 0  Black
    '#f7768e', // 1  Red
    '#9ece6a', // 2  Green
    '#e0af68', // 3  Yellow
    '#7aa2f7', // 4  Blue
    '#bb9af7', // 5  Magenta
    '#7dcfff', // 6  Cyan
    '#a9b1d6', // 7  White
    '#414868', // 8  Bright Black
    '#f7768e', // 9  Bright Red
    '#9ece6a', // 10 Bright Green
    '#e0af68', // 11 Bright Yellow
    '#7aa2f7', // 12 Bright Blue
    '#bb9af7', // 13 Bright Magenta
    '#7dcfff', // 14 Bright Cyan
    '#c0caf5', // 15 Bright White
  ],
  defaultFg: '#c0caf5',
  defaultBg: '#1a1b26',
}

/** Convert xterm ITheme to a TerminalSnapshot.
 *  Every field in the palette maps to the corresponding ITheme key. */
export function fromITheme(theme: ITheme): TerminalSnapshot {
  const p = DEFAULT_SNAPSHOT.palette
  return {
    palette: [
      theme.black ?? p[0],
      theme.red ?? p[1],
      theme.green ?? p[2],
      theme.yellow ?? p[3],
      theme.blue ?? p[4],
      theme.magenta ?? p[5],
      theme.cyan ?? p[6],
      theme.white ?? p[7],
      theme.brightBlack ?? p[8],
      theme.brightRed ?? p[9],
      theme.brightGreen ?? p[10],
      theme.brightYellow ?? p[11],
      theme.brightBlue ?? p[12],
      theme.brightMagenta ?? p[13],
      theme.brightCyan ?? p[14],
      theme.brightWhite ?? p[15],
    ],
    defaultFg: theme.foreground ?? DEFAULT_SNAPSHOT.defaultFg,
    defaultBg: theme.background ?? DEFAULT_SNAPSHOT.defaultBg,
  }
}

// ── 256-color palette ─────────────────────────────────────────────────────

/**
 * Maps a 256-color palette index to a CSS color string.
 * - 0-15: ANSI colors from the snapshot palette
 * - 16-231: 6×6×6 color cube (algorithmic — theme-independent)
 * - 232-255: grayscale ramp (algorithmic — theme-independent)
 */
export function paletteToRGB(snapshot: TerminalSnapshot, idx: number): string {
  if (idx < 0 || idx > 255) return snapshot.defaultFg
  if (idx < 16) return snapshot.palette[idx]
  if (idx < 232) {
    const i = idx - 16
    const r = Math.floor(i / 36)
    const g = Math.floor((i % 36) / 6)
    const b = i % 6
    const scale = (v: number) => (v === 0 ? 0 : v * 40 + 55)
    return `rgb(${scale(r)},${scale(g)},${scale(b)})`
  }
  const g = (idx - 232) * 10 + 8
  return `rgb(${g},${g},${g})`
}

/**
 * Maps an xterm color (mode + color) to a CSS color string, or null for default.
 * - mode 0: default terminal color (inherit via CSS / snapshot default)
 * - mode 1: 256-color palette index
 * - mode 2: 24-bit RGB (bits 0-7=R, 8-15=G, 16-23=B)
 */
export function colorToCSS(snapshot: TerminalSnapshot, color: number, mode: number): string | null {
  if (mode === 0) return null
  if (mode === 2) {
    const r = color & 0xff
    const g = (color >> 8) & 0xff
    const b = (color >> 16) & 0xff
    return `rgb(${r},${g},${b})`
  }
  if (mode === 1) return paletteToRGB(snapshot, color)
  return null
}

// ── Cell attributes ────────────────────────────────────────────────────────

export interface CellAttrs {
  fg: string | null
  bg: string | null
  bold: boolean
  italic: boolean
  underline: boolean
  inverse: boolean
  blink: boolean
  strikethrough: boolean
  overline: boolean
}

export function emptyAttrs(): CellAttrs {
  return {
    fg: null,
    bg: null,
    bold: false,
    italic: false,
    underline: false,
    inverse: false,
    blink: false,
    strikethrough: false,
    overline: false,
  }
}

export function attrsEqual(a: CellAttrs, b: CellAttrs): boolean {
  return (
    a.fg === b.fg &&
    a.bg === b.bg &&
    a.bold === b.bold &&
    a.italic === b.italic &&
    a.underline === b.underline &&
    a.inverse === b.inverse &&
    a.blink === b.blink &&
    a.strikethrough === b.strikethrough &&
    a.overline === b.overline
  )
}

/**
 * Extract cell attributes from an xterm buffer cell.
 * Works with xterm.js 5.x IBufferLine / ICell interfaces.
 */
export function cellAttrs(
  snapshot: TerminalSnapshot,
  line: IBufferLine,
  cellIdx: number,
): CellAttrs {
  const cell = line.getCell(cellIdx)
  if (!cell) return emptyAttrs()

  const fgColor = cell.getFgColor()
  const fgMode = cell.getFgColorMode()
  const bgColor = cell.getBgColor()
  const bgMode = cell.getBgColorMode()

  return {
    fg: colorToCSS(snapshot, fgColor, fgMode),
    bg: colorToCSS(snapshot, bgColor, bgMode),
    bold: cell.isBold() !== 0,
    italic: cell.isItalic() !== 0,
    underline: cell.isUnderline() !== 0,
    inverse: cell.isInverse() !== 0,
    blink: cell.isBlink() !== 0,
    strikethrough: cell.isStrikethrough() !== 0,
    overline: cell.isOverline() !== 0,
  }
}

/**
 * Build a CSS style string from a CellAttrs record.
 * Default-colour cells (null fg/bg) resolve to the snapshot's defaults so
 * frozen output carries its own colours regardless of CSS theme changes.
 * Inverse mode swaps foreground and background colors.
 */
export function attrsToStyle(snapshot: TerminalSnapshot, a: CellAttrs): string {
  const parts: string[] = []
  let effectiveFg = a.fg ?? snapshot.defaultFg
  let effectiveBg = a.bg ?? snapshot.defaultBg

  if (a.inverse) {
    ;[effectiveFg, effectiveBg] = [effectiveBg, effectiveFg]
  }

  if (effectiveFg) parts.push(`color:${effectiveFg}`)
  if (effectiveBg) parts.push(`background:${effectiveBg}`)
  if (a.bold) parts.push('font-weight:bold')
  if (a.italic) parts.push('font-style:italic')
  if (a.underline && !a.strikethrough) parts.push('text-decoration:underline')
  if (a.strikethrough && !a.underline) parts.push('text-decoration:line-through')
  if (a.underline && a.strikethrough) parts.push('text-decoration:underline line-through')
  if (a.overline) {
    if (parts.some((s) => s.startsWith('text-decoration:'))) {
      const idx = parts.findIndex((s) => s.startsWith('text-decoration:'))
      if (idx >= 0) parts[idx] += ' overline'
      else parts.push('text-decoration:overline')
    } else {
      parts.push('text-decoration:overline')
    }
  }

  return parts.join(';')
}

// ── Run merging ────────────────────────────────────────────────────────────

interface Run {
  chars: string
  attrs: CellAttrs
}

/**
 * Collects consecutive cells with identical attributes into runs.
 * Handles wide characters (CJK) by their cell width.
 * keepTrailingSpace: a soft-wrapped line is FULL by definition (that is why
 * it wrapped), so its trailing chars are real content, not xterm padding —
 * the caller passes true for every physical line that has a continuation.
 */
function collectRuns(
  snapshot: TerminalSnapshot,
  line: IBufferLine,
  keepTrailingSpace = false,
): Run[] {
  const len = line.length
  if (len === 0) return []

  const runs: Run[] = []
  let i = 0

  while (i < len) {
    const cell = line.getCell(i)
    if (!cell) {
      i++
      continue
    }

    const width = cell.getWidth()
    const chars = cell.getChars()

    if (chars.length === 0) {
      const attrs = cellAttrs(snapshot, line, i)
      if (runs.length > 0 && attrsEqual(runs[runs.length - 1].attrs, attrs)) {
        runs[runs.length - 1].chars += ' '
      } else {
        runs.push({ chars: ' ', attrs })
      }
      i += Math.max(1, width)
      continue
    }

    const escaped = chars.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')

    const attrs = cellAttrs(snapshot, line, i)

    if (runs.length > 0 && attrsEqual(runs[runs.length - 1].attrs, attrs)) {
      runs[runs.length - 1].chars += escaped
    } else {
      runs.push({ chars: escaped, attrs })
    }
    i += Math.max(1, width)
  }

  if (!keepTrailingSpace && runs.length > 0) {
    const last = runs[runs.length - 1]
    last.chars = last.chars.replace(/ +$/, '')
  }

  return runs
}

// ── Serialization ──────────────────────────────────────────────────────────

/**
 * Serialize a single buffer line to an HTML string (a <span class="term-line">
 * containing run-merged <span> elements).
 */
export function serializeLine(snapshot: TerminalSnapshot, line: IBufferLine | undefined): string {
  if (!line) return '<span class="term-line"></span>'

  const runs = collectRuns(snapshot, line)

  if (runs.length === 0) {
    return '<span class="term-line"></span>'
  }

  const hasContent = runs.some((r) => r.chars.length > 0)
  if (!hasContent) {
    return '<span class="term-line"></span>'
  }

  let html = '<span class="term-line">'
  for (const run of runs) {
    if (run.chars.length === 0) continue
    const style = attrsToStyle(snapshot, run.attrs)
    if (style) {
      html += `<span style="${style}">${run.chars}</span>`
    } else {
      html += run.chars
    }
  }
  html += '</span>'
  return html
}

/**
 * Serialize a range of buffer lines (inclusive [startLine, endLine]) into a
 * single HTML string.
 *
 * REFLOW (owner directive): physical lines that xterm soft-wrapped at the
 * PTY grid width (IBufferLine.isWrapped on the CONTINUATION line) are
 * joined back into one logical line — one <span class="term-line"> per
 * logical line. The wrap the application was forced into at print time is
 * NOT baked into the block; CSS re-wraps naturally at the block's actual
 * width, so frozen output reflows cleanly on window resize. Hard newlines
 * (table rows, ls output) are untouched.
 *
 * Trailing EMPTY logical lines are trimmed: the range typically ends at the
 * D-marker line (an empty prompt-again row), and a dangling empty term-line
 * at the bottom of every block renders as stray blank space. Interior blank
 * lines are preserved — they are real output spacing.
 */
export function serializeRange(
  snapshot: TerminalSnapshot,
  getLine: (y: number) => IBufferLine | undefined,
  startLine: number,
  endLine: number,
): string {
  const groups: string[] = []
  for (let y = startLine; y <= endLine; y++) {
    const line = getLine(y)
    const continuation = line?.isWrapped === true && groups.length > 0
    if (!line) {
      groups.push('')
      continue
    }
    const runs = collectRuns(snapshot, line, continuation || (getLine(y + 1)?.isWrapped ?? false))
    let content = ''
    for (const run of runs) {
      if (run.chars.length === 0) continue
      const style = attrsToStyle(snapshot, run.attrs)
      content += style ? `<span style="${style}">${run.chars}</span>` : run.chars
    }
    if (continuation) {
      groups[groups.length - 1] += content
    } else {
      groups.push(content)
    }
  }
  while (groups.length > 0 && groups[groups.length - 1] === '') {
    groups.pop()
  }
  return groups.map((g) => `<span class="term-line">${g}</span>`).join('')
}

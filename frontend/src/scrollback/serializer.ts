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
import { cellSGRAttrs, sgrParams, sgrEqual, emptySGR, type SGRAttrs } from './sgr'
// ТОЛЬКО ТИП, и он берётся у владельца вопроса, а не переобъявляется здесь:
// форма ответа классификатора одна, и вторая её копия разошлась бы с первой
// молча. Зависимости на модуль это не создаёт — сериализатор по-прежнему не
// знает, кто классифицирует, и вызывается с любым замыканием этой формы.
import type { CellBox } from './cell-fit'

/** Version of the serializer's row-transform contract. Bump when the
 *  transforms that shape a frozen block's text change: wrapped lines joined,
 *  leading/trailing blanks dropped (serializeRange). A frame minted from a
 *  frozen block records this version in its provenance so a later reader can
 *  tell which transform set produced the text (nocx-3j9b). */
export const SERIALIZER_VERSION = 1

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
 * The colour-mode flags xterm's getFgColorMode()/getBgColorMode() actually
 * return. They are the raw attribute bits (Attributes.CM_P16/CM_P256/CM_RGB),
 * NOT small integers — measured from a real xterm 5.5.0 buffer (nocx-07o7);
 * the typings deliberately do not document the values and point at the
 * isFg* predicates instead. The serializer used to compare against 1 and 2,
 * which matched nothing, so every coloured cell froze as default.
 */
const CM_MASK = 0xff000000
const CM_P16 = 0x01000000
const CM_P256 = 0x02000000
const CM_RGB = 0x03000000

/** Map an xterm colour mode to the canonical 0/1/2 this file reasons in
 *  (0 default, 1 palette, 2 RGB). */
function normalizeColorMode(mode: number): number {
  switch (mode & CM_MASK) {
    case CM_P16:
    case CM_P256:
      return 1
    case CM_RGB:
      return 2
    default:
      return 0
  }
}

/**
 * Maps an xterm color (mode + color) to a CSS color string, or null for default.
 * - mode 0: default terminal color (inherit via CSS / snapshot default)
 * - mode 1: 256-color palette index
 * - mode 2: 24-bit RGB packed 0xRRGGBB (xterm's own packing: R in bits
 *   16-23, G 8-15, B 0-7 — the serializer once unpacked R and B swapped,
 *   turning orange into blue in every frozen block, nocx-07o7)
 */
export function colorToCSS(snapshot: TerminalSnapshot, color: number, mode: number): string | null {
  if (mode === 0) return null
  if (mode === 2) {
    const r = (color >> 16) & 0xff
    const g = (color >> 8) & 0xff
    const b = color & 0xff
    return `rgb(${r},${g},${b})`
  }
  if (mode === 1) return paletteToRGB(snapshot, color)
  return null
}

export interface CellAttrs {
  fg: string | null
  bg: string | null
  bold: boolean
  italic: boolean
  dim: boolean
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
    dim: false,
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
    a.dim === b.dim &&
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

  // xterm hands back the RAW mode flags; normalize to the canonical 0/1/2
  // colorToCSS reasons in (nocx-07o7).
  return {
    fg: colorToCSS(snapshot, fgColor, normalizeColorMode(fgMode)),
    bg: colorToCSS(snapshot, bgColor, normalizeColorMode(bgMode)),
    bold: cell.isBold() !== 0,
    italic: cell.isItalic() !== 0,
    dim: cell.isDim() !== 0,
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

  // Only what the cell actually asked for. The defaults are resolved above
  // because `inverse` needs both sides to swap, but writing them out again is
  // painting the theme onto every run — and that is why every finished block
  // came out on a black slab while the live region beside it, which paints
  // nothing, sat on the app's own background. A block should inherit
  // `.cmd-output`'s colours and override only where the program said so
  // (nocx-6w4z).
  if (effectiveFg && effectiveFg !== snapshot.defaultFg) parts.push(`color:${effectiveFg}`)
  if (effectiveBg && effectiveBg !== snapshot.defaultBg) parts.push(`background:${effectiveBg}`)
  if (a.bold) parts.push('font-weight:bold')
  if (a.italic) parts.push('font-style:italic')
  // Dim (CSI 2 m): the live terminal renders it as 50% opacity
  // (xterm's multiplyOpacity(color, 0.5)); opacity is the static-block
  // equivalent and works for default-colour cells too, which have no
  // explicit color to dim. It dims an explicit background along with the
  // text — xterm dims the resolved background the same way (nocx-07o7).
  if (a.dim) parts.push('opacity:0.5')
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

/**
 * Collects consecutive cells with identical attributes into runs.
 * Handles wide characters (CJK) by their cell width.
 * keepTrailingSpace: a soft-wrapped line is FULL by definition (that is why
 * it wrapped), so its trailing chars are real content, not xterm padding —
 * the caller passes true for every physical line that has a continuation.
 */
interface GenericRun<A> {
  chars: string
  attrs: A
  /** Коробка ячейки, либо undefined — ран течёт потоком. Ран с этим полем
   *  НЕ склеивается ни с чем: он и есть одна ячейка, а слитая пара заняла
   *  бы одну колонку на двоих. */
  box?: CellBox
}

/** What one cell walk yields: the merged runs, and the COLUMNS they occupy
 *  on the grid. The walk has always stepped by getWidth(); it threw the
 *  number away, and `nocx-ec18` is what that costs — a frozen line whose
 *  true width nothing downstream can state. Kept beside the runs rather
 *  than recomputed from `chars`, because a character is not a column: a
 *  CJK cell is one character over two columns and an astral glyph is two
 *  code units over one. */
interface Walked<A> {
  runs: GenericRun<A>[]
  cols: number
}

/**
 * THE cell walk, over whatever an attribute is (nocx-2f0f).
 *
 * Three emissions need it — inline CSS for the frozen block, SGR for the
 * durable body, characters for the derived text — and they differ in what an
 * attribute IS and in nothing else: the wide-character stride, the
 * empty-cell-is-a-space rule and the trailing-space trim are one behaviour,
 * and a second copy of them is a restored block that does not match the block
 * a person saw.
 *
 * `escape` is the one genuinely HTML-only step and stays a parameter rather
 * than a caller's post-pass: it must happen per cell, before runs are merged,
 * or a `<` in the middle of a run escapes differently from one at its edge.
 */
function collectRunsOf<A>(
  line: IBufferLine,
  attrsOf: (line: IBufferLine, i: number) => A,
  equal: (a: A, b: A) => boolean,
  escape: boolean,
  keepTrailingSpace: boolean,
  boxOf?: (chars: string, width: number, attrs: A) => CellBox | null,
): Walked<A> {
  const len = line.length
  if (len === 0) return { runs: [], cols: 0 }

  const runs: GenericRun<A>[] = []
  let cols = 0
  let i = 0

  while (i < len) {
    const cell = line.getCell(i)
    if (!cell) {
      i++
      continue
    }

    const width = cell.getWidth()
    const chars = cell.getChars()
    const attrs = attrsOf(line, i)

    if (chars.length === 0) {
      const last = runs.length > 0 ? runs[runs.length - 1] : undefined
      if (last !== undefined && last.box === undefined && equal(last.attrs, attrs)) {
        last.chars += ' '
      } else {
        runs.push({ chars: ' ', attrs })
      }
      cols += Math.max(1, width)
      i += Math.max(1, width)
      continue
    }

    const text = escape
      ? chars.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
      : chars

    // Коробка принимается ТОЛЬКО на колонки самой ячейки. «Одна колонка»
    // для ячейки шириной две — это сдвиг, которого в сетке нет; лучше
    // сегодняшний поток, чем выдуманная геометрия.
    const columns = Math.max(1, width)
    const claimed = boxOf?.(chars, columns, attrs) ?? null
    const box = claimed?.cols === columns ? claimed : null
    const last = runs.length > 0 ? runs[runs.length - 1] : undefined
    if (box !== null) {
      runs.push({ chars: text, attrs, box })
    } else if (last !== undefined && last.box === undefined && equal(last.attrs, attrs)) {
      last.chars += text
    } else {
      runs.push({ chars: text, attrs })
    }
    cols += Math.max(1, width)
    i += Math.max(1, width)
  }

  if (!keepTrailingSpace && runs.length > 0) {
    const last = runs[runs.length - 1]
    if (last.box === undefined) {
      const trimmed = last.chars.replace(/ +$/, '')
      // Every trimmed character is a single-column pad cell, so the columns
      // come off one for one. Counting them would report drift on every
      // padded row in the buffer, which is most of them.
      cols -= last.chars.length - trimmed.length
      last.chars = trimmed
    }
  }

  return { runs, cols }
}

function collectRuns(
  snapshot: TerminalSnapshot,
  line: IBufferLine,
  keepTrailingSpace = false,
  boxOf?: (chars: string, width: number, attrs: CellAttrs) => CellBox | null,
): Walked<CellAttrs> {
  return collectRunsOf(
    line,
    (l, i) => cellAttrs(snapshot, l, i),
    attrsEqual,
    true,
    keepTrailingSpace,
    boxOf,
  )
}

// ── Serialization ──────────────────────────────────────────────────────────

/**
 * Serialize a single buffer line to an HTML string (a <span class="term-line">
 * containing run-merged <span> elements).
 */
export function serializeLine(snapshot: TerminalSnapshot, line: IBufferLine | undefined): string {
  if (!line) return '<span class="term-line"></span>'

  const { runs } = collectRuns(snapshot, line)

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
 * JOIN (owner directive): physical lines that xterm soft-wrapped at the
 * PTY grid width (IBufferLine.isWrapped on the CONTINUATION line) are
 * joined back into one logical line — one <span class="term-line"> per
 * logical line. The wrap the application was forced into at print time is
 * NOT re-created as separate rows: the frozen block never re-wraps
 * (nocx-juau — see .cmd-output in style.css), so a joined line wider than
 * the block overflows and is reached by horizontal scrolling as one row.
 * Hard newlines (table rows, ls output) are untouched.
 *
 * Trailing EMPTY logical lines are trimmed: the range typically ends at the
 * D-marker line (an empty prompt-again row), and a dangling empty term-line
 * at the bottom of every block renders as stray blank space. Interior blank
 * lines are preserved — they are real output spacing.
 */
type RowEmitter = (line: IBufferLine, keepTrailingSpace: boolean) => Emitted

/** One logical line as the walk leaves it: what to print, and the GRID
 *  COLUMNS it stood in. The two travel together through the join and both
 *  trims, because a count that is trimmed differently from its line is
 *  worse than no count — it names a drift that is only the pairing being
 *  off by a row. */
interface Emitted {
  content: string
  cols: number
}

/**
 * THE row walk: wrapped rows joined into one logical line, trailing empties
 * trimmed, then leading ones. Every emission shares it, so the three can
 * never disagree about how many rows a block has or where they end.
 */
function walkRange(
  getLine: (y: number) => IBufferLine | undefined,
  startLine: number,
  endLine: number,
  emit: RowEmitter,
): Emitted[] {
  const groups: Emitted[] = []
  for (let y = startLine; y <= endLine; y++) {
    const line = getLine(y)
    const continuation = line?.isWrapped === true && groups.length > 0
    if (!line) {
      groups.push({ content: '', cols: 0 })
      continue
    }
    const emitted = emit(line, continuation || (getLine(y + 1)?.isWrapped ?? false))
    if (continuation) {
      const last = groups[groups.length - 1]
      last.content += emitted.content
      last.cols += emitted.cols
    } else {
      groups.push(emitted)
    }
  }
  while (groups.length > 0 && groups[groups.length - 1].content === '') {
    groups.pop()
  }
  // Leading empties go too, and for a stronger reason than the trailing ones.
  // The rows between the C marker and the program's first output are where
  // readline ERASED its own rendering of the command: a submitted document is
  // pasted, readline draws it, and on accept it blanks those rows and redraws
  // lower. They are echo scaffolding, not output, and the block already shows
  // the command in its header from our own intent — so they arrive as a band
  // of empty term-lines above the output. Measured 2026-08-02: an eight-line
  // curl left SIXTEEN of them, a single-line one four; the count tracks the
  // rows the command occupied, which is what gives it away.
  //
  // A program that deliberately prints a leading blank line loses it. That is
  // the same trade the trailing trim already makes, for the same reason: one
  // line of spacing is cheaper than a screenful of nothing.
  let lead = 0
  while (lead < groups.length && groups[lead].content === '') lead++
  groups.splice(0, lead)
  return groups
}

export function serializeRange(
  snapshot: TerminalSnapshot,
  getLine: (y: number) => IBufferLine | undefined,
  startLine: number,
  endLine: number,
  colsOut?: number[],
  boxOf?: (chars: string, width: number, attrs: CellAttrs) => CellBox | null,
): string {
  const groups = walkRange(getLine, startLine, endLine, (line, keepTrailingSpace) => {
    const { runs, cols } = collectRuns(snapshot, line, keepTrailingSpace, boxOf)
    let content = ''
    for (const run of runs) {
      if (run.chars.length === 0) continue
      const style = attrsToStyle(snapshot, run.attrs)
      if (run.box !== undefined) {
        const styleAttr = style ? ` style="${style}"` : ''
        // АТРИБУТЫ ЯЧЕЙКИ — НА КОРОБКЕ, МАСШТАБ — НА ОБЁРТКЕ ВНУТРИ, и это
        // не вкусовщина. attrsToStyle вешает background-color именно на
        // коробку; трансформация, поставленная на неё же, ужала бы вместе
        // с краской и фон, и цветная ячейка стала бы вдвое у́же соседних —
        // ровно та дыра в строке, которую вся эта работа закрывает.
        // Масштабируется только краска, поэтому обёртка отдельная.
        // При fit === 1 обёртки нет вовсе: лишний узел на каждую коробку
        // ради `scale(1)`.
        const ink =
          run.box.fit < 1
            ? `<span class="term-cell-ink" style="--cell-fit:${run.box.fit}">${run.chars}</span>`
            : run.chars
        content += `<span class="term-cell" data-cols="${run.box.cols}"${styleAttr}>${ink}</span>`
      } else {
        content += style ? `<span style="${style}">${run.chars}</span>` : run.chars
      }
    }
    return { content, cols }
  })
  // An OUT PARAMETER rather than a second return value or a data-* on the
  // row, because the reader is a switched-off instrument (nocx-4n6sj) and
  // the shipped HTML must not change to carry it: the frozen block's markup
  // is asserted verbatim in fifty-odd tests and rewritten in place by link
  // decoration. Index i pairs with the i-th emitted term-line, which is
  // exact — the map below is one element per group, in order.
  if (colsOut) {
    colsOut.length = 0
    for (const g of groups) colsOut.push(g.cols)
  }
  return groups.map((g) => `<span class="term-line">${g.content}</span>`).join('')
}

/**
 * Пройти те же ячейки и назвать их, ничего не строя (nocx-ec18).
 *
 * Заморозка меряет ширины ПАКЕТОМ: чтение ректа после записи форсирует
 * раскладку, и поштучно это N раскладок в тот самый момент, когда блок
 * подменяет живую область. Значит кандидатов надо знать ДО сериализации.
 *
 * Это тот же collectRunsOf, а не второй обход в смысле AD-8: функция,
 * знающая, как ходить по ячейкам и как считать колонки, по-прежнему одна.
 * Здесь она вызывается с пустыми атрибутами — как это уже делает
 * serializeRangeText, — поэтому проход дешёвый: ни вывода цвета, ни
 * экранирования, ни склейки строк.
 *
 * ПОРЯДОК И СОСТАВ ОБЯЗАНЫ СОВПАДАТЬ с тем, что увидит классификатор при
 * сериализации, иначе кэш окажется холодным ровно там, где нужен, и коробка
 * не появится молча. Это утверждается тестом, сравнивающим два списка.
 */
export function collectFitCandidates(
  getLine: (y: number) => IBufferLine | undefined,
  startLine: number,
  endLine: number,
  sink: (chars: string, width: number, attrs: CellAttrs) => void,
): void {
  walkRange(getLine, startLine, endLine, (line, keepTrailingSpace) => {
    const { cols } = collectRunsOf<CellAttrs>(
      line,
      (l, i) => cellAttrs(DEFAULT_SNAPSHOT, l, i),
      attrsEqual,
      false,
      keepTrailingSpace,
      (chars, width, attrs) => {
        sink(chars, width, attrs)
        return null
      },
    )
    return { content: '', cols }
  })
}

/**
 * The DURABLE body of a block: the same rows, carrying colour as SGR and no
 * markup at all (nocx-2f0f, design §3). A row closes whatever it opened, so a
 * reader that starts mid-body — a restore drawing one block — is never left
 * wearing the previous row's colour.
 *
 * No theme snapshot is taken and none may be: resolving a palette index to a
 * hex colour here is what would freeze a restored block in the palette that
 * was current when it ran.
 */
export function serializeRangeSGR(
  getLine: (y: number) => IBufferLine | undefined,
  startLine: number,
  endLine: number,
): string {
  const empty = emptySGR()
  const groups = walkRange(getLine, startLine, endLine, (line, keepTrailingSpace) => {
    const { runs, cols } = collectRunsOf<SGRAttrs>(
      line,
      cellSGRAttrs,
      sgrEqual,
      false,
      keepTrailingSpace,
    )
    let content = ''
    let current = empty
    for (const run of runs) {
      if (run.chars.length === 0) continue
      content += sgrParams(current, run.attrs)
      current = run.attrs
      content += run.chars
    }
    if (!sgrEqual(current, empty)) content += '\u001b[0m'
    return { content, cols }
  })
  return groups.map((g) => g.content).join('\n')
}

/**
 * The DERIVED body: the same rows as characters. What search, copy and the
 * agent read — none of them wants an escape-sequence stream, and a needle
 * spanning a colour change would stop matching in one.
 */
export function serializeRangeText(
  getLine: (y: number) => IBufferLine | undefined,
  startLine: number,
  endLine: number,
): string {
  const groups = walkRange(getLine, startLine, endLine, (line, keepTrailingSpace) => {
    const { runs, cols } = collectRunsOf<null>(
      line,
      () => null,
      () => true,
      false,
      keepTrailingSpace,
    )
    return { content: runs.map((r) => r.chars).join(''), cols }
  })
  return groups.map((g) => g.content).join('\n')
}

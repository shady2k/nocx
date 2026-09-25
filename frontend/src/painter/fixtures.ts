// Test fixtures for the painter: wire frames built the way the runtime
// declares them, so the painter is always driven through the real cell
// model (createCellModel().apply) rather than past it. Not imported by any
// production module.
//
// Tests describe a row the way they always have — one CellSpec per COLUMN,
// spacer included ([grapheme, width, hasText], now with an optional style
// as a 4th element) — and wireRowOf runs the SAME algorithm
// internal/sessionruntime's encodeRow does (nocx-zg3k3.2.12) to produce the
// compact text+marks+runs shape the wire actually carries: a wide cluster's
// spacer folded away, a style packed to the bare integer 0 or the 5-tuple,
// a row of nothing but the default style shipped with `runs` omitted. A
// fixture that built the wire shape by hand would drift from the real
// encoder the day either one changed; this one cannot, because it is the
// same arithmetic.

import type { SessionFrame, Style as WireStyle } from '../generated/session.frame'
import {
  createCellModel,
  type ModelColor,
  type ModelStyle,
  type ScreenSnapshot,
} from '../cell-model'

export const DEFAULT_COLOR: ModelColor = { kind: 0, palette: 0, rgb: { r: 0, g: 0, b: 0 } }

export function styleOf(over: Partial<ModelStyle> = {}): ModelStyle {
  return {
    foreground: DEFAULT_COLOR,
    background: DEFAULT_COLOR,
    underlineColor: DEFAULT_COLOR,
    attributes: 0,
    underline: 0,
    ...over,
  }
}

const DEFAULT_STYLE = styleOf()

function colorEquals(a: ModelColor, b: ModelColor): boolean {
  return (
    a.kind === b.kind &&
    a.palette === b.palette &&
    a.rgb.r === b.rgb.r &&
    a.rgb.g === b.rgb.g &&
    a.rgb.b === b.rgb.b
  )
}

function styleEquals(a: ModelStyle, b: ModelStyle): boolean {
  return (
    colorEquals(a.foreground, b.foreground) &&
    colorEquals(a.background, b.background) &&
    colorEquals(a.underlineColor, b.underlineColor) &&
    a.attributes === b.attributes &&
    a.underline === b.underline
  )
}

/** Pack one resolved colour the way internal/sessionruntime's
 *  encodeColorValue does: 0 default, 1-256 a palette index plus one, 257+
 *  a packed RGB triple offset by 257. */
function wireColorOf(color: ModelColor): number {
  if (color.kind === 0) return 0
  if (color.kind === 1) return color.palette + 1
  return 257 + ((color.rgb.r << 16) | (color.rgb.g << 8) | color.rgb.b)
}

/** Pack one resolved style the way internal/sessionruntime's
 *  encodeStyleValue does: the bare integer 0 for the all-default style, or
 *  the 5-element tuple otherwise. */
function wireStyleOf(style: ModelStyle): WireStyle {
  if (styleEquals(style, DEFAULT_STYLE)) return 0
  return [
    wireColorOf(style.foreground),
    wireColorOf(style.background),
    wireColorOf(style.underlineColor),
    style.attributes,
    style.underline as 0 | 1 | 2 | 3 | 4 | 5,
  ]
}

/** One column: grapheme, declared width, has-text, and an optional style
 *  (defaults to the all-default style — the row-wide style every fixture
 *  used before per-cell styling existed). */
export type CellSpec = [
  grapheme: string,
  width: 1 | 2 | 3 | 4,
  hasText: boolean,
  style?: ModelStyle,
]

/** Encode one row's columns into the wire's text+marks+runs — the fixture
 *  mirror of internal/sessionruntime's encodeRow: styles merge into
 *  maximal runs measured in columns (spacers included), a wide cluster's
 *  spacer is folded out of text and marked instead, and a row whose runs
 *  would be the single implicit default omits `runs` altogether. */
export function wireRowOf(
  cells: readonly CellSpec[],
  wrap = false,
  continuation = false,
): SessionFrame['rows'][number] {
  let text = ''
  const marks: [number, number, 1 | 2 | 4][] = []
  const runs: SessionFrame['rows'][number]['runs'] = []
  let runStyle: ModelStyle = DEFAULT_STYLE
  let runLength = 0
  const flush = (): void => {
    if (runLength === 0) return
    runs.push([wireStyleOf(runStyle), runLength])
    runLength = 0
  }
  let pos = 0
  for (const [grapheme, width, hasText, style = DEFAULT_STYLE] of cells) {
    if (runLength > 0 && styleEquals(runStyle, style)) {
      runLength++
    } else {
      flush()
      runStyle = style
      runLength = 1
    }
    if (width === 3) continue // spacerTail: not a position of its own
    const g = hasText ? grapheme : ''
    text += g
    const codepoints = Array.from(g).length
    if (codepoints !== 1 || width !== 1) {
      marks.push([pos, codepoints, width])
    }
    pos++
  }
  flush()
  const row: SessionFrame['rows'][number] = { text }
  if (marks.length > 0) row.marks = marks
  if (!(runs.length === 1 && runs[0][0] === 0)) row.runs = runs
  if (wrap) row.wrap = wrap
  if (continuation) row.continuation = continuation
  return row
}

export function frameOf(
  revision: number,
  rowsSpec: CellSpec[][],
  cursor: SessionFrame['cursor'] = { x: 0, y: 0, visible: false },
  geometry: Partial<SessionFrame['geometry']> = {},
): SessionFrame {
  return {
    revision,
    geometry: {
      cols: Math.max(...rowsSpec.map((cells) => cells.length)),
      rows: rowsSpec.length,
      cellWidthPx: 8,
      cellHeightPx: 20,
      revision,
      ...geometry,
    },
    cursor,
    rows: rowsSpec.map((cells) => wireRowOf(cells)),
  }
}

/** Apply a frame through the real model and hand back the installed
 *  snapshot; a refusal is a test bug, not a branch to explore. */
export function snapshotOf(frame: SessionFrame): ScreenSnapshot {
  const result = createCellModel().apply(frame)
  if (!result.ok) throw new Error(`fixture frame refused: ${JSON.stringify(result.refusal)}`)
  return result.snapshot
}

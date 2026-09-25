// THE CLIENT CELL MODEL (nocx-zg3k3.2.3).
//
// The client's model of the screen: passive, versioned, never recomputed
// locally. A frame (contracts/session.frame.schema.json, generated as
// src/generated/session.frame.ts) arrives; the model serves its cells —
// each carries the authoritative column, span, style, has-text fact and
// its row's wrap/continuation exactly as the runtime declared them. The
// one thing this module never does is ask the browser anything: no font
// measurement, no layout read, no zoom or device-pixel-ratio input exists
// here at all. Column accounting is the wire's arithmetic — one entry per
// column, a cell's footprint is its declared width — because that is the
// only arithmetic hit-testing, selection and copy can all run backwards
// and agree with the paint (design .internal/specs/2026-09-21-the-cell-
// renderer-choice-design.md §3.9).
//
// What this module owns:
//
//   1. Intake validation, in the wire's own vocabulary: a frame that is
//      not the rectangle geometry declares (rows count, a row's explicit
//      content past geometry.cols), whose marks or runs are malformed, or
//      that carries a width outside {1, 2, 4} is malformed at the source
//      (the schema's words) and is refused — the previously installed
//      revision stays whole.
//   2. Versioning: revision is the runtime's monotonic clock. A frame at
//      a revision that is not newer than the installed one is stale and
//      refused; frames apply newest-first by construction.
//   3. Atomic replacement: apply() builds the next snapshot completely
//      before installing it, and snapshots are immutable. A reader holding
//      snapshot N walks cells of revision N and only revision N, however
//      many frames land mid-walk — the protocol obligation the design
//      names for identity across a mid-gesture frame (§3.9, §6.8), met by
//      shape rather than by locking.
//
//   4. DECODE: the wire's compact text+marks+runs (nocx-zg3k3.2.12) is
//      expanded, here and only here, into the column-indexed cell array
//      every other module already expects — a spacer synthesised back in
//      after a wide mark, a trailing run of default columns padded out to
//      geometry.cols, and each run's packed style resolved into the named-
//      field ModelStyle/ModelColor shape the painter and its style helpers
//      read. Nothing downstream of this module ever sees the wire's own
//      spelling of a style or a row; that is what lets paint-row.ts,
//      mapping.ts and style.ts stay unchanged by a wire compaction.
//
//   5. Intake freezing: the snapshot owns everything it built (there is
//      nothing left to alias — decode already copies every fact out of the
//      frame's own strings and numbers), and apply() freezes it before
//      install so a reader can never observe a later mutation of what it
//      is holding.
//
// What it does not own: painting and run spacing (run-geometry.ts is the
// one owner of run construction, nocx-zg3k3.7 — the painter walks this
// model's cells through it), fit measurement (cell-fit.ts; a cell that
// needs a box gets one at paint time, never here), and link identity —
// the frame contract carries no links yet (the design defers OSC 8), so
// there is no link fact to model. Adding links is a schema change first.

import type {
  Color as WireColor,
  SessionFrame,
  Style as WireStyle,
} from './generated/session.frame'

/** The column footprints a committed frame carries (emulator.Width minus
 *  the 0 a committed frame does not carry — refused at intake). The
 *  enumeration is the wire's: 1 narrow, 2 wide, 3 spacerTail, 4
 *  spacerHead. */
type DeclaredWidth = 1 | 2 | 3 | 4

/** One style colour, resolved from the wire's packed integer into the
 *  named-field shape the painter's style helpers (style.ts) read: kind
 *  0 default, 1 palette (palette meaningful), 2 rgb (rgb meaningful). This
 *  is the MODEL's own vocabulary, decoupled from the wire's — the wire may
 *  keep compacting without moving anything downstream of decode. */
export interface ModelColor {
  readonly kind: 0 | 1 | 2
  readonly palette: number
  readonly rgb: { readonly r: number; readonly g: number; readonly b: number }
}

/** One resolved style, the model's own vocabulary (see ModelColor). */
export interface ModelStyle {
  readonly foreground: ModelColor
  readonly background: ModelColor
  readonly underlineColor: ModelColor
  readonly attributes: number
  readonly underline: number
}

const DEFAULT_MODEL_COLOR: ModelColor = Object.freeze({
  kind: 0,
  palette: 0,
  rgb: Object.freeze({ r: 0, g: 0, b: 0 }),
})
const DEFAULT_MODEL_STYLE: ModelStyle = Object.freeze({
  foreground: DEFAULT_MODEL_COLOR,
  background: DEFAULT_MODEL_COLOR,
  underlineColor: DEFAULT_MODEL_COLOR,
  attributes: 0,
  underline: 0,
})

/** One cell as the model serves it: the wire's text+marks+runs, resolved
 *  with the style of the run covering it, plus the column facts. `column`
 *  is the cell's position in the grid — decode promises one entry per
 *  column, so the position IS the declaration and nothing recomputes it.
 *  `span` is the footprint derived from the declared width alone: two
 *  columns for a wide cluster, one for everything else, spacers included
 *  (a spacer stands in its column; it is the painter that skips it, not
 *  the grid). */
interface ModelCell {
  readonly grapheme: string
  readonly width: DeclaredWidth
  readonly hasText: boolean
  readonly style: ModelStyle
  readonly column: number
  readonly span: number
}

/** One physical line of the grid, cells positional by column. `wrap` and
 *  `continuation` ride verbatim — they are independent facts, not each
 *  other's negation. */
interface ModelRow {
  readonly index: number
  readonly cells: readonly ModelCell[]
  readonly wrap: boolean
  readonly continuation: boolean
  cellAt(column: number): ModelCell | null
}

type FrameGeometry = SessionFrame['geometry']
type FrameCursor = SessionFrame['cursor']

/** One immutable revision of the screen. A reader holding this reference
 *  observes this revision whole, however many frames are applied after
 *  it. */
export interface ScreenSnapshot {
  readonly revision: number
  readonly geometry: FrameGeometry
  readonly cursor: FrameCursor
  readonly rows: readonly ModelRow[]
  rowAt(index: number): ModelRow | null
  cellAt(row: number, column: number): ModelCell | null
}

/** Why a frame was refused, in the wire's vocabulary. Every refusal
 *  leaves the installed revision untouched. */
export type FrameRefusal =
  | { readonly reason: 'stale-revision'; readonly revision: number; readonly current: number }
  | { readonly reason: 'malformed-geometry'; readonly detail: 'row-count' }
  | {
      readonly reason: 'malformed-row'
      readonly row: number
      readonly detail: 'column-count' | 'marks' | 'run-partition'
    }

export type ApplyResult =
  | { readonly ok: true; readonly snapshot: ScreenSnapshot }
  | { readonly ok: false; readonly refusal: FrameRefusal }

export interface CellModel {
  /** The installed revision, or null before the first accepted frame. */
  current(): ScreenSnapshot | null
  /** Validate and, on success, atomically install the frame's revision.
   *  On refusal the model is unchanged — a caller can log and drop. */
  apply(frame: SessionFrame): ApplyResult
}

/** The footprint rule, the only one there is: the declared width alone
 *  decides. A wide cluster covers its own column and the spacer after
 *  it; every other cell — narrow, spacerTail, spacerHead — stands in one
 *  column. Never measured, never rounded. */
export function columnSpan(width: DeclaredWidth): number {
  return width === 2 ? 2 : 1
}

/** Unpack one wire colour: 0 default, 1-256 a palette index plus one,
 *  257+ a packed RGB triple offset by 257 — session.frame.schema.json's
 *  $defs/color, the same three ranges internal/sessionruntime's
 *  encodeColorValue writes and internal/helper/client's
 *  decodeWireColorValue reads. */
function resolveColor(value: WireColor): ModelColor {
  if (value === 0) return DEFAULT_MODEL_COLOR
  if (value < 257) {
    return Object.freeze({ kind: 1, palette: value - 1, rgb: DEFAULT_MODEL_COLOR.rgb })
  }
  const packed = value - 257
  return Object.freeze({
    kind: 2,
    palette: 0,
    rgb: Object.freeze({ r: (packed >> 16) & 0xff, g: (packed >> 8) & 0xff, b: packed & 0xff }),
  })
}

/** Unpack one wire style: the bare integer 0 (all-default) or the
 *  5-element tuple [foreground, background, underlineColor, attributes,
 *  underline]. */
function resolveStyle(value: WireStyle): ModelStyle {
  if (value === 0) return DEFAULT_MODEL_STYLE
  const [fg, bg, ul, attributes, underline] = value
  return Object.freeze({
    foreground: resolveColor(fg),
    background: resolveColor(bg),
    underlineColor: resolveColor(ul),
    attributes,
    underline,
  })
}

/** One row's positions, derived from its own text and marks — the same
 *  arithmetic internal/sessionruntime's encodeRow ran in reverse and
 *  internal/helper/client's decodeWireRowCells mirrors: unmarked positions
 *  are exactly one codepoint and one column, and `codepoints.length` minus
 *  what the marks explicitly claim, plus the marks themselves, is how many
 *  positions there are. Returns null when the row cannot be a well-formed
 *  member of this vocabulary (a duplicate mark, a mark past the text, an
 *  unrecognised width) — the caller turns that into the row's refusal. */
interface RowPositions {
  readonly codepoints: readonly string[]
  readonly marks: ReadonlyMap<number, readonly [span: number, width: DeclaredWidth]>
  readonly count: number
}

function positionsOf(wireRow: SessionFrame['rows'][number]): RowPositions | { malformed: true } {
  const codepoints = Array.from(wireRow.text)
  const marks = new Map<number, [number, DeclaredWidth]>()
  let explicitCodepoints = 0
  for (const [position, codepointCount, width] of wireRow.marks ?? []) {
    if (marks.has(position)) return { malformed: true }
    if (width !== 1 && width !== 2 && width !== 4) return { malformed: true }
    marks.set(position, [codepointCount, width])
    explicitCodepoints += codepointCount
  }
  const count = codepoints.length - explicitCodepoints + marks.size
  if (count < 0) return { malformed: true }
  return { codepoints, marks, count }
}

/** How many columns a row's own explicit content spans: one per position,
 *  plus one more for every wide mark (its synthesised spacer). */
function explicitColumnsOf(positions: RowPositions): number {
  let extra = 0
  for (const [, width] of positions.marks.values()) {
    if (width === 2) extra++
  }
  return positions.count + extra
}

/** The public read of a row's own explicit column count — a stored block
 *  (frontend/src/scrollback/block-rows.ts) carries no frame geometry of its
 *  own, so a reader that wants one column count per row (to find the
 *  widest line and pad the rest, the way a rectangle needs) reads it here
 *  rather than re-deriving the arithmetic. A malformed row reads as 0
 *  columns — the caller's own validation (createCellModel().apply) is
 *  still what refuses a frame built from it. */
export function rowColumnsOf(wireRow: SessionFrame['rows'][number]): number {
  const positions = positionsOf(wireRow)
  return 'malformed' in positions ? 0 : explicitColumnsOf(positions)
}

/** Intake validation, one pass, before anything is built: the wire names
 *  the malformed shapes (a rows array that is not geometry.rows long, a
 *  row whose explicit content spans more columns than geometry.cols, a
 *  mark that claims more codepoints than the text has left or names a
 *  position twice, a width outside {1, 2, 4}, present runs whose lengths
 *  do not partition the row's own explicit width) and refuses them. A
 *  refusal installs nothing. */
function refusalFor(frame: SessionFrame, currentRevision: number | null): FrameRefusal | null {
  if (currentRevision !== null && frame.revision <= currentRevision) {
    return { reason: 'stale-revision', revision: frame.revision, current: currentRevision }
  }
  if (frame.rows.length !== frame.geometry.rows) {
    return { reason: 'malformed-geometry', detail: 'row-count' }
  }
  for (let r = 0; r < frame.rows.length; r++) {
    const wireRow = frame.rows[r]
    const positions = positionsOf(wireRow)
    if ('malformed' in positions) {
      return { reason: 'malformed-row', row: r, detail: 'marks' }
    }
    const explicitColumns = explicitColumnsOf(positions)
    if (explicitColumns > frame.geometry.cols) {
      return { reason: 'malformed-row', row: r, detail: 'column-count' }
    }
    if (wireRow.runs !== undefined) {
      let covered = 0
      for (const [, length] of wireRow.runs) {
        if (!Number.isInteger(length) || length < 1) {
          return { reason: 'malformed-row', row: r, detail: 'run-partition' }
        }
        covered += length
      }
      if (covered !== explicitColumns) {
        return { reason: 'malformed-row', row: r, detail: 'run-partition' }
      }
    }
  }
  return null
}

/** Build one row of the next snapshot: walk positions in order, splitting
 *  `text` by each position's codepoint count (default 1) and consulting
 *  `runs` in COLUMN order — a wide position emits its own cell and a
 *  synthesised spacer, in parallel with the run cursor, exactly the way
 *  the encoder produced them from two independent per-column reads. A
 *  shorter row (a trailing run of default columns implied rather than
 *  sent) is padded out to geometry.cols in the default style. */
function buildRow(index: number, wireRow: SessionFrame['rows'][number], cols: number): ModelRow {
  const positions = positionsOf(wireRow)
  if ('malformed' in positions) {
    throw new Error('cell-model: buildRow called on a row refusalFor already rejected')
  }
  const cells: ModelCell[] = []
  let runIndex = 0
  let runRemaining = wireRow.runs?.[0]?.[1] ?? Number.POSITIVE_INFINITY
  let runStyle = resolveStyle(wireRow.runs?.[0]?.[0] ?? 0)
  const nextRun = (): void => {
    runIndex++
    const run = wireRow.runs?.[runIndex]
    runRemaining = run !== undefined ? run[1] : Number.POSITIVE_INFINITY
    runStyle = resolveStyle(run?.[0] ?? 0)
  }
  let idx = 0
  let column = 0
  const emit = (grapheme: string, width: DeclaredWidth, hasText: boolean): void => {
    if (runRemaining === 0) nextRun()
    runRemaining--
    cells.push(
      Object.freeze({ grapheme, width, hasText, style: runStyle, column, span: columnSpan(width) }),
    )
    column++
  }
  for (let pos = 0; pos < positions.count; pos++) {
    const mark = positions.marks.get(pos)
    const span = mark?.[0] ?? 1
    const width = mark?.[1] ?? 1
    const grapheme = span > 0 ? positions.codepoints.slice(idx, idx + span).join('') : ''
    idx += span
    emit(grapheme, width, span > 0)
    if (width === 2) emit('', 3, false)
  }
  while (column < cols) {
    emit('', 1, false)
  }
  Object.freeze(cells)
  return Object.freeze({
    index,
    cells,
    wrap: wireRow.wrap ?? false,
    continuation: wireRow.continuation ?? false,
    cellAt(c: number): ModelCell | null {
      return c >= 0 && c < cells.length ? cells[c] : null
    },
  })
}

export function createCellModel(): CellModel {
  let installed: ScreenSnapshot | null = null

  return {
    current(): ScreenSnapshot | null {
      return installed
    },

    apply(frame: SessionFrame): ApplyResult {
      const refusal = refusalFor(frame, installed?.revision ?? null)
      if (refusal !== null) {
        return { ok: false, refusal }
      }
      // Decode builds every fact fresh from the frame's strings and
      // numbers — there is nothing left to alias, so freezing the result
      // (rather than the frame) is what protects an installed revision
      // from a caller mutating the frame object afterward.
      const geometry = Object.freeze({ ...frame.geometry })
      const cursor = Object.freeze({ ...frame.cursor })
      const rows: ModelRow[] = []
      for (let r = 0; r < frame.rows.length; r++) {
        rows.push(buildRow(r, frame.rows[r], geometry.cols))
      }
      Object.freeze(rows)
      const snapshot: ScreenSnapshot = Object.freeze({
        revision: frame.revision,
        geometry,
        cursor,
        rows,
        rowAt(index: number): ModelRow | null {
          return index >= 0 && index < rows.length ? rows[index] : null
        },
        cellAt(row: number, column: number): ModelCell | null {
          return row >= 0 && row < rows.length ? rows[row].cellAt(column) : null
        },
      })
      installed = snapshot
      return { ok: true, snapshot }
    },
  }
}

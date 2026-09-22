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
//      not the rectangle geometry declares (rows count, cells per row),
//      whose runs do not partition its cells exactly, or that carries the
//      unknown width 0 is malformed at the source (the schema's words) and
//      is refused — the previously installed revision stays whole.
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
//   4. Intake freezing: the snapshot aliases three things the frame owns —
//      its geometry, its cursor and its run styles — and aliasing would
//      let the caller rewrite an installed revision by mutating its frame
//      afterwards. apply() therefore deep-freezes those before install,
//      and freezes the structures it built (rows, cells, snapshot). The
//      frame is consumed: on success its geometry, cursor and styles
//      cannot be mutated later (a strict-mode attempt throws); on refusal
//      validation runs first and the frame is untouched.
//
// What it does not own: painting and run spacing (run-geometry.ts is the
// one owner of run construction, nocx-zg3k3.7 — the painter walks this
// model's cells through it), fit measurement (cell-fit.ts; a cell that
// needs a box gets one at paint time, never here), and link identity —
// the frame contract carries no links yet (the design defers OSC 8), so
// there is no link fact to model. Adding links is a schema change first.

import type { SessionFrame, Style } from './generated/session.frame'

/** The column footprints a committed frame carries (emulator.Width minus
 *  the 0 a committed frame does not carry — refused at intake). The
 *  enumeration is the wire's: 1 narrow, 2 wide, 3 spacerTail, 4
 *  spacerHead. */
type DeclaredWidth = 1 | 2 | 3 | 4

/** One cell as the model serves it: the wire tuple [grapheme, width,
 *  hasText] resolved with the style of the run covering it, plus the
 *  column facts. `column` is the cell's position in the grid — the wire
 *  promises one entry per column, so the position IS the declaration and
 *  nothing recomputes it. `span` is the footprint derived from the
 *  declared width alone: two columns for a wide cluster, one for
 *  everything else, spacers included (a spacer stands in its column; it
 *  is the painter that skips it, not the grid). */
interface ModelCell {
  readonly grapheme: string
  readonly width: DeclaredWidth
  readonly hasText: boolean
  readonly style: Style
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
      readonly detail: 'cell-count' | 'run-partition'
    }
  | { readonly reason: 'unknown-width'; readonly row: number; readonly column: number }

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

/** Deep-freeze a wire object the snapshot will alias. Idempotent: styles
 *  are shared across runs and across rows, and the second freeze of one
 *  is a cheap early exit. Cycles do not exist in wire JSON. */
function freezeDeep(value: unknown): void {
  if (typeof value !== 'object' || value === null || Object.isFrozen(value)) {
    return
  }
  for (const key of Object.keys(value)) {
    freezeDeep((value as Record<string, unknown>)[key])
  }
  Object.freeze(value)
}

/** Intake validation, one pass, before anything is built or frozen: the
 *  wire names the malformed shapes (a rows array that is not geometry.rows
 *  long, a row whose cells are not geometry.cols long, runs whose lengths
 *  do not partition the cells exactly — each length an integer of at
 *  least 1) and the width 0 a committed frame does not carry. Anything
 *  else is trusted — the sender is the runtime. A refusal freezes
 *  nothing and installs nothing. */
function refusalFor(frame: SessionFrame, currentRevision: number | null): FrameRefusal | null {
  if (currentRevision !== null && frame.revision <= currentRevision) {
    return { reason: 'stale-revision', revision: frame.revision, current: currentRevision }
  }
  if (frame.rows.length !== frame.geometry.rows) {
    return { reason: 'malformed-geometry', detail: 'row-count' }
  }
  for (let r = 0; r < frame.rows.length; r++) {
    const wireRow = frame.rows[r]
    if (wireRow.cells.length !== frame.geometry.cols) {
      return { reason: 'malformed-row', row: r, detail: 'cell-count' }
    }
    let covered = 0
    for (const [, length] of wireRow.runs) {
      if (!Number.isInteger(length) || length < 1) {
        return { reason: 'malformed-row', row: r, detail: 'run-partition' }
      }
      covered += length
    }
    if (covered !== wireRow.cells.length) {
      return { reason: 'malformed-row', row: r, detail: 'run-partition' }
    }
    for (let c = 0; c < wireRow.cells.length; c++) {
      if (wireRow.cells[c][1] === 0) {
        return { reason: 'unknown-width', row: r, column: c }
      }
    }
  }
  return null
}

/** Build one row of the next snapshot: walk runs in parallel with cells
 *  (the wire's own read — runs are adjacent, in order, and partition the
 *  cells exactly, so the run covering a cell is found by counting) and
 *  stamp each cell with its authoritative column and declared span. Runs
 *  are maximal by construction, so the covering run changes at most once
 *  per cell. */
function buildRow(index: number, wireRow: SessionFrame['rows'][number]): ModelRow {
  const cells: ModelCell[] = []
  let runIndex = 0
  let runRemaining = wireRow.runs[0][1]
  for (let c = 0; c < wireRow.cells.length; c++) {
    while (runRemaining === 0) {
      runIndex++
      runRemaining = wireRow.runs[runIndex][1]
    }
    const [grapheme, width, hasText] = wireRow.cells[c]
    cells.push({
      grapheme,
      width: width as DeclaredWidth,
      hasText,
      style: wireRow.runs[runIndex][0],
      column: c,
      span: columnSpan(width as DeclaredWidth),
    })
    runRemaining--
  }
  Object.freeze(cells)
  return Object.freeze({
    index,
    cells,
    wrap: wireRow.wrap,
    continuation: wireRow.continuation,
    cellAt(column: number): ModelCell | null {
      return column >= 0 && column < cells.length ? cells[column] : null
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
      // The snapshot is built whole, then installed with one assignment —
      // that single reference write is the entire atomicity story, and it
      // is what a mid-walk reader never observes. What the snapshot aliases
      // from the frame is frozen first, so an installed revision can never
      // be rewritten through the caller's objects.
      freezeDeep(frame.geometry)
      freezeDeep(frame.cursor)
      for (const wireRow of frame.rows) {
        for (const [style] of wireRow.runs) {
          freezeDeep(style)
        }
      }
      const rows: ModelRow[] = []
      for (let r = 0; r < frame.rows.length; r++) {
        rows.push(buildRow(r, frame.rows[r]))
      }
      Object.freeze(rows)
      const snapshot: ScreenSnapshot = Object.freeze({
        revision: frame.revision,
        geometry: frame.geometry,
        cursor: frame.cursor,
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

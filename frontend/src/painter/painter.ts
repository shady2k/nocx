// THE LIVE CELL PAINTER (nocx-zg3k3.2.4): rows, runs, cursor and THE one
// pixel-to-cell mapping, over the passive cell model (nocx-zg3k3.2.3).
//
// Design: .internal/specs/2026-09-21-the-cell-renderer-choice-design.md
// §3.9 ("Write it") and §4.1 ("The smallest correct version"). The painter
// never segments, never measures, and never re-derives run geometry: it
// walks the model's cells through run-geometry.ts (the one owner,
// nocx-zg3k3.7) with the SAME measuring authority the frozen blocks use,
// injected at the composition root. This module owns no probe of its own,
// so it cannot measure with one shaping and key with another — the defect
// class that cost four adversarial rounds (ADR-0009:145-147).
//
// Frames are applied by row-level diffing against the installed revision: a
// row whose cells and style are unchanged keeps its DOM, so a cursor move
// at a new revision moves no text, and an unchanged uniform row stays the
// single text node ADR-0009 preserves. The cursor is an overlay positioned
// by THE mapping (mapping.ts), never a glyph in the text flow.
//
// Selection, copy, IME and the aria-live channel are later slices of the
// same epic; the cutover (nocx-zg3k3.2.5) mounts the painter in place of
// xterm — this module deliberately knows nothing about how it is mounted.

import type { ScreenSnapshot } from '../cell-model'
import type { CellFit, FitCandidate } from '../scrollback/cell-fit'
import type { RunMetric } from '../scrollback/run-geometry'
import { DEFAULT_SNAPSHOT, type TerminalSnapshot } from '../scrollback/serializer'
import { createMapping, type PixelMapping } from './mapping'
import { fitCandidatesOf, paintRow } from './paint-row'
import { styleEquals } from './style'
import { devicePxToCssPx, displayDpr } from './committed-metric'

/** The measuring authority, in the shape run-geometry's metric needs. A
 *  CellFit satisfies it structurally; tests inject tables. `geometry()`'
 *  row delta is the published --term-cell-delta — the default spacing of
 *  every cell whose ink nobody measured. */
type Measurer = Pick<CellFit, 'advanceOf' | 'boxOf' | 'geometry'>

/** CellFit → RunMetric, written ONCE here so the cutover cannot coin a
 *  second conversion. Null when the fit has nowhere to measure (no mounted
 *  container, no published cell width) — the same degrade the frozen
 *  blocks ship with: runs merge by attributes alone, spacing 0. */
export function metricOf(fit: Measurer): RunMetric | null {
  const g = fit.geometry()
  if (g === null) return null
  return {
    cellWidth: g.cellWidth,
    defaultSpacing: g.rowDelta,
    signature: g.signature,
    ...measurersOf(fit),
  }
}

/** The fit's two measurers, bound ONCE per fit. The painter decides
 *  "same metric, keep the rows" by comparing them by reference, and a
 *  fresh `.bind` per apply made every revision a full repaint
 *  (nocx-zg3k3.2.14). The lint rule reads a bare method reference as an
 *  unbound `this`; the fit functions close over their cache and never
 *  touch `this`, and bind keeps the signatures RunMetric's own. */
const boundMeasurers = new WeakMap<Measurer, Pick<RunMetric, 'advanceOf' | 'boxOf'>>()
function measurersOf(fit: Measurer): Pick<RunMetric, 'advanceOf' | 'boxOf'> {
  let bound = boundMeasurers.get(fit)
  if (bound === undefined) {
    bound = { advanceOf: fit.advanceOf.bind(fit), boxOf: fit.boxOf.bind(fit) }
    boundMeasurers.set(fit, bound)
  }
  return bound
}

export interface CellPainterOptions {
  /** The element the grid is painted into. Takes the .term-grid class and
   *  hosts the cursor overlay as its last child. */
  readonly surface: HTMLElement
  /** The metric supplier, re-read at every apply: mount, font loads and
   *  zoom all change it, and a verdict must never outlive the revision
   *  that carried it. Absent or null, the painter degrades to
   *  attribute-only runs — the pre-geometry behaviour, no worse. */
  readonly metric?: () => RunMetric | null
  /** The measuring authority's batch write: every cell about to paint is
   *  measured here, BEFORE any row paints, so the metric's boxOf/advanceOf
   *  are pure cache reads during the paint (cell-fit.ts's contract, which
   *  the stored rows already keep — block-rows.ts). Without it a cluster new
   *  to the session painted at the default spacing and moved every column
   *  after it (nocx-zg3k3.2.13). Absent, nothing is measured: the degrade
   *  of an absent metric. */
  readonly warm?: (candidates: Iterable<FitCandidate>) => void
  /** The theme the wire's palette colours resolve against. Defaults to the
   *  same snapshot the frozen path falls back to. */
  readonly palette?: TerminalSnapshot
}

export interface CellPainter {
  /** Apply one installed revision. Rows whose content is unchanged keep
   *  their DOM; changed rows are repainted through run-geometry. */
  apply(snapshot: ScreenSnapshot): void
  /** Paint a selection from model coordinates, or clear it. End offsets are exclusive. */
  setSelection(
    range: {
      readonly anchor: { readonly row: number; readonly offset: number }
      readonly focus: { readonly row: number; readonly offset: number }
    } | null,
  ): void
  /** THE mapping, bound to the installed revision's committed geometry.
   *  Null before the first apply — there is nothing to map yet. */
  mapping(): PixelMapping | null
  dispose(): void
  readonly surface: HTMLElement
}

const GRID_CLASS = 'term-grid'
const CURSOR_CLASS = 'term-grid-cursor'
const SELECTION_CLASS = 'term-grid-selection'

export function createCellPainter(opts: CellPainterOptions): CellPainter {
  const surface = opts.surface
  const palette = opts.palette ?? DEFAULT_SNAPSHOT
  surface.classList.add(GRID_CLASS)

  const cursor = document.createElement('div')
  cursor.className = CURSOR_CLASS
  surface.appendChild(cursor)

  let rows: HTMLDivElement[] = []
  let installed: ScreenSnapshot | null = null
  let lastMetric: RunMetric | null = null
  let selection: {
    anchor: { row: number; offset: number }
    focus: { row: number; offset: number }
  } | null = null
  let selectionNodes: HTMLDivElement[] = []

  /** A changed metric re-verdicts every run's spacing — rule 1's output is
   *  painted output — so it repaints like a content change. The numbers
   *  are compared by value, the measurers by identity (metricOf binds them
   *  once per fit), and the fit's signature by value: a late font load or a
   *  shaping change leaves the numbers equal and the verdicts different.
   *  Identical numbers, measurers and signature keep the rows' DOM. */
  function metricChanged(current: RunMetric | null): boolean {
    if (current === null || lastMetric === null) return current !== lastMetric
    return (
      current.cellWidth !== lastMetric.cellWidth ||
      current.defaultSpacing !== lastMetric.defaultSpacing ||
      current.padY !== lastMetric.padY ||
      current.signature !== lastMetric.signature ||
      current.advanceOf !== lastMetric.advanceOf ||
      current.boxOf !== lastMetric.boxOf
    )
  }

  function apply(snapshot: ScreenSnapshot): void {
    // The metric first: reading it is what begins the fit's row context,
    // which its warm() measures against.
    const metric = opts.metric?.() ?? null
    const repaintAll = rows.length !== snapshot.rows.length || metricChanged(metric)
    const toPaint = repaintAll
      ? snapshot.rows.map((_, r) => r)
      : snapshot.rows.flatMap((row, r) => {
          const prev = installed?.rows[r]
          return prev === undefined || !rowEquals(prev, row) ? [r] : []
        })
    // All writes, then all reads: one batch for exactly the rows about to
    // paint, before the first of them does.
    if (metric !== null && toPaint.length > 0) {
      opts.warm?.(fitCandidatesOf(toPaint.map((r) => snapshot.rows[r])))
    }
    if (repaintAll) {
      for (const row of rows) row.remove()
      rows = snapshot.rows.map((modelRow) => paintRow(modelRow, { metric, palette }))
      for (const row of rows) surface.insertBefore(row, cursor)
    } else {
      for (const r of toPaint) {
        const next = paintRow(snapshot.rows[r], { metric, palette })
        rows[r].replaceWith(next)
        rows[r] = next
      }
    }
    lastMetric = metric
    installed = snapshot
    placeCursor(snapshot)
    paintSelection(snapshot)
  }

  function paintSelection(snapshot: ScreenSnapshot): void {
    for (const node of selectionNodes) node.remove()
    selectionNodes = []
    if (selection === null) return
    const ordered =
      selection.anchor.row < selection.focus.row ||
      (selection.anchor.row === selection.focus.row &&
        selection.anchor.offset <= selection.focus.offset)
        ? ([selection.anchor, selection.focus] as const)
        : ([selection.focus, selection.anchor] as const)
    const [start, end] = ordered
    const mapping = createMapping(snapshot)
    const dpr = displayDpr()
    const cellWidth = devicePxToCssPx(snapshot.geometry.cellWidthPx, dpr)
    const cellHeight = devicePxToCssPx(snapshot.geometry.cellHeightPx, dpr)
    for (let row = start.row; row <= end.row; row++) {
      if (row < 0 || row >= snapshot.rows.length) continue
      const from = row === start.row ? start.offset : 0
      const to = row === end.row ? end.offset : snapshot.rows[row].cells.length
      if (to <= from) continue
      const point = mapping.cellToPixel(from, row)
      if (point === null) continue
      const node = document.createElement('div')
      node.className = SELECTION_CLASS
      node.dataset.row = String(row)
      node.dataset.start = String(from)
      node.dataset.end = String(to)
      node.style.left = `${point.x}px`
      node.style.top = `${point.y}px`
      node.style.width = `${(to - from) * cellWidth}px`
      node.style.height = `${cellHeight}px`
      surface.insertBefore(node, cursor)
      selectionNodes.push(node)
    }
  }

  function placeCursor(snapshot: ScreenSnapshot): void {
    const position = createMapping(snapshot).cellToPixel(snapshot.cursor.x, snapshot.cursor.y)
    if (!snapshot.cursor.visible || position === null) {
      cursor.hidden = true
      return
    }
    cursor.hidden = false
    cursor.style.left = `${position.x}px`
    cursor.style.top = `${position.y}px`
    cursor.style.width = `${devicePxToCssPx(snapshot.geometry.cellWidthPx, displayDpr())}px`
    cursor.style.height = `${devicePxToCssPx(snapshot.geometry.cellHeightPx, displayDpr())}px`
  }

  return {
    apply,

    setSelection(range) {
      selection = range === null ? null : { anchor: { ...range.anchor }, focus: { ...range.focus } }
      if (installed !== null) paintSelection(installed)
    },

    mapping() {
      return installed === null ? null : createMapping(installed)
    },

    dispose() {
      for (const row of rows) row.remove()
      rows = []
      installed = null
      lastMetric = null
      selection = null
      selectionNodes = []
      cursor.remove()
      surface.classList.remove(GRID_CLASS)
    },

    get surface(): HTMLElement {
      return surface
    },
  }
}

/** Rule 2's row comparison, over the wire's own vocabulary. Rows are
 *  positional (one entry per column, promised by the frame), so index
 *  alignment IS the identity; a repaint decision costs one pass over the
 *  cells, which is a rounding error next to building the DOM. */
function rowEquals(a: ScreenSnapshot['rows'][number], b: ScreenSnapshot['rows'][number]): boolean {
  if (a.wrap !== b.wrap || a.continuation !== b.continuation) return false
  if (a.cells.length !== b.cells.length) return false
  for (let i = 0; i < a.cells.length; i++) {
    const ca = a.cells[i]
    const cb = b.cells[i]
    if (
      ca.grapheme !== cb.grapheme ||
      ca.width !== cb.width ||
      ca.hasText !== cb.hasText ||
      !styleEquals(ca.style, cb.style)
    ) {
      return false
    }
  }
  return true
}

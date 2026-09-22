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
import type { CellFit } from '../scrollback/cell-fit'
import type { RunMetric } from '../scrollback/run-geometry'
import { DEFAULT_SNAPSHOT, type TerminalSnapshot } from '../scrollback/serializer'
import { createMapping, type PixelMapping } from './mapping'
import { paintRow } from './paint-row'
import { styleEquals } from './style'

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
    // The lint rule reads a bare method reference as an unbound `this`;
    // the fit functions close over their cache and never touch `this`, and
    // bind keeps the signatures RunMetric's own.
    advanceOf: fit.advanceOf.bind(fit),
    boxOf: fit.boxOf.bind(fit),
  }
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
  /** The theme the wire's palette colours resolve against. Defaults to the
   *  same snapshot the frozen path falls back to. */
  readonly palette?: TerminalSnapshot
}

export interface CellPainter {
  /** Apply one installed revision. Rows whose content is unchanged keep
   *  their DOM; changed rows are repainted through run-geometry. */
  apply(snapshot: ScreenSnapshot): void
  /** THE mapping, bound to the installed revision's committed geometry.
   *  Null before the first apply — there is nothing to map yet. */
  mapping(): PixelMapping | null
  dispose(): void
  readonly surface: HTMLElement
}

const GRID_CLASS = 'term-grid'
const CURSOR_CLASS = 'term-grid-cursor'

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

  /** A changed metric re-verdicts every run's spacing — rule 1's output is
   *  painted output — so it repaints like a content change. Compared by
   *  value, not reference: metricOf builds a fresh wrapper per apply from
   *  the same fit, and identical numbers with identical measurers must
   *  keep the rows' DOM. */
  function metricChanged(current: RunMetric | null): boolean {
    if (current === null || lastMetric === null) return current !== lastMetric
    return (
      current.cellWidth !== lastMetric.cellWidth ||
      current.defaultSpacing !== lastMetric.defaultSpacing ||
      current.padY !== lastMetric.padY ||
      current.advanceOf !== lastMetric.advanceOf ||
      current.boxOf !== lastMetric.boxOf
    )
  }

  function apply(snapshot: ScreenSnapshot): void {
    const metric = opts.metric?.() ?? null
    if (rows.length !== snapshot.rows.length || metricChanged(metric)) {
      for (const row of rows) row.remove()
      rows = snapshot.rows.map((modelRow) => paintRow(modelRow, { metric, palette }))
      for (const row of rows) surface.insertBefore(row, cursor)
    } else {
      for (let r = 0; r < snapshot.rows.length; r++) {
        const prev = installed?.rows[r]
        if (prev === undefined || !rowEquals(prev, snapshot.rows[r])) {
          const next = paintRow(snapshot.rows[r], { metric, palette })
          rows[r].replaceWith(next)
          rows[r] = next
        }
      }
    }
    lastMetric = metric
    installed = snapshot
    placeCursor(snapshot)
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
    cursor.style.width = `${snapshot.geometry.cellWidthPx}px`
    cursor.style.height = `${snapshot.geometry.cellHeightPx}px`
  }

  return {
    apply,

    mapping() {
      return installed === null ? null : createMapping(installed)
    },

    dispose() {
      for (const row of rows) row.remove()
      rows = []
      installed = null
      lastMetric = null
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

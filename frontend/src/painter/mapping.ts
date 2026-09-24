// THE ONE PIXEL-TO-CELL MAPPING (nocx-zg3k3.2.4).
//
// ADR-0009 rule 3 makes every cell contribute exactly columns × cellWidth
// of laid-out width, so where a column sits in the painted row is
// arithmetic, never a measurement. Hit-testing, selection anchors and
// cursor placement all read THIS mapping, so the three cannot disagree —
// two surfaces owning one position is the defect shape, whichever wins by
// evaluation order (design 2026-09-21 §3.9, "Identity"; the brief's "one
// pixel-to-cell mapping").
//
// The numbers come from the frame's committed geometry (cellWidthPx,
// cellHeightPx) — the runtime's own read, the same numbers the paint's
// letter-spacing corrects the DOM to. The mapping measures nothing, exactly
// as the cell model it reads measures nothing. Zoom is not a special case:
// a new committed geometry re-binds the arithmetic, which is what the two
// zoom levels of the acceptance test exercise.

import type { ScreenSnapshot } from '../cell-model'
import { devicePxToCssPx, displayDpr } from './committed-metric'

interface PixelPoint {
  readonly x: number
  readonly y: number
}

interface CellPosition {
  readonly col: number
  readonly row: number
}

export interface PixelMapping {
  /** The top-left pixel of a cell's rectangle, or null outside the grid. */
  cellToPixel(col: number, row: number): PixelPoint | null
  /** The cell that owns a pixel: the column of the cluster whose rectangle
   *  covers x — a wide cluster's spacer column resolves back to the
   *  cluster. Null outside the grid. */
  pixelToCell(x: number, y: number): CellPosition | null
}

export function createMapping(snapshot: ScreenSnapshot): PixelMapping {
  // The committed geometry is DEVICE pixels (review round 1); the mapping
  // answers in CSS pixels, what hit-testing and the cursor paint in.
  const dpr = displayDpr()
  const cellWidth = devicePxToCssPx(snapshot.geometry.cellWidthPx, dpr)
  const pitch = devicePxToCssPx(snapshot.geometry.cellHeightPx, dpr)
  const cols = snapshot.geometry.cols
  const rows = snapshot.geometry.rows
  // An unmeasured pane (nocx-zg3k3.2.9): every frame carries a zero cell
  // pixel metric until the client's own measurement is committed, and cols/
  // rows are real while cellWidthPx/cellHeightPx are not. Left to the
  // arithmetic below, a zero pitch or cellWidth divides x/y at
  // pixelToCell(0, 0) into NaN, which is neither < 0 nor >= rows/cols and so
  // slips the bounds check silently; cellToPixel meanwhile multiplies and
  // answers every cell with the same {x:0,y:0} instead of refusing. A
  // legitimate "not measured yet" state must read as null, not as a
  // coordinate nobody asked for.
  const measured = cellWidth > 0 && pitch > 0
  return {
    cellToPixel(col: number, row: number): PixelPoint | null {
      if (!measured || col < 0 || col >= cols || row < 0 || row >= rows) return null
      return { x: col * cellWidth, y: row * pitch }
    },
    pixelToCell(x: number, y: number): CellPosition | null {
      if (!measured) return null
      const row = Math.floor(y / pitch)
      if (row < 0 || row >= rows) return null
      const col = Math.floor(x / cellWidth)
      if (col < 0 || col >= cols) return null
      return { col: clusterOf(snapshot, row, col, cols), row }
    },
  }
}

/** A spacer column belongs to its wide cluster: spacerTail to the cluster
 *  before it, spacerHead to the one after (the wire's width 3 and 4). The
 *  walks are bounded by the row's length, so a lone spacer at an edge
 *  resolves to its nearest inked neighbour. */
function clusterOf(snapshot: ScreenSnapshot, row: number, col: number, cols: number): number {
  let c = col
  while (c > 0 && snapshot.cellAt(row, c)?.width === 3) c--
  while (c < cols - 1 && snapshot.cellAt(row, c)?.width === 4) c++
  return c
}

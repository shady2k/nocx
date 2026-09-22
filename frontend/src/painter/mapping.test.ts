// Mapping tests (nocx-zg3k3.2.4): pixel to cell and back, by ADR-0009 rule
// 3 — every cell contributes exactly columns × cellWidth, so the mapping is
// arithmetic on the committed geometry and never a measurement.
//
// The acceptance cases: a wide cluster (the pixel in its spacer column must
// come back to the cluster) and a cell whose ink cell-fit SCALED (the
// mapping must read the cell's rectangle, not the ink's scaled advance) —
// each at two zoom levels, because a zoom is a new committed geometry.

// @vitest-environment jsdom

import { describe, it, expect } from 'vitest'
import { DEFAULT_SNAPSHOT } from '../scrollback/serializer'
import type { RunMetric } from '../scrollback/run-geometry'
import { createCellPainter } from './painter'
import { frameOf, snapshotOf, type CellSpec } from './fixtures'

const ZOOM1 = { cols: 4, rows: 1, cellWidthPx: 8, cellHeightPx: 20 }
const ZOOM2 = { cols: 4, rows: 1, cellWidthPx: 12.8, cellHeightPx: 25.6 }

/** ⬢ boxed at half its cell, wide 漢 plus its spacer, plain z. */
const INKY_ROW: CellSpec[] = [
  ['⬢', 1, true],
  ['漢', 2, true],
  ['', 3, false],
  ['z', 1, true],
]

function metricAt(zoom: number): RunMetric {
  return {
    cellWidth: zoom,
    defaultSpacing: 0,
    advanceOf: (chars) => (chars === '漢' ? zoom * 2 - (zoom === 8 ? 1 : 0.6) : null),
    boxOf: (chars) => (chars === '⬢' ? { cols: 1, fit: 0.5 } : null),
  }
}

/** Each test mounts its own surface and reads only inside it: jsdom keeps
 *  document.body across tests in a file, so a document-wide query would
 *  find an earlier test's grid. */
function mounted(zoom: number) {
  const surface = document.createElement('div')
  document.body.appendChild(surface)
  const painter = createCellPainter({
    surface,
    metric: () => metricAt(zoom),
    palette: DEFAULT_SNAPSHOT,
  })
  const row = (): HTMLElement => {
    const found = surface.querySelector('.term-grid-row')
    if (!(found instanceof HTMLElement)) throw new Error('painter produced no row')
    return found
  }
  return { surface, painter, mapping: () => painter.mapping(), row }
}

describe('pixel to cell and back at two zoom levels', () => {
  it('round-trips the wide cluster: the spacer column belongs to the cluster', () => {
    const { painter, mapping } = mounted(8)
    painter.apply(snapshotOf(frameOf(1, [INKY_ROW], undefined, ZOOM1)))
    const m = mapping()
    if (m === null) throw new Error('no mapping after apply')

    // The pixel in the spacer column (col 2, the wide cell's second half)
    // resolves to the cluster at col 1 — the cell the ink belongs to.
    expect(m.pixelToCell(19.5, 5)).toEqual({ col: 1, row: 0 })
    // And back: the cluster's left edge, its exact two-column rectangle.
    const p = m.cellToPixel(1, 0)
    if (p === null) throw new Error('cellToPixel refused an in-grid cell')
    expect(p).toEqual({ x: 8, y: 0 })
    expect(m.pixelToCell(p.x + 0.5, 5)?.col).toBe(1)
    expect(m.pixelToCell(p.x + 15.5, 5)?.col).toBe(1)
  })

  it('round-trips a boxed cell by its rectangle, not its scaled ink', () => {
    const { painter, mapping } = mounted(8)
    painter.apply(snapshotOf(frameOf(1, [INKY_ROW], undefined, ZOOM1)))
    const m = mapping()
    if (m === null) throw new Error('no mapping after apply')

    // The ink is scaled to half the cell (fit 0.5), so 0.75 of the way
    // across is past the ink and still inside the cell: the mapping reads
    // the cell the grid declared, never the paint.
    expect(m.pixelToCell(6, 5)).toEqual({ col: 0, row: 0 })
    const p = m.cellToPixel(0, 0)
    if (p === null) throw new Error('cellToPixel refused an in-grid cell')
    expect(m.pixelToCell(p.x + 7.5, 5)?.col).toBe(0)
  })

  it('re-derives both answers at the second zoom level', () => {
    const { painter, mapping } = mounted(12.8)
    painter.apply(snapshotOf(frameOf(1, [INKY_ROW], undefined, ZOOM2)))
    const m = mapping()
    if (m === null) throw new Error('no mapping after apply')

    const end = m.cellToPixel(3, 0)
    if (end === null) throw new Error('cellToPixel refused col 3')
    expect(end.x).toBeCloseTo(38.4, 10)
    expect(end.y).toBe(0)
    // Wide cluster at the fractional pitch: cols 1-2, spacer resolves back.
    expect(m.pixelToCell(19, 5)?.col).toBe(1)
    expect(m.pixelToCell(31, 5)?.col).toBe(1)
    // Boxed cell, rectangle not ink.
    expect(m.pixelToCell(9.6, 5)?.col).toBe(0)
    // Every CELL round-trips: cell → pixel → probe inside → same cell.
    // The spacer column is not a cell of its own — its rectangle belongs
    // to the wide cluster, which the explicit assertions above pin.
    const cells: Array<[col: number, span: number]> = [
      [0, 1],
      [1, 2],
      [3, 1],
    ]
    for (const [col, span] of cells) {
      const p = m.cellToPixel(col, 0)
      if (p === null) throw new Error(`cellToPixel refused col ${col}`)
      const probe = p.x + span * 12.8 * 0.75
      expect(m.pixelToCell(probe, 12)?.col).toBe(col)
    }
  })

  it('agrees with the painted layout: the wide run is letter-spaced to exactly its columns', () => {
    const { painter, row } = mounted(12.8)
    painter.apply(snapshotOf(frameOf(1, [INKY_ROW], undefined, ZOOM2)))
    const wide = [...row().children].find((el) => el.textContent === '漢')
    if (!(wide instanceof HTMLElement)) throw new Error('wide run was not a span')
    // spacing = 2 × 12.8 − 25, so the run lays out at exactly 25.6px = the
    // two columns the mapping answers for it.
    expect(wide.style.letterSpacing).toBe('0.6px')
  })

  it('refuses pixels and cells outside the grid, and answers the ordinary row inside it', () => {
    const PLAIN_ROW: CellSpec[] = [
      ['a', 1, true],
      ['b', 1, true],
      ['c', 1, true],
      ['d', 1, true],
    ]
    const { painter, mapping } = mounted(8)
    painter.apply(snapshotOf(frameOf(1, [PLAIN_ROW], undefined, ZOOM1)))
    const m = mapping()
    if (m === null) throw new Error('no mapping after apply')

    expect(m.pixelToCell(-0.5, 5)).toBeNull()
    expect(m.pixelToCell(32, 5)).toBeNull()
    expect(m.pixelToCell(4, 20.5)).toBeNull()
    expect(m.cellToPixel(4, 0)).toBeNull()
    expect(m.cellToPixel(0, 1)).toBeNull()
    expect(m.cellToPixel(-1, -1)).toBeNull()
    // The paired success: every column of the ordinary row answers.
    for (let col = 0; col < 4; col++) {
      const p = m.cellToPixel(col, 0)
      if (p === null) throw new Error(`cellToPixel refused col ${col}`)
      expect(m.pixelToCell(p.x + 4, 10)).toEqual({ col, row: 0 })
    }
  })

  it('answers nothing before a snapshot is applied', () => {
    const { mapping } = mounted(8)
    expect(mapping()).toBeNull()
  })
})

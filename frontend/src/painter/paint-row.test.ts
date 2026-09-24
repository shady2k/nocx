// fitCandidatesOf (nocx-2v80t.3.18): the batch step cell-fit.ts's own header
// requires — every cell a caller is about to paint, gathered BEFORE any row
// paints, so `boxOf` is a pure cache read once the paint pass starts. Without
// this wiring `warm` never runs and no glyph is ever boxed, whatever the
// metric says: the failure this file guards against.

// @vitest-environment jsdom

import { describe, it, expect } from 'vitest'
import { frameOf, snapshotOf, styleOf, type CellSpec } from './fixtures'
import { fitCandidatesOf } from './paint-row'

const BOLD = 1

describe('fitCandidatesOf', () => {
  it('names every cell that carries ink, in row-then-column order', () => {
    const rows: CellSpec[][] = [
      [
        ['a', 1, true],
        ['⬢', 1, true],
      ],
      [
        ['漢', 2, true, styleOf()],
        ['', 3, false],
      ],
    ]
    const snapshot = snapshotOf(frameOf(1, rows))

    expect(fitCandidatesOf(snapshot.rows)).toEqual([
      { chars: 'a', width: 1, face: { bold: false, italic: false } },
      { chars: '⬢', width: 1, face: { bold: false, italic: false } },
      { chars: '漢', width: 2, face: { bold: false, italic: false } },
    ])
  })

  it('never candidates a blank cell — there is no ink to measure', () => {
    const rows: CellSpec[][] = [
      [
        ['x', 1, true],
        ['', 1, false],
      ],
    ]
    const snapshot = snapshotOf(frameOf(1, rows))

    expect(fitCandidatesOf(snapshot.rows)).toEqual([
      { chars: 'x', width: 1, face: { bold: false, italic: false } },
    ])
  })

  it('carries the face a bold or italic cell will be painted with, so warm keys the same run boxOf later queries', () => {
    const rows: CellSpec[][] = [[['⟳', 1, true, styleOf({ attributes: BOLD })]]]
    const snapshot = snapshotOf(frameOf(1, rows))

    expect(fitCandidatesOf(snapshot.rows)).toEqual([
      { chars: '⟳', width: 1, face: { bold: true, italic: false } },
    ])
  })

  it('answers empty for no rows, rather than throwing', () => {
    expect(fitCandidatesOf([])).toEqual([])
  })
})

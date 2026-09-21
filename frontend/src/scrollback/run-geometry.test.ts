// Run geometry owner tests (nocx-zg3k3.7, ADR-0009 rules 1–3 and 6).
//
// The module under test is THE owner of two decisions: which adjacent cells
// merge into one run, and what letter-spacing that run carries. Everything
// here asserts the rule, not an implementation: cells in, runs out.

// @vitest-environment jsdom

import { describe, it, expect } from 'vitest'
import { runsOf, cellSpacing, type GridCell, type RunMetric } from './run-geometry'
import type { CellBox, FitFace } from './cell-fit'

const REGULAR: FitFace = { bold: false, italic: false }

interface Attr {
  fg: string | null
}

const plain: Attr = { fg: null }

const attrsEqual = (a: Attr, b: Attr): boolean => a.fg === b.fg
const faceOf = (): FitFace => REGULAR

function cell(chars: string, cols = 1, attrs: Attr = plain): GridCell<Attr> {
  return { chars, cols, attrs }
}

/** A metric that answers every cluster from one table; unlisted → null
 *  (unmeasured). cellWidth 8, row default −0.5 like a published delta. */
function metric(
  advances: Record<string, number> = {},
  boxes: Record<string, CellBox> = {},
  over: Partial<RunMetric> = {},
): RunMetric {
  return {
    cellWidth: 8,
    defaultSpacing: -0.5,
    advanceOf: (chars) => (chars in advances ? advances[chars] : null),
    boxOf: (chars) => (chars in boxes ? boxes[chars] : null),
    ...over,
  }
}

describe('cellSpacing (ADR-0009 rule 1)', () => {
  it('is columns × cellWidth − measured advance', () => {
    expect(cellSpacing(2, 8, 16, -0.5)).toBe(0)
    expect(cellSpacing(2, 8, 15, -0.5)).toBe(1)
    expect(cellSpacing(1, 8, 13.572, -0.5)).toBe(-5.572)
  })

  it('falls to the row default when nobody measured the ink', () => {
    expect(cellSpacing(1, 8, null, -0.5)).toBe(-0.5)
  })

  it('measures to markup precision, so a hair off the grid reads as on it', () => {
    // 8 − 7.99996 differs from 0 by less than the four decimals the markup
    // carries; the two spacings must be ONE number or runs split on noise.
    expect(cellSpacing(1, 8, 7.99996, -0.5)).toBe(0)
    expect(cellSpacing(1, 8, 7.99996, -0.5)).toBe(cellSpacing(1, 8, 8, -0.5))
  })
})

describe('runsOf (ADR-0009 rule 2)', () => {
  it('merges by attributes alone when nothing is measured — the degrade the frozen block shipped with', () => {
    const runs = runsOf([cell('a'), cell('b'), cell('あ', 2), cell('c')], attrsEqual, faceOf)
    expect(runs).toEqual([{ chars: 'abあc', attrs: plain, cols: 5, spacing: 0 }])
  })

  it('does not merge attributes across a measured spacing that differs', () => {
    // あ was measured onto its two columns exactly: its spacing is 0, the
    // ASCII around it carries the row default. Same attrs, different
    // spacing — the rule says these are different runs.
    const runs = runsOf(
      [cell('a'), cell('あ', 2), cell('b')],
      attrsEqual,
      faceOf,
      metric({ あ: 16 }),
    )
    expect(runs.map((r) => [r.chars, r.spacing])).toEqual([
      ['a', -0.5],
      ['あ', 0],
      ['b', -0.5],
    ])
  })

  it('merges cells whose measured spacing agrees, whatever the cluster', () => {
    // Two different clusters that land on the same spacing are one run:
    // the rule names attributes AND spacing, never the characters.
    const runs = runsOf(
      [cell('あ', 2), cell('漢', 2)],
      attrsEqual,
      faceOf,
      metric({ あ: 16, 漢: 16 }),
    )
    expect(runs).toEqual([{ chars: 'あ漢', attrs: plain, cols: 4, spacing: 0 }])
  })

  it('derives a wide cluster spacing from BOTH its columns', () => {
    const runs = runsOf([cell('あ', 2)], attrsEqual, faceOf, metric({ あ: 15 }))
    expect(runs[0]?.spacing).toBe(1) // 2 × 8 − 15
    expect(runs[0]?.cols).toBe(2)
  })

  it('keeps a boxed cell a run of its own, even against equal attrs and spacing', () => {
    // cell-fit locked the ink into a box: the box owns the advance, so the
    // run is the box and nothing may flow into it.
    const runs = runsOf(
      [cell('⬢'), cell('⬢'), cell('x')],
      attrsEqual,
      faceOf,
      metric({ '⬢': 13.572 }, { '⬢': { cols: 1, fit: 0.5894 } }),
    )
    expect(runs).toEqual([
      { chars: '⬢', attrs: plain, cols: 1, spacing: 0, box: { cols: 1, fit: 0.5894 } },
      { chars: '⬢', attrs: plain, cols: 1, spacing: 0, box: { cols: 1, fit: 0.5894 } },
      { chars: 'x', attrs: plain, cols: 1, spacing: -0.5 },
    ])
  })

  it('rejects a box verdict that is not on the cells own columns', () => {
    // "One column" for a two-column cell is a shift the grid does not have.
    const runs = runsOf(
      [cell('あ', 2)],
      attrsEqual,
      faceOf,
      metric({ あ: 20 }, { あ: { cols: 1, fit: 0.4 } }),
    )
    expect(runs[0]?.box).toBeUndefined()
    // Rejected as a box, the cell still carries its measured spacing.
    expect(runs[0]?.spacing).toBe(-4)
  })

  it('falls to the row default for ink nobody measured', () => {
    const runs = runsOf([cell('⬢'), cell('a')], attrsEqual, faceOf, metric({}))
    expect(runs).toEqual([{ chars: '⬢a', attrs: plain, cols: 2, spacing: -0.5 }])
  })

  it('never asks the measurer about a blank the walk spelled as a space', () => {
    // The spacer after a wide cell has no ink; measuring it would be a
    // cache lookup for a cluster that is not there.
    const asked: string[] = []
    const m: RunMetric = {
      cellWidth: 8,
      defaultSpacing: -0.5,
      advanceOf: (chars) => {
        asked.push(chars)
        return chars === '漢' ? 16 : null
      },
      boxOf: (chars) => {
        asked.push(`box:${chars}`)
        return null
      },
    }
    const runs = runsOf(
      [
        { chars: '漢', cols: 2, attrs: plain },
        { chars: ' ', cols: 1, attrs: plain, blank: true },
        { chars: 'x', cols: 1, attrs: plain },
      ],
      attrsEqual,
      faceOf,
      m,
    )
    expect(asked).toEqual(['box:漢', '漢', 'box:x', 'x'])
    expect(runs.map((r) => r.chars)).toEqual(['漢', ' x'])
  })
})

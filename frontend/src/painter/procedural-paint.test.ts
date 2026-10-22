// Procedural ink at the paint seam (nocx-1ebrs.15): what paintRow builds for
// box drawing and block elements, what it refuses to measure, and the two
// degrades back to the font glyph. The geometry itself is asserted in
// cell-ink.test.ts; this file asserts the painted DOM that geometry lands in —
// including the bead's regression, two vertically adjacent U+2502 rows whose
// painted bands must cover the shared edge. Per the owner's decision the
// characters STAY TEXT in the row — selection, copy, find and the
// accessibility tree read exactly what the program painted — and only the
// font glyph is invisible; the copy regressions below hold the exact string
// on a live-painted row and on a frozen row.

// @vitest-environment jsdom

import { describe, it, expect } from 'vitest'
import { DEFAULT_SNAPSHOT, type TerminalSnapshot } from '../scrollback/serializer'
import type { RunMetric } from '../scrollback/run-geometry'
import { frameOf, snapshotOf, type CellSpec } from './fixtures'
import { fitCandidatesOf, paintRow } from './paint-row'
import { proceduralSvgOf } from './cell-ink'
import { paintStoredRows } from '../scrollback/block-rows'

const palette: TerminalSnapshot = DEFAULT_SNAPSHOT

/** A metric with the published pitch: cellWidth 8, row pitch 20. */
const metric: RunMetric = {
  cellWidth: 8,
  defaultSpacing: 0,
  pitch: 20,
  advanceOf: () => null,
}

const rowOf = (cells: CellSpec[]) => snapshotOf(frameOf(1, [cells])).rows[0]

const proceduralCellsOf = (row: HTMLElement): HTMLElement[] =>
  Array.from(row.querySelectorAll<HTMLElement>('.term-cell[data-procedural]'))

describe('paintRow procedural ink (nocx-1ebrs.15)', () => {
  it('paints a run of U+2502 cells as ONE procedural box spanning the run', () => {
    const el = paintRow(
      rowOf([
        ['│', 1, true],
        ['│', 1, true],
      ]),
      { metric, palette },
    )
    const boxes = proceduralCellsOf(el)
    expect(boxes).toHaveLength(1)
    const box = boxes[0]
    expect(box.dataset.cols).toBe('2')
    // Sized by the published vars, so a re-pitch moves the box with the
    // text around it and the SVG stretches with it.
    expect(box.style.width).toBe('calc(var(--term-cell-width, 1ch) * 2)')
    expect(box.style.height).toBe('var(--term-cell-height, 1.2em)')
    // THE CHARACTER STAYS TEXT (owner decision, 2026-10-21): a text node in
    // the row, so selection, copy, find and the accessibility tree read
    // exactly what the program painted. No aria-label stands in for it.
    expect(box.textContent).toBe('││')
    expect(box.getAttribute('aria-label')).toBeNull()
    // Only the font glyph is invisible, and it paints above the ink; the ink
    // keeps the run's own colour through the box's currentColor.
    const glyph = box.querySelector<HTMLElement>('.term-cell-glyph')
    expect(glyph).not.toBeNull()
    expect(glyph?.textContent).toBe('││')
    expect(glyph?.style.color).toBe('transparent')
    expect(box.style.color).not.toBe('transparent')
    const svg = box.querySelector('svg')
    expect(svg).not.toBeNull()
    expect(svg?.getAttribute('viewBox')).toBe('0 0 16 20')
    expect(svg?.getAttribute('aria-hidden')).toBe('true')
    expect(svg?.querySelectorAll('rect').length).toBeGreaterThanOrEqual(1)
  })

  it('paints two vertically adjacent U+2502 rows whose bands cover the shared edge — no gap pixels', () => {
    // The bead's regression test, on the painted result: each row's band
    // reaches past the shared edge on the same columns, so the union covers
    // the boundary and the line reads continuous. The font glyph cannot pass
    // this: it is shorter than the row and leaves the edge unpainted.
    const first = paintRow(rowOf([['│', 1, true]]), { metric, palette })
    const second = paintRow(rowOf([['│', 1, true]]), { metric, palette })
    const bandOf = (row: HTMLElement): { x: string; y: number; h: number } | null => {
      const rects = Array.from(row.querySelectorAll('rect'))
      const vertical = rects.find((r) => {
        const y = Number(r.getAttribute('y'))
        const h = Number(r.getAttribute('height'))
        return y <= 0 && y + h >= 20
      })
      return vertical
        ? {
            x: vertical.getAttribute('x') ?? '',
            y: Number(vertical.getAttribute('y')),
            h: Number(vertical.getAttribute('height')),
          }
        : null
    }
    const top = bandOf(first)
    const bottom = bandOf(second)
    expect(top).not.toBeNull()
    expect(bottom).not.toBeNull()
    expect(top?.x).toBe(bottom?.x)
  })

  it('keeps ordinary text beside a procedural cell, and boxes only the procedural cell', () => {
    const el = paintRow(
      rowOf([
        ['a', 1, true],
        ['│', 1, true],
        ['b', 1, true],
      ]),
      { metric, palette },
    )
    expect(proceduralCellsOf(el)).toHaveLength(1)
    expect(el.textContent).toContain('a')
    expect(el.textContent).toContain('b')
    expect(el.textContent).toContain('│')
  })

  it('never candidates a procedural cell for font measurement', () => {
    const rows = snapshotOf(
      frameOf(1, [
        [
          ['│', 1, true],
          ['█', 1, true],
          ['a', 1, true],
        ],
      ]),
    ).rows
    expect(fitCandidatesOf(rows)).toEqual([
      { chars: 'a', width: 1, face: { bold: false, italic: false } },
    ])
  })

  it('degrades to the font glyph with no metric', () => {
    const el = paintRow(rowOf([['│', 1, true]]), { metric: null, palette })
    expect(proceduralCellsOf(el)).toHaveLength(0)
    expect(el.textContent).toContain('│')
  })

  it('degrades to the font glyph with no published pitch', () => {
    const el = paintRow(rowOf([['│', 1, true]]), {
      metric: { cellWidth: 8, defaultSpacing: 0, advanceOf: () => null },
      palette,
    })
    expect(proceduralCellsOf(el)).toHaveLength(0)
    expect(el.textContent).toContain('│')
  })
})

describe('proceduralSvgOf', () => {
  it('draws each cell of the run at its own offset in one coordinate space', () => {
    const svg = proceduralSvgOf('││', [1, 1], 8, 20)
    expect(svg.getAttribute('viewBox')).toBe('0 0 16 20')
    const xs = Array.from(svg.querySelectorAll('rect')).map((r) => r.getAttribute('x'))
    expect(xs).toEqual(['4', '12'])
  })

  it('paints shapes with the run’s own colour via currentColor', () => {
    const svg = proceduralSvgOf('█', [1], 8, 20)
    expect(svg.querySelector('rect')?.getAttribute('fill')).toBe('currentColor')
  })
})

/** The exact string the owner's copy regression holds, and the row that
 *  spells it: │, painted spaces, ordinary letters, two procedural runs of
 *  two cells each — the shapes a full-screen program actually draws. */
const EXACT = '│ a ─┼ b ▀▄'
const COPY_ROW: CellSpec[] = [
  ['│', 1, true],
  [' ', 1, true],
  ['a', 1, true],
  [' ', 1, true],
  ['─', 1, true],
  ['┼', 1, true],
  [' ', 1, true],
  ['b', 1, true],
  [' ', 1, true],
  ['▀', 1, true],
  ['▄', 1, true],
]

/** What a copy of el carries: the DOM Selection's own text, the path the
 *  product's copy-on-select (terminal-content.ts) and every native
 *  selection, find and accessibility reader take. */
function copiedText(el: HTMLElement): string {
  const range = document.createRange()
  range.selectNodeContents(el)
  const sel = document.getSelection()
  sel?.removeAllRanges()
  sel?.addRange(range)
  const text = sel?.toString() ?? ''
  sel?.removeAllRanges()
  return text
}

describe('the painted characters copy exactly (nocx-1ebrs.15)', () => {
  it('a live-painted row is and copies exactly │ a ─┼ b ▀▄', () => {
    const el = paintRow(rowOf(COPY_ROW), { metric, palette })
    document.body.appendChild(el)
    // EXACT equality on BOTH halves (ADR-0096: the character stays text):
    // the row's own full text — what find and every reader walk — and the
    // DOM Selection's copy of the same row, character for character.
    expect(el.textContent).toBe(EXACT)
    expect(copiedText(el)).toBe(EXACT)
    el.remove()
  })

  it('a frozen row copies as │ a ─┼ b ▀▄', () => {
    const block = document.createElement('div')
    document.body.appendChild(block)
    paintStoredRows(
      block,
      {
        lines: [{ from: 0, row: { text: EXACT } }],
        droppedRows: 0,
        lostRows: 0,
        truncated: null,
      },
      { metric, palette },
    )
    const row = block.querySelector<HTMLElement>('.term-grid-row')
    expect(row).not.toBeNull()
    expect(row!.textContent).toBe(EXACT)
    expect(copiedText(row!)).toBe(EXACT)
    block.remove()
  })
})

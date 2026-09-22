// Painter tests (nocx-zg3k3.2.4): rows, runs and cursor by the geometry
// contract (ADR-0009).
//
// The first test is the acceptance criterion that proves there is ONE owner
// of run geometry and not two: the same metric fixture is fed to the frozen
// block serializer and to the live painter, and changing the fixture — the
// merge rule's own input, through its seam — must move BOTH surfaces in
// lockstep. A painter with a private second derivation of the merge would
// pass the first assertion and fail the second.

// @vitest-environment jsdom

import { describe, it, expect } from 'vitest'
import type { SessionFrame } from '../generated/session.frame'
import { DEFAULT_SNAPSHOT, serializeLine } from '../scrollback/serializer'
import { lineWith } from '../scrollback/test-helpers'
import type { RunMetric } from '../scrollback/run-geometry'
import { createCellPainter, metricOf } from './painter'
import { frameOf, snapshotOf, styleOf, type CellSpec } from './fixtures'

/** The row the one-owner test drives: `a`, wide `漢` plus its spacer, `b`.
 *  With M1 the wide ink measures 15px in an 8px cell, so its spacing (1)
 *  splits it from both neighbours: three runs. With M2 it measures 16px, so
 *  every spacing agrees and rule 2 merges the whole row: one run. */
const MERGE_ROW: CellSpec[] = [
  ['a', 1, true],
  ['漢', 2, true],
  ['', 3, false],
  ['b', 1, true],
]
const metricWhere = (wideAdvance: number): RunMetric => ({
  cellWidth: 8,
  defaultSpacing: 0,
  advanceOf: (chars) => (chars === '漢' ? wideAdvance : null),
})
const M1 = metricWhere(15)
const M2 = metricWhere(16)

function mount(metric: () => RunMetric | null) {
  const surface = document.createElement('div')
  document.body.appendChild(surface)
  const painter = createCellPainter({ surface, metric, palette: DEFAULT_SNAPSHOT })
  return { painter, surface }
}

/** Top-level run count of a frozen row: the serializer's HTML, parsed. */
function frozenRunCount(html: string): number {
  const host = document.createElement('div')
  host.innerHTML = html
  const line = host.firstElementChild
  if (line === null) throw new Error(`serializer produced no row: ${html}`)
  return line.childNodes.length
}

/** Top-level run count of a live row: the text nodes and spans the painter
 *  built. */
function liveRow(surface: HTMLElement): HTMLElement {
  const row = surface.querySelector('.term-grid-row')
  if (!(row instanceof HTMLElement)) throw new Error('painter produced no row')
  return row
}

describe('one owner of run geometry (the acceptance test)', () => {
  it('changing the merge rule through its seam moves the frozen blocks and the live grid together', () => {
    let metric: RunMetric | null = M1
    const { painter, surface } = mount(() => metric)

    painter.apply(snapshotOf(frameOf(1, [MERGE_ROW])))
    const frozenM1 = frozenRunCount(
      serializeLine(
        DEFAULT_SNAPSHOT,
        lineWith(
          { chars: 'a' },
          { chars: '漢', width: 2 },
          { chars: '', width: 0 },
          { chars: 'b' },
        ),
        M1,
      ),
    )
    const liveM1 = liveRow(surface).childNodes.length
    expect(frozenM1).toBe(3)
    expect(liveM1).toBe(3)

    // The fixture change — the rule's spacing input — and nothing else.
    metric = M2
    painter.apply(snapshotOf(frameOf(2, [MERGE_ROW])))
    const frozenM2 = frozenRunCount(
      serializeLine(
        DEFAULT_SNAPSHOT,
        lineWith(
          { chars: 'a' },
          { chars: '漢', width: 2 },
          { chars: '', width: 0 },
          { chars: 'b' },
        ),
        M2,
      ),
    )
    const liveM2 = liveRow(surface).childNodes.length
    expect(frozenM2).toBe(1)
    expect(liveM2).toBe(1)
  })
})

describe('rows and runs', () => {
  it('carries the wide run spacing as letter-spacing, so each cell contributes columns × cellWidth (rule 3)', () => {
    const { painter, surface } = mount(() => M1)
    painter.apply(snapshotOf(frameOf(1, [MERGE_ROW])))
    const wide = [...liveRow(surface).children].find((el) => el.textContent === '漢')
    if (!(wide instanceof HTMLElement)) throw new Error('wide run was not a span')
    expect(wide.style.letterSpacing).toBe('1px')
  })

  it('leaves an ordinary row of one colour as a single text node', () => {
    const { painter, surface } = mount(() => M1)
    painter.apply(
      snapshotOf(
        frameOf(1, [
          [
            ['a', 1, true],
            ['b', 1, true],
            ['', 1, false],
          ],
        ]),
      ),
    )
    const row = liveRow(surface)
    expect(row.childNodes.length).toBe(1)
    expect(row.childNodes[0]).toBeInstanceOf(Text)
    expect(row.textContent).toBe('ab ')
  })

  it('paints trailing blanks with the run background, because a status bar paints to the edge', () => {
    const bg: RunMetric = { cellWidth: 8, defaultSpacing: 0, advanceOf: () => null, padY: 1.5 }
    const { painter, surface } = mount(() => bg)
    const painted = styleOf({ background: { kind: 2, palette: 0, rgb: { r: 20, g: 30, b: 40 } } })
    const frame = frameOf(1, [
      [
        ['a', 1, true],
        ['', 1, false],
        ['', 1, false],
      ],
    ])
    frame.rows[0].runs = [[painted, 3]] as SessionFrame['rows'][number]['runs']
    painter.apply(snapshotOf(frame))
    const row = liveRow(surface)
    const span = row.firstElementChild
    if (!(span instanceof HTMLElement)) throw new Error('painted run was not a span')
    expect(row.childNodes.length).toBe(1)
    expect(span.textContent).toBe('a  ')
    expect(span.style.background).toContain('rgb(20, 30, 40)')
    expect(span.style.paddingBlock).toBe('1.5px')
  })
})

describe('the boxed cell (rule 6: scale the ink, never the advance)', () => {
  it('draws a cell-fit box of exactly its columns, with the ink scaled inside', () => {
    const boxed: RunMetric = {
      cellWidth: 8,
      defaultSpacing: 0,
      advanceOf: () => null,
      boxOf: (chars) => (chars === '⬢' ? { cols: 1, fit: 0.5 } : null),
    }
    const { painter, surface } = mount(() => boxed)
    painter.apply(
      snapshotOf(
        frameOf(1, [
          [
            ['⬢', 1, true],
            ['a', 1, true],
          ],
        ]),
      ),
    )
    const row = liveRow(surface)
    const box = row.querySelector('.term-cell')
    if (!(box instanceof HTMLElement)) throw new Error('boxed run was not a .term-cell')
    expect(box.dataset.cols).toBe('1')
    expect(box.style.letterSpacing).toBe('')
    const ink = box.querySelector('.term-cell-ink')
    if (!(ink instanceof HTMLElement)) throw new Error('scaled ink was not a .term-cell-ink')
    expect(ink.style.getPropertyValue('--cell-fit')).toBe('0.5')
    expect(row.textContent).toBe('⬢a')
  })
})

describe('the cursor overlay', () => {
  const GEOMETRY = { cols: 4, rows: 3, cellWidthPx: 8, cellHeightPx: 20 }
  const cols = (n: number): CellSpec[][] =>
    Array.from({ length: 3 }, () => Array.from({ length: n }, () => ['', 1, false] as CellSpec))

  it('sits on the cell the runtime names, sized to the cell, placed by THE mapping', () => {
    const { painter, surface } = mount(() => null)
    painter.apply(snapshotOf(frameOf(1, cols(4), { x: 1, y: 2, visible: true }, GEOMETRY)))
    const cursor = surface.querySelector('.term-grid-cursor')
    if (!(cursor instanceof HTMLElement)) throw new Error('no cursor overlay')
    expect(cursor.hidden).toBe(false)
    // cellToPixel(1, 2) on the committed geometry: x = 1 × 8, y = 2 × 20.
    expect(cursor.style.left).toBe('8px')
    expect(cursor.style.top).toBe('40px')
    expect(cursor.style.width).toBe('8px')
    expect(cursor.style.height).toBe('20px')
  })

  it('moves without moving any text: unchanged rows keep their DOM', () => {
    const { painter, surface } = mount(() => null)
    painter.apply(snapshotOf(frameOf(1, cols(4), { x: 1, y: 2, visible: true }, GEOMETRY)))
    const before = [...surface.querySelectorAll('.term-grid-row')]
    const textBefore = before.map((r) => r.textContent)

    painter.apply(snapshotOf(frameOf(2, cols(4), { x: 2, y: 2, visible: true }, GEOMETRY)))
    const after = [...surface.querySelectorAll('.term-grid-row')]
    const cursor = surface.querySelector('.term-grid-cursor')
    if (!(cursor instanceof HTMLElement)) throw new Error('no cursor overlay')
    expect(cursor.style.left).toBe('16px')
    expect(after.map((r, i) => r === before[i])).toEqual([true, true, true])
    expect(after.map((r) => r.textContent)).toEqual(textBefore)
  })

  it('hides when the program withdrew the caret (DECTCEM)', () => {
    const { painter, surface } = mount(() => null)
    painter.apply(snapshotOf(frameOf(1, cols(4), { x: 1, y: 2, visible: true }, GEOMETRY)))
    painter.apply(snapshotOf(frameOf(2, cols(4), { x: 1, y: 2, visible: false }, GEOMETRY)))
    const cursor = surface.querySelector('.term-grid-cursor')
    if (!(cursor instanceof HTMLElement)) throw new Error('no cursor overlay')
    expect(cursor.hidden).toBe(true)
  })
})

describe('metricOf — the CellFit → RunMetric seam the cutover calls', () => {
  /** A CellFit-shaped table measurer: geometry() answers the published
   *  numbers, advanceOf/boxOf a table — createCellFit's shape, without a
   *  DOM to measure in. */
  const fit = {
    geometry: () => ({ cellWidth: 8, rowDelta: -0.5 }),
    advanceOf: (chars: string) => (chars === '漢' ? 15 : null),
    boxOf: () => null,
  }
  const REGULAR = { bold: false, italic: false }

  it('builds the metric from the fit answers, and its measurers answer', () => {
    const metric = metricOf(fit)
    expect(metric).not.toBeNull()
    expect(metric?.cellWidth).toBe(8)
    expect(metric?.defaultSpacing).toBe(-0.5)
    expect(metric?.advanceOf('漢', 2, REGULAR)).toBe(15)
    expect(metric?.advanceOf('a', 1, REGULAR)).toBeNull()
    expect(metric?.boxOf?.('a', 1, REGULAR)).toBeNull()
  })

  it('answers null while the fit has nowhere to measure', () => {
    expect(metricOf({ geometry: () => null, advanceOf: () => null, boxOf: () => null })).toBeNull()
  })

  it('drives the paint: the wide spacing comes from rowDelta and the fit advance', () => {
    const { painter, surface } = mount(() => metricOf(fit))
    painter.apply(snapshotOf(frameOf(1, [MERGE_ROW])))
    const row = liveRow(surface)
    // defaultSpacing −0.5 for a and the spacer blank, 16 − 15 = 1 for the
    // wide ink: three runs, the wide one carrying its letter-spacing.
    expect(row.childNodes.length).toBe(3)
    const wide = [...row.children].find((el) => el.textContent === '漢')
    if (!(wide instanceof HTMLElement)) throw new Error('wide run was not a span')
    expect(wide.style.letterSpacing).toBe('1px')
  })
})

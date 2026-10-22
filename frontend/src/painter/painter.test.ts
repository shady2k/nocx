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

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  DEFAULT_SNAPSHOT,
  serializeLine,
  type AnsiPalette,
  type TerminalSnapshot,
} from '../scrollback/serializer'
import { lineWith } from '../scrollback/test-helpers'
import type { RunMetric } from '../scrollback/run-geometry'
import { createCellPainter, metricOf } from './painter'
import { CARET_IDLE_MS, CARET_PHASE_ATTR } from '../caret-phase'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
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
  const painter = createCellPainter({ surface, metric, palette: () => DEFAULT_SNAPSHOT })
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
  it('publishes the accepted model column count on the grid', () => {
    const { painter, surface } = mount(() => M1)
    const snapshot = snapshotOf(frameOf(1, [MERGE_ROW]))
    painter.apply(snapshot)
    expect(surface.getAttribute('aria-colcount')).toBe(String(snapshot.geometry.cols))
  })

  it('carries the wide run spacing as letter-spacing, so each cell contributes columns × cellWidth (rule 3)', () => {
    const { painter, surface } = mount(() => M1)
    painter.apply(snapshotOf(frameOf(1, [MERGE_ROW])))
    const wide = [...liveRow(surface).children].find((el) => el.textContent === '漢')
    if (!(wide instanceof HTMLElement)) throw new Error('wide run was not a span')
    expect(wide.style.letterSpacing).toBe('1px')
  })

  it('paints a wide cluster across its two columns and no more: its spacer spells nothing', () => {
    // The wide cell already stands in both columns (span 2), so a spacer
    // painted as a space pushed every later cell one column right of the
    // model and of the mapping. The frozen path, walking xterm's line, never
    // yielded the spacer at all; the live row spells what it spells.
    const { painter, surface } = mount(() => M2)
    painter.apply(snapshotOf(frameOf(1, [MERGE_ROW])))
    const frozen = document.createElement('div')
    frozen.innerHTML = serializeLine(
      DEFAULT_SNAPSHOT,
      lineWith({ chars: 'a' }, { chars: '漢', width: 2 }, { chars: '', width: 0 }, { chars: 'b' }),
      M2,
    )
    expect(liveRow(surface).textContent).toBe('a漢b')
    expect(liveRow(surface).textContent).toBe(frozen.textContent)
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
        ['a', 1, true, painted],
        ['', 1, false, painted],
        ['', 1, false, painted],
      ],
    ])
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
    // The overlay's position IS the mapping's answer for the runtime's
    // cell — asserted as an agreement with painter.mapping(), not as
    // numbers the test recomputed.
    const m = painter.mapping()
    if (m === null) throw new Error('no mapping after apply')
    const p = m.cellToPixel(1, 2)
    if (p === null) throw new Error('cellToPixel refused the cursor cell')
    expect(cursor.style.left).toBe(`${p.x}px`)
    expect(cursor.style.top).toBe(`${p.y}px`)
    expect(cursor.style.width).toBe('8px')
    expect(cursor.style.height).toBe('20px')
  })

  // A block cursor covers its cell, so it draws the character it covers in the
  // accent colour (nocx-zg3k3.24) — an opaque block without it hides the
  // letter the person is about to overwrite. The overlay follows the cell's
  // text as the cursor moves and as the cell changes under it.
  it('carries the character of the cell it covers, and a row-tall line box', () => {
    const { painter, surface } = mount(() => null)
    const row: CellSpec[] = [
      ['a', 1, true],
      ['b', 1, true],
      ['', 1, false],
      ['d', 1, true],
    ]
    painter.apply(snapshotOf(frameOf(1, [row, row, row], { x: 1, y: 2, visible: true }, GEOMETRY)))
    const cursor = surface.querySelector('.term-grid-cursor')
    if (!(cursor instanceof HTMLElement)) throw new Error('no cursor overlay')
    expect(cursor.textContent).toBe('b')
    expect(cursor.style.lineHeight).toBe('20px')
    painter.apply(snapshotOf(frameOf(2, [row, row, row], { x: 2, y: 0, visible: true }, GEOMETRY)))
    expect(cursor.textContent).toBe('')
  })

  it('sizes the cursor in CSS pixels on a dense display: device 17x34 at dpr 2 is 8.5x17', () => {
    vi.stubGlobal('devicePixelRatio', 2)
    try {
      const { painter, surface } = mount(() => null)
      painter.apply(
        snapshotOf(
          frameOf(
            1,
            cols(4),
            { x: 1, y: 2, visible: true },
            {
              cols: 4,
              rows: 3,
              cellWidthPx: 17,
              cellHeightPx: 34,
            },
          ),
        ),
      )
      const cursor = surface.querySelector('.term-grid-cursor')
      if (!(cursor instanceof HTMLElement)) throw new Error('no cursor overlay')
      expect(cursor.hidden).toBe(false)
      // The committed metric is DEVICE pixels; the cursor paints in CSS,
      // through the one conversion the mapping uses.
      expect(cursor.style.width).toBe('8.5px')
      expect(cursor.style.height).toBe('17px')
    } finally {
      vi.unstubAllGlobals()
    }
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
    geometry: () => ({ cellWidth: 8, rowDelta: -0.5, signature: 'regular' }),
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

  /** createCellFit's contract without a DOM: boxOf/advanceOf read a cache
   *  that only warm() writes. 漢 inks 20px in a 16px pair of cells, so once
   *  measured it is a box squeezed to 0.8. */
  function coldFit() {
    const measured = new Set<string>()
    const warmed: string[] = []
    return {
      warmed,
      fit: {
        geometry: () => ({ cellWidth: 8, rowDelta: 0, signature: 'regular' }),
        warm(candidates: Iterable<{ chars: string; width: number }>) {
          for (const c of candidates) {
            warmed.push(`${c.chars}/${c.width}`)
            measured.add(c.chars)
          }
        },
        advanceOf: (chars: string) => (measured.has(chars) && chars === '漢' ? 20 : null),
        boxOf: (chars: string) =>
          measured.has(chars) && chars === '漢' ? { cols: 2, fit: 0.8 } : null,
      },
    }
  }

  it('measures a cluster new to the session before it paints it, so it lands in its box (nocx-zg3k3.2.13)', () => {
    const { fit, warmed } = coldFit()
    const surface = document.createElement('div')
    document.body.appendChild(surface)
    const painter = createCellPainter({
      surface,
      metric: () => metricOf(fit),
      warm: (candidates) => fit.warm(candidates),
      palette: () => DEFAULT_SNAPSHOT,
    })
    painter.apply(snapshotOf(frameOf(1, [MERGE_ROW])))
    expect(warmed).toContain('漢/2')
    expect(liveRow(surface).querySelector('.term-cell[data-cols="2"]')?.textContent).toBe('漢')
  })

  it('keeps an unchanged row’s DOM when the next revision changes another row (nocx-zg3k3.2.14)', () => {
    // metricOf is re-run per apply, as the live wiring does; the same fit
    // with the same numbers is the same metric, not a reason to repaint.
    const surface = document.createElement('div')
    document.body.appendChild(surface)
    const painter = createCellPainter({
      surface,
      metric: () => metricOf(fit),
      palette: () => DEFAULT_SNAPSHOT,
    })
    const first: CellSpec[] = [['x', 1, true]]
    painter.apply(snapshotOf(frameOf(1, [MERGE_ROW, first])))
    const [keptBefore, changedBefore] = surface.querySelectorAll('.term-grid-row')
    painter.apply(snapshotOf(frameOf(2, [MERGE_ROW, [['y', 1, true]]])))
    const [keptAfter, changedAfter] = surface.querySelectorAll('.term-grid-row')
    expect(keptAfter).toBe(keptBefore)
    expect(changedAfter).not.toBe(changedBefore)
    expect(changedAfter.textContent?.trimEnd()).toBe('y')
  })

  it('repaints every row when the fit’s signature changes and its numbers do not', () => {
    // A late font load or a shaping change: same cell width, same row delta,
    // different verdicts. Keeping the rows here painted the old fit's boxes
    // until the rows happened to change (codex re-review, 2026-09-28).
    let signature = 'before-font-load'
    const shifting = { ...fit, geometry: () => ({ cellWidth: 8, rowDelta: -0.5, signature }) }
    const surface = document.createElement('div')
    document.body.appendChild(surface)
    const painter = createCellPainter({
      surface,
      metric: () => metricOf(shifting),
      palette: () => DEFAULT_SNAPSHOT,
    })
    painter.apply(snapshotOf(frameOf(1, [MERGE_ROW])))
    const before = liveRow(surface)
    signature = 'after-font-load'
    painter.apply(snapshotOf(frameOf(2, [MERGE_ROW])))
    expect(liveRow(surface)).not.toBe(before)
  })
})

describe('the live grid wears the theme (nocx-zg3k3.28)', () => {
  it('paints a palette colour from the theme it is given, and repaints when the theme changes', () => {
    // mc's panels are palette 4. Built without the theme, the live grid fell
    // back to Tokyo Night's #7aa2f7 under every theme, and no theme switch
    // ever reached it.
    const withBlue = (blue: string): TerminalSnapshot => {
      const p = DEFAULT_SNAPSHOT.palette
      const palette: AnsiPalette = [
        p[0],
        p[1],
        p[2],
        p[3],
        blue,
        p[5],
        p[6],
        p[7],
        p[8],
        p[9],
        p[10],
        p[11],
        p[12],
        p[13],
        p[14],
        p[15],
      ]
      return { ...DEFAULT_SNAPSHOT, palette }
    }
    const graphite = withBlue('#2b64c8')
    const dracula = withBlue('#bd93f9')
    let theme = graphite
    const surface = document.createElement('div')
    document.body.appendChild(surface)
    const painter = createCellPainter({ surface, metric: () => null, palette: () => theme })
    const blue = styleOf({ background: { kind: 1, palette: 4, rgb: { r: 0, g: 0, b: 0 } } })
    const snapshot = snapshotOf(frameOf(1, [[['x', 1, true, blue]]]))
    const background = (): string =>
      (liveRow(surface).querySelector('span') as HTMLElement).style.backgroundColor
    painter.apply(snapshot)
    expect(background()).toBe('rgb(43, 100, 200)')
    theme = dracula
    painter.apply(snapshot)
    expect(background()).toBe('rgb(189, 147, 249)')
  })
})

describe('live selection paint (nocx-zg3k3.4.4)', () => {
  it('highlights the model endpoints and retains them when a new frame paints', () => {
    const { painter, surface } = mount(() => null)
    const first = frameOf(
      1,
      [
        [
          ['a', 1, true],
          ['漢', 2, true],
          ['', 3, false],
          ['b', 1, true],
        ],
      ],
      { x: 0, y: 0, visible: false },
      { cols: 4, rows: 1, cellWidthPx: 8, cellHeightPx: 20 },
    )
    const snapshot = snapshotOf(first)
    painter.apply(snapshot)
    painter.setSelection({ anchor: { row: 0, offset: 1 }, focus: { row: 0, offset: 3 } })
    const highlight = surface.querySelector<HTMLElement>('.term-grid-selection')
    expect(highlight).not.toBeNull()
    expect(highlight?.style.left).toBe('8px')
    expect(highlight?.style.width).toBe('16px')
    expect(highlight?.style.top).toBe('0px')
    expect(highlight?.style.height).toBe('20px')
    painter.apply(
      snapshotOf(
        frameOf(
          2,
          [
            [
              ['z', 1, true],
              ['z', 1, true],
              ['z', 1, true],
              ['z', 1, true],
            ],
          ],
          { x: 0, y: 0, visible: false },
          { cols: 4, rows: 1, cellWidthPx: 8, cellHeightPx: 20 },
        ),
      ),
    )
    expect(surface.querySelector('.term-grid-selection')).not.toBeNull()
    expect(surface.querySelector('.term-grid-row')?.getAttribute('aria-selected')).toBe('true')
    painter.setSelection(null)
    expect(surface.querySelector('.term-grid-selection')).toBeNull()
  })
})

describe('accessible live rows (nocx-zg3k3.4.2)', () => {
  it('moves keyboard focus through readable rows without activating them', () => {
    const { painter, surface } = mount(() => null)
    painter.apply(snapshotOf(frameOf(1, [[['a', 1, true]], [['b', 1, true]], [['c', 1, true]]])))
    const rows = [...surface.querySelectorAll<HTMLElement>('.term-grid-row')]
    expect(surface.getAttribute('role')).toBe('grid')
    expect(rows.map((row) => [row.getAttribute('role'), row.textContent, row.tabIndex])).toEqual([
      ['row', 'a', 0],
      ['row', 'b', -1],
      ['row', 'c', -1],
    ])
    rows[0].focus()
    rows[0].dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }))
    expect(document.activeElement).toBe(rows[1])
    expect(rows[1].getAttribute('aria-current')).toBe('true')
    expect(rows[1].getAttribute('aria-selected')).toBe('false')
    expect(rows[1].getAttribute('data-focus-visible')).toBe('true')
  })

  it('exposes updates without a live announcement channel and ignores identical frames', () => {
    const { painter, surface } = mount(() => null)
    const snapshot = snapshotOf(frameOf(1, [[['readable row', 1, true]]]))
    painter.apply(snapshot)
    const row = surface.querySelector('.term-grid-row')
    expect(row?.textContent).toBe('readable row')
    expect(row?.getAttribute('aria-selected')).toBe('false')
    expect(surface.querySelector('[aria-live]')).toBeNull()
    painter.apply(snapshot)
    expect(surface.querySelector('.term-grid-row')).toBe(row)
  })
})

describe('the pointer answers who owns the click (nocx-1ebrs.24)', () => {
  /** One accepted frame, with the program's mouse-reporting bit as the
   *  backend measured it (1000/1002/1003 ORed together in
   *  internal/emulator/ghostty — the SAME fact the selection gesture already
   *  reads, never a second derivation of the escape sequences). */
  const withTracking = (tracking: boolean) =>
    snapshotOf(
      frameOf(
        1,
        [[['a', 1, true]]],
        undefined,
        {},
        {
          bufferKind: 'alternate',
          bufferInstance: '1',
          baseRow: 0,
          bracketedPaste: false,
          mouseTracking: tracking,
          title: null,
          cwd: null,
        },
      ),
    )

  it('shows the arrow while the program reports the mouse, and the I-beam otherwise', () => {
    // A program with mouse reporting on owns the click: the pointer must stop
    // promising a text selection it will not get. Other terminals show the
    // arrow in exactly this state and the I-beam in exactly the other, and
    // nocx showed the I-beam in both, because nothing set a cursor at all and
    // `auto` over the rows' text is an I-beam.
    const { painter, surface } = mount(() => null)
    painter.apply(withTracking(true))
    expect(surface.dataset.pointer).toBe('program')
    painter.apply(withTracking(false))
    expect(surface.dataset.pointer).toBe('text')
    painter.dispose()
  })

  it('keeps the state on the surface the pointer is over, not on a row it may replace', () => {
    // Rows are replaced one at a time by the level diff, so a state carried by
    // a row would be lost on the frame that touched it. The surface is the one
    // element that outlives every revision.
    const { painter, surface } = mount(() => null)
    painter.apply(withTracking(true))
    const stale = surface.firstElementChild
    painter.apply(withTracking(true))
    expect(surface.dataset.pointer).toBe('program')
    expect(stale?.isConnected).toBe(true)
    painter.dispose()
    expect(surface.dataset.pointer).toBeUndefined()
  })
})

describe('the armed link underline (nocx-1ebrs.48)', () => {
  // ADR-0072 took xterm's own hover underline away with xterm itself; the
  // armed provider's spans were left with nothing to paint them. The
  // underline is an overlay like the cursor and the selection — positioned
  // by THE mapping, over exactly the span's cells — and `from`/`to` arrive
  // in the grammar's own units (UTF-16 offsets into the row's text), the
  // same numbers `LivePolicy.ranges` speaks.
  const URL_ROW: CellSpec[] = [...'xhttps://e.co'].map((ch) => [ch, 1, true] as CellSpec)

  it('underlines exactly the span\u2019s cells, in the grammar\u2019s UTF-16 units', () => {
    const { painter, surface } = mount(() => null)
    painter.apply(
      snapshotOf(frameOf(1, [URL_ROW], { x: 0, y: 0, visible: false }, { cols: 13, rows: 1 })),
    )
    painter.setLinkHighlight({ row: 0, from: 1, to: 13 })
    const link = surface.querySelector<HTMLElement>('.term-grid-link')
    expect(link).not.toBeNull()
    // The `x` before the url is not the link's: column 1 through 12.
    expect(link?.style.left).toBe('8px')
    expect(link?.style.width).toBe('96px')
    expect(link?.style.top).toBe('0px')
    expect(link?.style.height).toBe('20px')
    expect(link?.dataset.row).toBe('0')
    expect(link?.dataset.start).toBe('1')
    expect(link?.dataset.end).toBe('13')
    painter.dispose()
  })

  it('carries a wide cell whole: the underline covers its columns, not its code units', () => {
    // `a漢b`: the wide cluster is one code unit and two columns. A span
    // naming it in UTF-16 (1..2) must underline both of its columns.
    const { painter, surface } = mount(() => null)
    painter.apply(
      snapshotOf(
        frameOf(1, [
          [
            ['a', 1, true],
            ['漢', 2, true],
            ['', 3, false],
            ['b', 1, true],
          ],
        ]),
      ),
    )
    painter.setLinkHighlight({ row: 0, from: 1, to: 2 })
    const link = surface.querySelector<HTMLElement>('.term-grid-link')
    expect(link?.style.left).toBe('8px')
    expect(link?.style.width).toBe('16px')
    // And `b` — text offset 2, because the spacer column spells no
    // character — still lands on column 3, its own column.
    painter.setLinkHighlight({ row: 0, from: 2, to: 3 })
    const b = surface.querySelector<HTMLElement>('.term-grid-link')
    expect(b?.style.left).toBe('24px')
    expect(b?.style.width).toBe('8px')
    painter.dispose()
  })

  it('clears when told the pointer left the link, and stays clear', () => {
    const { painter, surface } = mount(() => null)
    painter.apply(
      snapshotOf(frameOf(1, [URL_ROW], { x: 0, y: 0, visible: false }, { cols: 13, rows: 1 })),
    )
    painter.setLinkHighlight({ row: 0, from: 1, to: 13 })
    expect(surface.querySelector('.term-grid-link')).not.toBeNull()
    painter.setLinkHighlight(null)
    expect(surface.querySelector('.term-grid-link')).toBeNull()
    // Clearing twice is the steady state, not a second removal of one node.
    painter.setLinkHighlight(null)
    expect(surface.querySelector('.term-grid-link')).toBeNull()
    painter.dispose()
  })

  it('keeps the underline across a repaint that keeps the row, and drops it when the row goes', () => {
    const { painter, surface } = mount(() => null)
    painter.apply(
      snapshotOf(frameOf(1, [URL_ROW], { x: 0, y: 0, visible: false }, { cols: 13, rows: 1 })),
    )
    painter.setLinkHighlight({ row: 0, from: 1, to: 13 })
    // A new revision whose row is unchanged keeps its DOM, so the overlay
    // would survive anyway; a PALETTE change repaints every row, and the
    // underline has to come back with them.
    painter.apply(
      snapshotOf(frameOf(2, [URL_ROW], { x: 0, y: 0, visible: false }, { cols: 13, rows: 1 })),
    )
    expect(surface.querySelector('.term-grid-link')).not.toBeNull()
    const shrunk = frameOf(
      3,
      [[['z', 1, true]]],
      { x: 0, y: 0, visible: false },
      { cols: 1, rows: 1 },
    )
    painter.apply(snapshotOf(shrunk))
    expect(surface.querySelector('.term-grid-link')).toBeNull()
    painter.dispose()
  })

  it('takes no overlay for a row the grid does not have, and removes everything on dispose', () => {
    const { painter, surface } = mount(() => null)
    painter.apply(
      snapshotOf(frameOf(1, [URL_ROW], { x: 0, y: 0, visible: false }, { cols: 13, rows: 1 })),
    )
    painter.setLinkHighlight({ row: 5, from: 1, to: 13 })
    expect(surface.querySelector('.term-grid-link')).toBeNull()
    painter.setLinkHighlight({ row: 0, from: 1, to: 13 })
    expect(surface.querySelector('.term-grid-link')).not.toBeNull()
    painter.dispose()
    expect(surface.querySelector('.term-grid-link')).toBeNull()
  })
})

// The pitch is a paint input (nocx-1ebrs.15): the procedural ink bakes the
// row pitch into its geometry, so a republished pitch must repaint rows the
// way a metric change does — even though the cells, and the row comparison,
// are unchanged.

describe('the published pitch is a paint input (nocx-1ebrs.15)', () => {
  const fit = {
    geometry: () => ({ cellWidth: 8, rowDelta: -0.5, signature: 'regular' }),
    advanceOf: () => null,
    boxOf: () => null,
  }
  it('repaints unchanged rows when the pitch changes', () => {
    let pitch: number | undefined = 20
    const shifting = {
      ...fit,
      geometry: () => ({ cellWidth: 8, rowDelta: -0.5, signature: 's', pitch }),
    }
    const surface = document.createElement('div')
    document.body.appendChild(surface)
    const painter = createCellPainter({
      surface,
      metric: () => metricOf(shifting),
      palette: () => DEFAULT_SNAPSHOT,
    })
    painter.apply(snapshotOf(frameOf(1, [MERGE_ROW])))
    const before = liveRow(surface)
    pitch = 24
    painter.apply(snapshotOf(frameOf(2, [MERGE_ROW])))
    expect(liveRow(surface)).not.toBe(before)
  })

  it('carries the pitch into RunMetric through metricOf', () => {
    const withPitch = {
      ...fit,
      geometry: () => ({ cellWidth: 8, rowDelta: -0.5, signature: 's', pitch: 20 }),
    }
    expect(metricOf(withPitch)?.pitch).toBe(20)
    expect(metricOf(fit)?.pitch).toBeUndefined()
  })
})

describe('the grid cursor holds still while input arrives (nocx-1ebrs.37)', () => {
  // The rule the composer's caret already follows (nocx-1ebrs.31), applied to
  // the painter's overlay: solid while keys arrive and while the program
  // writes at the cursor, blinking again after the idle bound. A grid cursor
  // caught mid-off by a keystroke reads as a dropped key, the one thing a
  // cursor must never look like.
  //
  // The phase is asserted through the attribute the painter writes — the
  // same seam the stylesheet consumes — never through a class name. What the
  // stylesheet does with it is a stylesheet fact and is checked as one, in
  // the last test of this block.
  const GEOMETRY = { cols: 4, rows: 3, cellWidthPx: 8, cellHeightPx: 20 }
  const BLANK: CellSpec = ['', 1, false]

  /** Three rows whose only text is `first` in the top-left cell. */
  const rowsWith = (first: string): CellSpec[][] => [
    [[first, 1, true], BLANK, BLANK, BLANK],
    [BLANK, BLANK, BLANK, BLANK],
    [BLANK, BLANK, BLANK, BLANK],
  ]
  const at = (x: number, y: number) => ({ x, y, visible: true as const })

  const caretOf = (surface: HTMLElement): string | null =>
    surface.querySelector<HTMLElement>('.term-grid-cursor')?.getAttribute(CARET_PHASE_ATTR) ?? null

  /** A cursor that has been sitting quiet long enough to be blinking: the
   *  state every pane opens in and returns to. */
  const quiet = (painter: ReturnType<typeof createCellPainter>): void => {
    painter.apply(snapshotOf(frameOf(1, rowsWith('a'), at(0, 0), GEOMETRY)))
    vi.advanceTimersByTime(CARET_IDLE_MS)
  }

  beforeEach(() => {
    // Only the idle timer is faked, the way the composer's own file does it:
    // the painter schedules nothing else.
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('blinks from the first frame and stays blinking while nothing arrives', () => {
    const { painter, surface } = mount(() => null)
    quiet(painter)
    expect(caretOf(surface)).toBe('blinking')
    vi.advanceTimersByTime(CARET_IDLE_MS * 3)
    expect(caretOf(surface)).toBe('blinking')
  })

  it('a keystroke leaves the cursor solid, and the blink resumes after the idle bound', () => {
    const { painter, surface } = mount(() => null)
    quiet(painter)

    painter.noteInput() // the seam the intent path calls for every key, text and paste

    expect(caretOf(surface)).toBe('solid')
    vi.advanceTimersByTime(CARET_IDLE_MS - 1)
    expect(caretOf(surface)).toBe('solid')
    vi.advanceTimersByTime(1)
    expect(caretOf(surface)).toBe('blinking')
  })

  it('a program write at the cursor is input too: the covered character changed', () => {
    const { painter, surface } = mount(() => null)
    quiet(painter)

    // The frame a shell's echo produces: the cell under the cursor now
    // spells what was typed.
    painter.apply(snapshotOf(frameOf(2, rowsWith('b'), at(0, 0), GEOMETRY)))

    expect(caretOf(surface)).toBe('solid')
    vi.advanceTimersByTime(CARET_IDLE_MS)
    expect(caretOf(surface)).toBe('blinking')
  })

  it('the cursor moving under a frame is the program writing at it too', () => {
    const { painter, surface } = mount(() => null)
    quiet(painter)

    painter.apply(snapshotOf(frameOf(2, rowsWith('a'), at(1, 0), GEOMETRY)))

    expect(caretOf(surface)).toBe('solid')
  })

  it('a frame that changes nothing at the cursor wakes nothing', () => {
    // A theme switch re-applies the installed snapshot; a program may write
    // far from the cursor. Neither is activity AT the cursor, and a cursor
    // that went solid for them would blink whenever the screen moved.
    const { painter, surface } = mount(() => null)
    quiet(painter)

    const rows = rowsWith('a')
    rows[2] = [BLANK, BLANK, BLANK, ['z', 1, true]] // row the cursor is not on
    painter.apply(snapshotOf(frameOf(2, rows, at(0, 0), GEOMETRY)))

    expect(caretOf(surface)).toBe('blinking')
  })

  it('stays solid for as long as input and writes keep arriving', () => {
    const { painter, surface } = mount(() => null)
    quiet(painter)

    // Four acts, each inside the idle bound of the one before: a person
    // typing into a streaming program must never see the cursor flicker.
    for (const ch of 'abcd') {
      painter.noteInput()
      painter.apply(snapshotOf(frameOf(2, rowsWith(ch), at(0, 0), GEOMETRY)))
      vi.advanceTimersByTime(CARET_IDLE_MS - 100)
      expect(caretOf(surface)).toBe('solid')
    }
    vi.advanceTimersByTime(CARET_IDLE_MS)
    expect(caretOf(surface)).toBe('blinking')
  })

  it('a write restarts a half-spent idle wait', () => {
    const { painter, surface } = mount(() => null)
    quiet(painter)

    painter.noteInput()
    vi.advanceTimersByTime(300)
    painter.apply(snapshotOf(frameOf(2, rowsWith('b'), at(0, 0), GEOMETRY)))
    vi.advanceTimersByTime(300)
    expect(caretOf(surface)).toBe('solid') // 600 since the key, 300 since the write
    vi.advanceTimersByTime(200)
    expect(caretOf(surface)).toBe('blinking')
  })

  it('disposing stops the wait: no idle timer outlives the painter', () => {
    const { painter } = mount(() => null)
    quiet(painter)
    painter.noteInput()
    expect(vi.getTimerCount()).toBe(1)

    painter.dispose()
    expect(vi.getTimerCount()).toBe(0)
  })

  it('the phase is what the stylesheet reads: solid suspends the blink, focused runs it', () => {
    // jsdom computes no CSS and runs no animation, so the stylesheet is the
    // only place this fact exists — read as text, the way the composer's
    // file checks its own half.
    const css = readFileSync(resolve(import.meta.dirname ?? '.', '..', 'style.css'), 'utf8')
    const blink =
      /\.terminal-live-inner:focus-within \.term-grid-cursor \{[^}]*animation:\s*term-cursor-blink/.exec(
        css,
      )
    expect(blink).not.toBeNull()
    const solid =
      /\.terminal-live-inner:focus-within \.term-grid-cursor\[data-caret='solid'\] \{[^}]*animation:\s*none/.exec(
        css,
      )
    expect(solid).not.toBeNull()
  })
})

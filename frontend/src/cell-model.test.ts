// The client cell model tests (nocx-zg3k3.2.3).
//
// The module under test is the client's model of the screen: passive,
// versioned, and never recomputed locally. A frame arrives, the model
// serves its cells — each carries the authoritative column, span, style and
// continuation the runtime declared — and browser layout is never asked
// about any of it. These tests assert the contract's own statements:
//
//   - columns come only from what the backend declared, proven by a
//     measurement that contradicts the declared width and the columns do
//     not move;
//   - a wide cluster, a combining cluster and a zero-width joiner sequence
//     each occupy the columns the runtime said, at two zoom levels and two
//     device-pixel ratios;
//   - a frame at a later revision replaces the model atomically: a walk
//     that began on the old revision never observes a cell of the new one.
//
// The frame shape under test is contracts/session.frame.schema.json,
// generated into src/generated/session.frame.ts, in the compact
// text+marks+runs form nocx-zg3k3.2.12 brought it to. Fixtures are built
// through painter/fixtures.ts's wireRowOf/frameOf, which run the SAME
// algorithm the Go encoder does (one CellSpec per COLUMN, spacer included)
// rather than hand-writing the wire's text/marks/runs — a fixture that
// built the wire by hand would drift from the encoder the day either
// changed.

import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import type { SessionFrame } from './generated/session.frame'
import {
  columnSpan,
  createCellModel,
  type ApplyResult,
  type CellModel,
  type FrameRefusal,
  type ModelStyle,
  type ScreenSnapshot,
} from './cell-model'
import { type CellSpec, frameOf as fixtureFrameOf, styleOf, wireRowOf } from './painter/fixtures'

// --- fixtures ---------------------------------------------------------------

function style(palette: number): ModelStyle {
  return styleOf({ foreground: { kind: 1, palette, rgb: { r: 0, g: 0, b: 0 } } })
}

const PLAIN = style(7)

/** frameOf here additionally accepts a rowsCount override, the way the old
 *  local helper did (a malformed row-count fixture needs a geometry that
 *  disagrees with the rows array it is handed). */
function frame(
  revision: number,
  rows: CellSpec[][],
  cols: number,
  rowsCount = rows.length,
): SessionFrame {
  return fixtureFrameOf(revision, rows, undefined, { cols, rows: rowsCount })
}

/** A frame of blank plain cells — the ordinary frame every refusal test
 *  pairs with an accepted one. `rowsCount` sets the GEOMETRY's declared row
 *  count independently of the single row actually built, which is how the
 *  "wrong row count" refusal fixture creates its mismatch. */
function blankFrame(revision: number, cols: number, rowsCount = 1): SessionFrame {
  const cells: CellSpec[] = Array.from({ length: cols }, () => ['', 1, false])
  return frame(revision, [cells], cols, rowsCount)
}

function applied(model: CellModel, f: SessionFrame): ScreenSnapshot {
  const result = model.apply(f)
  expect(result.ok).toBe(true)
  return (result as { ok: true; snapshot: ScreenSnapshot }).snapshot
}

function refused(model: CellModel, f: SessionFrame): FrameRefusal {
  const result: ApplyResult = model.apply(f)
  expect(result.ok).toBe(false)
  return (result as { ok: false; refusal: FrameRefusal }).refusal
}

// --- the browser the model must not consult ---------------------------------

/** What a layout engine would answer for a cluster at a zoom level and a
 *  device-pixel ratio. Deliberately wrong for the clusters below: the wide
 *  cluster and the ZWJ sequence claim one column's advance, the combining
 *  cluster claims two. If the model ever asked, these answers would move
 *  the grid. */
function browserAdvancePx(
  grapheme: string,
  cellWidthPx: number,
  zoom: number,
  dpr: number,
): number {
  switch (grapheme) {
    case 'é':
      // Claims wide: two cells' advance.
      return 2 * cellWidthPx * zoom * dpr
    default:
      // Claims narrow: one cell's advance whatever the cluster really is.
      return cellWidthPx * zoom * dpr
  }
}

const ZOOMS = [1, 2]
const DPRS = [1, 2]

// --- columns come only from what the backend declared ------------------------

describe('declared columns, contradicting measurement', () => {
  it('keeps the declared accounting when the browser measurement disagrees', () => {
    // One row, three columns: a wide cluster the runtime gave width 2, its
    // spacer tail, then a narrow cell.
    const f = frame(
      1,
      [
        [
          ['汉', 2, true],
          ['', 3, false],
          ['a', 1, true],
        ],
      ],
      3,
    )
    const model = createCellModel()
    const snapshot = applied(model, f)

    // The disagreement is real: the browser would grant this cluster one
    // column (one cell's advance); the runtime declared two.
    const browserWouldGrant = Math.max(1, Math.round(browserAdvancePx('汉', 8, 1, 1) / (8 * 1 * 1)))
    expect(browserWouldGrant).toBe(1)

    const wide = snapshot.cellAt(0, 0)
    const tail = snapshot.cellAt(0, 1)
    const after = snapshot.cellAt(0, 2)
    expect(wide?.grapheme).toBe('汉')
    expect(wide?.width).toBe(2)
    expect(wide?.span).toBe(2)
    expect(wide?.column).toBe(0)
    expect(tail?.width).toBe(3)
    expect(tail?.grapheme).toBe('')
    expect(tail?.hasText).toBe(false)
    expect(after?.grapheme).toBe('a')
    expect(after?.column).toBe(2)
  })

  for (const zoom of ZOOMS) {
    for (const dpr of DPRS) {
      it(`holds wide, combining and ZWJ clusters at zoom ${zoom}, dpr ${dpr}`, () => {
        // Columns 0-1: wide cluster + spacer. Column 2: combining cluster.
        // Columns 3-4: ZWJ sequence + spacer. Column 5: narrow.
        const f = frame(
          1,
          [
            [
              ['汉', 2, true],
              ['', 3, false],
              ['é', 1, true],
              ['👩‍🚀', 2, true],
              ['', 3, false],
              ['b', 1, true],
            ],
          ],
          6,
        )
        const model = createCellModel()
        const snapshot = applied(model, f)

        // Every cluster above is one the browser mis-measures at this
        // zoom/dpr: wide and ZWJ claim one column, combining claims two.
        expect(browserAdvancePx('汉', 8, zoom, dpr)).toBe(8 * zoom * dpr)
        expect(browserAdvancePx('é', 8, zoom, dpr)).toBe(2 * 8 * zoom * dpr)
        expect(browserAdvancePx('👩‍🚀', 8, zoom, dpr)).toBe(8 * zoom * dpr)

        expect(snapshot.cellAt(0, 0)?.grapheme).toBe('汉')
        expect(snapshot.cellAt(0, 0)?.span).toBe(2)
        expect(snapshot.cellAt(0, 1)?.width).toBe(3)
        expect(snapshot.cellAt(0, 2)?.grapheme).toBe('é')
        expect(snapshot.cellAt(0, 2)?.span).toBe(1)
        expect(snapshot.cellAt(0, 3)?.grapheme).toBe('👩‍🚀')
        expect(snapshot.cellAt(0, 3)?.span).toBe(2)
        expect(snapshot.cellAt(0, 4)?.width).toBe(3)
        expect(snapshot.cellAt(0, 5)?.grapheme).toBe('b')
        expect(snapshot.cellAt(0, 5)?.column).toBe(5)
      })
    }
  }
})

describe('columnSpan', () => {
  it('derives the footprint from the declared width alone', () => {
    expect(columnSpan(1)).toBe(1)
    expect(columnSpan(2)).toBe(2)
    // Spacers stand in one column each; they are not rendered, but they
    // are positions.
    expect(columnSpan(3)).toBe(1)
    expect(columnSpan(4)).toBe(1)
  })
})

// --- the facts a cell carries -------------------------------------------------

describe('cell facts', () => {
  it('reads each cell style from the run covering it', () => {
    const red = style(1)
    const green = style(2)
    const f = frame(
      1,
      [
        [
          ['a', 1, true, red],
          ['b', 1, true, red],
          ['c', 1, true, green],
          ['d', 1, true, green],
          ['e', 1, true, green],
        ],
      ],
      5,
    )
    const model = createCellModel()
    const snapshot = applied(model, f)

    const styles = snapshot.rows[0].cells.map((c) => c.style)
    expect(styles[0]).toEqual(red)
    expect(styles[1]).toEqual(red)
    expect(styles[2]).toEqual(green)
    expect(styles[3]).toEqual(green)
    expect(styles[4]).toEqual(green)
  })

  it('carries hasText separately from an empty grapheme', () => {
    const inverse = style(4)
    const f = frame(
      1,
      [
        [
          ['', 1, false, inverse],
          ['x', 1, true, PLAIN],
        ],
      ],
      2,
    )
    const model = createCellModel()
    const snapshot = applied(model, f)

    const backgroundOnly = snapshot.cellAt(0, 0)
    expect(backgroundOnly?.grapheme).toBe('')
    expect(backgroundOnly?.hasText).toBe(false)
    expect(backgroundOnly?.style).toEqual(inverse)
    expect(snapshot.cellAt(0, 1)?.hasText).toBe(true)
  })

  it('carries wrap and continuation as independent facts', () => {
    // The last physical line of a wrapped sequence: wrap false and
    // continuation true — not each other's negation (the schema's own
    // example).
    const one: CellSpec = ['a', 1, true]
    const f: SessionFrame = {
      revision: 1,
      geometry: { cols: 1, rows: 3, cellWidthPx: 8, cellHeightPx: 20, revision: 1 },
      cursor: { x: 0, y: 0, visible: false },
      rows: [
        wireRowOf([one], true, false),
        wireRowOf([one], true, true),
        wireRowOf([one], false, true),
      ],
    }
    const model = createCellModel()
    const snapshot = applied(model, f)

    expect(snapshot.rows.map((r) => [r.wrap, r.continuation])).toEqual([
      [true, false],
      [true, true],
      [false, true],
    ])
  })

  it('serves cursor and geometry with the revision they were read at', () => {
    const f = frame(9, [[['a', 1, true]]], 1)
    const model = createCellModel()
    const snapshot = applied(model, f)

    expect(snapshot.revision).toBe(9)
    expect(snapshot.geometry.cols).toBe(1)
    expect(snapshot.geometry.cellWidthPx).toBe(8)
    expect(snapshot.cursor).toEqual({ x: 0, y: 0, visible: false })
  })
})

// --- the wire's own compaction: implied trailing default, omitted runs ------

describe('the row a shorter wire message implies', () => {
  it('pads a row shorter than geometry.cols with the default style', () => {
    const f: SessionFrame = {
      revision: 1,
      geometry: { cols: 5, rows: 1, cellWidthPx: 8, cellHeightPx: 20, revision: 1 },
      cursor: { x: 0, y: 0, visible: false },
      rows: [{ text: 'ab' }],
    }
    const model = createCellModel()
    const snapshot = applied(model, f)

    expect(snapshot.rows[0].cells).toHaveLength(5)
    expect(snapshot.cellAt(0, 0)?.grapheme).toBe('a')
    expect(snapshot.cellAt(0, 1)?.grapheme).toBe('b')
    for (const column of [2, 3, 4]) {
      const padded = snapshot.cellAt(0, column)
      expect(padded?.grapheme).toBe('')
      expect(padded?.hasText).toBe(false)
      expect(padded?.width).toBe(1)
      // The padded style is the wire's own default.
      expect(padded?.style.foreground.kind).toBe(0)
      expect(padded?.style.attributes).toBe(0)
    }
  })

  it('reads an absent runs as one implicit default run over the whole explicit width', () => {
    const f: SessionFrame = {
      revision: 1,
      geometry: { cols: 3, rows: 1, cellWidthPx: 8, cellHeightPx: 20, revision: 1 },
      cursor: { x: 0, y: 0, visible: false },
      rows: [{ text: 'abc' }],
    }
    const model = createCellModel()
    const snapshot = applied(model, f)
    for (const column of [0, 1, 2]) {
      expect(snapshot.cellAt(0, column)?.style.foreground.kind).toBe(0)
      expect(snapshot.cellAt(0, column)?.style.attributes).toBe(0)
    }
  })

  it('reads an absent marks as every position one codepoint, one column', () => {
    const f: SessionFrame = {
      revision: 1,
      geometry: { cols: 3, rows: 1, cellWidthPx: 8, cellHeightPx: 20, revision: 1 },
      cursor: { x: 0, y: 0, visible: false },
      rows: [{ text: 'xyz' }],
    }
    const model = createCellModel()
    const snapshot = applied(model, f)
    expect(snapshot.rows[0].cells.map((c) => [c.grapheme, c.width])).toEqual([
      ['x', 1],
      ['y', 1],
      ['z', 1],
    ])
  })
})

// --- the structure behind the criterion ---------------------------------------

// The first criterion is that the model's columns come only from what the
// backend declared — and the concrete test above demonstrates it on one
// disagreement. But a demonstration passes against a model that DID
// measure, as long as today's arithmetic happened to agree. The criterion's
// real content is structural: there is no measurement input at all. This
// scan guards that — it fails the day somebody wires the browser in,
// whatever the arithmetic says that day.

const srcDir = import.meta.dirname ?? resolve(new URL('.', import.meta.url).pathname)

describe('no measurement reach (the columns are the wire arithmetic)', () => {
  // Every spelling of "ask the browser instead of the wire". Each entry
  // names what is forbidden and why; a second source of column truth is
  // the defect this module exists to prevent, so if a future change
  // genuinely needs one of these, the design decision must be re-made out
  // loud in the module header — not smuggled past this test.
  //
  // The measuring-authority names below are matched only in import form
  // (`from '...'`), not as substrings: the module header legitimately
  // names cell-fit.ts and run-geometry.ts in its ownership docs, and a
  // substring match would fail on the prose. An actual import — even a
  // commented-out one — is the thing worth stopping.
  const forbidden: ReadonlyArray<readonly [string, RegExp, string]> = [
    [
      'getBoundingClientRect',
      /\bgetBoundingClientRect\b/,
      'a layout read answers in pixels from the render tree, not columns from the runtime',
    ],
    [
      'measureText',
      /\bmeasureText\b/,
      'a canvas text measure invents an advance the wire never sent',
    ],
    [
      'offsetWidth',
      /\boffsetWidth\b/,
      'a layout-width read is a font verdict the declared width exists to replace',
    ],
    [
      'clientWidth',
      /\bclientWidth\b/,
      'a layout-width read is a font verdict the declared width exists to replace',
    ],
    [
      'devicePixelRatio',
      /\bdevicePixelRatio\b/,
      'a display-density input would let zoom state move the grid',
    ],
    [
      'getComputedStyle',
      /\bgetComputedStyle\b/,
      'a computed-style read reaches for typographic facts the frame must carry',
    ],
    [
      'canvas',
      /\bcanvas\b|CanvasRenderingContext2D/i,
      'a canvas context is how a measurement would be taken — there is nothing here to take one with',
    ],
    [
      'measuring-authority imports',
      /from '[^']*(cell-fit|cell-metric|run-geometry)'/,
      'the measuring authorities stay with the painter; the model must not even import them',
    ],
  ]

  it('cell-model.ts reaches none of the browser measurement surfaces', () => {
    const src = readFileSync(resolve(srcDir, 'cell-model.ts'), 'utf8')
    for (const [name, pattern, why] of forbidden) {
      expect(src, `cell-model.ts must not reach ${name}: ${why}`).not.toMatch(pattern)
    }
  })
})

// --- atomic, versioned replacement --------------------------------------------

describe('atomic per-revision replacement', () => {
  it('a walk that began on the old revision never sees the new one', () => {
    const first = frame(
      1,
      [
        [
          ['r1-a', 1, true],
          ['r1-b', 1, true],
        ],
        [
          ['r1-c', 1, true],
          ['r1-d', 1, true],
        ],
      ],
      2,
      2,
    )
    const second = frame(
      2,
      [
        [
          ['r2-a', 1, true],
          ['r2-b', 1, true],
        ],
        [
          ['r2-c', 1, true],
          ['r2-d', 1, true],
        ],
      ],
      2,
      2,
    )
    const model = createCellModel()
    const snapshot1 = applied(model, first)

    // Walk snapshot1 while applying frame 2 mid-walk. A model that mutated
    // in place would show r2 cells before the walk ended.
    const seen: string[] = []
    for (const r of snapshot1.rows) {
      for (const c of r.cells) {
        if (seen.length === 1) {
          const result = model.apply(second)
          expect(result.ok).toBe(true)
        }
        seen.push(c.grapheme)
      }
    }
    expect(seen).toEqual(['r1-a', 'r1-b', 'r1-c', 'r1-d'])

    // The held snapshot is untouched; the model serves the new one.
    expect(snapshot1.revision).toBe(1)
    expect(snapshot1.cellAt(1, 0)?.grapheme).toBe('r1-c')
    expect(model.current()?.revision).toBe(2)
    expect(model.current()?.cellAt(1, 0)?.grapheme).toBe('r2-c')
  })

  it('a refused frame leaves the previous revision fully installed', () => {
    const good = blankFrame(5, 2)
    const model = createCellModel()
    applied(model, good)

    // Malformed in three different ways: wrong row count, a run partition
    // that does not sum to the row's own explicit width, and a row whose
    // explicit content is wider than geometry.cols.
    const wrongHeight = blankFrame(6, 2, 3)
    const badPartition: SessionFrame = {
      revision: 7,
      geometry: { cols: 1, rows: 1, cellWidthPx: 8, cellHeightPx: 20, revision: 7 },
      cursor: { x: 0, y: 0, visible: false },
      rows: [{ text: 'a', runs: [[0, 2]] }],
    }
    const tooWide: SessionFrame = {
      revision: 8,
      geometry: { cols: 1, rows: 1, cellWidthPx: 8, cellHeightPx: 20, revision: 8 },
      cursor: { x: 0, y: 0, visible: false },
      rows: [{ text: 'ab' }],
    }

    for (const malformed of [wrongHeight, badPartition, tooWide]) {
      const refusal = refused(model, malformed)
      expect(refusal.reason).not.toBe('stale-revision')
      expect(model.current()?.revision).toBe(5)
      expect(model.current()?.rows).toHaveLength(1)
      expect(model.current()?.rows[0].cells).toHaveLength(2)
    }
  })

  it('refuses a stale revision and keeps the newer one', () => {
    const model = createCellModel()
    applied(model, blankFrame(10, 1))

    const stale = refused(model, blankFrame(9, 1))
    expect(stale).toEqual({ reason: 'stale-revision', revision: 9, current: 10 })
    const equal = refused(model, blankFrame(10, 1))
    expect(equal).toEqual({ reason: 'stale-revision', revision: 10, current: 10 })
    expect(model.current()?.revision).toBe(10)
  })

  it('accepts an ordinary frame after refusals', () => {
    const model = createCellModel()
    applied(model, blankFrame(1, 1))
    refused(model, blankFrame(0, 1))

    const snapshot = applied(model, blankFrame(2, 1))
    expect(snapshot.revision).toBe(2)
    expect(model.current()?.revision).toBe(2)
  })

  it('never aliases the frame it decoded: mutating the caller’s objects after apply does not move the snapshot', () => {
    const red = style(1)
    const f = frame(1, [[['a', 1, true, red]]], 1)
    const model = createCellModel()
    const snapshot = applied(model, f)

    // Decode already copied every fact out of red/f's own strings and
    // numbers into fresh objects, so mutating the caller's inputs
    // afterward — something a wire-aliasing design would have had to
    // freeze against — simply does not reach the installed snapshot.
    f.geometry.cols = 80
    f.cursor.x = 7

    expect(snapshot.cellAt(0, 0)?.style.foreground.palette).toBe(1)
    expect(snapshot.geometry.cols).toBe(1)
    expect(snapshot.cursor.x).toBe(0)
  })

  it('freezes what it installs: a caller cannot rewrite an installed snapshot', () => {
    const f = frame(1, [[['a', 1, true]]], 1)
    const model = createCellModel()
    const snapshot = applied(model, f)

    expect(() => {
      ;(snapshot as { revision: number }).revision = 99
    }).toThrow(TypeError)
    expect(() => {
      ;(snapshot.geometry as { cols: number }).cols = 99
    }).toThrow(TypeError)
    expect(() => {
      ;(snapshot.rows[0].cells[0] as { grapheme: string }).grapheme = 'z'
    }).toThrow(TypeError)
  })

  it('refuses a run that covers less than one column', () => {
    const model = createCellModel()
    applied(model, blankFrame(1, 1))

    // The schema: run length is at least 1 — a run that covered nothing
    // would be sender noise. The zero-length run sits beside a run whose
    // lengths still sum to the row's explicit width, so the partition sum
    // alone does not catch it.
    const zeroRun: SessionFrame = {
      revision: 2,
      geometry: { cols: 3, rows: 1, cellWidthPx: 8, cellHeightPx: 20, revision: 2 },
      cursor: { x: 0, y: 0, visible: false },
      rows: [
        {
          text: 'abc',
          runs: [
            [[1, 0, 0, 0, 0], 0],
            [0, 3],
          ],
        },
      ],
    }
    const refusal = refused(model, zeroRun)
    expect(refusal.reason).toBe('malformed-row')
    expect(model.current()?.revision).toBe(1)

    // Paired ordinary frame succeeds.
    const snapshot = applied(model, blankFrame(3, 1))
    expect(snapshot.revision).toBe(3)
  })
})

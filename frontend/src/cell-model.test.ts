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
  captureLiveSelection,
  columnSpan,
  createCellModel,
  padSnapshotToColumns,
  snapshotRowText,
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
function defaultFrameState(): SessionFrame['state'] {
  return {
    bufferKind: 'normal',
    bufferInstance: '1',
    baseRow: 0,
    bracketedPaste: false,
    mouseTracking: false,
    pointerShape: null,
    title: null,
    cwd: null,
  }
}

function frame(
  revision: number,
  rows: CellSpec[][],
  cols: number,
  rowsCount = rows.length,
  wraps: readonly boolean[] = [],
): SessionFrame {
  const built = fixtureFrameOf(revision, rows, undefined, { cols, rows: rowsCount })
  if (wraps.length > 0) {
    built.rows = built.rows.map((wireRow, index) => ({
      ...wireRow,
      ...(wraps[index] ? { wrap: true } : {}),
    }))
  }
  return built
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

describe('backend terminal state', () => {
  it('installs buffer identity and input-routing modes with the same accepted revision', () => {
    const frame = fixtureFrameOf(
      7,
      [[['x', 1, true]]],
      { x: 0, y: 0, visible: true },
      {},
      {
        bufferKind: 'alternate',
        bufferInstance: '18446744073709551615',
        baseRow: 0,
        bracketedPaste: true,
        mouseTracking: true,
        pointerShape: null,
        title: null,
        cwd: null,
      },
    )
    const model = createCellModel()
    const result = model.apply(frame)
    expect(result.ok).toBe(true)
    if (!result.ok) return
    expect(result.snapshot.state).toEqual({
      bufferKind: 'alternate',
      bufferInstance: '18446744073709551615',
      baseRow: 0,
      bracketedPaste: true,
      mouseTracking: true,
      pointerShape: null,
      title: null,
      cwd: null,
    })
    expect(Object.isFrozen(result.snapshot.state)).toBe(true)
  })

  it('refuses an absolute row range outside JSON-safe coordinates without replacing the prior revision', () => {
    const model = createCellModel()
    const first = model.apply(fixtureFrameOf(1, [[['a', 1, true]]]))
    expect(first.ok).toBe(true)
    const malformed = fixtureFrameOf(2, [[['b', 1, true]]])
    malformed.state.baseRow = Number.MAX_SAFE_INTEGER
    const second = model.apply(malformed)
    expect(second).toEqual({ ok: false, refusal: { reason: 'malformed-state' } })
    expect(model.current()?.revision).toBe(1)
  })

  it('carries the pointer shape the program asked for, and judges only its type', () => {
    // The shape is the pane's cursor (nocx-1ebrs.24), so it rides the accepted
    // revision like every other mode and is readable from the snapshot the
    // painter's rules are written against.
    const model = createCellModel()
    const asked = fixtureFrameOf(1, [[['x', 1, true]]], undefined, undefined, {
      bufferKind: 'normal',
      bufferInstance: '1',
      baseRow: 0,
      bracketedPaste: false,
      mouseTracking: true,
      pointerShape: 'wait',
      title: null,
      cwd: null,
    })
    const accepted = model.apply(asked)
    expect(accepted.ok).toBe(true)
    if (accepted.ok) expect(accepted.snapshot.state.pointerShape).toBe('wait')

    // A shape is cosmetic: a value that is neither a name nor null is refused
    // as malformed state, because it is not the wire's shape at all...
    const wrongType = fixtureFrameOf(2, [[['y', 1, true]]], undefined, undefined, {
      bufferKind: 'normal',
      bufferInstance: '1',
      baseRow: 0,
      bracketedPaste: false,
      mouseTracking: true,
      pointerShape: 7 as unknown as SessionFrame['state']['pointerShape'],
      title: null,
      cwd: null,
    })
    expect(model.apply(wrongType)).toEqual({ ok: false, refusal: { reason: 'malformed-state' } })
    expect(model.current()?.revision).toBe(1)

    // ...while a name the stylesheet has no rule for still installs: the pane
    // keeps the base pointer instead of freezing the screen over a cursor.
    const unknownName = fixtureFrameOf(2, [[['z', 1, true]]], undefined, undefined, {
      bufferKind: 'normal',
      bufferInstance: '1',
      baseRow: 0,
      bracketedPaste: false,
      mouseTracking: true,
      pointerShape: 'not-a-cursor' as unknown as SessionFrame['state']['pointerShape'],
      title: null,
      cwd: null,
    })
    const installed = model.apply(unknownName)
    expect(installed.ok).toBe(true)
  })

  it('refuses malformed mode or buffer identity without replacing the prior revision', () => {
    const model = createCellModel()
    const firstFrame = fixtureFrameOf(1, [[['a', 1, true]]])
    const first = model.apply(firstFrame)
    expect(first.ok).toBe(true)
    const malformed = fixtureFrameOf(2, [[['b', 1, true]]], undefined, undefined, {
      bufferKind: 'normal',
      bufferInstance: '0',
      baseRow: 0,
      bracketedPaste: false,
      mouseTracking: false,
      pointerShape: null,
      title: null,
      cwd: null,
    })
    const second = model.apply(malformed)
    expect(second).toEqual({ ok: false, refusal: { reason: 'malformed-state' } })
    expect(model.current()?.revision).toBe(1)
  })
})

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

  it('reads a colour on either side of the palette/RGB boundary as the schema says', () => {
    // session.frame.schema.json $defs/color: 1-256 is a palette index plus
    // one, 257+ an RGB triple offset by 257. The last palette entry and RGB
    // black sit on either side of that line (a hand-planted `value <= 257`
    // at stage acceptance read black as palette 256, and nothing noticed).
    const lastPalette = styleOf({
      foreground: { kind: 1, palette: 255, rgb: { r: 0, g: 0, b: 0 } },
    })
    const rgbBlack = styleOf({ foreground: { kind: 2, palette: 0, rgb: { r: 0, g: 0, b: 0 } } })
    const rgbWhite = styleOf({
      foreground: { kind: 2, palette: 0, rgb: { r: 255, g: 255, b: 255 } },
    })
    const snapshot = applied(
      createCellModel(),
      frame(
        1,
        [
          [
            ['a', 1, true, lastPalette],
            ['b', 1, true, rgbBlack],
            ['c', 1, true, rgbWhite],
          ],
        ],
        3,
      ),
    )

    const fg = snapshot.rows[0].cells.map((c) => c.style.foreground)
    expect(fg[0]).toEqual({ kind: 1, palette: 255, rgb: { r: 0, g: 0, b: 0 } })
    expect(fg[1]).toEqual({ kind: 2, palette: 0, rgb: { r: 0, g: 0, b: 0 } })
    expect(fg[2]).toEqual({ kind: 2, palette: 0, rgb: { r: 255, g: 255, b: 255 } })
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
      state: defaultFrameState(),
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
      state: defaultFrameState(),
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
      state: defaultFrameState(),
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
      state: defaultFrameState(),
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
      state: defaultFrameState(),
      rows: [{ text: 'a', runs: [[0, 2]] }],
    }
    const tooWide: SessionFrame = {
      revision: 8,
      geometry: { cols: 1, rows: 1, cellWidthPx: 8, cellHeightPx: 20, revision: 8 },
      cursor: { x: 0, y: 0, visible: false },
      state: defaultFrameState(),
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

  it('a malformed final row cannot partially replace earlier rows or advance the revision', () => {
    const model = createCellModel()
    const previous = applied(model, frame(1, [[['old', 1, true]], [['tail', 1, true]]], 4))
    const next = frame(
      2,
      [
        [['new', 1, true]],
        [
          ['界', 2, true],
          ['', 3, false],
          ['é', 1, true],
        ],
      ],
      4,
    )
    const marks = next.rows[1].marks
    next.rows[1].marks = [[99, 1, 1]]
    expect(refused(model, next)).toEqual({ reason: 'malformed-row', row: 1, detail: 'marks' })
    expect(model.current()).toBe(previous)
    expect(model.current()?.cellAt(0, 0)?.grapheme).toBe('old')
    expect(model.current()?.cellAt(1, 0)?.grapheme).toBe('tail')

    next.rows[1].marks = marks
    const accepted = applied(model, next)
    expect(accepted.revision).toBe(2)
    expect(accepted.cellAt(0, 0)?.grapheme).toBe('new')
    expect(accepted.cellAt(1, 0)?.grapheme).toBe('界')
    expect(accepted.cellAt(1, 2)?.grapheme).toBe('é')
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
    const f = frame(
      1,
      [
        [
          ['界', 2, true, red],
          ['', 3, false, red],
          ['é', 1, true, red],
        ],
      ],
      3,
    )
    const model = createCellModel()
    const snapshot = applied(model, f)

    // Decode already copied every fact out of red/f's own strings and
    // numbers into fresh objects, so mutating the caller's inputs
    // afterward — something a wire-aliasing design would have had to
    // freeze against — simply does not reach the installed snapshot.
    f.geometry.cols = 80
    f.cursor.x = 7
    f.rows[0].text = 'changed'
    f.rows[0].marks![0][2] = 1
    f.rows[0].runs![0][0] = 0

    expect(snapshot.cellAt(0, 0)?.style.foreground.palette).toBe(1)
    expect(snapshot.geometry.cols).toBe(3)
    expect(snapshot.cursor.x).toBe(0)
    expect(snapshot.cellAt(0, 0)?.grapheme).toBe('界')
    expect(snapshot.cellAt(0, 0)?.width).toBe(2)
    expect(snapshot.cellAt(0, 1)?.width).toBe(3)
    expect(snapshot.cellAt(0, 2)?.grapheme).toBe('é')
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
      state: defaultFrameState(),
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

  it('refuses a mark whose position is past the row, and keeps the previous revision', () => {
    const model = createCellModel()
    applied(model, blankFrame(1, 2))

    // Hand-written: wireRowOf only ever emits a mark for a real column, so
    // the fixture encoder cannot produce a mark past the row's own
    // position sequence. text 'a' is one position (position 0); the mark
    // names position 10, which positionsOf's own count (1) never reaches —
    // buildRow's position loop runs 0..0 and simply never looks the mark
    // up, so today it is silently dropped and 'a' decodes as a narrow cell
    // plus padding instead of the wide cluster the mark declared.
    const markPastRow: SessionFrame = {
      revision: 2,
      geometry: { cols: 2, rows: 1, cellWidthPx: 8, cellHeightPx: 20, revision: 2 },
      cursor: { x: 0, y: 0, visible: false },
      state: defaultFrameState(),
      rows: [{ text: 'a', marks: [[10, 1, 2]] }],
    }
    const refusal = refused(model, markPastRow)
    expect(refusal.reason).toBe('malformed-row')
    expect(model.current()?.revision).toBe(1)

    // Paired ordinary frame still succeeds.
    const snapshot = applied(model, blankFrame(3, 2))
    expect(snapshot.revision).toBe(3)
  })

  it('refuses a mark whose position is not a whole number, and keeps the previous revision', () => {
    const model = createCellModel()
    applied(model, blankFrame(1, 2))
    // Hand-written, as above: the wire is runtime JSON, and a position of
    // 0.5 is inside [0, count) yet names no position, so no lookup ever
    // consumes it (codex re-review, 2026-09-28).
    const fractional: SessionFrame = {
      revision: 2,
      geometry: { cols: 4, rows: 1, cellWidthPx: 8, cellHeightPx: 20, revision: 2 },
      cursor: { x: 0, y: 0, visible: false },
      state: defaultFrameState(),
      rows: [{ text: 'ab', marks: [[0.5, 1, 2]] }],
    }
    expect(refused(model, fractional).reason).toBe('malformed-row')
    expect(model.current()?.revision).toBe(1)
  })

  it('refuses a row whose marks and text disagree, and keeps the previous revision', () => {
    const model = createCellModel()
    applied(model, blankFrame(1, 2))

    // Hand-written: text 'ab' is two codepoints, but the mark at position 0
    // claims 3 — more than the row's own text has, at any position. The
    // row's derived position count (codepoints.length - explicitCodepoints
    // + marks.size = 2 - 3 + 1 = 0) lands at zero rather than negative, so
    // today's `count < 0` guard does not catch it: positionsOf accepts a
    // row with no positions at all, buildRow's loop never runs, and 'ab' is
    // silently dropped, padded away instead of refused.
    const markOverclaims: SessionFrame = {
      revision: 2,
      geometry: { cols: 2, rows: 1, cellWidthPx: 8, cellHeightPx: 20, revision: 2 },
      cursor: { x: 0, y: 0, visible: false },
      state: defaultFrameState(),
      rows: [{ text: 'ab', marks: [[0, 3, 1]] }],
    }
    const refusal = refused(model, markOverclaims)
    expect(refusal.reason).toBe('malformed-row')
    expect(model.current()?.revision).toBe(1)

    // Paired ordinary frame still succeeds.
    const snapshot = applied(model, blankFrame(3, 2))
    expect(snapshot.revision).toBe(3)
  })

  it('accepts an ordinary row with valid marks: wide, combining, zero-codepoint', () => {
    // The paired positive case, so the two refusals above cannot be
    // satisfied by a check that rejects every marked row. Built through the
    // fixture encoder (wireRowOf), which only ever emits marks a real
    // position can carry: a wide cluster + its spacer, a combining cluster
    // (two codepoints, one position), a zero-codepoint blank (Cell.HasText
    // false, still a real position), and a plain narrow cell.
    const f = frame(
      1,
      [
        [
          ['汉', 2, true],
          ['', 3, false],
          ['é', 1, true],
          ['', 1, false],
          ['b', 1, true],
        ],
      ],
      5,
    )
    const model = createCellModel()
    const snapshot = applied(model, f)

    expect(snapshot.cellAt(0, 0)?.grapheme).toBe('汉')
    expect(snapshot.cellAt(0, 0)?.width).toBe(2)
    expect(snapshot.cellAt(0, 0)?.span).toBe(2)
    expect(snapshot.cellAt(0, 1)?.width).toBe(3)
    expect(snapshot.cellAt(0, 1)?.hasText).toBe(false)
    expect(snapshot.cellAt(0, 2)?.grapheme).toBe('é')
    expect(snapshot.cellAt(0, 2)?.hasText).toBe(true)
    expect(snapshot.cellAt(0, 2)?.width).toBe(1)
    expect(snapshot.cellAt(0, 3)?.grapheme).toBe('')
    expect(snapshot.cellAt(0, 3)?.hasText).toBe(false)
    expect(snapshot.cellAt(0, 3)?.width).toBe(1)
    expect(snapshot.cellAt(0, 4)?.grapheme).toBe('b')
    expect(snapshot.cellAt(0, 4)?.column).toBe(4)
  })
})

describe('live selection snapshots', () => {
  it('copies from the captured revision after a newer frame is installed', () => {
    const model = createCellModel()
    const snapshot = applied(
      model,
      frame(
        1,
        [
          [
            ['A', 1, true],
            ['界', 2, true],
            ['B', 1, true],
          ],
        ],
        4,
      ),
    )
    const selection = captureLiveSelection(
      'pane-1',
      snapshot,
      { row: 0, offset: 0 },
      { row: 0, offset: 4 },
    )
    applied(
      model,
      frame(
        2,
        [
          [
            ['X', 1, true],
            ['Y', 1, true],
            ['Z', 1, true],
            ['!', 1, true],
          ],
        ],
        4,
      ),
    )
    expect(selection.copy()).toBe('A界B')
    expect(selection.anchor).toMatchObject({ surfaceId: 'pane-1', revision: 1, row: 0, offset: 0 })
  })

  it('joins soft wraps, keeps hard newlines, and normalizes wide grapheme boundaries', () => {
    const snapshot = applied(
      createCellModel(),
      frame(
        1,
        [
          [
            ['A', 1, true],
            ['界', 2, true],
          ],
          [
            ['B', 1, true],
            ['C', 1, true],
            ['D', 1, true],
          ],
        ],
        3,
        2,
        [true, false],
      ),
    )
    expect(
      captureLiveSelection('pane', snapshot, { row: 0, offset: 1 }, { row: 1, offset: 3 }).copy(),
    ).toBe('界BCD')
    const hard = applied(
      createCellModel(),
      frame(
        1,
        [
          [
            ['A', 1, true],
            ['B', 1, true],
            ['C', 1, true],
          ],
          [
            ['D', 1, true],
            ['E', 1, true],
            ['F', 1, true],
          ],
        ],
        3,
      ),
    )
    expect(
      captureLiveSelection('pane', hard, { row: 0, offset: 1 }, { row: 1, offset: 2 }).copy(),
    ).toBe('BC\nDE')
  })
})

describe('snapshot row text', () => {
  it('uses backend-declared graphemes and omits non-text spacer cells', () => {
    const snapshot = applied(
      createCellModel(),
      frame(
        1,
        [
          [
            [' ', 1, true],
            ['A', 1, true],
            ['界', 2, true],
            ['', 3, false],
            ['B', 1, true],
          ],
        ],
        5,
      ),
    )

    expect(snapshotRowText(snapshot, 0)).toBe('A界B')
    expect(snapshotRowText(snapshot, 1)).toBeNull()
  })
})

// THE PANE'S RECTANGLE IS NOT THE PROGRAM'S GRID (nocx-1ebrs.10). A frame
// carries the width the PROGRAM runs at; a pane presents the width IT has
// measured, until the program's own grid catches up. The two are the same
// number in the steady state, which is why the widening is a presentation
// detail and not an intake one — the model still holds the frame the backend
// published.
describe('a revision presented on a wider grid', () => {
  function twoRows(cols: number): ScreenSnapshot {
    return applied(
      createCellModel(),
      frame(
        1,
        [
          [
            ['A', 1, true],
            ['B', 1, true],
          ],
          [
            ['C', 1, true],
            ['界', 2, true],
            ['', 3, false],
          ],
        ],
        cols,
      ),
    )
  }

  it('keeps every cell, adds blank default-styled columns, and reads the same text', () => {
    const narrow = twoRows(3)
    expect(narrow.rows[0].cells).toHaveLength(3)

    const wide = padSnapshotToColumns(narrow, 80)

    expect(wide.geometry.cols).toBe(80)
    for (const row of wide.rows) {
      expect(row.cells).toHaveLength(80)
      // The content is untouched, in place, and every added column is the
      // model's own blank.
      expect(row.cells.slice(0, 3)).toEqual(narrow.rows[row.index].cells.slice(0, 3))
      expect(row.cells.slice(3).every((cell) => !cell.hasText && cell.grapheme === '')).toBe(true)
      // A blank column carries the DEFAULT style, never the last run's: a
      // background must not bleed into the pane's empty rectangle.
      expect(row.cells[79].style).toEqual({
        foreground: { kind: 0, palette: 0, rgb: { r: 0, g: 0, b: 0 } },
        background: { kind: 0, palette: 0, rgb: { r: 0, g: 0, b: 0 } },
        underlineColor: { kind: 0, palette: 0, rgb: { r: 0, g: 0, b: 0 } },
        attributes: 0,
        underline: 0,
      })
    }
    expect(snapshotRowText(wide, 0)).toBe('AB')
    expect(snapshotRowText(wide, 1)).toBe('C界')
  })

  it('reads cells past the program’s last column as blank positions, not as content', () => {
    const wide = padSnapshotToColumns(twoRows(3), 80)
    expect(wide.rowAt(0)?.cellAt(40)?.hasText).toBe(false)
    expect(wide.rowAt(0)?.cellAt(80)).toBeNull()
    expect(wide.cellAt(1, 79)?.grapheme).toBe('')
  })

  it('never drops a row and never invents one', () => {
    const wide = padSnapshotToColumns(twoRows(3), 80)
    expect(wide.rows).toHaveLength(2)
    // The geometry's row count stays the program's: the pane's height is a
    // different decision, and a frame may legitimately be shorter.
    expect(wide.geometry.rows).toBe(2)
    expect(wide.revision).toBe(1)
    expect(wide.state).toEqual(twoRows(3).state)
    expect(wide.cursor).toEqual(twoRows(3).cursor)
  })

  it('returns the frame itself when the request would drop the program’s columns', () => {
    const wide = twoRows(80)
    // A presented grid may not lose cells: a narrower request is the
    // frame's own geometry, unchanged and by identity.
    expect(padSnapshotToColumns(wide, 40)).toBe(wide)
    expect(padSnapshotToColumns(wide, 80)).toBe(wide)
    expect(padSnapshotToColumns(wide, 120).geometry.cols).toBe(120)
  })
})

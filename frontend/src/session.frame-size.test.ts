// The frame contract's size discipline: one full screen must fit one payload
// on the two carrier bounds that already exist — internal/lifecycle's
// MaxFrameBytes (protocol.go, 256 KiB, the lifecycle channel's ceiling) and
// internal/helper/proto's MaxFrameBytes (frame.go, 1 MiB, the helper
// connection's frame ceiling). A frame that fits neither cannot be sent on
// ANY carrier, because the same bytes ride every wire.
//
// The fixture below is a realistic dense screen — a shell with a prompt,
// coloured output, a truecolour banner, CJK wide clusters, a ZWJ emoji, a
// cell-fit-boxed glyph, an inverse status bar — NOT an adversarial
// every-cell-unique-style screen (no run encoding fixes that) and NOT an
// empty one (everything fits that). Rows are the rectangle the schema
// demands: geometry.rows rows of geometry.cols cells every frame.
//
// This file measures bytes, so it uses local structural fixture types and
// does NOT import the generated module: the generated module's one consumer
// stays session.frame.test.ts (the contract pair's parity test), which is
// what keeps the dead-exports ratchet green with no baseline entry.
import { describe, expect, it } from 'vitest'

// The two carrier bounds, mirrored from the Go constants that enforce them.
// Keep in step with internal/lifecycle/protocol.go (MaxFrameBytes) and
// internal/helper/proto/frame.go (MaxFrameBytes).
const LIFECYCLE_MAX_FRAME_BYTES = 256 * 1024
const HELPER_MAX_FRAME_BYTES = 1 * 1024 * 1024

// Structural mirror of the wire shape under test (contracts/
// session.frame.schema.json), local to this file on purpose — see the header.
interface WireColor {
  kind: 0 | 1 | 2
  palette: number
  rgb: { r: number; g: number; b: number }
}
interface WireStyle {
  foreground: WireColor
  background: WireColor
  underlineColor: WireColor
  attributes: number
  underline: number
}
// [grapheme, width, hasText] — the positional cell tuple.
type WireCell = [string, 0 | 1 | 2 | 3 | 4, boolean]
// [style, length] — one maximal stretch of same-style cells.
type WireRun = [WireStyle, number]
interface WireRow {
  cells: WireCell[]
  runs: WireRun[]
  wrap: boolean
  continuation: boolean
}
interface WireFrame {
  revision: number
  geometry: {
    cols: number
    rows: number
    cellWidthPx: number
    cellHeightPx: number
    revision: number
  }
  cursor: { x: number; y: number; visible: boolean }
  rows: WireRow[]
}

const DEFAULT_COLOR: WireColor = { kind: 0, palette: 0, rgb: { r: 0, g: 0, b: 0 } }
const PALETTE_GREEN: WireColor = { kind: 1, palette: 2, rgb: { r: 0, g: 0, b: 0 } }
const PALETTE_RED: WireColor = { kind: 1, palette: 1, rgb: { r: 0, g: 0, b: 0 } }
const PALETTE_BLUE: WireColor = { kind: 1, palette: 4, rgb: { r: 0, g: 0, b: 0 } }
const PALETTE_CYAN: WireColor = { kind: 1, palette: 6, rgb: { r: 0, g: 0, b: 0 } }
const RGB_ORANGE: WireColor = { kind: 2, palette: 0, rgb: { r: 255, g: 95, b: 0 } }

const defaultStyle = (): WireStyle => ({
  foreground: DEFAULT_COLOR,
  background: DEFAULT_COLOR,
  underlineColor: DEFAULT_COLOR,
  attributes: 0,
  underline: 0,
})
const promptStyle = (): WireStyle => ({ ...defaultStyle(), foreground: PALETTE_GREEN })
const redStyle = (): WireStyle => ({ ...defaultStyle(), foreground: PALETTE_RED })
const blueStyle = (): WireStyle => ({ ...defaultStyle(), foreground: PALETTE_BLUE })
const cyanStyle = (): WireStyle => ({ ...defaultStyle(), foreground: PALETTE_CYAN })
const bannerStyle = (): WireStyle => ({
  ...defaultStyle(),
  foreground: RGB_ORANGE,
  background: { kind: 2, palette: 0, rgb: { r: 40, g: 40, b: 48 } },
})
const inverseStyle = (): WireStyle => ({ ...defaultStyle(), attributes: 16 })
const underlineStyle = (): WireStyle => ({ ...defaultStyle(), underline: 3 })

// One seed cell: the grapheme cluster and the column footprint the runtime
// declared for it (the five-value emulator.Width enumeration).
type CellSeed = [grapheme: string, width: 0 | 1 | 2 | 3 | 4]

interface RunSeed {
  style: WireStyle
  cells: CellSeed[]
}

interface FixtureRow {
  runs: RunSeed[]
  wrap: boolean
  continuation: boolean
}

// A filler run of blank default-styled cells.
const blank = (style: WireStyle, count: number): RunSeed => ({
  style,
  cells: Array.from({ length: count }, (): CellSeed => ['', 1]),
})

// Letters cycling a..z, one per cell — deterministic content with realistic
// one-codepoint graphemes.
const letters = (style: WireStyle, count: number): RunSeed => ({
  style,
  cells: Array.from({ length: count }, (_, i): CellSeed => [String.fromCharCode(97 + (i % 26)), 1]),
})

// One realistic screen row, content scaled to the column count. Row kinds
// cycle deterministically so every geometry gets the same mix: mostly blank
// and default-styled rows (what a real screen mostly is), a few coloured
// runs, one truecolour banner, wide clusters, a ZWJ sequence, a boxed
// glyph, and a full-width inverse status bar.
function fixtureRow(index: number, cols: number): FixtureRow {
  const base = defaultStyle()
  let runs: RunSeed[]
  switch (index % 12) {
    case 0: // shell prompt in green, rest of the line blank
      runs = [letters(promptStyle(), 2), blank(base, cols - 2)]
      break
    case 1: // a plain command line — default text, same style as the blank
      runs = [letters(base, Math.min(cols, 28)), blank(base, Math.max(0, cols - 28))]
      break
    case 2: {
      // coloured output: a red word, a blue word
      const first = Math.min(9, cols)
      const second = Math.min(14, Math.max(0, cols - first - 3))
      runs = [
        letters(redStyle(), first),
        letters(base, 3),
        letters(blueStyle(), second),
        blank(base, Math.max(0, cols - first - 3 - second)),
      ]
      break
    }
    case 5: {
      // CJK wide clusters in cyan: each cluster is a width-2 cell followed
      // by its width-3 spacerTail continuation
      const clusters = Math.min(6, Math.floor(cols / 2))
      const cells: CellSeed[] = []
      for (let i = 0; i < clusters; i++) {
        cells.push(['中', 2])
        cells.push(['', 3])
      }
      runs = [{ style: cyanStyle(), cells }, blank(base, Math.max(0, cols - clusters * 2))]
      break
    }
    case 6: // truecolour banner word
      runs = [letters(bannerStyle(), Math.min(18, cols)), blank(base, Math.max(0, cols - 18))]
      break
    case 8: {
      // inverse status bar: text in every column, one run
      runs = [letters(inverseStyle(), cols)]
      break
    }
    case 10: {
      // a ZWJ emoji (one grapheme, two columns) and a cell-fit-boxed glyph
      const cells: CellSeed[] = [
        ['👩‍💻', 2],
        ['', 3],
        ['⬢', 1],
        ['x', 1],
      ]
      runs = [{ style: base, cells }, blank(base, Math.max(0, cols - 4))]
      break
    }
    case 11: // curly-underlined word
      runs = [letters(underlineStyle(), Math.min(12, cols)), blank(base, Math.max(0, cols - 12))]
      break
    default: // blank row, the common case
      runs = [blank(base, cols)]
      break
  }
  return { runs, wrap: false, continuation: false }
}

function fixtureScreen(cols: number, rows: number): WireFrame {
  const frameRows: WireRow[] = []
  for (let i = 0; i < rows; i++) {
    const spec = fixtureRow(i, cols)
    const cells: WireCell[] = []
    const runs: WireRun[] = []
    for (const run of spec.runs) {
      runs.push([run.style, run.cells.length])
      for (const [grapheme, width] of run.cells) {
        cells.push([grapheme, width, grapheme !== ''])
      }
    }
    expect(cells).toHaveLength(cols)
    expect(runs.reduce((sum, [, length]) => sum + length, 0)).toBe(cells.length)
    frameRows.push({ cells, runs, wrap: spec.wrap, continuation: spec.continuation })
  }
  return {
    revision: 1,
    geometry: { cols, rows, cellWidthPx: 9, cellHeightPx: 18, revision: 1 },
    cursor: { x: 0, y: 0, visible: true },
    rows: frameRows,
  }
}

const byteLength = (value: unknown): number =>
  new TextEncoder().encode(JSON.stringify(value)).length

describe('session.frame fits one payload on both carrier bounds', () => {
  it.each([
    [80, 24],
    [120, 40],
    [200, 50],
  ])('a full frame at %ix%i is under both MaxFrameBytes bounds', (cols, rows) => {
    const frame = fixtureScreen(cols, rows)
    const bytes = byteLength(frame)
    console.log(`frame ${cols}x${rows}: ${bytes} bytes`)
    expect(bytes).toBeLessThan(LIFECYCLE_MAX_FRAME_BYTES)
    expect(bytes).toBeLessThan(HELPER_MAX_FRAME_BYTES)
  })

  it('the special-cell vocabulary round-trips with the columns the runtime declared', () => {
    const base = defaultStyle()
    const frame = fixtureScreen(8, 1)
    // Overwrite the single row with the four load-bearing cases, every cell
    // carrying a distinct style so each also lands in its own run:
    // a double-width cluster + spacerTail, a combining cluster, a ZWJ
    // sequence + spacerTail, and a cell-fit-boxed narrow glyph, then a
    // spacerHead (the column a wide cluster would have needed at a soft
    // wrap) and one plain blank.
    const cells: WireCell[] = [
      ['中', 2, true],
      ['', 3, false],
      ['é', 1, true],
      ['👩‍💻', 2, true],
      ['', 3, false],
      ['⬢', 1, true],
      ['', 4, false], // spacerHead at a soft wrap
      ['', 1, false],
    ]
    const runs: WireRun[] = [
      [cyanStyle(), 2],
      [redStyle(), 1],
      [blueStyle(), 2],
      [promptStyle(), 1],
      [inverseStyle(), 1],
      [base, 1],
    ]
    frame.rows = [{ cells, runs, wrap: false, continuation: false }]

    const parsed = JSON.parse(JSON.stringify(frame)) as WireFrame
    const row = parsed.rows[0]
    expect(row.cells.map((c) => c[0])).toEqual(cells.map((c) => c[0]))
    // The columns the runtime declared are the columns a receiver reads
    // back — for every case: wide cluster, combining cluster, ZWJ
    // sequence, boxed glyph, spacerHead, plain blank.
    expect(row.cells.map((c) => c[1])).toEqual([2, 3, 1, 2, 3, 1, 4, 1])
    expect(row.cells.map((c) => c[2])).toEqual(cells.map((c) => c[2]))
    // Style sharing loses no per-cell style: the run covering each special
    // cell is the one the sender wrote for it.
    const coveringStyle = (index: number): WireStyle => {
      let offset = 0
      for (const [style, length] of row.runs) {
        if (index < offset + length) return style
        offset += length
      }
      throw new Error(`no run covers cell ${index}`)
    }
    expect(coveringStyle(0)).toEqual(cyanStyle()) // double-width cluster
    expect(coveringStyle(2)).toEqual(redStyle()) // combining cluster
    expect(coveringStyle(3)).toEqual(blueStyle()) // ZWJ sequence
    expect(coveringStyle(5)).toEqual(promptStyle()) // cell-fit-boxed glyph
    expect(coveringStyle(6)).toEqual(inverseStyle()) // spacerHead
  })
})

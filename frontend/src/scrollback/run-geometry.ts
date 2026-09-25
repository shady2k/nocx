// THE OWNER OF RUN GEOMETRY (nocx-zg3k3.7).
//
// ADR-0009 (docs/decisions/0009-dom-scrollback-with-explicit-cell-geometry.md,
// rules 1–3 and 6; design .internal/specs/2026-09-05-frozen-grid-renderer-
// design.md, "frozen-grid.ts") planned one module that decides, for a row of
// terminal cells, which of them merge into a run and what spacing that run
// carries. This is that module.
//
//   1. per cell,  spacing = columns × cellWidth − measured advance
//   2. adjacent cells merge only when attributes AND spacing agree
//   3. the run carries its spacing as letter-spacing, so each cell
//      contributes exactly columns × cellWidth
//   6. where spacing is negative the ink is scaled to fit (cell-fit's box
//      verdict) — the advance is never changed to accommodate it
//
// It knows nothing about DOM markup and nothing about xterm: the cell walk
// stays with the serializer (AD-8 — one function knows how to walk cells and
// count columns), and whoever draws a run decides how its spacing reaches the
// surface. The frozen block serializer is the caller today; the live cell
// painter is the next stage and must be able to call this without a rewrite —
// which is why the metric is injected rather than reached for.

import type { CellBox, FitFace } from './cell-fit'

/** One cell as the walk yields it, in order: what it printed (already
 *  escaped, if the caller will emit HTML), the grid columns it stands in
 *  (never fewer than one), and whatever an attribute is for that caller.
 *  `blank` marks a cell that has no ink — a blank the walk spelled as a
 *  space or the zero-width spacer after a wide glyph; the measurer is never
 *  asked about one, exactly as it was never asked before runs owned their
 *  own geometry. */
export interface GridCell<A> {
  chars: string
  cols: number
  attrs: A
  blank?: boolean
}

/** The metric a run is built against — all of it measured or published
 *  elsewhere, never derived here. `cellWidth` is the grid pitch the renderer
 *  published; `defaultSpacing` is the published row correction
 *  (--term-cell-delta), which is also the spacing of every cell whose ink
 *  nobody measured: it cannot claim its columns are exact, so it falls to
 *  the correction the whole row carried before runs owned their own. The
 *  advance and box answers come from THE measuring authority (cell-fit) —
 *  this module derives geometry from measurements and never makes a second
 *  one. With no metric at all every spacing is 0 and runs merge by
 *  attributes alone, which is byte-for-byte the degrade the frozen block
 *  shipped with before this module existed. */
export interface RunMetric {
  readonly cellWidth: number
  readonly defaultSpacing: number
  /** Rule 4's measured vertical padding — half the difference between the
   *  row's pitch and the font's content box, published beside the cell
   *  metric (ADR-0009:136-144). The painter puts it on runs carrying a
   *  background, so the background covers the cell's rectangle and adjacent
   *  rows meet. Optional until a publisher measures it: absent, a
   *  background covers the content box, exactly as the frozen blocks ship
   *  today. */
  readonly padY?: number
  advanceOf(chars: string, cols: number, face: FitFace): number | null
  boxOf?(chars: string, cols: number, face: FitFace): CellBox | null
}

/** Rule 1, per cell. Rounded to the four decimals the markup carries (the
 *  same precision cell-fit's fit multiplier uses): rule 2 compares spacings
 *  for equality, and a hair-splitting double would split runs the eye cannot
 *  tell apart. A negative-zero folds to 0 — one number, not two spellings. */
export function cellSpacing(
  cols: number,
  cellWidth: number,
  advance: number | null,
  defaultSpacing: number,
): number {
  if (advance === null) return defaultSpacing
  const spacing = Math.round((cols * cellWidth - advance) * 1e4) / 1e4
  return spacing === 0 ? 0 : spacing
}

/** One run: cells of one attribute set and one spacing, merged; the grid
 *  columns they stand in; and the spacing each contributes (rule 3). `box`
 *  marks a single cell whose ink cell-fit locked into a box: it is its own
 *  run — a merged pair would have taken one column for two — the box owns
 *  the advance, and `spacing` is 0 because the drawer must not letter-space
 *  a box that is already exactly its columns wide. */
export interface GeometryRun<A> {
  chars: string
  attrs: A
  cols: number
  spacing: number
  box?: CellBox
}

/** THE merge walk (rule 2). Cells in, runs out, in order. */
export function runsOf<A>(
  cells: readonly GridCell<A>[],
  attrsEqual: (a: A, b: A) => boolean,
  faceOf: (attrs: A) => FitFace,
  metric?: RunMetric,
): GeometryRun<A>[] {
  const cellWidth = metric?.cellWidth ?? 0
  const defaultSpacing = metric?.defaultSpacing ?? 0
  const runs: GeometryRun<A>[] = []

  for (const cell of cells) {
    let spacing: number
    let box: CellBox | undefined
    if (cell.blank) {
      // No ink, nothing to measure: the blank carries the row default, the
      // same correction it inherited when the row carried one correction.
      spacing = defaultSpacing
    } else {
      const face = faceOf(cell.attrs)
      // The box verdict is cell-fit's — its header owns the question "does
      // this cell land on the grid". Enforced here is only its shape: a box
      // is accepted on the cell's OWN columns, because "one column" for a
      // two-column cell is a shift the grid does not have.
      const claimed = metric?.boxOf?.(cell.chars, cell.cols, face) ?? null
      box = claimed !== null && claimed.cols === cell.cols ? claimed : undefined
      spacing =
        box !== undefined
          ? 0
          : cellSpacing(
              cell.cols,
              cellWidth,
              metric?.advanceOf(cell.chars, cell.cols, face) ?? null,
              defaultSpacing,
            )
    }

    const last = runs[runs.length - 1]
    if (
      last !== undefined &&
      last.box === undefined &&
      box === undefined &&
      attrsEqual(last.attrs, cell.attrs) &&
      last.spacing === spacing
    ) {
      last.chars += cell.chars
      last.cols += cell.cols
    } else {
      const run: GeometryRun<A> = { chars: cell.chars, attrs: cell.attrs, cols: cell.cols, spacing }
      if (box !== undefined) run.box = box
      runs.push(run)
    }
  }

  return runs
}

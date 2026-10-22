// One grid row → one DOM row (nocx-zg3k3.2.4), by the geometry contract.
//
// WHERE a run starts and what spacing it carries is run-geometry's verdict
// — this file only draws what runsOf hands it, exactly as the frozen
// serializer does. Rule 3: the run's spacing becomes its letter-spacing, so
// each cell contributes advance + spacing = columns × cellWidth and the
// row's total is exact without a declared width. Rule 4: a run carrying a
// background also carries the published vertical padding (RunMetric.padY),
// so the background covers the cell's rectangle rather than the font's
// content box and adjacent rows meet. Rule 6: a boxed cell is drawn as a
// .term-cell of exactly its columns — the box owns the advance and takes no
// letter-spacing — with the ink scaled inside by .term-cell-ink, never the
// advance changed. No inline-block is introduced for ordinary runs
// (ADR-0009's consequence), and nothing is clipped.
//
// The row element is a .term-grid-row and not a .term-line: the grid row
// has a class of its own (ADR-0009) because .term-line is also the class
// the assistant's prose rows are drawn with, and one class carrying two
// meanings is the defect the repository names. Runs are built with
// createElement and textContent — terminal output is untrusted, so no
// painter of it goes through innerHTML the way the string serializer must.

import type { ModelStyle as Style, ScreenSnapshot } from '../cell-model'
import type { FitCandidate } from '../scrollback/cell-fit'
import { runsOf, type GeometryRun, type GridCell, type RunMetric } from '../scrollback/run-geometry'
import type { TerminalSnapshot } from '../scrollback/serializer'
import { isProceduralInk, proceduralSvgOf } from './cell-ink'
import { faceOf, resolveInk, styleEquals } from './style'

export interface PaintRowOptions {
  readonly metric: RunMetric | null
  readonly palette: TerminalSnapshot
}

/** Cell-fit's batch step, rule "ALL WRITES, THEN ALL READS" (cell-fit.ts's
 *  header): every cell a caller is ABOUT to paint, in the same shape and
 *  through the same `faceOf` that `paintRow` itself will query `boxOf`
 *  with, so a candidate warmed here is the exact key `runsOf` looks up
 *  later. A blank cell is never a candidate — it carries no ink to measure,
 *  and `faceOf` would only manufacture a key nothing ever reads. */
export function fitCandidatesOf(rows: Iterable<ScreenSnapshot['rows'][number]>): FitCandidate[] {
  const out: FitCandidate[] = []
  for (const row of rows) {
    for (const cell of row.cells) {
      if (!cell.hasText) continue
      // Box drawing and block elements are painted to cell bounds from the
      // metric itself (nocx-1ebrs.15): no font advance is consumed, so the
      // measurer is never asked — asking would only warm a cache entry that
      // no paint reads.
      if (isProceduralInk(cell.grapheme)) continue
      out.push({ chars: cell.grapheme, width: cell.span, face: faceOf(cell.style) })
    }
  }
  return out
}

export function paintRow(
  row: ScreenSnapshot['rows'][number],
  opts: PaintRowOptions,
): HTMLDivElement {
  const el = document.createElement('div')
  el.className = 'term-grid-row'
  // The walk's cells, in the owner's shape: a cell without text is a blank
  // spelled as a space — the measurer is never asked about one. `cols` is
  // the model's declared span, which is the wire's own arithmetic; nothing
  // here recomputes it. A spacerTail (width 3) is skipped: the wide cell
  // before it already stands in its column (span 2), and spelling it too
  // pushed every later cell one column right of the model and the mapping.
  // The frozen path's walk steps over the same blank spacer cell. A
  // spacerHead (width 4) is its own column, and paints as a blank.
  const cells: GridCell<Style>[] = row.cells
    .filter((cell) => cell.width !== 3)
    .map((cell) => ({
      chars: cell.hasText ? cell.grapheme : ' ',
      cols: cell.span,
      attrs: cell.style,
      blank: !cell.hasText,
    }))
  const runs = runsOf(
    cells,
    styleEquals,
    faceOf,
    opts.metric ?? undefined,
    proceduralOf(opts.metric) ? isProceduralInk : undefined,
  )
  const defaultSpacing = opts.metric?.defaultSpacing ?? 0
  for (const run of runs) {
    appendRun(el, run, defaultSpacing, opts)
  }
  return el
}

/** Procedural ink is paintable when the metric carries both halves of the
 *  cell: the width across and the published pitch down (nocx-1ebrs.15).
 *  Without either there is no cell rectangle to fill, and the font glyph —
 *  the behaviour every terminal without this module ships — stands. */
function proceduralOf(metric: RunMetric | null): boolean {
  return metric !== null && metric.cellWidth > 0 && (metric.pitch ?? 0) > 0
}

function appendRun(
  row: HTMLElement,
  run: GeometryRun<Style>,
  defaultSpacing: number,
  opts: PaintRowOptions,
): void {
  const ink = resolveInk(run.attrs, opts.palette)
  const padY = opts.metric?.padY

  // Box drawing and block elements drawn TO CELL BOUNDS (nocx-1ebrs.15):
  // the frozen path's .term-cell owns the advance, the ink is the metric's
  // own geometry — cols × cellWidth across, the published pitch down. One
  // box per RUN: every cell of it shares one SVG coordinate space, so a
  // line across cells never meets a per-element device-pixel snap.
  //
  // THE CHARACTER STAYS TEXT (owner decision, 2026-10-21): the run's
  // characters are a text node in the row, so selection, copy, find and the
  // accessibility tree read exactly what the program painted — no
  // aria-label stands in for them. Only the font glyph is made invisible
  // (.term-cell-glyph is transparent); the ink keeps the run's own colour
  // through the box's currentColor and paints behind the character (the
  // glyph's positioned layer, style.css). A glyph nobody can see cannot
  // tile wrongly; the geometry the bead is about is the SVG's.
  const metric = opts.metric
  if (
    metric !== null &&
    metric.cellWidth > 0 &&
    (metric.pitch ?? 0) > 0 &&
    isProceduralInk(run.chars)
  ) {
    const pitch = metric.pitch as number
    const cell = document.createElement('span')
    cell.className = 'term-cell'
    cell.dataset.cols = String(run.cols)
    cell.dataset.procedural = ''
    if (ink.css) cell.style.cssText = ink.css
    // SIZED BY THE PUBLISHED VARS, not baked pixels: the frozen block's
    // re-pitch story (cell-metric.ts) is that old blocks adopt the current
    // geometry without a repaint, and a box nailed to the day's numbers
    // would sit at the old pitch inside text that moved. The SVG carries
    // preserveAspectRatio="none" for the same reason: it stretches with the
    // box, and every shape in it is defined to fill or centre on the cell,
    // so the stretched ink is still exact.
    cell.style.width = `calc(var(--term-cell-width, 1ch) * ${run.cols})`
    cell.style.height = 'var(--term-cell-height, 1.2em)'
    cell.appendChild(
      proceduralSvgOf(
        run.chars,
        run.spans ?? Array.from(run.chars, () => 1),
        metric.cellWidth,
        pitch,
      ),
    )
    const glyph = document.createElement('span')
    glyph.className = 'term-cell-glyph'
    glyph.style.color = 'transparent'
    glyph.textContent = run.chars
    cell.appendChild(glyph)
    row.appendChild(cell)
    return
  }

  if (run.box !== undefined) {
    // The frozen path's own box classes — one CSS truth for "a cell that
    // cannot land on its own" (style.css .term-cell / .term-cell-ink).
    const cell = document.createElement('span')
    cell.className = 'term-cell'
    cell.dataset.cols = String(run.box.cols)
    if (ink.css) cell.style.cssText = ink.css
    if (run.box.fit < 1) {
      const inner = document.createElement('span')
      inner.className = 'term-cell-ink'
      inner.style.setProperty('--cell-fit', String(run.box.fit))
      inner.textContent = run.chars
      cell.appendChild(inner)
    } else {
      cell.textContent = run.chars
    }
    row.appendChild(cell)
    return
  }

  // An ordinary row of one colour stays the single text node it is today
  // (ADR-0009): a run with nothing to declare and the row's own default
  // spacing is bare text.
  if (!ink.css && run.spacing === defaultSpacing) {
    row.appendChild(document.createTextNode(run.chars))
    return
  }
  const span = document.createElement('span')
  if (ink.css) span.style.cssText = ink.css
  if (run.spacing !== defaultSpacing) span.style.letterSpacing = `${run.spacing}px`
  if (ink.hasBackground && padY !== undefined) span.style.paddingBlock = `${padY}px`
  span.textContent = run.chars
  row.appendChild(span)
}

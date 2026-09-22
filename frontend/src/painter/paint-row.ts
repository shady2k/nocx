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

import type { Style } from '../generated/session.frame'
import type { ScreenSnapshot } from '../cell-model'
import { runsOf, type GeometryRun, type GridCell, type RunMetric } from '../scrollback/run-geometry'
import type { TerminalSnapshot } from '../scrollback/serializer'
import { faceOf, resolveInk, styleEquals } from './style'

export interface PaintRowOptions {
  readonly metric: RunMetric | null
  readonly palette: TerminalSnapshot
}

export function paintRow(
  row: ScreenSnapshot['rows'][number],
  opts: PaintRowOptions,
): HTMLDivElement {
  const el = document.createElement('div')
  el.className = 'term-grid-row'
  // The walk's cells, in the owner's shape: a cell without text is a blank
  // spelled as a space (the spacer after a wide cluster included) — the
  // measurer is never asked about one. `cols` is the model's declared span,
  // which is the wire's own arithmetic; nothing here recomputes it.
  const cells: GridCell<Style>[] = row.cells.map((cell) => ({
    chars: cell.hasText ? cell.grapheme : ' ',
    cols: cell.span,
    attrs: cell.style,
    blank: !cell.hasText,
  }))
  const runs = runsOf(cells, styleEquals, faceOf, opts.metric ?? undefined)
  const defaultSpacing = opts.metric?.defaultSpacing ?? 0
  for (const run of runs) {
    appendRun(el, run, defaultSpacing, opts)
  }
  return el
}

function appendRun(
  row: HTMLElement,
  run: GeometryRun<Style>,
  defaultSpacing: number,
  opts: PaintRowOptions,
): void {
  const ink = resolveInk(run.attrs, opts.palette)
  const padY = opts.metric?.padY

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

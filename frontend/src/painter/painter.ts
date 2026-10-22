// THE LIVE CELL PAINTER (nocx-zg3k3.2.4): rows, runs, cursor and THE one
// pixel-to-cell mapping, over the passive cell model (nocx-zg3k3.2.3).
//
// Design: .internal/specs/2026-09-21-the-cell-renderer-choice-design.md
// §3.9 ("Write it") and §4.1 ("The smallest correct version"). The painter
// never segments, never measures, and never re-derives run geometry: it
// walks the model's cells through run-geometry.ts (the one owner,
// nocx-zg3k3.7) with the SAME measuring authority the frozen blocks use,
// injected at the composition root. This module owns no probe of its own,
// so it cannot measure with one shaping and key with another — the defect
// class that cost four adversarial rounds (ADR-0009:145-147).
//
// Frames are applied by row-level diffing against the installed revision: a
// row whose cells and style are unchanged keeps its DOM, so a cursor move
// at a new revision moves no text, and an unchanged uniform row stays the
// single text node ADR-0009 preserves. The cursor is an overlay positioned
// by THE mapping (mapping.ts), never a glyph in the text flow.
//
// Selection, copy, IME and the aria-live channel are later slices of the
// same epic; the cutover (nocx-zg3k3.2.5) mounts the painter in place of
// terminal input — this module deliberately knows nothing about how it is mounted.

import type { ScreenSnapshot } from '../cell-model'
import { CARET_IDLE_MS, CARET_PHASE_ATTR, type CaretPhase } from '../caret-phase'
import type { CellFit, FitCandidate } from '../scrollback/cell-fit'
import type { RunMetric } from '../scrollback/run-geometry'
import type { TerminalSnapshot } from '../scrollback/serializer'
import { createMapping, type PixelMapping } from './mapping'
import { fitCandidatesOf, paintRow } from './paint-row'
import { styleEquals } from './style'
import { devicePxToCssPx, displayDpr } from './committed-metric'

/** The measuring authority, in the shape run-geometry's metric needs. A
 *  CellFit satisfies it structurally; tests inject tables. `geometry()`'
 *  row delta is the published --term-cell-delta — the default spacing of
 *  every cell whose ink nobody measured. */
type Measurer = Pick<CellFit, 'advanceOf' | 'boxOf' | 'geometry'>

/** CellFit → RunMetric, written ONCE here so the cutover cannot coin a
 *  second conversion. Null when the fit has nowhere to measure (no mounted
 *  container, no published cell width) — the same degrade the frozen
 *  blocks ship with: runs merge by attributes alone, spacing 0. */
export function metricOf(fit: Measurer): RunMetric | null {
  const g = fit.geometry()
  if (g === null) return null
  return {
    cellWidth: g.cellWidth,
    defaultSpacing: g.rowDelta,
    signature: g.signature,
    ...(g.padY === undefined ? {} : { padY: g.padY }),
    ...(g.pitch === undefined ? {} : { pitch: g.pitch }),
    ...measurersOf(fit),
  }
}

/** The fit's two measurers, bound ONCE per fit. The painter decides
 *  "same metric, keep the rows" by comparing them by reference, and a
 *  fresh `.bind` per apply made every revision a full repaint
 *  (nocx-zg3k3.2.14). The lint rule reads a bare method reference as an
 *  unbound `this`; the fit functions close over their cache and never
 *  touch `this`, and bind keeps the signatures RunMetric's own. */
const boundMeasurers = new WeakMap<Measurer, Pick<RunMetric, 'advanceOf' | 'boxOf'>>()
function measurersOf(fit: Measurer): Pick<RunMetric, 'advanceOf' | 'boxOf'> {
  let bound = boundMeasurers.get(fit)
  if (bound === undefined) {
    bound = { advanceOf: fit.advanceOf.bind(fit), boxOf: fit.boxOf.bind(fit) }
    boundMeasurers.set(fit, bound)
  }
  return bound
}

export interface CellPainterOptions {
  /** The element the grid is painted into. Takes the .term-grid class and
   *  hosts the cursor overlay as its last child. */
  readonly surface: HTMLElement
  /** The metric supplier, re-read at every apply: mount, font loads and
   *  zoom all change it, and a verdict must never outlive the revision
   *  that carried it. Absent or null, the painter degrades to
   *  attribute-only runs — the pre-geometry behaviour, no worse. */
  readonly metric?: () => RunMetric | null
  /** The measuring authority's batch write: every cell about to paint is
   *  measured here, BEFORE any row paints, so the metric's boxOf/advanceOf
   *  are pure cache reads during the paint (cell-fit.ts's contract, which
   *  the stored rows already keep — block-rows.ts). Without it a cluster new
   *  to the session painted at the default spacing and moved every column
   *  after it (nocx-zg3k3.2.13). Absent, nothing is measured: the degrade
   *  of an absent metric. */
  readonly warm?: (candidates: Iterable<FitCandidate>) => void
  /** The theme the wire's palette colours resolve against, read on every
   *  apply. Required, with no fallback: a default here is how the live grid
   *  painted Tokyo Night under every theme once (nocx-zg3k3.28). A different
   *  answer from the last apply repaints every row, as a metric change does. */
  readonly palette: () => TerminalSnapshot
  /** Told where the caret was just placed, in the surface's own CSS pixels —
   *  the same coordinates the overlay is drawn at.
   *
   *  ONE OWNER, TWO READERS. "Where is the terminal caret" is answered here and
   *  nowhere else (createMapping's cellToPixel, the mapping the rows and the
   *  overlay already share), and the pane's input element is the second reader:
   *  an IME draws its candidate window against a focused element's position, so
   *  the element that carries the keyboard has to sit ON the caret
   *  (nocx-zg3k3.3.1). Fired on every placement, including one where the
   *  program has hidden its cursor: the anchor is about where typing goes, and
   *  a hidden caret is still a place the person is typing at. */
  readonly onCaretPlaced?: (at: { left: number; top: number; height: number }) => void
}

export interface CellPainter {
  /** Apply one installed revision. Rows whose content is unchanged keep
   *  their DOM; changed rows are repainted through run-geometry. */
  apply(snapshot: ScreenSnapshot): void
  /** Paint a selection from model coordinates, or clear it. End offsets are exclusive. */
  setSelection(
    range: {
      readonly anchor: { readonly row: number; readonly offset: number }
      readonly focus: { readonly row: number; readonly offset: number }
    } | null,
  ): void
  /** Paint the armed link's underline over one row's character span, or
   *  clear it. `from`/`to` are UTF-16 offsets into the row's own text —
   *  the units the link grammar and the pane's hit-test already speak —
   *  and this file converts them to columns, because the overlay's
   *  geometry is the painter's answer, not the caller's. */
  setLinkHighlight(range: LinkHighlightRange | null): void
  /** THE mapping, bound to the installed revision's committed geometry.
   *  Null before the first apply — there is nothing to map yet. */
  mapping(): PixelMapping | null
  /** The person acted on the keyboard: a key, committed text or a paste
   *  reached this pane's input (nocx-1ebrs.37). The overlay holds solid and
   *  resumes blinking after CARET_IDLE_MS of nothing — the rule the
   *  composer's caret already follows (nocx-1ebrs.31), from the same
   *  vocabulary (caret-phase.ts). */
  noteInput(): void
  dispose(): void
  readonly surface: HTMLElement
}

const GRID_CLASS = 'term-grid'
const CURSOR_CLASS = 'term-grid-cursor'
const SELECTION_CLASS = 'term-grid-selection'
const LINK_CLASS = 'term-grid-link'

/** The armed link the pointer is over: one row's character span. The units
 *  are the link grammar's (UTF-16 offsets into the row's text), so the
 *  caller never converts — the pane's hit-test hands the same numbers to
 *  `LivePolicy.ranges` that it hands here. */
interface LinkHighlightRange {
  readonly row: number
  readonly from: number
  readonly to: number
}

/** The pointer state the stylesheet reads (nocx-1ebrs.24): `program` while the
 *  program reports the mouse, `text` while the terminal owns the click. Named
 *  here and read by style.css, so the two cannot spell the same state twice. */
export const POINTER_ATTR = 'data-pointer'
export type PointerOwner = 'program' | 'text'

export function createCellPainter(opts: CellPainterOptions): CellPainter {
  const surface = opts.surface
  let lastPalette: TerminalSnapshot | null = null
  surface.classList.add(GRID_CLASS)
  surface.setAttribute('role', 'grid')
  surface.setAttribute('aria-label', 'Terminal output')

  let activeRow: number | null = null
  let keyboardFocus = false

  function updateRowAccessibility(): void {
    rows.forEach((row, index) => {
      row.setAttribute('role', 'row')
      row.setAttribute('aria-rowindex', String(index + 1))
      const selected =
        selection !== null &&
        index >= Math.min(selection.anchor.row, selection.focus.row) &&
        index <= Math.max(selection.anchor.row, selection.focus.row)
      row.setAttribute('aria-selected', String(selected))
      if (activeRow === index) row.setAttribute('aria-current', 'true')
      else row.removeAttribute('aria-current')
      row.tabIndex = activeRow === null ? (index === 0 ? 0 : -1) : activeRow === index ? 0 : -1
      if (activeRow === index && keyboardFocus) row.dataset.focusVisible = 'true'
      else delete row.dataset.focusVisible
    })
  }

  function onFocus(event: FocusEvent): void {
    const row = (event.target as HTMLElement).closest<HTMLElement>('.term-grid-row')
    const index = row === null ? -1 : rows.indexOf(row as HTMLDivElement)
    if (index < 0) return
    activeRow = index
    updateRowAccessibility()
  }

  function onFocusOut(event: FocusEvent): void {
    if (event.relatedTarget instanceof Node && surface.contains(event.relatedTarget)) return
    activeRow = null
    keyboardFocus = false
    updateRowAccessibility()
  }

  function onKeyDown(event: KeyboardEvent): void {
    const row = (event.target as HTMLElement).closest<HTMLElement>('.term-grid-row')
    if (row === null) return
    const index = rows.indexOf(row as HTMLDivElement)
    const destination =
      event.key === 'ArrowDown'
        ? index + 1
        : event.key === 'ArrowUp'
          ? index - 1
          : event.key === 'Home'
            ? 0
            : event.key === 'End'
              ? rows.length - 1
              : index
    if (destination === index || destination < 0 || destination >= rows.length) return
    event.preventDefault()
    keyboardFocus = true
    rows[destination].focus()
  }

  function onPointerDown(): void {
    keyboardFocus = false
    updateRowAccessibility()
  }

  surface.addEventListener('focusin', onFocus)
  surface.addEventListener('focusout', onFocusOut)
  surface.addEventListener('keydown', onKeyDown)
  surface.addEventListener('pointerdown', onPointerDown)

  const cursor = document.createElement('div')
  cursor.className = CURSOR_CLASS
  cursor.setAttribute('aria-hidden', 'true')
  // A cursor that has been sitting there is blinking before the first input
  // of the session, which is the state every terminal opens in (nocx-1ebrs.37).
  cursor.setAttribute(CARET_PHASE_ATTR, 'blinking')
  surface.appendChild(cursor)

  let caretIdle: ReturnType<typeof setTimeout> | null = null

  function setCaretPhase(phase: CaretPhase): void {
    cursor.setAttribute(CARET_PHASE_ATTR, phase)
  }

  /** Solid now; blinking again after the idle bound (caret-phase.ts's
   *  standard). One timer for the overlay: every wake restarts it, so a
   *  keystroke or a write inside the bound holds the cursor solid as long
   *  as they keep arriving. */
  function wakeCaret(): void {
    setCaretPhase('solid')
    if (caretIdle !== null) clearTimeout(caretIdle)
    caretIdle = setTimeout(() => {
      caretIdle = null
      setCaretPhase('blinking')
    }, CARET_IDLE_MS)
  }

  /** A program write AT the cursor is the keystroke's twin (nocx-1ebrs.37):
   *  the echo a shell paints under a typed key, a full-screen program
   *  redrawing where the person is looking. The cursor position moving is a
   *  write too — a shell echoing an arrow key moves it, and a redraw moves
   *  it first. A frame that changes nothing at the cursor — a write far
   *  from it, the installed snapshot re-applied by a theme switch — wakes
   *  nothing, or the cursor would blink whenever the screen moved. */
  function noteCursorWrite(previous: ScreenSnapshot | null, next: ScreenSnapshot): void {
    if (previous === null) return
    const coveredAt = (snap: ScreenSnapshot): string => {
      const covered = snap.rows[snap.cursor.y]?.cells[snap.cursor.x]
      return covered !== undefined && covered.hasText ? covered.grapheme : ''
    }
    if (
      previous.cursor.x !== next.cursor.x ||
      previous.cursor.y !== next.cursor.y ||
      coveredAt(previous) !== coveredAt(next)
    ) {
      wakeCaret()
    }
  }

  let rows: HTMLDivElement[] = []
  let installed: ScreenSnapshot | null = null
  let lastMetric: RunMetric | null = null
  let selection: {
    anchor: { row: number; offset: number }
    focus: { row: number; offset: number }
  } | null = null
  let selectionNodes: HTMLDivElement[] = []
  let linkHighlight: LinkHighlightRange | null = null
  let linkNodes: HTMLDivElement[] = []

  /** A changed metric re-verdicts every run's spacing — rule 1's output is
   *  painted output — so it repaints like a content change. The numbers
   *  are compared by value, the measurers by identity (metricOf binds them
   *  once per fit), and the fit's signature by value: a late font load or a
   *  shaping change leaves the numbers equal and the verdicts different.
   *  Identical numbers, measurers and signature keep the rows' DOM. */
  function metricChanged(current: RunMetric | null): boolean {
    if (current === null || lastMetric === null) return current !== lastMetric
    return (
      current.cellWidth !== lastMetric.cellWidth ||
      current.defaultSpacing !== lastMetric.defaultSpacing ||
      current.padY !== lastMetric.padY ||
      current.pitch !== lastMetric.pitch ||
      current.signature !== lastMetric.signature ||
      current.advanceOf !== lastMetric.advanceOf ||
      current.boxOf !== lastMetric.boxOf
    )
  }

  function apply(snapshot: ScreenSnapshot): void {
    surface.setAttribute('aria-colcount', String(snapshot.geometry.cols))
    // WHO OWNS THE CLICK, read from the frame the gesture already reads
    // (nocx-1ebrs.24). The backend ORs DECSET 1000/1002/1003 into one bit, and
    // selection-gesture.ts asks the same field to decide whether a double click
    // is a word or a report: one answer, two consumers. Set on the SURFACE and
    // not on a row, because rows are replaced by the level diff and the
    // pointer's state has to outlive every revision.
    surface.setAttribute(POINTER_ATTR, snapshot.state.mouseTracking ? 'program' : 'text')
    // The metric first: reading it is what begins the fit's row context,
    // which its warm() measures against.
    const metric = opts.metric?.() ?? null
    const palette = opts.palette()
    const repaintAll =
      rows.length !== snapshot.rows.length || metricChanged(metric) || palette !== lastPalette
    const toPaint = repaintAll
      ? snapshot.rows.map((_, r) => r)
      : snapshot.rows.flatMap((row, r) => {
          const prev = installed?.rows[r]
          return prev === undefined || !rowEquals(prev, row) ? [r] : []
        })
    // All writes, then all reads: one batch for exactly the rows about to
    // paint, before the first of them does.
    if (metric !== null && toPaint.length > 0) {
      opts.warm?.(fitCandidatesOf(toPaint.map((r) => snapshot.rows[r])))
    }
    if (repaintAll) {
      for (const row of rows) row.remove()
      rows = snapshot.rows.map((modelRow) => paintRow(modelRow, { metric, palette }))
      for (const row of rows) surface.insertBefore(row, cursor)
    } else {
      for (const r of toPaint) {
        const next = paintRow(snapshot.rows[r], { metric, palette })
        rows[r].replaceWith(next)
        rows[r] = next
      }
    }
    if (activeRow !== null && activeRow >= rows.length)
      activeRow = rows.length === 0 ? null : rows.length - 1
    updateRowAccessibility()
    lastMetric = metric
    lastPalette = palette
    noteCursorWrite(installed, snapshot)
    installed = snapshot
    placeCursor(snapshot)
    paintSelection(snapshot)
    paintLinkHighlight(snapshot)
  }

  function paintSelection(snapshot: ScreenSnapshot): void {
    for (const node of selectionNodes) node.remove()
    selectionNodes = []
    if (selection === null) return
    const ordered =
      selection.anchor.row < selection.focus.row ||
      (selection.anchor.row === selection.focus.row &&
        selection.anchor.offset <= selection.focus.offset)
        ? ([selection.anchor, selection.focus] as const)
        : ([selection.focus, selection.anchor] as const)
    const [start, end] = ordered
    const mapping = createMapping(snapshot)
    const dpr = displayDpr()
    const cellWidth = devicePxToCssPx(snapshot.geometry.cellWidthPx, dpr)
    const cellHeight = devicePxToCssPx(snapshot.geometry.cellHeightPx, dpr)
    for (let row = start.row; row <= end.row; row++) {
      if (row < 0 || row >= snapshot.rows.length) continue
      const from = row === start.row ? start.offset : 0
      const to = row === end.row ? end.offset : snapshot.rows[row].cells.length
      if (to <= from) continue
      const point = mapping.cellToPixel(from, row)
      if (point === null) continue
      const node = document.createElement('div')
      node.className = SELECTION_CLASS
      node.setAttribute('aria-hidden', 'true')
      node.dataset.row = String(row)
      node.dataset.start = String(from)
      node.dataset.end = String(to)
      node.style.left = `${point.x}px`
      node.style.top = `${point.y}px`
      node.style.width = `${(to - from) * cellWidth}px`
      node.style.height = `${cellHeight}px`
      surface.insertBefore(node, cursor)
      selectionNodes.push(node)
    }
  }

  /** Where a character span of one row's text lands on the grid: the
   *  columns its cells own. The row's text is its cells' graphemes joined —
   *  a blank column contributes nothing — which is the same text the pane's
   *  hit-test and the link grammar count offsets in, so this conversion is
   *  arithmetic over those cells and never a measurement. A grapheme is
   *  never split: a span ending inside one underlines the cell(s) it
   *  occupies, wide cluster included. Null when either end resolves to no
   *  inked cell — a stale range over a row that has since changed, which
   *  the tracker re-derives on the frame that changed it. */
  function columnsOfCharSpan(
    row: ScreenSnapshot['rows'][number],
    from: number,
    to: number,
  ): { from: number; to: number } | null {
    if (to <= from) return null
    let startCol: number | null = null
    let endCol: number | null = null
    let offset = 0
    for (const cell of row.cells) {
      const length = cell.grapheme.length
      if (length === 0) continue
      const end = offset + length
      if (startCol === null && end > from && offset < to) startCol = cell.column
      if (startCol !== null && end >= to) {
        endCol = cell.column + cell.span
        break
      }
      offset = end
    }
    return startCol !== null && endCol !== null && endCol > startCol
      ? { from: startCol, to: endCol }
      : null
  }

  function paintLinkHighlight(snapshot: ScreenSnapshot): void {
    for (const node of linkNodes) node.remove()
    linkNodes = []
    if (linkHighlight === null) return
    const row = snapshot.rows[linkHighlight.row]
    if (row === undefined) return
    const columns = columnsOfCharSpan(row, linkHighlight.from, linkHighlight.to)
    if (columns === null) return
    const point = createMapping(snapshot).cellToPixel(columns.from, linkHighlight.row)
    if (point === null) return
    const dpr = displayDpr()
    const node = document.createElement('div')
    node.className = LINK_CLASS
    node.setAttribute('aria-hidden', 'true')
    node.dataset.row = String(linkHighlight.row)
    node.dataset.start = String(columns.from)
    node.dataset.end = String(columns.to)
    node.style.left = `${point.x}px`
    node.style.top = `${point.y}px`
    node.style.width = `${(columns.to - columns.from) * devicePxToCssPx(snapshot.geometry.cellWidthPx, dpr)}px`
    node.style.height = `${devicePxToCssPx(snapshot.geometry.cellHeightPx, dpr)}px`
    surface.insertBefore(node, cursor)
    linkNodes.push(node)
  }

  function placeCursor(snapshot: ScreenSnapshot): void {
    const position = createMapping(snapshot).cellToPixel(snapshot.cursor.x, snapshot.cursor.y)
    const height = devicePxToCssPx(snapshot.geometry.cellHeightPx, displayDpr())
    if (position !== null) {
      // Told whatever the program decided about VISIBILITY: an IME's candidate
      // window follows where typing goes, not whether the caret is painted, and
      // a full-screen program that hid its cursor is still typed into.
      opts.onCaretPlaced?.({ left: position.x, top: position.y, height })
    }
    if (!snapshot.cursor.visible || position === null) {
      cursor.hidden = true
      return
    }
    cursor.hidden = false
    cursor.style.left = `${position.x}px`
    cursor.style.top = `${position.y}px`
    cursor.style.width = `${devicePxToCssPx(snapshot.geometry.cellWidthPx, displayDpr())}px`
    cursor.style.height = `${height}px`
    // A solid block covers its cell, so it draws the character it covers in
    // the cursor's accent colour (nocx-zg3k3.24): without it the letter about
    // to be overwritten disappears under the block. A row-tall line box puts
    // the glyph where the row's own glyph is.
    cursor.style.lineHeight = `${height}px`
    const covered = snapshot.rows[snapshot.cursor.y]?.cells[snapshot.cursor.x]
    const text = covered?.hasText ? covered.grapheme : ''
    if (cursor.textContent !== text) cursor.textContent = text
  }

  return {
    apply,

    setSelection(range) {
      selection = range === null ? null : { anchor: { ...range.anchor }, focus: { ...range.focus } }
      updateRowAccessibility()
      if (installed !== null) paintSelection(installed)
    },

    setLinkHighlight(range) {
      linkHighlight = range === null ? null : { ...range }
      if (installed !== null) paintLinkHighlight(installed)
    },

    mapping() {
      return installed === null ? null : createMapping(installed)
    },

    noteInput() {
      wakeCaret()
    },

    dispose() {
      if (caretIdle !== null) clearTimeout(caretIdle)
      caretIdle = null
      for (const row of rows) row.remove()
      rows = []
      installed = null
      lastMetric = null
      selection = null
      selectionNodes = []
      linkHighlight = null
      for (const node of linkNodes) node.remove()
      linkNodes = []
      cursor.remove()
      surface.removeEventListener('focusin', onFocus)
      surface.removeEventListener('focusout', onFocusOut)
      surface.removeEventListener('keydown', onKeyDown)
      surface.removeEventListener('pointerdown', onPointerDown)
      surface.removeAttribute('role')
      surface.removeAttribute('aria-label')
      surface.removeAttribute(POINTER_ATTR)
      surface.classList.remove(GRID_CLASS)
    },

    get surface(): HTMLElement {
      return surface
    },
  }
}

/** Rule 2's row comparison, over the wire's own vocabulary. Rows are
 *  positional (one entry per column, promised by the frame), so index
 *  alignment IS the identity; a repaint decision costs one pass over the
 *  cells, which is a rounding error next to building the DOM. */
function rowEquals(a: ScreenSnapshot['rows'][number], b: ScreenSnapshot['rows'][number]): boolean {
  if (a.wrap !== b.wrap || a.continuation !== b.continuation) return false
  if (a.cells.length !== b.cells.length) return false
  for (let i = 0; i < a.cells.length; i++) {
    const ca = a.cells[i]
    const cb = b.cells[i]
    if (
      ca.grapheme !== cb.grapheme ||
      ca.width !== cb.width ||
      ca.hasText !== cb.hasText ||
      !styleEquals(ca.style, cb.style)
    ) {
      return false
    }
  }
  return true
}

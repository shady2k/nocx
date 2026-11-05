// Pointer selection across the live cell painter and stored command cards.
// Endpoints are resolved from immutable cell snapshots and row identity; DOM
// text is never an input to selection or copy.
import {
  captureLiveSelection,
  type CellModel,
  type LiveSelection,
  type ScreenSnapshot,
} from '../cell-model'
import { snapshotForRows, storedBlockRowsForSelection } from '../scrollback/block-rows'
import type { CellPainter } from './painter'
import { columnToCell } from './mapping'
import { WORD_SEPARATORS, wordBounds } from '../word-selection'
import { insideLiveRegion } from '../live-region'

export interface LiveSelectionGestureOptions {
  readonly model: CellModel
  readonly painter: CellPainter
  readonly surface: HTMLElement
  readonly gestureRoot?: HTMLElement
  readonly surfaceId: string
  /** Whether the accepted backend frame says the program reports the mouse.
   *
   *  A DRAG is the person's either way (nocx-zg3k3.15.5): the pointer sequence
   *  reaches the program through the pane's own input renderer, never through
   *  this gesture, so standing down for a reporting program took the drag from
   *  the person and gave it to nobody. A DOUBLE click stays the program's
   *  (nocx-zg3k3.15.4). */
  readonly mouseReportingActive?: () => boolean
  /** Return keyboard input to the terminal input element after a joined gesture. */
  readonly focusInput?: () => void
  readonly copy: (text: string) => void
}

export interface LiveSelectionGesture {
  dispose(): void
  /** The program's screen changed: drop a selection that no longer describes
   *  what is on it (nocx-1ebrs.19).
   *
   *  A selection is the COPY SOURCE, so one that outlives its text is worse
   *  than a stale colour: a person copies what the program has since written
   *  there. Every other terminal clears when output lands on the selected
   *  cells, and so does this — compared against the snapshot the selection was
   *  captured on, cell by cell, because that is the snapshot its text came
   *  from. */
  screenChanged(snapshot: ScreenSnapshot): void
  /** Input went to the program: a SETTLED selection is over. The same rule as
   *  the one above, stated for the other direction of the conversation — a key
   *  sent to a full-screen program redraws the screen it was selected from.
   *
   *  A drag still under the pointer is not settled and is left alone: its text
   *  is copied at the release, so ending it early copies nothing. */
  dismiss(): void
  /** THE COPY CHORD (nocx-1ebrs.19.1). With a settled selection on screen,
   *  copy exactly what it paints — through the same sink copy-on-select writes
   *  at the release — and clear it; report false and change nothing when
   *  nothing is selected, so the chord keeps whatever else it means in this
   *  pane.
   *
   *  This is not a SECOND copy path: the text is already on the clipboard from
   *  the release (copy-on-select, docs/vision.md:113), and the chord rewrites
   *  the same value. What it adds is the answer a person expects — the chord
   *  copies the selection in front of them instead of reaching the program.
   *
   *  A drag still under the pointer is refused for the reason `dismiss` gives,
   *  and one more: `clear()` drops the ANCHOR as well, so a chord taken
   *  mid-drag would leave the release that follows with nothing to copy. */
  copyChord(): boolean
}

type Position = { readonly row: number; readonly offset: number }
type Endpoint =
  | { readonly kind: 'live'; readonly snapshot: ScreenSnapshot; readonly position: Position }
  | {
      readonly kind: 'card'
      readonly snapshot: ScreenSnapshot
      readonly blockId: string
      readonly artifactVersion: string
      readonly logicalLine: number
      readonly position: Position
    }

function asNode(target: EventTarget | null): Node | null {
  return target !== null && typeof (target as Node).nodeType === 'number' ? (target as Node) : null
}

/** How far a pressed pointer must travel before its motion is a drag, in
 *  CSS pixels — the distance Qt calls startDragDistance and GTK a drag
 *  threshold.
 *
 *  Measured defect nocx-zg3k3.14.19: roughly one click in five on the live
 *  grid selected the characters under the pointer and rewrote the clipboard.
 *  A cell is a handful of pixels wide, so hand tremor routinely carried a
 *  click's release into the neighbouring column; the press and the release
 *  then resolved to different cells, and different cells were read as a drag.
 *  Cell equality alone cannot tell a click from a drag at sub-cell distances;
 *  the MOTION can. 4 px sits above tremor and below half a cell, and the
 *  same-cell rule stands above it: a release on the pressed cell is a click
 *  at any distance. */
const DRAG_SLOP_PX = 4

export function installLiveSelectionGesture(
  opts: LiveSelectionGestureOptions,
): LiveSelectionGesture {
  let pinned = opts.model.current()
  let pinnedMapping = opts.painter.mapping()
  let anchor: Endpoint | null = null
  let selection: LiveSelection | null = null
  // The press's own pixels — the origin of the slop box below. Set in
  // `down`, read by `move` and `up`, and meaningless without an anchor.
  let pressX = 0
  let pressY = 0
  const root = opts.gestureRoot ?? opts.surface

  const livePosition = (event: PointerEvent): Endpoint | null => {
    if (!pinned) return null
    const rect = opts.surface.getBoundingClientRect()
    const cell = pinnedMapping?.pixelToCell(event.clientX - rect.left, event.clientY - rect.top)
    return cell
      ? { kind: 'live', snapshot: pinned, position: { row: cell.row, offset: cell.col } }
      : null
  }

  const cardPosition = (event: PointerEvent): Endpoint | null => {
    const node = asNode(event.target)
    const row = (node instanceof Element ? node : null)?.closest<HTMLElement>(
      '.term-grid-row[data-block-id][data-artifact-version][data-logical-line]',
    )
    if (!row || !row.contains(node)) return null
    const blockId = row.dataset.blockId
    const artifactVersion = row.dataset.artifactVersion
    const logicalLine = Number(row.dataset.logicalLine)
    if (!blockId || !artifactVersion || !Number.isInteger(logicalLine)) return null
    // The rows belong to the block element that painted them, and that
    // element is the `.cmd-output`'s parent (block-rows.ts `paintOutput`).
    const painted = row.closest<HTMLElement>('.cmd-output')?.parentElement ?? null
    const stored = painted === null ? null : storedBlockRowsForSelection(painted, artifactVersion)
    if (!stored) return null
    const rowIndex = stored.lines.findIndex((line) => line.from === logicalLine)
    const snapshot = snapshotForRows(stored.lines)
    if (!snapshot || rowIndex < 0) return null
    // The live mapping is the one geometry authority for both painted surfaces.
    // A card row has the same committed cell advance, while its own wire row
    // supplies grapheme spans and wide-cell ownership.
    const p0 = pinnedMapping?.cellToPixel(0, 0)
    const p1 = pinnedMapping?.cellToPixel(1, 0)
    if (!p0 || !p1) return null
    const cellWidth = p1.x - p0.x
    if (cellWidth <= 0) return null
    const rect = row.getBoundingClientRect()
    const column = Math.max(
      0,
      Math.min(snapshot.geometry.cols - 1, Math.floor((event.clientX - rect.left) / cellWidth)),
    )
    const col = columnToCell(snapshot, rowIndex, column)
    return {
      kind: 'card',
      snapshot,
      blockId,
      artifactVersion,
      logicalLine,
      position: { row: rowIndex, offset: col },
    }
  }

  const endpoint = (event: PointerEvent): Endpoint | null => {
    const node = asNode(event.target)
    const element = node instanceof Element ? node : null
    const cardRow = element?.closest('.term-grid-row[data-block-id]')
    if (cardRow) return cardPosition(event)
    // THE GRID'S OWN ROWS, WHEREVER THE GRID IS SEATED (ADR-0091). While a
    // command runs the live region is that command's block body, so a live row
    // has a `.cmd-block` ancestor — and every rule below this one was written
    // when that could not happen. A row of the grid is a row of the grid: the
    // press, the drag and the word all resolve through the cell snapshot, as
    // they did while the region sat outside every block. Read first, because
    // the block test underneath is about a block's own rows — its stored
    // `.term-line` output, which has no cells of its own to map.
    if (insideLiveRegion(node)) return livePosition(event)
    // A plain command block may contain selectable text but does not have
    // stored-row identity. It belongs to native text selection, never a
    // guessed live-grid cell based on coordinates outside the painter.
    if (element?.closest('.cmd-block')) return null
    return livePosition(event)
  }

  const drawLive = (point: Endpoint | null) => {
    if (anchor?.kind === 'live' && point?.kind === 'live') {
      const forward =
        anchor.position.row < point.position.row ||
        (anchor.position.row === point.position.row &&
          anchor.position.offset <= point.position.offset)
      const start = forward ? anchor.position : point.position
      const end = forward ? point.position : anchor.position
      selection = captureLiveSelection(opts.surfaceId, pinned!, start, {
        row: end.row,
        offset: end.offset + 1,
      })
      opts.painter.setSelection({ anchor: selection.anchor, focus: selection.focus })
      return
    }
    const live = anchor?.kind === 'live' ? anchor : point?.kind === 'live' ? point : null
    if (live && (anchor?.kind === 'card' || point?.kind === 'card')) {
      // Historical cards display above the live surface. The selected live
      // segment therefore starts at the top of the pinned live snapshot and
      // ends at the live endpoint, independent of drag direction.
      selection = captureLiveSelection(
        opts.surfaceId,
        live.snapshot,
        { row: 0, offset: 0 },
        {
          row: live.position.row,
          offset: live.position.offset + 1,
        },
      )
      opts.painter.setSelection({ anchor: selection.anchor, focus: selection.focus })
      return
    }
    opts.painter.setSelection(null)
  }

  const down = (event: PointerEvent) => {
    if (!root.contains(event.target as Node) || event.button !== 0) return
    // A REPORTING PROGRAM IS NO REASON TO STAND DOWN (nocx-zg3k3.15.5). This
    // returned early whenever the accepted frame said the program reports the
    // mouse, on the reasoning that a reporting program "must receive the
    // original pointer sequence, not a selection gesture that prevents its
    // default handling". That reasoning was about xterm, whose mousedown this
    // gesture's preventDefault suppressed, and xterm is gone (ADR-0072): the
    // sequence is emitted by the pane's own input renderer, from its own
    // listeners, and this listener is not in front of it. Measured against a
    // real full-screen reporting program: press, drag motions and the release
    // all still reach it with this gesture active.
    //
    // What standing down did instead was hand the drag to the BROWSER — a
    // native selection over the painter's rows, a second claimant on one input
    // — which the pane clears again at the release when it returns the
    // keyboard to its input element. So the highlight a person made vanished
    // as they let go, and no frame repainted it, because the painter's model
    // never held it.
    pinned = opts.model.current()
    pinnedMapping = opts.painter.mapping()
    if (!pinned || !pinnedMapping) return
    const point = endpoint(event)
    if (!point) return
    // A PRESS PAINTS NOTHING (nocx-1ebrs.19): a click without a drag must
    // leave no selection, and clear one that is already on screen - which is
    // what every terminal does. Painting the pressed cell here is what made a
    // single click select the character under the pointer: `drawLive` with an
    // anchor equal to the point captures a one-cell range and highlights it
    // before the person has dragged anything. So the press clears the PAINT
    // only - the anchor stays, `selection` starts null, and the first paint
    // happens in `move`, where a drag exists to paint.
    anchor = point
    pressX = event.clientX
    pressY = event.clientY
    selection = null
    opts.painter.setSelection(null)
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', up, { once: true })
    // Card-only selection uses the browser's native text selection and its
    // existing copy-on-select listener. Keep that path intact. A drag from a
    // card into the live grid is still joined by the handlers below.
    if (point.kind === 'live') event.preventDefault()
  }

  const copyAcross = (a: Endpoint, b: Endpoint): string | null => {
    if (a.kind === 'live' && b.kind === 'live') {
      const forward =
        a.position.row < b.position.row ||
        (a.position.row === b.position.row && a.position.offset <= b.position.offset)
      const start = forward ? a.position : b.position
      const end = forward ? b.position : a.position
      return captureLiveSelection(opts.surfaceId, a.snapshot, start, {
        row: end.row,
        offset: end.offset + 1,
      }).copy()
    }
    if (a.kind === 'card' && b.kind === 'card') {
      if (a.blockId !== b.blockId || a.artifactVersion !== b.artifactVersion) return null
      const forward =
        a.position.row < b.position.row ||
        (a.position.row === b.position.row && a.position.offset <= b.position.offset)
      const start = forward ? a.position : b.position
      const end = forward ? b.position : a.position
      return captureLiveSelection(`card:${a.blockId}:${a.artifactVersion}`, a.snapshot, start, {
        row: end.row,
        offset: end.offset + 1,
      }).copy()
    }
    const card = a.kind === 'card' ? a : b.kind === 'card' ? b : null
    const live = a.kind === 'live' ? a : b.kind === 'live' ? b : null
    if (!card || !live) return null
    const cardText = captureLiveSelection(
      `card:${card.blockId}:${card.artifactVersion}`,
      card.snapshot,
      card.position,
      {
        row: card.snapshot.rows.length - 1,
        offset: card.snapshot.rows[card.snapshot.rows.length - 1]?.cells.length ?? 0,
      },
    ).copy()
    const liveText = captureLiveSelection(
      opts.surfaceId,
      live.snapshot,
      { row: 0, offset: 0 },
      {
        row: live.position.row,
        offset: live.position.offset + 1,
      },
    ).copy()
    const lastCardRow = card.snapshot.rows[card.snapshot.rows.length - 1]
    return `${cardText}${lastCardRow && !lastCardRow.wrap ? '\n' : ''}${liveText}`
  }

  const tripleClick = (event: MouseEvent) => {
    if (event.detail !== 3) return
    const node = asNode(event.target)
    // A LIVE ROW'S TRIPLE CLICK IS THE BROWSER'S, as it always was (ADR-0091):
    // the rule below selects a row of the block's own output through the DOM,
    // and a row of the grid has no block output to select — while the region
    // sat outside every block this handler never saw one. Standing down keeps
    // the two surfaces' gestures as they were rather than giving the grid a
    // second row rule the cell painter does not know about.
    if (insideLiveRegion(node)) return
    const element = node instanceof Element ? node : null
    if (element?.closest('[data-block-control], .ui-context-menu')) return
    const block = element?.closest<HTMLElement>('.cmd-block')
    const target =
      element?.closest<HTMLElement>('.cmd-block .term-grid-row, .cmd-block .term-line') ??
      block?.querySelector<HTMLElement>('.cmd-output') ??
      block
    if (!target) return
    // The browser's triple-click default does not select a whole preformatted
    // cell row consistently. Select the row explicitly; terminal-content's
    // existing mouseup copy-on-select writer then writes it exactly once.
    const range = document.createRange()
    range.selectNodeContents(target)
    const selected = window.getSelection()
    if (!selected) return
    event.preventDefault()
    selected.removeAllRanges()
    selected.addRange(range)
  }

  /** Whether the pointer is still inside the press's slop box: the motion
   *  a click carries, never a drag (nocx-zg3k3.14.19). */
  const withinSlop = (event: PointerEvent): boolean =>
    Math.max(Math.abs(event.clientX - pressX), Math.abs(event.clientY - pressY)) < DRAG_SLOP_PX

  const move = (event: PointerEvent) => {
    if (!anchor) return
    // A DRAG IS A MOTION (nocx-zg3k3.14.19). Until the pointer has left the
    // slop box around the press, nothing has been dragged, so nothing
    // paints: a press with tremor on it is still a click being made, and
    // painting on the first two-pixel move is the flash a person read as
    // "my click selected a letter". The release decides a click the same way.
    if (withinSlop(event)) return
    // During a drag, the browser may keep targeting the source block or a
    // gap between the card and live grid. Coordinates still use the painter's
    // mapping, so resolve a live endpoint there instead of losing the gesture.
    const point = endpoint(event) ?? (anchor.kind === 'card' ? livePosition(event) : null)
    if (!point) return
    if (anchor.kind === 'card' && point.kind === 'live') opts.focusInput?.()
    drawLive(point)
  }

  const up = (event: PointerEvent) => {
    window.removeEventListener('pointermove', move)
    if (!anchor) return
    const focus = endpoint(event) ?? (anchor.kind === 'card' ? livePosition(event) : null)
    if (
      withinSlop(event) ||
      (anchor.kind === 'live' &&
        focus?.kind === 'live' &&
        focus.position.row === anchor.position.row &&
        focus.position.offset === anchor.position.offset)
    ) {
      // A press and release on one cell is a click — how a person gives the
      // terminal focus — not a one-cell selection. Copying it would replace
      // the clipboard with whatever character sat under the pointer. So is a
      // release that never left the press's slop box, whichever cell it landed
      // in (nocx-zg3k3.14.19): tremor across a cell boundary is not a drag.
      clear()
    } else if (focus && (anchor.kind === 'live' || focus.kind === 'live')) {
      drawLive(focus)
      const text = copyAcross(anchor, focus)
      if (text !== null) opts.copy(text)
      // A card-to-live drag may also have produced a native DOM range while
      // the browser delivered the drag. Clear it before mouseup so the
      // scrollback's native copy-on-select listener cannot write a second
      // (and different) value.
      if (anchor.kind === 'card') window.getSelection()?.removeAllRanges()
    } else if (!focus) {
      clear()
    }
    anchor = null
    // THE SELECTION OUTLIVES THE GESTURE THAT MADE IT (nocx-1ebrs.19). What
    // ends is the DRAG; the highlight stays, because it is what the person
    // reads and copies from — and because its lifetime is now somebody else's
    // question (`screenChanged`, `dismiss`). This used to be nulled here, and
    // nothing kept it: the painter went on painting a selection the gesture no
    // longer knew about, which is why nothing could ever clear it.
    //
    // AND `pinned` STAYS THE CAPTURE. It is the snapshot the settled
    // selection's text came from, so it is also what `touched` must ask.
    // Refreshing it here — to whatever frame happened to land last, during the
    // drag or after it — let a cell that changed WHILE the drag was being made
    // pass as unchanged, and reported the captured text coming BACK as a
    // change. The next press reads both again (`down`), which is the only
    // later reader.
  }

  // A double click on the live grid selects the token under the pointer by
  // the frozen block's own rule (word-selection.ts; nocx-zg3k3.15.4), so a
  // path, a URL or a flag is one word on both surfaces. The token is read
  // from the cell snapshot, one character per cell: a cell is a separator
  // only when its whole grapheme is one, and an empty cell is a space.
  const doubleClick = (event: MouseEvent) => {
    if (event.button !== 0 || !root.contains(event.target as Node)) return
    if (opts.mouseReportingActive?.()) return
    const point = endpoint(event as PointerEvent)
    if (point?.kind !== 'live') return
    const cells = point.snapshot.rows[point.position.row]?.cells ?? []
    const text = cells
      .map((cell) => {
        if (!cell.hasText || cell.grapheme === '') return ' '
        return WORD_SEPARATORS.includes(cell.grapheme) ? cell.grapheme : 'w'
      })
      .join('')
    const { start, end } = wordBounds(text, point.position.offset)
    if (start === end) return
    const range = captureLiveSelection(
      opts.surfaceId,
      point.snapshot,
      { row: point.position.row, offset: start },
      { row: point.position.row, offset: end },
    )
    selection = range
    opts.painter.setSelection({ anchor: range.anchor, focus: range.focus })
    const copied = range.copy()
    if (copied !== '') opts.copy(copied)
  }

  /** Drop the live selection in the painter and in this gesture's own state. */
  const clear = (): void => {
    anchor = null
    selection = null
    opts.painter.setSelection(null)
  }

  /** Do the two snapshots disagree anywhere inside the selected range? */
  const touched = (next: ScreenSnapshot): boolean => {
    if (selection === null || pinned === null) return false
    const start = selection.anchor
    const end = selection.focus
    if (start.surfaceId !== opts.surfaceId) return false
    const fromRow = Math.min(start.row, end.row)
    const toRow = Math.max(start.row, end.row)
    for (let row = fromRow; row <= toRow; row++) {
      const before = pinned.rows[row]?.cells ?? []
      const after = next.rows[row]?.cells ?? []
      const from = row === start.row ? start.offset : 0
      const to = row === end.row ? end.offset : Math.max(before.length, after.length)
      // A ROW THAT IS NO LONGER THERE IS A CHANGE: the screen shrank, so the
      // text the selection named is gone.
      if (next.rows[row] === undefined && before.length > 0) return true
      for (let column = Math.max(0, from); column < to; column++) {
        const a = before[column]
        const b = after[column]
        if (a === undefined || b === undefined) {
          if (a !== b) return true
          continue
        }
        if (a.grapheme !== b.grapheme || a.hasText !== b.hasText) return true
      }
    }
    return false
  }

  // THE COPY CHORD IS NOT CLAIMED HERE (nocx-1ebrs.19). Whether a keystroke is
  // this pane's to answer is a question about the PANE — which surface holds
  // the keyboard, whether an overlay is up, whether the composer is on screen —
  // and a listener on `window` inside this gesture answers it with "any pane
  // holding a selection", which is neither of the owners a chord has. It also
  // could not CLEAR what it copied, which is what the decision asks for. The
  // pane claims it in the capture phase and calls `copyChord` below; the
  // gesture keeps the state and the copy, and nothing else.
  root.addEventListener('pointerdown', down)
  root.addEventListener('mousedown', tripleClick)
  root.addEventListener('dblclick', doubleClick)
  /** A drag still under the pointer is not a selection yet: the range is not
   *  final, the person has not let go, and `move` redraws it from the capture
   *  on the next pointer event. So nothing outside the pointer may end it
   *  (nocx-1ebrs.19).
   *
   *  `clear()` is the wrong answer here for a second, worse reason: it drops
   *  the ANCHOR as well as the highlight, so the release that follows finds no
   *  gesture and copies NOTHING. The person's selection is lost, not merely
   *  unhighlighted — which is the opposite failure to the one this rule was
   *  written for, and it is the one e2e/live-to-card-joined.spec.ts measured:
   *  it types into a running program mid-drag to force a frame, the pane
   *  forwards those keys as intents, and the clipboard finished the gesture
   *  holding the text written before it. */
  const dragging = (): boolean => anchor !== null

  return {
    screenChanged(snapshot) {
      if (dragging()) return
      if (touched(snapshot)) clear()
    },

    dismiss() {
      if (dragging()) return
      if (selection !== null) clear()
    },

    copyChord() {
      if (dragging() || selection === null) return false
      const text = selection.copy()
      if (text !== '') opts.copy(text)
      clear()
      return true
    },

    dispose() {
      root.removeEventListener('pointerdown', down)
      root.removeEventListener('mousedown', tripleClick)
      root.removeEventListener('dblclick', doubleClick)
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', up)
      anchor = null
      selection = null
    },
  }
}

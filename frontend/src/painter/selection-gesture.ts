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

export interface LiveSelectionGestureOptions {
  readonly model: CellModel
  readonly painter: CellPainter
  readonly surface: HTMLElement
  readonly gestureRoot?: HTMLElement
  readonly surfaceId: string
  readonly copy: (text: string) => void
}

export interface LiveSelectionGesture {
  dispose(): void
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

export function installLiveSelectionGesture(
  opts: LiveSelectionGestureOptions,
): LiveSelectionGesture {
  let pinned = opts.model.current()
  let pinnedMapping = opts.painter.mapping()
  let anchor: Endpoint | null = null
  let selection: LiveSelection | null = null
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
    const stored = storedBlockRowsForSelection(blockId, artifactVersion)
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
    const cardRow = (node instanceof Element ? node : null)?.closest(
      '.term-grid-row[data-block-id]',
    )
    return cardRow ? cardPosition(event) : livePosition(event)
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
    pinned = opts.model.current()
    pinnedMapping = opts.painter.mapping()
    if (!pinned || !pinnedMapping) return
    const point = endpoint(event)
    if (!point) return
    anchor = point
    selection = null
    drawLive(point)
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', up, { once: true })
    event.preventDefault()
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

  const move = (event: PointerEvent) => {
    if (!anchor) return
    const point = endpoint(event)
    if (!point) return
    drawLive(point)
  }

  const up = (event: PointerEvent) => {
    window.removeEventListener('pointermove', move)
    if (!anchor) return
    const focus = endpoint(event)
    if (focus) {
      drawLive(focus)
      const text = copyAcross(anchor, focus)
      if (text !== null) opts.copy(text)
    } else {
      opts.painter.setSelection(null)
    }
    anchor = null
    selection = null
    pinned = opts.model.current()
    pinnedMapping = opts.painter.mapping()
  }

  root.addEventListener('pointerdown', down)
  return {
    dispose() {
      root.removeEventListener('pointerdown', down)
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', up)
      anchor = null
      selection = null
    },
  }
}

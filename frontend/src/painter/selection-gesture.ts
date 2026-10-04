// Pointer selection over the live cell painter. The snapshot is pinned on
// pointerdown and every endpoint is resolved by the painter mapping; DOM text
// is never an input to selection or copy.
import { captureLiveSelection, type CellModel, type LiveSelection } from '../cell-model'
import type { CellPainter } from './painter'

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

export function installLiveSelectionGesture(
  opts: LiveSelectionGestureOptions,
): LiveSelectionGesture {
  let pinned = opts.model.current()
  let pinnedMapping = opts.painter.mapping()
  let anchor: { row: number; offset: number } | null = null
  let selection: LiveSelection | null = null
  const position = (event: PointerEvent) => {
    const rect = opts.surface.getBoundingClientRect()
    const cell = pinnedMapping?.pixelToCell(event.clientX - rect.left, event.clientY - rect.top)
    if (!cell) return null
    return { row: cell.row, offset: cell.col }
  }
  const down = (event: PointerEvent) => {
    if (!(opts.gestureRoot ?? opts.surface).contains(event.target as Node) || event.button !== 0)
      return
    pinned = opts.model.current()
    pinnedMapping = opts.painter.mapping()
    if (!pinned || !pinnedMapping) return
    const point = position(event)
    if (!point) return
    anchor = point
    selection = null
    opts.painter.setSelection({ anchor: point, focus: point })
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', up, { once: true })
    event.preventDefault()
  }
  const move = (event: PointerEvent) => {
    if (!pinned || !anchor) return
    const point = position(event)
    if (!point) return
    const forward =
      anchor.row < point.row || (anchor.row === point.row && anchor.offset <= point.offset)
    const start = forward ? anchor : point
    const end = forward ? point : anchor
    selection = captureLiveSelection(opts.surfaceId, pinned, start, {
      row: end.row,
      offset: end.offset + 1,
    })
    opts.painter.setSelection({ anchor: selection.anchor, focus: selection.focus })
  }
  const up = (event: PointerEvent) => {
    window.removeEventListener('pointermove', move)
    if (!pinned || !anchor) return
    if (position(event) === null) {
      selection = null
      opts.painter.setSelection(null)
      anchor = null
      pinned = opts.model.current()
      pinnedMapping = opts.painter.mapping()
      return
    }
    move(event)
    if (selection) opts.copy(selection.copy())
    anchor = null
    pinned = opts.model.current()
    pinnedMapping = opts.painter.mapping()
  }
  const root = opts.gestureRoot ?? opts.surface
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

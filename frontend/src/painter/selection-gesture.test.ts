// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { createCellModel } from '../cell-model'
import { DEFAULT_SNAPSHOT } from '../scrollback/serializer'
import { frameOf, snapshotOf } from './fixtures'
import { createCellPainter } from './painter'
import { installLiveSelectionGesture } from './selection-gesture'

describe('live selection gesture', () => {
  it('copies cells from the pointerdown revision when frames advance during a drag', () => {
    const model = createCellModel()
    const root = document.createElement('div')
    const surface = document.createElement('div')
    root.append(surface)
    document.body.append(root)
    const first = frameOf(
      1,
      [
        [
          ['a', 1, true],
          ['漢', 2, true],
          ['', 3, false],
          ['b', 1, true],
        ],
      ],
      { x: 0, y: 0, visible: false },
      { cols: 4, rows: 1, cellWidthPx: 8, cellHeightPx: 20 },
    )
    const firstSnapshot = snapshotOf(first)
    model.apply(first)
    const painter = createCellPainter({ surface, palette: DEFAULT_SNAPSHOT })
    painter.apply(firstSnapshot)
    vi.spyOn(surface, 'getBoundingClientRect').mockReturnValue({
      left: 0,
      top: 0,
      right: 32,
      bottom: 20,
      width: 32,
      height: 20,
      x: 0,
      y: 0,
      toJSON: () => ({}),
    })
    const copy = vi.fn()
    const gesture = installLiveSelectionGesture({
      model,
      painter,
      surface,
      gestureRoot: root,
      surfaceId: 'live',
      copy,
    })
    const event = (type: string, x: number) =>
      new MouseEvent(type, { bubbles: true, button: 0, clientX: x, clientY: 5 })
    root.dispatchEvent(event('pointerdown', 8))
    const next = frameOf(
      2,
      [
        [
          ['z', 1, true],
          ['z', 1, true],
          ['z', 1, true],
          ['z', 1, true],
        ],
      ],
      { x: 0, y: 0, visible: false },
      { cols: 4, rows: 1, cellWidthPx: 8, cellHeightPx: 20 },
    )
    model.apply(next)
    painter.apply(snapshotOf(next))
    window.dispatchEvent(event('pointermove', 24))
    expect(surface.querySelector('.term-grid-selection')).not.toBeNull()
    window.dispatchEvent(event('pointerup', 24))
    expect(copy).toHaveBeenCalledWith('漢b')
    gesture.dispose()
    painter.dispose()
    root.remove()
  })
})

// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { createCellModel } from '../cell-model'
import { DEFAULT_SNAPSHOT } from '../scrollback/serializer'
import { frameOf, snapshotOf } from './fixtures'
import { createCellPainter } from './painter'
import { installLiveSelectionGesture } from './selection-gesture'
import { paintStoredRows, type StoredBlockRows } from '../scrollback/block-rows'
import { wireRowOf } from './fixtures'

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

  it('copies a drag from live output into a card in display order using stored rows', () => {
    const model = createCellModel()
    const root = document.createElement('div')
    const surface = document.createElement('div')
    root.append(surface)
    const card = document.createElement('article')
    card.dataset.entryId = 'block-7'
    root.append(card)
    document.body.append(root)
    const live = frameOf(
      1,
      [
        [
          ['x', 1, true],
          ['y', 1, true],
          ['z', 1, true],
          ['w', 1, true],
        ],
      ],
      { x: 0, y: 0, visible: false },
      { cols: 4, rows: 1, cellWidthPx: 8, cellHeightPx: 20 },
    )
    model.apply(live)
    const painter = createCellPainter({ surface, palette: DEFAULT_SNAPSHOT })
    painter.apply(snapshotOf(live))
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
    const rows: StoredBlockRows = {
      lines: [
        {
          from: 12,
          row: wireRowOf(
            [
              ['a', 1, true],
              ['漢', 2, true],
              ['', 3, false],
            ],
            true,
          ),
        },
        { from: 13, row: wireRowOf([['b', 1, true]]) },
        { from: 14, row: wireRowOf([['c', 1, true]]) },
      ],
      artifactVersion: 'artifact-v4',
      droppedRows: 0,
      lostRows: 0,
      truncated: null,
    }
    paintStoredRows(card, rows, { metric: null, palette: DEFAULT_SNAPSHOT })
    const originalRow = card.querySelector<HTMLElement>('.term-grid-row')!
    const cardRow = originalRow.cloneNode(true) as HTMLElement
    cardRow.textContent = 'DOM MUST NOT BE COPIED'
    originalRow.replaceWith(cardRow)
    const output = card.querySelector('.cmd-output')!
    output.insertBefore(output.lastElementChild!, cardRow)
    vi.spyOn(cardRow, 'getBoundingClientRect').mockReturnValue({
      left: 0,
      top: 30,
      right: 24,
      bottom: 50,
      width: 24,
      height: 20,
      x: 0,
      y: 30,
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
    const event = (type: string, target: HTMLElement, x: number, y: number) => {
      const e = new MouseEvent(type, { bubbles: true, button: 0, clientX: x, clientY: y })
      Object.defineProperty(e, 'pointerId', { value: 1 })
      target.dispatchEvent(e)
    }
    event('pointerdown', surface, 1, 5)
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
    event('pointermove', cardRow, 8, 35)
    expect(surface.querySelector('.term-grid-selection')).not.toBeNull()
    event('pointerup', cardRow, 8, 35)
    expect(copy).toHaveBeenCalledWith('漢b\nc\nx')
    expect(surface.querySelector('.term-grid-row')?.textContent).toBe('zzzz')
    gesture.dispose()

    // Reverse the drag. Display ordering, not drag direction, defines copy.
    const reverseCopy = vi.fn()
    const reverse = installLiveSelectionGesture({
      model,
      painter,
      surface,
      gestureRoot: root,
      surfaceId: 'live',
      copy: reverseCopy,
    })
    event('pointerdown', cardRow, 8, 35)
    event('pointermove', surface, 1, 5)
    event('pointerup', surface, 1, 5)
    expect(reverseCopy).toHaveBeenCalledWith('漢b\nc\nz')
    reverse.dispose()
    painter.dispose()
    root.remove()
  })
})

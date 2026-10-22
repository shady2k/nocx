// @vitest-environment jsdom
//
// The live grid's half of the armed-link gesture (nocx-1ebrs.48): while
// ⌘/⌃ is held, the pointer's link is underlined on the PAINTED grid and the
// pointer becomes a hand; release, blur or leaving the surface disarms.
//
// ADR-0072 gave the underline's job to the cell painter (the overlay) and
// left nobody tracking the pointer for links; this module is that tracker.
// It owns WHICH cells and WHEN — the grammar stays detect.ts's, the armed
// decision stays the policy's (`ranges` answers [] unarmed), the overlay
// geometry stays the painter's. Everything here is asserted through those
// real neighbours: the real ArmedTracker driven by real key events, the
// real LivePolicy, the real painter, with only the pane's own pixel seam
// (getBoundingClientRect) faked the way selection-gesture.test.ts fakes it.
import { describe, expect, it, vi } from 'vitest'
import { createCellModel, type ScreenSnapshot } from '../cell-model'
import { DEFAULT_SNAPSHOT } from '../scrollback/serializer'
import { createCellPainter } from '../painter/painter'
import { frameOf, snapshotOf, type CellSpec } from '../painter/fixtures'
import { trackLinkModifier } from './armed'
import { createLivePolicy } from './live'
import type { LinkOpener } from './open'
import { installLiveLinkHover, LINK_HOVER_ATTR } from './live-hover'

// `see ` before the url: the grammar needs the word boundary, and a real
// line has one.
const URL_ROW: CellSpec[] = [...'see https://e.co'].map((ch) => [ch, 1, true] as CellSpec)

function harness() {
  const model = createCellModel()
  const surface = document.createElement('div')
  document.body.append(surface)
  const painter = createCellPainter({ surface, palette: () => DEFAULT_SNAPSHOT })
  const first = frameOf(
    1,
    [URL_ROW],
    { x: 0, y: 0, visible: false },
    { cols: 16, rows: 1, cellWidthPx: 8, cellHeightPx: 20 },
  )
  model.apply(first)
  painter.apply(snapshotOf(first))
  vi.spyOn(surface, 'getBoundingClientRect').mockReturnValue({
    left: 0,
    top: 0,
    right: 128,
    bottom: 20,
    width: 128,
    height: 20,
    x: 0,
    y: 0,
    toJSON: () => ({}),
  })
  const armed = trackLinkModifier()
  const opener: LinkOpener = { open: () => Promise.resolve() }
  const policy = createLivePolicy({
    opener,
    origin: () => null,
    armed,
    notify: () => {},
  })
  // The pane's own seam (terminal-content's lineAtPoint): the model's
  // current revision, THE mapping, the row's text and the pointer's UTF-16
  // offset into it.
  const cellAt = (x: number, y: number) => {
    const snapshot: ScreenSnapshot | null = model.current()
    const mapping = painter.mapping()
    if (snapshot === null || mapping === null) return null
    const cell = mapping.pixelToCell(x, y)
    if (cell === null) return null
    const row = snapshot.rows[cell.row]
    if (!row) return null
    return {
      row: cell.row,
      text: row.cells.map((item) => item.grapheme).join(''),
      offset: row.cells.slice(0, cell.col).reduce((total, item) => total + item.grapheme.length, 0),
    }
  }
  const hover = installLiveLinkHover({
    surface,
    armed,
    spans: (lineText) => policy.ranges(lineText),
    cellAt,
    highlight: painter,
  })
  const move = (x: number, y = 10): void => {
    surface.dispatchEvent(new MouseEvent('pointermove', { bubbles: true, clientX: x, clientY: y }))
  }
  const key = (type: 'keydown' | 'keyup', down: boolean): void => {
    window.dispatchEvent(new KeyboardEvent(type, { key: 'Meta', metaKey: down, bubbles: true }))
  }
  return { model, surface, painter, armed, policy, hover, move, key, cellAt }
}

const underline = (surface: HTMLElement): HTMLElement | null =>
  surface.querySelector<HTMLElement>('.term-grid-link')

describe('the armed link hover on the live grid (nocx-1ebrs.48)', () => {
  it('underlines the pointer\u2019s link while the modifier is held, and says the pointer is over a link', () => {
    const { surface, hover, painter } = harness()
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Meta', metaKey: true }))
    // Column 6 is inside `https://e.co` (columns 4..15).
    surface.dispatchEvent(new MouseEvent('pointermove', { clientX: 48, clientY: 10 }))
    const link = underline(surface)
    expect(link).not.toBeNull()
    expect(link?.dataset.start).toBe('4')
    expect(link?.dataset.end).toBe('16')
    expect(surface.getAttribute(LINK_HOVER_ATTR)).toBe('true')
    hover.dispose()
    painter.dispose()
  })

  it('underlines nothing over ordinary text, and says so', () => {
    const { surface, hover, painter } = harness()
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Meta', metaKey: true }))
    // Column 0 is the `s` of `see`.
    surface.dispatchEvent(new MouseEvent('pointermove', { clientX: 0, clientY: 10 }))
    expect(underline(surface)).toBeNull()
    expect(surface.getAttribute(LINK_HOVER_ATTR)).toBeNull()
    hover.dispose()
    painter.dispose()
  })

  it('draws nothing while the modifier is up, even over a link', () => {
    const { surface, hover, painter } = harness()
    surface.dispatchEvent(new MouseEvent('pointermove', { clientX: 40, clientY: 10 }))
    expect(underline(surface)).toBeNull()
    expect(surface.getAttribute(LINK_HOVER_ATTR)).toBeNull()
    hover.dispose()
    painter.dispose()
  })

  it('disarms on the key release, and re-arms at the pointer\u2019s last position', () => {
    const { surface, hover, painter, move, key } = harness()
    key('keydown', true)
    move(48)
    expect(underline(surface)).not.toBeNull()
    key('keyup', false)
    expect(underline(surface)).toBeNull()
    expect(surface.getAttribute(LINK_HOVER_ATTR)).toBeNull()
    // The modifier comes back under a pointer that has not moved: the
    // underline must come back with it, at the same cells.
    key('keydown', true)
    const link = underline(surface)
    expect(link?.dataset.start).toBe('4')
    expect(link?.dataset.end).toBe('16')
    hover.dispose()
    painter.dispose()
  })

  it('clears when the pointer leaves the surface', () => {
    const { surface, hover, painter, move, key } = harness()
    key('keydown', true)
    move(48)
    expect(underline(surface)).not.toBeNull()
    surface.dispatchEvent(new MouseEvent('pointerleave'))
    expect(underline(surface)).toBeNull()
    expect(surface.getAttribute(LINK_HOVER_ATTR)).toBeNull()
    hover.dispose()
    painter.dispose()
  })

  it('re-derives at the pointer when the screen changes under it', () => {
    const { model, surface, painter, hover, move, key } = harness()
    key('keydown', true)
    move(48)
    expect(underline(surface)).not.toBeNull()
    // The program prints over the row: the url moves right by one column.
    const next = frameOf(
      2,
      [[...'seee https://e.co'].map((ch) => [ch, 1, true] as CellSpec)],
      { x: 0, y: 0, visible: false },
      { cols: 17, rows: 1, cellWidthPx: 8, cellHeightPx: 20 },
    )
    model.apply(next)
    hover.screenChanged()
    painter.apply(model.current()!)
    const link = underline(surface)
    expect(link?.dataset.start).toBe('5')
    expect(link?.dataset.end).toBe('17')
    hover.dispose()
    painter.dispose()
  })

  it('stops listening on dispose, and leaves no state behind', () => {
    const { surface, painter, hover, move, key, armed } = harness()
    key('keydown', true)
    move(48)
    expect(underline(surface)).not.toBeNull()
    hover.dispose()
    armed.dispose()
    expect(surface.getAttribute(LINK_HOVER_ATTR)).toBeNull()
    expect(underline(surface)).toBeNull()
    // A disposed tracker repaints nothing, even with the modifier still held.
    move(0)
    expect(underline(surface)).toBeNull()
    expect(surface.getAttribute(LINK_HOVER_ATTR)).toBeNull()
    painter.dispose()
  })
})

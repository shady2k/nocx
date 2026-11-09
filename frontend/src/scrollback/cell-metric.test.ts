// @vitest-environment jsdom
// Cell-metric publisher (nocx-yy9g): the renderer's real cell width is
// published to the scrollback container as custom properties the frozen
// block layout consumes. jsdom computes no layout, so the natural-advance
// measurement is stubbed here — these tests pin the plumbing (the metric is
// fetched, published, applied; 0/unmeasurable publishes nothing); only a
// real browser can confirm the pixel geometry (see the task report).
import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  gridRows,
  measureNaturalAdvance,
  publishCellMetric,
  publishRowPitch,
  type CellMetric,
} from './cell-metric'

const WIDTH_ONLY = { width: 640, height: 16 } as DOMRect

function stubProbeRect(container: HTMLElement, rect: DOMRect): HTMLElement {
  // publishCellMetric creates the probe lazily; create it the way the
  // module does so the test can stub its measurement.
  const probe = container.querySelector<HTMLElement>('.cell-metric-probe')
  const el =
    probe ??
    (() => {
      const p = document.createElement('span')
      p.className = 'cell-metric-probe'
      p.textContent = 'W'.repeat(64)
      container.appendChild(p)
      return p
    })()
  vi.spyOn(el, 'getBoundingClientRect').mockReturnValue(rect)
  return el
}

afterEach(() => {
  vi.restoreAllMocks()
  document.body.replaceChildren()
})

describe('measureNaturalAdvance', () => {
  it('measures the probe width divided by its character count', () => {
    const container = document.createElement('div')
    document.body.appendChild(container)
    stubProbeRect(container, { width: 640, height: 16 } as DOMRect)
    // 64 W's at 10px each = 640px.
    expect(measureNaturalAdvance(container)).toBe(10)
  })

  it('reuses one probe across calls', () => {
    const container = document.createElement('div')
    document.body.appendChild(container)
    stubProbeRect(container, WIDTH_ONLY)
    measureNaturalAdvance(container)
    measureNaturalAdvance(container)
    expect(container.querySelectorAll('.cell-metric-probe')).toHaveLength(1)
  })

  it('returns 0 when the probe has no measurable width (jsdom has no layout)', () => {
    const container = document.createElement('div')
    document.body.appendChild(container)
    expect(measureNaturalAdvance(container)).toBe(0)
  })
})

describe('publishCellMetric', () => {
  it('publishes the cell width and the per-character correction', () => {
    const container = document.createElement('div')
    document.body.appendChild(container)
    const measure = vi.fn(() => 8.4) // the DOM's natural advance
    const metric = publishCellMetric(container, 8.5, measure)
    expect(metric).toEqual<CellMetric>({
      cellWidth: 8.5,
      naturalAdvance: 8.4,
      delta: 0.1,
    })
    expect(container.style.getPropertyValue('--term-cell-width')).toBe('8.5px')
    expect(container.style.getPropertyValue('--term-cell-delta')).toBe('0.1px')
  })

  it('handles a negative delta — the DOM running WIDER than the grid', () => {
    const container = document.createElement('div')
    document.body.appendChild(container)
    const metric = publishCellMetric(container, 8.5, () => 8.8)
    expect(metric?.delta).toBe(-0.3)
    expect(container.style.getPropertyValue('--term-cell-delta')).toBe('-0.3px')
  })

  it('publishes nothing while the renderer cannot measure (cellWidth 0)', () => {
    const container = document.createElement('div')
    document.body.appendChild(container)
    const measure = vi.fn(() => 8.4)
    expect(publishCellMetric(container, 0, measure)).toBeNull()
    expect(container.style.getPropertyValue('--term-cell-width')).toBe('')
    expect(container.style.getPropertyValue('--term-cell-delta')).toBe('')
    expect(measure).not.toHaveBeenCalled()
  })

  it('publishes nothing for NaN/Infinity cell widths', () => {
    const container = document.createElement('div')
    document.body.appendChild(container)
    expect(publishCellMetric(container, Number.NaN, () => 8.4)).toBeNull()
    expect(publishCellMetric(container, Number.POSITIVE_INFINITY, () => 8.4)).toBeNull()
    expect(container.style.getPropertyValue('--term-cell-width')).toBe('')
  })

  it('publishes nothing when the natural advance is unmeasurable (0)', () => {
    const container = document.createElement('div')
    document.body.appendChild(container)
    expect(publishCellMetric(container, 8.5, () => 0)).toBeNull()
    expect(container.style.getPropertyValue('--term-cell-width')).toBe('')
    expect(container.style.getPropertyValue('--term-cell-delta')).toBe('')
  })
})

describe('publishRowPitch', () => {
  it('publishes the grid row pitch the frozen block lays its lines out on', () => {
    const container = document.createElement('div')
    document.body.appendChild(container)
    expect(publishRowPitch(container, 20)).toBe(20)
    expect(container.style.getPropertyValue('--term-cell-height')).toBe('20px')
  })

  it('publishes nothing while the renderer cannot measure, so the stylesheet fallback stands', () => {
    // A pitch of 0 would collapse every frozen line to nothing — worse than
    // the guess it replaces, so an unmeasurable renderer publishes nothing at
    // all and `.term-line` keeps its `1.2em` fallback.
    const container = document.createElement('div')
    document.body.appendChild(container)
    expect(publishRowPitch(container, 0)).toBeNull()
    expect(publishRowPitch(container, Number.NaN)).toBeNull()
    expect(publishRowPitch(container, Number.POSITIVE_INFINITY)).toBeNull()
    expect(container.style.getPropertyValue('--term-cell-height')).toBe('')
  })
})

describe('gridRows (independent read, 2026-11-08)', () => {
  it('counts the rows a box holds even when the division lands a hair short', () => {
    // The owner's numbers: a 26-device-pixel cell at DPR 1.5 is a
    // 17.333333333333332px pitch, and 31 of them are a box of
    // 537.3333333333334px. The quotient is 30.999999999999996 in IEEE double
    // arithmetic for the pair below, so `Math.floor` alone gives the program 30
    // rows while the alternate screen, reading the same box as a raw height,
    // gives it 31 — and the switch back sends a resize.
    const pitch = 26 / 1.5
    const box = 31 * pitch
    expect(box).toBeCloseTo(537.3333333333334, 10)
    expect(Math.floor(537.3333333333334 / 17.333333333333336)).toBe(30)
    expect(gridRows(537.3333333333334, 17.333333333333336)).toBe(31)
    // The derived pair, which is the one the pane actually produces.
    expect(gridRows(box, pitch)).toBe(31)
    // And the box derived from the count divides back to the same count, so
    // `fitUsableViewport` cannot lose the row it was handed.
    expect(gridRows(gridRows(box, pitch) * pitch, pitch)).toBe(31)
  })

  it('keeps a whole remainder below one row, and answers zero for no box', () => {
    expect(gridRows(3 * 16, 16)).toBe(3)
    expect(gridRows(3 * 16 - 1, 16)).toBe(2)
    expect(gridRows(0, 16)).toBe(0)
    expect(gridRows(100, 0)).toBe(0)
  })
})

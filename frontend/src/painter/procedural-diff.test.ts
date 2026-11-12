// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'
import { createCellPainter } from './painter'
import { paintRow } from './paint-row'
import { frameOf, snapshotOf, styleOf, type CellSpec } from './fixtures'
import { DEFAULT_SNAPSHOT } from '../scrollback/serializer'
import type { RunMetric } from '../scrollback/run-geometry'

const metric: RunMetric = { cellWidth: 8, defaultSpacing: 0, pitch: 20, advanceOf: () => null }
const palette = DEFAULT_SNAPSHOT
const screen = (suffix: string, border = '─', style = styleOf()) =>
  snapshotOf(
    frameOf(1, [
      [...Array.from({ length: 119 }, (): CellSpec => [border, 1, true, style]), [suffix, 1, true]],
    ]),
  )

describe('sparse updates beside procedural borders', () => {
  it('rebuilds procedural ink when the palette changes without a content change', () => {
    let currentPalette = palette
    const surface = document.createElement('div')
    const painter = createCellPainter({
      surface,
      metric: () => metric,
      palette: () => currentPalette,
    })
    const state = screen('0', '─', styleOf({ attributes: 1 << 4 }))
    painter.apply(state)
    const oldSvg = surface.querySelector('svg')
    const oldMarkup = surface.querySelector('.term-grid-row')?.innerHTML
    currentPalette = { ...palette, defaultFg: '#ffffff', defaultBg: '#000000' }
    painter.apply(state)
    expect(surface.querySelector('svg')).not.toBe(oldSvg)
    expect(surface.querySelector('.term-grid-row')?.innerHTML).not.toBe(oldMarkup)
    expect(surface.querySelector('.term-grid-row')?.innerHTML).toBe(
      paintRow(state.rows[0], { metric, palette: currentPalette }).innerHTML,
    )
    painter.dispose()
  })

  it('rebuilds procedural geometry when the metric changes without a content change', () => {
    let currentMetric = metric
    const surface = document.createElement('div')
    const painter = createCellPainter({
      surface,
      metric: () => currentMetric,
      palette: () => palette,
    })
    const state = screen('0')
    painter.apply(state)
    const oldSvg = surface.querySelector('svg')
    const oldMarkup = surface.querySelector('.term-grid-row')?.innerHTML
    currentMetric = { ...metric, cellWidth: 10, pitch: 24 }
    painter.apply(state)
    expect(surface.querySelector('svg')).not.toBe(oldSvg)
    expect(surface.querySelector('.term-grid-row')?.innerHTML).not.toBe(oldMarkup)
    expect(surface.querySelector('.term-grid-row')?.innerHTML).toBe(
      paintRow(state.rows[0], { metric: currentMetric, palette }).innerHTML,
    )
    painter.dispose()
  })

  it('replaces a changed procedural run without removing a later unchanged SVG', () => {
    const surface = document.createElement('div')
    const painter = createCellPainter({ surface, metric: () => metric, palette: () => palette })
    const state = (revision: number, first: string) =>
      snapshotOf(
        frameOf(revision, [
          [
            [first, 1, true],
            ['a', 1, true],
            ['│', 1, true],
          ],
        ]),
      )
    painter.apply(state(1, '─'))
    const [firstSvg, lastSvg] = surface.querySelectorAll('svg')
    expect(firstSvg).toBeDefined()
    expect(lastSvg).toBeDefined()
    const next = state(2, '┼')
    painter.apply(next)
    const svgs = surface.querySelectorAll('svg')
    expect(svgs).toHaveLength(2)
    expect(svgs[0]).not.toBe(firstSvg)
    expect(svgs[1]).toBe(lastSvg)
    expect(surface.querySelector('.term-grid-row')?.innerHTML).toBe(
      paintRow(next.rows[0], { metric, palette }).innerHTML,
    )
    painter.dispose()
  })

  it('keeps row focus and selection while moving the cursor across an updated row', () => {
    const surface = document.createElement('div')
    document.body.appendChild(surface)
    const painter = createCellPainter({ surface, metric: () => metric, palette: () => palette })
    const state = (revision: number, suffix: string, cursorX: number) =>
      snapshotOf(
        frameOf(
          revision,
          [
            [
              ['─', 1, true],
              ['─', 1, true],
              [suffix, 1, true],
            ],
          ],
          { x: cursorX, y: 0, visible: true },
          { cols: 3, rows: 1, cellWidthPx: 8, cellHeightPx: 20 },
        ),
      )
    try {
      painter.apply(state(1, 'a', 1))
      const row = surface.querySelector<HTMLElement>('.term-grid-row')!
      row.focus()
      painter.setSelection({ anchor: { row: 0, offset: 0 }, focus: { row: 0, offset: 2 } })
      painter.apply(state(2, 'b', 2))
      expect(document.activeElement).toBe(row)
      expect(row.getAttribute('aria-current')).toBe('true')
      expect(row.getAttribute('aria-selected')).toBe('true')
      const selection = surface.querySelector<HTMLElement>('.term-grid-selection')!
      expect(selection.style.left).toBe('0px')
      expect(selection.style.width).toBe('16px')
      expect(selection.style.height).toBe('20px')
      const cursor = surface.querySelector<HTMLElement>('.term-grid-cursor')!
      expect(cursor.hidden).toBe(false)
      expect(cursor.style.left).toBe('16px')
      expect(cursor.style.top).toBe('0px')
      expect(row.textContent).toBe('──b')
    } finally {
      painter.dispose()
      surface.remove()
    }
  })

  it('compares against installed geometry even when measurement verdicts disappear', () => {
    let measured = true
    const changingMetric: RunMetric = {
      ...metric,
      boxOf: (chars) => (measured && chars === '⬢' ? { cols: 1, fit: 0.8 } : null),
    }
    const surface = document.createElement('div')
    const painter = createCellPainter({
      surface,
      metric: () => changingMetric,
      palette: () => palette,
    })
    painter.apply(
      snapshotOf(
        frameOf(1, [
          [
            ['⬢', 1, true],
            ['a', 1, true],
            ['─', 1, true],
          ],
        ]),
      ),
    )
    measured = false
    const next = snapshotOf(
      frameOf(2, [
        [
          ['b', 1, true],
          ['a', 1, true],
          ['─', 1, true],
        ],
      ]),
    )
    painter.apply(next)
    expect(surface.querySelector('.term-grid-row')?.innerHTML).toBe(
      paintRow(next.rows[0], { metric: changingMetric, palette }).innerHTML,
    )
    painter.dispose()
  })

  it('keeps unchanged ink and row identity while changing the copyable suffix', () => {
    const surface = document.createElement('div')
    const painter = createCellPainter({ surface, metric: () => metric, palette: () => palette })
    painter.apply(screen('0'))
    const row = surface.firstChild
    const svg = surface.querySelector('svg')
    painter.apply(screen('1'))
    expect(surface.firstChild).toBe(row)
    expect(surface.querySelector('svg')).toBe(svg)
    expect(surface.querySelector('.term-grid-row')?.textContent).toBe('─'.repeat(119) + '1')
    const fresh = paintRow(screen('1').rows[0], { metric, palette })
    expect(surface.querySelector('.term-grid-row')?.innerHTML).toBe(fresh.innerHTML)
    painter.dispose()
  })

  it('matches fresh ink when borders, styles, run boundaries and wide cells change', () => {
    const surface = document.createElement('div')
    const painter = createCellPainter({ surface, metric: () => metric, palette: () => palette })
    const states = [
      screen('0'),
      screen('1', '┼'),
      screen('2', '│', styleOf({ attributes: 1 })),
      snapshotOf(
        frameOf(2, [
          [
            ['漢', 2, true],
            ['', 3, false],
            ['─', 1, true],
            ['a', 1, true],
          ],
        ]),
      ),
      snapshotOf(
        frameOf(3, [
          [
            ['a', 1, true],
            ['─', 1, true],
            ['─', 1, true],
            ['b', 1, true],
          ],
        ]),
      ),
    ]
    for (const state of states) {
      painter.apply(state)
      expect(surface.querySelector('.term-grid-row')?.innerHTML).toBe(
        paintRow(state.rows[0], { metric, palette }).innerHTML,
      )
    }
    painter.dispose()
  })
})

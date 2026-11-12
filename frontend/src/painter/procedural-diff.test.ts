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

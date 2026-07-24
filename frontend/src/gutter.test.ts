// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import { visibleGlyphs, Gutter } from './gutter'
import type { CommandRecord } from './command-ledger'
import type { TerminalRenderer } from './renderers/types'

function fakeRecord(
  overrides: Partial<CommandRecord> & { id: number; lineOfVal: number },
): CommandRecord {
  return {
    id: overrides.id,
    command: overrides.command ?? 'cmd',
    cwd: overrides.cwd ?? '/',
    host: overrides.host ?? '',
    status: overrides.status ?? 'success',
    exitCode: overrides.exitCode ?? 0,
    startedAt: overrides.startedAt ?? null,
    endedAt: overrides.endedAt ?? null,
    trusted: overrides.trusted ?? true,
    lineOf: () => overrides.lineOfVal,
    disposed: overrides.disposed ?? false,
  }
}

describe('visibleGlyphs', () => {
  it('returns empty for no records', () => {
    expect(visibleGlyphs([], 0, 24, 2)).toEqual([])
  })

  it('culls records with disposed markers', () => {
    const recs = [fakeRecord({ id: 1, lineOfVal: 5, disposed: true })]
    expect(visibleGlyphs(recs, 0, 24, 2)).toEqual([])
  })

  it('culls records whose lineOf returns undefined (marker disposed)', () => {
    const recs = [fakeRecord({ id: 1, lineOfVal: undefined as unknown as number, disposed: false })]
    // lineOf returns undefined so it's effectively culled
    const result = visibleGlyphs(recs, 0, 24, 2)
    expect(result).toHaveLength(0)
  })

  it('keeps records exactly at viewport top', () => {
    const recs = [fakeRecord({ id: 1, lineOfVal: 10 })]
    const result = visibleGlyphs(recs, 10, 24, 0)
    expect(result).toEqual([{ record: recs[0], top: 0 }])
  })

  it('keeps records at the last visible row', () => {
    const recs = [fakeRecord({ id: 1, lineOfVal: 33 })]
    // viewportTopLine=10, rows=24, overscan=0 → visible range [10, 34)
    const result = visibleGlyphs(recs, 10, 24, 0)
    expect(result).toEqual([{ record: recs[0], top: 23 }])
  })

  it('culls records above viewport (before overscan)', () => {
    const recs = [fakeRecord({ id: 1, lineOfVal: 5 })]
    // viewportTopLine=10, overscan=0 → visible starts at 10
    expect(visibleGlyphs(recs, 10, 24, 0)).toEqual([])
  })

  it('culls records below viewport', () => {
    const recs = [fakeRecord({ id: 1, lineOfVal: 40 })]
    // viewportTopLine=0, rows=24, overscan=0 → visible ends at 23
    expect(visibleGlyphs(recs, 0, 24, 0)).toEqual([])
  })

  it('includes records within overscan above', () => {
    const recs = [fakeRecord({ id: 1, lineOfVal: 8 })]
    // viewportTopLine=10, overscan=5 → visible starts at 5
    const result = visibleGlyphs(recs, 10, 24, 5)
    expect(result).toHaveLength(1)
    expect(result[0].top).toBe(-2)
  })

  it('includes records within overscan below', () => {
    const recs = [fakeRecord({ id: 1, lineOfVal: 36 })]
    // viewportTopLine=10, rows=24, overscan=3 → visible max = 10+24+3 = 37 (exclusive)
    const result = visibleGlyphs(recs, 10, 24, 3)
    expect(result).toHaveLength(1)
    expect(result[0].top).toBeGreaterThanOrEqual(24)
  })

  it('computes top pixel correctly for various positions', () => {
    const recs = [
      fakeRecord({ id: 1, lineOfVal: 50 }),
      fakeRecord({ id: 2, lineOfVal: 60 }),
      fakeRecord({ id: 3, lineOfVal: 75 }),
    ]
    const result = visibleGlyphs(recs, 50, 30, 0)
    expect(result).toEqual([
      { record: recs[0], top: 0 },
      { record: recs[1], top: 10 },
      { record: recs[2], top: 25 },
    ])
  })

  it('handles mixed visible and non-visible records', () => {
    const recs = [
      fakeRecord({ id: 1, lineOfVal: 5 }), // above
      fakeRecord({ id: 2, lineOfVal: 50 }), // visible
      fakeRecord({ id: 3, lineOfVal: 55 }), // visible
      fakeRecord({ id: 4, lineOfVal: 200 }), // below
      fakeRecord({ id: 5, lineOfVal: 52, disposed: true }), // disposed
    ]
    const result = visibleGlyphs(recs, 48, 10, 0)
    // Visible range: [48, 58). Records at 50, 55 are visible; 5 and 200 are not.
    expect(result).toHaveLength(2)
    expect(result[0].record.id).toBe(2)
    expect(result[1].record.id).toBe(3)
  })

  it('handles zero rows', () => {
    const recs = [fakeRecord({ id: 1, lineOfVal: 1 })]
    expect(visibleGlyphs(recs, 0, 0, 0)).toEqual([])
  })

  it('handles negative viewport (should not happen but safe)', () => {
    const recs = [fakeRecord({ id: 1, lineOfVal: 0 })]
    const result = visibleGlyphs(recs, -5, 10, 0)
    expect(result).toEqual([{ record: recs[0], top: 5 }])
  })
})

describe('Gutter.mount', () => {
  it('does not override the pane position (regression: broke multi-tab layout)', () => {
    // .pane is `position:absolute` via style.css, so its inline style.position
    // is empty. The gutter must NOT force `position:relative` inline — doing so
    // dropped the pane out of its absolute overlay and stacked panes in flow,
    // which made all tabs vanish when a second tab opened.
    const pane = document.createElement('div')
    const renderer = {
      paneElement: pane,
      onScroll: () => {},
      onRender: () => {},
      cellHeight: 16,
      viewportTopLine: 0,
      rows: 24,
    } as unknown as TerminalRenderer
    const g = new Gutter()
    g.mount(renderer)
    expect(pane.style.position).toBe('')
    expect(pane.querySelector('.nocx-gutter')).not.toBeNull()
    g.dispose()
  })
})

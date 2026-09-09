// @vitest-environment jsdom
// Tests for the frozen-line drift instrument (nocx-4n6sj).
//
// The instrument answers ONE question before anybody pays for nocx-ec18:
// how often, and by how much, is a frozen line wider than the columns the
// grid gave it. These tests cover the arithmetic and the bookkeeping; the
// measurement itself is layout, so it is proven in the browser (e2e), not
// here — jsdom computes no layout, which is exactly how 6211 green tests sat
// on top of a broken product.

import { describe, it, expect, beforeEach } from 'vitest'
import {
  emptyState,
  accumulateLines,
  accumulateGlyphs,
  report,
  loadState,
  saveState,
  isEnabled,
  setEnabled,
  MAX_GLYPHS,
  measureFrozenBlock,
  type Measurers,
  STORAGE_KEY,
  ENABLED_KEY,
} from './cell-drift'

const AT = '2026-09-05T00:00:00.000Z'

describe('accumulateLines', () => {
  it('counts a line that occupies exactly its columns as clean', () => {
    const s = accumulateLines(emptyState(AT), 8, [{ cols: 152, widthPx: 1216 }])
    expect(s.lines).toBe(1)
    expect(s.drifted).toBe(0)
    expect(s.wider).toBe(0)
    expect(s.worstCols).toBe(0)
  })

  it('reports the measured omp line as one column too wide', () => {
    // The real number from the owner's pane: 152 columns at 8px is 1216px,
    // and the row with ⬢ ⟳ ⟲ 🗑 laid out 8.65px wider — one whole column,
    // which is the corner that does not meet.
    const s = accumulateLines(emptyState(AT), 8, [{ cols: 152, widthPx: 1224.65 }])
    expect(s.drifted).toBe(1)
    expect(s.wider).toBe(1)
    expect(s.wideByAColumn).toBe(1)
    expect(s.worstCols).toBeCloseTo(1.081, 3)
    expect(s.buckets['1-2']).toBe(1)
  })

  it('separates a line the DOM lays out NARROWER than the grid', () => {
    // cellWidth − naturalAdvance may be positive on another platform font;
    // the block then runs narrow, which is the same defect and must not be
    // averaged away against the wide ones.
    const s = accumulateLines(emptyState(AT), 8, [{ cols: 100, widthPx: 780 }])
    expect(s.narrower).toBe(1)
    expect(s.wider).toBe(0)
    expect(s.wideByAColumn).toBe(0)
    expect(s.worstCols).toBeCloseTo(2.5, 6)
  })

  it('ignores sub-pixel noise so the report is not all drift', () => {
    const s = accumulateLines(emptyState(AT), 8, [{ cols: 40, widthPx: 320.2 }])
    expect(s.lines).toBe(1)
    expect(s.drifted).toBe(0)
  })

  it('skips a line with no columns rather than dividing by the grid', () => {
    const s = accumulateLines(emptyState(AT), 8, [{ cols: 0, widthPx: 0 }])
    expect(s.lines).toBe(0)
  })

  it('counts the lines a cap left unmeasured instead of pretending they were clean', () => {
    const lines = Array.from({ length: 10 }, () => ({ cols: 10, widthPx: 80 }))
    const s = accumulateLines(emptyState(AT), 8, lines, 4)
    expect(s.lines).toBe(4)
    expect(s.cappedLines).toBe(6)
  })

  it('counts one block per accumulate call', () => {
    let s = accumulateLines(emptyState(AT), 8, [{ cols: 10, widthPx: 80 }])
    s = accumulateLines(s, 8, [{ cols: 10, widthPx: 80 }])
    expect(s.blocks).toBe(2)
  })
})

describe('accumulateGlyphs', () => {
  it('names a cluster that fits neither one column nor two', () => {
    // 🗑 measured at 14px against an 8px cell: 6px too wide for one column,
    // 2px too narrow for two. Both numbers are reported because the grid's
    // own answer is not visible from the DOM, and guessing it would be the
    // instrument inventing the finding it was built to check.
    const s = accumulateGlyphs(emptyState(AT), 8, [
      { cluster: '\u{1F5D1}', advancePx: 14, lines: 3 },
    ])
    const g = report(s).offenders[0]
    expect(g.codepoints).toBe('U+1F5D1')
    expect(g.missIfOneCol).toBeCloseTo(6, 6)
    expect(g.missIfTwoCols).toBeCloseTo(-2, 6)
    expect(g.fitsNeither).toBe(true)
    expect(g.lines).toBe(3)
  })

  it('leaves a cluster that lands on the cell out of the offender list', () => {
    // ◫ measured at 8.4297 against a natural advance of 8.4287 — the
    // letter-spacing correction already carries it, and reporting it would
    // bury the four that matter under a hundred that do not.
    const s = accumulateGlyphs(emptyState(AT), 8, [{ cluster: '◫', advancePx: 8.02, lines: 9 }])
    expect(report(s).offenders).toEqual([])
  })

  it('adds up the lines a cluster was seen on across blocks', () => {
    let s = accumulateGlyphs(emptyState(AT), 8, [{ cluster: '⬢', advancePx: 9.34, lines: 2 }])
    s = accumulateGlyphs(s, 8, [{ cluster: '⬢', advancePx: 9.34, lines: 5 }])
    expect(report(s).offenders[0].lines).toBe(7)
  })

  it('ranks offenders by the damage they do, not by how odd they look', () => {
    // A small miss on many lines outweighs a large miss seen once: the fix
    // is prioritised by columns lost, which is what a reader sees.
    const s = accumulateGlyphs(emptyState(AT), 8, [
      { cluster: '\u{1F5D1}', advancePx: 14, lines: 1 },
      { cluster: '⟳', advancePx: 9.52, lines: 400 },
    ])
    expect(report(s).offenders.map((o) => o.cluster)).toEqual(['⟳', '\u{1F5D1}'])
  })

  it('keeps the store bounded, dropping the least damaging first', () => {
    const probes = Array.from({ length: MAX_GLYPHS + 20 }, (_, i) => ({
      cluster: String.fromCodePoint(0x3000 + i),
      advancePx: 8 + (i + 1) * 0.1,
      lines: 1,
    }))
    const s = accumulateGlyphs(emptyState(AT), 8, probes)
    expect(Object.keys(s.glyphs).length).toBe(MAX_GLYPHS)
    // The widest miss is the last one generated; it must have survived.
    expect(s.glyphs[String.fromCodePoint(0x3000 + probes.length - 1)]).toBeDefined()
  })

  it('spells a multi-codepoint cluster out in full', () => {
    const s = accumulateGlyphs(emptyState(AT), 8, [
      { cluster: '\u{1F5D1}️', advancePx: 14, lines: 1 },
    ])
    expect(report(s).offenders[0].codepoints).toBe('U+1F5D1 U+FE0F')
  })
})

describe('report', () => {
  it('says plainly that nothing has been measured yet', () => {
    const r = report(emptyState(AT))
    expect(r.lines).toBe(0)
    expect(r.offenders).toEqual([])
    expect(r.since).toBe(AT)
  })

  it('reports the share of lines that drifted, which is the whole question', () => {
    let s = emptyState(AT)
    s = accumulateLines(s, 8, [
      { cols: 100, widthPx: 800 },
      { cols: 100, widthPx: 800 },
      { cols: 100, widthPx: 800 },
      { cols: 100, widthPx: 809 },
    ])
    const r = report(s)
    expect(r.lines).toBe(4)
    expect(r.drifted).toBe(1)
    expect(r.driftedShare).toBe('25.0%')
  })
})

describe('storage', () => {
  beforeEach(() => localStorage.clear())

  it('is off until somebody turns it on, and stays on across a restart', () => {
    expect(isEnabled()).toBe(false)
    setEnabled(true)
    expect(localStorage.getItem(ENABLED_KEY)).toBe('1')
    expect(isEnabled()).toBe(true)
    setEnabled(false)
    expect(isEnabled()).toBe(false)
  })

  it('round-trips a state through storage', () => {
    const s = accumulateLines(emptyState(AT), 8, [{ cols: 152, widthPx: 1224.65 }])
    saveState(s)
    expect(loadState()).toEqual(s)
  })

  it('starts clean rather than throwing when the stored value is corrupt', () => {
    localStorage.setItem(STORAGE_KEY, '{not json')
    expect(loadState().lines).toBe(0)
  })

  it('stores no command text — only codepoints and counters', () => {
    let s = accumulateLines(emptyState(AT), 8, [{ cols: 30, widthPx: 250 }])
    s = accumulateGlyphs(s, 8, [{ cluster: '⬢', advancePx: 9.34, lines: 1 }])
    saveState(s)
    const raw = localStorage.getItem(STORAGE_KEY) ?? ''
    // The only strings in the blob are the bucket labels, the timestamp and
    // the single clusters themselves.
    expect(raw).not.toMatch(/[A-Za-z]{6,}\s+[A-Za-z]{6,}/)
    expect(raw).toContain('⬢')
  })
})

describe('measureFrozenBlock', () => {
  beforeEach(() => localStorage.clear())

  function block(...lines: string[]): HTMLElement {
    const out = document.createElement('div')
    out.className = 'cmd-output'
    for (const l of lines) {
      const span = document.createElement('span')
      span.className = 'term-line'
      span.textContent = l
      out.appendChild(span)
    }
    document.body.appendChild(out)
    return out
  }

  const fixed = (width: number, advance: number): Measurers => ({
    lineWidth: () => width,
    clusterAdvance: () => advance,
  })

  it('records nothing when the rows and the counts disagree', () => {
    // Something rewrote the DOM between serialization and here. A count
    // paired with the wrong row reports drift that is only the pairing being
    // off, and a silent wrong number is worse than no instrument at all.
    const el = block('aaa', 'bbb')
    expect(measureFrozenBlock(el, [3], 8, fixed(24, 8))).toBe(false)
    expect(loadState().lines).toBe(0)
  })

  it('measures every row against the columns the grid gave it', () => {
    const el = block('aaaa', 'bbbb')
    expect(measureFrozenBlock(el, [4, 4], 8, fixed(32, 8))).toBe(true)
    const s = loadState()
    expect(s.lines).toBe(2)
    expect(s.drifted).toBe(0)
  })

  it('probes the non-ASCII clusters and leaves ASCII alone', () => {
    const el = block('ok ⬢ ⬢ done', 'plain ascii')
    measureFrozenBlock(el, [11, 11], 8, fixed(88, 9.34))
    const offenders = report(loadState()).offenders
    expect(offenders.map((o) => o.cluster)).toEqual(['⬢'])
    // Seen twice on one line, on one line — the report ranks by rows a
    // person reads, not by occurrences.
    expect(offenders[0].lines).toBe(1)
  })

  it('reuses a stored advance instead of measuring the same glyph twice', () => {
    let probes = 0
    const counting: Measurers = {
      lineWidth: () => 88,
      clusterAdvance: () => {
        probes++
        return 9.34
      },
    }
    measureFrozenBlock(block('a ⬢'), [3], 8, counting)
    measureFrozenBlock(block('b ⬢'), [3], 8, counting)
    expect(probes).toBe(1)
    expect(report(loadState()).offenders[0].lines).toBe(2)
  })
})

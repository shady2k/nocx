// Procedural cell ink (nocx-1ebrs.15): box drawing U+2500..257F and block
// elements U+2580..259F are drawn TO CELL BOUNDS from the cell's geometry,
// not composited from the font's glyph. The font's glyph is designed for a
// typographic cell and cannot tile: a vertical U+2502 run painted from it is
// dashed, because each cell's glyph is shorter than the cell's line height.
//
// These tests assert the painter's own segment geometry — the painted result,
// not the font. The seam assertions ARE the regression test the bead asks
// for: two vertically adjacent U+2502 cells must cover the shared edge with
// no gap, a horizontal run must cover the shared boundary, and a block must
// fill its cell exactly at the cell's own aspect.

import { describe, it, expect } from 'vitest'
import { cellInkShapes, isProceduralInk, thicknessBaseOf, type InkRect } from './cell-ink'

const W = 8
const H = 20

const rectsOf = (chars: string, w = W, h = H, base = 1): InkRect[] =>
  cellInkShapes(chars, w, h, base).filter((s): s is InkRect => s.kind === 'rect')

describe('isProceduralInk', () => {
  it('claims box drawing and block elements, and nothing else', () => {
    expect(isProceduralInk('─')).toBe(true)
    expect(isProceduralInk('│')).toBe(true)
    expect(isProceduralInk('╬')).toBe(true)
    expect(isProceduralInk('╭')).toBe(true)
    expect(isProceduralInk('╱')).toBe(true)
    expect(isProceduralInk('█')).toBe(true)
    expect(isProceduralInk('▒')).toBe(true)
    expect(isProceduralInk('▏')).toBe(true)
    expect(isProceduralInk('\u{2500}')).toBe(true)
    expect(isProceduralInk('\u{259F}')).toBe(true)
    expect(isProceduralInk('a')).toBe(false)
    expect(isProceduralInk('漢')).toBe(false)
    expect(isProceduralInk('■')).toBe(false) // U+25A0, outside both blocks
    expect(isProceduralInk('\u{24FF}')).toBe(false)
    expect(isProceduralInk('\u{25A0}')).toBe(false)
    expect(isProceduralInk('')).toBe(false)
  })

  it('claims a run only when every codepoint is procedural', () => {
    expect(isProceduralInk('││')).toBe(true)
    expect(isProceduralInk('│\u{FE0E}')).toBe(false) // a cluster — the font owns it
    expect(isProceduralInk('a│')).toBe(false)
  })
})

describe('thicknessBaseOf', () => {
  it('is one pixel at the ordinary cell, and scales with the cell', () => {
    expect(thicknessBaseOf(8, 20)).toBe(1)
    expect(thicknessBaseOf(16, 40)).toBe(2)
    expect(thicknessBaseOf(7, 15)).toBe(1)
  })
})

describe('U+2502 paints edge to edge — the dashed-line regression', () => {
  it('draws one vertical band covering the full cell height, past both edges', () => {
    const rects = rectsOf('│')
    expect(rects).toHaveLength(1)
    const [r] = rects
    // No gap pixels at either row edge: the band reaches past y=0 and y=H,
    // the half-pixel overshoot that makes adjacent rows' paint overlap.
    expect(r.y).toBeLessThanOrEqual(0)
    expect(r.y + r.h).toBeGreaterThanOrEqual(H)
    // Centered on the cell's vertical centerline, one light pixel wide.
    expect(r.x).toBeLessThanOrEqual(W / 2)
    expect(r.x + r.w).toBeGreaterThanOrEqual(W / 2)
    expect(r.w).toBe(1)
  })

  it('joins two vertically adjacent cells with no gap at the shared edge', () => {
    // The bead's regression test, on the painter's own geometry: the top
    // cell's band must reach the shared edge, the bottom cell's must start
    // at it, and both must sit on the same columns — the union then covers
    // the edge, so the painted line is continuous.
    const top = rectsOf('│')[0]
    const bottom = rectsOf('│')[0]
    expect(top.y + top.h).toBeGreaterThanOrEqual(H)
    expect(bottom.y).toBeLessThanOrEqual(0)
    expect(top.x).toBe(bottom.x)
    expect(top.w).toBe(bottom.w)
  })

  it('pins the rounding convention: a light band in an 8px cell sits at x=4', () => {
    expect(rectsOf('│')[0].x).toBe(4)
  })
})

describe('U+2500 and the horizontal seam', () => {
  it('draws one horizontal band covering the full cell width, past both edges', () => {
    const rects = rectsOf('─')
    expect(rects).toHaveLength(1)
    const [r] = rects
    expect(r.x).toBeLessThanOrEqual(0)
    expect(r.x + r.w).toBeGreaterThanOrEqual(W)
    expect(r.y).toBeLessThanOrEqual(H / 2)
    expect(r.y + r.h).toBeGreaterThanOrEqual(H / 2)
    expect(r.h).toBe(1)
  })

  it('a corner ┌ reaches the edges it names: bottom and right', () => {
    const rects = rectsOf('┌')
    expect(rects.length).toBeGreaterThanOrEqual(2)
    const reachesBottom = rects.some((r) => r.y + r.h >= H)
    const reachesRight = rects.some((r) => r.x + r.w >= W)
    expect(reachesBottom).toBe(true)
    expect(reachesRight).toBe(true)
    // Both bands sit on the cell's centerlines, so the neighbour's half meets.
    for (const r of rects) {
      const vertical = r.h > r.w
      if (vertical) {
        expect(r.x).toBeLessThanOrEqual(W / 2)
        expect(r.x + r.w).toBeGreaterThanOrEqual(W / 2)
      } else {
        expect(r.y).toBeLessThanOrEqual(H / 2)
        expect(r.y + r.h).toBeGreaterThanOrEqual(H / 2)
      }
    }
  })

  it('a corner ┘ reaches the edges it names: top and left', () => {
    const rects = rectsOf('┘')
    expect(rects.some((r) => r.y <= 0)).toBe(true)
    expect(rects.some((r) => r.x <= 0)).toBe(true)
  })

  it('a tee ├ carries three arms: top, bottom and right', () => {
    const rects = rectsOf('├')
    expect(rects.length).toBeGreaterThanOrEqual(3)
    expect(rects.some((r) => r.y <= 0)).toBe(true)
    expect(rects.some((r) => r.y + r.h >= H)).toBe(true)
    expect(rects.some((r) => r.x + r.w >= W)).toBe(true)
  })
})

describe('weights: heavy is double light', () => {
  it('━ is twice ─', () => {
    expect(rectsOf('━')[0].h).toBe(rectsOf('─')[0].h * 2)
    expect(rectsOf('┃')[0].w).toBe(rectsOf('│')[0].w * 2)
  })

  it('a mixed junction ┽ keeps each arm at its own weight', () => {
    // ┽: left heavy, right light, up/down light. The heavy left arm is twice
    // the light right arm, both bands centered on the same midline.
    const rects = rectsOf('┽')
    const horizontal = rects.filter((r) => r.w > r.h)
    expect(horizontal.length).toBe(2)
    expect(horizontal.some((r) => r.h === 2)).toBe(true)
    expect(horizontal.some((r) => r.h === 1)).toBe(true)
    // Same midline: the heavy band's centre equals the light band's centre.
    const heavy = horizontal.find((r) => r.h === 2)
    const light = horizontal.find((r) => r.h === 1)
    expect(heavy && light && Math.abs(heavy.y + 1 - (light.y + 0.5))).toBeLessThanOrEqual(1)
  })
})

describe('doubles ═ ║', () => {
  it('═ stacks two bands around the midline with a gap between them', () => {
    const rects = rectsOf('═')
    expect(rects).toHaveLength(2)
    const [a, b] = rects.slice().sort((p, q) => p.y - q.y)
    expect(a.x).toBe(b.x)
    expect(a.w).toBe(b.w)
    expect(a.x).toBeLessThanOrEqual(0)
    expect(a.x + a.w).toBeGreaterThanOrEqual(W)
    expect(a.y + a.h).toBeLessThanOrEqual(b.y) // no overlap
    const mid = (a.y + a.h + b.y) / 2
    expect(Math.abs(mid - H / 2)).toBeLessThanOrEqual(1)
  })

  it('║ places two bands either side of the vertical midline', () => {
    const rects = rectsOf('║')
    expect(rects).toHaveLength(2)
    const [a, b] = rects.slice().sort((p, q) => p.x - q.x)
    expect(a.x + a.w).toBeLessThanOrEqual(b.x)
    expect(a.y).toBeLessThanOrEqual(0)
    expect(a.y + a.h).toBeGreaterThanOrEqual(H)
  })
})

describe('dashes tile per cell', () => {
  it('┄ draws three dashes in one band, inside the cell', () => {
    const rects = rectsOf('┄')
    expect(rects).toHaveLength(3)
    const ys = new Set(rects.map((r) => r.y))
    expect(ys.size).toBe(1)
    const sorted = rects.slice().sort((p, q) => p.x - q.x)
    expect(sorted[0].x).toBeGreaterThanOrEqual(0)
    expect(sorted[2].x + sorted[2].w).toBeLessThanOrEqual(W)
    for (let i = 1; i < sorted.length; i++) {
      expect(sorted[i].x).toBeGreaterThanOrEqual(sorted[i - 1].x + sorted[i - 1].w)
    }
  })

  it('┆ starts at the top edge so vertical runs keep their period', () => {
    const rects = rectsOf('┆')
    expect(rects).toHaveLength(3)
    const sorted = rects.slice().sort((p, q) => p.y - q.y)
    expect(sorted[0].y).toBe(0)
    // The extra gap sits at the bottom: the last dash stops short of the
    // edge, so tiling shows one full dash per cell boundary, not a seam.
    expect(sorted[2].y + sorted[2].h).toBeLessThan(H)
  })
})

describe('diagonals and arcs are strokes', () => {
  it('╱ ╲ ╳ carry a stroke of the light weight', () => {
    for (const ch of ['╱', '╲', '╳']) {
      const strokes = cellInkShapes(ch, W, H, 1).filter((s) => s.kind === 'stroke')
      expect(strokes.length).toBeGreaterThanOrEqual(1)
      for (const s of strokes) {
        expect(s.kind === 'stroke' && s.width).toBe(1)
        expect(s.kind === 'stroke' && s.d.startsWith('M')).toBe(true)
      }
    }
    expect(cellInkShapes('╳', W, H, 1).filter((s) => s.kind === 'stroke')).toHaveLength(2)
  })

  it('╭ is a stroked arc of the light weight', () => {
    const strokes = cellInkShapes('╭', W, H, 1).filter((s) => s.kind === 'stroke')
    expect(strokes).toHaveLength(1)
  })
})

describe('block elements fill the cell exactly', () => {
  it('█ covers the whole cell with no overshoot', () => {
    const rects = rectsOf('█')
    expect(rects).toHaveLength(1)
    expect(rects[0]).toEqual({ kind: 'rect', x: 0, y: 0, w: W, h: H })
  })

  it('▀ and ▄ tile at the midline with no gap and no double-draw', () => {
    const upper = rectsOf('▀')[0]
    const lower = rectsOf('▄')[0]
    expect(upper.y).toBe(0)
    expect(upper.y + upper.h).toBe(lower.y)
    expect(lower.y + lower.h).toBe(H)
  })

  it('▌ and ▐ tile at the midline across the width', () => {
    const left = rectsOf('▌')[0]
    const right = rectsOf('▐')[0]
    expect(left.x).toBe(0)
    expect(left.x + left.w).toBe(right.x)
    expect(right.x + right.w).toBe(W)
  })

  it('▁ is the lower one eighth of the HEIGHT, ▏ the left one eighth of the WIDTH', () => {
    const lower = rectsOf('▁')[0]
    expect(lower.y + lower.h).toBe(H)
    expect(lower.h).toBe(3) // round(20 × ⅛), rounding half up
    const leftEighth = rectsOf('▏')[0]
    expect(leftEighth.x).toBe(0)
    expect(leftEighth.w).toBe(1) // round(8 × ⅛)
  })

  it('▞ paints its two quadrants on the true quarters', () => {
    const rects = rectsOf('▞').sort((a, b) => a.y - b.y || a.x - b.x)
    expect(rects).toHaveLength(2)
    const [tr, bl] = rects
    expect(tr).toEqual({ kind: 'rect', x: 4, y: 0, w: 4, h: 10 })
    expect(bl).toEqual({ kind: 'rect', x: 0, y: 10, w: 4, h: 10 })
  })

  it('shades fill the cell at their fraction of the ink', () => {
    expect(rectsOf('░')[0]).toEqual({ kind: 'rect', x: 0, y: 0, w: W, h: H, opacity: 0.25 })
    expect(rectsOf('▒')[0]).toEqual({ kind: 'rect', x: 0, y: 0, w: W, h: H, opacity: 0.5 })
    expect(rectsOf('▓')[0]).toEqual({ kind: 'rect', x: 0, y: 0, w: W, h: H, opacity: 0.75 })
  })
})

describe('the cell aspect is the glyph aspect', () => {
  it('a wide cell centers its vertical line on its own middle', () => {
    const r = rectsOf('│', 16, 20)[0]
    expect(r.x).toBeLessThanOrEqual(8)
    expect(r.x + r.w).toBeGreaterThanOrEqual(8)
    expect(r.y).toBeLessThanOrEqual(0)
    expect(r.y + r.h).toBeGreaterThanOrEqual(20)
  })

  it('block fractions scale on the axis they name', () => {
    // 16 wide × 20 tall: ▁ is still a HEIGHT fraction, ▏ a WIDTH fraction.
    expect(rectsOf('▁', 16, 20)[0].h).toBe(3)
    expect(rectsOf('▏', 16, 20)[0].w).toBe(2)
    expect(rectsOf('█', 16, 20)[0]).toEqual({ kind: 'rect', x: 0, y: 0, w: 16, h: 20 })
  })
})

describe('every codepoint in the two blocks yields paintable geometry', () => {
  it('sweeps U+2500..259F with finite, in-bounds shapes', () => {
    let drawn = 0
    for (let cp = 0x2500; cp <= 0x259f; cp++) {
      const chars = String.fromCodePoint(cp)
      if (!isProceduralInk(chars)) continue
      const shapes = cellInkShapes(chars, W, H, 1)
      expect(shapes.length).toBeGreaterThanOrEqual(1)
      drawn++
      for (const s of shapes) {
        if (s.kind === 'rect') {
          for (const v of [s.x, s.y, s.w, s.h]) expect(Number.isFinite(v)).toBe(true)
          expect(s.x).toBeGreaterThanOrEqual(-1)
          expect(s.y).toBeGreaterThanOrEqual(-1)
          expect(s.x + s.w).toBeLessThanOrEqual(W + 1)
          expect(s.y + s.h).toBeLessThanOrEqual(H + 1)
          expect(s.w).toBeGreaterThan(0)
          expect(s.h).toBeGreaterThan(0)
        } else {
          expect(s.width).toBeGreaterThan(0)
          expect(s.d.length).toBeGreaterThan(0)
        }
      }
    }
    expect(drawn).toBe(160) // the whole two-block range is owned here
  })
})

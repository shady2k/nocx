// THE PROCEDURAL CELL INK (nocx-1ebrs.15): box drawing U+2500..257F and
// block elements U+2580..259F drawn TO CELL BOUNDS from the cell's geometry,
// never composited from the font's glyph.
//
// WHY. The font's glyph is designed for a typographic cell and cannot tile:
// a vertical U+2502 run painted from it is dashed, because each cell's glyph
// is shorter than the cell's line height, and a block element shows the
// font's proportions instead of the cell's aspect. Every terminal that gets
// this right (Ghostty, kitty, WezTerm) draws these codepoints procedurally.
// This module is that drawing: per-codepoint segment geometry over one cell,
// the segments computed from the cell's own width and pitch.
//
// OWNERSHIP. Measurement stays with cell-fit.ts — a procedural cell is never
// measured (its paint consumes no font advance), and the geometry here reads
// the same published metric every run already lays out at: RunMetric.cellWidth
// across, RunMetric.pitch down. Painting is paint-row.ts's: this module
// answers shapes, the painter builds DOM. The segment conventions for the
// traditional intersection characters (where each arm runs from its cell edge
// to the center, the doubled strokes sit a light width either side of the
// midline, dashes tile per cell with half gaps at the horizontal edges, the
// arcs are quarter curves of the light weight) follow the shared conventions
// of the terminals above; the numbers are derived here from the cell itself.
//
// SEAMS. Two adjacent cells join with no gap and no visible double-draw:
//  - within a run, every cell's shapes share one SVG coordinate space, so a
//    horizontal run is one exact rectangle chain, not per-element boxes that
//    each snap to device pixels on their own;
//  - a line that continues across a row edge overshoots that edge by half a
//    CSS pixel, so the two rows' paint overlaps. Overlap of the same opaque
//    ink is invisible; a gap never is. Diagonals overshoot their corners the
//    same half pixel, keeping their slope, so a diagonal run joins across
//    cell corners.
//
// DEGRADE. A codepoint outside the two blocks, a multi-codepoint cluster,
// a missing metric or an unpublished pitch all fall back to the font glyph —
// the behaviour every terminal without this module ships.

export interface InkRect {
  kind: 'rect'
  x: number
  y: number
  w: number
  h: number
  /** The fraction of the ink a shade carries: ░ 0.25, ▒ 0.5, ▓ 0.75. */
  opacity?: number
}

/** A stroked path (diagonals and arcs), in SVG path data, cell-local. */
interface InkStroke {
  kind: 'stroke'
  d: string
  width: number
}

export type InkShape = InkRect | InkStroke

const FIRST = 0x2500
const LAST = 0x259f

/** A half pixel: the overshoot that makes two rows' paint overlap at the
 *  shared edge instead of abutting it. */
const OVERSHOOT = 0.5

/** True when every codepoint is one the painter draws procedurally. The
 *  run walk uses it as its own-run predicate (single codepoints); the
 *  painter re-asks it of a whole run before painting one. */
export function isProceduralInk(chars: string): boolean {
  if (chars.length === 0) return false
  for (const ch of chars) {
    const cp = ch.codePointAt(0) ?? 0
    if (cp < FIRST || cp > LAST) return false
  }
  return true
}

/** The light line weight, from the cell itself: an eighth of the smaller
 *  side, never under one pixel, so zoom scales it the way a font's own
 *  hairline would. Heavy is double light; the doubled styles put one light
 *  width either side of the midline. */
export function thicknessBaseOf(cellWidth: number, cellHeight: number): number {
  return Math.max(1, Math.round(Math.min(cellWidth, cellHeight) / 8))
}

/** Fraction positions with complementary rounding (the convention that keeps
 *  adjacent fractions tiling exactly: 20 × half splits into 10 + 10, 21 into
 *  11 + 10, never a gap and never a double-draw). */
const minF = (f: number, size: number): number => size - Math.round((1 - f) * size)
const maxF = (f: number, size: number): number => Math.round(f * size)

interface Lines {
  up: Style
  right: Style
  down: Style
  left: Style
}
type Style = 'none' | 'light' | 'heavy' | 'double'

const N: Style = 'none'
const L: Style = 'light'
const Hv: Style = 'heavy'
const D: Style = 'double'

/** One cell's shapes, cell-local: x in [0, cellWidth], y in [0, cellHeight],
 *  with the documented half-pixel overshoots past the edges a glyph's lines
 *  continue across. `dx` shifts every shape — the SVG builder draws each
 *  cell of a run at its own offset in one coordinate space. */
export function cellInkShapes(
  chars: string,
  cellWidth: number,
  cellHeight: number,
  base: number,
  dx = 0,
): InkShape[] {
  const cp = chars.codePointAt(0) ?? 0
  if (cp < FIRST || cp > LAST) return []
  const out =
    cp <= 0x257f
      ? boxShapes(cp, cellWidth, cellHeight, base)
      : blockShapes(cp, cellWidth, cellHeight)
  if (dx !== 0) {
    for (const s of out) {
      if (s.kind === 'rect') s.x += dx
      else s.d = shiftPath(s.d, dx)
    }
  }
  return out
}

function rect(x: number, y: number, w: number, h: number, opacity?: number): InkRect {
  return opacity === undefined
    ? { kind: 'rect', x, y, w, h }
    : { kind: 'rect', x, y, w, h, opacity }
}

/** A rect that overshoots the edges its line continues across. */
function band(
  x: number,
  y: number,
  w: number,
  h: number,
  over: { left?: boolean; right?: boolean; top?: boolean; bottom?: boolean },
): InkRect {
  return {
    kind: 'rect',
    x: x - (over.left ? OVERSHOOT : 0),
    y: y - (over.top ? OVERSHOOT : 0),
    w: w + (over.left ? OVERSHOOT : 0) + (over.right ? OVERSHOOT : 0),
    h: h + (over.top ? OVERSHOOT : 0) + (over.bottom ? OVERSHOOT : 0),
  }
}

/** +dx on every x coordinate of path data this module generates. The arcs
 *  and diagonals are the only strokes, and both emit absolute M/L/C commands
 *  whose coordinates strictly alternate x, y — so every second number is an x. */
function shiftPath(d: string, dx: number): string {
  let index = 0
  return d.replace(/-?\d+(?:\.\d+)?/g, (n) => (index++ % 2 === 0 ? String(Number(n) + dx) : n))
}

// ── Box drawing, U+2500..257F ────────────────────────────────────────────────

function boxShapes(cp: number, w: number, h: number, t: number): InkShape[] {
  // The traditional intersection characters: each arm runs from its cell
  // edge to the centre, so a corner of four cells, a tee of two or a cross
  // of four meet on the cell's centre lines.
  const spec = BOX_LINES[cp - FIRST]
  if (spec) return linesChar(spec, w, h, t)

  switch (cp) {
    case 0x2500: // ─ light horizontal
      return [band(0, mid(h, t), w, t, { left: true, right: true })]
    case 0x2501: // ━ heavy horizontal
      return [band(0, mid(h, 2 * t), w, 2 * t, { left: true, right: true })]
    case 0x2502: // │ light vertical
      return [band(mid(w, t), 0, t, h, { top: true, bottom: true })]
    case 0x2503: // ┃ heavy vertical
      return [band(mid(w, 2 * t), 0, 2 * t, h, { top: true, bottom: true })]
    case 0x2504: // ┄ triple dash horizontal
      return dashHorizontal(w, h, 3, t, Math.max(4, t))
    case 0x2505: // ┅ triple dash horizontal, heavy
      return dashHorizontal(w, h, 3, 2 * t, Math.max(4, t))
    case 0x2506: // ┆ triple dash vertical
      return dashVertical(w, h, 3, t, Math.max(4, t))
    case 0x2507: // ┇ triple dash vertical, heavy
      return dashVertical(w, h, 3, 2 * t, Math.max(4, t))
    case 0x2508: // ┈ quadruple dash horizontal
      return dashHorizontal(w, h, 4, t, Math.max(4, t))
    case 0x2509: // ┉ quadruple dash horizontal, heavy
      return dashHorizontal(w, h, 4, 2 * t, Math.max(4, t))
    case 0x250a: // ┊ quadruple dash vertical
      return dashVertical(w, h, 4, t, Math.max(4, t))
    case 0x250b: // ┋ quadruple dash vertical, heavy
      return dashVertical(w, h, 4, 2 * t, Math.max(4, t))
    case 0x254c: // ╌ double dash horizontal
      return dashHorizontal(w, h, 2, t, t)
    case 0x254d: // ╍ double dash horizontal, heavy
      return dashHorizontal(w, h, 2, 2 * t, 2 * t)
    case 0x254e: // ╎ double dash vertical
      return dashVertical(w, h, 2, t, 2 * t)
    case 0x254f: // ╏ double dash vertical, heavy
      return dashVertical(w, h, 2, 2 * t, 2 * t)
    case 0x2550: // ═ double horizontal
    case 0x2551: {
      // ═ two full-span bands a light width either side of the midline;
      //   ║ the same across. Both overshoot the edges they continue past.
      const vertical = cp === 0x2551
      const lightTop = mid(h, t)
      const lightLeft = mid(w, t)
      return vertical
        ? [
            band(lightLeft - t, 0, t, h, { top: true, bottom: true }),
            band(lightLeft + t, 0, t, h, { top: true, bottom: true }),
          ]
        : [
            band(0, lightTop - t, w, t, { left: true, right: true }),
            band(0, lightTop + t, w, t, { left: true, right: true }),
          ]
    }
    case 0x256d: // ╭ arc down and right
      return [arc(w, h, t, 'br')]
    case 0x256e: // ╮ arc down and left
      return [arc(w, h, t, 'bl')]
    case 0x256f: // ╯ arc up and left
      return [arc(w, h, t, 'tl')]
    case 0x2570: // ╰ arc up and right
      return [arc(w, h, t, 'tr')]
    case 0x2571: // ╱ diagonal upper right to lower left
      return [diagonal(w, h, t, 'ur-ll')]
    case 0x2572: // ╲ diagonal upper left to lower right
      return [diagonal(w, h, t, 'ul-lr')]
    case 0x2573: // ╳ diagonal cross
      return [diagonal(w, h, t, 'ur-ll'), diagonal(w, h, t, 'ul-lr')]
    default:
      return []
  }
}

/** Where a centred band of thickness `t` starts across a side of `size`. */
function mid(size: number, t: number): number {
  return Math.max(0, Math.round((size - t) / 2))
}

/** The traditional intersection character, per Ghostty's reading of the
 *  block: each arm runs edge to centre, and an arm meeting a heavier or
 *  doubled arm extends to THAT arm's band so the junction has no step. */
function linesChar(lines: Lines, w: number, h: number, t: number): InkShape[] {
  const heavy = 2 * t
  const hLightTop = mid(h, t)
  const hLightBottom = hLightTop + t
  const hHeavyTop = mid(h, heavy)
  const hHeavyBottom = hHeavyTop + heavy
  const hDoubleTop = hLightTop - t
  const hDoubleBottom = hLightBottom + t
  const vLightLeft = mid(w, t)
  const vLightRight = vLightLeft + t
  const vHeavyLeft = mid(w, heavy)
  const vHeavyRight = vHeavyLeft + heavy
  const vDoubleLeft = vLightLeft - t
  const vDoubleRight = vLightRight + t

  // The bottom of the up arm / the top of the down arm: an arm meets the
  // heaviest horizontal arm present, the doubled band when the horizontal
  // arms are doubled, else the light band.
  const horizontals = [lines.left, lines.right]
  const heaviestHorizontal = horizontals.includes(Hv)
    ? 'heavy'
    : horizontals.includes(D)
      ? 'double'
      : horizontals.includes(L)
        ? 'light'
        : 'none'
  const upBottom =
    heaviestHorizontal === 'heavy'
      ? hHeavyBottom
      : heaviestHorizontal === 'double'
        ? hDoubleBottom
        : hLightBottom
  const downTop =
    heaviestHorizontal === 'heavy'
      ? hHeavyTop
      : heaviestHorizontal === 'double'
        ? hDoubleTop
        : hLightTop
  const verticals = [lines.up, lines.down]
  const heaviestVertical = verticals.includes(Hv)
    ? 'heavy'
    : verticals.includes(D)
      ? 'double'
      : verticals.includes(L)
        ? 'light'
        : 'none'
  const leftRight =
    heaviestVertical === 'heavy'
      ? vHeavyRight
      : heaviestVertical === 'double'
        ? vDoubleRight
        : vLightRight
  const rightLeft =
    heaviestVertical === 'heavy'
      ? vHeavyLeft
      : heaviestVertical === 'double'
        ? vDoubleLeft
        : vLightLeft

  const out: InkShape[] = []
  switch (lines.up) {
    case 'none':
      break
    case 'light':
      out.push(band(vLightLeft, 0, t, upBottom, { top: true }))
      break
    case 'heavy':
      out.push(band(vHeavyLeft, 0, heavy, upBottom, { top: true }))
      break
    case 'double':
      out.push(band(vDoubleLeft, 0, t, upBottom, { top: true }))
      out.push(band(vLightRight, 0, t, upBottom, { top: true }))
      break
  }
  switch (lines.right) {
    case 'none':
      break
    case 'light':
      out.push(band(rightLeft, hLightTop, w - rightLeft, t, { right: true }))
      break
    case 'heavy':
      out.push(band(rightLeft, hHeavyTop, w - rightLeft, heavy, { right: true }))
      break
    case 'double':
      out.push(band(rightLeft, hDoubleTop, w - rightLeft, t, { right: true }))
      out.push(band(rightLeft, hLightBottom, w - rightLeft, t, { right: true }))
      break
  }
  switch (lines.down) {
    case 'none':
      break
    case 'light':
      out.push(band(vLightLeft, downTop, t, h - downTop, { bottom: true }))
      break
    case 'heavy':
      out.push(band(vHeavyLeft, downTop, heavy, h - downTop, { bottom: true }))
      break
    case 'double':
      out.push(band(vDoubleLeft, downTop, t, h - downTop, { bottom: true }))
      out.push(band(vLightRight, downTop, t, h - downTop, { bottom: true }))
      break
  }
  switch (lines.left) {
    case 'none':
      break
    case 'light':
      out.push(band(0, hLightTop, leftRight, t, { left: true }))
      break
    case 'heavy':
      out.push(band(0, hHeavyTop, leftRight, heavy, { left: true }))
      break
    case 'double':
      out.push(band(0, hDoubleTop, leftRight, t, { left: true }))
      out.push(band(0, hLightBottom, leftRight, t, { left: true }))
      break
  }
  return out
}

/** The arm layout of every traditional intersection character, read off the
 *  block's own definition: which edges carry a line, at what weight. The
 *  pure lines, dashes, arcs and diagonals have their own cases above. */
const BOX_LINES: (Lines | undefined)[] = (() => {
  const s = (up: Style, right: Style, down: Style, left: Style): Lines => ({
    up,
    right,
    down,
    left,
  })
  const table: Record<number, Lines> = {
    0x250c: s(N, L, L, N), // ┌
    0x250d: s(N, Hv, L, N), // ┍
    0x250e: s(N, L, Hv, N), // ┎
    0x250f: s(N, Hv, Hv, N), // ┏
    0x2510: s(N, N, L, L), // ┐
    0x2511: s(N, N, L, Hv), // ┑
    0x2512: s(N, N, Hv, L), // ┒
    0x2513: s(N, N, Hv, Hv), // ┓
    0x2514: s(L, L, N, N), // └
    0x2515: s(L, Hv, N, N), // ┕
    0x2516: s(Hv, L, N, N), // ┖
    0x2517: s(Hv, Hv, N, N), // ┗
    0x2518: s(L, N, N, L), // ┘
    0x2519: s(L, N, N, Hv), // ┙
    0x251a: s(Hv, N, N, L), // ┚
    0x251b: s(Hv, N, N, Hv), // ┛
    0x251c: s(L, L, L, N), // ├
    0x251d: s(L, Hv, L, N), // ┝
    0x251e: s(Hv, L, L, N), // ┞
    0x251f: s(L, L, Hv, N), // ┟
    0x2520: s(Hv, L, Hv, N), // ┠
    0x2521: s(Hv, Hv, L, N), // ┡
    0x2522: s(L, Hv, Hv, N), // ┢
    0x2523: s(Hv, Hv, Hv, N), // ┣
    0x2524: s(L, N, L, L), // ┤
    0x2525: s(L, N, L, Hv), // ┥
    0x2526: s(Hv, N, L, L), // ┦
    0x2527: s(L, N, Hv, L), // ┧
    0x2528: s(Hv, N, Hv, L), // ┨
    0x2529: s(Hv, N, L, Hv), // ┩
    0x252a: s(L, N, Hv, Hv), // ┪
    0x252b: s(Hv, N, Hv, Hv), // ┫
    0x252c: s(N, L, L, L), // ┬
    0x252d: s(N, L, L, Hv), // ┭
    0x252e: s(N, Hv, L, L), // ┮
    0x252f: s(N, Hv, L, Hv), // ┯
    0x2530: s(N, L, Hv, L), // ┰
    0x2531: s(N, L, Hv, Hv), // ┱
    0x2532: s(N, Hv, Hv, L), // ┲
    0x2533: s(N, Hv, Hv, Hv), // ┳
    0x2534: s(L, L, N, L), // ┴
    0x2535: s(L, L, N, Hv), // ┵
    0x2536: s(L, Hv, N, L), // ┶
    0x2537: s(L, Hv, N, Hv), // ┷
    0x2538: s(Hv, L, N, L), // ┸
    0x2539: s(Hv, L, N, Hv), // ┹
    0x253a: s(Hv, Hv, N, L), // ┺
    0x253b: s(Hv, Hv, N, Hv), // ┻
    0x253c: s(L, L, L, L), // ┼
    0x253d: s(L, L, L, Hv), // ┽
    0x253e: s(L, Hv, L, L), // ┾
    0x253f: s(L, Hv, L, Hv), // ┿
    0x2540: s(Hv, L, L, L), // ╀
    0x2541: s(L, L, Hv, L), // ╁
    0x2542: s(Hv, L, Hv, L), // ╂
    0x2543: s(Hv, L, L, Hv), // ╃
    0x2544: s(Hv, Hv, L, L), // ╄
    0x2545: s(L, L, Hv, Hv), // ╅
    0x2546: s(L, Hv, Hv, L), // ╆
    0x2547: s(Hv, Hv, L, Hv), // ╇
    0x2548: s(L, Hv, Hv, Hv), // ╈
    0x2549: s(Hv, L, Hv, Hv), // ╉
    0x254a: s(Hv, Hv, Hv, L), // ╊
    0x254b: s(Hv, Hv, Hv, Hv), // ╋
    0x2552: s(N, D, L, N), // ╒
    0x2553: s(N, L, D, N), // ╓
    0x2554: s(N, D, D, N), // ╔
    0x2555: s(N, N, L, D), // ╕
    0x2556: s(N, N, D, L), // ╖
    0x2557: s(N, N, D, D), // ╗
    0x2558: s(L, D, N, N), // ╘
    0x2559: s(D, L, N, N), // ╙
    0x255a: s(D, D, N, N), // ╚
    0x255b: s(L, N, N, D), // ╛
    0x255c: s(D, N, N, L), // ╜
    0x255d: s(D, N, N, D), // ╝
    0x255e: s(L, D, L, N), // ╞
    0x255f: s(D, L, D, N), // ╟
    0x2560: s(D, D, D, N), // ╠
    0x2561: s(L, N, L, D), // ╡
    0x2562: s(D, N, D, L), // ╢
    0x2563: s(D, N, D, D), // ╣
    0x2564: s(N, D, L, D), // ╤
    0x2565: s(N, L, D, L), // ╥
    0x2566: s(N, D, D, D), // ╦
    0x2567: s(L, D, N, D), // ╧
    0x2568: s(D, L, N, L), // ╨
    0x2569: s(D, D, N, D), // ╩
    0x256a: s(L, D, L, D), // ╪
    0x256b: s(D, L, D, L), // ╫
    0x256c: s(D, D, D, D), // ╬
    0x2574: s(N, N, N, L), // ╴
    0x2575: s(L, N, N, N), // ╵
    0x2576: s(N, L, N, N), // ╶
    0x2577: s(N, N, L, N), // ╷
    0x2578: s(N, N, N, Hv), // ╸
    0x2579: s(Hv, N, N, N), // ╹
    0x257a: s(N, Hv, N, N), // ╺
    0x257b: s(N, N, Hv, N), // ╻
    0x257c: s(N, Hv, N, L), // ╼
    0x257d: s(L, N, Hv, N), // ╽
    0x257e: s(N, L, N, Hv), // ╾
    0x257f: s(Hv, N, L, N), // ╿
  }
  const out: (Lines | undefined)[] = Array.from({ length: 0x80 }, () => undefined)
  for (const [k, v] of Object.entries(table)) out[Number(k) - FIRST] = v
  return out
})()

/** Dashes tile PER CELL: half gaps at the horizontal edges make a horizontal
 *  run one even line, and the vertical dash spends its extra gap at the
 *  bottom, so a vertical run shows one full dash per boundary. When the cell
 *  is too small to carry the pattern, the dash degrades to a solid line. */
function dashHorizontal(
  w: number,
  h: number,
  count: number,
  thick: number,
  desiredGap: number,
): InkShape[] {
  const gapCount = count
  if (w < count + gapCount) return [band(0, mid(h, thick), w, thick, { left: true, right: true })]
  const gap = Math.min(desiredGap, Math.floor(w / (2 * count)))
  const dash = Math.floor((w - gapCount * gap) / count)
  let extra = (w - gapCount * gap) % count
  const y = mid(h, thick)
  const out: InkShape[] = []
  let x = Math.floor(gap / 2)
  for (let i = 0; i < count; i++) {
    let x1 = x + dash
    if (extra > 0) {
      extra -= 1
      x1 += 1
    }
    out.push(rect(x, y, x1 - x, thick))
    x = x1 + gap
  }
  return out
}

function dashVertical(
  w: number,
  h: number,
  count: number,
  thick: number,
  desiredGap: number,
): InkShape[] {
  const gapCount = count
  if (h < count + gapCount) return [band(mid(w, thick), 0, thick, h, { top: true, bottom: true })]
  const gap = Math.min(desiredGap, Math.floor(h / (2 * count)))
  const dash = Math.floor((h - gapCount * gap) / count)
  let extra = (h - gapCount * gap) % count
  const x = mid(w, thick)
  const out: InkShape[] = []
  let y = 0
  for (let i = 0; i < count; i++) {
    let y1 = y + dash
    if (extra > 0) {
      extra -= 1
      y1 += 1
    }
    out.push(rect(x, y, thick, y1 - y))
    y = y1 + gap
  }
  return out
}

/** A quarter arc of the light weight: a straight leg from the edge it names
 *  to the curve, a cubic approximation of the quarter circle, a leg out to
 *  the other edge. Radius is half the smaller side, so the arc meets both
 *  edges it joins. */
function arc(w: number, h: number, t: number, corner: 'tl' | 'tr' | 'bl' | 'br'): InkShape {
  const centerX = mid(w, t) + t / 2
  const centerY = mid(h, t) + t / 2
  const r = Math.min(w, h) / 2
  const s = 0.25
  const f = (n: number): number => Number(n.toFixed(4))
  let d: string
  switch (corner) {
    case 'tl': // ╯ reaches up edge and left edge
      d = `M ${f(centerX)} 0 L ${f(centerX)} ${f(centerY - r)} C ${f(centerX)} ${f(centerY - s * r)} ${f(centerX - s * r)} ${f(centerY)} ${f(centerX - r)} ${f(centerY)} L 0 ${f(centerY)}`
      break
    case 'tr': // ╰ reaches up edge and right edge
      d = `M ${f(centerX)} 0 L ${f(centerX)} ${f(centerY - r)} C ${f(centerX)} ${f(centerY - s * r)} ${f(centerX + s * r)} ${f(centerY)} ${f(centerX + r)} ${f(centerY)} L ${f(w)} ${f(centerY)}`
      break
    case 'bl': // ╮ reaches down edge and left edge
      d = `M ${f(centerX)} ${f(h)} L ${f(centerX)} ${f(centerY + r)} C ${f(centerX)} ${f(centerY + s * r)} ${f(centerX - s * r)} ${f(centerY)} ${f(centerX - r)} ${f(centerY)} L 0 ${f(centerY)}`
      break
    case 'br': // ╭ reaches down edge and right edge
      d = `M ${f(centerX)} ${f(h)} L ${f(centerX)} ${f(centerY + r)} C ${f(centerX)} ${f(centerY + s * r)} ${f(centerX + s * r)} ${f(centerY)} ${f(centerX + r)} ${f(centerY)} L ${f(w)} ${f(centerY)}`
      break
  }
  return { kind: 'stroke', d, width: t }
}

/** A diagonal of the light weight, overshooting both corners by half a
 *  pixel ALONG ITS OWN SLOPE, so a run of diagonals joins across cell
 *  corners and the slope never changes. */
function diagonal(w: number, h: number, t: number, which: 'ur-ll' | 'ul-lr'): InkShape {
  const sx = Math.min(1, w / h)
  const sy = Math.min(1, h / w)
  const f = (n: number): number => Number(n.toFixed(4))
  const d =
    which === 'ur-ll'
      ? `M ${f(w + OVERSHOOT * sx)} ${f(-OVERSHOOT * sy)} L ${f(-OVERSHOOT * sx)} ${f(h + OVERSHOOT * sy)}`
      : `M ${f(-OVERSHOOT * sx)} ${f(-OVERSHOOT * sy)} L ${f(w + OVERSHOOT * sx)} ${f(h + OVERSHOOT * sy)}`
  return { kind: 'stroke', d, width: t }
}

// ── Block elements, U+2580..259F ─────────────────────────────────────────────

function blockShapes(cp: number, w: number, h: number): InkShape[] {
  // Fractions name eighths of the axis they are drawn on: upper/lower name
  // the height, left/right the width. Complementary rounding keeps halves
  // tiling with no gap and no double-draw.
  const lower = (f: number): InkRect => rect(0, minF(1 - f, h), w, h - minF(1 - f, h))
  const upper = (f: number): InkRect => rect(0, 0, w, minF(f, h))
  const left = (f: number): InkRect => rect(0, 0, maxF(f, w), h)
  const right = (f: number): InkRect => rect(minF(f, w), 0, w - minF(f, w), h)
  const quarter = (q: { tl?: boolean; tr?: boolean; bl?: boolean; br?: boolean }): InkShape[] => {
    const out: InkShape[] = []
    if (q.tl) out.push(rect(0, 0, maxF(0.5, w), minF(0.5, h)))
    if (q.tr) out.push(rect(maxF(0.5, w), 0, w - maxF(0.5, w), minF(0.5, h)))
    if (q.bl) out.push(rect(0, minF(0.5, h), maxF(0.5, w), h - minF(0.5, h)))
    if (q.br) out.push(rect(maxF(0.5, w), minF(0.5, h), w - maxF(0.5, w), h - minF(0.5, h)))
    return out
  }
  const shade = (opacity: number): InkRect => rect(0, 0, w, h, opacity)
  switch (cp) {
    case 0x2580:
      return [upper(0.5)] // ▀
    case 0x2581:
      return [lower(0.125)] // ▁
    case 0x2582:
      return [lower(0.25)] // ▂
    case 0x2583:
      return [lower(0.375)] // ▃
    case 0x2584:
      return [lower(0.5)] // ▄
    case 0x2585:
      return [lower(0.625)] // ▅
    case 0x2586:
      return [lower(0.75)] // ▆
    case 0x2587:
      return [lower(0.875)] // ▇
    case 0x2588:
      return [rect(0, 0, w, h)] // █
    case 0x2589:
      return [left(0.875)] // ▉
    case 0x258a:
      return [left(0.75)] // ▊
    case 0x258b:
      return [left(0.625)] // ▋
    case 0x258c:
      return [left(0.5)] // ▌
    case 0x258d:
      return [left(0.375)] // ▍
    case 0x258e:
      return [left(0.25)] // ▎
    case 0x258f:
      return [left(0.125)] // ▏
    case 0x2590:
      return [right(0.5)] // ▐
    case 0x2591:
      return [shade(0.25)] // ░
    case 0x2592:
      return [shade(0.5)] // ▒
    case 0x2593:
      return [shade(0.75)] // ▓
    case 0x2594:
      return [upper(0.125)] // ▔
    case 0x2595:
      return [right(0.125)] // ▕
    case 0x2596:
      return quarter({ bl: true }) // ▖
    case 0x2597:
      return quarter({ br: true }) // ▗
    case 0x2598:
      return quarter({ tl: true }) // ▘
    case 0x2599:
      return quarter({ tl: true, bl: true, br: true }) // ▙
    case 0x259a:
      return quarter({ tl: true, br: true }) // ▚
    case 0x259b:
      return quarter({ tl: true, tr: true, bl: true }) // ▛
    case 0x259c:
      return quarter({ tl: true, tr: true, br: true }) // ▜
    case 0x259d:
      return quarter({ tr: true }) // ▝
    case 0x259e:
      return quarter({ tr: true, bl: true }) // ▞
    case 0x259f:
      return quarter({ tr: true, bl: true, br: true }) // ▟
    default:
      return []
  }
}

// ── The painted shape ────────────────────────────────────────────────────────

const SVG_NS = 'http://www.w3.org/2000/svg'

/** One run's procedural ink as an SVG over the run's cell rectangle: the
 *  viewBox in cell pixels, each codepoint drawn on its own cell at its own
 *  offset — one coordinate space for the whole run, so a line across cells
 *  never meets a per-element device-pixel snap. Shapes paint with
 *  currentColor, so the run's resolved ink — bold brightening, inverse, the
 *  readability floor — stays style.ts's, and the character itself is nowhere
 *  in the SVG (the painted cell carries it as a text node; only the font
 *  glyph is invisible — owner decision, 2026-10-21). */
export function proceduralSvgOf(
  chars: string,
  spans: readonly number[],
  cellWidth: number,
  cellHeight: number,
): SVGSVGElement {
  const svg = document.createElementNS(SVG_NS, 'svg')
  const totalWidth = spans.reduce((sum, span) => sum + span, 0) * cellWidth
  svg.setAttribute('viewBox', `0 0 ${fmt(totalWidth)} ${fmt(cellHeight)}`)
  svg.setAttribute('preserveAspectRatio', 'none')
  svg.setAttribute('aria-hidden', 'true')
  // The weight comes from the BASE cell, never the run: a wide cell's line
  // must weigh what its narrow neighbours' lines weigh.
  const base = thicknessBaseOf(cellWidth, cellHeight)
  let x = 0
  for (let i = 0; i < chars.length; i++) {
    const span = spans[i] ?? 1
    const width = span * cellWidth
    for (const shape of cellInkShapes(chars[i], width, cellHeight, base, x)) {
      svg.appendChild(shapeNode(shape))
    }
    x += width
  }
  return svg
}

function shapeNode(shape: InkShape): SVGElement {
  if (shape.kind === 'rect') {
    const el = document.createElementNS(SVG_NS, 'rect')
    el.setAttribute('x', fmt(shape.x))
    el.setAttribute('y', fmt(shape.y))
    el.setAttribute('width', fmt(shape.w))
    el.setAttribute('height', fmt(shape.h))
    el.setAttribute('fill', 'currentColor')
    if (shape.opacity !== undefined) el.setAttribute('fill-opacity', fmt(shape.opacity))
    return el
  }
  const el = document.createElementNS(SVG_NS, 'path')
  el.setAttribute('d', shape.d)
  el.setAttribute('fill', 'none')
  el.setAttribute('stroke', 'currentColor')
  el.setAttribute('stroke-width', fmt(shape.width))
  el.setAttribute('stroke-linecap', 'butt')
  return el
}

function fmt(n: number): string {
  return String(n)
}

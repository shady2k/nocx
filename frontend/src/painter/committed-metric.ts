// The committed cell metric's unit boundary (review round 1, nocx-zg3k3.2.9).
//
// The frame's cellWidthPx/cellHeightPx travel in DEVICE pixels: xterm builds
// its CSS cell FROM an integer device cell (css = device / dpr), so device
// pixels are the unit in which the metric is exact — a program asking its
// size gets physical pixels, as native terminals report on high-density
// screens. Painting and hit-testing live in CSS pixels, and xterm itself
// derives those by dividing the device cell by the display's ratio (its
// Viewport does exactly this division). This module is that division, named
// once so the mapping and the cursor cannot drift, and the ratio read once
// so tests stub one place.

/** The display's ratio, as the painter's conversions must see it. The one
 *  reader of window.devicePixelRatio in the painter; jsdom's absence of a
 *  ratio is the dpr-1 identity, which every historical fixture assumes. */
export function displayDpr(): number {
  return window.devicePixelRatio || 1
}

/** Committed DEVICE pixels → CSS pixels for one cell dimension. The exact
 *  inverse of the client's report arithmetic (cell = area ÷ count), so the
 *  painter's grid and xterm's own cells coincide at any ratio. */
export function devicePxToCssPx(px: number, dpr: number): number {
  return px / dpr
}

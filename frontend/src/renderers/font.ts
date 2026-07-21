// Shared font config for every renderer, matching Warp's defaults (14px / 1.2).
// `ui-monospace` resolves to SF Mono on macOS/WebKit — crisp and well-covered.
// The tail is a fallback chain so missing glyphs (box-drawing, symbols, emoji
// used by agent TUIs) fall back to a system font instead of rendering as tofu.
export const FONT_FAMILY =
  'ui-monospace, "SF Mono", Menlo, Monaco, "Apple Color Emoji", "Apple Symbols", monospace'
// xterm.js rasterises into a GPU atlas snapped to whole device pixels, so a
// round size is fine here. It would not be on a canvas-2d renderer: those fill
// each cell as its own rect in CSS pixels, and this font advances 0.60205em per
// cell, so 14px lands a cell on 16.86 device pixels at 2x — the antialiased
// half-pixel edges then show up as seams across solid blocks. See nocx-4kt for
// the sizes that land on whole pixels (14.12, 16.61) if a 2d renderer returns.
export const FONT_SIZE = 14
export const LINE_HEIGHT = 1.2

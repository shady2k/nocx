// Shared font config for every renderer, matching a comfortable terminal size
// (slightly smaller than Warp's 14px). `ui-monospace` resolves to SF Mono on
// macOS/WebKit — crisp and well-covered. The tail is a fallback chain so
// missing glyphs (box-drawing, symbols, emoji used by agent TUIs) fall back
// to a system font instead of rendering as tofu.
export const FONT_FAMILY =
  'ui-monospace, "SF Mono", Menlo, Monaco, "Apple Color Emoji", "Apple Symbols", monospace'
// xterm.js rasterises into a GPU atlas snapped to whole device pixels. At 13px
// with line-height 1.2, cells are 15.6 CSS px tall — not a whole pixel at 2x,
// but the GPU atlas path avoids the 2d-canvas antialiasing seams that motivated
// the earlier analysis. If a 2d renderer returns, see nocx-4kt for whole-pixel
// sizes (14.12, 16.61).
export const FONT_SIZE = 14
export const LINE_HEIGHT = 1.2

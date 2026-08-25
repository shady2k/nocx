// The ONE menu-clamp geometry (nocx-vnirv.2): both the kit's ContextMenu
// and the scrollback's imperative block menu position themselves through
// `clampMenuPosition` — a second copy of this math would be two owners of
// one rule, free to drift. These tests pin the geometry itself; the two
// call sites' tests (context-menu.test.tsx, blocks.test.ts) assert each
// menu lands exactly where THIS function says.
import { describe, expect, it } from 'vitest'
import { clampMenuPosition, EDGE_MARGIN_PX } from './menu-geometry'

const VIEWPORT = { width: 1024, height: 768 }

describe('clampMenuPosition', () => {
  it('leaves an in-viewport anchor alone', () => {
    expect(clampMenuPosition({ x: 100, y: 200 }, { width: 200, height: 120 }, VIEWPORT)).toEqual({
      left: 100,
      top: 200,
    })
  })

  it('pulls a menu back inside when the anchor is below the bottom edge', () => {
    // The block menu opens BELOW its ⋮ button (top = btnRect.bottom + 2),
    // so a running block at the bottom of the scrollback anchors the menu
    // off-screen — the exact defect this task fixes.
    const { top } = clampMenuPosition({ x: 40, y: 760 }, { width: 160, height: 120 }, VIEWPORT)
    expect(top).toBe(768 - 120 - EDGE_MARGIN_PX)
    expect(top + 120 + EDGE_MARGIN_PX).toBeLessThanOrEqual(VIEWPORT.height)
  })

  it('pulls a menu back inside when the anchor is past the right edge', () => {
    const { left } = clampMenuPosition({ x: 1030, y: 40 }, { width: 160, height: 120 }, VIEWPORT)
    expect(left).toBe(1024 - 160 - EDGE_MARGIN_PX)
    expect(left + 160 + EDGE_MARGIN_PX).toBeLessThanOrEqual(VIEWPORT.width)
  })

  it('never lets the menu start left of or above the margin', () => {
    const { left, top } = clampMenuPosition(
      { x: -50, y: -50 },
      { width: 160, height: 120 },
      VIEWPORT,
    )
    expect(left).toBe(EDGE_MARGIN_PX)
    expect(top).toBe(EDGE_MARGIN_PX)
  })

  it('keeps a menu TALLER than the viewport reachable: it starts at the margin', () => {
    // The clamp positions, it does not size. A menu taller than the window
    // still starts at the margin; the shell's max-height + overflow-y
    // (style.css, .cmd-overflow-menu) is what keeps every item reachable —
    // this pins the position half of that contract.
    const { top } = clampMenuPosition({ x: 40, y: 400 }, { width: 160, height: 900 }, VIEWPORT)
    expect(top).toBe(EDGE_MARGIN_PX)
    expect(top).toBeGreaterThanOrEqual(EDGE_MARGIN_PX)
  })
})

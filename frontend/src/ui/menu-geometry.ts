/**
 * The ONE geometry for a floating menu anchored at a point (nocx-vnirv.2).
 *
 * Both menus in the app — the kit's Solid `ContextMenu` and the scrollback's
 * imperative block overflow menu — clamp their on-screen position through
 * this module. AGENTS.md, "look for the existing answer before you write a
 * second one": the block menu used to position unclamped (a running block
 * sits at the bottom of the scrollback, so its ⋮ menu ran off the window's
 * bottom edge), and the kit already owned the clamp. A second implementation
 * of one concept would be a regression with a delay fuse — the two agree
 * everywhere you look and disagree somewhere you did not.
 */

/** Clearance from the viewport edge when clamping a menu on screen. */
export const EDGE_MARGIN_PX = 8

/** The point the menu is anchored at, in viewport coordinates. */
export interface MenuAnchor {
  readonly x: number
  readonly y: number
}

/** The menu's laid-out size. Measured, never guessed — a guessed height is
 *  what lets a menu overflow its own clamp. */
export interface MenuSize {
  readonly width: number
  readonly height: number
}

/** The viewport the menu must stay inside. */
export interface ViewportSize {
  readonly width: number
  readonly height: number
}

/**
 * Clamp the menu's top-left corner so the whole menu stays inside the
 * viewport with EDGE_MARGIN_PX to spare, flipping inward instead of
 * overflowing when the anchor sits near the bottom or right edge.
 *
 * A menu LARGER than the viewport still starts at the margin — this returns
 * a position, never a size — so keeping such a menu reachable is the menu's
 * own business: a `max-height` with `overflow-y: auto` on the shell (the
 * block menu does exactly this in style.css). The caller measures the shell
 * with `getBoundingClientRect`, which reports the box the CSS actually
 * renders — for the block menu that is the CAPPED height, never an uncapped
 * one — so the clamp sizes against the rendered shell. This function only
 * returns a position: that the menu never runs off the window is the cap's
 * guarantee, held by the block menu's CSS and by whatever shell carries it.
 */
export function clampMenuPosition(
  anchor: MenuAnchor,
  size: MenuSize,
  viewport: ViewportSize,
): { left: number; top: number } {
  const maxLeft = Math.max(EDGE_MARGIN_PX, viewport.width - size.width - EDGE_MARGIN_PX)
  const maxTop = Math.max(EDGE_MARGIN_PX, viewport.height - size.height - EDGE_MARGIN_PX)
  return {
    left: Math.min(Math.max(anchor.x, EDGE_MARGIN_PX), maxLeft),
    top: Math.min(Math.max(anchor.y, EDGE_MARGIN_PX), maxTop),
  }
}

// The terrain a pet walks on (nocx-q4qeh.1).
//
// nocx already draws the only landscape this feature needs: every frozen
// command block is a rectangle, and its TOP edge is a surface a small animal
// can stand on. This module turns those rectangles into ledges and nothing
// else — it never touches the DOM, so the rules below are testable without a
// browser, which is the whole reason it is a separate module (AD-8).
//
// Two rules are worth stating because both were learned the hard way in the
// mockup:
//
//   * A ledge needs HEAD CLEARANCE. The block's top edge is the floor, so the
//     animal's body occupies the space ABOVE it — which belongs to the
//     previous block, or to nothing at all when the edge is near the top of
//     the viewport. Without this rule the pet walks off the top of the screen
//     and the user never sees it again.
//   * A ledge needs WIDTH. A two-pixel sliver is a place to stand, not a
//     place to walk, and a pet that turns round every frame reads as broken.

/** A rectangle offered as possible ground, in viewport coordinates. */
export interface LedgeCandidate {
  /** Stable identity of the element this came from. The pet holds an id, not
   *  a rectangle: geometry changes on every scroll, identity does not. */
  readonly id: string
  readonly left: number
  readonly right: number
  /** The edge that can be stood on. */
  readonly top: number
}

/** A surface the pet may stand on, in viewport coordinates. */
export interface Ledge {
  readonly id: string
  readonly x0: number
  readonly x1: number
  readonly y: number
}

export interface TerrainOpts {
  /** How tall the animal is. Doubles as the clearance a ledge must have. */
  readonly petHeight: number
  /** Narrower than this and there is nowhere to walk. */
  readonly minWidth: number
  /** The area the pet is confined to. */
  readonly viewport: { readonly width: number; readonly height: number }
  /** Kept back from each end so the pet does not stand on the rounded corner. */
  readonly inset: number
}

export const DEFAULT_TERRAIN: Omit<TerrainOpts, 'viewport'> = {
  petHeight: 34,
  minWidth: 56,
  inset: 8,
}

/**
 * Turn candidate rectangles into the ledges a pet of this size can use.
 *
 * Returned top-to-bottom, so a caller picking "the ledge below y" can scan
 * forwards and stop at the first hit.
 */
export function deriveTerrain(candidates: readonly LedgeCandidate[], opts: TerrainOpts): Ledge[] {
  const out: Ledge[] = []
  for (const c of candidates) {
    const y = c.top
    // Off the top, or below the floor: not reachable at all.
    if (y < opts.petHeight) continue
    if (y > opts.viewport.height) continue
    const x0 = c.left + opts.inset
    const x1 = c.right - opts.inset
    if (x1 - x0 < opts.minWidth) continue
    out.push({ id: c.id, x0, x1, y })
  }
  out.sort((a, b) => a.y - b.y)
  return out
}

/** The ledge with this id, or null once the block that produced it is gone. */
export function ledgeById(terrain: readonly Ledge[], id: string | null): Ledge | null {
  if (id === null) return null
  return terrain.find((l) => l.id === id) ?? null
}

/**
 * The first ledge a fall from `fromY` to `toY` at horizontal position `x`
 * would pass through.
 *
 * Swept, not sampled: asking "is the pet inside a ledge right now" misses
 * every ledge thinner than one frame of travel, and at 60fps a fast fall
 * crosses tens of pixels per frame. The pet would drop through the terminal.
 */
export function ledgeCrossed(
  terrain: readonly Ledge[],
  x: number,
  fromY: number,
  toY: number,
): Ledge | null {
  if (toY <= fromY) return null
  let best: Ledge | null = null
  for (const l of terrain) {
    if (l.y <= fromY || l.y > toY) continue
    if (x < l.x0 || x > l.x1) continue
    if (best === null || l.y < best.y) best = l
  }
  return best
}

/**
 * The nearest ledge ABOVE this spot that a jump of `reach` could land on.
 *
 * Needed because the animal could only ever go down. Stepping off an edge and
 * descending from the middle move it through the terrain beautifully and in
 * one direction only, so over a few minutes every pet ended on the floor and
 * stayed there — which is the state that looks most like a sticker.
 *
 * `clearance` keeps it from picking the shelf it is already standing under:
 * a target must be far enough above to be worth jumping to.
 */
export function ledgeAbove(
  terrain: readonly Ledge[],
  x: number,
  y: number,
  reach: number,
  clearance = 12,
): Ledge | null {
  let best: Ledge | null = null
  for (const l of terrain) {
    if (l.y > y - clearance) continue
    if (y - l.y > reach) continue
    if (x < l.x0 || x > l.x1) continue
    if (best === null || l.y > best.y) best = l
  }
  return best
}

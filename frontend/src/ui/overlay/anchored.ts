/**
 * Anchored-overlay mechanics — the two things every transient overlay
 * anchored to a point does, owned once.
 *
 * A ContextMenu and a Popover are different components with different
 * semantics (one is a list of actions with roving focus, the other is a
 * panel of arbitrary content), and they are identical in exactly two
 * places: they clamp themselves onto the screen from a point the caller
 * measured, and they close on an outside pointerdown or on Escape. Written
 * twice, those two would agree until the day one of them grew a margin or
 * started listening on a different phase.
 *
 * Neither function knows what an overlay is. They take an element's
 * measurements and a pair of callbacks, so a component keeps its own
 * focus behaviour, its own roles and its own markup.
 */

/** Clearance from the viewport edge. One number, so two overlays cannot
 *  sit at two different distances from the same edge. */
export const OVERLAY_EDGE_MARGIN_PX = 8

export interface Point {
  x: number
  y: number
}

/**
 * Where an overlay of `size` should be placed so that a panel the caller
 * wants at `at` fits on screen.
 *
 * The point names the overlay's TOP-LEFT corner as the caller would like
 * it. An overlay that would overflow the right or bottom edge is pulled
 * back inside rather than flipped: flipping needs the anchor's own box and
 * every caller here anchors to a point. That also gives the bottom-zone
 * case for free — a panel opened from a button near the bottom of the
 * window is pushed up until its bottom edge clears the margin, which is
 * exactly where a person expects it.
 *
 * The `Math.max` on each axis is not decoration: when the overlay is taller
 * than the viewport the two bounds cross, and without it the clamp would
 * place the panel off the TOP of the screen, where nothing can reach it.
 */
export function clampToViewport(
  size: { width: number; height: number },
  at: Point,
  view: { width: number; height: number },
  margin: number = OVERLAY_EDGE_MARGIN_PX,
): Point {
  return {
    x: Math.min(Math.max(at.x, margin), Math.max(margin, view.width - size.width - margin)),
    y: Math.min(Math.max(at.y, margin), Math.max(margin, view.height - size.height - margin)),
  }
}

export interface TransientDismissDeps {
  /** The overlay's element, read at event time — an overlay that
   *  re-renders its panel must not be dismissed by its own new node. */
  element: () => HTMLElement | undefined
  /** A pointerdown landed outside the overlay. It is on its way to a new
   *  owner, so this is a notification and not a chance to keep focus. */
  onOutside: () => void
  /** Escape was pressed. The event is passed through: whether it stops
   *  propagating is the component's decision, because it depends on what
   *  is underneath — a menu over a terminal must swallow it, a panel that
   *  wants the next handler to see it need not. */
  onEscape: (e: KeyboardEvent) => void
  /** The document to listen on. A parameter for the same reason the
   *  picker takes one: a test drives its own. */
  doc?: Document
}

/**
 * Close on an outside pointerdown or on Escape, for as long as the returned
 * cleanup has not been called.
 *
 * `pointerdown` and not `click`: the overlay must be gone before the click
 * lands somewhere else, and a control INSIDE the overlay swallows its own
 * pointerdown by containment, so its subsequent click still activates it.
 */
export function attachTransientDismiss(deps: TransientDismissDeps): () => void {
  const doc = deps.doc ?? document
  const onPointerDown = (e: PointerEvent): void => {
    const el = deps.element()
    if (el && e.target instanceof Node && !el.contains(e.target)) deps.onOutside()
  }
  const onKeyDown = (e: KeyboardEvent): void => {
    if (e.key === 'Escape') deps.onEscape(e)
  }
  doc.addEventListener('pointerdown', onPointerDown)
  doc.addEventListener('keydown', onKeyDown)
  return () => {
    doc.removeEventListener('pointerdown', onPointerDown)
    doc.removeEventListener('keydown', onKeyDown)
  }
}

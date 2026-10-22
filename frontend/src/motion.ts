/**
 * Shared motion decisions — how long a layout movement takes, and whether
 * this build may move anything at all.
 *
 * One owner for each, because two modules animate the same hand-off: the
 * settle glide plays a block's displacement back (scrollback/controller.ts),
 * and the composer's box collapse and return are transitions of their own
 * (editor.ts, nocx-1ebrs.40). Durations that differ by a few milliseconds
 * would read as two movements; a motion permission derived twice would let
 * one of them move under `prefers-reduced-motion` while the other stands
 * still.
 */

/** How long the pane takes to settle after a block opens or freezes, and how
 *  long the composer's box takes to leave and come back. Short enough to
 *  belong to the keypress that caused it; long enough to read as a movement
 *  rather than a displacement. */
export const SETTLE_MS = 140

/** Fast out, slow in: the stack leaves at once and arrives gently, which is
 *  what makes it read as the block taking its place. */
export const SETTLE_EASING = 'cubic-bezier(0.2, 0, 0, 1)'

/** Whether this build may move `el`: the Web Animations API has to exist (it
 *  does not in jsdom, where a test asserts the layout and not the motion) and
 *  the person must not have asked for less of it. */
export function motionAllowed(el: Element): boolean {
  if (typeof el.animate !== 'function') return false
  const mq = window.matchMedia?.('(prefers-reduced-motion: reduce)')
  return mq ? !mq.matches : true
}

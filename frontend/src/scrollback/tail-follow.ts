/** ONE OWNER for "is this scroller following its tail" (nocx-yfpxl).
 *
 * Two facts make up that concept, and both of them were spelled twice: the
 * PREDICATE ("is it at its live end") and the REMEMBERED INTENT ("was the
 * person following it"). The scrollback controller had the careful spelling;
 * the summoned answer list had its own, weaker one, and the pair disagreed on
 * exactly the measurement that matters — a nested absolute surface read
 * mid-layout, which is the freeze/thaw transition. The list then read its own
 * zero viewport as "the reader has scrolled away" and never followed again.
 *
 * So the concept lives here and both scrollers extend it, rather than each
 * carrying a derivation that agrees everywhere anybody looked.
 */

/** Everything the concept needs off an element. A plain shape rather than
 *  `HTMLElement`, so a test can hand it a measurement without a DOM. */
interface TailGeometry {
  readonly scrollTop: number
  readonly clientHeight: number
  readonly scrollHeight: number
}

/** Whether the scroller can be asked at all. A zero viewport or a zero
 *  content height is not a scroll position — it is a scroller that has no
 *  layout box yet, or has just lost it, and `scrollTop + 0 >= scrollHeight - 2`
 *  answers "not at tail" for a reader who never moved. */
function isMeasurable(el: TailGeometry): boolean {
  return el.clientHeight > 0 && el.scrollHeight > 0
}

/** Whether the scroll position is at the live end. The two-pixel slack absorbs
 *  fractional layout; an unmeasurable scroller is never at its tail, because
 *  the question was not answered. */
function isAtTail(el: TailGeometry): boolean {
  return isMeasurable(el) && el.scrollTop + el.clientHeight >= el.scrollHeight - 2
}

/** The remembered half: follow intent that outlives one bad measurement.
 *
 * Geometry alone cannot carry this. A mutation that grows the content is
 * measured before it lands and reported after, and whoever watches the live
 * end — an IntersectionObserver, or the next mutation — can be a frame behind
 * the truth. One wrong reading must not cost the intent permanently. */
export class TailFollow {
  private _following: boolean

  constructor(following = true) {
    this._following = following
  }

  /** The remembered answer alone, for a caller that has already decided the
   *  geometry is not to be trusted at this instant. */
  get following(): boolean {
    return this._following
  }

  /** An authoritative observation from a watcher that owns the question — the
   *  scrollback's follow sentinel. It may clear the intent; a measurement
   *  taken during a mutation may not. */
  report(following: boolean): void {
    this._following = following
  }

  /** A deliberate move to the live end: it follows from here. */
  follow(): void {
    this._following = true
  }

  /** Put the scroller at its tail and remember that this is where it belongs.
   *  The one place a caller says "go to the end" about a plain scroller. */
  toTail(el: HTMLElement): void {
    this._following = true
    el.scrollTop = el.scrollHeight
  }

  /** Remembered intent, widened by the geometry. Never clears: the caller has
   *  a watcher of its own that owns leaving the live end. */
  intent(el: TailGeometry): boolean {
    return this._following || isAtTail(el)
  }

  /** Re-read the scroller and update the intent — but ONLY from a measurable
   *  reading. A scroller with no layout box has not told us anything, so the
   *  last thing it did tell us still stands. */
  private _reread(el: TailGeometry): boolean {
    if (isMeasurable(el)) this._following = isAtTail(el)
    return this._following
  }

  /** Keep `el` at its tail across a synchronous DOM mutation, if it was being
   *  followed. The decision belongs to the pre-mutation state: after the
   *  mutation the content is taller and every scroller reads "scrolled away"
   *  by exactly the amount that was just added. */
  mutateFollowingTail(el: HTMLElement, mutation: () => void): void {
    const following = this._reread(el)
    mutation()
    if (following) el.scrollTop = el.scrollHeight
  }
}

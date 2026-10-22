// The live grid's half of the armed-link gesture (nocx-1ebrs.48).
//
// ADR-0072 gave live display to the cell painter and took xterm's own hover
// underline away with xterm itself; the armed provider's spans were left
// with nothing tracking the pointer, so ⌘/⌃ over a printed url marked
// nothing and a click still opened it — an affordance the terminal promised
// on click and never showed. This module is that tracker.
//
// One question per owner (AD-8). WHICH text is a link stays the grammar's
// (`detect.ts`, read here through the policy's armed-gated `ranges` — the
// unarmed answer is [] because an underline under a dead click is a promise
// the terminal cannot keep). WHEN the modifier is held stays the shared
// ArmedTracker (armed.ts) — the same instance the click path and the frozen
// surface read, so one key cannot have two states. WHERE the overlay goes
// stays the painter's (`setLinkHighlight`, the seam this module only
// calls). What is left here is the part nobody else owns: the pointer,
// its last position, and the attribute the stylesheet reads for the hand.
//
// The pointer's last position is kept so the answer can be RE-DERIVED, not
// merely cleared: the modifier can return under a pointer that has not
// moved, and the program can scroll the screen under a pointer that has
// not moved — both must repaint the underline for the text that is there
// NOW, because a stale one advertises a click over text it does not
// underline. `screenChanged` is the re-derivation hook, the same seam the
// selection gesture is told about frames through (nocx-1ebrs.19).
import type { ArmedTracker } from './armed'

/** Set on the live surface while an armed link is under the pointer — the
 *  CSS hook that makes the pointer a hand exactly when ⌘+click would open
 *  something. One writer (this module), like the painter's `data-pointer`. */
export const LINK_HOVER_ATTR = 'data-link-hover'

/** The overlay painter the live grid exposes — `CellPainter` satisfies it
 *  structurally; declared here so this module depends on the capability,
 *  not on the painter. */
export interface LinkHighlightSink {
  setLinkHighlight(
    range: { readonly row: number; readonly from: number; readonly to: number } | null,
  ): void
}

/** The pane's pointer seam: the cell under a viewport point, as the row's
 *  text and the pointer's UTF-16 offset into it — the same answer the
 *  click path resolves and the grammar counts in. */
interface PointerCell {
  readonly row: number
  readonly text: string
  readonly offset: number
}

export interface LiveLinkHoverDeps {
  readonly surface: HTMLElement
  readonly armed: ArmedTracker
  /** The armed-gated spans of one row's text — `LivePolicy.ranges`. */
  readonly spans: (lineText: string) => Array<{ from: number; to: number }>
  readonly cellAt: (x: number, y: number) => PointerCell | null
  readonly highlight: LinkHighlightSink
}

export interface LiveLinkHover {
  /** The program's screen changed: re-derive at the pointer's last
   *  position, so the underline names the text that is there now. */
  screenChanged(): void
  dispose(): void
}

export function installLiveLinkHover(deps: LiveLinkHoverDeps): LiveLinkHover {
  const { surface } = deps
  let last: { x: number; y: number } | null = null

  const clear = (): void => {
    deps.highlight.setLinkHighlight(null)
    surface.removeAttribute(LINK_HOVER_ATTR)
  }

  const recompute = (): void => {
    if (!deps.armed.armed() || last === null) {
      clear()
      return
    }
    const at = deps.cellAt(last.x, last.y)
    if (at === null) {
      clear()
      return
    }
    const span = deps.spans(at.text).find((s) => at.offset >= s.from && at.offset < s.to)
    if (span === undefined) {
      clear()
      return
    }
    deps.highlight.setLinkHighlight({ row: at.row, from: span.from, to: span.to })
    surface.setAttribute(LINK_HOVER_ATTR, 'true')
  }

  const onMove = (event: PointerEvent): void => {
    last = { x: event.clientX, y: event.clientY }
    recompute()
  }
  const onLeave = (): void => {
    last = null
    clear()
  }
  const onArmed = (armed: boolean): void => {
    if (armed) recompute()
    else clear()
  }

  const unsubscribe = deps.armed.subscribe(onArmed)
  surface.addEventListener('pointermove', onMove)
  // pointerleave, not pointerout: crossing the rows below is not leaving
  // the surface, and the underline must survive row boundaries.
  surface.addEventListener('pointerleave', onLeave)

  return {
    screenChanged() {
      if (last !== null) recompute()
    },

    dispose() {
      surface.removeEventListener('pointermove', onMove)
      surface.removeEventListener('pointerleave', onLeave)
      unsubscribe()
      clear()
    },
  }
}

// ═══════════════════════════════════════════════════════════════════════════
// Terminal links (nocx-8yg.8) — the wiring point and the per-tab attachment.
//
// Module-level wiring set once by the composition root, the same shape
// `registerFileViewerSurface` uses and for the same reason: the opener needs
// the dispatcher, the file-viewer surface and the binding-liveness registry,
// all of which live in main.tsx, while the thing that needs the opener is a
// pane built four layers down by PaneManager. Threading it through would put
// a link dependency on PaneManager's constructor, on TerminalContent's, and
// on every test that builds either.
//
// The opener and the armed tracker are APP-level, not per-tab: one binding
// per session is cached in the opener (a binding holds an ssh connection
// reference), and the modifier is one key on one keyboard. A per-tab opener
// would mint a binding per tab for the same session.
//
// Unregistered is a supported state and a no-op, not a throw: a terminal in
// a test harness has no composition root, and it should render, not fail.
// ═══════════════════════════════════════════════════════════════════════════

import type { ActiveOrigin } from '../pane-content'
import { createLinkOpener, type LinkOpenDeps, type LinkOpener } from './open'
import { trackLinkModifier, type ArmedTracker } from './armed'
import { attachLinkClicks } from './surface'
import { createLivePolicy, type LivePolicy } from './live'
import { installLiveLinkHover, type LinkHighlightSink } from './live-hover'

// No re-export barrel: the modules below are imported directly by the two
// places that need them (blocks.ts asks decorate.ts, the renderer asks
// types.ts), and a barrel that forwarded them would be nine exports nothing
// reads — which the dead-export ratchet is right to refuse.

interface Wiring {
  readonly opener: LinkOpener
  readonly armed: ArmedTracker
  readonly notify: (message: string) => void
}

let wiring: Wiring | null = null

/** The one wiring point. Call once, from the composition root. */
export function registerTerminalLinks(deps: LinkOpenDeps): void {
  wiring = {
    opener: createLinkOpener(deps),
    armed: trackLinkModifier(),
    notify: deps.notify,
  }
}

/** One tab's link surfaces, held for the pane's lifetime. `screenChanged`
 *  is the frame seam: the live hover re-derives at the pointer when the
 *  program changes what is on the screen. */
export interface TerminalLinksHandle {
  detach(): void
  screenChanged(): void
}

/** One tab's frozen DOM and painter-owned live screen share the same
 *  grammar and link opener. The live reader supplies the row, its text and
 *  the UTF-16 offset under a pointer position, so this path does not
 *  depend on a terminal emulator. The highlight sink is the live grid's
 *  overlay painter (the cell painter); without one — a pane mounted before
 *  its painter, a test harness — the hover is simply not installed, and
 *  the frozen half and the click path are unaffected. */
export function attachTerminalLinks(
  scrollbackRoot: HTMLElement,
  liveSurface: HTMLElement,
  origin: () => Omit<ActiveOrigin, 'paneId'> | null,
  lineAtPoint: (x: number, y: number) => { row: number; text: string; offset: number } | null,
  highlight: LinkHighlightSink | null,
): TerminalLinksHandle {
  const w = wiring
  if (w === null) return { detach: () => {}, screenChanged: () => {} }
  const detachClicks = attachLinkClicks(scrollbackRoot, {
    opener: w.opener,
    origin,
    armed: w.armed,
  })
  const policy: LivePolicy = createLivePolicy({
    opener: w.opener,
    origin,
    armed: w.armed,
    notify: w.notify,
  })
  const hover =
    highlight === null
      ? null
      : installLiveLinkHover({
          surface: liveSurface,
          armed: w.armed,
          spans: (lineText) => policy.ranges(lineText),
          cellAt: lineAtPoint,
          highlight,
        })
  const onClick = (event: MouseEvent): void => {
    if (!(event.metaKey || event.ctrlKey)) return
    const point = lineAtPoint(event.clientX, event.clientY)
    if (point !== null) policy.activateAt(point.text, point.offset)
  }
  liveSurface.addEventListener('click', onClick)
  return {
    detach() {
      hover?.dispose()
      detachClicks()
      liveSurface.removeEventListener('click', onClick)
    },
    screenChanged() {
      hover?.screenChanged()
    },
  }
}

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

import type { TerminalRenderer } from '../renderers/types'
import type { ActiveOrigin } from '../pane-content'
import { createLinkOpener, type LinkOpenDeps, type LinkOpener } from './open'
import { trackLinkModifier, type ArmedTracker } from './armed'
import { attachLinkClicks } from './surface'
import { createLivePolicy } from './live'

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

/**
 * Give one tab both halves of the feature: ⌘-click on the frozen DOM
 * scrollback, and the link policy for the live xterm grid.
 *
 * Returns a detach the pane MUST call on dispose — the listeners and the
 * engine registration both close over this tab's origin.
 */
export function attachTerminalLinks(
  scrollbackRoot: HTMLElement,
  renderer: TerminalRenderer,
  origin: () => Omit<ActiveOrigin, 'paneId'> | null,
): () => void {
  const w = wiring
  if (w === null) return () => {}
  const detachClicks = attachLinkClicks(scrollbackRoot, {
    opener: w.opener,
    origin,
    armed: w.armed,
  })
  renderer.setLinkPolicy?.(
    createLivePolicy({ opener: w.opener, origin, armed: w.armed, notify: w.notify }),
  )
  return () => {
    detachClicks()
    renderer.setLinkPolicy?.(null)
  }
}

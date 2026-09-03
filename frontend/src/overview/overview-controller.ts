// What owns the overview's LIFETIME: the chord that opens it, the overlay
// entry that closes it, and the focus it hands back (bead nocx-edhcu).
//
// It is a separate module from the panel because the two answer different
// questions. The panel answers "what does this look like and what can I do in
// it"; this answers "when does it exist". Keeping the second inside the first
// is what makes a surface impossible to test without a keyboard, and a
// keyboard chord impossible to test without rendering.
//
// ESCAPE IS NOT HANDLED HERE, deliberately. `ui/overlay/stack` owns Escape for
// every overlay in the application and closes the TOPMOST one — which is what
// keeps a prompt raised over the overview from closing both. A second Escape
// listener of our own would be a second owner of one key, which is the defect
// AGENTS.md names ("Two surfaces may never own the same input").
//
// THE PANEL IS MOUNTED AND DISPOSED, not hidden. The overview is transient by
// design — opened a few times a day — so an application that is not showing it
// carries none of its DOM, none of its subscription to the port, and no timer.
// It also means every open reads the application fresh, which is the only
// honest answer for a surface whose whole job is to say what is true now.
import { render } from 'solid-js/web'
import { getPortalRoot } from '../ui/overlay/portal'
import { popOverlay, pushOverlay, type OverlayEntry } from '../ui/overlay/stack'
import { isOverviewChord } from './chord'
import { OverviewPanel } from './overview-panel'
import type { OverviewPort } from './overview-port'

export interface OverviewController {
  /** Show it. Opening an overview that is already open does nothing — see
   *  `openOverview` for why that is a requirement and not a nicety. */
  open(): void
  close(): void
  isOpen(): boolean
  /** Stop listening and tear the surface down. */
  dispose(): void
}

export interface OverviewControllerOptions {
  /** The clock the cards' ages are measured against. Injected for tests: a
   *  test that waited five real minutes for "for 5m" would depend on timing,
   *  which AGENTS.md forbids outright. */
  now?: () => number
  /** Where the document-level chord listener is installed. Defaults to
   *  `document`, which is where the other two ⌥⌘ surfaces install theirs. */
  target?: Pick<Document, 'addEventListener' | 'removeEventListener'>
  /** Refresh application-owned facts before mounting the transient surface. */
  onOpen?: () => void
}

/** Wire the overview to a port and to the keyboard. */
export function createOverviewController(
  port: OverviewPort,
  options: OverviewControllerOptions = {},
): OverviewController {
  const target = options.target ?? document
  let entry: OverlayEntry | null = null
  let host: HTMLElement | null = null
  let disposeRender: (() => void) | null = null

  const isOpen = (): boolean => host !== null

  const close = (): void => {
    if (!isOpen()) return
    disposeRender?.()
    disposeRender = null
    host?.remove()
    host = null
    if (entry) {
      popOverlay(entry)
      entry = null
    }
    // NOT `restoreFocus`. The overview covers the whole workspace, so what it
    // hands the keyboard back to is the pane in FRONT, not the thing that
    // opened it — see the port. Restoring the invoker parked the keyboard on
    // the toolbar button, where every keystroke went nowhere, and stole it
    // back from a pane the person had just chosen from a card.
    port.focusActive()
  }

  const openOverview = (): void => {
    // A SECOND OPEN IS NOT A SECOND OVERVIEW. Two overlay entries would need
    // two Escapes to get back to the terminal, and the second panel would be
    // drawn over a first one nothing was updating. The chord is a plain open
    // rather than a toggle on purpose: a person who presses it twice meant to
    // see the overview, not to see it and then not.
    if (isOpen()) return

    options.onOpen?.()
    // Recorded for the overlay stack's own bookkeeping, and read BEFORE the
    // panel takes focus: mounting focuses a card in the same turn, so an
    // `activeElement` read afterwards would be that card. Closing does not
    // return here — the keyboard goes to the pane in front (`focusActive`) —
    // but a nested overlay raised over this one still restores to it.
    const cameFrom = document.activeElement

    host = document.createElement('div')
    host.className = 'overview-host'
    getPortalRoot().appendChild(host)

    entry = pushOverlay(
      () => {
        close()
        return true
      },
      cameFrom,
      host,
    )

    disposeRender = render(() => OverviewPanel({ port, onClose: close, now: options.now }), host)
  }

  const onKeydown = (e: Event): void => {
    if (!isOverviewChord(e as KeyboardEvent)) return
    e.preventDefault()
    openOverview()
  }

  target.addEventListener('keydown', onKeydown)

  return {
    open: openOverview,
    close,
    isOpen,
    dispose(): void {
      target.removeEventListener('keydown', onKeydown)
      close()
    },
  }
}

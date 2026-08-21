// ApiContent — the API workbench as a PaneContent (design §9.1: the pane is
// the durable identity; a tab is the cheap wrapper a drag mints and destroys).
//
// A thin adapter over SolidPaneContent, which owns the host element, opens
// exactly one Solid root and disposes exactly once. What this class adds is
// what only it knows: the pane's name in the strip, the first listing, and
// where the keyboard goes when the tab is activated.
//
// The first listing is issued AFTER super.mount, and its failure is a state
// of the surface rather than a reason not to have one: the person still needs
// the field that opens a folder, and the tree still has to say why it is
// empty (AGENTS.md — a soft degrade must be visible in the product).

import { createComponent } from 'solid-js'
import { render } from 'solid-js/web'
import { SolidPaneContent, type PaneHost } from '../solid-pane-content'
import type { SingletonKey, SurfaceType } from '../pane-content'
import { ApiPane } from './api-pane'
import { createApiStore, type ApiStore } from './api-store'
import type { ApiWorkbenchServices } from './api-client'

// ── Registered surface constants (B.7) ─────────────────────────────────────

export const SURFACE_API: SurfaceType = 'nocx.api' as SurfaceType
export const SINGLETON_API: SingletonKey = 'nocx.api' as SingletonKey

/** What the strip calls it. */
export const API_PANE_TITLE = 'API'

export class ApiContent extends SolidPaneContent {
  private readonly store: ApiStore

  constructor(services: ApiWorkbenchServices) {
    super()
    this.store = createApiStore(services)
  }

  renderContent(root: HTMLElement): () => void {
    return render(() => createComponent(ApiPane, { store: this.store }), root)
  }

  async mount(target: HTMLElement, host: PaneHost, signal: AbortSignal): Promise<void> {
    if (this._disposed || this._hostElement) return
    if (signal.aborted) return
    host.setTitle(API_PANE_TITLE)
    await super.mount(target, host, signal)
    // Aborted while the root was opening: the base class has already
    // returned without mounting, so there is nothing to fill.
    if (this._disposed || this._hostElement === null) return
    await this.store.refresh()
  }

  /**
   * The tab was activated: the keyboard belongs where the person's next
   * keystroke belongs.
   *
   * With a request in the form that is the URL — the field edited between one
   * send and the next. With nothing open the URL field is disabled and cannot
   * take focus at all, so the keyboard goes to the field that opens a folder,
   * which is the only thing that can be done from the state the workbench
   * starts in. Focusing a disabled control silently does nothing, and a tab
   * activation that leaves the caret wherever it happened to be is how a
   * keyboard user loses their place.
   */
  focus(): void {
    const host = this._hostElement
    if (!host) return
    const url = host.querySelector<HTMLInputElement>('#api-url')
    if (url && !url.disabled) {
      url.focus()
      return
    }
    host.querySelector<HTMLInputElement>('#api-collection-path')?.focus()
  }

  // viewportChanged is inherited as a no-op: the workbench lays itself out in
  // CSS and has nothing to recompute from the pane's pixel size.
  //
  // dispose() is inherited too — it closes the Solid root and removes the
  // host element. There is no binding to release: the collection handles the
  // store holds belong to the app's opened-folder list (design §6.1), not to
  // this pane, and closing the tab must not close the user's folders.
}

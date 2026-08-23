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
import type {
  ApiWorkbenchServices,
  DirectoryPicker,
  FilePicker,
  NativeDropPort,
} from './api-client'

// ── Registered surface constants (B.7) ─────────────────────────────────────

export const SURFACE_API: SurfaceType = 'nocx.api' as SurfaceType
export const SINGLETON_API: SingletonKey = 'nocx.api' as SingletonKey

/** What the strip calls it — and the activity-bar entry with it, which is
 *  the point of the constant: the pane, its rail entry and the tab strip
 *  label are one word, spelled once (nocx-zccer). It was 'API', which names
 *  a protocol rather than what a person came here to do. */
export const API_PANE_TITLE = 'API testing'

export class ApiContent extends SolidPaneContent {
  private readonly store: ApiStore
  /** The native directory picker, when this build has one. Held beside the
   *  store rather than in it: `dialog.*` is another domain's method, and the
   *  store owns api.* state (AD-8). */
  private readonly openDirectory?: DirectoryPicker
  /** The native file picker, when this build has one — held beside the
   *  directory picker and never merged with it: either can be absent on its
   *  own, and a surface that read one capability for both would draw a
   *  control it cannot honour. */
  private readonly openFile?: FilePicker
  /** The native window drop, when this build has one. Held beside the
   *  pickers and never merged with them: no Wails runtime means no drop, and
   *  that is independent of whether either picker exists. */
  private readonly nativeDrop?: NativeDropPort

  constructor(services: ApiWorkbenchServices) {
    super()
    this.store = createApiStore(services)
    this.openDirectory = services.openDirectory
    this.openFile = services.openFile
    this.nativeDrop = services.nativeDrop
  }

  renderContent(root: HTMLElement): () => void {
    return render(
      () =>
        createComponent(ApiPane, {
          store: this.store,
          openDirectory: this.openDirectory,
          openFile: this.openFile,
          nativeDrop: this.nativeDrop,
        }),
      root,
    )
  }

  async mount(target: HTMLElement, host: PaneHost, signal: AbortSignal): Promise<void> {
    if (this._disposed || this._hostElement) return
    if (signal.aborted) return
    host.setTitle(API_PANE_TITLE)
    await super.mount(target, host, signal)
    // Aborted while the root was opening: the base class has already
    // returned without mounting, so there is nothing to fill.
    if (this._disposed || this._hostElement === null) return
    // Subscribe BEFORE the first listing. The subscription costs no round
    // trip — the watch set is published by the listing that follows — and
    // registering it afterwards would leave a window in which a change that
    // arrived during the listing had nobody to tell.
    this.store.startWatching()
    await this.store.refresh()
    // The connections a run may have gone through, so a run that went
    // through one can be LABELLED the moment it arrives rather than after
    // somebody opens the environments page. Not awaited into the mount path
    // in a way that could delay the tree: a failure here costs a name, and
    // the run still says which id it used.
    void this.store.loadConnections()
  }

  /**
   * The tab closed.
   *
   * There is still no COLLECTION handle to release — those belong to the
   * app's opened-folder list (design §6.1) and closing the tab must not close
   * the user's folders — but there IS now a files binding, minted here and
   * held by nothing else, so this is the only place it can be given back. The
   * base class closes the Solid root and removes the host element.
   */
  dispose(): void {
    this.store.dispose()
    super.dispose()
  }

  /**
   * The tab was activated: the keyboard belongs where the person's next
   * keystroke belongs.
   *
   * With a request in the form that is the URL — the field edited between one
   * send and the next. With nothing open the URL field is disabled and cannot
   * take focus at all, so the keyboard goes to the collections menu, which is
   * the only thing that can be done from the state the workbench starts in —
   * and which is now the one control the three doors are behind.
   * It used to be the folder field, and that field no longer sits in the
   * panel: opening a folder is an ask now (nocx-84shs), so the field lives
   * inside a closed dialog where nothing can focus it. Focusing a disabled
   * or hidden control silently does nothing, and a tab activation that
   * leaves the caret wherever it happened to be is how a keyboard user loses
   * their place.
   */
  focus(): void {
    const host = this._hostElement
    if (!host) return
    const url = host.querySelector<HTMLInputElement>('#api-url')
    if (url && !url.disabled) {
      url.focus()
      return
    }
    host.querySelector<HTMLButtonElement>('#api-collections-menu')?.focus()
  }

  // viewportChanged is inherited as a no-op: the workbench lays itself out in
  // CSS and has nothing to recompute from the pane's pixel size.
}

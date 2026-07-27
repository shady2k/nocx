// ═══════════════════════════════════════════════════════════════════════════
// SolidTabContent — generic base for TabContent implementations that render
// a Solid component into a .surface-host element.
//
// Creates the mount element, opens exactly one Solid root, and disposes
// exactly once. Never renders page chrome and never carries terminal
// behaviour. Subclasses provide the component via renderContent().
// ═══════════════════════════════════════════════════════════════════════════

import { BaseTabContent, type TabHost, type ContentViewport } from './tab-content'
export type { TabHost, ContentViewport }

/**
 * Base class for TabContent adapters that render a Solid component.
 * Owns the .surface-host element lifecycle — creation, root, and disposal.
 * Subclasses implement renderContent() to provide the component factory.
 */
export abstract class SolidTabContent extends BaseTabContent {
  protected _hostElement: HTMLElement | null = null
  private _dispose: (() => void) | null = null
  protected _disposed = false

  /** Render the Solid component into the given host element.
   *  Called once during mount, synchronously, after the host element is
   *  appended to the target. Returns the dispose function for cleanup. */
  abstract renderContent(host: HTMLElement): () => void

  // Not `async`: creating the host and opening a Solid root are synchronous,
  // and an async method with nothing to await is what @typescript-eslint's
  // require-await exists to catch. The signature stays Promise-returning
  // because TabContent.mount is awaited by the caller and by subclasses that
  // do have something to wait for — SettingsContent awaits its component's
  // ready() after calling super.mount().
  mount(target: HTMLElement, host: TabHost, signal: AbortSignal): Promise<void> {
    if (this._disposed || this._hostElement) return Promise.resolve()
    if (signal.aborted) return Promise.resolve()

    const root = document.createElement('div')
    root.className = 'surface-host'
    target.append(root)
    this._hostElement = root

    this._dispose = this.renderContent(root)
    return Promise.resolve()
  }

  // eslint-disable-next-line @typescript-eslint/no-unused-vars
  viewportChanged(_viewport: ContentViewport): void {
    // Override in subclass if needed
  }

  focus(): void {
    // Override in subclass if needed
  }

  dispose(): void {
    this._disposed = true
    this._dispose?.()
    this._dispose = null
    this._hostElement?.remove()
    this._hostElement = null
  }
}

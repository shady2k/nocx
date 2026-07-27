// ═══════════════════════════════════════════════════════════════════════════
// SettingsContent — wraps the Solid settings component as a TabContent.
// Thin adapter over SolidTabContent; keeps existing public behaviour
// (focus, scrollToKey). Page owns the narrow breakpoint
// (base.css @media max-width: 640px). ExportSection is rendered as a
// child component inside SettingsComponent.
// ═══════════════════════════════════════════════════════════════════════════

import { createComponent } from 'solid-js'
import { render } from 'solid-js/web'
import { type ProfileClient, type SSHProfile } from './profiles'
import { type SettingsObserver } from './settings-observer'
import { SolidTabContent, type TabHost } from './solid-tab-content'
import type { SurfaceType, SingletonKey } from './tab-content'
import { SettingsComponent, type SettingsComponentHandle } from './settings'

// ── Registered surface constants (B.7) ─────────────────────────────────

export const SURFACE_SETTINGS: SurfaceType = 'nocx.settings' as SurfaceType
export const SINGLETON_SETTINGS: SingletonKey = 'nocx.settings' as SingletonKey

// ── SettingsContent ─────────────────────────────────────────────────────

export class SettingsContent extends SolidTabContent {
  private handleRef: { current: SettingsComponentHandle | null } = { current: null }
  private handle: SettingsComponentHandle | null = null
  /** Callback for when the user clicks Connect on a profile. */
  onConnect?: (profile: SSHProfile) => void

  constructor(
    private readonly profileClient: ProfileClient,
    private readonly observer?: SettingsObserver,
  ) {
    super()
  }

  renderContent(root: HTMLElement): () => void {
    return render(
      () =>
        createComponent(SettingsComponent, {
          profileClient: this.profileClient,
          observer: this.observer,
          onConnect: (profile: SSHProfile) => {
            this.onConnect?.(profile)
          },
          ref: this.handleRef,
        }),
      root,
    )
  }

  // ── TabContent ───────────────────────────────────────────────────────

  async mount(target: HTMLElement, host: TabHost, signal: AbortSignal): Promise<void> {
    if (this._disposed || this._hostElement) return
    if (signal.aborted) return

    host.setTitle('Settings')
    await super.mount(target, host, signal)
    this.handle = this.handleRef.current!
    await this.handle.ready()
  }

  focus(): void {
    this.handle?.focus()
  }

  // viewportChanged is inherited from SolidTabContent as a no-op. Page owns the
  // narrow breakpoint in CSS now (base.css @media max-width: 640px), so Settings
  // has nothing to do with the viewport and does not override it.

  // dispose() inherited from SolidTabContent — it tears down the root
  // element and Solid root. The handle reference becomes stale naturally
  // as the component disposes.

  // ── Deep link ───────────────────────────────────────────────────────

  scrollToKey(key: string): void {
    this.handle?.scrollToKey(key)
  }
}

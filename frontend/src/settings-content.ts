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
import type { VaultController } from './vault'
import { type SettingsObserver } from './settings-observer'
import { SolidTabContent, type TabHost } from './solid-tab-content'
import type { SurfaceType, SingletonKey } from './tab-content'
import { SettingsComponent, type SettingsComponentHandle } from './settings'
import type { AgentClient } from './agent'
import type { EndpointClient } from './endpoints'

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
    private readonly vaultController?: VaultController,
    private readonly vaultClient?: import('./vault-client').VaultClient,
    private readonly dialogClient?: import('./dialog-client').DialogClient,
    private readonly footprintClient?: import('./footprint-client').FootprintClient,
    private readonly endpointsClient?: EndpointClient,
    private readonly agentClient?: AgentClient,
  ) {
    super()
  }

  renderContent(root: HTMLElement): () => void {
    return render(
      () =>
        createComponent(SettingsComponent, {
          profileClient: this.profileClient,
          vaultController: this.vaultController,
          vaultClient: this.vaultClient,
          dialogClient: this.dialogClient,
          footprintClient: this.footprintClient,
          agentClient: this.agentClient,
          endpointsClient: this.endpointsClient,
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
    if (this.pendingNewConnection) {
      this.pendingNewConnection = false
      this.handle.newConnection()
    }
    if (this.pendingNewSecret !== null) {
      const name = this.pendingNewSecret
      this.pendingNewSecret = null
      this.handle.newSecret(name)
    }
    if (this.pendingNewEndpoint) {
      this.pendingNewEndpoint = false
      this.handle.newEndpoint()
    }
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

  /**
   * Open the Connections page on a blank profile.
   *
   * Queued when the tab is not mounted yet: opening Settings and asking for a
   * new connection is one user action, but the mount is a promise the caller
   * does not hold, so a straight call would be dropped exactly when the tab was
   * freshly opened — the common case.
   */
  startNewConnection(): void {
    if (this.handle) {
      this.handle.newConnection()
      return
    }
    this.pendingNewConnection = true
  }

  /** Open the Secrets page with the add dialog up — the prompt's '@' picker
   *  offering to create the secret it could not find. Queued before mount
   *  for the same reason as startNewConnection. */
  startNewSecret(name = ''): void {
    if (this.handle) {
      this.handle.newSecret(name)
      return
    }
    this.pendingNewSecret = name
  }

  /** Open the Endpoints page with the editor up on a blank endpoint — the
   *  ask surface's repair for a question refused with "no endpoint
   *  configured". Queued before mount for the same reason as
   *  startNewConnection: opening Settings and asking for the editor is one
   *  user action, and the mount is a promise the caller does not hold. */
  startNewEndpoint(): void {
    if (this.handle) {
      this.handle.newEndpoint()
      return
    }
    this.pendingNewEndpoint = true
  }

  private pendingNewConnection = false
  /** The queued request's prefilled name, or null when nothing is queued.
   *  A string (including '') means "asked"; null means "nobody asked". */
  private pendingNewSecret: string | null = null
  private pendingNewEndpoint = false
}

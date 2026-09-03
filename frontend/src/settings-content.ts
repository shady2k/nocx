// ═══════════════════════════════════════════════════════════════════════════
// SettingsContent — wraps the Solid settings component as a PaneContent.
// Thin adapter over SolidPaneContent; keeps existing public behaviour
// (focus, scrollToKey). Page owns the narrow breakpoint
// (base.css @media max-width: 640px). ExportSection is rendered as a
// child component inside SettingsComponent.
// ═══════════════════════════════════════════════════════════════════════════

import { createComponent } from 'solid-js'
import { render } from 'solid-js/web'
import { type ProfileClient, type SSHProfile } from './profiles'
import type { VaultController } from './vault'
import { type SettingsObserver } from './settings-observer'
import { SolidPaneContent, type PaneHost } from './solid-pane-content'
import type { SurfaceType, SingletonKey } from './pane-content'
import { SettingsComponent, type SettingsComponentHandle } from './settings'
import type { AgentClient } from './agent'
import type { SnippetsStore } from './snippets/snippets-store'
import type { SkillsStore } from './skills-store'
import type { EndpointClient } from './endpoints'
import type { HistoryStatusStore } from './history-status'

// ── Registered surface constants (B.7) ─────────────────────────────────

export const SURFACE_SETTINGS: SurfaceType = 'nocx.settings' as SurfaceType
export const SINGLETON_SETTINGS: SingletonKey = 'nocx.settings' as SingletonKey

// ── SettingsContent ─────────────────────────────────────────────────────

export class SettingsContent extends SolidPaneContent {
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
    private readonly snippetsStore?: SnippetsStore,
    /** Whether durable command history is running (nocx-rtg0.15). Passed
     *  through to the History section, which otherwise offers a toggle, a
     *  retention age and a two-number budget that govern nothing when the
     *  store never opened. */
    private readonly historyStatus?: HistoryStatusStore,
    /** What build this is, for the About page (nocx-8bbp). */
    private readonly aboutClient?: import('./about-client').AboutClient,
    /** The clipboard the About page's Copy diagnostics writes through. */
    private readonly clipboard?: import('./clipboard').ClipboardAccess,
    private readonly policyClient?: import('./policy-client').PolicyClient,
    /** The ONE secret picker source (nocx-3o0ed.4). Built by the composition
     *  root, not here: the Connections and Endpoints pages place the same
     *  field the API workbench does, and a second source assembled beside
     *  main.tsx's would be the same wiring written twice — agreeing until the
     *  day one of them changed. */
    private readonly secretSource?: import('./ui/secret-picker').SecretPickerSource,
    private readonly skillsStore?: SkillsStore,
    /** What an enrolled pane is emitting (nocx-02uci), and what the window
     *  calls that pane. */
    private readonly emittingClient?: import('./emitting-client').EmittingClient,
    private readonly paneName?: (sessionId: string) => string | null,
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
          secretSource: this.secretSource,
          dialogClient: this.dialogClient,
          footprintClient: this.footprintClient,
          agentClient: this.agentClient,
          policyClient: this.policyClient,
          emittingClient: this.emittingClient,
          paneName: this.paneName,
          endpointsClient: this.endpointsClient,
          snippetsStore: this.snippetsStore,
          skillsStore: this.skillsStore,
          historyStatus: this.historyStatus,
          aboutClient: this.aboutClient,
          clipboard: this.clipboard,
          observer: this.observer,
          onConnect: (profile: SSHProfile) => {
            this.onConnect?.(profile)
          },
          ref: this.handleRef,
        }),
      root,
    )
  }

  // ── PaneContent ───────────────────────────────────────────────────────

  async mount(target: HTMLElement, host: PaneHost, signal: AbortSignal): Promise<void> {
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
      const queued = this.pendingNewSecret
      this.pendingNewSecret = null
      this.handle.newSecret(queued.name, queued.value)
    }
    if (this.pendingNewEndpoint) {
      this.pendingNewEndpoint = false
      this.handle.newEndpoint()
    }
    if (this.pendingPage !== null) {
      const id = this.pendingPage
      this.pendingPage = null
      this.handle.openPage(id)
    }
  }

  focus(): void {
    this.handle?.focus()
  }

  // viewportChanged is inherited from SolidPaneContent as a no-op. Page owns the
  // narrow breakpoint in CSS now (base.css @media max-width: 640px), so Settings
  // has nothing to do with the viewport and does not override it.

  // dispose() inherited from SolidPaneContent — it tears down the root
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
   *  offering to create the secret it could not find, or a value field whose
   *  lock offers to store what it is holding. Queued before mount for the
   *  same reason as startNewConnection.
   *
   *  `value` is what that field held (nocx-3o0ed.6): this page owns the only
   *  create form on the fallback path, so a value that stops here is a value
   *  the person has to fetch back by hand. A caller with none omits it and
   *  the form opens empty, which is what it always did. */
  startNewSecret(name = '', value = ''): void {
    if (this.handle) {
      this.handle.newSecret(name, value)
      return
    }
    this.pendingNewSecret = { name, value }
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

  /** Open a component page by its registry id — the general form of the
   *  three starters above, for a caller that wants the page and nothing
   *  more ("Manage snippets…", nocx-d346). Queued before mount for the same
   *  reason they are: opening Settings and naming the page is one user
   *  action, and the mount is a promise the caller does not hold. */
  openPage(id: string): void {
    if (this.handle) {
      this.handle.openPage(id)
      return
    }
    this.pendingPage = id
  }

  private pendingNewConnection = false
  /** The queued request's prefilled name and value, or null when nothing is
   *  queued. An object (with either field '') means "asked"; null means
   *  "nobody asked". */
  private pendingNewSecret: { name: string; value: string } | null = null
  private pendingNewEndpoint = false
  /** The queued page id, or null when nobody asked. */
  private pendingPage: string | null = null
}

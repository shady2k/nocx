// ═══════════════════════════════════════════════════════════════════════════
// Tab and TabManager — chrome, lifecycle, and tab-model management.
//
// Tab is chrome-only: it owns the pane, display state, and delegates content
// lifecycle to a TabContent instance. It implements TabHost so content can
// push title, tooltip, attention, and close requests upward.
//
// TabManager owns the ordered tab model, activation rules, and MRU stack.
// It constructs content, creates tabs, and wires tab-chrome intents.
// ═══════════════════════════════════════════════════════════════════════════

import type { WSClient } from './ipc'
import { detectAgentStatus, type AgentStatus } from './agent-status'
import { type ClipboardAccess, type ClipboardGate } from './clipboard'
import type { ClipboardBanner } from './banner'
import type { ProfileClient } from './profiles'
import { adoptAliasProfile } from './profiles'
import { showToast } from './ui/toast'
import { log } from './log'
import type { TabStrip } from './tab-strip'
import type {
  TabHost,
  TabContent,
  ContentDescriptor,
  ContentViewport,
  ActiveOrigin,
  SurfaceType,
} from './tab-content'
import { SURFACE_TERMINAL } from './tab-content'
import { TerminalContent, type HostKeyErrorEvidence } from './terminal-content'

// ═══════════════════════════════════════════════════════════════════════════
// Tab — chrome and lifecycle, delegates content to TabContent
// ═══════════════════════════════════════════════════════════════════════════

export class Tab implements TabHost {
  readonly id: number
  readonly pane = document.createElement('div')

  /** Model-level descriptor: surface type, singleton key, restore info. */
  readonly descriptor: ContentDescriptor

  readonly content: TabContent

  // ── Display state (read by TabStrip via TabView) ─────────────────────

  private _active = false
  onDisplayChange: (() => void) | null = null

  private _title = ''
  private _programTitle = ''
  private _hasActivity = false
  private _agentStatus: AgentStatus | null = null
  private _tooltip = ''
  private _subtitle = ''
  private _adoptable = false
  private _onAdopt: (() => void) | null = null
  private _warning = false
  private _warningLabel = ''
  private _disposed = false
  private _mountAbort = new AbortController()
  // ── B.5 geometry authority ──────────────────────────────────────────
  private _viewportObserver: ResizeObserver | null = null
  private _latestViewport: ContentViewport | null = null
  private _mountStarted = false

  constructor(content: TabContent, descriptor: ContentDescriptor, id: number) {
    this.id = id
    this.content = content
    this.descriptor = descriptor

    this.pane.className = 'pane'
    this.pane.id = `pane-${id}`
    this.pane.setAttribute('role', 'tabpanel')

    // ── Pre-mount target ──────────────────────────────────────────────
    // Hand the pane to the content before mount, so setVisible is
    // meaningful from the first setActive(true) call. setTarget is on the
    // TabContent interface — every implementation must accept or no-op it.
    content.setTarget(this.pane)
  }

  // ── TabView conformance ───────────────────────────────────────────────

  get title(): string {
    return this._title
  }

  /** Display title: falls back to descriptor.defaultTitle when empty. */
  get displayTitle(): string {
    return this._title || this.descriptor.defaultTitle
  }

  get hasActivity(): boolean {
    return this._hasActivity
  }

  get agentStatus(): AgentStatus | null {
    return this._agentStatus
  }

  get tooltip(): string {
    return this._tooltip
  }

  /**
   * The tab's location, for the strip's optional second line — empty when the first
   * line already says it.
   *
   * The decision cannot be made here. TerminalContent composes the title as
   * `programTitle || cwdTitle` and hands the RESULT to setTitle, so from the tab's
   * side every title looks equally like a name. Only the content knows whether a
   * program supplied one, so the content decides and pushes the answer.
   */
  get subtitle(): string {
    return this._subtitle
  }

  get paneId(): string {
    return this.pane.id
  }

  get adoptable(): boolean {
    return this._adoptable
  }

  get onAdopt(): (() => void) | null {
    return this._onAdopt
  }

  /** Mark the tab as saveable or not, with the save action. */
  setAdoptState(adoptable: boolean, onAdopt: () => void): void {
    if (this._disposed) return
    this._adoptable = adoptable
    this._onAdopt = adoptable ? onAdopt : null
    this.onDisplayChange?.()
  }

  /** Mark the tab's environment degraded/uncertain (nocx-4t37.2): the one
   *  signal tab chrome may carry. It persists for as long as the session
   *  stays degraded (nocx-5uu5) — the card is the once-per-(shell, reason)
   *  event, and this is the state that outlives it. The capability
   *  statement itself lives in the rail.
   *
   *  The label is what the mark is ABOUT. A mark that cannot say what it
   *  means is a mark people learn to ignore, so the integration status
   *  supplies its own wording rather than the chrome inventing one. */
  setWarningState(warning: boolean, label = ''): void {
    if (this._disposed) return
    if (warning === this._warning && label === this._warningLabel) return
    this._warning = warning
    this._warningLabel = label
    this.onDisplayChange?.()
  }

  get warning(): boolean {
    return this._warning
  }

  get warningLabel(): string {
    return this._warningLabel
  }

  setActive(active: boolean): void {
    this._active = active
    // Visibility crosses the seam through setVisible — the content
    // toggles the 'active' class on its mount target (AD-6 corollary).
    this.content.setVisible(active)
    if (active) {
      this._hasActivity = false
    }
    this.onDisplayChange?.()
  }

  // ── TabHost ───────────────────────────────────────────────────────────

  setTitle(title: string): void {
    if (this._disposed) return

    // A TUI clears the title on the way out by emitting OSC 0/2 with empty
    // string. Classify before falling back: the marker lives in the raw
    // title, and an empty title is the shell clearing it — not an agent state.
    this.updateAgentStatus(title)

    this._programTitle = title.trim()
    const next = this._programTitle || this.descriptor.defaultTitle
    if (next !== this._title) {
      this._title = next
      this.onDisplayChange?.()
    }
  }

  /** Terminal-content-only: update tooltip from cwd or SSH info.
   *  Not on TabHost — wired through TerminalContent's constructor. */
  updateTooltip(tooltip: string): void {
    if (this._disposed) return
    this._tooltip = tooltip
    this.onDisplayChange?.()
  }

  /** Terminal-content-only, like updateTooltip: the location line, or '' when the
   *  title already carries it. See the `subtitle` getter. */
  updateSubtitle(subtitle: string): void {
    if (this._disposed) return
    if (subtitle === this._subtitle) return
    this._subtitle = subtitle
    this.onDisplayChange?.()
  }

  requestAttention(): void {
    if (this._disposed) return
    this.markActivity()
  }

  requestClose(): void {
    if (this._disposed) return
    this.onCloseRequested?.()
  }

  /** Called when the content (terminal session) wants the tab closed. */
  onCloseRequested?: () => void

  // ── Lifecycle ─────────────────────────────────────────────────────────

  /** Mount the content into this tab's pane. Called by TabManager on first
   *  activation, and suppressed after that: mount-once is enforced here at
   *  the seam, not by a private flag inside one implementation (nocx-njrx.2).
   *  The pane is already visible by now — the content received it through
   *  setTarget() in the constructor — so mount and the first viewport
   *  delivery both measure a laid-out element. */
  async start(): Promise<void> {
    if (this._mountStarted) return
    this._mountStarted = true
    log.info('nocx: Tab.start() called', { id: this.id })
    await this.content.mount(this.pane, this, this._mountAbort.signal)
    // B.5: replay the latest buffered viewport, or measure now if none yet.
    if (this._latestViewport) {
      this.content.viewportChanged(this._latestViewport)
    } else {
      this._deliverViewport()
    }
  }

  focus(): void {
    this.content.focus()
  }

  close(): void {
    this._disposed = true
    this._mountAbort.abort()
    this._viewportObserver?.disconnect()
    this._viewportObserver = null
    this.content.dispose()
  }
  // ── Internals ─────────────────────────────────────────────────────────

  private markActivity(): void {
    if (this._disposed) return
    // Only a tab the user is not looking at can hold unread output. Without this
    // guard the flag was set by output arriving in the ACTIVE tab, where nothing
    // renders it — and it survived the switch away, because setActive() clears
    // the flag on activation and not on deactivation. The result: the tab you
    // just left lit up its indicator with nothing having happened in it since.
    if (this._active) return
    if (!this._hasActivity) {
      this._hasActivity = true
      this.onDisplayChange?.()
    }
  }

  private updateAgentStatus(title: string): void {
    if (this._disposed) return
    const next = detectAgentStatus(title)
    if (next === this._agentStatus) return
    this._agentStatus = next
    if (next === 'idle' && !this._active) {
      this.markActivity()
    }
  }

  // ── B.5 geometry authority ──────────────────────────────────────────

  /**
   * Start observing the pane element for resize. Called when the pane enters
   * the DOM (TabManager.addTab). Delivery is synchronous — the browser already
   * batches ResizeObserver entries once per frame — and suppressed after
   * disposal. See the callback for why an extra frame was the bug, not the fix.
   */
  setupViewportObserver(): void {
    if (this._viewportObserver) return
    const observer = new ResizeObserver((entries) => {
      if (this._disposed) return
      const entry = entries[entries.length - 1]
      if (!entry) return
      const { width, height } = entry.contentRect
      // Never send a misleading zero rectangle for hidden/inactive tabs (B.5).
      if (width === 0 && height === 0) return
      // Deliver synchronously. ResizeObserver already fires once per frame,
      // after layout and before paint, with pending entries batched — so
      // wrapping this in requestAnimationFrame coalesced nothing the browser
      // had not coalesced already, it only deferred delivery to the NEXT
      // frame. That extra frame IS the one-frame squeeze in nocx-dau: the grid
      // stayed sized for the old rectangle while the pane had already been
      // painted at the new one.
      //
      // Synchronous delivery inside a ResizeObserver callback risks a resize
      // loop when delivery changes the observed element's own box. It does not
      // here: the observer watches this.pane, sized by the flex layout, while
      // delivery ends in the renderer resizing the terminal INSIDE the pane.
      // The pane's contentRect is unaffected, so the callback cannot re-arm
      // itself.
      this._deliverViewport()
    })
    observer.observe(this.pane)
    this._viewportObserver = observer
  }

  /**
   * Measure the pane and deliver the viewport to content, but only after
   * mount has started and before disposal (B.5 delivery rules).
   */
  private _deliverViewport(): void {
    if (this._disposed || !this._mountStarted) return
    const rect = this.pane.getBoundingClientRect()
    if (rect.width === 0 && rect.height === 0) return
    const dpr = window.devicePixelRatio || 1
    const vp: ContentViewport = { width: rect.width, height: rect.height, devicePixelRatio: dpr }
    // Suppress equal consecutive rectangles (B.5).
    const prev = this._latestViewport
    if (
      prev &&
      prev.width === vp.width &&
      prev.height === vp.height &&
      prev.devicePixelRatio === vp.devicePixelRatio
    ) {
      return
    }
    this._latestViewport = vp
    this.content.viewportChanged(vp)
  }
}

// ═══════════════════════════════════════════════════════════════════════════
// TabManager — ordered tab model, activation rules, MRU
// ═══════════════════════════════════════════════════════════════════════════

export class TabManager {
  private readonly tabs: Tab[] = []
  private nextTabId = 1
  private activeTab: Tab | null = null
  private readonly panes: HTMLElement
  private readonly client: WSClient
  private readonly clipboard: ClipboardAccess
  private readonly gate: ClipboardGate
  private readonly banner: ClipboardBanner
  private readonly profileClient: ProfileClient
  private _initialTabReady: Promise<void> | undefined
  private tabStrip: TabStrip
  private readonly bar: HTMLElement
  private readonly verticalHost: HTMLElement
  /** MRU stack: most-recently-activated tab ids. */
  private readonly recentTabIds: number[] = []
  /** Called when an SSH connection fails because the vault is sealed. */
  onVaultSealed?: () => void
  /** Called when an SSH connection fails because the host key is unknown
   *  or changed. Resolves true only after explicit trust; the content then
   *  retries the same open. */
  onHostKeyError?: (evidence: HostKeyErrorEvidence, signal: AbortSignal) => Promise<boolean>
  /** Called when the reference picker's setup offer is activated and the
   *  machine has no OS key: the vault layer owns the setup dialog, so the
   *  hook raises it (wired by main.tsx to vaultController.openSetup). */
  onSetupVault?: () => void

  /** The prompt picker's "Add a secret…" row — opens Settings → Secrets
   *  with the add dialog up. */
  onCreateSecret?: (name: string) => void
  /** A question refused for want of an endpoint — opens Settings →
   *  Endpoints with the editor up on a blank one, so the refusal carries
   *  its repair. */
  onCreateEndpoint?: () => void
  /** Called when the user performs a UI action that should reset the
   *  vault idle timer. Wired by main.tsx to vaultClient.activity(). */
  onActivity?: () => void
  /** Called when the active tab changes — the seam for chrome that must
   *  re-scope to the tab in front. The sidebar's ports view follows the
   *  active tab through this (nocx-wzc4.7); wired by main.tsx to a Solid
   *  signal. */
  onActiveTabChange?: () => void

  constructor(
    bar: HTMLElement,
    verticalHost: HTMLElement,
    panes: HTMLElement,
    client: WSClient,
    clipboard: ClipboardAccess,
    gate: ClipboardGate,
    banner: ClipboardBanner,
    profileClient: ProfileClient,
    tabStrip: TabStrip,
  ) {
    this.panes = panes
    this.client = client
    this.clipboard = clipboard
    this.gate = gate
    this.banner = banner
    this.profileClient = profileClient
    this.tabStrip = tabStrip
    this.bar = bar
    this.verticalHost = verticalHost

    // Wire TabStrip intents.
    this.wireStrip(tabStrip)

    window.addEventListener('keydown', this.onKeydown, true)
  }

  /** Return the mount host for the given strip based on orientation. */
  private hostFor(strip: TabStrip): HTMLElement {
    return strip.orientation === 'vertical' ? this.verticalHost : this.bar
  }

  get tabCount(): number {
    return this.tabs.length
  }

  get initialTabReady(): Promise<void> {
    if (!this._initialTabReady) {
      throw new Error('initialTabReady accessed before openInitialTab')
    }
    return this._initialTabReady
  }

  /** Mount the tab strip and open the initial terminal tab.
   *
   *  Callable exactly once, and that is enforced here rather than documented:
   *  a second call would mount the strip again and open a second "initial"
   *  tab. This epic has already removed one contract that held by coincidence
   *  (mount-once, which lived in a private flag inside one TabContent
   *  implementation instead of at the seam), so a comment is not enough.
   *
   *  `initialTabReady` resolves only from terminal content — a non-terminal
   *  first tab must not be able to report the app healthy. */
  openInitialTab(): Promise<void> {
    if (this._initialTabReady) {
      throw new Error('openInitialTab called twice; the composition root calls it exactly once')
    }
    this.tabStrip.mount(this.hostFor(this.tabStrip))
    const initialTab = this.newTab()
    const initialContent = initialTab.content as TerminalContent
    this._initialTabReady = initialContent.ready.then((ok) => {
      if (!ok) throw new Error('initial tab failed to start')
    })
    return this._initialTabReady
  }
  // ── Tab creation ──────────────────────────────────────────────────────

  /** Create a new local terminal tab and activate it. */
  newTab(): Tab {
    const tabRef = { current: undefined as Tab | undefined }
    const content = new TerminalContent(
      this.client,
      this.clipboard,
      this.gate,
      this.banner,
      this.profileClient,
      (tooltip) => tabRef.current?.updateTooltip(tooltip),
      // The alt-screen callback that used to sit here is gone with the
      // parameter. It toggled `#app.alt-screen`, which emptied the tab strip so
      // a viewport-sized fullscreen xterm would not paint through it; the
      // fullscreen region lives inside its pane now (nocx-6w4z).
      undefined,
      {
        onSubtitleChange: (subtitle) => tabRef.current?.updateSubtitle(subtitle),
        onWarningChange: (warning, label) => tabRef.current?.setWarningState(warning, label),
        onPortsTargetChange: () => this.onActiveTabChange?.(),
        onActiveOriginChange: () => this.onActiveTabChange?.(),
        onSetupVault: this.onSetupVault,
        onCreateSecret: this.onCreateSecret,
        onCreateEndpoint: this.onCreateEndpoint,
      },
    )
    const descriptor: ContentDescriptor = {
      surfaceType: SURFACE_TERMINAL,
      singletonKey: null,
      restoreDescriptor: { type: 'local' },
      supportsAttention: true,
      // No placeholder. A terminal tab is named after where it is, and that
      // arrives one WebSocket round-trip after the tab appears; printing
      // 'Terminal' in the meantime showed a word that is never the answer and
      // then replaced it, which reads as a flicker rather than as loading
      // (nocx-83a). An empty title is honest and the strip's width is fixed, so
      // nothing moves when the real one lands.
      defaultTitle: '',
    }
    const tab = this.addTab(content, descriptor)
    tabRef.current = tab
    return tab
  }

  newSSHTab(profileId: string, host: string, user?: string, port?: number, title?: string): Tab {
    log.info('nocx: newSSHTab called', { profileId, host, user, port, title })
    const sshOpts = { profileId, host, user, port } as const
    const tabRef = { current: undefined as Tab | undefined }
    const content = new TerminalContent(
      this.client,
      this.clipboard,
      this.gate,
      this.banner,
      this.profileClient,
      (tooltip) => tabRef.current?.updateTooltip(tooltip),
      sshOpts,
      {
        onSubtitleChange: (subtitle) => tabRef.current?.updateSubtitle(subtitle),
        onAdoptabilityChange: (adoptable: boolean) => {
          const tab = tabRef.current
          if (!tab) return
          if (adoptable) {
            tab.setAdoptState(true, () => this._adoptAlias(host, user, port, tab))
          } else {
            tab.setAdoptState(false, () => {})
          }
        },
        onWarningChange: (warning, label) => tabRef.current?.setWarningState(warning, label),
        onActiveOriginChange: () => this.onActiveTabChange?.(),
        onPortsTargetChange: () => this.onActiveTabChange?.(),
        onVaultSealed: this.onVaultSealed,
        onHostKeyError: this.onHostKeyError,
        onSetupVault: this.onSetupVault,
        onCreateSecret: this.onCreateSecret,
        onCreateEndpoint: this.onCreateEndpoint,
      },
    )
    const descriptor: ContentDescriptor = {
      surfaceType: SURFACE_TERMINAL,
      singletonKey: null,
      restoreDescriptor: { type: 'ssh', profileId, host, user },
      supportsAttention: true,
      defaultTitle: title || host,
    }
    const tab = this.addTab(content, descriptor)
    tabRef.current = tab
    return tab
  }

  /** Adopt an SSH alias as a saved nocx profile. Creates the profile and switches
   *  the tab to track the saved profile. */
  private _adoptAlias(
    host: string,
    user: string | undefined,
    port: number | undefined,
    tab: Tab,
  ): void {
    const profile = adoptAliasProfile(host, user, port)

    void this.profileClient
      .createProfile(profile)
      .then((saved) => {
        // Use what the backend returned: the id is minted there, so `profile.id`
        // is still empty here.
        tab.setAdoptState(false, () => {})
        log.info('nocx: alias adopted', { host, profileId: saved.id })
        showToast({ level: 'success', message: `Saved "${host}" as a connection` })
      })
      .catch((err: unknown) => {
        const message = err instanceof Error ? err.message : String(err)
        log.error('nocx: alias adoption failed', { host, error: message })
        showToast({ level: 'danger', message: `Could not save: ${message}` })
      })
  }

  /** Open a hand-typed ssh target as a saved nocx connection: build the
   *  profile from the host the pane walked into (adoptAliasProfile — the
   *  backend mints the id, so createProfile's record is the id source) and
   *  open a NEW tab on the saved profile. A new tab is deliberate: the
   *  current tab's ssh is a child of the local shell, nocx owns no channel
   *  on it, and re-scoping that tab to a profile with no session would land
   *  the Ports panel on "open a session first" — the tab on the saved
   *  profile connects immediately, so Ports works and Forward exists there
   *  (W2). */
  openAsConnection(host: string, user: string | undefined): void {
    const profile = adoptAliasProfile(host, user, undefined)
    void this.profileClient
      .createProfile(profile)
      .then((saved) => {
        log.info('nocx: opened host as a connection', { host, profileId: saved.id })
        this.newSSHTab(saved.id, host, user)
        showToast({ level: 'success', message: `Opened "${host}" as a connection` })
      })
      .catch((err: unknown) => {
        const message = err instanceof Error ? err.message : String(err)
        log.error('nocx: open-as-connection failed', { host, error: message })
        showToast({ level: 'danger', message: `Could not connect to ${host}: ${message}` })
      })
  }

  /**
   * Open a tab with the given content, deduplicating by singletonKey.
   * If a tab with the same singletonKey already exists, activates it.
   */
  openTab(content: TabContent, descriptor: ContentDescriptor): Tab {
    if (descriptor.singletonKey) {
      const existing = this.tabs.find((t) => t.descriptor.singletonKey === descriptor.singletonKey)
      if (existing) {
        void this.activate(existing)
        return existing
      }
    }
    return this.addTab(content, descriptor)
  }

  /** Internal: create a Tab, wire lifecycle, add to model, activate. */
  private addTab(content: TabContent, descriptor: ContentDescriptor): Tab {
    const tab = new Tab(content, descriptor, this.nextTabId++)

    this.tabs.push(tab)
    this.panes.append(tab.pane)
    // B.5: start observing pane geometry once it's in the DOM.
    tab.setupViewportObserver()

    tab.onCloseRequested = () => this.closeTab(tab)
    this.tabStrip.addTab(tab)
    void this.activate(tab)
    return tab
  }

  /** Swap the TabStrip at runtime without restarting.  Transfers all
   *  existing tabs to the new strip, wires intents, and preserves the
   *  active-tab state.  The old strip's DOM is removed. */
  replaceStrip(newStrip: TabStrip): void {
    // Detach the old strip: clear intents so late callbacks are no-ops.
    const old = this.tabStrip
    old.onActivate = null
    old.onClose = null
    old.onNewTab = null
    old.onReorder = null

    // Determine the old and new mount hosts based on orientation.
    // This handles both horizontal→vertical and vertical→horizontal transitions.
    const oldHost = this.hostFor(old)
    const newHost = this.hostFor(newStrip)

    // Clear the old host and strip everything setupContainer put on it.
    // The class matters for layout: when switching vertical→horizontal,
    // #vertical-tabstrip must not keep .tabstrip-vertical or it leaves a 240px
    // empty column. The ARIA attributes matter for the accessibility tree:
    // an emptied host that keeps role="tablist" is a second, empty tablist
    // sitting beside the real one, which is worse than no tablist at all.
    oldHost.innerHTML = ''
    oldHost.classList.remove('tabstrip-vertical')
    oldHost.removeAttribute('role')
    oldHost.removeAttribute('aria-label')
    oldHost.removeAttribute('aria-orientation')

    // Mount the new strip on the correct host.
    newStrip.mount(newHost)

    // Transfer every existing tab into the new strip.
    for (const tab of this.tabs) {
      newStrip.addTab(tab)
    }

    // Wire new strip intents.
    this.wireStrip(newStrip)

    // Restore active-tab state.
    if (this.activeTab) {
      newStrip.setActive(this.activeTab.id)
    }

    this.tabStrip = newStrip
  }

  private wireStrip(strip: TabStrip): void {
    strip.onActivate = (id) => {
      const tab = this.tabs.find((t) => t.id === id)
      if (tab) void this.activate(tab)
    }
    strip.onClose = (id) => {
      const tab = this.tabs.find((t) => t.id === id)
      if (tab) this.closeTab(tab)
    }
    strip.onNewTab = () => this.newTab()
    strip.onReorder = (fromId, toId) => this.reorderTab(fromId, toId)
  }

  /**
   * Close a tab. If it was the active tab, activates the MRU tab.
   * Closing the last tab opens a fresh terminal — view tabs have no
   * restoreDescriptor and are never the automatic replacement.
   */
  closeTab(tab: Tab): void {
    const index = this.tabs.indexOf(tab)
    if (index === -1) return

    const wasActive = tab === this.activeTab
    this.removeFromRecent(tab.id)

    tab.close()
    tab.pane.remove()
    this.tabStrip.removeTab(tab.id)
    this.tabs.splice(index, 1)

    if (this.tabs.length === 0) {
      this.newTab()
      return
    }

    if (wasActive) {
      const mruTab = this.popRecent()
      if (mruTab) {
        void this.activate(mruTab)
      }
    }
  }

  /** Activate a tab: show its pane, mount content, focus. */
  async activate(tab: Tab): Promise<void> {
    log.info('nocx: TabManager.activate() called', {
      tabId: tab.id,
      isActive: tab === this.activeTab,
    })
    if (tab === this.activeTab) {
      tab.focus()
      return
    }

    if (this.activeTab) {
      this.pushRecent(this.activeTab.id)
    }

    this.activeTab?.setActive(false)
    this.activeTab = tab
    tab.setActive(true)

    this.removeFromRecent(tab.id)
    this.tabStrip.setActive(tab.id)

    log.info('nocx: tab.setActive(true) called', {
      paneClasses: tab.pane.className,
    })
    await tab.start()
    tab.focus()
    this.onActiveTabChange?.()
  }

  activateByIndex(index: number): void {
    const tab = this.tabs[index]
    if (tab) void this.activate(tab)
  }

  closeActiveTab(): void {
    if (this.activeTab) this.closeTab(this.activeTab)
  }

  /** The active tab's terminal content, when the active tab is a terminal.
   *  Global actions (the quick-connect "Integrate this shell" item,
   *  nocx-ynsx) reach the shell at the current prompt through this; the
   *  content itself owns the PROMPT_READY && trusted && owned gate. */
  activeTerminalContent(): TerminalContent | null {
    const content = this.activeTab?.content
    return content instanceof TerminalContent ? content : null
  }

  /** The ports.* target the ACTIVE tab scopes to (nocx-wzc4.8): the
   *  reserved "local" for a local shell, the saved-profile id for a
   *  saved-profile SSH tab, null otherwise (alias tab, Settings, …): the
   *  ports entry points are no-ops then. */
  portsTargetId(): string | null {
    return this.activeTerminalContent()?.portsTargetId ?? null
  }

  /** When portsTargetId is null because the pane walked into an environment
   *  we cannot enumerate, this names it. '' otherwise (nocx-695k.3). */
  portsUnavailableReason(): string {
    return this.activeTerminalContent()?.portsUnavailableReason ?? ''
  }

  /** The ACTIVE tab's origin for origin-following surfaces (the Files
   *  panel, design §5.4): the tab id from the Tab, the session and kind
   *  from the content's optional capability — never an instanceof branch,
   *  because the seam exists so TabManager never learns which content
   *  class replied (terminal content answers from its session; viewer
   *  content answers from the binding it was opened with). Null when the
   *  active tab has no origin or its content does not implement the
   *  capability. */
  activeOrigin(): ActiveOrigin | null {
    const tab = this.activeTab
    const origin = tab?.content.activeOrigin?.()
    return tab && origin ? { tabId: tab.id, ...origin } : null
  }

  /** The ACTIVE tab's surface type (B.8) — the seam chrome reads to answer
   *  "what kind of tab is in front" without instanceof tests. The sidebar's
   *  Settings collapse (nocx-3e3b) reads this through the composition root:
   *  the descriptor is the single owner of what a tab is, and neither
   *  activeTerminalContent() (null for viewer tabs too) nor activeOrigin()
   *  (null transiently while a session opens) can tell Settings apart.
   *  Null when no tab is active yet. */
  activeSurfaceType(): SurfaceType | null {
    return this.activeTab?.descriptor.surfaceType ?? null
  }

  reorderTab(draggedId: number, targetId: number): void {
    const draggedIndex = this.tabs.findIndex((t) => t.id === draggedId)
    const targetIndex = this.tabs.findIndex((t) => t.id === targetId)
    if (draggedIndex === -1 || targetIndex === -1) return

    const [draggedTab] = this.tabs.splice(draggedIndex, 1)
    const adjustedTarget = draggedIndex < targetIndex ? targetIndex - 1 : targetIndex
    this.tabs.splice(adjustedTarget, 0, draggedTab)

    this.tabStrip.reorder(this.tabs)
  }

  // ── MRU helpers ──────────────────────────────────────────────────────

  private pushRecent(id: number): void {
    this.removeFromRecent(id)
    this.recentTabIds.push(id)
  }

  private popRecent(): Tab | undefined {
    while (this.recentTabIds.length > 0) {
      const id = this.recentTabIds.pop()!
      const tab = this.tabs.find((t) => t.id === id)
      if (tab) return tab
    }
    return undefined
  }

  private removeFromRecent(id: number): void {
    const idx = this.recentTabIds.indexOf(id)
    if (idx !== -1) this.recentTabIds.splice(idx, 1)
  }

  // ── Keyboard shortcuts ───────────────────────────────────────────────

  private readonly onKeydown = (e: KeyboardEvent): void => {
    const mod = e.metaKey || e.ctrlKey
    if (!mod || e.altKey) return

    if (e.key === 't') {
      e.preventDefault()
      e.stopPropagation()
      this.onActivity?.()
      this.newTab()
      return
    }

    if (e.key === 'w') {
      e.preventDefault()
      e.stopPropagation()
      this.onActivity?.()
      this.closeActiveTab()
      return
    }

    // Cmd/Ctrl+1..9 — switch to tab by visual index (all tabs).
    const keyNum = Number(e.key)
    if (Number.isInteger(keyNum) && keyNum >= 1 && keyNum <= 9 && keyNum <= this.tabs.length) {
      e.preventDefault()
      e.stopPropagation()
      this.onActivity?.()
      this.activateByIndex(keyNum - 1)
    }
  }
}

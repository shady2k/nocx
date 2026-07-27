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
import { log } from './log'
import type { TabStrip } from './tab-strip'
import type { TabHost, TabContent, ContentDescriptor, ContentViewport } from './tab-content'
import { SURFACE_TERMINAL } from './tab-content'
import { TerminalContent } from './terminal-content'

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

  get paneId(): string {
    return this.pane.id
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
      (tooltip) => tabRef.current?.updateTooltip(tooltip),
      (inAlt) => {
        if (tabRef.current === this.activeTab) this.syncAltScreenClass(inAlt)
      },
    )
    const descriptor: ContentDescriptor = {
      surfaceType: SURFACE_TERMINAL,
      singletonKey: null,
      restoreDescriptor: { type: 'local' },
      supportsAttention: true,
      defaultTitle: 'Terminal',
    }
    const tab = this.addTab(content, descriptor)
    tabRef.current = tab
    return tab
  }

  newSSHTab(profileId: string, host: string, user?: string): Tab {
    log.info('nocx: newSSHTab called', { profileId, host })
    const sshOpts = { profileId, host, user }
    const tabRef = { current: undefined as Tab | undefined }
    const content = new TerminalContent(
      this.client,
      this.clipboard,
      this.gate,
      this.banner,
      (tooltip) => tabRef.current?.updateTooltip(tooltip),
      (inAlt) => {
        if (tabRef.current === this.activeTab) this.syncAltScreenClass(inAlt)
      },
      sshOpts,
    )
    const descriptor: ContentDescriptor = {
      surfaceType: SURFACE_TERMINAL,
      singletonKey: null,
      restoreDescriptor: { type: 'ssh', profileId, host, user },
      supportsAttention: true,
      defaultTitle: host,
    }
    const tab = this.addTab(content, descriptor)
    tabRef.current = tab
    return tab
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

  /** Toggle #app alt-screen class based on active terminal buffer type. */
  private syncAltScreenClass(inAlt: boolean): void {
    const app = document.getElementById('app')
    if (!app) return
    app.classList.toggle('alt-screen', inAlt)
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
  }

  activateByIndex(index: number): void {
    const tab = this.tabs[index]
    if (tab) void this.activate(tab)
  }

  closeActiveTab(): void {
    if (this.activeTab) this.closeTab(this.activeTab)
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
      this.newTab()
      return
    }

    if (e.key === 'w') {
      e.preventDefault()
      e.stopPropagation()
      this.closeActiveTab()
      return
    }

    // Cmd/Ctrl+1..9 — switch to tab by visual index (all tabs).
    const keyNum = Number(e.key)
    if (Number.isInteger(keyNum) && keyNum >= 1 && keyNum <= 9 && keyNum <= this.tabs.length) {
      e.preventDefault()
      e.stopPropagation()
      this.activateByIndex(keyNum - 1)
    }
  }
}

// ═══════════════════════════════════════════════════════════════════════════
// Pane and PaneManager — chrome and lifecycle over a model the BACKEND owns.
//
// Pane is chrome-only: it owns the pane element, display state, and delegates
// content lifecycle to a PaneContent instance. It implements PaneHost so
// content can push title, tooltip, attention, and close requests upward.
//
// PaneManager USED TO OWN THE ORDERED MODEL, and does not any more
// (nocx-isoph.4, design §4.1). The order, the membership and the decoration
// come from LayoutStore, which holds what the backend answered; this class
// asks for a change, renders the answer, and decides none of them. What is
// left here is chrome: which element is mounted, which pane is focused, and
// the MRU stack behind Cmd-W.
//
// THE MRU IS DELIBERATELY STILL HERE, and it is the one thing in this file
// that looks like state the epic should have moved. It is not: a window is a
// VIEWPORT (§10), so "which tab is in front" is a fact about a viewport and
// not about the chain — two windows on one workspace have two answers, and a
// stored one would be a fact with two owners the moment multi-window lands
// (nocx-mgbjx). §4.5's stored list says the same by omission: colour, name,
// position, pinned, layout, workspace_id, seen-mark, and no "active".
// ═══════════════════════════════════════════════════════════════════════════

import type { WSClient } from './ipc'
import { detectAgentStatus, type AgentStatus } from './agent-status'
import { type ClipboardAccess, type ClipboardGate } from './clipboard'
import type { ClipboardBanner } from './banner'
import type { ProfileClient, SSHProfile } from './profiles'
import { adoptAliasProfile, parseQuickConnect } from './profiles'
import { resolveSshProfileOverlay } from './quick-connect-assembly'
import { cardQuote } from './overview/overview-model'
import { showToast } from './ui/toast'
import { showConfirm } from './ui/dialog'
import {
  showTabEditDialog,
  showWorkspaceCreateDialog,
  showWorkspaceEditDialog,
} from './name-colour-dialog'
import type { OverviewPaneFacts, OverviewPort, OverviewSnapshot } from './overview/overview-port'
import { LayoutStore } from './layout/layout-store'
import type { UIStateClient } from './uistate-client'
import { tabLabel } from './layout/tab-label'
import { stripOrder } from './layout/strip-order'
import { lineageOrder } from './layout/strip-tree'
import { workspaceAxis, type GroupAxis } from './layout/strip-groups'
import { isWorkspaceColour } from './layout/workspace-colours'
import { workspaceActionRows, type WorkspaceMenuRow } from './workspace-menu'
import { uuidv7 } from './layout/uuid7'
import { restoreOnStartup } from './restore-setting'
import type { Pane as PaneRow, Workspace as WorkspaceRow } from './generated/layout.read'
import { leftRunningMessage, liveDescendants, type LineageNode } from './lineage'
import { closingWorkspaceMessage, type WorkspaceMember } from './live-work'
import { log } from './log'
import type { TabStrip } from './tab-strip'
import type {
  PaneHost,
  PaneContent,
  ContentDescriptor,
  ContentViewport,
  ActiveOrigin,
  SurfaceType,
} from './pane-content'
import { SURFACE_TERMINAL } from './pane-content'
import type { SnippetProviderDeps } from './snippets/snippet-provider'
import { TerminalContent, type HostKeyErrorEvidence, type PaneIdentity } from './terminal-content'

// ═══════════════════════════════════════════════════════════════════════════
// Pane — chrome and lifecycle, delegates content to PaneContent
// ═══════════════════════════════════════════════════════════════════════════

export class Pane implements PaneHost {
  readonly id: number
  /** THE PANE'S ONE IDENTITY: a UUIDv7 minted once per pane and never reused
   *  (nocx-tsajw, then nocx-isoph.4 §7). It is the id the layout chain stores,
   *  the id history.record scopes its captures to, and the id
   *  secrets.paneClosed names when they die — one identity, not one per seam.
   *  Chrome keeps its own numeric id for the DOM; this one is what crosses the
   *  wire, and it is durable: it must survive a restart, so it cannot come
   *  from a backend instance. */
  readonly wireId: string
  readonly pane = document.createElement('div')

  /** Model-level descriptor: surface type, singleton key, restore info. */
  readonly descriptor: ContentDescriptor

  readonly content: PaneContent

  // ── Display state (read by TabStrip via PaneView) ─────────────────────

  private _active = false
  onDisplayChange: (() => void) | null = null

  private _title = ''
  /** The last COMPOSED title the content pushed (`programTitle ||
   *  runningCommandTitle || cwdTitle`), before the default-title
   *  fallback. Deliberately not called `_programTitle`: that name was a
   *  lie — the field holds whatever the content composed, which is
   *  usually a filesystem path. The program's own title arrives
   *  separately, through updateProgramTitle. */
  private _pushedTitle = ''
  private _hasActivity = false
  /** Whether the content has declared its opening over (PaneHost.
   *  contentSettled). Output before that is the pane starting up, not
   *  something the user missed. */
  private _settled = false
  /** The tab's stored decoration, as the backend last answered. Never
   *  decided here — see setTabDecoration. */
  private _tabName: string | null = null
  private _colour: string | null = null
  private _pinned = false
  private _groupKey = ''
  private _depth = 0
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

  constructor(content: PaneContent, descriptor: ContentDescriptor, id: number, wireId: string) {
    this.id = id
    this.wireId = wireId
    this.content = content
    this.descriptor = descriptor

    this.pane.className = 'pane'
    this.pane.id = `pane-${id}`
    this.pane.setAttribute('role', 'tabpanel')

    // ── Pre-mount target ──────────────────────────────────────────────
    // Hand the pane to the content before mount, so setVisible is
    // meaningful from the first setActive(true) call. setTarget is on the
    // PaneContent interface — every implementation must accept or no-op it.
    content.setTarget(this.pane)
  }

  // ── PaneView conformance ───────────────────────────────────────────────

  get title(): string {
    return this._title
  }

  /**
   * What the strip shows for this tab.
   *
   * THE LABEL IS COMPUTED, NEVER STORED (§4.5): a name the user typed wins,
   * and otherwise the tab is named by its panes — which only works because a
   * pane is named by what is in it (the program's title, else the running
   * command, else the cwd; composed in terminal-content.ts). One pane per tab
   * today, so the list has one member; when a tab can hold several
   * (nocx-8m2x6) the composition moves up to a tab-level view and this getter
   * goes with it.
   *
   * The descriptor's default is the last resort, for a pane one round trip
   * old that has no title yet.
   */
  get displayTitle(): string {
    return tabLabel(this._tabName, [this._title]) || this.descriptor.defaultTitle
  }

  /**
   * The decoration the BACKEND stores for the tab this pane is in.
   *
   * Pushed in rather than read out: the Pane has no client and asks nobody —
   * PaneManager renders what LayoutStore holds, and a Pane that could fetch
   * its own colour would be a second reader of one fact.
   */
  setTabDecoration(d: { name: string | null; colour: string | null; pinned: boolean }): void {
    if (this._disposed) return
    if (d.name === this._tabName && d.colour === this._colour && d.pinned === this._pinned) return
    this._tabName = d.name
    this._colour = d.colour
    this._pinned = d.pinned
    this.onDisplayChange?.()
  }

  /**
   * WHERE THE STRIP DRAWS THIS ROW: which group it is under, and how far in
   * (nocx-isoph.5).
   *
   * Pushed in for the same reason the decoration is: a Pane asks nobody
   * anything. PaneManager reads the chain — the workspace the tab is in, and
   * the lineage depth its parents give it — and hands the answer down. Both
   * are projections of what the backend stores; neither is decided here or
   * remembered anywhere else.
   */
  setStripPlacement(placement: { groupKey: string; depth: number }): void {
    if (this._disposed) return
    if (placement.groupKey === this._groupKey && placement.depth === this._depth) return
    this._groupKey = placement.groupKey
    this._depth = placement.depth
    this.onDisplayChange?.()
  }

  get groupKey(): string {
    return this._groupKey
  }

  get depth(): number {
    return this._depth
  }

  get colour(): string | null {
    return this._colour
  }

  get pinned(): boolean {
    return this._pinned
  }

  /** The name the user typed for this pane's tab, or null. Read by the
   *  rename prompt so it opens on what is there. */
  get tabName(): string | null {
    return this._tabName
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
   * The pane's location, for the strip's optional second line — empty when the first
   * line already says it.
   *
   * The decision cannot be made here. TerminalContent composes the title as
   * `programTitle || runningCommandTitle || cwdTitle` and hands the RESULT to
   * setTitle, so from the pane's side every title looks equally like a name.
   * Only the content knows whether a program (or a command) supplied one, so
   * the content decides and pushes the answer.
   */
  get subtitle(): string {
    return this._subtitle
  }

  /**
   * WHAT IS HAPPENING IN THIS PANE, in one line — the same sentence the
   * overview's card prints, from the same function (`cardQuote`).
   *
   * It is derived here rather than remembered because every fact it reads is
   * live: the ledger's last record, the buffer's last line, whether a
   * full-screen program owns the screen. A stored copy would be stale by the
   * time the strip drew it, and a second derivation would be a second answer
   * — the failure AD-8 is about. The strip asks at display time; the overview
   * asks at snapshot time; both get the rule from one place.
   *
   * Empty for a pane that is not a terminal: a settings surface has no last
   * command and nothing to preview.
   */
  get preview(): string {
    const content = this.content
    if (!(content instanceof TerminalContent)) return ''
    const live = content.liveWork()
    return (
      cardQuote({
        title: this._title || null,
        agentStatus: this._agentStatus,
        runningCommand: live?.command ?? null,
        fullScreen: content.fullScreen(),
        lastBlock: content.lastBlock(),
        lastLine: content.lastOutputLine(),
      }) ?? ''
    )
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

  /** Mark the pane as saveable or not, with the save action. */
  setAdoptState(adoptable: boolean, onAdopt: () => void): void {
    if (this._disposed) return
    this._adoptable = adoptable
    this._onAdopt = adoptable ? onAdopt : null
    this.onDisplayChange?.()
  }

  /** Mark the pane's environment degraded/uncertain (nocx-4t37.2): the one
   *  signal pane chrome may carry. It persists for as long as the session
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

  // ── PaneHost ───────────────────────────────────────────────────────────

  setTitle(title: string): void {
    if (this._disposed) return

    this._pushedTitle = title.trim()
    const next = this._pushedTitle || this.descriptor.defaultTitle
    if (next !== this._title) {
      this._title = next
      this.onDisplayChange?.()
    }
  }

  /** Terminal-content-only: update tooltip from cwd or SSH info.
   *  Not on PaneHost — wired through TerminalContent's constructor. */
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

  /** The content's opening is over — see PaneHost.contentSettled. Anything
   *  the opening put on the tab is withdrawn here rather than filtered on
   *  the way in: a mark that has to be undone is cheaper to get right than
   *  a rule that has to catch every byte of a shell's start. */
  contentSettled(): void {
    if (this._disposed || this._settled) return
    this._settled = true
    if (!this._hasActivity) return
    this._hasActivity = false
    this.onDisplayChange?.()
  }

  requestClose(): void {
    if (this._disposed) return
    this.onCloseRequested?.()
  }

  /** Terminal-content-only, like updateTooltip: the program's own OSC 0/2
   *  title, delivered separately from the composed display title — the
   *  agent-state classifier keys on THIS, never on the composed title
   *  (which is usually a filesystem path or a command line). A TUI
   *  clearing its title on the way out emits OSC 0/2 with an EMPTY
   *  string; that empty delivery reaches here too and resets the status,
   *  the way an empty title always did.
   *  Wired through TerminalContent's constructor. */
  updateProgramTitle(programTitle: string): void {
    if (this._disposed) return
    this.updateAgentStatus(programTitle)
  }

  /** Called when the content (terminal session) wants the pane closed. */
  onCloseRequested?: () => void

  // ── Lifecycle ─────────────────────────────────────────────────────────

  /** Mount the content into this pane. Called by PaneManager on first
   *  activation, and suppressed after that: mount-once is enforced here at
   *  the seam, not by a private flag inside one implementation (nocx-njrx.2).
   *  The pane is already visible by now — the content received it through
   *  setTarget() in the constructor — so mount and the first viewport
   *  delivery both measure a laid-out element. */
  async start(): Promise<void> {
    if (this._mountStarted) return
    this._mountStarted = true
    log.info('nocx: Pane.start() called', { id: this.id })
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
    // The pane is still opening: its shell's banner, its rc file and its
    // first prompt are not unread output (PaneHost.contentSettled).
    if (!this._settled) return
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

  private updateAgentStatus(programTitle: string): void {
    if (this._disposed) return
    const next = detectAgentStatus(programTitle)
    if (next === this._agentStatus) return
    this._agentStatus = next
    if (next === 'idle' && !this._active) {
      this.markActivity()
    }
  }

  // ── B.5 geometry authority ──────────────────────────────────────────

  /**
   * Start observing the pane element for resize. Called when the pane enters
   * the DOM (PaneManager.addPane). Delivery is synchronous — the browser already
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
      // Never send a misleading zero rectangle for hidden/inactive panes (B.5).
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
// PaneManager — ordered pane model, activation rules, MRU
// ═══════════════════════════════════════════════════════════════════════════

/**
 * Where a dragged row lands, once it has been lifted out of the list.
 *
 * The two arguments are indices in the list BEFORE the removal, and `before`
 * is the half of the target the pointer was over. The −1 is the removal: with
 * the dragged row gone, everything after it has shifted up by one, so a drop
 * below its old place is one slot nearer than it looks.
 *
 * The `before` half is what makes the end of a list reachable at all. Every
 * drop used to mean "in front of the row I hit", so the last slot could only
 * be taken by a row that was already in it, and the bottom row could not be
 * dragged anywhere: the only targets below it were itself.
 */
function insertionIndex(from: number, to: number, before: boolean): number {
  const at = before ? to : to + 1
  return from < at ? at - 1 : at
}

export class PaneManager {
  private readonly panes: Pane[] = []
  private nextPaneId = 1
  private activePane: Pane | null = null
  private readonly panesContainer: HTMLElement
  private readonly client: WSClient
  private readonly clipboard: ClipboardAccess
  private readonly gate: ClipboardGate
  private readonly banner: ClipboardBanner
  private readonly profileClient: ProfileClient
  private _initialPaneReady: Promise<void> | undefined
  private tabStrip: TabStrip
  /** The chain, as the backend last answered. A cache, never an authority. */
  /** The panes this window is NOT showing: what the chain held at boot when
   *  the person asked for a clean start (nocx-yejir).
   *
   *  A SET of ids rather than a flag, and the difference is the whole
   *  correctness of it. A flag would have to be off for the boot and on
   *  again afterwards, and the first re-render after that — the one the
   *  clean start's own new tab causes — would put every stored pane on
   *  screen, which is the opposite of what was asked for. Naming the rows
   *  says exactly what is meant: these were left over, and a row created
   *  afterwards is not one of them and appears like any other.
   *
   *  Empty when restore is on, which is the normal case. */
  private readonly notShown = new Set<string>()
  private readonly layout: LayoutStore
  /**
   * WHICH TAB WAS IN FRONT — read once at boot, written on every activation.
   *
   * It is not in the chain and must not be: the chain says which tabs exist,
   * while "the one I was looking at" is a fact about a VIEWPORT, exactly like
   * `viewedWorkspaceId` above it. It is not in localStorage either — the
   * renderer holding a fact of its own is the arrangement the UI-state
   * document exists to end (ADR-0033).
   */
  private readonly uiState: UIStateClient
  /**
   * Whether the layout store answered at all.
   *
   * False is a real product state: the content store is encrypted and can
   * fail to open, and the transport then refuses every layout method. The
   * degrade is visible — a toast says tabs are not being remembered — because
   * a silent one is a feature that does not exist surviving a release. In
   * that state the renderer opens panes with ids of its own and asks for
   * nothing, which is what it did before this bead.
   */
  private layoutAvailable = false
  /** Panes the chain is KNOWN to hold, which is only ever true once the
   *  backend has answered. It is what tells "this row went away" apart from
   *  "this create has not landed yet" and from a view pane that was never in
   *  the chain at all. */
  private readonly registered = new Set<string>()
  /**
   * THE SAVED CONNECTIONS, read once before the chain is drawn.
   *
   * A CACHE and never an authority: it answers exactly one question — which
   * saved connection a stored endpoint names — so that a restored ssh pane
   * reconnects the way it was opened, with that connection's port, auth
   * bindings and jump host, rather than as a bare host the far end has to
   * guess the rest of.
   *
   * It is read BEFORE the layout, and that ordering is not an optimisation.
   * Adoption is synchronous — a row becomes a pane in the turn the store
   * hands it over — and boot then chooses which tab is in front from the
   * panes that exist. A lookup awaited inside adoption would make an ssh
   * pane appear after that choice, so the tab a person left in front would
   * never be the one they came back to.
   *
   * Empty is a legitimate answer and not a failure: a pane opened on a plain
   * host or an ssh-config alias never had a profile, and reopens through
   * ~/.ssh/config exactly as it was opened.
   */
  private savedProfiles: SSHProfile[] = []
  /** The endpoints this turn has yet to report as not reconnected, and
   *  whether the report is already scheduled. One statement per turn, not
   *  one per pane: four stored connections behind a host that is not
   *  answering used to mean four toasts. */
  private readonly notReconnected = new Set<string>()
  private notReconnectedScheduled = false
  /** Whether a row the chain hands over becomes a pane. False only while a
   *  CLEAN START reads the chain (readLayoutWithoutAdopting): the rows are
   *  learnt, and none of them is opened. */
  private adopting = true
  private readonly bar: HTMLElement
  private readonly verticalHost: HTMLElement
  /** MRU stack: most-recently-activated pane ids. */
  private readonly recentPaneIds: number[] = []
  /**
   * WHICH WORKSPACE THIS WINDOW IS SHOWING, and why it is here rather than in
   * the chain (tabs/panes design §10).
   *
   * A window is a VIEWPORT, not a container: it shows one workspace at a time
   * and owns no tabs. So "which one is in front" is a fact about a viewport,
   * exactly like the MRU stack above it — two windows on one profile have two
   * answers, and a stored one would be a fact with two owners the moment
   * multi-window lands (nocx-mgbjx). §4.5's stored list agrees by omission.
   *
   * Null means "whatever the default is", which is also the fallback whenever
   * this names a workspace that has since gone. It is set by switching, and
   * by activating a pane that belongs to a different workspace — a Cmd-W that
   * lands on the MRU pane elsewhere moves the window with it, or the person
   * would be typing into a tab the strip is not showing.
   */
  private viewedWorkspaceId: string | null = null
  /** Called when an SSH connection fails because the vault is sealed. */
  onVaultSealed?: () => void
  /** Called when an SSH connection fails because the host key is unknown
   *  or changed. Resolves true only after explicit trust; the content then
   *  retries the same open. */
  onHostKeyError?: (evidence: HostKeyErrorEvidence, signal: AbortSignal) => Promise<boolean>
  /** The strip's "show all workspaces" button was pressed. Wired by main.tsx
   *  to the overview controller's `open` — the surface's lifetime belongs to
   *  the composition root, and a PaneManager that owned an overlay would be
   *  holding a second thing that decides what is on screen. */
  onOpenOverview?: () => void
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
  /** The composer's model chip, clicked on the model — opens Settings →
   *  Roles, where the model that answers is chosen (nocx-rikz5). Relayed
   *  beside onCreateEndpoint, which the chip's other destination reuses. */
  onOpenRoles?: () => void
  /** Called when the user performs a UI action that should reset the
   *  vault idle timer. Wired by main.tsx to vaultClient.activity(). */
  onActivity?: () => void
  /** Called when the active pane changes — the seam for chrome that must
   *  re-scope to the tab in front. The sidebar's ports view follows the
   *  active pane through this (nocx-wzc4.7); wired by main.tsx to a Solid
   *  signal. */
  onActivePaneChange?: () => void
  /** The snippet palette chord (⌥⌘P) was pressed in the active pane —
   *  forwarded from the pane's TerminalContent, whose xterm boundary and
   *  editor arbiter both land here. The composition root opens the
   *  palette (design §10.1). */
  onSnippetChord?: () => void
  /** The snippet library the completion provider in every pane reads, and
   *  the acceptance it delegates (design §10.2). Set once by the
   *  composition root; handed to each TerminalContent as it is built. */
  snippets?: SnippetProviderDeps
  onSnippetAccepted?: (snippetId: string) => void

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
    layout: LayoutStore,
    uiState: UIStateClient,
  ) {
    this.panesContainer = panes
    this.client = client
    this.clipboard = clipboard
    this.gate = gate
    this.banner = banner
    this.profileClient = profileClient
    this.tabStrip = tabStrip
    this.bar = bar
    this.verticalHost = verticalHost
    this.layout = layout
    this.uiState = uiState

    // Wire TabStrip intents.
    this.wireStrip(tabStrip)

    // ONE trigger for redrawing the strip: the cache changed. Every path that
    // asks the backend for something ends here rather than each one also
    // remembering to re-render — which is how two of them end up disagreeing
    // about what "after a close" looks like.
    this.layout.onChange(() => this.renderFromLayout())

    window.addEventListener('keydown', this.onKeydown, true)
  }

  /** Return the mount host for the given strip based on orientation. */
  private hostFor(strip: TabStrip): HTMLElement {
    return strip.orientation === 'vertical' ? this.verticalHost : this.bar
  }

  get paneCount(): number {
    return this.panes.length
  }

  get initialPaneReady(): Promise<void> {
    if (!this._initialPaneReady) {
      throw new Error('initialPaneReady accessed before openInitialPane')
    }
    return this._initialPaneReady
  }

  /** Mount the tab strip and open the initial terminal pane.
   *
   *  Callable exactly once, and that is enforced here rather than documented:
   *  a second call would mount the strip again and open a second "initial"
   *  pane. This epic has already removed one contract that held by coincidence
   *  (mount-once, which lived in a private flag inside one PaneContent
   *  implementation instead of at the seam), so a comment is not enough.
   *
   *  `initialPaneReady` resolves only from terminal content — a non-terminal
   *  first tab must not be able to report the app healthy. */
  openInitialPane(): Promise<void> {
    if (this._initialPaneReady) {
      throw new Error('openInitialPane called twice; the composition root calls it exactly once')
    }
    // The promise is assigned SYNCHRONOUSLY even though the work is not: the
    // composition root reads `initialPaneReady` in the same turn it calls
    // this, and that contract predates the read this now waits on.
    this._initialPaneReady = this.boot()
    return this._initialPaneReady
  }

  /**
   * Mount the strip, read the layout, and put on screen whatever the backend
   * says is there — or open one pane when it says nothing is.
   *
   * THE READ COMES FIRST, and that ordering is the bead: a renderer that
   * opened a pane and then asked would have decided what the window looks
   * like before finding out. Reloading with the backend still up therefore
   * brings back the tabs with their colours, names, order and pinning,
   * because none of it was ever here.
   *
   * What does NOT come back is the shell: a session dies with the backend
   * (D5) and a restored local pane starts a fresh one in its place. An ssh
   * pane makes a NEW CONNECTION to the endpoint it applies at, which is not
   * a resurrection either — nothing of the old session survives it.
   */
  private async boot(): Promise<void> {
    this.tabStrip.mount(this.hostFor(this.tabStrip))
    // A CLEAN START, when the person asked for one (nocx-yejir). The chain is
    // still READ — a tab opened in this session must be recorded, or the
    // commands run in it have no pane to anchor their blocks to — and any row
    // that arrives on it does not become a pane.
    //
    // THE LEFTOVERS WERE ALREADY MARKED CLOSED, by the backend, before this
    // renderer could ask for anything (app.clearWindowOnCleanStart,
    // nocx-l21ib.4): a backend start IS an application start, so the decision
    // belongs there, in one transaction, rather than as a close per tab from
    // here. So on the ordinary clean start this read returns nothing, and
    // what the skip below still covers is the case the sweep cannot see — a
    // renderer reloading against a backend that is already up, whose chain
    // holds the tabs THIS session opened.
    //
    // Marked, not deleted: the rows stay, with every block still anchored to
    // the pane that printed it. Deleting the chain would null those anchors
    // (entries.pane_id ON DELETE SET NULL), a quieter and much larger loss
    // than the one that was asked for.
    if (!restoreOnStartup()) {
      await this.readLayoutWithoutAdopting()
      const fresh = this.newPane()
      await this.activate(fresh)
      const content = fresh.content
      if (!(content instanceof TerminalContent)) throw new Error('initial pane is not a terminal')
      if (!(await content.ready)) throw new Error('initial pane failed to start')
      return
    }
    // THE SAVED CONNECTIONS BEFORE THE CHAIN, because adoption reads them
    // and adoption happens inside the read (see savedProfiles).
    await this.primeProfiles()
    await this.readLayout()
    // readLayout's change notification has already adopted whatever the
    // backend holds; an empty chain means a first pane to open.
    // ONE ACTIVATION, DECIDED HERE. Adoption puts every stored row on screen
    // and activates none of them (`adopt`), so the window's answer to "which
    // tab is in front" is made once rather than falling out of whichever row
    // the chain handed over last.
    //
    // The answer is the tab the person LEFT, which the UI-state document
    // holds (nocx-mqie.4): the mirror was filled by the composition root's
    // `load()` before this manager was built, so it is readable here without
    // a second round trip and without boot waiting on one.
    //
    // A remembered id matching no pane falls through to the first one. That
    // is not a defect to fix later: the tab SET is not restored yet
    // (nocx-l21ib), so a pane a person had in front can legitimately be gone
    // by the next launch, and the window still has to open on something.
    const remembered = this.uiState.state.activeTab
    const front = this.panes.find((p) => p.wireId === remembered) ?? this.panes[0] ?? this.newPane()
    await this.activate(front)
    const content = front.content
    if (!(content instanceof TerminalContent)) {
      // `initialPaneReady` is what reports the app healthy, so it may only
      // ever resolve from terminal content.
      throw new Error('initial pane is not a terminal')
    }
    // A RESTORED CONNECTION IS NOT WHAT "the application started" MEANS, and
    // the window must not wait on one. Its session is a round trip to another
    // machine: it can be refused by a host that is down, or take a minute to
    // say so, and either would be read here as the application having failed
    // to start — `initialPaneReady` is what reports the app healthy. What did
    // or did not happen to the connection is the pane's own to report
    // (watchReconnect), and it reports it whether anybody was in front of it
    // or not.
    if (this.layout.panes().find((row) => row.id === front.wireId)?.kind === 'ssh') return
    const ok = await content.ready
    if (!ok) throw new Error('initial pane failed to start')
  }

  /**
   * Read the chain for a CLEAN START: learn what is there, show none of it.
   *
   * The read still happens, because the chain is where a tab opened in this
   * session is recorded — without it this session's commands would anchor
   * their blocks to nothing — and because the store learns the default
   * workspace a new tab belongs in. What the setting removes is the window
   * opening on what was left, and that is done by naming those rows rather
   * than by not reading them.
   *
   * On a cold start the chain is empty by the time this runs: the backend
   * marked the leftovers closed and no window read returns a closed row.
   * What is left for this to name is the RELOAD — the same backend, still
   * holding the rows this session opened, which no sweep at startup could
   * have seen.
   */
  private async readLayoutWithoutAdopting(): Promise<void> {
    // ADOPTION IS SUPPRESSED FOR THE LENGTH OF THE READ, rather than undone
    // after it. The rows arrive on the read's own change notification, so a
    // manager that adopted them and dropped the chrome afterwards would have
    // opened every one of them for the length of a turn — and now that an
    // ssh row reconnects on adoption (nocx-9y4ku), that turn is a connection
    // to every stored host, and a vault unlock behind it, on the one startup
    // that was asked to open nothing.
    this.adopting = false
    try {
      await this.readLayout()
    } finally {
      this.adopting = true
    }
    // Named AFTER the read, because that is when the rows are known. From
    // here on the ordinary skip in renderFromLayout keeps them off screen.
    for (const row of this.layout.panes()) this.notShown.add(row.id)
  }

  /**
   * Read the saved connections, once, for the reconnects a restore is about
   * to make (see savedProfiles).
   *
   * FAIL-QUIET, and the degrade is real rather than nominal: with no
   * profiles in hand a stored endpoint is still reopened, through
   * ~/.ssh/config and the host it names, which is how a pane opened on a
   * bare host or an alias was opened in the first place. What is lost is a
   * saved connection's port and auth bindings, and the connection that then
   * fails says so through the same path any failed reconnect does.
   */
  private async primeProfiles(): Promise<void> {
    try {
      this.savedProfiles = await this.profileClient.listProfiles()
    } catch (err) {
      this.savedProfiles = []
      log.warn('nocx: the saved connections could not be read before a restore', {
        error: err instanceof Error ? err.message : String(err),
      })
    }
  }

  /** Read the chain, and say so in the product when it cannot be read. */
  private async readLayout(): Promise<void> {
    // Set BEFORE the read, because the read's own change notification is
    // what puts the restored panes on screen: a flag set afterwards would
    // make the first — and most important — redraw the one that is skipped.
    this.layoutAvailable = true
    try {
      await this.layout.load()
    } catch (err) {
      this.layoutAvailable = false
      const message = err instanceof Error ? err.message : String(err)
      log.warn('nocx: the layout store is unavailable', { error: message })
      showToast({
        level: 'warning',
        message: 'Tabs are not being remembered — the layout store is unavailable',
      })
    }
  }
  // ── Tab creation ──────────────────────────────────────────────────────

  /**
   * The pane's one identity, and the row that goes with it.
   *
   * The id is minted HERE and the object is created THERE, which is §7's
   * split: a pane id is durable, so it cannot come from a backend instance,
   * and the row is the backend's to write. The chrome is built in the same
   * turn the user pressed the key; a refusal arrives on `created`, and a pane
   * the backend refused must not stay on screen.
   *
   * With no layout store the id is still a UUIDv7 and still one identity for
   * both history.record and secrets.paneClosed — it is simply not stored.
   *
   * THE READINESS COMES BACK WITH THE ID because the session may not name a
   * pane before its row exists: `open` refuses a paneId it cannot resolve
   * (-32602, nocx-isoph.2) and that refusal is deliberate, so a session
   * racing its own pane row would be refused outright and leave a tab that
   * never appears (nocx-rtg0.29). Returning the id alone is what made that
   * race impossible to close at the call site.
   */
  private mintPane(kind: 'local' | 'ssh', endpoint: string | null): PaneIdentity {
    // No store is a DEGRADE, not a refusal: the id is minted and simply
    // names no row, which `registered: false` states rather than leaving the
    // caller to infer it from a promise that never settles.
    if (!this.layoutAvailable) return { paneId: uuidv7(), registered: Promise.resolve(false) }
    // Into the workspace the window is SHOWING, not into the default: the
    // strip draws one workspace's tabs, so a tab that opened somewhere else
    // would either vanish on arrival or drag the window away from where the
    // person was working. The default is where it goes when that is where
    // they are.
    const opened = this.layout.openTab({ kind, endpoint, cwd: '' }, this.currentWorkspaceId())
    // ONE handler with both arms, not a .then() and a .catch(): two handlers
    // on the same promise leave the first one's rejection unhandled, which
    // surfaces as a process-level unhandled rejection rather than as the
    // toast below.
    //
    // Its BOOLEAN is the pane's readiness, so the two answers the session
    // needs — "there is a row" and "there is not, for either reason" — come
    // off the one handler that already knew them.
    const registered = opened.created.then(
      () => {
        this.registered.add(opened.paneId)
        return true
      },
      (err: unknown) => {
        const message = err instanceof Error ? err.message : String(err)
        log.error('nocx: the backend refused a new tab', { error: message })
        showToast({ level: 'danger', message: `Could not open a tab: ${message}` })
        const orphan = this.panes.find((p) => p.wireId === opened.paneId)
        if (orphan) this.dropChrome(orphan)
        return false
      },
    )
    return { paneId: opened.paneId, registered }
  }

  /** Create a new local terminal pane and activate it: mint the identity,
   *  ask the backend for the tab, and put the chrome up in the same turn. */
  newPane(): Pane {
    return this.buildLocalPane(this.mintPane('local', null))
  }

  /** The chrome and content of a LOCAL terminal pane with a given identity —
   *  one implementation for a pane the user just asked for and a pane the
   *  chain already holds, because "a local pane" must not mean two things. */
  private buildLocalPane(identity: PaneIdentity, activateNow = true): Pane {
    const paneRef = { current: undefined as Pane | undefined }
    const content = new TerminalContent(
      this.client,
      identity,
      this.clipboard,
      this.gate,
      this.banner,
      this.profileClient,
      (tooltip) => paneRef.current?.updateTooltip(tooltip),
      // The alt-screen callback that used to sit here is gone with the
      // parameter. It toggled `#app.alt-screen`, which emptied the tab strip so
      // a viewport-sized fullscreen xterm would not paint through it; the
      // fullscreen region lives inside its pane now (nocx-6w4z).
      undefined,
      {
        onSubtitleChange: (subtitle) => paneRef.current?.updateSubtitle(subtitle),
        onWarningChange: (warning, label) => paneRef.current?.setWarningState(warning, label),
        onPortsTargetChange: () => this.onActivePaneChange?.(),
        onActiveOriginChange: () => this.onActivePaneChange?.(),
        onSetupVault: this.onSetupVault,
        onCreateSecret: this.onCreateSecret,
        onSnippetChord: this.onSnippetChord,
        snippets: this.snippets,
        onSnippetAccepted: this.onSnippetAccepted,
        onCreateEndpoint: this.onCreateEndpoint,
        onOpenRoles: this.onOpenRoles,
        onProgramTitleChange: (programTitle) => paneRef.current?.updateProgramTitle(programTitle),
        // Where the pane IS, recorded so a restart reopens it there
        // (nocx-zkiv4). Fire-and-forget and fail-quiet: a directory the
        // chain did not take costs the NEXT restore its cwd — it falls back
        // to the one the pane was created in — and never costs the pane the
        // person is working in now.
        onPaneCwdChange: (cwd) => {
          if (!this.layoutAvailable) return
          void this.layout.setPaneCwd(identity.paneId, cwd).catch((err: unknown) => {
            log.warn('nocx: the pane cwd was not recorded', {
              error: err instanceof Error ? err.message : String(err),
            })
          })
        },
      },
    )
    const descriptor: ContentDescriptor = {
      surfaceType: SURFACE_TERMINAL,
      singletonKey: null,
      restoreDescriptor: { type: 'local' },
      supportsAttention: true,
      // No placeholder. A terminal pane is named after where it is, and that
      // arrives one WebSocket round-trip after the pane appears; printing
      // 'Terminal' in the meantime showed a word that is never the answer and
      // then replaced it, which reads as a flicker rather than as loading
      // (nocx-83a). An empty title is honest and the strip's width is fixed, so
      // nothing moves when the real one lands.
      defaultTitle: '',
    }
    const pane = this.addPane(content, descriptor, identity.paneId, activateNow)
    paneRef.current = pane
    return pane
  }

  /** Open a connection the user just asked for: mint the identity, ask the
   *  backend for the tab, and put the chrome up in the same turn. */
  newSSHPane(profileId: string, host: string, user?: string, port?: number, title?: string): Pane {
    log.info('nocx: newSSHPane called', { profileId, host, user, port, title })
    // The endpoint is the canonical user@host:port the pane applies at,
    // which is what §5 stores on an ssh pane — and what a restore reconnects
    // through, since the chain has no column for the profile a pane was
    // opened from (see reopenSSH).
    const identity = this.mintPane('ssh', endpointOf(host, user, port))
    return this.buildSSHPane(identity, { profileId, host, user, port }, title || host)
  }

  /** The chrome and content of an SSH pane with a given identity — one
   *  implementation for a connection the user just opened and one the chain
   *  already holds, because "an ssh pane" must not mean two things any more
   *  than "a local pane" may (see buildLocalPane). */
  private buildSSHPane(
    identity: PaneIdentity,
    sshOpts: { profileId: string; host: string; user?: string; port?: number },
    title: string,
    activateNow = true,
  ): Pane {
    const { host, user, port } = sshOpts
    const paneRef = { current: undefined as Pane | undefined }
    const content = new TerminalContent(
      this.client,
      identity,
      this.clipboard,
      this.gate,
      this.banner,
      this.profileClient,
      (tooltip) => paneRef.current?.updateTooltip(tooltip),
      sshOpts,
      {
        onSubtitleChange: (subtitle) => paneRef.current?.updateSubtitle(subtitle),
        onAdoptabilityChange: (adoptable: boolean) => {
          const pane = paneRef.current
          if (!pane) return
          if (adoptable) {
            pane.setAdoptState(true, () => this._adoptAlias(host, user, port, pane))
          } else {
            pane.setAdoptState(false, () => {})
          }
        },
        onWarningChange: (warning, label) => paneRef.current?.setWarningState(warning, label),
        onProgramTitleChange: (programTitle) => paneRef.current?.updateProgramTitle(programTitle),
        onActiveOriginChange: () => this.onActivePaneChange?.(),
        onPortsTargetChange: () => this.onActivePaneChange?.(),
        onVaultSealed: this.onVaultSealed,
        onHostKeyError: this.onHostKeyError,
        onSetupVault: this.onSetupVault,
        onCreateSecret: this.onCreateSecret,
        onSnippetChord: this.onSnippetChord,
        snippets: this.snippets,
        onSnippetAccepted: this.onSnippetAccepted,
        onCreateEndpoint: this.onCreateEndpoint,
        onOpenRoles: this.onOpenRoles,
      },
    )
    const descriptor: ContentDescriptor = {
      surfaceType: SURFACE_TERMINAL,
      singletonKey: null,
      restoreDescriptor: { type: 'ssh', profileId: sshOpts.profileId, host, user },
      supportsAttention: true,
      defaultTitle: title,
    }
    const pane = this.addPane(content, descriptor, identity.paneId, activateNow)
    paneRef.current = pane
    return pane
  }

  /** Adopt an SSH alias as a saved nocx profile. Creates the profile and switches
   *  the tab to track the saved profile. */
  private _adoptAlias(
    host: string,
    user: string | undefined,
    port: number | undefined,
    tab: Pane,
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
        this.newSSHPane(saved.id, host, user)
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
  openPane(content: PaneContent, descriptor: ContentDescriptor): Pane {
    if (descriptor.singletonKey) {
      const existing = this.panes.find((t) => t.descriptor.singletonKey === descriptor.singletonKey)
      if (existing) {
        void this.activate(existing)
        return existing
      }
    }
    // Every pane gets a wire identity — a view pane carries no captures, but
    // the chrome must still be able to announce a close (nocx-tsajw). It is
    // NOT registered in the layout chain: Settings and the file viewer are
    // surfaces the window shows, not durable panes with a cwd and a pipe, and
    // storing one would put a row in the chain that no restore could reopen.
    const pane = this.addPane(content, descriptor, uuidv7())
    // AND THE STRIP IS RE-SORTED, because nothing else will. Every other
    // path into the strip's order arrives through the layout store's change
    // notification, and opening a surface changes no layout — so the new row
    // simply stayed where it was appended, which is the end of the rail,
    // under the last workspace's heading. syncStripOrder is where "ungrouped
    // rows open the strip" is decided; this is the one caller that has to ask
    // for it by hand.
    if (this.layoutAvailable) this.syncStripOrder()
    return pane
  }

  /**
   * Internal: create a Tab, wire lifecycle, add to model, and — unless the
   * caller says otherwise — bring it to the front.
   *
   * STARTING AND ACTIVATING ARE TWO THINGS, and they were one. `activate` is
   * the only caller of `Pane.start`, so making a pane also meant putting it
   * in front; a restore, which makes one pane per stored row, therefore
   * activated all of them in turn and left the window on whichever row came
   * last — the last workspace's tab, never the one the person was in. Every
   * pane also went through the MRU stack and through active on its way past.
   *
   * A pane that is not activated is started here instead. It mounts hidden,
   * which is the state every background tab is in anyway (`visibility:
   * hidden` keeps the box measurable, so the renderer still has dimensions),
   * and its shell runs — an agent in a workspace nobody is looking at has to
   * keep working.
   */
  private addPane(
    content: PaneContent,
    descriptor: ContentDescriptor,
    wireId: string,
    activateNow = true,
  ): Pane {
    const pane = new Pane(content, descriptor, this.nextPaneId++, wireId)

    this.panes.push(pane)
    this.panesContainer.append(pane.pane)
    // B.5: start observing pane geometry once it's in the DOM.
    pane.setupViewportObserver()

    pane.onCloseRequested = () => void this.closePane(pane)
    this.tabStrip.addPane(pane)
    if (activateNow) {
      void this.activate(pane)
    } else {
      void pane.start()
    }
    return pane
  }

  /** Swap the TabStrip at runtime without restarting.  Transfers all
   *  existing tabs to the new strip, wires intents, and preserves the
   *  active-tab state.  The old strip's DOM is removed. */
  replaceStrip(newStrip: TabStrip): void {
    // Detach the old strip: clear intents so late callbacks are no-ops.
    const old = this.tabStrip
    old.onActivate = null
    old.onClose = null
    old.onNewPane = null
    old.onReorder = null
    old.onRename = null
    old.onRecolour = null
    old.onPin = null
    old.onSwitchWorkspace = null
    old.onNewWorkspace = null
    old.onOpenOverview = null
    old.workspaceMenuRows = null
    old.onCloseWorkspace = null

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
    while (oldHost.firstChild) {
      oldHost.removeChild(oldHost.firstChild)
    }
    oldHost.classList.remove('tabstrip-vertical')
    oldHost.removeAttribute('role')
    oldHost.removeAttribute('aria-label')
    oldHost.removeAttribute('aria-orientation')

    // Mount the new strip on the correct host.
    newStrip.mount(newHost)

    // Transfer every existing pane into the new strip.
    for (const pane of this.panes) {
      newStrip.addPane(pane)
    }

    // Wire new strip intents.
    this.wireStrip(newStrip)

    // Restore active-pane state.
    if (this.activePane) {
      newStrip.setActive(this.activePane.id)
    }

    this.tabStrip = newStrip
    // The two strips draw DIFFERENT SETS: the vertical one shows every
    // workspace as a tree, the horizontal one the current workspace behind a
    // chip. So the new strip is given the chain's answer for its own
    // orientation rather than inheriting the old one's rows — which is also
    // what puts the headings and the chip up, since neither transferred with
    // the panes.
    if (this.layoutAvailable) this.syncStripOrder()
  }

  private wireStrip(strip: TabStrip): void {
    strip.onActivate = (id) => {
      const pane = this.panes.find((t) => t.id === id)
      if (pane) void this.activate(pane)
    }
    strip.onClose = (id) => {
      const pane = this.panes.find((t) => t.id === id)
      if (pane) void this.closePane(pane)
    }
    strip.onNewPane = () => this.newPane()
    strip.onReorder = (fromId, toId, before) => this.reorderPane(fromId, toId, before)
    strip.onRename = (id) => void this.renameTab(id)
    strip.onRecolour = (id, colour) => void this.recolourTab(id, colour)
    strip.onPin = (id, pinned) => void this.pinTab(id, pinned)
    strip.onSwitchWorkspace = (workspaceId) => this.switchWorkspace(workspaceId)
    strip.onNewWorkspace = () => void this.newWorkspace()
    // Set by the composition root, which owns the overview's lifetime — the
    // PaneManager supplies the port and never holds the surface (nocx-edhcu).
    strip.onOpenOverview = () => this.onOpenOverview?.()
    strip.workspaceMenuRows = (workspaceId) => this.workspaceMenuRows(workspaceId)
    // The heading's close mark and the menu's "Close workspace" row are one
    // action with one owner — neither the strip nor the menu decides what
    // happens to the panes inside.
    strip.onCloseWorkspace = (workspaceId) => void this.closeWorkspaceById(workspaceId)
    strip.onMoveWorkspace = (movedId, targetId, before) =>
      void this.moveWorkspaceBeside(movedId, targetId, before)
  }

  // ── decoration: asked for here, decided by the backend ────────────────

  /**
   * Rename the tab a pane is in, or clear the name.
   *
   * Cancelling and clearing are DIFFERENT answers and the prompt keeps them
   * apart: null is "I changed my mind" and an empty string is "take the name
   * off", which puts the tab back to the label its panes give it (§4.5) — a
   * real product state and the normal one.
   */
  private async renameTab(paneId: number): Promise<void> {
    const tab = this.tabFor(paneId)
    if (!tab) return
    const draft = await showTabEditDialog(tab.name ?? '', tab.colour ?? null)
    if (draft === null) return
    const name = draft.name === '' ? null : draft.name
    // TWO FACTS, TWO CALLS, AND ONLY WHAT CHANGED. The wire has a method per
    // fact (§4.5) and this dialog can answer both at once, so the alternative
    // was one call that always sends both — which would write a colour every
    // time somebody edited a name, and make every rename a recolour in the
    // store's history.
    if (name !== (tab.name ?? null)) {
      await this.ask(() => this.layout.rename(tab.id, name), 'Could not rename the tab')
    }
    if (draft.colour !== (tab.colour ?? null)) {
      await this.ask(() => this.layout.recolour(tab.id, draft.colour), 'Could not colour the tab')
    }
  }

  /**
   * The rows a workspace's own menu offers (nocx-isoph.7).
   *
   * THE ROWS ARE BUILT ONCE, in workspace-menu.ts, and this method only says
   * what set they are about. The chip's switcher appends the same rows for the
   * workspace in front of it, so the two placements cannot come to disagree
   * about what a workspace may do — which they would first do over the rule
   * that the default offers nothing at all.
   *
   * The set is the BACKEND's list, never `workspaceRows()`: a reorder must be
   * a permutation of what the store holds, and the display list can carry a
   * synthesised default row the store has not written yet.
   */
  private workspaceMenuRows(workspaceId: string): WorkspaceMenuRow[] {
    if (!this.layoutAvailable) return []
    return workspaceActionRows(
      workspaceId,
      {
        // THE DEFAULT IS NOT IN THE SET, because it is not in the order: the
        // wire takes a permutation of the user-made workspaces and the
        // default keeps position 0 (content.ReorderWorkspaces). Sending it
        // was refused by the transport — its id is the reserved
        // `workspace:default` rather than a UUIDv7 — so every move failed
        // with "ids must be a UUIDv7", which named the wrong half.
        ids: this.layout
          .workspaces()
          .map((w) => w.id)
          .filter((id) => id !== this.layout.defaultWorkspaceId()),
        defaultWorkspaceId: this.layout.defaultWorkspaceId(),
      },
      {
        onRename: (id) => void this.renameWorkspace(id),
        onReorder: (ids) => void this.reorderWorkspaces(ids),
        onClose: (id) => void this.closeWorkspaceById(id),
      },
    )
  }

  /**
   * Rename a workspace.
   *
   * UNLIKE A TAB, A WORKSPACE MAY NOT LOSE ITS NAME. A tab with no name falls
   * back to the label its panes give it (§4.5); a workspace has no such
   * fallback and the backend refuses a blank one, so an empty answer here is
   * a cancel rather than a clear. The prompt is seeded with the current name
   * because renaming is almost always an edit.
   */
  private async renameWorkspace(workspaceId: string): Promise<void> {
    const current = this.layout.workspaces().find((w) => w.id === workspaceId)
    if (!current) return
    const draft = await showWorkspaceEditDialog(current.name, current.colour ?? null)
    if (draft === null || draft.name === '') return
    if (draft.name !== current.name) {
      await this.ask(
        () => this.layout.renameWorkspace(workspaceId, draft.name),
        'Could not rename the workspace',
      )
    }
    if (draft.colour !== (current.colour ?? null)) {
      await this.ask(
        () => this.layout.recolourWorkspace(workspaceId, draft.colour),
        'Could not colour the workspace',
      )
    }
  }

  /**
   * Put one workspace beside another — the heading drag.
   *
   * The WHOLE order is computed here and sent whole, exactly as the menu's
   * Move up / Move down do: the wire takes a permutation and refuses anything
   * else, so "next to that one" has to become a list before it leaves. Both
   * gestures therefore end in the same call, and cannot come to disagree
   * about what the order is.
   *
   * The default is not in the set — it is not a member of the arrangement
   * (§4.2) and the store keeps it at position 0.
   */
  private async moveWorkspaceBeside(
    movedId: string,
    targetId: string,
    before: boolean,
  ): Promise<void> {
    const defaultId = this.layout.defaultWorkspaceId()
    const ids = this.layout
      .workspaces()
      .map((w) => w.id)
      .filter((id) => id !== defaultId)
    if (movedId === defaultId || targetId === defaultId) return
    const from = ids.indexOf(movedId)
    const to = ids.indexOf(targetId)
    if (from === -1 || to === -1) return
    const next = [...ids]
    next.splice(from, 1)
    next.splice(insertionIndex(from, to, before), 0, movedId)
    if (next.every((id, i) => id === ids[i])) return
    await this.reorderWorkspaces(next)
  }

  private async reorderWorkspaces(ids: readonly string[]): Promise<void> {
    await this.ask(() => this.layout.reorderWorkspaces(ids), 'Could not reorder the workspaces')
  }

  private async recolourTab(paneId: number, colour: string | null): Promise<void> {
    const tab = this.tabFor(paneId)
    if (!tab) return
    await this.ask(() => this.layout.recolour(tab.id, colour), 'Could not colour the tab')
  }

  private async pinTab(paneId: number, pinned: boolean): Promise<void> {
    const tab = this.tabFor(paneId)
    if (!tab) return
    await this.ask(() => this.layout.pin(tab.id, pinned), 'Could not pin the tab')
  }

  /** The layout row behind a strip entry, or undefined when there is none —
   *  a Settings pane, or any pane at all while the layout store is down. */
  private tabFor(paneId: number) {
    const pane = this.panes.find((p) => p.id === paneId)
    return pane ? this.layout.tabOf(pane.wireId) : undefined
  }

  /**
   * Run one layout call and report a refusal.
   *
   * Nothing is applied here on success: the store writes the answer into the
   * cache and the cache's change notification redraws. That is what keeps a
   * refused call from moving anything — there is no optimistic step to undo.
   */
  private async ask(call: () => Promise<void>, whatFailed: string): Promise<boolean> {
    try {
      await call()
      return true
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err)
      log.error('nocx: ' + whatFailed, { error: message })
      showToast({ level: 'danger', message: `${whatFailed}: ${message}` })
      return false
    }
  }

  /**
   * Close a pane. If it was the active pane, activates the MRU pane.
   * Closing the last pane opens a fresh terminal — view panes have no
   * restoreDescriptor and are never the automatic replacement.
   *
   * A pane with LIVE DESCENDANTS asks first (nocx-wtv3p, design D6): the
   * prompt names the tabs this one opened that are still running, and says
   * that closing leaves them running. It asks rather than decides because a
   * parent's end carries no information about whether its children's work is
   * still wanted — and it never offers to close them, because that would make
   * the parent's end decide theirs, which is the rule itself.
   *
   * Async, and synchronous where it matters: with no descendants nothing is
   * awaited, so the ordinary close still completes within the caller's turn.
   * The ask is the ONE gap, and the pane is re-checked across it — a tab can
   * be closed by something else while a modal is open.
   */
  async closePane(pane: Pane): Promise<void> {
    if (this.panes.indexOf(pane) === -1) return

    const descendants = this.liveDescendantsOf(pane)
    if (descendants.length > 0) {
      const proceed = await showConfirm(leftRunningMessage(descendants), 'Close tab', 'Cancel')
      if (!proceed) return
      // The world moved while the modal was open: this pane may already be
      // gone, and closing it twice would take a tab that has since been
      // recycled under the same index.
      if (this.panes.indexOf(pane) === -1) return
    }
    this.commitClosePane(pane)
  }

  // ── the workspace this window is showing (§4.3, §10) ─────────────────

  /**
   * The workspace whose tabs the horizontal strip is drawing.
   *
   * Derived, with one small piece of viewport state behind it: the workspace
   * last put in front, falling back to the default. It falls back for two
   * reasons and both are ordinary — nothing has been switched to yet, and the
   * workspace that was in front has just been closed. A window is never
   * showing a workspace that does not exist.
   */
  currentWorkspaceId(): string {
    const fallback = this.layout.defaultWorkspaceId()
    const viewed = this.viewedWorkspaceId
    if (viewed === null) return fallback
    return this.layout.workspaces().some((w) => w.id === viewed) ? viewed : fallback
  }

  /**
   * Go to another workspace: unfold it in the strip, and put the tab you were
   * last in back in front.
   *
   * ONE CLICK ON THE PILL IS THE WHOLE OF IT. This used to be reached only
   * through the chip's dropdown, and being a two-step act through a menu is
   * half of what the rework removed; the other half is that the strip now
   * keeps every workspace on screen, so what changes here is which run of
   * tabs is unfolded, never which rows exist.
   *
   * THE TAB IT LANDS ON IS THE MRU ONE, not the first. Coming back to a
   * workspace means coming back to what you were doing in it — landing on its
   * leftmost tab instead would make switching away and back a destructive
   * act for anyone with more than two tabs. The MRU stack is already here and
   * is already the answer this application gives to "which pane next" when
   * one closes, so this is the existing answer applied rather than a second
   * one invented (AD-8 as a habit).
   *
   * A workspace whose panes are all rows the renderer never drew (a restored
   * ssh pane, see adopt) is still switched to — its rows exist and the person
   * asked. Nothing is activated, which is the honest picture of it.
   */
  switchWorkspace(workspaceId: string): void {
    if (!this.layoutAvailable) return
    this.viewedWorkspaceId = workspaceId
    this.renderFromLayout()
    const active = this.activePane
    if (active && this.workspaceOf(active) === workspaceId) return
    const next = this.mruPaneOfWorkspace(workspaceId)
    if (next) void this.activate(next)
  }

  /** Which workspace a pane's tab is in, or null for a pane the chain does
   *  not hold at all — Settings, a file viewer, a create still in flight. */
  private workspaceOf(pane: Pane): string | null {
    return this.layout.tabOf(pane.wireId)?.workspaceId ?? null
  }

  /** The workspace's most recently active pane, falling back to the first one
   *  it holds. Read off `recentPaneIds`, newest last, so a workspace never
   *  visited answers with its leading tab and one you have worked in answers
   *  with where you left off. */
  private mruPaneOfWorkspace(workspaceId: string): Pane | undefined {
    const members = this.panes.filter((p) => this.workspaceOf(p) === workspaceId)
    for (let i = this.recentPaneIds.length - 1; i >= 0; i--) {
      const pane = members.find((p) => p.id === this.recentPaneIds[i])
      if (pane) return pane
    }
    return members[0]
  }

  /**
   * Create a workspace: ask for the name, mint it with its first tab, and
   * show it.
   *
   * THE DIALOG ASKS FOR A NAME AND A COLOUR (nocx-2mipw), and it is a form
   * rather than a one-line prompt because a workspace is not one thing —
   * building it as a form now is what makes the next field an addition
   * instead of a second rewrite.
   *
   * The name is SUGGESTED and still the person's: the field opens with
   * `Workspace N` selected, so Enter accepts it and a keystroke replaces it.
   * That amends §4.1's "asked for and never invented" — the amendment is
   * recorded at workspace-dialog.tsx rather than left as a silent divergence.
   * A blank name is still refused, here and by the backend.
   *
   * Cancelling is a different answer from an empty name and both mean nothing
   * is created, which is why the dialog resolves null rather than a string.
   */
  async newWorkspace(): Promise<void> {
    if (!this.layoutAvailable) {
      showToast({
        level: 'warning',
        message: 'Workspaces are unavailable — the layout store could not be read',
      })
      return
    }
    const draft = await showWorkspaceCreateDialog(
      this.layout.workspaces().length,
      // Only the colours workspaces actually HOLD. The default's absence is
      // not a colour and must not push the offer around.
      this.layout.workspaces().map((w) => w.colour),
    )
    if (draft === null) return

    const made = this.layout.createWorkspace(draft.name, draft.colour, {
      kind: 'local',
      endpoint: null,
      cwd: '',
    })
    // The window follows the workspace it just made, before the answer: the
    // chrome goes up in the same turn the person pressed the key, and a
    // refusal takes it down again — the same shape as mintPane's.
    this.viewedWorkspaceId = made.workspaceId
    // The same shape mintPane returns, for the same reason: this pane's
    // session must not name a row the backend has not written yet, and the
    // handler that already knows the answer is the one that reports it. The
    // paneRef is the idiom buildLocalPane and newSSHPane already use — the
    // handler must reach the chrome, and the chrome cannot be built until
    // the readiness it waits on exists.
    const paneRef = { current: undefined as Pane | undefined }
    const registered = made.created.then(
      () => {
        this.registered.add(made.paneId)
        return true
      },
      (err: unknown) => {
        const message = err instanceof Error ? err.message : String(err)
        log.error('nocx: the backend refused a workspace', { error: message })
        showToast({ level: 'danger', message: `Could not create the workspace: ${message}` })
        this.viewedWorkspaceId = null
        if (paneRef.current) this.dropChrome(paneRef.current)
        return false
      },
    )
    paneRef.current = this.buildLocalPane({ paneId: made.paneId, registered })
    await registered
  }

  /**
   * Close the workspace this window is showing.
   *
   * TWO CALLS, AND NEITHER IS OPTIONAL. `closeWorkspace` below asks the
   * person — naming what is live before anything dies — and tears down the
   * chrome and the sessions of the members that are on screen. Then
   * `workspaces.close` takes the CONTAINER on the backend, in one
   * transaction: the workspace, its tabs and their panes, including the rows
   * this renderer never drew. Closing only what is drawn would leave an ssh
   * row and the workspace holding it standing.
   *
   * The default workspace is never closable and the affordance does not
   * exist (§4.2), so this refuses without asking rather than asking and then
   * refusing.
   */
  /**
   * Close a workspace, named by id.
   *
   * IT TAKES THE ID RATHER THAN CLOSING "THE CURRENT ONE" (nocx-isoph.7). The
   * chip closes the workspace it is showing and a vertical heading closes the
   * one it heads — and a vertical strip shows several at once, so "current"
   * cannot be the parameter. Both reach this through the shared menu rows,
   * which supply the subject; a second close path would be a second place
   * holding the confirm, the membership walk and the default's refusal, and
   * those three are exactly what must not drift apart.
   *
   * Returns true only after the backend accepted the close. A confirmation
   * cancellation, default-workspace refusal or backend error is false so a
   * caller such as Mission Control can keep its surface open.
   */
  async closeWorkspaceById(id: string): Promise<boolean> {
    if (!this.layoutAvailable) return false
    if (id === this.layout.defaultWorkspaceId()) return false

    // Membership is resolved by the CACHE OF THE CHAIN (LayoutStore), and
    // mapped onto chrome here: a row with no pane on screen has nothing to
    // tear down and is still a member, which is what the wire call is for.
    const members = this.layout
      .panesOfWorkspace(id)
      .map((row) => this.panes.find((p) => p.wireId === row.id))
      .filter((pane): pane is Pane => pane !== undefined)

    const name = this.workspaceLabel(id) ?? ''
    if (!(await this.closeWorkspace(name, members))) return false
    return this.ask(
      () => this.layout.closeWorkspace(id).then(() => undefined),
      'Could not close the workspace',
    )
  }

  /**
   * Close a workspace: every tab it holds goes, and the question that
   * precedes them NAMES what is live among them (nocx-isoph.6, design §4.1
   * and D6). "Close 4 tabs?" is a number nobody can weigh; that one of them
   * is a running deploy and another an ssh session into production is what
   * the answer actually turns on.
   *
   * The SAME ask as `closePane`'s, deliberately — one confirm path for
   * closes, one place where the renderer stops and asks. What differs is the
   * sentence, because the rule differs: closing a tab LEAVES its descendants
   * running (D6), and closing a workspace takes its members with it.
   *
   * `members` are the workspace's tabs, resolved by the caller: membership is
   * a fact the BACKEND owns (§4.4 — the session registry is the lifecycle
   * authority) and the renderer's cache of it arrives with nocx-isoph.4. This
   * method owns the ask and the close, and deliberately not the lookup.
   *
   * Returns whether the person said yes — a caller that must also tell the
   * backend (`workspaces.close`) needs to know, and must never send it before
   * the answer.
   *
   * NOTHING IS TORN DOWN BEFORE THE ANSWER. The close begins after the await
   * and not one step of it before, so cancelling leaves every tab, pane and
   * live session exactly as it was.
   */
  async closeWorkspace(name: string, members: readonly Pane[]): Promise<boolean> {
    const open = members.filter((pane) => this.panes.indexOf(pane) !== -1)
    const proceed = await showConfirm(
      closingWorkspaceMessage(
        name,
        open.map((pane) => this.liveWorkOf(pane)),
      ),
      'Close workspace',
      'Cancel',
    )
    if (!proceed) return false
    for (const pane of open) {
      // The world moved while the modal was open: a member may already be
      // gone, and closing it twice would take a pane that has since been
      // recycled under the same index. Re-checked per member, not once for
      // the set — they close one at a time.
      if (this.panes.indexOf(pane) === -1) continue
      // commitClosePane, not closePane: the person has just been asked about
      // this whole set, and asking again per member — once for the workspace
      // and once for every tab in it that opened another — is how a prompt
      // that matters gets dismissed by reflex. The prohibition it enforces is
      // untouched: a descendant OUTSIDE this workspace is not a member and is
      // not closed here.
      this.commitClosePane(pane)
    }
    return true
  }

  /** What one member tab of a workspace is doing, for the sentence that names
   *  it. Composed here for the same reason the lineage node is: live-work.ts
   *  owns the naming, the content answers for itself, and only the pane layer
   *  knows what the strip calls the tab. A content with no such capability —
   *  Settings, a viewer — is a tab that closes and is running nothing. */
  private liveWorkOf(pane: Pane): WorkspaceMember {
    const work = pane.content.liveWork?.() ?? null
    return {
      label: pane.displayTitle,
      command: work?.command ?? null,
      host: work?.host ?? null,
    }
  }

  /**
   * The live tabs this pane opened, at any depth, as the BACKEND admitted the
   * edges (nocx-9hu9d). Composed here because lineage.ts owns the walk and
   * the pane layer owns the labels: a content knows its session, never what
   * the strip calls its tab.
   *
   * Provenance only (ADR-0020 §5). It is read to describe what a close leaves
   * behind and for nothing else — never to decide that one tab may act on
   * another, which the backend refuses in any case
   * (internal/transport/ws_lineage_prohibitions_test.go).
   */
  private liveDescendantsOf(pane: Pane): LineageNode[] {
    const nodes: LineageNode[] = []
    let root: string | null = null
    for (const p of this.panes) {
      const edge = p.content.lineage?.()
      if (!edge) continue
      nodes.push({ ...edge, label: p.displayTitle })
      if (p === pane) root = edge.sessionId
    }
    if (root === null) return []
    return liveDescendants(root, nodes)
  }

  /**
   * The close itself, once it is settled that it happens.
   *
   * TWO MESSAGES, AND THEY ARE DIFFERENT ACTS (nocx-isoph.4). The
   * notification tells the capture registry that a scope is over — it touches
   * no store and needs no answer. panes.close removes the pane from the
   * durable chain, in a transaction that can also take the tab it emptied,
   * that tab's workspace, and mint a replacement tab. The renderer learns
   * what is left by asking, which the store's close does for it: what used to
   * be "if that was the last pane, open a fresh one" is now the backend's
   * replacement, adopted like any other row.
   *
   * Synchronous in signature on purpose. A caller closing several panes — the
   * workspace close asks once about the whole set — must not have to await
   * one before teaching the next, and the chrome teardown is what the user
   * sees; the wire call catches up.
   */
  private commitClosePane(pane: Pane): void {
    const index = this.panes.indexOf(pane)
    if (index === -1) return

    // Sent before the DOM teardown — a dropped notification is covered by the
    // transport-disconnect trigger, which is the same destruction.
    this.client.notifyPaneClosed(pane.wireId)

    const wasActive = pane === this.activePane
    this.removeFromRecent(pane.id)

    pane.close()
    pane.pane.remove()
    this.tabStrip.removePane(pane.id)
    this.panes.splice(index, 1)

    if (this.layoutAvailable && this.layout.tabOf(pane.wireId)) {
      void this.ask(
        () => this.layout.closePane(pane.wireId).then(() => undefined),
        'Could not close the tab',
      )
    } else if (this.panes.length === 0) {
      // No chain to ask, so the old rule stands: the window is never empty.
      this.newPane()
      return
    }

    if (wasActive) {
      const mruPane = this.popRecent()
      if (mruPane) {
        void this.activate(mruPane)
      }
    }
  }

  /** Activate a pane: show its pane, mount content, focus. */
  async activate(pane: Pane): Promise<void> {
    log.info('nocx: PaneManager.activate() called', {
      paneId: pane.id,
      isActive: pane === this.activePane,
    })
    if (pane === this.activePane) {
      pane.focus()
      return
    }

    if (this.activePane) {
      this.pushRecent(this.activePane.id)
    }

    this.activePane?.setActive(false)
    this.activePane = pane
    pane.setActive(true)
    // The window follows the tab that comes to the front, when that tab is in
    // a workspace at all: a Cmd-W landing on the MRU pane in another
    // workspace has to bring the strip with it, or the keyboard is in a tab
    // the strip is not drawing. A view pane (Settings, a viewer) is in no
    // workspace and moves the window nowhere.
    const workspaceId = this.layout.tabOf(pane.wireId)?.workspaceId
    if (workspaceId !== undefined && workspaceId !== this.viewedWorkspaceId) {
      this.viewedWorkspaceId = workspaceId
      if (this.layoutAvailable) this.syncStripOrder()
    }

    this.removeFromRecent(pane.id)
    this.tabStrip.setActive(pane.id)
    // Remember it, so the next launch opens here. Recorded BEFORE the content
    // is started and awaited: what was in front is true from the moment the
    // tab comes forward, and a start that never finishes must not be able to
    // cost the person their position. Fire-and-forget with the rejection
    // swallowed — this is a fact the app writes without being asked, nothing
    // on screen promises it succeeded, and a failed write costs the next
    // launch its tab and never this activation.
    void this.uiState.save({ activeTab: pane.wireId }).catch(() => {})

    log.info('nocx: pane.setActive(true) called', {
      paneClasses: pane.pane.className,
    })
    await pane.start()
    pane.focus()
    this.onActivePaneChange?.()
  }

  /**
   * Activate the nth tab the window is SHOWING.
   *
   * Cmd+1..9 is workspace-scoped since the chip (§4.3), and it has to be: the
   * horizontal strip draws one workspace's tabs, so the third row a person
   * counts is the third row of that set. Counting every tab in the
   * application would select one they cannot see. nocx-jv3q.1 asserts that
   * grouping does not change what these keys select — grouping does not; the
   * viewport does, and that bead's assertion needs editing to say so.
   */
  activateByIndex(index: number): void {
    const pane = this.stripRows()[index]
    if (pane) void this.activate(pane)
  }

  closeActivePane(): void {
    if (this.activePane) void this.closePane(this.activePane)
  }

  /** The active pane's terminal content, when the active pane is a terminal.
   *  Global actions (the quick-connect "Integrate this shell" item,
   *  the secret picker's insert) target it because the pane's own input
   *  presentation is the only place that knows where text should go;
   *  content itself owns the PROMPT_READY && trusted && owned gate. */
  activeTerminalContent(): TerminalContent | null {
    const content = this.activePane?.content
    return content instanceof TerminalContent ? content : null
  }

  /** The terminal content whose session matches, when any pane holds it —
   *  the readScreen pull's lookup (nocx-ljfwz): the renderer answers a
   *  screen request only for the pane that owns the session's grid. */
  terminalContentForSession(sessionId: string): TerminalContent | null {
    for (const pane of this.panes) {
      const content = pane.content
      if (content instanceof TerminalContent && content.sessionId() === sessionId) {
        return content
      }
    }
    return null
  }

  /** The active pane's PANE element — the always-visible mount the snippet
   *  palette floats in (design §10.1: it must answer when the editor is
   *  hidden, so it cannot live inside the editor root). Null when no tab
   *  is active. */
  activePaneElement(): HTMLElement | null {
    return this.activePane?.pane ?? null
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
   *  because the seam exists so PaneManager never learns which content
   *  class replied (terminal content answers from its session; viewer
   *  content answers from the binding it was opened with). Null when the
   *  active pane has no origin or its content does not implement the
   *  capability. */
  activeOrigin(): ActiveOrigin | null {
    const pane = this.activePane
    const origin = pane?.content.activeOrigin?.()
    return pane && origin ? { paneId: pane.id, ...origin } : null
  }

  /** The ACTIVE pane's surface type (B.8) — the seam chrome reads to answer
   *  "what kind of pane is in front" without instanceof tests. The sidebar's
   *  Settings collapse (nocx-3e3b) reads this through the composition root:
   *  the descriptor is the single owner of what a tab is, and neither
   *  activeTerminalContent() (null for viewer tabs too) nor activeOrigin()
   *  (null transiently while a session opens) can tell Settings apart.
   *  Null when no tab is active yet. */
  activeSurfaceType(): SurfaceType | null {
    return this.activePane?.descriptor.surfaceType ?? null
  }

  /**
   * Ask for a new strip order.
   *
   * NOTHING MOVES HERE. The whole order is sent, the backend writes the
   * positions and answers with the tabs as stored, and the strip is redrawn
   * from that — so a refusal leaves the strip exactly where it was, with no
   * optimistic move to snap back from. That property is the bead's fourth
   * criterion and it is a consequence of where the write lands, not of a
   * rollback anybody had to remember to write.
   */
  reorderPane(draggedId: number, targetId: number, before = true): void {
    const draggedIndex = this.panes.findIndex((t) => t.id === draggedId)
    const targetIndex = this.panes.findIndex((t) => t.id === targetId)
    if (draggedIndex === -1 || targetIndex === -1) return

    if (!this.layoutAvailable) {
      // Nothing stores the order, so the strip is the only place it exists.
      const [draggedPane] = this.panes.splice(draggedIndex, 1)
      this.panes.splice(insertionIndex(draggedIndex, targetIndex, before), 0, draggedPane)
      this.tabStrip.reorder(this.panes)
      return
    }

    const draggedTab = this.layout.tabOf(this.panes[draggedIndex].wireId)
    const targetTab = this.layout.tabOf(this.panes[targetIndex].wireId)
    // A pane the chain does not hold — Settings, a file viewer — has no
    // position to change and cannot be dropped on one: nothing in the backend
    // has an opinion about where those sit.
    if (!draggedTab || !targetTab || draggedTab.id === targetTab.id) return

    // THE REQUEST NAMES THE WHOLE WORKSPACE, NOT THE STRIP.
    //
    // The ids must be a permutation of that workspace's tabs or the backend
    // refuses the whole reorder — membership never changes through a reorder.
    // Deriving the request from the panes ON SCREEN was therefore wrong the
    // moment the chain could hold a tab the renderer does not draw, which it
    // can: an ssh pane is restored into the chain and not reopened
    // (see adopt), so after one reload every reorder was refused with "not a
    // permutation" and the strip never moved. Found by the e2e gate, in
    // specs that had opened an ssh tab earlier in the run.
    //
    // So the order comes from the CACHE — every tab the workspace holds, in
    // the order they are drawn in — and the dragged one is moved to the
    // target's slot within it. A tab with no chrome keeps its place among the
    // others; it is a row the renderer cannot show, not a row that stopped
    // existing.
    const order = stripOrder(
      this.layout.tabs().filter((t) => t.workspaceId === draggedTab.workspaceId),
    ).map((t) => t.id)
    const from = order.indexOf(draggedTab.id)
    const to = order.indexOf(targetTab.id)
    if (from === -1 || to === -1) return
    order.splice(from, 1)
    order.splice(insertionIndex(from, to, before), 0, draggedTab.id)
    void this.ask(
      () => this.layout.reorder(draggedTab.workspaceId, order),
      'Could not reorder the tabs',
    )
  }

  // ── rendering what the backend says ──────────────────────────────────

  /**
   * Draw the strip from the cache: adopt rows that have no chrome, drop
   * chrome whose row is gone, apply the decoration, and put the entries in
   * the backend's order.
   *
   * Called from ONE place — the store's change notification — so every path
   * that asks for something ends up here and none of them re-implements what
   * "after that change" looks like.
   */
  private renderFromLayout(): void {
    if (!this.layoutAvailable) return
    const rows = this.layout.panes()
    // A CLEAN START draws none of what was left (nocx-yejir), and it says so
    // twice: `adopting` is false for the read that learns the rows, and
    // `notShown` names them for every render after it. Both are needed —
    // the second cannot be filled until the read has answered.
    for (const row of this.adopting ? rows : []) {
      if (this.panes.some((p) => p.wireId === row.id)) continue
      if (this.notShown.has(row.id)) continue
      this.adopt(row)
    }
    for (const pane of [...this.panes]) {
      // A pane with no row is either a view pane (Settings, a file viewer),
      // which was never in the chain, or one whose row has just gone. Only
      // the second is dropped, and the strip's own record of which is which
      // is the descriptor's surface type.
      if (rows.some((r) => r.id === pane.wireId)) continue
      // `registered` is what tells the two apart, and it is only ever set
      // once the backend has ANSWERED: a pane whose create is still in flight
      // has no row yet and must not be mistaken for one that has lost it.
      if (this.registered.has(pane.wireId)) this.dropChrome(pane)
    }
    this.applyDecoration()
    this.syncStripOrder()
  }

  /**
   * Put a pane the backend holds on screen.
   *
   * A LOCAL pane starts a fresh shell, which is §8's rule: the process died
   * with the backend and is never resurrected, so what comes back is the
   * pane, not its shell. That is also the whole of what an INLINE ssh gets —
   * a pane somebody typed `ssh host` inside is a local pane and its row says
   * so, so it comes back as its local shell, and the commands that ran on
   * the far host go on saying where they ran because the entry, not the
   * pane, is what recorded it (design §7).
   *
   * AN SSH PANE MAKES A NEW CONNECTION to the endpoint it applies at
   * (reopenSSH). Not a resurrection and never allowed to look like one: the
   * old session died with the backend, and what this opens is another one.
   */
  private adopt(row: PaneRow): void {
    if (row.kind === 'ssh') {
      this.reopenSSH(row)
      return
    }
    this.registered.add(row.id)
    // The row is what the renderer was just told about, so it exists NOW:
    // nothing to wait for, and the session names the pane immediately.
    //
    // NOT ACTIVATED: adoption draws what the backend already holds, and the
    // window's answer to "which tab is in front" is not the last row the
    // chain happened to hand over — `boot` decides that once, from the pane
    // the person was actually in.
    this.buildLocalPane({ paneId: row.id, registered: Promise.resolve(true) }, false)
  }

  /**
   * Reopen a stored connection: a NEW session to the endpoint the row says
   * the pane applies at.
   *
   * THE PROFILE IS RESOLVED FROM THE ENDPOINT, because the chain has no
   * column for one — §5 stores where a pane applies, not which saved
   * connection it was opened from. The match is the one the product already
   * makes for a hand-typed destination (resolveSshProfileOverlay), and it
   * counts only when the profile's canonical identity IS this endpoint:
   * `deploy@srv-01:22` and `srv-01:22` are two destinations, and a profile
   * that merely shares the host is not the one this pane was opened from.
   *
   * NO MATCH IS THE ORDINARY CASE, not a failure — a pane opened on a plain
   * host or an ssh-config alias never had a profile. It reopens the way it
   * was opened, through the host, and the far end's config supplies the rest.
   *
   * The pane is built SYNCHRONOUSLY, in the turn the row arrives, so the row
   * is on screen before boot chooses which tab is in front.
   */
  private reopenSSH(row: PaneRow): void {
    this.registered.add(row.id)
    const endpoint = row.endpoint ?? ''
    // The one parser for `user@host:port` in this codebase; an endpoint it
    // cannot make a host out of leaves `host` empty, and the open is refused
    // for saying nothing about where to connect — which is the honest answer
    // to a row that does not say either.
    const dest = parseQuickConnect(endpoint).options
    const overlay = dest.host
      ? resolveSshProfileOverlay(this.savedProfiles, { host: dest.host, user: dest.user })
      : null
    const profileId = overlay?.identity === endpoint ? overlay.profileId : ''
    log.info('nocx: reopening a stored connection', { pane: row.id, endpoint, profileId })
    const pane = this.buildSSHPane(
      { paneId: row.id, registered: Promise.resolve(true) },
      { profileId, host: dest.host, user: dest.user, port: dest.port },
      // The tab is named after the endpoint until the session names it,
      // exactly as a connection opened by hand is named after its host.
      dest.host || endpoint,
      false,
    )
    void this.watchReconnect(pane, endpoint)
  }

  /**
   * A reconnect that did not come up SAYS SO, and keeps its pane.
   *
   * The pane is never replaced by a local shell: a tab that says a host and
   * runs your own machine is the worst answer available, and the second
   * worst is a `slog.Warn` nobody sees. What is left on screen is the tab,
   * its row in the chain, and the content's own account of why the session
   * did not start.
   *
   * WHAT IS NOT LEFT ON SCREEN YET is the pane's past. A content whose mount
   * failed replaces the pane's children with its notice, and the scrollback
   * the restored blocks are drawn into goes with them — so the blocks this
   * pane has are fetched into a tree nobody can see. The fix is one line in
   * terminal-content's mount catch (prepend the notice rather than replace
   * the pane), and that file belongs to nocx-8won8's worker; measured, not
   * assumed — with that one line the same pane shows both its block and its
   * reason.
   *
   * A pane that was dropped while the connect was in flight — a clean start
   * naming its rows, a tab the person closed — reports nothing: there is no
   * pane left to explain.
   */
  private async watchReconnect(pane: Pane, endpoint: string): Promise<void> {
    const content = pane.content
    if (!(content instanceof TerminalContent)) return
    if (await content.ready) return
    if (!this.panes.includes(pane)) return
    this.reportNotReconnected(endpoint)
  }

  /**
   * Say ONCE that connections did not come back, however many there were.
   *
   * One toast per pane was the first version and it was wrong twice over: a
   * user with four ssh tabs got four warnings on every load, and in the e2e
   * gate a warning left over from an earlier spec sat beside the toast a
   * later spec was asserting on, which is a strict-mode locator resolving to
   * two elements (git-panel, three specs). A count is the honest summary, and
   * the endpoints are in the log for whoever needs them.
   *
   * The window is a turn rather than a fixed delay: connections fail at their
   * own pace, and the tab each one left on screen — with the content's own
   * account of why the session did not start — is what carries the fact
   * afterwards. This is the summary, not the record.
   */
  private reportNotReconnected(endpoint: string): void {
    log.warn('nocx: a stored connection was not reopened', { endpoint })
    this.notReconnected.add(endpoint || 'a host')
    if (this.notReconnectedScheduled) return
    this.notReconnectedScheduled = true
    setTimeout(() => {
      this.notReconnectedScheduled = false
      const hosts = [...this.notReconnected]
      this.notReconnected.clear()
      if (hosts.length === 0) return
      showToast({
        level: 'warning',
        message:
          hosts.length === 1
            ? `Could not reconnect to ${hosts[0]} — its tab is still here`
            : `${hosts.length} connections could not be reopened — their tabs are still here`,
      })
    }, 0)
  }

  /** Remove chrome without touching the chain: the row is already gone. */
  private dropChrome(pane: Pane): void {
    const index = this.panes.indexOf(pane)
    if (index === -1) return
    const wasActive = pane === this.activePane
    this.removeFromRecent(pane.id)
    pane.close()
    pane.pane.remove()
    this.tabStrip.removePane(pane.id)
    this.panes.splice(index, 1)
    this.registered.delete(pane.wireId)
    if (wasActive) {
      const next = this.popRecent() ?? this.panes[0]
      if (next) void this.activate(next)
    }
  }

  /** Push each tab's stored decoration into its pane's chrome. */
  private applyDecoration(): void {
    for (const pane of this.panes) {
      const tab = this.layout.tabOf(pane.wireId)
      const colour = tab?.colour ?? null
      pane.setTabDecoration({
        name: tab?.name ?? null,
        // A colour this renderer does not know draws as none rather than as a
        // broken swatch: what is stored is the store's business.
        colour: isWorkspaceColour(colour) ? colour : null,
        pinned: tab?.pinned === true,
      })
    }
  }

  /**
   * Order the strip the way the backend's positions and pins say.
   *
   * ONLY THE CHAIN'S PANES MOVE. A pane the chain does not hold — Settings, a
   * file viewer — keeps the slot it already occupies, and the backend's order
   * is dealt into the slots that are left. Sweeping them to the end instead
   * was wrong and the e2e gate said so in four specs: opening Settings and
   * then a connection put the new tab BEFORE Settings, so "the last tab is
   * the one that just opened" stopped being true. Nothing in the backend has
   * an opinion about where a view pane sits, and a renderer that moves one on
   * the backend's behalf is inventing an opinion for it.
   */
  private syncStripOrder(): void {
    const chain = this.chainOrder()
    const fromChain: Pane[] = []
    for (const { pane, groupKey, depth } of chain) {
      pane.setStripPlacement({ groupKey, depth })
      fromChain.push(pane)
    }
    // A pane the chain does not hold yet carries no placement at all: no
    // group, no indent. It is on screen because the person opened it, and
    // nothing in the backend has an opinion about where it sits.
    for (const pane of this.panes) {
      if (!fromChain.includes(pane)) pane.setStripPlacement({ groupKey: '', depth: 0 })
    }
    // A VIEW PANE BELONGS TO THE UNGROUPED RUN, and to its own place within
    // it. Opening Settings used to put it wherever it was created — the end
    // of the rail — which after the strip learnt to group meant UNDER THE
    // LAST WORKSPACE'S HEADING, reading as a member of a workspace it is not
    // in and cannot be in.
    //
    // So it is placed among the DEFAULT workspace's rows rather than after
    // every group: the ungrouped run is where a thing with no workspace
    // belongs. And within that run it keeps its order relative to the rows
    // beside it, because "the last tab is the one that just opened" is a
    // promise several specs make — a rule that swept view panes to the end of
    // the run would put Settings after the connection a person had just
    // opened.
    const chainPanes = new Set(fromChain)
    const defaultWorkspaceId = this.layout.defaultWorkspaceId()
    const grouped = chain.filter((row) => row.groupKey !== defaultWorkspaceId).map((r) => r.pane)
    const ungrouped = chain.filter((row) => row.groupKey === defaultWorkspaceId).map((r) => r.pane)
    // The ungrouped run in the order the window already has: the chain's rows
    // take the chain's order among themselves, and a view pane sits where it
    // was opened relative to them.
    const ungroupedSet = new Set(ungrouped)
    const chainQueue = [...ungrouped]
    const run: Pane[] = []
    for (const pane of this.panes) {
      if (ungroupedSet.has(pane)) {
        const next = chainQueue.shift()
        if (next) run.push(next)
      } else if (!chainPanes.has(pane)) {
        run.push(pane)
      }
    }
    this.panes.splice(0, this.panes.length, ...run, ...grouped)
    this.tabStrip.setGroupHeadings(this.groupHeadings())
    this.tabStrip.setExpandedGroup(this.expandedGroup())
    this.tabStrip.reorder(this.stripRows())
  }

  /**
   * The chain's panes, in the order and the placement the chain implies —
   * workspace by workspace, and a lineage child under its parent
   * (layout/strip-tree.ts).
   *
   * ONE ORDER FOR BOTH ORIENTATIONS, deliberately. What differs between the
   * two strips is which of these rows is DRAWN (see stripRows), never where a
   * row sits relative to another: a tab that moves when you change the strip's
   * placement setting is a tab whose position two things decide.
   *
   * The depth is the vertical strip's alone — the tree stays there (§4.3) —
   * so the horizontal strip's rows are flat.
   */
  private chainOrder(): Array<{ pane: Pane; groupKey: string; depth: number }> {
    if (!this.layoutAvailable) return []
    const flat = this.tabStrip.orientation === 'horizontal'
    const rows: Array<{ pane: Pane; groupKey: string; depth: number }> = []
    const seen = new Set<Pane>()
    for (const workspace of this.workspaceRows()) {
      for (const { tab, depth } of lineageOrder(this.layout.tabsOfWorkspace(workspace.id))) {
        for (const row of this.layout.panesOf(tab.id)) {
          const chrome = this.panes.find((p) => p.wireId === row.id)
          if (!chrome || seen.has(chrome)) continue
          seen.add(chrome)
          rows.push({ pane: chrome, groupKey: workspace.id, depth: flat ? 0 : depth })
        }
      }
    }
    return rows
  }

  /**
   * THE ROWS THE STRIP DRAWS, in the order it draws them.
   *
   * BOTH ORIENTATIONS NOW GET EVERY ROW, and that is the rework. §4.3 had the
   * horizontal strip draw the current workspace alone, on the argument that
   * the row would otherwise grow with every tab in the application. The
   * argument was right about the row and wrong about the remedy: filtering
   * made the other workspaces *unreachable except through a menu*, which is
   * the pair of complaints this change answers. Folding does the same work
   * without the cost — a workspace nobody is looking at collapses to its
   * pill, so the row grows by ONE ELEMENT per workspace instead of by every
   * tab, and the workspace is still on screen and one click away.
   *
   * Which rows are folded is the STRIP's business, not this method's: it is a
   * question about what is on screen, it changes without the chain changing,
   * and the strip is the only thing that knows its own orientation at paint
   * time. This says what exists; `setExpandedGroup` says what is unfolded.
   */
  private stripRows(): Pane[] {
    return [...this.panes]
  }

  /**
   * Every workspace there is, with the default among them.
   *
   * THE DEFAULT WORKSPACE IS PERMANENT AND ITS ROW IS LAZY. The backend
   * writes that row when something first needs it (ensureDefaultWorkspace),
   * so a read taken before then answers with no default row at all — while
   * every tab in the application is in it. The renderer must not read that as
   * "there is no default workspace": the id is on the wire and always
   * answered, so the default is drawn from the ID and never from the presence
   * of a row.
   *
   * Two things break without this, and both were found by the tests that made
   * it: the switcher offers no way back to the default, which is the one
   * thing §4.3 says the chip must never fail to do; and the vertical strip
   * draws none of the default's tabs, because they belong to a workspace it
   * is not iterating.
   */
  private workspaceRows(): readonly WorkspaceRow[] {
    const known = this.layout.workspaces()
    const id = this.layout.defaultWorkspaceId()
    if (id === '' || known.some((w) => w.id === id)) return known
    // Name it with the empty string rather than anything readable: nothing
    // renders the default's name (workspaceAxis answers null for it), and a
    // placeholder that could be rendered is a name waiting to leak.
    // No colour either, for the same reason as the name: the default renders
    // none, so there is nothing here that could leak into a pill.
    return [{ id, name: '', colour: null, position: -1 }, ...known]
  }

  // ── The workspace overview's port (nocx-edhcu) ─────────────────────────

  /**
   * What the overview reads, and the seam it reads it through.
   *
   * TWO SOURCES, AND THE SPLIT IS THE EXISTING ONE. Membership comes from the
   * chain, because the backend owns it (§4.5); what a pane is DOING comes
   * from the pane's own content, because the renderer owns render state
   * (AD-6). Neither is copied into a third place for this surface to read —
   * a snapshot assembled at open time is a view, not a store.
   *
   * A PANE IN THE CHAIN THE RENDERER NEVER DREW STILL GETS A CARD. An adopted
   * ssh row is exactly that (see `adopt`), and it is precisely the thing a
   * person opening an overview wants to be told about — so the facts fall
   * back to the row, which is all that exists for it.
   */
  overviewPort(): OverviewPort {
    return {
      snapshot: () => this.overviewSnapshot(),
      activate: (paneId) => {
        const pane = this.panes.find((p) => p.wireId === paneId)
        // `activate` already moves the window to the pane's workspace, so a
        // card in a workspace you were not in lands you there whole — there
        // is nothing extra for this to do, and doing it here would be a
        // second answer to "which workspace is in front".
        if (pane) void this.activate(pane)
      },
      focusActive: () => this.activePane?.focus(),
      switchWorkspace: (workspaceId) => this.switchWorkspace(workspaceId),
      // INTO THE COLUMN THAT WAS PRESSED, not into wherever the window is.
      // The overview shows every workspace at once, so "the current one" is
      // not what the person pointed at. Switching first is what makes the
      // create land there: `mintPane` opens into the workspace the window is
      // showing, and that is the one owner of "where does a new tab go" —
      // a second rule here would be the AD-8 shape.
      createTab: (workspaceId) => {
        this.switchWorkspace(workspaceId)
        this.newPane()
      },
      closeWorkspace: (workspaceId) => this.closeWorkspaceById(workspaceId),
      createWorkspace: () => void this.newWorkspace(),
      subscribe: (listener) => this.layout.onChange(listener),
    }
  }

  private overviewSnapshot(): OverviewSnapshot {
    const defaultId = this.layout.defaultWorkspaceId()
    return {
      activePaneId: this.activePane?.wireId ?? null,
      workspaces: this.workspaceRows().map((w) => ({
        id: w.id,
        // NULL is what marks the default, and it is derived from the ID
        // rather than from the row's stored name — `workspaceRows` explains
        // why the default's row may not exist at all yet.
        name: w.id === defaultId ? null : w.name,
        colour: w.id === defaultId || !isWorkspaceColour(w.colour) ? null : w.colour,
        panes: this.layout.panesOfWorkspace(w.id).map((row) => this.overviewFacts(row)),
      })),
    }
  }

  /** One pane's facts, read at the moment they are asked for. */
  private overviewFacts(row: PaneRow): OverviewPaneFacts {
    const pane = this.panes.find((p) => p.wireId === row.id)
    const content = pane?.content
    const terminal = content instanceof TerminalContent ? content : null
    const live = terminal?.liveWork() ?? null
    return {
      paneId: row.id,
      title: pane?.displayTitle || null,
      // The row's endpoint is the fallback and not the first choice: a live
      // session knows where it actually is, and the row knows only where it
      // was opened.
      host: live?.host ?? (row.endpoint || null),
      cwd: row.cwd || null,
      // No per-pane branch exists in the renderer: D10 makes the git panel
      // follow the ACTIVE tab and the session the sole owner of "which
      // repository", so a branch per card would need a second owner of that
      // question. The field is carried so the day it does exist is a
      // one-line change.
      branch: null,
      agentStatus: pane?.agentStatus ?? null,
      runningCommand: live?.command ?? null,
      failed: pane?.warning === true,
      since: terminal?.runningSince() ?? null,
      lastLine: terminal?.lastOutputLine() ?? null,
      fullScreen: terminal?.fullScreen() ?? false,
      lastBlock: terminal?.lastBlock() ?? null,
      excerpt: terminal?.excerpt() ?? [],
    }
  }

  /** What a workspace is called ON SCREEN — the axis's answer, so the rule
   *  that the default has no name has exactly one owner
   *  (layout/strip-groups.ts) and the chip, the heading and the switcher
   *  cannot disagree about it. */
  private workspaceAxis(): GroupAxis<{ groupKey: string }> {
    return workspaceAxis(
      this.workspaceRows(),
      this.layout.defaultWorkspaceId(),
      (row) => row.groupKey,
    )
  }

  private workspaceLabel(workspaceId: string): string | null {
    return this.workspaceAxis().heading(workspaceId)
  }

  /**
   * One entry per workspace, so a group draws the heading its workspace has —
   * which for the default is none, whatever else exists.
   *
   * BOTH ORIENTATIONS ARE TOLD, and that changed with the rework. The
   * horizontal strip used to be told nothing, because it showed one workspace
   * behind a chip and a heading would have said the chip's sentence twice.
   * Now a workspace is a run of tabs in that row and the heading is what
   * stands in front of the run — the same object the vertical strip writes
   * above a column, drawn as a pill instead of a caption. One axis, one set
   * of names, two shapes.
   *
   * A heading of `null` is still what makes the default workspace's tabs
   * top-level rows (§4.2): no caption in the column, and no pill in the row.
   */
  private groupHeadings(): Array<{ key: string; heading: string | null; colour: string | null }> {
    const axis = this.workspaceAxis()
    // THE COLOUR TRAVELS WITH THE HEADING because it is the same fact about
    // the same object, and because the strip must not derive it: it derived
    // one once, by hashing the id, and a colour the user chose replaced that
    // (nocx-2mipw). What is stored is passed through unjudged — the strip
    // draws an unrecognised value as no colour, the same rule tabs follow.
    return this.workspaceRows().map((w) => ({
      key: w.id,
      heading: axis.heading(w.id),
      colour: w.colour ?? null,
    }))
  }

  /**
   * WHICH WORKSPACE HAS ITS TABS OUT in the horizontal strip.
   *
   * It is `currentWorkspaceId` and not "the active pane's workspace", and the
   * difference is the whole reason the strip is told rather than deriving it:
   * the active pane can be one the chain does not hold — Settings, a file
   * viewer, a pane whose create is still in flight — and every one of those
   * has no workspace at all. Derived, opening Settings would fold away the
   * tabs the person was working in and leave the row looking emptied. This
   * value survives that, because a viewport's workspace is not a fact about
   * whichever pane happens to have focus.
   *
   * Null with no chain to draw it from: with the layout store refused there
   * are no workspaces, so there is nothing to unfold and nothing folded.
   */
  private expandedGroup(): string | null {
    if (!this.layoutAvailable) return null
    return this.currentWorkspaceId()
  }

  // ── MRU helpers ──────────────────────────────────────────────────────

  private pushRecent(id: number): void {
    this.removeFromRecent(id)
    this.recentPaneIds.push(id)
  }

  private popRecent(): Pane | undefined {
    while (this.recentPaneIds.length > 0) {
      const id = this.recentPaneIds.pop()!
      const pane = this.panes.find((t) => t.id === id)
      if (pane) return pane
    }
    return undefined
  }

  private removeFromRecent(id: number): void {
    const idx = this.recentPaneIds.indexOf(id)
    if (idx !== -1) this.recentPaneIds.splice(idx, 1)
  }

  // ── Keyboard shortcuts ───────────────────────────────────────────────

  private readonly onKeydown = (e: KeyboardEvent): void => {
    const mod = e.metaKey || e.ctrlKey
    if (!mod || e.altKey) return

    if (e.key === 't') {
      e.preventDefault()
      e.stopPropagation()
      this.onActivity?.()
      this.newPane()
      return
    }

    if (e.key === 'w') {
      e.preventDefault()
      e.stopPropagation()
      this.onActivity?.()
      this.closeActivePane()
      return
    }

    // Cmd/Ctrl+1..9 — switch to tab by visual index (all tabs).
    const keyNum = Number(e.key)
    if (Number.isInteger(keyNum) && keyNum >= 1 && keyNum <= 9 && keyNum <= this.panes.length) {
      e.preventDefault()
      e.stopPropagation()
      this.onActivity?.()
      this.activateByIndex(keyNum - 1)
    }
  }
}

/**
 * The canonical `user@host:port` an ssh pane applies at — what §5 stores as
 * the pane's endpoint.
 *
 * Canonical means every part is written even when it was defaulted, because
 * the stored value is what a restore reconnects to and "the port I did not
 * type" is not a fact anyone can look up later. The user is omitted when the
 * connection did not name one: the remote's default user is the far end's to
 * decide, and inventing one here would store a fact nobody stated.
 */
function endpointOf(host: string, user?: string, port?: number): string {
  return `${user ? `${user}@` : ''}${host}:${port ?? 22}`
}

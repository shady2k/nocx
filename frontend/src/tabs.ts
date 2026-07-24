import { WSClient, type SessionHandle } from './ipc'
import { createRenderer, resolveRendererName, type RendererName } from './renderers'
import type { TerminalRenderer } from './renderers/types'
import { detectAgentStatus, type AgentStatus } from './agent-status'
import { InputStateController } from './input-state'
import { CommandEditor } from './editor'
import { ShellInputTarget } from './input-target'
import { submitCommand } from './submit'
import { shouldCopy, type ClipboardAccess, type ClipboardGate } from './clipboard'
import type { ClipboardBanner } from './banner'

// How long the grid must hold still before the PTY is told about it.
// Dragging a window edge walks the grid through every intermediate size,
// and each one forwarded straight to the PTY is another SIGWINCH — the
// TUI repaints itself from scratch for a size that is already stale,
// dozens of times per drag, which is what shreds the screen. Repaint
// locally as fast as the renderer likes, but only tell the PTY the size
// the drag actually settled on.
const RESIZE_SETTLE_MS = 80

// ADR-0006: OFF by default; flip only after the native-mode escape +
// readiness gating land.
const ENHANCED_INPUT = false

// Shown only until the session reports where it started; a tab named after a
// generic word tells the user nothing once there are three of them.
const FALLBACK_TITLE = 'Terminal'

/**
 * Names a tab after its directory, the way every other terminal does. Keeps
 * the tail, which is the informative end of a path — the CSS ellipsis cuts
 * from the right, so handing it a long path would hide exactly the part worth
 * reading. '~' stays as itself: that is already the shortest true answer.
 *
 * The label says nothing about where the cwd came from — surfacing the
 * session-open fallback (AD-5) is cwdTooltip's job, where there is room to
 * say it in words.
 */
function directoryLabel(cwd: string): string {
  const path = cwd.trim().replace(/\/+$/, '')
  if (!path) return FALLBACK_TITLE
  const parts = path.split('/').filter(Boolean)
  if (path === '~' || parts.length === 0) return path || FALLBACK_TITLE
  return parts.slice(-2).join('/')
}

/**
 * Tooltip for a cwd. When the value comes from session open (no OSC 7 yet)
 * the tooltip surfaces that fact (AD-5 fallback visibility).
 */
function cwdTooltip(cwd: string, fromOSC7: boolean): string {
  if (!cwd) return ''
  return fromOSC7 ? cwd : `${cwd} (initial cwd)`
}

const DEFAULT_RENDERER: RendererName = resolveRendererName()

export class Tab {
  readonly id: number

  readonly pane = document.createElement('div')
  readonly button = document.createElement('div')
  readonly closeBtn = document.createElement('button')
  readonly indexLabel = document.createElement('span')
  readonly titleSpan = document.createElement('span')
  /** Agent state icon: spinner while working, dot when waiting on the user. */
  readonly statusIcon = document.createElement('span')
  /** Centres the status icon and the title as one unit, the way Warp does. */
  readonly label = document.createElement('span')
  /** Active-tab indicator (top bar) / activity indicator (bottom bar) */
  readonly indicator = document.createElement('div')

  // Empty, matching the label the constructor paints: a tab has no name until
  // its session reports a directory (nocx-83a). Seeding this with FALLBACK_TITLE
  // would make the getter claim a name the user never sees.
  private _title = ''
  private _defaultTitle = FALLBACK_TITLE
  private _programTitle = ''
  private _hasActivity = false
  private _agentStatus: AgentStatus | null = null
  private _bufferType: 'normal' | 'alternate' = 'normal'
  private _cwdFromOSC7 = false
  private _lastExitCode: number | null = null
  private inputState = new InputStateController()
  private renderer: TerminalRenderer | null = null
  private session: SessionHandle | null = null
  private editor: CommandEditor | null = null
  private shellTarget: ShellInputTarget | null = null
  private started = false

  // _readyPromise resolves true when the renderer mounts and the PTY session
  // opens; resolves false when start() throws. Never rejects. It stays pending
  // until start() is called, so consumers can await it for the honest
  // did-it-actually-start signal (see §7.5 of distribution-and-updates-design).
  private readonly _readyPromise: Promise<boolean>
  private _readyResolve!: (value: boolean) => void
  cols = 0
  rows = 0
  private resizeTimer: number | undefined

  constructor(
    private readonly client: WSClient,
    private readonly rendererName: RendererName,
    private readonly clipboard: ClipboardAccess,
    private readonly gate: ClipboardGate,
    private readonly banner: ClipboardBanner,
    id: number,
  ) {
    this.id = id
    this._readyPromise = new Promise<boolean>((resolve) => {
      this._readyResolve = resolve
    })

    this.pane.className = 'pane'
    this.pane.id = `pane-${id}`
    this.pane.setAttribute('role', 'tabpanel')

    this.button.className = 'tab'
    this.button.setAttribute('role', 'tab')
    this.button.setAttribute('aria-controls', this.pane.id)
    this.button.setAttribute('data-tab-id', String(id))

    this.indicator.className = 'tab-indicator'

    this.indexLabel.className = 'tab-index'
    this.indexLabel.textContent = String(id)

    this.statusIcon.className = 'tab-status'
    this.label.className = 'tab-label'

    this.titleSpan.className = 'tab-title'
    // Title is empty until start() resolves with the real directory name.
    // Setting 'Terminal' here would paint a placeholder that flashes before
    // the real name lands (nocx-83a).

    this.closeBtn.className = 'tab-close'
    this.closeBtn.textContent = '×'
    this.closeBtn.setAttribute('aria-label', 'Close tab')

    this.label.append(this.statusIcon, this.titleSpan)
    this.button.append(this.indexLabel, this.label, this.closeBtn, this.indicator)
    this.setActive(false)
  }

  setActive(active: boolean): void {
    this.pane.classList.toggle('active', active)
    this.button.classList.toggle('active', active)
    this.button.setAttribute('aria-selected', String(active))
    if (active) {
      this._hasActivity = false
      this.indicator.classList.remove('tab-activity')
    }
  }

  get title(): string {
    return this._title
  }

  get hasActivity(): boolean {
    return this._hasActivity
  }

  /** Called when this background tab receives output. */
  private markActivity(): void {
    if (!this._hasActivity) {
      this._hasActivity = true
      this.indicator.classList.add('tab-activity')
    }
  }

  get agentStatus(): AgentStatus | null {
    return this._agentStatus
  }

  get lastExitCode(): number | null {
    return this._lastExitCode
  }

  /**
   * Resolves true when the renderer mounts and the PTY session opens;
   * resolves false when start() throws. Never rejects. Stays pending
   * until start() is called.
   */
  get ready(): Promise<boolean> {
    return this._readyPromise
  }

  /**
   * A coding agent runs as a single shell command, so OSC 133 command
   * boundaries (nocx-5mn.4) cannot see inside it — but the agent publishes its
   * state in the title, and we already receive that. When the title says
   * nothing about an agent the status clears and the byte-level activity rule
   * takes over again.
   */
  private updateAgentStatus(title: string): void {
    const next = detectAgentStatus(title)
    if (next === this._agentStatus) return
    this._agentStatus = next
    this.button.classList.toggle('working', next === 'working')
    this.button.classList.toggle('waiting', next === 'idle')

    // An agent that stopped working is asking for you — the one event the
    // activity underline exists to announce. The byte-level rule cannot see
    // it: the agent repaints inside the alternate buffer, which is exactly
    // what that rule suppresses (nocx-5mf).
    if (next === 'idle' && !this.button.classList.contains('active')) {
      this.markActivity()
    }
  }

  updateTitle(title: string): void {
    // A TUI clears the title on the way out by emitting OSC 0/2 with an empty
    // string. Taken literally that blanks the tab; kept as-is it would leave a
    // plain shell still labelled "Claude Code". Fall back to the default name.
    // Classify before falling back: the marker lives in the raw title, and an
    // empty title is the shell clearing it, which is not an agent state.
    this.updateAgentStatus(title)

    this._programTitle = title.trim()
    const next = this._programTitle || this._defaultTitle
    this._title = next
    this.titleSpan.textContent = next
  }

  /**
   * Called when the VT frontend parses OSC 7 (AD-6). Updates the cwd-based
   * tab name and the tooltip. If no program has set a title, the visible
   * tab title follows the cwd immediately.
   */
  updateCwd(path: string): void {
    this._cwdFromOSC7 = true
    this._defaultTitle = directoryLabel(path)
    this.button.title = cwdTooltip(path, true)

    // If no program has set a title, the visible title tracks the cwd.
    if (!this._programTitle) {
      this._title = this._defaultTitle
      this.titleSpan.textContent = this._defaultTitle
    }
  }

  /**
   * Mounted lazily on first activation, so a freshly-created tab costs nothing
   * until it is visited. Panes keep their layout box while inactive
   * (visibility, not display), so the renderer measures a real size the moment
   * it mounts.
   */
  async start(): Promise<void> {
    if (this.started) return
    this.started = true

    try {
      const renderer = createRenderer(this.rendererName)
      await renderer.mount(this.pane)

      this.cols = renderer.cols
      this.rows = renderer.rows

      // Open the session at the renderer's actual grid size. Per AD-1/AD-7,
      // the PTY is created at this size — never spawn-then-resize.
      const session = await this.client.openSession(this.cols, this.rows)

      // The directory names the tab until a program sets a title; from here
      // on OSC 7 keeps it following `cd` (nocx-5mn.2).
      this._defaultTitle = directoryLabel(session.cwd)
      // First paint of the label: it stayed empty until the name existed
      // (nocx-83a). Nothing can have set a title before this point — onTitle is
      // subscribed below, after this await.
      this.titleSpan.textContent = this._title = this._defaultTitle
      this.button.title = cwdTooltip(session.cwd, false)

      session.onData((data: string) => {
        renderer.write(data)
        // Normal-buffer output on a background tab lights the indicator.
        // Full-screen TUIs repaint constantly in the alternate buffer —
        // that is not news. A bell in either buffer still counts.
        if (this._bufferType === 'normal' && !this.button.classList.contains('active')) {
          this.markActivity()
        }
      })
      session.onExit((sid: string) => {
        console.log('nocx: session exited:', sid)
        this.inputState.dispatch({ type: 'exit' })
      })
      session.onReset(() => {
        renderer.reset()
        this.inputState.dispatch({ type: 'reset' })
      })

      renderer.onData((data: string) => session.send(data))
      renderer.onTitle((title: string) => {
        this.updateTitle(title)
      })
      renderer.onCwd(({ path }) => {
        this.updateCwd(path)
      })
      renderer.onBufferChange((type) => {
        this._bufferType = type
        this.inputState.dispatch({ type: 'buffer', buffer: type })
      })
      renderer.onBell(() => {
        // Bell is always attention-worthy, even in the alternate buffer.
        if (!this.button.classList.contains('active')) {
          this.markActivity()
        }
      })
      renderer.onCommandMarker((marker) => {
        this.inputState.dispatch({ type: 'marker', kind: marker.kind })
        // OSC 133 D carries the exit code of the just-finished command.
        // Stored for future consumers: command blocks, success/failure
        // colouring, activity indicator refinement.
        if (marker.kind === 'D' && marker.exitCode !== undefined) {
          this._lastExitCode = marker.exitCode
        }
      })

      this.inputState.onChange((m) => {
        console.debug('nocx: input-state', m.state, 'trusted=', m.trusted, 'owned=', m.owned)
        if (!ENHANCED_INPUT) return
        if (m.owned) this.editor!.show()
        else {
          this.editor!.hide()
          renderer.focus()
        }
      })

      // ── Command editor (DOM textarea) ────────────────────────────────
      // Wired behind ENHANCED_INPUT (ADR-0006 §5: fail-open). When the flag
      // is on, ownership (A→B) gives the editor focus; typing + Enter submits
      // via ShellInputTarget's atomic handoff (hide-before-send → bracketed
      // paste + CR). When the flag is off, keys flow unchanged to the PTY.
      this.shellTarget = new ShellInputTarget((data: string) => session.send(data))
      this.editor = new CommandEditor({
        submit: (doc: string) => {
          submitCommand(doc, {
            dispatchSubmit: () => this.inputState.dispatch({ type: 'submit' }),
            focusGrid: () => renderer.focus(),
            sendDoc: (d) => void this.shellTarget!.submit(d),
          })
        },
      })
      this.editor.mount(this.pane)

      // ── Clipboard ────────────────────────────────────────────────────
      // The renderer reports facts and never touches the clipboard (AD-6).
      // Policy — copy-on-select, OSC 52 notification, multi-line confirm —
      // lives here, above the renderer boundary.

      // Copy on select: write non-empty selection text to the clipboard.
      renderer.onSelectionChange((text) => {
        if (shouldCopy(text)) {
          this.clipboard.writeText(text).catch((e) => {
            console.warn('nocx: clipboard write failed (selection)', e)
          })
        }
      })

      // OSC 52 write: denied by default (Warp's default). The first
      // blocked attempt raises a banner across the top of the window
      // with the remedy built in. Once the user allows, writes are
      // silent — the user consented.
      renderer.onClipboardWrite((text) => {
        // Already allowed — write directly, no notification.
        if (this.gate.granted) {
          this.clipboard.writeText(text).catch((e) => {
            console.warn('nocx: clipboard write failed (OSC 52)', e)
          })
          return
        }

        // Suppressed — silently drop.
        if (this.gate.suppressed) return

        // Banner already shown — silently drop.
        if (this.banner.shown) return

        // First blocked write: raise the banner and wait for the user.
        void this.banner.show().then((choice) => {
          if (choice === 'allow') {
            this.gate.allow()
            this.clipboard.writeText(text).catch((e) => {
              console.warn('nocx: clipboard write failed (OSC 52)', e)
            })
          } else if (choice === 'suppress') {
            this.gate.suppress()
          }
          // dismiss: do nothing — neither grant nor suppress.
        })
      })

      // Paste on right-click AND middle-click (Tabby, Warp).
      const doPaste = () => {
        this.clipboard
          .readText()
          .then((text) => {
            if (!text) return
            // Multi-line paste is confirmed before it reaches the terminal,
            // except in the alternate screen — a full-screen program is not
            // a shell prompt. This is Tabby's exact condition.
            if (text.includes('\n') && this._bufferType === 'normal') {
              if (!window.confirm('Paste multi-line text?')) return
            }
            renderer.paste(text)
          })
          .catch((e) => {
            console.warn('nocx: clipboard read failed (paste)', e)
          })
      }

      this.pane.addEventListener('contextmenu', (e: MouseEvent) => {
        e.preventDefault()
        doPaste()
      })

      this.pane.addEventListener('mousedown', (e: MouseEvent) => {
        if (e.button === 1) {
          e.preventDefault()
          doPaste()
        }
      })

      renderer.onResize((cols: number, rows: number) => {
        if (cols === this.cols && rows === this.rows) return
        this.cols = cols
        this.rows = rows
        clearTimeout(this.resizeTimer)
        this.resizeTimer = window.setTimeout(() => session.sendResize(cols, rows), RESIZE_SETTLE_MS)
      })

      this.renderer = renderer
      this.session = session
      this._readyResolve(true)
      console.log(`nocx: tab ready (renderer=${this.rendererName})`, { sid: session.sessionId })
    } catch (err) {
      const notice = document.createElement('pre')
      notice.className = 'pane-error'
      notice.textContent = `Tab ${this.id} failed to start:\n\n${err instanceof Error ? err.message : String(err)}`
      this.pane.replaceChildren(notice)
      this._readyResolve(false)
      console.error(`nocx: tab ${this.id} failed`, err)
    }
  }

  refreshAtlas(): void {
    this.renderer?.refreshAtlas()
  }

  focus(): void {
    this.renderer?.focus()
  }

  close(): void {
    this.session?.close()
    this.renderer?.dispose()
    this.editor?.dispose()
  }
}

/**
 * Manages a dynamic list of numbered terminal tabs, each backed by a session
 * on a shared WSClient. One renderer type is used for all tabs in a window
 * (resolved via ?r=xterm|wterm at construction time).
 *
 * Behaviour invariants:
 *  - Closing the last tab immediately opens a fresh one, so the window
 *    is never empty (no start page — see tabs.ts newTab()).
 *  - A background tab shows the activity indicator on normal-buffer
 *    output or on bell (BEL). Alternate-buffer repaints (full-screen
 *    TUIs) do not light it — a bell is the TUI's way to ask for attention.
 *  - Titles come live from the shell via TerminalRenderer.onTitle().
 *    They are untrusted: set via textContent, never innerHTML, and
 *    truncated with CSS, never by cutting the string.
 */
export class TabManager {
  private readonly tabs: Tab[] = []
  private nextTabId = 1
  private activeTab: Tab | null = null
  private readonly bar: HTMLElement
  private readonly panes: HTMLElement
  private readonly client: WSClient
  private readonly rendererName: RendererName
  private readonly clipboard: ClipboardAccess
  private readonly gate: ClipboardGate
  private readonly banner: ClipboardBanner
  private readonly addBtn: HTMLButtonElement
  private readonly tabsContainer: HTMLElement
  private readonly _initialTabReady: Promise<void>

  constructor(
    bar: HTMLElement,
    panes: HTMLElement,
    client: WSClient,
    clipboard: ClipboardAccess,
    gate: ClipboardGate,
    banner: ClipboardBanner,
  ) {
    this.bar = bar
    this.panes = panes
    this.client = client
    this.rendererName = DEFAULT_RENDERER
    this.clipboard = clipboard
    this.gate = gate
    this.banner = banner

    bar.setAttribute('role', 'tablist')
    bar.classList.add('tabbar')

    // Non-growing tabs container — holds tab buttons.
    this.tabsContainer = document.createElement('div')
    this.tabsContainer.className = 'tabs-container'
    bar.append(this.tabsContainer)

    // + button — sits immediately after the last tab, before the spacer.
    this.addBtn = document.createElement('button')
    this.addBtn.className = 'tab-add'
    this.addBtn.textContent = '+'
    this.addBtn.setAttribute('aria-label', 'New tab')
    this.addBtn.addEventListener('click', () => this.newTab())
    bar.append(this.addBtn)

    // Flexible spacer — absorbs leftover width so tabs never stretch.
    const spacer = document.createElement('div')
    spacer.className = 'tabbar-spacer'
    bar.append(spacer)

    // Open the initial tab — the window is never empty.
    const initialTab = this.newTab()
    this._initialTabReady = initialTab.ready.then((ok) => {
      if (!ok) throw new Error('initial tab failed to start')
    })

    // Capture phase: the active terminal swallows keys once they reach it.
    window.addEventListener('keydown', this.onKeydown, true)
  }

  get tabCount(): number {
    return this.tabs.length
  }

  /**
   * Resolves when the initial tab's renderer mounts and its PTY session
   * opens. Rejects when the initial tab's start() threw — a broken tab
   * is not "ready" even though the UI shows a .pane-error notice.
   *
   * This is the signal §7.5 of distribution-and-updates-design uses to
   * gate the updater health check: ReportHealthy() is only called after
   * this promise resolves.
   */
  get initialTabReady(): Promise<void> {
    return this._initialTabReady
  }

  /** Create a new tab, activate it. */
  newTab(): Tab {
    const tab = new Tab(
      this.client,
      this.rendererName,
      this.clipboard,
      this.gate,
      this.banner,
      this.nextTabId++,
    )

    this.tabs.push(tab)
    // Append to the tabs container (addBtn sits after the container).
    this.tabsContainer.append(tab.button)
    this.panes.append(tab.pane)

    // Click → activate. The close button's own handler calls stopPropagation,
    // so close-button clicks never reach here.
    tab.button.addEventListener('click', () => {
      void this.activate(tab)
    })

    // Close button click → close.
    tab.closeBtn.addEventListener('click', (e: MouseEvent) => {
      e.stopPropagation()
      this.closeTab(tab)
    })

    // Middle-click on tab → close.
    tab.button.addEventListener('mousedown', (e: MouseEvent) => {
      if (e.button === 1) {
        e.preventDefault()
        this.closeTab(tab)
      }
    })

    this.refreshIndices()
    void this.activate(tab)
    return tab
  }

  /**
   * Close a tab, its session, and its DOM elements. If the closed tab was the
   * active one, the neighbour at the same visual position (or the previous one)
   * is activated. Closing the last tab opens a fresh one immediately.
   */
  closeTab(tab: Tab): void {
    const index = this.tabs.indexOf(tab)
    if (index === -1) return

    const wasActive = tab === this.activeTab

    tab.close()
    tab.pane.remove()
    tab.button.remove()
    this.tabs.splice(index, 1)

    // Closing the last tab opens a fresh one immediately so the window
    // is never empty (decision: no start page / empty state).
    if (this.tabs.length === 0) {
      this.newTab()
      return
    }

    this.refreshIndices()

    if (wasActive) {
      // Activate neighbour: prefer the tab at the same index, or the previous.
      const neighbourIndex = Math.min(index, this.tabs.length - 1)
      void this.activate(this.tabs[neighbourIndex])
    }
  }

  /** Activate a tab: show its pane, focus its renderer. */
  async activate(tab: Tab): Promise<void> {
    if (tab === this.activeTab) {
      tab.focus()
      return
    }

    this.activeTab?.setActive(false)
    this.activeTab = tab
    tab.setActive(true)

    await tab.start()
    tab.refreshAtlas()
    tab.focus()
  }

  /** Activate the tab at a 0-based position. */
  activateByIndex(index: number): void {
    const tab = this.tabs[index]
    if (tab) void this.activate(tab)
  }

  /** Close the currently-active tab. */
  closeActiveTab(): void {
    if (this.activeTab) this.closeTab(this.activeTab)
  }

  private refreshIndices(): void {
    this.tabs.forEach((tab, i) => {
      tab.indexLabel.textContent = String(i + 1)
    })
  }

  private readonly onKeydown = (e: KeyboardEvent): void => {
    const mod = e.metaKey || e.ctrlKey
    if (!mod || e.altKey) return

    // Cmd/Ctrl+T — new tab
    if (e.key === 't') {
      e.preventDefault()
      e.stopPropagation()
      this.newTab()
      return
    }

    // Cmd/Ctrl+W — close tab
    if (e.key === 'w') {
      e.preventDefault()
      e.stopPropagation()
      this.closeActiveTab()
      return
    }

    // Cmd/Ctrl+1..9 — switch to tab by visual index
    const keyNum = Number(e.key)
    if (Number.isInteger(keyNum) && keyNum >= 1 && keyNum <= 9 && keyNum <= this.tabs.length) {
      e.preventDefault()
      e.stopPropagation()
      this.activateByIndex(keyNum - 1)
    }
  }
}

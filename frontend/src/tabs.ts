import { WSClient, type SessionHandle } from './ipc'
import { createRenderer, resolveRendererName, type RendererName } from './renderers'
import type { TerminalRenderer } from './renderers/types'

// How long the grid must hold still before the PTY is told about it.
// Dragging a window edge walks the grid through every intermediate size,
// and each one forwarded straight to the PTY is another SIGWINCH — the
// TUI repaints itself from scratch for a size that is already stale,
// dozens of times per drag, which is what shreds the screen. Repaint
// locally as fast as the renderer likes, but only tell the PTY the size
// the drag actually settled on.
const RESIZE_SETTLE_MS = 80

const DEFAULT_RENDERER: RendererName = resolveRendererName()

class Tab {
  readonly id: number

  readonly pane = document.createElement('div')
  readonly button = document.createElement('div')
  readonly closeBtn = document.createElement('button')
  readonly indexLabel = document.createElement('span')
  readonly titleSpan = document.createElement('span')
  /** Active-tab indicator (top bar) / activity indicator (bottom bar) */
  readonly indicator = document.createElement('div')

  private _title = 'Terminal'
  private _hasActivity = false
  private renderer: TerminalRenderer | null = null
  private session: SessionHandle | null = null
  private started = false
  cols = 0
  rows = 0
  private resizeTimer: number | undefined

  constructor(
    private readonly client: WSClient,
    private readonly rendererName: RendererName,
    id: number,
  ) {
    this.id = id

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

    this.titleSpan.className = 'tab-title'
    this.titleSpan.textContent = this._title

    this.closeBtn.className = 'tab-close'
    this.closeBtn.textContent = '×'
    this.closeBtn.setAttribute('aria-label', 'Close tab')

    this.button.append(this.indexLabel, this.titleSpan, this.closeBtn, this.indicator)
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

  updateTitle(title: string): void {
    // Ignore empty or whitespace-only titles (e.g. OSC 0/2 with "" on exit).
    if (!title.trim()) return
    this._title = title
    this.titleSpan.textContent = title
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

      session.onData((data: string) => {
        renderer.write(data)
        // Activity tracking: background tab that receives output
        // gets the activity indicator, cleared when it becomes active.
        if (!this.button.classList.contains('active')) {
          this.markActivity()
        }
      })
      session.onExit((sid: string) => console.log('nocx: session exited:', sid))
      session.onReset(() => renderer.reset())

      renderer.onData((data: string) => session.send(data))
      renderer.onTitle((title: string) => {
        this.updateTitle(title)
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
      console.log(`nocx: tab ready (renderer=${this.rendererName})`, { sid: session.sessionId })
    } catch (err) {
      const notice = document.createElement('pre')
      notice.className = 'pane-error'
      notice.textContent = `Tab ${this.id} failed to start:\n\n${err instanceof Error ? err.message : String(err)}`
      this.pane.replaceChildren(notice)
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
 *  - A background tab receiving output shows an activity indicator,
 *    cleared when that tab is activated.
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
  private readonly addBtn: HTMLButtonElement
  private readonly tabsContainer: HTMLElement

  constructor(bar: HTMLElement, panes: HTMLElement, client: WSClient) {
    this.bar = bar
    this.panes = panes
    this.client = client
    this.rendererName = DEFAULT_RENDERER

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
    this.newTab()

    // Capture phase: the active terminal swallows keys once they reach it.
    window.addEventListener('keydown', this.onKeydown, true)
  }

  get tabCount(): number {
    return this.tabs.length
  }

  /** Create a new tab, activate it. */
  newTab(): Tab {
    const tab = new Tab(this.client, this.rendererName, this.nextTabId++)

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

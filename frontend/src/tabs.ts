import { WSClient } from './ipc'
import { createRenderer, type RendererName } from './renderers'
import type { TerminalRenderer } from './renderers/types'

// The renderer bake-off. Each tab drives its own WebSocket, and the backend
// spawns one PTY per connection (transport.handleSession), so every tab is an
// independent live shell. Run the same command in each and compare.
export interface TabSpec {
  id: RendererName
  label: string
  engine: string
}

// How long the grid must hold still before the PTY is told about it.
const RESIZE_SETTLE_MS = 80

export const TABS: readonly TabSpec[] = [
  { id: 'ghostty', label: 'ghostty-web', engine: 'WASM VT → canvas' },
  { id: 'xterm', label: 'xterm.js', engine: 'WebGL (GPU) → canvas' },
  { id: 'wterm', label: 'wterm', engine: 'DOM nodes (no canvas)' },
]

class Tab {
  readonly pane = document.createElement('div')
  readonly button = document.createElement('button')
  private renderer: TerminalRenderer | null = null
  private started = false
  private failure: string | null = null
  private resizeTimer: number | undefined
  cols = 0
  rows = 0

  constructor(
    readonly spec: TabSpec,
    private readonly port: number,
    private readonly onGrid: (tab: Tab) => void,
  ) {
    this.pane.className = 'pane'
    this.pane.id = `pane-${spec.id}`
    this.pane.setAttribute('role', 'tabpanel')

    this.button.className = 'tab'
    this.button.type = 'button'
    this.button.textContent = spec.label
    this.button.title = spec.engine
    this.button.setAttribute('role', 'tab')
    this.button.setAttribute('aria-controls', this.pane.id)
    this.setActive(false)
  }

  setActive(active: boolean): void {
    this.pane.classList.toggle('active', active)
    this.button.classList.toggle('active', active)
    this.button.setAttribute('aria-selected', String(active))
  }

  // Mounted lazily on first activation, so an unused renderer costs nothing.
  // Panes keep their layout box while inactive (visibility, not display), so
  // the renderer measures a real size the moment it mounts.
  async start(): Promise<void> {
    if (this.started) return
    this.started = true

    try {
      const renderer = createRenderer(this.spec.id)
      await renderer.mount(this.pane)

      const ws = new WSClient()
      await ws.connect(this.port)

      ws.onData((data) => renderer.write(data))
      renderer.onData((data) => ws.send(data))
      // Dragging a window edge walks the grid through every intermediate size,
      // and each one forwarded straight to the PTY is another SIGWINCH — the
      // TUI repaints itself from scratch for a size that is already stale,
      // dozens of times per drag, which is what shreds the screen. Repaint
      // locally as fast as the renderer likes, but only tell the PTY the size
      // the drag actually settled on.
      renderer.onResize((cols, rows) => {
        if (cols === this.cols && rows === this.rows) return
        this.cols = cols
        this.rows = rows
        this.onGrid(this)
        clearTimeout(this.resizeTimer)
        this.resizeTimer = window.setTimeout(() => ws.sendResize(cols, rows), RESIZE_SETTLE_MS)
      })

      this.cols = renderer.cols
      this.rows = renderer.rows
      ws.sendResize(this.cols, this.rows)
      this.renderer = renderer
      this.onGrid(this)
      console.log(`nocx: tab ready (renderer=${this.spec.id})`, { port: this.port })
    } catch (err) {
      // A renderer that cannot start is itself a bake-off result: report it in
      // place instead of taking the whole window down.
      this.failure = err instanceof Error ? err.message : String(err)
      const notice = document.createElement('pre')
      notice.className = 'pane-error'
      notice.textContent = `${this.spec.label} failed to start:\n\n${this.failure}`
      this.pane.replaceChildren(notice)
      this.onGrid(this)
      console.error(`nocx: renderer ${this.spec.id} failed`, err)
    }
  }

  focus(): void {
    this.renderer?.focus()
  }

  status(): string {
    if (this.failure) return `${this.spec.engine} · failed`
    if (!this.renderer) return this.spec.engine
    return `${this.spec.engine} · ${this.cols}×${this.rows}`
  }
}

export class TabManager {
  private readonly tabs = new Map<RendererName, Tab>()
  private readonly status = document.createElement('span')
  private active: Tab | null = null

  constructor(bar: HTMLElement, panes: HTMLElement, port: number) {
    this.status.className = 'status'

    for (const spec of TABS) {
      const tab = new Tab(spec, port, (t) => {
        if (t === this.active) this.renderStatus()
      })
      tab.button.addEventListener('click', () => void this.activate(spec.id))
      bar.append(tab.button)
      panes.append(tab.pane)
      this.tabs.set(spec.id, tab)
    }

    bar.setAttribute('role', 'tablist')
    bar.append(this.status)

    // Capture phase: the active terminal swallows keys once they reach it.
    window.addEventListener('keydown', this.onKeydown, true)
  }

  async activate(name: RendererName): Promise<void> {
    const tab = this.tabs.get(name)
    if (!tab) return
    if (tab === this.active) {
      tab.focus()
      return
    }

    this.active?.setActive(false)
    this.active = tab
    tab.setActive(true)
    this.renderStatus()

    await tab.start()
    tab.focus()
    this.renderStatus()
  }

  private renderStatus(): void {
    this.status.textContent = this.active?.status() ?? ''
  }

  private readonly onKeydown = (e: KeyboardEvent): void => {
    if (!(e.metaKey || e.ctrlKey) || e.altKey) return
    const index = Number(e.key) - 1
    if (!Number.isInteger(index) || index < 0 || index >= TABS.length) return
    e.preventDefault()
    e.stopPropagation()
    void this.activate(TABS[index].id)
  }
}

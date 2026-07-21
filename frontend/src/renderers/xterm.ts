import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebglAddon } from '@xterm/addon-webgl'
import { CanvasAddon } from '@xterm/addon-canvas'
import { Unicode11Addon } from '@xterm/addon-unicode11'
import '@xterm/xterm/css/xterm.css'
import { FONT_FAMILY, FONT_SIZE, LINE_HEIGHT } from './font'
import type { DataCallback, ResizeCallback, TitleCallback, TerminalRenderer } from './types'

// xterm.js (VS Code's engine, stable 5.x) with the WebGL (GPU) renderer,
// hardened the way Tabby runs it: recover from a lost GPU context and clear the
// glyph atlas on every reflow. WebGL → Canvas → built-in DOM as fallbacks.

const MAX_WEBGL_RECOVERY_ATTEMPTS = 3
const RESIZE_MIN_INTERVAL = 32

export class XtermRenderer implements TerminalRenderer {
  private term: Terminal | null = null
  private fit: FitAddon | null = null
  private webgl?: WebglAddon
  private canvas?: CanvasAddon
  private container: HTMLElement | null = null
  private recoveryAttempts = 0

  async mount(container: HTMLElement): Promise<void> {
    this.container = container

    const term = new Terminal({
      fontFamily: FONT_FAMILY,
      fontSize: FONT_SIZE,
      lineHeight: LINE_HEIGHT,
      allowProposedApi: true,
      smoothScrollDuration: 120,
      scrollback: 10000,
      theme: {
        background: '#1a1b26',
        foreground: '#c0caf5',
      },
    })
    this.term = term

    const fit = new FitAddon()
    this.fit = fit
    term.loadAddon(fit)
    term.loadAddon(new Unicode11Addon())
    term.unicode.activeVersion = '11'

    term.open(container)

    await document.fonts?.ready
    this.attachWebGL()
    this.safeFit()

    let pending = false
    let last = 0
    const run = () => {
      pending = false
      last = Date.now()
      this.safeFit()
      this.webgl?.clearTextureAtlas()
      this.canvas?.clearTextureAtlas()
    }
    new ResizeObserver(() => {
      if (pending) return
      pending = true
      const wait = Math.max(0, RESIZE_MIN_INTERVAL - (Date.now() - last))
      if (wait > 0) setTimeout(() => requestAnimationFrame(run), wait)
      else requestAnimationFrame(run)
    }).observe(container)
  }

  private safeFit(): void {
    const c = this.container
    if (c && c.clientWidth > 0 && c.clientHeight > 0) {
      try {
        this.fit?.fit()
      } catch {
        /* transient mid-layout measure */
      }
    }
  }

  private attachWebGL(): void {
    if (!this.term) return
    try {
      const addon = new WebglAddon()
      addon.onContextLoss(() => this.onContextLoss())
      this.term.loadAddon(addon)
      this.webgl = addon
    } catch {
      this.attachCanvas()
    }
  }

  private attachCanvas(): void {
    if (!this.term || this.canvas) return
    try {
      const addon = new CanvasAddon()
      this.term.loadAddon(addon)
      this.canvas = addon
    } catch {
      /* fall through to xterm's built-in DOM renderer */
    }
  }

  private onContextLoss(): void {
    this.webgl?.dispose()
    this.webgl = undefined
    const recoverable =
      !!this.container && this.container.offsetParent !== null && document.hasFocus()
    if (this.recoveryAttempts < MAX_WEBGL_RECOVERY_ATTEMPTS && recoverable) {
      this.recoveryAttempts++
      this.attachWebGL()
    } else {
      this.attachCanvas()
    }
  }

  write(data: string): void {
    this.term?.write(data)
  }

  reset(): void {
    this.term?.reset()
  }

  onData(cb: DataCallback): void {
    this.term?.onData(cb)
  }

  onResize(cb: ResizeCallback): void {
    this.term?.onResize(({ cols, rows }) => cb(cols, rows))
  }

  onTitle(cb: TitleCallback): void {
    this.term?.onTitleChange(cb)
  }

  refreshAtlas(): void {
    this.webgl?.clearTextureAtlas()
    this.canvas?.clearTextureAtlas()
    if (this.term) {
      this.term.refresh(0, this.term.rows - 1)
    }
  }

  focus(): void {
    this.term?.focus()
  }

  get cols(): number {
    return this.term?.cols ?? 80
  }

  get rows(): number {
    return this.term?.rows ?? 24
  }
}

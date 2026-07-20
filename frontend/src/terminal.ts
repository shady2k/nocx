import { init, Terminal as GhosttyTerminal, FitAddon } from 'ghostty-web'

export type ResizeCallback = (cols: number, rows: number) => void
export type DataCallback = (data: string) => void

export type WheelHandler = (event: WheelEvent) => boolean

function disableCanvasSmoothing(container: HTMLElement): void {
  const canvas = container.querySelector('canvas')
  if (!canvas) return
  const ctx = canvas.getContext('2d')
  if (ctx) ctx.imageSmoothingEnabled = false
}

export class Terminal {
  private term: GhosttyTerminal | null = null
  private fitAddon: FitAddon | null = null
  private container: HTMLElement | null = null

  async mount(container: HTMLElement): Promise<void> {
    this.container = container
    await init()

    this.term = new GhosttyTerminal({
      fontSize: 14,
      theme: {
        background: '#1a1b26',
        foreground: '#c0caf5',
      },
    })

    this.fitAddon = new FitAddon()
    this.term.loadAddon(this.fitAddon)
    this.term.open(container)

    requestAnimationFrame(() => {
      this.fitAddon?.fit()
      this.clampRows()
      disableCanvasSmoothing(container)
    })
  }

  write(data: string): void {
    this.term?.write(data)
  }

  onData(cb: DataCallback): void {
    this.term?.onData(cb)
  }

  onResize(cb: ResizeCallback): void {
    this.term?.onResize(({ cols, rows }) => cb(cols, rows))
  }

  fit(): void {
    this.fitAddon?.fit()
    this.clampRows()
    if (this.container) disableCanvasSmoothing(this.container)
  }

  private clampRows(): void {
    if (!this.term || !this.container) return
    const canvas = this.container.querySelector('canvas')
    if (!canvas) return
    const renderer = this.term.renderer
    if (!renderer) return
    const metrics = renderer.getMetrics?.()
    if (!metrics || metrics.height === 0) return

    const containerHeight = this.container.clientHeight
    const canvasHeight = canvas.clientHeight

    if (canvasHeight > containerHeight) {
      const maxRows = Math.floor(containerHeight / metrics.height)
      if (maxRows > 0 && maxRows < this.rows) {
        this.term.resize(this.cols, maxRows)
        disableCanvasSmoothing(this.container)
      }
    }
  }

  get cols(): number {
    return this.term?.cols ?? 80
  }

  get rows(): number {
    return this.term?.rows ?? 24
  }

  get isAlternateScreen(): boolean {
    return this.term?.buffer.active.type === 'alternate'
  }

  get hasMouseTracking(): boolean {
    return this.term?.wasmTerm?.hasMouseTracking() ?? false
  }

  onBufferChange(cb: () => void): void {
    this.term?.buffer.onBufferChange(cb)
  }

  attachCustomWheelEventHandler(handler?: WheelHandler): void {
    this.term?.attachCustomWheelEventHandler(handler)
  }
}

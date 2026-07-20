import { init, Terminal as GhosttyTerminal, FitAddon } from 'ghostty-web'

export type ResizeCallback = (cols: number, rows: number) => void
export type DataCallback = (data: string) => void

// Thin wrapper over ghostty-web, used the canonical way (see the library README):
// `new Terminal` + `FitAddon` + `open`. The library owns sizing, DPR and rendering —
// we deliberately do NOT hand-tweak the canvas, devicePixelRatio, imageSmoothing or
// wheel behaviour. Every past attempt to "improve" those fought the library and made
// things worse (blur / broken line-wrap / scroll snap).
export class Terminal {
  private term: GhosttyTerminal | null = null
  private fitAddon: FitAddon | null = null

  async mount(container: HTMLElement): Promise<void> {
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

    // Initial fit, then let the addon auto-fit whenever the container resizes.
    requestAnimationFrame(() => this.fitAddon?.fit())
    this.fitAddon.observeResize()
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

  get cols(): number {
    return this.term?.cols ?? 80
  }

  get rows(): number {
    return this.term?.rows ?? 24
  }
}

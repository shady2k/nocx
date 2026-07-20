import { init, Terminal as GhosttyTerminal, FitAddon } from 'ghostty-web'

export type ResizeCallback = (cols: number, rows: number) => void
export type DataCallback = (data: string) => void

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

    requestAnimationFrame(() => this.fitAddon?.fit())
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
  }

  get cols(): number {
    return this.term?.cols ?? 80
  }

  get rows(): number {
    return this.term?.rows ?? 24
  }
}

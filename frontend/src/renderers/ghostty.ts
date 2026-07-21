import { init, Terminal as GhosttyTerminal, FitAddon } from 'ghostty-web'
import { FONT_FAMILY, FONT_SIZE } from './font'
import type { DataCallback, ResizeCallback, TerminalRenderer } from './types'

// Original ghostty-web (WASM VT) renderer, kept as the baseline so it stays
// switchable via ?r=ghostty. Its blur was a canvas/DPR issue, not a font one —
// the shared font just clears the tofu glyphs, it will not un-blur the canvas.
export class GhosttyRenderer implements TerminalRenderer {
  private term: GhosttyTerminal | null = null
  private fitAddon: FitAddon | null = null

  async mount(container: HTMLElement): Promise<void> {
    await init()

    this.term = new GhosttyTerminal({
      fontFamily: FONT_FAMILY,
      fontSize: FONT_SIZE,
      theme: {
        background: '#1a1b26',
        foreground: '#c0caf5',
      },
    })

    this.fitAddon = new FitAddon()
    this.term.loadAddon(this.fitAddon)
    this.term.open(container)

    requestAnimationFrame(() => this.fitAddon?.fit())
    this.fitAddon.observeResize()
  }

  write(data: string): void {
    this.term?.write(data)
  }

  // No response pumping needed here: ghostty-web's writeInternal() already
  // calls processTerminalResponses() after every write, so DSR 5/6 replies
  // reach us through onData like any other input.
  onData(cb: DataCallback): void {
    this.term?.onData(cb)
  }

  onResize(cb: ResizeCallback): void {
    this.term?.onResize(({ cols, rows }) => cb(cols, rows))
  }

  focus(): void {
    ;(this.term as { focus?: () => void } | null)?.focus?.()
  }

  get cols(): number {
    return this.term?.cols ?? 80
  }

  get rows(): number {
    return this.term?.rows ?? 24
  }
}

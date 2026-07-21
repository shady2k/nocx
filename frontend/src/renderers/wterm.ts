import { WTerm } from '@wterm/dom'
import '@wterm/dom/css'
import { FONT_FAMILY, FONT_SIZE, LINE_HEIGHT } from './font'
import type { DataCallback, ResizeCallback, TitleCallback, TerminalRenderer } from './types'

// DOM-rendering candidate (vercel-labs/wterm). Text is real DOM nodes rendered
// by the browser's native font engine, so there is no canvas/DPR blur to fight.
// Uses the built-in lite Zig/WASM core (inlined, ~12KB — no asset to serve).
export class WtermRenderer implements TerminalRenderer {
  private term: WTerm | null = null
  private dataCb: DataCallback | null = null
  private resizeCb: ResizeCallback | null = null
  private titleCb: TitleCallback | null = null

  async mount(container: HTMLElement): Promise<void> {
    // @wterm/dom has no fontFamily option — it measures the cell from the
    // element's computed style, so set the shared font on the container.
    container.style.fontFamily = FONT_FAMILY
    container.style.fontSize = `${FONT_SIZE}px`
    container.style.lineHeight = String(LINE_HEIGHT)

    const term = new WTerm(container, {
      cols: 80,
      rows: 24,
      autoResize: true, // ResizeObserver-driven fit, like xterm's FitAddon
      onData: (data) => this.dataCb?.(data),
      onTitle: (title) => this.titleCb?.(title),
      onResize: (cols, rows) => this.resizeCb?.(cols, rows),
    })
    await term.init()
    this.term = term
  }

  write(data: string): void {
    this.term?.write(data)
  }

  reset(): void {
    // WTerm has no explicit reset() method; RIS (ESC c) is the
    // equivalent full terminal reset, clearing the screen, scrollback,
    // and all modes.
    this.term?.write('\x1bc')
  }

  onData(cb: DataCallback): void {
    this.dataCb = cb
  }

  onResize(cb: ResizeCallback): void {
    this.resizeCb = cb
  }

  onTitle(cb: TitleCallback): void {
    this.titleCb = cb
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

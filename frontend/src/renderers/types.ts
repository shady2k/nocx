// Renderer-agnostic terminal contract. The backend (PTY over WS) is renderer-
// agnostic, so any VT frontend just needs to satisfy this small surface:
// write PTY output in, emit user input out, and report its grid size.
export type DataCallback = (data: string) => void
export type ResizeCallback = (cols: number, rows: number) => void

export interface TerminalRenderer {
  mount(container: HTMLElement): Promise<void>
  write(data: string): void

  // reset performs a full terminal reset: clears the display, scrollback,
  // cursor position, character sets, modes (alt-screen, mouse tracking,
  // scroll region), and any other state. It is called when a reattach
  // returns {reset:true}, meaning the client fell out of the output ring
  // and terminal state is unknown — continuing with stale state would
  // render garbage over the resynced stream.
  reset(): void

  onData(cb: DataCallback): void
  onResize(cb: ResizeCallback): void
  focus(): void
  readonly cols: number
  readonly rows: number
}

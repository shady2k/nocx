// Renderer-agnostic terminal contract. The backend (PTY over WS) is renderer-
// agnostic, so any VT frontend just needs to satisfy this small surface:
// write PTY output in, emit user input out, and report its grid size.
export type DataCallback = (data: string) => void
export type ResizeCallback = (cols: number, rows: number) => void

// onTitle fires when the shell emits OSC 0 or OSC 2 (tab/window title).
// The title is untrusted shell output — the caller must set it via
// textContent, never innerHTML, and truncate with CSS, never by cutting
// the string.
export type TitleCallback = (title: string) => void

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
  // Register a callback for shell-originated title changes (OSC 0/2).
  // Some renderers (xterm.js) fire this from the terminal engine;
  // others may leave it unfired if the engine does not expose an
  // equivalent event. The fallback title is set by the tab bar.
  onTitle(cb: TitleCallback): void
  // refreshAtlas is called when the renderer becomes visible after being
  // hidden (e.g. tab switch). xterm.js's WebGL texture atlas goes stale
  // while hidden; this gives the renderer a chance to clear and repaint.
  refreshAtlas(): void
  focus(): void
  readonly cols: number
  readonly rows: number
}

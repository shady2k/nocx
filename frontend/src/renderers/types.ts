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

// CwdEvent carries the percent-decoded OSC 7 payload (AD-5).
// host is empty for local shells; path is an absolute filesystem path.
export interface CwdEvent {
  host: string
  path: string
}

// CwdCallback fires when the shell emits OSC 7 (current working directory).
// Per AD-6, the VT frontend parses OSC 7 via parser.registerOscHandler and
// surfaces it as an event — the backend never sniffs the byte stream.
export type CwdCallback = (event: CwdEvent) => void

// CommandMarker carries an OSC 133 command boundary marker.
// A = prompt start, B = prompt end, C = command output start,
// D = command finished (with optional exit code).
export interface CommandMarker {
  kind: 'A' | 'B' | 'C' | 'D'
  exitCode?: number
}

// CommandMarkerEvent enriches the OSC 133 marker with a cursor snapshot
// (absolute line, column, active buffer) taken at parse time. xterm.js only;
// @wterm/dom never fires.
export interface CommandMarkerEvent extends CommandMarker {
  line: number
  col: number
  buffer: 'normal' | 'alternate'
}

// CommandMarkerCallback fires when the shell emits OSC 133.
// The VT frontend parses OSC 133 via parser.registerOscHandler and surfaces
// each enriched marker as an event — the backend never sniffs the byte stream.
export type CommandMarkerCallback = (event: CommandMarkerEvent) => void

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

  // onBufferChange fires whenever the active screen buffer changes (normal ↔
  // alternate). xterm.js fires this from buffer.onBufferChange with the new
  // buffer type. @wterm/dom has no equivalent event — the callback is never
  // fired; callers must assume 'normal'.
  //
  // This event-driven approach is preferred over a polling getter because
  // xterm.js Terminal.buffer is a lazy-initialized getter that wraps an
  // internal _core reference, and accessing it through vite/esbuild's dev
  // transform can produce incorrect results when multiple Terminal instances
  // exist (each tab has its own). onBufferChange is a first-class xterm.js
  // API that fires reliably regardless of how the getter chain resolves.
  onBufferChange(cb: (type: 'normal' | 'alternate') => void): void

  // onCwd registers a callback that fires when the shell emits OSC 7
  // (current working directory). The VT frontend parses the OSC sequence
  // and percent-decodes host + path; the caller updates the tab title and
  // tooltip. xterm.js supports this via parser.registerOscHandler(7, ...);
  // @wterm/dom does not expose an OSC handler — the callback is never fired.
  onCwd(cb: CwdCallback): void

  // onCommandMarker registers a callback that fires when the shell emits
  // OSC 133 command boundary markers (A/B/C/D). The VT frontend parses the
  // OSC sequence and extracts the marker kind and optional exit code.
  // xterm.js supports this via parser.registerOscHandler(133, ...);
  // @wterm/dom does not expose an OSC handler — the callback is never fired.
  onCommandMarker(cb: CommandMarkerCallback): void

  // onBell registers a callback that fires when the terminal receives BEL
  // (\x07). Bell always deserves attention regardless of buffer, so the
  // tab bar always lights the activity indicator on bell. xterm.js fires
  // this natively; @wterm/dom does not expose a bell event — callers that
  // need it must use xterm.js.
  onBell(cb: () => void): void

  // onSelectionChange fires when the user completes a selection gesture in
  // the terminal, not per cell or per boundary movement. The callback
  // receives the current selection text (via getSelection()). An empty
  // string means the selection was cleared. @wterm/dom has no selection —
  // never fired.
  //
  // The renderer reports facts and never touches the clipboard (AD-6).
  // Copy-on-select policy lives above the renderer boundary.
  onSelectionChange(cb: (text: string) => void): void

  // onClipboardWrite fires when a program emits OSC 52 to place text on the
  // clipboard. The renderer decodes the OSC 52 payload and fires the
  // callback with the decoded text. @wterm/dom has no OSC handler — never
  // fired.
  //
  // The renderer reports the decoded text and never touches the clipboard
  // (AD-6). OSC 52 policy (notification, clipboard write) lives above the
  // renderer boundary.
  onClipboardWrite(cb: (text: string) => void): void

  // paste inserts text at the cursor, preserving bracketed-paste semantics
  // when the running program has enabled mode 2004. Implemented via
  // xterm.js's term.paste() so the engine owns the wrapping — hand-rolling
  // it would duplicate engine behaviour and drift from it.
  paste(text: string): void

  // refreshAtlas is called when the renderer becomes visible after being
  // hidden (e.g. tab switch). xterm.js's WebGL texture atlas goes stale
  // while hidden; this gives the renderer a chance to clear and repaint.
  refreshAtlas(): void

  /** When readOnly, the terminal ignores keyboard input but text selection
   *  still works. Used when the DOM editor owns input at a prompt. */
  setReadOnly(readOnly: boolean): void

  focus(): void
  readonly cols: number
  readonly rows: number

  // dispose releases renderer-held resources (timers, listeners). Called when
  // the tab owning this renderer is closed so a periodic forced-refresh pump
  // does not outlive the terminal it paints.
  dispose(): void

  // ── Marker/geometry API (ADR-0008 command-ledger gutter) ────────────────
  // These are optional — the gutter feature-detects and skips when absent.

  /**
   * Register a marker at the current cursor row. Returns an adapter that
   * exposes the live marker line, an onDispose callback (fired when scrollback
   * trims the line), and a dispose method. Returns undefined when the renderer
   * does not support markers (e.g. wterm).
   */
  registerMarker?(): MarkerAdapter | undefined

  /** Measured cell height in pixels, from the actual rendered char element.
   *  Falls back to fontSize * lineHeight only if measurement is unavailable. */
  readonly cellHeight?: number

  /** Absolute buffer line index at the top of the visible viewport.
   *  = buffer.active.baseY + buffer.active.viewportY in xterm terms. */
  readonly viewportTopLine?: number

  /** Subscribe to scroll events. Fires with the new viewportY (scroll offset). */
  onScroll?(cb: (viewportY: number) => void): void

  /** Subscribe to render events. Fires whenever viewport content is painted. */
  onRender?(cb: (range: { start: number; end: number }) => void): void

  /** The DOM element the renderer mounted into — the gutter overlays it. */
  readonly paneElement?: HTMLElement
}

/** Adapter over an xterm IMarker, exposing only what the gutter needs. */
export interface MarkerAdapter {
  /** The marker's current absolute buffer line, or undefined if disposed. */
  readonly line: () => number | undefined
  /** Fires when the marker is disposed (scrollback trim). */
  readonly onDispose: (cb: () => void) => void
  /** Dispose the marker. Idempotent. */
  dispose(): void
}

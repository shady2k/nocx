import type { ITheme } from '@xterm/xterm'

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
  // nocxEnv is the OSC 133 `nocx_env=<id>` parameter carried by a marker
  // tagged by an identified environment (spec §5.2). Untagged markers carry
  // none and still drive block boundaries exactly as before.
  nocxEnv?: string
}

// CommandMarkerEvent enriches the OSC 133 marker with a cursor snapshot
// (absolute line, column, active buffer) taken at parse time.
export interface CommandMarkerEvent extends CommandMarker {
  line: number
  col: number
  buffer: 'normal' | 'alternate'
}

// CommandMarkerCallback fires when the shell emits OSC 133.
// The VT frontend parses OSC 133 via parser.registerOscHandler and surfaces
// each enriched marker as an event — the backend never sniffs the byte stream.
export type CommandMarkerCallback = (event: CommandMarkerEvent) => void

// RenderFenceEvent — the ADR-0024 §7 carve-out rendezvous: the shell writes
// ESC]1337;NOCX_FENCE;<64hex> BEL to the pty AFTER a command's output, and
// carries the same 64 hex chars in the authenticated `complete` event. The
// renderer parses the OSC and reports WHERE the fence landed (render-only —
// a fence carries no authority; see ADR-0024 decision 1). The block model
// matches the fence hex against the authenticated completion to freeze the
// block at the true output end instead of truncating the in-flight tail.
export interface RenderFenceEvent {
  /** 64 lowercase hex chars — the nonce the shell generated at completion. */
  hex: string
  /** Absolute buffer line the fence sequence was parsed on. The command's
   *  last output byte is on this line or the one above it. */
  line: number
  /** Active buffer at parse time. A fence in the alternate buffer has no
   *  scrollback line to serialize — the consumer ignores it. */
  buffer: 'normal' | 'alternate'
}

export type RenderFenceCallback = (event: RenderFenceEvent) => void

export interface TerminalRenderer {
  mount(container: HTMLElement): Promise<void>
  /**
   * Fit the renderer to an explicit viewport delivered by the presentation
   * layer (B.5). The renderer computes cols/rows from its real cell metrics
   * and the given CSS-pixel dimensions. It MUST NOT independently measure
   * container geometry.
   */
  fitViewport(viewport: { width: number; height: number }): void

  /**
   * Write PTY output into the terminal. The write is QUEUED, not applied:
   * parsing is async, so hasUnsettledWrite() stays true until the bytes
   * have been parsed (the capture fence).
   *
   * MAY THROW when the underlying terminal refuses the write under flow
   * control (xterm's pending-data watermark): nothing was queued, and the
   * fence counter is repaired before the throw propagates. The refusal is
   * surfaced rather than swallowed because the caller believes it
   * delivered bytes that are gone — only the caller can decide the policy
   * (nocx-x8s2.3).
   */
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
  // The fallback title is set by the tab bar.
  onTitle(cb: TitleCallback): void

  // onBufferChange fires whenever the active screen buffer changes (normal ↔
  // alternate).
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
  // tooltip.
  onCwd(cb: CwdCallback): void

  // onCommandMarker registers a callback that fires when the shell emits
  // OSC 133 command boundary markers (A/B/C/D). The VT frontend parses the
  // OSC sequence and extracts the marker kind, optional exit code and the
  // nocx_env tag when the marker is tagged.
  onCommandMarker(cb: CommandMarkerCallback): void

  // onRenderFence registers a callback that fires when the shell emits the
  // private render fence (OSC 1337 NOCX_FENCE — ADR-0024 §7 carve-out).
  // Parse-and-report only: the renderer says where the fence landed; the
  // consumer matches it against the authenticated completion. Optional so a
  // renderer that does not parse fences degrades to the documented
  // no-fence deferral instead of failing to mount.
  onRenderFence?(cb: RenderFenceCallback): void
  // onBell registers a callback that fires when the terminal receives BEL
  // (\x07). Bell always deserves attention regardless of buffer, so the
  // tab bar always lights the activity indicator on bell.
  onBell(cb: () => void): void

  // onSelectionChange fires when the user completes a selection gesture in
  // the terminal, not per cell or per boundary movement. The callback
  // receives the current selection text (via getSelection()). An empty
  // string means the selection was cleared.
  //
  // The renderer reports facts and never touches the clipboard (AD-6).
  // Copy-on-select policy lives above the renderer boundary.
  onSelectionChange(cb: (text: string) => void): void

  // onClipboardWrite fires when a program emits OSC 52 to place text on the
  // clipboard. The renderer decodes the OSC 52 payload and fires the
  // callback with the decoded text.
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

  /**
   * Apply a new palette to the terminal controller. Sets term.options.theme
   * and performs whatever refresh / atlas invalidation is needed for the new
   * palette to actually appear — on WebKitGTK this is a full viewport refresh
   * on top of the 42ms pump (ADR-0005).
   *
   * Belongs to the imperative terminal controller, not to a Solid effect.
   */
  applyTheme(theme: ITheme): void

  /** When readOnly, the terminal ignores keyboard input but text selection
   *  still works. Used when the DOM editor owns input at a prompt. */
  setReadOnly(readOnly: boolean): void

  focus(): void
  readonly cols: number
  readonly rows: number

  /**
   * CSS pixels the written rows of the current viewport actually occupy.
   *
   * The presentation layer sizes the live region from this instead of from a
   * constant, so three lines of `ls` get three lines and a program repainting a
   * whole screen gets the whole screen. `0` when the grid is empty or the
   * renderer cannot measure a cell yet — the caller keeps its previous height
   * rather than collapsing.
   */
  liveContentHeight(): number

  // dispose releases renderer-held resources (timers, listeners). Called when
  // the tab owning this renderer is closed so a periodic forced-refresh pump
  // does not outlive the terminal it paints.
  dispose(): void

  // ── Marker/geometry API (ADR-0008 command-ledger gutter) ────────────────

  /**
   * Register a marker at the current cursor row. Returns an adapter that
   * exposes the live marker line, an onDispose callback (fired when scrollback
   * trims the line), and a dispose method.
   */
  registerMarker(): MarkerAdapter | undefined

  /** CSS-pixel pitch of one grid row — what the renderer DRAWS at, the same
   *  source as `cellWidth`. Falls back to fontSize * lineHeight, uncached,
   *  while nothing has been drawn yet: the caller gets the size the grid was
   *  asked for rather than a stale answer to a question with no answer. */
  readonly cellHeight: number

  /** CSS-pixel width of one grid cell — xterm's real cell advance, snapped
   *  to whole device pixels (the same source FitAddon fits to). 0 while the
   *  renderer cannot measure (not yet mounted, no layout). The frozen block
   *  layout publishes this so N columns of frozen output occupy exactly
   *  N × cellWidth (nocx-yy9g).
   */
  readonly cellWidth: number

  /**
   * Subscribe to "the cell dimensions MAY have changed" — fired at mount
   *  (after the fonts load), on grid resize and on device-pixel-ratio
   *  change, the three places xterm re-measures its char size. The
   *  scrollback re-publishes the cell metric to the frozen block layout on
   *  this. Optional so a renderer that never re-measures degrades to the
   *  single publish the scrollback does at construction.
   */
  onCellDimsChange?(cb: () => void): void

  /** Absolute buffer line index at the top of the visible viewport.
   *  = buffer.active.baseY + buffer.active.viewportY in xterm terms. */
  readonly viewportTopLine: number

  /** Subscribe to scroll events. Fires with the new viewportY (scroll offset). */
  onScroll(cb: (viewportY: number) => void): void

  /** Subscribe to render events. Fires whenever viewport content is painted. */
  onRender(cb: (range: { start: number; end: number }) => void): void

  // ── Frame capture surface (nocx-3j9b) ───────────────────────────────

  // onWriteParsed fires after a written chunk has been parsed into the
  // buffer. It is the frame generation's advance signal AND the capture
  // fence: write() queues parsing, so a snapshot taken mid-queue can hold
  // row 1 from before a write and row 20 from after it. Note xterm fires it
  // at the end of EVERY parse pass — BETWEEN chunks of a large write — so
  // hasUnsettledWrite() distinguishes "settled" from "chunk done".
  onWriteParsed(cb: () => void): void

  // onClear/onReset fire AFTER the renderer executed a full clear
  // (clearViewport) or a full reset — the explicit state-changing
  // operations that advance the frame generation alongside onWriteParsed.
  onClear(cb: () => void): void
  onReset(cb: () => void): void

  /** True while bytes queued via write() have not finished parsing — the
   *  capture fence. The per-write settle is tracked via write()'s callback,
   *  so this is exact even when onWriteParsed fires mid-write. */
  hasUnsettledWrite(): boolean

  /** The DOM element the renderer mounted into — the gutter overlays it. */
  readonly paneElement: HTMLElement

  /**
   * For DOM scrollback serialization. Returns the active buffer line at
   * the given absolute index. The returned object satisfies xterm's
   * IBufferLine interface for length, getCell(), isWrapped.
   */
  getBufferLine(line: number): import('@xterm/xterm').IBufferLine | undefined
  /** Absolute buffer line of the cursor — the line the next write lands on. */
  cursorLine(): number
  /** Column of the cursor — the column the next write lands on. */
  cursorCol(): number

  /**
   * Clear the visible xterm viewport. Used after freezing a block, so the
   * rows the block's DOM element now owns do not stay in the grid and get
   * re-displayed by the live region (nocx-m87n). The underlying
   * `Terminal.clear()` clears the whole buffer — "making the prompt line
   * the new first line" — which is exactly what the DOM block model
   * wants: the DOM owns the scrollback now, and the grid only ever holds
   * the running command's rows.
   */
  clearViewport(): void
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

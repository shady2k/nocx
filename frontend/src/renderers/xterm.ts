import { Terminal, type ITheme } from '@xterm/xterm'
import { WebglAddon } from '@xterm/addon-webgl'
import { CanvasAddon } from '@xterm/addon-canvas'
import { Unicode11Addon } from '@xterm/addon-unicode11'
import '@xterm/xterm/css/xterm.css'
import { FONT_FAMILY, FONT_SIZE, LINE_HEIGHT } from './font'
import type {
  CommandMarker,
  CommandMarkerCallback,
  CommandMarkerEvent,
  CwdCallback,
  DataCallback,
  MarkerAdapter,
  NotificationRequestCallback,
  RenderFenceCallback,
  RenderFenceEvent,
  ResizeCallback,
  TitleCallback,
  TerminalRenderer,
} from './types'
import { getCurrentTheme, subscribeThemeChanges } from './theme-adapter'
import { WORD_SEPARATORS } from '../word-selection'
import { decodeOsc52 } from '../clipboard'
import { CommandSnapshotStore } from '../command-snapshot'
import {
  CaptureAbortedError,
  CaptureIdentityTracker,
  ReadScreenRangeError,
} from '../frame/capture-identity'
import { mintLiveFrame } from '../frame/mint'
import type { CapturedFrame } from '../frame/types'
import { fromITheme } from '../scrollback/serializer'
import { isSnippetChord } from '../snippets/chord'
import { parseOscNotification } from '../osc-notification'
type BellCallback = () => void
type SelectionCallback = (text: string) => void
type ClipboardWriteCallback = (text: string) => void

// xterm.js (VS Code's engine, stable 5.x) with the WebGL (GPU) renderer,
// hardened the way Tabby runs it: recover from a lost GPU context and clear the
// glyph atlas on every reflow. WebGL → Canvas → built-in DOM as fallbacks.

const MAX_WEBGL_RECOVERY_ATTEMPTS = 3

// The readability floor applied to every cell (nocx-3lrm). WCAG AA; xterm's
// own default is 1, documented as "do nothing". See the Terminal options for
// why a floor is needed at all.
const MINIMUM_CONTRAST_RATIO = 4.5

// On WebKitGTK (Linux/Wails) the compositor may not present a frame until the
// window receives a user interaction, so xterm.js's rAF-scheduled repaint of
// the just-written data never runs — the initial shell prompt stays invisible
// until a click, and each typed character renders one frame behind (the last
// one never painted). A periodic timer that re-marks every row dirty forces a
// render attempt on each tick, keeping the buffer visible without any click.
// ~24 fps is smooth enough for terminal output and cheap (a no-op refresh when
// nothing changed costs little). Only active on Linux/WebKitGTK — on macOS
// (WKWebView) and in browsers the compositor is healthy and the pump is a
// waste of CPU.
const FORCED_REFRESH_MS = 42

// ── Shift+Enter must reach the program as its own chord (nocx-nt70) ──────
// A program that owns the keyboard cannot tell Shift+Enter from Enter: xterm
// encodes both as a bare CR (\r) and drops the modifier. There are two
// conventions for fixing that:
//
//  1. Legacy ESC CR — Shift+Enter sends ESC followed by CR (\x1b\r). One
//     mapping, no negotiation, understood by the editors that historically
//     adopted it. It fixes exactly this one chord and nothing else.
//  2. A negotiated modifier encoding — xterm's modifyOtherKeys or the kitty
//     keyboard protocol — where the program asks for the mode and every
//     modified chord then arrives as an explicit CSI-u sequence. xterm.js
//     5.5.0 ships neither (verified: no modifyOtherKeys option, no kitty
//     protocol in the bundle), so the mode-set handshake and the key table
//     would be hand-rolled beside the library — the shape this repo avoids —
//     and the default state would need a byte-identity test of its own.
//
// Chosen: legacy ESC CR. xterm's own hook for "a key xterm must not process"
// (attachCustomKeyEventHandler) is the existing answer for exactly this, and
// the alternative buys nothing for the one reported chord while shipping a
// hand-written key table. The cost is named: Alt+Enter already encodes as
// ESC CR in xterm, so a program still cannot distinguish Shift+Enter from
// Alt+Enter; Ctrl+Enter still sends a bare CR; Shift+Tab already arrives as
// ESC [ Z and is untouched. If chords beyond Shift+Enter are ever needed,
// the negotiated protocol is the upgrade path, and its first test must be
// the default-state byte identity this hook preserves.
const SHIFT_ENTER_SEQUENCE = '\x1b\r'

function isLinuxWebKit(): boolean {
  if (typeof navigator === 'undefined') return false
  // Wails on Linux embeds a WebKitGTK webview. The platform is Linux and the
  // user agent carries "WebKit". macOS uses WKWebView (platform is not Linux).
  return /linux/i.test(navigator.platform) && /webkit/i.test(navigator.userAgent)
}

// ── OSC 7 parser (AD-6: frontend parses OSC, backend never sniffs) ──────

// OSC 7 format: ESC ] 7 ; file://host/path ST
// xterm.js parser.registerOscHandler(7, handler) gives us the string
// after the ';', i.e. 'file://host/path'. Percent-decode per RFC 3986.
const OSC7_PREFIX = 'file://'

/**
 * Parses an OSC 7 payload into {host, path}. Returns null when the payload
 * does not start with 'file://' or percent-decoding fails.
 */
export function parseOsc7(payload: string): { host: string; path: string } | null {
  if (!payload.startsWith(OSC7_PREFIX)) return null
  const uri = payload.slice(OSC7_PREFIX.length)

  // Split at the first '/' after the authority section.
  // file://host/path  → host, /path
  // file:///path      → '',  /path
  const slashIdx = uri.indexOf('/')
  if (slashIdx === -1) return null

  const rawHost = uri.slice(0, slashIdx)
  const rawPath = uri.slice(slashIdx)

  try {
    const host = decodeURIComponent(rawHost)
    const path = decodeURIComponent(rawPath)
    return { host, path }
  } catch {
    // decodeURIComponent throws on malformed percent-encoding (e.g. '%ZZ').
    return null
  }
}

/**
 * Parses an OSC 133 payload into a CommandMarker. Returns null for invalid
 * or unrecognized payloads.
 *
 * Format: 'A' | 'B' | 'C' | 'D' | 'D;<exitcode>', optionally followed by
 * `;key=value` parameters — the parameter form OSC 133 already permits. A
 * `nocx_env=<id>` parameter tags the marker (spec §5.2); an untagged marker
 * keeps driving block boundaries exactly as before. A tag that is present
 * but malformed makes the whole marker invalid (never guessed at), while an
 * absent tag and unknown well-formed keys are tolerated.
 */
const OSC133_TAG_KEY = 'nocx_env'
const OSC133_TAG_VALUE_RE = /^[A-Za-z0-9._-]{1,64}$/

export function parseOsc133(payload: string): CommandMarker | null {
  if (payload.length === 0) return null
  const kind = payload[0] as CommandMarker['kind']
  if (kind !== 'A' && kind !== 'B' && kind !== 'C' && kind !== 'D') return null

  const marker: CommandMarker = { kind }
  if (payload.length === 1) return marker
  if (payload[1] !== ';') return marker // bare kind with trailing junk: unchanged

  // Everything after the kind is `;`-separated parameters. D's first
  // parameter is the positional exit code UNLESS it is itself a key=value
  // property (`D;nocx_env=id` has no exit code).
  const params = payload.slice(2).split(';')
  let i = 0
  if (kind === 'D' && params.length > 0 && params[0] !== '' && params[0].indexOf('=') === -1) {
    const codeStr = params[0]
    i = 1
    // Strict: reject negatives or out-of-range exit codes, keeping the
    // marker itself.
    if (/^\d+$/.test(codeStr)) {
      const code = parseInt(codeStr, 10)
      if (code >= 0 && code <= 255) marker.exitCode = code
    }
  }
  for (; i < params.length; i++) {
    const param = params[i]
    if (param === '') continue // empty parameter: tolerated (legacy `A;`)
    const eq = param.indexOf('=')
    if (eq === -1) return null // not key=value: malformed
    const key = param.slice(0, eq)
    const value = param.slice(eq + 1)
    if (key === OSC133_TAG_KEY) {
      if (!OSC133_TAG_VALUE_RE.test(value)) return null
      marker.nocxEnv = value
    }
    // Well-formed unknown keys are ignored: foreign parameter forms must
    // not break block boundaries.
  }
  return marker
}

// ── Render fence parser (ADR-0024 §7 carve-out) ──────────────────────────
// The shell writes ESC]1337;NOCX_FENCE;<64hex> BEL to the pty AFTER the
// command's output and carries the same 64 hex chars in the authenticated
// `complete` event (docs/lifecycle-protocol.md §8). The renderer reports
// where the fence landed — a rendezvous for render ordering, never an
// authority: a fence with no authenticated event behind it does nothing.
// OSC 1337 is a private namespace other software also uses (iTerm2 file
// transfer), so only an exact NOCX_FENCE; prefix with exactly 64 lowercase
// hex chars parses; everything else is nothing.
const FENCE_PREFIX = 'NOCX_FENCE;'
const FENCE_HEX_RE = /^[0-9a-f]{64}$/

/** Parses an OSC 1337 payload into the fence nonce. Returns null unless the
 *  payload is exactly `NOCX_FENCE;<64 lowercase hex>`. */
export function parseRenderFence(payload: string): { hex: string } | null {
  if (!payload.startsWith(FENCE_PREFIX)) return null
  const hex = payload.slice(FENCE_PREFIX.length)
  if (!FENCE_HEX_RE.test(hex)) return null
  return { hex }
}

/** Parses an OSC 1337 payload into the recovery fence nonce (ADR-0024
 *  decision 8). Returns null unless the payload is exactly
 *  `NOCX_RECOVERY;<64 lowercase hex>`. The shell writes this to the pty at
 *  the first prompt boundary after the lifecycle channel died, restoring a
 *  visible native prompt; the consumer matches it against the pre-provisioned
 *  nonce the backend published in the lost fact. Parse-and-report only: the
 *  renderer never inspects the grid and never pattern-matches a prompt — it
 *  matches this explicit fence, exactly as the completion fence. */
export function parseRecoveryFence(payload: string): { hex: string } | null {
  if (!payload.startsWith('NOCX_RECOVERY;')) return null
  const hex = payload.slice('NOCX_RECOVERY;'.length)
  if (!FENCE_HEX_RE.test(hex)) return null
  return { hex }
}

export class XtermRenderer implements TerminalRenderer {
  private term: Terminal | null = null
  private webgl?: WebglAddon
  private canvas?: CanvasAddon
  private container: HTMLElement | null = null
  private recoveryAttempts = 0
  // Periodic forced refresh — Linux/WebKitGTK only. See FORCED_REFRESH_MS.
  private refreshTimer: ReturnType<typeof setInterval> | null = null
  private commandMarkerSubs: CommandMarkerCallback[] = []
  private osc133Disposable?: { dispose(): void }
  private notificationSubs: NotificationRequestCallback[] = []
  private notifyOscDisposables: Array<{ dispose(): void }> = []
  private scrollSubs: Array<(viewportY: number) => void> = []
  private renderSubs: Array<(range: { start: number; end: number }) => void> = []
  private fenceSubs: Array<(event: RenderFenceEvent) => void> = []
  private recoverySubs: Array<(hex: string) => void> = []
  private fenceOscDisposable?: { dispose(): void }
  private snapshotOscDisposable?: { dispose(): void }
  private scrollDisposable?: { dispose(): void }
  private renderDisposable?: { dispose(): void }
  private _cachedCellHeight: number | null = null
  /** Subscribers to "the cell dimensions may have changed" (nocx-yy9g) —
   *  the frozen block layout re-publishes its metric on this. */
  private _cellDimsSubs: Array<() => void> = []
  /** The device-pixel-ratio watch, kept so dispose can detach it. */
  private _dprMedia: MediaQueryList | null = null
  private _dprChangeHandler: (() => void) | null = null
  /** This tab's command-existence store (OSC 636). Created per renderer so
   *  two tabs never share a snapshot; the editor and frozen headers of this
   *  tab read the same instance this OSC handler feeds. */
  readonly snapshotStore = new CommandSnapshotStore()
  /** Unsubscribe from the module-level theme watcher. */
  private _themeUnsub: (() => void) | null = null
  /** The snippet-palette chord handler (⌥⌘P), registered by the
   *  composition root after construction. Null until then: the chord falls
   *  through to xterm's ordinary encoding — and the bytes reach the pty —
   *  only while no handler is registered, which is the pre-wiring state,
   *  never a live one (TerminalContent registers it at mount). */
  private _snippetChordHandler: (() => void) | null = null

  /** Register (or clear) the snippet-palette chord handler. The custom key
   *  handler below calls it and returns false, so the chord never reaches
   *  xterm's encoder and zero bytes go to the pty (design §10.1). */
  onSnippetChord(cb: (() => void) | null): void {
    this._snippetChordHandler = cb
  }

  /** Frame capture (nocx-3j9b): subscribers to the parse-settle event. */
  private writeParsedSubs: Array<() => void> = []
  private writeParsedDisposable?: { dispose(): void }
  /** Subscribers to the explicit clear/reset operations. */
  private clearSubs: Array<() => void> = []
  private resetSubs: Array<() => void> = []
  /** Writes queued via write() whose bytes have not finished parsing — the
   *  capture fence's pending count. Settled via the per-write callback, so
   *  it is exact even when onWriteParsed fires between chunks. */
  private unsettledWrites = 0
  /** Subscribers to grid resizes — the frame identity's geometry axis
   *  (nocx-x8s2.4). Kept pre-mount and attached at mount, exactly like
   *  onWriteParsed: a tracker built before mount must not lose the resize
   *  signal (a resize reads `moved`/`same` instead of `notComparable`). */
  private resizeSubs: Array<(cols: number, rows: number) => void> = []
  private resizeDisposable?: { dispose(): void }
  /** Subscribers to active-buffer changes — the frame identity's buffer
   *  axis. Kept pre-mount and attached at mount: a tracker built before
   *  mount must not lose the switch (a frame saved on the normal buffer
   *  then compares as `same` after entering the alternate screen). */
  private bufferChangeSubs: Array<(type: 'normal' | 'alternate') => void> = []
  private bufferChangeDisposable?: { dispose(): void }
  /** Disposal subscribers — the capture fence's closing event (see
   *  onDispose). */
  private disposeSubs: Array<() => void> = []
  /** The capture identity tracker, constructed at mount (see mount): the
   *  generation it reports counts every mutation since the renderer was
   *  established. */
  private _captureTracker: CaptureIdentityTracker | null = null
  private _disposed = false

  async mount(container: HTMLElement): Promise<void> {
    this.container = container

    const term = new Terminal({
      fontFamily: FONT_FAMILY,
      fontSize: FONT_SIZE,
      lineHeight: LINE_HEIGHT,
      allowProposedApi: true,
      smoothScrollDuration: 120,
      scrollback: 10000,
      // When the DOM editor owns input at a prompt, focus is on the editor's
      // textarea and xterm is blurred — its default 'outline' inactive cursor
      // then paints a hollow box at the marker-only prompt, a second cursor
      // competing with the editor's caret (item 9). 'none' hides the terminal
      // cursor whenever xterm is not focused; a running program that takes
      // focus back still shows its active cursor.
      cursorInactiveStyle: 'none',
      // Holding Option (macOS) or Shift (elsewhere) forces selection in
      // mouse-tracking programs — the engine's own escape hatch for CAP-4.
      macOptionClickForcesSelection: true,
      // On macOS xterm.js defaults rightClickSelectsWord to true, which
      // word-selects, then with copy-on-select that overwrites the clipboard
      // and pastes the word under the pointer. Neither Warp nor Tabby ships
      // that combination; disable it so right-click pastes what the user
      // expects.
      rightClickSelectsWord: false,
      // The word-selection policy is shared with the frozen command blocks
      // (word-selection.ts): xterm's default separator set, made explicit so
      // double-click selects the same token on both surfaces (nocx-w7h.8).
      wordSeparator: WORD_SEPARATORS,
      // A readability floor under the theme's palette (nocx-3lrm). Without
      // it xterm renders the palette literally, and the one class of program
      // that uses an ANSI colour as a large BACKGROUND — mc paints its
      // panels `lightgray;blue` — becomes unreadable: under the default
      // theme that pair is 1.19:1, because a modern palette lightens blue
      // for TEXT on a dark ground. Warp raises the foreground against the
      // actual cell background, which is why the same mc reads there and
      // did not here.
      //
      // 4.5 is WCAG AA. Only pairs that fall below it are adjusted, so the
      // palette a theme declares still renders exactly as declared wherever
      // it is already legible; this raises a floor rather than restyling.
      minimumContrastRatio: MINIMUM_CONTRAST_RATIO,
      theme: getCurrentTheme(),
    })
    this.term = term

    // The capture tracker (nocx-3j9b) lives for the renderer's whole life:
    // its generation counts every write, buffer switch, resize, clear and
    // reset from mount on, so a readScreen capture's identity is honest —
    // a tracker constructed at capture time would falsely report
    // generation 0. Subscribed to this renderer (the CaptureEventSource);
    // dispose() rejects a pending capture through the renderer's onDispose.
    this._captureTracker = new CaptureIdentityTracker(this)

    term.loadAddon(new Unicode11Addon())
    term.unicode.activeVersion = '11'

    term.open(container)

    // Attach the frame-identity listeners now: a subscriber registered
    // before mount (the frame tracker constructs with the renderer) must
    // not lose the parse-settle, buffer-switch or resize signals — each
    // keeps pre-mount subscribers and attaches here, when the terminal
    // exists. The fence state (unsettledWrites) and the renderer-side
    // clear/reset subscribers were never mount-dependent.
    this._ensureWriteParsed()
    this._ensureResize()
    this._ensureBufferChange()

    // Shift+Enter as its own chord (nocx-nt70) — see SHIFT_ENTER_SEQUENCE.
    // xterm's blessed hook runs before any key processing; returning false
    // for the one chord we encode hands the bytes to the program via the
    // same data path a keystroke takes (onData → transport → pty), and
    // every other key returns true so xterm encodes it exactly as before.
    term.attachCustomKeyEventHandler((event) => {
      // The snippet palette chord (⌥⌘P, design §10.1) is consumed HERE, at
      // the xterm boundary: returning false keeps xterm from encoding the
      // chord, so ZERO bytes reach the program — the one place the chord is
      // needed most (a TUI owns the pane and no editor is showing). The
      // predicate is the shared one from snippets/chord.ts: the editor's
      // arbiter reads the same definition and delegates to the same opener
      // (AD-8). The opener runs synchronously in the keydown, like the
      // Shift+Enter branch below.
      if (isSnippetChord(event)) {
        event.preventDefault()
        this._snippetChordHandler?.()
        return false
      }
      // The plain chord only: Enter + Shift alone. A Ctrl/Alt/Meta-modified
      // Enter must not be collapsed into the Shift bytes — that would lie
      // about the chord. Keyup passes through so xterm's own cursor-style
      // bookkeeping still runs after the key is released.
      if (
        event.type === 'keydown' &&
        event.key === 'Enter' &&
        event.shiftKey &&
        !event.ctrlKey &&
        !event.altKey &&
        !event.metaKey
      ) {
        event.preventDefault()
        term.input(SHIFT_ENTER_SEQUENCE, false)
        return false
      }
      return true
    })

    await document.fonts?.ready
    this.attachWebGL()

    // Linux/WebKitGTK: re-mark every row dirty on a timer so a render is
    // always pending. No-op on macOS/browsers where the compositor is healthy.
    if (isLinuxWebKit()) {
      this.refreshTimer = setInterval(() => {
        const t = this.term
        if (t) t.refresh(0, (t.rows ?? 24) - 1)
      }, FORCED_REFRESH_MS)
    }

    // Invalidate the cellHeight cache and re-publish the cell metric on
    // resize (M1 + nocx-yy9g): xterm re-measures its char size on some
    // resize paths, and a republish is cheap even when nothing changed.
    this.term?.onResize(() => {
      this._cachedCellHeight = null
      this._fireCellDimsChange()
    })

    // Subscribe to theme changes BEFORE construction completes. Re-apply the
    // current theme immediately to close any fetch/subscribe race (a notification
    // published between the resolve above and this registration would otherwise
    // be missed). ADR-0013 §8, design spec §5.4.
    this._themeUnsub = subscribeThemeChanges((t: ITheme) => this.applyTheme(t))

    // OSC 636 — command-existence snapshot (command-snapshot.ts). The store
    // owns parse + policy; the renderer is just the wire, exactly like OSC
    // 7/52/133. Each renderer owns its own store, so tab 2 is never judged
    // against tab 1's command set — the editor and frozen headers receive
    // this same instance at the composition point (terminal-content.ts).
    this.snapshotOscDisposable = term.parser.registerOscHandler(636, (data: string) => {
      this.snapshotStore.ingest(data)
      return false
    })

    // OSC 1337 — the render fence (ADR-0024 §7 carve-out). The shell writes
    // NOCX_FENCE;<64hex> after a command's output; the renderer reports
    // where it landed. Render-only: the fence matches an authenticated
    // completion, it never creates one. Registered here (like OSC 636) and
    // lazily in onRenderFence, so a subscriber that mounts first or last
    // always lands on a live handler.
    this._ensureFenceOsc()
    this.applyTheme(getCurrentTheme())

    // Cell-dims change watch (nocx-yy9g): a device-pixel-ratio change
    // re-snaps xterm's cell width (xterm re-measures its char size on the
    // same resolution query), so the frozen block layout must re-publish.
    // Guarded: jsdom's matchMedia stub may lack addEventListener.
    if (typeof window !== 'undefined' && typeof window.matchMedia === 'function') {
      const mql = window.matchMedia(`(resolution: ${window.devicePixelRatio}dppx)`)
      if (typeof mql.addEventListener === 'function') {
        this._dprChangeHandler = () => {
          // The pitch is snapped to whole DEVICE pixels, so a new ratio is a
          // new pitch even when the grid keeps its rows and columns — and
          // then no resize fires and this is the only thing that clears the
          // cache (nocx-rnrl).
          this._cachedCellHeight = null
          this._fireCellDimsChange()
        }
        mql.addEventListener('change', this._dprChangeHandler)
        this._dprMedia = mql
      }
    }

    // Publish the initial metric: fonts have loaded, the atlas is attached,
    // and the char-size measurement is real now (mount awaited
    // document.fonts.ready above).
    this._fireCellDimsChange()
  }

  /** Register the OSC 1337 fence handler exactly once, when the terminal
   *  exists. The handler parses the payload and fans the sighting out to
   *  the subscribers with the absolute buffer line it landed on. */
  private _ensureFenceOsc(): void {
    if (this.fenceOscDisposable || !this.term) return
    this.fenceOscDisposable = this.term.parser.registerOscHandler(1337, (data: string) => {
      const parsed = parseRenderFence(data)
      if (parsed && this.term) {
        const buf = this.term.buffer.active
        const event: RenderFenceEvent = {
          hex: parsed.hex,
          line: buf.baseY + buf.cursorY,
          buffer: buf.type,
        }
        for (const sub of this.fenceSubs) sub(event)
      }
      // One handler owns OSC 1337 (ADR-8): the recovery fence is the same
      // ident with a different payload kind, so it dispatches from here —
      // a second handler for the same ident would fight for the sequence.
      const recovery = parseRecoveryFence(data)
      if (recovery) {
        for (const sub of this.recoverySubs) sub(recovery.hex)
      }
      return false
    })
  }
  /**
   * Fit the terminal grid to an explicit viewport from the presentation layer
   * (B.5). Computes cols/rows from real cell metrics and the given CSS-pixel
   * dimensions. Does NOT independently measure container geometry.
   */
  fitViewport(viewport: { width: number; height: number }): void {
    const t = this.term
    if (!t || viewport.width <= 0 || viewport.height <= 0) return
    const cell = this._getCellDims()
    if (!cell) return
    const cols = Math.max(1, Math.floor(viewport.width / cell.width))
    const rows = Math.max(1, Math.floor(viewport.height / cell.height))
    if (cols !== t.cols || rows !== t.rows) {
      t.resize(cols, rows)
      // A resize rebuilds the char atlas; cells xterm does not re-mark dirty
      // go on drawing from the old one, which on WKWebView is the mangled
      // overlapping glyphs of nocx-q18. Repaint the viewport so no cell is
      // left pointing at atlas coordinates that have moved. Only after a real
      // grid change: the live region delivers a viewport on every layout tick
      // as output grows, and repainting per tick would be a full redraw per
      // frame. Nothing outside the renderer can be asked to remember this —
      // e0d0a490 moved the resize out to the presentation layer and the
      // repaint stayed behind (nocx-jfgb).
      t.refresh(0, t.rows - 1)
    }
  }

  /**
   * Real cell dimensions from the xterm render service (same source as FitAddon).
   * Accesses internal xterm.js API not present on the public Terminal type.
   *
   * This is the ONLY place the grid's geometry is read, and null is the whole
   * degrade: a cell is what the renderer DRAWS at, and nothing else in the DOM
   * is that number. A fallback here used to measure
   * `.xterm-char-measure-element` and hand its box back as a cell, which is
   * wrong twice over — the span holds 32 characters, so its width is 32 cells,
   * and xterm styles it `line-height: normal`, so its height is the char box
   * with no `lineHeight` in it and no snap to whole device pixels. Callers
   * degrade honestly instead: `cellWidth` reports 0 ("keep the previous
   * metric"), `fit()` leaves the grid alone, `cellHeight` guesses once and
   * never keeps the guess (nocx-rnrl).
   */
  private _getCellDims(): { width: number; height: number } | null {
    const t = this.term
    if (!t) return null
    // xterm.js stores cell dimensions internally — unreachable via public API.
    // Single unchecked cast to narrow local, then structural access only.
    const internal = t as unknown as { _core: unknown }
    const core = internal._core as
      | { _renderService?: { dimensions?: { css?: { cell?: { width: number; height: number } } } } }
      | undefined
    const cell = core?._renderService?.dimensions?.css?.cell
    if (cell && cell.width > 0 && cell.height > 0) return cell
    return null
  }

  /** CSS-pixel width of one grid cell — xterm's real cell advance, snapped
   *  to whole device pixels (nocx-yy9g). 0 when the render service cannot
   *  measure yet (not mounted, no layout) — the frozen block layout treats
   *  0 as "keep the previous metric". */
  get cellWidth(): number {
    return this._getCellDims()?.width ?? 0
  }

  /** Subscribe to "the cell dimensions may have changed" — the frozen block
   *  layout re-publishes its metric on this. Fired at mount end, on grid
   *  resize and on device-pixel-ratio change. */
  onCellDimsChange(cb: () => void): void {
    this._cellDimsSubs.push(cb)
  }

  private _fireCellDimsChange(): void {
    for (const cb of this._cellDimsSubs) cb()
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
    const t = this.term
    if (!t) return
    this.unsettledWrites++
    try {
      t.write(data, () => {
        // The per-write callback fires exactly when THIS write's bytes have
        // been parsed (WriteBuffer's per-chunk callback) — the capture
        // fence's settle signal. onWriteParsed alone cannot be that signal:
        // xterm fires it at the end of EVERY parse pass, which can be
        // BETWEEN chunks of one large write.
        this.unsettledWrites = Math.max(0, this.unsettledWrites - 1)
      })
    } catch (err) {
      // xterm refuses a write once its pending-data watermark is exceeded:
      // it THROWS before queueing anything, so the caller's bytes never
      // entered the terminal. The counter is repaired first — a stuck count
      // would wedge the capture fence forever. Then the refusal is SURFACED
      // to the caller, not logged (nocx-x8s2.3): the caller believes it
      // delivered bytes that are gone, and only the caller can decide the
      // policy (pause, resend, tell the person). A log inside the renderer
      // would let the caller keep believing delivery — the old silent drop
      // in a different shape. Pre-flow-control behaviour was to throw; this
      // restores that while keeping the counter exact.
      this.unsettledWrites = Math.max(0, this.unsettledWrites - 1)
      throw err
    }
  }

  reset(): void {
    const t = this.term
    if (!t) return
    t.reset()
    // Report the explicit reset AFTER it executed, so a subscriber reading
    // state (e.g. the frame generation) observes the post-reset terminal.
    for (const sub of this.resetSubs) sub()
  }

  onData(cb: DataCallback): void {
    this.term?.onData(cb)
  }

  onResize(cb: ResizeCallback): void {
    this.resizeSubs.push(cb)
    this._ensureResize()
  }

  /** Attach the resize fan-out when the terminal exists. Subscribers
   *  registered before mount (the frame tracker constructs with the
   *  renderer) must not be lost — the same shape onWriteParsed uses. */
  private _ensureResize(): void {
    const t = this.term
    if (this.resizeDisposable || !t) return
    this.resizeDisposable = t.onResize(({ cols, rows }) => {
      for (const sub of this.resizeSubs) sub(cols, rows)
    })
  }

  onTitle(cb: TitleCallback): void {
    this.term?.onTitleChange(cb)
  }

  onBufferChange(cb: (type: 'normal' | 'alternate') => void): void {
    this.bufferChangeSubs.push(cb)
    this._ensureBufferChange()
  }

  /** Attach the buffer-change fan-out when the terminal exists — pre-mount
   *  subscribers (the frame tracker) must not be lost (nocx-x8s2.4). */
  private _ensureBufferChange(): void {
    const t = this.term
    if (this.bufferChangeDisposable || !t) return
    this.bufferChangeDisposable = t.buffer.onBufferChange((buf) => {
      for (const sub of this.bufferChangeSubs) sub(buf.type)
    })
  }

  onCwd(cb: CwdCallback): void {
    this.term?.parser.registerOscHandler(7, (data: string) => {
      const parsed = parseOsc7(data)
      if (parsed) {
        cb({ host: parsed.host, path: parsed.path })
      }
      return false // let xterm.js also handle it (default render is no-op)
    })
  }

  onCommandMarker(cb: CommandMarkerCallback): void {
    this.commandMarkerSubs.push(cb)
    if (this.osc133Disposable || !this.term) return
    this.osc133Disposable = this.term.parser.registerOscHandler(133, (data: string) => {
      const marker = parseOsc133(data)
      if (marker && this.term) {
        const buf = this.term.buffer.active
        const event: CommandMarkerEvent = {
          ...marker,
          line: buf.baseY + buf.cursorY,
          col: buf.cursorX,
          buffer: buf.type,
        }
        for (const sub of this.commandMarkerSubs) sub(event)
      }
      return false
    })
  }

  /** Subscribe to notification requests: a program asked nocx to present a
   *  message (ADR-0029). OSC 9 and OSC 777 are two spellings of one request,
   *  so both register here and fan out to one subscriber list — the consumer
   *  never learns which sequence a program chose, because nothing downstream
   *  may depend on it.
   *
   *  Render-only, exactly like every other OSC on this renderer: the request
   *  is reported, never granted. This handler decides nothing about where the
   *  message goes — that is the router's, on the backend — and it cannot,
   *  because the only thing it can send is the text the program supplied. */
  onNotification(cb: NotificationRequestCallback): void {
    this.notificationSubs.push(cb)
    if (this.notifyOscDisposables.length || !this.term) return
    for (const ident of [9, 777] as const) {
      this.notifyOscDisposables.push(
        this.term.parser.registerOscHandler(ident, (data: string) => {
          // Untrusted bytes from whatever the user ran. parseOscNotification
          // is total and returns null rather than throwing; a throw inside a
          // parser callback would take the renderer down.
          const parsed = parseOscNotification(ident, data)
          if (parsed) {
            for (const sub of this.notificationSubs) sub(parsed)
          }
          // false: xterm.js may also handle the ident. This matters for 9 —
          // the ConEmu progress payload (9;4;…) parses to null here and must
          // stay available to anything that renders progress.
          return false
        }),
      )
    }
  }

  onRenderFence(cb: RenderFenceCallback): void {
    this.fenceSubs.push(cb)
    this._ensureFenceOsc()
  }

  /** Subscribe to recovery-fence sightings: the shell wrote the one-shot
   *  NOCX_RECOVERY OSC after restoring a visible native prompt (ADR-0024
   *  decision 8). The consumer matches the hex against the nonce the lost
   *  fact published and acknowledges the restoration. */
  onRecoveryFence(cb: (hex: string) => void): void {
    this.recoverySubs.push(cb)
    this._ensureFenceOsc()
  }

  onBell(cb: BellCallback): void {
    this.term?.onBell(cb)
  }

  onSelectionChange(cb: SelectionCallback): void {
    this.term?.onSelectionChange(() => {
      cb(this.term?.getSelection() ?? '')
    })
  }

  onClipboardWrite(cb: ClipboardWriteCallback): void {
    this.term?.parser.registerOscHandler(52, (data: string) => {
      // decodeOsc52 is a pure parser imported from the clipboard module
      // and does not touch the clipboard — the callback fires the decoded
      // text upward, the policy layer writes it (AD-6).
      const decoded = decodeOsc52(data)
      if (decoded !== null) {
        cb(decoded)
      }
      return false
    })
  }

  paste(text: string): boolean {
    // term.paste() owns bracketed-paste wrapping: when the running program
    // has enabled mode 2004, it wraps the payload in the escape sequences.
    const term = this.term
    if (!term) return false
    // A submitted command must reach the program while the grid is
    // read-only: disableStdin guards USER input (keystrokes land in the
    // editor instead), and the editor's submit delivers its document
    // through this same method. xterm's paste() is dropped when
    // disableStdin is set, so lift the guard for the synchronous delivery
    // and restore it (nocx-u7uh.23).
    const wasDisabled = term.options.disableStdin
    if (wasDisabled) term.options.disableStdin = false
    try {
      term.paste(text)
    } finally {
      term.options.disableStdin = wasDisabled
    }
    return true
  }

  /** The running program's bracketed-paste mode (2004), or false when no
   *  terminal is mounted. */
  bracketedPasteActive(): boolean {
    return this.term?.modes.bracketedPasteMode ?? false
  }

  refreshAtlas(): void {
    // nocx-q18: clearing the texture atlas and then repainting races with
    // the atlas repopulation during _updateModel. After clearTextureAtlas(),
    // the atlas pages are blank and the glyph cache is empty. xterm.js's
    // default rendering path (renderRows → _updateModel → getRasterizedGlyph)
    // draws glyphs to the atlas on demand, so clearing first buys nothing.
    //
    // The resize path (fitViewport → resize) already refreshes the char atlas
    // char atlas via _refreshCharAtlas() which acquires a correctly-sized
    // atlas. The tab-activation path needs a viewport refresh because
    // terminal content may have changed while the tab was in the background.
    if (this.term) {
      this.term.refresh(0, this.term.rows - 1)
    }
  }

  applyTheme(theme: ITheme): void {
    // Deliverable 3: setting the option alone may leave a stale render,
    // especially on the WebKitGTK compositor (ADR-0005). The full viewport
    // refresh forces a repaint in the new palette. The 42 ms pump (when
    // active) continues alongside; this is the one-shot push, not a second
    // loop.
    if (!this.term) return
    this.term.options.theme = theme
    this.term.refresh(0, this.term.rows - 1)
  }

  setReadOnly(readOnly: boolean): void {
    if (this.term) this.term.options.disableStdin = readOnly
  }

  focus(): void {
    this.term?.focus()
  }

  /** Frame capture (nocx-x8s2.4): the dispose notification — the fence's
   *  closing event. The CaptureIdentityTracker rejects its pending
   *  awaitSettled() waiters on this, so a capture never hangs across
   *  disposal. Fired exactly once, at the top of dispose(), before the
   *  event subscriptions are torn down. */
  onDispose(cb: () => void): void {
    if (this._disposed) {
      // Already disposed: fire immediately — a late subscriber must not
      // wait forever on a source that is gone.
      cb()
      return
    }
    this.disposeSubs.push(cb)
  }
  dispose(): void {
    if (this._disposed) return
    this._disposed = true
    // Tell the frame tracker BEFORE the subscriptions go away: a capture
    // parked on the parse fence must settle (reject) now, while its waiter
    // is still registered — tearing the subscriptions down first would
    // orphan it.
    const disposeSubs = this.disposeSubs
    this.disposeSubs = []
    for (const sub of disposeSubs) sub()
    if (this.refreshTimer !== null) {
      clearInterval(this.refreshTimer)
      this.refreshTimer = null
    }
    this.snapshotOscDisposable?.dispose()
    this.snapshotOscDisposable = undefined
    this.osc133Disposable?.dispose()
    this.osc133Disposable = undefined
    this.commandMarkerSubs = []
    for (const d of this.notifyOscDisposables) d.dispose()
    this.notifyOscDisposables = []
    this.notificationSubs = []
    if (this._dprMedia !== null && this._dprChangeHandler !== null) {
      this._dprMedia.removeEventListener('change', this._dprChangeHandler)
      this._dprMedia = null
      this._dprChangeHandler = null
    }
    this._cellDimsSubs = []
    this.scrollDisposable?.dispose()
    this.scrollDisposable = undefined
    this.scrollSubs = []
    this.renderDisposable?.dispose()
    this.renderDisposable = undefined
    this.renderSubs = []
    this.resizeDisposable?.dispose()
    this.resizeDisposable = undefined
    this.resizeSubs = []
    this.bufferChangeDisposable?.dispose()
    this.bufferChangeDisposable = undefined
    this.bufferChangeSubs = []
    this.writeParsedDisposable?.dispose()
    this.writeParsedDisposable = undefined
    this.writeParsedSubs = []
    this.clearSubs = []
    this.resetSubs = []
    if (this._themeUnsub !== null) {
      this._themeUnsub()
      this._themeUnsub = null
    }
  }

  get cols(): number {
    return this.term?.cols ?? 80
  }

  get rows(): number {
    return this.term?.rows ?? 24
  }

  /**
   * Height in CSS pixels of the rows that have actually been WRITTEN to.
   *
   * Scans the viewport upward for the last non-blank line rather than
   * multiplying `rows` by the cell height. The two differ by the whole point of
   * this method: the grid is as tall as the pane, so `rows * cell` would give a
   * full-pane live region to a command that printed one line.
   *
   * THE CURSOR IS NOT COUNTED, and it used to be — deliberately, for a program
   * that clears the screen and parks the cursor thirty rows down while writing
   * only five. That reading was reversed on 2026-08-19 by the owner, against a
   * frame capture of his own machine, and the reason is that the row the cursor
   * sits on is the row the two halves of a block disagree about. A finished
   * command's block ends at its render fence; the shell has by then moved the
   * cursor one row past it, onto the row the NEXT prompt will occupy. Counting
   * it made the live region exactly one row taller than the frozen block it
   * becomes, so the pane rose while the command ran and dropped back one row
   * the instant it finished — the twitch this method was measured for
   * (nocx-i4h04.1). Neither side could give the row up alone: the block cannot
   * keep it without swallowing the row the next command's echo lands on
   * (nocx-4yhi).
   *
   * What that costs, stated rather than discovered later: a program waiting for
   * input on a row it has written nothing to shows no cursor until the first
   * keystroke — `read x` with no prompt is the case; `read -p 'name: '` writes
   * to the row and is unaffected. It is not a new class of blindness. The rows
   * still exist and are still drawn: the grid is fitted to `runningLiveCap`,
   * not to this number, so nothing about what a program may paint changed —
   * only how much of it the box reserves. And between Enter and the first byte
   * of output the cursor sits on the echo row, which the live region already
   * translates out of view, so this is the same behaviour a moment earlier.
   *
   * NULL is "cannot measure", and it is not the same answer as 0. Zero is a
   * grid nobody has written to — a real height, and the one the live region
   * must take between the keypress and the first byte of output. Null is a
   * renderer with no terminal or no cell dimensions yet, where the class's
   * own fallback height has to stand instead. They were one value, and the
   * moment the cursor stopped counting they became indistinguishable: a
   * command that had printed nothing yet read as unmeasurable and opened the
   * region at the 140px fallback, which then collapsed to nothing on the
   * first row of output — a bounce at the start of every command.
   *
   * Bounded by `rows`, so the cost is one pass over the visible grid — this runs
   * per animation frame while a command produces output.
   */
  liveContentHeight(): number | null {
    const t = this.term
    if (!t) return null
    const cell = this._getCellDims()
    if (!cell) return null
    const buf = t.buffer.active
    for (let y = t.rows - 1; y >= 0; y--) {
      const line = buf.getLine(buf.baseY + y)
      if (line && line.translateToString(true).length > 0) {
        return (y + 1) * cell.height
      }
    }
    return 0
  }

  // ── Marker/geometry API (ADR-0008 command-ledger gutter) ──────────────

  registerMarker(): MarkerAdapter | undefined {
    const t = this.term
    if (!t) return undefined
    const m = t.registerMarker(0)
    if (!m) return undefined
    return {
      line: () => {
        // m.line returns -1 when disposed, so map to undefined.
        const l = m.line
        return l >= 0 ? l : undefined
      },
      onDispose: (cb: () => void) => {
        m.onDispose(cb)
      },
      dispose: () => {
        m.dispose()
      },
    }
  }

  /** CSS-pixel pitch of one grid ROW — the same measurement `cellWidth` and
   *  `fit()` take, off the render service. The live region translates the grid
   *  by whole rows of this to drop the shell's echo (scrollback/controller.ts),
   *  so anything short of the drawn pitch leaves the remainder of the echoed
   *  command on screen, cut across the middle — 3px of it on a Retina Mac,
   *  where the pitch is 20 and this used to answer 17 (nocx-rnrl). */
  get cellHeight(): number {
    // M1: cache the pitch — this is read per paint by the gutter.
    if (this._cachedCellHeight !== null) return this._cachedCellHeight
    const measured = this._getCellDims()?.height
    if (measured !== undefined && measured > 0) {
      this._cachedCellHeight = measured
      return measured
    }
    // Nothing has been drawn yet, so there is no pitch to report — only the
    // size the grid was asked for. Deliberately NOT cached: a guess that
    // outlives the first frame is the defect, and the invalidations below
    // (grid resize, DPR change) cannot be relied on to arrive at all.
    return Math.ceil(FONT_SIZE * LINE_HEIGHT)
  }

  get viewportTopLine(): number {
    const t = this.term
    if (!t) return 0
    // viewportY is already the absolute buffer line at the top of the viewport
    // (xterm.d.ts). Adding baseY double-counts scrollback (B1).
    return t.buffer.active.viewportY
  }

  onScroll(cb: (viewportY: number) => void): void {
    this.scrollSubs.push(cb)
    if (this.scrollDisposable || !this.term) return
    this.scrollDisposable = this.term.onScroll((y: number) => {
      for (const sub of this.scrollSubs) sub(y)
    })
  }

  onRender(cb: (range: { start: number; end: number }) => void): void {
    this.renderSubs.push(cb)
    if (this.renderDisposable || !this.term) return
    this.renderDisposable = this.term.onRender((r: { start: number; end: number }) => {
      for (const sub of this.renderSubs) sub(r)
    })
  }
  /** Subscribe to parse-settles: fires after a written chunk has been
   *  parsed into the buffer. The frame generation advances here, and the
   *  capture fence waits on it. Subscribers registered before mount are
   *  attached when mount creates the terminal. */
  onWriteParsed(cb: () => void): void {
    this.writeParsedSubs.push(cb)
    this._ensureWriteParsed()
  }

  private _ensureWriteParsed(): void {
    const t = this.term
    if (this.writeParsedDisposable || !t) return
    this.writeParsedDisposable = t.onWriteParsed(() => {
      for (const sub of this.writeParsedSubs) sub()
    })
  }

  /** Subscribe to explicit clears — fired after clearViewport() executed. */
  onClear(cb: () => void): void {
    this.clearSubs.push(cb)
  }

  /** Subscribe to explicit resets — fired after reset() executed. */
  onReset(cb: () => void): void {
    this.resetSubs.push(cb)
  }

  /** True while bytes queued via write() have not finished parsing — the
   *  capture fence. */
  hasUnsettledWrite(): boolean {
    return this.unsettledWrites > 0
  }

  getBufferLine(line: number): import('@xterm/xterm').IBufferLine | undefined {
    return this.term?.buffer.active.getLine(line)
  }

  /** Absolute buffer line of the cursor — the line the next write lands on. */
  cursorLine(): number {
    if (!this.term) return 0
    const buf = this.term.buffer.active
    return buf.baseY + buf.cursorY
  }

  /** Column of the cursor — the column the next write lands on. */
  cursorCol(): number {
    return this.term?.buffer.active.cursorX ?? 0
  }

  /** Clear the whole buffer — "making the prompt line the new first
   *  line" (xterm's own contract). Called at a block freeze so the rows
   *  the DOM block now owns leave the grid; the grid only ever holds the
   *  running command's rows, and the DOM owns the scrollback (nocx-m87n). */
  clearViewport(): void {
    const t = this.term
    if (!t) return
    t.clear()
    // Report the explicit clear AFTER it executed, so a subscriber reading
    // state (e.g. the frame generation) observes the post-clear buffer.
    for (const sub of this.clearSubs) sub()
  }

  /** Capture the live frame of the current buffer (nocx-ljfwz): fence the
   *  parse queue (awaitSettled — one mint reads ONE settled state), read
   *  the capture identity the tracker has maintained since mount, and mint
   *  the frame over the requested row span — the visible screen by default,
   *  a clamped absolute span when a region is given. A region entirely past
   *  the end of the buffer is refused (ReadScreenRangeError), never lied
   *  about; disposal mid-capture rejects with CaptureAbortedError. */
  async captureLiveFrame(region?: { start: number; end: number }): Promise<CapturedFrame> {
    if (!this.term) throw new CaptureAbortedError()
    const tracker =
      this._captureTracker ?? (this._captureTracker = new CaptureIdentityTracker(this))
    await tracker.awaitSettled()
    const identity = tracker.identity()
    const buf = this.term.buffer.active
    const bufferLen = buf.length
    let start = buf.baseY
    let end = Math.min(buf.baseY + this.term.rows, bufferLen)
    if (region) {
      start = Math.max(0, region.start)
      end = Math.min(region.end, bufferLen)
      if (start >= end) {
        throw new ReadScreenRangeError(
          `region [${region.start}, ${region.end}) is past the end of the buffer (${bufferLen} rows)`,
        )
      }
    }
    return mintLiveFrame(
      identity,
      { start, end },
      {
        getLine: (y) => this.getBufferLine(y),
        cursor: { line: this.cursorLine(), col: this.cursorCol() },
        snapshot: fromITheme(getCurrentTheme()),
      },
    )
  }

  /** The active buffer's kind right now: 'normal' | 'alternate' — the one
   *  interactivity fact the backend cannot see for itself (AD-6). The
   *  renderer owns the grid; this reports the capture tracker's current
   *  identity, the same vocabulary agent.captureFrame carries. */
  activeBufferKind(): 'normal' | 'alternate' {
    const tracker =
      this._captureTracker ?? (this._captureTracker = new CaptureIdentityTracker(this))
    return tracker.identity().buffer.kind
  }
  get paneElement(): HTMLElement {
    return this.container ?? document.createElement('div')
  }
}

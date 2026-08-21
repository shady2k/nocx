// DOM scrollback controller — wires the renderer's OSC 133 markers to block
// creation, manages the live region visibility, alt-screen transitions, and
// `clear` detection. Owns the scrollback DOM structure inside the pane.
//
// ADR-0024 (bead nocx-u7uh.7): the block model is an attempt projection —
// the freeze/abandon paths below are driven by authenticated attempts, never
// by OSC 133 C/D (which the renderer now treats as render-only). The
// authority checks (kernel freezeBlock) run at the composition site; these
// methods paint the attempt's verdict.

import type { CommandAuthor } from '../command-ledger'
import type { TerminalRenderer } from '../renderers/types'
import { BlockManager, type BlockRecord, type GetLineFn } from './blocks'
import type { CommandSnapshotStore } from '../command-snapshot'
import { publishCellMetric, publishRowPitch } from './cell-metric'
import type { ExecutionAttempt } from '../lifecycle/state'
export type LiveRegionMode = 'idle' | 'running' | 'fullscreen' | 'unstructured'

/** How long the pane takes to settle after a block opens or freezes. Short
 *  enough to belong to the keypress that caused it; long enough to read as a
 *  movement rather than a displacement. */
const SETTLE_MS = 140
/** Fast out, slow in: the stack leaves at once and arrives gently, which is
 *  what makes it read as the block taking its place. */
const SETTLE_EASING = 'cubic-bezier(0.2, 0, 0, 1)'

export interface ScrollbackControllerOpts {
  /** The pane this controller owns the scrollback inside. */
  pane: HTMLElement
  /** The renderer for the terminal. */
  renderer: TerminalRenderer
  /** The renderer's per-tab command-existence snapshot store (OSC 636). */
  snapshotStore: CommandSnapshotStore
  /** Injectable clock. */
  now?: () => number
  /** Fired after the scrollback is cleared (a `clear` command): the ask
   *  chip's block went with the blocks, so the mode must close — a chip
   *  whose block no longer exists would be an invisible mode. */
  onClear?: () => void
  /** Fired at the end of every visual freeze — the block's output rows are
   *  fixed in the DOM (nocx-tjppv: the run tool's completion wait reads the
   *  output window from the frozen block). */
  onBlockFrozen?: (rec: BlockRecord) => void
}

export class ScrollbackController {
  readonly scrollbackLayout: HTMLElement
  readonly scrollbackArea: HTMLElement
  readonly scrollbackInner: HTMLElement
  /** Outer clipping container — height changes with mode. */
  readonly xtermLiveContainer: HTMLElement
  /** The end of the output, as a one-pixel box a sibling of the stack — the
   *  follow observer's target. See the constructor for why it is not the live
   *  region. */
  readonly followSentinel: HTMLElement
  /** Inner clipping window. It tracks the rows written while the outer box
   *  supplies the same height to flex layout. */
  readonly xtermLiveViewport: HTMLElement
  /** Inner wrapper with stable min-height — xterm mounts here so its grid
   *  stays sane regardless of the clipping container's CSS height. */
  readonly xtermInner: HTMLElement
  readonly separator: HTMLElement
  /** This tab's OSC 636 store, kept because a block built OUTSIDE the manager
   *  — a restored one (nocx-m3fqk) — needs the same instance its live
   *  neighbours judge against, and the manager's copy is private. */
  readonly snapshotStore: CommandSnapshotStore

  private _blockManager: BlockManager
  private _renderer: TerminalRenderer
  private _mode: LiveRegionMode = 'idle'
  /**
   * Sticky for the life of the command. Once a program has filled the pane the
   * flag stays set until the next idle or the next command start, so it cannot
   * oscillate: giving the editor's box back would shrink the scroller, which
   * shrinks the grid, which could put the program back at the ceiling and take
   * the box away again — a layout that flickers forever at a frame's period.
   * Codex's rule for this class of switch, and it applies verbatim: enter on
   * evidence, leave only at a command boundary.
   */
  private _filledPane = false
  /** True while the end of the live output is visible. */
  private _following = true
  /** The glide in flight per element, so a change landing mid-settle
   *  retargets rather than snapping — see `_glide`. */
  private readonly _settleAnimations = new Map<Element, Animation>()
  /** Where the stack was on the last painted frame, and the handle of the
   *  loop that records it — see `_watchPaintedTop`. */
  private _paintedTop: number | null = null
  private _paintWatch = 0
  private _painting = false
  private _followObserver: IntersectionObserver | null = null
  /** The ask mode's owner (nocx-x8s2.2): called when the scrollback is
   *  cleared, so the chip — and with it the agent target — closes when its
   *  block is gone. */
  private _onClear?: () => void

  constructor(opts: ScrollbackControllerOpts) {
    this._renderer = opts.renderer
    this._onClear = opts.onClear
    this.snapshotStore = opts.snapshotStore
    const now = opts.now ?? (() => performance.now())

    // ── Build the scrollback DOM ─────────────────────────────────────────
    this.scrollbackLayout = document.createElement('div')
    this.scrollbackLayout.className = 'scrollback-layout'

    this.scrollbackArea = document.createElement('div')
    this.scrollbackArea.className = 'scrollback-area'

    this.scrollbackInner = document.createElement('div')
    this.scrollbackInner.className = 'scrollback-inner'

    // Blocks live in the inner wrapper.
    this.scrollbackArea.appendChild(this.scrollbackInner)

    // WHERE THE OUTPUT ENDS, as a fact about layout rather than about paint.
    // It is a sibling of the stack and never moves with it, which is the
    // whole point: the follow observer used to watch the live region, and the
    // live region is inside the stack the settle displaces (see `_glide`).
    // One pixel tall with a matching negative margin, so it occupies a box
    // the observer can see and no space the layout can feel.
    this.followSentinel = document.createElement('div')
    this.followSentinel.className = 'scrollback-follow-sentinel'
    this.scrollbackArea.appendChild(this.followSentinel)

    // The outer container participates in flex layout; the separate inner
    // viewport clips xterm's larger grid. Both follow the written rows so the
    // running shape matches the frozen block it becomes.
    this.xtermLiveContainer = document.createElement('div')
    this.xtermLiveContainer.className = 'xterm-live-container live-idle'

    this.xtermLiveViewport = document.createElement('div')
    this.xtermLiveViewport.className = 'xterm-live-viewport'

    this.xtermInner = document.createElement('div')
    this.xtermInner.className = 'xterm-inner'
    this.xtermLiveViewport.appendChild(this.xtermInner)
    this.xtermLiveContainer.appendChild(this.xtermLiveViewport)

    // Separator between blocks and live region — inserted before the
    // xterm container so blocks stack above it. Hidden when no blocks.
    this.separator = document.createElement('div')
    this.separator.className = 'scrollback-separator'
    this.separator.style.display = 'none'
    this.scrollbackInner.appendChild(this.separator)
    this.scrollbackInner.appendChild(this.xtermLiveContainer)

    this.scrollbackLayout.appendChild(this.scrollbackArea)

    // Insert the layout as the first child of the pane (before the editor,
    // which is absolute-positioned).
    opts.pane.insertBefore(this.scrollbackLayout, opts.pane.firstChild)

    this._blockManager = new BlockManager(this.scrollbackInner, this.xtermLiveContainer, {
      now,
      snapshotStore: opts.snapshotStore,
      // A DEFERRED freeze landed inside the manager (the fence arrived, or
      // the FENCE_DEFER_MS window elapsed): hand the block's rows to the
      // DOM and settle the live region exactly like a direct freeze, since
      // freezeFromAttempt already returned.
      onDeferredFreeze: () => this._settleFrozen(),
      onBlockFrozen: opts.onBlockFrozen,
      // Read at freeze time rather than captured at construction: a pane is
      // resized, and the provenance must say what the serializer actually
      // saw.
      dimensions: () => ({ cols: this._renderer.cols, rows: this._renderer.rows }),
    })

    // ── Frozen block cell metric (nocx-yy9g) ──────────────────────────
    // The frozen block layout must reproduce xterm's cell width exactly,
    // so the renderer's real measurement is published as custom properties
    // on the scrollback container (see cell-metric.ts for why the DOM
    // cannot be trusted to match on its own). Subscribed BEFORE the
    // renderer mounts, so the mount-end notification lands on a live
    // subscription; the publish itself is a no-op until the renderer can
    // measure (cellWidth 0), so the first real publish is the mount-end
    // fire. Old blocks adopt the current geometry on every republish —
    // deliberate, see the module comment.
    this._renderer.onCellDimsChange?.(() => this._republishCellMetric())
    this._republishCellMetric()

    // ── Render fence rendezvous (nocx-u7uh.8, ADR-0024 §7 carve-out) ────
    // The renderer reports where the fence landed; the block manager matches
    // it against the pending authenticated completion. A fence in the
    // alternate buffer has no scrollback line to serialize — ignored here.
    // Optional on the renderer: without it, every freeze takes the defined
    // no-fence path (defer, then settle at the current output end).
    opts.renderer.onRenderFence?.((ev) => {
      if (ev.buffer !== 'normal') return
      this._blockManager.sightFence(ev.hex, ev.line)
    })

    // ── Follow state ─────────────────────────────────────────────────────
    // Whether the end of the live output is on screen. Not "did the last scroll
    // look like a human" — that question has no answer: the `scroll` event does
    // not carry its origin, and a classifier built from wheel and touch misses
    // Page Up, Home, dragging the scrollbar and jumping to a search hit. This
    // asks the thing actually wanted — can the user see the live end — and the
    // browser answers it.
    // Guarded: the test environment has no IntersectionObserver, and this
    // constructor runs inside the mount path — an unguarded `new` there took
    // the whole terminal down with "terminal content failed", not just the
    // follow behaviour. Without it the view simply always follows, which is the
    // behaviour that predates this observer anyway.
    if (typeof IntersectionObserver === 'undefined') return
    this._followObserver = new IntersectionObserver(
      (entries) => {
        for (const e of entries) this._following = e.isIntersecting
      },
      { root: this.scrollbackArea, threshold: 0 },
    )
    this._followObserver.observe(this.followSentinel)
  }

  /** The element the xterm renderer mounts into. Returns the stable
   *  xterm-inner wrapper, NOT the clipping live-container, so the grid
   *  never collapses to 1 row (P0-1 fix). */
  get mountTarget(): HTMLElement {
    return this.xtermInner
  }

  get blockManager(): BlockManager {
    return this._blockManager
  }

  /** The currently selected block id, or null (P1-8). */
  get selectedBlockId(): number | null {
    return this._blockManager.selectedBlockId
  }

  /** Deselect all blocks (P0-4: Escape key, P1-8: click empty space). */
  deselectBlocks(): void {
    this._blockManager.deselectAll()
  }

  get mode(): LiveRegionMode {
    return this._mode
  }

  // ── Live region visibility ────────────────────────────────────────────

  /** Collapse the live region only after its rows belong to the DOM block. */
  setIdle(): void {
    if (this._mode === 'fullscreen' || this._blockManager.visualFreezePending) return
    this._mode = 'idle'
    // Both inline heights have to go: running content height and its clip
    // otherwise outrank the idle/fullscreen mode classes.
    this.xtermLiveContainer.style.height = ''
    this.xtermLiveViewport.style.height = ''
    this._setFilledPane(false)
    this.xtermLiveContainer.className = 'xterm-live-container live-idle'
    this.xtermInner.className = 'xterm-inner'
    // The echo shift is a running-mode property: clear it with the region.
    this._applyEchoShift()
    this._updateSeparator()
  }

  /** Show live output below the running block. */
  setRunning(): void {
    if (this._mode === 'fullscreen') return
    const entering = this._mode !== 'running'
    this._mode = 'running'
    if (entering) {
      this.xtermLiveContainer.style.height = ''
      this.xtermLiveViewport.style.height = ''
    }
    this._setFilledPane(false)
    this.xtermLiveContainer.className = 'xterm-live-container live-running'
    this.xtermInner.className = 'xterm-inner'
    // The block just opened; its echo row is the grid's top row, so the
    // shift applies from the first frame (nocx-w1n4).
    this._applyEchoShift()
    this._updateSeparator()
    this._watchPaintedTop()
    // Size both layers from the rows that exist now. A short command therefore
    // opens beside the prompt instead of reserving a pane-high empty window.
    // Both layers change in the same layout pass: a transition here can still
    // be half-open when a fast command freezes, creating an extra jump.
    this.setLiveHeight(this._renderer.liveContentHeight())
    this._scrollToBottom()
  }

  /** Fill the pane with the live region — for sessions with no shell
   *  integration (no OSC 133 markers yet, e.g. a plain SSH shell): the
   *  scrollback-block model never takes over, so the terminal itself must be
   *  visible at full size. The FIRST marker transitions back to the normal
   *  layout (setIdle/setRunning/enterFullscreen all override this mode).
   *  Class mirrors live-fullscreen's fill treatment; the semantics differ —
   *  this is not an alt-screen program. */
  setUnstructured(): void {
    if (this._mode === 'fullscreen') return
    this._mode = 'unstructured'
    this.xtermLiveViewport.style.height = ''
    this.xtermLiveContainer.className = 'xterm-live-container live-unstructured'
    this.xtermInner.className = 'xterm-inner inner-fullscreen'
    this._setFilledPane(true)
    this._applyEchoShift()
    const max = this.scrollbackArea.clientHeight
    if (max > 0) this.xtermLiveContainer.style.height = `${max}px`
  }

  /**
   * Maximum space available to running output: the scroller's client height
   * less the running block's header, which share the viewport (nocx-6w4z).
   * The measured content grows only up to this ceiling, and the grid is fitted
   * to the same cap so tall inline TUIs keep their last rows reachable
   * (nocx-zn4d). Null outside `running` or while unmeasurable.
   */
  get runningLiveCap(): number | null {
    if (this._mode !== 'running') return null
    const header = this._blockManager.runningBlock?.el.getBoundingClientRect().height ?? 0
    const max = this.scrollbackArea.clientHeight - header
    return max > 0 ? max : null
  }

  /**
   * Size the live output to the rows it actually shows.
   *
   * The renderer measures the grid, including the echoed command row that
   * `_applyEchoShift` translates above the clip. Both the outer flow box and
   * its inner clip reserve only the remainder. That keeps running geometry
   * aligned with the frozen body, which omits the same echo row.
   * Height changes are synchronous: animation can be only 2–4px open when a
   * fast command freezes, turning one layout change into two movements.
   */
  setLiveHeight(px: number | null): void {
    if (this._mode !== 'running') return
    // nocx-w1n4: the echoed command line leaves the LIVE region the same way
    // it leaves the frozen body — the first shown row is the running block's
    // outputStart. Applied before the height guards so a viewport scroll that
    // leaves the box size unchanged still releases the shift.
    this._applyEchoShift()
    // Null is the renderer saying it cannot measure; the class's fallback
    // height stands. Zero is a grid nobody has written to, and it sizes the
    // region like any other measurement — to nothing but the body's padding.
    if (px === null) return
    const max = this.runningLiveCap
    if (max === null) return
    const pad = this._bodyPaddingPx()
    // Two subtractions, and both are what makes the running region the same
    // box as the frozen one. The echo row: the grid still holds the shell's
    // echo of the command at its top and the transform moves it out of view,
    // so the flow box must not reserve it. The padding: the block body has
    // it, this region is that body while the command runs, and the cap is a
    // ceiling on the whole box rather than on the rows inside it.
    const content = Math.min(Math.max(0, px - this._echoShiftPx()), Math.max(0, max - pad))
    const box = `${content + pad}px`
    // Keep the block's bottom at the bottom of the view — but only on the
    // frames where the box actually CHANGED SIZE. This runs on every chunk of
    // output, and a program that repaints in place changes nothing about the
    // layout: `top` redrawing the same 33 rows every three seconds was moving
    // the scroll each time for no reason, which is unpleasant if you are
    // reading. The comparison below is the whole guard.
    if (this.xtermLiveContainer.style.height !== box) {
      this.xtermLiveContainer.style.height = box
      if (this._following) this._scrollToBottom()
    }
    const inner = `${content}px`
    if (this.xtermLiveViewport.style.height !== inner) {
      this.xtermLiveViewport.style.height = inner
    }
  }

  /** The body padding this region wears while it is the running block's body
   *  — read off the element rather than repeated here, so the stylesheet
   *  stays the one place the number lives (`--cmd-output-pad-*`). Zero
   *  wherever there is no layout to read (jsdom), which is the same answer
   *  the class's own fallback gives. */
  private _bodyPaddingPx(): number {
    const cs = getComputedStyle(this.xtermLiveContainer)
    const top = parseFloat(cs.paddingTop)
    const bottom = parseFloat(cs.paddingBottom)
    return (Number.isFinite(top) ? top : 0) + (Number.isFinite(bottom) ? bottom : 0)
  }

  /**
   * The vertical offset, in CSS pixels, that moves the live region's first
   * SHOWN row to the running block's outputStart (nocx-w1n4).
   *
   * The frozen body already skips the shell's echo of the command: the
   * app-owned submit opens the block before the bytes, the echo lands on
   * the creation line, and the output range starts one row later
   * (nocx-4yhi). The live region is the grid itself, which still holds
   * that echoed line on the creation row — so the running block showed one
   * row more than the frozen one will. The box clips the grid's TOP rows,
   * which is why offsetting the measured height does nothing useful: it
   * hides the bottom rows, never the echo. The grid itself must move.
   *
   * The offset is `outputStart - viewportTopLine`: exactly the rows between
   * the top of the viewport and the first row the frozen block will
   * contain. While the echo is the top visible row that is one cell; the
   * moment the output outgrows the viewport and the echo scrolls above the
   * grid, the offset drops to zero and the first real output row is left
   * alone.
   */
  private _echoShiftPx(): number {
    if (this._mode !== 'running') return 0
    const rec = this._blockManager.runningBlock
    if (!rec) return 0
    const cell = this._renderer.cellHeight
    if (!cell || cell <= 0) return 0
    const top = this._renderer.viewportTopLine ?? 0
    const rows = rec.outputStart - top
    return rows > 0 ? rows * cell : 0
  }

  /** Apply the echo shift to the grid, or clear it. The write is guarded:
   *  identical values are skipped so the per-frame sizing pass does not
   *  rewrite the style on every chunk of output. */
  private _applyEchoShift(): void {
    const px = this._echoShiftPx()
    const next = px > 0 ? `translateY(-${px}px)` : ''
    if (this.xtermInner.style.transform === next) return
    this.xtermInner.style.transform = next
  }

  /** Re-read the renderer's cell metric — width AND row pitch — and
   *  republish it for the frozen blocks (nocx-yy9g and its vertical twin).
   *  No-op while the renderer cannot measure; a republish is cheap, so it
   *  runs on every cell-dims notification without trying to detect whether
   *  anything actually changed. */
  private _republishCellMetric(): void {
    publishCellMetric(this.scrollbackInner, this._renderer.cellWidth)
    publishRowPitch(this.scrollbackInner, this._renderer.cellHeight)
  }

  /**
   * Put blocks the STORE holds above everything the live session draws
   * (nocx-m3fqk), and mark where the past ends.
   *
   * Inserted before the first live element rather than appended, so restored
   * blocks keep the order they are given and a session that has already
   * printed something does not find its past underneath its present.
   *
   * The boundary is an element of its own rather than a class on the last
   * restored block: ADR-0019 §3 asks for the difference to be VISIBLE, and a
   * line saying where the previous session ended is what a person reads — a
   * block that merely looks a little different is not an answer to "is this
   * shell still running".
   */
  restorePast(blocks: HTMLElement[]): void {
    if (blocks.length === 0) return
    const anchor = this.scrollbackInner.firstChild
    for (const el of blocks) this.scrollbackInner.insertBefore(el, anchor)
    const boundary = document.createElement('div')
    boundary.className = 'scrollback-restore-boundary'
    boundary.dataset.restoreBoundary = 'true'
    boundary.textContent = 'Previous session'
    this.scrollbackInner.insertBefore(boundary, anchor)
    // AND LAND AT THE NEWEST BLOCK, which is where a terminal always puts
    // you. The insert goes ABOVE everything, so the scroller keeps the
    // offset it had — 0, on a pane that has just been built — and the person
    // arrives at the oldest command of the previous session with the prompt
    // they were about to type at somewhere below the fold.
    //
    // UNCONDITIONAL, not `scrollToBottom()`'s follow-guard: the guard asks
    // whether the person scrolled away from the live end, and nobody has
    // scrolled anything yet. Worse, the answer is about to be wrong — the
    // sentinel that answers it sits at the bottom of the stack, and this
    // insert is exactly what pushes it out of the scroller, so the observer
    // would report "not following" one frame later for a scroll position the
    // user never chose.
    this._scrollToBottom()
  }

  /**
   * Hide the block chrome, or bring it back.
   *
   * Driven by the ALTERNATE BUFFER and nothing else. A rule was tried that hid
   * the chrome whenever the live region reached the top of the pane, on the
   * reasoning that a header has nowhere to live once a program fills the
   * screen. Warp does not do that: `top` runs there with its block header, its
   * duration and its actions intact, and only a true alt-screen program such as
   * `htop` takes the pane bare. Filling the pane is something a long `ls` does
   * too, and it is not a reason to take away the only handle on the block.
   *
   * So there is no classifier here, and no heuristic anywhere in this file: the
   * program's choice of buffer decides the chrome, and its output decides the
   * height (nocx-6w4z).
   */
  private _setFilledPane(on: boolean): void {
    if (on && this._filledPane) return
    this._filledPane = on
    this.scrollbackInner.classList.toggle('inner-fullscreen-mode', on)
  }

  /**
   * Alt-screen: the program takes the PANE.
   *
   * It used to take the viewport — `position: fixed` with all four insets —
   * which covered the tab strip and the activity bar, so `htop` erased the
   * window's own chrome while `top` (same kind of program, different buffer)
   * did not. Two programs doing the same thing looked like two different
   * products.
   *
   * The two paths are one treatment now: the live region grows to the
   * scroller, the block chrome steps aside, and the editor gives up its
   * reserved box. Which buffer the program chose stops being a layout
   * decision and goes back to being what it is — a detail of how the program
   * preserves your scrollback (nocx-6w4z).
   */
  enterFullscreen(): void {
    this._mode = 'fullscreen'
    this.xtermLiveViewport.style.height = ''
    this.xtermLiveContainer.className = 'xterm-live-container live-fullscreen'
    this.xtermInner.className = 'xterm-inner inner-fullscreen'
    this._setFilledPane(true)
    this._applyEchoShift()
    const max = this.scrollbackArea.clientHeight
    if (max > 0) this.xtermLiveContainer.style.height = `${max}px`
  }

  /** Exit alt-screen: restore normal layout. */
  exitFullscreen(): void {
    this._mode = 'idle'
    this.xtermLiveContainer.style.height = ''
    this.xtermLiveViewport.style.height = ''
    this._setFilledPane(false)
    this.xtermLiveContainer.className = 'xterm-live-container live-idle'
    this.xtermInner.className = 'xterm-inner'
    this._applyEchoShift()
    this.scrollbackInner.classList.remove('inner-fullscreen-mode')
    this._updateSeparator()
  }

  // ── Command cycle ─────────────────────────────────────────────────────

  /**
   * Called on OSC 133 C: create a running block, expand the live region.
   */
  onCommandStart(command: string, cwd: string, startLine: number): void {
    const cmd = command || '(empty)'
    this._blockManager.startBlock(cmd, cwd, startLine)
    this.setRunning()
  }

  /**
   * Called at editor submit time (nocx-atyf.4): start a running block
   * from the app-owned half of the lifecycle. The block is marked as
   * running immediately; when C arrives later the cReceived flag is set.
   * `outputStart` is the block's OUTPUT range start — the first row
   * serialized at freeze — which the app-owned submit sets to
   * startLine + 1 because the shell's echo lands on the creation line
   * (nocx-4yhi). It defaults to startLine for shell-originated blocks.
   */
  beginBlock(
    command: string,
    cwd: string,
    startLine: number,
    outputStart?: number,
    /** Who submitted the command (design §3.1, nocx-iadtt): the app-owned
     *  submit passes the minted author; a shell-originated block — the
     *  shell's own start event, no app-owned submit — is the human's
     *  shell and defaults to 'shell'. */
    author: CommandAuthor = 'shell',
  ): void {
    this._glide(() => this.beginBlockNow(command, cwd, startLine, outputStart, author))
  }

  /**
   * The same mutation WITHOUT the settle, for a caller whose own glide already
   * owns the whole transition.
   *
   * The app-owned submit is that caller: clearing the draft, releasing the
   * composer's box and opening the block are one movement to the eye, and they
   * all run in the keydown task with no paint between them. Nesting `_glide`
   * inside `_glide` does not compose — the inner call starts an animation on
   * `scrollbackInner` that the outer one then replaces in `_settleAnimations`
   * without cancelling, so the element carries two, and `_cancelGlides` at the
   * top of the inner call kills whatever the outer was retargeting. One
   * user-visible transition gets one glide, owned by whoever knows every
   * synchronous mutation in it.
   */
  beginBlockNow(
    command: string,
    cwd: string,
    startLine: number,
    outputStart?: number,
    author: CommandAuthor = 'shell',
  ): void {
    const cmd = command || '(empty)'
    this._blockManager.startBlock(cmd, cwd, startLine, outputStart, author)
    this.setRunning()
  }

  /**
   * Hand a frozen block's rows back to the DOM: the block's element now
   * owns them, so they must leave the grid — otherwise the live region
   * re-displays them below the block, and on a grid that has never
   * scrolled every finished command's rows appear a second time inside
   * the running one (nocx-m87n). The marker paths cleared the viewport at
   * every freeze before the attempt-driven lifecycle; restoring that is
   * this call.
   *
   * Guarded two ways. No NEWER command may own the running slot: a newer
   * command's rows sit BELOW the frozen ones in the same buffer, and
   * clearing would wipe its still-unserialized serialization window (the
   * deferred-fence overlap, nocx-m87n). And an alt-screen program owns
   * the pane: clearing would blank its screen.
   */
  private _clearFrozenRows(): void {
    if (this._blockManager.runningBlock !== null) return
    if (this._mode === 'fullscreen') return
    this._renderer.clearViewport()
  }

  /**
   * Called on OSC 133 D: serialize output, freeze the block.
   * @param getLine Accessor for xterm buffer lines.
   * @param endLine Absolute buffer line of the OSC 133 D marker.
   * @param exitCode Optional exit code from the D payload.
   */
  onCommandEnd(getLine: GetLineFn, endLine: number, exitCode: number | null): void {
    const rec = this._blockManager.freezeBlock(getLine, endLine, exitCode)
    if (rec) {
      this._settleFrozen()
    }
  }

  /**
   * Put the top of the finished command's block at the top of the view.
   *
   * Nothing scrolled here at all before, so the view stayed wherever the
   * running command had left it — and a program that had filled the pane left
   * it at the top of a block taller than the viewport, showing its first screen
   * with the prompt somewhere far below. Neither "stay put" nor "jump to the
   * bottom" is right for a block UI: what you want to read when a command
   * finishes is the command and the start of what it printed.
   *
   * A frame late on purpose. `setIdle` has just collapsed the live region and
   * the block was inserted in the same tick, so the scroller's height is still
   * the old one until layout runs.
   */
  private _scrollToLastBlockStart(): void {
    requestAnimationFrame(() => {
      const blocks = this.scrollbackInner.querySelectorAll('.cmd-block')
      const last = blocks[blocks.length - 1]
      if (!last) return
      // ONLY FOR A BLOCK THAT DOES NOT FIT, which is the case this was
      // written for: a program that filled the pane leaves the view at the top
      // of a block taller than the viewport, showing its first screen with the
      // prompt far below. A block that fits is already whole on screen — the
      // stack hangs from the bottom edge and we are following it — so there is
      // nothing to bring into view, and asking anyway put a second owner on
      // the scroll position beside the settle that was still unwinding
      // (nocx-i4h04.2: `scrollIntoView` reads the transformed box, and in the
      // container it scrolled a row it then had to give back).
      if (last.getBoundingClientRect().height <= this.scrollbackArea.clientHeight) return
      // Through the glide: the freeze's own settle may still be unwinding, and
      // a scroll that lands as a jump in the middle of it is the twitch this
      // whole seam exists to remove.
      this._glide(() => {
        last.scrollIntoView({ block: 'start', behavior: 'instant' })
      })
    })
  }

  // ── clear handling ────────────────────────────────────────────────────

  /**
   * Check if a command was `clear` (or starts with `clear`). If so, clear
   * all DOM blocks. The xterm viewport is already cleared by the escape
   * sequence `clear` emits — we just clean up our blocks.
   */
  maybeClear(command: string): void {
    const trimmed = command.trim()
    const firstWord = trimmed.split(/\s+/)[0] ?? ''
    const isClear = firstWord === 'clear' || firstWord.endsWith('/clear')
    if (isClear) {
      this._blockManager.clearAll()
      this._updateSeparator()
      // The ask chip's block went with the blocks: close the mode, or the
      // chip's owner would hold a scope that no longer exists (nocx-x8s2.2).
      this._onClear?.()
    }
  }

  /** Scroll to the bottom, unless the user has scrolled away from the live
   *  end. */
  scrollToBottom(): void {
    if (!this._following) return
    this._scrollToBottom()
  }

  private _scrollToBottom(): void {
    this.scrollbackArea.scrollTo({
      top: this.scrollbackArea.scrollHeight,
      behavior: 'instant',
    })
  }

  /**
   * MOVE THE PANE, DO NOT JUMP IT.
   *
   * Every structural change to a command's block — it opens, it freezes —
   * changes the pane's height, and the stack of blocks hangs from the bottom
   * edge of the scroller (`.scrollback-inner` has `margin-top: auto`), so all
   * of it moves at once. Two of those in thirty milliseconds is what the
   * owner reported as the pane "not settling": the eye reads a sequence of
   * instant displacements as a twitch, whatever their direction.
   *
   * This is FLIP, and the F and the I are why it is not the animation that
   * was tried and removed. The DOM change is applied WHOLE and at once; then
   * the stack is given the inverse translation, so the frame looks exactly as
   * it did; then the translation is released over `SETTLE_MS`. What animates
   * is a TRANSFORM — it takes no part in layout, so nothing measuring these
   * elements can read a value mid-flight, which is precisely what a
   * `transition: height` on the live region did (it re-laid the box on every
   * frame of output and never finished, so the scroll aimed at a target that
   * had already moved).
   *
   * ONE element carries it, because the stack is one element: the blocks, the
   * separator and the live region are all inside `.scrollback-inner`. That is
   * also why "am I following the output" is answered by a sentinel OUTSIDE it
   * — `IntersectionObserver` reports the TRANSFORMED box, so while the stack
   * is displaced the live region it used to watch is out of the scroller and
   * the pane concludes the person scrolled away. That turned following off
   * silently: no more scroll-to-bottom as output arrives, and every later
   * glide skipped, which is a jump. It reproduced only where a command
   * finishes INSIDE the settle it started — the owner's machine at 27ms,
   * never the container at 900 (nocx-i4h04.3).
   *
   * Skipped when the person is not following the output — movement they did
   * not cause is not theirs to watch — and under `prefers-reduced-motion`,
   * like every other motion in this app. An in-flight glide is measured INTO
   * the next one rather than cancelled first, so a change landing mid-settle
   * retargets instead of snapping (nocx-i4h04.2).
   */
  /**
   * Run `mutate` and play its displacement back as the settle glide.
   *
   * The public door to `_glide`, for a mutation this controller does not own.
   * The editor's box is the case that opened it: the composer leaves the
   * layout at submit — it has to, because the keyboard goes to the program
   * and nocx may not sniff the stream to find out whether the program wants
   * it (ADR-0004) — and the scrollback hangs from the scroller's bottom edge,
   * so its 77px leaves as a jump. Reserving the box instead was the older
   * answer and cost an inline TUI four rows of pane (nocx-i4h04). The
   * displacement is not the defect; an UNGLIDED displacement is.
   */
  settleAround(mutate: () => void): void {
    this._glide(mutate)
  }

  /**
   * Put the end of the output back at the bottom edge, if that is where the
   * person was.
   *
   * For a caller outside this controller whose mutation SHRINKS the scroller —
   * the composer returning at the end of a command is the one that needs it.
   * `overflow-anchor: none` is set on the scroller deliberately (the settle
   * owns the position, and an anchor would be a second owner), so nothing
   * holds the bottom for us: the 77px the composer takes back would otherwise
   * hide the last rows the command printed behind it.
   *
   * Call it INSIDE the mutation passed to `settleAround`, never after. The
   * glide measures where the stack ended up as soon as the mutation returns,
   * and a scroll landing after that measurement is a displacement it has
   * already decided not to play back.
   */
  scrollToBottomIfFollowing(): void {
    if (this._following) this._scrollToBottom()
  }

  private _glide(mutate: () => void): void {
    // Measured with any in-flight transform still applied: this is where the
    // stack IS, not where layout says it belongs. A capture taken earlier in
    // this same task wins — see `_captureGlideOrigin`.
    // WHERE THE STACK WAS LAST PAINTED, which is not where it is now.
    // Several changes land in one task — the live region takes the shell's
    // last row, then the block freezes and takes it back — and the frames
    // between them are never drawn. Measuring here would invert the glide for
    // exactly those, and it did: the block approached its place from the
    // wrong side by one row (nocx-i4h04.2, measured in the container).
    const before = this._paintedTop ?? this.scrollbackInner.getBoundingClientRect().top
    // The in-flight transform goes BEFORE the mutation, not after it. A
    // `scrollIntoView` inside `mutate` reads the element's transformed box and
    // would scroll by the offset the glide is currently hiding — two owners of
    // the scroll position, which is the defect `overflow-anchor: none` is set
    // against one line further out. Cancelling first leaves the mutation to
    // see layout, and `before` above has already remembered where the stack
    // looked to be.
    this._cancelGlides()
    mutate()
    this._watchPaintedTop()
    if (!this._following || !this._motionAllowed()) return
    const dy = before - this.scrollbackInner.getBoundingClientRect().top
    // Below a pixel there is nothing to watch. Above a viewport it is not a
    // settle at all — a clear, a restore, a jump the person asked for — and
    // gliding it would be a second animation over their own gesture.
    if (!Number.isFinite(dy) || Math.abs(dy) < 1) return
    if (Math.abs(dy) > this.scrollbackArea.clientHeight) return
    // NO SCROLLBAR FOR THE SETTLE'S OWN OVERFLOW. The stack hangs from the
    // scroller's bottom edge, so displacing it downward — which is what the
    // inverse of a growth IS — puts its last pixels past that edge and makes
    // the scroller scrollable by exactly the amount the glide is hiding. The
    // owner saw the bar flash on every command. `hidden` rather than `clip`:
    // it keeps the scroll offset and the element a scroll container, and
    // `scrollbar-gutter: stable` means no width changes hands either way. The
    // cost is named: a wheel during the settle is ignored, for the ~140ms it
    // lasts, and only when the person was already following the output.
    this.scrollbackArea.classList.add('is-settling')
    for (const el of [this.scrollbackInner]) {
      const anim = el.animate(
        [{ transform: `translateY(${dy}px)` }, { transform: 'translateY(0px)' }],
        { duration: SETTLE_MS, easing: SETTLE_EASING },
      )
      this._settleAnimations.set(el, anim)
      anim.finished.then(
        () => {
          if (this._settleAnimations.get(el) === anim) this._settleAnimations.delete(el)
          if (this._settleAnimations.size === 0) {
            this.scrollbackArea.classList.remove('is-settling')
          }
        },
        () => {
          /* cancelled by the next glide — the next one owns the element */
        },
      )
    }
  }

  private _cancelGlides(): void {
    for (const anim of this._settleAnimations.values()) anim.cancel()
    this._settleAnimations.clear()
    this.scrollbackArea.classList.remove('is-settling')
  }

  /**
   * Record where the stack is, once per frame, while there is something to
   * watch.
   *
   * An animation frame runs after that frame's tasks and before its paint, so
   * what it reads is exactly what is about to be drawn — the position the
   * person will have seen when the next change arrives. That is the only
   * honest origin for a glide: within one task the live region can take a row
   * and the freeze give it back, and neither of those states is ever on
   * screen.
   *
   * It runs while a command is running or a glide is unwinding, and stops
   * itself otherwise: an idle pane watching nothing would be one layout read
   * per frame per pane, for the life of the window.
   */
  private _watchPaintedTop(): void {
    if (this._paintWatch !== 0) return
    const tick = (): void => {
      // Re-entrancy, not recursion: a host that runs animation-frame callbacks
      // synchronously — which the unit tests do, to make a frame deterministic
      // — would otherwise re-enter here until the stack ends.
      if (this._painting) return
      this._painting = true
      try {
        this._paintedTop = this.scrollbackInner.getBoundingClientRect().top
        if (this._mode === 'running' || this._settleAnimations.size > 0) {
          this._paintWatch = requestAnimationFrame(tick)
          return
        }
        this._paintWatch = 0
        this._paintedTop = null
      } finally {
        this._painting = false
      }
    }
    this._paintWatch = requestAnimationFrame(tick)
  }

  /** Whether this build may move anything: the Web Animations API has to
   *  exist (it does not in jsdom, where a test asserts the layout and not the
   *  motion) and the person must not have asked for less of it. */
  private _motionAllowed(): boolean {
    if (typeof this.scrollbackInner.animate !== 'function') return false
    const mq = window.matchMedia?.('(prefers-reduced-motion: reduce)')
    return mq ? !mq.matches : true
  }

  /**
   * The block has taken its rows. Hand them back to the DOM, collapse the
   * live region and bring the block's start into view — as ONE settle.
   *
   * Six paths freeze a block (the marker path, the attempt, two
   * abandonments, an entered environment, and the deferred fence), and every
   * one of them did these three calls in the same order by hand. That is one
   * behaviour with six copies: the glide had to be added in six places, and
   * the seventh path to arrive would have been written without it.
   */
  private _settleFrozen(): void {
    this._glide(() => {
      this._clearFrozenRows()
      this.setIdle()
    })
    this._scrollToLastBlockStart()
  }

  private _updateSeparator(): void {
    const hasBlocks = this._blockManager.blocks.length > 0
    this.separator.style.display = hasBlocks && this._mode !== 'fullscreen' ? '' : 'none'
  }

  /** The attempt-driven freeze (ADR-0024 §7 projection, bead nocx-u7uh.8):
   *  the LOGICAL freeze — the block's status, exit code and attempt
   *  binding — lands on the authenticated completion event alone (history
   *  and the ledger have already landed); only the VISUAL boundary (which
   *  rows belong to the block) waits for the matching render fence. When
   *  the fence bytes have not arrived, this returns false and the live
   *  region stays up — the manager's onDeferredFreeze settles it on the
   *  sighting, or after FENCE_DEFER_MS at the current output end. The
   *  authority check (kernel freezeBlock) is the caller's. */
  freezeFromAttempt(attempt: ExecutionAttempt, endLine: number): boolean {
    const getLine = (y: number) => this._renderer.getBufferLine(y)
    const rec = this._blockManager.freezeFromAttempt(attempt, getLine, endLine, () =>
      this._renderer.cursorLine(),
    )
    if (rec) {
      this._settleFrozen()
      return true
    }
    return false
  }

  /** The attempt-driven abandonment: the attempt went `unknown`, the block
   *  freezes as abandoned — never successful (ADR-0024 §5). */
  abandonAttempt(attempt: ExecutionAttempt, endLine: number): boolean {
    const getLine = (y: number) => this._renderer.getBufferLine(y)
    const rec = this._blockManager.abandonAttempt(attempt, getLine, endLine)
    if (rec) {
      this._settleFrozen()
      return true
    }
    return false
  }

  /** Abandon a running block that never bound to an attempt: its domain
   *  ended before any start frame arrived, so nothing will ever complete it
   *  (nocx-mlyu). Unknown, never successful. */
  abandonUnbound(endLine: number): boolean {
    const getLine = (y: number) => this._renderer.getBufferLine(y)
    const rec = this._blockManager.abandonUnbound(getLine, endLine)
    if (rec) {
      this._settleFrozen()
      return true
    }
    return false
  }

  /** Freeze the running block as ENTERED (nocx-95kt): the command opened a
   *  nested environment, so it ends here, painted as neither success nor
   *  failure, and the running slot is freed for the blocks that follow. */
  enterBlock(endLine: number): boolean {
    const getLine = (y: number) => this._renderer.getBufferLine(y)
    const rec = this._blockManager.freezeEntered(getLine, endLine)
    if (rec) {
      this._settleFrozen()
      return true
    }
    return false
  }

  dispose(): void {
    if (this._paintWatch !== 0) cancelAnimationFrame(this._paintWatch)
    this._paintWatch = 0
    this._cancelGlides()
    this._followObserver?.disconnect()
    this._followObserver = null
    this._blockManager.dispose()
    this.scrollbackLayout.remove()
  }
}

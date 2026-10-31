// DOM scrollback controller — manages block layout, live-region visibility,
// alternate-screen transitions, and clear-boundary cleanup. It owns the
// scrollback DOM structure inside one pane.
//
// ADR-0024 (bead nocx-u7uh.7): blocks project authenticated lifecycle
// attempts. Freeze and abandon decisions come from the lifecycle kernel;
// this controller only applies their presentation state.

import type { CommandAuthor } from '../command-ledger'
import {
  BlockManager,
  type BlockRecord,
  type BlockScreenAnchor,
  type RunningBlockActions,
} from './blocks'
import type { CommandSnapshotStore } from '../command-snapshot'
import type { StoredBlockRows } from './block-rows'
import { publishCellMetric, publishRowPitch } from './cell-metric'
import type { ExecutionAttempt } from '../lifecycle/state'
import type { AgentDump } from '../generated/agent.dump'
import { windowPet } from '../pets/window-pet'
import { SETTLE_EASING, SETTLE_MS, motionAllowed } from '../motion'
import { isAtTail, TailFollow } from './tail-follow'
export type LiveRegionMode = 'idle' | 'running' | 'fullscreen' | 'unstructured'

export interface ScrollbackControllerOpts {
  /** The pane this controller owns the scrollback inside. */
  pane: HTMLElement
  /** Current painter row height in CSS pixels, or null before its first frame. */
  liveContentHeight: () => number | null
  /** The painter's measured cell width and row pitch, when the frame is laid out. */
  cellMetrics: () => { cellWidth: number; cellHeight: number } | null
  /** This tab's command-existence snapshot store (OSC 636). */
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
  /** Paints backend-owned rows through the shared row painter. */
  paintStoredRows?: (block: HTMLElement, rows: StoredBlockRows) => void
  /** What a session is called to a person — passed straight to the block
   *  manager, which hands it to every tool-call line it draws (nocx-vnzek).
   *  This controller neither derives nor caches it. */
  sessionName?: (sessionId: string) => string | null
  /** The durable text of one ANSWER entry — passed straight to the block
   *  manager, which hands it to the copy menu of every answer block it
   *  frames (nocx-v13pd). This controller neither fetches nor caches it. */
  answerText?: (entryId: string) => Promise<string | null>
  /** The recorded provider drives for one finished answer turn. */
  dump?: (entryId: string) => Promise<AgentDump>
  /** Reads back what a tool call returned, for the call's own expansion
   *  (nocx-hp8p2.13). Absent and a call draws none. */
  toolResult?: (actionEntryId: string) => Promise<string | null>
  /** What a RUNNING block's ⋮ menu can do about the command in it — passed
   *  straight to the block manager (nocx-92gfl, nocx-23rph). This controller
   *  neither summons the editor nor signals a session. */
  runningActions?: RunningBlockActions
  /** Fired whenever the mode settles — idle, running, unstructured,
   *  fullscreen. The live tier's surface (scrollback/live-history.ts) is
   *  the unstructured mode's alone, and this is how it learns the pane
   *  changed hands (nocx-zg3k3.10.4). */
  onModeChanged?: (mode: LiveRegionMode) => void
  /** Fired when the follow sentinel's answer CHANGES: the reader left the
   *  live end, or returned to it. The same observer that owns `_tail`
   *  delivers it — no second watcher of one scroller. */
  onTailFollow?: (following: boolean) => void
}

export class ScrollbackController {
  readonly scrollbackLayout: HTMLElement
  readonly scrollbackArea: HTMLElement
  readonly scrollbackInner: HTMLElement
  /** Outer clipping container — height changes with mode. */
  readonly terminalLiveContainer: HTMLElement
  /** The end of the output, as a one-pixel box a sibling of the stack — the
   *  follow observer's target. See the constructor for why it is not the live
   *  region. */
  readonly followSentinel: HTMLElement
  /** Inner clipping window. It tracks the rows written while the outer box
   *  supplies the same height to flex layout. */
  readonly terminalLiveViewport: HTMLElement
  /** Inner wrapper with stable min-height — the painter surface mounts here
   *  so row geometry stays sane regardless of the clipping container height. */
  readonly terminalLiveInner: HTMLElement
  readonly separator: HTMLElement
  /** This tab's OSC 636 store, kept because a block built OUTSIDE the manager
   *  — a restored one (nocx-m3fqk) — needs the same instance its live
   *  neighbours judge against, and the manager's copy is private. */
  readonly snapshotStore: CommandSnapshotStore

  private _blockManager: BlockManager
  private readonly _liveContentHeight: () => number | null
  private readonly _cellMetrics: () => { cellWidth: number; cellHeight: number } | null
  private _mode: LiveRegionMode = 'idle'
  /** A ready prompt asked for the idle layout while a completed block was
   *  still waiting for its block.closed; applied when the close lands, and
   *  dropped the moment a command owns the live region again. */
  private _idleRequested = false
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
  /** Most recent exact-row capture used to decide whether the shell echo still occupies the live grid. */
  private _lastLiveHeightPx: number | null = null
  /** True while the end of the live output is visible. The intent lives in
   *  `tail-follow.ts`, which owns the same concept for the summoned answer
   *  list — one predicate, one memory, two scrollers. */
  private readonly _tail = new TailFollow()
  /** The glide in flight per element, so a change landing mid-settle
   *  retargets rather than snapping — see `_glide`. */
  private readonly _settleAnimations = new Map<Element, Animation>()
  /** Where the stack was on the last painted frame, and the handle of the
   *  loop that records it — see `_watchPaintedTop`. */
  private _paintedTop: number | null = null
  private _paintWatch = 0
  private _painting = false
  private _followObserver: IntersectionObserver | null = null
  /** Watches the scroller's own box, so a box the mutation paths know
   *  nothing about still keeps the tail — see the constructor. */
  private _boxResizeObserver: ResizeObserver | null = null
  /** The ask mode's owner (nocx-x8s2.2): called when the scrollback is
   *  cleared, so the chip — and with it the agent target — closes when its
   *  block is gone. */
  private _onClear?: () => void
  private readonly _onModeChanged?: (mode: LiveRegionMode) => void
  private readonly _onTailFollow?: (following: boolean) => void

  constructor(opts: ScrollbackControllerOpts) {
    this._liveContentHeight = opts.liveContentHeight
    this._cellMetrics = opts.cellMetrics
    this._onClear = opts.onClear
    this._onModeChanged = opts.onModeChanged
    this._onTailFollow = opts.onTailFollow
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
    // viewport clips the painted grid. Both follow the written rows so the
    // running shape matches the frozen block it becomes.
    this.terminalLiveContainer = document.createElement('div')
    this.terminalLiveContainer.className =
      'terminal-live-container terminal-live-container live-idle'

    this.terminalLiveViewport = document.createElement('div')
    this.terminalLiveViewport.className = 'terminal-live-viewport terminal-live-viewport'

    this.terminalLiveInner = document.createElement('div')
    this.terminalLiveInner.className = 'terminal-live-inner terminal-live-inner'
    this.terminalLiveViewport.appendChild(this.terminalLiveInner)
    this.terminalLiveContainer.appendChild(this.terminalLiveViewport)

    // Separator between blocks and live region — inserted before the
    // live container so blocks stack above it. Hidden when no blocks.
    //
    // THE ONLY TWO CHILDREN THIS FILE PUTS IN. They are the container's own
    // frame: built with it, never removed, and the anchor the manager
    // inserts against. Everything that comes and goes — a live block, an
    // answer, the restored past and its boundary — belongs to BlockManager
    // and enters through its `_own` (nocx-0zb1m). A second inserter here is
    // what let `clear` miss half the scrollback.
    this.separator = document.createElement('div')
    this.separator.className = 'scrollback-separator'
    this.separator.style.display = 'none'
    this.scrollbackInner.appendChild(this.separator)
    this.scrollbackInner.appendChild(this.terminalLiveContainer)

    this.scrollbackLayout.appendChild(this.scrollbackArea)

    // Insert the layout as the first child of the pane (before the editor,
    // which is absolute-positioned).
    opts.pane.insertBefore(this.scrollbackLayout, opts.pane.firstChild)

    this._blockManager = new BlockManager(this.scrollbackInner, this.terminalLiveContainer, {
      now,
      snapshotStore: opts.snapshotStore,
      // A DEFERRED freeze landed inside the manager (the backend's
      // block.closed met the completion): settle the block the way a direct
      // freeze does — the pet
      // learns the verdict. Nothing clears: the grid is the frame the
      // backend diffs against (nocx-2v80t.3.3).
      onDeferredFreeze: (rec) => {
        this._finishFreeze(rec)
        // The idle layout a ready prompt asked for while this close was
        // pending is applied now that nothing is waiting (see setIdle).
        if (this._idleRequested && this._blockManager.runningBlock === null) this.setIdle()
      },
      onBlockFrozen: opts.onBlockFrozen,
      paintStoredRows: opts.paintStoredRows,
      sessionName: opts.sessionName,
      answerText: opts.answerText,
      dump: opts.dump,
      toolResult: opts.toolResult,
      runningActions: opts.runningActions,
    })

    // ── Frozen block cell metric (nocx-yy9g) ──────────────────────────
    // Frozen blocks and the live painter share the cell metric published by
    // scrollback/cell-metric.ts. Subscribe before the first accepted frame;
    // the shared font probe supplies the initial value, and later frame or
    // viewport changes republish it. Existing blocks adopt current geometry.
    this.cellMetricsChanged()

    // ── Follow state ─────────────────────────────────────────────────────
    // THE FOLD NEVER DETACHES WHILE FOLLOWING (nocx-1ebrs.40). The pane's box
    // redistribution — the composer's animated hand-off, the process bar's
    // swap, a pane or window resize — changes this scroller's box without any
    // block mutation, and the mutation paths below do not run for it: the
    // frames the box moves are the frames the fold moves. Measured on main:
    // the composer's 130px left, the echo and the process bar landed below the
    // fold, and nothing re-pinned until the freeze, which snapped the view 98px
    // in one frame. Keep the live end on the bottom edge whenever the box
    // moves under a follower. ResizeObserver delivers after that frame's
    // layout and before its paint, so the pin lands in the same frame as the
    // box that caused it. Guarded like the observer below: jsdom has none.
    if (typeof ResizeObserver === 'function') {
      this._boxResizeObserver = new ResizeObserver(() => {
        if (this._tail.following) this._scrollToBottom()
      })
      this._boxResizeObserver.observe(this.scrollbackArea)
    }

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
        for (const e of entries) {
          const was = this._tail.following
          this._tail.observe(this.scrollbackArea, e.isIntersecting)
          // The flip, not the delivery: the reader left the live end or
          // returned to it. The live tier's surface drops a stale past and
          // re-arms its paging on the return (nocx-zg3k3.10.4).
          if (this._tail.following !== was) this._onTailFollow?.(this._tail.following)
        }
      },
      { root: this.scrollbackArea, threshold: 0 },
    )
    this._followObserver.observe(this.followSentinel)
  }

  /** The painter surface mounts into this stable terminal-live-inner wrapper,
   *  NOT the clipping live-container, so the grid never collapses to one row. */
  get mountTarget(): HTMLElement {
    return this.terminalLiveInner
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

  /** Collapse the live region only once no finished block still waits for
   *  its block.closed: until then its closing rows are in the live terminal
   *  and not yet in the block, and collapsing would hide them. The request
   *  is remembered and applied when the close lands (nocx-2v80t.3.27). */
  setIdle(): void {
    if (this._mode === 'fullscreen') return
    if (this._blockManager.closePending) {
      this._idleRequested = true
      return
    }
    this._idleRequested = false
    this._mode = 'idle'
    // Both inline heights have to go: running content height and its clip
    // otherwise outrank the idle/fullscreen mode classes.
    this.terminalLiveContainer.style.height = ''
    this.terminalLiveViewport.style.height = ''
    this._setFilledPane(false)
    this.terminalLiveContainer.className =
      'terminal-live-container terminal-live-container live-idle'
    this.terminalLiveInner.className = 'terminal-live-inner terminal-live-inner'
    this.scrollbackInner.classList.remove('inner-unstructured')
    // The echo shift is a running-mode property: clear it with the region.
    this._applyEchoShift()
    this._updateSeparator()
    this._seatLive()
    this._onModeChanged?.(this._mode)
  }

  /** Show live output below the running block. */
  setRunning(): void {
    this._idleRequested = false
    if (this._mode === 'fullscreen') return
    const entering = this._mode !== 'running'
    this._mode = 'running'
    if (entering) {
      this.terminalLiveContainer.style.height = ''
      this.terminalLiveViewport.style.height = ''
    }
    this._setFilledPane(false)
    this.terminalLiveContainer.className =
      'terminal-live-container terminal-live-container live-running'
    this.terminalLiveInner.className = 'terminal-live-inner terminal-live-inner'
    this.scrollbackInner.classList.remove('inner-unstructured')
    // The block just opened; its echo row is the grid's top row, so the
    // shift applies from the first frame (nocx-w1n4).
    this._applyEchoShift()
    this._updateSeparator()
    this._seatLive()
    this._onModeChanged?.(this._mode)
    this._watchPaintedTop()
    // Size both layers from the rows that exist now. A short command therefore
    // opens beside the prompt instead of reserving a pane-high empty window.
    // Both layers change in the same layout pass: a transition here can still
    // be half-open when a fast command freezes, creating an extra jump.
    this.setLiveHeight(this._liveContentHeight())
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
    this.terminalLiveViewport.style.height = ''
    this.terminalLiveContainer.className =
      'terminal-live-container terminal-live-container live-unstructured'
    this.terminalLiveInner.className = 'terminal-live-inner terminal-live-inner inner-fullscreen'
    this._setFilledPane(true)
    this._applyEchoShift()
    // The live tier's surface is this mode's alone: the class is what the
    // stylesheet's filled-pane exclusion reads to hand the rows above the
    // live rectangle back (nocx-zg3k3.10.4).
    this.scrollbackInner.classList.add('inner-unstructured')
    this._seatLive()
    this._fillPane()
    this._onModeChanged?.(this._mode)
  }

  /** ADR-3kqrem7v: one fixed header cost in either buffer. The token is
   * owned by the UI kit, not measured from a presentation-dependent seat. */
  get runningHeaderHeightPx(): number {
    const value = parseFloat(
      getComputedStyle(this.scrollbackArea).getPropertyValue('--control-height-md'),
    )
    return Number.isFinite(value) && value > 0 ? value : 32
  }

  get runningLiveCap(): number | null {
    if (this._mode !== 'running') return null
    const max = this._areaHeightPx() - this.runningHeaderHeightPx
    return max > 0 ? max : null
  }

  /** The scroller's laid-out height in the FRACTIONAL CSS pixels the browser
   *  lays out in.
   *
   *  `clientHeight` rounds to a whole pixel, and the grid the pane fits and
   *  this cap are two halves of one geometry: rounding the scroller here while
   *  the fit added the composer's fractional box rounded nowhere made the two
   *  differ by a fraction, the pty was fitted a row into space the running box
   *  did not have, and the last row was clipped at the bottom
   *  (nocx-3an1s.1.4). jsdom computes no layout and answers a zero rect, so
   *  the named `clientHeight` stands there — the same seam every unit test
   *  below mocks. */
  private _areaHeightPx(): number {
    const rect = this.scrollbackArea.getBoundingClientRect().height
    return rect > 0 ? rect : this.scrollbackArea.clientHeight
  }

  /**
   * Size the live output to the rows it actually shows.
   *
   * The accepted grid snapshot supplies the full painted height, including
   * the echoed command row that `_applyEchoShift` translates above the clip.
   * Both the outer flow box and
   * its inner clip reserve only the remainder. That keeps running geometry
   * aligned with the frozen body, which omits the same echo row.
   * Height changes are synchronous: animation can be only 2–4px open when a
   * fast command freezes, turning one layout change into two movements.
   */
  /** Recompute the live clip after an accepted backend output anchor/frame update. */
  outputAnchorChanged(): void {
    this.setLiveHeight(this._lastLiveHeightPx ?? this._liveContentHeight())
  }

  setLiveHeight(px: number | null): void {
    // The two modes where the terminal owns the pane are re-measured from the
    // scroller, and `px` — the running region's measured content — is not
    // consulted there: the box is the pane, whatever the grid holds. This is
    // the seam terminal-content's scheduleLiveResize already calls on every
    // re-measure, which is why the fill belongs here rather than in a method
    // every caller has to remember to call (the version that had to be
    // remembered was called exactly twice, at the two mode entries).
    if (this._mode === 'unstructured' || this._mode === 'fullscreen') {
      this._fillPane()
      return
    }
    if (this._mode !== 'running') return
    this._sizeRunningSeat()
    this._lastLiveHeightPx = px
    // The backend anchor and its accepted frame decide the echo shift. Apply
    // it before height guards so a frame update can release the shift even
    // when the measured live box does not change.
    this._applyEchoShift()
    // Null means the accepted grid has no usable metrics; the class fallback
    // height stands. Zero is a grid nobody has written to yet. While the
    // running block still owns its waiting stand-in, keep that fallback:
    // the indicator is absolutely positioned and the padding-only,
    // border-box-sized live region would clip it before the editor's next
    // layout pass. Once output retires the stand-in, zero may size the
    // region to the body's padding again.
    if (px === null) return
    if (
      px === 0 &&
      this.terminalLiveContainer.querySelector(':scope > .cmd-answer-typing') !== null
    ) {
      return
    }
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
    if (this.terminalLiveContainer.style.height !== box) {
      this.terminalLiveContainer.style.height = box
      if (this._tail.following) this._scrollToBottom()
    }
    const inner = `${content}px`
    if (this.terminalLiveViewport.style.height !== inner) {
      this.terminalLiveViewport.style.height = inner
    }
  }

  /** The body padding this region wears while it is the running block's body
   *  — read off the element rather than repeated here, so the stylesheet
   *  stays the one place the number lives (`--cmd-output-pad-*`). Zero
   *  wherever there is no layout to read (jsdom), which is the same answer
   *  the class's own fallback gives. */
  private _bodyPaddingPx(): number {
    const cs = getComputedStyle(this.terminalLiveContainer)
    const top = parseFloat(cs.paddingTop)
    const bottom = parseFloat(cs.paddingBottom)
    return (Number.isFinite(top) ? top : 0) + (Number.isFinite(bottom) ? bottom : 0)
  }

  /** The vertical offset that hides the shell echo at the backend-anchored
   * screen row. Both row text and offset are supplied only by an accepted,
   * identity-matching CellModel snapshot. */
  private _echoShiftPx(): number {
    if (this._mode !== 'running') return 0
    const rec = this._blockManager.runningBlock
    if (!rec) return 0
    const cell = this._cellMetrics()?.cellHeight ?? 0
    if (!cell || cell <= 0) return 0
    // Until the backend's anchor places the output, the row the block opened
    // on is the bound: the rows above it are the previous command's, still on
    // a screen nothing clears (nocx-zg3k3.32). The echo itself stays visible
    // for that moment; the anchor is what knows where it ends.
    if (rec.outputAnchorOffset === undefined) return (rec.startOffset ?? 0) * cell
    // A placed anchor carries the echo row's reading, and the shift is
    // released only when that reading no longer ends with the command — a
    // reflow can renumber the row it names (nocx-zg3k3.30). An anchor this
    // screen cannot place carries no reading at all, because it names no
    // visible row: its offset is already the whole grid (nocx-1ebrs.66.1.24).
    const command = rec.command.trim()
    if (
      rec.outputAnchorText !== undefined &&
      command !== '' &&
      !rec.outputAnchorText.endsWith(command)
    ) {
      return 0
    }
    return rec.outputAnchorOffset * cell
  }

  /** Keep an assistant-owned command inside its turn while extending ONLY
   * its active rectangle through the transcript's retained ancestor insets.
   * This is placement, not a second desired-grid calculation (ADR-3kqrem7v).
   * Freeze replaces this element, so history keeps its normal indentation. */
  private _sizeRunningSeat(): void {
    const block = this._blockManager.runningBlock?.el
    const width = this.scrollbackArea.clientWidth
    if (!block || width <= 0) return
    let left = 0
    for (
      let ancestor = block.parentElement;
      ancestor && ancestor !== this.scrollbackArea;
      ancestor = ancestor.parentElement
    ) {
      const style = getComputedStyle(ancestor)
      left += (parseFloat(style.paddingLeft) || 0) + (parseFloat(style.borderLeftWidth) || 0)
    }
    const seatWidth = `${width}px`
    const offset = `${-left}px`
    if (block.style.width !== seatWidth) block.style.width = seatWidth
    if (block.style.marginLeft !== offset) block.style.marginLeft = offset
  }

  private _fullscreenHeader: HTMLElement | null = null

  private _restoreRunningHeader(): void {
    const header = this._fullscreenHeader
    if (!header) return
    this._blockManager.runningBlock?.el.prepend(header)
    this._fullscreenHeader = null
  }

  private _seatLive(): void {
    // Restore the SAME header before the block manager can freeze/update it.
    // DOM focus is preserved across a seat move; it is not a second keyboard
    // state and never changes the desired PTY geometry (ADR-3kqrem7v).
    const focused = document.activeElement
    const header = this._fullscreenHeader
    const movingHeader = header ?? this._blockManager.runningBlock?.header ?? null
    const heldHeaderFocus =
      focused instanceof HTMLElement && movingHeader?.contains(focused) === true
    if (header !== null && this._mode !== 'fullscreen') {
      const block = this._blockManager.runningBlock?.el
      if (block) block.prepend(header)
      else header.remove()
      this._fullscreenHeader = null
    }
    if (this._mode === 'running') {
      this._blockManager.holdLiveInRunningBlock()
      this._sizeRunningSeat()
    } else this._blockManager.releaseLiveRegion()
    if (this._mode === 'fullscreen' && this._fullscreenHeader === null) {
      const active =
        this._blockManager.runningBlock?.el.querySelector<HTMLElement>(':scope > .cmd-header')
      if (active) {
        this.scrollbackInner.insertBefore(active, this.terminalLiveContainer)
        this._fullscreenHeader = active
      }
    }
    if (
      heldHeaderFocus &&
      focused instanceof HTMLElement &&
      focused.isConnected &&
      document.activeElement !== focused
    ) {
      focused.focus({ preventScroll: true })
    }
  }

  /** Apply the echo shift to the grid, or clear it. The write is guarded:
   *  identical values are skipped so the per-frame sizing pass does not
   *  rewrite the style on every chunk of output. */
  private _applyEchoShift(): void {
    const px = this._echoShiftPx()
    const next = px > 0 ? `translateY(-${px}px)` : ''
    if (this.terminalLiveInner.style.transform === next) return
    this.terminalLiveInner.style.transform = next
  }

  /** Re-publish layout metrics after the cell model accepts new geometry. */
  cellMetricsChanged(): void {
    const metric = this._cellMetrics()
    if (metric === null) return
    publishCellMetric(this.scrollbackInner, metric.cellWidth)
    publishRowPitch(this.scrollbackInner, metric.cellHeight)
  }

  /**
   * Draw the pane's recorded blocks, and land the view where a terminal always lands.
   *
   * The blocks and any previous-session boundary go in through the MANAGER
   * (nocx-0zb1m): putting them in here made a second owner of this container's
   * children, and `clear` — which asks the manager — could not see half the
   * scrollback. What is left here is the part that is genuinely the
   * controller's, because it is about the VIEW rather than about the blocks.
   */
  restorePast(blocks: HTMLElement[], options?: { previousSession?: boolean }): void {
    if (blocks.length === 0) return
    this._blockManager.restorePast(blocks, options)
    // AND LAND AT THE NEWEST BLOCK, which is where a terminal always puts
    // you. The insert goes ABOVE everything, so the scroller keeps the
    // offset it had — 0, on a pane that has just been built — and the person
    // arrives at the oldest restored block with the prompt they were about to
    // type at somewhere below the fold.
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

  /** ADR-3kqrem7v: alternate mode hides transcript rows, not the shared
   * running header. Buffer identity is still authoritative and immediate;
   * it changes presentation, never desired geometry. */
  private _setFilledPane(on: boolean): void {
    if (on && this._filledPane) return
    this._filledPane = on
    this.scrollbackInner.classList.toggle('inner-fullscreen-mode', on)
  }

  /** Show the alternate grid beneath the same one-line running header
   * (ADR-3kqrem7v), preserving the actual header node and its controls. */
  enterFullscreen(): void {
    this._idleRequested = false
    this._mode = 'fullscreen'
    this.terminalLiveViewport.style.height = ''
    this.terminalLiveContainer.className =
      'terminal-live-container terminal-live-container live-fullscreen'
    this.terminalLiveInner.className = 'terminal-live-inner terminal-live-inner inner-fullscreen'
    this._setFilledPane(true)
    this._applyEchoShift()
    // The program owns the pane: the primary's history must not be
    // reachable by scrolling, so the surface loses the hand-back class and
    // the filled-pane exclusion hides it (nocx-zg3k3.10.4).
    this.scrollbackInner.classList.remove('inner-unstructured')
    // OUT OF THE BLOCK BEFORE THE PANE IS THE PROGRAM'S (ADR-0091): the
    // fullscreen rule hides transcript blocks, so a region inside one
    // would take the terminal down with it. The seat goes first and the class
    // second, in one task, so no frame sees both.
    this._seatLive()
    this._fillPane()
    this._onModeChanged?.(this._mode)
  }

  /** The live box IS the scroller, in the two modes where the terminal owns
   *  the pane: `unstructured` (a markerless session — there are no blocks to
   *  show) and `fullscreen` (an alt-screen program has taken it).
   *
   *  One owner, reached from every re-measure rather than once at mode entry.
   *  It used to be two copies of these two lines, run only as the mode was
   *  entered, and nothing re-ran them — so a pane that changed size afterwards
   *  kept the box it had when the mode began. The integration card is the
   *  everyday way that happens: it takes its own height off the top of the
   *  pane while it is up, a markerless session enters `unstructured`
   *  underneath it, and dismissing it gave the pixels back to the SCROLLER and
   *  to the grid fitted against it, but not to the box between them.
   *  `.scrollback-inner` was bottom-aligned then (`margin-top: auto`,
   *  superseded by decision 2026-09-15-terminal-screen-mockup-decision.md
   *  §1 item 1 — it is top-aligned now), so the difference showed up as a
   *  blank strip above the prompt, and the rows past the box were clipped
   *  by its overflow — `top` drawing 34 of the 40 rows it had been given.
   *  A window resize is the same defect with no card in it. */
  private _fillPane(): void {
    const max = this._areaHeightPx() - (this._fullscreenHeader ? this.runningHeaderHeightPx : 0)
    if (max > 0) this.terminalLiveContainer.style.height = `${max}px`
  }

  /** Exit alt-screen: restore normal layout. */
  exitFullscreen(): void {
    this._mode = 'idle'
    this.terminalLiveContainer.style.height = ''
    this.terminalLiveViewport.style.height = ''
    this._setFilledPane(false)
    this.terminalLiveContainer.className =
      'terminal-live-container terminal-live-container live-idle'
    this.terminalLiveInner.className = 'terminal-live-inner terminal-live-inner'
    this._applyEchoShift()
    this.scrollbackInner.classList.remove('inner-unstructured')
    this.scrollbackInner.classList.remove('inner-fullscreen-mode')
    this._updateSeparator()
    this._seatLive()
    this._onModeChanged?.(this._mode)
  }

  // ── Command cycle ─────────────────────────────────────────────────────

  /**
   * Called on OSC 133 C: create a running block, expand the live region.
   */
  onCommandStart(
    command: string,
    cwd: string,
    startLine: number,
    startAnchor?: BlockScreenAnchor,
  ): void {
    const cmd = command || '(empty)'
    this._blockManager.startBlock(cmd, cwd, startLine, 'shell', startAnchor)
    this.setRunning()
  }

  /**
   * Open a running block for an authenticated start (nocx-2v80t.3.2): the
   * block is the projection of what the backend sent, so both the shell's
   * own start event and the published app-owned attempt open it — the
   * submit itself opens nothing. `outputStart` is the block's OUTPUT range
   * start — the first row serialized at freeze. The shell-originated fact
   * lands on or past the echo line, so it omits the argument and the range
   * defaults to startLine (nocx-4yhi); the app-owned attempt arrives before
   * its bytes are written, so its caller passes startLine + 1 for the
   * shell's echo of the typed command.
   */
  beginBlock(
    command: string,
    cwd: string,
    startLine: number,
    /** Who submitted the command (design §3.1, nocx-iadtt): the app-owned
     *  submit passes the minted author; a shell-originated block — the
     *  shell's own start event, no app-owned submit — is the human's
     *  shell and defaults to 'shell'. */
    author: CommandAuthor = 'shell',
    startAnchor?: BlockScreenAnchor,
  ): void {
    const cmd = command || '(empty)'
    this._glide(() => {
      this._blockManager.startBlock(cmd, cwd, startLine, author, startAnchor)
      // The pet learned only about endings before this, so during the minute a
      // build takes — the minute somebody is actually watching the terminal —
      // it wandered about as though nothing were happening.
      windowPet()?.attendTo(author)
      this.setRunning()
    })
  }

  // ── the backend's clear boundary (nocx-2v80t.3.17) ───────────────────

  /**
   * The backend sighted a clear boundary: the program erased the display
   * and its saved lines (ED3 — the sequence `clear` emits, never plain ED2
   * alone, which a full-screen program redrawing sends). This is never a
   * guess from the command's text — the client no longer decides a clear by
   * what was typed, which is what this replaces (the removed maybeClear
   * matched the literal word `clear`, guessing at SUBMIT time, before the
   * command had even run). The backend parses the real VT erase in the
   * emulator it owns (ADR-0066) and this is purely a rendezvous with that
   * fact.
   *
   * keepEntryId is the block whose interval the erase happened inside —
   * almost always the `clear` command's own, still running — which must
   * stay open and keep receiving rows normally; every other block is
   * removed. Null means nothing was open at the sighting, and every block
   * is removed. The accepted screen frame reflects the erase sequence; this
   * only cleans up the frozen DOM blocks.
   */
  onClearBoundary(keepEntryId: string | null): void {
    this._blockManager.applyClearBoundary(keepEntryId)
    this._updateSeparator()
    // The ask chip's block went with the blocks: close the mode, or the
    // chip's owner would hold a scope that no longer exists (nocx-x8s2.2).
    this._onClear?.()
  }

  /** Preserve follow intent across a synchronous DOM mutation. The geometry
   *  answer comes from `isAtTail`, which refuses to answer from a scroller
   *  that has no layout box — the observer can briefly lose its target during
   *  a layout change. */
  private _followIntent(): boolean {
    return this._tail.intent(this.scrollbackArea)
  }

  /** At the live end, WITH an end to be at. A scroller whose content fits has
   *  nothing to scroll and is therefore trivially "at its end" — which is
   *  exactly the state a seated answer passes through before its box grows,
   *  so it is no proof that the seat has landed (nocx-yfpxl). */
  tailReached(): boolean {
    const { clientHeight, scrollHeight } = this.scrollbackArea
    return scrollHeight > clientHeight && isAtTail(this.scrollbackArea)
  }

  /** Move to the live end BECAUSE THE CALLER KNOWS the person is reading it,
   *  the way `restorePast` does. The follow sentinel is not consulted: it has
   *  been behind an overlay that owned the pane, and the content it watches
   *  has just grown by everything that was reparented into it — the two
   *  conditions under which its answer is stale rather than wrong (nocx-yfpxl).
   */
  scrollToTail(): void {
    this._tail.follow()
    this._scrollToBottom()
  }

  /** Scroll to the bottom, unless the user has scrolled away from the live
   *  end. */
  scrollToBottom(): void {
    if (!this._followIntent()) return
    this._tail.follow()
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
   * changes the stack's total content height, and while the person is
   * FOLLOWING the live end the scroller's `scrollTop` moves to match
   * (`_scrollToBottom`), so the whole stack's position in the viewport
   * shifts at once. Two of those in thirty milliseconds is what the owner
   * reported as the pane "not settling": the eye reads a sequence of instant
   * displacements as a twitch, whatever their direction. `.scrollback-inner`
   * is top-aligned now (decision 2026-09-15-terminal-screen-mockup-decision.
   * md §1 item 1 — it carried `margin-top: auto` before, and the whole stack
   * hung from the scroller's bottom edge), which removes exactly the case
   * this glide otherwise has nothing to compensate: a transcript that still
   * FITS the scroller never changes `scrollTop` at all, so `dy` below comes
   * out at (or under) zero and no settle plays — the FLIP mechanism itself
   * is unchanged and still owns the case that does move: a transcript long
   * enough that opening or freezing a block moves the live end's scrollTop.
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
   * it (ADR-0004) — and `.scrollback-area` grows by the composer's 77px in
   * response (it is the flex sibling that absorbs the freed space). A short
   * transcript that still fits notices nothing: `.scrollback-inner` is
   * top-aligned (decision §1 item 1) and its own position never depended on
   * where the composer's box ended. A transcript that OVERFLOWS does move —
   * the taller scroller needs less `scrollTop` to reach the same live end —
   * and that is the 77px this glide still exists to play back rather than
   * let land as a jump. Reserving the composer's box instead was the older
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
    if (this._tail.following) this._scrollToBottom()
  }

  private _glide(mutate: () => void, followIntent = this._followIntent()): void {
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
    if (!followIntent) return
    // The observer can report the old sentinel position after this mutation
    // has grown the scroller. The decision belongs to the pre-mutation state.
    this._tail.follow()
    this._scrollToBottom()
    if (!this._motionAllowed()) return
    const dy = before - this.scrollbackInner.getBoundingClientRect().top
    // Below a pixel there is nothing to watch. Above a viewport it is not a
    // settle at all — a clear, a restore, a jump the person asked for — and
    // gliding it would be a second animation over their own gesture.
    if (!Number.isFinite(dy) || Math.abs(dy) < 1) return
    if (Math.abs(dy) > this.scrollbackArea.clientHeight) return
    // THE SETTLE IS A STATE, NOT A STYLE (nocx-vydj). This class used to hide
    // the scroller's bar so it would not flash on every command (nocx-i4h04.4).
    // The scroller now always shows its bar — `overflow-y: scroll` in
    // style.css, because the box the grid is fitted to must not depend on
    // whether the transcript overflows — so there is no flash left to hide, and
    // toggling the overflow here would move that box by the bar's width for the
    // ~140ms of the glide, which a settling command would spend as a pending
    // width refit. The class stays as the state: the e2e specs wait for it to
    // clear, which is how they tell "the stack is gliding" from "it has
    // landed".
    //
    // AND IS ONLY WRITTEN WHEN THE TRANSCRIPT FITS (nocx-2v80t.3.35). A class
    // write on the scroller is not free — both engines treat it as a change to
    // the whole scrolling subtree and re-lay out every block in it; measured in
    // WebKit at 181 blocks, 218 ms per write, and a command settles about seven
    // times. A transcript that already overflows says nothing this class does
    // not, and a transcript that fits is short, which is exactly when the write
    // is cheap. Layout is clean here — the rect read above settled it — so this
    // read costs nothing.
    const hidesBar = this.scrollbackArea.scrollHeight <= this.scrollbackArea.clientHeight
    if (hidesBar) this.scrollbackArea.classList.add('is-settling')
    for (const el of [this.scrollbackInner]) {
      const anim = el.animate(
        [{ transform: `translateY(${dy}px)` }, { transform: 'translateY(0px)' }],
        { duration: SETTLE_MS, easing: SETTLE_EASING },
      )
      this._settleAnimations.set(el, anim)
      anim.finished.then(
        () => {
          if (this._settleAnimations.get(el) === anim) this._settleAnimations.delete(el)
          if (this._settleAnimations.size === 0) this._releaseSettlingBar()
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
    this._releaseSettlingBar()
  }

  /** End the settle's state — and write nothing when it was never entered:
   *  every glide begins by cancelling the last one, and a class write on the
   *  scroller is not free (see `_glide`). */
  private _releaseSettlingBar(): void {
    if (this.scrollbackArea.classList.contains('is-settling')) {
      this.scrollbackArea.classList.remove('is-settling')
    }
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
        // A command's life, not a layout class. The freeze no longer leaves
        // `running` behind — nothing clears the terminal, so the whole
        // surface stays up (nocx-2v80t.3.3) — and a watcher keyed on the
        // mode would spin forever after the last command. The running BLOCK
        // is the output; the settle animation is the playback.
        if (this._blockManager.runningBlock !== null || this._settleAnimations.size > 0) {
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

  /** Whether this build may move anything. The shared decision (motion.ts) —
   *  the composer's box transition reads the same one. */
  private _motionAllowed(): boolean {
    return motionAllowed(this.scrollbackInner)
  }

  /** The freeze landed: play back the card's displacement and tell the pet
   *  how the command went.
   *
   *  Every freeze path arrives here — the attempt-driven one, the abandoned
   *  one, the environment entry, the deferred close. What the settle used to
   *  do is mostly gone: nothing clears the terminal and the live region
   *  never collapses — the whole surface is the session's terminal now
   *  (nocx-2v80t.3.3), and the rows a command printed stay on it. What
   *  survives is the two things the swap above the terminal still owes:
   *  the glide that plays back the running card → frozen card height
   *  change, so the terminal's own rows hold still for the person reading
   *  them; and the pet, whose only view of a command's verdict is this
   *  moment.
   *
   *  'entered' and 'unknown' are neither success nor failure and must not be
   *  read as one: a pet that sulked at every ssh and every abandoned attempt
   *  would sulk most of the time. The author is the block's own, minted at
   *  submit and never derived: the animal answers your command differently
   *  from the assistant's, and guessing which lane a finished block came
   *  from is exactly the kind of derivation the ledger exists to make
   *  unnecessary. */
  private _finishFreeze(rec: BlockRecord, followIntent = this._followIntent()): void {
    windowPet()?.reactTo(
      rec.status === 'success' || rec.status === 'failure' ? rec.status : 'unknown',
      rec.author,
    )
    // And if something is STILL running, the animal goes back to watching it.
    // A freeze waits on the backend's block.closed and a start does not, so the verdict
    // on the last command routinely arrives after the next one has begun;
    // without this the pet stopped attending the moment it answered, and the
    // command actually in flight had nobody watching it.
    const stillRunning = this._blockManager.runningBlock
    if (stillRunning) windowPet()?.attendTo(stillRunning.author)
    // The echo shift belonged to the block that just froze; with the
    // running slot empty it computes to zero, and this is the moment it is
    // released. Inside the mutate: the transform is part of the frame the
    // glide plays back, not a second movement after it.
    // The mutate is otherwise empty on purpose: the card swap already
    // happened inside the manager, and the glide's painted-frame origin
    // (see _glide) is what lets it play that swap back without
    // re-measuring mid-flight.
    this._glide(() => {
      this._applyEchoShift()
    }, followIntent)
  }

  private _updateSeparator(): void {
    const hasBlocks = this._blockManager.blocks.length > 0
    this.separator.style.display = hasBlocks && this._mode !== 'fullscreen' ? '' : 'none'
  }

  /** The attempt-driven freeze (ADR-0024 §7 projection): the LOGICAL
   *  freeze (status, exit code) lands on the authenticated event; the
   *  VISUAL one waits for the backend's block.closed for the entry. When it
   *  has not arrived, this returns false — the manager's onDeferredFreeze
   *  settles it when it does, and on nothing else (nocx-2v80t.3.27). The
   *  authority check (kernel freezeBlock) is the caller's. */
  freezeFromAttempt(attempt: ExecutionAttempt, endLine: number): boolean {
    const followIntent = this._followIntent()
    this._restoreRunningHeader()
    const rec = this._blockManager.freezeFromAttempt(attempt, endLine)
    if (rec) {
      this._finishFreeze(rec, followIntent)
      return true
    }
    return false
  }

  /** The attempt-driven abandonment: the attempt went `unknown`, the block
   *  freezes as abandoned — never successful (ADR-0024 §5). */
  abandonAttempt(attempt: ExecutionAttempt, endLine: number, sessionGone = false): boolean {
    const followIntent = this._followIntent()
    this._restoreRunningHeader()
    const rec = this._blockManager.abandonAttempt(attempt, endLine, sessionGone)
    if (rec) {
      this._finishFreeze(rec, followIntent)
      return true
    }
    return false
  }

  /** Abandon a running block that never bound to an attempt: its domain
   *  ended before any start frame arrived, so nothing will ever complete it
   *  (nocx-mlyu). Unknown, never successful. */
  abandonUnbound(endLine: number): boolean {
    const followIntent = this._followIntent()
    this._restoreRunningHeader()
    const rec = this._blockManager.abandonUnbound(endLine)
    if (rec) {
      this._finishFreeze(rec, followIntent)
      return true
    }
    return false
  }

  /** Freeze the running block as ENTERED (nocx-95kt): the command opened a
   *  nested environment, so it ends here, painted as neither success nor
   *  failure, and the running slot is freed for the blocks that follow. */
  enterBlock(endLine: number): boolean {
    const followIntent = this._followIntent()
    this._restoreRunningHeader()
    const rec = this._blockManager.freezeEntered(endLine)
    if (rec) {
      this._finishFreeze(rec, followIntent)
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
    this._boxResizeObserver?.disconnect()
    this._boxResizeObserver = null
    this._blockManager.dispose()
    this.scrollbackLayout.remove()
  }
}

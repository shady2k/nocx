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
import { gridRows, publishCellMetric, publishRowPitch } from './cell-metric'
import type { ExecutionAttempt } from '../lifecycle/state'
import type { AgentDump } from '../generated/agent.dump'
import { windowPet } from '../pets/window-pet'
import { isAtTail, TailFollow } from './tail-follow'
export type LiveRegionMode = 'idle' | 'running' | 'fullscreen' | 'unstructured'

/** WHERE THE PERSON'S EYES ARE (nocx-3wmve): the first element the transcript
 *  renders whose box starts inside the viewport, named so it can be found again
 *  after a change.
 *
 *  THE IDENTITY IS TWO SCHEMES, because the transcript renders two kinds of
 *  thing and only one of them carries an identity of its own. A painted output
 *  row has the record's absolute line (`data-logical-line`, with the block's
 *  `data-entry-id`): it is the backend's own numbering and does not move while
 *  a block grows. Everything else — answer prose, a table, a code block, a
 *  header — has no such mark, and giving every painted node one would be a new
 *  attribute on every element for one reader's benefit. What IS true of those
 *  is that a block's flow only ever grows at its end (a stream appends; a
 *  freeze repaints the same content in order), so an element's ordinal among
 *  the block's rendered units is stable, and its class signature makes the
 *  lookup safe when a neighbour is inserted or removed nearby. */
interface ReaderPlace {
  /** The block's durable identity (`data-entry-id`), null when it has none. */
  blockId: string | null
  kind: 'row' | 'unit'
  /** The record's absolute line, for a row. */
  line: string | null
  /** The unit's ordinal among the block's rendered units, for anything else. */
  index: number
  /** The unit's class signature, so an ordinal that moved one neighbour can be
   *  corrected. */
  signature: string
  /** Where the element sat, measured from the scroller's top edge. */
  offset: number
  /** The scroller's own position, for the case no element can be named. */
  top: number
}

/**
 * HOW MUCH OF THE VIEW A PINNED HEADER MAY COVER (nocx-4y82c.1.3, P2).
 *
 * A multiline command wraps its own header, and a header taller than the
 * scrollport would cover every row when pinned. The owner's rule is that the
 * pinned header is a COMPACT part of the view -- Warp's is one line -- so a
 * header that is more than a third of the scrollport does not pin at all; it
 * scrolls away the way it did before the feature. One third is the bound
 * because it is a share of the thing being covered, not a font-size constant:
 * a short pane clamps sooner, a tall pane is allowed a taller header, and the
 * rows always keep the other two thirds.
 */
const PINNED_HEADER_MAX_SHARE = 1 / 3

/** `true` when a header of `headerHeight` may be pinned in `viewportHeight`. */
export function headerFitsPinned(headerHeight: number, viewportHeight: number): boolean {
  if (!(viewportHeight > 0)) return false
  return headerHeight <= viewportHeight * PINNED_HEADER_MAX_SHARE
}

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
  /** The grid on screen is the one a full-screen program was given, kept after
   *  it left the alternate screen and not yet handed back (ADR-2yzsq8mk
   *  decision 4; nocx-djx1u). Only the unstructured layout reacts to it. */
  private _borrowedGrid = false
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
  /** A settle is running: a height write inside it is that settle's, not a
   *  second one. */
  private _settling = 0
  private _paintWatch = 0
  private _painting = false
  private _followObserver: IntersectionObserver | null = null
  /** ONE MECHANISM for "may this header pin" (nocx-4y82c.1.3): the observer
   *  watches every ledger-level header's own box and the scroller's, and a
   *  header's verdict is its own height against a third of the view. There is
   *  no scroll, freeze or window trigger to add for the next path that re-wraps
   *  a header -- a font, a theme, a rename, a width change -- because the
   *  header's own size is what all of them change. */
  private _sizeObserver: ResizeObserver | null = null
  private readonly _observedHeaders = new Set<HTMLElement>()
  /** Keeps the watched set in step with blocks being created, replaced and
   *  removed. It judges nothing; it only registers the headers the one
   *  observer watches. */
  private _headerSetObserver: MutationObserver | null = null
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
    // live region is inside the stack the settle measures.
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
      // The freeze replaces a block's whole element, and the frozen card's
      // header and body rhythm are not the running one's: the rows under it
      // move by 30px as it arrives. A reader scrolled into that output is
      // reading those rows (nocx-3wmve).
      holdReaderPlace: (mutate) => this.holdReaderPlace(mutate),
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
        // A SCROLLER THAT CHANGED SIZE RESIZES THE LIVE BOX IN THE SAME TASK
        // (owner, 2026-11-08: "the composer is back, the live grid is cut
        // short mid-row, and a blank band lies between them"). The composer
        // returning at the end of a command takes 130px back from the
        // scroller; a live box still sized for the taller one overflows it,
        // so its last rows are clipped mid-row for as long as the next
        // resize pass takes, and the tail sits where the taller box put it.
        // `runningLiveCap` is read from this box, so re-sizing here is the
        // only place the two agree in the frame the box moved.
        this.setLiveHeight(this._liveContentHeight())
        if (this._tail.following) this._scrollToBottom()
      })
      this._boxResizeObserver.observe(this.scrollbackArea)
    }

    // A HEADER THAT IS TOO TALL TO BE COMPACT DOES NOT PIN (nocx-4y82c.1.3).
    // ONE MECHANISM: a ResizeObserver watches every ledger-level header and the
    // scroller, so any path that changes a header's own box is covered -- a
    // font or theme change re-wraps it, a freeze replaces it, a pane resize
    // shrinks the view, an expand/collapse changes it. The judge reads the
    // header's height against a third of the scroller and writes one attribute;
    // there is no scroll, freeze or window trigger left to miss the next path.
    if (typeof ResizeObserver === 'function') {
      this._sizeObserver = new ResizeObserver(() => this._judgeHeaderSizes())
      this._sizeObserver.observe(this.scrollbackArea)
    }
    if (typeof MutationObserver === 'function') {
      this._headerSetObserver = new MutationObserver(() => this._syncObservedHeaders())
      this._headerSetObserver.observe(this.scrollbackInner, { childList: true })
    }
    this._syncObservedHeaders()

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
    // otherwise outrank the idle/fullscreen mode classes. THE BOX IS PART OF
    // THE TRANSCRIPT'S HEIGHT (nocx-3wmve): a reader scrolled up keeps their
    // place across the collapse, and a follower is put back at the end.
    this._settleHeight(() => {
      this.terminalLiveContainer.style.height = ''
      this.terminalLiveViewport.style.height = ''
    })
    this._setFilledPane(false)
    this.terminalLiveContainer.className =
      'terminal-live-container terminal-live-container live-idle'
    this.terminalLiveInner.className = 'terminal-live-inner terminal-live-inner'
    this.scrollbackInner.classList.remove('inner-unstructured')
    // The echo shift is a running-mode property: clear it with the region.
    this._applyEchoShift()
    this._updateSeparator()
    this._seatLive()
    this._announceMode()
  }

  /** Show live output below the running block. */
  setRunning(): void {
    this._idleRequested = false
    if (this._mode === 'fullscreen') return
    const entering = this._mode !== 'running'
    this._mode = 'running'
    if (entering) {
      this._settleHeight(() => {
        this.terminalLiveContainer.style.height = ''
        this.terminalLiveViewport.style.height = ''
      })
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
    this._announceMode()
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
    this._announceMode()
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
    if (max <= 0) return null
    // A CAP THAT ENDS MID-ROW IS A ROW CUT IN HALF (owner, 2026-11-08: "the
    // live grid is cut short mid-row"). The rows are laid at whole multiples
    // of the cell height, so a ceiling between two of them leaves the clip
    // slicing the last row — measured in the e2e container at the hand-off,
    // 514px of pane for 30.24 rows, four pixels of a row left showing. Floored
    // here, the ceiling is a row boundary and both the grid the fit sends and
    // the box that clips it end on the same one.
    const cell = this._cellMetrics()?.cellHeight ?? 0
    if (cell > 0 && max > cell) return gridRows(max, cell) * cell
    return max
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
    // ONE ARITHMETIC FOR THE BOX AND THE GRID (owner, 2026-11-08: "a row never
    // moves down by a line when the live box grows by one"). The grid is
    // translated by exactly what the box does NOT show — `px - content` —
    // never by the anchor's own offset. The two agree while the box is
    // growing with its rows; they part company the moment the CAP clamps,
    // which is where the echo shift's release used to land: a frame measured
    // in an ssh session carried the shift from 68px to 0 (four rows) while
    // the box grew by only three, and every visible row moved DOWN a line.
    // Derived this way a row's place is `bottom + row·cell - px`: a function
    // of the painted content alone, so releasing the shift cannot move it.
    this._applyShift(px - content)
    const box = `${content + pad}px`
    const inner = `${content}px`
    // Keep the block's bottom at the bottom of the view — but only on the
    // frames where the box actually CHANGED SIZE. This runs on every chunk of
    // output, and a program that repaints in place changes nothing about the
    // layout: `top` redrawing the same 33 rows every three seconds was moving
    // the scroll each time for no reason, which is unpleasant if you are
    // reading. The comparison below is the whole guard.
    if (this.terminalLiveContainer.style.height !== box) {
      this._settleHeight(() => {
        this.terminalLiveContainer.style.height = box
        this.terminalLiveViewport.style.height = inner
      })
    } else if (this.terminalLiveViewport.style.height !== inner) {
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
  /** The grid's translation for the region's current structural state — the
   *  anchor's own offset, for the frames before a height has been measured. */
  private _applyEchoShift(): void {
    this._applyShift(this._echoShiftPx())
  }

  /** Translate the grid so that the rows below `shift` px of it are the ones
   *  the box shows. The write is guarded: identical values are skipped so the
   *  per-frame sizing pass does not rewrite the style on every chunk. */
  private _applyShift(shift: number): void {
    const next = shift > 0.01 ? `translateY(-${Math.round(shift * 100) / 100}px)` : ''
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

  /** The layout's vertical margin is the ledger's — a markerless session keeps
   *  it too (ADR-2yzsq8mk) — and only the alternate screen starts at the pane's
   *  own top edge, one row of margin at its bottom instead. Every mode change
   *  announces itself through here, so the class cannot lag the mode. */
  private _announceMode(): void {
    this.scrollbackLayout.classList.toggle('inner-pane-program', this._mode === 'fullscreen')
    // A BORROWED GRID IS THE PANE'S OWN GEOMETRY (nocx-djx1u). A markerless
    // session has no prompt marker to hand a borrowed grid back, so the rows a
    // full-screen program left behind are the pane's for as long as it holds
    // them — and the four-side margin is two rows it cannot show. In the
    // unstructured mode, and only there, the layout gives the margin up and the
    // program's own box (no top edge, one row at the bottom) is the pane. The
    // structured modes keep it: there the block owns the rows and scrolls.
    this.scrollbackLayout.classList.toggle(
      'inner-borrowed-grid',
      this._borrowedGrid && this._mode === 'unstructured',
    )
    this._onModeChanged?.(this._mode)
  }

  /**
   * Say whether the grid on screen is one a program borrowed — the rows a
   * full-screen program was given, kept after it left the alternate screen
   * (ADR-2yzsq8mk decision 4) — so the unstructured layout can size the pane
   * for them instead of for the ledger's margin (nocx-djx1u).
   */
  setBorrowedGrid(borrowed: boolean): void {
    if (this._borrowedGrid === borrowed) return
    this._borrowedGrid = borrowed
    this._announceMode()
  }

  /** Whether the layout is holding the margin back for a borrowed grid. */
  isBorrowedGrid(): boolean {
    return this._borrowedGrid && this._mode === 'unstructured'
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
    this._announceMode()
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
    const grid = this.paneFillHeight
    if (grid <= 0) return
    // The box IS the grid: a markerless session's four-side margin is the
    // stack's padding, not this box's (ADR-2yzsq8mk), and `paneFillHeight`
    // already took it out of the rows this box may show.
    this.terminalLiveContainer.style.height = `${grid}px`
  }

  /**
   * THE HEIGHT OF THE BOX THAT FILLS THE PANE, AND OF THE GRID THAT FILLS IT.
   *
   * One owner, read by two callers: `_fillPane` writes it to the live box, and
   * `terminal-content`'s `_gridHeight` returns it as the desired grid height
   * while the program owns the pane. Two expressions — one box that ends on a
   * row boundary, one grid the PTY is told — is how a markerless session's
   * full-screen program (vim or top over a plain ssh shell, where there is no
   * running block to give `_gridHeight` the structured branch) came to be told
   * the whole scroller while its box showed one row less, and lost its last
   * row to the clip.
   *
   * Floored to whole rows for the reason `runningLiveCap` is: a box that ends
   * inside a row shows a sliced one. The alternate screen then gives one of
   * those rows back — the margin ADR-2yzsq8mk keeps below a program's last row,
   * which the stylesheet declares as `margin-bottom` — so the box and its
   * margin together are the pane and no row is clipped for the margin.
   */
  get paneFillHeight(): number {
    const max = this.paneBoxHeight
    if (max <= 0) return 0
    const cell = this._cellMetrics()?.cellHeight ?? 0
    const height = cell > 0 && max > cell ? gridRows(max, cell) * cell : max
    return this._mode === 'fullscreen' && cell > 0 ? Math.max(0, height - cell) : height
  }

  /** THE SAME BOX, BEFORE ITS ROWS ARE COUNTED (nocx-djx1u). `paneFillHeight`
   *  answers in whole rows, which is what a box and a pty are told; the one
   *  fit that must SUBTRACT a margin from it has to subtract before that
   *  rounding, or it rounds twice — floor((422 − 48)/17) = 22 rows against
   *  floor((408 − 48)/17) = 21 — and asks the shell for a grid the pane does
   *  not have. So the unrounded expression is the one owner, and the floored
   *  one is derived from it. */
  get paneBoxHeight(): number {
    const max = this._areaHeightPx() - (this._fullscreenHeader ? this.runningHeaderHeightPx : 0)
    return Math.max(0, max)
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
    this._announceMode()
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
    this.settleAtTail(() => {
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
   * Run `mutate` and hold the tail — the one door for a structural change.
   *
   * NO ANIMATION (owner, 2026-11-08, replacing an earlier wish for a smooth
   * scroll): "like Warp, no animation. The output is anchored to the bottom,
   * and every frame shows the newest rows where they will stay; nothing
   * reflows". This used to be a FLIP settle: the mutation landed whole, the
   * stack was translated by the displacement it caused, and the translation
   * was released over 140 ms. The measurement that killed it, in the e2e
   * container, is e2e/output-jitter.spec.ts: the frame after every
   * `block.grew` carried the stack translated by the very rows it had just
   * committed (68, 96, 116 px, one viewport at the worst), re-armed every
   * 50 ms at apt's pace, so the pane never settled — and at the freeze it
   * moved the last rows DOWN by 34 px, which is a frame that goes back.
   *
   * What a person gets instead is the terminal's own behaviour: the DOM change
   * lands in one task, the recalc and the re-pin land in the same task, and
   * the frame that paints shows the rows where they will stay.
   */
  settleAround(mutate: () => void): void {
    this.settleAtTail(mutate)
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

  /** WHERE THE PERSON'S EYES ARE, so a mutation can give it back (nocx-3wmve).
   *
   * A row is named by the block it belongs to and its own line in that block
   * (`data-block-id` + `data-logical-line`, the identity the painted rows
   * carry), never by its element: a freeze re-paints the block's body, and the
   * row a reader is on is the same LINE in the same block, not the same DOM
   * node. `offset` is where that row sat, measured from the scroller's top
   * edge — the one thing "not moved" means. */
  private _captureReaderPlace(): ReaderPlace | null {
    const area = this.scrollbackArea
    if (area.scrollHeight <= area.clientHeight) return null
    // The GEOMETRY answers "is this person reading rows", not the remembered
    // intent: a wheel that has not reached the observer yet already moved
    // them, and the intent's own staleness is what the observer exists to
    // absorb. A reader at the live end is the tail rule's, and it is not this
    // one's business (the caller's settle re-pins them).
    if (isAtTail(area)) return null
    const clip = area.getBoundingClientRect()
    // ONLY THE BLOCK THE VIEWPORT'S TOP IS IN, not the whole transcript: a
    // restored pane can hold thousands of rows, and a settle must not touch
    // every one of them.
    for (const block of this.scrollbackInner.querySelectorAll<HTMLElement>(':scope > .cmd-block')) {
      const box = block.getBoundingClientRect()
      if (box.bottom <= clip.top) continue
      if (box.top >= clip.bottom) break
      const found = this._unitAtViewportTop(block, clip)
      if (found === null) continue
      const { unit, index } = found
      const element = unit.getBoundingClientRect()
      const blockId = block.dataset.entryId ?? null
      if (unit.classList.contains('term-grid-row') && unit.dataset.logicalLine !== undefined) {
        return {
          blockId,
          kind: 'row',
          line: unit.dataset.logicalLine,
          index,
          signature: '',
          offset: element.top - clip.top,
          top: area.scrollTop,
        }
      }
      return {
        blockId,
        kind: 'unit',
        line: null,
        index,
        signature: unit.className,
        offset: element.top - clip.top,
        top: area.scrollTop,
      }
    }
    // Nothing painted in the viewport can be named (the live grid above the
    // fold, an empty body). The scroller's own position still survives a
    // shrink that is paid back in the same task.
    return {
      blockId: null,
      kind: 'unit',
      line: null,
      index: -1,
      signature: '',
      offset: 0,
      top: area.scrollTop,
    }
  }

  /** THE FIRST RENDERED UNIT OF `block` THAT STARTS INSIDE THE VIEWPORT
   *  (nocx-3wmve).
   *
   * A UNIT IS ANYTHING THE TRANSCRIPT RENDERS INSIDE A BODY — a painted row, a
   * paragraph of answer prose, a table, a code block — because the person reads
   * whatever is there, and an anchor that only knows rows falls back to the
   * numeric position over a body made of prose, which is exactly where an
   * earlier block's freeze then moves them.
   *
   * Units are in document order and laid out one under the next, so their top
   * edges are non-decreasing and the first visible one is a bisection: a
   * handful of measurements instead of one per row. Measured on a 50 000-row
   * command, reading near its fortieth thousand: the linear walk took 39 984
   * row measurements on EVERY settle — and a settle is every stored-rows
   * delivery, every freeze, every header change — while the bisection takes 17.
   * `document.elementFromPoint` would answer in one call and is not acceptable:
   * a modal, a menu or the composer over the scroller would name their element
   * instead of the unit.
   *
   * EVERY BODY UNDER THE BLOCK, not only a top-level block's own: a command
   * inside a turn lives under `.cmd-children` (ADR-0040), and a reader scrolled
   * into that output was left with the numeric fallback, so the freeze of a
   * nested command moved their rows. */
  private _unitAtViewportTop(
    block: HTMLElement,
    clip: { top: number; bottom: number },
  ): { unit: HTMLElement; index: number } | null {
    const units = block.querySelectorAll<HTMLElement>('.cmd-output > *')
    if (units.length === 0) return null
    const topOf = (index: number): number => units[index].getBoundingClientRect().top
    let low = 0
    let high = units.length - 1
    let found = -1
    while (low <= high) {
      const mid = (low + high) >> 1
      if (topOf(mid) < clip.top - 0.5) low = mid + 1
      else {
        found = mid
        high = mid - 1
      }
    }
    if (found < 0) return null
    // A unit can be inside the block and still above the fold's top edge only
    // by rounding; walking forward from the bisection's answer is bounded by
    // the units that start within one row of the edge, and it keeps the
    // contract the linear walk had (a unit that STARTS in the viewport).
    for (let index = found; index < units.length; index += 1) {
      const unit = units[index]
      const cell = unit.getBoundingClientRect()
      if (cell.height <= 0) continue
      if (cell.top < clip.top - 0.5) continue
      if (cell.top >= clip.bottom - 0.5) return null
      return { unit, index }
    }
    return null
  }

  private _restoreReaderPlace(place: ReaderPlace): void {
    const area = this.scrollbackArea
    const max = Math.max(0, area.scrollHeight - area.clientHeight)
    // FIRST THE POSITION THEY CHOSE, as far as the content allows: across a
    // program's excursion the elements above the reader are the same ones, and
    // the scroller was only clamped because the transcript left the flow.
    const top = Math.min(place.top, max)
    if (area.scrollTop !== top) area.scrollTop = top
    // THEN THE ELEMENT ITSELF, for the case the position alone cannot answer: a
    // card that changed shape above the reader keeps what they were reading at
    // the same distance from the top edge.
    const unit = this._findReaderUnit(place)
    if (unit === null) return
    const shift = unit.getBoundingClientRect().top - area.getBoundingClientRect().top - place.offset
    if (Math.abs(shift) >= 0.5) area.scrollTop = Math.max(0, area.scrollTop + shift)
  }

  /** The element a place names, found again in the transcript as it is now. */
  private _findReaderUnit(place: ReaderPlace): HTMLElement | null {
    const area = this.scrollbackArea
    if (place.kind === 'row') {
      if (place.line === null) return null
      const selector =
        place.blockId === null
          ? `.cmd-output .term-grid-row[data-logical-line="${place.line}"]`
          : `.cmd-block[data-entry-id="${place.blockId}"] .cmd-output` +
            ` .term-grid-row[data-logical-line="${place.line}"]`
      return area.querySelector<HTMLElement>(selector)
    }
    if (place.blockId === null || place.index < 0) return null
    const block = area.querySelector<HTMLElement>(`.cmd-block[data-entry-id="${place.blockId}"]`)
    if (block === null) return null
    const units = block.querySelectorAll<HTMLElement>('.cmd-output > *')
    const matches = (index: number): boolean =>
      units[index] !== undefined && units[index].className === place.signature
    if (matches(place.index)) return units[place.index]
    // THE ORDINAL MOVED BY A NEIGHBOUR (a row removed above, the working dots
    // taken away): the nearest unit with the same signature is the same thing,
    // and the search is bounded because a body that shifted by more than this
    // is a different body.
    for (let step = 1; step <= 64; step += 1) {
      if (matches(place.index - step)) return units[place.index - step]
      if (matches(place.index + step)) return units[place.index + step]
    }
    return null
  }

  /** THE ONE DOOR A CONTENT CHANGE GOES THROUGH (nocx-3wmve).
   *
   * Every mutation that can change the transcript's height ends in exactly one
   * of two settles, and this is the only place that chooses: a reader who is
   * following is put back at the bottom edge, and a reader who has scrolled up
   * keeps the element they were reading at the same distance from the top.
   * There is no third outcome — and no caller that can skip both, which was the
   * defect: the freeze's swap went through an anchor-only door, so a deferred
   * freeze landing while the person followed another command grew the
   * transcript, found no anchor to take at the live end, and left the follower
   * off it with nothing to re-pin them.
   *
   * The transcript changes shape for reasons that are nobody's gesture: a
   * command ends and its card takes the frozen rhythm, a block is rebuilt, a
   * body is re-painted. The scroller owns its position (`overflow-anchor: none`
   * is deliberate — one controller, nocx-6w4z), so the settle is here rather
   * than left to the engine. */
  holdReaderPlace(mutate: () => void): void {
    this.settleAtTail(mutate)
  }

  /**
   * The one implementation of "apply it, then hold the tail".
   *
   * Read top to bottom it is the whole rule: decide the follow intent from
   * the PRE-mutation geometry, apply the DOM change whole, re-pin the bottom
   * edge, and leave the frame as it is. Nothing is translated, nothing is
   * scheduled, and no second pass can grow a box this one has already
   * measured.
   */
  /** A HEIGHT THE TRANSCRIPT WEARS IS A CONTENT CHANGE (nocx-3wmve): the live
   *  box is hundreds of pixels of the transcript's own height, so writing it
   *  moves a reader exactly as a block growing does. It goes through the same
   *  two outcomes as everything else — the bottom edge for a follower, the
   *  anchor for a reader who is away — but it cannot call `settleAtTail`,
   *  because `settleAtTail` writes the height itself; inside a settle the outer
   *  one owns the outcome (`_settling`). */
  private _settleHeight(write: () => void): void {
    if (this._settling > 0) {
      write()
      return
    }
    const following = this._followIntent()
    const place = following ? null : this._captureReaderPlace()
    write()
    if (following) {
      this._tail.follow()
      this._scrollToBottom()
    } else if (place !== null) {
      this._restoreReaderPlace(place)
    }
  }

  private settleAtTail(mutate: () => void, followIntent = this._followIntent()): void {
    this._settling += 1
    try {
      this._settleTail(mutate, followIntent)
    } finally {
      this._settling -= 1
    }
  }

  private _settleTail(mutate: () => void, followIntent: boolean): void {
    // A FOLLOWER IS MOVED BY THE END; EVERYBODY ELSE IS MOVED BY THEIR ROWS
    // (nocx-3wmve). This is the one door every structural change comes through
    // — a stored-rows delivery growing an earlier block, a nested command
    // freezing, a header changing — and only the follower branch had an anchor,
    // so any height that changed above a reader who had scrolled up moved them.
    const place = followIntent ? null : this._captureReaderPlace()
    mutate()
    // The live box follows the mutation IN THIS TASK. The composer returning
    // takes 130px back from the scroller and the cap is read from that box; a
    // box still sized for the taller one is a grid cut mid-row for as long as
    // the next resize pass takes, which is the frame the owner photographed.
    //
    // SIZED FROM WHAT WAS PAINTED, never from a fresher model (owner,
    // 2026-11-08). A stored-rows delivery lands between accepting a frame and
    // painting it, and this call used to read the model at that instant: the
    // box took the newer frame's text extent while the grid below it still
    // showed the older one, so a block that had just grown by one committed
    // row could lose a row of box and move every visible row down 17px.
    // `_lastLiveHeightPx` is the height the last paint computed, which is the
    // only one the rows on screen agree with.
    this.setLiveHeight(this._lastLiveHeightPx ?? this._liveContentHeight())
    if (!followIntent) {
      if (place !== null) this._restoreReaderPlace(place)
      return
    }
    // The observer can report the old sentinel position after this mutation
    // has grown the scroller. The decision is the pre-mutation one.
    this._tail.follow()
    this._scrollToBottom()
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
    // (see settleAtTail) is what lets it land without
    // re-measuring mid-flight.
    this.settleAtTail(() => {
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

  /** Register the headers the one size observer watches: every block seated
   *  directly in the transcript contributes its own header. Called whenever
   *  the stack's children change, so a block created, replaced (a freeze swaps
   *  the element) or removed is watched from that moment. It does not decide
   *  anything; `_judgeHeaderSizes` does. */
  private _syncObservedHeaders(): void {
    const observer = this._sizeObserver
    if (observer === null) return
    const current = new Set(
      this.scrollbackInner.querySelectorAll<HTMLElement>(':scope > .cmd-block > .cmd-header'),
    )
    for (const header of this._observedHeaders) {
      if (current.has(header)) continue
      observer.unobserve(header)
      this._observedHeaders.delete(header)
    }
    for (const header of current) {
      if (this._observedHeaders.has(header)) continue
      observer.observe(header)
      this._observedHeaders.add(header)
    }
    this._judgeHeaderSizes()
  }

  /** Each watched header's verdict: its own height against a third of the
   *  scroller. Independent of which block owns the top -- the sticky rule
   *  already gives one pinned header -- so a scroll cannot make it stale, and
   *  a header that changes size is re-judged by its own observer. The answer
   *  is one attribute; no box changes. */
  private _judgeHeaderSizes(): void {
    const heightPx = this.scrollbackArea.clientHeight
    for (const header of this._observedHeaders) {
      if (!header.isConnected) continue
      if (headerFitsPinned(header.offsetHeight, heightPx)) header.removeAttribute('data-pinnable')
      else header.setAttribute('data-pinnable', 'false')
    }
  }

  dispose(): void {
    this._sizeObserver?.disconnect()
    this._sizeObserver = null
    this._headerSetObserver?.disconnect()
    this._headerSetObserver = null
    this._observedHeaders.clear()
    this._followObserver?.disconnect()
    this._followObserver = null
    this._boxResizeObserver?.disconnect()
    this._boxResizeObserver = null
    this._blockManager.dispose()
    this.scrollbackLayout.remove()
  }
}

// THE LIVE TIER'S SCROLLBACK SURFACE (nocx-zg3k3.10.4, ADR-0078 decision 1).
//
// The rows above the live rectangle in a session with no shell integration:
// pages of the emulator's own scrollback, asked for as the reader scrolls,
// painted by the same painter as the live screen. Nothing here parses a byte
// and nothing here measures a character's width: the rows arrive in the frame
// contract's own text+marks+runs vocabulary (session.historyPageRows), the
// pane's cell model decodes them — the same decode a frame gets — and
// `paintRow`, the painter's own row primitive, draws them against the
// measuring authority the pane already shares (the same CellFit the live
// grid warms and the stored rows are painted with). The palette is the live
// painter's own default, so the rows above the fold and the rows below it
// resolve as one picture.
//
// The reader's contract (the stage's DONE WHEN):
//   - scrolling up pages the emulator's history until the floor, and every
//     row that left the screen is found;
//   - output arriving does not move the reader's anchor (the page is
//     inserted above and the scroller — which owns the position, the
//     stylesheet's own `overflow-anchor: none` — is compensated in the same
//     task);
//   - returning to the live end drops a stale past, so the next scroll-up
//     reads the head again and the tail follows as it did;
//   - a sighted erase-saved-lines drops everything painted: the live tier
//     shows the emulator's view as it is, and no boundary row is invented;
//   - while a full-screen program owns the pane the surface is out of the
//     flow (the stylesheet's filled-pane exclusion), so Page Up and the
//     scrollbar reveal nothing of the primary's history.

import type { SessionHistoryPage } from '../generated/session.historyPage'
import type { SessionHistoryPageRows } from '../generated/session.historyPageRows'
import { createCellModel, type ScreenSnapshot } from '../cell-model'
import { fitCandidatesOf, paintRow } from '../painter/paint-row'
import type { FitCandidate } from './cell-fit'
import { isAtTail } from './tail-follow'
import type { RunMetric } from './run-geometry'
import { DEFAULT_SNAPSHOT, type TerminalSnapshot } from './serializer'
import { log } from '../log'

/** One page bound. Every layer that spells the request holds the wire's own
 *  maximum (internal/sessionruntime/history_page.go MaxHistoryPageRows). */
const PAGE_ROWS = 64

/** The trailing edge the empty surface's head refresh is coalesced behind:
 *  one request per burst of output, never one per chunk, and small enough
 *  that the wheel has somewhere to go before the reader gives up. */
export const HEAD_REFRESH_MS = 120

/** How long a result waits for its rows document before the page is given
 *  back to the next gesture. Longer than any real socket gap on one
 *  connection — the planes ride one FIFO — and bounded, so a genuinely
 *  lost document cannot wedge the cursor forever. */
export const AWAIT_ROWS_MS = 5_000

/** The session's page seam, as ipc's SessionHandle already spells it. */
export interface LiveHistoryPageSource {
  historyPage(before: number | null, limit: number): Promise<SessionHistoryPage>
  onHistoryPageRows(cb: (page: SessionHistoryPageRows) => void): void
}

export interface LiveHistoryOptions {
  /** The scrolling element (scrollbackArea): the reader anchor's owner. */
  readonly scroller: HTMLElement
  /** The stack the surface is the top of (scrollbackInner). */
  readonly stack: HTMLElement
  /** The pane's committed screen columns; null before the first frame. A
   *  stored row needs exactly one external fact — geometry.cols — to pad
   *  the implied trailing blanks the compact wire does not carry. */
  readonly columns: () => number | null
  /** The measuring authority, re-read per paint — the same supplier the
   *  live painter uses, so one shaping engines every row on the pane. */
  readonly metric: () => RunMetric | null
  /** Cell-fit's batch write, run for the whole page before any row paints
   *  (cell-fit.ts's contract, the one the stored rows already keep). */
  readonly warm?: (candidates: Iterable<FitCandidate>) => void
  /** The theme the wire's palette colours resolve against. Defaults to the
   *  live painter's own default; the composition site passes nothing, so
   *  the rows above the fold resolve exactly as the rows below them. */
  readonly palette?: TerminalSnapshot
}

interface InstalledPage {
  readonly el: HTMLElement
  /** The page's absolute history row interval [start, end), as the
   *  session.historyPage result named it. */
  start: number
  readonly end: number
  /** The page's rows as the cell model decoded them, oldest first —
   *  `rows[i]` is absolute row `start + i`. Kept so a floor that lands
   *  inside the page can trim what the emulator no longer holds without a
   *  second decode. */
  readonly rows: readonly ScreenSnapshot['rows'][number][]
}

export class LiveHistorySurface {
  readonly el: HTMLElement
  private readonly _scroller: HTMLElement
  private readonly _columns: () => number | null
  private readonly _metric: () => RunMetric | null
  private readonly _warm: ((candidates: Iterable<FitCandidate>) => void) | undefined
  private readonly _palette: TerminalSnapshot
  private readonly _offScroll: () => void

  private _source: LiveHistoryPageSource | null = null
  /** The painted pages, DOM order: oldest first, youngest last. Their union
   *  is one contiguous absolute interval — every fetch chains from the
   *  oldest painted row — so pruning from the front is whole pages. */
  private _pages: InstalledPage[] = []
  /** The next fetch's `before`: the oldest painted row, or null for the
   *  head. An absolute row number, stable while output arrives (ADR-0078). */
  private _cursor: number | null = null
  /** The last answer said nothing remains below the cursor. Re-armed by
   *  output (rows may have departed since) and by every drop. */
  private _exhausted = false
  private _inflight = false
  /** A result landed; its rows document has not. The install waits for
   *  BOTH planes, and nothing else may page until it resolves — a second
   *  request in between would chain its cursor from a page that has not
   *  landed. */
  private _awaitingRows: string | null = null
  private _awaitTimer = 0
  /** The coalesced head refresh (see noteOutput): one timer per burst of
   *  output, not one request per chunk. The INTENT outlives a blocked
   *  fire: the timer may go off while the head read it wants to precede is
   *  still on the wire, and the refresh then re-arms behind that response
   *  instead of being lost with it. */
  private _refreshTimer = 0
  private _refreshPending = false
  /** Counts noteOutput calls, so an answer's own arithmetic — more=false,
   *  an exhausted chain — is judged against the read it was taken at: an
   *  answer that read the buffer BEFORE the output departed must not latch
   *  exhaustion over rows that departed after it. */
  private _outputSeq = 0
  /** Bumped by every drop and rebind; results of an older epoch are
   *  dropped rather than installed into a surface that forgot them. */
  private _epoch = 0
  /** Output arrived since the reader last held the tail. */
  private _dirty = false
  /** The reader is off the live end. The DEPARTURE is the one moment a
   *  stale past may be rebuilt under them — they are still within sight of
   *  the live screen, and the anchor pays the change back — because it is
   *  the moment the coverage they are about to read was known to be stale.
   *  Once away, nothing touches the rows they are reading (nocx-zg3k3.10.4:
   *  arriving output does not move the reader). */
  private _away = false
  /** The reader's distance from the live end at the moment a stale rebuild
   *  began (leftTail): paid back by the first page the rebuild installs, so
   *  the gesture that left the tail is not eaten by the replacement. */
  private _rebuildTailGap: number | null = null
  /** The reader's place, held across the caret reveal one keystroke makes
   *  (nocx-zg3k3.15.2) — see _holdThroughTyping. Null when nothing is held. */
  private _holdTop: number | null = null
  private _holdFrame = 0
  /** True only while the unstructured mode owns the pane. */
  private _active = false
  /** One page's two planes, held apart until they meet: the rows document
   *  rides the binary carrier and lands before the result that names it
   *  (one socket, one FIFO — the seam's own pinned ordering). */
  private readonly _results = new Map<
    string,
    { page: SessionHistoryPage; epoch: number; readSeq: number }
  >()
  private readonly _docs = new Map<string, SessionHistoryPageRows>()

  constructor(opts: LiveHistoryOptions) {
    this._scroller = opts.scroller
    this._columns = opts.columns
    this._metric = opts.metric
    this._warm = opts.warm
    this._palette = opts.palette ?? DEFAULT_SNAPSHOT
    this.el = document.createElement('div')
    this.el.className = 'live-history'
    this.el.hidden = true
    opts.stack.insertBefore(this.el, opts.stack.firstChild)
    const onScroll = (): void => {
      // THE ENGINE'S OWN CARET REVEAL IS NOT A SCROLL (nocx-zg3k3.15.2,
      // _holdThroughTyping): the reader did not move, so there is nothing to
      // pump for and nothing to page.
      if (this._restoreHeld()) return
      this._pump()
    }
    opts.scroller.addEventListener('scroll', onScroll, { passive: true })
    // Capture phase: the keystroke belongs to the grid's hidden input, and
    // the hold has to be taken before the engine's default action — the
    // reveal it makes — rather than after the input layer has seen the key.
    const onKeyDown = (ev: KeyboardEvent): void => this._holdThroughTyping(ev)
    opts.scroller.addEventListener('keydown', onKeyDown, true)
    // THE WHEEL, TRANSLATED BY THE SCROLLER'S OWN OWNER (nocx-zg3k3.10.4).
    // The input surface is still xterm's (until nocx-zg3k3.3), and xterm
    // consumes a wheel over the live screen into its OWN viewport — which
    // is empty now that the emulator is the backend's (ADR-0078): the
    // gesture died there and the person's first wheels after output moved
    // nothing. A preventDefault cancels the default action, never the
    // propagation, so this bubble-phase listener on the scroller still
    // sees every wheel and applies the delta itself — in the unstructured
    // mode alone. The alternate screen keeps xterm's wheel (mouse
    // reporting, the program's own mode), and keys are untouched.
    const onWheel = (ev: WheelEvent): void => this._translateWheel(ev)
    opts.scroller.addEventListener('wheel', onWheel, { passive: false })
    this._offScroll = () => {
      opts.scroller.removeEventListener('scroll', onScroll)
      opts.scroller.removeEventListener('wheel', onWheel)
      opts.scroller.removeEventListener('keydown', onKeyDown, true)
    }
  }

  /** Apply one wheel gesture to the scroller, in the units the device
   *  sent. Pixels pass through; lines and pages are scaled by the
   *  geometry the scroller itself knows. */
  private _translateWheel(ev: WheelEvent): void {
    if (!this._active) return
    let delta = ev.deltaY
    if (ev.deltaMode === WheelEvent.DOM_DELTA_LINE) {
      const line = parseFloat(getComputedStyle(this._scroller).lineHeight)
      delta *= Number.isFinite(line) && line > 0 ? line : 20
    } else if (ev.deltaMode === WheelEvent.DOM_DELTA_PAGE) {
      delta *= this._scroller.clientHeight
    }
    this._releaseHold()
    const before = this._scroller.scrollTop
    // The browser clamps; when the clamp holds the reader exactly where
    // they were, there is nothing to claim — but the GESTURE still counts:
    // an empty surface's wheel-up is the "next gesture" a refused page or a
    // timed-out rows document waits on, and the live rectangle fills the
    // scroller exactly, so the clamp would otherwise swallow the retry
    // (the e2e review's second P2). The pump decides; claiming the event
    // needs a move.
    this._scroller.scrollTop = before + delta
    if (this._scroller.scrollTop === before) {
      this._pump()
      return
    }
    ev.preventDefault()
    // The browser's own scroll would have fired one; the translation just
    // made the change ourselves, so the pump runs for the same reason.
    this._pump()
  }

  /** The pane bound a session: the page seam is replaced, the past is
   *  forgotten (a new session is a new history space), and — if the
   *  unstructured mode already owns the pane — the head page is asked for
   *  once, so scrolling up is possible the moment the reader tries. */
  bind(source: LiveHistoryPageSource): void {
    this._source = source
    this._away = false
    this._forget()
    source.onHistoryPageRows((doc) => {
      this._docs.set(doc.pageId, doc)
      if (doc.pageId === this._awaitingRows) {
        // The second plane landed: the install may proceed, and paging with
        // it.
        this._awaitingRows = null
        if (this._awaitTimer !== 0) {
          window.clearTimeout(this._awaitTimer)
          this._awaitTimer = 0
        }
      }
      this._tryInstall(doc.pageId)
    })
    this._sync()
  }

  /** The pane's mode changed. Only the unstructured mode shows the surface
   *  and grows it; every other mode hides it, and the filled-pane
   *  stylesheet rule hides it again where an alt-screen program owns the
   *  pane. Hidden pages stay painted: an alternate-screen excursion moves
   *  nothing in the primary's numbering (the page contract's own words),
   *  so what returns is what left. */
  setMode(mode: 'unstructured' | 'other'): void {
    const active = mode === 'unstructured'
    if (active === this._active) return
    this._active = active
    this.el.hidden = !active
    if (active) this._sync()
  }

  /** Output arrived. What the emulator holds has moved under the reader:
   *  the painted past may no longer reach the head, so the reader's next
   *  return to the live end drops it, and an exhausted surface can ask
   *  again. Nothing visible changes here — that is the point.
   *
   *  With NOTHING painted, though, there is also nothing to scroll: the
   *  fixed live viewport offers no scroll event to grow by, and a reader
   *  at the live end could never discover that history now exists above.
   *  So the empty surface asks for the head again — once per burst of
   *  output, never once per chunk — and the first page that lands is what
   *  gives the wheel something to scroll. */
  noteOutput(): void {
    this._outputSeq++
    // The DIRTY past is a painted past: pages read before this output have
    // a coverage that ends below the head the emulator holds now. An empty
    // surface with a stale cursor has nothing to rebuild — its next read
    // is the head anyway.
    if (this._pages.length > 0) this._dirty = true
    this._exhausted = false
    // Both shapes ask for the head again, one burst at a time: the EMPTY
    // surface so the wheel has somewhere to go, and the STALE one so the
    // past is replaced while the reader still sits at the live end — the
    // one position where a rebuild cannot move them (the install's anchor
    // pays the added height back). A reader who is AWAY is never touched:
    // their stale past waits for their own return (tailReengaged).
    this._scheduleHeadRefresh()
  }

  /** The reader left the live end (the follow sentinel's own word, or the
   *  surface's own geometry check on the first scroll away). A stale past
   *  is rebuilt HERE — the one moment the reader is still within sight of
   *  the live screen and the anchor can pay the change back invisibly —
   *  because it is the moment the coverage they are about to read was
   *  known to be stale. */
  leftTail(): void {
    if (this._away) return
    this._away = true
    if (this._dirty) {
      // The reader's distance from the live end survives the rebuild: the
      // forget clamps scrollTop to what remains and the replacement page
      // would otherwise pay its full height back into scrollTop — the
      // reader ends at the live end again and the gesture that left was
      // eaten (the e2e review's first-wheel finding). The first page the
      // rebuild installs places the reader the same distance from the tail
      // as the moment they wheeled.
      const gap = Math.max(
        0,
        this._scroller.scrollHeight - this._scroller.clientHeight - this._scroller.scrollTop,
      )
      this._forget()
      this._rebuildTailGap = gap
      this._request()
    }
  }

  /** The reader returned to the live end. A stale past is dropped so the
   *  next scroll-up reads the head — every printed line, not the ones that
   *  existed when the reader left — and the head page is asked for once so
   *  the gesture has something to scroll into view. */
  tailReengaged(): void {
    // A live end delivered inside a hold is the engine's caret reveal being
    // reported, not the reader coming back: put them where they were and
    // keep their past. See _holdThroughTyping.
    if (this._restoreHeld()) return
    this._away = false
    if (this._dirty) {
      this._forget()
    }
    this._sync()
  }

  /** The pane sighted a real erase-saved-lines (block.cleared, the same
   *  fact the block model rendezvouses with). The live tier shows the
   *  emulator's view as it is: ED3 erased its saved lines, so nothing
   *  painted survives, and no boundary row is invented. The content above
   *  the reader is gone, so the scroller clamps them back to the live
   *  screen — the reader is AT the live end, wherever they were. */
  cleared(): void {
    this._away = false
    this._forget()
  }

  /** The screen's columns changed. The emulator renumbered its history
   *  under the reflow (ADR-0078: live positions move under reflow), so the
   *  painted pages are addresses of a numbering that no longer exists and
   *  are dropped rather than shown misaligned. As with a clear, the
   *  content above is gone and the reader is back at the live end — and
   *  the head is asked for again at once, because a resize that drops the
   *  past and waits for output to restore it leaves the pane with nothing
   *  to scroll: the wheel cannot ask for what it cannot reach (the e2e
   *  review's first finding). */
  reflowed(): void {
    if (this._pages.length === 0 && this._cursor === null) return
    this._away = false
    this._forget()
    this._sync()
  }

  dispose(): void {
    this._epoch++
    this._offScroll()
    this._results.clear()
    this._docs.clear()
    this._clearTimers()
    this.el.remove()
  }

  private _clearTimers(): void {
    if (this._holdFrame !== 0) {
      window.cancelAnimationFrame(this._holdFrame)
      this._holdFrame = 0
    }
    this._holdTop = null
    if (this._refreshTimer !== 0) {
      window.clearTimeout(this._refreshTimer)
      this._refreshTimer = 0
    }
    this._refreshPending = false
    if (this._awaitTimer !== 0) {
      window.clearTimeout(this._awaitTimer)
      this._awaitTimer = 0
    }
    this._awaitingRows = null
  }

  /** Drop everything painted and forget the cursor: the next scroll-up
   *  reads the head. */
  private _forget(): void {
    this._epoch++
    // The flight is OURS again: a request of the dropped epoch is still on
    // the wire, but its callback carries the old epoch and owns nothing —
    // it will not clear this flag, so the new history can page at once.
    this._inflight = false
    for (const page of this._pages) page.el.remove()
    this._pages = []
    this._cursor = null
    this._exhausted = false
    this._results.clear()
    this._docs.clear()
    this._clearTimers()
    this._dirty = false
    this._rebuildTailGap = null
  }

  /** Ask for the head page once, when there is a reason to want it: the
   *  mode is showing the surface, nothing is in flight, and neither the
   *  session nor the geometry is missing. A dirty surface rebuilds instead
   *  of paging on — see _pump. */
  private _sync(): void {
    if (!this._active || this._inflight) return
    if (this._awaitingRows !== null) return
    if (this._source === null) return
    if (this._columns() === null) return
    if (this._dirty) {
      this._forget()
      this._request()
      return
    }
    if (this._exhausted || this._pages.length > 0) return
    this._request()
  }

  /** A KEYSTROKE IS NOT A SCROLL (nocx-zg3k3.15.2).
   *
   * MEASURED, in the container's WebKit, with the scroller's own `scroll`
   * events logged beside every decision this class makes: the first
   * character typed into the grid throws a reader who is wheeled into the
   * past to the bottom, and between the key and that scroll the trace
   * carries NO scroll call at all — neither this surface's nor the
   * controller's. What moved the scroller is the ENGINE revealing the caret
   * of the focused control by scrolling its ancestors, and the grid's hidden
   * input (xterm's helper textarea) sits at the cursor: inside this scroller,
   * at the live end. The reader is at the live end afterwards, so this
   * class's own rule — a stale past is rebuilt under a reader who is there
   * (`_fireHeadRefresh`, `tailReengaged`) — takes the rows they were reading
   * away, which is the whole of the reported defect.
   *
   * The position is this scroller's — the stylesheet's own
   * `overflow-anchor: none` says why, against the engine's other controller
   * (scroll anchoring) — so a key that TYPES holds the reader's place across
   * the reveal its own default action makes. A key that NAVIGATES (Page Up,
   * the arrows, Home/End) holds nothing, because it is the reader's own way
   * of moving; neither does a wheel (`_translateWheel`), for the same
   * reason. The hold lasts ONE FRAME: long enough for the reveal and for the
   * follow sentinel's delivery of it, and short enough that it can never
   * undo a scroll the reader makes afterwards.
   *
   * The reveal itself is not preventable from here: where that input sits is
   * xterm's (`position: fixed`, or any containing block outside this
   * scroller, would take the IME's candidate window out with it), so the
   * reader's place is kept on this side instead of the engine's scroll being
   * argued out of happening.
   */
  private _holdThroughTyping(ev: KeyboardEvent): void {
    if (this._holdTop !== null) return
    if (ev.ctrlKey || ev.metaKey || ev.altKey) return
    // A key that types one character. `Enter`, `Backspace` and the editing
    // keys are not this: they move no caret the engine has to reveal.
    if (ev.key.length !== 1) return
    if (!this._active) return
    const el = this._scroller
    // Nothing to hold: a scroller with no layout box, or with nothing to
    // scroll, has no place the reader could lose — and one already at its
    // live end has no place the reveal could take them from. The GEOMETRY
    // answers that, rather than this class's own `_away`: a departure the
    // scroll event's own guards swallowed still leaves a reader to keep.
    if (el.clientHeight === 0 || el.scrollHeight <= el.clientHeight) return
    if (isAtTail(el)) return
    this._holdTop = el.scrollTop
    this._holdFrame = window.requestAnimationFrame(() => {
      this._holdFrame = 0
      this._holdTop = null
    })
  }

  /** A hold this surface takes back, released here rather than waited out:
   *  every one of these is a position THIS class or the reader moved, so the
   *  reveal has nothing left to undo. */
  private _releaseHold(): void {
    this._holdTop = null
  }

  /** Put the reader back at the place a hold remembers, and answer whether
   *  there was one to restore. Called from the two places the reveal can
   *  reach this surface: the scroller's own `scroll` event, and the follow
   *  sentinel's delivery of the live end — which is a frame behind the
   *  scroll it reports. */
  private _restoreHeld(): boolean {
    const top = this._holdTop
    if (top === null) return false
    if (this._scroller.scrollTop !== top) this._scroller.scrollTop = top
    return true
  }

  /** Grow while the reader is near the top and the emulator holds more. */
  private _pump(): void {
    if (!this._active || this._inflight) return
    if (this._awaitingRows !== null) return
    if (this._source === null) return
    if (this._columns() === null) return
    // THE DEPARTURE, seen synchronously: the follow sentinel's callback is
    // a frame behind the scroll that crossed the live end, and the rebuild
    // (when the past is stale) belongs to the first scroll away — before
    // the reader is deep enough into the past for a rebuild to move them.
    if (!this._away && !isAtTail(this._scroller)) {
      this.leftTail()
      if (this._inflight) return
    }
    // Growth is for a reader who LEFT the live end: at the tail of a
    // painted history there is nothing to scroll towards, and paging
    // there would chain one page past the head on every install. An
    // EMPTY surface stays eligible — its reader has nowhere to be but
    // the tail, and the wheel needs the first page to have anywhere to
    // go at all.
    if (this._pages.length > 0 && isAtTail(this._scroller)) return
    if (this._dirty) return
    if (this._exhausted) return
    if (this._scroller.scrollTop > this._scroller.clientHeight) return
    this._request()
  }

  /** One coalesced head refresh per burst of output (HEAD_REFRESH_MS's
   *  trailing edge). The refresh re-reads the HEAD — a cursor an empty
   *  answer left behind names a position nothing new ever grows below. */
  private _scheduleHeadRefresh(): void {
    this._refreshPending = true
    if (this._refreshTimer !== 0) return
    this._refreshTimer = window.setTimeout(() => this._fireHeadRefresh(), HEAD_REFRESH_MS)
  }

  private _fireHeadRefresh(): void {
    this._refreshTimer = 0
    if (!this._refreshPending) return
    // Blocked — the head read this refresh wants to precede is still on
    // the wire, or its rows document has not landed. The intent STANDS:
    // _rearHeadRefresh re-arms it the moment that response settles, so an
    // answer that read the buffer before the output departed is followed
    // by the re-read, never replaced by a lost scrollback.
    if (!this._active || this._exhausted || this._inflight) return
    if (this._awaitingRows !== null) return
    if (this._source === null) return
    if (this._columns() === null) return
    if (this._pages.length === 0) {
      // The empty surface: re-read the HEAD — a cursor an empty answer
      // left behind names a position nothing new ever grows below.
      this._refreshPending = false
      this._cursor = null
      this._request()
      return
    }
    // A PAINTED past: the refresh replaces it only under a reader at the
    // live end, the one position a rebuild cannot move — the install's
    // anchor pays the added height back and the reader stays on the live
    // screen with a fresh past above them. A reader who is away, or
    // already scrolling into the past, is never touched here: their stale
    // coverage waits for their own return (tailReengaged), because
    // rebuilding under a departing reader would eat the very gesture that
    // left (nocx-zg3k3.10.4: the first wheel-up moved nothing).
    this._refreshPending = false
    if (!this._away && isAtTail(this._scroller) && this._dirty) {
      this._forget()
      this._request()
    }
  }

  /** Re-arm a retained refresh intent behind the response that blocked it. */
  private _rearHeadRefresh(): void {
    if (!this._refreshPending || this._refreshTimer !== 0) return
    this._refreshTimer = window.setTimeout(() => this._fireHeadRefresh(), HEAD_REFRESH_MS)
  }

  private _request(): void {
    const source = this._source
    if (source === null || this._inflight || this._awaitingRows !== null) return
    const epoch = this._epoch
    const readSeq = this._outputSeq
    this._inflight = true
    source
      .historyPage(this._cursor, PAGE_ROWS)
      .then((page) => {
        // A request a drop left behind owns nothing: not the flight flag —
        // which a new epoch's own request may hold — and not the surface.
        if (epoch !== this._epoch) return
        this._inflight = false
        this._results.set(page.pageId, { page, epoch, readSeq })
        if (this._docs.has(page.pageId)) {
          this._tryInstall(page.pageId)
          return
        }
        // The rows document has not landed yet. The install waits for BOTH
        // planes, and paging waits for the install: the next page's cursor
        // chains from this one.
        this._awaitingRows = page.pageId
        // The seam's own contract makes a dropped page the caller's retry
        // (ipc.ts); a document that never arrives is the same case seen
        // from the other plane. The watchdog gives it back to the next
        // gesture rather than wedging the cursor under a page in flight.
        this._awaitTimer = window.setTimeout(() => {
          this._awaitTimer = 0
          this._awaitingRows = null
          this._results.delete(page.pageId)
          this._rearHeadRefresh()
        }, AWAIT_ROWS_MS)
      })
      .catch((err: unknown) => {
        if (epoch !== this._epoch) return
        // A dropped page is the caller's retry (ipc.ts's own contract): the
        // next scroll gesture asks again. Not an error to survive loudly.
        this._inflight = false
        this._rearHeadRefresh()
        log.debug('nocx: history page refused', {
          error: err instanceof Error ? err.message : String(err),
        })
      })
  }

  /** Install a page whose two planes have both landed. */
  private _tryInstall(pageId: string): void {
    const entry = this._results.get(pageId)
    const doc = this._docs.get(pageId)
    if (entry === undefined || doc === undefined) return
    this._results.delete(pageId)
    this._docs.delete(pageId)
    if (entry.epoch !== this._epoch) return
    const { page } = entry

    // The floor is the emulator's own word on how far its history still
    // reaches. Painted pages at or below it are rows the library no longer
    // holds — retention pruning, or an erase-saved-lines whose notification
    // this surface never saw — and they are dropped, never shown. A fresh
    // page's floor is also how a clear that arrived between two fetches is
    // caught: the floor is the head, everything painted is below it, and
    // the surface empties instead of inventing a boundary.
    this._pruneBelow(page.floor)
    this._cursor = page.start
    // "Nothing remains below the cursor" is the READ's verdict, and it
    // stands only if nothing has departed since the read began.
    this._exhausted = !page.more && entry.readSeq === this._outputSeq
    if (doc.rows.length === 0) {
      this._rearHeadRefresh()
      return
    }
    if (entry.readSeq !== this._outputSeq) {
      // The page was read BEFORE output departed during its flight: its
      // coverage already ends below the head the emulator holds now, and
      // the install is what makes the past stale — noteOutput ran while
      // nothing was painted and could not mark it. The refresh intent
      // STANDS for this page (it is already behind), so the trailing
      // refresh replaces it under an at-tail reader; a departing reader's
      // own first gesture rebuilds it instead (leftTail).
      this._dirty = true
    } else {
      // The refresh intent was FOR this page: the wheel has somewhere to
      // go, and the coverage is current.
      this._refreshPending = false
    }
    this._paint(page, doc)
    this._pump()
    this._rearHeadRefresh()
  }

  /** Drop painted pages the emulator no longer retains, from the oldest
   *  side; a floor that lands INSIDE the oldest page trims it to its
   *  surviving rows, repainted in place. The union stays contiguous, so
   *  the answer that follows — whose `start` this install makes the
   *  cursor — keeps `before` the oldest painted row, or the emptied
   *  surface's own next head read. */
  private _pruneBelow(floor: number): void {
    while (this._pages.length > 0 && this._pages[0].end <= floor) {
      this._pages[0].el.remove()
      this._pages.shift()
    }
    const oldest = this._pages[0]
    if (oldest === undefined || oldest.start >= floor) return
    // The rows below the floor are gone from the emulator: retention
    // pruned them between two reads. Showing them would be showing rows
    // that were asked for and not delivered, so the page is repainted
    // from its surviving rows — the decoded cells are kept for exactly
    // this — and the reader's anchor is paid back the height removed.
    const keep = oldest.rows.slice(floor - oldest.start)
    const metric = this._metric()
    this._warm?.(fitCandidatesOf(keep))
    const el = document.createElement('div')
    el.className = 'live-history-page'
    el.dataset.start = String(floor)
    el.dataset.end = String(oldest.end)
    for (const row of keep) el.appendChild(paintRow(row, { metric, palette: this._palette }))
    const before = this._scroller.scrollHeight
    oldest.el.replaceWith(el)
    const removed = before - this._scroller.scrollHeight
    // Ours, not the engine's: a reveal has nothing left to undo here.
    this._releaseHold()
    if (removed > 0) this._scroller.scrollTop = Math.max(0, this._scroller.scrollTop - removed)
    this._pages[0] = { el, start: floor, end: oldest.end, rows: keep }
  }

  /** Decode one page through the pane's cell model and paint it with the
   *  live painter's own row primitive, above everything painted so far. */
  private _paint(page: SessionHistoryPage, doc: SessionHistoryPageRows): void {
    const cols = this._columns()
    if (cols === null || cols === 0) return
    const frame = {
      revision: 1,
      geometry: { cols, rows: doc.rows.length, cellWidthPx: 1, cellHeightPx: 1, revision: 1 },
      cursor: { x: 0, y: 0, visible: false },
      rows: doc.rows,
    }
    const result = createCellModel().apply(frame)
    if (!result.ok) {
      // The vocabulary is the frame contract's, so a refusal is the same
      // refusal a frame would meet: log, deliver nothing, keep the surface
      // honest. The cursor has already moved past what was delivered.
      log.debug('nocx: history page refused by the cell model', { refusal: result.refusal })
      return
    }
    const metric = this._metric()
    this._warm?.(fitCandidatesOf(result.snapshot.rows))
    const pageEl = document.createElement('div')
    pageEl.className = 'live-history-page'
    pageEl.dataset.start = String(page.start)
    pageEl.dataset.end = String(page.end)
    for (const row of result.snapshot.rows) {
      pageEl.appendChild(paintRow(row, { metric, palette: this._palette }))
    }
    // THE ANCHOR. The page lands above everything, so the rows the reader
    // is looking at would slide down by its height. The scroller owns the
    // position (the stylesheet's own `overflow-anchor: none` — one
    // controller, nocx-6w4z), so the height added above is added to
    // scrollTop in the same task: a reader at the live end stays at it, a
    // reader mid-history keeps their rows, and output arriving below moves
    // nothing at all because the live rectangle is one fixed box.
    const before = this._scroller.scrollHeight
    this.el.insertBefore(pageEl, this.el.firstChild)
    const added = this._scroller.scrollHeight - before
    // Ours, not the engine's: a reveal has nothing left to undo here.
    this._releaseHold()
    if (added > 0) this._scroller.scrollTop += added
    if (this._rebuildTailGap !== null) {
      // A stale rebuild's first page: the reader's own distance from the
      // tail, taken as they left it, is where they land — the gesture that
      // asked to scroll up is honored by the replacement, not reset.
      const gap = this._rebuildTailGap
      this._rebuildTailGap = null
      this._scroller.scrollTop = Math.max(
        0,
        this._scroller.scrollHeight - this._scroller.clientHeight - gap,
      )
    }
    this._pages.unshift({
      el: pageEl,
      start: page.start,
      end: page.end,
      rows: result.snapshot.rows,
    })
  }
}

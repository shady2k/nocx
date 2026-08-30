// Capture identity tracking (spec §2.3, bead nocx-3j9b).
//
// The identity is buffer instance + geometry + generation. The generation
// advances on onWriteParsed plus the explicit state-changing operations
// (buffer switch, resize, clear, reset) — NEVER on onRender: ADR-0005 forces
// periodic repaints on Linux/WebKitGTK, so a paint-driven generation would
// stale a motionless screen continuously, on one platform only.
//
// Deliberately conservative: a write that repaints identical cells still
// advances the generation. We prefer a false "it moved" (costs a re-ask) to
// a false "unchanged" (describes a screen that is gone).
//
// ADR-0029: generation inequality is a TRIGGER, never a verdict. This module
// reports comparability — same | moved | notComparable — and nothing here
// wires inequality to a refusal (that arrives with DRIVE), and no surface
// may present drift as "stale".
//
// THE CAPTURE FENCE. write() queues parsing, so a snapshot taken with bytes
// still queued shows a screen the terminal has already been told to leave.
// The fence is a WRITE BARRIER, not a queue-emptiness check and not the next
// parse boundary: awaitSettled() asks the source for a barrier that settles
// once everything queued BEFORE the request has been parsed (xterm's
// WriteBuffer is FIFO, so an empty write's callback is exactly that).
//
// It waits for all of the backlog and for none of the future, which is what
// makes it both complete and terminating:
//
//   - global emptiness STARVES a continuously repainting TUI (`top`): each
//     per-write callback settles one repaint while the next replaces it, so
//     the count is exact, the callbacks are live, and zero never happens
//     (nocx-2ryxf.3, an instrumented run: pending 1 → 1, generation +1).
//   - the next parse boundary TERMINATES but is INCOMPLETE: xterm breaks its
//     parse loop on a 12 ms budget BETWEEN chunks, so a boundary can arrive
//     with bytes that were already queued when the request came in still
//     unparsed — a bulk write (`cat` of a large file, a reattach replay)
//     would answer with a screen from several passes ago.
//
// A source whose barrier never settles is a wedged renderer, not a busy one
// (a parser handler that threw leaves WriteBuffer with no pass scheduled).
// That is bounded locally and answered as a failed capture; it never reaches
// the broker's 30-second timeout.

import type { CaptureComparability, CaptureEventSource, CaptureIdentity } from './types'

/** Local renderer bound on the write barrier.
 *
 *  Chosen ABOVE the browser's background-tab timer clamp and far below the
 *  broker's 30-second request timeout. xterm continues parsing from
 *  setTimeout(0); a hidden document (window minimised or occluded — the
 *  webview follows page visibility) has those clamped to ~1 s, so a bound at
 *  1 s would turn "the window is in the background" into a failed capture on
 *  a renderer that was about to answer correctly. */
const DEFAULT_CAPTURE_SETTLE_TIMEOUT_MS = 5000

/** Thrown by awaitSettled() when the capture source is disposed before the
 *  fence opens: the renderer is gone (tab close, renderer replacement), so
 *  the frame can never be captured. */
export class CaptureAbortedError extends Error {
  constructor() {
    super('frame capture aborted: the renderer was disposed before the write settled')
    this.name = 'CaptureAbortedError'
  }
}

/** The write barrier never settled and the source was never disposed: the
 *  renderer's parse queue is wedged, not busy — a barrier queued behind a
 *  backlog settles as soon as that backlog is parsed, however much more
 *  arrives behind it. The pull handler turns this into an honest failed
 *  outcome; it must never outlive the broker request in silence. */
export class CaptureUnsettledError extends Error {
  constructor(timeoutMs: number) {
    super(`frame capture did not settle within ${timeoutMs}ms`)
    this.name = 'CaptureUnsettledError'
  }
}

/** Thrown by a live capture when the requested region is entirely past the
 *  end of the buffer: the renderer refuses rather than minting a frame over
 *  rows that do not exist (a frame never lies about gaps — mintLiveFrame's
 *  promise). The pull handler answers such a request with the failed
 *  outcome, honestly. */
export class ReadScreenRangeError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'ReadScreenRangeError'
  }
}

/** A capture parked on the barrier, kept so disposal and the local bound can
 *  settle (reject) it instead of orphaning it forever. The bound's timer
 *  lives on the record so nothing has to close over a binding declared after
 *  it. */
interface AbortableCapture {
  timer?: ReturnType<typeof setTimeout>
  reject: (err: Error) => void
}

export class CaptureIdentityTracker {
  private _generation = 0
  private _buffer: { kind: 'normal' } | { kind: 'alternate'; altSession: number } = {
    kind: 'normal',
  }
  private _altSession = 0
  /** Captures currently parked on a write barrier. */
  private _parked: AbortableCapture[] = []
  /** True once the source reported disposal: the fence can never open. */
  private _disposed = false

  constructor(
    private readonly _source: CaptureEventSource,
    private readonly _settleTimeoutMs = DEFAULT_CAPTURE_SETTLE_TIMEOUT_MS,
  ) {
    _source.onWriteParsed(() => this._onWriteParsed())
    _source.onBufferChange((type) => this._onBufferChange(type))
    _source.onResize(() => this._onExplicitMutation())
    _source.onClear(() => this._onExplicitMutation())
    _source.onReset(() => this._onExplicitMutation())
    // The fence's closing event: disposal must settle (reject) a capture
    // parked on a barrier whose callback went away with the terminal. A
    // waiter with no closing event hangs forever — AGENTS.md rule 3: an
    // invariant needs both ends.
    _source.onDispose(() => this._onSourceDisposed())
  }

  /** The current capture identity. The buffer record is copied so a saved
   *  identity is a snapshot: mutating it cannot corrupt later comparisons. */
  identity(): CaptureIdentity {
    return {
      buffer:
        this._buffer.kind === 'alternate'
          ? { kind: 'alternate', altSession: this._buffer.altSession }
          : { kind: 'normal' },
      cols: this._source.cols,
      rows: this._source.rows,
      generation: this._generation,
    }
  }

  /** Compare a saved identity against the current one.
   *
   *  A buffer switch or a resize is NOT staleness — it is incomparability
   *  (the alt buffer's contents are discarded on exit; a resize reflows and
   *  shifts absolute line indices). Across that discontinuity the answer is
   *  `notComparable`, a distinct value, never a flag on `moved`. */
  compareIdentity(saved: CaptureIdentity): CaptureComparability {
    const current = this.identity()
    if (saved.buffer.kind !== current.buffer.kind) return { status: 'notComparable' }
    if (
      saved.buffer.kind === 'alternate' &&
      current.buffer.kind === 'alternate' &&
      saved.buffer.altSession !== current.buffer.altSession
    ) {
      return { status: 'notComparable' }
    }
    if (saved.cols !== current.cols || saved.rows !== current.rows) {
      return { status: 'notComparable' }
    }
    if (saved.generation !== current.generation) return { status: 'moved' }
    return { status: 'same' }
  }

  /** The capture fence (see the header). Resolves when everything queued
   *  before the call has been parsed; rejects on disposal, and on the local
   *  bound when the source's barrier never settles. */
  async awaitSettled(): Promise<void> {
    this._throwIfDisposed()
    // Nothing queued: the buffer already holds every byte the renderer was
    // given, so capture now. This fast path is not only an optimisation —
    // a barrier issued on a motionless screen costs a parse pass, and a
    // parse pass ADVANCES THE GENERATION. Capturing twice with no output in
    // between would then compare `moved`, which is precisely the false
    // positive ADR-0029 tolerates but never manufactures.
    if (!this._source.hasUnsettledWrite()) return
    await this._awaitWriteBarrier()
  }

  private _throwIfDisposed(): void {
    if (this._disposed) throw new CaptureAbortedError()
  }

  private _awaitWriteBarrier(): Promise<void> {
    if (this._disposed) return Promise.reject(new CaptureAbortedError())
    // Promise.withResolvers needs an ES2024 lib and this project targets
    // ES2021, so the resolver is captured via the executor form.
    let fail!: (err: Error) => void
    const aborted = new Promise<never>((_, reject) => {
      fail = reject
    })
    const parked: AbortableCapture = {
      reject: (err: Error): void => {
        this._unpark(parked)
        fail(err)
      },
    }
    // Registering is synchronous with the unsettled check above; the barrier
    // callback and disposal can only arrive in a later task, so neither the
    // settle nor the closing event can be missed.
    this._parked.push(parked)
    parked.timer = setTimeout(() => {
      parked.reject(new CaptureUnsettledError(this._settleTimeoutMs))
    }, this._settleTimeoutMs)
    const settled = this._source.awaitWriteBarrier().then(() => {
      this._unpark(parked)
    })
    // Promise.race subscribes to both, so `aborted` can never become an
    // unhandled rejection, and a barrier that settles first unparks itself
    // so nothing can reject it afterwards.
    return Promise.race([settled, aborted])
  }

  /** Take a capture off the parked list and stop its bound — after this it
   *  can be neither timed out nor rejected by disposal. */
  private _unpark(parked: AbortableCapture): void {
    if (parked.timer !== undefined) {
      clearTimeout(parked.timer)
      parked.timer = undefined
    }
    const index = this._parked.indexOf(parked)
    if (index >= 0) this._parked.splice(index, 1)
  }

  private _onWriteParsed(): void {
    this._generation++
  }

  /** The source was disposed: a capture parked on the barrier can never
   *  settle — reject it (the reason lives at awaitSettled). */
  private _onSourceDisposed(): void {
    this._disposed = true
    const parked = this._parked
    this._parked = []
    for (const p of parked) p.reject(new CaptureAbortedError())
  }

  private _onBufferChange(type: 'normal' | 'alternate'): void {
    this._generation++
    if (type === 'alternate') {
      this._altSession++
      this._buffer = { kind: 'alternate', altSession: this._altSession }
    } else {
      this._buffer = { kind: 'normal' }
    }
  }

  private _onExplicitMutation(): void {
    this._generation++
  }
}

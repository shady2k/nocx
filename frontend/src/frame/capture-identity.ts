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
// The capture fence: write() queues parsing, so a snapshot taken before the
// next parse pass can miss bytes already handed to xterm. awaitSettled() waits
// for one completed onWriteParsed boundary. That boundary is a coherent buffer
// state even when a continuously repainting TUI has already queued a later
// write; global queue emptiness would starve. A source that claims pending work
// but produces neither a parse boundary nor disposal rejects on a local bound.

import type { CaptureComparability, CaptureEventSource, CaptureIdentity } from './types'

/** Local renderer bound, far below the broker's 30-second request timeout. */
const DEFAULT_CAPTURE_SETTLE_TIMEOUT_MS = 1000

/** Thrown by awaitSettled() when the capture source is disposed before the
 *  fence opens: the renderer is gone (tab close, renderer replacement), so
 *  the frame can never be captured. */
export class CaptureAbortedError extends Error {
  constructor() {
    super('frame capture aborted: the renderer was disposed before the write settled')
    this.name = 'CaptureAbortedError'
  }
}

/** The renderer still claimed a queued write but produced no completed parse
 *  pass within the capture bound. The pull handler turns this into an honest
 *  failed outcome; it must never outlive the broker request in silence. */
export class CaptureUnsettledError extends Error {
  constructor(timeoutMs: number) {
    super(`frame capture did not reach a parse boundary within ${timeoutMs}ms`)
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

export class CaptureIdentityTracker {
  private _generation = 0
  private _buffer: { kind: 'normal' } | { kind: 'alternate'; altSession: number } = {
    kind: 'normal',
  }
  private _altSession = 0
  /** Waiters for the next completed xterm parse pass — the fence's
   *  rendezvous. Each carries its reject so disposal or the local capture
   *  bound settles a pending request instead of orphaning it. */
  private _writeParsedWaiters: Array<{
    resolve: () => void
    reject: (err: Error) => void
  }> = []
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
    // parked on a write that will never settle (the per-write callback went
    // away with the terminal). A waiter with no closing event hangs forever
    // — AGENTS.md rule 3: an invariant needs both ends.
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

  /** The capture fence. With no queued write, resolves immediately. Otherwise
   *  it waits for ONE completed xterm parse pass: JavaScript cannot inspect
   *  the buffer during a pass, so that boundary is a coherent state that
   *  existed even when a continuously repainting TUI already queued the next
   *  write. Requiring global queue emptiness starves exactly that workload.
   *
   *  A claimed pending write that produces neither a pass nor disposal is a
   *  failed capture after the local bound, never a 30-second broker hang. */
  async awaitSettled(): Promise<void> {
    this._throwIfDisposed()
    if (!this._source.hasUnsettledWrite()) return
    await this._waitForWriteParsed()
  }

  private _throwIfDisposed(): void {
    if (this._disposed) throw new CaptureAbortedError()
  }

  private _waitForWriteParsed(): Promise<void> {
    if (this._disposed) return Promise.reject(new CaptureAbortedError())
    // Promise.withResolvers needs an ES2024 lib and this project targets
    // ES2021, so the resolvers are captured via the executor form.
    let resolve!: () => void
    let reject!: (err: Error) => void
    const promise = new Promise<void>((done, fail) => {
      resolve = done
      reject = fail
    })
    const waiter = {
      resolve: (): void => {
        clearTimeout(timer)
        resolve()
      },
      reject: (err: Error): void => {
        clearTimeout(timer)
        reject(err)
      },
    }
    // Subscribing is synchronous with the unsettled check; parse/dispose can
    // only arrive in a later task, so the boundary cannot be missed.
    this._writeParsedWaiters.push(waiter)
    const timer = setTimeout(() => {
      const index = this._writeParsedWaiters.indexOf(waiter)
      if (index < 0) return
      this._writeParsedWaiters.splice(index, 1)
      waiter.reject(new CaptureUnsettledError(this._settleTimeoutMs))
    }, this._settleTimeoutMs)
    return promise
  }

  private _onWriteParsed(): void {
    this._generation++
    const waiters = this._writeParsedWaiters
    this._writeParsedWaiters = []
    for (const w of waiters) w.resolve()
  }

  /** The source was disposed: a capture waiting on the fence can never
   *  settle — reject it (the reason lives at awaitSettled). */
  private _onSourceDisposed(): void {
    this._disposed = true
    const waiters = this._writeParsedWaiters
    this._writeParsedWaiters = []
    for (const w of waiters) w.reject(new CaptureAbortedError())
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

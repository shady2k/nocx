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
// The capture fence: write() queues parsing, so a snapshot taken mid-queue
// can hold row 1 from before a write and row 20 from after it — a state that
// never existed. awaitSettled() defers until no queued write is mid-parse.
// xterm fires onWriteParsed at the end of EVERY parse pass, which can be
// BETWEEN chunks of a large write, so the fence re-checks hasUnsettledWrite()
// after every fire and only opens when the final pass has settled.

import type { CaptureComparability, CaptureEventSource, CaptureIdentity } from './types'

/** Thrown by awaitSettled() when the capture source is disposed before the
 *  fence opens: the renderer is gone (tab close, renderer replacement), so
 *  the frame can never be captured. */
export class CaptureAbortedError extends Error {
  constructor() {
    super('frame capture aborted: the renderer was disposed before the write settled')
    this.name = 'CaptureAbortedError'
  }
}

export class CaptureIdentityTracker {
  private _generation = 0
  private _buffer: { kind: 'normal' } | { kind: 'alternate'; altSession: number } = {
    kind: 'normal',
  }
  private _altSession = 0
  /** Waiters for the next onWriteParsed fire — the fence's rendezvous. Each
   *  carries its reject so disposal can settle (reject) a pending capture
   *  instead of orphaning it forever. */
  private _writeParsedWaiters: Array<{
    resolve: () => void
    reject: (err: CaptureAbortedError) => void
  }> = []
  /** True once the source reported disposal: the fence can never open. */
  private _disposed = false

  constructor(private readonly _source: CaptureEventSource) {
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

  /** The capture fence. Resolves once no queued write is mid-parse; if
   *  nothing is queued it resolves immediately (no event may ever come).
   *
   *  Because the fire can land BETWEEN chunks of one large write, the loop
   *  re-checks hasUnsettledWrite() after every fire and waits for the pass
   *  that actually settles the queue. */
  async awaitSettled(): Promise<void> {
    while (this._source.hasUnsettledWrite()) {
      // A write can outlive its source: disposal mid-parse leaves the
      // pending count stuck forever. That is a refusal, not a wait —
      // rejecting is chosen over resolving-as-not-capturable: "settled"
      // would let a caller mint a frame from a dead renderer, a refusal
      // that looks like success is the exact silence the fence exists to
      // prevent. A caller that sees the rejection knows the capture cannot
      // happen and can decide (re-ask, tell the person).
      this._throwIfDisposed()
      await this._waitForWriteParsed()
    }
  }

  private _throwIfDisposed(): void {
    if (this._disposed) throw new CaptureAbortedError()
  }

  private _waitForWriteParsed(): Promise<void> {
    // Promise.withResolvers needs an ES2024 lib and this project targets
    // ES2021, so the resolvers are captured via the executor form (the
    // codebase pattern).
    let resolve!: () => void
    let reject!: (err: CaptureAbortedError) => void
    const promise = new Promise<void>((done, fail) => {
      resolve = done
      reject = fail
    })
    // Subscribing a waiter is synchronous with the hasUnsettledWrite()
    // check that led here (both run in the same task), and a fire can only
    // arrive in a later task — so this waiter can never miss its event,
    // and a disposal that lands before this registration is caught here
    // instead of orphaning the waiter.
    if (this._disposed) {
      reject(new CaptureAbortedError())
      return promise
    }
    this._writeParsedWaiters.push({ resolve, reject })
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

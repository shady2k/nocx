// The disposable projections (ADR-0024 §5–§7, bead nocx-u7uh.7): the
// command ledger, history persistence and the block model consume the
// kernel — the published-fact state machine in lifecycle/state.ts — and
// hold no lifecycle state of their own. This module is the one observer:
// it subscribes to kernel changes and drives the projections from the
// kernel's current state. The byte stream reaches none of them; the eslint
// Rule 9 boundary keeps the parsing surface out of this directory.
//
// The attempt is the authority everywhere. The ledger binds its pending
// app-owned record to the published attempt and completes it only from the
// attempt (exit status exactly once; an abandoned attempt is `unknown` and
// never successful). History persistence is invoked only for a COMPLETED
// attempt and only when an app-owned record exists — a shell-originated
// attempt opens no record, so its command text, which may carry a literal
// password, persists nowhere (the command-text decision this bead owns:
// authenticated origin makes the completion trustworthy, never the line).
// The block model opens on an attempt and freezes on its completion — the
// VISUAL freeze may wait for the render fence (u7uh.8), but this module
// never does: logical completion (ledger, history) lands on the event alone.
// The DOM half is a port (BlockProjectionPort): the composition root
// (terminal-content) implements it over the scrollback controller; this
// module never touches the DOM, which is what makes it testable without a
// renderer and keeps stream parsing out of the authority path.

import { CommandLedger } from '../command-ledger'
import type { CommandRecord } from '../command-ledger'
import type { ExecutionAttempt, LifecycleKernel } from './state'

/** The block-model half of the projection, implemented by the composition
 *  root over the scrollback controller. Each operation is attempt-keyed:
 *  the DOM freeze only ever acts on the block bound to the attempt. */
export interface BlockProjectionPort {
  /** Bind the running block (opened at the app-owned submit) to the
   *  published attempt. */
  bindBlock(attempt: ExecutionAttempt): void
  /** Open a running block for a shell-originated attempt — no pending app
   *  record exists, so the block is the structure the attempt earns
   *  (ADR-0024 §5), and nothing of it persists. */
  openBlock(attempt: ExecutionAttempt): void
  /** Freeze the bound block with the attempt's authenticated exit status.
   *  The port may defer the VISUAL freeze until the render fence (u7uh.8)
   *  proves where the output ended; the projection never waits for that —
   *  the ledger and history land on this event alone. */
  freezeBlock(attempt: ExecutionAttempt): void
  /** Freeze the bound block as abandoned — the attempt went `unknown`. */
  abandonBlock(attempt: ExecutionAttempt): void
  /** Freeze the running block that never bound to an attempt at all: it was
   *  opened at the app-owned submit and the domain it was submitted under
   *  has ended, so nothing can ever complete it. */
  abandonPending(): void
  /** Freeze the running block as ENTERED: a child domain took the lane, so
   *  the command that opened it — the `ssh` line — has done its local job
   *  and the running slot belongs to the far host's blocks now. No exit
   *  status: the process is still alive and reports its own at the local D
   *  (nocx-95kt). */
  enterBlock(): void
}

/** The history half: persists a completed app-owned record, authorized by
 *  the completed attempt. Resolves with the store's ack or null. */
export type HistoryPort = (rec: CommandRecord, attempt: ExecutionAttempt) => Promise<unknown>

/** Notifies the transport that an app-owned attempt reached its authenticated
 * running boundary. The callback is invoked once per attempt, after the local
 * record is bound and never from stream parsing. */
export type AttemptBindPort = (record: CommandRecord, attempt: ExecutionAttempt) => void

/** One observer that drives the ledger, history and block projections from
 *  the kernel. It holds no lifecycle state: the per-attempt `_bound` and
 *  `_done` sets are idempotency bookkeeping, not a second model — each
 *  published fact still changes exactly the kernel, and the projections
 *  re-read it on every change. */
export class LifecycleProjections {
  /** Attempt ids the projections already bound a record/block to. */
  private readonly _bound = new Set<string>()
  /** Attempt ids already terminal-processed (completed or abandoned) —
   *  an attempt's exit status is applied exactly once. */
  private readonly _done = new Set<string>()
  /** The kernel's ended-domain count as of the last pump. A change means a
   *  domain ended since — the moment an unbound submit became unfinishable. */
  private _endedSeen = 0
  /** The lane's domain-stack depth as of the last pump. A GROWTH is an
   *  environment entry — a child took the lane — which is the moment the
   *  command that opened it stops being the pane's running block. */
  private _depthSeen = 0
  private _unsub: (() => void) | null = null

  constructor(
    private readonly kernel: LifecycleKernel,
    private readonly ledger: CommandLedger,
    private readonly blocks: BlockProjectionPort,
    private readonly persist: HistoryPort,
    private readonly bindAttempt?: AttemptBindPort,
  ) {}

  /** Subscribe to kernel changes and drive the projections once with the
   *  current state (a no-op until the first fact). */
  attach(): void {
    if (this._unsub !== null) return
    this._unsub = this.kernel.onChange(() => this.pump())
    this.pump()
  }

  detach(): void {
    this._unsub?.()
    this._unsub = null
  }

  /** Finalize app-owned work before the kernel forgets the old session. */
  reset(): void {
    for (const id of this._bound) {
      if (this._done.has(id)) continue
      const attempt = this.kernel.attempt(id)
      if (attempt === undefined || attempt.state !== 'open') continue
      const abandoned: ExecutionAttempt = { ...attempt, state: 'unknown' }
      this._done.add(id)
      this.ledger.complete(abandoned)
      this.blocks.abandonBlock(abandoned)
    }
    if (this.ledger.abandonPending() !== null) this.blocks.abandonPending()
    this._bound.clear()
    this._done.clear()
    this._endedSeen = 0
    this._depthSeen = 0
  }

  /** Reconcile the projections with the kernel's current state. The ONLY
   *  input is kernel state; a stream event can never reach this method. */
  pump(): void {
    const state = this.kernel.state
    // Close out every bound attempt the kernel concluded while some OTHER
    // domain held the lane. Two paths reach it: a lane that fell to Lost
    // abandoned all of them (decision 8), and a nested domain that closed
    // abandoned its own — the shell that would have sent `exit`'s
    // completion is the one `exit` destroyed, so the kernel unknowns the
    // attempt as the domain closes and the lane moves straight on to the
    // parent (nocx-mlyu). Reading only the lane's current attempt left that
    // block with a running dot that could never stop.
    this.reconcileBound(state.kind === 'running' ? state.attempt.id : null)
    // A domain ended. Anything submitted under it that never reached an
    // attempt can be completed by nobody: measured against a real sshd, the
    // far shell's start frame for `exit` never leaves — the command destroys
    // the shell that would have sent it — so the block opened at the submit
    // has no attempt to go unknown with, and used to climb forever
    // (nocx-mlyu). A suspension is not an ending and touches none of this:
    // the parent's own block outlives the whole nested session.
    if (this.kernel.endedDomains !== this._endedSeen) {
      this._endedSeen = this.kernel.endedDomains
      if (this.ledger.abandonPending() !== null) this.blocks.abandonPending()
    }
    // A child domain took the lane: the remote session has begun, so the
    // local `ssh` block ends HERE rather than waiting for a completion that
    // will not come until the far session is over (nocx-95kt, nocx-z5k9).
    // Suspension alone is deliberately not enough — the stack does not grow
    // until the child is live, so a handshake that fails freezes nothing.
    // Only growth counts: a parent reclaiming the lane shrinks the stack and
    // must not freeze anything a second time.
    // Depth 1 is the FIRST domain of the lane integrating, which is not an
    // entry into anything — there is no command below it that opened it.
    const depth = this.kernel.domainStack.length
    if (depth >= 2 && depth > this._depthSeen) this.blocks.enterBlock()
    this._depthSeen = depth
    if (state.kind !== 'running') return
    const attempt = state.attempt
    if (attempt.state === 'open') {
      if (this._bound.has(attempt.id)) return
      this._bound.add(attempt.id)
      const rec = this.ledger.bindAttempt(attempt)
      if (rec === null) {
        // Shell-originated: the attempt's line is the shell's own, which
        // may carry a literal password — no ledger record, no history.
        this.blocks.openBlock(attempt)
      } else {
        this.bindAttempt?.(rec, attempt)
        this.blocks.bindBlock(attempt)
      }
      return
    }

    // Terminal: completed or unknown. Process exactly once.
    if (this._done.has(attempt.id)) return
    this._done.add(attempt.id)

    const rec = this.ledger.complete(attempt)
    if (attempt.state === 'completed') {
      // Only a completed attempt persists, and only through its app-owned
      // record — the attempt's own command text never crosses to the store.
      if (rec !== null) void this.persist(rec, attempt)
      this.blocks.freezeBlock(attempt)
    } else {
      this.blocks.abandonBlock(attempt)
    }
  }

  /** Abandon every bound attempt the kernel has already concluded as
   *  `unknown` — a lane that fell to Lost, or a domain that closed under
   *  the attempt. `current` is skipped: pump() processes the lane's own
   *  attempt in order below, so each attempt reaches the ledger and the
   *  block exactly once, in the order the kernel produced it. */
  private reconcileBound(current: string | null): void {
    for (const id of this._bound) {
      if (id === current || this._done.has(id)) continue
      const attempt = this.kernel.attempt(id)
      if (attempt === undefined || attempt.state !== 'unknown') continue
      this._done.add(id)
      this.ledger.complete(attempt)
      this.blocks.abandonBlock(attempt)
    }
  }
}

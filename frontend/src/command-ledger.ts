import type { ExecutionAttempt } from './lifecycle/state'

// Command ledger model (ADR-0008) — the completion projection (ADR-0024,
// bead nocx-u7uh.7). App-owned command records that no stream marker may
// populate or complete: `onMarker` (the anonymous entry point for OSC 133
// kinds) is deleted, and with it the marker cycle (A→B→C→D trust tracking),
// the `trusted` boolean, and the N6 environment-transition machinery
// (enter/completeTransition — stream-driven activation and completion).
// A record is opened at the app-owned submit with its start time (ADR-0024
// §5: the attempt exists, started, before any bytes that could cause the
// shell's own start event are written) and completed only by an
// authenticated attempt: `bindAttempt` ties the pending record to the
// published attempt, `complete` applies its exit status exactly once, and
// an abandoned attempt is `unknown` and never successful. History
// persistence (history-client.ts) consumes the completed record and takes
// the attempt as its authority.
// The status vocabulary of a ledger record. Exported because the
// completion projection reconnects the ledger to history persistence and
// the block model, which name a status across modules (ADR-0024 §5).
// Who submitted a command, in the ledger's own vocabulary: entries.kind
// (internal/content/sqlite.go) distinguishes exactly the two command-bearing
// kinds — the human's shell and the agent's lane. 'action' is the ledger's
// third kind and can never be an author: an action has no block and no
// command line. The author is minted at submit, by the target that submits
// (design §3.1, nocx-iadtt) — never derived afterwards, or a human command
// typed while an agent run is in flight would be attributed to the agent.
export type CommandAuthor = 'shell' | 'agent'
export type CommandStatus = 'running' | 'success' | 'failure' | 'interrupted' | 'unknown'
export interface CommandRecord {
  readonly id: number
  readonly command: string
  readonly cwd: string
  readonly host: string
  /** The submitting target's author, minted at submit and never derived
   *  afterwards (design §3.1). 'shell' is the human; 'agent' is the
   *  assistant's lane. */
  readonly author: CommandAuthor
  status: CommandStatus
  exitCode: number | null
  startedAt: number | null
  endedAt: number | null
  /** Live marker line accessor — read fresh, never cached. */
  readonly lineOf: () => number | undefined
  disposed: boolean
}

export interface LedgerOpts {
  /**
   * Injectable wall clock in Unix epoch milliseconds (`Date.now()` units).
   * startedAt is persisted, survives a restart, and renders as "3 days ago"
   * across sessions — only a wall clock can express that. A monotonic clock
   * (`performance.now()`, milliseconds since page load) would stamp values
   * the store reads as January 1970 and sweeps the moment the row is written
   * (nocx-rtg0.16). If a duration in the ledger ever needs monotonic time,
   * keep a second, separate clock for it — never one clock serving both
   * meanings.
   */
  now: () => number
}

export class CommandLedger {
  private _records: CommandRecord[] = []
  private _nextId = 1
  private readonly _now: () => number

  /** attempt id → record id. The binding is projection bookkeeping: an
   *  attempt belongs to exactly one record, and only the record bound to an
   *  authenticated attempt may be completed by it (ADR-0024 §5). */
  private readonly _attemptBindings = new Map<string, number>()
  constructor(opts: LedgerOpts) {
    this._now = opts.now
  }

  /**
   * Open a new command record at the app-owned submit (ADR-0024 §5: the
   * attempt exists with its start time before any bytes that could cause the
   * shell's own start event are written). The record is 'running' from
   * submit; without an authenticated completion nothing may close it, assign
   * an exit code or persist it.
   *
   * @param command The app-owned submitted command text (from the DOM editor).
   * @param cwd Current working directory at submission time.
   * @param host Empty for local shells, hostname for SSH.
   * @param lineOf An opaque accessor backed by a live xterm IMarker.
   * @param author The submitting target's author — minted at submit, by
   *   the target that submits, and required so no submit path can forget
   *   it and silently attribute a command to the human (design §3.1).
   */
  open(
    command: string,
    cwd: string,
    host: string,
    lineOf: () => number | undefined,
    author: CommandAuthor,
  ): CommandRecord {
    if (!command) throw new Error('command must not be empty')

    const rec: CommandRecord = {
      id: this._nextId++,
      command,
      cwd,
      host,
      status: 'running',
      exitCode: null,
      startedAt: this._now(),
      endedAt: null,
      author,
      lineOf,
      disposed: false,
    }
    this._records.push(rec)
    return rec
  }

  /** All records, oldest first. Returns a defensive copy. */
  records(): readonly CommandRecord[] {
    return [...this._records]
  }

  /** Mark a record as disposed (called when its marker is trimmed). Idempotent. */
  dispose(id: number): void {
    const rec = this._records.find((r) => r.id === id)
    if (rec && !rec.disposed) {
      rec.disposed = true
    }
  }

  /** Look up a record by id. Returns undefined if not found. */
  resolveID(id: number): CommandRecord | undefined {
    return this._records.find((r) => r.id === id)
  }

  /** Tie the single unbound running record to an authenticated attempt.
   *  Mirrors the kernel's attachment semantics: a start attaches to the one
   *  pending app attempt, so the renderer binds the one pending app record.
   *  Returns the bound record, or null when there is no pending record —
   *  a shell-originated attempt, which opens no ledger record (its text may
   *  carry a literal password and never persists; the block projection
   *  still gives it structure). Idempotent for a repeated binding. */
  bindAttempt(attemptId: string): CommandRecord | null {
    const already = this._attemptBindings.get(attemptId)
    if (already !== undefined) return this.resolveID(already) ?? null
    const pending = this._records.find(
      (r) => r.status === 'running' && !this._boundRecordIds().has(r.id),
    )
    if (pending === undefined) return null
    this._attemptBindings.set(attemptId, pending.id)
    return pending
  }

  /** Record ids already claimed by an attempt — one attempt per record. */
  private _boundRecordIds(): Set<number> {
    return new Set(this._attemptBindings.values())
  }

  recordForAttempt(attemptId: string): CommandRecord | undefined {
    const id = this._attemptBindings.get(attemptId)
    return id === undefined ? undefined : this.resolveID(id)
  }

  /** Abandon the single unbound running record: the domain it was submitted
   *  under has ended, so no attempt will ever arrive to carry its status.
   *  Measured against a real sshd (nocx-mlyu): `exit` destroys the shell
   *  that would have sent the start frame, so the record opened at the
   *  submit is never bound to anything — it is not a record awaiting a
   *  completion, it is one that can never receive one. Unknown, never
   *  successful, and it persists nothing (only a completed record does).
   *  Returns the abandoned record, or null when nothing was pending. */
  abandonPending(): CommandRecord | null {
    const bound = this._boundRecordIds()
    const rec = this._records.find((r) => r.status === 'running' && !bound.has(r.id))
    if (rec === undefined) return null
    rec.status = 'unknown'
    rec.exitCode = null
    rec.endedAt = this._now()
    return rec
  }

  /** Complete the record bound to the attempt from the authenticated
   *  attempt (ADR-0024 §5): the exit status is set exactly once, only by an
   *  authenticated same-domain completion, and an abandoned attempt is
   *  `unknown` and never successful. The attempt's command text is never
   *  copied into the record — the record keeps the app-owned text captured
   *  at submit, which may carry references where the shell's wire line
   *  carries resolved values. Returns the completed record, or null when
   *  nothing was bound (a shell-originated attempt persists nothing). */
  complete(attempt: ExecutionAttempt): CommandRecord | null {
    const rec =
      this.recordForAttempt(attempt.id) ??
      this._records.find((r) => r.status === 'running' && !this._boundRecordIds().has(r.id))
    if (rec === undefined) return null
    // 'running' cannot be completed again, by any attempt.
    if (rec.status !== 'running') return null
    if (attempt.state === 'completed') {
      rec.status = attempt.exitCode === 0 ? 'success' : 'failure'
      rec.exitCode = attempt.exitCode ?? null
    } else {
      // abandoned: unknown, and never successful (decision 5's interval).
      rec.status = 'unknown'
      rec.exitCode = null
    }
    rec.endedAt = this._now()
    return rec
  }
}

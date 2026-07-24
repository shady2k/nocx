// Command ledger model (ADR-0008). A keyboard-first structural index of
// trusted command landmarks over a real terminal — not cards. Each OSC 133
// command cycle becomes a compact record with app-owned command text, cwd,
// host, timestamps, status, and exit code. Output bytes are never retained.
//
// Trust logic mirrors input-state.ts §reduce: a clean A→B→C→D cycle is
// trusted; an orphan C, a D with no C, or an A interrupting a running
// command clears trust.

export type CommandStatus = 'running' | 'success' | 'failure' | 'interrupted' | 'unknown'

export interface CommandRecord {
  readonly id: number
  readonly command: string
  readonly cwd: string
  readonly host: string
  status: CommandStatus
  exitCode: number | null
  startedAt: number | null
  endedAt: number | null
  trusted: boolean
  /** Live marker line accessor — read fresh, never cached. */
  readonly lineOf: () => number | undefined
  disposed: boolean
}

export interface LedgerOpts {
  /** Injectable clock. Default: `() => performance.now()`. Never call Date.now(). */
  now: () => number
}

type MarkerEvent = 'A' | 'B' | 'C' | 'D'

/**
 * Internal tracking state for the current prompt/command cycle.
 * Reset on any break in the trusted sequence.
 */
interface CycleState {
  /** Did we see a clean A (arrived from RAW or finished command)? */
  sawCleanA: boolean
  /** Did the B marker confirm ownership after A? */
  sawB: boolean
  /** The record currently in the running slot (C received, D not yet). */
  running: CommandRecord | null
}

function createCycle(): CycleState {
  return { sawCleanA: false, sawB: false, running: null }
}

export class CommandLedger {
  private _records: CommandRecord[] = []
  private _nextId = 1
  private readonly _now: () => number
  private _cycle: CycleState = createCycle()

  constructor(opts: LedgerOpts) {
    this._now = opts.now
  }

  /**
   * Open a new command record. The record starts with status 'unknown' and
   * is transitioned to 'running' when the OSC 133 C marker arrives.
   *
   * @param command The app-owned submitted command text (from the DOM editor).
   * @param cwd Current working directory at submission time.
   * @param host Empty for local shells, hostname for SSH.
   * @param lineOf An opaque accessor backed by a live xterm IMarker.
   */
  open(
    command: string,
    cwd: string,
    host: string,
    lineOf: () => number | undefined,
  ): CommandRecord {
    if (!command) throw new Error('command must not be empty')

    // L2: open() while a record is still running finalizes the old one.
    if (this._cycle.running) {
      this._finalizeRunning()
    }

    const rec: CommandRecord = {
      id: this._nextId++,
      command,
      cwd,
      host,
      status: 'unknown',
      exitCode: null,
      startedAt: null,
      endedAt: null,
      trusted: false,
      lineOf,
      disposed: false,
    }
    this._records.push(rec)
    return rec
  }

  /**
   * Feed an OSC 133 marker into the ledger. Advances the current cycle
   * state and transitions open records between statuses.
   */
  onMarker(kind: MarkerEvent, exitCode?: number): void {
    switch (kind) {
      case 'A': {
        // A fresh prompt. If a record is currently running and this A
        // interrupts it (no D arrived), finalize it.
        if (this._cycle.running) {
          this._cycle.running.status = this._cycle.running.trusted ? 'interrupted' : 'unknown'
          this._cycle.running.endedAt = this._now()
        }
        // Start a new prompt cycle. Trusted only when we didn't interrupt
        // a running command — i.e. the cycle was idle or completed.
        this._cycle = {
          sawCleanA: this._cycle.running === null,
          sawB: false,
          running: null,
        }
        break
      }
      case 'B': {
        // B grants ownership only when a clean A preceded it (mirrors
        // input-state.ts: gating on trusted closes the B,B latch).
        if (this._cycle.sawCleanA) {
          this._cycle.sawB = true
        }
        break
      }
      case 'C': {
        // Command output start. Find the most recently opened record that is
        // still pending ('unknown') and transition it to running.
        const pending = this._findPending()
        if (pending) {
          pending.status = 'running'
          pending.startedAt = this._now()
          // Trusted only when a clean A→B sequence preceded this C.
          pending.trusted = this._cycle.sawCleanA && this._cycle.sawB
          this._cycle.running = pending
        }
        break
      }
      case 'D': {
        // Command finished. Only meaningful while a record is running.
        if (this._cycle.running) {
          const rec = this._cycle.running
          rec.endedAt = this._now()
          if (exitCode !== undefined) {
            rec.exitCode = exitCode
          }
          // L1: D with no exit code known → 'unknown', not 'failure'.
          rec.status =
            rec.exitCode === 0 ? 'success' : rec.exitCode !== null ? 'failure' : 'unknown'
          this._cycle = createCycle()
        }
        break
      }
    }
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

  /** B3: finalize any still-running record (fail-open on reset/exit). */
  finalizeOpen(): void {
    this._finalizeRunning()
  }

  /** Internal: finalize the current running record and reset cycle state. */
  private _finalizeRunning(): void {
    if (this._cycle.running) {
      this._cycle.running.status = this._cycle.running.trusted ? 'interrupted' : 'unknown'
      this._cycle.running.endedAt = this._now()
    }
    this._cycle = createCycle()
  }

  /**
   * Find the most recently opened record that is still pending (status
   * 'unknown'). Used by the C marker to transition exactly one record.
   */
  private _findPending(): CommandRecord | null {
    for (let i = this._records.length - 1; i >= 0; i--) {
      if (this._records[i].status === 'unknown') return this._records[i]
    }
    return null
  }
}

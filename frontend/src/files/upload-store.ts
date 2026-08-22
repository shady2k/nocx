// UploadStore — what the renderer knows about the transfers it started.
//
// ## The one property
//
// IN-FLIGHT STATE COMES FROM files.upload's RESULT AND files.uploadDone,
// AND NEVER FROM HAVING SEEN A PROGRESS NOTIFICATION (design §5.5).
//
// files.uploadProgress is an INDICATOR, not a ledger. It is emitted to the
// binding's session's current subscriber, resolved at emit time, and
// DROPPED when there is none — "we are at 40%" expires in about a second
// and a queue of expired ones is worse than silence. files.uploadDone is
// the opposite: it is retained against the session and flushed on the next
// attach, because a transfer is bounded by its session and not by the
// WebSocket that started it.
//
// So a person can start a 400 MB upload, close the laptop, and reattach
// after it finished — and receive one uploadDone and not a single progress
// frame. Two ways to get this wrong, both of which this store's shape makes
// unwritable: infer "running" from the first sample and that transfer never
// started; infer "still running" from the absence of samples and it uploads
// forever.
//
// Three rules follow, and each is a test:
//
// 1. `begin` (from the RPC result) is the only thing that STARTS a row, and
//    `applyDone` is the only thing that ENDS one.
// 2. A progress frame for a transfer with no row is ignored, and a progress
//    frame for a row that has ended does not reopen it.
// 3. A done frame for a transfer with no row MINTS one, marked `adopted`.
//    That is the case retention exists to serve — the row lived in a page
//    that was reloaded — and the correct handling is to say the upload
//    finished, not to discard the frame.
//
// Rule 1 is why there are two non-terminal phases and not one. When the
// renderer's own half of a transfer ends without an answer — a `409`, a
// dropped connection — it has learnt something and it has NOT learnt the
// outcome. `unsettled` is that state written down. Collapsing it into
// `failed` would make the renderer the second author of a terminal
// account, over the top of the one notification that is retained across a
// reconnect for exactly this reason.
//
// ## Speed is derived here
//
// The wire carries `bytes` and `total`; bytes-per-second is this store's
// arithmetic over successive samples. If no samples arrive there is no
// speed and the surface shows NOTHING — not zero, which is a claim that
// the transfer is stalled. The byte count is `null` for the same reason
// and until the same moment.

import { createSignal, untrack } from 'solid-js'
import type { FilesUploadDone } from '../generated/files.uploadDone'
import type { FilesUploadProgress } from '../generated/files.uploadProgress'
import { isTerminalPhase, type OperationPhase } from '../ui/operation'
import type { UploadServices } from './upload-client'

/**
 * How many FINISHED transfers this store remembers.
 *
 * A finished transfer does not vanish — somebody who goes to look at the
 * operations indicator can see that it really landed — and it does not
 * accumulate either. It used to do the second thing: a row stayed above the
 * Files tree until a person clicked its `×`, which is a chore the product
 * invented for itself and a list that grows for as long as the app is open.
 *
 * The bound lives HERE, in the store, because remembering is what a store
 * does; the operations model that reads it only orders what it is given. A
 * second source of operations bounds its own memory the same way, for the
 * same reason.
 *
 * Nothing is persisted: this is session memory, and history across restarts
 * is a separate conversation the notification design defers on purpose.
 */
export const FINISHED_TRANSFERS_RETAINED = 20

export interface UploadTransfer {
  /** The backend's opaque id — the address of every later frame. */
  transferId: string
  /** The name asked for. `finalName` is what was actually written, which
   *  keepBoth may have changed. */
  name: string
  destDir: string
  /** WHICH MACHINE `destDir` is on, as a person names it (machine-name.ts).
   *  '' only where it is genuinely unknown — an adopted transfer, which
   *  never saw the call that started it. The list is GLOBAL, one list for
   *  every tab, so a path without a machine beside it answers nothing the
   *  moment two connections are open (owner, 2026-08-22). It is recorded
   *  HERE, at the start, because this store is deliberately global and
   *  knows neither binding nor session: there is nothing to derive it from
   *  later. */
  machine: string
  /** The declared size, refreshed from any progress frame's `total`. Zero
   *  is legitimate: an empty file is a file — and null is the ABSENCE of a
   *  declaration, which is what an adopted transfer has. */
  size: number | null
  /** Bytes confirmed onto the server, or null while nothing has been
   *  observed. NOT zero — zero is a measurement and this is its absence. */
  bytes: number | null
  /** Derived, never on the wire. Null until two samples exist. */
  speedBytesPerSecond: number | null
  phase: OperationPhase
  /** When `begin` was called, on the store's own clock; null for an adopted
   *  transfer, which this renderer never saw start. The other end of the
   *  duration a finished row reports — a span needs both, and naming only
   *  the end is how a "how long did it take" becomes a moment. */
  startedAt: number | null
  /** When the transfer reached a terminal phase, on the store's own clock;
   *  null while it is still live. It is what orders the finished half of
   *  the operations list and what decides which one falls off the end —
   *  the ARRAY's order is start order, and a transfer that started first
   *  does not finish first. */
  endedAt: number | null
  /** The name actually written; empty for every outcome but `written`. */
  finalName: string
  /** Why the row says what it says: the wire's reason on `failed`, and
   *  what the renderer's own half hit on `unsettled` — which is a reason
   *  for not knowing and not a fault. Null on every other phase; a
   *  cancelled transfer's underlying error is a context cancellation,
   *  which must never be shown to a person as a failure. */
  error: string | null
  /** Paths the transfer created or moved and could not clean up. Always an
   *  array; orthogonal to the outcome. */
  stranded: string[]
  /** Minted by a retained `uploadDone` alone — this renderer never saw the
   *  transfer start, because the row lived in a page that was reloaded.
   *  Its `destDir` and `size` are unknown and say so rather than guessing. */
  adopted: boolean
}

export interface UploadStore {
  /** Every transfer this renderer knows about, oldest first. */
  transfers(): UploadTransfer[]
  transfer(transferId: string): UploadTransfer | undefined
  /** Seed a row from files.upload's RESULT. This — not a progress frame —
   *  is what says a transfer exists. */
  begin(t: {
    transferId: string
    name: string
    destDir: string
    machine: string
    size: number
  }): void
  /**
   * Record a failure only the renderer can know about, and that is the
   * whole story: no bytes left this machine and none are going to — the
   * file could not be read, or the sink refused the body outright. It is
   * still not the terminal ACCOUNT: `files.uploadDone` overrules whatever
   * this wrote, because the backend may have its own view of a transfer it
   * created.
   */
  failLocally(transferId: string, error: string): void
  /**
   * Record that the renderer's half is over and THE OUTCOME IS UNKNOWN —
   * the body was claimed by another request, or the connection dropped
   * mid-send. Not a failure and not an ending: the transfer may be running
   * on the backend this moment, `files.uploadCancel` still reaches it, and
   * `files.uploadDone` is what will say how it went.
   */
  unsettle(transferId: string, reason: string): void
  /** Ask the backend to cancel. Deliberately changes NO phase: the person's
   *  cancel races the transfer's own completion every time, and what
   *  actually happened arrives as `uploadDone` with outcome `cancelled`
   *  when the cancel landed and `written` when it did not. */
  cancel(transferId: string): void
  /**
   * Has the person asked for this transfer to stop?
   *
   * The renderer's own intent, recorded the instant `cancel` is called and
   * never unset — an id that was cancelled stays cancelled whatever the
   * backend then says, because the question this answers is "did we ask",
   * not "did it work". It is NOT a phase and must never be rendered as one:
   * the cancel races the transfer's own completion every time and
   * `files.uploadDone` is still the only account of what happened.
   *
   * It exists because a body send that fails AFTER a cancel is the cancel
   * working, and the renderer is the only party that knows that (nocx-hbdw4.3).
   */
  cancelRequested(transferId: string): boolean
  /** Drop the wire subscriptions. */
  dispose(): void
}

export interface UploadStoreDeps {
  services: UploadServices
  /** The clock speed is measured against, injected so a test can move it by
   *  hand — a rate derived from a real duration is a test that depends on
   *  timing, which is broken on a fast machine too. */
  now?: () => number
}

/**
 * How much of a new measurement the reported speed takes. Frames are
 * COALESCED — at most one in flight per transfer, the byte count
 * overwritten in place — so consecutive samples can skip arbitrarily far
 * ahead and the instantaneous rate between two of them swings wildly. The
 * reported number is a number a person reads, so it moves towards each new
 * measurement rather than snapping to it. The first measurement has nothing
 * to move from and is taken whole.
 */
const SPEED_SMOOTHING = 0.4

export function createUploadStore(deps: UploadStoreDeps): UploadStore {
  const { services } = deps
  const now = deps.now ?? (() => Date.now())
  const [transfers, setTransfers] = createSignal<UploadTransfer[]>([])
  /** The last progress sample per transfer, which is the whole of what
   *  deriving a rate needs. Kept beside the records rather than inside
   *  them: it is arithmetic bookkeeping, and a surface that could read it
   *  would start rendering it. */
  const samples = new Map<string, { at: number; bytes: number }>()
  /** Transfers the person has asked to stop. Ids and not records: a cancel
   *  is legitimate for a transfer with no row (it finished, or the page
   *  reloaded), and the set is what makes the answer survive the row. */
  const cancelled = new Set<string>()
  let disposed = false

  /** The public lookup: a TRACKED read, so a surface that asks for one
   *  transfer inside its JSX re-renders when that transfer moves. */
  const find = (transferId: string): UploadTransfer | undefined =>
    transfers().find((t) => t.transferId === transferId)

  /** The same lookup for the wire handlers below, UNTRACKED. They are
   *  event handlers and not derivations: they read the current value to
   *  decide what to write next, and a tracked read there would be a
   *  subscription nothing owns. */
  const currentOf = (transferId: string): UploadTransfer | undefined =>
    untrack(transfers).find((t) => t.transferId === transferId)

  /**
   * Drop the oldest FINISHED transfers past the bound, and nothing else.
   *
   * Oldest by `endedAt`, never by position: the array is in START order and
   * a transfer that started first does not finish first. A live transfer is
   * never dropped whatever the count — the bound is about how long an
   * outcome is worth reading, and a running one has no outcome yet.
   */
  const retain = (list: UploadTransfer[]): UploadTransfer[] => {
    const finished = list.filter((t) => t.endedAt !== null)
    if (finished.length <= FINISHED_TRANSFERS_RETAINED) return list
    const keep = new Set(
      [...finished]
        .sort((a, b) => (b.endedAt ?? 0) - (a.endedAt ?? 0))
        .slice(0, FINISHED_TRANSFERS_RETAINED)
        .map((t) => t.transferId),
    )
    for (const t of finished) if (!keep.has(t.transferId)) samples.delete(t.transferId)
    return list.filter((t) => t.endedAt === null || keep.has(t.transferId))
  }

  /** Replace one record in place; a no-op when there is no row for the id.
   *  Immutable replacement, so a Solid surface sees the change. */
  const patch = (transferId: string, change: (t: UploadTransfer) => UploadTransfer): void => {
    setTransfers((list) => {
      const i = list.findIndex((t) => t.transferId === transferId)
      if (i === -1) return list
      const next = [...list]
      next[i] = change(list[i])
      return retain(next)
    })
  }

  function begin(t: {
    transferId: string
    name: string
    destDir: string
    machine: string
    size: number
  }): void {
    if (disposed) return
    const startedAt = now()
    setTransfers((list) => {
      // A transfer the retained done frame already adopted, now being
      // begun: one row, not two. The id is the identity.
      if (list.some((x) => x.transferId === t.transferId)) return list
      return [
        ...list,
        {
          transferId: t.transferId,
          name: t.name,
          destDir: t.destDir,
          machine: t.machine,
          size: t.size,
          bytes: null,
          speedBytesPerSecond: null,
          phase: 'running',
          startedAt,
          endedAt: null,
          finalName: '',
          error: null,
          stranded: [],
          adopted: false,
        },
      ]
    })
  }

  function applyProgress(p: FilesUploadProgress): void {
    const current = currentOf(p.transferId)
    // Rule 2: no row means nothing to draw; a finished row is not reopened
    // by a frame that was in flight when it ended.
    if (current === undefined || isTerminalPhase(current.phase)) return
    const at = now()
    const last = samples.get(p.transferId)
    let speed = current.speedBytesPerSecond
    if (last !== undefined && at > last.at && p.bytes >= last.bytes) {
      // The count is a running TOTAL and never an increment, so the delta
      // is the difference between two totals — which is also why a frame
      // that skipped several chunks still yields the right average.
      const instant = ((p.bytes - last.bytes) * 1000) / (at - last.at)
      speed = speed === null ? instant : speed + SPEED_SMOOTHING * (instant - speed)
    }
    samples.set(p.transferId, { at, bytes: p.bytes })
    patch(p.transferId, (t) => ({
      ...t,
      bytes: p.bytes,
      // `total` is repeated on every frame on purpose: a transfer adopted
      // on reattach learns its size here and nowhere else.
      size: p.total,
      speedBytesPerSecond: speed,
    }))
  }

  function applyDone(p: FilesUploadDone): void {
    if (disposed) return
    const phase: OperationPhase = p.outcome
    const endedAt = now()
    samples.delete(p.transferId)
    setTransfers((list) => {
      const i = list.findIndex((t) => t.transferId === p.transferId)
      // Rule 3: adopt a transfer this renderer never saw start.
      if (i === -1) {
        return retain([
          ...list,
          {
            transferId: p.transferId,
            name: p.finalName,
            destDir: '',
            // Unknown, and saying so: this renderer never saw the call that
            // would have carried the machine, the destination or the size,
            // and a guess at any of the three reads exactly like a fact.
            machine: '',
            size: null,
            bytes: null,
            speedBytesPerSecond: null,
            phase,
            startedAt: null,
            endedAt,
            finalName: p.finalName,
            error: p.outcome === 'failed' ? (p.error ?? null) : null,
            stranded: p.stranded,
            adopted: true,
          },
        ])
      }
      const next = [...list]
      next[i] = {
        ...list[i],
        phase,
        // Set once, by whoever moved the record to a terminal phase — a
        // second uploadDone for the same transfer cannot rewrite when it
        // ended, and the retention order stays stable.
        endedAt: list[i].endedAt ?? endedAt,
        finalName: p.finalName,
        // Only a failure carries a reason, and it replaces whatever the
        // renderer recorded about its own half of the transfer.
        error: p.outcome === 'failed' ? (p.error ?? null) : null,
        stranded: p.stranded,
        speedBytesPerSecond: null,
      }
      return retain(next)
    })
  }

  function failLocally(transferId: string, error: string): void {
    patch(transferId, (t) =>
      isTerminalPhase(t.phase)
        ? t
        : { ...t, phase: 'failed', endedAt: now(), error, speedBytesPerSecond: null },
    )
  }

  function unsettle(transferId: string, reason: string): void {
    // The rate stops rather than freezing at its last value: it is
    // arithmetic over samples, and the renderer has stopped taking them.
    // The byte count is left alone — for a `409` the other claimant's
    // progress frames are real and keep arriving, and applyProgress goes
    // on applying them without ever deciding the phase.
    patch(transferId, (t) =>
      isTerminalPhase(t.phase)
        ? t
        : { ...t, phase: 'unsettled', error: reason, speedBytesPerSecond: null },
    )
  }

  function cancel(transferId: string): void {
    // The INTENT is recorded before the call goes out, and synchronously:
    // the body POST this cancel is about to break may fail before the
    // request resolves, and the flow asks this question at that moment.
    cancelled.add(transferId)
    // Fire and forget, and quiet: cancelling a transfer that has already
    // finished, or one that never existed, is not an error. What happened
    // arrives as uploadDone either way.
    void services.cancel(transferId).catch(() => {})
  }

  const unsubProgress = services.subscribeProgress(applyProgress)
  const unsubDone = services.subscribeDone(applyDone)

  function cancelRequested(transferId: string): boolean {
    return cancelled.has(transferId)
  }

  function dispose(): void {
    disposed = true
    unsubProgress()
    unsubDone()
    samples.clear()
    cancelled.clear()
    setTransfers([])
  }

  return {
    transfers,
    transfer: find,
    begin,
    failLocally,
    unsettle,
    cancel,
    cancelRequested,
    dispose,
  }
}

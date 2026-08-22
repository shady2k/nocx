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
// ## And one thing the wire cannot tell it
//
// The three rules above are about state the BACKEND owns. A batch has a
// second half the backend has never heard of: design §4 sends one file at
// a time per binding, so every file after the first is waiting its turn,
// and until nocx-hbdw4.6 that half existed nowhere — the flow held it in a
// loop variable. Drop five files and one row appeared; the other four were
// not in the list, not in the count and not cancellable (owner,
// 2026-08-22).
//
// So `enqueue` is a fourth rule and NOT a hole in the first three: it
// records what THIS RENDERER intends, which is the one thing it is the
// authority on, and it deliberately cannot produce `running`. A queued row
// has no transferId, takes no progress frame, and reaches `running` only
// through `start`, which is still fed by files.upload's result and nothing
// else. The property the first three rules defend — that in-flight state
// never comes from having seen a notification — is untouched by a row that
// says "not started".
//
// That is also why a row's identity is `id` and not `transferId`: a queued
// file is real before the backend has named it. `id` is minted here when
// the file joins the queue and never changes; `transferId` is the WIRE's
// address for it, null until there is one. A surface keys on `id`, so a
// row survives its own promotion from queued to running.
//
// Rule 1 is why there are two non-terminal phases the wire can produce and
// not one. When the renderer's own half of a transfer ends without an
// answer — a `409`, a dropped connection — it has learnt something and it
// has NOT learnt the outcome. `unsettled` is that state written down. Collapsing it into
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
  /**
   * THE ROW'S IDENTITY, minted here when the file joins the queue and never
   * changed afterwards — not even when the backend names the transfer.
   *
   * It is not the transferId because a queued file is real before there is
   * one, and it must not BECOME the transferId either: a surface keys its
   * rows on this (see operations-view.tsx, and nocx-hbdw4.1 for what a
   * re-keyed row costs somebody mid-press), so a promotion from queued to
   * running has to leave the identity alone.
   */
  id: string
  /** The backend's opaque id — the address of every later frame — and null
   *  while the file is still queued, because the call that mints it has
   *  not been made. It is the WIRE's name for the row, never the row's. */
  transferId: string | null
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
  /** When `start` was called, on the store's own clock; null for a transfer
   *  that has not started — a QUEUED row, whose turn has not come, and an
   *  ADOPTED one, which this renderer never saw start. The other end of the
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

/** One file joining the queue: everything knowable about it before the
 *  backend has been told it exists. Not exported — a caller passes the
 *  literal and never names the type, the way the flow's collision ask is
 *  not named either. */
interface QueuedUpload {
  name: string
  destDir: string
  machine: string
  size: number
}

export interface UploadStore {
  /** Every transfer this renderer knows about, oldest first. */
  transfers(): UploadTransfer[]
  /** Look one up by the WIRE's address. Undefined for a queued row, which
   *  has none — `row` is what reaches those. */
  transfer(transferId: string): UploadTransfer | undefined
  /** Look one up by the row's own identity, which every row has. */
  row(id: string): UploadTransfer | undefined
  /**
   * REGISTER A WHOLE BATCH BEFORE THE FIRST ONE IS SENT, and answer with
   * one id per file, in the order given.
   *
   * This is the only thing that puts a row on screen that the backend has
   * not been told about, and it is what makes a multi-file drop visible at
   * all: the send is sequential by design (§4), so without this the files
   * after the first exist nowhere and cannot be counted or cancelled.
   *
   * It cannot produce `running` and does not pretend to know anything it
   * was not told: no bytes, no speed, no start time. The one thing it does
   * know is the declared size, which is what lets the aggregate progress
   * bar be about the batch rather than about whichever file is moving.
   */
  enqueue(files: readonly QueuedUpload[]): string[]
  /**
   * Give a queued row the address files.upload just answered with, and set
   * it running. Still the RESULT and never a progress frame — rule 1 is
   * unchanged, this is only where it is written down now.
   *
   * Answers FALSE when the row is gone, which means the person cancelled
   * the file while its files.upload call was in flight. That is not a
   * no-op to swallow: the transfer now exists on the backend and nobody
   * owns it, so this cancels it on the wire and remembers not to adopt the
   * outcome when it arrives. The caller's job is only to send no body.
   */
  start(id: string, transferId: string): boolean
  /**
   * Close a queued row that will never be attempted, with the reason.
   *
   * A refusal from files.upload stops the batch — it is almost always
   * about the destination, and one message per remaining file would be
   * noise — and the files after it are now ROWS. They cannot be left
   * waiting for a turn that is not coming, and they cannot be deleted
   * either: nobody asked for them to be dropped. `skipped` is the wire's
   * own word for "not written, and that is fine", which is exactly what
   * happened to them.
   */
  abandon(id: string, reason: string): void
  /**
   * Record a failure only the renderer can know about, and that is the
   * whole story: no bytes left this machine and none are going to — the
   * file could not be read, the sink refused the body outright, or
   * files.upload refused the call and no transfer was ever created. It is
   * still not the terminal ACCOUNT where there IS a transfer:
   * `files.uploadDone` overrules whatever this wrote, because the backend
   * may have its own view of a transfer it created. Where the call itself
   * was refused there is nothing to overrule it, which is precisely why
   * the row has to be closed here.
   */
  failLocally(id: string, error: string): void
  /**
   * Record that the renderer's half is over and THE OUTCOME IS UNKNOWN —
   * the body was claimed by another request, or the connection dropped
   * mid-send. Not a failure and not an ending: the transfer may be running
   * on the backend this moment, `files.uploadCancel` still reaches it, and
   * `files.uploadDone` is what will say how it went.
   */
  unsettle(id: string, reason: string): void
  /**
   * Stop one row — the whole of what the row's × means, for every row that
   * has one, because a person pressing it has asked for one thing and the
   * difference is the store's to know rather than the surface's.
   *
   * A STARTED transfer is asked to stop on the wire, and the phase
   * deliberately does not change: the person's cancel races the transfer's
   * own completion every time, and what actually happened arrives as
   * `uploadDone` with outcome `cancelled` when the cancel landed and
   * `written` when it did not.
   *
   * A QUEUED one has no transfer to stop — files.upload was never called
   * for it — so nothing goes on the wire and the file simply leaves the
   * batch. It is removed rather than left as a `cancelled` outcome because
   * there is no outcome: nothing was attempted, nothing was written, and a
   * row in the finished list would assert that something happened to it.
   * The row going is what the person asked for and is the whole feedback.
   *
   * ONE PRESS STOPS ONE FILE, whichever kind of row it was on. Cancelling
   * the running file does not drop the ones behind it and cancelling a
   * waiting one does not stop the running one — the button is labelled
   * with the file's own name, so anything else would make the batch's fate
   * depend on which row somebody happened to press (nocx-hbdw4.6).
   */
  cancel(id: string): void
  /**
   * Has the person asked for this transfer to stop?
   *
   * The renderer's own intent, recorded the instant `cancel` is called and
   * never unset — a row that was cancelled stays cancelled whatever the
   * backend then says, because the question this answers is "did we ask",
   * not "did it work". Asked of a QUEUED row it is moot and stays false:
   * that row is gone, and the only caller asks after a body send, which a
   * queued file never reaches. It is NOT a phase and must never be
   * rendered as one:
   * the cancel races the transfer's own completion every time and
   * `files.uploadDone` is still the only account of what happened.
   *
   * It exists because a body send that fails AFTER a cancel is the cancel
   * working, and the renderer is the only party that knows that (nocx-hbdw4.3).
   */
  cancelRequested(id: string): boolean
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
  /** Rows the person has asked to stop, by row id. A set and not a field
   *  on the record: the question it answers is "did we ask", which stays
   *  true after the outcome lands and must survive the record being
   *  replaced on every progress frame. */
  const cancelled = new Set<string>()
  /**
   * Transfers this renderer started and then disowned — the person
   * cancelled the row while files.upload was in flight, so by the time an
   * id came back there was nothing to give it to.
   *
   * They are remembered by TRANSFER id, and only so that the retained
   * `uploadDone` for them does not mint a row (rule 3). Adoption exists for
   * a transfer this renderer never saw start; this is the opposite case —
   * it saw it start, it stopped it on purpose, and the person watched the
   * row go. Bringing it back as an adopted row that knows neither its
   * destination nor its size would be the store contradicting them.
   */
  const disowned = new Set<string>()
  /** Mints row ids. A counter and not a random string: the ids never leave
   *  this renderer, and a monotonic one makes a test's failure readable. */
  let nextRowId = 0
  let disposed = false

  /** The public lookups: TRACKED reads, so a surface that asks for one
   *  transfer inside its JSX re-renders when that transfer moves. Two of
   *  them because a row has two names — its own, which it always has, and
   *  the wire's, which it has only once the backend has minted one. */
  const find = (transferId: string): UploadTransfer | undefined =>
    transfers().find((t) => t.transferId === transferId)

  const findRow = (id: string): UploadTransfer | undefined => transfers().find((t) => t.id === id)

  /** The same lookups for the handlers below, UNTRACKED. They are event
   *  handlers and not derivations: they read the current value to decide
   *  what to write next, and a tracked read there would be a subscription
   *  nothing owns. */
  const currentOf = (transferId: string): UploadTransfer | undefined =>
    untrack(transfers).find((t) => t.transferId === transferId)

  const currentRow = (id: string): UploadTransfer | undefined =>
    untrack(transfers).find((t) => t.id === id)

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
        .map((t) => t.id),
    )
    for (const t of finished) {
      if (!keep.has(t.id) && t.transferId !== null) samples.delete(t.transferId)
    }
    return list.filter((t) => t.endedAt === null || keep.has(t.id))
  }

  /** Replace one record in place, by ROW id; a no-op when there is no row.
   *  Immutable replacement, so a Solid surface sees the change. */
  const patch = (id: string, change: (t: UploadTransfer) => UploadTransfer): void => {
    setTransfers((list) => {
      const i = list.findIndex((t) => t.id === id)
      if (i === -1) return list
      const next = [...list]
      next[i] = change(list[i])
      return retain(next)
    })
  }

  function enqueue(files: readonly QueuedUpload[]): string[] {
    if (disposed) return files.map(() => '')
    const ids = files.map(() => `q${++nextRowId}`)
    setTransfers((list) => [
      ...list,
      ...files.map((f, i): UploadTransfer => ({
        id: ids[i],
        // No transfer exists yet, and saying so is the point: every
        // later frame is addressed by an id the backend has not minted.
        transferId: null,
        name: f.name,
        destDir: f.destDir,
        machine: f.machine,
        // The one measurement a waiting file HAS. It is what lets the
        // aggregate bar be about the batch rather than about whichever
        // file happens to be moving.
        size: f.size,
        bytes: null,
        speedBytesPerSecond: null,
        phase: 'queued',
        // Not started, so no start time. A duration needs both ends and
        // this end has not happened.
        startedAt: null,
        endedAt: null,
        finalName: '',
        error: null,
        stranded: [],
        adopted: false,
      })),
    ])
    return ids
  }

  function start(id: string, transferId: string): boolean {
    if (disposed) return false
    const current = currentRow(id)
    if (current === undefined) {
      // The person took this file out of the batch while files.upload was
      // in flight. The transfer exists on the backend and has no row and
      // no owner, so it is stopped here — the alternative is a transfer
      // running on somebody's server that nothing in the product can
      // reach. See `disowned` for why its outcome is not adopted back.
      disowned.add(transferId)
      cancelled.add(id)
      void services.cancel(transferId).catch(() => {})
      return false
    }
    // A row that has already started is not started twice: the second call
    // could only come from a retry the flow does not make, and rewriting
    // the address of a live transfer would orphan its frames.
    if (current.phase !== 'queued') return true
    // ONE ROW PER TRANSFER, not two. A retained `uploadDone` can adopt a
    // transfer (rule 3) that this batch is about to be told the id of, and
    // the two are the same file — so the queued row's knowledge is folded
    // into the one that already has the outcome and the duplicate goes.
    // What it contributes is exactly what an adopted row does not have:
    // where the file was going, onto which machine, and how big it is.
    const adopted = untrack(transfers).find((t) => t.transferId === transferId && t.id !== id)
    if (adopted !== undefined) {
      setTransfers((list) =>
        list
          .filter((t) => t.id !== id)
          .map((t) =>
            t.id === adopted.id
              ? {
                  ...t,
                  name: t.name !== '' ? t.name : current.name,
                  destDir: current.destDir,
                  machine: current.machine,
                  size: t.size ?? current.size,
                  // It is no longer a row this renderer knows nothing
                  // about: it knows where the file was going.
                  adopted: false,
                }
              : t,
          ),
      )
      // Its outcome has already arrived, so there is nothing left to send.
      return false
    }
    patch(id, (t) => ({ ...t, transferId, phase: 'running', startedAt: now() }))
    return true
  }

  function abandon(id: string, reason: string): void {
    patch(id, (t) =>
      // Only a file that never started can be abandoned. Anything else has
      // an account of its own coming, and this must not pre-empt it.
      t.phase === 'queued' ? { ...t, phase: 'skipped', endedAt: now(), error: reason } : t,
    )
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
    patch(current.id, (t) => ({
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
      // Rule 3: adopt a transfer this renderer never saw start — unless it
      // is one this renderer started and then disowned, which is the one
      // case where the absence of a row is not ignorance.
      if (i === -1) {
        if (disowned.has(p.transferId)) return list
        return retain([
          ...list,
          {
            id: `q${++nextRowId}`,
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

  function failLocally(id: string, error: string): void {
    patch(id, (t) =>
      isTerminalPhase(t.phase)
        ? t
        : { ...t, phase: 'failed', endedAt: now(), error, speedBytesPerSecond: null },
    )
  }

  function unsettle(id: string, reason: string): void {
    // The rate stops rather than freezing at its last value: it is
    // arithmetic over samples, and the renderer has stopped taking them.
    // The byte count is left alone — for a `409` the other claimant's
    // progress frames are real and keep arriving, and applyProgress goes
    // on applying them without ever deciding the phase.
    patch(id, (t) =>
      isTerminalPhase(t.phase)
        ? t
        : { ...t, phase: 'unsettled', error: reason, speedBytesPerSecond: null },
    )
  }

  function cancel(id: string): void {
    const current = currentRow(id)
    if (current === undefined) return
    if (current.transferId === null) {
      // Nothing was ever asked of the backend for this file, so there is
      // nothing to tell it. The file leaves the batch and the flow skips
      // it when its turn comes, because by then it has no row.
      cancelled.add(id)
      setTransfers((list) => list.filter((t) => t.id !== id))
      return
    }
    // The INTENT is recorded before the call goes out, and synchronously:
    // the body POST this cancel is about to break may fail before the
    // request resolves, and the flow asks this question at that moment.
    cancelled.add(id)
    // Fire and forget, and quiet: cancelling a transfer that has already
    // finished is not an error. What happened arrives as uploadDone.
    void services.cancel(current.transferId).catch(() => {})
  }

  const unsubProgress = services.subscribeProgress(applyProgress)
  const unsubDone = services.subscribeDone(applyDone)

  function cancelRequested(id: string): boolean {
    return cancelled.has(id)
  }

  function dispose(): void {
    disposed = true
    unsubProgress()
    unsubDone()
    samples.clear()
    cancelled.clear()
    disowned.clear()
    setTransfers([])
  }

  return {
    transfers,
    transfer: find,
    row: findRow,
    enqueue,
    start,
    abandon,
    failLocally,
    unsettle,
    cancel,
    cancelRequested,
    dispose,
  }
}

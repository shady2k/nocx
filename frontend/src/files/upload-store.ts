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
import type { UploadServices } from './upload-client'

/** Where a transfer is. `running` is the only non-terminal value, and the
 *  four terminal ones are the wire's own outcome vocabulary — told to a
 *  person in four different ways, so they are four values and not a
 *  boolean plus an error string. */
export type TransferPhase = 'running' | 'written' | 'skipped' | 'cancelled' | 'failed'

export interface UploadTransfer {
  /** The backend's opaque id — the address of every later frame. */
  transferId: string
  /** The name asked for. `finalName` is what was actually written, which
   *  keepBoth may have changed. */
  name: string
  destDir: string
  /** The declared size, refreshed from any progress frame's `total`. Zero
   *  is legitimate: an empty file is a file. */
  size: number
  /** Bytes confirmed onto the server, or null while nothing has been
   *  observed. NOT zero — zero is a measurement and this is its absence. */
  bytes: number | null
  /** Derived, never on the wire. Null until two samples exist. */
  speedBytesPerSecond: number | null
  phase: TransferPhase
  /** The name actually written; empty for every outcome but `written`. */
  finalName: string
  /** Why it failed. Null on every other outcome — a cancelled transfer's
   *  underlying error is a context cancellation, which is not a fault and
   *  must never be shown to a person as one. */
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
  begin(t: { transferId: string; name: string; destDir: string; size: number }): void
  /**
   * Record a failure only the renderer can know about: the POST that
   * carries the bytes never landed. It is NOT the terminal account — a
   * `409` means somebody else claimed the ticket and the transfer is very
   * much alive — so `files.uploadDone` overrules whatever this wrote.
   */
  failLocally(transferId: string, error: string): void
  /** Ask the backend to cancel. Deliberately changes NO phase: the person's
   *  cancel races the transfer's own completion every time, and what
   *  actually happened arrives as `uploadDone` with outcome `cancelled`
   *  when the cancel landed and `written` when it did not. */
  cancel(transferId: string): void
  /** Forget a row the person has finished reading. */
  dismiss(transferId: string): void
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

function isTerminal(phase: TransferPhase): boolean {
  return phase !== 'running'
}

export function createUploadStore(deps: UploadStoreDeps): UploadStore {
  const { services } = deps
  const now = deps.now ?? (() => Date.now())
  const [transfers, setTransfers] = createSignal<UploadTransfer[]>([])
  /** The last progress sample per transfer, which is the whole of what
   *  deriving a rate needs. Kept beside the records rather than inside
   *  them: it is arithmetic bookkeeping, and a surface that could read it
   *  would start rendering it. */
  const samples = new Map<string, { at: number; bytes: number }>()
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

  /** Replace one record in place; a no-op when there is no row for the id.
   *  Immutable replacement, so a Solid surface sees the change. */
  const patch = (transferId: string, change: (t: UploadTransfer) => UploadTransfer): void => {
    setTransfers((list) => {
      const i = list.findIndex((t) => t.transferId === transferId)
      if (i === -1) return list
      const next = [...list]
      next[i] = change(list[i])
      return next
    })
  }

  function begin(t: { transferId: string; name: string; destDir: string; size: number }): void {
    if (disposed) return
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
          size: t.size,
          bytes: null,
          speedBytesPerSecond: null,
          phase: 'running',
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
    if (current === undefined || isTerminal(current.phase)) return
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
    const phase: TransferPhase = p.outcome
    samples.delete(p.transferId)
    setTransfers((list) => {
      const i = list.findIndex((t) => t.transferId === p.transferId)
      // Rule 3: adopt a transfer this renderer never saw start.
      if (i === -1) {
        return [
          ...list,
          {
            transferId: p.transferId,
            name: p.finalName,
            destDir: '',
            size: 0,
            bytes: null,
            speedBytesPerSecond: null,
            phase,
            finalName: p.finalName,
            error: p.outcome === 'failed' ? (p.error ?? null) : null,
            stranded: p.stranded,
            adopted: true,
          },
        ]
      }
      const next = [...list]
      next[i] = {
        ...list[i],
        phase,
        finalName: p.finalName,
        // Only a failure carries a reason, and it replaces whatever the
        // renderer recorded about its own half of the transfer.
        error: p.outcome === 'failed' ? (p.error ?? null) : null,
        stranded: p.stranded,
        speedBytesPerSecond: null,
      }
      return next
    })
  }

  function failLocally(transferId: string, error: string): void {
    patch(transferId, (t) =>
      isTerminal(t.phase) ? t : { ...t, phase: 'failed', error, speedBytesPerSecond: null },
    )
  }

  function cancel(transferId: string): void {
    // Fire and forget, and quiet: cancelling a transfer that has already
    // finished, or one that never existed, is not an error. What happened
    // arrives as uploadDone either way.
    void services.cancel(transferId).catch(() => {})
  }

  function dismiss(transferId: string): void {
    samples.delete(transferId)
    setTransfers((list) => list.filter((t) => t.transferId !== transferId))
  }

  const unsubProgress = services.subscribeProgress(applyProgress)
  const unsubDone = services.subscribeDone(applyDone)

  function dispose(): void {
    disposed = true
    unsubProgress()
    unsubDone()
    samples.clear()
    setTransfers([])
  }

  return { transfers, transfer: find, begin, failLocally, cancel, dismiss, dispose }
}

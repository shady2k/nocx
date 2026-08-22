// DownloadStore — what the renderer knows about the downloads it started.
//
// ## The one property, unchanged from the other direction
//
// IN-FLIGHT STATE COMES FROM files.download's RESULT AND files.downloadDone,
// AND NEVER FROM HAVING SEEN A PROGRESS NOTIFICATION.
//
// `files.downloadProgress` is an INDICATOR: emitted to the binding's
// session's current subscriber, resolved at emit time, and DROPPED when
// there is none. `files.downloadDone` is the opposite — retained against
// the session and flushed on the next attach — because a transfer is
// bounded by its session and not by the WebSocket that started it. So a
// person can start a 4 GB download, close the laptop, and reattach after it
// finished, receiving one done frame and not a single progress one.
//
// The same three rules follow as in `upload-store.ts`, and each is a test:
//
// 1. `begin` (from the RPC result) is the only thing that STARTS a row, and
//    `applyDone` is the only thing that ENDS one.
// 2. A progress frame for a transfer with no row is ignored, and a progress
//    frame for a row that has ended does not reopen it.
// 3. A done frame for a transfer with no row MINTS one, marked `adopted`.
//
// ## Why this is a second store and not the upload store parameterised
//
// The two hold different records because the two directions ACCOUNT
// differently, and the differences are exactly the fields a shared record
// would have to make optional. An upload's terminal frame carries
// `finalName` (keepBoth may have renamed the file) and `stranded` (it can
// leave a file behind); a download can do neither, because nothing on the
// far host is written. What a download's frame carries instead is `bytes`,
// and on a failure that is the WHOLE account: an upload can be undone and a
// download cannot, so "how much did they actually get" is the only honest
// thing left to say. A `destDir` here would be a lie in the same shape —
// the browser chose where the file went and never told us.
//
// What the two DO share is `OperationPhase`, `isTerminalPhase` and the
// retention bound, all of which live outside both stores already.

import { createSignal, untrack } from 'solid-js'
import type { FilesDownloadDone } from '../generated/files.downloadDone'
import type { FilesDownloadProgress } from '../generated/files.downloadProgress'
import { isTerminalPhase, type OperationPhase } from '../ui/operation'
import type { DownloadServices } from './download-client'
// How many FINISHED transfers this store remembers — the upload store's
// bound, re-used rather than restated. A finished transfer does not vanish
// and does not accumulate either, and two directions remembering for
// different lengths would be a difference nobody chose.
import { FINISHED_TRANSFERS_RETAINED } from './upload-store'

/** One transfer's record. Not exported: a surface reads it off
 *  `transfers()` and never names the type — the operations projection is
 *  the only reader and it maps straight into `Operation`. */
interface DownloadTransfer {
  /** The backend's opaque id — the address of every later frame. */
  transferId: string
  /** The base name the file lands under. The wire repeats it on the done
   *  frame, including on a failure, because a person shown "the download
   *  failed" and no name cannot tell which of two downloads it was. */
  name: string
  /** The path on the far host the bytes came from. The renderer named it,
   *  so it always knows it — except on an adopted row, which is why it can
   *  be empty. */
  sourcePath: string
  /** WHICH MACHINE `sourcePath` is on, as a person names it
   *  (machine-name.ts). '' only where it is genuinely unknown — an adopted
   *  transfer, which never saw the call that started it. Recorded HERE, at
   *  the start, for the reason the upload store records it here: the
   *  operations list is GLOBAL, one list for every tab, so by the time a
   *  row is drawn there is no tab to ask — and this store knows neither
   *  binding nor session, so there is nothing to derive it from later.
   *
   *  For a download the machine is the host the file came FROM, which is
   *  the machine of the binding it was read through. That is the same
   *  string an upload TO that host would carry, and it reaches this store
   *  by the same route: the origin the panel is already following, which
   *  is where `machine-name.ts`'s answer is put so that no call site has
   *  to derive one of its own. */
  machine: string
  /** The size measured on the open handle at mint time, refreshed from any
   *  frame's `total`. Zero is legitimate: an empty file is a file. */
  size: number
  /** Bytes handed to this client's connection, or null while nothing has
   *  been OBSERVED. NOT zero — zero is a measurement and this is its
   *  absence. */
  bytes: number | null
  /** Derived here, never on the wire. Null until two samples exist. */
  speedBytesPerSecond: number | null
  phase: OperationPhase
  /** When `begin` was called, on the store's own clock; null for an adopted
   *  transfer, which this renderer never saw start. The other end of the
   *  duration a finished row reports — a span needs both, and naming only
   *  the end is how a "how long did it take" becomes a moment. */
  startedAt: number | null
  /** When it reached a terminal phase, on the store's own clock; null while
   *  it is live. It orders the finished half of the operations list and
   *  decides which one falls off the end. */
  endedAt: number | null
  /** Why the row says what it says: the wire's reason on `failed`, and what
   *  the renderer's own half hit on `unsettled`. Null on every other phase
   *  — a cancelled transfer's underlying error is a context cancellation,
   *  which must never be shown to a person as a failure. */
  error: string | null
  /** Minted by a retained `downloadDone` alone — this renderer never saw
   *  the transfer start, because the row lived in a page that was reloaded.
   *  Its `sourcePath` is unknown and says so rather than guessing. */
  adopted: boolean
}

export interface DownloadStore {
  /** Every transfer this renderer knows about, oldest first. */
  transfers(): DownloadTransfer[]
  transfer(transferId: string): DownloadTransfer | undefined
  /** Seed a row from files.download's RESULT. This — not a progress frame —
   *  is what says a transfer exists. `machine` is passed rather than derived
   *  because this store is global and has no tab to ask; `startedAt` is
   *  taken from the clock here, because this call IS the start. */
  begin(t: {
    transferId: string
    name: string
    sourcePath: string
    machine: string
    size: number
  }): void
  /**
   * Record a failure only the renderer can know about, and that is the
   * whole story: no bytes are going to be asked for. The one caller is a
   * saver that could not be reached at all — there is no connection to
   * resolve the URL against — so the ticket will expire unredeemed. It is
   * still not the terminal ACCOUNT: files.downloadDone overrules it.
   */
  failLocally(transferId: string, error: string): void
  /**
   * Record that the renderer's half is over and THE OUTCOME IS UNKNOWN.
   * Not a failure and not an ending: files.downloadCancel still reaches the
   * transfer, and files.downloadDone is what will say how it went.
   */
  unsettle(transferId: string, reason: string): void
  /** Ask the backend to cancel. Deliberately changes NO phase: the person's
   *  cancel races the transfer's own completion every time, and what
   *  actually happened arrives as downloadDone with outcome `cancelled`
   *  when the cancel landed and `sent` when it did not. */
  cancel(transferId: string): void
  /** Drop the wire subscriptions. */
  dispose(): void
}

export interface DownloadStoreDeps {
  services: DownloadServices
  /** The clock speed is measured against, injected so a test can move it by
   *  hand — a rate derived from a real duration is a test that depends on
   *  timing, which is broken on a fast machine too. */
  now?: () => number
}

/** How much of a new measurement the reported speed takes. Frames are
 *  COALESCED, so consecutive samples can skip arbitrarily far ahead and the
 *  instantaneous rate between two of them swings wildly; the reported
 *  number is one a person reads, so it moves towards each measurement
 *  rather than snapping to it. The same constant as the upload store's, and
 *  a separate one on purpose: it is a display choice per direction, not a
 *  shared law, and importing it would make changing one change both. */
const SPEED_SMOOTHING = 0.4

export function createDownloadStore(deps: DownloadStoreDeps): DownloadStore {
  const { services } = deps
  const now = deps.now ?? (() => Date.now())
  const [transfers, setTransfers] = createSignal<DownloadTransfer[]>([])
  /** The last progress sample per transfer — the whole of what deriving a
   *  rate needs. Beside the records rather than inside them: it is
   *  arithmetic bookkeeping, and a surface that could read it would start
   *  rendering it. */
  const samples = new Map<string, { at: number; bytes: number }>()
  let disposed = false

  /** The public lookup: a TRACKED read, so a surface that asks for one
   *  transfer inside its JSX re-renders when that transfer moves. */
  const find = (transferId: string): DownloadTransfer | undefined =>
    transfers().find((t) => t.transferId === transferId)

  /** The same lookup for the wire handlers, UNTRACKED. They are event
   *  handlers and not derivations: they read the current value to decide
   *  what to write next, and a tracked read there would be a subscription
   *  nothing owns. */
  const currentOf = (transferId: string): DownloadTransfer | undefined =>
    untrack(transfers).find((t) => t.transferId === transferId)

  /** Drop the oldest FINISHED transfers past the bound, and nothing else.
   *  Oldest by `endedAt`, never by position: the array is in START order
   *  and a transfer that started first does not finish first. A live
   *  transfer is never dropped whatever the count. */
  const retain = (list: DownloadTransfer[]): DownloadTransfer[] => {
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
  const patch = (transferId: string, change: (t: DownloadTransfer) => DownloadTransfer): void => {
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
    sourcePath: string
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
          sourcePath: t.sourcePath,
          machine: t.machine,
          size: t.size,
          bytes: null,
          speedBytesPerSecond: null,
          phase: 'running',
          startedAt,
          endedAt: null,
          error: null,
          adopted: false,
        },
      ]
    })
  }

  function applyProgress(p: FilesDownloadProgress): void {
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

  function applyDone(p: FilesDownloadDone): void {
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
            name: p.name,
            // Unknown, and saying so: this renderer never saw the call that
            // would have carried the path or the machine, and a guess at
            // either reads exactly like a fact. The SIZE is the one thing
            // an adopted download does know — files.downloadDone carries
            // `total` on every outcome, which is the asymmetry with an
            // adopted upload, whose done frame carries no size at all.
            sourcePath: '',
            machine: '',
            size: p.total,
            // The frame's own count, and this is where the two directions
            // differ most: a failed download's byte count is the account
            // of what the far end already has, so it is recorded on every
            // outcome and not only on a success.
            bytes: p.bytes,
            speedBytesPerSecond: null,
            phase,
            startedAt: null,
            endedAt,
            error: p.outcome === 'failed' ? (p.error ?? null) : null,
            adopted: true,
          },
        ])
      }
      const next = [...list]
      next[i] = {
        ...list[i],
        name: p.name,
        phase,
        size: p.total,
        bytes: p.bytes,
        // Set once, by whoever moved the record to a terminal phase — a
        // second downloadDone for the same transfer cannot rewrite when it
        // ended, and the retention order stays stable.
        endedAt: list[i].endedAt ?? endedAt,
        // Only a failure carries a reason, and it replaces whatever the
        // renderer recorded about its own half of the transfer.
        error: p.outcome === 'failed' ? (p.error ?? null) : null,
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
    patch(transferId, (t) =>
      isTerminalPhase(t.phase)
        ? t
        : { ...t, phase: 'unsettled', error: reason, speedBytesPerSecond: null },
    )
  }

  function cancel(transferId: string): void {
    // Fire and forget, and quiet: cancelling a transfer that has already
    // finished, or one that never existed, is not an error. What happened
    // arrives as downloadDone either way.
    void services.cancel(transferId).catch(() => {})
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

  return { transfers, transfer: find, begin, failLocally, unsettle, cancel, dispose }
}

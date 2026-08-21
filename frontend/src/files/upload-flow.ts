// UploadFlow — one file at a time, one question at most, for both gestures.
//
// The two gestures (design §4) differ only in where the destination comes
// from: a drop names the tab's cwd, the panel's action names the folder the
// panel is showing. Everything after that is this module, so the collision
// rule, the source routing and the failure wording cannot drift between
// them — two implementations of one concept agree everywhere you look and
// disagree somewhere you did not.
//
// ## The collision question is asked once
//
// files.upload stats the destination and answers `collision:"exists"` when
// no decision was supplied — creating NOTHING. So the shape is always:
// call, get the collision, ask the person, call again with `onExists`.
// Nothing here stats anything itself; pre-empting the backend's question
// would be a second stat with a second answer.
//
// On a multi-file drop the dialog carries "apply to the N remaining files".
// When the person ticks it, the decision is remembered and every later file
// is called WITH it — so the backend never answers `collision` again and
// the dialog is never mounted again. That is what "asked exactly once"
// means here: not that a second question is suppressed, but that the second
// call cannot produce one.
//
// ## The two sources are a routing decision, not two implementations
//
// A source the renderer holds bytes for (a browser drop, a browser picker)
// is a STREAM upload: files.upload with no sourceTicket, then a POST of the
// body to the url the result carried. A source a human chose on the
// backend's machine is named by a backend-minted TICKET: files.upload
// carrying it, and no body at all. The backend already distinguishes them —
// a request with a sourceTicket is a path upload and one without is a
// stream upload — so this only routes.

import type { CollisionRequest, CollisionResult } from '../ui/collision-dialog'
import type { ToastLevel } from '../ui/toast'
import type { UploadDecision, UploadServices } from './upload-client'
import type { UploadStore } from './upload-store'

/** One file to send. Exactly one of `blob` and `sourceTicket` is set: the
 *  renderer either holds the bytes or holds a ticket naming them, and it
 *  can never hold a path (R2). */
export interface UploadSource {
  /** The base name, for display and as the default destination name. */
  name: string
  size: number
  /** Bytes the renderer holds — a stream upload. */
  blob?: Blob
  /** A backend-minted ticket — a path upload with no body. */
  sourceTicket?: string
}

/** Where the files go. Both gestures resolve one of these and nothing else
 *  distinguishes them afterwards. */
export interface UploadDestination {
  bindingId: string
  /** Absolute, in the provider's syntax. Shown to the person in the
   *  collision question, so it is the destination as they know it. */
  destDir: string
}

/** Ask the person about one collision. The surface supplies this because
 *  mounting a dialog is a surface's job; the flow only needs the answer.
 *  Not exported: a caller passes a function, never names the type. */
type CollisionAsk = (request: CollisionRequest) => Promise<CollisionResult>

/** Tell the person something went wrong. A refusal is an action outcome and
 *  must be visible in the product, never only in a log. */
export type UploadReport = (message: string, level: ToastLevel) => void

export interface UploadFlow {
  /** Send every source to one destination, sequentially — one transfer at
   *  a time per binding (§4). Never rejects: every failure is reported. */
  send(destination: UploadDestination, sources: UploadSource[]): Promise<void>
}

export interface UploadFlowDeps {
  services: UploadServices
  store: UploadStore
  ask: CollisionAsk
  report: UploadReport
}

export function createUploadFlow(deps: UploadFlowDeps): UploadFlow {
  const { services, store, ask, report } = deps

  /** The body half of a stream upload. A failure here is the RENDERER's
   *  half of the transfer, recorded as such: the backend's uploadDone is
   *  still the terminal account and overrules it. The wording distinguishes
   *  the statuses because they mean different things — 409 says another
   *  claimant has the ticket and that transfer is alive, 410 says the
   *  ticket names nothing at all. */
  async function sendBody(transferId: string, url: string, source: UploadSource): Promise<void> {
    const body = source.blob
    if (body === undefined) {
      // The sink is waiting for bytes and the caller has none. It cannot
      // happen through either gesture — a ticket source never gets this
      // branch back — and if it ever does, saying so beats hanging.
      store.failLocally(transferId, 'the sink asked for a body and there is none to send')
      return
    }
    const outcome = await services.sendBody(url, body, source.size)
    if (outcome.ok) return
    const why =
      outcome.kind === 'status'
        ? outcome.status === 409
          ? `${source.name}: the upload was already claimed by another request`
          : outcome.status === 410
            ? `${source.name}: the upload had already ended`
            : `${source.name}: the server refused the body (${outcome.status})`
        : outcome.kind === 'size'
          ? `${source.name}: the file changed size while it was being sent`
          : `${source.name}: ${outcome.message}`
    store.failLocally(transferId, why)
    report(why, 'danger')
  }

  async function sendOne(
    destination: UploadDestination,
    source: UploadSource,
    onExists: UploadDecision | undefined,
  ): Promise<{ kind: 'done' } | { kind: 'collision' } | { kind: 'refused'; message: string }> {
    let result
    try {
      result = await services.upload({
        bindingId: destination.bindingId,
        destDir: destination.destDir,
        name: source.name,
        size: source.size,
        ...(source.sourceTicket !== undefined ? { sourceTicket: source.sourceTicket } : {}),
        ...(onExists !== undefined ? { onExists } : {}),
      })
    } catch (e) {
      return { kind: 'refused', message: e instanceof Error ? e.message : String(e) }
    }
    if ('collision' in result) return { kind: 'collision' }
    // The RESULT is what says the transfer exists — never a progress frame.
    store.begin({
      transferId: result.transferId,
      name: source.name,
      destDir: destination.destDir,
      size: source.size,
    })
    if ('url' in result) await sendBody(result.transferId, result.url, source)
    return { kind: 'done' }
  }

  async function send(destination: UploadDestination, sources: UploadSource[]): Promise<void> {
    /** The apply-to-all decision, once the person has made one. */
    let sticky: UploadDecision | undefined
    for (let i = 0; i < sources.length; i++) {
      const source = sources[i]
      const first = await sendOne(destination, source, sticky)
      if (first.kind === 'refused') {
        // Stop the batch. A refusal from files.upload is almost always
        // about the DESTINATION — a local binding has no uploader (R1), a
        // dead binding has no handle — and carrying on would produce one
        // identical message per remaining file about a place that is not
        // going to start accepting them.
        report(`Could not upload ${source.name}: ${first.message}`, 'danger')
        return
      }
      if (first.kind === 'done') continue

      // A collision, and nobody has been asked yet — if `sticky` were set
      // the call above carried it and the backend could not have answered
      // this branch.
      const answer = await ask({
        name: source.name,
        destination: destination.destDir,
        remaining: sources.length - i,
      })
      if (answer.applyToAll) sticky = answer.answer
      const second = await sendOne(destination, source, answer.answer)
      if (second.kind === 'refused') {
        report(`Could not upload ${source.name}: ${second.message}`, 'danger')
        return
      }
      // A second `collision` is not reachable: the call carried a decision.
      // Nothing is done about it here because there is nothing honest to
      // do — the transfer was not created and the person already answered.
    }
  }

  return { send }
}

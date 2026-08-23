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
// ## The batch is registered before the first byte moves
//
// The send is SEQUENTIAL and stays that way — one transfer at a time per
// binding is design §4 and this module does not make uploads concurrent.
// What changed in nocx-hbdw4.6 is that the waiting half is now DATA: every
// file joins the upload store as a `queued` row before the first
// files.upload call goes out, so a five-file drop is five rows, five in
// the count, and five things somebody can cancel one at a time.
//
// It used to live in the loop variable below and nowhere else, because a
// transfer only became real when files.upload answered with a transferId.
// Drop two files and the second was not in the list, not counted and not
// cancellable until the first had finished (owner, 2026-08-22).
//
// Two consequences the loop now has to carry. A file can leave the batch
// while an earlier one is sending — its row is simply gone, and its turn
// is skipped. And a refusal that stops the batch has to ACCOUNT for the
// files it never attempted, because they are rows on screen and cannot be
// left waiting for a turn that is not coming.
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
  /**
   * WHICH MACHINE `destDir` is on, as a person names it — the string
   * `machine-name.ts` produces, and the same one the tab strip's second
   * line shows for that tab.
   *
   * Required, and supplied by the caller rather than derived here, because
   * this is the LAST place that can know it. The upload store is global and
   * deliberately holds neither binding nor session; the operations list is
   * global too, one list for every tab, so a row that said `/var/www` with
   * three connections open named no machine at all (owner, 2026-08-22). The
   * gesture knows which tab raised it; nothing downstream does.
   *
   * The composition root cannot fill it in either, and that was checked:
   * only the Files panel's binding is opened through the root's wrapper
   * (main.tsx), the terminal drop opens its own inside the pane, and no
   * sessionId → machine lookup exists on PaneManager — the mapping lives in
   * the surfaces, so the surfaces pass it.
   */
  machine: string
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

  /** What one unsuccessful body send means for the row. `settled` is the
   *  question the phase turns on, and it is NOT "did this go wrong" — it
   *  is "does the renderer now know how the transfer ended". */
  interface BodyAccount {
    why: string
    settled: boolean
  }

  /** The body half of a stream upload. A failure here is the RENDERER's
   *  half of the transfer, recorded as such: the backend's uploadDone is
   *  still the terminal account and overrules it.
   *
   *  Two of these results are not failures at all, and calling them one is
   *  the defect this table exists to spell out. A 409 says another
   *  claimant's body is ALREADY RUNNING for this ticket — that transfer is
   *  alive, files.uploadCancel still reaches it, and a row that said
   *  "failed" would announce it dead and take away the only control that
   *  can stop it. A network failure says the request never got an answer,
   *  so the backend may be writing the file successfully this moment. The
   *  rest are the renderer's own half ending for good: the file could not
   *  be read at the declared size, the ticket names nothing, or the sink
   *  read the body and refused it. */
  async function sendBody(id: string, url: string, source: UploadSource): Promise<void> {
    const body = source.blob
    if (body === undefined) {
      // The sink is waiting for bytes and the caller has none. It cannot
      // happen through either gesture — a ticket source never gets this
      // branch back — and if it ever does, saying so beats hanging. No
      // bytes are going to leave this machine, so it is settled.
      store.failLocally(id, 'the sink asked for a body and there is none to send')
      return
    }
    const outcome = await services.sendBody(url, body, source.size)
    if (outcome.ok) return
    // THE PERSON ASKED FOR THIS. A body send that fails after a cancel is
    // the cancel WORKING — the backend tore the sink down under it — and
    // the renderer is the only party that knows the two events are one.
    //
    // Left to the table below, the row said `Cancelled` while a red toast
    // beside it said "the server refused the body (500)": two messages
    // about one event, contradicting each other, and the toast blaming the
    // server for what the person did on purpose (nocx-hbdw4.3).
    //
    // So: no report at all, because intent succeeding is not news, and
    // `unsettled` rather than `failed`, because that is precisely what the
    // renderer now knows — its own half is over and the outcome is the
    // backend's to state. The cancel races the transfer's completion every
    // time, and `files.uploadDone` still says which of `cancelled` and
    // `written` actually happened.
    //
    // The check is AFTER the await and never before it: the button is
    // pressed while the body is in flight, which is the whole case.
    if (store.cancelRequested(id)) {
      store.unsettle(id, `${source.name}: cancelled — waiting for the server to say how it ended`)
      return
    }
    const account: BodyAccount =
      outcome.kind === 'status'
        ? outcome.status === 409
          ? {
              why: `${source.name}: the upload was already claimed by another request — waiting for the server to say how it ended`,
              settled: false,
            }
          : outcome.status === 410
            ? { why: `${source.name}: the upload had already ended`, settled: true }
            : {
                why: `${source.name}: the server refused the body (${outcome.status})`,
                settled: true,
              }
        : outcome.kind === 'size'
          ? { why: `${source.name}: the file changed size while it was being sent`, settled: true }
          : {
              why: `${source.name}: ${outcome.message} — waiting for the server to say how it ended`,
              settled: false,
            }
    if (account.settled) {
      store.failLocally(id, account.why)
      report(account.why, 'danger')
      return
    }
    // A warning and not a danger: nothing has gone wrong that anybody can
    // act on yet, and a person who is told "failed" about a transfer that
    // then succeeds has been lied to twice.
    store.unsettle(id, account.why)
    report(account.why, 'warning')
  }

  async function sendOne(
    destination: UploadDestination,
    source: UploadSource,
    onExists: UploadDecision | undefined,
    /** The row this file already has, from the batch registered before the
     *  first send. Every store call below addresses it, never the wire's
     *  id: the row is what a person is looking at and pressing. */
    id: string,
  ): Promise<
    | { kind: 'done' }
    | { kind: 'collision' }
    | { kind: 'refused'; message: string }
    /** The person took the file out of the batch while this call was in
     *  flight. The store has already stopped the transfer it created; what
     *  is left for the loop is to send no body and move on. */
    | { kind: 'withdrawn' }
  > {
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
    // The row already existed; this is where it stops waiting.
    if (!store.start(id, result.transferId)) return { kind: 'withdrawn' }
    if ('url' in result) await sendBody(id, result.url, source)
    return { kind: 'done' }
  }

  /**
   * What the rows behind a refusal are told.
   *
   * THE FILES AFTER A REFUSAL ARE NOT AN ERROR EACH. The refusal is about
   * the destination and it has already been reported once; saying it again
   * per file would be the noise the early return exists to prevent. But
   * they are rows on screen now, and leaving them `queued` for ever would
   * be the product claiming work is coming that nobody is going to do.
   *
   * So each is closed as `skipped` — the wire's own word for "not written,
   * and that is fine" — carrying why. Neutral, not danger: one thing went
   * wrong and it is already marked on the row above them.
   *
   * They are closed rather than REMOVED, which is the opposite of what a
   * cancel does to a queued row, and the difference is who acted. A person
   * who cancels a file has just decided that and watched the row go; these
   * files were dropped by somebody who asked for them to be sent, and a
   * product that silently discarded four of five would be answering a
   * question nobody asked it.
   */
  function accountForUnattempted(ids: string[], because: string): void {
    for (const id of ids) store.abandon(id, `not attempted: ${because}`)
  }

  async function send(destination: UploadDestination, sources: UploadSource[]): Promise<void> {
    // THE WHOLE BATCH, BEFORE THE FIRST CALL. Every file is on screen, in
    // the count and cancellable from this moment — which is the whole of
    // what a person dropping five files could not see (nocx-hbdw4.6).
    const ids = store.enqueue(
      sources.map((s) => ({
        name: s.name,
        destDir: destination.destDir,
        machine: destination.machine,
        size: s.size,
      })),
    )
    /** The apply-to-all decision, once the person has made one. */
    let sticky: UploadDecision | undefined
    for (let i = 0; i < sources.length; i++) {
      const source = sources[i]
      const id = ids[i]
      // Its row is gone, so the person took this file out of the batch
      // while an earlier one was sending. Cancelling one file is not
      // cancelling the batch: the rest still go.
      if (store.row(id) === undefined) continue
      const first = await sendOne(destination, source, sticky, id)
      if (first.kind === 'refused') {
        // Stop the batch. A refusal from files.upload is almost always
        // about the DESTINATION — a local binding has no uploader (R1), a
        // dead binding has no handle — and carrying on would produce one
        // identical message per remaining file about a place that is not
        // going to start accepting them.
        report(`Could not upload ${source.name}: ${first.message}`, 'danger')
        // This file was asked for and refused, which is a failure of its
        // own and reads as one. The ones behind it were never asked.
        store.failLocally(id, first.message)
        accountForUnattempted(ids.slice(i + 1), `${source.name} was refused`)
        return
      }
      if (first.kind === 'done' || first.kind === 'withdrawn') continue

      // A collision, and nobody has been asked yet — if `sticky` were set
      // the call above carried it and the backend could not have answered
      // this branch.
      const answer = await ask({
        name: source.name,
        destination: destination.destDir,
        // THE FILES STILL IN THE BATCH, this one included — not the ones
        // left in the array. Once a waiting file can be taken out, the two
        // are different numbers, and the dialog offers to apply an answer
        // to files it would be applied to.
        remaining: ids.slice(i).filter((x) => store.row(x) !== undefined).length,
      })
      if (answer.applyToAll) sticky = answer.answer
      // ASKED AGAIN AFTER THE QUESTION, because the question is the one
      // place the batch waits on a person and a person can spend that time
      // taking this very file out of it.
      if (store.row(id) === undefined) continue
      const second = await sendOne(destination, source, answer.answer, id)
      if (second.kind === 'refused') {
        report(`Could not upload ${source.name}: ${second.message}`, 'danger')
        store.failLocally(id, second.message)
        accountForUnattempted(ids.slice(i + 1), `${source.name} was refused`)
        return
      }
      // A second `collision` is not reachable: the call carried a decision.
      // Nothing is done about it here because there is nothing honest to
      // do — the transfer was not created and the person already answered.
    }
  }

  return { send }
}

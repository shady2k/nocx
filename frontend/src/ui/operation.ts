/**
 * The operation vocabulary — what kinds of background work nocx does on
 * somebody's behalf, and where one of them can be.
 *
 * A plain module beside `operation-row.tsx` for the reason `tree-row-kind.ts`
 * is one beside `tree-row.tsx`: the modules that reason about an operation
 * (the upload store, the operations model) must be able to ask these
 * questions without importing a component and pulling Solid's DOM
 * delegation at load.
 *
 * The words are the WIRE's, not a second spelling of them. `written`,
 * `skipped`, `cancelled` and `failed` are files.uploadDone's own outcome
 * enum; `sent` is files.downloadDone's word for the same success, and it is
 * carried rather than folded into `written` for the reason the whole rule
 * exists — the moment one of these is translated on the way in, there are
 * two vocabularies and a table between them to get wrong. `cancelled` and
 * `failed` are spelt identically by both directions and so are ONE member
 * each, not a coincidence to be split. `queued`/`running`/`unsettled` are
 * the three states a transfer can be in before an outcome arrives, and
 * `queued` is the one the wire has no word for, because it names work
 * the backend has not been told about yet (see `OperationPhase` below).
 */

/**
 * What kind of work this is.
 *
 * Two members, and the second cost exactly what the first one's comment
 * predicted: download (nocx-9le.8.3) joined by adding a member here and a
 * glyph in `operation-row.tsx`'s table, and nothing in the indicator, the
 * model or the row switches on kind, so nothing else changed. What is
 * deliberately NOT here is a framework for operations that do not exist —
 * no priorities, no retry policy, and no scheduler. The one queue there is
 * belongs to `files/upload-store.ts` and is not a policy: design §4 sends
 * one file at a time per binding, so a batch has a waiting half whether or
 * not anything models it, and the bead this cost was about it not being
 * modelled.
 */
export type OperationKind = 'upload' | 'download'

/**
 * Where an operation is. Five of the eight values are terminal, and three
 * are not.
 *
 * `unsettled` is the renderer saying it does not know: its own half of the
 * work is over and the answer never came back. It sits on the LIVE side of
 * the split — the backend may be finishing successfully this moment, and
 * cancelling still reaches it — so a surface must not read it as a failure.
 * Collapsing it into `failed` would make the renderer the second author of
 * a terminal account (see `files/upload-store.ts`, which is where that
 * costs something).
 *
 * `queued` is the one word here the wire does not say, and it is the only
 * one it CANNOT say: it names work that exists solely because a person
 * asked for it and that the backend has not been told about yet. A batch
 * sends one file at a time per binding (design §4), so every file after the
 * first is in this state from the moment it is dropped — and before
 * nocx-hbdw4.6 it was in no state at all, because a transfer became data
 * only once `files.upload` had answered with a transferId. Drop five files
 * and four of them lived in a loop variable: not in the list, not in the
 * count, not cancellable (owner, 2026-08-22).
 *
 * It is on the LIVE side of the split too, and that is what makes the
 * badge honest — four files waiting their turn are four things nocx owes
 * somebody.
 */
export type OperationPhase =
  'queued' | 'running' | 'unsettled' | 'written' | 'sent' | 'skipped' | 'cancelled' | 'failed'

/** The values that mean nothing more can happen. Exported because a surface
 *  that labels an outcome needs the CLOSED set of outcomes; a second
 *  spelling of it in a view is how a new phase comes to render as nothing. */
export type TerminalOperationPhase = Exclude<OperationPhase, 'queued' | 'running' | 'unsettled'>

/** The one owner of "is this over?": `queued`, `running` and `unsettled`
 *  all still have an outcome coming. */
export function isTerminalPhase(phase: OperationPhase): phase is TerminalOperationPhase {
  return phase !== 'queued' && phase !== 'running' && phase !== 'unsettled'
}

/**
 * The one owner of "has this started?" — a SECOND cut through the same
 * vocabulary rather than a third branch of the first, because the two
 * answer different questions.
 *
 * `isTerminalPhase` decides whether an operation can still change, which is
 * what the cancel turns on. This one decides whether any byte has moved,
 * which is what the progress bar and the percentage turn on: a queued row
 * must draw neither, since a bar at zero says a transfer is stalled when
 * the truth is that its turn has not come.
 */
export function isWaitingPhase(phase: OperationPhase): phase is 'queued' {
  return phase === 'queued'
}

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
 * enum, and `running`/`unsettled` are the two states a transfer can be in
 * before one of those arrives. Passing them through untranslated is what
 * keeps a mapping layer — and the defects that live in one — from existing
 * at all.
 */

/**
 * What kind of work this is.
 *
 * One member today, and the type exists so the second is an ADDITION.
 * Download (nocx-9le.8) joins by adding a member here and a glyph in
 * `operation-row.tsx`'s table; nothing else in the indicator, the model or
 * the row switches on kind, so nothing else has to change. What is
 * deliberately NOT here is a framework for operations that do not exist —
 * no priorities, no queues, no retry policy.
 */
export type OperationKind = 'upload'

/**
 * Where an operation is. Four of the six values are terminal, and two are
 * not.
 *
 * `unsettled` is the renderer saying it does not know: its own half of the
 * work is over and the answer never came back. It sits on the LIVE side of
 * the split — the backend may be finishing successfully this moment, and
 * cancelling still reaches it — so a surface must not read it as a failure.
 * Collapsing it into `failed` would make the renderer the second author of
 * a terminal account (see `files/upload-store.ts`, which is where that
 * costs something).
 */
export type OperationPhase =
  'running' | 'unsettled' | 'written' | 'skipped' | 'cancelled' | 'failed'

/** The values that mean nothing more can happen. Exported because a surface
 *  that labels an outcome needs the CLOSED set of outcomes; a second
 *  spelling of it in a view is how a new phase comes to render as nothing. */
export type TerminalOperationPhase = Exclude<OperationPhase, 'running' | 'unsettled'>

/** The one owner of "is this over?": `running` and `unsettled` both still
 *  have an outcome coming. */
export function isTerminalPhase(phase: OperationPhase): phase is TerminalOperationPhase {
  return phase !== 'running' && phase !== 'unsettled'
}

/**
 * sandbox-command — the typed `/sandbox` editor command (design §5.1).
 *
 * `/sandbox` is a nocx editor command, not shell syntax. The parser below
 * recognizes only the exact trimmed, case-sensitive string; `/sandbox x`,
 * `/Sandbox`, and embedded occurrences remain ordinary shell input and go to
 * the PTY as typed.
 *
 * Interception produces a CLOSED outcome. The boolean `beforeSubmit` seam can
 * only veto while preserving the draft; it cannot express a successfully
 * consumed internal command. `consumed` clears the draft and suppresses PTY,
 * history, and ledger; `refused` keeps the draft and suppresses them, with the
 * reason already on its way to a toast; `notHandled` falls through to the
 * ordinary submit path.
 */

/** The exact command text. Case-sensitive, no arguments. */
export const SANDBOX_COMMAND = '/sandbox'

export type InternalCommandOutcome =
  { kind: 'notHandled' } | { kind: 'consumed' } | { kind: 'refused'; reason: string }

/** Recognize only the exact trimmed, case-sensitive command. */
export function recognizeSandboxCommand(doc: string): boolean {
  return doc.trim() === SANDBOX_COMMAND
}

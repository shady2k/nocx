/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/checkouts.status.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the checkouts.status JSON-RPC method: the ONE way the product says whether the automatic sweep of nocx-made worker checkouts can actually run, and why not (nocx-xn63t.1.6). It exists for the failure AGENTS.md names: with the content store unavailable the durable checkout record is not wired, the sweep can judge nothing and remove nothing, and the Settings screen would go on offering a period that governs nothing. The degrade is decided once by the composition root before the transport starts and the store never un-opens at runtime, so there is deliberately no statusChanged notification: the renderer reads this when the section that cares renders. The reason set is closed, in the house style of history.status's — the renderer picks its own sentence from the code rather than parsing prose. Deliberately NOT the history.status surface: that one says whether durable command history is running, in its own words, and a checkout failure raised there would put history's sentence on the checkout's notice.
 */
export interface CheckoutsStatus {
  /**
   * True when the checkout sweep can run: the durable record of nocx-made checkouts is wired behind a store that opened. False means no checkout is ever judged and none is ever removed, whatever the worktrees.idleDays setting says.
   */
  available: boolean
  /**
   * Why the sweep cannot run — a closed machine code, so the renderer picks its own sentence rather than parsing prose. Null exactly when available is true. 'noRecord' the durable checkout record is not wired, which is what the content store failing to open is; there is no runtime member, because a store never un-opens and a mid-pass read failure is a per-pass fact the sweep logs, not a state of the process.
   */
  reason: 'noRecord' | null
  /**
   * The underlying failure in the words the backend has for it, for the second line of the notice and for a bug report. Null when available is true, and may be null even when it is false — a reason without a detail is still a complete answer.
   */
  detail: string | null
}

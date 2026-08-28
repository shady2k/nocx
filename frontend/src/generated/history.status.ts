/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/history.status.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the history.status JSON-RPC method, and — byte for byte the same shape — the params of the history.statusChanged notification. It is the ONE way the product says whether durable command history is actually running, and why not. Two different unavailabilities speak through it and must never grow a second vocabulary: the store never opened (no content key, an unusable budget, a failed Open — nocx-rtg0.15), and the store is open but writes are failing or the outbox overflowed at runtime (nocx-rtg0.10). It is deliberately NOT named after startup for that reason. The shape is raise/clear rather than one-shot: available=false opens a degrade episode and available=true closes it, so a notice can be raised once per episode instead of once per lost command, and the interval has a named closing event. Settings reads it so the History section never offers a toggle, a retention age and a two-number budget that govern nothing; a silent degrade the UI contradicts is how a feature that does not exist survives a release (AGENTS.md). It also carries what happens to a session's output while no client is attached, because that consequence follows from the same switches and is otherwise invisible.
 */
export interface HistoryStatus {
  /**
   * True when durable command history is running: the store is open and accepting writes. False means commands are not being kept, whatever the History settings say.
   */
  available: boolean
  /**
   * Why durable history is not running — a closed machine code, so the renderer picks its own sentence rather than parsing prose. Null exactly when available is true. 'noKey' the content key could not be read; 'invalidBudget' the History size settings do not make a usable budget; 'openFailed' the history database could not be opened; 'writeFailed' it opened and is refusing writes, which is the only reason that can end without a restart. A runtime write failure (nocx-rtg0.10) adds its member here rather than inventing a second status.
   */
  reason: 'noKey' | 'invalidBudget' | 'openFailed' | 'writeFailed' | null
  /**
   * The underlying error in the words the backend has for it, for the second line of the notice and for a bug report. Null when available is true, and may be null even when it is false — a reason without a detail is still a complete answer.
   */
  detail: string | null
  /**
   * How many commands the store threw away when it opened, because the file had been written by a different schema — null when nothing was, which is every ordinary start. It is NOT a degrade and rides beside `available` rather than as a reason for it: history IS running, and it is empty because the format changed under it. Saying that with available:false would claim the feature is off; saying it only in a log is the silent degrade this method exists to end, and the one that hurts most — an empty history is indistinguishable from a fresh install. -1 means the old file held nothing this build could count, which is still a discard worth stating.
   */
  discarded: number | null
  /**
   * What happens to a session's output while NO client is attached. The backend's replay ring blocks its writer when it is full and nothing has consumed it (AD-10 — throttle the source, never drop); the acks that free it come from an attached client, so with nobody attached the recorder is the only consumer there is. Without one, a session whose window is closed keeps running only until the ring fills and then stops producing until somebody attaches. It rides beside `available` rather than as a reason for it, exactly as `discarded` does: durable history may be running perfectly and this still be false, because it is governed by the output-retention switch and not by whether the store opened.
   */
  detachedOutput: {
    /**
     * True when a detached session's output is being written to the store, which is also what keeps the session producing while nobody is attached.
     */
    recorded: boolean
    /**
     * Why it is not — a closed machine code, so the renderer picks its own sentence rather than parsing prose. Null exactly when recorded is true. 'historyOff' the keep-history switch is off, so there is no store to record into; 'outputOff' history is kept but command output is not retained. Both are the person's own settings and neither is a fault.
     */
    reason: 'historyOff' | 'outputOff' | null
  }
}

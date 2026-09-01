/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/files.refreshStateChanged.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * The files.refreshStateChanged JSON-RPC notification: a server-initiated binding-level edge that tells the Files panel whether its visible refresh loop is keeping up. It carries no path, queue depth, ladder position, timing, or provider error text — only the user-facing state. Routine polling and designed remote polling remain silent; delayed is emitted only after the visible observation window crosses its threshold, and ok on recovery. The current state is re-emitted on attach so AD-9 reconnects cannot leave a stale warning behind. Nothing existing carries this state: files.changed means "this directory is dirty, re-list it", not "I could not observe it"; files.watch.degradedReason describes provider establishment and explicitly excludes designed remote polling; a files.watch response cannot report a transition minutes after its request; and control.saturated reports a different failure.
 */
export interface FilesRefreshStateChanged {
  /**
   * Which files binding the state belongs to. The panel ignores notifications for other bindings, and the binding's session determines the attached destination.
   */
  bindingId: string
  /**
   * Whether the visible refresh loop is keeping up with eligible observations.
   */
  state: 'ok' | 'delayed'
}

/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/panes.create.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the panes.create JSON-RPC method (nocx-isoph.2, design §5 and §7): the stored pane, and whether this call was a retry of one already made. This file is also the single declaration of the pane shape; panes.move references it cross-file. The pane is the durable identity and its id is minted by the FRONTEND for exactly that reason — it must survive a restart, so it cannot come from a backend instance. The session id is the opposite case and is minted by the backend (AD-7): it is embedded in the remote launcher before the connection exists, and it dies with the backend while the pane does not.
 */
export interface PanesCreateResult {
  pane: Pane
  /**
   * Whether this call found the work already done. A create whose answer was lost is retried — AD-9 exists because the socket drops — and the retry returns the FIRST object rather than minting a second one. true says so out loud, so the renderer can tell 'I made this' from 'this was already made' without comparing rows, and so the property is assertable over the wire instead of inferred from the absence of an error. A repeat asking for something DIFFERENT under the same id is not a replay: it is refused with -32602.
   */
  replayed: boolean
}
export interface Pane {
  /**
   * The pane's id, and the DURABLE IDENTITY of this whole chain (§5): it outlives its shell, its tab and the application, and its blocks are found by it after a restart. Client-minted UUIDv7 and therefore UNTRUSTED INPUT (design .internal/specs/2026-08-16-tabs-panes-and-blocks-design.md §7): the shape is validated and never believed, an insert on an id that already means something else FAILS rather than overwriting, and knowing an id confers NO RIGHT to use it — a UUIDv7 embeds a timestamp and is guessable by construction, so nothing anywhere may treat possession of one as evidence.
   */
  id: string
  /**
   * The tab currently holding this pane — the pane's ONLY edge, because panes do not nest (§5). It is a field of an object the renderer asked for, NOT an address: every backend→renderer message is still addressed by sessionId (§4.4), since a tab holds several panes and 'the tab that spoke' is not well defined.
   */
  tabId: string
  /**
   * Where the pane's shell is, and what a restore reopens in.
   */
  cwd: string
  /**
   * Where the pane's pipe goes, and what decides restore behaviour rather than a dialog (§8): a local pane starts a fresh shell in the same cwd, an ssh pane attempts to reconnect. Deliberately two values and not the four an environment has — 'container' and 'unknown' are honest answers about where a recorded command RAN, and a pane is a thing the user opens.
   */
  kind: 'local' | 'ssh'
  /**
   * The canonical user@host:port an ssh pane applies at; null for a local pane. null rather than an empty string, which is a real value meaning the local machine.
   */
  endpoint: string | null
  /**
   * This pane's share of its tab's extent. Size is a property of the MEMBER, direction a property of the set (§5).
   */
  sizeShare: number
  /**
   * Whether this open pane carries an immutable sandbox grant. The grant is the cause of native enforcement and of the startup sweep that makes the pane non-restorable.
   */
  sandboxGranted: boolean
}

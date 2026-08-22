/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/panes.setCwd.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the panes.setCwd JSON-RPC method (nocx-zkiv4, design §5): the pane after its recorded directory changed. It is the directory a RESTORE reopens the pane in, which is why the column needed a writer after creation at all — it was written once, when the pane was created, and a tab that has been working in a repository all afternoon would otherwise come back where it started. The renderer reports only a VERIFIED cwd (AD-5: an OSC 7 the shell sent, never the provider's session-open fallback), and only for a pane whose shell is local: a path on a far host is not somewhere a local shell can be reopened. Idempotent — the same directory twice answers the same pane.
 */
export interface PanesSetCwdResult {
  pane: Pane
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

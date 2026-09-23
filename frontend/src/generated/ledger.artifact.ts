/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/ledger.artifact.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the ledger.artifact JSON-RPC method (nocx-m3fqk, design §6) — ONE artifact's body, joined from its chunks in seq order. It is a separate method from ledger.get on purpose: the recall read must not haul bytes (ADR-0019 §6), so the page and the detail carry metadata and whoever actually wants a body asks for it one at a time. This is what a restored block's output is drawn from.
 */
export interface LedgerArtifact {
  /**
   * The artifact asked for, echoed back.
   */
  id: string
  /**
   * What the body is: application/vt carries the SGR a restored block draws, text/plain the derived text search and copy read, and application/x-nocx-rows is a streamed block's output — JSON Lines whose line shape ledger.blockRows.schema.json declares, one row of the screen frame's cell vocabulary per line.
   */
  mediaType: string
  /**
   * The chunks joined in seq order. Empty is a real answer and means the command printed nothing — it is NOT the same as an artifact that is not there, which is a body retention has evicted and which this method reports by refusing the id (ADR-0019 §7).
   */
  body: string
  /**
   * Why the body is not the whole of what was printed, or null when it is: 'cap' when the middle was dropped at the per-command limit, 'gap' when a range was lost, 'suppressed' when capture was refused.
   */
  truncated: 'cap' | 'gap' | 'suppressed' | null
  /**
   * The logical content bytes the store holds for this artifact — the retention budget's unit, and what a reader can check its join against.
   */
  byteLen: number
}

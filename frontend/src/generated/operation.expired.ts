/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/operation.expired.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Data payload of a -32005 JSON-RPC error: a call whose declared lifetime ran out. Before any effect the reason is 'not-started' (nothing was changed and the same action may be asked again); after one it is the catalogue entry's own expired outcome — 'outcome-unknown' for a mutation that may have happened, 'retry-read' for a read that changed nothing. The reason is the catalogue's enum, never free text, and it is the only field: a call that did not start carries no scope, no retry hint and nothing from the request. ADR-8eej3e66 stage 8 removed the -32004 capacity refusal this replaces.
 */
export interface OperationExpired {
  /**
   * The catalogue's declared outcome at the deadline.
   */
  reason: 'not-started' | 'outcome-unknown' | 'retry-read'
}

/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/connections.hostMode.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the connections.hostMode JSON-RPC method: the method the named connection's host uses. The single declaration of this shape: the renderer's TypeScript type is generated from it and the Go transport is validated against it.
 */
export interface ConnectionsHostModeResult {
  /**
   * The connection that was asked about, echoed so the renderer keys the answer on the connection the backend resolved rather than its own copy of the request.
   */
  profileId: string
  /**
   * The host's integration method. auto means no answer is recorded for this host's key yet — the connect-time ask will decide (ADR-0033) — and it is also every answer for a destination known_hosts has no key for.
   */
  mode: 'auto' | 'raw' | 'script' | 'helper'
}

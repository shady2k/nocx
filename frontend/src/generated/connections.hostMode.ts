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
 * Result of the connections.hostMode JSON-RPC method: the method the named connection's host uses, and whether this machine has any key for that host at all. The single declaration of this shape: the renderer's TypeScript type is generated from it and the Go transport is validated against it.
 */
export interface ConnectionsHostModeResult {
  /**
   * The connection that was asked about, echoed so the renderer keys the answer on the connection the backend resolved rather than its own copy of the request.
   */
  profileId: string
  /**
   * The host's integration method. auto means no answer is recorded for this host's key yet — the connect-time ask will decide (ADR-0033) — and it is also the answer for a destination known_hosts has no key for, which hostKnown distinguishes.
   */
  mode: 'auto' | 'raw' | 'script' | 'helper'
  /**
   * Whether this machine's known_hosts holds at least one key for the connection's destination, resolved as a dial resolves it. False means the host has never been connected to, so a method cannot be recorded for it: the record is keyed by a host-key fingerprint, and the connection editor shows the control disabled with its effective value (auto) rather than hiding it.
   */
  hostKnown: boolean
}

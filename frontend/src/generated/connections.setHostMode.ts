/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/connections.setHostMode.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the connections.setHostMode JSON-RPC method: the method now in force for the connection's host. A record that could not be persisted is not a result — the call answers an error whose message is the reason the connection editor shows under the field (“Not saved: {reason}”), and nothing about the host changes, so the editor's Save can be retried.
 */
export interface ConnectionsSetHostModeResult {
  /**
   * The connection the write was made for.
   */
  profileId: string
  /**
   * The method in force after the call.
   */
  mode: 'auto' | 'raw' | 'script' | 'helper'
}

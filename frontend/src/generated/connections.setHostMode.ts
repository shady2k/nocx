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
 * Result of the connections.setHostMode JSON-RPC method: the method now in force for the connection's host, and — when the record could not be persisted — the sentence that says so. The method stands for this run either way, because the store keeps a failed write in memory; note is present only when it will not survive a restart.
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
  /**
   * A sentence for the person when the host's record could not be saved: the method applies to this run, and the host will be answered again after a restart. Absent when the record was written.
   */
  note?: string
}

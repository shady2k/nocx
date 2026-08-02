/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/connections.trustHostKey.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the connections.trustHostKey JSON-RPC method: the fingerprint of the key that was appended to known_hosts. The single declaration of this shape: the renderer's TypeScript type is generated from it and the Go transport is validated against it.
 */
export interface TrustHostKeyResult {
  /**
   * SHA256 fingerprint of the host key appended to known_hosts.
   */
  fingerprint: string
}

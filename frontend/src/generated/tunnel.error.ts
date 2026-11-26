/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/tunnel.error.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Data payload of the tunnel.open JSON-RPC error. A bind on a local port somebody else already holds carries the typed cause `address-in-use`, so the renderer opens the same forward again on an allocated port from that cause and never from the message — prose the backend may reword. Every other failure (an unresolvable profile, a refused connection, an invalid destination, a permission error) carries no data at all: a retry on another port cannot help it.
 */
export interface TunnelError {
  /**
   * Fixed cause vocabulary. `address-in-use` is the only value today: the requested local port is held, and port 0 is the remedy the renderer applies exactly once.
   */
  reason: 'address-in-use'
}

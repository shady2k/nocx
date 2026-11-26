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
 * The cause tunnel.open adds itself. A bind on a local port somebody else already holds carries `address-in-use`, so the renderer opens the same forward again on an allocated port from that cause and never from the message — prose the backend may reword. No other tunnel.open failure is typed here: an unresolvable profile, a refused connection, an invalid destination and a permission error add no cause, because a retry on another port cannot help them. A failure that was already owned elsewhere keeps that owner's cause — a sealed vault refuses the profile resolve with `vault-sealed`, for instance — so `data` is not simply absent on every other error.
 */
export interface TunnelError {
  /**
   * Fixed cause vocabulary of this surface. `address-in-use` is the only value tunnel.open adds: the requested local port is held, and port 0 is the remedy the renderer applies exactly once.
   */
  reason: 'address-in-use'
}

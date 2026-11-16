/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/pane.reconnect.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Acknowledges that the supervisor accepted reconnect. Dial and shell adoption continue on pane.connection; this RPC does not wait for them.
 */
export interface PaneReconnect {
  paneId: string
  registered: true
}

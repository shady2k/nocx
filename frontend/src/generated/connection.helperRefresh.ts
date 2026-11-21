/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/connection.helperRefresh.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Acknowledges that the far helper's owner accepted a refresh press. The redial and its hello continue on pane.connection as the axis changes; this RPC does not wait for them and never installs. `refreshed` is true because a refusal is an error, never a result with a false flag.
 */
export interface ConnectionHelperRefreshResult {
  /**
   * The connection whose helper is being redialled. It is the id the pane's observation carried, shared by every pane riding the connection.
   */
  connectionId: string
  /**
   * The request was accepted. It says nothing about the redial's outcome; the axis reports serving, helper-lost or another reason as it happens.
   */
  refreshed: true
}

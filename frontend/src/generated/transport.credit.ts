/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/transport.credit.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the transport.credit JSON-RPC method: the per-connection credit window the renderer must respect (ADR-8eej3e66 section 7). window is how many ordinary calls may be outstanding; exempt methods take no credit; each reserved method has one credit outside the window. A peer that sends more than this is violating the protocol and is disconnected.
 */
export interface TransportCreditResult {
  /**
   * How many ordinary calls this connection may have outstanding at once. Notifications and the methods named in exempt do not count.
   */
  window: number
  /**
   * Methods that take no credit and are never counted against the window: cancel, ack, the resolvers, the heartbeat and diagnostics. Server vocabulary, never request text.
   */
  exempt: string[]
  /**
   * Methods with one credit of their own outside the window, so a window full of work waiting on an unlock cannot hold up the answer that releases them.
   */
  reserved: string[]
}

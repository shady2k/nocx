/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/tunnel.list.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * The coordinator’s forwarding ledger.
 */
export interface TunnelListResult {
  /**
   * Tracked forwards, including those stopped by a transport failure. An empty ledger is an empty array.
   */
  forwards: {
    /**
     * Backend-assigned tunnel id. The renderer echoes it to tunnel.stop; nothing else can stop a forward.
     */
    id: string
    /**
     * Forwarding strategy.
     */
    direction: 'local' | 'remote' | 'dynamic'
    /**
     * The bind the caller asked for. Port 0 means 'allocate' — the usable address is actualBind, never this.
     */
    requestedBind: {
      host: string
      port: number
    }
    /**
     * The address the listener really holds. Never the requested port when the request was 0.
     */
    actualBind: {
      host: string
      port: number
    }
    /**
     * Remote target of the forward, host:port, as dialed over the SSH connection.
     */
    destination: string
    /**
     * Owner label the renderer attached at open.
     */
    scope: string
    /**
     * Success-time bind caution, empty when none applies. Only remote (-R) forwards set it: the requested bind address is not verified — the server may have bound a different address (GatewayPorts) — so a URL built from this forward may only work on the server. Never an error: the forward is running.
     */
    caveat: string
    /**
     * Lifecycle state. A tracked record is running, or stopped by transport loss.
     */
    state: 'starting' | 'running' | 'stopped'
    /**
     * Why the forward stopped. Null while running.
     */
    stopReason: 'user' | 'connection lost' | 'error' | null
    /**
     * The error behind a stopped forward. Null while running or after a clean user stop.
     */
    error: string | null
  }[]
}

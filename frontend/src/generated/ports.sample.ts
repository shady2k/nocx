/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/ports.sample.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * The pane status after asking its provider for a sample.
 */
export interface PortsSampleResult {
  /**
   * The backend pane id this status belongs to.
   */
  paneId: string
  /**
   * Host the discovery runs against, as the session opened it — the machine's hostname for a local pane. Empty before the first connection to the target.
   */
  host: string
  /**
   * The discovery read path: what the remote listens on and why we know (or do not know) it. A successful empty listeners list means nothing is listening; every non-available state means could-not-determine and must never render as 'no ports'.
   */
  discovery: {
    /**
     * Overall outcome of the last sample. pending is the state before the first sample completed; available with an empty listeners list means 'no listeners observed'.
     */
    state: 'available' | 'available-limited' | 'unavailable' | 'failed-transiently' | 'pending'
    /**
     * Remote listening TCP ports from the last successful sample. Always an array, never null: an empty list is the honest 'nothing is listening' and must only appear when that is true.
     */
    listeners: {
      /**
       * IP family of the bind address.
       */
      family: 'ipv4' | 'ipv6'
      /**
       * Bind address as the probe reported it; may be a wildcard.
       */
      address: string
      /**
       * The remote listening port.
       */
      port: number
      /**
       * Three-valued process evidence: known | permission-denied | unsupported. Never absent — 'nobody owns it' and 'I was not allowed to see' are different facts and must render differently.
       */
      process: {
        evidence: 'known' | 'permission-denied' | 'unsupported'
        /**
         * Process name; empty unless evidence is known.
         */
        name: string
        /**
         * Process id; 0 unless evidence is known.
         */
        pid: number
      }
    }[]
    /**
     * Dialect that produced the last sample; empty when none did.
     */
    probe: string
    /**
     * Probes attempted for the last pass, in order. Always an array, never null.
     */
    probesTried: string[]
    /**
     * Human-readable why: refusal detail, truncation, tool absence. Never an uncontrolled dump of remote output.
     */
    classification: string
    /**
     * Bounded excerpt of the probe's stderr, for diagnostics.
     */
    stderr: string
    /**
     * ISO-8601 UTC time of the last successful sample; null before any has completed.
     */
    lastSampleAt: string | null
    /**
     * The user's Pause state: when true, no automatic samples run (manual Retry still does).
     */
    paused: boolean
    /**
     * Whether a watcher (the ports panel) is present and visible. Periodic sampling runs only while true.
     */
    visible: boolean
  }
  /**
   * Every forward the backend currently tracks for the connection, running or stopped-by-transport-loss. Always an array, never null. User-stopped records leave the ledger at stop time — the renderer keeps those on its own side. Always empty for a local pane: there is nothing to forward from the machine you are already on, and the renderer must not offer forwarding actions for it.
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

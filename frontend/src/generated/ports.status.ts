/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/ports.status.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * The latest discovery state for the pane.
 */
export interface PortsStatusResult {
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
}

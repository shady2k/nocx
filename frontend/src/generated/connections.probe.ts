/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/connections.probe.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the connections.test JSON-RPC method. The single declaration of this shape: the renderer's TypeScript type is generated from it and the Go transport is validated against it.
 */
export interface ConnectionTestResult {
  /**
   * Typed probe outcome. host-key-unknown means the host is not in known_hosts at all (routine first contact); host-key-changed means the offered key differs from the stored one (the MITM signature). The renderer must never call a changed key 'unknown' or offer the routine accept button for it.
   */
  outcome:
    | 'accepted'
    | 'rejected'
    | 'unreachable'
    | 'host-key-unknown'
    | 'host-key-changed'
    | 'needs-interactive'
  /**
   * Human-readable detail sentence for the outcome, suitable for display.
   */
  detail?: string
  /**
   * Host-key evidence, present only for the two host-key outcomes. Carries the offered key so the renderer can show its fingerprint and, for a changed key, the stored fingerprint; the renderer echoes host+key back to connections.trustHostKey to accept. A host key is public material (ADR-0011 §3): it may cross the wire and be displayed.
   */
  hostKey?: {
    /**
     * Resolved address as probed (host, or host:port), the exact string known_hosts matching used. The trust call must use this same string or the appended line will not match the next probe.
     */
    host: string
    /**
     * Key algorithm of the offered key, e.g. ssh-ed25519, ecdsa-sha2-nistp256.
     */
    algorithm: string
    /**
     * SHA256 fingerprint of the offered key.
     */
    fingerprint: string
    /**
     * Fingerprint(s) recorded in known_hosts for this host. Present only when outcome is host-key-changed: the user cannot judge a changed key without both fingerprints.
     */
    storedFingerprint?: string
    /**
     * Base64-encoded wire-format public key blob of the offered key. Echoed back verbatim by connections.trustHostKey.
     */
    key: string
  }
}

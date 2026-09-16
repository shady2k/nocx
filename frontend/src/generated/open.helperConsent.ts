/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/open.helperConsent.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * The `open` JSON-RPC error's data field for the connect-time helper ask (ADR-0068, owner's decision 2026-09-16): an auto connection (ADR-0033) whose destination has no consent record (ADR-0034) for its host-key fingerprint is asked before the connect proceeds, instead of silently opening without the helper. This is domain-specific error data carried on a -32603 open failure, not a method result — see contracts/README.md 'Domain-specific error data continues to use its existing schema where one exists'. hostKey is present only when the same dial that needed this ask ALSO discovered the key is unknown or changed, so the renderer raises ONE dialog carrying both questions rather than two in sequence; its shape mirrors connections.probe.schema.json's hostKey field exactly (the two are read by the same renderer code, host-key-dialog.tsx).
 */
export interface OpenHelperConsentData {
  /**
   * The destination as the open path named it.
   */
  host: string
  /**
   * The identity the helper answer is keyed by (ADR-0034): the already-trusted fingerprint when hostKey is absent, or the OFFERED fingerprint — deterministic from the key bytes alone — when hostKey is present and the key itself is not yet trusted. Echoed back verbatim to connections.helperConsent.
   */
  fingerprint: string
  /**
   * Always true on this shape: its presence is what tells the renderer a helper decision is needed, distinct from an ordinary host-key-only open failure (which carries no helperAsk field at all).
   */
  helperAsk: true
  /**
   * Host-key evidence, present only when the key itself also needs a trust decision in the same refusal. Absent means the key is already trusted and only the helper question remains.
   */
  hostKey?: {
    /**
     * Resolved address as dialed (host, or host:port).
     */
    host: string
    /**
     * Backend-issued known_hosts storage identity for this route. The trust call must use this value.
     */
    knownHostsHost: string
    /**
     * True only when this route already has a stored key that differs from the offered key.
     */
    changed: boolean
    /**
     * Key algorithm of the offered key, e.g. ssh-ed25519, ecdsa-sha2-nistp256.
     */
    algorithm: string
    /**
     * SHA256 fingerprint of the offered key.
     */
    fingerprint: string
    /**
     * Fingerprint recorded in known_hosts for this host. Present only when changed is true.
     */
    storedFingerprint?: string
    /**
     * Base64-encoded wire-format public key blob of the offered key. Echoed back verbatim by connections.trustHostKey.
     */
    key: string
  }
}

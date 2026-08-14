/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/shell.footprint.consent.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * The accept of the git panel's consent prompt (remote-helper design D8): the session's machine — keyed by its host public-key fingerprint, resolved server-side from the sessionId the requesting connection owns — has been raised to the relay tier, and the next git.open on that machine proceeds past consentRequired. The write is durable: a grant survives a store reconstruction, and an empty fingerprint never grants.
 */
export interface ShellFootprintConsentResult {
  /**
   * The persisted answer. Exactly one value exists: granted. The denied value has no writer in this deliverable (the resolver honours it so a later writer changes behaviour without touching the decision).
   */
  state: 'granted'
}

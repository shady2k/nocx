/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/connections.helperConsent.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the connections.helperConsent JSON-RPC method: which fingerprint was answered and how, so the renderer's retry of the open is keyed on the identity the backend actually wrote rather than its own copy of the request. The single declaration of this shape: the renderer's TypeScript type is generated from it and the Go transport is validated against it.
 */
export interface ConnectionsHelperConsentResult {
  /**
   * SHA256 fingerprint of the host key the answer was recorded under.
   */
  fingerprint: string
  /**
   * The answer as written (internal/helper/consent.Answer): granted or denied.
   */
  answer: 'granted' | 'denied'
}

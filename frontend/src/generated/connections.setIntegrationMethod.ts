/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/connections.setIntegrationMethod.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the connections.setIntegrationMethod JSON-RPC method: which fingerprint was answered, with which method — so the renderer's retry of the open is keyed on the identity the backend actually wrote rather than its own copy of the request. The single declaration of this shape: the renderer's TypeScript type is generated from it and the Go transport is validated against it.
 */
export interface ConnectionsSetIntegrationMethodResult {
  /**
   * Whether this answer saved permission to deploy the helper. True for a successful helper choice, even if the host method could not be saved; false for raw or script, which do not change consent.
   */
  helperConsentSaved: boolean
  /**
   * Whether the chosen host method was saved. False means the method is retained only for this run; a separately saved helper grant can still be reused after restart.
   */
  hostRecordSaved: boolean
  /**
   * SHA256 fingerprint of the host key the answer was recorded under.
   */
  fingerprint: string
  /**
   * The method as written (raw, script or helper).
   */
  method: 'raw' | 'script' | 'helper'
}

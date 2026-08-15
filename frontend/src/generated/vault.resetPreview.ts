/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/vault.resetPreview.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the vault.resetPreview JSON-RPC method: what resetting the vault would cost, and whether every store can actually be cleared. Changes nothing. Works while the vault is sealed, which is the only state a reset is ever wanted in.
 */
export interface VaultResetPreview {
  /**
   * Distinct stored secrets that will be destroyed. A secret shared by two connections counts once — it is one thing the user loses.
   */
  secretCount: number
  /**
   * Connections holding at least one of those secrets. Connections that store nothing — agent auth, a key read from a path — are not counted, because they lose nothing. Each of these will ask for a password again afterwards.
   */
  profileCount: number
  /**
   * AI endpoints holding at least one of those secrets (ADR-0030). Endpoints that store no credential are not counted, because they lose nothing. Counted separately from profileCount: the endpoint clause is a different sentence answering a different question (ADR-0031).
   */
  endpointCount: number
  /**
   * False when the OS keychain is not answering, so secrets stored there cannot be removed and will remain readable. The user is told this BEFORE confirming, so proceeding is an informed choice rather than a surprise half-way through. True when there is no system keychain on this platform at all: an absent store is not a broken one.
   */
  systemKeychainReachable: boolean
  /**
   * False when there is no vault to reset. The action is still offered — a reset interrupted part-way leaves no vault document and references that still need clearing.
   */
  vaultInitialized: boolean
}

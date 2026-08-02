/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/secrets.savePassword.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the secrets.savePassword JSON-RPC method: a password minted into the vault, addressed by the renderer's row handle. The editor names that handle on the profile's options; the backend resolves it to the stored reference (ADR-0017 §1).
 */
export interface SecretMintResult {
  /**
   * The renderer-addressable row handle of the minted secret (secrow:...). Never a secret reference — the renderer may not hold one (ADR-0011 §2).
   */
  row: string
}

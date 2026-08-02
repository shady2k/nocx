/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/secrets.saveKeyPassphrase.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the secrets.saveKeyPassphrase JSON-RPC method: a key passphrase verified against the stored key and minted into the vault, addressed by the renderer's row handle.
 */
export interface SecretMintResult {
  /**
   * The renderer-addressable row handle of the minted secret (secrow:...). Never a secret reference — the renderer may not hold one (ADR-0011 §2).
   */
  row: string
}

/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/secrets.saveKeyMaterial.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the secrets.saveKeyMaterial JSON-RPC method: a private key minted into the vault, addressed by the renderer's row handle, with the parse results the editor acts on (fingerprint, and whether a passphrase is wanted).
 */
export interface SaveKeyMaterialMintResult {
  /**
   * The renderer-addressable row handle of the minted secret (secrow:...). Never a secret reference — the renderer may not hold one (ADR-0011 §2).
   */
  row: string
  /**
   * SHA256 fingerprint of the stored key. Empty when the key is encrypted in a traditional PEM envelope whose public half is behind the passphrase — unknown-until-unlocked, not absent.
   */
  fingerprint: string
  /**
   * True when the stored key is encrypted and no passphrase for it is stored yet. The renderer must ask for the key's passphrase then and there — a wrong one is refused against the key.
   */
  passphraseWanted: boolean
}

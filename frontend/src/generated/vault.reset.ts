/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/vault.reset.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the vault.reset JSON-RPC method: what was actually destroyed, and what could not be. The vault is uninitialized afterwards whether or not every store answered, so this is a success shape with residue rather than an error.
 */
export interface VaultResetResult {
  /**
   * Distinct stored secrets whose references were cleared.
   */
  secretCount: number
  /**
   * Connections that held at least one of them and will ask for a password again.
   */
  profileCount: number
  /**
   * AI endpoints that held at least one of them and will need a new key (ADR-0030, ADR-0031).
   */
  endpointCount: number
  /**
   * Stores whose material could not be removed — empty when everything was. The renderer must not say 'everything was deleted' while this is non-empty. Always an array, never null: a null where the renderer's type says list has cost this project a defect once already (nocx-25k9.14).
   */
  residue: ResidueEntry[]
}
export interface ResidueEntry {
  /**
   * Provider id of the store that still holds material.
   */
  store: string
  /**
   * Why it could not be cleared. A code, never a sentence — the renderer owns the wording. Absent when the failure carried no reason code.
   */
  reason?:
    'no-service' | 'locked' | 'denied' | 'timeout' | 'unsupported-platform' | 'unknown-provider'
}

/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/vault.replaceSecret.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the vault.replaceSecret JSON-RPC method: the user replaced a secret's value on the Secrets page. The reference did not change — the new material landed under the same SecretID — so every connection using the secret keeps working, and the name and kind are untouched. The renderer reloads the inventory to see the row.
 */
export interface VaultReplaceSecret {}

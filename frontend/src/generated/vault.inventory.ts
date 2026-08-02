/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/vault.inventory.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the vault.inventory JSON-RPC method. One row per stored secret. The name is the secret's own (ADR-0016) — the vault owns it — with a fallback to the derived label or the kind when it did not land. The row id is an opaque renderer-addressable handle, never a secret reference (nocx-jb20.1).
 */
export interface VaultInventory {
  /**
   * Every secret the vault holds, referenced or not. Never null: an empty vault is [].
   */
  entries: InventoryEntry[]
}
export interface InventoryEntry {
  /**
   * Opaque row handle (secrow:...), minted by the backend from the secret's own id. The renderer may hold this; it is not a secret reference, routes nothing, and is the address rename takes.
   */
  id: string
  /**
   * The secret's display name, owned by the vault. Never blank, never the secret reference: falls back to the derived label where an owner exists, and to the kind otherwise.
   */
  name: string
  /**
   * What the material is. The closed vocabulary of the registry (spec §4.1); a new kind is an addition, not a degradation into 'unknown'.
   */
  kind: 'password' | 'key-passphrase' | 'private-key' | 'public-key' | 'otp-seed'
  /**
   * Provider tag from the secret reference: sec:v1:<provider>:<32 hex>.
   */
  provider: string
  /**
   * Credential that references this secret, or empty for a secret no connection uses — which ADR-0016 exists to make possible.
   */
  ownerId: string
  /**
   * How many connections reference the owning credential. 0 for an unowned secret.
   */
  usedBy: number
  /**
   * Whether the store that holds this secret answered its last probe.
   */
  reachable: boolean
}

/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/vault.status.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the vault.status JSON-RPC method. The single declaration of this shape: the renderer's TypeScript type is generated from it and the Go transport is validated against it.
 */
export interface VaultStatus {
  /**
   * Whether the vault has been set up, and whether it is open.
   */
  state: 'uninitialized' | 'sealed' | 'unsealed'
  /**
   * STATE: this vault holds an OS-held key, so it can be unsealed by one. False until setup has stored one. Ask this before offering 'unlock with the OS key'.
   */
  osKeyAvailable: boolean
  /**
   * CAPABILITY: this machine has a system keyring that is ready and writable, so setup can mint an OS-held key with no passphrase. One word apart from osKeyAvailable and a different question — ask this before deciding whether setup must prompt.
   */
  osKeyCapable: boolean
  /**
   * STATE: the vault holds a passphrase envelope. False on OS-key-only vaults, where changing the passphrase or reissuing the recovery code is refused.
   */
  hasPassphrase: boolean
  /**
   * Idle timeout before the vault locks itself. 0 means never.
   */
  autoSealMinutes: number
  /**
   * The store new secrets are written to. Null on an uninitialized vault, which has not chosen one yet — null rather than "" so the renderer can tell 'not chosen' from 'a store id I do not recognise'. Chosen once by setup from what was ready then, and never changed by the machine afterwards.
   */
  defaultProvider: string | null
  /**
   * Every registered store and its health.
   */
  providers: ProviderStatus[]
}
export interface ProviderStatus {
  /**
   * Provider id as it appears in a secret reference: sec:v1:<provider>:<32 hex>.
   */
  id: string
  /**
   * Whether this store can accept new secrets at all.
   */
  writable: boolean
  /**
   * Whether the store answered its last probe.
   */
  ready: boolean
  /**
   * Why the store is not ready. Absent when it is. A code, never a sentence — the renderer owns the wording.
   */
  reason?:
    'no-service' | 'locked' | 'denied' | 'timeout' | 'unsupported-platform' | 'unknown-provider'
}

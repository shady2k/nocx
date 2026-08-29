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
   * STATE: the vault holds a passphrase envelope. True of every initialized vault since ADR-0050 step 1 removed the mode that had none, so what this now distinguishes is an initialized vault from one that has never been set up.
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
   * Why the store is not ready. Absent when it is. A code, never a sentence — the renderer owns the wording. "excluded" is the one that is not a claim about the machine: this build declared the OS keystore out of play and never asked it anything, because asking is a keychain write and on a host with no keychain that write is a modal nobody can dismiss (design D10).
   */
  reason?:
    | 'no-service'
    | 'locked'
    | 'denied'
    | 'timeout'
    | 'unsupported-platform'
    | 'unknown-provider'
    | 'excluded'
}

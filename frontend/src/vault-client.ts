// Vault RPC client — typed methods for the vault.* control-plane methods.
// Sibling of ProfileClient over the same Dispatcher.

import type { Dispatcher } from './dispatcher'

// The wire types are GENERATED from the contracts (npm run contracts). They
// are re-exported here so callers keep importing them from the client, and so
// this module stays the one place that says what vault.* speaks.
//
// They used to be hand-written, and that is precisely how `defaultProvider`
// came to be declared here, read on every render, and never sent: a
// hand-written type can want a field the wire does not carry. A generated one
// cannot. Do not re-declare these — change the schema.
export type { VaultStatus, ProviderStatus } from './generated/vault.status'
export type { VaultResetPreview } from './generated/vault.resetPreview'
export type { VaultResetResult, ResidueEntry } from './generated/vault.reset'
export type { VaultInventory, InventoryEntry } from './generated/vault.inventory'
import type { VaultStatus } from './generated/vault.status'
import type { VaultResetPreview } from './generated/vault.resetPreview'
import type { VaultResetResult } from './generated/vault.reset'
import type { VaultInventory, InventoryEntry } from './generated/vault.inventory'

/** The vault's lifecycle state, as the schema's enum spells it. */
export type VaultState = VaultStatus['state']

export interface VaultSetupParams {
  passphrase?: string
}

export interface VaultSetupResult {
  recoveryCode?: string
}

export interface VaultChangePassphraseParams {
  oldPassphrase?: string
  recoveryCode?: string
  newPassphrase: string
}

export interface VaultRegenerateRecoveryParams {
  passphrase: string
}

export interface VaultRegenerateRecoveryResult {
  recoveryCode: string
}

export interface VaultSetDefaultProviderParams {
  provider: string
}

export interface VaultUnsealParams {
  means: 'os' | 'passphrase' | 'recovery'
  secret?: string
}

export interface VaultCreateSecretParams {
  /** The name the user was asked for on the Secrets page. Required. */
  name: string
  /** What the material is: password | key-passphrase | ... */
  kind: InventoryEntry['kind']
  /** The value to store. Goes to the default store, never back out. Either
   *  this or `path` is sent; never both. */
  value?: string
  /** A path the backend dereferences to the file's contents (private keys
   *  in Path mode). What is stored is the key, never a filename (dcf566b). */
  path?: string
}

export interface VaultRenameSecretParams {
  /** The row handle the inventory entry carried — never a SecretID. */
  id: string
  name: string
}

export interface VaultReplaceSecretParams {
  /** The row handle the inventory entry carried — never a SecretID. */
  id: string
  /** The replacement material. Either this or `path` is sent; never both. */
  value?: string
  /** A path the backend dereferences to the file's contents (private keys).
   *  Never stored as a path — the stored material is the key. */
  path?: string
}

export interface VaultDeleteSecretParams {
  /** The row handle the inventory entry carried — never a SecretID. */
  id: string
}
export class VaultClient {
  /** The shared control-plane dispatcher. Public so the vault state module
   *  can install the sealed-access hook (the vault owns the unlock prompt). */
  constructor(readonly dispatcher: Dispatcher) {}

  status(): Promise<VaultStatus> {
    return this.dispatcher.call('vault.status', {})
  }

  setup(params: VaultSetupParams): Promise<VaultSetupResult> {
    return this.dispatcher.call('vault.setup', params)
  }

  unseal(params: VaultUnsealParams): Promise<Record<string, never>> {
    return this.dispatcher.call('vault.unseal', params)
  }

  seal(): Promise<Record<string, never>> {
    return this.dispatcher.call('vault.seal', {})
  }

  changePassphrase(params: VaultChangePassphraseParams): Promise<Record<string, never>> {
    return this.dispatcher.call('vault.changePassphrase', params)
  }

  regenerateRecovery(
    params: VaultRegenerateRecoveryParams,
  ): Promise<VaultRegenerateRecoveryResult> {
    return this.dispatcher.call('vault.regenerateRecovery', params)
  }

  setDefaultProvider(params: VaultSetDefaultProviderParams): Promise<Record<string, never>> {
    return this.dispatcher.call('vault.setDefaultProvider', params)
  }

  setAutoSeal(minutes: number): Promise<Record<string, never>> {
    return this.dispatcher.call('vault.setAutoSeal', { minutes })
  }

  /** What resetting the vault would cost, and whether every store can be
   *  cleared. Changes nothing. Works while sealed — the only state a reset is
   *  ever wanted in. */
  resetPreview(): Promise<VaultResetPreview> {
    return this.dispatcher.call('vault.resetPreview', {})
  }

  /** Destroy everything the vault holds and return it to uninitialized.
   *  Irreversible. Takes no parameters: what is destroyed is decided by what
   *  is stored, never by the caller. */
  reset(): Promise<VaultResetResult> {
    return this.dispatcher.call('vault.reset', {})
  }

  inventory(): Promise<VaultInventory> {
    return this.dispatcher.call('vault.inventory', {})
  }

  /** Store a secret created on the Secrets page: name and kind were asked of
   *  the user, the value goes to the default store and never comes back. */
  createSecret(params: VaultCreateSecretParams): Promise<Record<string, never>> {
    return this.dispatcher.call('vault.createSecret', params)
  }

  /** Rename a secret, addressed by its inventory row handle — never by a
   *  secret reference (the renderer may not name one, nocx-jb20.1). */
  renameSecret(params: VaultRenameSecretParams): Promise<Record<string, never>> {
    return this.dispatcher.call('vault.renameSecret', params)
  }

  /** Replace a secret's value, addressed by its inventory row handle — never
   *  by a secret reference (the renderer may not name one, nocx-jb20.1). The
   *  reference does not change: every connection using the secret keeps
   *  working. The old value is never shown back (ADR-0011 §2). */
  replaceSecret(params: VaultReplaceSecretParams): Promise<Record<string, never>> {
    return this.dispatcher.call('vault.replaceSecret', params)
  }

  /** Delete a secret and its stored material, addressed by its inventory row
   *  handle — never by a secret reference (the renderer may not name one,
   *  nocx-jb20.1). Metadata first, stored secret second (ADR-0011 §4): any
   *  connection that used the secret is told before this is called, and the
   *  profile references are cleared with the delete. */
  deleteSecret(params: VaultDeleteSecretParams): Promise<Record<string, never>> {
    return this.dispatcher.call('vault.deleteSecret', params)
  }
  activity(): Promise<Record<string, never>> {
    return this.dispatcher.call('vault.activity', {})
  }
}

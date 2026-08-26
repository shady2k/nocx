// Vault RPC client — typed methods for the vault.* control-plane methods.
// Sibling of ProfileClient over the same Dispatcher.

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
export type { VaultResolveLine, ResolveRef } from './generated/vault.resolveLine'
export type { SecretsDetect } from './generated/secrets.detect'
export type { SecretsCaptureSave } from './generated/secrets.captureSave'
export type { SecretsCaptureDismiss } from './generated/secrets.captureDismiss'
import type { VaultStatus } from './generated/vault.status'
import type { VaultResetPreview } from './generated/vault.resetPreview'
import type { VaultResetResult } from './generated/vault.reset'
import type { VaultInventory, InventoryEntry } from './generated/vault.inventory'
import type { VaultResolveLine } from './generated/vault.resolveLine'
import type { SecretsDetect } from './generated/secrets.detect'
import type { SecretsCaptureSave } from './generated/secrets.captureSave'
import type { SecretsCaptureDismiss } from './generated/secrets.captureDismiss'

/** The vault's lifecycle state, as the schema's enum spells it. */
export type VaultState = VaultStatus['state']

/** The vault's closed kind vocabulary, as the schema's enum spells it — the
 *  same set internal/vault/meta.go owns. One alias, because a second name for
 *  it in a surface is a second list waiting to drift. */
export type VaultSecretKind = InventoryEntry['kind']

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
  /** Ask for atomic name-collision resolution — the same path the capture
   *  save takes — and for the name ACTUALLY used in the response. The
   *  prompt's ⌘S save needs it: the {{secret:NAME}} reference is built
   *  from the vault's answer, never from the name that was sent. */
  resolve?: boolean
}

export interface VaultCreateSecretResult {
  /** The vault inventory name the secret was stored under — the resolved
   *  name when resolve was asked, the requested name otherwise. */
  name: string
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

/**
 * The RPC seam VaultClient speaks over — the full Dispatcher in the app, or
 * any caller that can route control-plane methods (WSClient's `call`, which
 * wraps the same dispatcher). The optional sealed hook is the ONE seam where
 * a sealed vault raises the unlock prompt; the vault layer installs it, and a
 * caller without a real dispatcher (a test double) simply keeps the
 * caller-side behavior.
 */
export interface VaultRpc {
  call<T = unknown>(method: string, params: unknown): Promise<T>
  onVaultSealed?: (method: string) => Promise<void>
}

export class VaultClient {
  /** The shared control-plane dispatcher. Public so the vault state module
   *  can install the sealed-access hook (the vault owns the unlock prompt). */
  constructor(readonly dispatcher: VaultRpc) {}

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
   *  the user, the value goes to the default store and never comes back.
   *  With resolve, the vault resolves name collisions atomically and the
   *  response carries the name ACTUALLY used. */
  createSecret(params: VaultCreateSecretParams): Promise<VaultCreateSecretResult> {
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

  /** Resolve every {{secret:NAME}} reference in a command line to its live
   *  value — the line to write to the PTY, and only that. The result's `line`
   *  may carry secret values and must never be persisted; history.record
   *  receives the line with the reference INTACT. */
  resolveLine(line: string): Promise<VaultResolveLine> {
    return this.dispatcher.call('vault.resolveLine', { line })
  }

  /** The ONE detector, over the wire: findings for a line, with UTF-16
   *  code-unit offsets, echoing the revision they were computed for. The
   *  caller drops a response whose revision no longer matches the current
   *  document — never adjusting an old range onto a newer one. */
  detect(line: string, revision: number): Promise<SecretsDetect> {
    return this.dispatcher.call('secrets.detect', { line, revision })
  }

  /** Settle a pending capture into the vault: create the secret (the vault
   *  resolves name collisions atomically and the real name comes back) and
   *  rewrite the linked history rows to the reference. Idempotent: retrying
   *  with the same capture id returns the recorded outcome. */
  captureSave(params: { captureId: string; name?: string }): Promise<SecretsCaptureSave> {
    return this.dispatcher.call('secrets.captureSave', params)
  }

  /** Destroy a pending capture and suppress its value for the rest of the
   *  application session. Idempotent. */
  captureDismiss(captureId: string): Promise<SecretsCaptureDismiss> {
    return this.dispatcher.call('secrets.captureDismiss', { captureId })
  }

  activity(): Promise<Record<string, never>> {
    return this.dispatcher.call('vault.activity', {})
  }

  /** Report the outcome of a backend-initiated unlock request.
   *  Sent after the user unlocks or cancels the dialog shown for a
   *  vault.unlockRequest notification. */
  unlockResolved(params: {
    requestId: string
    outcome: 'unsealed' | 'cancelled'
  }): Promise<Record<string, never>> {
    return this.dispatcher.call('vault.unlockResolved', params)
  }
}

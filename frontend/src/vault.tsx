// Vault dialogs and controller — setup (passphrase + recovery code),
// unlock (os / passphrase / recovery), and the silent-setup path.
//
// SetupDialog appears when the user saves a password and the vault is
// uninitialized with no OS-held key. It collects a master passphrase
// (with confirmation) then shows the recovery code exactly once.
//
// UnlockDialog appears when the vault is sealed, offering the available
// means. On a machine with an OS-held key, unlocking is a single click
// with no prompt.
//
// Surfaces import createVaultState for reactive state + the two dialogs,
// calling ensureBeforeSave to intercept the password-save flow.

import {
  createSignal,
  createEffect,
  onMount,
  Show,
  For,
  type Component,
  type Accessor,
} from 'solid-js'
import { Dialog } from './ui/dialog'
import { Prompt } from './ui/prompt'
import { Button } from './ui/button'
import { Stack } from './ui/stack'
import { TextField } from './ui/text-field'
import { CodeBlock } from './ui/code-block'
import { IconButton } from './ui/icon-button'
import { PageSection, Select, Field, StatusCard, StatusDot, Badge, Checkbox } from './ui'
import type { StatusCardTone, BadgeTone } from './ui'
import { CopyIcon, LockIcon, LockOpenIcon } from './ui/icons'
import { showToast } from './ui/toast'
import { RpcError } from './dispatcher'
import type {
  VaultClient,
  VaultStatus,
  ProviderStatus,
  VaultResetPreview,
  ResidueEntry,
} from './vault-client'

/**
 * Thrown when the user cancels the vault prompt that was deferring a save.
 *
 * saveSecretWithVault rejects the caller's promise with this when its dialog
 * is cancelled before the deferred save ran. A caller that treated cancel as
 * success continued as if the save had happened — the profile was created
 * while the secret was never stored, a two-step save silently halved.
 */
export class VaultOperationCancelledError extends Error {
  constructor() {
    super('Vault operation cancelled')
    this.name = 'VaultOperationCancelledError'
  }
}

const REASON_MESSAGES: Record<string, string> = {
  'no-service': 'No system keyring available. Use a passphrase to unlock.',
  locked: 'Your login keychain is locked. Unlock it and try again.',
  denied: 'Access to the system keyring was denied.',
  timeout: 'The operation timed out. Please try again.',
  'unsupported-platform': 'System keyring is not supported on this platform.',
  'unknown-provider': 'This secret reference names a provider not available in this build.',
  // Without this entry the reason fell through to the backend's own words and
  // the user was shown "unseal failed" — an internal phrase, in lower case,
  // naming an operation no interface mentions. Every reason the transport can
  // send needs a line here; that is what makes this table the single owner of
  // user-facing wording rather than a partial one.
  'unseal-failed': 'That did not unlock the vault. Check what you entered and try again.',
  'vault-changed': 'The vault changed while this was open. Try again.',
  'vault-sealed': 'The vault is locked.',
  'vault-uninitialized': 'Protection has not been set up yet.',
}

function vaultErrorMessage(err: unknown): string {
  if (err instanceof RpcError && err.data && typeof err.data === 'object') {
    const d = err.data as { reason?: string }
    if (d.reason && REASON_MESSAGES[d.reason]) {
      return REASON_MESSAGES[d.reason]
    }
  }
  if (err instanceof Error) return err.message
  return 'Operation failed'
}

// ── Store display helpers ───────────────────────────────────────────────
// Maps provider IDs to user-facing store names, and constructs state
// sentences from provider status using REASON_MESSAGES as the single owner
// of user-facing wording.

const STORE_NAMES: Record<string, string> = {
  system: 'System keychain',
  file: 'Encrypted nocx storage',
}

function storeLabelName(id: string): string {
  return STORE_NAMES[id] ?? id
}

/** The fallback sentence shown when a store is not answering and its reason
 *  code is not in REASON_MESSAGES. */
const UNKNOWN_REASON_SENTENCE = 'Not answering: check the store and try again.'

function storeStateSentence(p: ProviderStatus): string {
  if (p.ready) return `${storeLabelName(p.id)} is available and answering.`
  const msg = p.reason
    ? (REASON_MESSAGES[p.reason] ?? UNKNOWN_REASON_SENTENCE)
    : UNKNOWN_REASON_SENTENCE
  return `Not answering: ${msg}`
}

/** A store's health, as the list row shows it: a tone for the dot and a name
 *  for the dot's meaning, since a coloured circle says nothing out loud. */
interface StoreRowStatus {
  tone: 'ok' | 'warning' | 'error'
  accessibleName: string
}

function storeRowStatus(p: ProviderStatus): StoreRowStatus {
  if (!p.ready) {
    const label = p.reason ? (REASON_MESSAGES[p.reason] ?? 'Not available') : 'Not available'
    return { tone: 'error', accessibleName: label }
  }
  return {
    tone: p.writable ? 'ok' : 'warning',
    accessibleName: p.writable ? 'Available' : 'Read-only',
  }
}

// ── Vault controller (reactive state + methods for surfaces) ────────────

export interface VaultController {
  /** Latest vault status from the backend, or null before the first fetch. */
  status: Accessor<VaultStatus | null>
  /** True when the setup dialog should be shown. */
  showSetup: Accessor<boolean>
  /** True when the unlock dialog should be shown. */
  showUnlock: Accessor<boolean>
  /**
   * The operation that triggered the unlock prompt, or null. Every password
   * prompt must say WHICH password it wants and why it is asking now
   * (nocx-s8jn): "Unlock the vault" alone cannot be told apart from the key
   * and connection prompts. Set by openUnlock/ensureBeforeSave/
   * saveSecretWithVault, cleared by closeUnlock.
   */
  unlockReason: Accessor<string | null>
  refresh(): Promise<boolean>
  /** Preflight-based vault check — see saveSecretWithVault for the operation-first replacement. */
  ensureBeforeSave(doSave: () => Promise<void>, reason?: string): void
  /** Call when the setup dialog completes so the deferred save can run. */
  onSetupDone(): void
  /** Call when the unlock dialog completes so the deferred save can run. */
  onUnsealDone(): void
  /** Show the unlock dialog (e.g. after a sealed-on-connect error). The
   *  reason names the operation that needs the vault open. */
  openUnlock(reason?: string): void
  /** Show the setup dialog, for a surface offering to set protection up. */
  openSetup(): void
  closeSetup(): void
  closeUnlock(): void
  /**
   * Runs `saveFn` and catches vault errors with dialog + retry. The reason
   * names the operation for the unlock prompt, when a dialog is shown.
   */
  saveSecretWithVault(saveFn: () => Promise<void>, reason?: string): Promise<void>
  /** Seal the vault immediately. */
  seal(): Promise<void>
  /** Change the master passphrase using old passphrase or recovery code. */
  changePassphrase(params: {
    oldPassphrase?: string
    recoveryCode?: string
    newPassphrase: string
  }): Promise<void>
  /** Regenerate the recovery code. Shows once. */
  regenerateRecovery(params: { passphrase: string }): Promise<{ recoveryCode: string }>
  /** Set the default writable provider. */
  setDefaultProvider(params: { provider: string }): Promise<void>
}

/** Create the vault reactive state for a surface. */
export function createVaultState(vaultClient: VaultClient): VaultController {
  const [status_, setStatus] = createSignal<VaultStatus | null>(null)
  const [showSetup, setShowSetup] = createSignal(false)
  const [showUnlock, setShowUnlock] = createSignal(false)
  const [unlockReason, setUnlockReason] = createSignal<string | null>(null)

  // Pending save callback — set when we defer a save to show a dialog
  let pendingSave: (() => Promise<void>) | null = null
  // Promise controls for saveSecretWithVault — resolve/reject the caller's promise
  // when the deferred save runs or the dialog is cancelled.
  let pendingResolve: ((value: undefined) => void) | null = null
  let pendingReject: ((reason: unknown) => void) | null = null

  // The vault layer owns the unlock prompt: ANY control-plane RPC that lands
  // on a sealed vault defers through this promise — no call site wraps its
  // own vault calls (the dispatcher installs this seam). Concurrent sealed
  // calls coalesce on ONE dialog and ONE promise; onUnsealDone resolves it
  // (each RPC retries exactly once in the dispatcher), cancelling rejects it
  // with VaultOperationCancelledError so the caller abandons its operation.
  let sealedAccessResolve: (() => void) | null = null
  let sealedAccessReject: ((e: unknown) => void) | null = null
  let sealedUnlock: Promise<void> | null = null

  // Test doubles may lack the real dispatcher; the seam is the real wiring's
  // job, and a double without it simply keeps the caller-side behavior.
  if (vaultClient.dispatcher) {
    vaultClient.dispatcher.onVaultSealed = () => {
      if (!sealedUnlock) {
        sealedUnlock = new Promise<void>((resolve, reject) => {
          sealedAccessResolve = resolve
          sealedAccessReject = reject
          setUnlockReason('The vault is locked. Unlock it to continue.')
          setShowUnlock(true)
        }).finally(() => {
          sealedUnlock = null
        })
      }
      return sealedUnlock
    }
  }

  async function refresh(): Promise<boolean> {
    try {
      const s = await vaultClient.status()
      setStatus(s)
      return true
    } catch {
      return false
    }
  }

  function ensureBeforeSave(doSave: () => Promise<void>, reason?: string): void {
    const s = status_()
    if (!s) {
      void refresh().then((ok) => {
        if (ok) {
          ensureBeforeSave(doSave)
        } else {
          showToast({
            level: 'danger',
            message: 'Could not check vault status. Password was not saved.',
          })
        }
      })
      return
    }

    if (s.state === 'unsealed') {
      void doSave()
      return
    }

    if (s.state === 'uninitialized') {
      if (s.osKeyCapable) {
        void vaultClient
          .setup({})
          .then(() => doSave())
          .catch((e: unknown) => {
            showToast({
              level: 'danger',
              message: vaultErrorMessage(e),
            })
          })
        return
      }
      pendingSave = () => doSave()
      setShowSetup(true)
      return
    }

    // sealed
    pendingSave = () => doSave()
    setUnlockReason(reason ?? null)
    setShowUnlock(true)
  }

  // True while a deferred operation started by onSetupDone/onUnsealDone is
  // still running.
  //
  // The dialogs call "done" and then "close" in that order, and close used to
  // resolve the caller's promise and drop the reject handle — while the retry
  // it had just launched was still in flight. So a retry that failed reported
  // success and its error went nowhere: no toast, no log, and a dialog that sat
  // there looking inert. That is how a Tabby import stopped after vault setup
  // with nothing on screen to say why. The retry settles its own promise; close
  // must not settle it from underneath.
  let retryInFlight = false

  function settleAfterRetry(): void {
    retryInFlight = false
  }

  function onSetupDone(): void {
    const save = pendingSave
    pendingSave = null
    if (!save) return
    retryInFlight = true
    void save().finally(settleAfterRetry)
  }

  function onUnsealDone(): void {
    const save = pendingSave
    pendingSave = null
    void refresh()
    // Resume any RPCs that deferred on a sealed vault: each retries exactly
    // once inside the dispatcher.
    if (sealedAccessResolve) {
      sealedAccessResolve()
      sealedAccessResolve = null
      sealedAccessReject = null
    }
    if (!save) return
    retryInFlight = true
    void save().finally(settleAfterRetry)
  }

  function openUnlock(reason?: string): void {
    pendingSave = null
    setUnlockReason(reason ?? null)
    setShowUnlock(true)
  }

  /** Show the setup dialog with no save waiting behind it. The counterpart of
   *  openUnlock, for a surface that has nothing to show until protection
   *  exists and should offer the remedy rather than name it. */
  function openSetup(): void {
    pendingSave = null
    setShowSetup(true)
  }
  /** Closing a dialog cancels a pending operation — unless one is already
   *  running, in which case that operation owns the promise.
   *
   *  When the cancel interrupts a DEFERRED save (pendingSave set, save not
   *  yet run), the caller's promise is rejected with VaultOperationCancelled
   *  so the caller can abandon the operation it started. Resolving there was
   *  how a cancelled unlock left the profile created with the secret never
   *  stored: the caller continued as if the save had happened. */
  function closeDialog(hide: () => void): void {
    const deferredSavePending = pendingSave !== null
    pendingSave = null
    if (!retryInFlight) {
      if (deferredSavePending) {
        pendingReject?.(new VaultOperationCancelledError())
      } else {
        pendingResolve?.(undefined)
      }
      pendingResolve = null
      pendingReject = null
    }
    hide()
  }

  function closeSetup(): void {
    closeDialog(() => setShowSetup(false))
  }

  function closeUnlock(): void {
    // Cancelling a sealed-access prompt rejects the deferred RPCs so their
    // callers abandon the operations they started. A save deferred through
    // pendingSave is rejected by closeDialog below, unchanged.
    if (!retryInFlight && sealedAccessReject) {
      sealedAccessReject(new VaultOperationCancelledError())
      sealedAccessResolve = null
      sealedAccessReject = null
    }
    closeDialog(() => setShowUnlock(false))
    setUnlockReason(null)
  }
  /**
   * saveSecretWithVault — operation-first vault error handling with retry.
   *
   * 1. Tries saveFn first. On success, resolves.
   * 2. On vault-uninitialized: checks osKeyCapable (fetches fresh status).
   *    osKeyCapable → silent setup, then retry. Silent setup failure → rejects.
   *    !osKeyCapable → SetupDialog, retry on completion.
   * 3. On vault-sealed: UnlockDialog, retry on completion.
   * 4. On any other error: rejects (propagates to caller's catch).
   * 5. User cancels a dialog: rejects with VaultOperationCancelledError, so
   *    the caller abandons the operation it started — the deferred save has
   *    not run and nothing may be reported as saved.
   */
  function saveSecretWithVault(saveFn: () => Promise<void>, reason?: string): Promise<void> {
    return new Promise<void>((resolve, reject) => {
      pendingResolve = resolve
      pendingReject = reject

      let retriedOnce = false

      const attempt = (): void => {
        void saveFn()
          .then(() => {
            pendingResolve?.(undefined)
            pendingResolve = null
            pendingReject = null
          })
          .catch((err: unknown) => {
            if (!(err instanceof RpcError)) {
              pendingReject?.(err)
              pendingResolve = null
              pendingReject = null
              return
            }
            // Named rpcReason so it cannot shadow the caller's `reason` — the
            // operation the unlock prompt must name (nocx-s8jn).
            const rpcReason = (err.data as { reason?: string } | undefined)?.reason
            if (rpcReason === 'vault-uninitialized') {
              void handleUninitialized(saveFn)
              return
            }
            if (rpcReason === 'vault-sealed') {
              void handleSealed(saveFn, reason)
              return
            }
            if (rpcReason === 'vault-changed' && !retriedOnce) {
              // The vault moved under the write — sealed and re-opened, or the
              // default store changed — so the result was discarded. Unlocking
              // does not fix this and asking the user to would be the endless
              // loop of nocx-25k9.20. Retry the operation once, silently, which
              // is what the error actually calls for.
              retriedOnce = true
              attempt()
              return
            }
            // Non-vault RPC error — propagate
            pendingReject?.(err)
            pendingResolve = null
            pendingReject = null
          })
      }

      attempt()
    })
  }

  /** Handle a vault-uninitialized error: silent setup or dialog, then retry once. */
  async function handleUninitialized(saveFn: () => Promise<void>): Promise<void> {
    // Fetch fresh status — the error came from the backend, cached status may be stale.
    try {
      const s = await vaultClient.status()
      setStatus(s)
      if (s.osKeyCapable) {
        try {
          await vaultClient.setup({})
          // Retry the save once
          await saveFn()
          pendingResolve?.(undefined)
          pendingResolve = null
          pendingReject = null
          return
        } catch (e2) {
          // Silent setup failed — reject so caller never shows "Saved"
          pendingReject?.(e2)
          pendingResolve = null
          pendingReject = null
          return
        }
      }
      // No OS key — show SetupDialog, retry on completion
      pendingSave = (): Promise<void> => {
        return saveFn().then(
          () => {
            pendingResolve?.(undefined)
            pendingResolve = null
            pendingReject = null
          },
          (e3: unknown) => {
            pendingReject?.(e3)
            pendingResolve = null
            pendingReject = null
          },
        )
      }
      setShowSetup(true)
    } catch {
      // Status fetch itself failed — cannot determine remedy
      pendingReject?.(new Error('Vault status unavailable'))
      pendingResolve = null
      pendingReject = null
    }
  }

  /** Handle a vault-sealed error: show UnlockDialog, retry on completion. */
  function handleSealed(saveFn: () => Promise<void>, reason?: string): void {
    void refresh()
    pendingSave = (): Promise<void> => {
      return saveFn().then(
        () => {
          pendingResolve?.(undefined)
          pendingResolve = null
          pendingReject = null
        },
        (e: unknown) => {
          pendingReject?.(e)
          pendingResolve = null
          pendingReject = null
        },
      )
    }

    setUnlockReason(reason ?? null)
    setShowUnlock(true)
  }

  async function seal(): Promise<void> {
    await vaultClient.seal()
    await refresh()
  }

  async function changePassphrase(params: {
    oldPassphrase?: string
    recoveryCode?: string
    newPassphrase: string
  }): Promise<void> {
    await vaultClient.changePassphrase(params)
  }

  async function regenerateRecovery(params: {
    passphrase: string
  }): Promise<{ recoveryCode: string }> {
    return vaultClient.regenerateRecovery(params)
  }

  async function setDefaultProvider(params: { provider: string }): Promise<void> {
    await vaultClient.setDefaultProvider(params)
    await refresh()
  }

  return {
    status: status_,
    showSetup,
    showUnlock,
    unlockReason,
    refresh,
    ensureBeforeSave,
    onSetupDone,
    onUnsealDone,
    openUnlock,
    openSetup,
    closeSetup,
    closeUnlock,
    saveSecretWithVault,
    seal,
    changePassphrase,
    regenerateRecovery,
    setDefaultProvider,
  }
}

// ── Setup dialog ─────────────────────────────────────────────────────────

export interface SetupDialogProps {
  open: boolean
  onClose: () => void
  /** Called after setup completes and the user has dismissed the recovery code. */
  onSetupComplete?: () => void
  vaultClient: VaultClient
}

export const SetupDialog: Component<SetupDialogProps> = (props) => {
  const [passphrase, setPassphrase] = createSignal('')
  const [confirm, setConfirm] = createSignal('')
  const [error, setError] = createSignal('')
  const [saving, setSaving] = createSignal(false)
  const [recoveryCode, setRecoveryCode] = createSignal('')
  const [copied, setCopied] = createSignal(false)

  const reset = () => {
    setPassphrase('')
    setConfirm('')
    setError('')
    setSaving(false)
    setRecoveryCode('')
    setCopied(false)
  }

  const handleSetup = async () => {
    const p = passphrase()
    const c = confirm()
    if (!p) {
      setError('Enter a master passphrase')
      return
    }
    if (p !== c) {
      setError('Passphrases do not match')
      return
    }
    setSaving(true)
    setError('')
    try {
      const result = await props.vaultClient.setup({ passphrase: p })
      if (result.recoveryCode) {
        setRecoveryCode(result.recoveryCode)
      } else {
        // Silent setup (unlikely at this point, but handle it)
        props.onSetupComplete?.()
        props.onClose()
      }
    } catch (e: unknown) {
      setError(vaultErrorMessage(e))
    } finally {
      setSaving(false)
    }
  }

  // A recovery code is shown exactly once. A copy that silently failed is
  // therefore not a small annoyance: the user presses Done believing the code
  // is on the clipboard, and it is gone for good. Both outcomes are stated.
  const handleCopy = async () => {
    try {
      await navigator.clipboard.writeText(recoveryCode())
      setCopied(true)
      setTimeout(() => setCopied(false), 3000)
      showToast({ level: 'success', message: 'Recovery code copied to the clipboard.' })
    } catch {
      showToast({
        level: 'danger',
        message: 'Could not copy the recovery code. Write it down before closing this.',
        duration: 0,
      })
    }
  }

  const handleDownload = () => {
    const blob = new Blob([recoveryCode()], { type: 'text/plain' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = 'nocx-vault-recovery-code.txt'
    a.click()
    URL.revokeObjectURL(url)
  }

  // Step 1: passphrase entry
  const passphraseView = (
    <Stack>
      <TextField
        id="vault-setup-passphrase"
        label="Master passphrase"
        type="password"
        value={passphrase()}
        onInput={(v) => {
          setPassphrase(v)
          setError('')
        }}
        error={error()}
        autoFocus
      />
      <TextField
        id="vault-setup-confirm"
        label="Confirm passphrase"
        type="password"
        value={confirm()}
        onInput={(v) => {
          setConfirm(v)
          setError('')
        }}
        error={confirm() && passphrase() !== confirm() ? 'Passphrases do not match' : undefined}
      />
    </Stack>
  )

  // Step 2: recovery code (shown exactly once)
  const recoveryView = (
    <Stack>
      <p>Your vault is ready. Save this recovery code somewhere safe — it is shown only once.</p>
      <div class="ui-vault-code-block-wrap">
        <CodeBlock>{recoveryCode()}</CodeBlock>
      </div>
      <div class="ui-vault-action-row">
        <IconButton
          ariaLabel={copied() ? 'Copied' : 'Copy recovery code'}
          onClick={() => {
            void handleCopy()
          }}
          size="sm"
        >
          <CopyIcon />
        </IconButton>
        <Button variant="ghost" onClick={handleDownload}>
          Download
        </Button>
      </div>
    </Stack>
  )

  const hasRecoveryCode = () => recoveryCode().length > 0

  // The recovery-code step is not an interruption: it shows a one-time code
  // the user must copy down, and there is nothing to type. It stays a Dialog.
  // The passphrase step is a vault password prompt — it slides in as a
  // top-sheet, the same treatment as unlock and change-passphrase.
  //
  // The switch between the two is a <Show>: a component body executes once,
  // so a top-level ternary would freeze the first branch and the recovery
  // code could never appear.
  return (
    <Show
      when={hasRecoveryCode()}
      fallback={
        <Prompt
          open={props.open}
          onClose={() => {
            reset()
            props.onClose()
          }}
          ariaLabel="Set Up Vault"
          placement="top-sheet"
          title="Set Up Vault"
          onSubmit={() => {
            void handleSetup()
          }}
          actions={
            <>
              <Button
                variant="primary"
                disabled={saving()}
                onClick={() => {
                  void handleSetup()
                }}
              >
                {saving() ? 'Setting up…' : 'Set Up'}
              </Button>
              <Button variant="default" disabled={saving()} onClick={props.onClose}>
                Cancel
              </Button>
            </>
          }
        >
          {passphraseView}
        </Prompt>
      }
    >
      <Dialog
        open={props.open}
        onClose={() => {
          reset()
          props.onClose()
        }}
        title="Recovery Code"
        footer={
          <Button
            variant="primary"
            onClick={() => {
              reset()
              props.onSetupComplete?.()
              props.onClose()
            }}
          >
            Done
          </Button>
        }
      >
        {recoveryView}
      </Dialog>
    </Show>
  )
}

// ── Reset dialog ─────────────────────────────────────────────────────────

export interface ResetVaultDialogProps {
  open: boolean
  onClose: () => void
  /** Called after the vault has been reset, so the page can refresh. */
  onReset?: () => void
  vaultClient: VaultClient
}

/**
 * The way back for a user who has forgotten the passphrase and has no recovery
 * code. Without it they are locked out permanently, because everything the
 * vault protects is protected by the thing they have lost.
 *
 * It states what will be destroyed BEFORE it is destroyed, and it says the
 * word irreversible, because it is: the file store's secrets are already
 * unrecoverable — their key is wrapped in the forgotten passphrase — and the
 * keychain's are removed outright.
 *
 * The confirmation is a checkbox rather than a typed word, matching the group
 * deletion in connections.tsx. A second vocabulary for "yes I am sure" is the
 * duplication the kit exists to prevent.
 */
export const ResetVaultDialog: Component<ResetVaultDialogProps> = (props) => {
  const [preview, setPreview] = createSignal<VaultResetPreview | null>(null)
  const [confirmed, setConfirmed] = createSignal(false)
  const [resetting, setResetting] = createSignal(false)
  const [error, setError] = createSignal('')

  const reset = () => {
    setPreview(null)
    setConfirmed(false)
    setResetting(false)
    setError('')
  }

  // The preview is fetched when the dialog opens, not when the page loads: the
  // counts must describe the store as it is at the moment the user is asked,
  // and a page that has been open for an hour has had time to be wrong.
  createEffect(() => {
    if (!props.open) return
    void props.vaultClient
      .resetPreview()
      .then(setPreview)
      .catch((e: unknown) => setError(vaultErrorMessage(e)))
  })

  const handleReset = async () => {
    setResetting(true)
    setError('')
    try {
      const result = await props.vaultClient.reset()
      reset()
      // Residue is the honest half. Saying "everything was deleted" while a
      // store still holds readable secrets is the one thing this dialog must
      // never do.
      if (result.residue.length > 0) {
        showToast({
          level: 'warning',
          message: `Vault reset. ${residueSentence(result.residue)}`,
          duration: 0,
        })
      } else {
        showToast({ level: 'success', message: 'Vault reset. Set up protection to start again.' })
      }
      props.onReset?.()
      props.onClose()
    } catch (e: unknown) {
      setResetting(false)
      setError(vaultErrorMessage(e))
    }
  }

  return (
    <Dialog
      open={props.open}
      onClose={() => {
        reset()
        props.onClose()
      }}
      title="Reset the vault"
      footer={
        <>
          <Button
            variant="danger"
            disabled={!confirmed() || resetting()}
            onClick={() => {
              void handleReset()
            }}
          >
            {resetting() ? 'Resetting…' : 'Reset the vault'}
          </Button>
          <Button variant="default" disabled={resetting()} onClick={props.onClose}>
            Cancel
          </Button>
        </>
      }
    >
      <Stack>
        <p class="ui-vault-desc-text">
          This deletes every password and key passphrase nocx has saved, and cannot be undone. There
          is no way to recover them afterwards.
        </p>

        <Show when={preview()}>
          {(p) => (
            <>
              <Show when={p().secretCount > 0}>
                <p class="ui-vault-reset-impact">
                  {countPhrase(p().secretCount, 'saved secret', 'saved secrets')} will be deleted.{' '}
                  {countPhrase(p().profileCount, 'connection', 'connections')} will ask for a
                  password again.
                </p>
              </Show>
              <Show when={p().secretCount === 0}>
                <p class="ui-vault-reset-impact">There are no saved secrets to delete.</p>
              </Show>
              {/* Stated before the choice, not after it. Whether the keychain
                  answers decides whether anything stored there can be removed
                  at all, and finding that out afterwards is a surprise rather
                  than a decision. */}
              <Show when={!p().systemKeychainReachable}>
                <p class="ui-vault-reset-warning">
                  The system keychain is not answering, so secrets stored there cannot be removed.
                  They will remain readable until it is available and you reset again.
                </p>
              </Show>
            </>
          )}
        </Show>

        <Checkbox
          checked={confirmed()}
          onChange={setConfirmed}
          label="I understand this cannot be undone"
        />

        <Show when={error()}>
          <p class="ui-vault-reset-warning">{error()}</p>
        </Show>
      </Stack>
    </Dialog>
  )
}

/** "1 connection" / "3 connections" — a count that reads as a sentence. */
function countPhrase(n: number, one: string, many: string): string {
  return `${n} ${n === 1 ? one : many}`
}

function residueSentence(residue: ResidueEntry[]): string {
  const names = residue.map((r) => storeLabelName(r.store)).join(', ')
  return `${names} could not be cleared, and still holds secrets.`
}

// ── Unlock dialog ────────────────────────────────────────────────────────

export type UnlockMeans = 'os' | 'passphrase' | 'recovery'

export interface UnlockDialogProps {
  open: boolean
  onClose: () => void
  /** Called after the vault is unsealed. */
  onUnsealed?: () => void
  vaultClient: VaultClient
  vaultStatus: VaultStatus | null
  /**
   * The operation the unlock is needed for, or null for a bare "Unlock the
   * vault". Every password prompt must say WHICH password it wants and why it
   * is asking now (nocx-s8jn): "Unlock the vault" cannot be told apart from
   * the key and connection prompts a user meets a week later.
   */
  reason?: string | null
}

export const UnlockDialog: Component<UnlockDialogProps> = (props) => {
  const [means, setMeans] = createSignal<UnlockMeans | undefined>(undefined)
  const currentMeans = () => means() ?? (props.vaultStatus?.osKeyAvailable ? 'os' : 'passphrase')
  const [secret, setSecret] = createSignal('')
  const [error, setError] = createSignal('')
  const [unlocking, setUnlocking] = createSignal(false)

  const reset = () => {
    setSecret('')
    setError('')
    setUnlocking(false)
  }

  /** What to say when the vault refuses. The generic reason line cannot name
   *  the thing the user actually typed, and this is the one place that knows
   *  which of the two it was. */
  const refusalMessage = (m: UnlockMeans, e: unknown): string => {
    const generic = vaultErrorMessage(e)
    if (generic !== REASON_MESSAGES['unseal-failed']) return generic
    if (m === 'passphrase') return 'That passphrase does not unlock this vault.'
    if (m === 'recovery') return 'That recovery code does not unlock this vault.'
    return 'The system key did not unlock this vault.'
  }

  const handleUnseal = async (overrideMeans?: UnlockMeans) => {
    const m = overrideMeans ?? currentMeans()
    // Field validation stays in the field: it is about what is in the box, it
    // clears as you type, and it is answered without asking the backend
    // anything. The OUTCOME of the call is a different kind of message and
    // goes where every other outcome on these surfaces goes — a toast.
    if (m !== 'os' && !secret()) {
      const lbl = m === 'passphrase' ? 'vault passphrase' : 'vault recovery code'
      setError(`Enter your ${lbl}`)
      return
    }
    setError('')
    setUnlocking(true)
    try {
      await props.vaultClient.unseal(m === 'os' ? { means: m } : { means: m, secret: secret() })
      reset()
      showToast({ level: 'success', message: 'Vault unlocked.' })
      props.onUnsealed?.()
      props.onClose()
    } catch (e: unknown) {
      setUnlocking(false)
      // The outcome of the call is a toast, like every other outcome on these
      // surfaces. ToastHost portals into the topmost open overlay, so it is
      // visible from inside a modal.
      showToast({ level: 'danger', message: refusalMessage(m, e) })
    }
  }

  const meansRow = (
    <div class="ui-vault-means-row">
      <Show when={props.vaultStatus?.osKeyAvailable}>
        <Button
          variant={currentMeans() === 'os' ? 'primary' : 'default'}
          onClick={() => setMeans('os')}
        >
          System key
        </Button>
      </Show>
      <Button
        variant={currentMeans() === 'passphrase' ? 'primary' : 'default'}
        onClick={() => setMeans('passphrase')}
      >
        Passphrase
      </Button>
      <Button
        variant={currentMeans() === 'recovery' ? 'primary' : 'default'}
        onClick={() => setMeans('recovery')}
      >
        Recovery code
      </Button>
    </div>
  )

  const meansForm = () => {
    const m = currentMeans()
    if (m === 'os') {
      return (
        <p class="ui-vault-desc-text">Unlock with your system keychain — no passphrase needed.</p>
      )
    }
    const label = m === 'passphrase' ? 'Vault passphrase' : 'Vault recovery code'
    const placeholder = m === 'passphrase' ? 'Your vault passphrase' : 'Your vault recovery code'
    const inputId = m === 'passphrase' ? 'vault-unlock-passphrase' : 'vault-unlock-recovery'
    return (
      <Stack>
        <TextField
          id={inputId}
          label={label}
          placeholder={placeholder}
          type="password"
          value={secret()}
          onInput={(v) => {
            setSecret(v)
            setError('')
          }}
          error={error()}
          autoFocus
        />
      </Stack>
    )
  }

  return (
    <Prompt
      open={props.open}
      onClose={() => {
        reset()
        props.onClose()
      }}
      // The title says WHICH password and WHY (nocx-s8jn): a bare "Unlock the
      // vault" cannot be told apart from the key and connection prompts, and
      // the reason names the operation that made the app ask now.
      ariaLabel={props.reason ? `Unlock the vault to ${props.reason}` : 'Unlock the vault'}
      placement="top-sheet"
      title={props.reason ? `Unlock the vault to ${props.reason}` : 'Unlock the vault'}
      // Enter unlocks, supplied by the Prompt's onSubmit — the same contract
      // Dialog offered. The one control a user reaches for reflexively in a
      // passphrase prompt must not do nothing.
      onSubmit={() => {
        if (unlocking()) return
        void handleUnseal()
      }}
      actions={
        <>
          <Button
            variant="primary"
            disabled={unlocking()}
            onClick={() => {
              void handleUnseal()
            }}
          >
            {currentMeans() === 'os' ? 'Unlock' : unlocking() ? 'Unlocking…' : 'Unlock'}
          </Button>
          <Button variant="default" disabled={unlocking()} onClick={props.onClose}>
            Cancel
          </Button>
        </>
      }
    >
      {meansRow}
      {meansForm()}
    </Prompt>
  )
}

// ── Change passphrase dialog ────────────────────────────────────────────

export interface ChangePassphraseDialogProps {
  open: boolean
  onClose: () => void
  vaultClient: VaultClient
}

export const ChangePassphraseDialog: Component<ChangePassphraseDialogProps> = (props) => {
  const [mode, setMode] = createSignal<'passphrase' | 'recovery'>('passphrase')
  const [oldPassphrase, setOldPassphrase] = createSignal('')
  const [recoveryCode, setRecoveryCode] = createSignal('')
  const [newPassphrase, setNewPassphrase] = createSignal('')
  const [confirmPassphrase, setConfirmPassphrase] = createSignal('')
  const [error, setError] = createSignal('')
  const [changing, setChanging] = createSignal(false)

  const reset = () => {
    setMode('passphrase')
    setOldPassphrase('')
    setRecoveryCode('')
    setNewPassphrase('')
    setConfirmPassphrase('')
    setError('')
    setChanging(false)
  }

  const handleChange = async () => {
    setError('')
    const np = newPassphrase()
    if (!np) {
      setError('Enter a new vault passphrase')
      return
    }
    if (np !== confirmPassphrase()) {
      setError('Passphrases do not match')
      return
    }

    const m = mode()
    if (m === 'passphrase' && !oldPassphrase()) {
      setError('Enter your current vault passphrase')
      return
    }
    if (m === 'recovery' && !recoveryCode()) {
      setError('Enter your recovery code')
      return
    }

    setChanging(true)
    try {
      await props.vaultClient.changePassphrase(
        m === 'passphrase'
          ? { oldPassphrase: oldPassphrase(), newPassphrase: np }
          : { recoveryCode: recoveryCode(), newPassphrase: np },
      )
      reset()
      props.onClose()
      showToast({ level: 'success', message: 'Passphrase changed.' })
    } catch (e: unknown) {
      setChanging(false)
      setError(vaultErrorMessage(e))
    }
  }

  return (
    <Prompt
      open={props.open}
      onClose={() => {
        reset()
        props.onClose()
      }}
      ariaLabel="Change vault passphrase"
      placement="top-sheet"
      title="Change vault passphrase"
      onSubmit={() => {
        void handleChange()
      }}
      actions={
        <>
          <Button
            variant="primary"
            disabled={changing()}
            onClick={() => {
              void handleChange()
            }}
          >
            {changing() ? 'Changing…' : 'Change passphrase'}
          </Button>
          <Button
            variant="default"
            disabled={changing()}
            onClick={() => {
              reset()
              props.onClose()
            }}
          >
            Cancel
          </Button>
        </>
      }
    >
      <Stack>
        <div class="ui-vault-means-row">
          <Button
            variant={mode() === 'passphrase' ? 'primary' : 'default'}
            onClick={() => {
              setMode('passphrase')
              setError('')
            }}
          >
            I know my passphrase
          </Button>
          <Button
            variant={mode() === 'recovery' ? 'primary' : 'default'}
            onClick={() => {
              setMode('recovery')
              setError('')
            }}
          >
            I have a recovery code
          </Button>
        </div>

        <Show when={mode() === 'passphrase'}>
          <TextField
            id="vault-change-old-passphrase"
            label="Current vault passphrase"
            type="password"
            value={oldPassphrase()}
            onInput={(v) => {
              setOldPassphrase(v)
              setError('')
            }}
            autoFocus
          />
        </Show>
        <Show when={mode() === 'recovery'}>
          <TextField
            id="vault-change-recovery"
            label="Vault recovery code"
            type="password"
            value={recoveryCode()}
            onInput={(v) => {
              setRecoveryCode(v)
              setError('')
            }}
            autoFocus
          />
        </Show>

        <TextField
          id="vault-change-new-passphrase"
          label="New vault passphrase"
          type="password"
          value={newPassphrase()}
          onInput={(v) => {
            setNewPassphrase(v)
            setError('')
          }}
        />
        <TextField
          id="vault-change-confirm-passphrase"
          label="Confirm new vault passphrase"
          type="password"
          value={confirmPassphrase()}
          onInput={(v) => {
            setConfirmPassphrase(v)
            setError('')
          }}
          error={error()}
        />

        <p class="ui-vault-desc-text">
          Changing the passphrase requires your current passphrase or a recovery code. An OS-held
          key alone is not sufficient — a factor that only unlocks must not be able to replace the
          factor that recovers.
        </p>
      </Stack>
    </Prompt>
  )
}

// ── Recovery code dialog ────────────────────────────────────────────────

export interface RecoveryCodeDialogProps {
  open: boolean
  onClose: () => void
  vaultClient: VaultClient
}

export const RecoveryCodeDialog: Component<RecoveryCodeDialogProps> = (props) => {
  const [passphrase, setPassphrase] = createSignal('')
  const [recoveryCode, setRecoveryCode] = createSignal<string | null>(null)
  const [error, setError] = createSignal('')
  const [generating, setGenerating] = createSignal(false)
  const [copied, setCopied] = createSignal(false)

  const reset = () => {
    setPassphrase('')
    setRecoveryCode(null)
    setError('')
    setGenerating(false)
    setCopied(false)
  }

  const handleGenerate = async () => {
    if (!passphrase()) {
      setError('Enter your vault passphrase')
      return
    }
    setError('')
    setGenerating(true)
    try {
      const result = await props.vaultClient.regenerateRecovery({ passphrase: passphrase() })
      setRecoveryCode(result.recoveryCode)
    } catch (e: unknown) {
      setGenerating(false)
      setError(vaultErrorMessage(e))
    }
  }

  // Shown once, so a silent failure costs the code — see SetupDialog's copy
  // handler for the same reasoning.
  const handleCopy = () => {
    const code = recoveryCode()
    if (!code) return
    void navigator.clipboard.writeText(code).then(
      () => {
        setCopied(true)
        showToast({ level: 'success', message: 'Recovery code copied to the clipboard.' })
      },
      () => {
        showToast({
          level: 'danger',
          message: 'Could not copy the recovery code. Write it down before closing this.',
          duration: 0,
        })
      },
    )
  }

  const handleDone = () => {
    reset()
    props.onClose()
  }

  return (
    <Prompt
      open={props.open}
      onClose={() => {
        reset()
        props.onClose()
      }}
      ariaLabel="Reissue recovery code"
      placement="top-sheet"
      title="Reissue recovery code"
      // Enter submits the passphrase, the same as everywhere else a passphrase
      // is asked for. Only while it is still being asked: once the code is on
      // screen the affirmative action is Done, and Enter must not dismiss a
      // one-time code the user has not written down yet.
      onSubmit={() => {
        if (recoveryCode() !== null || generating()) return
        void handleGenerate()
      }}
      // The buttons live in the body, one set per step — the code-display
      // step has only Done, no Cancel, so there is no shared action row.
      actions={<></>}
    >
      <Show when={recoveryCode() === null}>
        <Stack>
          <TextField
            id="vault-reissue-passphrase"
            label="Current vault passphrase"
            type="password"
            value={passphrase()}
            onInput={(v) => {
              setPassphrase(v)
              setError('')
            }}
            error={error()}
            autoFocus
          />
          <Button
            variant="primary"
            disabled={generating()}
            onClick={() => {
              void handleGenerate()
            }}
          >
            {generating() ? 'Generating…' : 'Generate new recovery code'}
          </Button>
          <Button
            variant="default"
            disabled={generating()}
            onClick={() => {
              reset()
              props.onClose()
            }}
          >
            Cancel
          </Button>
        </Stack>
      </Show>
      <Show when={recoveryCode() !== null}>
        <Stack>
          <p class="ui-vault-desc-text">
            Your new recovery code is shown below. Copy it now — it will not be displayed again.
            Keep it in a safe place.
          </p>
          <div class="ui-vault-recovery-row">
            <CodeBlock>{recoveryCode() ?? ''}</CodeBlock>
            <IconButton ariaLabel={copied() ? 'Copied' : 'Copy recovery code'} onClick={handleCopy}>
              <CopyIcon />
            </IconButton>
          </div>
          <Button variant="primary" onClick={handleDone}>
            Done
          </Button>
        </Stack>
      </Show>
    </Prompt>
  )
}

// ── Vault settings section ─────────────────────────────────────────────

export interface VaultSectionProps {
  vaultClient: VaultClient
  vaultController: VaultController
}

export function VaultSection(props: VaultSectionProps) {
  // Re-read the state from the backend on every mount. Without this the page
  // renders the controller's CACHED status, so a vault that sealed while the
  // user was elsewhere — by the idle timer, or from another surface — still
  // shows "Lock now" and offers actions that cannot work. It also cost hours of
  // diagnosis: every "the page says unsealed but the write says sealed" reading
  // was the cache disagreeing with the truth, not the backend contradicting
  // itself.
  onMount(() => {
    void props.vaultController.refresh()
  })

  const [dialog, setDialog] = createSignal<'setup' | 'passphrase' | 'recovery' | 'reset' | null>(
    null,
  )
  const [sealing, setSealing] = createSignal(false)
  const [lastTestRun, setLastTestRun] = createSignal<Record<string, number>>({})

  const handleSeal = async () => {
    setSealing(true)
    try {
      await props.vaultController.seal()
      showToast({ level: 'success', message: 'Vault locked.' })
    } catch (e: unknown) {
      showToast({ level: 'danger', message: vaultErrorMessage(e) })
    } finally {
      setSealing(false)
    }
  }

  const status = () => props.vaultController.status()

  const handleDefaultProvider = async (provider: string) => {
    try {
      await props.vaultController.setDefaultProvider({ provider })
      showToast({ level: 'success', message: 'Default provider updated.' })
    } catch (e: unknown) {
      showToast({ level: 'danger', message: vaultErrorMessage(e) })
    }
  }

  const actionCanRun = () => {
    // Both "Change passphrase" and "Reissue recovery code" require an
    // unsealed vault with a passphrase envelope. An OS-held key alone
    // is not sufficient — a factor that only unseals must not be able
    // to replace the factor that recovers (vault.ChangePassphrase doc).
    const s = status()
    if (!s) return false
    return s.state === 'unsealed' && s.hasPassphrase
  }

  const actionDisabledReason = () => {
    const s = status()
    if (!s) return ''
    // An instruction, not a restatement. The card at the top of the page has
    // already said what state the vault is in; repeating "Vault is locked."
    // here answered a question nobody had asked and left the actual one —
    // what do I do about it — unanswered.
    if (s.state === 'uninitialized')
      return 'Set up protection first; there is nothing to change yet.'
    if (s.state === 'sealed') return 'Unlock the vault to change how it is protected.'
    // Unsealed, but the vault has no passphrase envelope: it is held by an
    // OS key alone, and both of these actions are about a passphrase.
    return 'This vault is held by an OS key alone, so there is no passphrase to change or recover.'
  }

  const AUTO_LOCK_OPTIONS = [
    { value: 0, label: 'Never' },
    { value: 5, label: '5 min' },
    { value: 15, label: '15 min' },
    { value: 30, label: '30 min' },
    { value: 60, label: '60 min' },
  ]

  const handleSetAutoLock = async (minutes: number) => {
    try {
      await props.vaultClient.setAutoSeal(minutes)
      await props.vaultController.refresh()
      showToast({ level: 'success', message: 'Auto-lock timeout updated.' })
    } catch (e: unknown) {
      showToast({ level: 'danger', message: vaultErrorMessage(e) })
    }
  }

  // Test re-probes every store and reports what THIS one answered.
  //
  // It used to refresh and stamp a timestamp, which is a record of when the
  // question was asked and no answer at all: on a store that was already
  // failing, pressing Test changed one line of small print and nothing else,
  // so the button read as broken. The dot and the sentence do update — but
  // only if they change, and the common case for a Test press is that they do
  // not.
  const handleTest = async (id: string) => {
    const asked = Date.now()
    try {
      await props.vaultController.refresh()
    } catch (e: unknown) {
      showToast({ level: 'danger', message: vaultErrorMessage(e) })
      return
    }
    setLastTestRun((r) => ({ ...r, [id]: asked }))

    const p = status()?.providers.find((x) => x.id === id)
    if (!p) {
      showToast({ level: 'warning', message: `${storeLabelName(id)} is no longer registered.` })
      return
    }
    showToast({
      level: p.ready ? 'success' : 'danger',
      message: storeStateSentence(p),
    })
  }

  // ── The state card ──────────────────────────────────────────────────
  // The headline fact of this page is a condition, not a control, and it was
  // rendered as a paragraph with a button beside it — which made the most
  // important sentence on the page the least prominent thing on it.

  const cardTone = (): StatusCardTone => {
    const s = status()
    if (!s) return 'neutral'
    if (s.state === 'unsealed') return 'ok'
    if (s.state === 'uninitialized') return 'warning'
    return 'neutral'
  }

  const cardTitle = (): string => {
    const s = status()
    if (!s) return ''
    if (s.state === 'unsealed') return 'Vault is unlocked'
    if (s.state === 'sealed') return 'Vault is locked'
    return 'Protection is not set up yet'
  }

  const cardDescription = (): string => {
    const s = status()
    if (!s) return ''
    if (s.state === 'unsealed') {
      return s.autoSealMinutes > 0
        ? `Saved passwords and key passphrases are available to your connections, and lock again after ${s.autoSealMinutes} minutes idle.`
        : 'Saved passwords and key passphrases are available to your connections.'
    }
    if (s.state === 'sealed') {
      return 'Saved passwords and key passphrases stay encrypted until you unlock. Connections that need one will ask.'
    }
    return 'nocx keeps the passwords and key passphrases you save for your connections. Set up protection to start storing them encrypted.'
  }

  const diagStateTone = (): BadgeTone => {
    const s = status()
    if (!s) return 'neutral'
    if (s.state === 'unsealed') return 'success'
    if (s.state === 'uninitialized') return 'warning'
    return 'neutral'
  }

  const cardActionLabel = (): string => {
    const s = status()
    if (!s) return ''
    if (s.state === 'unsealed') return sealing() ? 'Locking…' : 'Lock now'
    return s.state === 'sealed' ? 'Unlock' : 'Set up protection'
  }

  // ── The store list ──────────────────────────────────────────────────
  // A row per store, every state visible at once. This was a vertical Tabs
  // rail, whose only advantage was compactness and whose cost the user paid
  // twice: the store that is not answering is the one you are not looking at,
  // and the rail's fixed 9.5rem clipped both store names — which, because a
  // scroll container on one axis forces the other, put a horizontal scrollbar
  // under the rail that looked like a rendering artefact.

  const stores = () => status()?.providers ?? []

  return (
    <div>
      <Show when={status()}>
        {(s) => (
          <StatusCard
            tone={cardTone()}
            icon={s().state === 'unsealed' ? <LockOpenIcon /> : <LockIcon />}
            title={cardTitle()}
            description={cardDescription()}
            action={
              <Button
                variant="primary"
                disabled={s().state === 'unsealed' && sealing()}
                onClick={() => {
                  if (s().state === 'uninitialized') setDialog('setup')
                  else if (s().state === 'sealed')
                    props.vaultController.openUnlock('change your vault settings')
                  else void handleSeal()
                }}
              >
                {cardActionLabel()}
              </Button>
            }
          />
        )}
      </Show>

      {/* Where it is stored */}
      <Show when={stores().length > 0}>
        <PageSection title="Where it is stored" divided>
          <For each={stores()}>
            {(p) => {
              const rowStatus = storeRowStatus(p)
              const isDefault = () => status()?.defaultProvider === p.id
              const lastCheck = () => lastTestRun()[p.id]
              // A store that is not answering cannot be told to take new
              // secrets: the write would fail at the moment the user saves a
              // password, which is the worst possible moment to discover it.
              // The row already says why — the state sentence is right below —
              // so the disabled button needs no explanation of its own.
              const canTakeNewSecrets = () => p.ready && p.writable
              return (
                <div class="ui-vault-store" data-default={isDefault() ? 'true' : undefined}>
                  <div class="ui-vault-store__head">
                    <StatusDot tone={rowStatus.tone} accessibleName={rowStatus.accessibleName}>
                      <span class="ui-vault-store__name">{storeLabelName(p.id)}</span>
                    </StatusDot>
                    <Show when={isDefault()}>
                      <Badge tone="info">New secrets go here</Badge>
                    </Show>
                    <div class="ui-vault-store__actions">
                      <Show when={!isDefault()}>
                        <Button
                          variant="default"
                          disabled={!canTakeNewSecrets()}
                          onClick={() => {
                            void handleDefaultProvider(p.id)
                          }}
                        >
                          Store new secrets here
                        </Button>
                      </Show>
                      <Button
                        variant="default"
                        onClick={() => {
                          void handleTest(p.id)
                        }}
                      >
                        Test
                      </Button>
                    </div>
                  </div>
                  <p class="ui-vault-store__state">
                    {storeStateSentence(p)}
                    <Show when={lastCheck()}>
                      {(ts) => <> · Last checked: {new Date(ts()).toLocaleTimeString()}</>}
                    </Show>
                  </p>
                </div>
              )
            }}
          </For>
        </PageSection>
      </Show>

      {/* Protection. The reason these controls are unavailable is stated once,
          for the section — not repeated under each of them, which is what put
          "Vault is locked." on this page three times. */}
      <PageSection
        title="Protection"
        description={!actionCanRun() ? actionDisabledReason() : undefined}
        divided
      >
        <Show when={status()?.state === 'unsealed'}>
          <Field for="vault-auto-lock" label="Lock automatically after" orientation="horizontal">
            <Select
              value={String(status()!.autoSealMinutes)}
              onChange={(v) => void handleSetAutoLock(Number(v))}
              options={AUTO_LOCK_OPTIONS.map((o) => ({
                value: String(o.value),
                label: o.label,
              }))}
            />
          </Field>
        </Show>
        <Field
          for="vault-change-passphrase"
          label="Change passphrase"
          orientation="horizontal"
          description="The passphrase that unlocks the vault on a machine with no OS-held key."
        >
          <Button
            variant="default"
            disabled={!actionCanRun()}
            onClick={() => setDialog('passphrase')}
          >
            Change passphrase
          </Button>
        </Field>
        <Field
          for="vault-reissue-recovery"
          label="Recovery code"
          orientation="horizontal"
          description="The one-time code that recovers the vault when the passphrase is lost."
        >
          <Button
            variant="default"
            disabled={!actionCanRun()}
            onClick={() => setDialog('recovery')}
          >
            Reissue recovery code
          </Button>
        </Field>
        {/* Last, and the only enabled control here while the vault is locked.
            It is the way back for someone who has forgotten the passphrase and
            has no recovery code — for whom every other row on this page is
            disabled precisely because they cannot get in. */}
        <Field
          for="vault-reset"
          label="Reset the vault"
          orientation="horizontal"
          description="Delete every saved password and key passphrase and start again. Cannot be undone."
        >
          <Button variant="danger" onClick={() => setDialog('reset')}>
            Reset the vault
          </Button>
        </Field>
      </PageSection>

      {/* Diagnostics disclosure */}
      <Show when={status()}>
        <details class="ui-vault-diagnostics">
          <summary>Diagnostics</summary>
          {/* Values are Badges, not fine print. These lines are read while
              something is wrong, by someone comparing "Ready" against "Not
              ready" down a column — the difference has to be visible before
              it is read, and a smaller-than-body type size was working
              directly against that. The tone carries it: `success` says
              nothing to do here, which is the whole reason Badge grew one. */}
          <Stack>
            <Field for="vault-state-raw" label="State" orientation="horizontal">
              <Badge tone={diagStateTone()}>{status()!.state}</Badge>
            </Field>
            <Field for="vault-oskey-raw" label="OS-held key" orientation="horizontal">
              <Badge tone={status()!.osKeyAvailable ? 'success' : 'neutral'}>
                {status()!.osKeyAvailable ? 'Available' : 'Not available'}
              </Badge>
            </Field>
            <Show when={status()!.providers.length > 0}>
              <For each={status()!.providers}>
                {(p) => (
                  <Field
                    for={'vault-diag-' + p.id}
                    label={storeLabelName(p.id)}
                    orientation="horizontal"
                  >
                    <Badge tone={p.writable ? 'neutral' : 'warning'}>
                      {p.writable ? 'Writable' : 'Read-only'}
                    </Badge>
                    <Badge tone={p.ready ? 'success' : 'danger'}>
                      {p.ready ? 'Ready' : 'Not ready'}
                    </Badge>
                    {/* The reason code, verbatim. Everywhere else the surface
                        turns it into a sentence; here the raw code is the
                        point — it is what goes in a bug report. */}
                    <Show when={p.reason}>{(r) => <Badge tone="danger">{r()}</Badge>}</Show>
                  </Field>
                )}
              </For>
            </Show>
          </Stack>
        </details>
      </Show>

      {/* Dialogs */}
      <ChangePassphraseDialog
        open={dialog() === 'passphrase'}
        onClose={() => setDialog(null)}
        vaultClient={props.vaultClient}
      />
      <RecoveryCodeDialog
        open={dialog() === 'recovery'}
        onClose={() => setDialog(null)}
        vaultClient={props.vaultClient}
      />
      <ResetVaultDialog
        open={dialog() === 'reset'}
        onClose={() => setDialog(null)}
        onReset={() => void props.vaultController.refresh()}
        vaultClient={props.vaultClient}
      />
      <SetupDialog
        open={dialog() === 'setup'}
        onClose={() => setDialog(null)}
        onSetupComplete={() => {
          void props.vaultController.refresh()
          props.vaultController.onSetupDone()
        }}
        vaultClient={props.vaultClient}
      />
    </div>
  )
}

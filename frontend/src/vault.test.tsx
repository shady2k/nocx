// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest'
import { cleanup, render, screen, fireEvent } from '@solidjs/testing-library'
import {
  SetupDialog,
  UnlockDialog,
  createVaultState,
  ChangePassphraseDialog,
  RecoveryCodeDialog,
  VaultSection,
  VaultOperationCancelledError,
  ResetVaultDialog,
} from './vault'
import { ToastHost, clearToasts } from './ui/toast'
import type { VaultClient, VaultResetPreview, VaultResetResult } from './vault-client'
import { RpcError } from './dispatcher'

// ── jsdom patch: native <dialog> showModal/close are unsupported ──────
// jsdom 29 does not implement HTMLDialogElement.prototype.showModal.
// We stub both methods so Dialog's createEffect does not throw.
const origShowModal = HTMLDialogElement.prototype.showModal.bind(HTMLDialogElement.prototype)
const origClose = HTMLDialogElement.prototype.close.bind(HTMLDialogElement.prototype)

beforeEach(() => {
  HTMLDialogElement.prototype.showModal = vi.fn()
  HTMLDialogElement.prototype.close = vi.fn()
})

afterEach(() => {
  cleanup()
  HTMLDialogElement.prototype.showModal = origShowModal
  HTMLDialogElement.prototype.close = origClose
})

// ── Helpers ────────────────────────────────────────────────────────────

function mockClient() {
  const status = vi.fn()
  const setup = vi.fn()
  const unseal = vi.fn()
  const seal = vi.fn()
  const changePassphrase = vi.fn()
  const regenerateRecovery = vi.fn()
  const setDefaultProvider = vi.fn()
  const setAutoSeal = vi.fn()
  const activity = vi.fn()
  const client = {
    status,
    setup,
    unseal,
    seal,
    changePassphrase,
    regenerateRecovery,
    setDefaultProvider,
    setAutoSeal,
    activity,
  } as unknown as VaultClient
  return {
    client,
    status,
    setup,
    unseal,
    seal,
    changePassphrase,
    regenerateRecovery,
    setDefaultProvider,
    setAutoSeal,
    activity,
  }
}

const BASE_STATUS = {
  state: 'sealed' as const,
  osKeyAvailable: false,
  osKeyCapable: false,
  hasPassphrase: false,
  autoSealMinutes: 0,
  providers: [],
  defaultProvider: null,
}

// ── createVaultState — controller behavior (no Dialog rendering) ───────

describe('createVaultState', () => {
  it('calls silent setup + doSave when osKeyCapable and uninitialized', async () => {
    const { client, setup } = mockClient()
    ;(client.status as ReturnType<typeof vi.fn>).mockResolvedValue({
      state: 'uninitialized',
      osKeyAvailable: false,
      osKeyCapable: true,
      hasPassphrase: false,
      autoSealMinutes: 0,
      providers: [],
      defaultProvider: null,
    })
    setup.mockResolvedValue({})

    const ctrl = createVaultState(client)
    await ctrl.refresh()

    const doSave = vi.fn().mockResolvedValue(undefined)
    ctrl.ensureBeforeSave(doSave)

    // Silent setup: no dialog shown, setup called, save called
    expect(ctrl.showSetup()).toBe(false)
    await vi.waitFor(() => {
      expect(setup).toHaveBeenCalledWith({})
    })
    await vi.waitFor(() => {
      expect(doSave).toHaveBeenCalled()
    })
  })

  it('does not save when silent setup fails', async () => {
    const { client, setup } = mockClient()
    ;(client.status as ReturnType<typeof vi.fn>).mockResolvedValue({
      state: 'uninitialized',
      osKeyAvailable: false,
      osKeyCapable: true,
      hasPassphrase: false,
      autoSealMinutes: 0,
      providers: [],
      defaultProvider: null,
    })
    setup.mockRejectedValue(new Error('no-service'))

    const ctrl = createVaultState(client)
    await ctrl.refresh()

    const doSave = vi.fn().mockResolvedValue(undefined)
    ctrl.ensureBeforeSave(doSave)

    await vi.waitFor(() => {
      expect(setup).toHaveBeenCalledWith({})
    })
    // doSave must NOT be called when setup fails
    expect(doSave).not.toHaveBeenCalled()
  })

  it('shows setup dialog when uninitialized and no OS key', async () => {
    const { client } = mockClient()
    ;(client.status as ReturnType<typeof vi.fn>).mockResolvedValue({
      state: 'uninitialized',
      osKeyAvailable: false,
      hasPassphrase: false,
      autoSealMinutes: 0,
      providers: [],
      defaultProvider: null,
    })

    const ctrl = createVaultState(client)
    await ctrl.refresh()
    const doSave = vi.fn()

    ctrl.ensureBeforeSave(doSave)

    expect(ctrl.showSetup()).toBe(true)
    expect(ctrl.showUnlock()).toBe(false)
    expect(doSave).not.toHaveBeenCalled()
  })

  it('shows unlock dialog when sealed', async () => {
    const { client } = mockClient()
    ;(client.status as ReturnType<typeof vi.fn>).mockResolvedValue({
      state: 'sealed',
      osKeyAvailable: true,
      hasPassphrase: false,
      autoSealMinutes: 0,
      providers: [],
      defaultProvider: null,
    })

    const ctrl = createVaultState(client)
    await ctrl.refresh()
    const doSave = vi.fn()

    ctrl.ensureBeforeSave(doSave)

    expect(ctrl.showUnlock()).toBe(true)
    expect(ctrl.showSetup()).toBe(false)
    expect(doSave).not.toHaveBeenCalled()
  })

  it('calls doSave immediately when already unsealed', async () => {
    const { client } = mockClient()
    ;(client.status as ReturnType<typeof vi.fn>).mockResolvedValue({
      state: 'unsealed',
      osKeyAvailable: true,
      hasPassphrase: false,
      autoSealMinutes: 0,
      providers: [],
      defaultProvider: null,
    })

    const ctrl = createVaultState(client)
    await ctrl.refresh()
    const doSave = vi.fn().mockResolvedValue(undefined)

    ctrl.ensureBeforeSave(doSave)

    await vi.waitFor(() => {
      expect(doSave).toHaveBeenCalled()
    })
    expect(ctrl.showSetup()).toBe(false)
    expect(ctrl.showUnlock()).toBe(false)
  })

  it('fetches status first when status is null', async () => {
    const { client } = mockClient()
    // Status is null initially — refresh not called
    ;(client.status as ReturnType<typeof vi.fn>).mockResolvedValue({
      state: 'unsealed',
      osKeyAvailable: false,
      hasPassphrase: false,
      autoSealMinutes: 0,
      providers: [],
      defaultProvider: null,
    })

    const ctrl = createVaultState(client)
    // Do NOT call refresh() — status signal is null
    const doSave = vi.fn().mockResolvedValue(undefined)

    ctrl.ensureBeforeSave(doSave)

    // Should fetch status, see it's unsealed, and call doSave
    await vi.waitFor(() => {
      expect(doSave).toHaveBeenCalled()
    })
  })

  it('does not save when null status and refresh fails', async () => {
    const { client, status } = mockClient()
    // Status is null (refresh not called) and status() rejects
    status.mockRejectedValue(new Error('disconnected'))

    const ctrl = createVaultState(client)
    const doSave = vi.fn().mockResolvedValue(undefined)

    ctrl.ensureBeforeSave(doSave)

    // Refresh fails, doSave must NOT be called
    await vi.waitFor(() => {
      expect(status).toHaveBeenCalled()
    })
    expect(doSave).not.toHaveBeenCalled()
  })

  it('resumes deferred save via onSetupDone after setup dialog', async () => {
    const { client } = mockClient()
    ;(client.status as ReturnType<typeof vi.fn>).mockResolvedValue({
      state: 'uninitialized',
      osKeyAvailable: false,
      hasPassphrase: false,
      autoSealMinutes: 0,
      providers: [],
      defaultProvider: null,
    })

    const ctrl = createVaultState(client)
    await ctrl.refresh()
    const doSave = vi.fn().mockResolvedValue(undefined)

    ctrl.ensureBeforeSave(doSave)
    expect(ctrl.showSetup()).toBe(true)
    expect(doSave).not.toHaveBeenCalled()

    // Surface calls this after setup dialog completes
    ctrl.onSetupDone()
    await vi.waitFor(() => {
      expect(doSave).toHaveBeenCalled()
    })
  })

  it('resumes deferred save via onUnsealDone after unlock dialog', async () => {
    const { client } = mockClient()
    ;(client.status as ReturnType<typeof vi.fn>).mockResolvedValue({
      state: 'sealed',
      osKeyAvailable: false,
      hasPassphrase: false,
      autoSealMinutes: 0,
      providers: [],
      defaultProvider: null,
    })

    const ctrl = createVaultState(client)
    await ctrl.refresh()
    const doSave = vi.fn().mockResolvedValue(undefined)

    ctrl.ensureBeforeSave(doSave)
    expect(ctrl.showUnlock()).toBe(true)
    expect(doSave).not.toHaveBeenCalled()

    // Surface calls this after unlock dialog completes
    ctrl.onUnsealDone()
    await vi.waitFor(() => {
      expect(doSave).toHaveBeenCalled()
    })
  })

  it('openUnlock(reason) records the operation that triggered it', () => {
    const { client } = mockClient()
    const ctrl = createVaultState(client)

    ctrl.openUnlock('view your secrets')
    expect(ctrl.showUnlock()).toBe(true)
    expect(ctrl.unlockReason()).toBe('view your secrets')

    ctrl.closeUnlock()
    expect(ctrl.showUnlock()).toBe(false)
    expect(ctrl.unlockReason()).toBeNull()
  })

  it('ensureBeforeSave carries the reason to the unlock prompt', async () => {
    const { client } = mockClient()
    ;(client.status as ReturnType<typeof vi.fn>).mockResolvedValue({
      state: 'sealed',
      osKeyAvailable: true,
      osKeyCapable: true,
      autoSealMinutes: 0,
      providers: [],
      defaultProvider: null,
    })
    const ctrl = createVaultState(client)
    const doSave = vi.fn().mockResolvedValue(undefined)
    await ctrl.refresh()

    ctrl.ensureBeforeSave(doSave, 'save this connection')
    expect(ctrl.showUnlock()).toBe(true)
    expect(ctrl.unlockReason()).toBe('save this connection')
  })
})

// ── saveSecretWithVault — operation-first vault error handling ──────────

function makeRpcError(reason: string): Error {
  return new RpcError('vault error', -32000, { reason })
}

describe('saveSecretWithVault', () => {
  it('vault-uninitialized + no OS key: shows SetupDialog, retries save after setup', async () => {
    const { client, status, setup } = mockClient()
    status.mockResolvedValue({
      state: 'uninitialized',
      osKeyAvailable: false,
      hasPassphrase: false,
      autoSealMinutes: 0,
      providers: [],
      defaultProvider: null,
    })
    setup.mockResolvedValue({})

    const ctrl = createVaultState(client)
    await ctrl.refresh()

    const savePassword = vi
      .fn<(...args: string[]) => Promise<void>>()
      .mockRejectedValueOnce(makeRpcError('vault-uninitialized'))
      .mockResolvedValueOnce(undefined)
    const saveFn = () => savePassword('my-pw')

    const promise = ctrl.saveSecretWithVault(saveFn)

    await vi.waitFor(() => expect(ctrl.showSetup()).toBe(true))
    expect(ctrl.showUnlock()).toBe(false)
    expect(savePassword).toHaveBeenCalledTimes(1)

    ctrl.onSetupDone()
    await expect(promise).resolves.toBeUndefined()
    expect(savePassword).toHaveBeenCalledTimes(2)
    expect(savePassword.mock.calls[0]).toEqual(['my-pw'])
    expect(savePassword.mock.calls[1]).toEqual(['my-pw'])
  })

  it('vault-uninitialized + osKeyCapable: silent setup, no dialog, retries save', async () => {
    const { client, status, setup } = mockClient()
    status.mockResolvedValue({
      state: 'uninitialized',
      osKeyAvailable: false,
      osKeyCapable: true,
      hasPassphrase: false,
      autoSealMinutes: 0,
      providers: [],
      defaultProvider: null,
    })
    setup.mockResolvedValue({})

    const ctrl = createVaultState(client)
    await ctrl.refresh()

    const savePassword = vi
      .fn<(...args: string[]) => Promise<void>>()
      .mockRejectedValueOnce(makeRpcError('vault-uninitialized'))
      .mockResolvedValueOnce(undefined)
    const saveFn = () => savePassword('my-pw')

    const promise = ctrl.saveSecretWithVault(saveFn)

    await expect(promise).resolves.toBeUndefined()
    expect(ctrl.showSetup()).toBe(false)
    expect(ctrl.showUnlock()).toBe(false)
    expect(setup).toHaveBeenCalledWith({})
    expect(savePassword).toHaveBeenCalledTimes(2)
    expect(savePassword.mock.calls[0]).toEqual(['my-pw'])
    expect(savePassword.mock.calls[1]).toEqual(['my-pw'])
  })

  it('vault-sealed: shows UnlockDialog, retries after unseal', async () => {
    const { client, status } = mockClient()
    status.mockResolvedValue({
      state: 'sealed',
      osKeyAvailable: true,
      hasPassphrase: false,
      autoSealMinutes: 0,
      providers: [],
      defaultProvider: null,
    })

    const ctrl = createVaultState(client)
    await ctrl.refresh()

    const savePassword = vi
      .fn<(...args: string[]) => Promise<void>>()
      .mockRejectedValueOnce(makeRpcError('vault-sealed'))
      .mockResolvedValueOnce(undefined)
    const saveFn = () => savePassword('my-pw')

    const promise = ctrl.saveSecretWithVault(saveFn)

    await vi.waitFor(() => expect(ctrl.showUnlock()).toBe(true))
    expect(ctrl.showSetup()).toBe(false)
    expect(savePassword).toHaveBeenCalledTimes(1)

    ctrl.onUnsealDone()
    await expect(promise).resolves.toBeUndefined()
    expect(savePassword).toHaveBeenCalledTimes(2)
  })

  it('vault-sealed: unlock reason names the operation', async () => {
    const { client, status } = mockClient()
    status.mockResolvedValue({
      state: 'sealed',
      osKeyAvailable: true,
      hasPassphrase: false,
      autoSealMinutes: 0,
      providers: [],
      defaultProvider: null,
    })

    const ctrl = createVaultState(client)
    await ctrl.refresh()

    const savePassword = vi
      .fn<(...args: string[]) => Promise<void>>()
      .mockRejectedValueOnce(makeRpcError('vault-sealed'))
      .mockResolvedValueOnce(undefined)

    const promise = ctrl.saveSecretWithVault(() => savePassword('my-pw'), 'save this key')

    await vi.waitFor(() => expect(ctrl.showUnlock()).toBe(true))
    expect(ctrl.unlockReason()).toBe('save this key')

    ctrl.onUnsealDone()
    await expect(promise).resolves.toBeUndefined()
  })

  it('non-vault error: propagates to caller', async () => {
    const { client, status } = mockClient()
    status.mockResolvedValue({
      state: 'unsealed',
      osKeyAvailable: false,
      hasPassphrase: false,
      autoSealMinutes: 0,
      providers: [],
      defaultProvider: null,
    })

    const ctrl = createVaultState(client)
    await ctrl.refresh()

    const saveFn = vi.fn().mockRejectedValue(new Error('network error'))
    const promise = ctrl.saveSecretWithVault(saveFn)

    await expect(promise).rejects.toThrow('network error')
  })
  it('silent setup failure: rejects so caller shows error', async () => {
    const { client, status, setup } = mockClient()
    status.mockResolvedValue({
      state: 'uninitialized',
      osKeyAvailable: false,
      osKeyCapable: true,
      hasPassphrase: false,
      autoSealMinutes: 0,
      providers: [],
      defaultProvider: null,
    })
    setup.mockRejectedValue(new Error('secret-service-unavailable'))

    const ctrl = createVaultState(client)
    await ctrl.refresh()

    const saveFn = vi.fn().mockRejectedValueOnce(makeRpcError('vault-uninitialized'))
    const promise = ctrl.saveSecretWithVault(saveFn)

    await expect(promise).rejects.toThrow('secret-service-unavailable')
    expect(saveFn).toHaveBeenCalledTimes(1)
  })

  it('retry failure after unlock: rejects', async () => {
    const { client, status } = mockClient()
    status.mockResolvedValue({
      state: 'sealed',
      osKeyAvailable: false,
      hasPassphrase: false,
      autoSealMinutes: 0,
      providers: [],
      defaultProvider: null,
    })

    const ctrl = createVaultState(client)
    await ctrl.refresh()

    const savePassword = vi
      .fn<(...args: string[]) => Promise<void>>()
      .mockRejectedValueOnce(makeRpcError('vault-sealed'))
      .mockRejectedValueOnce(new Error('still-sealed'))
    const saveFn = () => savePassword('pw')

    const promise = ctrl.saveSecretWithVault(saveFn)

    await vi.waitFor(() => expect(ctrl.showUnlock()).toBe(true))
    ctrl.onUnsealDone()

    await expect(promise).rejects.toThrow('still-sealed')
    expect(savePassword).toHaveBeenCalledTimes(2)
  })

  it('user cancels setup dialog: rejects, abandoning the deferred save', async () => {
    const { client, status } = mockClient()
    status.mockResolvedValue({
      state: 'uninitialized',
      osKeyAvailable: false,
      hasPassphrase: false,
      autoSealMinutes: 0,
      providers: [],
      defaultProvider: null,
    })

    const ctrl = createVaultState(client)
    await ctrl.refresh()

    const savePassword = vi
      .fn<(...args: string[]) => Promise<void>>()
      .mockRejectedValueOnce(makeRpcError('vault-uninitialized'))
    const saveFn = () => savePassword('pw')
    const promise = ctrl.saveSecretWithVault(saveFn)

    await vi.waitFor(() => expect(ctrl.showSetup()).toBe(true))
    ctrl.closeSetup()

    // The caller's promise rejects so it can abandon the operation it
    // started — the save never ran, and nothing may be reported as saved.
    await expect(promise).rejects.toBeInstanceOf(VaultOperationCancelledError)
    expect(savePassword).toHaveBeenCalledTimes(1)
  })

  it('user cancels unlock dialog: rejects, abandoning the deferred save', async () => {
    const { client, status } = mockClient()
    status.mockResolvedValue({
      state: 'sealed',
      osKeyAvailable: false,
      hasPassphrase: false,
      autoSealMinutes: 0,
      providers: [],
      defaultProvider: null,
    })

    const ctrl = createVaultState(client)
    await ctrl.refresh()

    const savePassword = vi
      .fn<(...args: string[]) => Promise<void>>()
      .mockRejectedValueOnce(makeRpcError('vault-sealed'))
    const saveFn = () => savePassword('pw')
    const promise = ctrl.saveSecretWithVault(saveFn)

    await vi.waitFor(() => expect(ctrl.showUnlock()).toBe(true))
    ctrl.closeUnlock()

    await expect(promise).rejects.toBeInstanceOf(VaultOperationCancelledError)
    expect(savePassword).toHaveBeenCalledTimes(1)
  })
})

// ── SetupDialog ────────────────────────────────────────────────────────

describe('SetupDialog', () => {
  it('renders passphrase fields when open', () => {
    const { client } = mockClient()
    render(() => <SetupDialog open={true} onClose={vi.fn()} vaultClient={client} />)
    expect(screen.getByLabelText('Master passphrase')).toBeTruthy()
    expect(screen.getByLabelText('Confirm passphrase')).toBeTruthy()
    expect(screen.getByText('Set Up')).toBeTruthy()
    expect(screen.getByText('Cancel')).toBeTruthy()
  })

  it('calls vaultClient.setup when passphrases match and Set Up is clicked', async () => {
    const { client, setup } = mockClient()
    setup.mockResolvedValue({})
    render(() => <SetupDialog open={true} onClose={vi.fn()} vaultClient={client} />)

    const passphrase = screen.getByLabelText('Master passphrase')
    const confirm = screen.getByLabelText('Confirm passphrase')
    fireEvent.input(passphrase, { target: { value: 'hunter2' } })
    fireEvent.input(confirm, { target: { value: 'hunter2' } })
    fireEvent.click(screen.getByText('Set Up'))

    await vi.waitFor(() => {
      expect(setup).toHaveBeenCalledWith({ passphrase: 'hunter2' })
    })
  })

  it('shows error and does not call setup when passphrases do not match', () => {
    const { client, setup } = mockClient()
    render(() => <SetupDialog open={true} onClose={vi.fn()} vaultClient={client} />)

    const passphrase = screen.getByLabelText('Master passphrase')
    const confirm = screen.getByLabelText('Confirm passphrase')
    fireEvent.input(passphrase, { target: { value: 'hunter2' } })
    fireEvent.input(confirm, { target: { value: 'wrong' } })
    fireEvent.click(screen.getByText('Set Up'))

    const errors = screen.getAllByText('Passphrases do not match')
    expect(errors.length).toBeGreaterThanOrEqual(1)
    expect(setup).not.toHaveBeenCalled()
  })

  it('shows error and does not call setup when passphrase is empty', () => {
    const { client, setup } = mockClient()
    render(() => <SetupDialog open={true} onClose={vi.fn()} vaultClient={client} />)

    fireEvent.click(screen.getByText('Set Up'))

    expect(screen.getByText('Enter a master passphrase')).toBeTruthy()
    expect(setup).not.toHaveBeenCalled()
  })

  it('shows recovery code after setup succeeds', async () => {
    const { client, setup } = mockClient()
    setup.mockResolvedValue({ recoveryCode: 'ABCD-1234-EFGH-5678' })
    render(() => <SetupDialog open={true} onClose={vi.fn()} vaultClient={client} />)

    const passphrase = screen.getByLabelText('Master passphrase')
    const confirm = screen.getByLabelText('Confirm passphrase')
    fireEvent.input(passphrase, { target: { value: 'hunter2' } })
    fireEvent.input(confirm, { target: { value: 'hunter2' } })
    fireEvent.click(screen.getByText('Set Up'))

    await vi.waitFor(() => {
      expect(screen.getByText('ABCD-1234-EFGH-5678')).toBeTruthy()
    })
    expect(screen.getByText('Done')).toBeTruthy()
  })
  it('shows error message when vaultClient.setup rejects', async () => {
    const { client, setup } = mockClient()
    setup.mockRejectedValue(new Error('Backend refused'))
    render(() => <SetupDialog open={true} onClose={vi.fn()} vaultClient={client} />)

    const passphrase = screen.getByLabelText('Master passphrase')
    const confirm = screen.getByLabelText('Confirm passphrase')
    fireEvent.input(passphrase, { target: { value: 'hunter2' } })
    fireEvent.input(confirm, { target: { value: 'hunter2' } })
    fireEvent.click(screen.getByText('Set Up'))

    await vi.waitFor(() => {
      expect(screen.getByText('Backend refused')).toBeTruthy()
    })
    // Dialog stays open, user can retry
    expect(screen.getByText('Set Up')).toBeTruthy()
  })
})

// ── UnlockDialog ───────────────────────────────────────────────────────

describe('UnlockDialog', () => {
  it('calls vaultClient.unseal with os means when OS key is available', async () => {
    const { client, unseal } = mockClient()
    unseal.mockResolvedValue({})
    render(() => (
      <UnlockDialog
        open={true}
        onClose={vi.fn()}
        vaultClient={client}
        vaultStatus={{ ...BASE_STATUS, osKeyAvailable: true }}
      />
    ))

    fireEvent.click(screen.getByText('Unlock'))

    await vi.waitFor(() => {
      expect(unseal).toHaveBeenCalledWith({ means: 'os' })
    })
  })

  it('calls vaultClient.unseal with passphrase when passphrase is entered', async () => {
    const { client, unseal } = mockClient()
    unseal.mockResolvedValue({})
    render(() => (
      <UnlockDialog open={true} onClose={vi.fn()} vaultClient={client} vaultStatus={BASE_STATUS} />
    ))

    const buttons = screen.getAllByText('Passphrase')
    fireEvent.click(buttons[0])
    const input = screen.getByLabelText('Vault passphrase')
    fireEvent.input(input, { target: { value: 'mypass' } })
    fireEvent.click(screen.getByText('Unlock'))

    await vi.waitFor(() => {
      expect(unseal).toHaveBeenCalledWith({ means: 'passphrase', secret: 'mypass' })
    })
  })

  it('calls vaultClient.unseal with recovery code when recovery mode is selected', async () => {
    const { client, unseal } = mockClient()
    unseal.mockResolvedValue({})
    render(() => (
      <UnlockDialog open={true} onClose={vi.fn()} vaultClient={client} vaultStatus={BASE_STATUS} />
    ))

    fireEvent.click(screen.getByText('Recovery code'))
    const input = screen.getByLabelText('Vault recovery code')
    fireEvent.input(input, { target: { value: 'ABCD-1234' } })
    fireEvent.click(screen.getByText('Unlock'))

    await vi.waitFor(() => {
      expect(unseal).toHaveBeenCalledWith({ means: 'recovery', secret: 'ABCD-1234' })
    })
  })

  it('shows error when unlocking with empty passphrase', () => {
    const { client, unseal } = mockClient()
    render(() => (
      <UnlockDialog open={true} onClose={vi.fn()} vaultClient={client} vaultStatus={BASE_STATUS} />
    ))

    const buttons = screen.getAllByText('Passphrase')
    fireEvent.click(buttons[0])
    fireEvent.click(screen.getByText('Unlock'))

    expect(screen.getByText('Enter your vault passphrase')).toBeTruthy()
    expect(unseal).not.toHaveBeenCalled()
  })

  it('reports a refused passphrase as a toast, naming what was refused', async () => {
    clearToasts()
    const { client, unseal } = mockClient()
    // What the backend actually sends when the passphrase does not fit: the
    // reason code, not prose. It used to reach the user as the literal words
    // "unseal failed", because REASON_MESSAGES had no line for it.
    unseal.mockRejectedValue(new RpcError('unseal failed', -32003, { reason: 'unseal-failed' }))
    const onUnsealed = vi.fn()
    render(() => (
      <>
        <UnlockDialog
          open={true}
          onClose={vi.fn()}
          vaultClient={client}
          vaultStatus={BASE_STATUS}
          onUnsealed={onUnsealed}
        />
        <ToastHost />
      </>
    ))

    const buttons = screen.getAllByText('Passphrase')
    fireEvent.click(buttons[0])
    const input = screen.getByLabelText('Vault passphrase')
    fireEvent.input(input, { target: { value: 'wrongpw' } })
    fireEvent.click(screen.getByText('Unlock'))

    await vi.waitFor(() => {
      const toast = document.querySelector('.ui-toast')
      expect(toast).toBeTruthy()
      expect(toast!.getAttribute('data-level')).toBe('danger')
      expect(toast!.textContent).toBe('That passphrase does not unlock this vault.')
    })
    // The property that makes it reachable rather than merely present: the
    // toast is only visible if it is rendered INSIDE the topmost overlay.
    // ToastHost portals itself there, and the Unlock prompt registers itself
    // as one — a Prompt pushes its element onto the overlay stack, exactly
    // like a modal dialog does. Asserting presence alone would pass either
    // way, because jsdom has no top layer to hide anything.
    expect(document.querySelector('.ui-toast')!.closest('.ui-prompt-overlay')).not.toBeNull()
    // Never the backend's own words.
    expect(document.body.textContent).not.toContain('unseal failed')
    // Prompt stays open, onUnsealed not called
    expect(onUnsealed).not.toHaveBeenCalled()
  })

  // Enter is what a person presses in a passphrase prompt without thinking.
  // The Prompt's onSubmit offers it with the same contract Dialog did; this
  // prompt passes one, so Enter unlocks.
  it('unlocks on Enter in the passphrase field', async () => {
    const { client, unseal } = mockClient()
    unseal.mockResolvedValue({})
    render(() => (
      <UnlockDialog open={true} onClose={vi.fn()} vaultClient={client} vaultStatus={BASE_STATUS} />
    ))

    fireEvent.click(screen.getAllByText('Passphrase')[0])
    const input = screen.getByLabelText('Vault passphrase')
    fireEvent.input(input, { target: { value: 'my-passphrase' } })
    fireEvent.keyDown(input, { key: 'Enter' })

    await vi.waitFor(() => {
      expect(unseal).toHaveBeenCalledWith({ means: 'passphrase', secret: 'my-passphrase' })
    })
  })

  // An empty box is not an outcome to report — it is a correction to make in
  // the box, which is still on screen and clears as you type. Outcomes go to
  // toasts; field validation stays in the field.
  it('keeps the empty-field prompt in the field rather than raising a toast', () => {
    clearToasts()
    const { client, unseal } = mockClient()
    render(() => (
      <>
        <UnlockDialog
          open={true}
          onClose={vi.fn()}
          vaultClient={client}
          vaultStatus={BASE_STATUS}
        />
        <ToastHost />
      </>
    ))

    fireEvent.click(screen.getAllByText('Passphrase')[0])
    fireEvent.click(screen.getByText('Unlock'))

    expect(screen.getByText('Enter your vault passphrase')).toBeTruthy()
    expect(document.querySelector('.ui-toast')).toBeNull()
    expect(unseal).not.toHaveBeenCalled()
  })

  // Every password prompt must say WHICH password it wants and why it is
  // asking now (nocx-s8jn). A bare "Unlock the vault" cannot be told apart
  // from the key and connection prompts; the reason names the operation.
  it('names the operation that triggered it in the title', () => {
    const { client } = mockClient()
    render(() => (
      <UnlockDialog
        open={true}
        onClose={vi.fn()}
        vaultClient={client}
        vaultStatus={BASE_STATUS}
        reason="save this connection"
      />
    ))
    expect(screen.getByText('Unlock the vault to save this connection')).toBeTruthy()
    // The field says which password, not just "Passphrase".
    expect(screen.getByLabelText('Vault passphrase')).toBeTruthy()
  })

  it('falls back to a bare title when no operation is given', () => {
    const { client } = mockClient()
    render(() => (
      <UnlockDialog open={true} onClose={vi.fn()} vaultClient={client} vaultStatus={BASE_STATUS} />
    ))
    expect(screen.getByText('Unlock the vault')).toBeTruthy()
  })
})

// ── ChangePassphraseDialog ─────────────────────────────────────────────

describe('ChangePassphraseDialog', () => {
  it('renders passphrase mode by default', () => {
    const { client } = mockClient()
    render(() => <ChangePassphraseDialog open={true} onClose={vi.fn()} vaultClient={client} />)
    expect(screen.getByText('I know my passphrase')).toBeTruthy()
    expect(screen.getByText('I have a recovery code')).toBeTruthy()
    expect(screen.getByLabelText('Current vault passphrase')).toBeTruthy()
    expect(screen.getByLabelText('New vault passphrase')).toBeTruthy()
    expect(screen.getByLabelText('Confirm new vault passphrase')).toBeTruthy()
  })

  it('calls changePassphrase with old passphrase on submit', async () => {
    const { client, changePassphrase } = mockClient()
    changePassphrase.mockResolvedValue({})
    render(() => <ChangePassphraseDialog open={true} onClose={vi.fn()} vaultClient={client} />)

    fireEvent.input(screen.getByLabelText('Current vault passphrase'), {
      target: { value: 'oldpw' },
    })
    fireEvent.input(screen.getByLabelText('New vault passphrase'), { target: { value: 'newpw' } })
    fireEvent.input(screen.getByLabelText('Confirm new vault passphrase'), {
      target: { value: 'newpw' },
    })
    fireEvent.click(screen.getByText('Change passphrase'))

    await vi.waitFor(() => {
      expect(changePassphrase).toHaveBeenCalledWith({
        oldPassphrase: 'oldpw',
        newPassphrase: 'newpw',
      })
    })
  })

  it('switches to recovery code mode', () => {
    const { client } = mockClient()
    render(() => <ChangePassphraseDialog open={true} onClose={vi.fn()} vaultClient={client} />)

    fireEvent.click(screen.getByText('I have a recovery code'))
    expect(() => screen.getByLabelText('Current vault passphrase')).toThrow()
    expect(screen.getByLabelText('Vault recovery code')).toBeTruthy()
    expect(screen.getByLabelText('New vault passphrase')).toBeTruthy()
  })

  it('calls changePassphrase with recovery code in recovery mode', async () => {
    const { client, changePassphrase } = mockClient()
    changePassphrase.mockResolvedValue({})
    render(() => <ChangePassphraseDialog open={true} onClose={vi.fn()} vaultClient={client} />)

    fireEvent.click(screen.getByText('I have a recovery code'))
    fireEvent.input(screen.getByLabelText('Vault recovery code'), {
      target: { value: 'ABCD-1234' },
    })
    fireEvent.input(screen.getByLabelText('New vault passphrase'), { target: { value: 'newpw' } })
    fireEvent.input(screen.getByLabelText('Confirm new vault passphrase'), {
      target: { value: 'newpw' },
    })
    fireEvent.click(screen.getByText('Change passphrase'))

    await vi.waitFor(() => {
      expect(changePassphrase).toHaveBeenCalledWith({
        recoveryCode: 'ABCD-1234',
        newPassphrase: 'newpw',
      })
    })
  })

  it('shows error when passphrases do not match', () => {
    const { client, changePassphrase } = mockClient()
    render(() => <ChangePassphraseDialog open={true} onClose={vi.fn()} vaultClient={client} />)

    fireEvent.input(screen.getByLabelText('Current vault passphrase'), {
      target: { value: 'oldpw' },
    })
    fireEvent.input(screen.getByLabelText('New vault passphrase'), { target: { value: 'newpw' } })
    fireEvent.input(screen.getByLabelText('Confirm new vault passphrase'), {
      target: { value: 'different' },
    })
    fireEvent.click(screen.getByText('Change passphrase'))

    expect(screen.getByText('Passphrases do not match')).toBeTruthy()
    expect(changePassphrase).not.toHaveBeenCalled()
  })

  it('shows error when changePassphrase rejects', async () => {
    const { client, changePassphrase } = mockClient()
    changePassphrase.mockRejectedValue(new RpcError('wrong', -32000, { reason: 'denied' }))
    render(() => <ChangePassphraseDialog open={true} onClose={vi.fn()} vaultClient={client} />)

    fireEvent.input(screen.getByLabelText('Current vault passphrase'), {
      target: { value: 'wrong' },
    })
    fireEvent.input(screen.getByLabelText('New vault passphrase'), { target: { value: 'newpw' } })
    fireEvent.input(screen.getByLabelText('Confirm new vault passphrase'), {
      target: { value: 'newpw' },
    })
    fireEvent.click(screen.getByText('Change passphrase'))

    await vi.waitFor(() => {
      expect(screen.getByText('Access to the system keyring was denied.')).toBeTruthy()
    })
  })
})

// ── RecoveryCodeDialog ─────────────────────────────────────────────────

describe('RecoveryCodeDialog', () => {
  it('shows passphrase input first', () => {
    const { client } = mockClient()
    render(() => <RecoveryCodeDialog open={true} onClose={vi.fn()} vaultClient={client} />)
    expect(screen.getByLabelText('Current vault passphrase')).toBeTruthy()
    expect(screen.getByText('Generate new recovery code')).toBeTruthy()
  })

  it('calls regenerateRecovery with passphrase', async () => {
    const { client, regenerateRecovery } = mockClient()
    regenerateRecovery.mockResolvedValue({ recoveryCode: 'NEW-CODE-1234' })
    const onClose = vi.fn()
    render(() => <RecoveryCodeDialog open={true} onClose={onClose} vaultClient={client} />)

    fireEvent.input(screen.getByLabelText('Current vault passphrase'), {
      target: { value: 'mypw' },
    })
    fireEvent.click(screen.getByText('Generate new recovery code'))

    await vi.waitFor(() => {
      expect(regenerateRecovery).toHaveBeenCalledWith({ passphrase: 'mypw' })
    })
    await vi.waitFor(() => {
      expect(screen.getByText('NEW-CODE-1234')).toBeTruthy()
    })
  })

  it('shows error when regenerateRecovery rejects', async () => {
    const { client, regenerateRecovery } = mockClient()
    regenerateRecovery.mockRejectedValue(new RpcError('bad', -32000, { reason: 'denied' }))
    render(() => <RecoveryCodeDialog open={true} onClose={vi.fn()} vaultClient={client} />)

    fireEvent.input(screen.getByLabelText('Current vault passphrase'), {
      target: { value: 'wrong' },
    })
    fireEvent.click(screen.getByText('Generate new recovery code'))

    await vi.waitFor(() => {
      expect(screen.getByText('Access to the system keyring was denied.')).toBeTruthy()
    })
  })

  it('shows recovery code once, then Done returns to the passphrase screen on reopen', async () => {
    const { client, regenerateRecovery } = mockClient()
    regenerateRecovery.mockResolvedValue({ recoveryCode: 'SECRET-CODE' })
    render(() => <RecoveryCodeDialog open={true} onClose={vi.fn()} vaultClient={client} />)

    fireEvent.input(screen.getByLabelText('Current vault passphrase'), {
      target: { value: 'mypw' },
    })
    fireEvent.click(screen.getByText('Generate new recovery code'))

    await vi.waitFor(() => {
      expect(screen.getByText('SECRET-CODE')).toBeTruthy()
    })
    expect(screen.getByText('Done')).toBeTruthy()
    expect(() => screen.getByText('Generate new recovery code')).toThrow()
  })
})

// ── VaultSection ───────────────────────────────────────────────────────

describe('VaultSection', () => {
  const UNSEALED_STATUS = {
    state: 'unsealed' as const,
    osKeyAvailable: true,
    hasPassphrase: true,
    autoSealMinutes: 0,
    providers: [{ id: 'keychain', writable: true, ready: true }],
    defaultProvider: 'keychain',
  }
  const SEALED_STATUS = {
    state: 'sealed' as const,
    osKeyAvailable: false,
    hasPassphrase: false,
    autoSealMinutes: 0,
    providers: [{ id: 'secret-service', writable: true, ready: true }],
    defaultProvider: null,
  }
  const UNINIT_STATUS = {
    state: 'uninitialized' as const,
    osKeyAvailable: false,
    hasPassphrase: false,
    autoSealMinutes: 0,
    providers: [],
    defaultProvider: null,
  }

  async function renderVaultSection(mockStatus: object) {
    const { client } = mockClient()
    ;(client.status as ReturnType<typeof vi.fn>).mockResolvedValue(mockStatus)
    const ctrl = createVaultState(client)
    await ctrl.refresh()
    render(() => <VaultSection vaultClient={client} vaultController={ctrl} />)
    return { client, ctrl }
  }

  /** Return the primary button within the state card at the top of the page. */
  function statusRowPrimary(): HTMLElement | null {
    const card = document.querySelector('.ui-status-card')
    if (!card) return null
    return card.querySelector('button[data-variant="primary"]')
  }

  // ── Acceptance 1: primary action by state ─────────────────────────

  it('shows Set up protection as primary for uninitialized', async () => {
    await renderVaultSection(UNINIT_STATUS)
    const btn = statusRowPrimary()
    expect(btn).toBeTruthy()
    expect(btn!.textContent).toBe('Set up protection')
    expect(btn!.getAttribute('disabled')).toBeNull()
    expect(btn!.getAttribute('data-variant')).toBe('primary')
  })

  it('shows Unlock as primary for sealed', async () => {
    await renderVaultSection(SEALED_STATUS)
    const btn = statusRowPrimary()
    expect(btn).toBeTruthy()
    expect(btn!.textContent).toBe('Unlock')
    expect(btn!.getAttribute('disabled')).toBeNull()
    expect(btn!.getAttribute('data-variant')).toBe('primary')
  })

  it('shows Lock now as primary for unsealed', async () => {
    await renderVaultSection(UNSEALED_STATUS)
    const btn = statusRowPrimary()
    expect(btn).toBeTruthy()
    expect(btn!.textContent).toBe('Lock now')
    expect(btn!.getAttribute('disabled')).toBeNull()
    expect(btn!.getAttribute('data-variant')).toBe('primary')
  })

  it('exactly one primary button in the state card per state', async () => {
    await renderVaultSection(UNINIT_STATUS)
    let card = document.querySelector('.ui-status-card')
    expect(card!.querySelectorAll('button[data-variant="primary"]').length).toBe(1)
    cleanup()

    const { client } = mockClient()
    ;(client.status as ReturnType<typeof vi.fn>).mockResolvedValue(SEALED_STATUS)
    const ctrl = createVaultState(client)
    await ctrl.refresh()
    render(() => <VaultSection vaultClient={client} vaultController={ctrl} />)
    card = document.querySelector('.ui-status-card')
    expect(card!.querySelectorAll('button[data-variant="primary"]').length).toBe(1)
  })

  it('the card carries the tone of the state it announces', async () => {
    await renderVaultSection(UNSEALED_STATUS)
    expect(document.querySelector('.ui-status-card')!.getAttribute('data-tone')).toBe('ok')
    cleanup()

    const { client } = mockClient()
    ;(client.status as ReturnType<typeof vi.fn>).mockResolvedValue(UNINIT_STATUS)
    const ctrl = createVaultState(client)
    await ctrl.refresh()
    render(() => <VaultSection vaultClient={client} vaultController={ctrl} />)
    expect(document.querySelector('.ui-status-card')!.getAttribute('data-tone')).toBe('warning')
  })

  // ── Primary action behavior ───────────────────────────────────────

  it('Set up protection opens SetupDialog for uninitialized', async () => {
    const { client } = mockClient()
    ;(client.status as ReturnType<typeof vi.fn>).mockResolvedValue(UNINIT_STATUS)
    const ctrl = createVaultState(client)
    await ctrl.refresh()
    render(() => <VaultSection vaultClient={client} vaultController={ctrl} />)

    fireEvent.click(statusRowPrimary()!)
    expect(screen.getByText('Set Up Vault')).toBeTruthy()
  })

  it('Unlock calls vaultController.openUnlock for sealed', async () => {
    const { client } = mockClient()
    ;(client.status as ReturnType<typeof vi.fn>).mockResolvedValue(SEALED_STATUS)
    const ctrl = createVaultState(client)
    const openUnlockSpy = vi.spyOn(ctrl, 'openUnlock')
    await ctrl.refresh()
    render(() => <VaultSection vaultClient={client} vaultController={ctrl} />)

    fireEvent.click(statusRowPrimary()!)
    await vi.waitFor(() => {
      expect(openUnlockSpy).toHaveBeenCalled()
    })
  })

  // ── Acceptance 2: store rows with status markers ───────────────────

  // Every store's name and every store's state are on screen together. The
  // rail this replaced showed one and hid the rest behind a click, so the
  // store that was not answering was reliably the one not being looked at.
  it('names every store and states every store, all at once', async () => {
    const status = {
      state: 'unsealed' as const,
      osKeyAvailable: true,
      hasPassphrase: true,
      autoSealMinutes: 0,
      providers: [
        { id: 'system', writable: true, ready: true },
        { id: 'file', writable: true, ready: false, reason: 'locked' },
      ],
      defaultProvider: 'system',
    }
    await renderVaultSection(status)
    const rows = document.querySelectorAll('.ui-vault-store')
    expect(rows.length).toBe(2)

    const text = Array.from(rows).map((r) => r.textContent ?? '')
    expect(text[0]).toContain('System keychain')
    expect(text[0]).toContain('is available and answering')
    expect(text[1]).toContain('Encrypted nocx storage')
    expect(text[1]).toContain('Not answering')
  })

  // A name that does not fit is a name the user cannot read. The rail clipped
  // both of these at 9.5rem, and clipped them silently.
  it('leaves no store name to be truncated by a fixed-width container', async () => {
    const status = {
      state: 'unsealed' as const,
      osKeyAvailable: true,
      hasPassphrase: true,
      autoSealMinutes: 0,
      providers: [
        { id: 'system', writable: true, ready: true },
        { id: 'file', writable: true, ready: true },
      ],
      defaultProvider: 'system',
    }
    await renderVaultSection(status)
    const names = Array.from(document.querySelectorAll('.ui-vault-store__name')).map(
      (n) => n.textContent,
    )
    expect(names).toEqual(['System keychain', 'Encrypted nocx storage'])
    // Nothing in the section is a scroll container, so nothing in it can grow
    // a scrollbar over a name.
    expect(document.querySelector('.ui-vault-store [style*="overflow"]')).toBeNull()
  })

  it('each store carries a status dot toned to its health', async () => {
    const status = {
      state: 'unsealed' as const,
      osKeyAvailable: true,
      hasPassphrase: true,
      autoSealMinutes: 0,
      providers: [
        { id: 'system', writable: true, ready: true },
        { id: 'file', writable: true, ready: false, reason: 'locked' },
      ],
      defaultProvider: 'system',
    }
    await renderVaultSection(status)
    const tones = Array.from(document.querySelectorAll('.ui-status-dot')).map((d) =>
      d.getAttribute('data-tone'),
    )
    expect(tones).toEqual(['ok', 'error'])
  })

  it('unready store identifiable without selecting it', async () => {
    const status = {
      state: 'unsealed' as const,
      osKeyAvailable: true,
      hasPassphrase: true,
      autoSealMinutes: 0,
      providers: [
        { id: 'system', writable: true, ready: true },
        { id: 'file', writable: true, ready: false, reason: 'locked' },
      ],
      defaultProvider: 'system',
    }
    await renderVaultSection(status)
    // The visually-hidden span contains the REASON_MESSAGES sentence
    const hiddenSpans = document.querySelectorAll('.ui-visually-hidden')
    const lockedMsg = Array.from(hiddenSpans).find((s) =>
      s.textContent?.includes('Your login keychain is locked'),
    )
    expect(lockedMsg).toBeTruthy()
  })

  // ── Acceptance 3: store panel shows state sentence ────────────────

  it('store panel shows state as sentence with remedy, not a reason code', async () => {
    const status = {
      state: 'unsealed' as const,
      osKeyAvailable: true,
      hasPassphrase: true,
      autoSealMinutes: 0,
      providers: [{ id: 'file', writable: false, ready: false, reason: 'locked' }],
      defaultProvider: null,
    }
    await renderVaultSection(status)
    // The panel shows a sentence starting with "Not answering:"
    expect(screen.getByText(/Not answering: Your login keychain is locked/)).toBeTruthy()
  })

  it('ready store panel shows availability sentence', async () => {
    await renderVaultSection(UNSEALED_STATUS)
    expect(screen.getByText(/is available and answering/)).toBeTruthy()
  })

  // ── Acceptance 4: protection actions on no-passphrase vault ───────

  it('change passphrase and recovery disabled with explanation when no passphrase', async () => {
    const status = {
      state: 'unsealed' as const,
      hasPassphrase: false,
      osKeyAvailable: true,
      autoSealMinutes: 0,
      providers: [{ id: 'keychain', writable: true, ready: true }],
      defaultProvider: 'keychain',
    }
    await renderVaultSection(status)
    expect(
      screen.getByRole('button', { name: 'Change passphrase' }).getAttribute('disabled'),
    ).not.toBeNull()
    expect(
      screen.getByRole('button', { name: 'Reissue recovery code' }).getAttribute('disabled'),
    ).not.toBeNull()
    // Description appears twice (once per field)
    expect(
      screen.getAllByText(
        'This vault is held by an OS key alone, so there is no passphrase to change or recover.',
      ).length,
    ).toBeGreaterThanOrEqual(1)
  })

  it('action buttons enabled on unsealed with passphrase', async () => {
    await renderVaultSection(UNSEALED_STATUS)
    expect(
      screen.getByRole('button', { name: 'Change passphrase' }).getAttribute('disabled'),
    ).toBeNull()
    expect(
      screen.getByRole('button', { name: 'Reissue recovery code' }).getAttribute('disabled'),
    ).toBeNull()
  })

  it('protection actions disabled on uninitialized with explanation', async () => {
    await renderVaultSection(UNINIT_STATUS)
    expect(
      screen.getByRole('button', { name: 'Change passphrase' }).getAttribute('disabled'),
    ).not.toBeNull()
    expect(
      screen.getByRole('button', { name: 'Reissue recovery code' }).getAttribute('disabled'),
    ).not.toBeNull()
    expect(
      screen.getAllByText('Set up protection first; there is nothing to change yet.').length,
    ).toBeGreaterThanOrEqual(1)
  })

  it('protection actions disabled on sealed with explanation', async () => {
    await renderVaultSection(SEALED_STATUS)
    expect(
      screen.getByRole('button', { name: 'Change passphrase' }).getAttribute('disabled'),
    ).not.toBeNull()
    expect(
      screen.getByRole('button', { name: 'Reissue recovery code' }).getAttribute('disabled'),
    ).not.toBeNull()
    expect(
      screen.getAllByText('Unlock the vault to change how it is protected.').length,
    ).toBeGreaterThanOrEqual(1)
  })

  // ── Locking is offered once, by the state card ────────────────────
  // There used to be two "Lock now" buttons — the card's and a disabled one in
  // Protection with "Vault is locked." underneath it. A control that cannot
  // run is not information; the card already says the state.

  it('offers Lock now exactly once, and only while the vault is unlocked', async () => {
    await renderVaultSection(UNSEALED_STATUS)
    const enabled = screen
      .getAllByText('Lock now')
      .filter((el) => el.tagName === 'BUTTON' && !el.hasAttribute('disabled'))
    expect(enabled.length).toBe(1)
    expect(enabled[0].closest('.ui-status-card')).not.toBeNull()
  })

  it('does not offer Lock now at all when the vault is already locked', async () => {
    await renderVaultSection(SEALED_STATUS)
    expect(screen.queryAllByText('Lock now').filter((el) => el.tagName === 'BUTTON')).toEqual([])
  })

  it('does not offer Lock now at all when protection is not set up', async () => {
    await renderVaultSection(UNINIT_STATUS)
    expect(screen.queryAllByText('Lock now').filter((el) => el.tagName === 'BUTTON')).toEqual([])
  })

  // The reason the protection controls are unavailable is stated for the
  // section, once — not repeated under every control the state disabled.
  it('states why protection cannot be changed exactly once', async () => {
    await renderVaultSection(SEALED_STATUS)
    const stated = screen.getAllByText('Unlock the vault to change how it is protected.')
    expect(stated.length).toBe(1)
    expect(stated[0].classList.contains('ui-page-section__desc')).toBe(true)
  })
  // ── Auto-lock select ──────────────────────────────────────────────

  it('auto-lock select round-trips: set then refresh shows updated value', async () => {
    const { client, setAutoSeal } = mockClient()
    ;(client.status as ReturnType<typeof vi.fn>).mockResolvedValue({
      ...UNSEALED_STATUS,
      autoSealMinutes: 0,
    })
    const ctrl = createVaultState(client)
    await ctrl.refresh()

    render(() => <VaultSection vaultClient={client} vaultController={ctrl} />)
    // Find the auto-lock select by looking for "Never" option text.
    const allSelects = document.querySelectorAll('select.ui-select')
    const selectEl = Array.from(allSelects).find(
      (s) => s.querySelector('option[value="0"]')?.textContent === 'Never',
    ) as HTMLSelectElement
    expect(selectEl.value).toBe('0')

    // Change to 30 minutes.
    fireEvent.change(selectEl, { target: { value: '30' } })

    await vi.waitFor(() => {
      expect(setAutoSeal).toHaveBeenCalledWith(30)
    })

    // Simulate round-trip: status refresh returns updated value.
    ;(client.status as ReturnType<typeof vi.fn>).mockResolvedValue({
      ...UNSEALED_STATUS,
      autoSealMinutes: 30,
    })
    await ctrl.refresh()

    // Re-render and verify select shows the new value.
    render(() => <VaultSection vaultClient={client} vaultController={ctrl} />)
    const updatedSelects = document.querySelectorAll('select.ui-select')
    const updatedSelect = Array.from(updatedSelects).find(
      (s) => s.querySelector('option[value="0"]')?.textContent === 'Never',
    ) as HTMLSelectElement
    expect(updatedSelect.value).toBe('30')
  })

  // ── Default provider ("Store new secrets here") ───────────────────

  it('the default store is marked, and is not offered the button to become one', async () => {
    await renderVaultSection(UNSEALED_STATUS)
    const row = document.querySelector('.ui-vault-store')!
    expect(row.querySelector('.ui-badge')!.textContent).toBe('New secrets go here')
    expect(Array.from(row.querySelectorAll('button')).map((b) => b.textContent)).not.toContain(
      'Store new secrets here',
    )
  })

  // A store that is not answering cannot be told to take new secrets: the write
  // would fail at the moment the user saves a password, which is the worst
  // moment to find out. Measured on a machine with no Secret Service, where the
  // action was enabled on the store that had just reported no-service.
  it('refuses to offer an unreachable store as the place for new secrets', async () => {
    const status = {
      state: 'unsealed' as const,
      osKeyAvailable: false,
      hasPassphrase: true,
      autoSealMinutes: 0,
      providers: [
        { id: 'system', writable: true, ready: false, reason: 'no-service' },
        { id: 'file', writable: true, ready: true },
      ],
      defaultProvider: 'file',
    }
    await renderVaultSection(status)

    const btn = screen
      .getAllByText('Store new secrets here')
      .find((el) => el.tagName === 'BUTTON') as HTMLButtonElement
    expect(btn).toBeTruthy()
    expect(btn.hasAttribute('disabled')).toBe(true)
    // It is not a ghost. A control that decides where every future password
    // lands has to look like something you press.
    expect(btn.getAttribute('data-variant')).toBe('default')
  })

  it('offers a reachable read-only store nothing either', async () => {
    const status = {
      state: 'unsealed' as const,
      osKeyAvailable: false,
      hasPassphrase: true,
      autoSealMinutes: 0,
      providers: [
        { id: 'system', writable: false, ready: true },
        { id: 'file', writable: true, ready: true },
      ],
      defaultProvider: 'file',
    }
    await renderVaultSection(status)
    const btn = screen
      .getAllByText('Store new secrets here')
      .find((el) => el.tagName === 'BUTTON') as HTMLButtonElement
    expect(btn.hasAttribute('disabled')).toBe(true)
  })

  it('non-default store shows Store new secrets here button', async () => {
    const status = {
      state: 'unsealed' as const,
      osKeyAvailable: true,
      hasPassphrase: true,
      autoSealMinutes: 0,
      providers: [
        { id: 'system', writable: true, ready: true },
        { id: 'file', writable: true, ready: true },
      ],
      defaultProvider: 'system',
    }
    const { client, setDefaultProvider } = mockClient()
    setDefaultProvider.mockResolvedValue({})
    ;(client.status as ReturnType<typeof vi.fn>).mockResolvedValue(status)
    const ctrl = createVaultState(client)
    await ctrl.refresh()
    render(() => <VaultSection vaultClient={client} vaultController={ctrl} />)

    // Find "Store new secrets here" button for the non-default store
    const storeBtns = screen
      .getAllByText('Store new secrets here')
      .filter((el) => el.tagName === 'BUTTON')
    expect(storeBtns.length).toBeGreaterThan(0)
    fireEvent.click(storeBtns[0])

    await vi.waitFor(() => {
      expect(setDefaultProvider).toHaveBeenCalledWith({ provider: 'file' })
    })
  })

  // ── Diagnostics section ───────────────────────────────────────────

  it('diagnostics section contains raw provider info', async () => {
    await renderVaultSection(UNSEALED_STATUS)
    const details = document.querySelector('details.ui-vault-diagnostics')
    expect(details).toBeTruthy()
    // The details contains the summary text
    expect(details!.textContent).toContain('Diagnostics')
    // Raw state text appears inside details
    expect(details!.textContent).toContain('unsealed')
  })

  // ── State transitions ─────────────────────────────────────────────

  it('page reflects new state after setup completes', async () => {
    const { client, setup } = mockClient()
    setup.mockResolvedValue({ recoveryCode: 'test-recovery' })
    ;(client.status as ReturnType<typeof vi.fn>).mockResolvedValue(UNINIT_STATUS)
    const ctrl = createVaultState(client)
    await ctrl.refresh()
    render(() => <VaultSection vaultClient={client} vaultController={ctrl} />)

    // Click Set up protection to open dialog
    fireEvent.click(statusRowPrimary()!)
    expect(screen.getByText('Set Up Vault')).toBeTruthy()

    // Fill in passphrase
    const passInput = screen.getByLabelText('Master passphrase')
    fireEvent.input(passInput, { target: { value: 'my-passphrase' } })
    const confirmInput = screen.getByLabelText('Confirm passphrase')
    fireEvent.input(confirmInput, { target: { value: 'my-passphrase' } })

    // Click Set Up button inside dialog
    const setupBtn = screen.getAllByText('Set Up').find((el) => el.tagName === 'BUTTON')
    fireEvent.click(setupBtn!)

    await vi.waitFor(() => {
      expect(setup).toHaveBeenCalledWith({ passphrase: 'my-passphrase' })
    })

    // Recovery code shown — now mock refresh to return sealed status
    ;(client.status as ReturnType<typeof vi.fn>).mockResolvedValue(SEALED_STATUS)
    // Click Done
    const doneBtn = screen.getAllByText('Done').find((el) => el.tagName === 'BUTTON')
    fireEvent.click(doneBtn!)

    // After Done: onSetupComplete fires refresh + onSetupDone, then onClose resets dialog
    await vi.waitFor(() => {
      // The page should now show Unlock (from refreshed sealed status)
      expect(screen.getByRole('button', { name: 'Unlock' })).toBeTruthy()
    })
  })

  // ── Acceptance 5: no secret values ────────────────────────────────

  it('no secret value appears anywhere in rendered output', async () => {
    await renderVaultSection(UNSEALED_STATUS)
    const bodyText = document.body.textContent ?? ''
    // Should not contain secret-like patterns
    expect(bodyText).not.toMatch(/sec:v1:/)
  })

  // ── The state card says what the state means ──────────────────────
  // Not one sentence about the page for every state, which said nothing about
  // the state the user is actually in.

  it('the card explains what being locked means for the user', async () => {
    await renderVaultSection(SEALED_STATUS)
    const card = document.querySelector('.ui-status-card')!
    expect(card.querySelector('.ui-status-card__title')!.textContent).toBe('Vault is locked')
    expect(card.querySelector('.ui-status-card__desc')!.textContent).toContain(
      'stay encrypted until you unlock',
    )
  })

  it('the card explains what being unlocked means, and when it will lock again', async () => {
    const { client } = mockClient()
    ;(client.status as ReturnType<typeof vi.fn>).mockResolvedValue({
      ...UNSEALED_STATUS,
      autoSealMinutes: 15,
    })
    const ctrl = createVaultState(client)
    await ctrl.refresh()
    render(() => <VaultSection vaultClient={client} vaultController={ctrl} />)
    const card = document.querySelector('.ui-status-card')!
    expect(card.querySelector('.ui-status-card__title')!.textContent).toBe('Vault is unlocked')
    expect(card.querySelector('.ui-status-card__desc')!.textContent).toContain(
      'lock again after 15 minutes idle',
    )
  })

  it('the card says protection is missing before it says anything else', async () => {
    await renderVaultSection(UNINIT_STATUS)
    const card = document.querySelector('.ui-status-card')!
    expect(card.querySelector('.ui-status-card__title')!.textContent).toBe(
      'Protection is not set up yet',
    )
  })

  // ── Test button ───────────────────────────────────────────────────

  it('Test button is present on store panels', async () => {
    await renderVaultSection(UNSEALED_STATUS)
    const testButtons = screen.getAllByText('Test').filter((el) => el.tagName === 'BUTTON')
    expect(testButtons.length).toBeGreaterThan(0)
  })

  // Pressing Test on a store that was already failing used to change exactly
  // one line of small print — the timestamp — and nothing else, because the dot
  // and the sentence only move when the answer moves, and the common case for a
  // Test press is that it does not. The button read as broken. It has to say
  // what it found, every time.
  /** Renders the section with the toast overlay the app mounts, so a test can
   *  see what the user would see rather than what the store holds. */
  async function renderWithToasts(mockStatus: object) {
    clearToasts()
    const { client } = mockClient()
    ;(client.status as ReturnType<typeof vi.fn>).mockResolvedValue(mockStatus)
    const ctrl = createVaultState(client)
    await ctrl.refresh()
    render(() => (
      <>
        <VaultSection vaultClient={client} vaultController={ctrl} />
        <ToastHost />
      </>
    ))
    return { client, ctrl }
  }

  it('Test says what the store answered, not merely when it was asked', async () => {
    const status = {
      state: 'unsealed' as const,
      osKeyAvailable: false,
      hasPassphrase: true,
      autoSealMinutes: 0,
      providers: [{ id: 'system', writable: true, ready: false, reason: 'no-service' }],
      defaultProvider: null,
    }
    await renderWithToasts(status)

    const testBtn = screen.getAllByText('Test').find((el) => el.tagName === 'BUTTON')!
    fireEvent.click(testBtn)

    await vi.waitFor(() => {
      const toast = document.querySelector('.ui-toast')
      expect(toast).toBeTruthy()
      expect(toast!.getAttribute('data-level')).toBe('danger')
      expect(toast!.textContent).toContain('No system keyring available')
    })
  })

  it('Test reports success on a store that answers', async () => {
    await renderWithToasts(UNSEALED_STATUS)

    const testBtn = screen.getAllByText('Test').find((el) => el.tagName === 'BUTTON')!
    fireEvent.click(testBtn)

    await vi.waitFor(() => {
      const toast = document.querySelector('.ui-toast')
      expect(toast).toBeTruthy()
      expect(toast!.getAttribute('data-level')).toBe('success')
      expect(toast!.textContent).toContain('is available and answering')
    })
  })

  // ── Diagnostics values are badges ─────────────────────────────────

  it('states every diagnostics value as a badge, toned to what it means', async () => {
    const status = {
      state: 'unsealed' as const,
      osKeyAvailable: false,
      hasPassphrase: true,
      autoSealMinutes: 0,
      providers: [
        { id: 'system', writable: true, ready: false, reason: 'no-service' },
        { id: 'file', writable: true, ready: true },
      ],
      defaultProvider: 'file',
    }
    await renderVaultSection(status)
    const details = document.querySelector('details.ui-vault-diagnostics')!

    const badges = Array.from(details.querySelectorAll('.ui-badge')).map((b) => [
      b.textContent,
      b.getAttribute('data-tone'),
    ])
    expect(badges).toContainEqual(['unsealed', 'success'])
    expect(badges).toContainEqual(['Not available', 'neutral'])
    expect(badges).toContainEqual(['Not ready', 'danger'])
    expect(badges).toContainEqual(['Ready', 'success'])
    // The raw reason code, verbatim — this is the line that goes in a bug
    // report, so it must not be reworded into a sentence here.
    expect(badges).toContainEqual(['no-service', 'danger'])
  })

  // The rows were a bare <div>, so nothing spaced them and they sat flush
  // against each other. Vertical rhythm in this kit is Stack's job, and a
  // surface is not allowed to substitute margins for it — the CSS integrity
  // gate refuses that. So the assertion is that the rows are IN a Stack, which
  // is the thing that cannot be true and unspaced at the same time.
  it('spaces the diagnostics rows with the Stack primitive, not by touching', async () => {
    await renderVaultSection(UNSEALED_STATUS)
    const details = document.querySelector('details.ui-vault-diagnostics')!
    const stack = details.querySelector(':scope > .ui-stack')
    expect(stack).not.toBeNull()
    // Every row is a child of it, not a sibling that missed the rhythm.
    const rows = details.querySelectorAll('.ui-field')
    expect(rows.length).toBeGreaterThan(0)
    for (const row of rows) {
      expect(row.parentElement).toBe(stack)
    }
  })

  // The reason this action exists. A user who has forgotten the passphrase
  // finds every other control on this page disabled — precisely because they
  // cannot get in — so the way out must not be disabled with them.
  it('offers the reset while the vault is locked, when nothing else is available', async () => {
    await renderVaultSection(SEALED_STATUS)
    const btn = screen
      .getAllByText('Reset the vault')
      .find((el) => el.tagName === 'BUTTON') as HTMLButtonElement
    expect(btn).toBeTruthy()
    expect(btn.hasAttribute('disabled')).toBe(false)
    expect(btn.getAttribute('data-variant')).toBe('danger')

    // Every other action in Protection is disabled in this state.
    expect(screen.getByRole('button', { name: 'Change passphrase' }).hasAttribute('disabled')).toBe(
      true,
    )
  })

  it('offers the reset when protection was never set up', async () => {
    await renderVaultSection(UNINIT_STATUS)
    const btn = screen
      .getAllByText('Reset the vault')
      .find((el) => el.tagName === 'BUTTON') as HTMLButtonElement
    expect(btn.hasAttribute('disabled')).toBe(false)
  })

  it('the reset row states that it cannot be undone, before the dialog is opened', async () => {
    await renderVaultSection(SEALED_STATUS)
    expect(screen.getByText(/Delete every saved password.*Cannot be undone/)).toBeTruthy()
  })

  it('does not set the diagnostics block below the page type size', async () => {
    await renderVaultSection(UNSEALED_STATUS)
    const details = document.querySelector('details.ui-vault-diagnostics')!
    expect((details as HTMLElement).style.fontSize).toBe('')
  })
})

// ── ResetVaultDialog ───────────────────────────────────────────────────
// The way back for a user who has forgotten the passphrase and has no recovery
// code. Everything here is about the moment BEFORE the destruction: what the
// user is told, and what they have to do to proceed.

describe('ResetVaultDialog', () => {
  function mockResetClient(overrides?: {
    preview?: Partial<VaultResetPreview>
    result?: Partial<VaultResetResult>
    resetRejects?: unknown
  }) {
    const resetPreview = vi.fn().mockResolvedValue({
      secretCount: 3,
      profileCount: 5,
      systemKeychainReachable: true,
      vaultInitialized: true,
      ...overrides?.preview,
    })
    const reset =
      overrides?.resetRejects !== undefined
        ? vi.fn().mockRejectedValue(overrides.resetRejects)
        : vi.fn().mockResolvedValue({
            secretCount: 3,
            profileCount: 5,
            residue: [],
            ...overrides?.result,
          })
    const client = { resetPreview, reset } as unknown as VaultClient
    return { client, resetPreview, reset }
  }

  async function openReset(overrides?: Parameters<typeof mockResetClient>[0]) {
    clearToasts()
    const m = mockResetClient(overrides)
    const onReset = vi.fn()
    render(() => (
      <>
        <ResetVaultDialog open={true} onClose={vi.fn()} onReset={onReset} vaultClient={m.client} />
        <ToastHost />
      </>
    ))
    await vi.waitFor(() => expect(m.resetPreview).toHaveBeenCalled())
    return { ...m, onReset }
  }

  function resetButton(): HTMLButtonElement {
    return screen
      .getAllByText('Reset the vault')
      .find((el) => el.tagName === 'BUTTON') as HTMLButtonElement
  }

  it('says the action cannot be undone, before anything is destroyed', async () => {
    await openReset()
    expect(document.body.textContent).toMatch(/cannot be undone/i)
    expect(document.body.textContent).toMatch(/no way to recover them/i)
  })

  it('names what will be lost, in the numbers the store actually holds', async () => {
    await openReset()
    await vi.waitFor(() => {
      const impact = document.querySelector('.ui-vault-reset-impact')!
      expect(impact.textContent).toContain('3 saved secrets')
      expect(impact.textContent).toContain('5 connections')
    })
  })

  // The confirmation is the whole point of the dialog. A destructive action
  // one click away from a page the user was reading is an action they take by
  // accident.
  it('refuses to reset until the user confirms they understand', async () => {
    const { reset } = await openReset()
    expect(resetButton().disabled).toBe(true)
    fireEvent.click(resetButton())
    expect(reset).not.toHaveBeenCalled()
  })

  it('resets once confirmed', async () => {
    const { reset, onReset } = await openReset()
    fireEvent.click(screen.getByLabelText('I understand this cannot be undone'))
    expect(resetButton().disabled).toBe(false)
    fireEvent.click(resetButton())
    await vi.waitFor(() => expect(reset).toHaveBeenCalled())
    await vi.waitFor(() => expect(onReset).toHaveBeenCalled())
  })

  // Stated before the choice, not discovered after it. Whether the keychain
  // answers decides whether anything stored there can be removed at all.
  it('warns that an unreachable keychain keeps its secrets, before confirming', async () => {
    await openReset({ preview: { systemKeychainReachable: false } })
    await vi.waitFor(() => {
      const warning = document.querySelector('.ui-vault-reset-warning')!
      expect(warning.textContent).toContain('system keychain is not answering')
      expect(warning.textContent).toContain('remain readable')
    })
  })

  it('says nothing about the keychain when it is answering', async () => {
    await openReset({ preview: { systemKeychainReachable: true } })
    expect(document.querySelector('.ui-vault-reset-warning')).toBeNull()
  })

  // The honest half. Claiming everything was deleted while a store still holds
  // readable secrets is the one thing this flow must never do.
  it('reports what was left behind instead of claiming everything was deleted', async () => {
    await openReset({
      preview: { systemKeychainReachable: false },
      result: { residue: [{ store: 'system', reason: 'no-service' }] },
    })
    fireEvent.click(screen.getByLabelText('I understand this cannot be undone'))
    fireEvent.click(resetButton())

    await vi.waitFor(() => {
      const toast = document.querySelector('.ui-toast')!
      expect(toast.getAttribute('data-level')).toBe('warning')
      expect(toast.textContent).toContain('System keychain')
      expect(toast.textContent).toContain('still holds secrets')
    })
  })

  it('confirms plainly when there was nothing left behind', async () => {
    await openReset({ result: { residue: [] } })
    fireEvent.click(screen.getByLabelText('I understand this cannot be undone'))
    fireEvent.click(resetButton())

    await vi.waitFor(() => {
      const toast = document.querySelector('.ui-toast')!
      expect(toast.getAttribute('data-level')).toBe('success')
      expect(toast.textContent).toContain('Set up protection to start again')
    })
  })

  it('says so when there is nothing to delete', async () => {
    await openReset({ preview: { secretCount: 0, profileCount: 0 } })
    await vi.waitFor(() => {
      expect(document.querySelector('.ui-vault-reset-impact')!.textContent).toBe(
        'There are no saved secrets to delete.',
      )
    })
  })
})

// ── Copying the recovery code ──────────────────────────────────────────
// A recovery code is shown exactly once. Silence after pressing Copy is the
// dangerous outcome, not the missing one: the user presses Done believing the
// code is on the clipboard, and it is gone for good.

describe('copying a one-time recovery code', () => {
  function withClipboard(write: () => Promise<void>) {
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText: vi.fn(write) },
      configurable: true,
    })
  }

  async function showRecoveryCode() {
    clearToasts()
    const { client, setup } = mockClient()
    setup.mockResolvedValue({ recoveryCode: 'r1pw-1js2-j4cx' })
    render(() => (
      <>
        <SetupDialog open={true} onClose={vi.fn()} onSetupComplete={vi.fn()} vaultClient={client} />
        <ToastHost />
      </>
    ))
    fireEvent.input(screen.getByLabelText('Master passphrase'), {
      target: { value: 'my-passphrase' },
    })
    fireEvent.input(screen.getByLabelText('Confirm passphrase'), {
      target: { value: 'my-passphrase' },
    })
    fireEvent.click(screen.getAllByText('Set Up').find((el) => el.tagName === 'BUTTON')!)
    await vi.waitFor(() => expect(screen.getByText('r1pw-1js2-j4cx')).toBeTruthy())
    const copy = document.querySelector('.ui-vault-action-row .ui-icon-button') as HTMLButtonElement
    expect(copy).toBeTruthy()
    return copy
  }

  it('confirms the code reached the clipboard', async () => {
    withClipboard(() => Promise.resolve())
    const copy = await showRecoveryCode()
    fireEvent.click(copy)

    await vi.waitFor(() => {
      const toast = document.querySelector('.ui-toast')!
      expect(toast.getAttribute('data-level')).toBe('success')
      expect(toast.textContent).toContain('copied to the clipboard')
    })
  })

  // The one that matters. A copy that failed silently is worse than no copy
  // button at all, because the user acts on a promise nothing made.
  it('says the copy failed, and says what to do instead', async () => {
    withClipboard(() => Promise.reject(new Error('denied')))
    const copy = await showRecoveryCode()
    fireEvent.click(copy)

    await vi.waitFor(() => {
      const toast = document.querySelector('.ui-toast')!
      expect(toast.getAttribute('data-level')).toBe('danger')
      expect(toast.textContent).toContain('Write it down before closing this')
    })
  })
})

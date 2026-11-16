// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, within } from '@solidjs/testing-library'
import { Show } from 'solid-js'
import { ConnectionsView } from './connections'
import { Dispatcher, RpcError } from './dispatcher'
import { fixedEndpoint } from './endpoint'
import { ProfileClient, type TabbyPreviewResponse } from './profiles'
import { createVaultState, SetupDialog, UnlockDialog } from './vault'
import type { VaultClient } from './vault-client'
import { clearToasts, toasts } from './ui'

// The repository's ES2021 library target does not expose Promise.withResolvers.
function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

function preview(planToken = 'plan-one'): TabbyPreviewResponse {
  return {
    planToken,
    profilesToImport: 1,
    groupsToImport: 0,
    secretsToImport: 0,
    secretProvider: 'vault',
    profileEntries: [{ name: planToken, action: 'new' }],
  }
}

const imported = { profilesImported: 1, groupsImported: 0 }

function mount(vaultState?: 'sealed' | 'uninitialized') {
  const client = new ProfileClient(new Dispatcher(fixedEndpoint(9876)))
  vi.spyOn(client, 'listProfiles').mockResolvedValue([])
  vi.spyOn(client, 'listGroups').mockResolvedValue([])
  vi.spyOn(client, 'loadEffective').mockResolvedValue({ profiles: [] })
  vi.spyOn(client, 'sessionStatus').mockResolvedValue({ statuses: {} })
  vi.spyOn(client, 'sshConfigPath').mockResolvedValue({
    path: '/home/test/.ssh/config',
    available: true,
  })
  const prepare = vi.spyOn(client, 'tabbyPreview').mockResolvedValue(preview())
  const execute = vi.spyOn(client, 'tabbyExecute').mockResolvedValue(imported)
  const vaultClient = {
    status: vi.fn().mockResolvedValue({
      state: vaultState,
      hasPassphrase: true,
      autoSealMinutes: 0,
      providers: [],
      defaultProvider: null,
    }),
  } as unknown as VaultClient
  const vault = vaultState ? createVaultState(vaultClient) : undefined
  const container = document.body.appendChild(document.createElement('div'))
  const result = render(
    () => (
      <>
        <ConnectionsView client={client} vaultController={vault} />
        <Show when={vault} keyed>
          {(controller) => (
            <>
              <SetupDialog
                open={controller.showSetup()}
                onClose={() => controller.closeSetup()}
                onSetupComplete={() => controller.onSetupDone()}
                vaultClient={vaultClient}
              />
              <UnlockDialog
                open={controller.showUnlock()}
                onClose={() => controller.closeUnlock()}
                onUnsealed={() => controller.onUnsealDone()}
                vaultClient={vaultClient}
                vaultStatus={controller.status()}
                reason={controller.unlockReason()}
              />
            </>
          )}
        </Show>
      </>
    ),
    { container },
  )
  return { ...result, client, prepare, execute }
}

function dialog(container: HTMLElement, title: string) {
  return within(container).getByRole('dialog', { name: title })
}

function openImport(
  container: HTMLElement,
  text: () => Promise<string> = () => Promise.resolve('profiles: []'),
) {
  within(container).getAllByRole('button', { name: 'Import…' })[0].click()
  const form = dialog(container, 'Import Connections')
  fireEvent.click(within(form).getByRole('radio', { name: 'Tabby config (.yml/.yaml)' }))
  const file = new File(['profiles: []'], 'tabby.yaml', { type: 'application/yaml' })
  // jsdom's File has no text() implementation; keep the real picker and stub only file IO.
  Object.defineProperty(file, 'text', { value: text })
  fireEvent.change(form.querySelector('input[type="file"]')!, { target: { files: [file] } })
  within(form).getByRole('button', { name: 'Preview' }).click()
  return form
}

async function openPreview(container: HTMLElement) {
  openImport(container)
  return await within(container).findByRole('dialog', { name: 'Tabby Import Preview' })
}

afterEach(() => {
  cleanup()
  clearToasts()
  vi.restoreAllMocks()
})

describe('Tabby import — real dialogs', () => {
  it('allows only one execute and cannot dismiss a committing preview', async () => {
    const { container, execute } = mount()
    const response = deferred<typeof imported>()
    execute.mockReturnValue(response.promise)
    const review = await openPreview(container)
    const confirm = within(review).getByRole('button', { name: 'Import' })
    confirm.click()
    confirm.click()

    expect(execute).toHaveBeenCalledTimes(1)
    expect(execute).toHaveBeenCalledWith('plan-one')
    expect((confirm as HTMLButtonElement).disabled).toBe(true)
    const cancel = within(review).getByRole('button', { name: 'Cancel' })
    expect((cancel as HTMLButtonElement).disabled).toBe(true)
    cancel.click()
    fireEvent(review, new Event('cancel', { cancelable: true }))
    fireEvent.keyDown(review, { key: 'Escape' })
    expect(within(review).queryByRole('button', { name: 'Close dialog' })).toBeNull()
    expect(dialog(container, 'Tabby Import Preview')).toBe(review)
    expect(toasts().some((toast) => toast.level === 'success')).toBe(false)

    response.resolve(imported)
    await vi.waitFor(() => {
      expect(within(container).queryByRole('dialog', { name: 'Tabby Import Preview' })).toBeNull()
      expect(toasts().filter((toast) => toast.level === 'success')).toHaveLength(1)
    })
    expect(execute).toHaveBeenCalledTimes(1)
  })

  it.each(['Cancel', 'Close dialog'])('ignores a preview response after %s', async (action) => {
    const { container, prepare, execute } = mount()
    const response = deferred<TabbyPreviewResponse>()
    prepare.mockReturnValue(response.promise)
    const form = openImport(container)
    await vi.waitFor(() => expect(prepare).toHaveBeenCalledTimes(1))
    within(form).getByRole('button', { name: action }).click()
    expect(within(container).queryByRole('dialog', { name: 'Import Connections' })).toBeNull()

    response.resolve(preview())
    await response.promise
    expect(within(container).queryByRole('dialog', { name: 'Tabby Import Preview' })).toBeNull()
    expect(execute).not.toHaveBeenCalled()
  })

  it('does not send a preview if dismissed while reading the file', async () => {
    const { container, prepare } = mount()
    const contents = deferred<string>()
    const form = openImport(container, () => contents.promise)
    within(form).getByRole('button', { name: 'Close dialog' }).click()
    contents.resolve('profiles: []')
    await contents.promise
    expect(prepare).not.toHaveBeenCalled()
    expect(within(container).queryByRole('dialog', { name: 'Tabby Import Preview' })).toBeNull()
  })

  it.each(['resolve', 'reject'] as const)(
    'keeps the new preview when an old request settles: %s',
    async (outcome) => {
      const { container, prepare, execute } = mount()
      const old = deferred<TabbyPreviewResponse>()
      prepare.mockReturnValueOnce(old.promise).mockResolvedValueOnce(preview('plan-new'))
      const first = openImport(container)
      await vi.waitFor(() => expect(prepare).toHaveBeenCalledTimes(1))
      within(first).getByRole('button', { name: 'Close dialog' }).click()
      const review = await openPreview(container)
      expect(review.textContent).toContain('plan-new')

      if (outcome === 'resolve') old.resolve(preview('plan-old'))
      else old.reject(new Error('old preview failed'))
      await old.promise.catch(() => undefined)
      expect(dialog(container, 'Tabby Import Preview').textContent).toContain('plan-new')
      expect(dialog(container, 'Tabby Import Preview').textContent).not.toContain('plan-old')
      expect(toasts()).toHaveLength(0)
      within(review).getByRole('button', { name: 'Import' }).click()
      await vi.waitFor(() => expect(execute).toHaveBeenCalledWith('plan-new'))
    },
  )

  it('does not let an old preview completion release the new request busy state', async () => {
    const { container, prepare } = mount()
    const old = deferred<TabbyPreviewResponse>()
    const current = deferred<TabbyPreviewResponse>()
    prepare.mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise)
    const first = openImport(container)
    await vi.waitFor(() => expect(prepare).toHaveBeenCalledTimes(1))
    within(first).getByRole('button', { name: 'Close dialog' }).click()
    const second = openImport(container)
    await vi.waitFor(() => expect(prepare).toHaveBeenCalledTimes(2))
    old.resolve(preview('plan-old'))
    await old.promise
    expect(dialog(container, 'Import Connections')).toBe(second)
    expect(
      within(second).getByRole<HTMLButtonElement>('button', { name: 'Importing…' }).disabled,
    ).toBe(true)
    current.resolve(preview('plan-new'))
    await within(container).findByRole('dialog', { name: 'Tabby Import Preview' })
  })

  it('releases the execute guard after a failure without discarding the preview', async () => {
    const { container, execute } = mount()
    execute.mockRejectedValueOnce(new Error('write refused'))
    const review = await openPreview(container)
    within(review).getByRole('button', { name: 'Import' }).click()
    await vi.waitFor(() =>
      expect(toasts().some((toast) => toast.message.includes('write refused'))).toBe(true),
    )
    const confirm = within(review).getByRole('button', { name: 'Import' })
    await vi.waitFor(() => expect((confirm as HTMLButtonElement).disabled).toBe(false))
    confirm.click()
    await vi.waitFor(() => expect(execute).toHaveBeenCalledTimes(2))
    expect(execute.mock.calls).toEqual([['plan-one'], ['plan-one']])
  })

  it.each(['sealed', 'uninitialized'] as const)(
    'keeps the plan retryable after cancelling vault %s',
    async (state) => {
      const { container, execute } = mount(state)
      execute.mockRejectedValueOnce(
        new RpcError('vault unavailable', -32000, { reason: `vault-${state}` }),
      )
      const review = await openPreview(container)
      within(review).getByRole('button', { name: 'Import' }).click()
      await vi.waitFor(() => expect(container.querySelector('.ui-prompt')).toBeTruthy())
      within(container.querySelector('.ui-prompt') as HTMLElement)
        .getByRole('button', { name: 'Cancel' })
        .click()
      await vi.waitFor(() => expect(container.querySelector('.ui-prompt')).toBeNull())
      const confirm = within(review).getByRole('button', { name: 'Import' })
      await vi.waitFor(() => expect((confirm as HTMLButtonElement).disabled).toBe(false))
      expect(toasts()).toHaveLength(0)
      confirm.click()
      await vi.waitFor(() => expect(execute).toHaveBeenCalledTimes(2))
      expect(execute.mock.calls).toEqual([['plan-one'], ['plan-one']])
    },
  )

  it('presents every unsupported field warning before confirming', async () => {
    const { container, prepare, execute } = mount()
    prepare.mockResolvedValue({
      ...preview(),
      warnings: [
        { profile: 'web', field: 'privateKeys', reason: 'Multiple keys are not supported' },
        { profile: 'db', field: 'proxyCommand', reason: 'This option cannot be imported' },
      ],
    })
    const review = await openPreview(container)
    const warnings = review.querySelectorAll('.ui-status-card[data-tone="warning"]')
    expect(warnings).toHaveLength(2)
    expect(warnings[0].textContent).toContain('web')
    expect(warnings[0].textContent).toContain('privateKeys')
    expect(warnings[0].textContent).toContain('Multiple keys are not supported')
    expect(warnings[1].textContent).toContain('db')
    expect(warnings[1].textContent).toContain('proxyCommand')
    expect(warnings[1].textContent).toContain('This option cannot be imported')
    expect(execute).not.toHaveBeenCalled()
    within(review).getByRole('button', { name: 'Import' }).click()
    await vi.waitFor(() => expect(execute).toHaveBeenCalledWith('plan-one'))
  })
})

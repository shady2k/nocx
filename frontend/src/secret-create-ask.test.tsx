// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi, type Mock } from 'vitest'
import { cleanup, fireEvent, render } from '@solidjs/testing-library'
import {
  SecretCreateDialog,
  type SecretCreateAsk,
  type SecretCreateVault,
} from './secret-create-ask'
import type { InventoryEntry } from './vault-client'

afterEach(() => cleanup())

const entry = (overrides: Partial<InventoryEntry>): InventoryEntry => ({
  id: 'secrow:default',
  name: 'default',
  kind: 'password',
  provider: 'file',
  ownerId: '',
  usedBy: 0,
  reachable: true,
  ...overrides,
})

function vaultWith(
  lists: InventoryEntry[][],
  createSecretImpl: SecretCreateVault['createSecret'] = vi.fn().mockResolvedValue({
    name: 'created',
  }),
) {
  const list = vi.fn<SecretCreateVault['list']>()
  const createSecret = vi.mocked(createSecretImpl)
  for (const result of lists) list.mockResolvedValueOnce(result)
  return { list, createSecret }
}

type CreatedCallback = (created: { handle: string; name: string }) => void
type CloseCallback = () => void
type CreateParams = Parameters<SecretCreateVault['createSecret']>[0]

function mount(
  ask: SecretCreateAsk,
  vault: SecretCreateVault,
  onCreated: Mock<CreatedCallback> = vi.fn<CreatedCallback>(),
  onClose: Mock<CloseCallback> = vi.fn<CloseCallback>(),
) {
  return {
    ...render(() => (
      <SecretCreateDialog ask={ask} vault={vault} onCreated={onCreated} onClose={onClose} />
    )),
    onCreated,
    onClose,
  }
}

const ask = (overrides: Partial<SecretCreateAsk> = {}): SecretCreateAsk => ({
  name: 'prod token',
  kind: 'api-token',
  value: 'word',
  ...overrides,
})

const nameInput = (): HTMLInputElement =>
  document.querySelector<HTMLInputElement>('#secret-create-name') as HTMLInputElement
const valueInput = (): HTMLInputElement =>
  document.querySelector<HTMLInputElement>('#secret-create-value') as HTMLInputElement
const save = (): HTMLButtonElement =>
  Array.from(document.querySelectorAll<HTMLButtonElement>('button')).find(
    (button) => button.textContent?.trim() === 'Save to vault',
  ) as HTMLButtonElement
const kindOption = (): HTMLSelectElement =>
  document.querySelector<HTMLSelectElement>('#secret-create-kind') as HTMLSelectElement

const setName = (name: string): void => {
  fireEvent.input(nameInput(), { target: { value: name } })
}

const inventoryRow = (name: string, id: string): InventoryEntry =>
  entry({ name, id, kind: 'api-token' })

describe('SecretCreateDialog', () => {
  it('mounts nothing while closed and seeds a carried value when open', () => {
    const vault = vaultWith([])
    render(() => (
      <SecretCreateDialog ask={null} vault={vault} onCreated={vi.fn()} onClose={vi.fn()} />
    ))
    expect(document.querySelector('dialog')).toBeNull()
    expect(document.querySelector('#secret-create-value')).toBeNull()

    cleanup()
    mount(ask({ value: 'word' }), vault)
    expect(valueInput().value).toBe('word')
  })

  it('opens with an empty value when the door knows none', () => {
    const vault = vaultWith([])
    mount(ask({ value: undefined }), vault)
    expect(valueInput().value).toBe('')
  })

  it('accepts the proposed name and reports the re-listed row handle', async () => {
    const created = inventoryRow('prod token', 'secrow:created')
    const createSecret = vi.fn().mockResolvedValue({ name: created.name })
    const vault = vaultWith([[], [created]], createSecret)
    const { onCreated, onClose } = mount(ask(), vault)

    fireEvent.click(save())

    await vi.waitFor(() => expect(createSecret).toHaveBeenCalledOnce())
    expect(createSecret).toHaveBeenCalledWith({
      name: 'prod token',
      kind: 'api-token',
      value: 'word',
      resolve: true,
    })
    expect(onCreated).toHaveBeenCalledWith({ handle: 'secrow:created', name: 'prod token' })
    expect(onClose).toHaveBeenCalledOnce()
    expect(onCreated.mock.invocationCallOrder[0]).toBeLessThan(onClose.mock.invocationCallOrder[0])
  })

  it('writes an edited name rather than the proposed name or value', async () => {
    const created = inventoryRow('renamed', 'secrow:renamed')
    const createSecret = vi.fn().mockResolvedValue({ name: created.name })
    const vault = vaultWith([[], [created]], createSecret)
    mount(ask({ name: 'suggested', value: 'word' }), vault)
    setName('renamed')

    fireEvent.click(save())

    await vi.waitFor(() => expect(createSecret).toHaveBeenCalledOnce())
    const written = createSecret.mock.calls[0]?.[0] as CreateParams
    expect(written).toMatchObject({ name: 'renamed', value: 'word' })
    expect(written.name).not.toBe('word')
  })
  it('never derives the name from the carried value', async () => {
    const created = inventoryRow('suggested', 'secrow:suggested')
    const createSecret = vi.fn().mockResolvedValue({ name: created.name })
    const vault = vaultWith([[], [created]], createSecret)
    mount(ask({ name: 'suggested', value: 'apple' }), vault)

    fireEvent.click(save())

    await vi.waitFor(() => expect(createSecret).toHaveBeenCalledOnce())
    const written = createSecret.mock.calls[0]?.[0] as CreateParams
    expect(written.name).toBe('suggested')
    expect(written.name).not.toContain('apple')
  })

  it('allows correcting the derived kind before writing', async () => {
    const created = inventoryRow('prod token', 'secrow:kind')
    const createSecret = vi.fn().mockResolvedValue({ name: created.name })
    const vault = vaultWith([[], [created]], createSecret)
    mount(ask({ kind: 'password' }), vault)

    fireEvent.change(kindOption(), { target: { value: 'api-token' } })
    fireEvent.click(save())

    await vi.waitFor(() => expect(createSecret).toHaveBeenCalledOnce())
    expect(createSecret.mock.calls[0]?.[0]).toMatchObject({ kind: 'api-token' })
  })

  it('proposes the first free collision variant and writes it on the next action', async () => {
    const existing = [
      inventoryRow('prod token', 'secrow:one'),
      inventoryRow('prod token 2', 'secrow:two'),
    ]
    const created = inventoryRow('prod token 3', 'secrow:three')
    const createSecret = vi.fn().mockResolvedValue({ name: created.name })
    const vault = vaultWith([existing, existing, [created]], createSecret)
    const { container } = mount(ask(), vault)

    fireEvent.click(save())

    await vi.waitFor(() => expect(nameInput().value).toBe('prod token 3'))
    expect(container.textContent).toContain('A secret named "prod token" is already in the vault')
    expect(createSecret).not.toHaveBeenCalled()

    fireEvent.click(save())
    await vi.waitFor(() => expect(createSecret).toHaveBeenCalledOnce())
    expect(createSecret).toHaveBeenCalledWith({
      name: 'prod token 3',
      kind: 'api-token',
      value: 'word',
      resolve: true,
    })
  })
  it('keeps the value and shows a sealed-vault refusal', async () => {
    const createSecret = vi.fn().mockRejectedValue(new Error('vault is sealed'))
    const vault = vaultWith([[]], createSecret)
    const { onCreated } = mount(ask({ value: 'still secret' }), vault)

    fireEvent.click(save())

    await vi.waitFor(() => expect(document.body.textContent).toContain('vault is sealed'))
    expect(valueInput().value).toBe('still secret')
    expect(onCreated).not.toHaveBeenCalled()
  })

  it('refuses a secrow name before listing or writing', async () => {
    const vault = vaultWith([])
    const { onCreated } = mount(ask({ name: 'secrow:forbidden' }), vault)

    fireEvent.click(save())

    await vi.waitFor(() =>
      expect(document.body.textContent).toContain('Secret names cannot start with "secrow:"'),
    )
    expect(vault.list).not.toHaveBeenCalled()
    expect(vault.createSecret).not.toHaveBeenCalled()
    expect(onCreated).not.toHaveBeenCalled()
    expect(valueInput().value).toBe('word')
  })

  it('refuses success when the created name is absent from the re-listed inventory', async () => {
    const createSecret = vi.fn().mockResolvedValue({ name: 'missing' })
    const vault = vaultWith([[], []], createSecret)
    const { onCreated, onClose } = mount(ask(), vault)

    fireEvent.click(save())

    await vi.waitFor(() =>
      expect(document.body.textContent).toContain('not found in the vault inventory'),
    )
    expect(onCreated).not.toHaveBeenCalled()
    expect(onClose).not.toHaveBeenCalled()
    expect(valueInput().value).toBe('word')
  })
})

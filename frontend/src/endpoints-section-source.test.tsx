// @vitest-environment jsdom
/**
 * The endpoint form's key field and custom-header rows (nocx-rzjw + nocx-lyyk,
 * acceptance 6 & 7, rewritten for nocx-3o0ed.4): each is the SAME field the
 * connections editor and the API workbench place, and a value that IS a
 * `{{secret:…}}` reference is a reference to an existing vault secret rather
 * than a second copy minted here. The two-way choice these tests used to make
 * through a segmented control is now made by what the field holds; every
 * assertion about the WIRE is unchanged, because the wire is unchanged.
 */
import { cleanup, render, fireEvent, waitFor } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi, type Mock } from 'vitest'

import { EndpointsSection } from './endpoints-section'
import { EndpointClient, type Endpoint, type EndpointWrite } from './endpoints'
import { Dispatcher } from './dispatcher'
import { fixedEndpoint } from './endpoint'
import {
  VaultOperationCancelledError,
  createVaultSecretSource,
  createVaultState,
  type VaultController,
} from './vault'
import type { VaultClient, InventoryEntry } from './vault-client'
import type { SecretPickerSource } from './ui/secret-picker'
import {
  bindSecretByTyping,
  bindSecretFromLock,
  offeredSecretRows,
  pressLock,
} from './secret-field-test-helpers'

afterEach(cleanup)

function ep(overrides: Partial<Endpoint> = {}): Endpoint {
  return {
    id: 'endpoint:custom:provider:1',
    name: 'provider',
    baseUrl: 'https://api.example.com/v1',
    noKey: false,
    schema: 'openai-compatible',
    credential: null,
    models: [{ name: 'gpt-4o', alias: null }],
    headers: [],
    ...overrides,
  }
}

/** A recording client over a mutable store, exactly like the section's own
 *  test harness. */
function createClient(initial: Endpoint[] = []) {
  const store: Endpoint[] = [...initial]
  let next = 1
  const client = new EndpointClient(new Dispatcher(fixedEndpoint(9876)))
  vi.spyOn(client, 'listEndpoints').mockImplementation(async () => [...store]) // eslint-disable-line @typescript-eslint/require-await
  const createEndpoint = vi
    .spyOn(client, 'createEndpoint')
    // eslint-disable-next-line @typescript-eslint/require-await
    .mockImplementation(async (input: EndpointWrite) => {
      const created: Endpoint = {
        id: `endpoint:custom:${input.name.toLowerCase().replace(/[^a-z0-9]+/g, '-')}:${next++}`,
        name: input.name,
        baseUrl: input.baseUrl,
        schema: 'openai-compatible',
        noKey: input.noKey,
        credential:
          input.key !== '' ? `secrow:${next++}` : input.credential !== '' ? input.credential : null,
        models: input.models.map((m) => ({ name: m.name, alias: m.alias })),
        headers: input.headers.map((h) => ({ name: h.name, value: h.value, secret: h.secret })),
      }
      store.push(created)
      return created
    })
  const updateEndpoint = vi
    .spyOn(client, 'updateEndpoint')
    // eslint-disable-next-line @typescript-eslint/require-await
    .mockImplementation(async (id: string, input: EndpointWrite) => {
      const index = store.findIndex((e) => e.id === id)
      if (index < 0) throw new Error('endpoint not found')
      const existing = store[index]
      const updated: Endpoint = {
        ...existing,
        name: input.name,
        baseUrl: input.baseUrl,
        noKey: input.noKey,
        models: input.models.map((m) => ({ name: m.name, alias: m.alias })),
        headers: input.headers.map((h) => ({ name: h.name, value: h.value, secret: h.secret })),
      }
      store[index] = updated
      return updated
    })
  return { client, createEndpoint, updateEndpoint, store }
}

/** An unsealed vault: the pickers offer rows; a save that only references a
 *  secret never needs the mint seam. */
function unsealedVault(rows: InventoryEntry[]) {
  const status = vi.fn().mockResolvedValue({
    state: 'unsealed' as const,
    hasPassphrase: true,
    autoSealMinutes: 0,
    providers: [],
    defaultProvider: null,
  })
  const inventory = vi.fn().mockResolvedValue({ entries: rows })
  const client = { status, inventory } as unknown as VaultClient
  const ctrl = createVaultState(client)
  return { ctrl, client, status, inventory }
}

const PASSWORD_ROWS: InventoryEntry[] = [
  {
    id: 'secrow:aaaaaaaa',
    name: 'prod api key',
    kind: 'password',
    provider: 'file',
    ownerId: '',
    usedBy: 0,
    reachable: true,
  },
]

function mount(
  initial: Endpoint[] = [],
  vault: {
    ctrl: VaultController
    client: VaultClient
    status: Mock
    inventory: Mock
  } = unsealedVault(PASSWORD_ROWS),
) {
  const harness = createClient(initial)
  const container = document.body.appendChild(document.createElement('div'))
  const openSecretCreate = vi.fn()
  // The source main.tsx wires, not a second one built for the test: the panel
  // behind every lock on this page is the composition root's (nocx-3o0ed.4).
  const secretSource: SecretPickerSource = createVaultSecretSource({
    vaultClient: vault.client,
    vaultController: vault.ctrl,
    openSecretCreate,
  })
  render(
    () => (
      <EndpointsSection
        client={harness.client}
        vaultController={vault.ctrl}
        vaultClient={vault.client}
        secretSource={secretSource}
      />
    ),
    { container },
  )
  return { ...harness, ...vault, container, openSecretCreate }
}

/** Flush the microtask chain deterministically — the repo's convention for
 *  "let a promise settle" (no real timers, AGENTS.md). */
const flush = async (): Promise<void> => {
  for (let i = 0; i < 5; i++) await Promise.resolve()
}

async function waitForRows(container: HTMLElement, count: number) {
  await waitFor(() => {
    expect(container.querySelectorAll('.ui-collection-row').length).toBe(count)
  })
}

function findDialogByTitle(container: HTMLElement, partial: string): HTMLElement | null {
  const titles = container.querySelectorAll('.nocx-dialog__title')
  for (const t of titles) {
    if (t.textContent && t.textContent.includes(partial)) return t.closest('.nocx-dialog')
  }
  return null
}

function openNew(container: HTMLElement) {
  const btn = Array.from(container.querySelectorAll('.ui-button')).find(
    (b) => b.textContent?.trim() === '+ New endpoint',
  )
  expect(btn, '+ New endpoint button not found').toBeTruthy()
  fireEvent.click(btn!)
  const dialog = findDialogByTitle(container, 'New Endpoint')
  expect(dialog, 'new-endpoint dialog did not open').toBeTruthy()
  return dialog as HTMLElement
}

function fillField(container: HTMLElement, id: string, value: string) {
  const el = container.querySelector<HTMLInputElement>(`#${id}`)
  expect(el, `field #${id} not found`).toBeTruthy()
  fireEvent.input(el!, { target: { value } })
}

function clickButton(scope: HTMLElement, label: string) {
  const btn = Array.from(scope.querySelectorAll('.ui-button')).find(
    (b) => b.textContent?.trim() === label,
  )
  expect(btn, `button ${label} not found`).toBeTruthy()
  fireEvent.click(btn!)
}

function fillModelAndBase(container: HTMLElement, dialog: HTMLElement) {
  fillField(container, 'endpoint-name', 'provider')
  fillField(container, 'endpoint-base-url', 'https://api.example.com/v1')
  clickButton(dialog, 'Add model')
  fillField(container, 'endpoint-model-0-name', 'gpt-4o')
}

/** The field with this id, which must exist. */
function fieldEl(scope: ParentNode, id: string): HTMLInputElement {
  const el = scope.querySelector<HTMLInputElement>(`#${id}`)
  expect(el, `field #${id} not found`).toBeTruthy()
  return el!
}

/** What a bound field READS as — the chip's text, never the handle. */
function chipText(input: HTMLInputElement): string | undefined {
  return input
    .closest('.ui-text-field__control')
    ?.querySelector('.ui-text-field__mark')
    ?.textContent?.trim()
}

describe('the endpoint key field (nocx-rzjw, nocx-3o0ed.4)', () => {
  it('places the same field the other surfaces do, and references an existing secret instead of minting', async () => {
    const { container, createEndpoint, inventory } = mount()
    await waitForRows(container, 0)
    const dialog = openNew(container)

    // ONE control. There is no source choice to make in advance any more —
    // no segments, and no second field under a second copy of the label.
    expect(dialog.querySelectorAll('[role="radio"]')).toHaveLength(0)
    expect(
      Array.from(dialog.querySelectorAll('label')).filter(
        (l) => l.textContent?.trim() === 'API key',
      ),
    ).toHaveLength(1)

    // Reaching for a stored secret is what asks the vault for its rows
    // (nocx-5ratm); a blank form nobody has reached from never does.
    expect(inventory).not.toHaveBeenCalled()
    const key = fieldEl(dialog, 'endpoint-key')
    await bindSecretFromLock(key, 'prod api key')
    expect(inventory).toHaveBeenCalled()

    // The field holds the opaque handle and READS as the secret's name.
    expect(key.value).toBe('{{secret:secrow:aaaaaaaa}}')
    expect(chipText(key)).toBe('prod api key')

    fillModelAndBase(container, dialog)
    clickButton(dialog, 'Create Endpoint')

    await waitFor(() => {
      expect(createEndpoint).toHaveBeenCalledTimes(1)
    })
    // The wire carries the row handle, NOT a minted key — one source, and
    // the referenced one.
    const input = createEndpoint.mock.calls[0][0]
    expect(input.key).toBe('')
    expect(input.credential).toBe('secrow:aaaaaaaa')
  })

  it("the '@' door binds the same field to the same row", async () => {
    const { container, createEndpoint } = mount()
    await waitForRows(container, 0)
    const dialog = openNew(container)

    const key = fieldEl(dialog, 'endpoint-key')
    await bindSecretByTyping(key, 'prod', 'prod api key')
    expect(key.value).toBe('{{secret:secrow:aaaaaaaa}}')

    fillModelAndBase(container, dialog)
    clickButton(dialog, 'Create Endpoint')
    await waitFor(() => {
      expect(createEndpoint).toHaveBeenCalledTimes(1)
    })
    expect(createEndpoint.mock.calls[0][0].credential).toBe('secrow:aaaaaaaa')
    expect(createEndpoint.mock.calls[0][0].key).toBe('')
  })

  it('a typed key still rides the wire once and mints (the source control is not a removal)', async () => {
    const { container, createEndpoint } = mount()
    await waitForRows(container, 0)
    const dialog = openNew(container)

    fillField(container, 'endpoint-key', 'sk-typed-fresh')
    fillModelAndBase(container, dialog)
    clickButton(dialog, 'Create Endpoint')

    await waitFor(() => {
      expect(createEndpoint).toHaveBeenCalledTimes(1)
    })
    const input = createEndpoint.mock.calls[0][0]
    expect(input.key).toBe('sk-typed-fresh')
    expect(input.credential).toBe('')
  })
})

describe('custom header rows (nocx-lyyk)', () => {
  it('edits headers through EditableRowList, literal and secret sources, and the wire carries them', async () => {
    const { container, createEndpoint } = mount()
    await waitForRows(container, 0)
    const dialog = openNew(container)

    // The header list is the kit's EditableRowList — the same component the
    // model rows use, never a bespoke row stack.
    clickButton(dialog, 'Add header')
    const list = dialog.querySelector('.ui-row-list')
    expect(list).toBeTruthy()

    // Row 1: a literal.
    fillField(container, 'endpoint-header-0-name', 'HTTP-Referer')
    fillField(container, 'endpoint-header-0-value', 'nocx')

    // Row 2: an existing secret — the value is the SAME field the key is, and
    // it is bound through the same lock.
    clickButton(dialog, 'Add header')
    fillField(container, 'endpoint-header-1-name', 'api-key')
    const rows = dialog.querySelectorAll('.ui-row-list__row')
    const row2 = rows[1] as HTMLElement
    const secretValue = fieldEl(row2, 'endpoint-header-1-value')
    await bindSecretFromLock(secretValue, 'prod api key')
    expect(chipText(secretValue)).toBe('prod api key')

    fillModelAndBase(container, dialog)
    clickButton(dialog, 'Create Endpoint')

    await waitFor(() => {
      expect(createEndpoint).toHaveBeenCalledTimes(1)
    })
    const input = createEndpoint.mock.calls[0][0]
    expect(input.headers).toEqual([
      { name: 'HTTP-Referer', value: 'nocx', secret: null },
      { name: 'api-key', value: null, secret: 'secrow:aaaaaaaa' },
    ])
  })

  it('an empty header value offers the vault rows and nothing else to escape (nocx-0sagl)', async () => {
    const { container } = mount()
    await waitForRows(container, 0)
    const dialog = openNew(container)
    clickButton(dialog, 'Add header')

    const row = dialog.querySelector('.ui-row-list__row') as HTMLElement
    const value = fieldEl(row, 'endpoint-header-0-value')
    // The em-dashed "— None —" option this test used to check belonged to the
    // Select inside the segmented control: an empty CHOICE only exists where
    // something forces a choice. An empty field is its own empty state, so
    // what is asserted here is what the panel offers over it — the rows, and
    // the create row — with the placeholder still a real em dash rather than
    // the text of an escape.
    expect(value.value).toBe('')
    expect(value.placeholder).toBe('nocx')
    pressLock(value)
    await waitFor(() => {
      expect(offeredSecretRows()).toEqual(['prod api key', 'Add a secret\u2026'])
    })
    expect(offeredSecretRows().join('')).not.toContain('u2026')
  })

  it("a saved endpoint's header rows reopen with their sources intact", async () => {
    const { container, updateEndpoint } = mount([
      ep({
        id: 'endpoint:custom:provider:1',
        name: 'provider',
        headers: [
          { name: 'HTTP-Referer', value: 'nocx', secret: null },
          { name: 'api-key', value: null, secret: 'secrow:aaaaaaaa' },
        ],
      }),
    ])
    await waitForRows(container, 1)

    const edit = container.querySelector('[aria-label="Edit provider"]') as HTMLElement
    fireEvent.click(edit)
    const dialog = findDialogByTitle(container, 'Edit Endpoint') as HTMLElement
    expect(dialog).toBeTruthy()

    expect((dialog.querySelector('#endpoint-header-0-value') as HTMLInputElement).value).toBe(
      'nocx',
    )
    const bound = fieldEl(dialog, 'endpoint-header-1-value')
    expect(bound.value).toBe('{{secret:secrow:aaaaaaaa}}')
    await waitFor(() => {
      expect(chipText(bound)).toBe('prod api key')
    })
    expect(dialog.textContent).not.toContain('secrow:aaaaaaaa')

    // Saving without touching them re-sends the restored source through update.
    clickButton(dialog, 'Save Endpoint')
    await waitFor(() => {
      expect(updateEndpoint).toHaveBeenCalledTimes(1)
    })
    const input = updateEndpoint.mock.calls[0][1]
    expect(input.headers).toEqual([
      { name: 'HTTP-Referer', value: 'nocx', secret: null },
      { name: 'api-key', value: null, secret: 'secrow:aaaaaaaa' },
    ])
  })
})

/** A SEALED vault. The pickers must still ask it: needing the vault is a
 *  property of the call, not of the call site (ADR-0032), and the layer
 *  that owns the unlock dialog can only raise it for a call that reaches
 *  it. `status` stays visible so a test can flip it after the unlock. */
function sealedVault(rows: InventoryEntry[], state: 'sealed' | 'uninitialized' = 'sealed') {
  const status = vi.fn().mockResolvedValue({
    state,
    hasPassphrase: state === 'sealed',
    autoSealMinutes: 0,
    providers: [],
    defaultProvider: null,
  })
  const inventory = vi.fn().mockResolvedValue({ entries: rows })
  const client = { status, inventory } as unknown as VaultClient
  const ctrl = createVaultState(client)
  return { ctrl, client, status, inventory }
}

const UNSEALED_STATUS = {
  state: 'unsealed' as const,
  hasPassphrase: true,
  autoSealMinutes: 0,
  providers: [],
  defaultProvider: null,
}

function openEdit(container: HTMLElement, name: string) {
  const edit = container.querySelector(`[aria-label="Edit ${name}"]`) as HTMLElement
  expect(edit, `Edit button for "${name}" not found`).toBeTruthy()
  fireEvent.click(edit)
  const dialog = findDialogByTitle(container, 'Edit Endpoint')
  expect(dialog, 'edit dialog did not open').toBeTruthy()
  return dialog as HTMLElement
}

describe('the picker asks the vault (nocx-5ratm, ADR-0032)', () => {
  it('asks a SEALED vault the moment somebody reaches for a secret — the vault layer is what raises the unlock', async () => {
    const vault = sealedVault(PASSWORD_ROWS)
    const { container, inventory, ctrl } = mount([], vault)
    await ctrl.refresh()
    expect(ctrl.status()?.state).toBe('sealed')
    await waitForRows(container, 0)
    expect(inventory).not.toHaveBeenCalled() // the LIST never asks

    // A blank form holds no reference and wants nothing from the vault.
    const dialog = openNew(container)
    await flush()
    expect(inventory).not.toHaveBeenCalled()

    // Pressing the field's lock IS the request, and a list of secret NAMES is
    // something only the vault can answer. The caller must not read the
    // vault's state and decide not to ask: the explicit door raises the real
    // unlock (secret-picker.ts) and the sealed seam (dispatcher.ts) re-sends
    // any call that lands on a sealed vault.
    pressLock(fieldEl(dialog, 'endpoint-key'))
    await waitFor(() => {
      expect(inventory).toHaveBeenCalled()
    })
  })

  it('asks on open when the key IS a bound row, and names it — never its handle', async () => {
    const vault = sealedVault(PASSWORD_ROWS)
    const { container, ctrl, status, inventory, updateEndpoint } = mount(
      [ep({ name: 'provider', credential: 'secrow:aaaaaaaa' })],
      vault,
    )
    await ctrl.refresh()
    await waitForRows(container, 1)
    expect(inventory).not.toHaveBeenCalled()

    // The editor opens holding the reference, so it has a NAME to render from
    // the first frame and asks at once.
    const dialog = openEdit(container, 'provider')
    await waitFor(() => {
      expect(inventory).toHaveBeenCalled()
    })

    // The person unlocks the vault; the chip receives the row's name.
    status.mockResolvedValue(UNSEALED_STATUS)
    await ctrl.refresh()

    const key = fieldEl(dialog, 'endpoint-key')
    await waitFor(() => {
      expect(chipText(key)).toBe('prod api key')
    })
    expect(key.value).toBe('{{secret:secrow:aaaaaaaa}}')
    expect(dialog.textContent).not.toContain('secrow:aaaaaaaa')

    // The restored binding is observable at the surface's write seam, not
    // through the chip a person reads.
    clickButton(dialog, 'Save Endpoint')
    await waitFor(() => {
      expect(updateEndpoint).toHaveBeenCalledTimes(1)
    })
    expect(updateEndpoint.mock.calls[0][1].credential).toBe('secrow:aaaaaaaa')
  })

  it('does not ask an UNINITIALIZED vault: there is nothing to unlock', async () => {
    const vault = sealedVault(PASSWORD_ROWS, 'uninitialized')
    const { container, inventory, ctrl } = mount([], vault)
    await ctrl.refresh()
    expect(ctrl.status()?.state).toBe('uninitialized')
    await waitForRows(container, 0)

    const dialog = openNew(container)
    pressLock(fieldEl(dialog, 'endpoint-key'))
    await flush()

    expect(inventory).not.toHaveBeenCalled()
    expect(ctrl.showUnlock()).toBe(false)
  })

  it('a dismissed unlock leaves the editor usable, and a later unseal still loads the rows', async () => {
    const vault = sealedVault(PASSWORD_ROWS)
    const { container, ctrl, status, inventory } = mount(
      [ep({ name: 'provider', credential: 'secrow:aaaaaaaa' })],
      vault,
    )
    await ctrl.refresh()
    await waitForRows(container, 1)
    inventory.mockRejectedValueOnce(new VaultOperationCancelledError())
    const dialog = openEdit(container, 'provider')
    await waitFor(() => {
      expect(inventory).toHaveBeenCalledTimes(1)
    })

    // The editor still works — a dismissal is a choice, not a failure.
    fillField(container, 'endpoint-name', 'renamed')
    expect((container.querySelector('#endpoint-name') as HTMLInputElement).value).toBe('renamed')
    // And the bound key still reads as something a person can act on rather
    // than as its own handle. The Select's "Unavailable secret" option was
    // this fact's old shape; the chip's is the vault's own sentence, which
    // says WHY as well — the rows never arrived because the unlock was
    // dismissed, and the vault is still sealed.
    const key = fieldEl(dialog, 'endpoint-key')
    expect(chipText(key)).toBe('Vault locked \u2014 unlock to view')
    expect(dialog.textContent).not.toContain('secrow:aaaaaaaa')

    // And the refusal is not remembered as a store failure: unsealing the
    // vault later loads the rows the list needs for its credential state.
    status.mockResolvedValue(UNSEALED_STATUS)
    await ctrl.refresh()
    await waitFor(() => {
      expect(inventory).toHaveBeenCalledTimes(2)
    })
  })
})

// @vitest-environment jsdom
/**
 * The endpoint form's secret-source control and custom-header rows (bead
 * nocx-rzjw + nocx-lyyk, acceptance 6 & 7): the key field offers the same
 * two-way source choice the connections editor does and can reference an
 * existing vault secret instead of minting a second copy; header rows edit
 * through the kit's EditableRowList with the same source control per row.
 */
import { cleanup, render, fireEvent, waitFor } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi, type Mock } from 'vitest'

import { EndpointsSection } from './endpoints-section'
import { EndpointClient, type Endpoint, type EndpointWrite } from './endpoints'
import { Dispatcher } from './dispatcher'
import { VaultOperationCancelledError, createVaultState, type VaultController } from './vault'
import type { VaultClient, InventoryEntry } from './vault-client'

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
  const client = new EndpointClient(new Dispatcher())
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
    osKeyAvailable: false,
    osKeyCapable: true,
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
  render(
    () => (
      <EndpointsSection
        client={harness.client}
        vaultController={vault.ctrl}
        vaultClient={vault.client}
      />
    ),
    { container },
  )
  return { ...harness, ...vault, container }
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

/** Click the segmented segment with the given label. */
function clickSegment(scope: HTMLElement, label: string) {
  const segment = Array.from(scope.querySelectorAll('[role="radio"]')).find(
    (r) => r.textContent?.trim() === label,
  )
  expect(segment, `segment ${label} not found`).toBeTruthy()
  fireEvent.click(segment!)
}

/** Wait until the given select offers the vault rows, then pick one. */
async function pickSecret(select: HTMLSelectElement, row: string, name: string) {
  await waitFor(() => {
    expect(Array.from(select.options).map((o) => o.text)).toContain(name)
  })
  fireEvent.change(select, { target: { value: row } })
}

describe('the endpoint key source control (nocx-rzjw)', () => {
  it('reuses the connections editor source choice and references an existing secret instead of minting', async () => {
    const { container, createEndpoint, inventory } = mount()
    await waitForRows(container, 0)
    const dialog = openNew(container)

    // The two-way source choice, with the connections editor's vocabulary.
    const segments = Array.from(dialog.querySelectorAll('[role="radio"]')).map((r) => r.textContent)
    expect(segments).toContain('Type a new one')
    expect(segments).toContain('Use existing secret')

    // Choose the existing secret: the picker lists the vault's password rows.
    // Putting it on screen is what asks the vault for them (nocx-5ratm); a
    // blank form showing no picker never does.
    expect(inventory).not.toHaveBeenCalled()
    clickSegment(dialog, 'Use existing secret')
    await waitFor(() => {
      expect(inventory).toHaveBeenCalled()
    })
    const picker = dialog.querySelector('select') as HTMLSelectElement
    expect(picker).toBeTruthy()
    await pickSecret(picker, 'secrow:aaaaaaaa', 'prod api key')

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

    // Row 2: an existing secret — the value source is the SAME control the
    // key uses. Scope to the header row: the dialog also holds the key
    // source's segments.
    clickButton(dialog, 'Add header')
    fillField(container, 'endpoint-header-1-name', 'api-key')
    const rows = dialog.querySelectorAll('.ui-row-list__row')
    const row2 = rows[1] as HTMLElement
    const row2Source = Array.from(row2.querySelectorAll('[role="radio"]')).find(
      (r) => r.textContent?.trim() === 'Use existing secret',
    )
    expect(row2Source, 'header value source control missing').toBeTruthy()
    fireEvent.click(row2Source!)
    const picker = row2.querySelector('select') as HTMLSelectElement
    await pickSecret(picker, 'secrow:aaaaaaaa', 'prod api key')

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

  it('offers an em-dashed "None" as the empty choice, not the text of an escape (nocx-0sagl)', async () => {
    const { container } = mount()
    await waitForRows(container, 0)
    const dialog = openNew(container)
    clickButton(dialog, 'Add header')

    const row = dialog.querySelector('.ui-row-list__row') as HTMLElement
    clickSegment(row, 'Use existing secret')
    const picker = row.querySelector('select') as HTMLSelectElement
    // The placeholder is a JS string, so it must arrive through an
    // expression: as a JSX string attribute the escapes are never
    // interpreted and the person reads the source code of a dash.
    await waitFor(() => {
      expect(Array.from(picker.options).map((o) => o.text)).toContain('\u2014 None \u2014')
    })
    expect(picker.textContent).not.toContain('u2014')
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

    // The literal row's value is visible; the secret row's picker is bound
    // to the stored row handle — a reference, never material.
    expect((dialog.querySelector('#endpoint-header-0-value') as HTMLInputElement).value).toBe(
      'nocx',
    )
    const picker = dialog.querySelector('select') as HTMLSelectElement
    expect(picker.value).toBe('secrow:aaaaaaaa')

    // Saving without touching them re-sends the same rows through update.
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
    osKeyAvailable: false,
    osKeyCapable: false,
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
  osKeyAvailable: false,
  osKeyCapable: true,
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
  it('asks a SEALED vault the moment a picker is on screen — the vault layer is what raises the unlock', async () => {
    const vault = sealedVault(PASSWORD_ROWS)
    const { container, inventory, ctrl } = mount([], vault)
    await ctrl.refresh()
    expect(ctrl.status()?.state).toBe('sealed')
    await waitForRows(container, 0)
    expect(inventory).not.toHaveBeenCalled() // the LIST never asks

    // A blank form shows no picker and wants nothing from the vault.
    const dialog = openNew(container)
    await flush()
    expect(inventory).not.toHaveBeenCalled()

    // Switching the key's source to the vault puts a picker on screen —
    // and a picker renders secret NAMES, which only the vault can answer.
    // The caller must not read the vault's state and decide not to ask: the
    // sealed seam (dispatcher.ts) raises the unlock for any call that lands
    // on a sealed vault and re-sends it once unlocked.
    clickSegment(dialog, 'Use existing secret')
    await waitFor(() => {
      expect(inventory).toHaveBeenCalled()
    })
  })

  it('asks on open when the key IS a bound row, and names it — never its handle', async () => {
    const vault = sealedVault(PASSWORD_ROWS)
    const { container, ctrl, status, inventory } = mount(
      [ep({ name: 'provider', credential: 'secrow:aaaaaaaa' })],
      vault,
    )
    await ctrl.refresh()
    await waitForRows(container, 1)
    expect(inventory).not.toHaveBeenCalled()

    // The editor opens on "Use existing secret" with the row bound, so the
    // picker is on screen from the first frame and asks at once.
    const dialog = openEdit(container, 'provider')
    await waitFor(() => {
      expect(inventory).toHaveBeenCalled()
    })

    // The unlock resolves: the vault is open and the rows arrive.
    status.mockResolvedValue(UNSEALED_STATUS)
    await ctrl.refresh()

    const picker = dialog.querySelector('select') as HTMLSelectElement
    await waitFor(() => {
      expect(Array.from(picker.options).map((o) => o.text)).toContain('prod api key')
    })
    // The bound row reads as what it IS. A picker labelling it
    // `secrow:aaaaaaaa` shows the person an opaque id where the name of
    // their own secret belongs.
    expect(picker.value).toBe('secrow:aaaaaaaa')
    expect(picker.selectedOptions[0].text).toBe('prod api key')
    expect(Array.from(picker.options).map((o) => o.text)).not.toContain('secrow:aaaaaaaa')
  })

  it('does not ask an UNINITIALIZED vault: there is nothing to unlock', async () => {
    const vault = sealedVault(PASSWORD_ROWS, 'uninitialized')
    const { container, inventory, ctrl } = mount([], vault)
    await ctrl.refresh()
    expect(ctrl.status()?.state).toBe('uninitialized')
    await waitForRows(container, 0)

    const dialog = openNew(container)
    clickSegment(dialog, 'Use existing secret')
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

    // The person chose not to unlock: the deferred call rejects with the
    // cancellation the vault layer speaks.
    inventory.mockRejectedValueOnce(new VaultOperationCancelledError())
    const dialog = openEdit(container, 'provider')
    await waitFor(() => {
      expect(inventory).toHaveBeenCalledTimes(1)
    })

    // The editor still works — a dismissal is a choice, not a failure.
    fillField(container, 'endpoint-name', 'renamed')
    expect((container.querySelector('#endpoint-name') as HTMLInputElement).value).toBe('renamed')
    const picker = dialog.querySelector('select') as HTMLSelectElement
    expect(Array.from(picker.options).map((o) => o.text)).not.toContain('prod api key')

    // And the refusal is not remembered as a store failure: unsealing the
    // vault later loads the rows the list needs for its credential state.
    status.mockResolvedValue(UNSEALED_STATUS)
    await ctrl.refresh()
    await waitFor(() => {
      expect(inventory).toHaveBeenCalledTimes(2)
    })
  })
})

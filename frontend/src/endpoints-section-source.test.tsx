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
import { createVaultState, type VaultController } from './vault'
import type { VaultClient, InventoryEntry } from './vault-client'

afterEach(cleanup)

function ep(overrides: Partial<Endpoint> = {}): Endpoint {
  return {
    id: 'endpoint:custom:provider:1',
    name: 'provider',
    baseUrl: 'https://api.example.com/v1',
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
    expect(inventory).toHaveBeenCalled()

    // Choose the existing secret: the picker lists the vault's password rows.
    clickSegment(dialog, 'Use existing secret')
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

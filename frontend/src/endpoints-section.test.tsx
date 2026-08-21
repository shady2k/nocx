// @vitest-environment jsdom
/**
 * Component-level acceptance for the AI endpoints surface (nocx-kn9q,
 * design §4.5, ADR-0030).
 *
 * Drives the real EndpointsSection the way a user drives it — the buttons,
 * not the handlers — against a client whose four methods are spied, and
 * asserts the wire and the surface together: an add reaches
 * endpoints.create with the key once, an edit reaches endpoints.update with
 * the unchanged id, a delete reaches endpoints.delete after a confirm, a
 * refused submit keeps every call off the wire and announces through the
 * kit gate, and a backend refusal is said on the surface.
 */
import { describe, it, expect, vi, afterEach, type Mock } from 'vitest'
import { cleanup, render, fireEvent } from '@solidjs/testing-library'
import { EndpointsSection } from './endpoints-section'
import { EndpointClient, type Endpoint, type EndpointWrite } from './endpoints'
import { Dispatcher, RpcError } from './dispatcher'
import { clearToasts, toasts } from './ui'
import {
  SetupDialog,
  VaultOperationCancelledError,
  createVaultState,
  type VaultController,
} from './vault'
import type { InventoryEntry, VaultClient } from './vault-client'

/** One stored endpoint as the wire declares it. */
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

/**
 * A recording client over a mutable store: list reads the store, create
 * pushes, update replaces in place, delete removes — so a save's reload
 * shows the change, exactly the round trip the real backend makes.
 */
function createHarness(initial: Endpoint[] = [], opts: { firstListError?: Error } = {}) {
  const store: Endpoint[] = [...initial]
  let next = 1
  const client = new EndpointClient(new Dispatcher())
  // The real client's methods are async; these fakes match their signatures
  // and answer from the store, so there is nothing to await.
  // eslint-disable-next-line @typescript-eslint/require-await
  const listEndpoints = vi.spyOn(client, 'listEndpoints').mockImplementation(async () => {
    // The mount-time load is the first call; a harness that wants the load
    // to fail must arm it before the component renders, or the rejection
    // lands on the retry instead.
    if (opts.firstListError) {
      const err = opts.firstListError
      opts.firstListError = undefined
      throw err
    }
    return [...store]
  })
  const createEndpoint = vi
    .spyOn(client, 'createEndpoint')
    // eslint-disable-next-line @typescript-eslint/require-await
    .mockImplementation(async (input: EndpointWrite) => {
      const created: Endpoint = {
        id: `endpoint:custom:${input.name.toLowerCase().replace(/[^a-z0-9]+/g, '-')}:${next++}`,
        name: input.name,
        baseUrl: input.baseUrl,
        schema: 'openai-compatible',
        // The backend mints the key into the vault and returns only the row
        // handle — the value never crosses back.
        credential: input.key !== '' ? `secrow:${next++}` : null,
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
      }
      store[index] = updated
      return updated
    })
  const deleteEndpoint = vi
    .spyOn(client, 'deleteEndpoint')
    // eslint-disable-next-line @typescript-eslint/require-await
    .mockImplementation(async (id: string) => {
      store.splice(
        store.findIndex((e) => e.id === id),
        1,
      )
      return {}
    })
  const probeEndpoint = vi.spyOn(client, 'probeEndpoint').mockImplementation(
    // eslint-disable-next-line @typescript-eslint/require-await
    async (input: { name: string; baseUrl: string; key: string; model: string }) => {
      // A backend probe answers with a result — the Test button's whole
      // contract is that a failed probe is a RESULT, not an error.
      return {
        name: input.name,
        model: input.model,
        kind: 'model' as const,
        ok: true,
        elapsedMs: 12,
        at: new Date().toISOString(),
      }
    },
  )
  return {
    client,
    listEndpoints,
    createEndpoint,
    updateEndpoint,
    deleteEndpoint,
    probeEndpoint,
    store,
  }
}

function mount(
  initial: Endpoint[] = [],
  opts?: { firstListError?: Error; vaultController?: VaultController },
) {
  const harness = createHarness(initial, opts)
  const container = document.body.appendChild(document.createElement('div'))
  render(
    () => <EndpointsSection client={harness.client} vaultController={opts?.vaultController} />,
    { container },
  )
  return { ...harness, container }
}

afterEach(() => {
  clearToasts()
  vi.clearAllMocks()
  cleanup()
  document.body.innerHTML = ''
})

function rows(container: HTMLElement): HTMLElement[] {
  return Array.from(container.querySelectorAll<HTMLElement>('.ui-collection-row'))
}

async function waitForRows(container: HTMLElement, count: number) {
  await vi.waitFor(() => {
    expect(rows(container).length).toBe(count)
  })
}

function findDialogByTitle(container: HTMLElement, partial: string): HTMLElement | null {
  const titles = container.querySelectorAll('.nocx-dialog__title')
  for (const t of titles) {
    if (t.textContent && t.textContent.includes(partial)) return t.closest('.nocx-dialog')
  }
  return null
}

/** The confirm dialog mounts on document.body (showConfirm's own root), so
 *  it is found by its message text, not its title. */
function findConfirmDialog(message: string): HTMLElement | null {
  const dialogs = document.querySelectorAll('.nocx-dialog')
  for (const d of dialogs) {
    const msg = d.querySelector('.nocx-dialog__message')
    if (msg?.textContent === message) return d as HTMLElement
  }
  return null
}

function clickButton(container: HTMLElement, label: string, scope?: HTMLElement) {
  const root = scope ?? container
  const btn = Array.from(root.querySelectorAll('.ui-button')).find(
    (b) => b.textContent?.trim() === label,
  )
  expect(btn, `button "${label}" not found`).toBeTruthy()
  fireEvent.click(btn!)
}

function fillField(container: HTMLElement, id: string, value: string) {
  const field = container.querySelector(`#${id}`) as HTMLInputElement
  expect(field, `field #${id} not found`).toBeTruthy()
  fireEvent.input(field, { target: { value } })
}

function openNew(container: HTMLElement) {
  clickButton(container, '+ New endpoint')
  const dialog = findDialogByTitle(container, 'New Endpoint')
  expect(dialog, 'new-endpoint dialog did not open').toBeTruthy()
  return dialog!
}

function openEdit(container: HTMLElement, name: string) {
  const editBtn = container.querySelector(`.ui-collection-row__actions [aria-label="Edit ${name}"]`)
  expect(editBtn, `Edit button for "${name}" not found`).toBeTruthy()
  fireEvent.click(editBtn!)
  const dialog = findDialogByTitle(container, name)
  expect(dialog, `edit dialog for "${name}" did not open`).toBeTruthy()
  return dialog!
}

function toastMessages(): string[] {
  return toasts().map((t) => t.message)
}

/** Flush the microtask chain deterministically — the repo's convention for
 *  "let a promise rejection propagate" (no real timers, AGENTS.md). */
const flush = async (): Promise<void> => {
  for (let i = 0; i < 5; i++) await Promise.resolve()
}

/** The vault seam harness: a REAL vault controller over a stubbed client.
 *  `status` and `setup` stay visible so a test can re-mock them. */
interface VaultHarness {
  ctrl: VaultController
  client: VaultClient
  status: Mock
  setup: Mock
  /** The vault's rows mock — a test supplies the entries (or rejection) per
   *  case before the effect's unsealed-triggered load reads it. */
  inventory: Mock
}

/** A real controller on a fresh install: the vault is uninitialized with no
 *  OS key, so any secret mint must raise the vault layer's own setup sheet
 *  (the nocx-v64o behavior the connections path already has). */
function vaultHarness(statusOverride: Record<string, unknown> = {}): VaultHarness {
  const status = vi.fn().mockResolvedValue({
    state: 'uninitialized' as const,
    osKeyAvailable: false,
    osKeyCapable: false,
    hasPassphrase: false,
    autoSealMinutes: 0,
    providers: [],
    defaultProvider: null,
    ...statusOverride,
  })
  const setup = vi.fn().mockResolvedValue({})
  /** The vault's rows, for the row credential state (nocx-9bx0m): a
   *  referenced row absent from the inventory is a deleted credential. The
   *  test supplies the entries per case. */
  const inventory = vi.fn().mockResolvedValue({ entries: [] as InventoryEntry[] })
  const client = { status, setup, inventory } as unknown as VaultClient
  const ctrl = createVaultState(client)
  return { ctrl, client, status, setup, inventory }
}
/** Mount the section with the vault seam AND the vault layer's own setup
 *  dialog, wired exactly as main.tsx wires them — so a key-creation save on
 *  an unprotected install raises the real setup sheet and resumes through
 *  it, the same journey a person takes. */
function mountWithVault(initial: Endpoint[] = [], vault: VaultHarness = vaultHarness()) {
  const harness = createHarness(initial)
  const container = document.body.appendChild(document.createElement('div'))
  render(
    () => (
      <>
        <EndpointsSection
          client={harness.client}
          vaultController={vault.ctrl}
          vaultClient={vault.client}
        />
        <SetupDialog
          open={vault.ctrl.showSetup()}
          onClose={() => vault.ctrl.closeSetup()}
          onSetupComplete={() => vault.ctrl.onSetupDone()}
          vaultClient={vault.client}
        />
      </>
    ),
    { container },
  )
  return { ...harness, ...vault, container }
}

describe('AI endpoints surface — real surface, real client seam', () => {
  it('adds an endpoint: the fields reach endpoints.create and the saved row appears', async () => {
    const { container, createEndpoint } = mount()
    await waitForRows(container, 0)

    const dialog = openNew(container)
    fillField(container, 'endpoint-name', 'My provider')
    fillField(container, 'endpoint-base-url', 'https://api.example.com/v1')
    fillField(container, 'endpoint-key', 'sk-live-abc')
    clickButton(dialog, 'Add model')
    fillField(container, 'endpoint-model-0-name', 'gpt-4o')
    fillField(container, 'endpoint-model-0-alias', 'Flagship')
    clickButton(dialog, 'Create Endpoint')

    await vi.waitFor(() => {
      expect(createEndpoint).toHaveBeenCalledTimes(1)
    })
    // No schema on the wire: the form has no dialect control while one
    // schema exists (design §4.5, decision 2), so the backend owns the
    // value and completes it at the wire seam (ws_endpoints.go
    // resolveEndpointSchema) — this exact absence is what that default
    // fills, and it is pinned on the other side by the transport's
    // renderer-shape over-socket test (nocx-qtim).
    expect(createEndpoint.mock.calls[0][0]).toEqual({
      name: 'My provider',
      baseUrl: 'https://api.example.com/v1',
      key: 'sk-live-abc',
      // No schema on the wire: the form has no dialect control while one
      // schema exists (design §4.5, decision 2), so the backend owns the
      // value and completes it at the wire seam (ws_endpoints.go
      // resolveEndpointSchema) — this exact absence is what that default
      // fills, and it is pinned on the other side by the transport's
      // renderer-shape over-socket test (nocx-qtim).
      credential: '',
      headers: [],
      models: [{ name: 'gpt-4o', alias: 'Flagship' }],
    })

    // The saved row appears in the list — and never the key itself, which is
    // the whole point: a key crosses to the backend once and never back.
    await waitForRows(container, 1)
    const row = rows(container)[0]
    expect(row.textContent).toContain('My provider')
    expect(row.textContent).not.toContain('sk-live-abc')
    expect(toastMessages()).toContain('Saved "My provider"')
  })

  it('the dialog is ordered by what Test actually tests: headers before the button, models after', async () => {
    const { container } = mount()
    await waitForRows(container, 0)
    const dialog = openNew(container)

    // Custom headers ride on every request the probe sends
    // (ws_assistant.go resolveProbeHeaders), so they are part of the
    // CONNECTION and belong above the button that checks it. Models are
    // what the connection then offers — and the model field discovers them
    // from a successful test — so they belong below it. The dialog used to
    // read name, url, key, TEST, models, headers, which asked a person to
    // configure half the connection after already testing it.
    const headers = dialog.querySelector('[aria-label="Custom headers"]')!
    const testRow = dialog.querySelector('.ep-test-row')!
    const models = dialog.querySelector('[aria-label="Endpoint models"]')!
    expect(headers).toBeTruthy()
    expect(testRow).toBeTruthy()
    expect(models).toBeTruthy()

    const before = Node.DOCUMENT_POSITION_FOLLOWING
    expect(headers.compareDocumentPosition(testRow) & before).toBeTruthy()
    expect(testRow.compareDocumentPosition(models) & before).toBeTruthy()
  })

  it('Enter with the model list open takes the option instead of saving; Enter with it closed submits (nocx-0plm6)', async () => {
    const { container, createEndpoint, probeEndpoint } = mount()
    await waitForRows(container, 0)

    const dialog = openNew(container)
    fillField(container, 'endpoint-name', 'My provider')
    fillField(container, 'endpoint-base-url', 'https://api.example.com/v1')
    fillField(container, 'endpoint-key', 'sk-live-abc')
    clickButton(dialog, 'Add model')
    // Focus discovers the endpoint's models over the wire — a connection
    // probe answers with the offering list.
    probeEndpoint.mockResolvedValueOnce({
      name: 'My provider',
      model: '',
      kind: 'connection',
      ok: true,
      models: ['gpt-4o', 'qwen3'],
      elapsedMs: 12,
      at: new Date().toISOString(),
    })
    const modelInput = dialog.querySelector<HTMLInputElement>('#endpoint-model-0-name')!
    modelInput.focus()
    await vi.waitFor(() => {
      expect(modelInput.getAttribute('aria-expanded')).toBe('true')
    })
    // The list lives inside the dialog element (the top layer) — portalled
    // out of the panel that used to clip it, so it is never behind the
    // dialog and never parked in the body.
    const list = dialog.querySelector('.ui-suggestion-field__list')
    expect(list).not.toBeNull()
    expect(document.body.contains(list)).toBe(true)

    // Type to narrow and activate the first match — the state the owner is
    // in when the dropdown is open: Enter must pick the option, not save.
    fireEvent.input(modelInput, { target: { value: 'g' } })
    expect(modelInput.getAttribute('aria-activedescendant')).toBe(
      'endpoint-model-0-name-suggestions-option-0',
    )
    fireEvent.keyDown(modelInput, { key: 'Enter' })
    await flush()
    expect(modelInput.value).toBe('gpt-4o')
    expect(createEndpoint).not.toHaveBeenCalled()

    // The take closed the list; Enter now means "save" again — the same
    // walk the owner takes, one keystroke apart.
    expect(modelInput.getAttribute('aria-expanded')).toBe('false')
    fireEvent.keyDown(modelInput, { key: 'Enter' })
    await vi.waitFor(() => {
      expect(createEndpoint).toHaveBeenCalledTimes(1)
    })
    expect(createEndpoint.mock.calls[0][0].models[0].name).toBe('gpt-4o')
  })

  it('closing the dialog with the list open leaves nothing floating in the portal host (nocx-0plm6)', async () => {
    const { container, probeEndpoint } = mount()
    await waitForRows(container, 0)

    const dialog = openNew(container)
    fillField(container, 'endpoint-name', 'My provider')
    fillField(container, 'endpoint-base-url', 'https://api.example.com/v1')
    clickButton(dialog, 'Add model')
    probeEndpoint.mockResolvedValueOnce({
      name: 'My provider',
      model: '',
      kind: 'connection',
      ok: true,
      models: ['gpt-4o', 'qwen3'],
      elapsedMs: 12,
      at: new Date().toISOString(),
    })
    const modelInput = dialog.querySelector<HTMLInputElement>('#endpoint-model-0-name')!
    modelInput.focus()
    await vi.waitFor(() => {
      expect(modelInput.getAttribute('aria-expanded')).toBe('true')
    })
    // The list is open, inside the dialog element — its portal host.
    const list = dialog.querySelector('.ui-suggestion-field__list')!
    expect(list.hasAttribute('hidden')).toBe(false)

    // Close the dialog the way a person does — a real pointer gesture on
    // the Cancel button (pointerdown, then the click). The pointerdown is
    // the dismissal's event; the list must not survive the gesture, and
    // the portal host must not hold a floating list afterwards. (A naive
    // portal without dismissal wiring strands the open list over whatever
    // the dialog covered — this fails.)
    const cancel = Array.from(dialog.querySelectorAll('.ui-button')).find(
      (b) => b.textContent?.trim() === 'Cancel',
    )!
    fireEvent.pointerDown(cancel)
    fireEvent.click(cancel)
    expect(list.hasAttribute('hidden')).toBe(true)
    expect(document.querySelector('.ui-suggestion-field__list:not([hidden])')).toBeNull()
  })

  it('opens on the key SOURCE, never the key material: a saved credential is the bound row', async () => {
    const { container } = mount([
      ep({ id: 'endpoint:custom:provider:1', name: 'provider', credential: 'secrow:9' }),
    ])
    await waitForRows(container, 1)

    const dialog = openEdit(container, 'provider')
    // The source control is the key field's owner (nocx-rzjw): a saved
    // credential opens on "Use existing secret" with the bound row — the
    // material never crosses back, and keeping the key is a visible choice.
    const existing = dialog.querySelector('select') as HTMLSelectElement
    expect(existing).toBeTruthy()
    expect(existing.value).toBe('secrow:9')
    // No password input is drawn in this mode: there is no key to type.
    expect(dialog.querySelector('#endpoint-key')).toBeNull()

    // Switching to "Type a new one" reveals the EMPTY password field — an
    // input, never a stored value. The segmented control is the dialog's
    // first (the key source; header rows come below), and its first segment
    // is "Type a new one".
    const newSegment = dialog.querySelector('.ui-segmented-control [role="radio"]')
    expect(newSegment).toBeTruthy()
    fireEvent.click(newSegment!)
    const keyInput = dialog.querySelector('#endpoint-key') as HTMLInputElement
    expect(keyInput).toBeTruthy()
    expect(keyInput.type).toBe('password')
    expect(keyInput.value).toBe('')
  })

  it('edits an endpoint through endpoints.update with the unchanged id', async () => {
    const { container, updateEndpoint } = mount([
      ep({ id: 'endpoint:custom:provider:1', name: 'provider' }),
    ])
    await waitForRows(container, 1)

    const dialog = openEdit(container, 'provider')
    fillField(container, 'endpoint-name', 'Renamed provider')
    clickButton(dialog, 'Save Endpoint')

    await vi.waitFor(() => {
      expect(updateEndpoint).toHaveBeenCalledTimes(1)
    })
    const [id, input] = updateEndpoint.mock.calls[0]
    expect(id).toBe('endpoint:custom:provider:1')
    expect(input.name).toBe('Renamed provider')
    // A blank "Type a new one" key on update means "keep the existing
    // material" (design §4.5.4) — the surface sends '', never a fabricated
    // value.
    expect(input.key).toBe('')
    expect(input.credential).toBe('')
    expect(input.headers).toEqual([])

    await vi.waitFor(() => {
      expect(rows(container)[0].textContent).toContain('Renamed provider')
    })
    expect(toastMessages()).toContain('Saved "Renamed provider"')
  })

  it('deletes an endpoint only after the confirm, through endpoints.delete', async () => {
    const { container, deleteEndpoint } = mount([
      ep({ id: 'endpoint:custom:provider:1', name: 'provider' }),
    ])
    await waitForRows(container, 1)

    const del = container.querySelector(
      '.ui-collection-row__actions [aria-label="Delete provider"]',
    )
    expect(del).toBeTruthy()
    fireEvent.click(del!)

    // Confirm dialog: the delete must not have happened yet.
    const confirm = findConfirmDialog('Delete "provider"?')
    expect(confirm, 'confirm dialog did not open').toBeTruthy()
    expect(deleteEndpoint).not.toHaveBeenCalled()

    clickButton(confirm!, 'OK', confirm!)
    await vi.waitFor(() => {
      expect(deleteEndpoint).toHaveBeenCalledWith('endpoint:custom:provider:1')
    })
    await waitForRows(container, 0)
    expect(toastMessages()).toContain('Deleted "provider"')
  })

  it('refuses a submit with nothing filled: per-field message, focus, no wire call', async () => {
    const { container, createEndpoint } = mount()
    await waitForRows(container, 0)

    const dialog = openNew(container)
    clickButton(dialog, 'Create Endpoint')

    // The kit gate announces the first failing rule with the count and
    // focuses the first offender; nothing reaches the wire. The gate is
    // async — the toast and the focus land on a microtask after the click.
    expect(createEndpoint).not.toHaveBeenCalled()
    await vi.waitFor(() => {
      expect(toastMessages()).toContain('Name is required — 3 fields need attention')
    })
    expect(document.activeElement?.id).toBe('endpoint-name')
    const nameError = dialog.querySelector('#endpoint-name__error')
    expect(nameError?.textContent).toBe('Name is required')
  })

  it('requires a model: no rows is refused, an empty model row is refused and focused', async () => {
    const { container, createEndpoint } = mount()
    await waitForRows(container, 0)

    const dialog = openNew(container)
    fillField(container, 'endpoint-name', 'provider')
    fillField(container, 'endpoint-base-url', 'https://api.example.com/v1')
    clickButton(dialog, 'Create Endpoint')

    expect(createEndpoint).not.toHaveBeenCalled()
    // No rows: no control to focus — the gate says so honestly.
    await vi.waitFor(() => {
      expect(toastMessages()).toContain('Add at least one model — could not focus the first field')
    })
    // The group error is on the surface too, through the kit's field-error
    // identity inside the list — one error vocabulary, exact message.
    const groupError = dialog.querySelector('.ui-row-list .ui-field-error')
    expect(groupError?.textContent).toBe('Add at least one model')

    clickButton(dialog, 'Add model')
    clickButton(dialog, 'Create Endpoint')
    expect(createEndpoint).not.toHaveBeenCalled()
    await vi.waitFor(() => {
      expect(toastMessages()).toContain('Model name is required')
    })
    expect(document.activeElement?.id).toBe('endpoint-model-0-name')

    fillField(container, 'endpoint-model-0-name', 'gpt-4o')
    clickButton(dialog, 'Create Endpoint')
    await vi.waitFor(() => {
      expect(createEndpoint).toHaveBeenCalledTimes(1)
    })
  })

  it('refuses a base URL that is not an absolute http(s) URL and focuses it', async () => {
    const { container, createEndpoint } = mount()
    await waitForRows(container, 0)

    const dialog = openNew(container)
    fillField(container, 'endpoint-name', 'provider')
    fillField(container, 'endpoint-base-url', 'not a url')
    clickButton(dialog, 'Add model')
    fillField(container, 'endpoint-model-0-name', 'gpt-4o')
    clickButton(dialog, 'Create Endpoint')

    expect(createEndpoint).not.toHaveBeenCalled()
    await vi.waitFor(() => {
      expect(toastMessages()).toContain('Must be an absolute http(s) URL')
    })
    expect(document.activeElement?.id).toBe('endpoint-base-url')
  })

  it('says a backend refusal on the surface — the vault sealed, the store refused', async () => {
    const { container, createEndpoint } = mount()
    await waitForRows(container, 0)
    createEndpoint.mockRejectedValueOnce(
      new RpcError('vault is sealed', -32001, { reason: 'vault-sealed' }),
    )

    const dialog = openNew(container)
    fillField(container, 'endpoint-name', 'provider')
    fillField(container, 'endpoint-base-url', 'https://api.example.com/v1')
    clickButton(dialog, 'Add model')
    fillField(container, 'endpoint-model-0-name', 'gpt-4o')
    clickButton(dialog, 'Create Endpoint')

    await vi.waitFor(() => {
      expect(toastMessages()).toContain('Could not save the endpoint: vault is sealed')
    })
    // The dialog stays open — the user can fix what the backend refused.
    expect(findDialogByTitle(container, 'New Endpoint')).toBeTruthy()
  })
  it('tests the draft through the Test button and shows the streaming verdict', async () => {
    const { container, probeEndpoint } = mount()
    await waitForRows(container, 0)

    const dialog = openNew(container)
    // With no base URL there is nothing to reach, so the control is
    // unavailable AND says why — a disabled button that stays silent is the
    // defect this pair of assertions exists to hold closed. With no model it
    // still offers the connection check, so its label says which check it is
    // about to run.
    const btn = Array.from(dialog.querySelectorAll('.ui-button')).find((b) =>
      b.textContent?.includes('Test connection'),
    )
    expect(btn, 'Test connection button not found').toBeTruthy()
    expect((btn as HTMLButtonElement).disabled).toBe(true)
    expect(dialog.textContent).toContain('Add a base URL to test the connection')

    fillField(container, 'endpoint-name', 'Local')
    fillField(container, 'endpoint-base-url', 'http://127.0.0.1:11434/v1')
    clickButton(dialog, 'Add model')
    fillField(container, 'endpoint-model-0-name', 'qwen3')
    expect((btn as HTMLButtonElement).disabled).toBe(false)

    clickButton(dialog, 'Test endpoint')

    await vi.waitFor(() => {
      expect(probeEndpoint).toHaveBeenCalledTimes(1)
    })
    expect(probeEndpoint.mock.calls[0][0]).toEqual({
      name: 'Local',
      baseUrl: 'http://127.0.0.1:11434/v1',
      key: '',
      model: 'qwen3',
      headers: [],
    })
    await vi.waitFor(() => {
      expect(dialog.textContent).toContain('qwen3 answered in')
    })
  })

  it('a cancelled unlock never paints a Test failure (ADR-0032)', async () => {
    const { container, probeEndpoint } = mount()
    const dialog = openNew(container)
    fillField(container, 'endpoint-name', 'Local')
    fillField(container, 'endpoint-base-url', 'http://127.0.0.1:11434/v1')
    clickButton(dialog, 'Add model')
    fillField(container, 'endpoint-model-0-name', 'qwen3')

    // A successful verdict first: the cancellation below must not replace
    // what a working test showed with a failure the person did not cause.
    clickButton(dialog, 'Test endpoint')
    await vi.waitFor(() => {
      expect(dialog.textContent).toContain('qwen3 answered in')
    })

    probeEndpoint.mockRejectedValueOnce(new VaultOperationCancelledError())
    clickButton(dialog, 'Test endpoint')
    await vi.waitFor(() => {
      expect(probeEndpoint).toHaveBeenCalledTimes(2)
    })
    // The person chose not to unlock: the test did not run, and nothing
    // failed. Pressing Test clears the previous verdict (a new attempt
    // started), and the dismissal must not paint "Test failed" in its
    // place — the badge area stays empty, silently.
    await vi.waitFor(() => {
      expect(dialog.textContent).not.toContain('Test failed')
    })
    expect(dialog.textContent).not.toContain('answered in')
  })

  it('tests a SAVED endpoint by naming it — the key stays blank and the backend resolves the stored credential', async () => {
    const { container, probeEndpoint } = mount([
      ep({
        id: 'endpoint:custom:provider:1',
        name: 'provider',
        baseUrl: 'https://api.example.com/v1',
        credential: 'secrow:0123456789abcdef',
        models: [{ name: 'gpt-4o', alias: null }],
      }),
    ])
    await waitForRows(container, 1)

    const dialog = openEdit(container, 'provider')
    // The key SOURCE is pre-selected to the bound row (the material never
    // crosses back, ADR-0030 §3); the empty "Type a new one" field exists
    // only after switching, so the source is what the wire sees.
    const existing = dialog.querySelector('select') as HTMLSelectElement
    expect(existing.value).toBe('secrow:0123456789abcdef')

    clickButton(dialog, 'Test endpoint')

    await vi.waitFor(() => {
      expect(probeEndpoint).toHaveBeenCalledTimes(1)
    })
    // The probe NAMES the record and lets the backend resolve its
    // credential — exactly how connections.test names a profile. The key
    // never crosses the wire in the direction the renderer could have sent
    // it (it has none), and it must not be re-fetched here either.
    expect(probeEndpoint.mock.calls[0][0]).toEqual({
      name: 'provider',
      baseUrl: 'https://api.example.com/v1',
      key: '',
      model: 'gpt-4o',
      endpointId: 'endpoint:custom:provider:1',
      headers: [],
    })
    await vi.waitFor(() => {
      expect(dialog.textContent).toContain('gpt-4o answered in')
    })
  })

  it('shows a failed probe as a result, not a crash', async () => {
    const { container, probeEndpoint } = mount()
    await waitForRows(container, 0)
    // The probe contract: a failed dial is a RESULT with ok:false, never
    // an RPC error (the engine returns outcomes, not exceptions).
    probeEndpoint.mockResolvedValueOnce({
      name: 'Local',
      model: 'qwen3',
      kind: 'model' as const,
      ok: false,
      error: 'dial tcp: connection refused',
      elapsedMs: 0,
      at: new Date().toISOString(),
    })

    const dialog = openNew(container)
    fillField(container, 'endpoint-name', 'Local')
    fillField(container, 'endpoint-base-url', 'http://127.0.0.1:1/v1')
    clickButton(dialog, 'Add model')
    fillField(container, 'endpoint-model-0-name', 'qwen3')
    clickButton(dialog, 'Test endpoint')

    await vi.waitFor(() => {
      expect(dialog.textContent).toContain('Test failed: dial tcp: connection refused')
    })
  })

  it('says a failed list load on the surface and retries from there', async () => {
    const { container, listEndpoints } = mount(
      [ep({ id: 'endpoint:custom:provider:1', name: 'provider' })],
      { firstListError: new RpcError('endpoints not available', -32601) },
    )

    await vi.waitFor(() => {
      expect(container.textContent).toContain("Couldn't load endpoints")
    })
    expect(container.textContent).toContain('endpoints not available')

    clickButton(container, 'Retry')
    await waitForRows(container, 1)
    expect(listEndpoints).toHaveBeenCalledTimes(2)
  })
})

describe('the vault seam — a key is minted through the vault layer (nocx-8rwj)', () => {
  /** Fill a valid new-endpoint form (with a key) and return its dialog. */
  function fillNewWithKey(container: HTMLElement) {
    const dialog = openNew(container)
    fillField(container, 'endpoint-name', 'provider')
    fillField(container, 'endpoint-base-url', 'https://api.example.com/v1')
    fillField(container, 'endpoint-key', 'sk-live-abc')
    clickButton(dialog, 'Add model')
    fillField(container, 'endpoint-model-0-name', 'gpt-4o')
    return dialog
  }

  it('raises the setup sheet on a first run, then saves the endpoint the person typed', async () => {
    const { container, createEndpoint, ctrl } = mountWithVault()
    await waitForRows(container, 0)
    // The wire refuses: the backend mints the key into the vault BEFORE it
    // writes the record (capability/config.go CreateEndpoint), so the
    // refusal is atomic — nothing was stored, and the error carries the
    // vault reason saveSecretWithVault recognizes.
    createEndpoint.mockRejectedValueOnce(
      new RpcError('vault is not initialized', -32603, { reason: 'vault-uninitialized' }),
    )

    fillNewWithKey(container)
    clickButton(container, 'Create Endpoint')

    // The first attempt hit the missing vault; the vault layer's own setup
    // sheet is up (the nocx-v64o behavior), nothing was stored or toasted.
    await vi.waitFor(() => {
      expect(ctrl.showSetup()).toBe(true)
    })
    expect(createEndpoint).toHaveBeenCalledTimes(1)
    expect(toastMessages()).not.toContain('Could not save the endpoint:')
    expect(findDialogByTitle(container, 'New Endpoint')).toBeTruthy()

    // Setup completes (the SetupDialog's Done → onSetupComplete →
    // onSetupDone): the EXACT save is retried and the endpoint appears.
    ctrl.onSetupDone()
    await vi.waitFor(() => {
      expect(createEndpoint).toHaveBeenCalledTimes(2)
    })
    expect(createEndpoint.mock.calls[1][0].key).toBe('sk-live-abc')
    await waitForRows(container, 1)
    expect(rows(container)[0].textContent).toContain('provider')
    await vi.waitFor(() => {
      expect(toastMessages()).toContain('Saved "provider"')
    })
    // The editor closed (the dialog stays mounted in the DOM, hidden).
    const closed = findDialogByTitle(container, 'New Endpoint')
    expect(closed).toBeTruthy()
    expect((closed as HTMLDialogElement).open).toBe(false)
  })

  it('a cancelled setup leaves the editor open with the draft intact and nothing stored', async () => {
    const { container, createEndpoint, setup, ctrl } = mountWithVault()
    await waitForRows(container, 0)
    createEndpoint.mockRejectedValueOnce(
      new RpcError('vault is not initialized', -32603, { reason: 'vault-uninitialized' }),
    )

    const dialog = fillNewWithKey(container)
    clickButton(container, 'Create Endpoint')

    await vi.waitFor(() => {
      expect(ctrl.showSetup()).toBe(true)
    })

    // The person closes the setup sheet without setting protection up.
    ctrl.closeSetup()
    await flush()

    // What is on screen: the editor, still open, with everything they typed.
    expect(findDialogByTitle(container, 'New Endpoint')).toBeTruthy()
    expect((dialog.querySelector('#endpoint-name') as HTMLInputElement).value).toBe('provider')
    expect((dialog.querySelector('#endpoint-base-url') as HTMLInputElement).value).toBe(
      'https://api.example.com/v1',
    )
    expect((dialog.querySelector('#endpoint-key') as HTMLInputElement).value).toBe('sk-live-abc')
    // What is in the vault: nothing — setup never ran.
    expect(setup).not.toHaveBeenCalled()
    // What is on the wire: one refused attempt, no silent retry.
    expect(createEndpoint).toHaveBeenCalledTimes(1)
    // Nothing reported saved, and no new failure toast either — a cancelled
    // setup is not an error the endpoint form must shout about.
    expect(toastMessages()).not.toContain('Saved "provider"')
    expect(toastMessages()).not.toContain('Could not save the endpoint:')
  })

  it('a failed setup stays in the setup sheet; the save resumes only after it succeeds', async () => {
    const { container, createEndpoint, setup, ctrl } = mountWithVault()
    await waitForRows(container, 0)
    createEndpoint.mockRejectedValueOnce(
      new RpcError('vault is not initialized', -32603, { reason: 'vault-uninitialized' }),
    )

    fillNewWithKey(container)
    clickButton(container, 'Create Endpoint')
    await vi.waitFor(() => {
      expect(ctrl.showSetup()).toBe(true)
    })

    // The backend refuses the setup (a dead store, a keychain error): the
    // setup sheet says so inline and stays up; the endpoint save is not
    // retried and nothing is toasted by the endpoint form.
    setup.mockRejectedValueOnce(new Error('Backend refused'))
    fillField(container, 'vault-setup-passphrase', 'correct horse')
    fillField(container, 'vault-setup-confirm', 'correct horse')
    clickButton(container, 'Set Up')
    await vi.waitFor(() => {
      const err = container.querySelector('#vault-setup-passphrase__error')
      expect(err?.textContent).toBe('Backend refused')
    })
    expect(createEndpoint).toHaveBeenCalledTimes(1)
    expect(toastMessages()).not.toContain('Could not save the endpoint:')

    // The person retries and this time the setup completes with a recovery
    // code; Done dismisses it and the deferred save lands.
    setup.mockResolvedValueOnce({ recoveryCode: 'ABCD-1234-EFGH-5678' })
    clickButton(container, 'Set Up')
    await vi.waitFor(() => {
      expect(findDialogByTitle(container, 'Recovery Code')).toBeTruthy()
    })
    clickButton(container, 'Done')
    await vi.waitFor(() => {
      expect(createEndpoint).toHaveBeenCalledTimes(2)
    })
    await waitForRows(container, 1)
    expect(rows(container)[0].textContent).toContain('provider')
    expect(createEndpoint.mock.calls[1][0].key).toBe('sk-live-abc')
  })

  it('a new key on an edit rotates through the same seam, keeping the id', async () => {
    const { container, updateEndpoint, ctrl } = mountWithVault([
      ep({ id: 'endpoint:custom:provider:1', name: 'provider' }),
    ])
    await waitForRows(container, 1)
    updateEndpoint.mockRejectedValueOnce(
      new RpcError('vault is not initialized', -32603, { reason: 'vault-uninitialized' }),
    )

    const dialog = openEdit(container, 'provider')
    fillField(container, 'endpoint-key', 'sk-rotated')
    clickButton(dialog, 'Save Endpoint')

    await vi.waitFor(() => {
      expect(ctrl.showSetup()).toBe(true)
    })
    expect(updateEndpoint).toHaveBeenCalledTimes(1)

    ctrl.onSetupDone()
    await vi.waitFor(() => {
      expect(updateEndpoint).toHaveBeenCalledTimes(2)
    })
    const [id, input] = updateEndpoint.mock.calls[1]
    expect(id).toBe('endpoint:custom:provider:1')
    expect(input.key).toBe('sk-rotated')
    await vi.waitFor(() => {
      expect(toastMessages()).toContain('Saved "provider"')
    })
  })

  it('a sealed vault raises the unlock sheet naming the operation, and resumes after unseal', async () => {
    const { container, createEndpoint, status, ctrl } = mountWithVault()
    status.mockResolvedValue({
      state: 'sealed' as const,
      osKeyAvailable: false,
      osKeyCapable: false,
      hasPassphrase: true,
      autoSealMinutes: 0,
      providers: [],
      defaultProvider: null,
    })
    await waitForRows(container, 0)
    createEndpoint.mockRejectedValueOnce(
      new RpcError('vault is sealed', -32603, { reason: 'vault-sealed' }),
    )

    fillNewWithKey(container)
    clickButton(container, 'Create Endpoint')

    await vi.waitFor(() => {
      expect(ctrl.showUnlock()).toBe(true)
    })
    // The unlock prompt must say WHICH operation needs the vault open and
    // why now (nocx-s8jn) — the reason the endpoint save passed along.
    expect(ctrl.unlockReason()).toBe('save this endpoint key')
    expect(createEndpoint).toHaveBeenCalledTimes(1)

    ctrl.onUnsealDone()
    await vi.waitFor(() => {
      expect(createEndpoint).toHaveBeenCalledTimes(2)
    })
    await waitForRows(container, 1)
    expect(rows(container)[0].textContent).toContain('provider')
  })

  it('a save with no key never touches the vault seam', async () => {
    const { container, createEndpoint, ctrl } = mountWithVault()
    await waitForRows(container, 0)

    const dialog = openNew(container)
    fillField(container, 'endpoint-name', 'provider')
    fillField(container, 'endpoint-base-url', 'https://api.example.com/v1')
    clickButton(dialog, 'Add model')
    fillField(container, 'endpoint-model-0-name', 'gpt-4o')
    clickButton(dialog, 'Create Endpoint')

    await vi.waitFor(() => {
      expect(createEndpoint).toHaveBeenCalledTimes(1)
    })
    // No key on the wire means no secret minted: the vault is not a party,
    // so no sheet and no deferred save.
    expect(ctrl.showSetup()).toBe(false)
    expect(ctrl.showUnlock()).toBe(false)
    await waitForRows(container, 1)
  })
})

describe('the saved endpoint row (nocx-9bx0m)', () => {
  function clickRowTest(container: HTMLElement, name: string): HTMLButtonElement {
    const btn = container.querySelector<HTMLButtonElement>(
      `.ui-collection-row [aria-label="Test ${name}"]`,
    )
    expect(btn, `Test button for "${name}" not found`).toBeTruthy()
    fireEvent.click(btn!)
    return btn!
  }

  it('renders the row through the composite: one kind badge, meta text, and the status dot', async () => {
    const { container } = mount([
      ep({
        id: 'endpoint:custom:provider:1',
        name: 'provider',
        baseUrl: 'https://api.example.com/v1',
        credential: 'secrow:0123456789abcdef',
        models: [{ name: 'gpt-4o', alias: null }],
      }),
    ])
    await waitForRows(container, 1)

    const row = rows(container)[0]
    expect(row.querySelector('.ui-record-row__title')?.textContent).toBe('provider')
    // Exactly one badge: the schema kind. The old "Key saved" BADGE is gone —
    // the credential state is now the kit's status dot + text.
    const badges = row.querySelectorAll('.ui-badge')
    expect(badges.length).toBe(1)
    expect(badges[0].textContent).toBe('OpenAI-compatible')
    expect(row.querySelector('.ui-record-row__meta-text')?.textContent).toBe('1 model')
    // And NOTHING about the credential. A resolvable key is the absence of a
    // problem; the row speaks only to refuse. "Key saved" was a green
    // reassurance under every healthy endpoint and the owner struck it.
    expect(row.querySelector('.ui-record-row__status')).toBeNull()
    expect(row.textContent).not.toContain('Key saved')
  })

  it('a keyless endpoint renders "No key" as the neutral dot + text, and the Test stays enabled', async () => {
    const { container } = mount([ep({ id: 'endpoint:custom:provider:1', name: 'provider' })])
    await waitForRows(container, 1)

    const row = rows(container)[0]
    expect(row.querySelector('.ui-status-dot')?.getAttribute('data-tone')).toBe('neutral')
    expect(row.textContent).toContain('No key')
    // A no-key dial can still pass against a public endpoint — the check is
    // not doomed, so the row does not refuse it (nocx-q27y's connection
    // check needs no model and no key).
    const test = row.querySelector('[aria-label="Test provider"]') as HTMLButtonElement
    expect(test.disabled).toBe(false)
  })

  it('a saved endpoint row is tested by naming the record — the SAME client method the editor uses', async () => {
    const { container, probeEndpoint } = mount([
      ep({
        id: 'endpoint:custom:provider:1',
        name: 'provider',
        baseUrl: 'https://api.example.com/v1',
        credential: 'secrow:0123456789abcdef',
        models: [{ name: 'gpt-4o', alias: null }],
        headers: [{ name: 'X-Custom', value: 'abc', secret: null }],
      }),
    ])
    await waitForRows(container, 1)

    clickRowTest(container, 'provider')

    await vi.waitFor(() => {
      expect(probeEndpoint).toHaveBeenCalledTimes(1)
    })
    // The row's Test is the editor's Test on a saved endpoint: the key stays
    // blank so the backend resolves the stored credential (nocx-reu5), the
    // record is NAMED, and the stored custom headers ride — one call path,
    // watched through the client method the editor already uses.
    expect(probeEndpoint.mock.calls[0][0]).toEqual({
      name: 'provider',
      baseUrl: 'https://api.example.com/v1',
      key: '',
      model: '',
      endpointId: 'endpoint:custom:provider:1',
      headers: [{ name: 'X-Custom', value: 'abc', secret: null }],
    })
  })

  it('a connection outcome renders on the row in the editor vocabulary', async () => {
    const { container, probeEndpoint } = mount([
      ep({ id: 'endpoint:custom:provider:1', name: 'provider', credential: 'secrow:abc' }),
    ])
    await waitForRows(container, 1)
    probeEndpoint.mockResolvedValueOnce({
      name: 'provider',
      model: '',
      kind: 'connection' as const,
      ok: true,
      models: ['gpt-4o', 'qwen3'],
      elapsedMs: 41,
      at: new Date().toISOString(),
    })

    clickRowTest(container, 'provider')
    await vi.waitFor(() => {
      expect(rows(container)[0].textContent).toContain('Connected — 2 models offered')
    })
    expect(rows(container)[0].querySelector('.ui-status-dot')?.getAttribute('data-tone')).toBe('ok')
  })

  it('a refused outcome renders on the row with the editor sentence', async () => {
    const { container, probeEndpoint } = mount([
      ep({ id: 'endpoint:custom:provider:1', name: 'provider', credential: 'secrow:abc' }),
    ])
    await waitForRows(container, 1)
    probeEndpoint.mockResolvedValueOnce({
      name: 'provider',
      model: '',
      kind: 'connection' as const,
      ok: false,
      error: 'dial tcp: connection refused',
      elapsedMs: 0,
      at: new Date().toISOString(),
    })

    clickRowTest(container, 'provider')
    await vi.waitFor(() => {
      expect(rows(container)[0].textContent).toContain('Test failed: dial tcp: connection refused')
    })
    expect(rows(container)[0].querySelector('.ui-status-dot')?.getAttribute('data-tone')).toBe(
      'error',
    )
  })

  it('a sealed vault says nothing on the row and leaves the Test live — pressing it is what raises the unlock', async () => {
    const { container, probeEndpoint, ctrl } = mountWithVault(
      [ep({ id: 'endpoint:custom:provider:1', name: 'provider', credential: 'secrow:abc' })],
      vaultHarness({ state: 'sealed' as const, hasPassphrase: true }),
    )
    // The controller's status is null until it has fetched — the row reads
    // the live accessor, so let the fetch land before asserting the state.
    await ctrl.refresh()
    await waitForRows(container, 1)

    const row = rows(container)[0]
    // A locked vault is the vault's resting state, not this endpoint's
    // defect: the row does not narrate it, and does not pre-refuse the
    // check either. endpoints.probe raises vault.ErrVaultSealed, the
    // dispatcher seam normalizes it and the renderer raises the unlock and
    // re-sends (ADR-0032, ws_assistant.go resolveProbeCredential) — so the
    // check completes once the vault answers. Greying the button out was
    // the frontend refusing a path the backend had kept open.
    expect(row.querySelector('.ui-record-row__status')).toBeNull()
    const test = row.querySelector('[aria-label="Test provider"]') as HTMLButtonElement
    expect(test.disabled).toBe(false)
    fireEvent.click(test)
    await vi.waitFor(() => expect(probeEndpoint).toHaveBeenCalled())
  })

  it('a vanished vault says the key was deleted on the row and does not run the check', async () => {
    const { container, probeEndpoint, ctrl } = mountWithVault(
      [ep({ id: 'endpoint:custom:provider:1', name: 'provider', credential: 'secrow:abc' })],
      vaultHarness({ state: 'uninitialized' as const }),
    )
    await ctrl.refresh()
    await waitForRows(container, 1)

    const row = rows(container)[0]
    // No vault exists, so no secret can — the reference dangles: the
    // "deleted" sentence, never a doomed dial.
    expect(row.textContent).toContain("The endpoint's key was deleted — add it again")
    const test = row.querySelector('[aria-label="Test provider"]') as HTMLButtonElement
    expect(test.disabled).toBe(true)
    fireEvent.click(test)
    expect(probeEndpoint).not.toHaveBeenCalled()
  })

  it('a credential whose secret was deleted (the unsealed vault no longer lists the row) says so and does not run the check', async () => {
    const vault = vaultHarness({ state: 'unsealed' as const })
    // The endpoint references secrow:abc; the vault holds only secrow:xyz —
    // the referenced secret was deleted on the Secrets page. This is the
    // case the bead's criterion 9 exists for: a usable vault, a gone key.
    vault.inventory.mockResolvedValue({
      entries: [
        {
          id: 'secrow:xyz',
          name: 'other-key',
          kind: 'password',
          provider: 'system-keychain',
          ownerId: '',
          usedBy: 0,
          reachable: true,
        },
      ],
    })
    const { container, probeEndpoint, ctrl } = mountWithVault(
      [ep({ id: 'endpoint:custom:provider:1', name: 'provider', credential: 'secrow:abc' })],
      vault,
    )
    await ctrl.refresh()
    await waitForRows(container, 1)

    const row = rows(container)[0]
    await vi.waitFor(() => {
      expect(row.textContent).toContain("The endpoint's key was deleted — add it again")
    })
    const test = row.querySelector('[aria-label="Test provider"]') as HTMLButtonElement
    expect(test.disabled).toBe(true)
    fireEvent.click(test)
    expect(probeEndpoint).not.toHaveBeenCalled()
  })

  it('an unsealed vault whose inventory read fails says unavailable and does not run the check', async () => {
    const vault = vaultHarness({ state: 'unsealed' as const })
    vault.inventory.mockRejectedValueOnce(new Error('store is full'))
    const { container, probeEndpoint, ctrl } = mountWithVault(
      [ep({ id: 'endpoint:custom:provider:1', name: 'provider', credential: 'secrow:abc' })],
      vault,
    )
    await ctrl.refresh()
    await waitForRows(container, 1)

    const row = rows(container)[0]
    await vi.waitFor(() => {
      expect(row.textContent).toContain('The credential is unavailable right now')
    })
    const test = row.querySelector('[aria-label="Test provider"]') as HTMLButtonElement
    expect(test.disabled).toBe(true)
    fireEvent.click(test)
    expect(probeEndpoint).not.toHaveBeenCalled()
  })

  it('a resolvable credential (the unsealed vault lists the row) keeps the Test enabled', async () => {
    const vault = vaultHarness({ state: 'unsealed' as const })
    vault.inventory.mockResolvedValue({
      entries: [
        {
          id: 'secrow:abc',
          name: 'provider-key',
          kind: 'password',
          provider: 'system-keychain',
          ownerId: '',
          usedBy: 0,
          reachable: true,
        },
      ],
    })
    const { container, probeEndpoint, ctrl } = mountWithVault(
      [ep({ id: 'endpoint:custom:provider:1', name: 'provider', credential: 'secrow:abc' })],
      vault,
    )
    await ctrl.refresh()
    await waitForRows(container, 1)

    const row = rows(container)[0]
    // The unsealed vault lists the row, so the credential resolves — and the
    // row therefore says nothing about it. The live Test is the assertion.
    await vi.waitFor(() => {
      expect(row.querySelector('.ui-record-row__status')).toBeNull()
    })
    const test = row.querySelector('[aria-label="Test provider"]') as HTMLButtonElement
    expect(test.disabled).toBe(false)

    clickRowTest(container, 'provider')
    await vi.waitFor(() => {
      expect(probeEndpoint).toHaveBeenCalledTimes(1)
    })
  })
})

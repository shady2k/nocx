// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest'
import { cleanup, fireEvent, render } from '@solidjs/testing-library'
import { SecretsSection } from './secrets'
import { createVaultState } from './vault'
import type { VaultClient, VaultInventory, InventoryEntry } from './vault-client'
import type { ProfileClient } from './profiles'

// ── jsdom patch: native <dialog> showModal/close are unsupported ──────
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

const SEALED_STATUS = {
  state: 'sealed' as const,
  osKeyAvailable: false,
  osKeyCapable: false,
  hasPassphrase: false,
  autoSealMinutes: 0,
  providers: [],
  defaultProvider: null,
}

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
  const inventory = vi.fn<() => Promise<VaultInventory>>()
  const createSecret = vi.fn()
  const renameSecret = vi.fn()
  const replaceSecret = vi.fn()
  const deleteSecret = vi.fn()
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
    inventory,
    createSecret,
    renameSecret,
    replaceSecret,
    deleteSecret,
  } as unknown as VaultClient
  return { client, inventory, createSecret, renameSecret, replaceSecret, deleteSecret, status }
}

const UNSEALED_STATUS = {
  state: 'unsealed' as const,
  osKeyAvailable: true,
  osKeyCapable: true,
  hasPassphrase: true,
  autoSealMinutes: 0,
  providers: [],
  defaultProvider: null,
}

const MOCK_ENTRY_1: InventoryEntry = {
  id: 'secrow:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
  kind: 'password',
  name: 'SSH password for deploy',
  provider: 'system',
  ownerId: 'cred:prod-deploy',
  usedBy: 12,
  reachable: true,
}

const MOCK_ENTRY_2: InventoryEntry = {
  id: 'secrow:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
  kind: 'password',
  name: 'SSH password for shady@vm-dsm01:22',
  provider: 'system',
  ownerId: 'cred:shady',
  usedBy: 0,
  reachable: true,
}

/** Mount the SecretsSection with mocked client and controller. */
async function mount(client: VaultClient, profileClient?: ProfileClient) {
  const ctrl = createVaultState(client)
  // Wait for the initial status fetch
  await ctrl.refresh()
  const container = document.body.appendChild(document.createElement('div'))
  render(
    () => (
      <SecretsSection vaultClient={client} vaultController={ctrl} profileClient={profileClient} />
    ),
    { container },
  )
  return { container, ctrl }
}

// ── Tests ──────────────────────────────────────────────────────────────

describe('SecretsSection', () => {
  it('shows locked state when vault is sealed — no rows, no counts', async () => {
    const { client } = mockClient()
    client.status = vi.fn().mockResolvedValue(SEALED_STATUS)

    const { container } = await mount(client)

    // The locked state
    expect(container.querySelector('.ui-empty-state')).toBeTruthy()
    expect(container.querySelector('.ui-empty-state__title')?.textContent).toBe('Vault is locked')
    expect(container.querySelector('.ui-empty-state__action')).toBeTruthy()
    expect(container.querySelector('.ui-empty-state__action')?.textContent).toBe('Unlock vault')

    // No rows
    expect(container.querySelector('.sr-row-label')).toBeNull()
    // No usage count anywhere
    expect(container.textContent).not.toMatch(/\d+\s*connections?/)
    // "Sealed: a locked state and one action. Nothing else." — the surface spec
    // (.internal/specs/2026-07-30). A standing line of body copy above the
    // plate broke that rule and looked like it: a paragraph with no heading
    // over it and a centred plate floating underneath.
    expect(container.textContent).not.toMatch(/passwords and key passphrases/)
  })

  it('shows empty state when unsealed with no entries — different from sealed', async () => {
    const { client, inventory } = mockClient()
    client.status = vi.fn().mockResolvedValue(UNSEALED_STATUS)
    inventory.mockResolvedValue({ entries: [] })

    const { container } = await mount(client)

    await vi.waitFor(() => {
      expect(container.querySelector('.ui-empty-state')).toBeTruthy()
    })

    // Empty state with vault-empty message (different from sealed)
    const titles = container.querySelectorAll('.ui-empty-state__title')
    const emptyTitle = Array.from(titles).find((t) => t.textContent === 'Vault is empty')
    expect(emptyTitle).toBeTruthy()

    // No rows
    expect(container.querySelector('.sr-row-label')).toBeNull()
  })

  it('shows rows for each entry with label, store, and usage count', async () => {
    const { client, inventory } = mockClient()
    client.status = vi.fn().mockResolvedValue(UNSEALED_STATUS)
    inventory.mockResolvedValue({ entries: [MOCK_ENTRY_1, MOCK_ENTRY_2] })

    const { container } = await mount(client)

    await vi.waitFor(() => {
      expect(container.querySelector('.sr-row-label')).toBeTruthy()
    })

    // A row is the kit's CollectionRow — no longer a ghost Button, and no
    // longer a hand-rolled div either.
    const rows = Array.from(container.querySelectorAll('.ui-collection-row'))
    expect(rows.length).toBe(2)

    const row1Kind = rows[0].querySelector('.ui-badge')
    expect(row1Kind?.textContent).toBe('Password')

    const row1Store = rows[0].querySelectorAll('.ui-badge')[1]
    expect(row1Store?.textContent).toBe('System keychain')

    const row1Usage = rows[0].querySelector('.sr-row-usage')
    expect(row1Usage?.textContent).toBe('12 connections')

    // Row 2: usage 0
    const row2Labels = rows[1].querySelectorAll('.sr-row-label')
    expect(row2Labels[0].textContent).toBe('SSH password for shady@vm-dsm01:22')

    const row2Usage = rows[1].querySelector('.sr-row-usage')
    expect(row2Usage?.textContent).toBe('0 connections')
  })

  it('shows "store unreachable" when entry is not reachable', async () => {
    const { client, inventory } = mockClient()
    client.status = vi.fn().mockResolvedValue(UNSEALED_STATUS)
    const unreachable: InventoryEntry = {
      ...MOCK_ENTRY_1,
      reachable: false,
    }
    inventory.mockResolvedValue({ entries: [unreachable] })

    const { container } = await mount(client)

    // A status is a Badge, not a bespoke red span the surface drew itself.
    await vi.waitFor(() => {
      expect(container.querySelector('.ui-badge')).toBeTruthy()
    })

    const badge = Array.from(container.querySelectorAll('.ui-badge')).find(
      (b) => b.getAttribute('data-tone') === 'danger',
    )
    expect(badge?.textContent).toBe('Store unreachable')
  })

  it('no secret value appears in rendered output and no copy/reveal controls exist', async () => {
    const { client, inventory } = mockClient()
    client.status = vi.fn().mockResolvedValue(UNSEALED_STATUS)
    inventory.mockResolvedValue({ entries: [MOCK_ENTRY_1] })

    const { container } = await mount(client)

    await vi.waitFor(() => {
      expect(container.querySelector('.sr-row-label')).toBeTruthy()
    })

    // No copy or reveal buttons
    expect(container.querySelector('[aria-label*="copy" i]')).toBeNull()
    expect(container.querySelector('[aria-label*="reveal" i]')).toBeNull()
    expect(container.querySelector('[aria-label*="show" i]')).toBeNull()

    // No <input type="password"> or similar
    expect(container.querySelector('input[type="password"]')).toBeNull()

    // The label is NOT a secret value — it's metadata
    const rowLabels = container.querySelectorAll('.sr-row-label')
    expect(rowLabels.length).toBeGreaterThanOrEqual(1)
    // Label is the derived description, NOT the secret
    expect(rowLabels[0].textContent).not.toMatch(/^\*{6,}$/)
    expect(rowLabels[0].textContent).not.toMatch(/^sec:v1:/)
  })

  // The promise "never shown back to you" answers a question you only have
  // once you are looking at a list of secrets and wondering whether you can
  // read one — so it belongs to that section, not to the top of the page.
  // The page is a CollectionView now, like Connections: a pinned toolbar with
  // a filter and the add action, and the rows scrolling below. The promise
  // that a stored secret is never shown back lives in the empty state, where a
  // new user reads it — Connections carries no standing description line
  // either, and this page is meant to match it.
  it('gives the list a toolbar with a filter and the add action', async () => {
    const { client, inventory } = mockClient()
    client.status = vi.fn().mockResolvedValue(UNSEALED_STATUS)
    inventory.mockResolvedValue({ entries: [MOCK_ENTRY_1] })

    const { container } = await mount(client)
    await vi.waitFor(() => {
      expect(container.querySelector('.ui-collection-view')).toBeTruthy()
    })
    expect(container.querySelector('.ui-search-field')).toBeTruthy()
    const add = Array.from(container.querySelectorAll('.ui-toolbar button')).find((b) =>
      b.textContent?.includes('Add a secret'),
    )
    expect(add).toBeTruthy()
  })

  // Offered before there is anything to add to. A create action that appears
  // only once one item exists is unreachable for the first one.
  it('offers the add action on an empty vault too', async () => {
    const { client, inventory } = mockClient()
    client.status = vi.fn().mockResolvedValue(UNSEALED_STATUS)
    inventory.mockResolvedValue({ entries: [] })

    const { container } = await mount(client)
    await vi.waitFor(() => {
      expect(container.querySelector('.ui-collection-view')).toBeTruthy()
    })
    const add = Array.from(container.querySelectorAll('.ui-toolbar button')).find((b) =>
      b.textContent?.includes('Add a secret'),
    )
    expect(add).toBeTruthy()
  })

  it('filters the rows by name, and says so rather than claiming the vault is empty', async () => {
    const { client, inventory } = mockClient()
    client.status = vi.fn().mockResolvedValue(UNSEALED_STATUS)
    inventory.mockResolvedValue({ entries: [MOCK_ENTRY_1, MOCK_ENTRY_2] })

    const { container } = await mount(client)
    await vi.waitFor(() => {
      expect(container.querySelectorAll('.ui-collection-row').length).toBe(2)
    })

    const search = container.querySelector('.ui-search-field input') as HTMLInputElement
    fireEvent.input(search, { target: { value: 'nothing-matches-this' } })

    await vi.waitFor(() => {
      expect(container.querySelectorAll('.ui-collection-row').length).toBe(0)
    })
    // Not "Vault is empty" — the user can disprove that by clearing the box.
    expect(container.querySelector('.ui-empty-state__title')?.textContent).toBe(
      'No secret matches that',
    )
  })

  it('states which store holds a secret as a badge, not as bare text', async () => {
    const { client, inventory } = mockClient()
    client.status = vi.fn().mockResolvedValue(UNSEALED_STATUS)
    inventory.mockResolvedValue({ entries: [MOCK_ENTRY_1] })

    const { container } = await mount(client)
    await vi.waitFor(() => {
      expect(container.querySelector('.ui-collection-row')).toBeTruthy()
    })
    const badges = Array.from(container.querySelectorAll('.ui-badge')).map((b) => b.textContent)
    expect(badges).toContain('System keychain')
    expect(container.querySelector('.sr-row-store')).toBeNull()
  })

  it('keeps the never-shown-back promise where a new user reads it', async () => {
    const { client, inventory } = mockClient()
    client.status = vi.fn().mockResolvedValue(UNSEALED_STATUS)
    inventory.mockResolvedValue({ entries: [] })

    const { container } = await mount(client)

    await vi.waitFor(() => {
      expect(container.querySelector('.ui-empty-state__desc')).toBeTruthy()
    })

    const description = container.querySelector('.ui-empty-state__desc')
    expect(description?.textContent).toMatch(/never shown back to you/)
  })

  // The blank page. `uninitialized` was reachable only by resetting the vault,
  // which until now could not be done from the interface — so no test named
  // the state, the nested Shows let it fall through every branch, and the
  // whole panel rendered empty. Found by resetting and clicking Secrets.
  it('offers to set protection up when there is no vault yet', async () => {
    const { client } = mockClient()
    client.status = vi.fn().mockResolvedValue({
      ...UNSEALED_STATUS,
      state: 'uninitialized' as const,
    })

    const { container, ctrl } = await mount(client)

    await vi.waitFor(() => {
      expect(container.querySelector('.ui-empty-state__title')?.textContent).toBe(
        'Protection is not set up yet',
      )
    })
    // And the remedy is offered here, not merely named: a page that announces
    // a problem offers its way out.
    const action = container.querySelector('.ui-empty-state__action button') as HTMLButtonElement
    expect(action.textContent).toBe('Set up protection')
    const openSetup = vi.spyOn(ctrl, 'openSetup')
    action.click()
    expect(openSetup).toHaveBeenCalled()
  })

  it('says so when the inventory could not be loaded, instead of showing nothing', async () => {
    const { client, inventory } = mockClient()
    client.status = vi.fn().mockResolvedValue(UNSEALED_STATUS)
    inventory.mockRejectedValue(new Error('backend went away'))

    const { container } = await mount(client)

    await vi.waitFor(() => {
      expect(container.querySelector('.ui-empty-state__title')?.textContent).toBe(
        'Could not load secrets',
      )
    })
    expect(container.querySelector('.ui-empty-state__desc')?.textContent).toContain(
      'backend went away',
    )
  })

  // The guard against the whole class. Whatever the vault says, this page
  // shows the user something — a blank panel is never an answer, and it is the
  // failure that no per-state test can rule out on its own.
  it('never renders an empty panel, in any vault state', async () => {
    for (const state of ['uninitialized', 'sealed', 'unsealed'] as const) {
      cleanup()
      const { client, inventory } = mockClient()
      client.status = vi.fn().mockResolvedValue({ ...UNSEALED_STATUS, state })
      inventory.mockResolvedValue({ entries: [] })

      const { container } = await mount(client)

      await vi.waitFor(() => {
        expect(container.textContent?.trim(), `state ${state} rendered nothing`).not.toBe('')
      })
    }
  })

  // ADR-0016: the vault owns the name. A row renders the vault's name for the
  // secret — the backend resolves the fallbacks, so the page renders verbatim
  // and never a blank.
  it('renders the vault-owned name, not a derived label', async () => {
    const { client, inventory } = mockClient()
    client.status = vi.fn().mockResolvedValue(UNSEALED_STATUS)
    inventory.mockResolvedValue({
      entries: [{ ...MOCK_ENTRY_1, name: 'prod password', ownerId: '' }],
    })

    const { container } = await mount(client)

    await vi.waitFor(() => {
      expect(container.querySelector('.sr-row-label')?.textContent).toBe('prod password')
    })
  })

  // Add: the user was asked for the name because they set out to create a
  // secret. The control exists in the loaded state, opens the dialog, and the
  // create call carries name, kind and value to the wire.
  it('adds a secret by name and kind from the loaded state', async () => {
    const { client, inventory, createSecret } = mockClient()
    client.status = vi.fn().mockResolvedValue(UNSEALED_STATUS)
    inventory.mockResolvedValue({ entries: [MOCK_ENTRY_1] })
    createSecret.mockResolvedValue({})

    const { container } = await mount(client)

    await vi.waitFor(() => {
      expect(container.querySelector('.sr-row-label')).toBeTruthy()
    })

    const addButton = Array.from(container.querySelectorAll('button')).find((b) =>
      b.textContent?.includes('Add a secret'),
    )
    expect(addButton).toBeTruthy()
    addButton!.click()

    await vi.waitFor(() => {
      expect(container.querySelector('.nocx-dialog__title')?.textContent).toContain('Add secret')
    })

    // The dialog asks for the name — the user did set out to create a secret.
    const nameInput = container.querySelector('#sr-add-name') as HTMLInputElement
    expect(nameInput).toBeTruthy()
    nameInput.value = 'prod password'
    nameInput.dispatchEvent(new Event('input', { bubbles: true }))

    const valueInput = container.querySelector('#sr-add-value') as HTMLInputElement
    valueInput.value = 'hunter2'
    valueInput.dispatchEvent(new Event('input', { bubbles: true }))

    const submit = Array.from(container.querySelectorAll('.nocx-dialog button')).find(
      (b) => b.textContent === 'Add secret',
    ) as HTMLButtonElement | undefined
    submit!.click()

    await vi.waitFor(() => {
      expect(createSecret).toHaveBeenCalledWith({
        name: 'prod password',
        kind: 'password',
        value: 'hunter2',
      })
    })
  })

  // Rename: addressed by the row handle, never by a secret reference — the
  // renderer may not name one (nocx-jb20.1).
  it('renames a secret by its row handle', async () => {
    const { client, inventory, renameSecret } = mockClient()
    client.status = vi.fn().mockResolvedValue(UNSEALED_STATUS)
    inventory.mockResolvedValue({ entries: [MOCK_ENTRY_1] })
    renameSecret.mockResolvedValue({})

    const { container } = await mount(client)

    await vi.waitFor(() => {
      expect(container.querySelector('.sr-row-label')).toBeTruthy()
    })

    const renameButton = container.querySelector('[aria-label^="Rename"]') as HTMLButtonElement
    expect(renameButton).toBeTruthy()
    renameButton.click()

    await vi.waitFor(() => {
      expect(container.querySelector('.nocx-dialog__title')?.textContent).toContain('Rename')
    })

    const nameInput = container.querySelector('#sr-rename-name') as HTMLInputElement
    nameInput.value = 'the prod password'
    nameInput.dispatchEvent(new Event('input', { bubbles: true }))

    const submit = Array.from(container.querySelectorAll('.nocx-dialog button')).find(
      (b) => b.textContent === 'Rename',
    ) as HTMLButtonElement | undefined
    submit!.click()

    await vi.waitFor(() => {
      expect(renameSecret).toHaveBeenCalledWith({
        id: MOCK_ENTRY_1.id,
        name: 'the prod password',
      })
    })
  })

  // An unowned secret — no connection uses it — is a row like any other: that
  // is the point of ADR-0016, not a side effect.
  it('renders a secret with no connection using it', async () => {
    const { client, inventory } = mockClient()
    client.status = vi.fn().mockResolvedValue(UNSEALED_STATUS)
    inventory.mockResolvedValue({
      entries: [{ ...MOCK_ENTRY_1, ownerId: '', usedBy: 0, name: 'prod password' }],
    })

    const { container } = await mount(client)

    await vi.waitFor(() => {
      expect(container.querySelector('.sr-row-label')?.textContent).toBe('prod password')
    })
    const usage = container.querySelector('.sr-row-usage')
    expect(usage?.textContent).toBe('0 connections')
  })
})

// A private key on the Secrets page is supplied the same three ways the
// connection editor offers — path, file, paste. Pasting is material: the
// stored value IS the key.
it('adds a private key from pasted material', async () => {
  const { client, inventory, createSecret } = mockClient()
  client.status = vi.fn().mockResolvedValue(UNSEALED_STATUS)
  inventory.mockResolvedValue({ entries: [MOCK_ENTRY_1] })
  createSecret.mockResolvedValue({})

  // The mint path verifies the key and detects encryption; the add routes
  // material through it instead of vault.createSecret.
  const saveKeyMaterial = vi.fn().mockResolvedValue({
    row: 'secrow:added-key',
    fingerprint: 'SHA256:abc',
    passphraseWanted: false,
  })
  const pc = { saveKeyMaterial } as unknown as ProfileClient

  const { container } = await mount(client, pc)

  await vi.waitFor(() => {
    expect(container.querySelector('.sr-row-label')).toBeTruthy()
  })

  const addButton = Array.from(container.querySelectorAll('button')).find((b) =>
    b.textContent?.includes('Add a secret'),
  )
  addButton!.click()
  await vi.waitFor(() => {
    expect(container.querySelector('.nocx-dialog__title')?.textContent).toContain('Add secret')
  })

  const nameInput = container.querySelector('#sr-add-name') as HTMLInputElement
  nameInput.value = 'deploy key'
  nameInput.dispatchEvent(new Event('input', { bubbles: true }))

  // Switch the kind to Private key — the dialog then offers the three-way
  // input instead of the single value field.
  const kindOptions = Array.from(container.querySelectorAll('[role="radio"]'))
  const privateKeyKind = kindOptions.find(
    (o) => o.textContent?.trim() === 'Private key',
  ) as HTMLElement
  expect(privateKeyKind).toBeTruthy()
  privateKeyKind.click()
  await vi.waitFor(() => {
    // The three-way input replaced the single value field.
    expect(container.querySelector('[aria-label="Key input mode"]')).toBeTruthy()
  })
  expect(container.querySelector('#sr-add-value')).toBeNull()
  // …and it opens on the mode that asks the user for nothing (nocx-uvb3).
  const firstSegment = container.querySelector('[aria-label="Key input mode"] [role="radio"]')!
  expect(firstSegment.textContent?.trim()).toBe('Choose file')
  expect(firstSegment.getAttribute('aria-checked')).toBe('true')

  const modeOptions = Array.from(container.querySelectorAll('[role="radio"]'))
  const paste = modeOptions.find((o) => o.textContent?.trim() === 'Paste key') as HTMLElement
  paste.click()

  await vi.waitFor(() => {
    expect(container.querySelector('#sr-add-key-text')).toBeTruthy()
  })
  const keyInput = container.querySelector('#sr-add-key-text') as HTMLTextAreaElement
  keyInput.value = '-----BEGIN PRIVATE KEY-----\nMIIE\n-----END PRIVATE KEY-----'
  keyInput.dispatchEvent(new Event('input', { bubbles: true }))

  const submit = Array.from(container.querySelectorAll('.nocx-dialog button')).find(
    (b) => b.textContent === 'Add secret',
  ) as HTMLButtonElement | undefined
  submit!.click()

  await vi.waitFor(() => {
    expect(saveKeyMaterial).toHaveBeenCalledWith(
      '-----BEGIN PRIVATE KEY-----\nMIIE\n-----END PRIVATE KEY-----',
      'deploy key',
    )
  })
  // The material path never reaches vault.createSecret.
  expect(createSecret).not.toHaveBeenCalled()
})

/**
 * The three-way key input opens on Choose file (nocx-uvb3). A test that is
 * about the PATH field presses the segment, the way a user does, instead of
 * assuming the mode the input used to open on.
 */
function selectKeyPathMode(container: HTMLElement) {
  const option = Array.from(container.querySelectorAll('[role="radio"]')).find(
    (o) => o.textContent?.trim() === 'Path',
  ) as HTMLElement | undefined
  expect(option, 'Path segment not found').toBeTruthy()
  option!.click()
}

// Path mode sends the path for the BACKEND to dereference — the renderer
// cannot read arbitrary paths, and the stored material is the key, never a
// filename (dcf566b).
it('adds a private key from a path the backend dereferences', async () => {
  const { client, inventory, createSecret } = mockClient()
  client.status = vi.fn().mockResolvedValue(UNSEALED_STATUS)
  inventory.mockResolvedValue({ entries: [MOCK_ENTRY_1] })
  createSecret.mockResolvedValue({})

  const { container } = await mount(client)
  await vi.waitFor(() => {
    expect(container.querySelector('.sr-row-label')).toBeTruthy()
  })

  const addButton = Array.from(container.querySelectorAll('button')).find((b) =>
    b.textContent?.includes('Add a secret'),
  )
  addButton!.click()
  await vi.waitFor(() => {
    expect(container.querySelector('.nocx-dialog__title')?.textContent).toContain('Add secret')
  })

  const nameInput = container.querySelector('#sr-add-name') as HTMLInputElement
  nameInput.value = 'deploy key'
  nameInput.dispatchEvent(new Event('input', { bubbles: true }))

  const kindOptions = Array.from(container.querySelectorAll('[role="radio"]'))
  const privateKeyKind = kindOptions.find(
    (o) => o.textContent?.trim() === 'Private key',
  ) as HTMLElement
  privateKeyKind.click()

  selectKeyPathMode(container)
  await vi.waitFor(() => {
    expect(container.querySelector('#sr-add-key-path')).toBeTruthy()
  })

  const pathInput = container.querySelector('#sr-add-key-path') as HTMLInputElement
  pathInput.value = '/home/dev/.ssh/id_ed25519'
  pathInput.dispatchEvent(new Event('input', { bubbles: true }))

  const submit = Array.from(container.querySelectorAll('.nocx-dialog button')).find(
    (b) => b.textContent === 'Add secret',
  ) as HTMLButtonElement | undefined
  submit!.click()

  await vi.waitFor(() => {
    expect(createSecret).toHaveBeenCalledWith({
      name: 'deploy key',
      kind: 'private-key',
      path: '/home/dev/.ssh/id_ed25519',
    })
  })
})

// Replacing the value: the field is EMPTY, never prefilled, and labelled as
// a replacement — the vault never hands the old value back (ADR-0011 §2).
it('replaces a secret value by its row handle, never showing the old value', async () => {
  const { client, inventory, replaceSecret } = mockClient()
  client.status = vi.fn().mockResolvedValue(UNSEALED_STATUS)
  inventory.mockResolvedValue({ entries: [MOCK_ENTRY_1] })
  replaceSecret.mockResolvedValue({})

  const { container } = await mount(client)
  await vi.waitFor(() => {
    expect(container.querySelector('.sr-row-label')).toBeTruthy()
  })

  const replaceButton = container.querySelector(
    '[aria-label^="Replace value of"]',
  ) as HTMLButtonElement
  expect(replaceButton).toBeTruthy()
  replaceButton.click()

  await vi.waitFor(() => {
    expect(container.querySelector('.nocx-dialog__title')?.textContent).toContain(
      'Replace value of',
    )
  })

  // The field is labelled a replacement and is EMPTY — the old value is
  // never shown back, so it cannot be prefilled.
  const valueInput = container.querySelector('#sr-replace-value') as HTMLInputElement
  expect(valueInput).toBeTruthy()
  expect(valueInput.value).toBe('')
  const fieldLabel = container.querySelector('label[for="sr-replace-value"]')
  expect(fieldLabel?.textContent).toContain('New value')
  expect(container.textContent).not.toContain('hunter2')

  valueInput.value = 'new secret value'
  valueInput.dispatchEvent(new Event('input', { bubbles: true }))

  const submit = Array.from(container.querySelectorAll('.nocx-dialog button')).find(
    (b) => b.textContent === 'Replace value',
  ) as HTMLButtonElement | undefined
  submit!.click()

  await vi.waitFor(() => {
    expect(replaceSecret).toHaveBeenCalledWith({
      id: MOCK_ENTRY_1.id,
      value: 'new secret value',
    })
  })
})

// Renaming and replacing are independent: both controls exist on a row and
// one never requires the other.
it('offers rename and replace independently on every row', async () => {
  const { client, inventory } = mockClient()
  client.status = vi.fn().mockResolvedValue(UNSEALED_STATUS)
  inventory.mockResolvedValue({ entries: [MOCK_ENTRY_1] })

  const { container } = await mount(client)
  await vi.waitFor(() => {
    expect(container.querySelector('.sr-row-label')).toBeTruthy()
  })

  expect(container.querySelector('[aria-label^="Rename"]')).toBeTruthy()
  expect(container.querySelector('[aria-label^="Replace value of"]')).toBeTruthy()
})

// Replacing a private-key secret uses the same three-way input the add
// dialog does, including the backend-dereferenced path.
it('replaces a private key secret via the three-way input', async () => {
  const { client, inventory, replaceSecret } = mockClient()
  client.status = vi.fn().mockResolvedValue(UNSEALED_STATUS)
  inventory.mockResolvedValue({
    entries: [{ ...MOCK_ENTRY_1, kind: 'private-key', name: 'deploy key' }],
  })
  replaceSecret.mockResolvedValue({})

  const { container } = await mount(client)
  await vi.waitFor(() => {
    expect(container.querySelector('.sr-row-label')).toBeTruthy()
  })

  const replaceButton = container.querySelector(
    '[aria-label^="Replace value of"]',
  ) as HTMLButtonElement
  replaceButton.click()

  selectKeyPathMode(container)
  await vi.waitFor(() => {
    expect(container.querySelector('#sr-replace-key-path')).toBeTruthy()
  })

  const pathInput = container.querySelector('#sr-replace-key-path') as HTMLInputElement
  pathInput.value = '/home/dev/.ssh/id_ed25519'
  pathInput.dispatchEvent(new Event('input', { bubbles: true }))

  const submit = Array.from(container.querySelectorAll('.nocx-dialog button')).find(
    (b) => b.textContent === 'Replace value',
  ) as HTMLButtonElement | undefined
  submit!.click()

  await vi.waitFor(() => {
    expect(replaceSecret).toHaveBeenCalledWith({
      id: MOCK_ENTRY_1.id,
      path: '/home/dev/.ssh/id_ed25519',
    })
  })
})

// The rows the bug report showed were indistinguishable: a private key and
// its passphrase share the generated name, the key icon and the store badge
// (nocx-mg9r). The kind is on the wire already — this asserts the SURFACE
// renders it, in text a user can read, next to the storage badge.
it('states each row kind in readable text beside the storage badge', async () => {
  const { client, inventory } = mockClient()
  client.status = vi.fn().mockResolvedValue(UNSEALED_STATUS)
  inventory.mockResolvedValue({
    entries: [
      { ...MOCK_ENTRY_1, name: 'root@192.168.0.57', kind: 'private-key' },
      { ...MOCK_ENTRY_2, name: 'root@192.168.0.57', kind: 'key-passphrase' },
    ],
  })

  const { container } = await mount(client)

  await vi.waitFor(() => {
    expect(container.querySelector('.sr-row-label')).toBeTruthy()
  })

  const rows = Array.from(container.querySelectorAll('.ui-collection-row'))
  expect(rows.length).toBe(2)

  // Same name, same store — the kind badge is what tells them apart.
  const badges = rows.map((r) =>
    Array.from(r.querySelectorAll('.ui-badge')).map((b) => b.textContent),
  )
  expect(badges[0][0]).toBe('Private key')
  expect(badges[1][0]).toBe('Key passphrase')
  expect(badges[0][1]).toBe('System keychain')
  expect(badges[1][1]).toBe('System keychain')
  expect(badges[0][0]).not.toBe(badges[1][0])
})

// A secret nothing uses may go with a plain confirmation: no usage fetch, one
// dialog, delete reached with the row handle, row gone after.
it('deletes a secret nothing uses with a plain confirmation', async () => {
  const { client, inventory, deleteSecret } = mockClient()
  client.status = vi.fn().mockResolvedValue(UNSEALED_STATUS)
  // First load shows the row; after the delete the component reloads and the
  // row is gone — the empty inventory is what the reload sees.
  inventory.mockResolvedValueOnce({ entries: [MOCK_ENTRY_2] }).mockResolvedValue({ entries: [] })
  deleteSecret.mockResolvedValue({})

  const { container } = await mount(client)

  await vi.waitFor(() => {
    expect(container.querySelector('.sr-row-label')).toBeTruthy()
  })

  const del = container.querySelector('button[aria-label^="Delete "]') as HTMLButtonElement
  expect(del).toBeTruthy()
  del.click()

  await vi.waitFor(() => {
    expect(container.querySelector('.nocx-dialog')).toBeTruthy()
  })
  expect(container.textContent).toContain('No connection uses this secret')

  // The destructive action is not the default: Cancel carries autofocus.
  const cancel = Array.from(container.querySelectorAll('.nocx-dialog button')).find(
    (b) => b.textContent === 'Cancel',
  ) as HTMLButtonElement
  expect(cancel.hasAttribute('autofocus')).toBe(true)

  const confirm = Array.from(container.querySelectorAll('.nocx-dialog button')).find(
    (b) => b.textContent === 'Delete secret',
  ) as HTMLButtonElement
  confirm.click()

  await vi.waitFor(() => {
    expect(deleteSecret).toHaveBeenCalledWith({ id: MOCK_ENTRY_2.id })
  })

  // The row is gone from the list after the delete, without a manual refresh:
  // the component reloads the inventory itself.
  await vi.waitFor(() => {
    expect(container.querySelector('.sr-row-label')).toBeNull()
  })
})

// A secret connections still use NAMES them before it goes. The confirmation
// says which connections break, and the delete button stays disabled until
// the names are actually known.
it('names the connections a delete would break before offering the danger button', async () => {
  const { client, inventory, deleteSecret } = mockClient()
  client.status = vi.fn().mockResolvedValue(UNSEALED_STATUS)
  inventory.mockResolvedValueOnce({ entries: [MOCK_ENTRY_1] }).mockResolvedValue({ entries: [] })
  deleteSecret.mockResolvedValue({})

  const secretUsage = vi.fn()
  const pc = { secretUsage } as unknown as ProfileClient
  secretUsage.mockResolvedValue({
    profiles: [
      { profileId: 'ssh:p1:1', profileName: 'web-prod', source: 'profile' },
      { profileId: 'ssh:p2:1', profileName: 'db-prod', source: 'profile' },
    ],
  })

  const { container } = await mount(client, pc)

  await vi.waitFor(() => {
    expect(container.querySelector('.sr-row-label')).toBeTruthy()
  })

  const del = container.querySelector('button[aria-label^="Delete "]') as HTMLButtonElement
  del.click()

  // The names arrive from credentials.usage, and the dialog says who breaks.
  await vi.waitFor(() => {
    expect(container.textContent).toContain('web-prod')
  })
  expect(container.textContent).toContain('db-prod')
  expect(container.textContent).toContain('will stop using it')

  const confirm = Array.from(container.querySelectorAll('.nocx-dialog button')).find(
    (b) => b.textContent === 'Delete secret',
  ) as HTMLButtonElement
  expect(confirm.disabled).toBe(false)
  confirm.click()

  await vi.waitFor(() => {
    expect(deleteSecret).toHaveBeenCalledWith({ id: MOCK_ENTRY_1.id })
  })
  await vi.waitFor(() => {
    expect(container.querySelector('.sr-row-label')).toBeNull()
  })
})

// The danger button is never the dialog's default: while the connection names
// are still loading it is disabled, so Enter or a premature click cannot
// destroy a secret whose blast radius nobody has named yet.
it('keeps the delete button disabled until the breaking connections are named', async () => {
  const { client, inventory } = mockClient()
  client.status = vi.fn().mockResolvedValue(UNSEALED_STATUS)
  inventory.mockResolvedValue({ entries: [MOCK_ENTRY_1] })

  let resolveUsage: (v: { profiles: [] }) => void
  const pc = {
    secretUsage: vi.fn(
      () =>
        new Promise((resolve) => {
          resolveUsage = resolve
        }),
    ),
  } as unknown as ProfileClient

  const { container } = await mount(client, pc)

  await vi.waitFor(() => {
    expect(container.querySelector('.sr-row-label')).toBeTruthy()
  })

  const del = container.querySelector('button[aria-label^="Delete "]') as HTMLButtonElement
  del.click()

  await vi.waitFor(() => {
    expect(container.querySelector('.nocx-dialog')).toBeTruthy()
  })
  const confirm = Array.from(container.querySelectorAll('.nocx-dialog button')).find(
    (b) => b.textContent === 'Delete secret',
  ) as HTMLButtonElement
  expect(confirm.disabled).toBe(true)
  expect(container.textContent).toContain('Checking which connections use this secret')

  resolveUsage!({ profiles: [] })
  await vi.waitFor(() => {
    expect(confirm.disabled).toBe(false)
  })
})

// When the usage fetch fails the dialog says so and offers a retry — a
// count-only confirmation would break the "names them before it goes"
// contract silently.
it('refuses to delete when the breaking connections cannot be named', async () => {
  const { client, inventory, deleteSecret } = mockClient()
  client.status = vi.fn().mockResolvedValue(UNSEALED_STATUS)
  inventory.mockResolvedValue({ entries: [MOCK_ENTRY_1] })

  const pc = {
    secretUsage: vi.fn().mockRejectedValue(new Error('backend unreachable')),
  } as unknown as ProfileClient

  const { container } = await mount(client, pc)

  await vi.waitFor(() => {
    expect(container.querySelector('.sr-row-label')).toBeTruthy()
  })

  const del = container.querySelector('button[aria-label^="Delete "]') as HTMLButtonElement
  del.click()

  await vi.waitFor(() => {
    expect(container.textContent).toContain('Could not check which connections use this secret')
  })

  const confirm = Array.from(container.querySelectorAll('.nocx-dialog button')).find(
    (b) => b.textContent === 'Delete secret',
  ) as HTMLButtonElement
  expect(confirm.disabled).toBe(true)
  confirm.click()
  expect(deleteSecret).not.toHaveBeenCalled()

  // Retry is offered, and a recovered fetch enables the danger button.
  const retry = Array.from(container.querySelectorAll('.nocx-dialog button')).find(
    (b) => b.textContent === 'Try again',
  ) as HTMLButtonElement
  expect(retry).toBeTruthy()
})

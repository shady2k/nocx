// @vitest-environment jsdom
/**
 * Behavior tests for ConnectionsView — the user-facing list of SSH profiles.
 *
 * These tests cover what a user would notice breaking: filtering, live state,
 * probe (Test) outcome display, bound-secret display, the dialog editor, and
 * group-tree rendering. Internal refactors (save route, field revert, form
 * validation) are tested in connections.test.tsx.
 */
import { describe, it, expect, vi, afterEach } from 'vitest'
import { Show } from 'solid-js'
import { cleanup, render, fireEvent } from '@solidjs/testing-library'
import { ConnectionsView } from './connections'
import { ProfileClient } from './profiles'
import { createVaultState, UnlockDialog, type VaultController } from './vault'
import type { VaultClient, InventoryEntry } from './vault-client'
import type { DialogClient } from './dialog-client'
import { Dispatcher, RpcError } from './dispatcher'
import { clearToasts, toasts } from './ui'
import type {
  SSHProfile,
  ProfileGroup,
  EffectiveProfileDTO,
  SessionStatus,
  ConnectionTestResult,
  GroupImpactResponse,
} from './profiles'

const MOCK_PROFILES: SSHProfile[] = [
  {
    id: 'ssh:p1',
    type: 'ssh',
    name: 'prod-web',
    options: {
      host: 'web.example.com',
      port: 22,
      user: 'deploy',
      keepaliveInterval: 0,
      keepaliveCountMax: 0,
      readyTimeout: 0,
      agentForward: false,
      canBeJumpServer: false,
    },
  },
  {
    id: 'ssh:p2',
    type: 'ssh',
    name: 'prod-db',
    group: 'group:prod',
    options: {
      host: 'db.example.com',
      port: 5432,
      user: 'admin',
      keepaliveInterval: 0,
      keepaliveCountMax: 0,
      readyTimeout: 0,
      agentForward: false,
      canBeJumpServer: false,
    },
  },
  {
    id: 'ssh:p3',
    type: 'ssh',
    name: 'staging-web',
    options: {
      host: 'staging.example.com',
      port: 22,
      user: 'dev',
      keepaliveInterval: 0,
      keepaliveCountMax: 0,
      readyTimeout: 0,
      agentForward: false,
      canBeJumpServer: false,
    },
  },
]

const MOCK_GROUPS: ProfileGroup[] = [{ id: 'group:prod', name: 'Production' }]

const MOCK_SECRET_ROWS: InventoryEntry[] = [
  {
    id: 'secrow:prod-key',
    name: 'Key for prod-web',
    kind: 'private-key',
    provider: 'test',
    ownerId: '',
    usedBy: 0,
    reachable: true,
  },
  {
    id: 'secrow:prod-pass',
    name: 'Password for prod-web',
    kind: 'password',
    provider: 'test',
    ownerId: '',
    usedBy: 0,
    reachable: true,
  },
]

const MOCK_EFFECTIVE_CRED: EffectiveProfileDTO = {
  id: 'ssh:p1',
  fields: {
    passwordSecret: {
      value: 'secrow:prod-pass',
      source: { kind: 'profile', id: 'ssh:p1', label: 'prod-web' },
    },
  },
}

const MOCK_SESSION_STATUSES: Record<string, SessionStatus> = {
  'ssh:p1': { live: true, lastUsed: '2026-07-28T12:00:00Z' },
  'ssh:p2': { live: false },
  'ssh:p3': { live: false },
}

// ── Mock helpers ────────────────────────────────────────────────────────

function createMockClient(overrides?: {
  profiles?: SSHProfile[]
  groups?: ProfileGroup[]
  secretRows?: InventoryEntry[]
  sessionStatuses?: Record<string, SessionStatus>
  effectiveProfiles?: EffectiveProfileDTO[]
  connectionTestResult?: ConnectionTestResult
}) {
  const pc = new ProfileClient(new Dispatcher())

  vi.spyOn(pc, 'listProfiles').mockResolvedValue(overrides?.profiles ?? [])
  vi.spyOn(pc, 'listGroups').mockResolvedValue(overrides?.groups ?? [])
  vi.spyOn(pc, 'sessionStatus').mockResolvedValue({ statuses: overrides?.sessionStatuses ?? {} })
  vi.spyOn(pc, 'loadEffective').mockResolvedValue({ profiles: overrides?.effectiveProfiles ?? [] })
  const connectionTest = vi
    .spyOn(pc, 'connectionTest')
    .mockResolvedValue(overrides?.connectionTestResult ?? { outcome: 'accepted' })
  const trustHostKey = vi.spyOn(pc, 'trustHostKey').mockResolvedValue({ fingerprint: 'SHA256:abc' })
  return { client: pc, connectionTest, trustHostKey }
}

function mount(
  overrides?: Parameters<typeof createMockClient>[0] & {
    onConnect?: () => void
    onNavigateToSecrets?: () => void
    openFileDialog?: () => Promise<{ path: string }>
    /** Vault controller wired like main.tsx wires it, with the dialog shown
     *  beside the surface. Absent → ConnectionsView runs without a vault. */
    vaultController?: VaultController
    vaultClient?: VaultClient
  },
) {
  const { client, connectionTest, trustHostKey } = createMockClient(overrides)
  const vaultController = overrides?.vaultController
  const vaultClient =
    overrides?.vaultClient ??
    ({
      inventory: () => ({ entries: overrides?.secretRows ?? [] }),
    } as unknown as VaultClient)
  const dialogClient = overrides?.openFileDialog
    ? ({ openFileDialog: overrides.openFileDialog } as unknown as DialogClient)
    : undefined
  const container = document.body.appendChild(document.createElement('div'))
  render(
    () => (
      <>
        <ConnectionsView
          client={client}
          dialogClient={dialogClient}
          vaultController={vaultController}
          vaultClient={vaultClient}
          onConnect={overrides?.onConnect}
          onNavigateToSecrets={overrides?.onNavigateToSecrets}
        />
        <Show when={vaultController && vaultClient}>
          <UnlockDialog
            open={vaultController!.showUnlock()}
            onClose={() => vaultController!.closeUnlock()}
            onUnsealed={() => vaultController!.onUnsealDone()}
            vaultClient={vaultClient}
            vaultStatus={vaultController!.status()}
            reason={vaultController!.unlockReason()}
          />
        </Show>
      </>
    ),
    { container },
  )
  return { container, client, connectionTest, trustHostKey }
}

afterEach(() => {
  clearToasts()
  vi.clearAllMocks()
  cleanup()
})

// ── Helper: wait for profiles to render ──────────────────────────────

async function waitForProfiles(container: HTMLElement, count: number) {
  await vi.waitFor(() => {
    expect(container.querySelectorAll('.cm-item-name').length).toBe(count)
  })
}

// ── Filter narrows the list ───────────────────────────────────────────

describe('filter', () => {
  it('shows all profiles when search is empty', async () => {
    const { container } = mount({ profiles: MOCK_PROFILES })
    await waitForProfiles(container, 3)
  })

  it('narrows the list when search query matches a subset', async () => {
    const { container } = mount({ profiles: MOCK_PROFILES })
    await waitForProfiles(container, 3)

    const input = container.querySelector<HTMLInputElement>(
      'input[aria-label="Filter connections"]',
    )
    expect(input).toBeTruthy()
    input!.value = 'staging'
    input!.dispatchEvent(new Event('input', { bubbles: true }))

    await vi.waitFor(() => {
      const names = container.querySelectorAll('.cm-item-name')
      expect(names.length).toBe(1)
      expect(names[0].textContent).toBe('staging-web')
    })
  })

  it('matches against host and user in addition to name', async () => {
    const { container } = mount({ profiles: MOCK_PROFILES })
    await waitForProfiles(container, 3)

    const input = container.querySelector<HTMLInputElement>(
      'input[aria-label="Filter connections"]',
    )
    expect(input).toBeTruthy()
    input!.value = 'db.example'
    input!.dispatchEvent(new Event('input', { bubbles: true }))

    await vi.waitFor(() => {
      const names = container.querySelectorAll('.cm-item-name')
      expect(names.length).toBe(1)
      expect(names[0].textContent).toBe('prod-db')
    })
  })

  it('filtering by partial name shows multiple matching profiles', async () => {
    const { container } = mount({ profiles: MOCK_PROFILES })
    await waitForProfiles(container, 3)

    const input = container.querySelector<HTMLInputElement>(
      'input[aria-label="Filter connections"]',
    )
    expect(input).toBeTruthy()
    input!.value = 'prod'
    input!.dispatchEvent(new Event('input', { bubbles: true }))

    await vi.waitFor(() => {
      const names = container.querySelectorAll('.cm-item-name')
      expect(names.length).toBe(2)
      expect(names[0].textContent).toBe('prod-web')
      expect(names[1].textContent).toBe('prod-db')
    })
  })
})

// ── Live session state ───────────────────────────────────────────────

describe('session state', () => {
  it('marks one profile live and others disconnected from sessionStatus', async () => {
    const { container } = mount({
      profiles: MOCK_PROFILES.slice(0, 2),
      sessionStatuses: MOCK_SESSION_STATUSES,
    })

    await waitForProfiles(container, 2)

    const items = container.querySelectorAll('.ui-collection-row')
    const liveState = items[0].querySelector('.cm-session-state')
    expect(liveState).toBeTruthy()
    expect(liveState!.textContent).toContain('Connected')
    expect(liveState!.classList.contains('cm-session-live')).toBe(true)

    const offlineState = items[1].querySelector('.cm-session-state')
    expect(offlineState).toBeTruthy()
    expect(offlineState!.textContent).toContain('Disconnected')
    expect(offlineState!.classList.contains('cm-session-live')).toBe(false)
  })

  it('shows last-used date when present', async () => {
    const { container } = mount({
      profiles: MOCK_PROFILES.slice(0, 1),
      sessionStatuses: MOCK_SESSION_STATUSES,
    })

    await vi.waitFor(() => {
      expect(container.querySelector('.cm-session-last-used')).toBeTruthy()
    })

    const lastUsed = container.querySelector('.cm-session-last-used')
    expect(lastUsed!.textContent).toContain('last used')
  })
})

// ── Test action (distinct from Connect) ─────────────────────────────

describe('Test action', () => {
  it('calls connectionTest and reports the typed outcome in a toast', async () => {
    const onConnect = vi.fn()
    const { container, connectionTest } = mount({
      profiles: MOCK_PROFILES.slice(0, 1),
      onConnect,
      connectionTestResult: { outcome: 'rejected', detail: 'Password authentication failed' },
    })

    await waitForProfiles(container, 1)

    const testBtn = container.querySelector('[aria-label^="Test connection"]')
    expect(testBtn, 'Test button not found').toBeTruthy()
    ;(testBtn! as HTMLElement).click()

    await vi.waitFor(() => {
      expect(toasts()).toHaveLength(1)
      expect(toasts()[0].message).toContain('Password authentication failed')
      expect(toasts()[0].level).toBe('warning')
    })

    expect(onConnect).not.toHaveBeenCalled()

    expect(connectionTest).toHaveBeenCalledWith('ssh:p1')
  })

  it('displays accepted outcome as success', async () => {
    const { container } = mount({
      profiles: MOCK_PROFILES.slice(0, 1),
      connectionTestResult: { outcome: 'accepted', detail: 'Connection successful' },
    })

    await waitForProfiles(container, 1)

    const testBtn = container.querySelector('[aria-label^="Test connection"]')
    expect(testBtn, 'Test button not found').toBeTruthy()
    ;(testBtn! as HTMLElement).click()

    await vi.waitFor(() => {
      expect(toasts()).toHaveLength(1)
      expect(toasts()[0].message).toContain('Connection successful')
      expect(toasts()[0].level).toBe('success')
    })
  })
})

// ── Host key accept (nocx-ved0) ────────────────────────────────────────
// From the state a user is actually in — a host that is not in known_hosts —
// the accept control exists, activating it reaches the client method, and the
// next probe succeeds. The changed-key case is a different question: it must
// name both fingerprints, never be the default action, and declining writes
// nothing at all.

const UNKNOWN_KEY_RESULT: ConnectionTestResult = {
  outcome: 'host-key-unknown',
  detail: 'unknown host key for host.example.com:22: ssh-ed25519 SHA256:abc',
  hostKey: {
    host: 'host.example.com:22',
    algorithm: 'ssh-ed25519',
    fingerprint: 'SHA256:abc',
    key: 'a2V5',
  },
}

const CHANGED_KEY_RESULT: ConnectionTestResult = {
  outcome: 'host-key-changed',
  detail: 'host key mismatch for host.example.com:22: got SHA256:new, expected SHA256:stored',
  hostKey: {
    host: 'host.example.com:22',
    algorithm: 'ssh-ed25519',
    fingerprint: 'SHA256:new',
    storedFingerprint: 'SHA256:stored',
    key: 'bmV3',
  },
}

function clickTest(container: HTMLElement) {
  const testBtn = container.querySelector('[aria-label^="Test connection"]')
  expect(testBtn, 'Test button not found').toBeTruthy()
  ;(testBtn! as HTMLElement).click()
}

describe('host key accept', () => {
  it('unknown host key: accept control reaches trustHostKey and the next probe succeeds', async () => {
    const { container, connectionTest, trustHostKey } = mount({
      profiles: MOCK_PROFILES.slice(0, 1),
      connectionTestResult: UNKNOWN_KEY_RESULT,
    })
    connectionTest
      .mockResolvedValueOnce(UNKNOWN_KEY_RESULT)
      .mockResolvedValueOnce({ outcome: 'accepted', detail: 'Connection successful' })

    await waitForProfiles(container, 1)
    clickTest(container)

    // The accept dialog shows the offered fingerprint — the user must be able
    // to read it before deciding.
    await vi.waitFor(() => {
      expect(container.textContent).toContain('SHA256:abc')
    })
    // The routine accept is the primary action, and its words never say
    // "changed" — this is first contact, not a warning.
    expect(container.textContent).toContain('Unknown host key')
    expect(container.textContent).not.toContain('changed')

    clickButtonByText(container, 'Trust host key')

    await vi.waitFor(() => {
      expect(trustHostKey).toHaveBeenCalledWith('host.example.com:22', 'a2V5')
    })
    // The next probe runs and succeeds.
    await vi.waitFor(() => {
      expect(connectionTest).toHaveBeenCalledTimes(2)
    })
    await vi.waitFor(() => {
      expect(
        toasts().some((t) => t.level === 'success' && t.message.includes('Connection successful')),
      ).toBe(true)
    })
  })

  it('unknown host key: declining writes nothing at all', async () => {
    const { container, connectionTest, trustHostKey } = mount({
      profiles: MOCK_PROFILES.slice(0, 1),
      connectionTestResult: UNKNOWN_KEY_RESULT,
    })
    await waitForProfiles(container, 1)
    clickTest(container)

    await vi.waitFor(() => {
      expect(container.textContent).toContain('SHA256:abc')
    })
    clickButtonByText(container, 'Cancel')

    expect(trustHostKey).not.toHaveBeenCalled()
    expect(connectionTest).toHaveBeenCalledTimes(1)
  })
  it('changed key: names both fingerprints, is never the default action, and declining writes nothing', async () => {
    const { container, connectionTest, trustHostKey } = mount({
      profiles: MOCK_PROFILES.slice(0, 1),
      connectionTestResult: CHANGED_KEY_RESULT,
    })
    await waitForProfiles(container, 1)
    clickTest(container)

    // Both fingerprints are on screen — the user cannot judge a changed key
    // without the stored one to compare it against.
    await vi.waitFor(() => {
      expect(container.textContent).toContain('SHA256:new')
      expect(container.textContent).toContain('SHA256:stored')
    })
    expect(container.textContent).toContain('Host key changed')

    // The trust action is danger, not primary — it has to be aimed at.
    const buttons = Array.from(container.querySelectorAll<HTMLElement>('.ui-button'))
    const dangerAction = buttons.find((b) => b.textContent?.trim() === 'Trust the new key')
    const cancelButton = buttons.find((b) => b.textContent?.trim() === 'Cancel')
    expect(dangerAction, 'danger action not found').toBeTruthy()
    expect(cancelButton, 'cancel not found').toBeTruthy()
    expect(dangerAction!.dataset.variant).toBe('danger')
    expect(cancelButton!.dataset.variant).toBe('default')

    // Decline first: nothing is written, no second probe.
    cancelButton!.click()
    expect(trustHostKey).not.toHaveBeenCalled()
    expect(connectionTest).toHaveBeenCalledTimes(1)

    // Run the test again and take the deliberate danger action.
    clickTest(container)
    await vi.waitFor(() => {
      expect(container.textContent).toContain('SHA256:stored')
    })
    clickButtonByText(container, 'Trust the new key')
    await vi.waitFor(() => {
      expect(trustHostKey).toHaveBeenCalledWith('host.example.com:22', 'bmV3')
    })
  })
})

// ── The bound secret is visible before Connect is pressed (b5bu) ────────

describe('bound password secret', () => {
  it('shows the bound secret under the Password method in the editor', async () => {
    const PROFILE_WITH_PASSWORD_SECRET: SSHProfile = {
      ...MOCK_PROFILES[0],
      options: {
        ...MOCK_PROFILES[0].options,
        auth: 'password',
        passwordSecret: 'secrow:prod-pass',
      },
    }
    const { container } = mount({
      profiles: [PROFILE_WITH_PASSWORD_SECRET],
      secretRows: MOCK_SECRET_ROWS,
    })

    await waitForProfiles(container, 1)
    await openProfileEditor(container, 'prod-web')
    selectProfileSection(container, 'Authentication')

    // The Password action names the bound row, and the picker holds it as
    // the current value — an empty credential is visible before Connect is
    // pressed, not remembered by the user.
    await vi.waitFor(() => {
      expect(container.textContent).toContain('Password: Password for prod-web')
    })
    const changeBtn = Array.from(container.querySelectorAll('.ui-button')).find(
      (b) => b.textContent?.trim() === 'Change Password',
    )
    expect(changeBtn, 'Change Password not found').toBeTruthy()
    const picker = container
      .querySelector('label[for="profile-auth-secret"]')!
      .closest('.ui-field')!
      .querySelector('.ui-select') as HTMLSelectElement
    expect(picker.value).toBe('secrow:prod-pass')
  })
})

// ── Quick connect ────────────────────────────────────────────────────

describe('quick connect', () => {
  it('opens a quick-connect dialog before the full form', async () => {
    const { container } = mount({ profiles: [] })
    await waitForProfiles(container, 0)

    const newBtn = container.querySelector('.ui-button')
    expect(newBtn).toBeTruthy()
    ;(newBtn! as HTMLElement).click()

    await vi.waitFor(() => {
      expect(container.querySelector('#quick-connect-input')).toBeTruthy()
    })

    // The full form should not open until Next is clicked
    expect(container.querySelector('#profile-host')).toBeFalsy()
  })

  it('typing a connection string and clicking Next opens the form with parsed values', async () => {
    const { container } = mount({ profiles: [] })

    await waitForProfiles(container, 0)

    const newBtn = container.querySelector('.ui-button')
    expect(newBtn).toBeTruthy()
    ;(newBtn! as HTMLElement).click()

    await vi.waitFor(() => {
      expect(container.querySelector('#quick-connect-input')).toBeTruthy()
    })

    const input = container.querySelector('#quick-connect-input') as HTMLInputElement
    expect(input, 'Quick connect input not found').toBeTruthy()
    fireEvent.input(input, { target: { value: 'deploy@web.example.com:2222' } })

    const nextBtn = Array.from(container.querySelectorAll('.ui-button')).find(
      (b) => b.textContent?.trim() === 'Next',
    )
    expect(nextBtn, 'Next button not found').toBeTruthy()
    ;(nextBtn! as HTMLElement).click()

    // Form dialog should open with parsed values
    await vi.waitFor(() => {
      const hostInput = container.querySelector('#profile-host') as HTMLInputElement
      expect(hostInput, 'Form dialog did not open').toBeTruthy()
      expect(hostInput.value).toBe('web.example.com')
    })

    const portInput = container.querySelector('#profile-port') as HTMLInputElement
    expect(portInput.value).toBe('2222')

    const userInput = container.querySelector('#profile-auth-user') as HTMLInputElement
    expect(userInput.value).toBe('deploy')
  })

  it('empty input and Next shows a warning but does not close the dialog', async () => {
    const { container } = mount({ profiles: [] })

    await waitForProfiles(container, 0)

    const newBtn = container.querySelector('.ui-button')
    expect(newBtn).toBeTruthy()
    ;(newBtn! as HTMLElement).click()

    // Quick-connect dialog opens
    await vi.waitFor(() => {
      expect(container.querySelector('#quick-connect-input')).toBeTruthy()
    })

    // Click Next without typing anything
    const nextBtn = Array.from(container.querySelectorAll('.ui-button')).find(
      (b) => b.textContent?.trim() === 'Next',
    )
    expect(nextBtn).toBeTruthy()
    ;(nextBtn! as HTMLElement).click()

    // Dialog should still be open (no profile-name in DOM)
    expect(container.querySelector('#profile-name')).toBeFalsy()
    expect(container.querySelector('#quick-connect-input')).toBeTruthy()
  })
})

// ── Inline secret minting from the connection form (ADR-0017) ─────────────

describe('inline password minting', () => {
  // Setting a password mints the secret at the action moment and binds the
  // returned row to the profile. There is no credential object to create or
  // name: the secret owns its name, and the profile saves with
  // options.passwordSecret holding the minted row (ADR-0017 §1).
  it('Set Password mints the secret and binds its row to the profile', async () => {
    const { container, client } = mount({
      profiles: MOCK_PROFILES.slice(0, 1),
      secretRows: [{ ...MOCK_SECRET_ROWS[1], id: 'secrow:pass-1' }],
    })
    const savePasswordSpy = vi.spyOn(client, 'savePassword').mockResolvedValue({
      row: 'secrow:pass-1',
    })
    const patchSpy = vi.spyOn(client, 'patchProfile').mockResolvedValue(MOCK_EFFECTIVE_CRED)

    await waitForProfiles(container, 1)
    await openProfileEditor(container, 'prod-web')
    selectProfileSection(container, 'Authentication')
    clickSegmentedOption(container, 'Password')

    clickButtonByText(container, 'Set Password')
    await vi.waitFor(() => expect(container.querySelector('#password-value')).toBeTruthy())
    fireEvent.input(container.querySelector('#password-value')!, {
      target: { value: 'hunter2' },
    })
    clickButtonByText(container, 'OK')

    // The mint carries the generated name — what the secret is, plus whose
    // login it is (ADR-0016).
    await vi.waitFor(() => {
      expect(savePasswordSpy).toHaveBeenCalledWith('hunter2', 'Password for deploy@web.example.com')
    })

    // The minted row is bound on the draft before the profile save is
    // pressed: the Password action now names the bound secret.
    await vi.waitFor(() => {
      expect(container.textContent).toContain('Password: Password for prod-web')
    })

    // Saving the profile now persists the binding.
    const dialog = findDialogByTitleContaining(container, 'prod-web')!
    const saveBtn = Array.from(dialog.querySelectorAll('.ui-button')).find(
      (b) => b.textContent?.trim() === 'Save Connection',
    )
    expect(saveBtn, 'Save Connection button not found').toBeTruthy()
    fireEvent.click(saveBtn!)

    await vi.waitFor(() => {
      expect(patchSpy).toHaveBeenCalled()
    })
    expect(patchSpy.mock.calls[0][0].set).toMatchObject({
      'options.passwordSecret': 'secrow:pass-1',
    })
  })

  it('the connection editor no longer offers to create a credential by hand', async () => {
    const { container } = mount({ profiles: MOCK_PROFILES.slice(0, 1) })

    await waitForProfiles(container, 1)

    // Open edit dialog
    const editBtn = container.querySelector('.ui-collection-row__actions [aria-label^="Edit "]')
    expect(editBtn).toBeTruthy()
    ;(editBtn! as HTMLElement).click()

    await vi.waitFor(() => {
      expect(container.querySelector('.nocx-dialog__panel')).toBeTruthy()
    })

    // The "+" button beside the credential select is gone — the only way to
    // authenticate with an existing secret is to pick a vault row.
    const plusBtn = container.querySelector('[aria-label="New credential"]')
    expect(plusBtn).toBeNull()
    // And no credential creation form is reachable from the editor.
    expect(container.querySelector('#cred-name')).toBeNull()
  })
})

// ── Shared impact stubs ──────────────────────────────────────────────────

const IMPACT_DANGEROUS: GroupImpactResponse = {
  dangerous: true,
  affectedProfiles: [
    {
      profileId: 'ssh:p2',
      profileName: 'prod-db',
      diffs: [{ field: 'passwordSecret', oldValue: null, newValue: 'secrow:new', dangerous: true }],
    },
  ],
}

const IMPACT_COSMETIC: GroupImpactResponse = {
  dangerous: false,
  affectedProfiles: [
    {
      profileId: 'ssh:p2',
      profileName: 'prod-db',
      diffs: [{ field: 'port', oldValue: 5432, newValue: 22, dangerous: false }],
    },
  ],
}

const IMPACT_DELETE_PROMOTE: GroupImpactResponse = {
  dangerous: false,
  deleteImpact: {
    action: 'promote_to_root',
    reason: 'The group contains child profiles that will be reparented.',
    affectedGroupIds: [],
  },
}

// ── Helpers: find a dialog by title ────────────────────────────────────

function findDialogByTitle(container: HTMLElement, titleText: string): HTMLElement | null {
  const titles = container.querySelectorAll('.nocx-dialog__title')
  for (const t of titles) {
    if (t.textContent === titleText) return t.closest('.nocx-dialog')
  }
  return null
}

function findDialogByTitleContaining(container: HTMLElement, partial: string): HTMLElement | null {
  const titles = container.querySelectorAll('.nocx-dialog__title')
  for (const t of titles) {
    if (t.textContent && t.textContent.includes(partial)) return t.closest('.nocx-dialog')
  }
  return null
}

// ── Helper: open the group editor dialog ────────────────────────────────

async function openGroupEditorByName(container: HTMLElement, groupName: string) {
  const headers = container.querySelectorAll('.cm-group-header')
  const targetHeader = Array.from(headers).find(
    (h) => h.querySelector('.cm-group-name')?.textContent === groupName,
  )
  expect(targetHeader, `Group header "${groupName}" not found`).toBeTruthy()
  const editBtn = targetHeader!.querySelector(`[aria-label="Edit group ${groupName}"]`)
  expect(editBtn, `Edit button for "${groupName}" not found`).toBeTruthy()
  ;(editBtn! as HTMLElement).click()

  await vi.waitFor(() => {
    const dialog = findDialogByTitle(container, `Edit Group: ${groupName}`)
    expect(dialog, `Group edit dialog "${groupName}" not found`).toBeTruthy()
  })
}

/**
 * Click a section in the group editor's rail.
 */
function selectGroupSection(container: HTMLElement, label: string) {
  const btn = Array.from(container.querySelectorAll('.ui-tabs__list .ui-button')).find(
    (b) => b.textContent?.trim() === label,
  )
  expect(btn, `tab "${label}" not found`).toBeTruthy()
  ;(btn! as HTMLElement).click()
}

// ── Helper: open the profile edit dialog for a named profile ─────────────

async function openProfileEditor(container: HTMLElement, profileName: string) {
  const editBtn = container.querySelector('.ui-collection-row__actions [aria-label^="Edit "]')
  expect(editBtn, `Edit button for "${profileName}" not found`).toBeTruthy()
  ;(editBtn! as HTMLElement).click()

  await vi.waitFor(() => {
    const dialog = findDialogByTitleContaining(container, profileName)
    expect(dialog, `Profile edit dialog "${profileName}" not found`).toBeTruthy()
  })
}

function selectProfileSection(container: HTMLElement, label: string) {
  const btn = Array.from(container.querySelectorAll('.ui-tabs__list .ui-button')).find(
    (b) => b.textContent?.trim() === label,
  )
  expect(btn, `profile tab "${label}" not found`).toBeTruthy()
  ;(btn! as HTMLElement).click()
}

/**
 * The key input opens on Choose file (nocx-uvb3), which is what a user meets.
 * A test that is about the PATH field has to ask for it the way a user does —
 * by pressing the segment — rather than assuming the mode it used to open on.
 */
async function selectKeyPathMode(container: HTMLElement) {
  await vi.waitFor(() => {
    expect(container.querySelector('[aria-label="Key input mode"]')).toBeTruthy()
  })
  clickSegmentedOption(container, 'Path')
}

function clickSegmentedOption(container: HTMLElement, label: string) {
  const option = Array.from(container.querySelectorAll('[role="radio"]')).find(
    (r) => r.textContent?.trim() === label,
  )
  expect(option, `SegmentedControl option "${label}" not found`).toBeTruthy()
  ;(option! as HTMLElement).click()
}

// ── Four-way key input: connection editor ───────────────────────────────

describe('four-way key input — connection editor', () => {
  it('shows four modes (Path, Choose file, Paste key, Secret) for publicKey auth', async () => {
    const { container } = mount({ profiles: MOCK_PROFILES.slice(0, 1) })
    await waitForProfiles(container, 1)

    await openProfileEditor(container, 'prod-web')
    selectProfileSection(container, 'Authentication')

    // Set auth to Public Key
    clickSegmentedOption(container, 'Public Key')

    // Wait for the key input field
    await selectKeyPathMode(container)
    await vi.waitFor(() => {
      expect(container.querySelector('#profile-key-path')).toBeTruthy()
    })

    // The SegmentedControl should have all four options
    const segments = container.querySelectorAll('[role="radio"]')
    const keySegments = Array.from(segments).filter(
      (s) =>
        s.textContent?.trim() === 'Path' ||
        s.textContent?.trim() === 'Choose file' ||
        s.textContent?.trim() === 'Paste key' ||
        s.textContent?.trim() === 'Secret',
    )
    expect(keySegments.length).toBe(4)
  })

  // The editor used to open on Path, which is the one mode that asks the user
  // for something they have to know — an absolute path, typed from memory,
  // with the native picker behind Browse absent outside a packaged build.
  // Asserted from the state a user starts in: the segment is selected without
  // anybody clicking it, and the path field is not the one on screen.
  it('opens on Choose file, the mode that asks for nothing', async () => {
    const { container } = mount({ profiles: MOCK_PROFILES.slice(0, 1) })
    await waitForProfiles(container, 1)

    await openProfileEditor(container, 'prod-web')
    selectProfileSection(container, 'Authentication')
    clickSegmentedOption(container, 'Public Key')

    const group = await vi.waitFor(() => {
      const g = container.querySelector('[aria-label="Key input mode"]')
      expect(g).toBeTruthy()
      return g!
    })
    const segments = Array.from(group.querySelectorAll('[role="radio"]'))
    expect(segments[0].textContent?.trim()).toBe('Choose file')
    expect(segments[0].getAttribute('aria-checked')).toBe('true')
    expect(container.querySelector('#profile-key-path')).toBeNull()
  })

  it('path mode records a path and calls no vault method', async () => {
    const { container, client } = mount({ profiles: MOCK_PROFILES.slice(0, 1) })
    const saveKeyMatSpy = vi.spyOn(client, 'saveKeyMaterial')

    await waitForProfiles(container, 1)
    await openProfileEditor(container, 'prod-web')
    selectProfileSection(container, 'Authentication')
    clickSegmentedOption(container, 'Public Key')

    await selectKeyPathMode(container)
    await vi.waitFor(() => {
      expect(container.querySelector('#profile-key-path')).toBeTruthy()
    })

    // Type a path
    const pathInput = container.querySelector('#profile-key-path') as HTMLInputElement
    fireEvent.input(pathInput, { target: { value: '/home/user/.ssh/id_ed25519' } })

    // Save the profile
    const dialog = findDialogByTitleContaining(container, 'prod-web')!
    const saveBtn = Array.from(dialog.querySelectorAll('.ui-button')).find(
      (b) => b.textContent?.trim() === 'Save Connection',
    )
    expect(saveBtn, 'Save Connection button not found').toBeTruthy()
    fireEvent.click(saveBtn!)

    // saveKeyMaterial should NOT have been called (no vault interaction for path mode)
    expect(saveKeyMatSpy).not.toHaveBeenCalled()
  })

  // Choosing a file must STORE THE KEY, not a filename. It used to read
  // `File.path` — an Electron extension present in neither a browser nor a
  // Wails webview — so the fallback fired every time and `id_ed25519` was
  // saved as if it were a path to a key. Broken on every target, and no test
  // asked what the mode actually produced.
  it('choose-file mode stores the file contents as key material, not its name', async () => {
    const { container, client } = mount({ profiles: MOCK_PROFILES.slice(0, 1) })
    const saveKeyMatSpy = vi.spyOn(client, 'saveKeyMaterial').mockResolvedValue({
      row: 'secrow:key-file',
      fingerprint: 'SHA256:abc123',
      passphraseWanted: false,
    })
    const patchSpy = vi.spyOn(client, 'patchProfile').mockResolvedValue(MOCK_EFFECTIVE_CRED)

    await waitForProfiles(container, 1)
    await openProfileEditor(container, 'prod-web')
    selectProfileSection(container, 'Authentication')
    clickSegmentedOption(container, 'Public Key')
    clickSegmentedOption(container, 'Choose file')

    const KEY = '-----BEGIN PRIVATE KEY-----\nfrom-a-file\n-----END PRIVATE KEY-----'
    const native = container.querySelector('.ui-file-input__native') as HTMLInputElement
    expect(native, 'file input not found').toBeTruthy()
    const file = new File([KEY], 'id_ed25519', { type: 'text/plain' })
    Object.defineProperty(native, 'files', { value: [file], configurable: true })
    fireEvent.change(native)

    const dialog = findDialogByTitleContaining(container, 'prod-web')!
    await vi.waitFor(() => {
      const btn = Array.from(dialog.querySelectorAll('.ui-button')).find(
        (b) => b.textContent?.trim() === 'Save Connection',
      )
      expect(btn).toBeTruthy()
    })
    const saveBtn = Array.from(dialog.querySelectorAll('.ui-button')).find(
      (b) => b.textContent?.trim() === 'Save Connection',
    )!
    fireEvent.click(saveBtn)

    await vi.waitFor(() => {
      expect(saveKeyMatSpy).toHaveBeenCalled()
    })
    // The CONTENTS reached the vault.
    expect(saveKeyMatSpy.mock.calls[0][0]).toBe(KEY)
    // The minted row is bound on the profile — and no filename is anywhere
    // on the wire.
    await vi.waitFor(() => {
      expect(patchSpy).toHaveBeenCalled()
    })
    expect(patchSpy.mock.calls[0][0].set).toMatchObject({
      'options.keySecret': 'secrow:key-file',
    })
    for (const call of patchSpy.mock.calls) {
      expect(JSON.stringify(call)).not.toContain('id_ed25519')
    }
  })

  it('material mode calls saveKeyMaterial and records no path', async () => {
    const { container, client } = mount({ profiles: MOCK_PROFILES.slice(0, 1) })
    const saveKeyMatSpy = vi.spyOn(client, 'saveKeyMaterial').mockResolvedValue({
      row: 'secrow:key-mat',
      fingerprint: 'SHA256:abc123',
      passphraseWanted: false,
    })
    const patchSpy = vi.spyOn(client, 'patchProfile').mockResolvedValue(MOCK_EFFECTIVE_CRED)

    await waitForProfiles(container, 1)
    await openProfileEditor(container, 'prod-web')
    selectProfileSection(container, 'Authentication')
    clickSegmentedOption(container, 'Public Key')

    // Switch to Paste key mode
    clickSegmentedOption(container, 'Paste key')

    await vi.waitFor(() => {
      expect(container.querySelector('#profile-key-text')).toBeTruthy()
    })

    // Paste key text
    const keyInput = container.querySelector('#profile-key-text') as HTMLInputElement
    fireEvent.input(keyInput, {
      target: { value: '-----BEGIN PRIVATE KEY-----\nMIIEvQIB...\n-----END PRIVATE KEY-----' },
    })

    // Save
    const dialog = findDialogByTitleContaining(container, 'prod-web')!
    const saveBtn = Array.from(dialog.querySelectorAll('.ui-button')).find(
      (b) => b.textContent?.trim() === 'Save Connection',
    )
    expect(saveBtn, 'Save button not found').toBeTruthy()
    fireEvent.click(saveBtn!)

    await vi.waitFor(() => {
      expect(saveKeyMatSpy).toHaveBeenCalled()
    })

    // And no path is recorded: the profile binds the minted row instead —
    // the half of the criterion that used to be unassertable with a
    // credential in the way.
    await vi.waitFor(() => {
      expect(patchSpy).toHaveBeenCalled()
    })
    expect(patchSpy.mock.calls[0][0].set).toMatchObject({
      'options.keySecret': 'secrow:key-mat',
    })
    for (const call of patchSpy.mock.calls) {
      expect(JSON.stringify(call)).not.toContain('keyPath')
    }
    expect(saveKeyMatSpy).toHaveBeenCalledWith(
      expect.stringContaining('BEGIN PRIVATE KEY'),
      // ADR-0016: the secret owns its name — the save carries the generated
      // name, which says WHAT the secret is as well as whose login it is. A
      // connection that stores a key and its passphrase would otherwise
      // produce two rows called `deploy@web.example.com`.
      'Key for deploy@web.example.com',
    )
  })

  // ── Key-passphrase ask (nocx-dze3) ────────────────────────────────────
  // Saving a passphrase-protected key must ask for the key's passphrase on
  // the spot, verify it against the key, and store it — not defer the ask to
  // connect time, where a wrong passphrase is a dead end. The prompt names
  // the KEY, so the vault passphrase can never be typed into it by mistake.

  async function openKeyPassphrasePrompt(container: HTMLElement) {
    await waitForProfiles(container, 1)
    await openProfileEditor(container, 'prod-web')
    selectProfileSection(container, 'Authentication')
    clickSegmentedOption(container, 'Public Key')
    clickSegmentedOption(container, 'Paste key')
    await vi.waitFor(() => {
      expect(container.querySelector('#profile-key-text')).toBeTruthy()
    })
    fireEvent.input(container.querySelector('#profile-key-text')!, {
      target: {
        value:
          '-----BEGIN OPENSSH PRIVATE KEY-----\nencrypted-fixture\n-----END OPENSSH PRIVATE KEY-----',
      },
    })
    const dialog = findDialogByTitleContaining(container, 'prod-web')!
    const saveBtn = Array.from(dialog.querySelectorAll('.ui-button')).find(
      (b) => b.textContent?.trim() === 'Save Connection',
    )!
    fireEvent.click(saveBtn)
    await vi.waitFor(() => {
      expect(container.querySelector('.ui-prompt__title')?.textContent).toContain('Passphrase for')
    })
  }

  it('asks for the key passphrase when saveKeyMaterial reports passphraseWanted', async () => {
    const { container, client } = mount({ profiles: MOCK_PROFILES.slice(0, 1) })
    vi.spyOn(client, 'saveKeyMaterial').mockResolvedValue({
      row: 'secrow:key-enc',
      fingerprint: 'SHA256:enc123',
      passphraseWanted: true,
    })

    await openKeyPassphrasePrompt(container)

    // The prompt names the KEY it belongs to — not "the vault", not "a
    // passphrase". The generated secret name is user@host.
    expect(container.querySelector('.ui-prompt__title')?.textContent).toBe(
      'Passphrase for deploy@web.example.com',
    )

    expect(container.querySelector('.ui-prompt__body')?.textContent).toContain('Key passphrase')
  })

  it('stores a verified passphrase and continues the save', async () => {
    const { container, client } = mount({ profiles: MOCK_PROFILES.slice(0, 1) })
    const savePassphraseSpy = vi
      .spyOn(client, 'saveKeyPassphrase')
      .mockResolvedValue({ row: 'secrow:pass-enc' })
    vi.spyOn(client, 'saveKeyMaterial').mockResolvedValue({
      row: 'secrow:key-enc',
      fingerprint: 'SHA256:enc123',
      passphraseWanted: true,
    })
    const patchSpy = vi.spyOn(client, 'patchProfile').mockResolvedValue(MOCK_EFFECTIVE_CRED)

    await openKeyPassphrasePrompt(container)

    fireEvent.input(container.querySelector('#key-passphrase')!, {
      target: { value: 'correct-horse' },
    })
    const saveBtn = Array.from(container.querySelectorAll('.ui-button')).find(
      (b) => b.textContent?.trim() === 'Save passphrase',
    )!
    fireEvent.click(saveBtn)

    await vi.waitFor(() => {
      expect(savePassphraseSpy).toHaveBeenCalledWith(
        'secrow:key-enc',
        'correct-horse',
        'Passphrase for deploy@web.example.com',
      )
    })
    // The save the prompt interrupted continues — with both minted rows bound.
    await vi.waitFor(() => {
      expect(patchSpy).toHaveBeenCalled()
    })
    expect(patchSpy.mock.calls[0][0].set).toMatchObject({
      'options.keySecret': 'secrow:key-enc',
      'options.keyPassphraseSecret': 'secrow:pass-enc',
    })
  })

  it('refuses a wrong passphrase in the prompt, keeping the key saved', async () => {
    const { container, client } = mount({ profiles: MOCK_PROFILES.slice(0, 1) })
    const savePassphraseSpy = vi.spyOn(client, 'saveKeyPassphrase').mockRejectedValue(
      new RpcError('that passphrase does not open this key', -32603, {
        reason: 'invalid-key-passphrase',
      }),
    )
    vi.spyOn(client, 'saveKeyMaterial').mockResolvedValue({
      row: 'secrow:key-enc',
      fingerprint: 'SHA256:enc123',
      passphraseWanted: true,
    })
    const patchSpy = vi.spyOn(client, 'patchProfile').mockResolvedValue(MOCK_EFFECTIVE_CRED)

    await openKeyPassphrasePrompt(container)

    fireEvent.input(container.querySelector('#key-passphrase')!, {
      target: { value: 'wrong-passphrase' },
    })
    const saveBtn = Array.from(container.querySelectorAll('.ui-button')).find(
      (b) => b.textContent?.trim() === 'Save passphrase',
    )!
    fireEvent.click(saveBtn)

    // Refused there and then: the backend's sentence lands in the field, the
    // prompt stays open, and the deferred save has NOT run.
    await vi.waitFor(() => {
      expect(container.textContent).toContain('that passphrase does not open this key')
    })
    expect(container.querySelector('.ui-prompt')).toBeTruthy()
    expect(patchSpy).not.toHaveBeenCalled()
    // The key material itself was already stored and is not rolled back.
    expect(savePassphraseSpy).toHaveBeenCalledTimes(1)
  })

  it('declining keeps the key and continues the save', async () => {
    const { container, client } = mount({ profiles: MOCK_PROFILES.slice(0, 1) })
    const savePassphraseSpy = vi.spyOn(client, 'saveKeyPassphrase')
    vi.spyOn(client, 'saveKeyMaterial').mockResolvedValue({
      row: 'secrow:key-enc',
      fingerprint: 'SHA256:enc123',
      passphraseWanted: true,
    })
    const patchSpy = vi.spyOn(client, 'patchProfile').mockResolvedValue(MOCK_EFFECTIVE_CRED)

    await openKeyPassphrasePrompt(container)

    const skipBtn = Array.from(container.querySelectorAll('.ui-button')).find(
      (b) => b.textContent?.trim() === 'Not now',
    )!
    fireEvent.click(skipBtn)

    // Declining is allowed: nothing stored, nothing rolled back, the save the
    // prompt interrupted continues.
    expect(savePassphraseSpy).not.toHaveBeenCalled()
    await vi.waitFor(() => {
      expect(patchSpy).toHaveBeenCalled()
    })
    expect(container.querySelector('.ui-prompt')).toBeNull()
  })

  it('switching from material to path clears the key text', async () => {
    const { container } = mount({ profiles: MOCK_PROFILES.slice(0, 1) })
    await waitForProfiles(container, 1)
    await openProfileEditor(container, 'prod-web')
    selectProfileSection(container, 'Authentication')
    clickSegmentedOption(container, 'Public Key')

    clickSegmentedOption(container, 'Paste key')

    // Type key text
    const keyInput = container.querySelector('#profile-key-text') as HTMLInputElement
    expect(keyInput, 'Key text field should be visible').toBeTruthy()
    fireEvent.input(keyInput, { target: { value: 'some-private-key-text' } })

    // Switch to Path mode
    clickSegmentedOption(container, 'Path')

    // The key text field should no longer be visible
    await vi.waitFor(() => {
      expect(container.querySelector('#profile-key-text')).toBeFalsy()
    })

    // The path field should be visible now
    expect(container.querySelector('#profile-key-path')).toBeTruthy()
  })

  it('shows the bound key secret as the current value of the Secret picker', async () => {
    const PROFILE_WITH_KEY_SECRET: SSHProfile = {
      ...MOCK_PROFILES[0],
      options: {
        ...MOCK_PROFILES[0].options,
        auth: 'publicKey',
        keySecret: 'secrow:kmat',
      },
    }
    const { container } = mount({
      profiles: [PROFILE_WITH_KEY_SECRET],
      secretRows: [{ ...MOCK_SECRET_ROWS[0], id: 'secrow:kmat' }],
    })
    await waitForProfiles(container, 1)
    await openProfileEditor(container, 'prod-web')
    selectProfileSection(container, 'Authentication')

    // The bound row is the picker's current value — visible before Connect
    // is pressed, not an empty credential the user must remember (b5bu).
    clickSegmentedOption(container, 'Secret')
    await vi.waitFor(() => {
      const picker = container
        .querySelector('label[for="profile-key-secret"]')!
        .closest('.ui-field')!
        .querySelector('.ui-select') as HTMLSelectElement
      expect(picker, 'Secret picker not found').toBeTruthy()
      expect(picker.value).toBe('secrow:kmat')
    })
  })
  it('selecting a row in the secret picker binds it on save', async () => {
    const { container, client } = mount({
      profiles: MOCK_PROFILES.slice(0, 1),
      secretRows: MOCK_SECRET_ROWS,
    })
    const patchSpy = vi.spyOn(client, 'patchProfile').mockResolvedValue(MOCK_EFFECTIVE_CRED)

    await waitForProfiles(container, 1)
    await openProfileEditor(container, 'prod-web')
    selectProfileSection(container, 'Authentication')
    clickSegmentedOption(container, 'Password')
    // The password source is a two-way choice now (ADR-0017 §1): type a new
    // one, or pick an existing secret. Pick the existing-secret side.
    clickSegmentedOption(container, 'Use existing secret')

    // Pick an existing password row.
    const picker = await vi.waitFor(() => {
      const el = container
        .querySelector('label[for="profile-auth-secret"]')!
        .closest('.ui-field')!
        .querySelector('.ui-select') as HTMLSelectElement
      expect(el, 'password picker not found').toBeTruthy()
      return el
    })
    fireEvent.change(picker, { target: { value: 'secrow:prod-pass' } })

    const dialog = findDialogByTitleContaining(container, 'prod-web')!
    const saveBtn = Array.from(dialog.querySelectorAll('.ui-button')).find(
      (b) => b.textContent?.trim() === 'Save Connection',
    )
    expect(saveBtn, 'Save Connection button not found').toBeTruthy()
    fireEvent.click(saveBtn!)

    await vi.waitFor(() => {
      expect(patchSpy).toHaveBeenCalledTimes(1)
    })
    expect(patchSpy.mock.calls[0][0].set).toMatchObject({
      'options.passwordSecret': 'secrow:prod-pass',
    })
  })

  it('preserves newlines in pasted key text on save', async () => {
    const { container, client } = mount({ profiles: MOCK_PROFILES.slice(0, 1) })
    const saveKeyMatSpy = vi.spyOn(client, 'saveKeyMaterial').mockResolvedValue({
      row: 'secrow:key-nl',
      fingerprint: 'SHA256:newline-test',
      passphraseWanted: false,
    })

    await waitForProfiles(container, 1)
    await openProfileEditor(container, 'prod-web')
    selectProfileSection(container, 'Authentication')
    clickSegmentedOption(container, 'Public Key')

    // Switch to Paste key mode
    clickSegmentedOption(container, 'Paste key')

    await vi.waitFor(() => {
      expect(container.querySelector('#profile-key-text')).toBeTruthy()
    })

    // Set a multi-line key value directly on the textarea, then dispatch input
    const keyField = container.querySelector('#profile-key-text') as HTMLTextAreaElement
    const keyContent =
      '-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA\n-----END OPENSSH PRIVATE KEY-----\n'
    const originalNewlineCount = (keyContent.match(/\n/g) || []).length
    keyField.value = keyContent
    fireEvent.input(keyField)

    // Save
    const dialog = findDialogByTitleContaining(container, 'prod-web')!
    const saveBtn = Array.from(dialog.querySelectorAll('.ui-button')).find(
      (b) => b.textContent?.trim() === 'Save Connection',
    )
    expect(saveBtn, 'Save button not found').toBeTruthy()
    fireEvent.click(saveBtn!)

    await vi.waitFor(() => {
      expect(saveKeyMatSpy).toHaveBeenCalled()
    })

    const capturedArg = saveKeyMatSpy.mock.calls[0][0]
    const capturedNewlineCount = (capturedArg.match(/\n/g) || []).length
    expect(capturedNewlineCount).toBe(originalNewlineCount)
    expect(capturedNewlineCount).toBeGreaterThan(0)
    // Confirm it's the same content, not truncated
    expect(capturedArg).toBe(keyContent)
  })
})

// ── Three-way key input: group editor ──────────────────────────────────

describe('four-way key input — group editor', () => {
  it('shows four modes for publicKey in group defaults', async () => {
    const { container } = mount({ profiles: MOCK_PROFILES, groups: MOCK_GROUPS })
    await waitForProfiles(container, 3)

    await openGroupEditorByName(container, 'Production')
    selectGroupSection(container, 'Connection')

    // Set auth to Public Key (the group editor has an AuthMethodEditor inside)
    clickSegmentedOption(container, 'Public Key')

    const group = await vi.waitFor(() => {
      const g = container.querySelector('[aria-label="Key input mode"]')
      expect(g).toBeTruthy()
      return g!
    })
    // Group defaults open on the same mode the connection editor does, and it
    // is the leftmost segment (nocx-uvb3).
    const first = group.querySelector('[role="radio"]')!
    expect(first.textContent?.trim()).toBe('Choose file')
    expect(first.getAttribute('aria-checked')).toBe('true')

    await selectKeyPathMode(container)
    await vi.waitFor(() => {
      expect(container.querySelector('#group-default-key-path')).toBeTruthy()
    })

    // The SegmentedControl should have all four key input options
    const segments = container.querySelectorAll('[role="radio"]')
    const keySegments = Array.from(segments).filter(
      (s) =>
        s.textContent?.trim() === 'Path' ||
        s.textContent?.trim() === 'Choose file' ||
        s.textContent?.trim() === 'Paste key' ||
        s.textContent?.trim() === 'Secret',
    )
    expect(keySegments.length).toBe(4)
  })

  it('path mode records a path in group defaults', async () => {
    const { container } = mount({ profiles: MOCK_PROFILES, groups: MOCK_GROUPS })
    await waitForProfiles(container, 3)

    await openGroupEditorByName(container, 'Production')
    selectGroupSection(container, 'Connection')
    clickSegmentedOption(container, 'Public Key')

    await selectKeyPathMode(container)
    await vi.waitFor(() => {
      expect(container.querySelector('#group-default-key-path')).toBeTruthy()
    })

    const pathInput = container.querySelector('#group-default-key-path') as HTMLInputElement
    fireEvent.input(pathInput, { target: { value: '/home/user/.ssh/id_ed25519' } })

    // Verify the path was entered
    expect(pathInput.value).toBe('/home/user/.ssh/id_ed25519')
  })

  it('Paste key mode exists and can be selected', async () => {
    const { container } = mount({ profiles: MOCK_PROFILES, groups: MOCK_GROUPS })
    await waitForProfiles(container, 3)

    await openGroupEditorByName(container, 'Production')
    selectGroupSection(container, 'Connection')
    clickSegmentedOption(container, 'Public Key')

    // Switch to Paste key
    clickSegmentedOption(container, 'Paste key')

    await vi.waitFor(() => {
      expect(container.querySelector('#group-default-key-text')).toBeTruthy()
    })

    const keyInput = container.querySelector('#group-default-key-text') as HTMLInputElement
    fireEvent.input(keyInput, { target: { value: 'pasted-key-content' } })
    expect(keyInput.value).toBe('pasted-key-content')
  })

  it('switching modes clears the previous mode value', async () => {
    const { container } = mount({ profiles: MOCK_PROFILES, groups: MOCK_GROUPS })
    await waitForProfiles(container, 3)

    await openGroupEditorByName(container, 'Production')
    selectGroupSection(container, 'Connection')
    clickSegmentedOption(container, 'Public Key')

    // Enter path mode value
    await selectKeyPathMode(container)
    const pathInput = container.querySelector('#group-default-key-path') as HTMLInputElement
    fireEvent.input(pathInput, { target: { value: '/tmp/test-key' } })

    // Switch to Paste key — path should be cleared
    clickSegmentedOption(container, 'Paste key')

    await vi.waitFor(() => {
      expect(container.querySelector('#group-default-key-text')).toBeTruthy()
    })

    // Switch back to Path — the path should be cleared
    await selectKeyPathMode(container)
    await vi.waitFor(() => {
      expect(container.querySelector('#group-default-key-path')).toBeTruthy()
    })

    // The path input should be empty (cleared on mode switch)
    const pathInput2 = container.querySelector('#group-default-key-path') as HTMLInputElement
    expect(pathInput2.value).toBe('')
  })

  it('preserves newlines in pasted key text on group save', async () => {
    const { container, client } = mount({ profiles: MOCK_PROFILES, groups: MOCK_GROUPS })
    const saveKeyMatSpy = vi.spyOn(client, 'saveKeyMaterial').mockResolvedValue({
      row: 'secrow:grp-nl',
      fingerprint: 'SHA256:group-newline',
      passphraseWanted: false,
    })
    vi.spyOn(client, 'groupApply').mockResolvedValue([])
    vi.spyOn(client, 'groupImpact').mockResolvedValue(IMPACT_COSMETIC)

    await waitForProfiles(container, 3)
    await openGroupEditorByName(container, 'Production')
    selectGroupSection(container, 'Connection')
    clickSegmentedOption(container, 'Public Key')

    // Switch to Paste key mode
    clickSegmentedOption(container, 'Paste key')

    await vi.waitFor(() => {
      expect(container.querySelector('#group-default-key-text')).toBeTruthy()
    })

    // Set multi-line key value directly on the textarea, then dispatch input
    const keyField = container.querySelector('#group-default-key-text') as HTMLTextAreaElement
    const keyContent = '-----BEGIN EC PRIVATE KEY-----\nMHQCAQEEIIm\n-----END EC PRIVATE KEY-----\n'
    const originalNewlineCount = (keyContent.match(/\n/g) || []).length
    keyField.value = keyContent
    fireEvent.input(keyField)

    // Save the group
    const dialog = findDialogByTitle(container, 'Edit Group: Production')!
    const saveBtn = Array.from(dialog.querySelectorAll('.ui-button')).find(
      (b) => b.textContent?.trim() === 'Save Group',
    )
    expect(saveBtn, 'Save Group button not found').toBeTruthy()
    fireEvent.click(saveBtn!)

    await vi.waitFor(() => {
      expect(saveKeyMatSpy).toHaveBeenCalled()
    })

    const capturedArg = saveKeyMatSpy.mock.calls[0][0]
    const capturedNewlineCount = (capturedArg.match(/\n/g) || []).length
    expect(capturedNewlineCount).toBe(originalNewlineCount)
    expect(capturedNewlineCount).toBeGreaterThan(0)
    expect(capturedArg).toBe(keyContent)
  })
})

// ── Native file picker (dialog.openFile) ───────────────────────────────

describe('native file picker — path mode', () => {
  it('Browse fills the path field with the picked absolute path', async () => {
    const openFileDialog = vi.fn().mockResolvedValue({ path: '/home/dev/.ssh/id_ed25519' })
    const { container } = mount({
      profiles: MOCK_PROFILES.slice(0, 1),
      openFileDialog,
    })
    await waitForProfiles(container, 1)
    await openProfileEditor(container, 'prod-web')
    selectProfileSection(container, 'Authentication')
    clickSegmentedOption(container, 'Public Key')

    await selectKeyPathMode(container)
    await vi.waitFor(() => {
      expect(container.querySelector('#profile-key-path')).toBeTruthy()
    })

    const browse = container.querySelector(
      '[aria-label="Browse for a private key file"]',
    ) as HTMLButtonElement
    expect(browse, 'Browse button should be present when a dialog client is wired').toBeTruthy()
    browse.click()

    await vi.waitFor(() => {
      expect(openFileDialog).toHaveBeenCalled()
    })
    const pathInput = container.querySelector('#profile-key-path') as HTMLInputElement
    await vi.waitFor(() => {
      expect(pathInput.value).toBe('/home/dev/.ssh/id_ed25519')
    })
  })

  // The dev-web harness has no Wails runtime: the picker rejects, and the
  // surface must degrade — a hint beside the field, the field still
  // hand-typable — not fail.
  it('degrades when the native picker is unavailable', async () => {
    const openFileDialog = vi.fn().mockRejectedValue(new Error('dialog not available'))
    const { container } = mount({
      profiles: MOCK_PROFILES.slice(0, 1),
      openFileDialog,
    })
    await waitForProfiles(container, 1)
    await openProfileEditor(container, 'prod-web')
    selectProfileSection(container, 'Authentication')
    clickSegmentedOption(container, 'Public Key')

    await selectKeyPathMode(container)
    await vi.waitFor(() => {
      expect(container.querySelector('#profile-key-path')).toBeTruthy()
    })

    const browse = container.querySelector(
      '[aria-label="Browse for a private key file"]',
    ) as HTMLButtonElement
    browse.click()

    await vi.waitFor(() => {
      expect(container.textContent).toContain('The native file picker is not available here')
    })

    // The path field still works by hand.
    const pathInput = container.querySelector('#profile-key-path') as HTMLInputElement
    fireEvent.input(pathInput, { target: { value: '~/.ssh/id_ed25519' } })
    expect(pathInput.value).toBe('~/.ssh/id_ed25519')
  })

  it('no Browse action when no dialog capability is wired', async () => {
    const { container } = mount({ profiles: MOCK_PROFILES.slice(0, 1) })
    await waitForProfiles(container, 1)
    await openProfileEditor(container, 'prod-web')
    selectProfileSection(container, 'Authentication')
    clickSegmentedOption(container, 'Public Key')

    await selectKeyPathMode(container)
    await vi.waitFor(() => {
      expect(container.querySelector('#profile-key-path')).toBeTruthy()
    })
    expect(container.querySelector('[aria-label="Browse for a private key file"]')).toBeNull()
  })
})

// ── Group editor tests ──────────────────────────────────────────────────

describe('group editor', () => {
  it('blast radius appears before applying', async () => {
    const { container, client } = mount({
      profiles: MOCK_PROFILES,
      groups: MOCK_GROUPS,
    })
    await waitForProfiles(container, 3)

    await openGroupEditorByName(container, 'Production')

    // Impact section should not be visible before any change
    expect(container.querySelector('.cm-impact-count')).toBeFalsy()

    const impactSpy = vi.spyOn(client, 'groupImpact').mockResolvedValue(IMPACT_COSMETIC)

    selectGroupSection(container, 'Connection')

    // Change the port default to trigger impact computation
    const portInput = container.querySelector('#group-default-port') as HTMLInputElement
    expect(portInput).toBeTruthy()
    fireEvent.input(portInput, { target: { value: '22' } })

    // Wait for the impact summary to appear
    await vi.waitFor(() => {
      const count = container.querySelector('.cm-impact-count')
      expect(count).toBeTruthy()
      expect(count!.textContent).toContain('Affects')
    })

    expect(impactSpy).toHaveBeenCalled()
  })

  it('dangerous change gates the save button', async () => {
    const { container, client } = mount({
      profiles: MOCK_PROFILES,
      groups: MOCK_GROUPS,
    })
    await waitForProfiles(container, 3)

    await openGroupEditorByName(container, 'Production')

    const impactSpy = vi.spyOn(client, 'groupImpact').mockResolvedValue(IMPACT_DANGEROUS)

    selectGroupSection(container, 'Connection')

    // Change a default to trigger impact computation
    const portInput = container.querySelector('#group-default-port') as HTMLInputElement
    expect(portInput).toBeTruthy()
    fireEvent.input(portInput, { target: { value: '22' } })

    // Wait for dangerous badge to appear
    await vi.waitFor(() => {
      expect(container.querySelector('.cm-impact-danger-badge')).toBeTruthy()
    })

    // Scope to the group editor dialog
    const groupDialog = findDialogByTitle(container, 'Edit Group: Production')!

    // Save button should be disabled before confirmation
    const saveBtn = Array.from(groupDialog.querySelectorAll('.ui-button')).find(
      (b) => b.textContent?.trim() === 'Save Group',
    )
    expect(saveBtn).toBeTruthy()
    expect((saveBtn! as HTMLButtonElement).disabled).toBe(true)

    // Click the danger confirmation checkbox
    const confirmCheckbox = groupDialog.querySelector(
      '.cm-danger-confirm input[type="checkbox"]',
    ) as HTMLInputElement
    expect(confirmCheckbox).toBeTruthy()
    fireEvent.click(confirmCheckbox)

    // Save button should now be enabled
    await vi.waitFor(() => {
      expect((saveBtn! as HTMLButtonElement).disabled).toBe(false)
    })

    expect(impactSpy).toHaveBeenCalled()
  })

  it('cosmetic change does not gate the save', async () => {
    const { container, client } = mount({
      profiles: MOCK_PROFILES,
      groups: MOCK_GROUPS,
    })
    await waitForProfiles(container, 3)

    await openGroupEditorByName(container, 'Production')

    const impactSpy = vi.spyOn(client, 'groupImpact').mockResolvedValue(IMPACT_COSMETIC)

    selectGroupSection(container, 'Connection')

    // Change a non-dangerous default
    const portInput = container.querySelector('#group-default-port') as HTMLInputElement
    expect(portInput).toBeTruthy()
    fireEvent.input(portInput, { target: { value: '22' } })

    // Wait for impact to appear
    await vi.waitFor(() => {
      expect(container.querySelector('.cm-impact-count')).toBeTruthy()
    })

    const groupDialog = findDialogByTitle(container, 'Edit Group: Production')!

    // No danger confirmation checkbox should exist
    expect(groupDialog.querySelector('.cm-danger-confirm')).toBeFalsy()

    // Save button should be enabled
    const saveBtn = Array.from(groupDialog.querySelectorAll('.ui-button')).find(
      (b) => b.textContent?.trim() === 'Save Group',
    )
    expect(saveBtn).toBeTruthy()
    expect((saveBtn! as HTMLButtonElement).disabled).toBe(false)

    expect(impactSpy).toHaveBeenCalled()
  })

  it('cancelling the editor applies nothing', async () => {
    const { container, client } = mount({
      profiles: MOCK_PROFILES,
      groups: MOCK_GROUPS,
    })
    await waitForProfiles(container, 3)

    await openGroupEditorByName(container, 'Production')

    const applySpy = vi.spyOn(client, 'groupApply')

    // Find Cancel inside the group editor dialog
    const groupDialog = findDialogByTitle(container, 'Edit Group: Production')
    expect(groupDialog, 'Group dialog not found').toBeTruthy()
    const cancelBtn = Array.from(groupDialog!.querySelectorAll('.ui-button')).find(
      (b) => b.textContent?.trim() === 'Cancel',
    )
    expect(cancelBtn).toBeTruthy()
    ;(cancelBtn! as HTMLElement).click()

    // The group dialog should close
    await vi.waitFor(() => {
      expect(findDialogByTitle(container, 'Edit Group: Production')).toBeFalsy()
    })

    // groupApply should never be called
    expect(applySpy).not.toHaveBeenCalled()
  })

  it('delete states what happens to children before confirming', async () => {
    const { container, client } = mount({
      profiles: MOCK_PROFILES,
      groups: MOCK_GROUPS,
    })
    await waitForProfiles(container, 3)

    // Spy BEFORE clicking delete — computeDeleteImpact calls groupImpact immediately
    const impactSpy = vi.spyOn(client, 'groupImpact').mockResolvedValue(IMPACT_DELETE_PROMOTE)

    // Find and click the Delete button in the Production group header
    const headers = container.querySelectorAll('.cm-group-header')
    const prodHeader = Array.from(headers).find(
      (h) => h.querySelector('.cm-group-name')?.textContent === 'Production',
    )
    expect(prodHeader).toBeTruthy()
    const deleteBtn = prodHeader!.querySelector('[aria-label="Delete group Production"]')
    expect(deleteBtn).toBeTruthy()
    ;(deleteBtn! as HTMLElement).click()

    // Wait for delete dialog. Matched loosely: the title now names the group
    // it is about to destroy, which is the point of the confirmation.
    await vi.waitFor(() => {
      expect(findDialogByTitleContaining(container, 'Delete Group')).toBeTruthy()
    })

    const deleteDialog = findDialogByTitleContaining(container, 'Delete Group')!
    expect(deleteDialog.textContent).toContain('Production')

    // The impact should explain what happens to children
    await vi.waitFor(() => {
      const deleteText = deleteDialog.querySelector('.cm-delete-impact')
      expect(deleteText).toBeTruthy()
      expect(deleteText!.textContent).toContain('reparented')
      expect(deleteText!.textContent).toContain('child profiles')
    })

    expect(impactSpy).toHaveBeenCalled()
  })
})

// ── Move preview test ────────────────────────────────────────────────────

describe('profile move preview', () => {
  it('moving a profile into a group with different defaults previews the diff', async () => {
    const { container, client } = mount({
      profiles: MOCK_PROFILES,
      groups: MOCK_GROUPS,
    })
    await waitForProfiles(container, 3)

    // Open the profile editor for prod-db (which is in the Production group)
    await openProfileEditor(container, 'prod-db')

    // The profile form should be visible
    expect(container.querySelector('.cm-form')).toBeTruthy()

    const profileDialog = findDialogByTitleContaining(container, 'prod-db')
    expect(profileDialog, 'Profile dialog not found').toBeTruthy()

    // Find the Group select: label[for="profile-group"] inside the dialog
    const groupLabel = profileDialog!.querySelector('label[for="profile-group"]')
    expect(groupLabel, 'Group label not found').toBeTruthy()
    const groupSelect = groupLabel!
      .closest('.ui-field')
      ?.querySelector('.ui-select') as HTMLSelectElement
    expect(groupSelect, 'Group select not found in profile dialog').toBeTruthy()

    // Mock moveImpact to return a result
    const moveImpactSpy = vi.spyOn(client, 'moveImpact').mockResolvedValue(IMPACT_COSMETIC)

    // Change the group to empty (ungrouped) to trigger moveImpact
    fireEvent.change(groupSelect, { target: { value: '' } })

    // Wait for the move impact preview to appear (reuses renderImpactSummary)
    await vi.waitFor(() => {
      const count = container.querySelector('.cm-impact-count')
      expect(count).toBeTruthy()
      expect(count!.textContent).toContain('Affects')
    })

    expect(moveImpactSpy).toHaveBeenCalledWith({
      profileIds: ['ssh:p2'],
      targetGroupId: '',
    })
  })
})

// ── Vault prompt cancellation abandons the save ─────────────────────────
// Minting the secret and saving the profile are two steps, and cancelling
// the vault prompt must abandon the operation the user started — no profile
// created, nothing stored, editor open with their input intact.

function sealedVaultClient() {
  const status = vi.fn().mockResolvedValue({
    state: 'sealed',
    osKeyAvailable: false,
    osKeyCapable: false,
    hasPassphrase: false,
    autoSealMinutes: 0,
    providers: [],
    defaultProvider: null,
  })
  const unseal = vi.fn().mockResolvedValue({})
  const setup = vi.fn().mockResolvedValue({})
  const seal = vi.fn().mockResolvedValue({})
  const changePassphrase = vi.fn().mockResolvedValue({})
  const regenerateRecovery = vi.fn().mockResolvedValue({ recoveryCode: 'X' })
  const setDefaultProvider = vi.fn().mockResolvedValue({})
  const setAutoSeal = vi.fn().mockResolvedValue({})
  const activity = vi.fn().mockResolvedValue({})
  const inventory = vi.fn().mockResolvedValue({ entries: [] })
  const vaultClient = {
    status,
    unseal,
    setup,
    seal,
    changePassphrase,
    regenerateRecovery,
    setDefaultProvider,
    setAutoSeal,
    activity,
    inventory,
  } as unknown as VaultClient
  const vaultController = createVaultState(vaultClient)
  return { vaultClient, vaultController }
}

function clickButtonByText(container: HTMLElement, text: string, scope?: ParentNode): HTMLElement {
  const root = scope ?? container
  const btn = Array.from(root.querySelectorAll('.ui-button')).find(
    (b) => b.textContent?.trim() === text,
  )
  expect(btn, `button "${text}" not found`).toBeTruthy()
  ;(btn! as HTMLElement).click()
  return btn! as HTMLElement
}

describe('vault prompt cancellation', () => {
  it('cancel on the unlock prompt abandons the connection save', async () => {
    const { vaultClient, vaultController } = sealedVaultClient()
    await vaultController.refresh()
    const { container, client } = mount({ profiles: [], vaultController, vaultClient })

    const createProfileSpy = vi.spyOn(client, 'createProfile')
    const savePasswordSpy = vi
      .spyOn(client, 'savePassword')
      .mockRejectedValue(new RpcError('vault error', -32000, { reason: 'vault-sealed' }))

    await waitForProfiles(container, 0)

    // + New connection → quick-connect → full form
    clickButtonByText(container, '+ New connection')
    await vi.waitFor(() => expect(container.querySelector('#quick-connect-input')).toBeTruthy())
    fireEvent.input(container.querySelector('#quick-connect-input')!, {
      target: { value: 'web.example.com' },
    })
    clickButtonByText(container, 'Next')
    await vi.waitFor(() => expect(container.querySelector('#profile-host')).toBeTruthy())

    // Authentication → Password → set a password
    selectProfileSection(container, 'Authentication')
    clickSegmentedOption(container, 'Password')
    clickButtonByText(container, 'Set Password')
    await vi.waitFor(() => expect(container.querySelector('#password-value')).toBeTruthy())
    fireEvent.input(container.querySelector('#password-value')!, {
      target: { value: 'hunter2' },
    })
    clickButtonByText(container, 'OK')

    // The mint is attempted at the action moment — the vault asks to unlock
    // before any profile is created.
    await vi.waitFor(() => {
      expect(savePasswordSpy).toHaveBeenCalled()
      expect(container.querySelector('.ui-prompt[data-placement="top-sheet"]')).toBeTruthy()
    })

    // Cancel the unlock prompt
    const prompt = container.querySelector('.ui-prompt')!
    clickButtonByText(container, 'Cancel', prompt)

    // The whole save is abandoned: no profile created, nothing stored, editor
    // still open.
    expect(createProfileSpy).not.toHaveBeenCalled()
    await vi.waitFor(() => {
      expect(container.querySelector('.ui-prompt')).toBeNull()
    })
    expect(container.querySelector('#profile-host')).toBeTruthy()
  })

  it('cancel on the unlock prompt abandons the group save', async () => {
    const { vaultClient, vaultController } = sealedVaultClient()
    await vaultController.refresh()
    const { container, client } = mount({
      profiles: MOCK_PROFILES,
      groups: MOCK_GROUPS,
      vaultController,
      vaultClient,
    })

    const createGroupSpy = vi.spyOn(client, 'createGroup')
    const groupApplySpy = vi.spyOn(client, 'groupApply')
    const saveKeyMaterialSpy = vi
      .spyOn(client, 'saveKeyMaterial')
      .mockRejectedValue(new RpcError('vault error', -32000, { reason: 'vault-sealed' }))

    await waitForProfiles(container, 3)
    await openGroupEditorByName(container, 'Production')
    selectGroupSection(container, 'Connection')
    clickSegmentedOption(container, 'Public Key')
    clickSegmentedOption(container, 'Paste key')
    await vi.waitFor(() => expect(container.querySelector('#group-default-key-text')).toBeTruthy())
    fireEvent.input(container.querySelector('#group-default-key-text')!, {
      target: { value: '-----BEGIN PRIVATE KEY-----\nkey\n-----END PRIVATE KEY-----' },
    })

    const groupEditor = findDialogByTitle(container, 'Edit Group: Production')!
    clickButtonByText(container, 'Save Group', groupEditor)
    await vi.waitFor(() => {
      expect(saveKeyMaterialSpy).toHaveBeenCalled()
      expect(container.querySelector('.ui-prompt[data-placement="top-sheet"]')).toBeTruthy()
    })

    const prompt = container.querySelector('.ui-prompt')!
    clickButtonByText(container, 'Cancel', prompt)

    expect(createGroupSpy).not.toHaveBeenCalled()
    expect(groupApplySpy).not.toHaveBeenCalled()
    await vi.waitFor(() => {
      expect(container.querySelector('.ui-prompt')).toBeNull()
    })
    expect(findDialogByTitle(container, 'Edit Group: Production')).toBeTruthy()
  })
})

// ── The caret stays where the user put it ────────────────────────────────
// Reported from the running app and reproduced in a browser before it was
// fixed: after one character `document.activeElement` was `<body>` and the
// next keystroke went nowhere. The cause is Solid, not the kit — a render
// helper called from a JSX position that reads its draft signal synchronously
// becomes one computation over the whole form, so every keystroke rebuilds the
// DOM and replaces the very input being typed into. These tests assert the
// symptom a user feels rather than the mechanism, so they survive a rewrite of
// how the form is assembled.

describe('typing does not steal the caret', () => {
  it('keeps focus in the connection editor Host field across keystrokes', async () => {
    const { container } = mount({ profiles: MOCK_PROFILES.slice(0, 1) })
    await waitForProfiles(container, 1)
    await openProfileEditor(container, 'prod-web')

    const host = container.querySelector('#profile-host') as HTMLInputElement
    host.focus()
    expect(document.activeElement).toBe(host)

    fireEvent.input(host, { target: { value: 'exampl' } })
    expect(document.activeElement).toBe(container.querySelector('#profile-host'))

    fireEvent.input(container.querySelector('#profile-host')!, { target: { value: 'example' } })
    expect(document.activeElement).toBe(container.querySelector('#profile-host'))
    expect((container.querySelector('#profile-host') as HTMLInputElement).value).toBe('example')
  })

  it('keeps focus in the group editor User field across keystrokes', async () => {
    const { container } = mount({ profiles: MOCK_PROFILES, groups: MOCK_GROUPS })
    await waitForProfiles(container, 3)
    await openGroupEditorByName(container, 'Production')
    selectGroupSection(container, 'Connection')

    const user = await vi.waitFor(() => {
      const el = container.querySelector<HTMLInputElement>('#group-default-auth-user')
      expect(el).toBeTruthy()
      return el!
    })
    user.focus()
    expect(document.activeElement).toBe(user)

    fireEvent.input(user, { target: { value: 'ro' } })
    expect(document.activeElement).toBe(container.querySelector('#group-default-auth-user'))

    fireEvent.input(container.querySelector('#group-default-auth-user')!, {
      target: { value: 'roo' },
    })
    expect(document.activeElement).toBe(container.querySelector('#group-default-auth-user'))
  })
})

// ── Validation reports a wrong answer while it is still on screen ────────
// "Почему мы сразу не показываем, что нам что-то не нравится?" — a host with
// characters a host cannot contain looked accepted until Create was pressed.
// Being unanswered still waits; being wrong does not.

describe('eager validation', () => {
  it('shows the host message while typing, with no blur and no submit', async () => {
    const { container } = mount({ profiles: MOCK_PROFILES.slice(0, 1) })
    await waitForProfiles(container, 1)
    await openProfileEditor(container, 'prod-web')

    const host = container.querySelector('#profile-host') as HTMLInputElement
    fireEvent.input(host, { target: { value: 'фывфы' } })

    await vi.waitFor(() => {
      expect(container.textContent).toContain('Host contains characters that are not valid')
    })
  })

  it('says nothing about an empty field until it is left or submitted', async () => {
    const { container } = mount({ profiles: MOCK_PROFILES.slice(0, 1) })
    await waitForProfiles(container, 1)
    await openProfileEditor(container, 'prod-web')

    const host = container.querySelector('#profile-host') as HTMLInputElement
    fireEvent.input(host, { target: { value: '' } })
    expect(container.textContent).not.toContain('Host is required')

    fireEvent.blur(host)
    await vi.waitFor(() => {
      expect(container.textContent).toContain('Host is required')
    })
  })
})

// ── A generated name says what the secret is ─────────────────────────────
// Reported from the running app: a connection with an encrypted key stores
// the key and its passphrase, and the Secrets page showed two rows both
// called `root@192.168.0.57`. The kind badge tells them apart in the list;
// the name has to tell them apart everywhere else, starting with any picker
// that chooses between them.

describe('generated secret names', () => {
  it('names a key and its passphrase differently for one login', async () => {
    const { container, client } = mount({ profiles: [] })
    const saveKeyMat = vi.spyOn(client, 'saveKeyMaterial').mockResolvedValue({
      row: 'secrow:key-named',
      fingerprint: 'SHA256:zz',
      passphraseWanted: true,
    })
    const savePassphrase = vi
      .spyOn(client, 'saveKeyPassphrase')
      .mockResolvedValue({ row: 'secrow:pass-named' })
    const createProfileSpy = vi
      .spyOn(client, 'createProfile')
      .mockImplementation((p) => Promise.resolve({ ...p, id: 'ssh:named' }))

    await waitForProfiles(container, 0)
    clickButtonByText(container, '+ New connection')
    await vi.waitFor(() => expect(container.querySelector('#quick-connect-input')).toBeTruthy())
    fireEvent.input(container.querySelector('#quick-connect-input')!, {
      target: { value: 'root@box.example.com' },
    })
    clickButtonByText(container, 'Next')
    await vi.waitFor(() => expect(container.querySelector('#profile-host')).toBeTruthy())

    selectProfileSection(container, 'Authentication')
    clickSegmentedOption(container, 'Public Key')
    clickSegmentedOption(container, 'Paste key')
    await vi.waitFor(() => expect(container.querySelector('#profile-key-text')).toBeTruthy())
    fireEvent.input(container.querySelector('#profile-key-text')!, {
      target: { value: '-----BEGIN PRIVATE KEY-----\nx\n-----END PRIVATE KEY-----' },
    })

    const editor = findDialogByTitleContaining(container, 'New Connection')!
    clickButtonByText(container, 'Create Connection', editor)

    // The key is named for what it is…
    await vi.waitFor(() => {
      expect(saveKeyMat).toHaveBeenCalledWith(
        expect.stringContaining('BEGIN PRIVATE KEY'),
        'Key for root@box.example.com',
      )
    })

    // …and the passphrase prompt, which is asked about the KEY, stores its
    // own secret under its own name rather than repeating the key's.
    const prompt = await vi.waitFor(() => {
      const p = container.querySelector('.ui-prompt')
      expect(p).toBeTruthy()
      return p!
    })
    expect(prompt.textContent).toContain('root@box.example.com')
    fireEvent.input(container.querySelector('#key-passphrase')!, { target: { value: 'letmein' } })
    clickButtonByText(container, 'Save passphrase', prompt)

    await vi.waitFor(() => {
      expect(savePassphrase).toHaveBeenCalledWith(
        'secrow:key-named',
        'letmein',
        'Passphrase for root@box.example.com',
      )
    })
    // And both minted rows are bound on the profile that is created.
    await vi.waitFor(() => {
      expect(createProfileSpy).toHaveBeenCalled()
    })
    const created = createProfileSpy.mock.calls[0][0]
    expect(created.options.keySecret).toBe('secrow:key-named')
    expect(created.options.keyPassphraseSecret).toBe('secrow:pass-named')
  })
})

// @vitest-environment jsdom
/**
 * Characterization test for the connections surface — tests only what a user
 * can see and do through the ConnectionsContent TabContent interface, not
 * internal CSS classes or DOM structure.
 *
 * Step 1 (characterize): written before the rewrite, run against the current
 * imperative implementation (ConnectionManagerViewImpl).
 * Step 3-4 (rewrite + same test green): after replacing with the Solid
 * component, this file must pass unchanged — if it needs changes, the
 * rewrite has changed behaviour.
 * Step 5 (delete): old connections.ts + connections-content.ts go away in the
 * same commit as the Solid replacement; this file survives as the regression
 * suite.
 */
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { ConnectionsContent } from './connections-content'
import type { SSHProfile, Credential } from './profiles'
import { ProfileClient } from './profiles'
import { Dispatcher } from './dispatcher'
import type { TabHost } from './tab-content'
function stubHost(): TabHost {
  return {
    setTitle: vi.fn(),
    requestAttention: vi.fn(),
    requestClose: vi.fn(),
  }
}

/** Mount the connections content and wait for data to load. */
async function mount(content: ConnectionsContent, container: HTMLElement) {
  content.setTarget(container)
  const abort = new AbortController()
  await content.mount(container, stubHost(), abort.signal)
}

/**
 * Find a button by its exact visible text. Throws if not found — all callers
 * expect the button to exist.
 */
function buttonByText(container: HTMLElement, text: string): HTMLButtonElement {
  const btn = [...container.querySelectorAll('button')].find((b) => b.textContent?.trim() === text)
  if (!btn) throw new Error(`Button "${text}" not found`)
  return btn
}

/**
 * Click an item (list row) by its name text. Items show the profile/credential
 * name as the first text child of their info section.
 */
function clickItemByProfileName(container: HTMLElement, name: string): void {
  // Find any element whose text content includes the profile name and has a
  // click listener (is inside .cm-item or similar). Walk siblings to find
  // the row element.
  const allElements = container.querySelectorAll('*')
  for (const el of allElements) {
    if (el.textContent?.trim() === name) {
      const item = el.closest('[class*="item"]')
      if (item && item instanceof HTMLElement) {
        item.click()
        return
      }
    }
  }
  // Fallback: find by text anywhere in the container.
  for (const btn of container.querySelectorAll('button')) {
    if (btn.textContent?.trim() === name) {
      btn.click()
      return
    }
  }
  throw new Error(`Item "${name}" not found`)
}

/** Check if text is visible in the container. */
function textVisible(container: HTMLElement, text: string): boolean {
  return container.textContent?.includes(text) ?? false
}

// ── Profile fixtures ────────────────────────────────────────────────────────

function makeProfile(
  id: string,
  name: string,
  overrides?: Partial<SSHProfile['options']>,
): SSHProfile {
  return {
    id,
    type: 'ssh',
    name,
    options: {
      host: overrides?.host ?? `${name}.example.com`,
      port: overrides?.port ?? 22,
      user: overrides?.user ?? 'admin',
      auth: overrides?.auth ?? '',
      keepaliveInterval: overrides?.keepaliveInterval,
      keepaliveCountMax: overrides?.keepaliveCountMax,
      readyTimeout: overrides?.readyTimeout,
      jumpHost: overrides?.jumpHost,
      agentForward: overrides?.agentForward,
      canBeJumpServer: overrides?.canBeJumpServer,
    },
  }
}

function makeCredential(overrides?: Partial<Credential>): Credential {
  return {
    id: overrides?.id ?? 'c1',
    name: overrides?.name ?? 'Admin Key',
    username: overrides?.username ?? 'admin',
    auth: overrides?.auth ?? 'publicKey',
    keyPath: overrides?.keyPath,
  }
}

// ── Suite ───────────────────────────────────────────────────────────────────

describe('Connections surface — observable behaviour', () => {
  let container: HTMLElement
  let client: ProfileClient
  let content: ConnectionsContent

  beforeEach(() => {
    document.body.replaceChildren()
    container = document.createElement('div')
    document.body.append(container)
    client = new ProfileClient(new Dispatcher())

    // Default mocks: empty state.
    vi.spyOn(client, 'listProfiles').mockResolvedValue([])
    vi.spyOn(client, 'listGroups').mockResolvedValue([])
    vi.spyOn(client, 'listCredentials').mockResolvedValue([])
    vi.spyOn(client, 'createProfile').mockResolvedValue({} as SSHProfile)
    vi.spyOn(client, 'deleteProfile').mockResolvedValue(true)
    vi.spyOn(client, 'importTabby').mockResolvedValue(0)
    vi.spyOn(client, 'createCredential').mockResolvedValue({} as never)
    vi.spyOn(client, 'deleteCredential').mockResolvedValue(true)
    vi.spyOn(client, 'savePassword').mockResolvedValue(true)
    vi.spyOn(client, 'hasPassword').mockResolvedValue(false)

    content = new ConnectionsContent(client)
  })

  // ── Header & empty state ────────────────────────────────────────────────

  it('renders header with title and action buttons', async () => {
    await mount(content, container)

    expect(textVisible(container, 'Connections')).toBe(true)
    expect(textVisible(container, '+ New connection')).toBe(true)
    expect(textVisible(container, 'Import from Tabby')).toBe(true)
    expect(textVisible(container, 'Saved credentials')).toBe(true)
  })

  it('shows empty state when no profiles and no credentials', async () => {
    await mount(content, container)

    expect(textVisible(container, 'No connections yet')).toBe(true)
  })

  it('shows title "Connections" set on the host', async () => {
    const host = stubHost()
    content.setTarget(container)
    const abort = new AbortController()
    await content.mount(container, host, abort.signal)

    // eslint-disable-next-line @typescript-eslint/unbound-method
    expect(host.setTitle).toHaveBeenCalledWith('Connections')
  })

  it('renders profile names in the list', async () => {
    vi.spyOn(client, 'listProfiles').mockResolvedValue([
      makeProfile('p1', 'web1', { user: 'alice' }),
      makeProfile('p2', 'web2', { user: 'bob' }),
    ])
    await mount(content, container)

    expect(textVisible(container, 'web1')).toBe(true)
    expect(textVisible(container, 'web2')).toBe(true)
  })

  it('renders group names as section headers', async () => {
    vi.spyOn(client, 'listGroups').mockResolvedValue([{ id: 'g1', name: 'Production' }])
    vi.spyOn(client, 'listProfiles').mockResolvedValue([
      { id: 'p1', type: 'ssh', name: 'web1', group: 'g1', options: { host: 'h1', port: 22 } },
    ])
    await mount(content, container)

    expect(textVisible(container, 'Production')).toBe(true)
  })

  // ── Profile selection & form ─────────────────────────────────────────────

  it('shows form panel when a profile is selected via click', async () => {
    vi.spyOn(client, 'listProfiles').mockResolvedValue([
      makeProfile('p1', 'web1', { host: 'h1', user: 'admin' }),
    ])
    await mount(content, container)

    // Click the profile item.
    clickItemByProfileName(container, 'web1')

    // The form should show editing controls: Connect and Save buttons.
    expect(textVisible(container, 'Connect')).toBe(true)
    expect(textVisible(container, 'Save')).toBe(true)
  })

  it('opens new profile form on "+ New connection" click', async () => {
    await mount(content, container)

    buttonByText(container, '+ New connection').click()

    // The new-profile form shows the basic fields and save as "Create".
    expect(textVisible(container, 'Basic')).toBe(true)
    expect(textVisible(container, 'Host')).toBe(true)
    expect(textVisible(container, 'Port')).toBe(true)
    expect(textVisible(container, 'Create')).toBe(true)
  })

  // ── Auth methods (user-visible) ──────────────────────────────────────────

  it('form exposes all five auth method labels', async () => {
    await mount(content, container)

    buttonByText(container, '+ New connection').click()

    expect(textVisible(container, 'Auto')).toBe(true)
    expect(textVisible(container, 'Password')).toBe(true)
    expect(textVisible(container, 'Public Key')).toBe(true)
    expect(textVisible(container, 'Agent')).toBe(true)
    expect(textVisible(container, 'Keyboard Interactive')).toBe(true)
  })

  // ── Advanced settings ────────────────────────────────────────────────────

  it('form exposes advanced SSH settings', async () => {
    await mount(content, container)

    buttonByText(container, '+ New connection').click()

    expect(textVisible(container, 'Keepalive interval')).toBe(true)
    expect(textVisible(container, 'Keepalive count max')).toBe(true)
    expect(textVisible(container, 'Ready timeout')).toBe(true)
    expect(textVisible(container, 'Agent forward')).toBe(true)
  })

  // ── Connect callback ─────────────────────────────────────────────────────

  it('fires onConnect when Connect button is clicked', async () => {
    const profile = makeProfile('p1', 'web1', { host: 'h', user: 'u' })
    vi.spyOn(client, 'listProfiles').mockResolvedValue([profile])

    let connected: SSHProfile | null = null
    content.onConnect = (p) => {
      connected = p
    }

    await mount(content, container)

    // Select the profile.
    clickItemByProfileName(container, 'web1')

    // Click Connect button.
    buttonByText(container, 'Connect').click()

    expect(connected).not.toBeNull()
    expect(connected!.id).toBe('p1')
  })

  // ── Import button ────────────────────────────────────────────────────────

  it('shows Import from Tabby button that is clickable', async () => {
    await mount(content, container)

    const btn = buttonByText(container, 'Import from Tabby')
    expect(btn).toBeDefined()
  })

  // ── XSS: credential name rendered as text, not HTML ──────────────────────

  it('renders credential name as text — no HTML injection', async () => {
    const payload = '<img src=x onerror="window.__pwned=1">'
    const cred = makeCredential({ id: 'c1', name: payload, username: 'alice', auth: 'publicKey' })
    vi.spyOn(client, 'listCredentials').mockResolvedValue([cred])
    vi.spyOn(client, 'listProfiles').mockResolvedValue([
      {
        id: 'p1',
        type: 'ssh',
        name: 'web1',
        options: { host: 'h', user: 'u', credentialId: 'c1', port: 22 },
      },
    ])

    await mount(content, container)

    // The literal payload text must be visible.
    expect(container.textContent).toContain(payload)

    // No <img> element should exist — the name was rendered as text, not HTML.
    expect(container.querySelector('img')).toBeNull()
  })
})

// ── ADR-0013 regression: credential form has no Bind to Host/Port ───────────

describe('ADR-0013: credential form security boundary', () => {
  let container: HTMLElement
  let client: ProfileClient
  let content: ConnectionsContent

  function fillField(label: string, value: string): void {
    const labels = container.querySelectorAll('label')
    for (const lbl of labels) {
      if (lbl.textContent?.includes(label)) {
        const field = lbl.parentElement
        if (!field) continue
        const input = field.querySelector<HTMLInputElement>('input')
        if (input) {
          input.value = value
          input.dispatchEvent(new Event('input', { bubbles: true }))
          return
        }
      }
    }
    throw new Error(`Field with label "${label}" not found`)
  }

  function clickSave(): void {
    const btns = container.querySelectorAll('button')
    for (const btn of btns) {
      if (/Create Credential|Save Credential/.test(btn.textContent ?? '')) {
        btn.click()
        return
      }
    }
    throw new Error('Credential save button not found')
  }

  function openCredentials(): void {
    const btns = container.querySelectorAll('button')
    for (const btn of btns) {
      if (btn.textContent?.trim() === 'Saved credentials') {
        btn.click()
        return
      }
    }
    throw new Error('"Saved credentials" button not found')
  }

  beforeEach(async () => {
    document.body.replaceChildren()
    container = document.createElement('div')
    document.body.append(container)
    client = new ProfileClient(new Dispatcher())

    vi.spyOn(client, 'listProfiles').mockResolvedValue([])
    vi.spyOn(client, 'listGroups').mockResolvedValue([])
    vi.spyOn(client, 'listCredentials').mockResolvedValue([])
    vi.spyOn(client, 'hasPassword').mockResolvedValue(false)
    vi.spyOn(client, 'savePassword').mockResolvedValue(true)
    vi.spyOn(client, 'createCredential').mockResolvedValue({} as never)

    content = new ConnectionsContent(client)
    await mount(content, container)
  })

  it('form does not show Bind to Host field', async () => {
    openCredentials()

    // ADR-0013: Bind to Host is backend-owned, not exposed in credential form
    expect(textVisible(container, 'Bind to Host')).toBe(false)
  })

  it('form does not show Port field for credentials', async () => {
    openCredentials()

    // ADR-0013: Port binding is backend-owned, not exposed in credential form
    // (Port field exists in profile form, but not in credential form)
    const portFields = [...container.querySelectorAll('label')].filter(
      (lbl) => lbl.textContent?.includes('Port') && lbl.closest('.cm-form'),
    )
    expect(portFields.length).toBe(0)
  })

  it('submit without host succeeds and payload has no host/port/trustedEndpoints', async () => {
    const create = vi
      .spyOn(client, 'createCredential')
      .mockResolvedValue({ id: 'c1', name: 'no-host', username: 'bob', auth: 'password' } as never)

    openCredentials()
    fillField('Name', 'no-host')
    fillField('Username', 'bob')
    clickSave()

    await Promise.resolve()

    expect(create).toHaveBeenCalledTimes(1)
    const payload = create.mock.calls[0][0]
    expect((payload as any).host).toBeUndefined()
    expect((payload as any).port).toBeUndefined()
    expect((payload as any).trustedEndpoints).toBeUndefined()
  })
})

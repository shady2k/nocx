// @vitest-environment jsdom
import { describe, expect, it, vi, afterEach, beforeEach, type Mock } from 'vitest'
import { cleanup } from '@solidjs/testing-library'
import {
  ActionsQuickConnectProvider,
  SSHQuickConnectProvider,
  SSHAliasQuickConnectProvider,
  AdHocQuickConnectProvider,
  SecretsQuickConnectProvider,
  QuickConnectController,
  type QuickConnectItem,
  type DrillSelection,
  type QuickConnectProvider,
  type SecretsProviderDeps,
} from './quick-connect'

afterEach(() => {
  cleanup()
})

/* ── Actions provider ───────────────────────────────────────────────── */

describe('ActionsQuickConnectProvider', () => {
  it('offers the local shell and new connection, in that order', async () => {
    const provider = new ActionsQuickConnectProvider(vi.fn(), vi.fn())
    const items = await Promise.resolve(provider.getItems())

    // The order is the contract, not an accident: these are the palette's
    // first group and the separator below them is drawn from the group
    // boundary.
    expect(items.map((i) => i.id)).toEqual(['__local__', '__new_connection__'])
    expect(items[0].label).toBe('Local shell')
    expect(items[0].detail).toContain('local terminal')
    expect(items[1].label).toBe('New connection')
  })

  it('every item is typed Command — the palette badge vocabulary (nocx-4t37)', async () => {
    const provider = new ActionsQuickConnectProvider(vi.fn(), vi.fn())
    const items = await Promise.resolve(provider.getItems())

    expect(items.every((i) => i.kind === 'command')).toBe(true)
  })

  it('adds a target-needing command ("Forward a port") when one is provided', async () => {
    const run = vi.fn()
    const drillCommand = {
      id: '__forward_port__',
      label: 'Forward a port',
      detail: 'Expose a port on this machine',
      steps: [
        { name: 'server', fetch: () => Promise.resolve([]) },
        { name: 'port', fetch: () => Promise.resolve([]) },
      ],
      run,
    }
    const provider = new ActionsQuickConnectProvider(vi.fn(), vi.fn(), drillCommand)

    const items = await Promise.resolve(provider.getItems())
    // Last, not first: the first row is what Enter activates on open, and
    // that stays the muscle-memory "Local shell".
    expect(items.map((i) => i.id)).toEqual(['__local__', '__new_connection__', '__forward_port__'])
    expect(items[2].label).toBe('Forward a port')
    expect(items[2].kind).toBe('command')
    expect(items[2].drill).toBe(drillCommand)
    // Activating the drill item never runs it directly — the surface walks
    // the steps instead.
    items[2].run()
    expect(run).not.toHaveBeenCalled()
  })

  it('does not offer Ports in the palette — it is a sidebar view now (nocx-wzc4.7)', async () => {
    const provider = new ActionsQuickConnectProvider(vi.fn(), vi.fn())
    const items = await Promise.resolve(provider.getItems())

    // Ports is a surface you keep open beside the terminal, not a one-shot
    // verb; the palette is for verbs.
    expect(items.some((i) => i.label === 'Ports')).toBe(false)
  })

  it('calls newPane when the local-shell item runs', () => {
    const newPane = vi.fn()
    const newConnection = vi.fn()
    const provider = new ActionsQuickConnectProvider(newPane, newConnection)

    const items = provider.getItems()
    items[0].run()

    expect(newPane).toHaveBeenCalledOnce()
    expect(newConnection).not.toHaveBeenCalled()
  })

  it('opens the connection editor when the new-connection item runs', () => {
    const newPane = vi.fn()
    const newConnection = vi.fn()
    const provider = new ActionsQuickConnectProvider(newPane, newConnection)

    const items = provider.getItems()
    items[1].run()

    expect(newConnection).toHaveBeenCalledOnce()
    expect(newPane).not.toHaveBeenCalled()
  })
})

/* ── SSH provider ───────────────────────────────────────────────────── */

describe('SSHQuickConnectProvider', () => {
  it('returns items from listProfiles', async () => {
    const profileClient = {
      listProfiles: vi.fn().mockResolvedValue([
        {
          id: 'ssh:custom:server1:uuid',
          type: 'ssh' as const,
          name: 'Production DB',
          options: { host: 'db.example.com', port: 22, user: 'admin' },
        },
        {
          id: 'ssh:custom:server2:uuid',
          type: 'ssh' as const,
          name: 'Dev box',
          options: { host: 'dev.local', port: 22 },
        },
      ]),
    }
    const newSSHPane = vi.fn()
    const provider = new SSHQuickConnectProvider(profileClient as never, newSSHPane)

    const items = await Promise.resolve(provider.getItems())

    expect(items).toHaveLength(2)
    expect(items[0].label).toBe('admin@db.example.com')
    expect(items[0].detail).toBe('Production DB')
    expect(items[1].label).toBe('dev.local')
    expect(items[1].detail).toBe('Dev box')
  })

  it('calls newSSHPane with correct args on run()', async () => {
    const profileClient = {
      listProfiles: vi.fn().mockResolvedValue([
        {
          id: 'ssh:custom:x:uuid',
          type: 'ssh' as const,
          name: 'X',
          options: { host: 'x.example.com', port: 22, user: 'me' },
        },
      ]),
    }
    const newSSHPane = vi.fn()
    const provider = new SSHQuickConnectProvider(profileClient as never, newSSHPane)

    const items = await Promise.resolve(provider.getItems())
    items[0].run()

    expect(newSSHPane).toHaveBeenCalledWith('ssh:custom:x:uuid', 'x.example.com', 'me')
  })

  it('handles empty profile list', async () => {
    const profileClient = {
      listProfiles: vi.fn().mockResolvedValue([]),
    }
    const newSSHPane = vi.fn()
    const provider = new SSHQuickConnectProvider(profileClient as never, newSSHPane)

    const items = await Promise.resolve(provider.getItems())
    expect(items).toHaveLength(0)
  })

  it('filters profiles with empty or whitespace-only host', async () => {
    const profileClient = {
      listProfiles: vi.fn().mockResolvedValue([
        {
          id: 'ssh:c:good:uuid',
          type: 'ssh' as const,
          name: 'Good',
          options: { host: 'server.example.com', port: 22 },
        },
        {
          id: 'ssh:c:empty:uuid',
          type: 'ssh' as const,
          name: 'Empty host',
          options: { host: '', port: 22 },
        },
        {
          id: 'ssh:c:spaces:uuid',
          type: 'ssh' as const,
          name: 'Whitespace host',
          options: { host: '  ', port: 22 },
        },
      ]),
    }
    const newSSHPane = vi.fn()
    const provider = new SSHQuickConnectProvider(profileClient as never, newSSHPane)

    const items = await Promise.resolve(provider.getItems())

    expect(items).toHaveLength(1)
    expect(items[0].id).toBe('ssh:c:good:uuid')
    expect(newSSHPane).not.toHaveBeenCalled()
  })
})

/* ── Controller rendering and interaction ───────────────────────────── */

describe('QuickConnectController', () => {
  let container: HTMLDivElement

  beforeEach(() => {
    container = document.createElement('div')
    document.body.append(container)
  })

  afterEach(() => {
    container.remove()
  })

  /** Wait for microtasks (createEffect's async body) to settle. */
  async function waitForItems(): Promise<void> {
    return new Promise((resolve) => setTimeout(resolve, 0))
  }

  function makeController(): QuickConnectController {
    const c = new QuickConnectController()
    afterEach(() => c.destroy())
    return c
  }

  it('mounts without error', () => {
    const ctrl = makeController()
    const providers: QuickConnectProvider[] = [new ActionsQuickConnectProvider(vi.fn(), vi.fn())]
    ctrl.mount(container, providers)
    const dialog = container.querySelector<HTMLDialogElement>('dialog.nocx-dialog')
    expect(dialog).toBeTruthy()
    expect(dialog?.open).toBe(false)
  })

  it('show() opens the dialog', () => {
    const ctrl = makeController()
    const providers: QuickConnectProvider[] = [new ActionsQuickConnectProvider(vi.fn(), vi.fn())]
    ctrl.mount(container, providers)
    ctrl.show()

    const dialog = container.querySelector<HTMLDialogElement>('dialog.nocx-dialog')
    expect(dialog).toBeTruthy()
  })

  it('show() is no-op before mount', () => {
    const ctrl = makeController()
    expect(() => ctrl.show()).not.toThrow()
  })

  it('destroy() cleans up the DOM', () => {
    const ctrl = makeController()
    ctrl.mount(container, [new ActionsQuickConnectProvider(vi.fn(), vi.fn())])
    ctrl.show()
    ctrl.destroy()

    expect(container.children.length).toBe(0)
  })

  it('Escape closes the dialog via native cancel event', () => {
    const ctrl = makeController()
    ctrl.mount(container, [new ActionsQuickConnectProvider(vi.fn(), vi.fn())])
    ctrl.show()

    const dialog = container.querySelector<HTMLDialogElement>('dialog.nocx-dialog')
    expect(dialog).toBeTruthy()
    expect(dialog?.open).toBe(true)

    dialog!.dispatchEvent(new Event('cancel', { bubbles: true }))
    expect(dialog?.open).toBe(false)
  })

  it('ArrowDown/ArrowUp navigates the list and Enter activates', async () => {
    const ctrl = makeController()
    const providers: QuickConnectProvider[] = [
      new (class implements QuickConnectProvider {
        readonly id = 'test'
        readonly label = 'Test'
        readonly kinds = ['host', 'command', 'secret'] as const
        getItems(): QuickConnectItem[] {
          return [
            { id: 'a', kind: 'host', label: 'First', run: vi.fn() },
            { id: 'b', kind: 'host', label: 'Second', run: vi.fn() },
          ]
        }
      })(),
    ]
    ctrl.mount(container, providers)
    ctrl.show()
    await waitForItems()

    const itemsEl = container.querySelectorAll('.quick-connect__item')
    expect(itemsEl.length).toBe(2)
    expect(itemsEl[0].classList.contains('quick-connect__item--selected')).toBe(true)

    const input = container.querySelector<HTMLElement>('.quick-connect__search input')
    expect(input).toBeTruthy()

    input!.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }))
    expect(itemsEl[1].classList.contains('quick-connect__item--selected')).toBe(true)
    expect(itemsEl[0].classList.contains('quick-connect__item--selected')).toBe(false)

    input!.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowUp', bubbles: true }))
    expect(itemsEl[0].classList.contains('quick-connect__item--selected')).toBe(true)
  })

  it('Enter on a selected item closes the dialog', async () => {
    const ctrl = makeController()
    const newPane = vi.fn()
    ctrl.mount(container, [new ActionsQuickConnectProvider(newPane, vi.fn())])
    // The only provider here is a command provider, and commands live in the
    // palette — open it (nocx-4t37).
    ctrl.showPalette()
    await waitForItems()

    const dialog = container.querySelector<HTMLDialogElement>('dialog.nocx-dialog')
    expect(dialog?.open).toBe(true)

    const input = container.querySelector<HTMLElement>('.quick-connect__search input')
    expect(input).toBeTruthy()
    input!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))

    expect(newPane).toHaveBeenCalledOnce()
    expect(dialog?.open).toBe(false)
  })

  it('typing in the search input filters the list', async () => {
    const ctrl = makeController()
    const providers: QuickConnectProvider[] = [
      new (class implements QuickConnectProvider {
        readonly id = 'test'
        readonly label = 'Test'
        readonly kinds = ['host', 'command', 'secret'] as const
        getItems(): QuickConnectItem[] {
          return [
            { id: 'a', kind: 'host', label: 'admin@server1', detail: 'Production', run: vi.fn() },
            { id: 'b', kind: 'host', label: 'root@server2', detail: 'Staging', run: vi.fn() },
            { id: 'c', kind: 'host', label: 'Local shell', run: vi.fn() },
          ]
        }
      })(),
    ]
    ctrl.mount(container, providers)
    ctrl.show()
    await waitForItems()

    expect(container.querySelectorAll('.quick-connect__item').length).toBe(3)

    const input = container.querySelector<HTMLInputElement>('.quick-connect__search input')
    expect(input).toBeTruthy()
    input!.value = 'production'
    input!.dispatchEvent(new Event('input', { bubbles: true }))

    expect(container.querySelectorAll('.quick-connect__item').length).toBe(1)
    expect(container.querySelector('.quick-connect__item')?.textContent).toContain('admin@server1')
  })

  it('search input is focused after opening via rAF', async () => {
    const ctrl = makeController()
    ctrl.mount(container, [new ActionsQuickConnectProvider(vi.fn(), vi.fn())])
    ctrl.show()
    await waitForItems()

    // Flush requestAnimationFrame queue (the component focuses inside rAF).
    await new Promise((resolve) => requestAnimationFrame(resolve))

    const input = container.querySelector('.quick-connect__search input')
    expect(input).toBeTruthy()

    // In jsdom, rAF fires synchronously via the polyfill. The component's
    // panelRef.querySelector focus should have run.
    expect(document.activeElement).toBe(input)
  })

  it('focus restoration verified in e2e (jsdom cannot model dialog focus return)', () => {
    const ctrl = makeController()
    const button = document.createElement('button')
    button.textContent = 'Trigger'
    container.append(button)
    button.focus()
    expect(document.activeElement).toBe(button)

    ctrl.mount(container, [new ActionsQuickConnectProvider(vi.fn(), vi.fn())])
    ctrl.show()

    const dialog = container.querySelector<HTMLDialogElement>('dialog.nocx-dialog')
    expect(dialog).toBeTruthy()

    dialog!.dispatchEvent(new Event('cancel', { bubbles: true }))
    expect(dialog?.open).toBe(false)

    // jsdom's patched showModal/close do not model the browser's native
    // dialog focus-return mechanism. The Dialog component's restoreFocus
    // uses rAF and relies on the browser's native prevFocus tracking,
    // which jsdom cannot replicate.
    //
    // Focus-return is verified in e2e/quick-connect.spec.ts (the Escape
    // test asserts .tab-caret is focused after close).
  })

  it('stale async provider result is discarded on reopen', async () => {
    // Each call to getItems returns a new deferred promise, so we can
    // control the resolution order independently per open.
    type ResolveFn = (items: QuickConnectItem[]) => void
    const resolvers: ResolveFn[] = []
    const slowProvider: QuickConnectProvider = {
      id: 'slow',
      label: 'Slow',
      kinds: ['host', 'command', 'secret'] as const,
      getItems: () =>
        new Promise<QuickConnectItem[]>((resolve) => {
          resolvers.push(resolve)
        }),
    }

    const ctrl = makeController()
    ctrl.mount(container, [slowProvider])

    // First open — provider doesn't resolve yet.
    ctrl.show()
    await waitForItems()
    expect(resolvers.length).toBe(1)

    // Close before the first provider resolves.
    const d1 = container.querySelector<HTMLDialogElement>('dialog.nocx-dialog')!
    d1.dispatchEvent(new Event('cancel', { bubbles: true }))
    await waitForItems()
    await new Promise((resolve) => setTimeout(resolve, 0))

    // Reopen — second call to getItems creates a new resolver.
    ctrl.show()
    await waitForItems()
    expect(resolvers.length).toBe(2)

    // Resolve the SECOND (current) load first.
    resolvers[1]([{ id: 'fresh', kind: 'host', label: 'Fresh Item', run: vi.fn() }])
    await waitForItems()

    // The list shows the fresh item.
    const items1 = container.querySelectorAll('.quick-connect__item')
    expect(items1.length).toBe(1)
    expect(items1[0].textContent).toContain('Fresh Item')

    // Now resolve the FIRST (stale) load.
    resolvers[0]([{ id: 'stale', kind: 'host', label: 'Stale Item', run: vi.fn() }])
    await waitForItems()

    // List must NOT have changed — stale result discarded by generation guard.
    const items2 = container.querySelectorAll('.quick-connect__item')
    expect(items2.length).toBe(1)
    expect(items2[0].textContent).toContain('Fresh Item')
    expect(items2[0].textContent).not.toContain('Stale')
  })
})

/* ── Filtering logic ────────────────────────────────────────────────── */

describe('filtering behavior', () => {
  it('filter text narrows list items (case-insensitive)', () => {
    const items: QuickConnectItem[] = [
      { id: 'a', kind: 'host', label: 'admin@server1', detail: 'Production', run: vi.fn() },
      { id: 'b', kind: 'host', label: 'root@server2', detail: 'Staging', run: vi.fn() },
      { id: 'c', kind: 'host', label: 'Local shell', run: vi.fn() },
    ]

    const q = 'prod'
    const filtered = items.filter(
      (it) =>
        it.label.toLowerCase().includes(q) ||
        (it.detail !== undefined && it.detail.toLowerCase().includes(q)),
    )

    expect(filtered).toHaveLength(1)
    expect(filtered[0].id).toBe('a')
  })

  it('empty query returns all items', () => {
    const items: QuickConnectItem[] = [
      { id: 'a', kind: 'host', label: 'admin@server1', run: vi.fn() },
      { id: 'b', kind: 'host', label: 'root@server2', run: vi.fn() },
    ]

    const filtered = items.filter((it) => it.label.toLowerCase().includes(''))
    expect(filtered).toHaveLength(2)
  })

  it('matches on detail field', () => {
    const items: QuickConnectItem[] = [
      { id: 'a', kind: 'host', label: '192.168.1.1', detail: 'Production DB', run: vi.fn() },
    ]

    const filtered = items.filter(
      (it) =>
        it.label.toLowerCase().includes('production') ||
        (it.detail !== undefined && it.detail.toLowerCase().includes('production')),
    )

    expect(filtered).toHaveLength(1)
  })

  it('no match returns empty list', () => {
    const items: QuickConnectItem[] = [
      { id: 'a', kind: 'host', label: 'admin@server1', run: vi.fn() },
    ]

    const q = 'zzzzz'
    const filtered = items.filter(
      (it) =>
        it.label.toLowerCase().includes(q) ||
        (it.detail !== undefined && it.detail.toLowerCase().includes(q)),
    )

    expect(filtered).toHaveLength(0)
  })
})

/* ── SSH Alias provider ─────────────────────────────────────────────── */

describe('SSHAliasQuickConnectProvider', () => {
  it('returns items from listSSHAliases', async () => {
    const profileClient = {
      listProfiles: vi.fn().mockResolvedValue([]),
      listSSHAliases: vi.fn().mockResolvedValue({
        aliases: [
          { alias: 'prod-db', hostName: '10.0.0.1', user: 'deploy', port: 2222 },
          { alias: 'dev-box', hostName: 'dev.local' },
        ],
        unavailable: null,
      }),
    }
    const newPaneByHost = vi.fn()
    const provider = new SSHAliasQuickConnectProvider(profileClient as never, newPaneByHost)

    const items = await provider.getItems()

    expect(items).toHaveLength(2)
    expect(items[0].label).toBe('deploy@prod-db')
    expect(items[0].detail).toBe('10.0.0.1')
    expect(items[0].system).toBe(true)
    expect(items[1].label).toBe('dev-box')
    expect(items[1].detail).toBe('dev.local')
  })

  it('suppresses aliases covered by saved profiles (same host)', async () => {
    const profileClient = {
      listProfiles: vi.fn().mockResolvedValue([
        {
          id: 'ssh:custom:prod:uuid',
          type: 'ssh' as const,
          name: 'Production DB',
          options: { host: 'prod-db', port: 22, user: 'deploy' },
        },
      ]),
      listSSHAliases: vi.fn().mockResolvedValue({
        aliases: [
          { alias: 'prod-db', hostName: '10.0.0.1' },
          { alias: 'dev-box', hostName: 'dev.local' },
        ],
        unavailable: null,
      }),
    }
    const newPaneByHost = vi.fn()
    const provider = new SSHAliasQuickConnectProvider(profileClient as never, newPaneByHost)

    const items = await provider.getItems()

    // prod-db is suppressed because a saved profile targets it
    expect(items).toHaveLength(1)
    expect(items[0].label).toBe('dev-box')
  })

  it('suppresses aliases covered by differently-named profiles', async () => {
    const profileClient = {
      listProfiles: vi.fn().mockResolvedValue([
        {
          id: 'ssh:custom:my-db:uuid',
          type: 'ssh' as const,
          name: 'My Database',
          options: { host: 'prod-db', port: 22, user: 'admin' },
        },
      ]),
      listSSHAliases: vi.fn().mockResolvedValue({
        aliases: [{ alias: 'prod-db', hostName: '10.0.0.1' }],
        unavailable: null,
      }),
    }
    const newPaneByHost = vi.fn()
    const provider = new SSHAliasQuickConnectProvider(profileClient as never, newPaneByHost)

    const items = await provider.getItems()

    // Profile named "My Database" targeting "prod-db" suppresses the alias
    expect(items).toHaveLength(0)
  })

  it('calls newPaneByHost with correct args on run()', async () => {
    const profileClient = {
      listProfiles: vi.fn().mockResolvedValue([]),
      listSSHAliases: vi.fn().mockResolvedValue({
        aliases: [{ alias: 'myserver', hostName: '10.0.0.1', user: 'admin' }],
        unavailable: null,
      }),
    }
    const newPaneByHost = vi.fn()
    const provider = new SSHAliasQuickConnectProvider(profileClient as never, newPaneByHost)

    const items = await provider.getItems()
    items[0].run()

    expect(newPaneByHost).toHaveBeenCalledWith('myserver', 'admin', undefined)
  })

  it('handles degraded resolver with unavailable', async () => {
    const profileClient = {
      listProfiles: vi.fn(),
      listSSHAliases: vi.fn().mockResolvedValue({
        aliases: [],
        unavailable: { reason: 'no-ssh-binary', detail: 'ssh binary not found on PATH' },
      }),
    }
    const newPaneByHost = vi.fn()
    const provider = new SSHAliasQuickConnectProvider(profileClient as never, newPaneByHost)

    const items = await provider.getItems()

    expect(items).toHaveLength(1)
    expect(items[0].label).toContain('no-ssh-binary')
    expect(items[0].system).toBe(true)
    expect(newPaneByHost).not.toHaveBeenCalled()
  })

  it('handles empty aliases list', async () => {
    const profileClient = {
      listProfiles: vi.fn(),
      listSSHAliases: vi.fn().mockResolvedValue({
        aliases: [],
        unavailable: null,
      }),
    }
    const newPaneByHost = vi.fn()
    const provider = new SSHAliasQuickConnectProvider(profileClient as never, newPaneByHost)

    const items = await provider.getItems()

    expect(items).toHaveLength(0)
  })

  it('marks items with system flag', async () => {
    const profileClient = {
      listProfiles: vi.fn().mockResolvedValue([]),
      listSSHAliases: vi.fn().mockResolvedValue({
        aliases: [{ alias: 'myserver', hostName: '10.0.0.1' }],
        unavailable: null,
      }),
    }
    const newPaneByHost = vi.fn()
    const provider = new SSHAliasQuickConnectProvider(profileClient as never, newPaneByHost)

    const items = await provider.getItems()

    expect(items).toHaveLength(1)
    expect(items[0].system).toBe(true)
    expect(items[0].id).toBe('__ssh_alias:myserver')
  })

  it('calls newPaneByHost with port when alias has one', async () => {
    const profileClient = {
      listProfiles: vi.fn().mockResolvedValue([]),
      listSSHAliases: vi.fn().mockResolvedValue({
        aliases: [{ alias: 'db-prod', hostName: '10.0.0.5', user: 'deploy', port: 2222 }],
        unavailable: null,
      }),
    }
    const newPaneByHost = vi.fn()
    const provider = new SSHAliasQuickConnectProvider(profileClient as never, newPaneByHost)

    const items = await provider.getItems()
    items[0].run()

    expect(newPaneByHost).toHaveBeenCalledWith('db-prod', 'deploy', 2222)
  })

  it('surfaces alias without user or port when config sets none', async () => {
    const profileClient = {
      listProfiles: vi.fn().mockResolvedValue([]),
      listSSHAliases: vi.fn().mockResolvedValue({
        aliases: [{ alias: 'bare-host', hostName: '10.0.0.1' }],
        unavailable: null,
      }),
    }
    const newPaneByHost = vi.fn()
    const provider = new SSHAliasQuickConnectProvider(profileClient as never, newPaneByHost)

    const items = await provider.getItems()
    expect(items[0].label).toBe('bare-host')
    expect(items[0].detail).toBe('10.0.0.1')
    items[0].run()
    expect(newPaneByHost).toHaveBeenCalledWith('bare-host', undefined, undefined)
  })

  it('suppresses alias when a saved profile matches by alias (not hostName)', async () => {
    const profileClient = {
      // Profile saved with host='myserver' (the alias, NOT the resolved hostName)
      listProfiles: vi
        .fn()
        .mockResolvedValue([
          { id: 'ssh:myserver:1', type: 'ssh', name: 'My Server', options: { host: 'myserver' } },
        ]),
      listSSHAliases: vi.fn().mockResolvedValue({
        aliases: [
          { alias: 'myserver', hostName: '10.0.0.1', user: 'admin' },
          { alias: 'other', hostName: '10.0.0.2' },
        ],
        unavailable: null,
      }),
    }
    const newPaneByHost = vi.fn()
    const provider = new SSHAliasQuickConnectProvider(profileClient as never, newPaneByHost)

    const items = await provider.getItems()
    // 'myserver' should be suppressed because its alias matches a saved profile's host
    expect(items).toHaveLength(1)
    expect(items[0].id).toBe('__ssh_alias:other')
  })

  it('preserves alias when saved profile host differs from alias', async () => {
    const profileClient = {
      // Profile saved with host='10.0.0.1' (resolved hostName, NOT the alias)
      listProfiles: vi
        .fn()
        .mockResolvedValue([
          { id: 'ssh:server:1', type: 'ssh', name: 'Server', options: { host: '10.0.0.1' } },
        ]),
      listSSHAliases: vi.fn().mockResolvedValue({
        aliases: [{ alias: 'myserver', hostName: '10.0.0.1', user: 'admin' }],
        unavailable: null,
      }),
    }
    const newPaneByHost = vi.fn()
    const provider = new SSHAliasQuickConnectProvider(profileClient as never, newPaneByHost)

    const items = await provider.getItems()
    // 'myserver' should NOT be suppressed — saved profile stores hostName, not alias
    expect(items).toHaveLength(1)
    expect(items[0].id).toBe('__ssh_alias:myserver')
  })

  it('shows alias without user/port when config provided none', async () => {
    const profileClient = {
      listProfiles: vi.fn().mockResolvedValue([]),
      listSSHAliases: vi.fn().mockResolvedValue({
        aliases: [{ alias: 'minimal', hostName: '10.0.0.1' }],
        unavailable: null,
      }),
    }
    const newPaneByHost = vi.fn()
    const provider = new SSHAliasQuickConnectProvider(profileClient as never, newPaneByHost)

    const items = await provider.getItems()
    expect(items).toHaveLength(1)
    // Label uses alias directly, no user or port decoration
    expect(items[0].label).toBe('minimal')
    expect(items[0].detail).toBe('10.0.0.1')
  })
})
/* ── Free-form (ad-hoc) connect provider ────────────────────────────── */

describe('AdHocQuickConnectProvider', () => {
  it('offers a Connect item for a bare host', () => {
    const newPaneByHost = vi.fn()
    const provider = new AdHocQuickConnectProvider(newPaneByHost)

    const items = provider.getQueryItems('example.com')

    expect(items).toHaveLength(1)
    expect(items[0].id).toBe('__ad_hoc_connect__')
    expect(items[0].label).toBe('Connect to example.com')
    expect(items[0].system).toBe(true)
    expect(items[0].badge).toBe('ad-hoc')

    items[0].run()
    expect(newPaneByHost).toHaveBeenCalledWith('example.com', undefined, 22)
  })

  it('parses user@host:port into host, user and port', () => {
    const newPaneByHost = vi.fn()
    const provider = new AdHocQuickConnectProvider(newPaneByHost)

    const items = provider.getQueryItems('deploy@example.com:2222')

    expect(items).toHaveLength(1)
    expect(items[0].label).toBe('Connect to deploy@example.com')
    expect(items[0].detail).toBe('port 2222')

    items[0].run()
    expect(newPaneByHost).toHaveBeenCalledWith('example.com', 'deploy', 2222)
  })

  it('accepts the ssh:// scheme', () => {
    const newPaneByHost = vi.fn()
    const provider = new AdHocQuickConnectProvider(newPaneByHost)

    const items = provider.getQueryItems('ssh://deploy@10.0.0.1:2222')
    items[0].run()
    expect(newPaneByHost).toHaveBeenCalledWith('10.0.0.1', 'deploy', 2222)
  })

  it('accepts a bracketed IPv6 literal', () => {
    const newPaneByHost = vi.fn()
    const provider = new AdHocQuickConnectProvider(newPaneByHost)

    const items = provider.getQueryItems('[::1]:2222')
    items[0].run()
    expect(newPaneByHost).toHaveBeenCalledWith('::1', undefined, 2222)
  })

  it('trims surrounding whitespace', () => {
    const provider = new AdHocQuickConnectProvider(vi.fn())
    expect(provider.getQueryItems('  example.com  ')).toHaveLength(1)
  })

  it('offers nothing for an empty query', () => {
    const provider = new AdHocQuickConnectProvider(vi.fn())
    expect(provider.getQueryItems('')).toHaveLength(0)
    expect(provider.getQueryItems('   ')).toHaveLength(0)
  })

  it('offers nothing for a malformed string with no host', () => {
    const provider = new AdHocQuickConnectProvider(vi.fn())
    expect(provider.getQueryItems('user@')).toHaveLength(0)
    expect(provider.getQueryItems(':2222')).toHaveLength(0)
    expect(provider.getQueryItems('ssh://')).toHaveLength(0)
  })

  it('hides the default port from the detail line', () => {
    const provider = new AdHocQuickConnectProvider(vi.fn())
    expect(provider.getQueryItems('example.com:22')[0].detail).toBeUndefined()
  })

  it('contributes no static items', () => {
    const provider = new AdHocQuickConnectProvider(vi.fn())
    expect(provider.getItems()).toHaveLength(0)
  })
})

/* ── Free-form connect entry in the picker ──────────────────────────── */

describe('free-form connect entry', () => {
  let container: HTMLDivElement

  beforeEach(() => {
    container = document.createElement('div')
    document.body.append(container)
  })

  afterEach(() => {
    container.remove()
  })

  /** Wait for microtasks (createEffect's async body) to settle. */
  async function waitForItems(): Promise<void> {
    return new Promise((resolve) => setTimeout(resolve, 0))
  }

  function typeQuery(input: HTMLInputElement, value: string): void {
    input.value = value
    input.dispatchEvent(new Event('input', { bubbles: true }))
  }

  function makePicker(newPaneByHost: Mock): QuickConnectController {
    const ctrl = new QuickConnectController()
    afterEach(() => ctrl.destroy())
    const providers: QuickConnectProvider[] = [
      new ActionsQuickConnectProvider(vi.fn(), vi.fn()),
      new SSHQuickConnectProvider(
        { listProfiles: vi.fn().mockResolvedValue([]) } as never,
        vi.fn(),
      ),
      new AdHocQuickConnectProvider(newPaneByHost),
    ]
    ctrl.mount(container, providers)
    ctrl.show()
    return ctrl
  }

  it('offers Connect for a query matching nothing and connects via newPaneByHost', async () => {
    const newPaneByHost = vi.fn()
    makePicker(newPaneByHost)
    await waitForItems()

    const input = container.querySelector<HTMLInputElement>('.quick-connect__search input')
    expect(input).toBeTruthy()
    typeQuery(input!, 'unknown-host')

    const items = container.querySelectorAll('.quick-connect__item')
    expect(items).toHaveLength(1)
    expect(items[0].textContent).toContain('Connect to unknown-host')

    // Reachable by keyboard alone: the row is selected and Enter activates it.
    input!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))

    expect(newPaneByHost).toHaveBeenCalledWith('unknown-host', undefined, 22)
    const dialog = container.querySelector<HTMLDialogElement>('dialog.nocx-dialog')
    expect(dialog?.open).toBe(false)

    // Nothing was persisted: the ad-hoc path only ever called newPaneByHost —
    // the picker's profile client has no write method on this path at all.
    expect(newPaneByHost).toHaveBeenCalledOnce()
  })

  it('ranks a matching saved profile above the ad-hoc entry — suppresses it', async () => {
    const ctrl = new QuickConnectController()
    afterEach(() => ctrl.destroy())
    const providers: QuickConnectProvider[] = [
      new ActionsQuickConnectProvider(vi.fn(), vi.fn()),
      new SSHQuickConnectProvider(
        {
          listProfiles: vi.fn().mockResolvedValue([
            {
              id: 'ssh:custom:prod:uuid',
              type: 'ssh' as const,
              name: 'Production DB',
              options: { host: 'example.com', port: 22, user: 'deploy' },
            },
          ]),
        } as never,
        vi.fn(),
      ),
      new AdHocQuickConnectProvider(vi.fn()),
    ]
    ctrl.mount(container, providers)
    ctrl.show()
    await waitForItems()

    const input = container.querySelector<HTMLInputElement>('.quick-connect__search input')
    expect(input).toBeTruthy()
    typeQuery(input!, 'example.com')

    const items = container.querySelectorAll('.quick-connect__item')
    expect(items).toHaveLength(1)
    expect(items[0].textContent).toContain('deploy@example.com')
    expect(items[0].textContent).not.toContain('Connect to')
  })

  it('ranks a matching alias above the ad-hoc entry — suppresses it', async () => {
    const ctrl = new QuickConnectController()
    afterEach(() => ctrl.destroy())
    const providers: QuickConnectProvider[] = [
      new ActionsQuickConnectProvider(vi.fn(), vi.fn()),
      new SSHQuickConnectProvider(
        { listProfiles: vi.fn().mockResolvedValue([]) } as never,
        vi.fn(),
      ),
      new SSHAliasQuickConnectProvider(
        {
          listProfiles: vi.fn().mockResolvedValue([]),
          listSSHAliases: vi.fn().mockResolvedValue({
            aliases: [{ alias: 'example.com', hostName: '10.0.0.1' }],
            unavailable: null,
          }),
        } as never,
        vi.fn(),
      ),
      new AdHocQuickConnectProvider(vi.fn()),
    ]
    ctrl.mount(container, providers)
    ctrl.show()
    await waitForItems()

    const input = container.querySelector<HTMLInputElement>('.quick-connect__search input')
    expect(input).toBeTruthy()
    typeQuery(input!, 'example.com')

    const items = container.querySelectorAll('.quick-connect__item')
    expect(items).toHaveLength(1)
    expect(items[0].textContent).toContain('example.com')
    expect(items[0].textContent).toContain('alias')
    expect(items[0].textContent).not.toContain('Connect to')
  })

  it('reports a malformed string instead of connecting', async () => {
    const newPaneByHost = vi.fn()
    makePicker(newPaneByHost)
    await waitForItems()

    const input = container.querySelector<HTMLInputElement>('.quick-connect__search input')
    expect(input).toBeTruthy()
    typeQuery(input!, 'user@')

    expect(container.querySelectorAll('.quick-connect__item')).toHaveLength(0)
    const empty = container.querySelector('.quick-connect__empty')
    expect(empty?.textContent).toContain('Could not parse')
    expect(empty?.textContent).toContain('user@host:port')

    input!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    expect(newPaneByHost).not.toHaveBeenCalled()
  })
})

/* ── Palette and drill-in (nocx-4t37) ───────────────────────────────── */

describe('palette and drill-in', () => {
  let container: HTMLDivElement

  beforeEach(() => {
    container = document.createElement('div')
    document.body.append(container)
  })

  afterEach(() => {
    container.remove()
  })

  async function waitForItems(): Promise<void> {
    return new Promise((resolve) => setTimeout(resolve, 0))
  }

  function typeQuery(input: HTMLInputElement, value: string): void {
    input.value = value
    input.dispatchEvent(new Event('input', { bubbles: true }))
  }

  /** The full picker: commands (with a drill), one saved profile, ad-hoc. */
  function makePicker(run = vi.fn()): {
    ctrl: QuickConnectController
    run: Mock
    newPaneByHost: Mock
  } {
    const newPaneByHost = vi.fn()
    const ctrl = new QuickConnectController()
    afterEach(() => ctrl.destroy())
    const drillCommand = {
      id: '__forward_port__',
      label: 'Forward a port',
      detail: 'Expose a port on this machine',
      steps: [
        {
          name: 'server',
          fetch: () =>
            Promise.resolve([{ id: 'srv-1', label: 'deploy@example.com', detail: 'Prod' }]),
        },
        {
          name: 'port',
          fetch: () =>
            Promise.resolve([
              { id: 'port-8080', label: ':8080', detail: '10.0.0.1:8080', value: '10.0.0.1:8080' },
            ]),
        },
      ],
      run,
    }
    const providers: QuickConnectProvider[] = [
      new ActionsQuickConnectProvider(vi.fn(), vi.fn(), drillCommand),
      new SSHQuickConnectProvider(
        {
          listProfiles: vi.fn().mockResolvedValue([
            {
              id: 'ssh:custom:prod:uuid',
              type: 'ssh' as const,
              name: 'Production DB',
              options: { host: 'example.com', port: 22, user: 'deploy' },
            },
          ]),
        } as never,
        vi.fn(),
      ),
      new AdHocQuickConnectProvider(newPaneByHost),
    ]
    ctrl.mount(container, providers)
    return { ctrl, run, newPaneByHost }
  }

  function labels(): string[] {
    return Array.from(container.querySelectorAll('.quick-connect__item')).map((el) =>
      (el.textContent ?? '').trim(),
    )
  }

  it('the palette (showPalette) mixes commands and hosts, each row typed on the right', async () => {
    const { ctrl } = makePicker()
    ctrl.showPalette()
    await waitForItems()

    const shown = labels()
    expect(shown.some((l) => l.includes('Local shell'))).toBe(true)
    expect(shown.some((l) => l.includes('Forward a port'))).toBe(true)
    expect(shown.some((l) => l.includes('deploy@example.com'))).toBe(true)

    // The type badges: Command on commands, Host on hosts.
    const kinds = Array.from(container.querySelectorAll('.quick-connect__item-kind')).map((el) =>
      (el.textContent ?? '').trim(),
    )
    expect(kinds.some((k) => k === 'Command')).toBe(true)
    expect(kinds.some((k) => k === 'Host')).toBe(true)
  })

  it('the caret (show) opens the plain server list: no commands, no type badges', async () => {
    const { ctrl } = makePicker()
    ctrl.show()
    await waitForItems()

    const shown = labels()
    expect(shown.some((l) => l.includes('deploy@example.com'))).toBe(true)
    expect(shown.some((l) => l.includes('Local shell'))).toBe(false)
    expect(shown.some((l) => l.includes('Forward a port'))).toBe(false)
    expect(container.querySelectorAll('.quick-connect__item-kind')).toHaveLength(0)
  })

  it('ad-hoc connect still works inside the palette', async () => {
    const { ctrl, newPaneByHost } = makePicker()
    ctrl.showPalette()
    await waitForItems()

    const input = container.querySelector<HTMLInputElement>('.quick-connect__search input')
    expect(input).toBeTruthy()
    typeQuery(input!, 'unknown-host')

    const items = container.querySelectorAll('.quick-connect__item')
    expect(items).toHaveLength(1)
    expect(items[0].textContent).toContain('Connect to unknown-host')
    input!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    expect(newPaneByHost).toHaveBeenCalledWith('unknown-host', undefined, 22)
  })

  it('a command that needs a target drills in: server, then port, then runs', async () => {
    const { ctrl, run } = makePicker()
    ctrl.showPalette()
    await waitForItems()

    const input = container.querySelector<HTMLInputElement>('.quick-connect__search input')
    expect(input).toBeTruthy()
    typeQuery(input!, 'forward')
    input!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    await waitForItems()

    // Drill mode: the breadcrumb names the command, the list is the servers.
    const drill = container.querySelector('.quick-connect__drill')
    expect(drill?.textContent).toContain('Forward a port')
    expect(drill?.textContent).toContain('server')
    const serverRows = container.querySelectorAll('.quick-connect__item')
    expect(serverRows).toHaveLength(1)
    expect(serverRows[0].textContent).toContain('deploy@example.com')

    // Choose the server: the list becomes the ports, the trail gains it.
    input!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    await waitForItems()

    const drill2 = container.querySelector('.quick-connect__drill')
    expect(drill2?.textContent).toContain('deploy@example.com')
    expect(drill2?.textContent).toContain('port')
    const portRows = container.querySelectorAll('.quick-connect__item')
    expect(portRows).toHaveLength(1)
    expect(portRows[0].textContent).toContain(':8080')

    // Choose the port: the command runs with the completed selection.
    input!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    await waitForItems()

    expect(run).toHaveBeenCalledOnce()
    const selections = run.mock.calls[0][0] as readonly DrillSelection[]
    expect(selections.map((s) => s.stepName)).toEqual(['server', 'port'])
    expect(selections[0].item.id).toBe('srv-1')
    expect(selections[1].item.value).toBe('10.0.0.1:8080')

    const dialog = container.querySelector<HTMLDialogElement>('dialog.nocx-dialog')
    expect(dialog?.open).toBe(false)
  })

  it('Backspace on an empty filter walks the drill back one step at a time', async () => {
    const { ctrl, run } = makePicker()
    ctrl.showPalette()
    await waitForItems()

    const input = container.querySelector<HTMLInputElement>('.quick-connect__search input')
    expect(input).toBeTruthy()
    typeQuery(input!, 'forward')
    input!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    await waitForItems()
    input!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    await waitForItems()

    // Two steps in: the trail shows the chosen server.
    expect(container.querySelector('.quick-connect__drill')?.textContent).toContain(
      'deploy@example.com',
    )

    // Backspace walks back to the server step; the trail loses the choice.
    input!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Backspace', bubbles: true }))
    await waitForItems()
    expect(container.querySelector('.quick-connect__drill')?.textContent).not.toContain(
      'deploy@example.com',
    )

    // One more Backspace exits the drill back to the palette; the dialog
    // stays open.
    input!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Backspace', bubbles: true }))
    await waitForItems()
    expect(container.querySelector('.quick-connect__drill')).toBeNull()
    const dialog = container.querySelector<HTMLDialogElement>('dialog.nocx-dialog')
    expect(dialog?.open).toBe(true)
    expect(labels().some((l) => l.includes('Local shell'))).toBe(true)

    expect(run).not.toHaveBeenCalled()
  })

  it('Escape walks the drill back and only closes at the palette root', async () => {
    const { ctrl } = makePicker()
    ctrl.showPalette()
    await waitForItems()

    const input = container.querySelector<HTMLInputElement>('.quick-connect__search input')
    expect(input).toBeTruthy()
    typeQuery(input!, 'forward')
    input!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    await waitForItems()
    input!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    await waitForItems()

    // Escape at a drill step walks back — it does not close the dialog.
    input!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    await waitForItems()
    expect(container.querySelector('.quick-connect__drill')).toBeTruthy()
    let dialog = container.querySelector<HTMLDialogElement>('dialog.nocx-dialog')
    expect(dialog?.open).toBe(true)

    // Escape at the drill root exits the drill; the dialog stays open.
    input!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    await waitForItems()
    expect(container.querySelector('.quick-connect__drill')).toBeNull()
    dialog = container.querySelector<HTMLDialogElement>('dialog.nocx-dialog')
    expect(dialog?.open).toBe(true)
  })

  it('a degraded ssh -G resolver renders as a row in both presentations — never as no results', async () => {
    const ctrl = new QuickConnectController()
    afterEach(() => ctrl.destroy())
    const providers: QuickConnectProvider[] = [
      new ActionsQuickConnectProvider(vi.fn(), vi.fn()),
      new SSHAliasQuickConnectProvider(
        {
          listProfiles: vi.fn().mockResolvedValue([]),
          listSSHAliases: vi.fn().mockResolvedValue({
            aliases: [],
            unavailable: { reason: 'no-ssh-binary', detail: 'ssh binary not found on PATH' },
          }),
        } as never,
        vi.fn(),
      ),
    ]
    ctrl.mount(container, providers)

    ctrl.show()
    await waitForItems()
    expect(labels().some((l) => l.includes('SSH config: no-ssh-binary'))).toBe(true)

    ctrl.showPalette()
    await waitForItems()
    expect(labels().some((l) => l.includes('SSH config: no-ssh-binary'))).toBe(true)
    expect(labels().some((l) => l.includes('Local shell'))).toBe(true)
  })
})

/* ── The caret admits a way to create a connection (nocx-d4us) ──────── */

describe('the caret offers "New connection" (nocx-d4us)', () => {
  let container: HTMLDivElement

  beforeEach(() => {
    container = document.createElement('div')
    document.body.append(container)
  })

  afterEach(() => {
    container.remove()
  })

  async function waitForItems(): Promise<void> {
    return new Promise((resolve) => setTimeout(resolve, 0))
  }

  /** The full picker, as the app wires it (main.tsx): the actions provider
   *  FIRST (its "New connection" row would otherwise jump ahead of every
   *  saved host), one saved profile, the drill command, ad-hoc. */
  function makeCaret(run = vi.fn()) {
    const newConnection = vi.fn()
    const newSSHPane = vi.fn()
    const ctrl = new QuickConnectController()
    afterEach(() => ctrl.destroy())
    const drillCommand = {
      id: '__forward_port__',
      label: 'Forward a port',
      detail: 'Expose a port on this machine',
      steps: [
        {
          name: 'server',
          fetch: () =>
            Promise.resolve([{ id: 'srv-1', label: 'deploy@example.com', detail: 'Prod' }]),
        },
        {
          name: 'port',
          fetch: () =>
            Promise.resolve([
              { id: 'port-8080', label: ':8080', detail: '10.0.0.1:8080', value: '10.0.0.1:8080' },
            ]),
        },
      ],
      run,
    }
    const providers: QuickConnectProvider[] = [
      new ActionsQuickConnectProvider(vi.fn(), newConnection, drillCommand),
      new SSHQuickConnectProvider(
        {
          listProfiles: vi.fn().mockResolvedValue([
            {
              id: 'ssh:custom:prod:uuid',
              type: 'ssh' as const,
              name: 'Production DB',
              options: { host: 'example.com', port: 22, user: 'deploy' },
            },
          ]),
        } as never,
        newSSHPane,
      ),
      new AdHocQuickConnectProvider(vi.fn()),
    ]
    ctrl.mount(container, providers)
    return { ctrl, newConnection, newSSHPane }
  }

  function labels(): string[] {
    return Array.from(container.querySelectorAll('.quick-connect__item')).map((el) =>
      (el.textContent ?? '').trim(),
    )
  }

  it('lists a row for creating a connection, after the saved hosts, and activating it reaches startNewConnection()', async () => {
    const { ctrl, newConnection } = makeCaret()
    ctrl.show()
    await waitForItems()

    const shown = labels()
    expect(shown.some((l) => l.includes('New connection'))).toBe(true)
    expect(shown.some((l) => l.includes('Define an SSH connection'))).toBe(true)

    // The saved host is still the first row: the create affordance is added
    // after the hosts, not in front of them.
    expect(shown[0]).toContain('deploy@example.com')

    const row = [...container.querySelectorAll<HTMLElement>('.quick-connect__item')].find((el) =>
      (el.textContent ?? '').includes('New connection'),
    )
    expect(row).toBeTruthy()
    row?.click()

    expect(newConnection).toHaveBeenCalledOnce()
    const dialog = container.querySelector<HTMLDialogElement>('dialog.nocx-dialog')
    expect(dialog?.open).toBe(false)
  })

  it('keeps "Forward a port" out of the caret — admitted by flag, not by kind', async () => {
    const { ctrl } = makeCaret()
    ctrl.show()
    await waitForItems()

    const shown = labels()
    expect(shown.some((l) => l.includes('Forward a port'))).toBe(false)
    expect(shown.some((l) => l.includes('Local shell'))).toBe(false)
  })

  it('Enter on an empty query in the caret still activates the first saved host', async () => {
    const { ctrl, newSSHPane, newConnection } = makeCaret()
    ctrl.show()
    await waitForItems()

    const input = container.querySelector<HTMLElement>('.quick-connect__search input')
    expect(input).toBeTruthy()
    input!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))

    expect(newSSHPane).toHaveBeenCalledWith('ssh:custom:prod:uuid', 'example.com', 'deploy')
    expect(newConnection).not.toHaveBeenCalled()
  })

  it('the palette still shows both command rows — New connection and Forward a port', async () => {
    const { ctrl } = makeCaret()
    ctrl.showPalette()
    await waitForItems()

    const shown = labels()
    expect(shown.some((l) => l.includes('New connection'))).toBe(true)
    expect(shown.some((l) => l.includes('Forward a port'))).toBe(true)
  })

  it('the secrets variant still admits no command row — the flagged one included', async () => {
    const ctrl = new QuickConnectController()
    afterEach(() => ctrl.destroy())
    ctrl.mount(container, [
      new ActionsQuickConnectProvider(vi.fn(), vi.fn()),
      new SecretsQuickConnectProvider({
        status: () => Promise.resolve({ state: 'unsealed' as const }),
        inventory: () => Promise.resolve({ entries: [] }),
        insert: vi.fn(),
        create: vi.fn(),
        requestUnseal: vi.fn(),
        requestSetup: vi.fn(),
      }),
    ])
    ctrl.showSecrets()
    await waitForItems()

    const rows = [...container.querySelectorAll('[role="option"]')].map((el) =>
      (el.textContent ?? '').trim(),
    )
    expect(rows.some((r) => r.includes('New connection'))).toBe(false)
    expect(rows.some((r) => r.includes('Forward a port'))).toBe(false)
  })
})

describe('the secret picker (nocx-fk32)', () => {
  let container: HTMLDivElement

  beforeEach(() => {
    container = document.createElement('div')
    document.body.append(container)
  })
  afterEach(() => {
    container.remove()
  })

  const inventory = () =>
    Promise.resolve({
      entries: [
        { id: 'secrow:1', name: 'pi@192.168.0.93', kind: 'password' },
        { id: 'secrow:2', name: 'deploy key', kind: 'private-key' },
      ],
    })

  function mountSecrets(deps: Partial<SecretsProviderDeps> = {}) {
    const ctrl = new QuickConnectController()
    afterEach(() => ctrl.destroy())
    ctrl.mount(container, [
      new ActionsQuickConnectProvider(vi.fn(), vi.fn()),
      new SecretsQuickConnectProvider({
        status: () => Promise.resolve({ state: 'unsealed' as const }),
        inventory,
        insert: vi.fn(),
        create: vi.fn(),
        requestUnseal: vi.fn(),
        requestSetup: vi.fn(),
        ...deps,
      }),
    ])
    return ctrl
  }

  const settle = () => new Promise((r) => setTimeout(r, 0))

  const rowTexts = () =>
    [...container.querySelectorAll('[role="option"]')].map((el) => (el.textContent ?? '').trim())

  const typeFilter = async (text: string) => {
    const input = container.querySelector<HTMLInputElement>('.quick-connect__search input')
    if (!input) throw new Error('no filter field')
    input.value = text
    input.dispatchEvent(new Event('input', { bubbles: true }))
    await settle()
  }

  it('lists the vault names and hands the chosen NAME to the insert seam', async () => {
    const insert = vi.fn()
    const ctrl = mountSecrets({ insert })
    ctrl.showSecrets()
    await settle()

    const rows = [...container.querySelectorAll('[role="option"]')].map((el) =>
      (el.textContent ?? '').trim(),
    )
    expect(rows.some((r) => r.includes('pi@192.168.0.93'))).toBe(true)
    expect(rows.some((r) => r.includes('deploy key'))).toBe(true)
    // Commands are not secrets: the variant admits one kind set.
    expect(rows.some((r) => r.includes('Local shell'))).toBe(false)

    const target = [...container.querySelectorAll<HTMLElement>('[role="option"]')].find((el) =>
      (el.textContent ?? '').includes('pi@192.168.0.93'),
    )
    target?.click()
    // The picker never sees material — only the name the reference carries.
    expect(insert).toHaveBeenCalledWith('pi@192.168.0.93')
  })

  it('keeps secrets out of the palette — one Enter must not mean two things', async () => {
    const insert = vi.fn()
    const ctrl = mountSecrets({ insert })
    ctrl.showPalette()
    await settle()

    const rows = [...container.querySelectorAll('[role="option"]')].map((el) =>
      (el.textContent ?? '').trim(),
    )
    expect(rows.some((r) => r.includes('Local shell'))).toBe(true)
    expect(rows.some((r) => r.includes('pi@192.168.0.93'))).toBe(false)
  })

  it('keeps secrets out of the plain server list too', async () => {
    const ctrl = mountSecrets()
    ctrl.show()
    await settle()
    const rows = [...container.querySelectorAll('[role="option"]')].map((el) =>
      (el.textContent ?? '').trim(),
    )
    expect(rows.some((r) => r.includes('pi@192.168.0.93'))).toBe(false)
  })

  // The secret the vault does not hold yet is the one the user came to type
  // (nocx-fk32.1). Without this row the picker's answer to "it is not here"
  // is silence plus a Settings page they have to know about.
  it('offers to create the secret, as the last row, with what was typed', async () => {
    const create = vi.fn()
    const ctrl = mountSecrets({ create })
    ctrl.showSecrets()
    await settle()

    const initial = rowTexts()
    expect(initial[initial.length - 1]).toContain('Add a secret')

    await typeFilter('gitlab token')
    // Nothing matched, and the offer is still there — closing it at exactly
    // the keystroke that names a new secret takes the offer away as the user
    // reaches for it.
    const rows = rowTexts()
    expect(rows).toHaveLength(1)
    expect(rows[0]).toContain('gitlab token')

    const row = container.querySelector<HTMLElement>('[role="option"]')
    row?.click()
    expect(create).toHaveBeenCalledWith('gitlab token')
  })

  it('offers it on an empty vault, where the list has nothing else to say', async () => {
    const ctrl = mountSecrets({ inventory: () => Promise.resolve({ entries: [] }) })
    ctrl.showSecrets()
    await settle()

    expect(rowTexts()).toEqual([expect.stringContaining('Add a secret')])
  })

  it('never offers to create from the palette or the server list', async () => {
    const ctrl = mountSecrets()
    ctrl.showPalette()
    await settle()
    expect(rowTexts().some((r) => r.includes('Add a secret'))).toBe(false)

    ctrl.show()
    await settle()
    expect(rowTexts().some((r) => r.includes('Add a secret'))).toBe(false)
  })

  // "No secrets" and "there is no vault" are different facts, and an empty
  // list tells the user the first one (nocx-fk32.3).
  it('offers to set protection up when there is no vault yet', async () => {
    const requestSetup = vi.fn()
    const create = vi.fn()
    const ctrl = mountSecrets({
      status: () => Promise.resolve({ state: 'uninitialized' as const }),
      inventory: () => Promise.reject(new Error('vault-uninitialized')),
      requestSetup,
      create,
    })
    ctrl.showSecrets()
    await settle()

    expect(rowTexts()).toEqual([expect.stringContaining('Set up the vault')])
    // Nothing to add a secret TO yet: the offer to create would be a second
    // answer to the same question, and the wrong one.
    expect(rowTexts().some((r) => r.includes('Add a secret'))).toBe(false)

    container.querySelector<HTMLElement>('[role="option"]')?.click()
    expect(requestSetup).toHaveBeenCalledTimes(1)
    expect(create).not.toHaveBeenCalled()
  })

  // The dispatcher raises the unlock dialog for a sealed vault and retries,
  // so a successful unlock fills the list. This is the path where it did
  // not: the prompt was dismissed, and the vault is locked, not empty.
  it('names the condition when the inventory could not be read', async () => {
    const requestUnseal = vi.fn()
    const ctrl = mountSecrets({
      status: () => Promise.resolve({ state: 'sealed' as const }),
      inventory: () => Promise.reject(new Error('cancelled')),
      requestUnseal,
    })
    ctrl.showSecrets()
    await settle()

    expect(rowTexts()).toEqual([expect.stringContaining('Unlock the vault')])
    expect(rowTexts().some((r) => r.includes('Add a secret'))).toBe(false)

    container.querySelector<HTMLElement>('[role="option"]')?.click()
    expect(requestUnseal).toHaveBeenCalledTimes(1)
  })

  it('lists the secrets after an unlock the dispatcher retried for it', async () => {
    // A sealed vault whose inventory call resolves is exactly what the
    // dispatcher's unlock-and-retry seam produces; no offer row belongs in
    // front of a list that arrived.
    const ctrl = mountSecrets({ status: () => Promise.resolve({ state: 'sealed' as const }) })
    ctrl.showSecrets()
    await settle()

    expect(rowTexts().some((r) => r.includes('pi@192.168.0.93'))).toBe(true)
    expect(rowTexts().some((r) => r.includes('Unlock the vault'))).toBe(false)
  })
})

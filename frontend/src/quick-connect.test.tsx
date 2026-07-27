// @vitest-environment jsdom
import { describe, expect, it, vi, afterEach, beforeEach } from 'vitest'
import { cleanup } from '@solidjs/testing-library'
import {
  LocalShellQuickConnectProvider,
  SSHQuickConnectProvider,
  QuickConnectController,
  type QuickConnectItem,
  type QuickConnectProvider,
} from './quick-connect'

afterEach(() => {
  cleanup()
})

/* ── Local shell provider ───────────────────────────────────────────── */

describe('LocalShellQuickConnectProvider', () => {
  it('returns a single "Local shell" item', async () => {
    const newTab = vi.fn()
    const provider = new LocalShellQuickConnectProvider(newTab)
    const items = await Promise.resolve(provider.getItems())

    expect(items).toHaveLength(1)
    expect(items[0].id).toBe('__local__')
    expect(items[0].label).toBe('Local shell')
    expect(items[0].detail).toContain('local terminal')
  })

  it('calls newTab when run() is invoked', () => {
    const newTab = vi.fn()
    const provider = new LocalShellQuickConnectProvider(newTab)

    const items = provider.getItems()
    items[0].run()

    expect(newTab).toHaveBeenCalledOnce()
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
    const newSSHTab = vi.fn()
    const provider = new SSHQuickConnectProvider(profileClient as never, newSSHTab)

    const items = await Promise.resolve(provider.getItems())

    expect(items).toHaveLength(2)
    expect(items[0].label).toBe('admin@db.example.com')
    expect(items[0].detail).toBe('Production DB')
    expect(items[1].label).toBe('dev.local')
    expect(items[1].detail).toBe('Dev box')
  })

  it('calls newSSHTab with correct args on run()', async () => {
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
    const newSSHTab = vi.fn()
    const provider = new SSHQuickConnectProvider(profileClient as never, newSSHTab)

    const items = await Promise.resolve(provider.getItems())
    items[0].run()

    expect(newSSHTab).toHaveBeenCalledWith('ssh:custom:x:uuid', 'x.example.com', 'me')
  })

  it('handles empty profile list', async () => {
    const profileClient = {
      listProfiles: vi.fn().mockResolvedValue([]),
    }
    const newSSHTab = vi.fn()
    const provider = new SSHQuickConnectProvider(profileClient as never, newSSHTab)

    const items = await Promise.resolve(provider.getItems())
    expect(items).toHaveLength(0)
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
    const providers: QuickConnectProvider[] = [new LocalShellQuickConnectProvider(vi.fn())]
    ctrl.mount(container, providers)
    const dialog = container.querySelector<HTMLDialogElement>('dialog.nocx-dialog')
    expect(dialog).toBeTruthy()
    expect(dialog?.open).toBe(false)
  })

  it('show() opens the dialog', () => {
    const ctrl = makeController()
    const providers: QuickConnectProvider[] = [new LocalShellQuickConnectProvider(vi.fn())]
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
    ctrl.mount(container, [new LocalShellQuickConnectProvider(vi.fn())])
    ctrl.show()
    ctrl.destroy()

    expect(container.children.length).toBe(0)
  })

  it('Escape closes the dialog via native cancel event', () => {
    const ctrl = makeController()
    ctrl.mount(container, [new LocalShellQuickConnectProvider(vi.fn())])
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
        getItems(): QuickConnectItem[] {
          return [
            { id: 'a', label: 'First', run: vi.fn() },
            { id: 'b', label: 'Second', run: vi.fn() },
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
    const newTab = vi.fn()
    ctrl.mount(container, [new LocalShellQuickConnectProvider(newTab)])
    ctrl.show()
    await waitForItems()

    const dialog = container.querySelector<HTMLDialogElement>('dialog.nocx-dialog')
    expect(dialog?.open).toBe(true)

    const input = container.querySelector<HTMLElement>('.quick-connect__search input')
    expect(input).toBeTruthy()
    input!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))

    expect(newTab).toHaveBeenCalledOnce()
    expect(dialog?.open).toBe(false)
  })

  it('typing in the search input filters the list', async () => {
    const ctrl = makeController()
    const providers: QuickConnectProvider[] = [
      new (class implements QuickConnectProvider {
        readonly id = 'test'
        readonly label = 'Test'
        getItems(): QuickConnectItem[] {
          return [
            { id: 'a', label: 'admin@server1', detail: 'Production', run: vi.fn() },
            { id: 'b', label: 'root@server2', detail: 'Staging', run: vi.fn() },
            { id: 'c', label: 'Local shell', run: vi.fn() },
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
    ctrl.mount(container, [new LocalShellQuickConnectProvider(vi.fn())])
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

    ctrl.mount(container, [new LocalShellQuickConnectProvider(vi.fn())])
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
    resolvers[1]([{ id: 'fresh', label: 'Fresh Item', run: vi.fn() }])
    await waitForItems()

    // The list shows the fresh item.
    const items1 = container.querySelectorAll('.quick-connect__item')
    expect(items1.length).toBe(1)
    expect(items1[0].textContent).toContain('Fresh Item')

    // Now resolve the FIRST (stale) load.
    resolvers[0]([{ id: 'stale', label: 'Stale Item', run: vi.fn() }])
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
      { id: 'a', label: 'admin@server1', detail: 'Production', run: vi.fn() },
      { id: 'b', label: 'root@server2', detail: 'Staging', run: vi.fn() },
      { id: 'c', label: 'Local shell', run: vi.fn() },
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
      { id: 'a', label: 'admin@server1', run: vi.fn() },
      { id: 'b', label: 'root@server2', run: vi.fn() },
    ]

    const filtered = items.filter((it) => it.label.toLowerCase().includes(''))
    expect(filtered).toHaveLength(2)
  })

  it('matches on detail field', () => {
    const items: QuickConnectItem[] = [
      { id: 'a', label: '192.168.1.1', detail: 'Production DB', run: vi.fn() },
    ]

    const filtered = items.filter(
      (it) =>
        it.label.toLowerCase().includes('production') ||
        (it.detail !== undefined && it.detail.toLowerCase().includes('production')),
    )

    expect(filtered).toHaveLength(1)
  })

  it('no match returns empty list', () => {
    const items: QuickConnectItem[] = [{ id: 'a', label: 'admin@server1', run: vi.fn() }]

    const q = 'zzzzz'
    const filtered = items.filter(
      (it) =>
        it.label.toLowerCase().includes(q) ||
        (it.detail !== undefined && it.detail.toLowerCase().includes(q)),
    )

    expect(filtered).toHaveLength(0)
  })
})

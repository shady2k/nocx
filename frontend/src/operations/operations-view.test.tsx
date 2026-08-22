// @vitest-environment jsdom
//
// What a person can do that they could not before (AGENTS.md rule 1): see a
// running transfer, and stop it, from anywhere in the app — with the panel
// showing something else, and with the sidebar collapsed altogether.
//
// So this mounts the real activity bar with the real operations view over a
// real upload store, and drives the store the way the wire does. Nothing
// here asserts what the components render on their own; each of those has
// its own test. What is asserted is the thing none of them can report: that
// the ICON goes on saying what is happening when the panel is not showing
// the list, and that the LIST cancels for real when it is.
import { cleanup, fireEvent } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { beginTransfer, fakeClock, fakeUploadServices } from '../files/upload-fixtures'
import { uploadOperations } from '../files/upload-operations'
import { createUploadStore, type UploadStore } from '../files/upload-store'
import { mountSidebar, type SidebarHandle, type SidebarViewDescriptor } from '../sidebar'
import { ArrowUpIcon, SettingsIcon } from '../ui/icons'
import { createOperationsModel } from './operations'
import {
  createOperationsView,
  OPERATIONS_VIEW_ID,
  type OperationsViewDeps,
} from './operations-view'

const OTHER_VIEWS: readonly SidebarViewDescriptor[] = [
  { id: 'files', title: 'Files', icon: ArrowUpIcon, view: () => <div>files</div>, order: 0 },
  { id: 'ports', title: 'Ports', icon: ArrowUpIcon, view: () => <div>ports</div>, order: 1 },
]

let handle: SidebarHandle | null = null

function mount(
  deps: OperationsViewDeps = {},
  storeClock: () => number = fakeClock().now,
): { store: UploadStore; services: ReturnType<typeof fakeUploadServices> } {
  const services = fakeUploadServices()
  const store = createUploadStore({ services, now: storeClock })
  const model = createOperationsModel([uploadOperations(store)])

  const bar = document.createElement('div')
  bar.id = 'activitybar'
  const panel = document.createElement('div')
  panel.id = 'sidebar'
  document.body.append(bar, panel)

  handle = mountSidebar(
    bar,
    panel,
    [...OTHER_VIEWS, createOperationsView(model, deps)],
    [{ id: 'settings', title: 'Settings', icon: SettingsIcon, onActivate: () => {} }],
  )
  return { store, services }
}

afterEach(() => {
  vi.useRealTimers()
  handle?.destroy()
  handle = null
  document.body.replaceChildren()
  cleanup()
})

const icon = () => document.querySelector<HTMLElement>(`button[data-view="${OPERATIONS_VIEW_ID}"]`)
const badge = () => document.querySelector<HTMLElement>(`[data-view-badge="${OPERATIONS_VIEW_ID}"]`)
const progress = () =>
  document.querySelector<HTMLElement>(`[data-view-progress="${OPERATIONS_VIEW_ID}"]`)
// THREE LISTS NOW, one per state (nocx-hbdw4.5, then nocx-hbdw4.6). A
// helper that looked at `[data-testid="ops-list"]` alone stopped seeing
// finished rows the moment they moved under their own heading — so these
// span all of them deliberately.
const LIST_SELECTOR =
  '[data-testid="ops-list"], [data-testid="ops-list-queued"], [data-testid="ops-list-finished"]'
const anyList = () => [...document.querySelectorAll<HTMLElement>(LIST_SELECTOR)]
const rows = () => [...document.querySelectorAll<HTMLElement>('.ui-operation-row')]

function openTheList(): void {
  icon()!.click()
}

function cancelButton(): HTMLElement {
  const el = [
    ...document.querySelectorAll<HTMLElement>(
      LIST_SELECTOR.split(', ')
        .map((s) => `${s} button`)
        .join(', '),
    ),
  ].find((b) =>
    // BY ITS ACCESSIBLE NAME, not its text. Cancel became an icon button so
    // it would stop taking a third of a rail-width row from the content
    // (nocx-hbdw4.5), and an icon button has no text content at all — a
    // helper matching on textContent silently found nothing and every test
    // through it failed as "no cancel in the operations list".
    (b.getAttribute('aria-label') ?? '').startsWith('Cancel'),
  )
  if (!el) throw new Error('no cancel in the operations list')
  return el
}

function startTransfer(store: UploadStore, id = 't1'): string {
  return beginTransfer(store, {
    transferId: id,
    name: 'big.iso',
    destDir: '/srv/data',
    machine: 'deploy@srv-01',
    size: 400,
  })
}

describe('the icon reports from anywhere; the list opens the panel', () => {
  it('is an ordinary view in the TOP zone, and the bottom zone holds only the gear', () => {
    // nocx-hbdw4.1: the popover and the second kind of bottom-zone entry are
    // gone, and the list opens the panel the way Files, Git and Ports do.
    mount()
    expect(
      document.querySelector(`.activity-bar-top button[data-view="${OPERATIONS_VIEW_ID}"]`),
    ).not.toBeNull()
    const bottom = document.querySelector('.activity-bar-bottom')
    expect([...(bottom?.querySelectorAll('button') ?? [])].map((b) => b.dataset.action)).toEqual([
      'settings',
    ])
  })

  it('is always in the bar, running or not — a fixed position is one a person learns', () => {
    mount()
    expect(icon()).not.toBeNull()
    // And nothing jumps in and out of the bar: the badge and the aggregate
    // are what appear, never the button itself.
    expect(badge()).toBeNull()
    expect(progress()).toBeNull()
  })

  it('goes on counting while ANOTHER view is on screen', () => {
    const { store } = mount()
    startTransfer(store)
    expect(badge()?.textContent).toBe('1')

    document.querySelector<HTMLElement>('button[data-view="ports"]')!.click()
    expect(document.querySelector('[data-testid="operations-panel"]')).toBeNull()
    expect(badge()?.textContent).toBe('1')
    expect(progress()).not.toBeNull()
  })

  it('goes on counting with the sidebar collapsed entirely', () => {
    // The case the Files panel could not answer.
    const { store } = mount()
    startTransfer(store)
    fireEvent.keyDown(document, { key: 'b', metaKey: true })
    expect(document.getElementById('sidebar')?.classList.contains('collapsed')).toBe(true)
    expect(icon()).not.toBeNull()
    expect(badge()?.textContent).toBe('1')
    expect(progress()).not.toBeNull()
  })

  it('opens the panel on the list from a collapsed sidebar, and cancels from there', () => {
    const { store, services } = mount()
    startTransfer(store)
    fireEvent.keyDown(document, { key: 'b', metaKey: true })
    expect(document.getElementById('sidebar')?.classList.contains('collapsed')).toBe(true)

    openTheList()
    expect(document.getElementById('sidebar')?.classList.contains('collapsed')).toBe(false)
    expect(rows()).toHaveLength(1)
    expect(rows()[0].getAttribute('data-phase')).toBe('running')

    cancelButton().click()
    expect(services.cancels).toEqual(['t1'])
    // The cancel goes to the WIRE and decides no phase locally: it races the
    // transfer's own completion every time, and uploadDone says which won.
    expect(rows()[0].getAttribute('data-phase')).toBe('running')
  })
})

describe('what the badge and the bar say', () => {
  it('counts the live operations and disappears at zero', () => {
    const { store, services } = mount()
    startTransfer(store, 'a')
    startTransfer(store, 'b')
    expect(badge()?.textContent).toBe('2')

    services.emitDone({ transferId: 'a', outcome: 'written', finalName: 'a', stranded: [] })
    expect(badge()?.textContent).toBe('1')
    services.emitDone({ transferId: 'b', outcome: 'written', finalName: 'b', stranded: [] })
    expect(badge()).toBeNull()
  })

  it('carries the count in the button’s name too, for somebody who cannot see the badge', () => {
    const { store } = mount()
    expect(icon()?.getAttribute('aria-label')).toBe('Operations')
    startTransfer(store)
    expect(icon()?.getAttribute('aria-label')).toContain('1 running')
  })

  it('shows a determinate aggregate while something runs, and nothing when nothing does', () => {
    const { store, services } = mount()
    startTransfer(store)
    services.emitProgress({ transferId: 't1', bytes: 100, total: 400 })
    expect(progress()?.querySelector('[role="progressbar"]')?.getAttribute('aria-valuenow')).toBe(
      '25',
    )
    // Never a spinner: a 20-minute upload must not put permanent motion in
    // somebody's peripheral vision.
    expect(progress()?.querySelector('.ui-spinner')).toBeNull()

    services.emitDone({ transferId: 't1', outcome: 'written', finalName: 'big.iso', stranded: [] })
    expect(progress()).toBeNull()
  })
})

describe('the list itself', () => {
  it('says so when there is nothing, rather than opening on an empty box', () => {
    mount()
    openTheList()
    expect(document.querySelector('.ui-empty-state')?.textContent).toContain('Nothing is running')
    expect(rows()).toHaveLength(0)
  })

  it('names the FILE, never the transfer id the row is addressed by', () => {
    // nocx-hbdw4.1 defect 2. The id is the row's identity and is deliberately
    // not on screen: a person recognises the thing they dropped.
    const { store } = mount()
    startTransfer(store, 'ffffffffffffffffffffffffffffffff')
    openTheList()
    expect(rows()[0].querySelector('.ui-operation-row__title')?.textContent).toBe('big.iso')
    expect(rows()[0].textContent).not.toContain('ffffffffffffffffffffffffffffffff')
  })

  it('shows the name the transfer actually landed under, once there is one', () => {
    const { store, services } = mount()
    startTransfer(store)
    services.emitDone({
      transferId: 't1',
      outcome: 'written',
      finalName: 'big (1).iso',
      stranded: [],
    })
    openTheList()
    expect(rows()[0].querySelector('.ui-operation-row__title')?.textContent).toBe('big (1).iso')
  })

  it('keeps a finished operation in the list after it has left the badge', () => {
    // Success does not shout, and does not vanish without trace either:
    // somebody who goes to look can see it really landed. It used to sit
    // above the Files tree until a person clicked its ×, which is a chore
    // the product invented for itself.
    const { store, services } = mount()
    startTransfer(store)
    services.emitDone({ transferId: 't1', outcome: 'written', finalName: 'big.iso', stranded: [] })
    expect(badge()).toBeNull()

    openTheList()
    expect(rows()).toHaveLength(1)
    expect(rows()[0].getAttribute('data-phase')).toBe('written')
    // And there is no × to press: nothing asks the person to tidy up.
    expect(
      anyList()
        .map((l) => l.textContent)
        .join(''),
    ).not.toContain('Dismiss')
  })

  it('marks a failure among the finished ones, and offers it no cancel', () => {
    const { store, services } = mount()
    startTransfer(store)
    services.emitDone({
      transferId: 't1',
      outcome: 'failed',
      finalName: '',
      error: 'permission denied',
      stranded: [],
    })
    openTheList()
    expect(rows()[0].getAttribute('data-phase')).toBe('failed')
    expect(rows()[0].textContent).toContain('Failed')
    expect(rows()[0].textContent).toContain('permission denied')
    expect(
      anyList()
        .map((l) => l.textContent)
        .join(''),
    ).not.toContain('Cancel')
  })

  it('puts the live ones above the finished ones', () => {
    const { store, services } = mount()
    startTransfer(store, 'done')
    services.emitDone({ transferId: 'done', outcome: 'written', finalName: 'done', stranded: [] })
    beginTransfer(store, {
      transferId: 'live',
      name: 'live.iso',
      destDir: '/srv',
      machine: 'deploy@srv-01',
      size: 10,
    })

    openTheList()
    expect(rows().map((r) => r.getAttribute('data-phase'))).toEqual(['running', 'written'])
  })

  it('follows the store while it is open — a row does not need reopening to move', () => {
    const { store, services } = mount()
    startTransfer(store)
    openTheList()
    expect(rows()[0].getAttribute('data-phase')).toBe('running')
    services.emitDone({ transferId: 't1', outcome: 'cancelled', finalName: '', stranded: [] })
    expect(rows()[0].getAttribute('data-phase')).toBe('cancelled')
  })

  it('KEEPS THE ROW’S OWN DOM NODE across a store change (nocx-hbdw4.1, defect 1)', () => {
    // The cancel defect, at the level a unit test can reach it. `For` matches
    // items by REFERENCE and every operations source mints fresh objects on
    // every read, so an unkeyed list rebuilt every row on every progress
    // frame — and a press that straddled one was lost, because the browser
    // fires `click` on the nearest common ancestor of mousedown and mouseup
    // and the button it started on no longer existed.
    //
    // A unit test cannot straddle a real press (jsdom's click is synthesised
    // on the element), so what it asserts is the property the press needs:
    // the node survives. e2e/ops-indicator.spec.ts is what watches a real
    // press survive a real store change.
    const { store, services } = mount()
    startTransfer(store)
    openTheList()
    const before = rows()[0]
    const buttonBefore = cancelButton()

    services.emitProgress({ transferId: 't1', bytes: 100, total: 400 })
    // A second transfer arriving moves the list itself, not just one row.
    beginTransfer(store, {
      transferId: 't2',
      name: 'other.iso',
      destDir: '/srv/data',
      machine: 'deploy@srv-01',
      size: 8,
    })
    services.emitProgress({ transferId: 't1', bytes: 200, total: 400 })

    expect(rows()).toHaveLength(2)
    expect(rows()[0]).toBe(before)
    expect(before.isConnected).toBe(true)
    expect(cancelButton()).toBe(buttonBefore)
    // And it is still the right operation's cancel.
    buttonBefore.click()
    expect(services.cancels).toEqual(['t1'])
  })
})

// ── Which machine, and it is not a rendering tweak (amendment) ───────────
//
// The list is GLOBAL: one list for every tab. A row saying `/home/dev` is
// unambiguous with one connection open and meaningless with three — and by
// the time the row is drawn there is no tab to ask, so the answer has to
// have been recorded when the transfer started.
describe('the list says which machine each operation is on', () => {
  it('distinguishes two transfers going to two different hosts', () => {
    // THE TEST THAT WOULD HAVE CAUGHT THIS. Two rows whose paths are the
    // same word and whose machines are not: without the machine they are
    // one row printed twice.
    const { store } = mount()
    beginTransfer(store, {
      transferId: 't1',
      name: 'a.zip',
      destDir: '/var/www',
      machine: 'deploy@web-01',
      size: 4,
    })
    beginTransfer(store, {
      transferId: 't2',
      name: 'b.zip',
      destDir: '/var/www',
      machine: 'deploy@web-02',
      size: 4,
    })

    openTheList()
    const where = [...document.querySelectorAll('.ui-operation-row__destination')].map(
      (e) => e.textContent,
    )
    expect(where).toEqual(['deploy@web-01 · /var/www', 'deploy@web-02 · /var/www'])
  })

  it('names the local machine too, rather than leaving that row blank', () => {
    const { store } = mount()
    beginTransfer(store, {
      transferId: 't1',
      name: 'a.zip',
      destDir: '/home/dev',
      machine: 'This machine',
      size: 4,
    })
    openTheList()
    expect(document.querySelector('.ui-operation-row__destination')?.textContent).toBe(
      'This machine · /home/dev',
    )
  })
})

// ── What a finished row is worth reading (nocx-hbdw4.4) ─────────────────
describe('a finished row in the real list', () => {
  it('carries its size, when it landed and how long it took', () => {
    const clock = fakeClock()
    // One clock for the store (which stamps the record) and for the view
    // (which reads the age against it), so the test states one time.
    const { store, services } = mount({ now: clock.now }, clock.now)
    beginTransfer(store, {
      transferId: 't1',
      name: 'big.iso',
      destDir: '/srv',
      machine: 'deploy@srv-01',
      size: 4_000_000,
    })
    clock.advance(14_000)
    services.emitDone({ transferId: 't1', outcome: 'written', finalName: 'big.iso', stranded: [] })
    clock.advance(5 * 60_000)

    openTheList()
    expect(document.querySelector('.ui-operation-row__summary')?.textContent).toBe(
      '4.0 MB · 5 min ago · took 14 s',
    )
  })
})

// ── Numbers a person can read (nocx-hbdw4.4) ────────────────────────────
//
// Smoothing is the store's (SPEED_SMOOTHING) and the repaint rate is the
// view's, and fixing either alone leaves the other: a steady number
// repainted thirty times a second is unreadable, and a calm repaint of a
// value that swings is a calm display of noise.
describe('the list repaints a few times a second, whatever the wire does', () => {
  /** Both clocks at once — the injected one the throttle reads, and the
   *  fake timer its release is scheduled on. */
  function ticking(): { deps: OperationsViewDeps; advance: (ms: number) => void } {
    vi.useFakeTimers()
    let t = 1000
    return {
      deps: { now: () => t },
      advance: (ms: number) => {
        t += ms
        vi.advanceTimersByTime(ms)
      },
    }
  }

  const detail = (): string =>
    document.querySelector('.ui-operation-row__progress')?.textContent ?? ''

  it('holds a byte count that moves inside the window, and lands it after', () => {
    const c = ticking()
    const { store, services } = mount(c.deps)
    startTransfer(store)
    openTheList()

    c.advance(250)
    services.emitProgress({ transferId: 't1', bytes: 100, total: 400 })
    expect(detail()).toBe('100 B of 400 B')

    // Three frames inside one window: the row does not flicker through them.
    c.advance(10)
    services.emitProgress({ transferId: 't1', bytes: 200, total: 400 })
    services.emitProgress({ transferId: 't1', bytes: 300, total: 400 })
    expect(detail()).toBe('100 B of 400 B')

    // And the newest of them lands when the window closes — not the oldest,
    // and not never.
    c.advance(250)
    expect(detail()).toBe('300 B of 400 B')
  })

  it('shows the outcome the moment it arrives, mid-window', () => {
    // THE DEFECT A THROTTLE INTRODUCES: a held last update is a row frozen
    // at 98% for the rest of the session.
    const c = ticking()
    const { store, services } = mount(c.deps)
    startTransfer(store)
    openTheList()

    c.advance(250)
    services.emitProgress({ transferId: 't1', bytes: 392, total: 400 })
    c.advance(10)
    services.emitProgress({ transferId: 't1', bytes: 399, total: 400 })
    expect(detail()).toBe('392 B of 400 B')

    services.emitDone({ transferId: 't1', outcome: 'written', finalName: 'big.iso', stranded: [] })
    expect(rows()[0].getAttribute('data-phase')).toBe('written')
    // NOT a "Done" badge any more: the list groups finished work under its
    // own heading, so a pill repeating that on every row said what the
    // heading says once, in the space the file name wanted (nocx-hbdw4.5).
    // What the outcome moves is which LIST the row is in — assert that, and
    // that nothing re-added the redundant word.
    expect(document.querySelector('[data-testid="ops-list-finished"]')?.contains(rows()[0])).toBe(
      true,
    )
    expect(rows()[0].textContent).not.toContain('Done')
    // No bar and no held progress line left behind it.
    expect(document.querySelector('[role="progressbar"]')).toBeNull()
  })
})

// ── The waiting half of a batch, on screen (nocx-hbdw4.6) ────────────────
describe('a batch with files still to be sent', () => {
  /** One running transfer and `n` behind it, the way the flow leaves them:
   *  the batch is registered first and the first file then starts. */
  function batchOf(store: UploadStore, n: number): string[] {
    const ids = store.enqueue(
      Array.from({ length: n }, (_, i) => ({
        name: `f${i}.iso`,
        destDir: '/srv/data',
        machine: 'deploy@srv-01',
        size: 400,
      })),
    )
    store.start(ids[0], 't1')
    return ids
  }

  const heading = (text: string): HTMLElement | undefined =>
    [...document.querySelectorAll<HTMLElement>('.ops-group__heading')].find((h) =>
      (h.textContent ?? '').startsWith(text),
    )

  it('gives the waiting files their own heading, with how many are coming', () => {
    // Not "In progress", which would be false about most of what is under
    // it: three of these four have not started, and the count beside that
    // heading is the very number a person is looking for.
    const { store } = mount()
    batchOf(store, 4)
    openTheList()
    expect(heading('In progress')?.textContent).toBe('In progress1')
    expect(heading('Queued')?.textContent).toBe('Queued3')
  })

  it('reads the batch chronologically: what is moving, then what is coming', () => {
    const { store } = mount()
    batchOf(store, 3)
    openTheList()
    const order = [...document.querySelectorAll<HTMLElement>('.ops-group__heading, .ops-list')].map(
      (el) => el.getAttribute('data-testid') ?? el.textContent,
    )
    expect(order).toEqual(['In progress1', 'ops-list', 'Queued2', 'ops-list-queued'])
  })

  it('draws no progress bar under a file that has not started', () => {
    // A bar at zero claims bytes are moving when none are, and it is the
    // row's loudest element — a panel of them reads "everything is
    // stalled".
    const { store } = mount()
    batchOf(store, 3)
    openTheList()
    const queued = document.querySelector<HTMLElement>('[data-testid="ops-list-queued"]')
    expect(queued?.querySelectorAll('[role="progressbar"]')).toHaveLength(0)
    expect(queued?.textContent).toContain('Queued')
    // The one that IS moving still has one.
    expect(
      document.querySelector('[data-testid="ops-list"]')?.querySelectorAll('[role="progressbar"]'),
    ).toHaveLength(1)
  })

  it('counts every waiting file on the icon, from anywhere in the app', () => {
    // The badge is what somebody sees without opening anything, and a
    // count of one for a four-file drop would be wrong at exactly the
    // moment they dropped it.
    const { store } = mount()
    batchOf(store, 4)
    expect(badge()?.textContent).toBe('4')
  })

  it('cancels the waiting file the person pressed, and nothing else', () => {
    const { store, services } = mount()
    const ids = batchOf(store, 3)
    openTheList()
    const queuedCancel = [
      ...document.querySelectorAll<HTMLElement>('[data-testid="ops-list-queued"] button'),
    ].find((b) => b.getAttribute('aria-label') === 'Cancel f2.iso')
    queuedCancel!.click()
    // No transfer exists for it, so nothing is said on the wire.
    expect(services.cancels).toEqual([])
    expect(store.transfers().map((t) => t.id)).toEqual([ids[0], ids[1]])
    expect(badge()?.textContent).toBe('2')
  })

  it('says nothing about a queue when there is none', () => {
    const { store } = mount()
    startTransfer(store)
    openTheList()
    expect(heading('Queued')).toBeUndefined()
    expect(document.querySelector('[data-testid="ops-list-queued"]')).toBeNull()
  })
})

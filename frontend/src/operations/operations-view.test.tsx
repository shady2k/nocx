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
import { afterEach, describe, expect, it } from 'vitest'

import { fakeClock, fakeUploadServices } from '../files/upload-fixtures'
import { uploadOperations } from '../files/upload-operations'
import { createUploadStore, type UploadStore } from '../files/upload-store'
import { mountSidebar, type SidebarHandle, type SidebarViewDescriptor } from '../sidebar'
import { ArrowUpIcon, SettingsIcon } from '../ui/icons'
import { createOperationsModel } from './operations'
import { createOperationsView, OPERATIONS_VIEW_ID } from './operations-view'

const OTHER_VIEWS: readonly SidebarViewDescriptor[] = [
  { id: 'files', title: 'Files', icon: ArrowUpIcon, view: () => <div>files</div>, order: 0 },
  { id: 'ports', title: 'Ports', icon: ArrowUpIcon, view: () => <div>ports</div>, order: 1 },
]

let handle: SidebarHandle | null = null

function mount(): { store: UploadStore; services: ReturnType<typeof fakeUploadServices> } {
  const services = fakeUploadServices()
  const store = createUploadStore({ services, now: fakeClock().now })
  const model = createOperationsModel([uploadOperations(store)])

  const bar = document.createElement('div')
  bar.id = 'activitybar'
  const panel = document.createElement('div')
  panel.id = 'sidebar'
  document.body.append(bar, panel)

  handle = mountSidebar(
    bar,
    panel,
    [...OTHER_VIEWS, createOperationsView(model)],
    [{ id: 'settings', title: 'Settings', icon: SettingsIcon, onActivate: () => {} }],
  )
  return { store, services }
}

afterEach(() => {
  handle?.destroy()
  handle = null
  document.body.replaceChildren()
  cleanup()
})

const icon = () => document.querySelector<HTMLElement>(`button[data-view="${OPERATIONS_VIEW_ID}"]`)
const badge = () => document.querySelector<HTMLElement>(`[data-view-badge="${OPERATIONS_VIEW_ID}"]`)
const progress = () =>
  document.querySelector<HTMLElement>(`[data-view-progress="${OPERATIONS_VIEW_ID}"]`)
const list = () => document.querySelector<HTMLElement>('[data-testid="ops-list"]')
const rows = () => [...document.querySelectorAll<HTMLElement>('.ui-operation-row')]

function openTheList(): void {
  icon()!.click()
}

function cancelButton(): HTMLElement {
  const el = [...document.querySelectorAll<HTMLElement>('[data-testid="ops-list"] button')].find(
    (b) => (b.textContent ?? '').includes('Cancel'),
  )
  if (!el) throw new Error('no cancel in the operations list')
  return el
}

function startTransfer(store: UploadStore, id = 't1'): void {
  store.begin({ transferId: id, name: 'big.iso', destDir: '/srv/data', size: 400 })
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
    expect(list()?.textContent).not.toContain('Dismiss')
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
    expect(list()?.textContent).not.toContain('Cancel')
  })

  it('puts the live ones above the finished ones', () => {
    const { store, services } = mount()
    startTransfer(store, 'done')
    services.emitDone({ transferId: 'done', outcome: 'written', finalName: 'done', stranded: [] })
    store.begin({ transferId: 'live', name: 'live.iso', destDir: '/srv', size: 10 })

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
    store.begin({ transferId: 't2', name: 'other.iso', destDir: '/srv/data', size: 8 })
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

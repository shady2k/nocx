// @vitest-environment jsdom
//
// What a person can do that they could not before (AGENTS.md rule 1): see a
// running transfer, and stop it, from anywhere in the app — with the Files
// panel showing something else, and with the sidebar collapsed altogether.
//
// So this mounts the real activity bar with the real indicator over a real
// upload store, and drives the store the way the wire does. Nothing here
// asserts what the components render on their own; each of those has its
// own test. What is asserted is the thing none of them can report: that the
// indicator is still on screen and still operable when the panel is not.
import { cleanup, fireEvent } from '@solidjs/testing-library'
import { afterEach, describe, expect, it } from 'vitest'

import { fakeClock, fakeUploadServices } from '../files/upload-fixtures'
import { uploadOperations } from '../files/upload-operations'
import { createUploadStore, type UploadStore } from '../files/upload-store'
import { mountSidebar, type SidebarHandle, type SidebarViewDescriptor } from '../sidebar'
import { ArrowUpIcon, SettingsIcon } from '../ui/icons'
import { createOperationsModel } from './operations'
import { createOperationsIndicator } from './operations-indicator'

const VIEWS: readonly SidebarViewDescriptor[] = [
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
    VIEWS,
    [{ id: 'settings', title: 'Settings', icon: SettingsIcon, onActivate: () => {} }],
    null,
    undefined,
    undefined,
    undefined,
    undefined,
    [createOperationsIndicator(model)],
  )
  return { store, services }
}

afterEach(() => {
  handle?.destroy()
  handle = null
  document.body.replaceChildren()
  cleanup()
})

const indicator = () => document.querySelector<HTMLElement>('[data-testid="ops-indicator"]')
const badge = () => document.querySelector<HTMLElement>('[data-testid="ops-badge"]')
const progress = () => document.querySelector<HTMLElement>('[data-testid="ops-progress"]')
const popover = () => document.querySelector<HTMLElement>('[data-testid="ops-popover"]')
const rows = () => [...document.querySelectorAll<HTMLElement>('.ui-operation-row')]

function cancelButton(): HTMLElement {
  const el = [...document.querySelectorAll<HTMLElement>('[data-testid="ops-popover"] button')].find(
    (b) => (b.textContent ?? '').includes('Cancel'),
  )
  if (!el) throw new Error('no cancel in the operations list')
  return el
}

function startTransfer(store: UploadStore, id = 't1'): void {
  store.begin({ transferId: id, name: 'big.iso', destDir: '/srv/data', size: 400 })
}

describe('the indicator is in the bar whatever the panel is doing', () => {
  it('is always there, running or not — a fixed position is one a person learns', () => {
    mount()
    expect(indicator()).not.toBeNull()
    // And nothing jumps in and out of the bar: the badge and the aggregate
    // are what appear, never the button itself.
    expect(badge()).toBeNull()
    expect(progress()).toBeNull()
  })

  it('is in the BOTTOM zone, above the gear', () => {
    // The bottom zone is the only part of the sidebar that stays on screen
    // whatever the panel is doing, which is the whole requirement; and the
    // gear stays bottom-most, because a fixed position it already had must
    // not move because something new arrived.
    mount()
    const zone = document.querySelector('.activity-bar-bottom')
    const buttons = [...(zone?.querySelectorAll('button') ?? [])]
    expect(
      buttons.map((b) => b.getAttribute('data-indicator') ?? b.getAttribute('data-action')),
    ).toEqual(['operations', 'settings'])
  })

  it('survives a switch to another sidebar view, still counting and still operable', () => {
    const { store, services } = mount()
    startTransfer(store)
    expect(badge()?.textContent).toBe('1')

    // The user goes to another view. The Files panel is gone from the
    // screen and the transfer is not.
    document.querySelector<HTMLElement>('button[data-view="ports"]')!.click()
    expect(indicator()).not.toBeNull()
    expect(badge()?.textContent).toBe('1')

    indicator()!.click()
    expect(rows()).toHaveLength(1)
    cancelButton().click()
    expect(services.cancels).toEqual(['t1'])
  })

  it('survives collapsing the sidebar entirely — the case the Files panel could not answer', () => {
    const { store, services } = mount()
    startTransfer(store)

    // Cmd+B, the way a person collapses it.
    fireEvent.keyDown(document, { key: 'b', metaKey: true })
    expect(document.getElementById('sidebar')?.classList.contains('collapsed')).toBe(true)

    expect(indicator()).not.toBeNull()
    expect(badge()?.textContent).toBe('1')
    indicator()!.click()
    expect(rows()).toHaveLength(1)
    expect(rows()[0].getAttribute('data-phase')).toBe('running')

    cancelButton().click()
    expect(services.cancels).toEqual(['t1'])
    // The cancel goes to the WIRE and decides no phase locally: it races
    // the transfer's own completion every time, and uploadDone says which
    // won.
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
    expect(indicator()?.getAttribute('aria-label')).toBe('Background operations')
    startTransfer(store)
    expect(indicator()?.getAttribute('aria-label')).toContain('1 running')
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

describe('the list the indicator opens', () => {
  it('says so when there is nothing, rather than opening on an empty box', () => {
    mount()
    indicator()!.click()
    expect(popover()).not.toBeNull()
    expect(document.querySelector('.ui-empty-state')?.textContent).toContain('Nothing is running')
    expect(rows()).toHaveLength(0)
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

    indicator()!.click()
    expect(rows()).toHaveLength(1)
    expect(rows()[0].getAttribute('data-phase')).toBe('written')
    // And there is no × to press: nothing asks the person to tidy up.
    expect(popover()?.textContent).not.toContain('Dismiss')
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
    indicator()!.click()
    expect(rows()[0].getAttribute('data-phase')).toBe('failed')
    expect(rows()[0].textContent).toContain('Failed')
    expect(rows()[0].textContent).toContain('permission denied')
    expect(popover()?.textContent).not.toContain('Cancel')
  })

  it('puts the live ones above the finished ones', () => {
    const { store, services } = mount()
    startTransfer(store, 'done')
    services.emitDone({ transferId: 'done', outcome: 'written', finalName: 'done', stranded: [] })
    store.begin({ transferId: 'live', name: 'live.iso', destDir: '/srv', size: 10 })

    indicator()!.click()
    expect(rows().map((r) => r.getAttribute('data-phase'))).toEqual(['running', 'written'])
  })

  it('closes when the person clicks away, and opens again from the same button', () => {
    mount()
    indicator()!.click()
    expect(popover()).not.toBeNull()
    fireEvent.pointerDown(document.body)
    expect(popover()).toBeNull()
    indicator()!.click()
    expect(popover()).not.toBeNull()
  })

  it('is a toggle: the same button closes what it opened', () => {
    mount()
    indicator()!.click()
    expect(popover()).not.toBeNull()
    indicator()!.click()
    expect(popover()).toBeNull()
  })

  it('follows the store while it is open — a row does not need reopening to move', () => {
    const { store, services } = mount()
    startTransfer(store)
    indicator()!.click()
    expect(rows()[0].getAttribute('data-phase')).toBe('running')
    services.emitDone({ transferId: 't1', outcome: 'cancelled', finalName: '', stranded: [] })
    expect(rows()[0].getAttribute('data-phase')).toBe('cancelled')
  })
})

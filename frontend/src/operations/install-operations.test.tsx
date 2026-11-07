// @vitest-environment jsdom
//
// A first open's helper install is ONE row for its whole duration, with what
// it is doing, cancellable, and it goes on settle (nocx-3an1s.2.10,
// ADR-8eej3e66 §8).
//
// Driven through the real sidebar, the real activity bar, the real operations
// view and the real row — the seam a person reaches — over a fake dispatcher
// that carries the two notifications the backend sends. The backend's half
// (the real install, the real socket, the cancellation) is
// internal/app/open_install_row_test.go, and the whole thing is
// e2e/helper-install-row.spec.ts.
import { cleanup, fireEvent } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { CallSettlement, Dispatcher, PendingCallView } from '../dispatcher'
import { mountSidebar, type SidebarHandle } from '../sidebar'
import { SettingsIcon } from '../ui/icons'
import { callOperations } from './call-operations'
import { installOperations } from './install-operations'
import { createOperationsModel } from './operations'
import { createOperationsView, OPERATIONS_VIEW_ID } from './operations-view'

type Notify = (params: unknown) => void

interface FakeDispatcher {
  dispatcher: Dispatcher
  cancel: ReturnType<typeof vi.fn>
  /** A person's `open`, as the call source sees it. */
  pending: (id: number, method: string) => void
  /** The call the backend is working on under `requestID`: the translation
   *  the real dispatcher owns, and the reason the two ids are not one. */
  wire: (requestID: number, callID: number) => void
  settled: (id: number) => void
  notify: (method: string, params: unknown) => void
  disconnect: () => void
}

function fakeDispatcher(): FakeDispatcher {
  const cancel = vi.fn()
  const handlers = new Map<string, Set<Notify>>()
  const disconnects = new Set<() => void>()
  // Wire id → the call it was sent under, as `Dispatcher.pending` holds it:
  // a response forgets the wire id, the call's own id lives on.
  const wireIDs = new Map<number, number>()
  const pendingHandlers = new Set<(call: PendingCallView) => void>()
  const settledHandlers = new Set<(id: number, settlement: CallSettlement) => void>()
  const dispatcher = {
    cancel,
    callIDForRequest(requestID: number | string): number | null {
      if (typeof requestID !== 'number') return null
      return wireIDs.get(requestID) ?? null
    },
    onCallPending(handler: (call: PendingCallView) => void) {
      pendingHandlers.add(handler)
      return () => {
        pendingHandlers.delete(handler)
      }
    },
    onCallSettled(handler: (id: number, settlement: CallSettlement) => void) {
      settledHandlers.add(handler)
      return () => {
        settledHandlers.delete(handler)
      }
    },
    subscribe(method: string, handler: Notify) {
      let set = handlers.get(method)
      if (!set) {
        set = new Set()
        handlers.set(method, set)
      }
      set.add(handler)
      return () => set.delete(handler)
    },
    onDisconnect(handler: () => void) {
      disconnects.add(handler)
      return () => disconnects.delete(handler)
    },
  } as unknown as Dispatcher
  return {
    dispatcher,
    cancel,
    pending: (id, method) => {
      for (const handler of pendingHandlers) {
        handler({ id, method, startedAt: Date.now(), cancel: () => {} })
      }
    },
    wire: (requestID, callID) => wireIDs.set(requestID, callID),
    settled: (id) => {
      for (const [requestID, callID] of wireIDs) if (callID === id) wireIDs.delete(requestID)
      for (const handler of settledHandlers) handler(id, 'answered')
    },
    notify: (method, params) => {
      for (const handler of handlers.get(method) ?? []) handler(params)
    },
    disconnect: () => {
      for (const handler of disconnects) handler()
    },
  }
}

/** The two sources main.tsx builds, in its own order. */
function mount(fake: FakeDispatcher): void {
  const callOps = callOperations(fake.dispatcher)
  const installOps = installOperations(fake.dispatcher)
  const model = createOperationsModel([callOps.source, installOps.source])
  const bar = document.createElement('div')
  const panel = document.createElement('div')
  document.body.append(bar, panel)
  handle = mountSidebar(
    bar,
    panel,
    [createOperationsView(model)],
    [{ id: 'settings', title: () => 'Settings', icon: SettingsIcon, onActivate: () => {} }],
  )
}

let handle: SidebarHandle | null = null

afterEach(() => {
  handle?.destroy()
  handle = null
  document.body.replaceChildren()
  cleanup()
})

function icon(): HTMLElement {
  const found = document.querySelector<HTMLElement>(`button[data-view="${OPERATIONS_VIEW_ID}"]`)
  if (found === null) throw new Error('operations view button missing')
  return found
}

function openList(): void {
  icon().click()
}

function rows(): HTMLElement[] {
  return [...document.querySelectorAll<HTMLElement>('.ui-operation-row')]
}

function emptyState(): string {
  return document.querySelector('[data-testid="operations-panel"]')?.textContent ?? ''
}

const copying = {
  requestId: 1,
  kind: 'install',
  title: 'Copying the nocx helper',
  machine: 'e2e@web-01',
  done: 4_194_304,
  total: 16_043_212,
}

describe("a first open's helper install", () => {
  it('takes a row at once, with what it is doing and how far it has got', () => {
    const fake = fakeDispatcher()
    mount(fake)
    openList()
    fake.pending(1, 'open')
    fake.wire(1, 1)
    fake.notify('operation.row', copying)
    expect(rows()).toHaveLength(1)
    const text = rows()[0].textContent ?? ''
    expect(text).toContain('Copying the nocx helper')
    expect(text).toContain('e2e@web-01')
    expect(text).toContain('4.2 MB of 16.0 MB')
    // A row that appears at once is the whole point: no threshold, because
    // this is work a person is watching (the catalogue says row: always).
    expect(document.querySelector('.ui-operation-row__percent')?.textContent).toBe('26%')
  })

  it("stays ONE row while its title changes, and the open's own row never doubles it", () => {
    vi.useFakeTimers()
    try {
      const fake = fakeDispatcher()
      mount(fake)
      // The person's open is on the wire, and the install starts inside it.
      fake.pending(1, 'open')
      fake.wire(1, 1)
      fake.notify('operation.row', copying)
      // Past the call threshold: the call source would show "Opening your
      // terminal" for the same request, and one operation is one row.
      vi.advanceTimersByTime(3000)
      openList()
      expect(rows()).toHaveLength(1)
      fake.notify('operation.row', {
        ...copying,
        title: 'Checking the nocx helper',
        done: 0,
        total: 0,
      })
      expect(rows()).toHaveLength(1)
      expect(rows()[0].textContent).toContain('Checking the nocx helper')
      // No bar and no percentage for a stage that counts nothing.
      expect(document.querySelector('.ui-operation-row__percent')).toBeNull()
      expect(rows()[0].textContent).not.toContain('of 0 B')
    } finally {
      vi.useRealTimers()
    }
  })

  it('goes when the call it belongs to settles, with no row-done frame', () => {
    const fake = fakeDispatcher()
    mount(fake)
    fake.pending(3, 'open') // the call's own id
    fake.wire(11, 3) // the id the backend names the work with
    fake.notify('operation.row', { ...copying, requestId: 11 })
    openList()
    expect(rows()).toHaveLength(1)
    // The row IS the call, so the call's own settlement ends it — an answer,
    // a withdrawal, or a socket that took the answer away. The row-done frame
    // is the backend saying the install finished, which a dropped frame must
    // not be able to leave a row behind for: Cancel settles the call, and a
    // row that outlived its call could not be withdrawn by anything.
    fake.settled(3)
    expect(rows()).toHaveLength(0)
    expect(emptyState()).toContain('Nothing is running')
  })

  it('goes on the row-done frame when the install ends before the open does', () => {
    const fake = fakeDispatcher()
    mount(fake)
    fake.pending(3, 'open')
    fake.wire(11, 3)
    fake.notify('operation.row', { ...copying, requestId: 11 })
    openList()
    expect(rows()).toHaveLength(1)
    fake.notify('operation.rowDone', { requestId: 11 })
    expect(rows()).toHaveLength(0)
    expect(emptyState()).toContain('Nothing is running')
  })

  it("cancels the open through the dispatcher by the call's own id, which is not the id the backend named", () => {
    const fake = fakeDispatcher()
    mount(fake)
    fake.pending(3, 'open') // the call's own id: what a row and Cancel name
    fake.wire(11, 3) // the id the backend is working on
    fake.notify('operation.row', { ...copying, requestId: 11 })
    openList()
    const button = document.querySelector<HTMLElement>(
      '[aria-label="Cancel Copying the nocx helper"]',
    )
    expect(button).not.toBeNull()
    fireEvent.click(button!)
    // The row borrows the call's identity, so its Cancel is the ordinary
    // withdrawal of the open — there is no second verb. The id the backend
    // named is not that identity: after a queued call, a reordered send or a
    // sealed retry it names a different call, or none.
    expect(fake.cancel).toHaveBeenCalledTimes(1)
    expect(fake.cancel).toHaveBeenCalledWith(3)
  })

  it("is ONE row for the call, under the call's id", () => {
    vi.useFakeTimers()
    try {
      const fake = fakeDispatcher()
      mount(fake)
      // The person's open waits long enough for its own row, and the install
      // inside it starts under the id the backend is working on.
      fake.pending(3, 'open')
      fake.wire(11, 3)
      vi.advanceTimersByTime(3000)
      openList()
      expect(rows()).toHaveLength(1)
      expect(rows()[0].textContent).toContain('Opening your terminal')
      fake.notify('operation.row', { ...copying, requestId: 11 })
      // One operation is one row: the install's account of the work replaces
      // the call's, and the two ids never make it two.
      expect(rows()).toHaveLength(1)
      expect(rows()[0].textContent).toContain('Copying the nocx helper')
    } finally {
      vi.useRealTimers()
    }
  })

  it('goes by the id the backend named even after the call it describes has settled', () => {
    const fake = fakeDispatcher()
    mount(fake)
    fake.pending(3, 'open')
    fake.wire(11, 3)
    fake.notify('operation.row', { ...copying, requestId: 11 })
    openList()
    expect(rows()).toHaveLength(1)
    // The answer arrives, so the dispatcher no longer knows the wire id; the
    // row still knows which row it is, and its own end still removes it.
    fake.settled(3)
    fake.notify('operation.rowDone', { requestId: 11 })
    expect(rows()).toHaveLength(0)
    expect(emptyState()).toContain('Nothing is running')
  })

  it('draws nothing for a row whose call this renderer does not hold', () => {
    const fake = fakeDispatcher()
    mount(fake)
    // No call of ours was sent under this id: there is no call identity to
    // key the row by and no withdrawal the row could offer, so there is no
    // row. A key that guessed would be another action's key.
    fake.notify('operation.row', copying)
    openList()
    expect(rows()).toHaveLength(0)
    expect(emptyState()).toContain('Nothing is running')
  })

  it('goes when the socket does, because the work died with it', () => {
    const fake = fakeDispatcher()
    mount(fake)
    fake.pending(1, 'open')
    fake.wire(1, 1)
    fake.notify('operation.row', copying)
    openList()
    expect(rows()).toHaveLength(1)
    fake.disconnect()
    expect(rows()).toHaveLength(0)
    expect(emptyState()).toContain('Nothing is running')
  })

  it('shows nothing for an open that installs nothing', () => {
    vi.useFakeTimers()
    try {
      const fake = fakeDispatcher()
      mount(fake)
      // The open answers inside the call threshold and no install was
      // reported: the row count stays at zero.
      fake.pending(3, 'open')
      fake.settled(3)
      vi.advanceTimersByTime(10_000)
      openList()
      expect(rows()).toHaveLength(0)
      expect(emptyState()).toContain('Nothing is running')
    } finally {
      vi.useRealTimers()
    }
  })

  it('ignores a frame it cannot read rather than drawing a broken row', () => {
    const fake = fakeDispatcher()
    mount(fake)
    fake.notify('operation.row', { requestId: 1, kind: 'install' })
    fake.notify('operation.row', null)
    openList()
    expect(rows()).toHaveLength(0)
  })
})

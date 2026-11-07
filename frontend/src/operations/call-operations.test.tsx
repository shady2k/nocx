// @vitest-environment jsdom
//
// A person's call waiting past two seconds gets a row with the catalogue's
// sentence and Cancel; the row goes when the call settles and leaves no
// finished entry; ordinary work that answers quickly never appears. Driven
// through the real sidebar, the real activity bar and the real operations
// view — the seam a person reaches, not the store alone.
import { cleanup, fireEvent } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { CallSettlement, Dispatcher, PendingCallView } from '../dispatcher'
import { mountSidebar, type SidebarHandle } from '../sidebar'
import { SettingsIcon } from '../ui/icons'
import { callOperations } from './call-operations'
import { createOperationsModel } from './operations'
import { createOperationsView, OPERATIONS_VIEW_ID } from './operations-view'

function fakeDispatcher(): {
  dispatcher: Dispatcher
  cancel: ReturnType<typeof vi.fn>
  call: ReturnType<typeof vi.fn>
  /** A person's call on the wire, as the dispatcher announces it. */
  pending: (call: PendingCallView) => void
  /** That call settling, however it settled. */
  settled: (id: number, settlement?: CallSettlement) => void
} {
  const cancel = vi.fn()
  const call = vi.fn()
  const pendingHandlers = new Set<(call: PendingCallView) => void>()
  const settledHandlers = new Set<(id: number, settlement: CallSettlement) => void>()
  const dispatcher = {
    cancel,
    call,
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
  } as unknown as Dispatcher
  return {
    dispatcher,
    cancel,
    call,
    pending: (c) => {
      for (const handler of pendingHandlers) handler(c)
    },
    settled: (id, settlement = 'answered') => {
      for (const handler of settledHandlers) handler(id, settlement)
    },
  }
}

function pending(id: number, method: string, mutation?: string): PendingCallView {
  return { id, method, startedAt: Date.now(), mutation, cancel: () => {} }
}

/** Let the outcome ask's promise and its continuation run. */
async function settle(): Promise<void> {
  for (let i = 0; i < 5; i++) await Promise.resolve()
}

let handle: SidebarHandle | null = null

function mount(dispatcher: Dispatcher, thresholdMs?: number): void {
  const ops = callOperations(dispatcher, thresholdMs)
  const model = createOperationsModel([ops.source])
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

afterEach(() => {
  vi.useRealTimers()
  handle?.destroy()
  handle = null
  document.body.replaceChildren()
  cleanup()
})

describe('a waiting call', () => {
  it('takes no row inside two seconds and a row with its sentence after', () => {
    vi.useFakeTimers()
    const fake = fakeDispatcher()
    mount(fake.dispatcher)
    fake.pending(pending(1, 'git.commit'))
    vi.advanceTimersByTime(1999)
    expect(rows()).toHaveLength(0)
    vi.advanceTimersByTime(1)
    openList()
    expect(rows()).toHaveLength(1)
    expect(rows()[0].textContent).toContain('Committing in this checkout')
  })

  it('goes on settle and leaves no finished entry', () => {
    vi.useFakeTimers()
    const fake = fakeDispatcher()
    mount(fake.dispatcher)
    fake.pending(pending(1, 'git.commit'))
    vi.advanceTimersByTime(2000)
    openList()
    expect(rows()).toHaveLength(1)
    fake.settled(1, 'answered')
    expect(rows()).toHaveLength(0)
    expect(emptyState()).toContain('Nothing is running')
  })

  it('cancels through the dispatcher and the row leaves', () => {
    vi.useFakeTimers()
    const fake = fakeDispatcher()
    const { cancel } = fake
    mount(fake.dispatcher)
    fake.pending(pending(7, 'git.commit'))
    vi.advanceTimersByTime(2000)
    openList()
    const button = document.querySelector<HTMLElement>(
      '[aria-label="Cancel Committing in this checkout"]',
    )
    expect(button).not.toBeNull()
    fireEvent.click(button!)
    expect(cancel).toHaveBeenCalledWith(7)
    fake.settled(7, 'answered')
    expect(rows()).toHaveLength(0)
  })

  it('never shows a method the catalogue says never', () => {
    vi.useFakeTimers()
    const fake = fakeDispatcher()
    mount(fake.dispatcher)
    fake.pending(pending(2, 'transport.ping'))
    vi.advanceTimersByTime(10_000)
    openList()
    expect(rows()).toHaveLength(0)
  })

  it('a cancelled call takes its row with it, mutation or not', async () => {
    // ADR-8eej3e66 §8: A WAIT ROW GOES THE MOMENT IT SETTLES and leaves no
    // finished entry, because only transfers have an outcome worth keeping.
    // A cancelled mutation's row used to stay behind to say which of the
    // three answers it was, and that is the entry the record says Operations
    // must not keep: the answer belongs to the call the person made, whose
    // own rejection carries it, not to a row nobody can act on.
    vi.useFakeTimers()
    const fake = fakeDispatcher()
    const { call } = fake
    mount(fake.dispatcher)
    fake.pending(pending(1, 'git.commit', 'm-1'))
    vi.advanceTimersByTime(2000)
    openList()
    expect(rows()).toHaveLength(1)

    fake.settled(1, 'no-answer')
    await settle()

    expect(rows()).toHaveLength(0)
    expect(call).not.toHaveBeenCalled()
  })

  it('a call that settles leaves nothing to re-ask about', async () => {
    // The row is the WAIT. Once the call settles there is nothing waiting,
    // so nothing is asked and nothing is kept — however the call ended.
    vi.useFakeTimers()
    const fake = fakeDispatcher()
    const { call } = fake
    mount(fake.dispatcher)
    fake.pending(pending(1, 'git.commit', 'm-1'))
    vi.advanceTimersByTime(2000)
    fake.settled(1, 'no-answer')
    await settle()
    expect(rows()).toHaveLength(0)
    vi.advanceTimersByTime(10_000)
    await settle()
    expect(call).not.toHaveBeenCalled()
  })

  it('a mutation that was answered keeps no row, and a read is never asked about', async () => {
    vi.useFakeTimers()
    const fake = fakeDispatcher()
    const { call } = fake
    mount(fake.dispatcher)
    fake.pending(pending(1, 'git.commit', 'm-1'))
    vi.advanceTimersByTime(2000)
    fake.settled(1, 'answered')
    await settle()
    expect(call).not.toHaveBeenCalled()
    expect(rows()).toHaveLength(0)

    fake.pending(pending(2, 'git.status'))
    vi.advanceTimersByTime(2000)
    fake.settled(2, 'no-answer')
    await settle()
    expect(call).not.toHaveBeenCalled()
    expect(rows()).toHaveLength(0)
  })

  it('goes even when nothing can say what became of the call', async () => {
    // The sentence for "nobody knows what happened" is mutations.ts's, and it
    // reaches the person through the call the surface made. A row that waited
    // for it was the one a Cancel could not remove.
    vi.useFakeTimers()
    const fake = fakeDispatcher()
    const { call } = fake
    call.mockResolvedValue({ outcome: 'unknown', operation: '' })
    mount(fake.dispatcher)
    fake.pending(pending(1, 'git.commit', 'm-1'))
    vi.advanceTimersByTime(2000)
    openList()
    expect(rows()).toHaveLength(1)
    fake.settled(1, 'no-answer')
    await settle()
    expect(rows()).toHaveLength(0)
    expect(call).not.toHaveBeenCalled()
  })

  it('shows nothing for ordinary work that answers inside the threshold', () => {
    vi.useFakeTimers()
    const fake = fakeDispatcher()
    mount(fake.dispatcher)
    for (let id = 1; id <= 20; id++) {
      fake.pending(pending(id, 'git.status'))
      fake.settled(id, 'answered')
    }
    vi.advanceTimersByTime(10_000)
    openList()
    expect(rows()).toHaveLength(0)
    expect(emptyState()).toContain('Nothing is running')
  })
})

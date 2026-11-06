// @vitest-environment jsdom
//
// A person's call waiting past two seconds gets a row with the catalogue's
// sentence and Cancel; the row goes when the call settles and leaves no
// finished entry; ordinary work that answers quickly never appears. Driven
// through the real sidebar, the real activity bar and the real operations
// view — the seam a person reaches, not the store alone.
import { cleanup, fireEvent } from '@solidjs/testing-library'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { Dispatcher, PendingCallView } from '../dispatcher'
import { mountSidebar, type SidebarHandle } from '../sidebar'
import { SettingsIcon } from '../ui/icons'
import { callOperations } from './call-operations'
import { createOperationsModel } from './operations'
import { createOperationsView, OPERATIONS_VIEW_ID } from './operations-view'

function fakeDispatcher(): { dispatcher: Dispatcher; cancel: ReturnType<typeof vi.fn> } {
  const cancel = vi.fn()
  return { dispatcher: { cancel } as unknown as Dispatcher, cancel }
}

function pending(id: number, method: string): PendingCallView {
  return { id, method, startedAt: Date.now(), cancel: () => {} }
}

let handle: SidebarHandle | null = null

function mount(dispatcher: Dispatcher): void {
  const ops = callOperations(dispatcher)
  const model = createOperationsModel([ops.source])
  const bar = document.createElement('div')
  const panel = document.createElement('div')
  document.body.append(bar, panel)
  handle = mountSidebar(
    bar,
    panel,
    [createOperationsView(model)],
    [{ id: 'settings', title: 'Settings', icon: SettingsIcon, onActivate: () => {} }],
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
    const { dispatcher } = fakeDispatcher()
    mount(dispatcher)
    dispatcher.onCallPending!(pending(1, 'git.commit'))
    vi.advanceTimersByTime(1999)
    expect(rows()).toHaveLength(0)
    vi.advanceTimersByTime(1)
    openList()
    expect(rows()).toHaveLength(1)
    expect(rows()[0].textContent).toContain('Committing in this checkout')
  })

  it('goes on settle and leaves no finished entry', () => {
    vi.useFakeTimers()
    const { dispatcher } = fakeDispatcher()
    mount(dispatcher)
    dispatcher.onCallPending!(pending(1, 'git.commit'))
    vi.advanceTimersByTime(2000)
    openList()
    expect(rows()).toHaveLength(1)
    dispatcher.onCallSettled!(1)
    expect(rows()).toHaveLength(0)
    expect(emptyState()).toContain('Nothing is running')
  })

  it('cancels through the dispatcher and the row leaves', () => {
    vi.useFakeTimers()
    const { dispatcher, cancel } = fakeDispatcher()
    mount(dispatcher)
    dispatcher.onCallPending!(pending(7, 'git.commit'))
    vi.advanceTimersByTime(2000)
    openList()
    const button = document.querySelector<HTMLElement>(
      '[aria-label="Cancel Committing in this checkout"]',
    )
    expect(button).not.toBeNull()
    fireEvent.click(button!)
    expect(cancel).toHaveBeenCalledWith(7)
    dispatcher.onCallSettled!(7)
    expect(rows()).toHaveLength(0)
  })

  it('never shows a method the catalogue says never', () => {
    vi.useFakeTimers()
    const { dispatcher } = fakeDispatcher()
    mount(dispatcher)
    dispatcher.onCallPending!(pending(2, 'transport.ping'))
    vi.advanceTimersByTime(10_000)
    openList()
    expect(rows()).toHaveLength(0)
  })

  it('shows nothing for ordinary work that answers inside the threshold', () => {
    vi.useFakeTimers()
    const { dispatcher } = fakeDispatcher()
    mount(dispatcher)
    for (let id = 1; id <= 20; id++) {
      dispatcher.onCallPending!(pending(id, 'git.status'))
      dispatcher.onCallSettled!(id)
    }
    vi.advanceTimersByTime(10_000)
    openList()
    expect(rows()).toHaveLength(0)
    expect(emptyState()).toContain('Nothing is running')
  })
})

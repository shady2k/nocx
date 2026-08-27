// @vitest-environment jsdom
/**
 * A banner clicked while nocx was in the background brings you back to the
 * pane the notification came from (nocx-jiwq.1, plan D1).
 *
 * Driven through the seams a person reaches: a real PaneManager with real
 * tabs, the subscription wired exactly as main.tsx wires it, and the push the
 * backend sends when the OS hands back a click. The resolution asserted here
 * is the ONE the product has — PaneManager.findBySession, the same lookup the
 * notification panel's row activation uses — because a second one would agree
 * everywhere anyone looked and disagree where it mattered.
 */
import { describe, expect, it, vi, beforeEach } from 'vitest'
import {
  createRendererMock,
  resetSessionCounter,
  mountPaneManager,
  makeClient,
  makeSession,
} from '../test-support/panes-fixtures'
import { subscribeSessionFocus } from './focus-request'
import { LOCAL_BACKEND_ID, type PaneManager } from '../panes'
import type { SessionFocus } from '../generated/session.focus'

vi.mock('../renderers/xterm', () => ({
  XtermRenderer: vi.fn(createRendererMock),
}))

function fakeDispatcher() {
  const handlers = new Map<string, Set<(p: unknown) => void>>()
  return {
    subscribe(method: string, h: (p: unknown) => void) {
      let s = handlers.get(method)
      if (!s) handlers.set(method, (s = new Set()))
      s.add(h)
      return () => {
        s.delete(h)
      }
    },
    /** What the backend pushes, typed with the GENERATED declaration. */
    emit(push: SessionFocus) {
      handlers.get('session.focus')?.forEach((h) => h(push))
    },
    emitRaw(params: unknown) {
      handlers.get('session.focus')?.forEach((h) => h(params))
    },
    subscriberCount() {
      return handlers.get('session.focus')?.size ?? 0
    },
  }
}

/** The subscription as the composition root builds it (main.tsx): resolution
 *  is the renderer's, through the one lookup. */
function wire(dispatcher: ReturnType<typeof fakeDispatcher>, manager: PaneManager) {
  return subscribeSessionFocus(dispatcher, (sessionId) => {
    const pane = manager.findBySession(LOCAL_BACKEND_ID, sessionId)
    if (pane) void manager.activate(pane)
  })
}

describe('a banner click and the pane it came from', () => {
  beforeEach(() => {
    resetSessionCounter()
    vi.clearAllMocks()
  })

  /** Two tabs; the SECOND is the one in front of the user, which is the state
   *  a background click finds — the notification came from the other one. */
  async function twoTabs() {
    const opened: ReturnType<typeof makeSession>[] = []
    const client = makeClient({
      openSession: vi.fn(() => {
        const session = makeSession()
        opened.push(session)
        return Promise.resolve(session)
      }),
    })
    const { bar, manager } = await mountPaneManager(client)
    manager.newPane()
    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(2)
    })
    return { bar, manager, opened }
  }

  it('focuses the pane holding the session the banner named', async () => {
    const { bar, manager, opened } = await twoTabs()
    const dispatcher = fakeDispatcher()
    wire(dispatcher, manager)
    expect(bar.querySelectorAll('.nocx-tab')[1].getAttribute('aria-selected')).toBe('true')

    dispatcher.emit({ sessionId: opened[0].sessionId })

    await vi.waitFor(() => {
      expect(bar.querySelectorAll('.nocx-tab')[0].getAttribute('aria-selected')).toBe('true')
    })
  })

  // A pane that is gone is not an error: the tab was closed, the click moved
  // the window, and there is nothing left to focus. Nothing else moves.
  it('does nothing when no pane holds the session', async () => {
    const { bar, manager } = await twoTabs()
    const dispatcher = fakeDispatcher()
    wire(dispatcher, manager)

    dispatcher.emit({ sessionId: 'a-session-no-pane-holds' })

    await Promise.resolve()
    expect(bar.querySelectorAll('.nocx-tab')[1].getAttribute('aria-selected')).toBe('true')
    expect(bar.querySelectorAll('.nocx-tab')[0].getAttribute('aria-selected')).toBe('false')
  })

  // Server-initiated and unsolicited: nothing correlated it and nothing
  // checked its shape at a call site.
  it.each([
    ['null', null],
    ['not an object', 'sess-1'],
    ['no session id', {}],
    ['an empty session id', { sessionId: '' }],
    ['a non-string session id', { sessionId: 7 }],
  ])('ignores a malformed push (%s)', async (_name, params) => {
    const { manager } = await twoTabs()
    const dispatcher = fakeDispatcher()
    const focused: string[] = []
    subscribeSessionFocus(dispatcher, (sessionId) => focused.push(sessionId))
    void manager

    dispatcher.emitRaw(params)

    expect(focused).toEqual([])
  })

  it('stops resolving once unsubscribed', async () => {
    const { bar, manager, opened } = await twoTabs()
    const dispatcher = fakeDispatcher()
    const unsubscribe = wire(dispatcher, manager)
    unsubscribe()

    dispatcher.emit({ sessionId: opened[0].sessionId })

    await Promise.resolve()
    expect(dispatcher.subscriberCount()).toBe(0)
    expect(bar.querySelectorAll('.nocx-tab')[1].getAttribute('aria-selected')).toBe('true')
  })
})

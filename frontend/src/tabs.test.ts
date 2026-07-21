// @vitest-environment jsdom
/* eslint-disable @typescript-eslint/unbound-method */
import { describe, expect, it, vi, beforeEach } from 'vitest'
import { TabManager } from './tabs'
import type {
  TerminalRenderer,
  DataCallback,
  ResizeCallback,
  TitleCallback,
} from './renderers/types'
import type { WSClient } from './ipc'

// ── Mocks ──────────────────────────────────────────────────────────────────

// Mock the renderer module before any imports use it.
vi.mock('./renderers', () => ({
  createRenderer: vi.fn(() => {
    const cbs: { onData?: DataCallback; onResize?: ResizeCallback; onTitle?: TitleCallback } = {}
    return {
      mount: vi.fn().mockResolvedValue(undefined),
      write: vi.fn(),
      reset: vi.fn(),
      onData: vi.fn((cb: DataCallback) => {
        cbs.onData = cb
      }),
      onResize: vi.fn((cb: ResizeCallback) => {
        cbs.onResize = cb
      }),
      onTitle: vi.fn((cb: TitleCallback) => {
        cbs.onTitle = cb
      }),
      focus: vi.fn(),
      cols: 80,
      rows: 24,
      _cbs: cbs,
    }
  }),
  resolveRendererName: vi.fn(() => 'xterm' as const),
}))

let mockSessionIdCounter = 0

/** Create a mock session with stored callbacks so tests can fire them. */
function makeSession() {
  let dataCb: ((data: string) => void) | null = null
  const session = {
    sessionId: `mock-sid-${++mockSessionIdCounter}`,
    send: vi.fn(),
    sendResize: vi.fn(),
    close: vi.fn(),
    onData: vi.fn((cb: (data: string) => void) => {
      dataCb = cb
    }),
    onExit: vi.fn(),
    onReset: vi.fn(),
    // Helper for tests to fire the registered data callback
    fireData: (data: string) => {
      dataCb?.(data)
    },
  }
  return session
}

/** Create a mock client that returns distinct sessions per call. */
function makeClient() {
  const sessions: ReturnType<typeof makeSession>[] = []
  const client = {
    connect: vi.fn().mockResolvedValue(undefined),
    openSession: vi.fn(() => {
      const s = makeSession()
      sessions.push(s)
      return Promise.resolve(s)
    }),
    close: vi.fn(),
    sendToSession: vi.fn(),
    sendResize: vi.fn(),
    closeSession: vi.fn(),
    onSessionData: vi.fn(),
    onSessionExit: vi.fn(),
    onSessionReset: vi.fn(),
    get connected() {
      return true
    },
    // Test access to sessions
    _sessions: sessions,
  }
  return client as unknown as WSClient & { _sessions: ReturnType<typeof makeSession>[] }
}

// ── Tests ──────────────────────────────────────────────────────────────────

describe('TabManager', () => {
  let bar: HTMLElement
  let panes: HTMLElement

  beforeEach(() => {
    document.body.innerHTML = ''
    bar = document.createElement('div')
    panes = document.createElement('div')
    document.body.append(bar, panes)
    vi.clearAllMocks()
  })

  // ── opening a tab creates a session and a pane ────────────────────────

  it('opens a session when a tab is created and activated', async () => {
    const client = makeClient()
    new TabManager(bar, panes, client)

    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalled()
    })

    expect(bar.querySelectorAll('.tab').length).toBe(1)
    expect(panes.querySelectorAll('.pane').length).toBe(1)
  })

  it('creates a session for each new tab', async () => {
    const client = makeClient()
    const tm = new TabManager(bar, panes, client)

    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(1)
    })

    tm.newTab()
    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(2)
    })

    expect(bar.querySelectorAll('.tab').length).toBe(2)
    expect(panes.querySelectorAll('.pane').length).toBe(2)
  })

  // ── closing closes the session and activates a neighbour ──────────────

  it('closes the session when the active tab is closed', async () => {
    const client = makeClient()
    const manager = new TabManager(bar, panes, client)

    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalled()
    })

    const session = client._sessions[0]
    manager.closeActiveTab()

    // The session should have been closed, but a new one created for the replacement
    expect(session.close).toHaveBeenCalled()
  })

  it('activates a neighbour tab when the active tab is closed', async () => {
    const client = makeClient()
    const manager = new TabManager(bar, panes, client)

    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalled()
    })

    manager.newTab()
    manager.newTab()
    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(3)
    })

    // Three tabs: [tab1, tab2, tab3]. Tab3 is active (last created).
    const tabs = bar.querySelectorAll('.tab')
    expect(tabs.length).toBe(3)
    expect(tabs[2].classList.contains('active')).toBe(true)

    // Close the active tab (tab3)
    manager.closeActiveTab()

    // Two tabs remain; the neighbour (tab2 at original index 1) is now active
    const remainingTabs = bar.querySelectorAll('.tab')
    expect(remainingTabs.length).toBe(2)
    // The last remaining tab should be active (neighbour)
    expect(remainingTabs[1].classList.contains('active')).toBe(true)
  })

  // ── closing the last tab leaves exactly one fresh tab ─────────────────

  it('closing the last tab opens a fresh tab immediately', async () => {
    const client = makeClient()
    const manager = new TabManager(bar, panes, client)

    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(1)
    })

    // Close the only tab
    manager.closeActiveTab()

    // A new tab replaces it (window never empty)
    expect(bar.querySelectorAll('.tab').length).toBe(1)
    expect(panes.querySelectorAll('.pane').length).toBe(1)
    // A new session was opened for the replacement (may be async)
    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(2)
    })
  })

  // ── fallback title consistency (badge vs title after close) ───────────

  it('fallback title drops the number so it never disagrees with the positional badge', async () => {
    const client = makeClient()
    const manager = new TabManager(bar, panes, client)

    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalled()
    })

    // Open tabs until the badge says 4.
    manager.newTab()
    manager.newTab()
    manager.newTab()
    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(4)
    })

    const labels = bar.querySelectorAll('.tab-index')
    const titles = bar.querySelectorAll('.tab-title')

    // Before close: badge = 1..4, fallback title is always 'Terminal'.
    expect(labels[0].textContent).toBe('1')
    expect(labels[1].textContent).toBe('2')
    expect(labels[2].textContent).toBe('3')
    expect(labels[3].textContent).toBe('4')
    titles.forEach((t) => expect(t.textContent).toBe('Terminal'))

    // Close the first two tabs via public API: activate then close.
    manager.activateByIndex(0)
    manager.closeActiveTab()
    manager.activateByIndex(0)
    manager.closeActiveTab()

    // Re-query after DOM mutations; stale references reflect removed elements.
    const afterLabels = bar.querySelectorAll('.tab-index')
    const afterTitles = bar.querySelectorAll('.tab-title')
    // After close: badge = 1..2, titles stay 'Terminal'.
    expect(afterLabels[0].textContent).toBe('1')
    expect(afterLabels[1].textContent).toBe('2')
    afterTitles.forEach((t) => expect(t.textContent).toBe('Terminal'))
  })

  // ── switching focuses the right renderer ──────────────────────────────

  it('switches between tabs on activateByIndex', async () => {
    const client = makeClient()
    const manager = new TabManager(bar, panes, client)

    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalled()
    })

    manager.newTab()
    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(2)
    })

    const tabButtons = bar.querySelectorAll('.tab')
    expect(tabButtons.length).toBe(2)

    // Tab 2 (index 1) is active by default (last created)
    expect(tabButtons[1].classList.contains('active')).toBe(true)

    // Switch to tab 1 (index 0)
    manager.activateByIndex(0)
    expect(tabButtons[0].classList.contains('active')).toBe(true)
    expect(tabButtons[1].classList.contains('active')).toBe(false)

    // Switch to tab 2 (index 1)
    manager.activateByIndex(1)
    expect(tabButtons[0].classList.contains('active')).toBe(false)
    expect(tabButtons[1].classList.contains('active')).toBe(true)
  })

  // ── a title event updates that tab's label and no other ───────────────

  it('updates the title of the correct tab when onTitle fires', async () => {
    const client = makeClient()
    const manager = new TabManager(bar, panes, client)

    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalled()
    })

    manager.newTab()
    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(2)
    })

    // Flush pending microtasks so both renderers are fully initialised.
    await Promise.resolve()

    const titles = bar.querySelectorAll('.tab-title')
    expect(titles.length).toBe(2)
    expect(titles[0].textContent).toBe('Terminal')
    expect(titles[1].textContent).toBe('Terminal')

    // Each call to createRenderer returned a mock with _cbs.onTitle.
    const { createRenderer } = await import('./renderers')
    const results = vi.mocked(createRenderer).mock.results
    expect(results.length).toBe(2)

    const r0 = results[0].value as TerminalRenderer & { _cbs: { onTitle?: TitleCallback } }
    const r1 = results[1].value as TerminalRenderer & { _cbs: { onTitle?: TitleCallback } }

    // Fire title for first tab only
    expect(r0._cbs.onTitle).toBeDefined()
    r0._cbs.onTitle!('~/project')
    expect(titles[0].textContent).toBe('~/project')
    expect(titles[1].textContent).toBe('Terminal')

    // Fire title for second tab only
    expect(r1._cbs.onTitle).toBeDefined()
    r1._cbs.onTitle!('bash-3.2')
    expect(titles[1].textContent).toBe('bash-3.2')
    expect(titles[0].textContent).toBe('~/project')
  })

  // ── activity indicator ────────────────────────────────────────────────

  it('shows activity indicator on a background tab receiving output', async () => {
    const client = makeClient()
    const manager = new TabManager(bar, panes, client)

    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(1)
    })

    manager.newTab()
    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(2)
    })

    // Make tab 1 (index 0) active; tab 2 (index 1) is now background.
    manager.activateByIndex(0)

    // Deliver output to the background tab (index 1 = session 2).
    const bgSession = client._sessions[1]
    bgSession.fireData('hello')

    // The background tab's indicator should have the activity class
    const indicators = bar.querySelectorAll('.tab-indicator')
    // Tab 1 (background) should have the activity class
    expect(indicators[1].classList.contains('tab-activity')).toBe(true)
    // Tab 0 (active) should not
    expect(indicators[0].classList.contains('tab-activity')).toBe(false)
  })

  it('clears activity indicator when activated', async () => {
    const client = makeClient()
    const manager = new TabManager(bar, panes, client)

    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(1)
    })

    manager.newTab()
    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(2)
    })

    // Tab 2 is active. Fire data on it while it's active → no activity mark.
    manager.activateByIndex(1)
    const activeSession = client._sessions[1]
    activeSession.fireData('output while active')

    const indicators = bar.querySelectorAll('.tab-indicator')
    expect(indicators[1].classList.contains('tab-activity')).toBe(false)
  })

  // ── keyboard shortcuts ────────────────────────────────────────────────

  it('opens a new tab on Cmd+T', async () => {
    const client = makeClient()
    new TabManager(bar, panes, client)

    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(1)
    })

    window.dispatchEvent(new KeyboardEvent('keydown', { key: 't', metaKey: true, bubbles: true }))

    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(2)
    })
    expect(bar.querySelectorAll('.tab').length).toBe(2)
  })

  it('opens a new tab on Ctrl+T', async () => {
    const client = makeClient()
    new TabManager(bar, panes, client)

    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(1)
    })

    window.dispatchEvent(new KeyboardEvent('keydown', { key: 't', ctrlKey: true, bubbles: true }))

    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(2)
    })
  })

  it('closes the active tab on Cmd+W', async () => {
    const client = makeClient()
    new TabManager(bar, panes, client)

    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalled()
    })

    const session = client._sessions[0]

    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'w', metaKey: true, bubbles: true }))

    // Closing the last tab opens a fresh one, so there's still 1 tab
    expect(bar.querySelectorAll('.tab').length).toBe(1)
    expect(session.close).toHaveBeenCalled()
  })

  it('switches tabs on Cmd+1..9', async () => {
    const client = makeClient()
    const manager = new TabManager(bar, panes, client)

    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(1)
    })

    manager.newTab()
    manager.newTab()
    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(3)
    })

    const tabButtons = bar.querySelectorAll('.tab')
    expect(tabButtons.length).toBe(3)

    // Tab 3 (index 2) is active (last created)
    expect(tabButtons[2].classList.contains('active')).toBe(true)

    // Cmd+1 → first tab
    window.dispatchEvent(new KeyboardEvent('keydown', { key: '1', metaKey: true, bubbles: true }))
    expect(tabButtons[0].classList.contains('active')).toBe(true)
    expect(tabButtons[1].classList.contains('active')).toBe(false)
    expect(tabButtons[2].classList.contains('active')).toBe(false)

    // Cmd+3 → third tab
    window.dispatchEvent(new KeyboardEvent('keydown', { key: '3', metaKey: true, bubbles: true }))
    expect(tabButtons[0].classList.contains('active')).toBe(false)
    expect(tabButtons[1].classList.contains('active')).toBe(false)
    expect(tabButtons[2].classList.contains('active')).toBe(true)
  })

  it('ignores keyboard shortcuts when alt is held', async () => {
    const client = makeClient()
    new TabManager(bar, panes, client)

    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(1)
    })

    window.dispatchEvent(
      new KeyboardEvent('keydown', { key: 't', metaKey: true, altKey: true, bubbles: true }),
    )

    expect(bar.querySelectorAll('.tab').length).toBe(1)
  })

  it('ignores Cmd+0 (not a valid tab index)', async () => {
    const client = makeClient()
    const manager = new TabManager(bar, panes, client)

    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(1)
    })

    manager.newTab()
    manager.newTab()
    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(3)
    })

    // Cmd+0 should do nothing (no switching to index -1 or 0)
    window.dispatchEvent(new KeyboardEvent('keydown', { key: '0', metaKey: true, bubbles: true }))

    // Active should still be the last tab
    const tabButtons = bar.querySelectorAll('.tab')
    expect(tabButtons[2].classList.contains('active')).toBe(true)
  })

  // ── close by middle-click ─────────────────────────────────────────────

  it('closes a tab on middle-click', async () => {
    const client = makeClient()
    const manager = new TabManager(bar, panes, client)

    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalled()
    })

    manager.newTab()
    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(2)
    })

    const tabButtons = bar.querySelectorAll('.tab')
    expect(tabButtons.length).toBe(2)

    const session0 = client._sessions[0]

    // Middle-click on the first tab
    tabButtons[0].dispatchEvent(new MouseEvent('mousedown', { button: 1, bubbles: true }))

    // Check it was closed
    expect(session0.close).toHaveBeenCalled()
    expect(bar.querySelectorAll('.tab').length).toBe(1)
  })
})

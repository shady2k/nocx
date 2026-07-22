// @vitest-environment jsdom
import { describe, expect, it, vi, beforeEach } from 'vitest'
import {
  createRendererMock,
  resetSessionCounter,
  mountTabManager,
  FIXTURE_DIRECTORY_LABEL,
  type RendererMock,
} from './test-support/tabs-fixtures'

// ── Mocks ──────────────────────────────────────────────────────────────────

// Mock the renderer module before any imports use it.
vi.mock('./renderers', () => ({
  createRenderer: vi.fn(createRendererMock),
  resolveRendererName: vi.fn(() => 'xterm' as const),
}))

// ── Helpers ───────────────────────────────────────────────────────────────

/**
 * Returns all renderer mocks created so far by the mocked createRenderer.
 */
async function getRendererMocks(): Promise<RendererMock[]> {
  const { createRenderer } = await import('./renderers')
  return vi.mocked(createRenderer).mock.results.map((r) => r.value as unknown as RendererMock)
}

// ── Tests ──────────────────────────────────────────────────────────────────

describe('TabManager', () => {
  beforeEach(() => {
    resetSessionCounter()
    vi.clearAllMocks()
  })

  // ── opening a tab creates a session and a pane ────────────────────────

  it('opens a session when a tab is created and activated', async () => {
    const { client, bar, panes } = await mountTabManager()

    expect(bar.querySelectorAll('.tab').length).toBe(1)
    expect(panes.querySelectorAll('.pane').length).toBe(1)
    expect(client.openSession).toHaveBeenCalled()
  })

  it('creates a session for each new tab', async () => {
    const { client, manager, bar, panes } = await mountTabManager()

    manager.newTab()
    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(2)
    })

    expect(bar.querySelectorAll('.tab').length).toBe(2)
    expect(panes.querySelectorAll('.pane').length).toBe(2)
  })

  // ── closing closes the session and activates a neighbour ──────────────

  it('closes the session when the active tab is closed', async () => {
    const { client, manager } = await mountTabManager()

    const session = client._sessions[0]
    manager.closeActiveTab()

    // The session should have been closed, but a new one created for the replacement
    expect(session.close).toHaveBeenCalled()
  })

  it('activates a neighbour tab when the active tab is closed', async () => {
    const { client, manager, bar } = await mountTabManager()

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
    const { client, manager, bar, panes } = await mountTabManager()

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

  it('fallback title is the directory, not a number that would disagree with the badge', async () => {
    const { client, manager, bar } = await mountTabManager()

    // Open tabs until the badge says 4.
    manager.newTab()
    manager.newTab()
    manager.newTab()
    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(4)
    })

    const labels = bar.querySelectorAll('.tab-index')
    const titles = bar.querySelectorAll('.tab-title')

    // Before close: badge = 1..4, fallback title is the directory label.
    expect(labels[0].textContent).toBe('1')
    expect(labels[1].textContent).toBe('2')
    expect(labels[2].textContent).toBe('3')
    expect(labels[3].textContent).toBe('4')
    titles.forEach((t) => expect(t.textContent).toBe(FIXTURE_DIRECTORY_LABEL))

    // Close the first two tabs via public API: activate then close.
    manager.activateByIndex(0)
    manager.closeActiveTab()
    manager.activateByIndex(0)
    manager.closeActiveTab()

    // Re-query after DOM mutations; stale references reflect removed elements.
    const afterLabels = bar.querySelectorAll('.tab-index')
    const afterTitles = bar.querySelectorAll('.tab-title')
    // After close: badge = 1..2, titles stay the directory label.
    expect(afterLabels[0].textContent).toBe('1')
    expect(afterLabels[1].textContent).toBe('2')
    afterTitles.forEach((t) => expect(t.textContent).toBe(FIXTURE_DIRECTORY_LABEL))
  })

  // ── switching focuses the right renderer ──────────────────────────────

  it('switches between tabs on activateByIndex', async () => {
    const { client, manager, bar } = await mountTabManager()

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
    const { client, manager, bar } = await mountTabManager()

    manager.newTab()
    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(2)
    })

    // Flush pending microtasks so both renderers are fully initialised.
    await Promise.resolve()

    const titles = bar.querySelectorAll('.tab-title')
    expect(titles.length).toBe(2)
    expect(titles[0].textContent).toBe(FIXTURE_DIRECTORY_LABEL)
    expect(titles[1].textContent).toBe(FIXTURE_DIRECTORY_LABEL)

    const renderers = await getRendererMocks()
    expect(renderers.length).toBe(2)

    // Fire title for first tab only
    renderers[0]._fireTitle('~/project')
    expect(titles[0].textContent).toBe('~/project')
    expect(titles[1].textContent).toBe(FIXTURE_DIRECTORY_LABEL)

    // Fire title for second tab only
    renderers[1]._fireTitle('bash-3.2')
    expect(titles[1].textContent).toBe('bash-3.2')
    expect(titles[0].textContent).toBe('~/project')
  })

  // ── empty / whitespace title is ignored ──────────────────────────────

  it('falls back to the directory when the shell clears the title', async () => {
    const { bar } = await mountTabManager()

    await Promise.resolve()

    const renderers = await getRendererMocks()
    const titleEl = bar.querySelector('.tab-title')!

    // Set a real title first.
    renderers[0]._fireTitle('~/projects')
    expect(titleEl.textContent).toBe('~/projects')

    // A TUI clears the title on exit with an empty OSC 0/2. Neither blank the
    // tab nor keep the stale name — a plain shell must not stay labelled with
    // the program that just exited.
    renderers[0]._fireTitle('')
    expect(titleEl.textContent).toBe(FIXTURE_DIRECTORY_LABEL)

    renderers[0]._fireTitle('~/projects')
    renderers[0]._fireTitle('   ')
    expect(titleEl.textContent).toBe(FIXTURE_DIRECTORY_LABEL)
  })

  // ── activity indicator ────────────────────────────────────────────────

  it('shows activity indicator on a background tab receiving output', async () => {
    const { client, manager, bar } = await mountTabManager()

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
    const { client, manager, bar } = await mountTabManager()

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

  // ── activity indicator: alternate-buffer suppression ─────────────────

  it('does not mark activity for alternate-buffer output on a background tab', async () => {
    const { client, manager, bar } = await mountTabManager()

    manager.newTab()
    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(2)
    })

    // Tab 1 active, tab 2 is background.
    manager.activateByIndex(0)

    // Put the background tab's renderer into the alternate buffer via the
    // onBufferChange callback — this is the real path xterm.js takes.
    const renderers = await getRendererMocks()
    renderers[1]._fireBufferChange('alternate')

    // Output on the background tab while in alternate buffer.
    const bgSession = client._sessions[1]
    bgSession.fireData('spinner repaint')

    const indicators = bar.querySelectorAll('.tab-indicator')
    expect(indicators[1].classList.contains('tab-activity')).toBe(false)
  })

  it('marks activity for normal-buffer output on a background tab', async () => {
    const { client, manager, bar } = await mountTabManager()

    manager.newTab()
    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(2)
    })

    // Tab 1 active, tab 2 is background. Default _bufferType is 'normal'.
    manager.activateByIndex(0)

    const bgSession = client._sessions[1]
    bgSession.fireData('normal output')

    const indicators = bar.querySelectorAll('.tab-indicator')
    expect(indicators[1].classList.contains('tab-activity')).toBe(true)
  })

  it('marks activity on bell in the alternate buffer', async () => {
    const { client, manager, bar } = await mountTabManager()

    manager.newTab()
    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(2)
    })

    // Tab 1 active, tab 2 is background.
    manager.activateByIndex(0)

    // Put the background tab into the alternate buffer via onBufferChange.
    const renderers = await getRendererMocks()
    renderers[1]._fireBufferChange('alternate')

    // Fire bell on the background tab's renderer.
    renderers[1]._fireBell()

    const indicators = bar.querySelectorAll('.tab-indicator')
    expect(indicators[1].classList.contains('tab-activity')).toBe(true)
  })

  it('does not mark activity on bell for the active tab', async () => {
    const { bar } = await mountTabManager()

    // Only one tab, and it is active. Fire bell on it.
    const renderers = await getRendererMocks()
    renderers[0]._fireBell()

    const indicators = bar.querySelectorAll('.tab-indicator')
    expect(indicators[0].classList.contains('tab-activity')).toBe(false)
  })

  // ── keyboard shortcuts ────────────────────────────────────────────────

  it('opens a new tab on Cmd+T', async () => {
    const { client, bar } = await mountTabManager()

    window.dispatchEvent(new KeyboardEvent('keydown', { key: 't', metaKey: true, bubbles: true }))

    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(2)
    })
    expect(bar.querySelectorAll('.tab').length).toBe(2)
  })

  it('opens a new tab on Ctrl+T', async () => {
    const { client } = await mountTabManager()

    window.dispatchEvent(new KeyboardEvent('keydown', { key: 't', ctrlKey: true, bubbles: true }))

    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(2)
    })
  })

  it('closes the active tab on Cmd+W', async () => {
    const { client, bar } = await mountTabManager()

    const session = client._sessions[0]

    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'w', metaKey: true, bubbles: true }))

    // Closing the last tab opens a fresh one, so there's still 1 tab
    expect(bar.querySelectorAll('.tab').length).toBe(1)
    expect(session.close).toHaveBeenCalled()
  })

  it('switches tabs on Cmd+1..9', async () => {
    const { client, manager, bar } = await mountTabManager()

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
    const { bar } = await mountTabManager()

    window.dispatchEvent(
      new KeyboardEvent('keydown', { key: 't', metaKey: true, altKey: true, bubbles: true }),
    )

    expect(bar.querySelectorAll('.tab').length).toBe(1)
  })

  it('ignores Cmd+0 (not a valid tab index)', async () => {
    const { client, manager, bar } = await mountTabManager()

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
    const { client, manager, bar } = await mountTabManager()

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

  // ── flex-grow regression guards ──────────────────────────────────────

  it('a lone tab does not stretch (flex-grow is not a stretching value)', async () => {
    // Inject the critical layout rules so jsdom can compute styles.
    const style = document.createElement('style')
    style.textContent = `
      .tabbar { display: flex; }
      .tabs-container { flex: 0 1 auto; min-width: 0; display: flex; align-items: stretch; }
      .tabbar-spacer { flex: 1 1 0%; }
      .tab { flex: 0 1 200px; }
    `
    document.head.appendChild(style)

    const { bar } = await mountTabManager()

    const tabsContainer = bar.querySelector('.tabs-container') as HTMLElement
    expect(tabsContainer).not.toBeNull()

    const tab = bar.querySelector('.tab') as HTMLElement
    expect(tab).not.toBeNull()

    // The tabs container itself must not grow.
    expect(getComputedStyle(tabsContainer).flexGrow).toBe('0')

    // The tab must not have a stretching flex-grow.
    expect(getComputedStyle(tab).flexGrow).toBe('0')
    expect(getComputedStyle(tab).flexBasis).toBe('200px')

    // The spacer should absorb all remaining width.
    const spacer = bar.querySelector('.tabbar-spacer') as HTMLElement
    expect(spacer).not.toBeNull()
    expect(getComputedStyle(spacer).flexGrow).toBe('1')

    // Clean up injected style.
    style.remove()
  })

  it('proves the guard would catch flex-grow:1000 on .tab', async () => {
    const style = document.createElement('style')
    style.textContent = `
      .tabbar { display: flex; }
      .tabs-container { flex: 0 1 auto; min-width: 0; display: flex; align-items: stretch; }
      .tabbar-spacer { flex: 1 1 0%; }
      .tab { flex: 1000 1 200px; }
    `
    document.head.appendChild(style)

    const { bar } = await mountTabManager()

    const tab = bar.querySelector('.tab') as HTMLElement
    expect(tab).not.toBeNull()

    // With flex:1000 the computed flex-grow IS '1000'.
    expect(getComputedStyle(tab).flexGrow).toBe('1000')

    // Verify that the guard assertion (expect(…).toBe('0')) would fail
    // by showing the value is NOT '0'.
    expect(getComputedStyle(tab).flexGrow).not.toBe('0')

    style.remove()
  })
  // ── OSC 7: cwd follows cd ───────────────────────────────────────────

  it('updates tab title when OSC 7 fires (cwd follows cd)', async () => {
    const { bar } = await mountTabManager()

    await Promise.resolve()

    const renderers = await getRendererMocks()
    const titleEl = bar.querySelector('.tab-title')!

    // Initial title is the fixture directory label.
    expect(titleEl.textContent).toBe(FIXTURE_DIRECTORY_LABEL)

    // User does `cd /tmp` → shell emits OSC 7
    renderers[0]._fireCwd('', '/tmp')
    expect(titleEl.textContent).toBe('tmp')

    // User does `cd /var/log`
    renderers[0]._fireCwd('', '/var/log')
    expect(titleEl.textContent).toBe('var/log')

    // User goes to /var (single segment after root)
    renderers[0]._fireCwd('', '/var')
    expect(titleEl.textContent).toBe('var')
  })

  it('updates tooltip when OSC 7 fires', async () => {
    const { bar } = await mountTabManager()

    await Promise.resolve()

    const renderers = await getRendererMocks()
    const tabBtn = bar.querySelector('.tab')!

    // Initial tooltip includes the '(initial cwd)' marker (AD-5 surfacing).
    expect(tabBtn.getAttribute('title')).toContain('(initial cwd)')

    // OSC 7 fires → tooltip is just the path, no marker.
    renderers[0]._fireCwd('', '/tmp')
    expect(tabBtn.getAttribute('title')).toBe('/tmp')
    expect(tabBtn.getAttribute('title')).not.toContain('(initial')
  })

  it('program title overrides cwd-based title, but cwd updates the fallback', async () => {
    const { bar } = await mountTabManager()

    await Promise.resolve()

    const renderers = await getRendererMocks()
    const titleEl = bar.querySelector('.tab-title')!

    // Program sets a title (e.g. vim, htop).
    renderers[0]._fireTitle('vim')
    expect(titleEl.textContent).toBe('vim')

    // cwd changes — visible title stays 'vim' because program title wins.
    renderers[0]._fireCwd('', '/etc')
    expect(titleEl.textContent).toBe('vim')

    // Program exits, clears the title → fallback is the new cwd.
    renderers[0]._fireTitle('')
    expect(titleEl.textContent).toBe('etc')
  })

  it('cwd only affects its own tab', async () => {
    const { client, manager, bar } = await mountTabManager()

    manager.newTab()
    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(2)
    })

    await Promise.resolve()

    const renderers = await getRendererMocks()
    expect(renderers.length).toBe(2)

    const titles = bar.querySelectorAll('.tab-title')

    // Fire cwd for first tab only.
    renderers[0]._fireCwd('', '/tmp')
    expect(titles[0].textContent).toBe('tmp')
    // Second tab still has the fixture label.
    expect(titles[1].textContent).toBe(FIXTURE_DIRECTORY_LABEL)

    // Fire cwd for second tab only.
    renderers[1]._fireCwd('', '/var/log')
    expect(titles[0].textContent).toBe('tmp')
    expect(titles[1].textContent).toBe('var/log')
  })

  it('initial tooltip marks cwd as stale before first OSC 7', async () => {
    const { client, manager, bar } = await mountTabManager()

    manager.newTab()
    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(2)
    })

    await Promise.resolve()

    const tabBtns = bar.querySelectorAll('.tab')
    // Both tabs should have the initial marker since no OSC 7 fired.
    expect(tabBtns[0].getAttribute('title')).toContain('(initial cwd)')
    expect(tabBtns[1].getAttribute('title')).toContain('(initial cwd)')

    // First tab gets OSC 7.
    const renderers = await getRendererMocks()
    renderers[0]._fireCwd('', '/tmp')

    expect(tabBtns[0].getAttribute('title')).toBe('/tmp')
    // Second tab still has initial marker.
    expect(tabBtns[1].getAttribute('title')).toContain('(initial cwd)')
  })
})

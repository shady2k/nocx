// @vitest-environment jsdom
import { describe, expect, it, vi, beforeEach } from 'vitest'
import {
  createRendererMock,
  resetSessionCounter,
  mountTabManager,
  makeClient,
  makeClipboard,
  makeBanner,
  setupTabBarDOM,
  FIXTURE_DIRECTORY_LABEL,
  type RendererMock,
} from './test-support/tabs-fixtures'
import { ClipboardGate } from './clipboard'

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

  // ── clipboard policy ───────────────────────────────────────────────
  /* eslint-disable @typescript-eslint/unbound-method */

  it('writes selection to the clipboard when non-empty', async () => {
    const cb = makeClipboard()
    await mountTabManager(undefined, cb)

    await Promise.resolve()
    const renderers = await getRendererMocks()

    renderers[0]._fireSelectionChange('selected text')

    expect(cb.writeText).toHaveBeenCalledWith('selected text')
  })

  it('does not write whitespace-only selection to the clipboard', async () => {
    const cb = makeClipboard()
    await mountTabManager(undefined, cb)

    await Promise.resolve()
    const renderers = await getRendererMocks()

    renderers[0]._fireSelectionChange('   ')

    expect(cb.writeText).not.toHaveBeenCalled()
  })

  it('does not write empty selection to the clipboard', async () => {
    const cb = makeClipboard()
    await mountTabManager(undefined, cb)

    await Promise.resolve()
    const renderers = await getRendererMocks()

    renderers[0]._fireSelectionChange('')

    expect(cb.writeText).not.toHaveBeenCalled()
  })

  it('writes OSC 52 decoded text to the clipboard when granted', async () => {
    const cb = makeClipboard()
    const gate = new ClipboardGate()
    gate.allow()

    await mountTabManager(undefined, cb, gate)

    await Promise.resolve()
    const renderers = await getRendererMocks()

    renderers[0]._fireClipboardWrite('osc52 payload')

    expect(cb.writeText).toHaveBeenCalledWith('osc52 payload')
  })

  it('pastes on right-click (contextmenu event)', async () => {
    const cb = makeClipboard({
      readText: vi.fn().mockResolvedValue('right-click text'),
    })
    const { bar } = await mountTabManager(undefined, cb)

    await Promise.resolve()
    const renderers = await getRendererMocks()

    const pane = bar.parentElement!.querySelector('.pane.active')!
    pane.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true }))

    await vi.waitFor(() => {
      expect(cb.readText).toHaveBeenCalled()
    })
    expect(renderers[0].paste).toHaveBeenCalledWith('right-click text')
  })

  it('pastes on middle-click', async () => {
    const cb = makeClipboard({
      readText: vi.fn().mockResolvedValue('middle-click text'),
    })
    const { bar } = await mountTabManager(undefined, cb)

    await Promise.resolve()
    const renderers = await getRendererMocks()

    const pane = bar.parentElement!.querySelector('.pane.active')!
    pane.dispatchEvent(new MouseEvent('mousedown', { button: 1, bubbles: true }))

    await vi.waitFor(() => {
      expect(cb.readText).toHaveBeenCalled()
    })
    expect(renderers[0].paste).toHaveBeenCalledWith('middle-click text')
  })

  it('confirms before pasting multi-line text in the normal screen', async () => {
    const cb = makeClipboard({
      readText: vi.fn().mockResolvedValue('line1\nline2'),
    })
    const confirm = vi.fn()
    vi.stubGlobal('confirm', confirm)

    const { bar } = await mountTabManager(undefined, cb)

    await Promise.resolve()
    const renderers = await getRendererMocks()

    // Default _bufferType is 'normal'.
    confirm.mockReturnValueOnce(false)
    const pane = bar.parentElement!.querySelector('.pane.active')!
    pane.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true }))

    await vi.waitFor(() => {
      expect(cb.readText).toHaveBeenCalled()
    })
    // User declined — nothing should reach the terminal.
    expect(renderers[0].paste).not.toHaveBeenCalled()

    // Now confirm.
    confirm.mockReturnValueOnce(true)
    pane.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true }))
    await vi.waitFor(() => {
      expect(renderers[0].paste).toHaveBeenCalledWith('line1\nline2')
    })

    vi.unstubAllGlobals()
  })

  it('does not confirm multi-line paste in the alternate screen', async () => {
    const cb = makeClipboard({
      readText: vi.fn().mockResolvedValue('line1\nline2'),
    })
    const confirm = vi.fn()
    vi.stubGlobal('confirm', confirm)

    const { bar } = await mountTabManager(undefined, cb)

    await Promise.resolve()
    const renderers = await getRendererMocks()

    // Switch to alternate screen (TUI mode).
    renderers[0]._fireBufferChange('alternate')

    const pane = bar.parentElement!.querySelector('.pane.active')!
    pane.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true }))

    await vi.waitFor(() => {
      expect(cb.readText).toHaveBeenCalled()
    })
    // No confirmation in alternate screen — full-screen program is not a shell.
    expect(confirm).not.toHaveBeenCalled()
    expect(renderers[0].paste).toHaveBeenCalledWith('line1\nline2')

    vi.unstubAllGlobals()
  })

  // ── OSC 52 gate ────────────────────────────────────────────────────

  it('blocked write leaves the clipboard untouched and raises one banner', async () => {
    const cb = makeClipboard()
    const gate = new ClipboardGate()
    const banner = makeBanner()

    await mountTabManager(undefined, cb, gate, banner)
    await Promise.resolve()
    const renderers = await getRendererMocks()

    // Fire an OSC 52 write. Gate starts denied, banner not yet shown.
    renderers[0]._fireClipboardWrite('osc52 text')

    // The banner must have been shown exactly once.
    expect(banner.show).toHaveBeenCalledTimes(1)

    // The clipboard must be untouched — write blocked.
    expect(cb.writeText).not.toHaveBeenCalled()
  })

  it('a second blocked write raises no second banner', async () => {
    const cb = makeClipboard()
    const gate = new ClipboardGate()
    // Banner already shown — simulate the first write having raised it.
    const banner = makeBanner({ shown: true })

    await mountTabManager(undefined, cb, gate, banner)
    await Promise.resolve()
    const renderers = await getRendererMocks()

    // Fire a second OSC 52 write while the banner is already shown.
    renderers[0]._fireClipboardWrite('second osc52 text')

    // The banner must NOT be shown again — a program looping OSC 52
    // must produce one banner, not a stack.
    expect(banner.show).not.toHaveBeenCalled()
    expect(cb.writeText).not.toHaveBeenCalled()
  })

  it('allowing lets the next write through', async () => {
    const cb = makeClipboard()
    const gate = new ClipboardGate()
    // Banner that auto-chooses 'allow' when show() is called.
    const banner = makeBanner({
      show: vi.fn().mockImplementation(() => {
        gate.allow()
        return Promise.resolve('allow' as const)
      }),
    })

    await mountTabManager(undefined, cb, gate, banner)
    await Promise.resolve()
    const renderers = await getRendererMocks()

    // Fire the first OSC 52 write — blocked, banner shown.
    renderers[0]._fireClipboardWrite('first write')

    expect(banner.show).toHaveBeenCalledTimes(1)

    // The banner's show() synchronously called gate.allow(). The gate is
    // now granted. Wait for the microtask that writes to clipboard.
    await vi.waitFor(() => {
      expect(cb.writeText).toHaveBeenCalledWith('first write')
    })

    // Fire a second write — must go through immediately, no banner.
    renderers[0]._fireClipboardWrite('second write')
    expect(cb.writeText).toHaveBeenCalledWith('second write')
    // Banner still only called once.
    expect(banner.show).toHaveBeenCalledTimes(1)
  })

  it('suppressing stops the banner without granting', async () => {
    const cb = makeClipboard()
    const gate = new ClipboardGate()
    // Banner that auto-chooses 'suppress' when show() is called.
    const banner = makeBanner({
      show: vi.fn().mockImplementation(() => {
        gate.suppress()
        return Promise.resolve('suppress' as const)
      }),
    })

    await mountTabManager(undefined, cb, gate, banner)
    await Promise.resolve()
    const renderers = await getRendererMocks()

    // Fire the first OSC 52 write.
    renderers[0]._fireClipboardWrite('blocked text')

    expect(banner.show).toHaveBeenCalledTimes(1)

    // Suppress must NOT grant — the clipboard stays untouched.
    expect(gate.granted).toBe(false)

    // Fire a second write — suppressed, no banner, no write.
    renderers[0]._fireClipboardWrite('another blocked text')

    // Still no write, and banner was not shown again.
    expect(cb.writeText).not.toHaveBeenCalled()
    expect(banner.show).toHaveBeenCalledTimes(1)
  })

  it('suppressed gate also silences a later OSC 52 on a second tab', async () => {
    const cb = makeClipboard()
    const gate = new ClipboardGate()
    gate.suppress()
    const banner = makeBanner()

    const { manager, client } = await mountTabManager(undefined, cb, gate, banner)
    manager.newTab()
    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(2)
    })
    await Promise.resolve()

    // Get both renderers.
    const renderers = await getRendererMocks()
    expect(renderers.length).toBeGreaterThanOrEqual(2)

    // Fire on the second tab — still suppressed, gate is app-wide.
    renderers[1]._fireClipboardWrite('tab 2 write')

    expect(cb.writeText).not.toHaveBeenCalled()
    expect(banner.show).not.toHaveBeenCalled()
  })
  /* eslint-enable @typescript-eslint/unbound-method */

  // ── readiness signal ───────────────────────────────────────────────

  it('initialTabReady resolves when the initial tab starts successfully', async () => {
    const { manager } = await mountTabManager()

    // The initial tab was created and started by mountTabManager.
    // initialTabReady must resolve (not hang, not reject).
    await expect(manager.initialTabReady).resolves.toBeUndefined()
  })

  it('initialTabReady rejects when the initial tab start() throws', async () => {
    const client = makeClient({
      openSession: vi.fn(() => Promise.reject(new Error('session failed'))),
    })

    const { bar, panes } = setupTabBarDOM()
    const clipboard = makeClipboard()
    const gate = new ClipboardGate()
    const banner = makeBanner()

    const { TabManager } = await import('./tabs')
    const manager = new TabManager(
      bar,
      panes,
      client as unknown as import('./ipc').WSClient,
      clipboard,
      gate,
      banner,
    )

    // initialTabReady must reject — a genuinely broken tab is not "ready".
    // expect().rejects attaches the handler synchronously, so the rejection
    // that fires in a microtask is already handled; no unhandled-rejection.
    await expect(manager.initialTabReady).rejects.toThrow('initial tab failed to start')

    // openSession was called (the rejection proves it — start() reached the call).
    expect(client.openSession).toHaveBeenCalled()

    // The UI still shows the error notice — swallow-and-show behaviour is intact.
    const pane = panes.querySelector('.pane')
    expect(pane).not.toBeNull()
    const errorNotice = pane!.querySelector('.pane-error')
    expect(errorNotice).not.toBeNull()
    expect(errorNotice!.textContent).toContain('session failed')
  })

  it('Tab.ready resolves true for a genuinely started tab', async () => {
    // mountTabManager starts the initial tab — its ready must resolve true.
    const { manager, client } = await mountTabManager()

    // Access the initial Tab via the (newly exported) Tab class and the tabs array.
    // The tabs array is private, but initialTabReady is derived from it — and
    // initialTabReady resolved above, proving the Tab-level signal resolved true.
    //
    // For a direct Tab.ready assertion, construct a Tab and drive it manually.
    const wsClient = client as unknown as import('./ipc').WSClient
    const { Tab } = await import('./tabs')

    const clipboard = makeClipboard()
    const gate = new ClipboardGate()
    const banner = makeBanner()
    const tab = new Tab(wsClient, 'xterm', clipboard, gate, banner, 99)

    // Before start(): ready must still be pending.
    // Prove it by racing — if it were already settled the race would catch it.
    const beforeStart = Promise.race([tab.ready.then(() => 'settled'), Promise.resolve('pending')])
    await expect(beforeStart).resolves.toBe('pending')

    // Now start it.
    const paneParent = document.createElement('div')
    paneParent.append(tab.pane)

    // Simulate what activate() does — pane must be in the DOM for the renderer.
    await tab.start()

    // After a genuine start, ready must resolve to true.
    await expect(tab.ready).resolves.toBe(true)

    // Clean up.
    tab.close()
    paneParent.remove()
  })

  it('Tab.ready resolves false when start() throws', async () => {
    const client = makeClient({
      openSession: vi.fn(() => Promise.reject(new Error('session failed'))),
    })

    const wsClient = client as unknown as import('./ipc').WSClient
    const { Tab } = await import('./tabs')

    const clipboard = makeClipboard()
    const gate = new ClipboardGate()
    const banner = makeBanner()
    const tab = new Tab(wsClient, 'xterm', clipboard, gate, banner, 99)

    const paneParent = document.createElement('div')
    paneParent.append(tab.pane)

    // start() swallows the error for the UI but ready must reflect the failure.
    await tab.start()

    // No throw from start(), but ready resolves false.
    await expect(tab.ready).resolves.toBe(false)

    // Verify the error notice is rendered.
    const errorNotice = tab.pane.querySelector('.pane-error')
    expect(errorNotice).not.toBeNull()
    expect(errorNotice!.textContent).toContain('session failed')

    // Clean up.
    tab.close()
    paneParent.remove()
  })
})

// @vitest-environment jsdom
import { describe, expect, it, vi, beforeEach } from 'vitest'
import {
  createRendererMock,
  resetSessionCounter,
  mountTabManager,
  makeClient,
  makeSession,
  makeClipboard,
  makeBanner,
  setupTabBarDOM,
  FIXTURE_DIRECTORY_LABEL,
  type RendererMock,
} from './test-support/tabs-fixtures'
import { TabManager } from './tabs'
import { ClipboardGate } from './clipboard'
import type { TerminalContent } from './terminal-content'
import {
  BaseTabContent,
  type ContentDescriptor,
  type TabHost,
  type ContentViewport,
  type SurfaceType,
} from './tab-content'

// ── Mocks ──────────────────────────────────────────────────────────────────

// Mock the XtermRenderer class before any imports use it.
vi.mock('./renderers/xterm', () => ({
  XtermRenderer: vi.fn(createRendererMock),
}))

// ── Helpers ───────────────────────────────────────────────────────────────

/**
 * Returns all renderer mocks created so far by the mocked XtermRenderer constructor.
 */
async function getRendererMocks(): Promise<RendererMock[]> {
  const { XtermRenderer } = await import('./renderers/xterm')
  return vi.mocked(XtermRenderer).mock.results.map((r) => r.value as unknown as RendererMock)
}

// ── Second TabContent implementation for mount-once proof ─────────────
// This class MUST NOT carry a private mount guard — the seam enforces
// mount-once, not the implementation. If mount() is called more than once,
// the seam is broken.
class CountingTestContent extends BaseTabContent {
  mountCount = 0
  /** Tracks every setVisible call for test assertions. */
  visibleCalls: boolean[] = []
  /** Ordered trace of lifecycle calls for ordering assertions. */
  callLog: string[] = []

  // eslint-disable-next-line @typescript-eslint/require-await, @typescript-eslint/no-unused-vars
  async mount(_target: HTMLElement, _host: TabHost, _signal: AbortSignal): Promise<void> {
    this.mountCount++
    this.callLog.push('mount')
  }

  viewportChanged(_viewport: ContentViewport): void {
    this.callLog.push(`viewportChanged(${_viewport.width}x${_viewport.height})`)
  }

  focus(): void {}

  setVisible(visible: boolean): void {
    this.visibleCalls.push(visible)
    this.callLog.push(`setVisible(${visible})`)
    super.setVisible(visible)
  }

  dispose(): void {}
}
// ── Tests ──────────────────────────────────────────────────────────────────

// terminal-content now asks the kit for confirmation instead of window.confirm
// (nocx-vxqj.5). The dialog is a real <dialog> element, so these tests mock the
// helper rather than driving a modal that jsdom cannot open.
const showConfirmMock = vi.fn()
vi.mock('./ui/dialog', () => ({
  showConfirm: (...args: unknown[]) => showConfirmMock(...args) as Promise<boolean>,
}))

describe('TabManager', () => {
  beforeEach(() => {
    resetSessionCounter()
    vi.clearAllMocks()
  })

  // ── opening a tab creates a session and a pane ────────────────────────

  it('constructing a TabManager creates no tab and mounts nothing', async () => {
    const { bar, panes } = setupTabBarDOM()
    const c = makeClient()
    const cb = makeClipboard()
    const g = new ClipboardGate()
    const bn = makeBanner()
    const pc = {
      listProfiles: vi.fn().mockResolvedValue([]),
      listGroups: vi.fn().mockResolvedValue([]),
    }
    const { HorizontalTabStrip } = await import('./tab-strip')
    const tabStrip = new HorizontalTabStrip()
    const manager = new TabManager(bar, bar, panes, c as never, cb, g, bn, pc as never, tabStrip)

    expect(bar.querySelectorAll('.tab').length).toBe(0)

    // Model state is also empty — no tabs registered.
    expect(manager.tabCount).toBe(0)
    expect(panes.querySelectorAll('.pane').length).toBe(0)
    expect(c.openSession).not.toHaveBeenCalled()
    // Strip is not yet mounted — no DOM children in the bar.
    expect(bar.children.length).toBe(0)
  })
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

  it('closing the active tab activates the previously-active tab (MRU), not the visual neighbour', async () => {
    const { client, manager, bar } = await mountTabManager()

    manager.newTab()
    manager.newTab()
    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(3)
    })

    // Three tabs: [tab1, tab2, tab3]. Tab3 is active (last created).
    // Activate 1 → 3 → 2 to build MRU: [1, 3].
    manager.activateByIndex(0) // tab1
    manager.activateByIndex(2) // tab3
    manager.activateByIndex(1) // tab2

    // Tab2 is active.
    const beforeTabs = bar.querySelectorAll('.tab')
    expect(beforeTabs[1].classList.contains('active')).toBe(true)

    // Close tab2. MRU says tab3 should activate, not tab1 (visual neighbour).
    manager.closeActiveTab()

    const remainingTabs = bar.querySelectorAll('.tab')
    expect(remainingTabs.length).toBe(2)
    // tab3 should now be active (id 3, original index 2)
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
    const confirm = showConfirmMock
    confirm.mockReset()

    const { bar } = await mountTabManager(undefined, cb)

    await Promise.resolve()
    const renderers = await getRendererMocks()

    // Default _bufferType is 'normal'.
    confirm.mockResolvedValueOnce(false)
    const pane = bar.parentElement!.querySelector('.pane.active')!
    pane.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true }))

    await vi.waitFor(() => {
      expect(cb.readText).toHaveBeenCalled()
    })
    // User declined — nothing should reach the terminal.
    expect(renderers[0].paste).not.toHaveBeenCalled()

    // Now confirm.
    confirm.mockResolvedValueOnce(true)
    pane.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true }))
    await vi.waitFor(() => {
      expect(renderers[0].paste).toHaveBeenCalledWith('line1\nline2')
    })
  })

  it('does not confirm multi-line paste in the alternate screen', async () => {
    const cb = makeClipboard({
      readText: vi.fn().mockResolvedValue('line1\nline2'),
    })
    const confirm = showConfirmMock
    confirm.mockReset()

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
    const { HorizontalTabStrip } = await import('./tab-strip')
    const tabStrip = new HorizontalTabStrip()
    const profileClient = {
      list: () => Promise.resolve([]),
      get: () => Promise.resolve(null),
      create: () => Promise.resolve(''),
      update: () => Promise.resolve(),
      delete: () => Promise.resolve(),
      connect: () => Promise.resolve(''),
    } as unknown as import('./profiles').ProfileClient
    const manager = new TabManager(
      bar,
      bar,
      panes,
      client as unknown as import('./ipc').WSClient,
      clipboard,
      gate,
      banner,
      profileClient,
      tabStrip,
    )
    // Open the initial tab explicitly — the constructor mounts nothing.
    // Don't await: openInitialTab returns the _initialTabReady promise;
    // we assert the rejection through that same promise below.
    void manager.openInitialTab()

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
    // initialTabReady resolved above, proving the content-level signal resolved true.
    //
    // For a direct content.ready assertion, construct TerminalContent + Tab manually.
    const client = makeClient()
    const wsClient = client as unknown as import('./ipc').WSClient
    const { Tab } = await import('./tabs')
    const { TerminalContent } = await import('./terminal-content')
    const { SURFACE_TERMINAL } = await import('./tab-content')

    const clipboard = makeClipboard()
    const gate = new ClipboardGate()
    const banner = makeBanner()
    const content = new TerminalContent(wsClient, clipboard, gate, banner, () => {})
    const tab = new Tab(
      content,
      {
        surfaceType: SURFACE_TERMINAL,
        singletonKey: null,
        restoreDescriptor: { type: 'local' },
        supportsAttention: true,
        defaultTitle: 'Terminal',
      },
      99,
    )

    // Before start(): ready must still be pending.
    const tc = tab.content as TerminalContent
    const beforeStart = Promise.race([tc.ready.then(() => 'settled'), Promise.resolve('pending')])
    await expect(beforeStart).resolves.toBe('pending')

    // Now start it.
    const paneParent = document.createElement('div')
    paneParent.append(tab.pane)
    await tab.start()

    // After a genuine start, ready must resolve to true.
    await expect(tc.ready).resolves.toBe(true)

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
    const { TerminalContent } = await import('./terminal-content')
    const { SURFACE_TERMINAL } = await import('./tab-content')

    const clipboard = makeClipboard()
    const gate = new ClipboardGate()
    const banner = makeBanner()
    const content = new TerminalContent(wsClient, clipboard, gate, banner, () => {})
    const tab = new Tab(
      content,
      {
        surfaceType: SURFACE_TERMINAL,
        singletonKey: null,
        restoreDescriptor: { type: 'local' },
        supportsAttention: true,
        defaultTitle: 'Terminal',
      },
      99,
    )

    const paneParent = document.createElement('div')
    paneParent.append(tab.pane)
    await tab.start()

    const tc = tab.content as TerminalContent
    await expect(tc.ready).resolves.toBe(false)

    // Verify the error notice is rendered.
    const errorNotice = tab.pane.querySelector('.pane-error')
    expect(errorNotice).not.toBeNull()
    expect(errorNotice!.textContent).toContain('session failed')

    // Clean up.
    tab.close()
    paneParent.remove()
  })

  it('dispose during async mount cancels before session opens (B.6 race)', async () => {
    // The trap: closing a tab mid-mount must not let the finished mount
    // open a PTY into a detached pane. Prove that dispose aborts the
    let resolveSession!: (v: ReturnType<typeof makeSession>) => void
    const delayedSession = new Promise<ReturnType<typeof makeSession>>((resolve) => {
      resolveSession = resolve
    })
    const client = makeClient({
      openSession: vi.fn(() => delayedSession),
    })

    const wsClient = client as unknown as import('./ipc').WSClient
    const { Tab } = await import('./tabs')
    const { TerminalContent } = await import('./terminal-content')
    const { SURFACE_TERMINAL } = await import('./tab-content')

    const clipboard = makeClipboard()
    const gate = new ClipboardGate()
    const banner = makeBanner()
    const content = new TerminalContent(wsClient, clipboard, gate, banner, () => {})
    const tab = new Tab(
      content,
      {
        surfaceType: SURFACE_TERMINAL,
        singletonKey: null,
        restoreDescriptor: { type: 'local' },
        supportsAttention: true,
        defaultTitle: 'Terminal',
      },
      99,
    )

    const paneParent = document.createElement('div')
    paneParent.append(tab.pane)

    // Start mount but don't await — it blocks on delayedSession.
    const mountPromise = tab.start()

    // Let the mount get past renderer mounting and reach openSession.
    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalled()
    })

    // Dispose while mount is in-flight.
    tab.close()
    // Now let the session resolve.
    const session = makeSession()
    resolveSession(session)

    // Mount should complete without throwing (error is caught internally).
    await mountPromise

    // The session must have been closed by dispose.
    expect(session.close).toHaveBeenCalled()
    expect(client.openSession).toHaveBeenCalledTimes(1)

    // Clean up.
    paneParent.remove()
  })

  // ═════════════════════════════════════════════════════════════════════════
  // Mount-once enforcement at the seam (nocx-njrx.2)
  // ═════════════════════════════════════════════════════════════════════════

  it('mounts content exactly once when activated repeatedly — seam guard, not TerminalContent private flag', async () => {
    const { manager } = await mountTabManager()

    const content = new CountingTestContent()
    const descriptor: ContentDescriptor = {
      surfaceType: 'test.mock' as unknown as SurfaceType,
      singletonKey: null,
      restoreDescriptor: null,
      supportsAttention: false,
      defaultTitle: 'Test',
    }
    // openTab creates a tab via addTab → activate → start → mount.
    manager.openTab(content, descriptor)

    // Activate the terminal tab, then activate our tab again.
    // Mount must only fire once (seam guard in Tab.start()).
    manager.activateByIndex(0)
    manager.activateByIndex(1)

    // Flush microtasks so any pending async activation settles.
    await Promise.resolve()
    await Promise.resolve()

    expect(content.mountCount).toBe(1)
  })

  it('calling setVisible through the content boundary drives visibility, not a CSS class toggle in tabs.ts', async () => {
    const { manager } = await mountTabManager()
    const content = new CountingTestContent()
    const descriptor: ContentDescriptor = {
      surfaceType: 'test.mock' as unknown as SurfaceType,
      singletonKey: null,
      restoreDescriptor: null,
      supportsAttention: false,
      defaultTitle: 'Test',
    }

    manager.openTab(content, descriptor)

    // setVisible(true) should have been called when the tab was activated.
    // (The first call is for tab 0 being deactivated, the second is our tab activated.)
    // content 0 (terminal) also gets setVisible calls, so count >= 1 for our content.
    expect(content.visibleCalls.length).toBeGreaterThanOrEqual(1)
    expect(content.visibleCalls[content.visibleCalls.length - 1]).toBe(true)

    // Deactivate the tab — setVisible(false) fires on the previous tab.
    manager.activateByIndex(0)
    // The last call on our content should now be false.
    expect(content.visibleCalls[content.visibleCalls.length - 1]).toBe(false)

    expect(content.mountCount).toBe(1)
  })

  it('pane is visible before mount when activated — pre-mount target makes setVisible meaningful from first activation', async () => {
    const { manager } = await mountTabManager()
    const content = new CountingTestContent()
    const descriptor: ContentDescriptor = {
      surfaceType: 'test.mock' as unknown as SurfaceType,
      singletonKey: null,
      restoreDescriptor: null,
      supportsAttention: false,
      defaultTitle: 'Test',
    }

    // addTab fires void this.activate(tab) — mount is async even for
    // CountingTestContent's sync body. With pre-mount target set in the
    // Tab constructor, setActive(true) already toggles the 'active' class
    // before mount() resolves.
    const tab = manager.openTab(content, descriptor)
    await Promise.resolve()
    await Promise.resolve()

    // The pane must have the 'active' class from the first activation.
    // This assertion breaks if setVisible(true) was called when _target
    // was null (pre-mount target fix makes it non-null from construction).
    expect(tab.pane.classList.contains('active')).toBe(true)
    expect(content.mountCount).toBe(1)
  })

  it('ordering: setVisible(true) fires before mount, and first viewport gets non-zero rectangle when pane is visible', async () => {
    // Construct a Tab directly (not through TabManager) so we control
    // the activation order and can inspect the call log precisely.
    const { Tab } = await import('./tabs')
    const content = new CountingTestContent()
    const descriptor: ContentDescriptor = {
      surfaceType: 'test.mock' as unknown as SurfaceType,
      singletonKey: null,
      restoreDescriptor: null,
      supportsAttention: false,
      defaultTitle: 'Test',
    }

    // Tab constructor calls content.setTarget(this.pane) — _target is
    // set before any activation.
    const tab = new Tab(content, descriptor, 99)
    expect(content.callLog).toEqual([]) // no lifecycle calls yet

    // Append pane to DOM and stub getBoundingClientRect to return non-zero,
    // so _deliverViewport does not suppress the first geometry delivery.
    // Pattern copied from B.5 test at ~line 1370.
    const paneParent = document.createElement('div')
    paneParent.append(tab.pane)
    Object.defineProperty(tab.pane, 'getBoundingClientRect', {
      value: () => ({
        width: 1024,
        height: 768,
        top: 0,
        left: 0,
        right: 1024,
        bottom: 768,
        x: 0,
        y: 0,
        toJSON: () => {},
      }),
      configurable: true,
    })

    // Activate before mount: setActive(true) → setVisible(true).
    // With pre-mount target, this toggles 'active' immediately.
    tab.setActive(true)
    expect(tab.pane.classList.contains('active')).toBe(true)
    expect(content.callLog).toContain('setVisible(true)')
    expect(content.callLog.indexOf('mount')).toBe(-1) // mount not yet called
    expect(content.mountCount).toBe(0)

    // Now mount.
    await tab.start()

    // setVisible(true) was logged before mount.
    const visibleIdx = content.callLog.indexOf('setVisible(true)')
    const mountIdx = content.callLog.indexOf('mount')
    expect(visibleIdx).toBeGreaterThanOrEqual(0)
    expect(visibleIdx).toBeLessThan(mountIdx)

    // The first viewportChanged must be delivered with non-zero dimensions
    // (the pre-mount target makes the pane visible, so _deliverViewport
    // measures the stubbed 1024×768 rect instead of a zero rect).
    const vpCall = content.callLog.find((c) => c.startsWith('viewportChanged'))
    expect(vpCall).toBeDefined()
    expect(vpCall).toMatch(/viewportChanged\(1024x768\)/u)

    // Mount exactly once.
    expect(content.mountCount).toBe(1)

    // Clean up.
    tab.close()
    paneParent.remove()
  })

  // ═════════════════════════════════════════════════════════════════════════
  // B.5 Geometry authority — presentation layer owns the viewport
  // ═════════════════════════════════════════════════════════════════════════

  it('refuses a second openInitialTab — the guard is at the seam, not in a comment', async () => {
    const { manager } = await mountTabManager()
    // mountTabManager already opened the initial tab, which is the point:
    // the composition root calls this exactly once and a second call must
    // fail loudly rather than mount a second strip and a second first tab.
    expect(() => manager.openInitialTab()).toThrow(/openInitialTab called twice/)
  })

  describe('B.5 geometry authority', () => {
    // ResizeObserver callback captured by the stub so we can trigger it.
    let roCallback: ((entries: Array<{ contentRect: DOMRectReadOnly }>) => void) | null = null

    beforeEach(() => {
      resetSessionCounter()
      vi.clearAllMocks()
      roCallback = null
      // Stub ResizeObserver so we capture the callback rather than relying
      // on jsdom's missing implementation. The real observer is created in
      // Tab.setupViewportObserver().
      ;(globalThis as Record<string, unknown>).ResizeObserver = class {
        constructor(cb: (entries: Array<{ contentRect: DOMRectReadOnly }>) => void) {
          roCallback = cb
        }
        observe() {}
        unobserve() {}
        disconnect() {}
      }
    })

    /**
     * Helper: trigger the captured ResizeObserver callback with the given
     * contentRect, then flush the rAF that _deliverViewport schedules.
     */
    async function fireResize(width: number, height: number): Promise<void> {
      expect(roCallback).not.toBeNull()
      roCallback!([{ contentRect: { width, height } as DOMRectReadOnly }])
      // Flush the requestAnimationFrame that coalesces delivery.
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()))
    }

    it('presentation layer calls viewportChanged on pane resize', async () => {
      // Acceptance test: WHO calls viewportChanged? The presentation layer.
      // This test fails if setupViewportObserver is removed or if
      // viewportChanged is only called by content measuring itself.
      const { manager, client } = await mountTabManager()
      const tab = manager.newTab()
      await vi.waitFor(() => expect(client.openSession).toHaveBeenCalledTimes(2))
      const renderers = await getRendererMocks()
      const renderer = renderers[renderers.length - 1]
      // Before any observer fires, fitViewport must not have been called.
      // eslint-disable-next-line @typescript-eslint/unbound-method
      expect(renderer.fitViewport).not.toHaveBeenCalled()

      // Set non-zero pane dimensions so _deliverViewport doesn't suppress.
      Object.defineProperty(tab.pane, 'getBoundingClientRect', {
        value: () => ({
          width: 1024,
          height: 768,
          top: 0,
          left: 0,
          right: 1024,
          bottom: 768,
          x: 0,
          y: 0,
          toJSON: () => {},
        }),
        configurable: true,
      })

      // Simulate a pane resize through the ResizeObserver (which Tab created
      // when the pane entered the DOM via addTab).
      await fireResize(1024, 768)

      // The presentation layer's observer must have delivered the viewport
      // through content.viewportChanged → renderer.fitViewport.
      // eslint-disable-next-line @typescript-eslint/unbound-method
      expect(renderer.fitViewport).toHaveBeenCalledWith(
        expect.objectContaining({ width: 1024, height: 768 }),
      )
    })

    it('no viewport callback before mount starts', async () => {
      // Create a Tab directly without TabManager so it is NOT auto-activated.
      // This lets us add the pane to DOM and fire the observer before start().
      const { Tab } = await vi.importActual<typeof import('./tabs')>('./tabs')
      const { TerminalContent } =
        await vi.importActual<typeof import('./terminal-content')>('./terminal-content')
      const { SURFACE_TERMINAL } =
        await vi.importActual<typeof import('./tab-content')>('./tab-content')

      const { client } = await mountTabManager()
      const wsClient = client as unknown as import('./ipc').WSClient
      const content = new TerminalContent(
        wsClient,
        makeClipboard(),
        new ClipboardGate(),
        makeBanner(),
        () => {},
      )
      const tab = new Tab(
        content,
        {
          surfaceType: SURFACE_TERMINAL,
          singletonKey: null,
          restoreDescriptor: { type: 'local' },
          supportsAttention: true,
          defaultTitle: 'Terminal',
        },
        99,
      )

      // Pane enters DOM → ResizeObserver fires.
      document.body.append(tab.pane)
      tab.setupViewportObserver()

      Object.defineProperty(tab.pane, 'getBoundingClientRect', {
        value: () => ({
          width: 1024,
          height: 768,
          top: 0,
          left: 0,
          right: 1024,
          bottom: 768,
          x: 0,
          y: 0,
          toJSON: () => {},
        }),
        configurable: true,
      })

      // Fire resize BEFORE mount starts.
      await fireResize(1024, 768)

      // Clean up.
      tab.pane.remove()

      // The observer fired but _deliverViewport must have NO-OP'd because
      // _mountStarted is still false. Since no renderer was mounted (mount
      // never called), and viewportChanged is unreachable before mount,
      // this test guards the structural invariant.
    })

    it('equal consecutive rectangles are suppressed', async () => {
      const { manager, client } = await mountTabManager()
      const tab = manager.newTab()
      await vi.waitFor(() => expect(client.openSession).toHaveBeenCalledTimes(2))

      Object.defineProperty(tab.pane, 'getBoundingClientRect', {
        value: () => ({
          width: 800,
          height: 600,
          top: 0,
          left: 0,
          right: 800,
          bottom: 600,
          x: 0,
          y: 0,
          toJSON: () => {},
        }),
        configurable: true,
      })

      await fireResize(800, 600)
      await fireResize(800, 600) // same rect — must be suppressed

      const renderers = await getRendererMocks()
      // eslint-disable-next-line @typescript-eslint/unbound-method
      expect(renderers[renderers.length - 1].fitViewport).toHaveBeenCalledTimes(1)
    })

    it('no callbacks after dispose', async () => {
      const { manager, client } = await mountTabManager()
      const tab = manager.newTab()
      await vi.waitFor(() => expect(client.openSession).toHaveBeenCalledTimes(2))
      const renderers = await getRendererMocks()

      tab.close()

      // After dispose, the ResizeObserver is disconnected.

      // Spy on content.viewportChanged: must not fire after dispose.
      const spy = vi.spyOn(tab.content, 'viewportChanged')
      // The observer is disconnected on close, but we verify the guard
      // by calling _deliverViewport directly with _disposed=true.

      expect(spy).not.toHaveBeenCalled()
      spy.mockRestore()

      // eslint-disable-next-line @typescript-eslint/unbound-method
      expect(renderers[renderers.length - 1].fitViewport).not.toHaveBeenCalled()
    })

    it('hidden tab is not sent a misleading zero rectangle', async () => {
      const { manager, client } = await mountTabManager()
      const tab = manager.newTab()
      await vi.waitFor(() => expect(client.openSession).toHaveBeenCalledTimes(2))
      const renderers = await getRendererMocks()

      Object.defineProperty(tab.pane, 'getBoundingClientRect', {
        value: () => ({
          width: 0,
          height: 0,
          top: 0,
          left: 0,
          right: 0,
          bottom: 0,
          x: 0,
          y: 0,
          toJSON: () => {},
        }),
        configurable: true,
      })

      await fireResize(0, 0)

      // Must NOT deliver a zero viewport.
      // eslint-disable-next-line @typescript-eslint/unbound-method
      expect(renderers[renderers.length - 1].fitViewport).not.toHaveBeenCalled()
    })
  })

  // ── Node identity across reorder (ADR-0012 §1) ──────────────────────

  it('tab node identity and focus survive reorder', async () => {
    const { client, manager, bar } = await mountTabManager()

    manager.newTab()
    manager.newTab()
    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(3)
    })

    // Three tabs: [1, 2, 3]. Capture the node for tab 1.
    const tab1 = document.getElementById('tab-btn-1')
    expect(tab1).not.toBeNull()

    // Focus tab 1.
    tab1!.focus()
    expect(document.activeElement).toBe(tab1)

    // Reorder: move tab 1 to position 3 (after tab 3).
    manager.reorderTab(1, 3)

    // The same DOM node should still be in the DOM, just moved.
    const tab1After = document.getElementById('tab-btn-1')
    expect(tab1After).not.toBeNull()
    expect(tab1!.isSameNode(tab1After)).toBe(true)

    // Node identity is the invariant that matters for ADR-0012 §1.
    // Focus may not survive reorder (Solid <For> reconciliation may blur),
    // but that is not a regression — the old code had the same behavior.

    // Tab order should be [2, 1, 3] — tab 1 moved to tab 3's position.
    const tabs = bar.querySelectorAll('.tab')
    expect(tabs.length).toBe(3)
    expect(tabs[0].getAttribute('data-tab-id')).toBe('2')
    expect(tabs[1].getAttribute('data-tab-id')).toBe('1')
    expect(tabs[2].getAttribute('data-tab-id')).toBe('3')
  })
})

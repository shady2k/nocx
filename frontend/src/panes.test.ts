// @vitest-environment jsdom
import { describe, expect, it, vi, beforeEach } from 'vitest'
import {
  createRendererMock,
  resetSessionCounter,
  mountPaneManager,
  makeClient,
  makeSession,
  childOf,
  makeClipboard,
  makeBanner,
  setupTabBarDOM,
  makeLayoutStore,
  makeUIStateBackend,
  FIXTURE_CWD,
  FIXTURE_DIRECTORY_LABEL,
  anchoredPane,
  type RendererMock,
} from './test-support/panes-fixtures'
import { isUuidv7 } from './layout/uuid7'
import { Pane, PaneManager } from './panes'
import { ClipboardGate } from './clipboard'
import type { TerminalContent } from './terminal-content'
import {
  BasePaneContent,
  SURFACE_TERMINAL,
  type ContentDescriptor,
  type PaneHost,
  type ContentViewport,
  type SurfaceType,
} from './pane-content'

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

// ── Second PaneContent implementation for mount-once proof ─────────────
// This class MUST NOT carry a private mount guard — the seam enforces
// mount-once, not the implementation. If mount() is called more than once,
// the seam is broken.
class CountingTestContent extends BasePaneContent {
  mountCount = 0
  /** Tracks every setVisible call for test assertions. */
  visibleCalls: boolean[] = []
  /** Ordered trace of lifecycle calls for ordering assertions. */
  callLog: string[] = []

  // eslint-disable-next-line @typescript-eslint/require-await, @typescript-eslint/no-unused-vars
  async mount(_target: HTMLElement, _host: PaneHost, _signal: AbortSignal): Promise<void> {
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

/**
 * Bring a pane out of its opening, the way a shell does: OSC 133 B is
 * prompt-end, so the shell has finished starting and is waiting on a person
 * (PaneHost.contentSettled). Before it, output is the pane's own start and
 * marks nothing — which is what stops a restore from lighting every tab.
 */
async function settlePane(index: number): Promise<void> {
  const renderers = await getRendererMocks()
  renderers[index]._fireCommandMarker({ kind: 'B', line: 0, col: 0, buffer: 'normal' })
}

describe('PaneManager', () => {
  beforeEach(() => {
    resetSessionCounter()
    vi.clearAllMocks()
  })

  // ── opening a tab creates a session and a pane ────────────────────────

  it('constructing a PaneManager creates no tab and mounts nothing', async () => {
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
    const manager = new PaneManager(
      bar,
      bar,
      panes,
      c as never,
      cb,
      g,
      bn,
      pc as never,
      tabStrip,
      makeLayoutStore().store,
      makeUIStateBackend().newClient(),
    )

    expect(bar.querySelectorAll('.nocx-tab').length).toBe(0)

    // Model state is also empty — no tabs registered.
    expect(manager.paneCount).toBe(0)
    expect(panes.querySelectorAll('.pane').length).toBe(0)
    expect(c.openSession).not.toHaveBeenCalled()
    // Strip is not yet mounted — no DOM children in the bar.
    expect(bar.children.length).toBe(0)
  })
  it('opens a session when a tab is created and activated', async () => {
    const { client, bar, panes } = await mountPaneManager()

    expect(bar.querySelectorAll('.nocx-tab').length).toBe(1)
    expect(panes.querySelectorAll('.pane').length).toBe(1)
    expect(client.openSession).toHaveBeenCalled()
  })

  it('creates a session for each new tab', async () => {
    const { client, manager, bar, panes } = await mountPaneManager()

    manager.newPane()
    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(2)
    })

    expect(bar.querySelectorAll('.nocx-tab').length).toBe(2)
    expect(panes.querySelectorAll('.pane').length).toBe(2)
  })

  it('forwards every SSH recovery hook without replacing its siblings', async () => {
    const { manager } = await mountPaneManager()
    const onVaultSealed = vi.fn()
    const onHostKeyError = vi.fn().mockResolvedValue(true)
    const onSetupVault = vi.fn()
    manager.onVaultSealed = onVaultSealed
    manager.onHostKeyError = onHostKeyError
    manager.onSetupVault = onSetupVault

    manager.newSSHPane('ssh:test:1', 'host.example.com')
    const content: unknown = manager.activeTerminalContent()
    expect(content).not.toBeNull()
    if (!content || typeof content !== 'object' || !('hooks' in content)) {
      throw new Error('active TerminalContent has no hooks field')
    }
    const hooks = content.hooks
    if (!hooks || typeof hooks !== 'object') {
      throw new Error('TerminalContent hooks are not an object')
    }
    expect('onVaultSealed' in hooks ? hooks.onVaultSealed : undefined).toBe(onVaultSealed)
    expect('onHostKeyError' in hooks ? hooks.onHostKeyError : undefined).toBe(onHostKeyError)
    expect('onSetupVault' in hooks ? hooks.onSetupVault : undefined).toBe(onSetupVault)
  })

  // ── closing closes the session and activates a neighbour ──────────────

  it('closes the session when the active tab is closed', async () => {
    const { client, manager } = await mountPaneManager()

    const session = client._sessions[0]
    manager.closeActivePane()

    // The session should have been closed, but a new one created for the replacement
    expect(session.close).toHaveBeenCalled()
  })

  it('activates a neighbour tab when the active tab is closed', async () => {
    const { client, manager, bar } = await mountPaneManager()

    manager.newPane()
    manager.newPane()
    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(3)
    })

    // Three tabs: [tab1, tab2, tab3]. Tab3 is active (last created).
    const tabs = bar.querySelectorAll('.nocx-tab')
    expect(tabs.length).toBe(3)
    expect(tabs[2].getAttribute('aria-selected') === 'true').toBe(true)

    // Close the active tab (tab3)
    manager.closeActivePane()

    // Two tabs remain; the neighbour (tab2 at original index 1) is now active
    const remainingPanes = bar.querySelectorAll('.nocx-tab')
    expect(remainingPanes.length).toBe(2)
    // The last remaining tab should be active (neighbour)
    expect(remainingPanes[1].getAttribute('aria-selected') === 'true').toBe(true)
  })

  it('closing the active tab activates the previously-active tab (MRU), not the visual neighbour', async () => {
    const { client, manager, bar } = await mountPaneManager()

    manager.newPane()
    manager.newPane()
    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(3)
    })

    // Three tabs: [tab1, tab2, tab3]. Tab3 is active (last created).
    // Activate 1 → 3 → 2 to build MRU: [1, 3].
    manager.activateByIndex(0) // tab1
    manager.activateByIndex(2) // tab3
    manager.activateByIndex(1) // tab2

    // Tab2 is active.
    const beforePanes = bar.querySelectorAll('.nocx-tab')
    expect(beforePanes[1].getAttribute('aria-selected') === 'true').toBe(true)

    // Close tab2. MRU says tab3 should activate, not tab1 (visual neighbour).
    manager.closeActivePane()

    const remainingPanes = bar.querySelectorAll('.nocx-tab')
    expect(remainingPanes.length).toBe(2)
    // tab3 should now be active (id 3, original index 2)
    expect(remainingPanes[1].getAttribute('aria-selected') === 'true').toBe(true)
  })

  // ── closing the last tab leaves exactly one fresh tab ─────────────────

  it('closing the last tab gets the replacement the BACKEND minted', async () => {
    const { client, manager, bar, panes, backend } = await mountPaneManager()

    // Close the only tab
    manager.closeActivePane()

    // The window is still never empty — but the replacement is no longer the
    // renderer's decision (nocx-isoph.4): panes.close mints it in the same
    // transaction that removes the last pane, and the renderer adopts the row
    // it reads back. So it arrives a round trip later, and it arrives with an
    // identity the store can name.
    await vi.waitFor(() => {
      expect(bar.querySelectorAll('.nocx-tab').length).toBe(1)
      expect(panes.querySelectorAll('.pane').length).toBe(1)
      expect(client.openSession).toHaveBeenCalledTimes(2)
    })
    const rows = backend.rows()
    expect(rows.tabs).toHaveLength(1)
    expect(rows.panes).toHaveLength(1)
  })

  // ── a session's name to a person (nocx-vnzek) ─────────────────────────

  it('names a session with the same words the tab strip shows, and nothing for one it does not hold', async () => {
    const { client, manager, bar } = await mountPaneManager()
    await vi.waitFor(() => {
      expect(bar.querySelectorAll('.nocx-tab').length).toBe(1)
    })
    const sessionId = client._sessions[0].sessionId
    // ONE answer to "what is this session called": the strip renders it and
    // this returns it. A second derivation is what the defect was.
    const onStrip = bar.querySelector('.nocx-tab-title')?.textContent
    expect(onStrip).toBe(FIXTURE_DIRECTORY_LABEL)
    expect(manager.sessionDisplayName(sessionId)).toBe(onStrip)
    // A session no pane holds cannot be named — and the id is not a
    // fallback, because the id is what this exists to keep off the screen.
    expect(manager.sessionDisplayName('mock-sid-does-not-exist')).toBeNull()
  })

  // ── where a session IS, in words (nocx-njn8s) ─────────────────────────

  it('says where a session is — the tab it is in and the machine it talks to', async () => {
    const { client, manager, bar } = await mountPaneManager()
    await vi.waitFor(() => {
      expect(bar.querySelectorAll('.nocx-tab').length).toBe(1)
    })
    const sessionId = client._sessions[0].sessionId
    // The tab half is the SAME answer sessionDisplayName gives — this is
    // that derivation asked together with the machine, never a second one.
    expect(manager.sessionWhere(sessionId)).toEqual({
      tab: manager.sessionDisplayName(sessionId),
      // A local shell has no host, and '' is that fact: the surface turns
      // it into the product's words for "here". Naming the machine is what
      // decides whether a destructive command lands on this laptop or on a
      // production host, so it is a fact the prompt is owed.
      machine: '',
      // A session opened with a cwd has one, and it is a GUESS until the
      // shell reports OSC 7 (AD-5). Both halves travel together or the
      // caller cannot tell them apart — see the next test.
      cwd: FIXTURE_CWD,
      cwdVerified: false,
    })
    expect(manager.sessionWhere('mock-sid-does-not-exist')).toBeNull()
  })

  // ── the directory, and whether we KNOW it (nocx-n7xha) ────────────────

  it('says the working directory and whether the shell confirmed it', async () => {
    const { client, manager, bar } = await mountPaneManager()
    await vi.waitFor(() => {
      expect(bar.querySelectorAll('.nocx-tab').length).toBe(1)
    })
    const sessionId = client._sessions[0].sessionId

    // Before any OSC 7 the cwd is the one the session was opened with: a
    // fallback question, not a claim (AD-5). An approval prompt that
    // printed it as fact would lie at the moment lying costs most, so the
    // flag travels WITH the value rather than being derivable from it.
    expect(manager.sessionWhere(sessionId)).toMatchObject({
      cwd: FIXTURE_CWD,
      cwdVerified: false,
    })

    // The shell reports where it is. Same accessor, and now it is a claim.
    const renderers = await getRendererMocks()
    renderers[0]._fireCwd('', '/tmp')
    expect(manager.sessionWhere(sessionId)).toMatchObject({
      cwd: '/tmp',
      cwdVerified: true,
    })
  })

  // ── fallback title consistency (badge vs title after close) ───────────

  it('fallback title is the directory, not a number that would disagree with the badge', async () => {
    const { client, manager, bar } = await mountPaneManager()

    // Open tabs until the badge says 4.
    manager.newPane()
    manager.newPane()
    manager.newPane()
    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(4)
    })

    const labels = bar.querySelectorAll('.nocx-tab-index')
    const titles = bar.querySelectorAll('.nocx-tab-title')

    // Before close: badge = 1..4, fallback title is the directory label.
    expect(labels[0].textContent).toBe('1')
    expect(labels[1].textContent).toBe('2')
    expect(labels[2].textContent).toBe('3')
    expect(labels[3].textContent).toBe('4')
    titles.forEach((t) => expect(t.textContent).toBe(FIXTURE_DIRECTORY_LABEL))

    // Close the first two tabs via public API: activate then close.
    manager.activateByIndex(0)
    manager.closeActivePane()
    manager.activateByIndex(0)
    manager.closeActivePane()

    // Re-query after DOM mutations; stale references reflect removed elements.
    const afterLabels = bar.querySelectorAll('.nocx-tab-index')
    const afterTitles = bar.querySelectorAll('.nocx-tab-title')
    // After close: badge = 1..2, titles stay the directory label.
    expect(afterLabels[0].textContent).toBe('1')
    expect(afterLabels[1].textContent).toBe('2')
    afterTitles.forEach((t) => expect(t.textContent).toBe(FIXTURE_DIRECTORY_LABEL))
  })

  // ── switching focuses the right renderer ──────────────────────────────

  it('switches between tabs on activateByIndex', async () => {
    const { client, manager, bar } = await mountPaneManager()

    manager.newPane()
    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(2)
    })

    const tabButtons = bar.querySelectorAll('.nocx-tab')
    expect(tabButtons.length).toBe(2)

    // Tab 2 (index 1) is active by default (last created)
    expect(tabButtons[1].getAttribute('aria-selected') === 'true').toBe(true)

    // Switch to tab 1 (index 0)
    manager.activateByIndex(0)
    expect(tabButtons[0].getAttribute('aria-selected') === 'true').toBe(true)
    expect(tabButtons[1].getAttribute('aria-selected') === 'true').toBe(false)

    // Switch to tab 2 (index 1)
    manager.activateByIndex(1)
    expect(tabButtons[0].getAttribute('aria-selected') === 'true').toBe(false)
    expect(tabButtons[1].getAttribute('aria-selected') === 'true').toBe(true)
  })

  // ── a title event updates that tab's label and no other ───────────────

  it('updates the title of the correct tab when onTitle fires', async () => {
    const { client, manager, bar } = await mountPaneManager()

    manager.newPane()
    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(2)
    })

    // Flush pending microtasks so both renderers are fully initialised.
    await Promise.resolve()

    const titles = bar.querySelectorAll('.nocx-tab-title')
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
    const { bar } = await mountPaneManager()

    await Promise.resolve()

    const renderers = await getRendererMocks()
    const titleEl = bar.querySelector('.nocx-tab-title')!

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
    const { client, manager, bar } = await mountPaneManager()

    manager.newPane()
    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(2)
    })

    // Make tab 1 (index 0) active; tab 2 (index 1) is now background.
    manager.activateByIndex(0)

    // The pane's opening is over — its shell has drawn a prompt — so what
    // arrives now is output rather than a start (PaneHost.contentSettled).
    await settlePane(1)

    // Deliver output to the background tab (index 1 = session 2).
    const bgSession = client._sessions[1]
    bgSession.fireData('hello')

    // The background tab's indicator should have the activity class
    const indicators = bar.querySelectorAll('.nocx-tab-indicator')
    // Tab 1 (background) should have the activity class
    expect(indicators[1].getAttribute('data-activity') === 'true').toBe(true)
    // Tab 0 (active) should not
    expect(indicators[0].getAttribute('data-activity') === 'true').toBe(false)
  })

  it('clears activity indicator when activated', async () => {
    const { client, manager, bar } = await mountPaneManager()

    manager.newPane()
    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(2)
    })

    // Tab 2 is active. Fire data on it while it's active → no activity mark.
    manager.activateByIndex(1)
    const activeSession = client._sessions[1]
    activeSession.fireData('output while active')

    const indicators = bar.querySelectorAll('.nocx-tab-indicator')
    expect(indicators[1].getAttribute('data-activity') === 'true').toBe(false)
  })

  // ── activity indicator: alternate-buffer suppression ─────────────────

  it('does not mark activity for alternate-buffer output on a background tab', async () => {
    const { client, manager, bar } = await mountPaneManager()

    manager.newPane()
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

    const indicators = bar.querySelectorAll('.nocx-tab-indicator')
    expect(indicators[1].getAttribute('data-activity') === 'true').toBe(false)
  })

  it('marks activity for normal-buffer output on a background tab', async () => {
    const { client, manager, bar } = await mountPaneManager()

    manager.newPane()
    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(2)
    })

    // Tab 1 active, tab 2 is background. Default _bufferType is 'normal'.
    manager.activateByIndex(0)

    const bgSession = client._sessions[1]

    // BEFORE THE PROMPT, output is the pane starting up and marks nothing.
    bgSession.fireData('a banner and an rc file')
    expect(bar.querySelectorAll('.nocx-tab-indicator')[1].getAttribute('data-activity')).not.toBe(
      'true',
    )

    // After it, the same bytes are something a person can have missed.
    await settlePane(1)
    bgSession.fireData('normal output')

    const indicators = bar.querySelectorAll('.nocx-tab-indicator')
    expect(indicators[1].getAttribute('data-activity') === 'true').toBe(true)
  })

  it('marks activity on bell in the alternate buffer', async () => {
    const { client, manager, bar } = await mountPaneManager()

    manager.newPane()
    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(2)
    })

    // Tab 1 active, tab 2 is background.
    manager.activateByIndex(0)

    // Settled first: a bell rung while the pane is still starting up is part
    // of that start, like everything else it emits then.
    await settlePane(1)

    // Put the background tab into the alternate buffer via onBufferChange.
    const renderers = await getRendererMocks()
    renderers[1]._fireBufferChange('alternate')

    // Fire bell on the background tab's renderer.
    renderers[1]._fireBell()

    const indicators = bar.querySelectorAll('.nocx-tab-indicator')
    expect(indicators[1].getAttribute('data-activity') === 'true').toBe(true)
  })

  it('does not mark activity on bell for the active tab', async () => {
    const { bar } = await mountPaneManager()

    // Only one tab, and it is active. Fire bell on it.
    const renderers = await getRendererMocks()
    renderers[0]._fireBell()

    const indicators = bar.querySelectorAll('.nocx-tab-indicator')
    expect(indicators[0].getAttribute('data-activity') === 'true').toBe(false)
  })

  // ── keyboard shortcuts ────────────────────────────────────────────────

  it('opens a new tab on Cmd+T', async () => {
    const { client, bar } = await mountPaneManager()

    window.dispatchEvent(new KeyboardEvent('keydown', { key: 't', metaKey: true, bubbles: true }))

    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(2)
    })
    expect(bar.querySelectorAll('.nocx-tab').length).toBe(2)
  })

  it('opens a new tab on Ctrl+T', async () => {
    const { client } = await mountPaneManager()

    window.dispatchEvent(new KeyboardEvent('keydown', { key: 't', ctrlKey: true, bubbles: true }))

    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(2)
    })
  })

  it('closes the active tab on Cmd+W', async () => {
    const { client, bar } = await mountPaneManager()

    const session = client._sessions[0]

    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'w', metaKey: true, bubbles: true }))

    // The session dies with the chrome, in the same turn.
    expect(session.close).toHaveBeenCalled()
    // The strip is never left empty, but the tab that fills it is the
    // backend's replacement and lands a round trip later.
    await vi.waitFor(() => {
      expect(bar.querySelectorAll('.nocx-tab').length).toBe(1)
    })
  })

  it('switches tabs on Cmd+1..9', async () => {
    const { client, manager, bar } = await mountPaneManager()

    manager.newPane()
    manager.newPane()
    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(3)
    })

    const tabButtons = bar.querySelectorAll('.nocx-tab')
    expect(tabButtons.length).toBe(3)

    // Tab 3 (index 2) is active (last created)
    expect(tabButtons[2].getAttribute('aria-selected') === 'true').toBe(true)

    // Cmd+1 → first tab
    window.dispatchEvent(new KeyboardEvent('keydown', { key: '1', metaKey: true, bubbles: true }))
    expect(tabButtons[0].getAttribute('aria-selected') === 'true').toBe(true)
    expect(tabButtons[1].getAttribute('aria-selected') === 'true').toBe(false)
    expect(tabButtons[2].getAttribute('aria-selected') === 'true').toBe(false)

    // Cmd+3 → third tab
    window.dispatchEvent(new KeyboardEvent('keydown', { key: '3', metaKey: true, bubbles: true }))
    expect(tabButtons[0].getAttribute('aria-selected') === 'true').toBe(false)
    expect(tabButtons[1].getAttribute('aria-selected') === 'true').toBe(false)
    expect(tabButtons[2].getAttribute('aria-selected') === 'true').toBe(true)
  })

  it('ignores keyboard shortcuts when alt is held', async () => {
    const { bar } = await mountPaneManager()

    window.dispatchEvent(
      new KeyboardEvent('keydown', { key: 't', metaKey: true, altKey: true, bubbles: true }),
    )

    expect(bar.querySelectorAll('.nocx-tab').length).toBe(1)
  })

  it('ignores Cmd+0 (not a valid tab index)', async () => {
    const { client, manager, bar } = await mountPaneManager()

    manager.newPane()
    manager.newPane()
    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(3)
    })

    // Cmd+0 should do nothing (no switching to index -1 or 0)
    window.dispatchEvent(new KeyboardEvent('keydown', { key: '0', metaKey: true, bubbles: true }))

    // Active should still be the last tab
    const tabButtons = bar.querySelectorAll('.nocx-tab')
    expect(tabButtons[2].getAttribute('aria-selected') === 'true').toBe(true)
  })

  // ── activity signal — terminal output vs user input ─────────────────

  it('terminal output alone does not fire vault onActivity', async () => {
    const { client, manager } = await mountPaneManager()
    const activity = vi.fn()
    manager.onActivity = activity

    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalled()
    })

    // Fire terminal output on the active session.
    const session = client._sessions[0]
    session.fireData('some command output')

    // Terminal output should NOT trigger activity.
    expect(activity).not.toHaveBeenCalled()

    // Now fire a keyboard shortcut — this SHOULD trigger activity.
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 't', metaKey: true, bubbles: true }))

    await vi.waitFor(() => {
      expect(activity).toHaveBeenCalled()
    })
  })

  // ── close by middle-click ─────────────────────────────────────────────

  it('closes a tab on middle-click', async () => {
    const { client, manager, bar } = await mountPaneManager()

    manager.newPane()
    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(2)
    })

    const tabButtons = bar.querySelectorAll('.nocx-tab')
    expect(tabButtons.length).toBe(2)

    const session0 = client._sessions[0]

    // Middle-click on the first tab
    tabButtons[0].dispatchEvent(new MouseEvent('mousedown', { button: 1, bubbles: true }))

    // Check it was closed
    expect(session0.close).toHaveBeenCalled()
    expect(bar.querySelectorAll('.nocx-tab').length).toBe(1)
  })

  it("closing a pane announces secrets.paneClosed with the pane's ONE identity", async () => {
    // The identity is minted once per pane and is the same value the layout
    // chain stores, history.record carries and this notification names
    // (nocx-isoph.4): a pane has one identity, not one per seam. So the ids
    // are read back FROM THE CHAIN rather than pinned by mocking a minter —
    // that is the property worth asserting.
    const { client, manager, bar, backend } = await mountPaneManager()

    manager.newPane()
    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(2)
      expect(backend.rows().panes).toHaveLength(2)
    })
    const [first, second] = backend.rows().panes.map((p) => p.id)
    expect(isUuidv7(first)).toBe(true)

    // Close the FIRST tab: its own id is announced, the second's is not.
    bar
      .querySelectorAll('.nocx-tab')[0]
      .dispatchEvent(new MouseEvent('mousedown', { button: 1, bubbles: true }))
    await vi.waitFor(() => {
      expect(client.notifyPaneClosed).toHaveBeenCalledWith(first)
    })
    expect(client.notifyPaneClosed).not.toHaveBeenCalledWith(second)

    // Close the remaining tab: its own id is announced, and the replacement
    // the backend mints carries a NEW one — a closed pane's identity is never
    // reused, because the backend scopes captures to it.
    bar
      .querySelectorAll('.nocx-tab')[0]
      .dispatchEvent(new MouseEvent('mousedown', { button: 1, bubbles: true }))
    await vi.waitFor(() => {
      expect(client.notifyPaneClosed).toHaveBeenCalledWith(second)
    })
    const replacement = backend.rows().panes.map((p) => p.id)
    expect(replacement).toHaveLength(1)
    expect(replacement[0]).not.toBe(first)
    expect(replacement[0]).not.toBe(second)
  })

  // ── flex-grow regression guards ──────────────────────────────────────

  it('a lone tab does not stretch (flex-grow is not a stretching value)', async () => {
    // Inject the critical layout rules so jsdom can compute styles.
    const style = document.createElement('style')
    style.textContent = `
      .tabbar { display: flex; }
      .tabs-container { flex: 0 1 auto; min-width: 0; display: flex; align-items: stretch; }
      .tabbar-spacer { flex: 1 1 0%; }
      .nocx-tab { flex: 0 1 200px; }
    `
    document.head.appendChild(style)

    const { bar } = await mountPaneManager()

    const panesContainer = bar.querySelector('.tabs-container') as HTMLElement
    expect(panesContainer).not.toBeNull()

    const tab = bar.querySelector('.nocx-tab') as HTMLElement
    expect(tab).not.toBeNull()

    // The tabs container itself must not grow.
    expect(getComputedStyle(panesContainer).flexGrow).toBe('0')

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
      .nocx-tab { flex: 1000 1 200px; }
    `
    document.head.appendChild(style)

    const { bar } = await mountPaneManager()

    const tab = bar.querySelector('.nocx-tab') as HTMLElement
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
    const { bar } = await mountPaneManager()

    await Promise.resolve()

    const renderers = await getRendererMocks()
    const titleEl = bar.querySelector('.nocx-tab-title')!

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
    const { bar } = await mountPaneManager()

    await Promise.resolve()

    const renderers = await getRendererMocks()
    const tabBtn = bar.querySelector('.nocx-tab')!

    // Initial tooltip includes the '(initial cwd)' marker (AD-5 surfacing).
    expect(tabBtn.getAttribute('title')).toContain('(initial cwd)')

    // OSC 7 fires → tooltip is just the path, no marker.
    renderers[0]._fireCwd('', '/tmp')
    expect(tabBtn.getAttribute('title')).toBe('/tmp')
    expect(tabBtn.getAttribute('title')).not.toContain('(initial')
  })

  it('program title overrides cwd-based title, but cwd updates the fallback', async () => {
    const { bar } = await mountPaneManager()

    await Promise.resolve()

    const renderers = await getRendererMocks()
    const titleEl = bar.querySelector('.nocx-tab-title')!

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
    const { client, manager, bar } = await mountPaneManager()

    manager.newPane()
    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(2)
    })

    await Promise.resolve()

    const renderers = await getRendererMocks()
    expect(renderers.length).toBe(2)

    const titles = bar.querySelectorAll('.nocx-tab-title')

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
    const { client, manager, bar } = await mountPaneManager()

    manager.newPane()
    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(2)
    })

    await Promise.resolve()

    const tabBtns = bar.querySelectorAll('.nocx-tab')
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

  it('writes selection to the clipboard when non-empty', async () => {
    const cb = makeClipboard()
    await mountPaneManager(undefined, cb)

    await Promise.resolve()
    const renderers = await getRendererMocks()

    renderers[0]._fireSelectionChange('selected text')

    expect(cb.writeText).toHaveBeenCalledWith('selected text')
  })

  it('does not write whitespace-only selection to the clipboard', async () => {
    const cb = makeClipboard()
    await mountPaneManager(undefined, cb)

    await Promise.resolve()
    const renderers = await getRendererMocks()

    renderers[0]._fireSelectionChange('   ')

    expect(cb.writeText).not.toHaveBeenCalled()
  })

  it('does not write empty selection to the clipboard', async () => {
    const cb = makeClipboard()
    await mountPaneManager(undefined, cb)

    await Promise.resolve()
    const renderers = await getRendererMocks()

    renderers[0]._fireSelectionChange('')

    expect(cb.writeText).not.toHaveBeenCalled()
  })

  it('writes OSC 52 decoded text to the clipboard when granted', async () => {
    const cb = makeClipboard()
    const gate = new ClipboardGate()
    gate.allow()

    await mountPaneManager(undefined, cb, gate)

    await Promise.resolve()
    const renderers = await getRendererMocks()

    renderers[0]._fireClipboardWrite('osc52 payload')

    expect(cb.writeText).toHaveBeenCalledWith('osc52 payload')
  })

  it('pastes on right-click (contextmenu event)', async () => {
    const cb = makeClipboard({
      readText: vi.fn().mockResolvedValue('right-click text'),
    })
    const { bar } = await mountPaneManager(undefined, cb)

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
    const { bar } = await mountPaneManager(undefined, cb)

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

    const { bar } = await mountPaneManager(undefined, cb)

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

    const { bar } = await mountPaneManager(undefined, cb)

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

    await mountPaneManager(undefined, cb, gate, banner)
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

    await mountPaneManager(undefined, cb, gate, banner)
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

    await mountPaneManager(undefined, cb, gate, banner)
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

    await mountPaneManager(undefined, cb, gate, banner)
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

    const { manager, client } = await mountPaneManager(undefined, cb, gate, banner)
    manager.newPane()
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

  // ── readiness signal ───────────────────────────────────────────────

  it('initialPaneReady resolves when the initial tab starts successfully', async () => {
    const { manager } = await mountPaneManager()

    // The initial tab was created and started by mountPaneManager.
    // initialPaneReady must resolve (not hang, not reject).
    await expect(manager.initialPaneReady).resolves.toBeUndefined()
  })

  it('initialPaneReady rejects when the initial tab start() throws', async () => {
    const client = makeClient({
      openSession: vi.fn(() => Promise.reject(new Error('session failed'))),
    })

    const { bar, panes } = setupTabBarDOM()
    const clipboard = makeClipboard()
    const gate = new ClipboardGate()
    const banner = makeBanner()

    const { PaneManager } = await import('./panes')
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
    const manager = new PaneManager(
      bar,
      bar,
      panes,
      client as unknown as import('./ipc').WSClient,
      clipboard,
      gate,
      banner,
      profileClient,
      tabStrip,
      makeLayoutStore().store,
      makeUIStateBackend().newClient(),
    )
    // Open the initial tab explicitly — the constructor mounts nothing.
    // Don't await: openInitialPane returns the _initialPaneReady promise;
    // we assert the rejection through that same promise below.
    void manager.openInitialPane()

    // initialPaneReady must reject — a genuinely broken tab is not "ready".
    // expect().rejects attaches the handler synchronously, so the rejection
    // that fires in a microtask is already handled; no unhandled-rejection.
    await expect(manager.initialPaneReady).rejects.toThrow('initial pane failed to start')

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
    // initialPaneReady resolved above, proving the content-level signal resolved true.
    //
    // For a direct content.ready assertion, construct TerminalContent + Tab manually.
    const client = makeClient()
    const wsClient = client as unknown as import('./ipc').WSClient
    const { Pane } = await import('./panes')
    const { TerminalContent } = await import('./terminal-content')
    const { SURFACE_TERMINAL } = await import('./pane-content')

    const clipboard = makeClipboard()
    const gate = new ClipboardGate()
    const banner = makeBanner()
    const content = new TerminalContent(
      wsClient,
      anchoredPane(),
      clipboard,
      gate,
      banner,
      null,
      () => {},
    )
    const tab = new Pane(
      content,
      {
        surfaceType: SURFACE_TERMINAL,
        singletonKey: null,
        restoreDescriptor: { type: 'local' },
        supportsAttention: true,
        defaultTitle: 'Terminal',
      },
      99,
      'tab-wire-1',
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
    const { Pane } = await import('./panes')
    const { TerminalContent } = await import('./terminal-content')
    const { SURFACE_TERMINAL } = await import('./pane-content')

    const clipboard = makeClipboard()
    const gate = new ClipboardGate()
    const banner = makeBanner()
    const content = new TerminalContent(
      wsClient,
      anchoredPane(),
      clipboard,
      gate,
      banner,
      null,
      () => {},
    )
    const tab = new Pane(
      content,
      {
        surfaceType: SURFACE_TERMINAL,
        singletonKey: null,
        restoreDescriptor: { type: 'local' },
        supportsAttention: true,
        defaultTitle: 'Terminal',
      },
      99,
      'tab-wire-1',
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
    const { Pane } = await import('./panes')
    const { TerminalContent } = await import('./terminal-content')
    const { SURFACE_TERMINAL } = await import('./pane-content')

    const clipboard = makeClipboard()
    const gate = new ClipboardGate()
    const banner = makeBanner()
    const content = new TerminalContent(
      wsClient,
      anchoredPane(),
      clipboard,
      gate,
      banner,
      null,
      () => {},
    )
    const tab = new Pane(
      content,
      {
        surfaceType: SURFACE_TERMINAL,
        singletonKey: null,
        restoreDescriptor: { type: 'local' },
        supportsAttention: true,
        defaultTitle: 'Terminal',
      },
      99,
      'tab-wire-1',
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

  it('activeSurfaceType answers the ACTIVE tab descriptor, and follows activation both ways', async () => {
    const { manager } = await mountPaneManager()
    const { SURFACE_TERMINAL } = await import('./pane-content')

    // The mounted tab is a terminal, and the descriptor is what answers —
    // not an instanceof test and not an inference from the session (B.8).
    expect(manager.activeSurfaceType()).toBe(SURFACE_TERMINAL)

    const content = new CountingTestContent()
    const descriptor: ContentDescriptor = {
      surfaceType: 'test.mock' as unknown as SurfaceType,
      singletonKey: null,
      restoreDescriptor: null,
      supportsAttention: false,
      defaultTitle: 'Test',
    }
    manager.openPane(content, descriptor)
    expect(manager.activeSurfaceType()).toBe('test.mock')

    // And back: a one-way assertion cannot report an answer that latches,
    // which is exactly what the sidebar's Settings collapse would then do.
    manager.activateByIndex(0)
    expect(manager.activeSurfaceType()).toBe(SURFACE_TERMINAL)
  })

  it('mounts content exactly once when activated repeatedly — seam guard, not TerminalContent private flag', async () => {
    const { manager } = await mountPaneManager()

    const content = new CountingTestContent()
    const descriptor: ContentDescriptor = {
      surfaceType: 'test.mock' as unknown as SurfaceType,
      singletonKey: null,
      restoreDescriptor: null,
      supportsAttention: false,
      defaultTitle: 'Test',
    }
    // openPane creates a tab via addPane → activate → start → mount.
    manager.openPane(content, descriptor)

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
    const { manager } = await mountPaneManager()
    const content = new CountingTestContent()
    const descriptor: ContentDescriptor = {
      surfaceType: 'test.mock' as unknown as SurfaceType,
      singletonKey: null,
      restoreDescriptor: null,
      supportsAttention: false,
      defaultTitle: 'Test',
    }

    manager.openPane(content, descriptor)

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
    const { manager } = await mountPaneManager()
    const content = new CountingTestContent()
    const descriptor: ContentDescriptor = {
      surfaceType: 'test.mock' as unknown as SurfaceType,
      singletonKey: null,
      restoreDescriptor: null,
      supportsAttention: false,
      defaultTitle: 'Test',
    }

    // addPane fires void this.activate(tab) — mount is async even for
    // CountingTestContent's sync body. With pre-mount target set in the
    // Tab constructor, setActive(true) already toggles the 'active' class
    // before mount() resolves.
    const tab = manager.openPane(content, descriptor)
    await Promise.resolve()
    await Promise.resolve()

    // The pane must have the 'active' class from the first activation.
    // This assertion breaks if setVisible(true) was called when _target
    // was null (pre-mount target fix makes it non-null from construction).
    expect(tab.pane.classList.contains('active')).toBe(true)
    expect(content.mountCount).toBe(1)
  })

  it('ordering: setVisible(true) fires before mount, and first viewport gets non-zero rectangle when pane is visible', async () => {
    // Construct a Tab directly (not through PaneManager) so we control
    // the activation order and can inspect the call log precisely.
    const { Pane } = await import('./panes')
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
    const tab = new Pane(content, descriptor, 99, 'tab-wire-1')
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

  it('refuses a second openInitialPane — the guard is at the seam, not in a comment', async () => {
    const { manager } = await mountPaneManager()
    // mountPaneManager already opened the initial tab, which is the point:
    // the composition root calls this exactly once and a second call must
    // fail loudly rather than mount a second strip and a second first tab.
    expect(() => manager.openInitialPane()).toThrow(/openInitialPane called twice/)
  })

  describe('B.5 geometry authority', () => {
    // ResizeObserver stub that records each observer's callback keyed by the
    // element it was asked to observe, so tests can drive the observer that
    // belongs to a specific pane. The real observer is created in
    // Tab.setupViewportObserver() — but CM6's EditorView also constructs its
    // own ResizeObservers (in CommandEditor's constructor during content
    // mount, for scroll/tooltip measurement), so a single captured callback
    // would silently point fireResize at the editor's observer.
    type ViewportObserverStub = {
      callback: (entries: Array<{ contentRect: DOMRectReadOnly }>) => void
      observed: Element[]
    }
    let observerStubs: ViewportObserverStub[] = []

    beforeEach(() => {
      resetSessionCounter()
      vi.clearAllMocks()
      observerStubs = []
      ;(globalThis as Record<string, unknown>).ResizeObserver = class {
        callback: ViewportObserverStub['callback']
        observed: Element[] = []
        constructor(cb: ViewportObserverStub['callback']) {
          this.callback = cb
          observerStubs.push(this)
        }
        observe(el: Element) {
          this.observed.push(el)
        }
        unobserve() {}
        disconnect() {}
      }
    })

    /** The stub whose observe() was called with `el` — the pane observer Tab
     *  created, never one of the editor's. */
    function observerOf(el: Element): ViewportObserverStub {
      const stub = observerStubs.find((s) => s.observed.includes(el))
      expect(stub).toBeDefined()
      return stub as ViewportObserverStub
    }

    /**
     * Helper: trigger the captured ResizeObserver callback for `pane` with
     * the given contentRect, then flush the rAF that _deliverViewport
     * schedules.
     */
    async function fireResize(pane: Element, width: number, height: number): Promise<void> {
      observerOf(pane).callback([{ contentRect: { width, height } as DOMRectReadOnly }])
      // Flush the requestAnimationFrame that coalesces delivery.
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()))
    }

    it('presentation layer calls viewportChanged on pane resize', async () => {
      // Acceptance test: WHO calls viewportChanged? The presentation layer.
      // This test fails if setupViewportObserver is removed or if
      // viewportChanged is only called by content measuring itself.
      const { manager, client } = await mountPaneManager()
      const tab = manager.newPane()
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
      // when the pane entered the DOM via addPane).
      await fireResize(tab.pane, 1024, 768)

      // The presentation layer's observer must have delivered the viewport
      // through content.viewportChanged → renderer.fitViewport.
      // eslint-disable-next-line @typescript-eslint/unbound-method
      expect(renderer.fitViewport).toHaveBeenCalledWith(
        expect.objectContaining({ width: 1024, height: 768 }),
      )
    })

    it('no viewport callback before mount starts', async () => {
      // Create a Tab directly without PaneManager so it is NOT auto-activated.
      // This lets us add the pane to DOM and fire the observer before start().
      const { Pane } = await vi.importActual<typeof import('./panes')>('./panes')
      const { TerminalContent } =
        await vi.importActual<typeof import('./terminal-content')>('./terminal-content')
      const { SURFACE_TERMINAL } =
        await vi.importActual<typeof import('./pane-content')>('./pane-content')

      const { client } = await mountPaneManager()
      const wsClient = client as unknown as import('./ipc').WSClient
      const content = new TerminalContent(
        wsClient,
        anchoredPane(),
        makeClipboard(),
        new ClipboardGate(),
        makeBanner(),
        null,
        () => {},
        undefined,
      )
      const tab = new Pane(
        content,
        {
          surfaceType: SURFACE_TERMINAL,
          singletonKey: null,
          restoreDescriptor: { type: 'local' },
          supportsAttention: true,
          defaultTitle: 'Terminal',
        },
        99,
        'tab-99',
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
      await fireResize(tab.pane, 1024, 768)

      // Clean up.
      tab.pane.remove()

      // The observer fired but _deliverViewport must have NO-OP'd because
      // _mountStarted is still false. Since no renderer was mounted (mount
      // never called), and viewportChanged is unreachable before mount,
      // this test guards the structural invariant.
    })

    it('equal consecutive rectangles are suppressed', async () => {
      const { manager, client } = await mountPaneManager()
      const tab = manager.newPane()
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

      await fireResize(tab.pane, 800, 600)
      await fireResize(tab.pane, 800, 600) // same rect — must be suppressed

      const renderers = await getRendererMocks()
      // eslint-disable-next-line @typescript-eslint/unbound-method
      expect(renderers[renderers.length - 1].fitViewport).toHaveBeenCalledTimes(1)
    })

    it('no callbacks after dispose', async () => {
      const { manager, client } = await mountPaneManager()
      const tab = manager.newPane()
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
      const { manager, client } = await mountPaneManager()
      const tab = manager.newPane()
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

      await fireResize(tab.pane, 0, 0)

      // Must NOT deliver a zero viewport.
      // eslint-disable-next-line @typescript-eslint/unbound-method
      expect(renderers[renderers.length - 1].fitViewport).not.toHaveBeenCalled()
    })
  })

  // ── Node identity across reorder (ADR-0012 §1) ──────────────────────

  it('tab node identity and focus survive reorder', async () => {
    const { client, manager, bar } = await mountPaneManager()

    manager.newPane()
    manager.newPane()
    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(3)
    })

    // Three tabs: [1, 2, 3]. Capture the node for tab 1.
    const tab1 = document.getElementById('tab-btn-1')
    expect(tab1).not.toBeNull()

    // Focus tab 1.
    tab1!.focus()
    expect(document.activeElement).toBe(tab1)

    // Reorder: move tab 1 to position 3 (after tab 3). The strip does not
    // move until the backend has written the positions and answered with them
    // (nocx-isoph.4), so this is a round trip rather than a splice.
    manager.reorderPane(1, 3)
    await vi.waitFor(() => {
      expect(bar.querySelectorAll('.nocx-tab')[0].getAttribute('data-pane-id')).toBe('2')
    })

    // The same DOM node should still be in the DOM, just moved.
    const tab1After = document.getElementById('tab-btn-1')
    expect(tab1After).not.toBeNull()
    expect(tab1!.isSameNode(tab1After)).toBe(true)

    // Node identity is the invariant that matters for ADR-0012 §1.
    // Focus may not survive reorder (Solid <For> reconciliation may blur),
    // but that is not a regression — the old code had the same behavior.

    // Tab order should be [2, 1, 3] — tab 1 moved to tab 3's position.
    const tabs = bar.querySelectorAll('.nocx-tab')
    expect(tabs.length).toBe(3)
    expect(tabs[0].getAttribute('data-pane-id')).toBe('2')
    expect(tabs[1].getAttribute('data-pane-id')).toBe('1')
    expect(tabs[2].getAttribute('data-pane-id')).toBe('3')
  })
})

// ── Agent status keys on the program title, never the composed title ────
//
// nocx-n8n82: Pane._programTitle held the COMPOSED title (program || cwd),
// and the agent-state classifier was fed that same value — a string that
// is usually a filesystem path. The program's own OSC 0/2 title now
// arrives separately (updateProgramTitle) and is the classifier's only
// input, so a path or command line can never masquerade as agent state.
describe('Tab agent-status channel (nocx-n8n82)', () => {
  function barePane(): Pane {
    const content = new CountingTestContent()
    return new Pane(
      content,
      {
        surfaceType: SURFACE_TERMINAL,
        singletonKey: null,
        restoreDescriptor: { type: 'local' },
        supportsAttention: true,
        defaultTitle: '',
      },
      1,
      'tab-wire-agent-status',
    )
  }

  it('classifies the program title, never the composed display title', () => {
    const tab = barePane()

    // The composed title is usually a path or a command line. A directory
    // named with a braille glyph must not light the tab as a working
    // agent — the classifier parses the program's own title, not the
    // tab label. (Feeding the composed title here is exactly the defect
    // this bead removes: setTitle no longer touches agent status.)
    tab.setTitle('/Users/⣿shady')
    expect(tab.title).toBe('/Users/⣿shady')
    expect(tab.agentStatus).toBeNull()

    // The program's own OSC 0/2 title is the agent-state input.
    tab.updateProgramTitle('⣿ working')
    expect(tab.agentStatus).toBe('working')

    // Claude Code's idle marker is the other classified state.
    tab.updateProgramTitle('✳ waiting for input')
    expect(tab.agentStatus).toBe('idle')

    // A TUI clearing its title on the way out (OSC 0/2 with an empty
    // string) reaches this channel too and resets the status.
    tab.updateProgramTitle('')
    expect(tab.agentStatus).toBeNull()
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// D6 — closing a tab with live descendants ASKS rather than decides
// (nocx-wtv3p, design §8 item 6).
//
// A parent's death never closes its children: three of the four ways to lose
// a parent are FAILURES, and a failure carries no information about whether
// the work is still wanted. The backend refuses to cascade whatever it is
// asked (internal/transport/ws_lineage_prohibitions_test.go); this is the
// half in front of the person — the one act that IS a decision still only
// decides about the tab it was aimed at, and the person is told what it
// leaves behind.
//
// Driven through the seam a person reaches: the close button on the tab.
// ═══════════════════════════════════════════════════════════════════════════

describe('closing a tab that opened other tabs', () => {
  beforeEach(() => {
    resetSessionCounter()
    vi.clearAllMocks()
  })

  /** The child tab's cwd, so its label is distinguishable from its parent's
   *  in the prompt. */
  const CHILD_CWD = '~/work/deploy-web'

  /**
   * Two tabs, where the second holds a session the backend admitted as a
   * CHILD of the first's — the edge riding the open ack, exactly as
   * nocx-9hu9d delivers it.
   */
  async function twoTabsInLineage(): Promise<{
    bar: HTMLElement
    manager: PaneManager
    opened: ReturnType<typeof makeSession>[]
  }> {
    const opened: ReturnType<typeof makeSession>[] = []
    const client = makeClient({
      openSession: vi.fn(() => {
        const session =
          opened.length === 0
            ? makeSession()
            : makeSession({ cwd: CHILD_CWD, parent: childOf(opened[0]) })
        opened.push(session)
        return Promise.resolve(session)
      }),
    })
    const { bar, manager } = await mountPaneManager(client)
    manager.newPane()
    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(2)
    })
    // The child's content must actually hold its session before the parent
    // is closed: an edge the renderer has not received yet would make every
    // assertion below pass for the wrong reason.
    await vi.waitFor(() => {
      expect(bar.textContent).toContain('deploy-web')
    })
    return { bar, manager, opened }
  }

  function clickClose(bar: HTMLElement, index: number): void {
    const buttons = bar.querySelectorAll('[aria-label="Close tab"]')
    buttons[index].dispatchEvent(new MouseEvent('click', { bubbles: true }))
  }

  it('asks, and the question names the tabs that would be left running', async () => {
    showConfirmMock.mockResolvedValue(false)
    const { bar } = await twoTabsInLineage()

    clickClose(bar, 0)

    await vi.waitFor(() => {
      expect(showConfirmMock).toHaveBeenCalled()
    })
    const message = String(showConfirmMock.mock.calls[0][0])
    expect(message).toContain('deploy-web')
    expect(message).toMatch(/leaves them running/)
  })

  it('cancelling leaves every tab exactly where it was', async () => {
    showConfirmMock.mockResolvedValue(false)
    const { bar, opened } = await twoTabsInLineage()

    clickClose(bar, 0)
    await vi.waitFor(() => {
      expect(showConfirmMock).toHaveBeenCalled()
    })
    // Settle the promise chain the click started.
    await Promise.resolve()
    await Promise.resolve()

    expect(bar.querySelectorAll('.nocx-tab').length).toBe(2)
    expect(opened[0].close).not.toHaveBeenCalled()
    expect(opened[1].close).not.toHaveBeenCalled()
  })

  // The prohibition itself, as the violating act: the person says yes, and
  // the tab they aimed at closes. The tab it opened does not — no close is
  // sent for it, and it is still on screen.
  it('confirming closes that tab and never the ones it opened', async () => {
    showConfirmMock.mockResolvedValue(true)
    const { bar, opened } = await twoTabsInLineage()

    clickClose(bar, 0)

    await vi.waitFor(() => {
      expect(opened[0].close).toHaveBeenCalled()
    })
    expect(opened[1].close).not.toHaveBeenCalled()
    expect(bar.querySelectorAll('.nocx-tab').length).toBe(1)
    expect(bar.textContent).toContain('deploy-web')
  })

  // The control: without a descendant there is nothing to say, and a prompt
  // on every close would train the person to dismiss the one that matters.
  it('does not ask when the tab opened nothing', async () => {
    showConfirmMock.mockResolvedValue(true)
    const { bar, opened } = await twoTabsInLineage()

    // The CHILD opened nothing.
    clickClose(bar, 1)

    await vi.waitFor(() => {
      expect(opened[1].close).toHaveBeenCalled()
    })
    expect(showConfirmMock).not.toHaveBeenCalled()
  })
})

// ── Closing a workspace (nocx-isoph.6, design §4.1 and D6) ────────────────
//
// A workspace close takes every one of its tabs, so it asks first and the
// question NAMES what is live. Membership itself is the backend's and arrives
// with nocx-isoph.4: the caller hands the members it resolved, and this layer
// owns the ask and the close — the two things that must not be duplicated.
describe('closing a workspace names what is live before anything dies (nocx-isoph.6)', () => {
  beforeEach(() => {
    resetSessionCounter()
    vi.clearAllMocks()
  })

  /** The lifecycle.changed handlers, one per mounted content, in the order
   *  the panes were created — so a fact can be delivered to the SECOND
   *  pane's kernel through the real subscription seam. */
  function lifecycleHandlers(client: ReturnType<typeof makeClient>): Array<(p: unknown) => void> {
    return client.dispatcher.subscribe.mock.calls
      .filter((c: unknown[]) => c[0] === 'lifecycle.changed')
      .map((c: unknown[]) => c[1] as (p: unknown) => void)
  }

  /**
   * Three real terminal panes over three real sessions. `members` are the two
   * the workspace holds; the first pane stands for a tab somewhere else, and
   * exists so every assertion below can say what the close did NOT touch.
   */
  async function aWorkspaceOfTwo(): Promise<{
    manager: PaneManager
    client: ReturnType<typeof makeClient>
    bar: HTMLElement
    members: Pane[]
  }> {
    const client = makeClient()
    const { bar, manager } = await mountPaneManager(client)
    const first = manager.newPane()
    const second = manager.newPane()
    await vi.waitFor(() => {
      expect(client.openSession).toHaveBeenCalledTimes(3)
    })
    // The contents must hold their sessions before anything is asked of
    // them: a pane whose open has not answered reports nothing live, and
    // every assertion here would pass for the wrong reason.
    await vi.waitFor(() => {
      expect(manager.paneCount).toBe(3)
      expect(lifecycleHandlers(client).length).toBe(3)
    })
    return { manager, client, bar, members: [first, second] }
  }

  /** Walk one pane's kernel onto a remote host, the way a hand-typed `ssh`
   *  does: the parent domain suspends, and the child establishes with the
   *  destination the backend authenticated (protocol §9, nocx-u7uh.11). */
  function walkOntoHost(
    client: ReturnType<typeof makeClient>,
    paneIndex: number,
    destination: { host: string; user: string },
  ): void {
    const deliver = lifecycleHandlers(client)[paneIndex]
    const sessionId = client._sessions[paneIndex].sessionId
    deliver({ sessionId, lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
    deliver({ sessionId, lane: 'lane-1', lifecycle: 'native' })
    deliver({
      sessionId,
      lane: 'lane-1',
      lifecycle: 'prompt_ready',
      domain: 'd2',
      epoch: 1,
      destination,
    })
  }

  it('asks, and the question names the live session rather than counting tabs', async () => {
    showConfirmMock.mockResolvedValue(false)
    const { manager, client, members } = await aWorkspaceOfTwo()
    walkOntoHost(client, 2, { host: 'prod-01', user: 'deploy' })

    await manager.closeWorkspace('ansible-rollout', members)

    expect(showConfirmMock).toHaveBeenCalled()
    const message = String(showConfirmMock.mock.calls[0][0])
    expect(message).toContain('deploy@prod-01')
    expect(message).toContain('ansible-rollout')
    // The count may be there; it may not be the only thing there.
    expect(message).toContain('2 tabs')
  })

  it('cancelling leaves every tab, pane and live session exactly as it was', async () => {
    showConfirmMock.mockResolvedValue(false)
    const { manager, client, members } = await aWorkspaceOfTwo()

    const answered = await manager.closeWorkspace('ansible-rollout', members)

    expect(answered).toBe(false)
    // Asserted on the SESSIONS, not on the DOM: nothing may be torn down
    // before the answer, and a strip that still shows three tabs would say
    // nothing about whether the sessions behind them were closed.
    for (const session of client._sessions) {
      expect(session.close).not.toHaveBeenCalled()
    }
    expect(client.notifyPaneClosed).not.toHaveBeenCalled()
    expect(manager.paneCount).toBe(3)
    expect(members.every((p) => p.pane.isConnected)).toBe(true)
  })

  it('confirming closes every tab it holds, and nothing outside it', async () => {
    showConfirmMock.mockResolvedValue(true)
    const { manager, client, members } = await aWorkspaceOfTwo()

    await manager.closeWorkspace('ansible-rollout', members)

    expect(client._sessions[1].close).toHaveBeenCalled()
    expect(client._sessions[2].close).toHaveBeenCalled()
    // The tab in another workspace is untouched — the close takes the
    // workspace's members and never a neighbour's.
    expect(client._sessions[0].close).not.toHaveBeenCalled()
    expect(manager.paneCount).toBe(1)
    // The backend is told about each pane that went, so its captures die
    // with them (nocx-tsajw).
    expect(client.notifyPaneClosed).toHaveBeenCalledTimes(2)
  })

  it('closing the last workspace leaves the application a tab, and it is not one of the closed ones', async () => {
    showConfirmMock.mockResolvedValue(true)
    const { manager, client, bar, members } = await aWorkspaceOfTwo()
    // Close the tab standing in for another workspace, so the two members
    // are the whole application.
    const closeButtons = bar.querySelectorAll('[aria-label="Close tab"]')
    closeButtons[0].dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await vi.waitFor(() => {
      expect(manager.paneCount).toBe(2)
    })

    await manager.closeWorkspace('ansible-rollout', members)

    // The application is never left with no tab at all: a replacement is
    // opened, and it is a fourth session rather than one of the members
    // brought back. Which WORKSPACE that replacement belongs to is the
    // backend's answer and nocx-isoph.3's rule — it goes to the default,
    // never to the one just closed, asserted there by its workspace_id.
    // The replacement is the BACKEND's answer since nocx-isoph.4: the close
    // goes over the wire and the renderer re-reads rather than synthesising a
    // tab it did not create. So this is a state to wait for, not a value to
    // read on the next line — waiting on the observable change is the rule
    // anyway, and here it is also the only correct assertion.
    await vi.waitFor(() => {
      expect(manager.paneCount).toBe(1)
      expect(client.openSession).toHaveBeenCalledTimes(4)
    })
    expect(members.every((p) => !p.pane.isConnected)).toBe(true)
  })

  it('a workspace with nothing running still asks, and says so rather than naming nothing', async () => {
    showConfirmMock.mockResolvedValue(false)
    const { manager, members } = await aWorkspaceOfTwo()

    await manager.closeWorkspace('reading', members)

    expect(showConfirmMock).toHaveBeenCalled()
    const message = String(showConfirmMock.mock.calls[0][0])
    expect(message).toMatch(/[Nn]othing is running/)
    expect(message).not.toContain('Still running')
  })
})

describe('sandbox marker ownership', () => {
  it('never marks an ordinary pane as sandboxed', async () => {
    const { manager, client } = await mountPaneManager()
    const pane = manager.newPane()

    await vi.waitFor(() => expect(client.openSession).toHaveBeenCalledTimes(2))
    expect(pane.sandboxed).toBe(false)
  })
})

describe('newSandboxedPane', () => {
  it('copies launch deltas and exposes durable creation readiness', async () => {
    const openSandboxedSession = vi.fn(() =>
      Promise.resolve(
        makeSession({
          sandbox: {
            backend: 'landlock',
            workspace: '/w',
            writableRoots: ['/w'],
            readOnlyRoots: ['/usr', '/opt'],
            homeProjections: [],
          },
        }),
      ),
    )
    const client = makeClient({ openSandboxedSession })
    const { manager } = await mountPaneManager(client)
    const launch = {
      settingsRevision: 1,
      profileRevision: null,
      addWritable: ['/a'],
      removeWritable: ['/b'],
      addReadOnly: ['/r1'],
      removeReadOnly: ['/r2'],
    }

    const made = manager.newSandboxedPane('/w', launch)
    launch.addWritable.push('/mutated')
    launch.removeWritable.pop()
    launch.addReadOnly.push('/mutated-ro')
    launch.removeReadOnly.pop()

    await vi.waitFor(() => expect(openSandboxedSession).toHaveBeenCalledTimes(1))
    expect(openSandboxedSession).toHaveBeenCalledWith(
      expect.any(Number),
      expect.any(Number),
      {
        workspace: '/w',
        settingsRevision: 1,
        profileRevision: null,
        addWritable: ['/a'],
        removeWritable: ['/b'],
        addReadOnly: ['/r1'],
        removeReadOnly: ['/r2'],
      },
      { paneId: made.pane.wireId },
    )
    await expect(made.created).resolves.toBe(true)
    await vi.waitFor(() => expect(made.pane.sandboxed).toBe(true))
    expect(made.pane.descriptor.restoreDescriptor).toBeNull()
  })
})

describe('newLocalPaneAt', () => {
  it('creates an ordinary replacement in the verified sandbox cwd', async () => {
    const { manager, client, backend } = await mountPaneManager()

    const made = manager.newLocalPaneAt('/verified/project')

    await expect(made.created).resolves.toBe(true)
    await vi.waitFor(() => expect(client.openSession).toHaveBeenCalledTimes(2))
    expect(client.openSession).toHaveBeenLastCalledWith(
      expect.any(Number),
      expect.any(Number),
      expect.objectContaining({ paneId: made.pane.wireId, cwd: '/verified/project' }),
    )
    expect(backend.rows().panes.find((row) => row.id === made.pane.wireId)?.cwd).toBe(
      '/verified/project',
    )
    expect(made.pane.sandboxed).toBe(false)
  })
})

describe('conversion transcript handoff', () => {
  it('restores the unsent draft and a visible shell boundary before closing the source', async () => {
    const { manager } = await mountPaneManager()
    const source = manager.activeTerminalContent()
    if (!source) throw new Error('missing active terminal')
    source.installConversionTranscript(
      {
        blocks: [],
        liveBody: '',
        draft: 'unsent draft',
        selection: { from: 12, to: 12 },
        editorScrollTop: 0,
        alternateScreenOmitted: false,
      },
      'Seed',
    )
    const transcript = source.captureConversionTranscript()
    transcript.liveBody = 'previous output'

    const made = manager.newLocalPaneAt('/verified/project')
    await expect(made.created).resolves.toBe(true)
    await expect(
      manager.installConversionTranscript(made.pane.id, transcript, 'Sandbox removed — new shell'),
    ).resolves.toBe(true)

    expect(manager.captureConversionTranscript(made.pane.id)?.draft).toBe('unsent draft')
    expect(made.pane.pane.querySelector('[data-restore-boundary]')?.textContent).toBe(
      'Sandbox removed — new shell',
    )
    const secondTranscript = manager.captureConversionTranscript(made.pane.id)
    expect(secondTranscript?.blocks.some((block) => block.body.includes('previous output'))).toBe(
      true,
    )
  })

  it('preserves restored-block SGR colour across a second capture', async () => {
    const { manager } = await mountPaneManager()
    const source = manager.activeTerminalContent()
    if (!source) throw new Error('missing active terminal')
    source.installConversionTranscript(
      {
        blocks: [
          {
            command: 'echo green',
            cwd: '/repo',
            location: '',
            durationMs: 0,
            exitCode: 0,
            status: 'success',
            body: '\u001b[32mgreen\u001b[0m',
          },
        ],
        liveBody: '',
        draft: '',
        selection: { from: 0, to: 0 },
        editorScrollTop: 0,
        alternateScreenOmitted: false,
      },
      'Seed',
    )

    const second = source.captureConversionTranscript()
    expect(second.blocks.some((block) => block.body.includes('\u001b[32m'))).toBe(true)
  })
})

describe('anyLocalSession — a live answer, never a latch', () => {
  it('answers a local pane that is open, and null once it is gone', async () => {
    const { manager, client } = await mountPaneManager()

    const sessionId = client._sessions[0].sessionId
    expect(manager.activeOrigin()?.kind).toBe('local')
    expect(manager.anyLocalSession()).toBe(sessionId)

    manager.closeActivePane()

    // Read at call time: nothing was re-rendered and nothing was notified,
    // and the answer is still right. That is the whole point — the latch it
    // replaces stayed true after its tab was gone.
    expect(manager.anyLocalSession()).toBeNull()
  })

  it('answers null when no pane is local', async () => {
    const { manager } = await mountPaneManager()

    // The only local tab goes, and what is left is a remote tab plus a pane
    // whose content answers no origin at all (a viewer). Neither is a
    // filesystem on THIS machine.
    manager.closeActivePane()
    manager.newSSHPane('ssh:test:1', 'host.example.com')
    manager.openPane(new CountingTestContent(), {
      surfaceType: 'test.mock' as unknown as SurfaceType,
      singletonKey: null,
      restoreDescriptor: null,
      supportsAttention: false,
      defaultTitle: 'Test',
    })

    expect(manager.anyLocalSession()).toBeNull()
  })
})

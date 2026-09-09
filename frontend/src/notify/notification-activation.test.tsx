// @vitest-environment jsdom
/**
 * Clicking a notification row takes you to the tab it came from — for as long
 * as that TAB exists, not for as long as its shell runs (nocx-2gfh6).
 *
 * The epic's own first source is `session.ended`, so the row a person is most
 * likely to click is by definition a row whose session has exited. Resolution
 * used to go through `activeOrigin()`, which answers "which MACHINE does this
 * tab speak for" and goes silent the moment the shell exits — so every
 * session.ended row rendered inert while its tab was still on the strip, and
 * the panel's own meta line said "session closed" about a tab in front of the
 * user.
 *
 * Driven through the seams a person reaches: a real PaneManager with real
 * tabs, the panel wired to it exactly as main.tsx wires it, and a click on the
 * row.
 */
import { describe, expect, it, vi, beforeEach } from 'vitest'
import { render, fireEvent } from '@solidjs/testing-library'
import {
  createRendererMock,
  resetSessionCounter,
  mountPaneManager,
  makeClient,
  makeSession,
} from '../test-support/panes-fixtures'
import { NotificationsPanel } from './notifications-panel'
import type { FeedStore } from './feed-store'
import type { NotifyFeedRead, Occurrence } from '../generated/notify.feed.read'
import type { PaneManager } from '../panes'

vi.mock('../renderers/xterm', () => ({
  XtermRenderer: vi.fn(createRendererMock),
}))

const ROW_TITLE = '.notifications-panel__list .ui-record-row__title'
const ROW_META = '.notifications-panel__list .ui-record-row__meta-text'

function occurrence(sessionId: string): Occurrence {
  return {
    id: 'occ-1',
    at: '2026-08-22T10:00:00Z',
    title: 'Local session was interrupted',
    body: '',
    kind: 'session.ended',
    level: 'warning',
    count: 1,
    read: false,
    backendId: 'local',
    sessionId,
    host: '',
    // A lone occurrence is a run of itself (see the feed's Count invariant).
    run: [
      {
        id: 'occ-1',
        at: '2026-08-22T10:00:00Z',
        title: 'Local session was interrupted',
        read: false,
      },
    ],
    runDropped: 0,
  }
}

function storeOf(o: Occurrence): FeedStore {
  const snapshot: NotifyFeedRead = {
    revision: 1,
    unreadCount: 1,
    occurrences: [o],
    dropped: { count: 0, oldest: '', newest: '' },
  }
  return {
    occurrences: () => snapshot.occurrences,
    visibleOccurrences: () => snapshot.occurrences,
    unreadCount: () => snapshot.unreadCount,
    readKnown: () => true,
    dropped: () => snapshot.dropped,
    markRead: vi.fn(),
    destroy: () => {},
  }
}

/** The panel as the composition root builds it (main.tsx): resolution is the
 *  renderer's, and both the click and the inertness read the same answer. */
function renderPanel(manager: PaneManager, o: Occurrence) {
  return render(() => (
    <NotificationsPanel
      store={storeOf(o)}
      onActivate={(backendId, sessionId) => {
        const pane = manager.findBySession(backendId, sessionId)
        if (pane) void manager.activate(pane)
      }}
      canActivate={(backendId, sessionId) =>
        manager.findBySession(backendId, sessionId) !== undefined
      }
      subscribe={(listener) => manager.onPanesChanged(listener)}
    />
  ))
}

describe('a notification row and the tab it came from', () => {
  beforeEach(() => {
    resetSessionCounter()
    vi.clearAllMocks()
  })

  /** Two tabs, the first of which has LOST its session — the state a
   *  session.ended row is in whenever its tab is still there to go back to.
   *  A clean exit closes the tab itself (terminal-content: `A clean exit
   *  closes the tab exactly as it always did`), and a row whose tab is gone
   *  is inert for a reason nobody disputes; a loss keeps the tab, with its
   *  scrollback and its "Connection lost" mark, and that is the row this
   *  bead is about. */
  async function twoTabsFirstLost() {
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
    // The shell of the FIRST tab exits on its own. The tab stays: only a
    // person closes a tab.
    await vi.waitFor(() => {
      expect(opened[0].onExit.mock.calls.length).toBe(1)
    })
    const onExit = opened[0].onExit.mock.calls[0][0] as (e: {
      sessionId: string
      cause: string
      status?: number
    }) => void
    onExit({ sessionId: opened[0].sessionId, cause: 'interrupted' })
    return { bar, manager, opened }
  }

  it('a row whose session ended still focuses its tab, which is still open', async () => {
    const { bar, manager, opened } = await twoTabsFirstLost()
    const tabs = bar.querySelectorAll('.nocx-tab')
    expect(tabs.length).toBe(2)
    expect(tabs[1].getAttribute('aria-selected')).toBe('true')

    const { container } = renderPanel(manager, occurrence(opened[0].sessionId))
    expect(container.querySelector(ROW_META)?.textContent).not.toContain('session closed')

    fireEvent.click(container.querySelector(ROW_TITLE)!)

    await vi.waitFor(() => {
      expect(bar.querySelectorAll('.nocx-tab')[0].getAttribute('aria-selected')).toBe('true')
    })
  })

  it('a row whose TAB was closed is inert, and says so', async () => {
    const { manager, opened } = await twoTabsFirstLost()
    const gonePane = manager.findBySession('local', opened[0].sessionId)
    expect(gonePane).toBeDefined()
    await manager.closePane(gonePane!)

    const { container } = renderPanel(manager, occurrence(opened[0].sessionId))
    expect(container.querySelector(ROW_META)?.textContent).toContain('session closed')

    fireEvent.click(container.querySelector(ROW_TITLE)!)
    expect(manager.findBySession('local', opened[0].sessionId)).toBeUndefined()
  })

  // THE PANEL IS MOUNTED WHILE THE SIDEBAR IS COLLAPSED — sidebar.tsx toggles
  // a class, it does not unmount the view — so rows are built as occurrences
  // arrive, which for a session.ended row is the moment its tab is closing.
  // Without this, `canActivate` is a snapshot taken then and never revised:
  // the row went on offering a tab that had since closed, and the panel's own
  // meta line said the session was still there (nocx-bu8fl). The two tests
  // above both render AFTER the state settled, which is exactly why neither
  // could see it.
  it('a row goes inert when its tab closes while the panel is on screen', async () => {
    const { manager, opened } = await twoTabsFirstLost()
    const { container } = renderPanel(manager, occurrence(opened[0].sessionId))
    expect(container.querySelector(ROW_META)?.textContent).not.toContain('session closed')

    const gone = manager.findBySession('local', opened[0].sessionId)
    expect(gone).toBeDefined()
    await manager.closePane(gone!)

    await vi.waitFor(() => {
      expect(container.querySelector(ROW_META)?.textContent).toContain('session closed')
    })
  })

  it('a backend id that is not this machine never resolves', async () => {
    const { manager, opened } = await twoTabsFirstLost()
    expect(manager.findBySession('helper-7', opened[0].sessionId)).toBeUndefined()
    expect(manager.findBySession('local', '')).toBeUndefined()
  })
})

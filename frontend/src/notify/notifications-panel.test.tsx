// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { render, fireEvent } from '@solidjs/testing-library'
import { NotificationsPanel } from './notifications-panel'
import { createFeedStore, type FeedStore } from './feed-store'
import type { NotifyFeedRead, Occurrence } from '../generated/notify.feed.read'

function occurrence(over: Partial<Occurrence> = {}): Occurrence {
  return {
    id: 'occ-1',
    at: '2026-08-22T10:00:00Z',
    title: 'build finished',
    body: '',
    kind: 'block.finished',
    level: 'success',
    count: 1,
    read: false,
    backendId: 'local',
    sessionId: 'sess-1',
    host: 'localhost',
    ...over,
  }
}

/** A store the test drives by hand. The panel reads accessors and nothing
 *  else, so nothing here needs a socket or a dispatcher. */
function fakeStore(over: Partial<NotifyFeedRead> = {}) {
  const snapshot: NotifyFeedRead = {
    revision: 1,
    unreadCount: 0,
    occurrences: [],
    dropped: { count: 0, oldest: '', newest: '' },
    ...over,
  }
  const markRead = vi.fn()
  const store: FeedStore = {
    occurrences: () => snapshot.occurrences,
    unreadCount: () => snapshot.unreadCount,
    dropped: () => snapshot.dropped,
    markRead,
    destroy: () => {},
  }
  return { store, markRead }
}

const titles = (container: HTMLElement) =>
  [...container.querySelectorAll('.notifications-panel__list .ui-record-row__title')].map(
    (el) => el.textContent,
  )

describe('NotificationsPanel', () => {
  it('renders one kit row per occurrence, newest first as the feed gave them', () => {
    const { store } = fakeStore({
      occurrences: [
        occurrence({ id: 'a', title: 'newest' }),
        occurrence({ id: 'b', title: 'oldest' }),
      ],
    })
    const { container } = render(() => (
      <NotificationsPanel store={store} onActivate={() => {}} canActivate={() => true} />
    ))

    expect(titles(container)).toEqual(['newest', 'oldest'])
    expect(container.querySelector('.ui-empty-state')).toBeNull()
  })

  it('renders a collapsed row as ×N', () => {
    const { store } = fakeStore({
      occurrences: [occurrence({ title: 'bell', count: 4 })],
    })
    const { container } = render(() => (
      <NotificationsPanel store={store} onActivate={() => {}} canActivate={() => true} />
    ))
    expect(titles(container)).toEqual(['bell ×4'])
  })

  it('tones the kind badge from the level', () => {
    const { store } = fakeStore({
      occurrences: [
        occurrence({ id: 'a', level: 'danger' }),
        occurrence({ id: 'b', level: 'warning' }),
        occurrence({ id: 'c', level: 'success' }),
        // info is NEUTRAL, not "info": an ordinary completion is not an
        // advisory, and toning every row blue distinguishes nothing.
        occurrence({ id: 'd', level: 'info' }),
      ],
    })
    const { container } = render(() => (
      <NotificationsPanel store={store} onActivate={() => {}} canActivate={() => true} />
    ))
    const tones = [...container.querySelectorAll('.notifications-panel__list .ui-badge')].map(
      (el) => el.getAttribute('data-tone'),
    )
    expect(tones).toEqual(['danger', 'warning', 'success', 'neutral'])
  })

  it('an empty feed renders the kit EmptyState, never bespoke markup', () => {
    const { store } = fakeStore()
    const { container } = render(() => (
      <NotificationsPanel store={store} onActivate={() => {}} canActivate={() => true} />
    ))
    expect(container.querySelector('.ui-empty-state')).not.toBeNull()
    expect(container.querySelector('.notifications-panel__list')).toBeNull()
  })

  it('activating a row resolves its session in the RENDERER', () => {
    const onActivate = vi.fn()
    const { store } = fakeStore({
      occurrences: [occurrence({ backendId: 'local', sessionId: 'sess-7' })],
    })
    const { container } = render(() => (
      <NotificationsPanel store={store} onActivate={onActivate} canActivate={() => true} />
    ))
    const row = container.querySelector('.notifications-panel__list .ui-record-row__title')
    expect(row).not.toBeNull()
    fireEvent.click(row!)
    expect(onActivate).toHaveBeenCalledWith('local', 'sess-7')
  })

  it('a row whose session is gone is not activatable and says so', () => {
    const onActivate = vi.fn()
    const { store } = fakeStore({
      occurrences: [occurrence({ host: 'web-1' })],
    })
    const { container } = render(() => (
      <NotificationsPanel store={store} onActivate={onActivate} canActivate={() => false} />
    ))
    const meta = container.querySelector('.notifications-panel__list .ui-record-row__meta-text')
    expect(meta?.textContent).toContain('session closed')
    const title = container.querySelector('.notifications-panel__list .ui-record-row__title')
    fireEvent.click(title!)
    expect(onActivate).not.toHaveBeenCalled()
  })

  it('a local occurrence has no host, and the meta line does not open with a separator', () => {
    // A local session carries no host — the backend sets one only on the
    // remote branch — so a meta line built by joining a fixed number of parts
    // rendered " · 10:00" (nocx-lmmi5). The separator belongs BETWEEN two
    // things that are there.
    const { store } = fakeStore({ occurrences: [occurrence({ host: '' })] })
    const { container } = render(() => (
      <NotificationsPanel store={store} onActivate={() => {}} canActivate={() => true} />
    ))
    const meta = container.querySelector('.notifications-panel__list .ui-record-row__meta-text')
    expect(meta?.textContent?.startsWith('·')).toBe(false)
    expect(meta?.textContent).not.toContain('·')

    const gone = fakeStore({ occurrences: [occurrence({ host: '' })] })
    const { container: c2 } = render(() => (
      <NotificationsPanel store={gone.store} onActivate={() => {}} canActivate={() => false} />
    ))
    const meta2 = c2.querySelector('.notifications-panel__list .ui-record-row__meta-text')
    expect(meta2?.textContent).toBe('session closed')
  })

  it('a remote occurrence still names its host before the instant', () => {
    const { store } = fakeStore({ occurrences: [occurrence({ host: 'web-1' })] })
    const { container } = render(() => (
      <NotificationsPanel store={store} onActivate={() => {}} canActivate={() => true} />
    ))
    const meta = container.querySelector('.notifications-panel__list .ui-record-row__meta-text')
    expect(meta?.textContent?.startsWith('web-1 · ')).toBe(true)
  })

  it('names what eviction dropped, outside the list and never activatable', () => {
    const { store } = fakeStore({
      occurrences: [occurrence()],
      dropped: { count: 12, oldest: '2026-08-22T08:00:00Z', newest: '2026-08-22T09:00:00Z' },
    })
    const { container } = render(() => (
      <NotificationsPanel store={store} onActivate={() => {}} canActivate={() => true} />
    ))
    // A soft degrade must be visible in the product, not only in a log.
    const dropped = container.querySelector('.notifications-panel__dropped')
    expect(dropped?.textContent).toContain('12')
    expect(dropped?.closest('.notifications-panel__list')).toBeNull()
    expect(dropped?.querySelector('button')).toBeNull()
  })

  it('says nothing about eviction when nothing was evicted', () => {
    const { store } = fakeStore({ occurrences: [occurrence()] })
    const { container } = render(() => (
      <NotificationsPanel store={store} onActivate={() => {}} canActivate={() => true} />
    ))
    expect(container.querySelector('.notifications-panel__dropped')).toBeNull()
  })
})

describe('reading is not visiting', () => {
  it('focusing the tab a notification came from leaves the unread count alone; only Mark all read changes it', async () => {
    // The two facts this epic exists to separate: "output arrived" and "you
    // saw what we told you". The 2026-08-13 design conflated them, which is
    // why a centre could not be built on top of it.
    const read = vi.fn().mockResolvedValue({
      revision: 1,
      unreadCount: 2,
      occurrences: [occurrence({ id: 'a' }), occurrence({ id: 'b', sessionId: 'sess-2' })],
      dropped: { count: 0, oldest: '', newest: '' },
    })
    const markRead = vi.fn().mockResolvedValue({ revision: 2 })
    const dispatcher = {
      subscribe: () => () => {},
      onConnect: () => () => {},
    }
    const store = createFeedStore({ read, markRead }, dispatcher)
    await vi.waitFor(() => expect(store.unreadCount()).toBe(2))

    const focused: string[] = []
    const { container } = render(() => (
      <NotificationsPanel
        store={store}
        onActivate={(_backendId, sessionId) => focused.push(sessionId)}
        canActivate={() => true}
      />
    ))

    const rows = [...container.querySelectorAll('.notifications-panel__list .ui-record-row__title')]
    fireEvent.click(rows[0])
    fireEvent.click(rows[1])
    await vi.waitFor(() => expect(focused).toEqual(['sess-1', 'sess-2']))

    // Visiting both tabs the notifications came from: still two unread.
    expect(store.unreadCount()).toBe(2)
    expect(markRead).not.toHaveBeenCalled()

    // Only the deliberate act changes it.
    store.markRead()
    await vi.waitFor(() => expect(store.unreadCount()).toBe(0))
    expect(markRead).toHaveBeenCalledTimes(1)
    store.destroy()
  })
})

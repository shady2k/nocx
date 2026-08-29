// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { createSignal } from 'solid-js'
import { render, fireEvent } from '@solidjs/testing-library'
import { NotificationsPanel } from './notifications-panel'
import { createFeedStore, type FeedStore } from './feed-store'
import type { NotifyCatalogue } from '../generated/notify.catalogue'
import type { NotifyFeedRead, Occurrence, RunMember } from '../generated/notify.feed.read'

function member(over: Partial<RunMember> = {}): RunMember {
  return {
    id: 'occ-1',
    at: '2026-08-22T10:00:00Z',
    title: 'build finished',
    read: false,
    ...over,
  }
}

function occurrence(over: Partial<Occurrence> = {}): Occurrence {
  const base: Occurrence = {
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
    run: [],
    runDropped: 0,
    ...over,
  }
  // A fresh occurrence holds exactly one member — itself — so an expansion
  // never has to special-case a run of one (D2). A fixture that wants a real
  // run passes `run` and `count` together: count == run.length + runDropped
  // holds for every occurrence the feed emits, so it holds for every fixture.
  if (base.run.length > 0) return base
  return {
    ...base,
    run: [member({ id: base.id, at: base.at, title: base.title, read: base.read })],
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
    visibleOccurrences: () => snapshot.occurrences,
    unreadCount: () => snapshot.unreadCount,
    readKnown: () => true,
    dropped: () => snapshot.dropped,
    markRead,
    destroy: () => {},
  }
  return { store, markRead }
}

/** The titles of the LIST's own rows. Deliberately not a descendant query:
 *  an expanded row discloses kit rows of its own, and a descendant query would
 *  count a run's members as rows of the feed. */
const titles = (container: HTMLElement) =>
  [...container.querySelectorAll('.notifications-panel__list > .ui-collection-row')].map(
    (row) => row.querySelector('.ui-record-row__title')?.textContent,
  )

/** The rows of one expansion, in the order they are drawn. */
const memberRows = (container: HTMLElement, rowIndex = 0) => {
  const rows = [...container.querySelectorAll('.notifications-panel__list > .ui-collection-row')]
  const run = rows[rowIndex]?.querySelector('.notifications-panel__run')
  return run === null || run === undefined ? [] : [...run.querySelectorAll('.ui-collection-row')]
}

const disclosureOf = (container: HTMLElement, rowIndex = 0) =>
  [...container.querySelectorAll('.notifications-panel__list > .ui-collection-row')][
    rowIndex
  ]?.querySelector('.ui-record-row__disclosure') ?? null

const dataDisclosure = (container: HTMLElement, rowIndex = 0) =>
  [...container.querySelectorAll('.notifications-panel__list > .ui-collection-row')][rowIndex]
    ?.querySelector('.ui-record-row')
    ?.getAttribute('data-disclosure')

/** The narrowing control for one axis, found by the label a person reads. */
const filterSelect = (container: HTMLElement, label: string): HTMLSelectElement | null => {
  const field = [...container.querySelectorAll('.notifications-panel__filters .ui-field')].find(
    (f) => f.querySelector('label')?.textContent === label,
  )
  return (field?.querySelector('select.ui-select') as HTMLSelectElement | undefined) ?? null
}

const shownCount = (container: HTMLElement) =>
  container.querySelector('.notifications-panel__shown')?.textContent

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

  it('distinguishes an unreadable feed from a successfully empty feed', () => {
    const store: FeedStore = {
      occurrences: () => [],
      visibleOccurrences: () => [],
      unreadCount: () => 0,
      readKnown: () => false,
      dropped: () => ({ count: 0, oldest: '', newest: '' }),
      markRead: vi.fn(),
      destroy: () => {},
    }
    const { container } = render(() => (
      <NotificationsPanel store={store} onActivate={() => {}} canActivate={() => true} />
    ))
    expect(container.querySelector('.ui-empty-state')?.textContent).toContain(
      'Could not read notifications',
    )
    expect(container.querySelector('.ui-empty-state')?.textContent).not.toContain(
      'Nothing to catch up on',
    )
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

/** A store whose feed the test can change under the panel — what a refetch
 *  does in the product. The static fakeStore above cannot express it. */
function mutableStore(initial: Occurrence[]) {
  const [occurrences, setOccurrences] = createSignal<Occurrence[]>(initial)
  const store: FeedStore = {
    occurrences,
    visibleOccurrences: occurrences,
    unreadCount: () => 0,
    readKnown: () => true,
    dropped: () => ({ count: 0, oldest: '', newest: '' }),
    markRead: vi.fn(),
    destroy: () => {},
  }
  return { store, setOccurrences }
}

describe('a collapsed row discloses what it stands for (nocx-ctl6q task 4)', () => {
  it('a row of one is a leaf, and still reserves the disclosure column', () => {
    // Not "expandable absent": every title in THIS list must stand in one
    // column, and a single-occurrence row that reserved nothing would put its
    // own title a chevron's width to the left of its neighbours'.
    const { store } = fakeStore({ occurrences: [occurrence({ count: 1 })] })
    const { container } = render(() => (
      <NotificationsPanel store={store} onActivate={() => {}} canActivate={() => true} />
    ))
    expect(dataDisclosure(container)).toBe('leaf')
    expect(disclosureOf(container)).toBeNull()
    expect(container.querySelector('.ui-record-row__leading')).not.toBeNull()
  })

  it('a collapsed row opens onto its constituents, newest first, each with its own instant', () => {
    const { store } = fakeStore({
      occurrences: [
        occurrence({
          id: 'run',
          title: 'deploy',
          count: 3,
          run: [
            member({ id: 'm3', at: '2026-08-22T10:09:00Z', title: 'deploy step 3' }),
            member({ id: 'm2', at: '2026-08-22T10:04:00Z', title: 'deploy step 2' }),
            member({ id: 'm1', at: '2026-08-22T10:00:00Z', title: 'deploy step 1' }),
          ],
        }),
      ],
    })
    const { container } = render(() => (
      <NotificationsPanel store={store} onActivate={() => {}} canActivate={() => true} />
    ))

    // Closed until asked: the row states the count and nothing more.
    expect(titles(container)).toEqual(['deploy ×3'])
    expect(memberRows(container)).toHaveLength(0)
    expect(dataDisclosure(container)).toBe('collapsed')

    fireEvent.click(disclosureOf(container)!)

    expect(dataDisclosure(container)).toBe('expanded')
    const members = memberRows(container)
    expect(members.map((m) => m.querySelector('.ui-record-row__title')?.textContent)).toEqual([
      'deploy step 3',
      'deploy step 2',
      'deploy step 1',
    ])
    // Its OWN instant, not the row's: an expansion whose rows share one
    // timestamp is not worth opening, which is why the member carries `at`.
    const whens = members.map((m) => m.querySelector('.ui-record-row__meta-text')?.textContent)
    expect(new Set(whens).size).toBe(3)
    expect(whens.every((w) => w !== undefined && w !== '')).toBe(true)

    // And it closes again.
    fireEvent.click(disclosureOf(container)!)
    expect(memberRows(container)).toHaveLength(0)
  })

  it('each member keeps its own unread mark: an unread row over members already seen', () => {
    // The asymmetry D2 exists for. Mark-all-read marked the row and its two
    // members; a later join cleared only the ROW's mark, so the row is unread
    // and the two it already held are not.
    const { store } = fakeStore({
      occurrences: [
        occurrence({
          id: 'run',
          title: 'bell',
          count: 3,
          read: false,
          run: [
            member({ id: 'm3', at: '2026-08-22T10:09:00Z', title: 'bell', read: false }),
            member({ id: 'm2', at: '2026-08-22T10:04:00Z', title: 'bell', read: true }),
            member({ id: 'm1', at: '2026-08-22T10:00:00Z', title: 'bell', read: true }),
          ],
        }),
      ],
    })
    const { container } = render(() => (
      <NotificationsPanel store={store} onActivate={() => {}} canActivate={() => true} />
    ))
    fireEvent.click(disclosureOf(container)!)

    const row = container.querySelector('.notifications-panel__list > .ui-collection-row')
    expect(row?.getAttribute('data-selected')).toBe('true')
    expect(memberRows(container).map((m) => m.getAttribute('data-selected'))).toEqual([
      'true',
      null,
      null,
    ])
  })

  it('a run whose tail overflowed says how much of it is shown', () => {
    const { store } = fakeStore({
      occurrences: [
        occurrence({
          id: 'flood',
          title: 'output',
          count: 4310,
          runDropped: 4290,
          run: Array.from({ length: 20 }, (_, i) =>
            member({ id: `m${i}`, at: `2026-08-22T10:${String(i).padStart(2, '0')}:00Z` }),
          ),
        }),
      ],
    })
    const { container } = render(() => (
      <NotificationsPanel store={store} onActivate={() => {}} canActivate={() => true} />
    ))
    fireEvent.click(disclosureOf(container)!)

    expect(memberRows(container)).toHaveLength(20)
    // Never the tail presented as the whole.
    expect(container.querySelector('.notifications-panel__run-dropped')?.textContent).toBe(
      '20 of 4310 shown',
    )
  })

  it('a run that lost nothing says nothing about losing anything', () => {
    const { store } = fakeStore({
      occurrences: [
        occurrence({
          id: 'run',
          count: 2,
          runDropped: 0,
          run: [member({ id: 'm2', at: '2026-08-22T10:04:00Z' }), member({ id: 'm1' })],
        }),
      ],
    })
    const { container } = render(() => (
      <NotificationsPanel store={store} onActivate={() => {}} canActivate={() => true} />
    ))
    fireEvent.click(disclosureOf(container)!)
    expect(memberRows(container)).toHaveLength(2)
    expect(container.querySelector('.notifications-panel__run-dropped')).toBeNull()
  })

  it('looking inside a row does not open the tab it came from', () => {
    // Expanding is not opening. A click that did both would make an expansion
    // impossible to reach with the mouse.
    const onActivate = vi.fn()
    const { store } = fakeStore({
      occurrences: [occurrence({ count: 2, run: [member({ id: 'm2' }), member({ id: 'm1' })] })],
    })
    const { container } = render(() => (
      <NotificationsPanel store={store} onActivate={onActivate} canActivate={() => true} />
    ))
    fireEvent.click(disclosureOf(container)!)
    expect(onActivate).not.toHaveBeenCalled()
    expect(memberRows(container)).toHaveLength(2)
  })
})

describe('the panel narrows the feed, and the bell goes on counting (D3)', () => {
  const twoHosts = () => [
    occurrence({ id: 'a', title: 'web build', host: 'web-1', sessionId: 'sess-a' }),
    occurrence({ id: 'b', title: 'db migrate', host: 'db-1', sessionId: 'sess-b' }),
    occurrence({ id: 'c', title: 'web deploy', host: 'web-1', sessionId: 'sess-c' }),
  ]

  it('offers no control for an axis that cannot narrow anything', () => {
    // One host, one session, one kind: three controls that could only ever
    // choose what is already shown. An offer that cannot be honoured is a lie.
    const { store } = fakeStore({
      occurrences: [occurrence({ id: 'a' }), occurrence({ id: 'b' })],
    })
    const { container } = render(() => (
      <NotificationsPanel store={store} onActivate={() => {}} canActivate={() => true} />
    ))
    expect(container.querySelector('.notifications-panel__filters')).toBeNull()
    expect(shownCount(container)).toBeUndefined()
  })

  it('narrowing to one host hides every row from another, and states the narrowed count', () => {
    const { store } = fakeStore({ occurrences: twoHosts() })
    const { container } = render(() => (
      <NotificationsPanel store={store} onActivate={() => {}} canActivate={() => true} />
    ))
    expect(titles(container)).toEqual(['web build', 'db migrate', 'web deploy'])
    // Nothing narrowed: no count line, because "3 of 3 shown" is noise.
    expect(shownCount(container)).toBeUndefined()

    const host = filterSelect(container, 'Host')
    expect(host).not.toBeNull()
    fireEvent.change(host!, { target: { value: 'v:web-1' } })

    expect(titles(container)).toEqual(['web build', 'web deploy'])
    expect(shownCount(container)).toBe('2 of 3 shown')
  })

  it('the BELL keeps counting everything while the list is narrowed', async () => {
    // D3's whole point: feed.unreadCount is the one number the bell and the
    // dock badge read, and a bell that quietened itself because you narrowed a
    // list would be lying about what is waiting.
    const read = vi.fn().mockResolvedValue({
      revision: 1,
      unreadCount: 3,
      occurrences: twoHosts(),
      dropped: { count: 0, oldest: '', newest: '' },
    })
    const markRead = vi.fn().mockResolvedValue({ revision: 2 })
    const store = createFeedStore(
      { read, markRead },
      { subscribe: () => () => {}, onConnect: () => () => {} },
    )
    await vi.waitFor(() => expect(store.unreadCount()).toBe(3))

    const { container } = render(() => (
      <NotificationsPanel store={store} onActivate={() => {}} canActivate={() => true} />
    ))
    const before = store.unreadCount()

    fireEvent.change(filterSelect(container, 'Host')!, { target: { value: 'v:db-1' } })
    expect(titles(container)).toEqual(['db migrate'])
    expect(shownCount(container)).toBe('1 of 3 shown')

    // The badge reads this accessor and nothing else (main.tsx).
    expect(store.unreadCount()).toBe(3)
    expect(store.unreadCount()).toBe(before)
    expect(markRead).not.toHaveBeenCalled()
    store.destroy()
  })

  it('clearing the filter restores every row, and a row open before it is still open after', () => {
    const { store } = fakeStore({
      occurrences: [
        occurrence({
          id: 'a',
          title: 'web build',
          host: 'web-1',
          count: 2,
          run: [member({ id: 'm2', at: '2026-08-22T10:04:00Z' }), member({ id: 'm1' })],
        }),
        occurrence({ id: 'b', title: 'db migrate', host: 'db-1' }),
      ],
    })
    const { container } = render(() => (
      <NotificationsPanel store={store} onActivate={() => {}} canActivate={() => true} />
    ))

    fireEvent.click(disclosureOf(container)!)
    expect(memberRows(container)).toHaveLength(2)

    fireEvent.change(filterSelect(container, 'Host')!, { target: { value: 'v:web-1' } })
    expect(titles(container)).toEqual(['web build ×2'])
    expect(memberRows(container)).toHaveLength(2)

    fireEvent.change(filterSelect(container, 'Host')!, { target: { value: '' } })
    expect(titles(container)).toEqual(['web build ×2', 'db migrate'])
    expect(shownCount(container)).toBeUndefined()
    // Which rows are open is view state of its own, keyed by occurrence id —
    // it is not a property of the list a filter rebuilt.
    expect(dataDisclosure(container)).toBe('expanded')
    expect(memberRows(container)).toHaveLength(2)
  })

  it('narrows by session and by kind on the same terms', () => {
    const { store } = fakeStore({
      occurrences: [
        occurrence({ id: 'a', title: 'a', sessionId: 'sess-a', kind: 'bell' }),
        occurrence({ id: 'b', title: 'b', sessionId: 'sess-b', kind: 'session.ended' }),
      ],
    })
    const { container } = render(() => (
      <NotificationsPanel
        store={store}
        sessionNameOf={(_backendId, sessionId) => (sessionId === 'sess-a' ? 'A tab' : 'B tab')}
        onActivate={() => {}}
        canActivate={() => true}
      />
    ))
    // One host on both rows, so that axis offers nothing.
    expect(filterSelect(container, 'Host')).toBeNull()

    fireEvent.change(filterSelect(container, 'Session')!, { target: { value: 'v:local/sess-b' } })
    expect(titles(container)).toEqual(['b'])
    expect(shownCount(container)).toBe('1 of 2 shown')
  })

  it('two axes narrow together, and a combination nothing satisfies says so', () => {
    const { store } = fakeStore({ occurrences: twoHosts() })
    const { container } = render(() => (
      <NotificationsPanel
        store={store}
        sessionNameOf={(_backendId, sessionId) => `Tab ${sessionId}`}
        onActivate={() => {}}
        canActivate={() => true}
      />
    ))
    fireEvent.change(filterSelect(container, 'Host')!, { target: { value: 'v:web-1' } })
    fireEvent.change(filterSelect(container, 'Session')!, { target: { value: 'v:local/sess-b' } })

    expect(container.querySelector('.notifications-panel__list')).toBeNull()
    expect(shownCount(container)).toBe('0 of 3 shown')
    // The way back stays on screen: an empty list whose filter had vanished
    // with it would be a dead end.
    expect(filterSelect(container, 'Host')).not.toBeNull()
    expect(container.querySelector('.ui-empty-state')).not.toBeNull()
  })

  it('a pick the feed no longer holds reads as no filter at all', () => {
    // Rows evict. A filter left pointing at a host the feed has forgotten
    // would empty the list with nothing on screen to explain it.
    const { store, setOccurrences } = mutableStore(twoHosts())
    const { container } = render(() => (
      <NotificationsPanel store={store} onActivate={() => {}} canActivate={() => true} />
    ))
    fireEvent.change(filterSelect(container, 'Host')!, { target: { value: 'v:db-1' } })
    expect(titles(container)).toEqual(['db migrate'])

    setOccurrences([occurrence({ id: 'c', title: 'web deploy', host: 'web-1' })])
    expect(titles(container)).toEqual(['web deploy'])
    expect(shownCount(container)).toBeUndefined()
  })

  it('the local machine is a host you can narrow to, though its host name is empty', () => {
    // A local session carries no host at all. An option whose value was the
    // empty string would be indistinguishable from "All hosts".
    const { store } = fakeStore({
      occurrences: [
        occurrence({ id: 'a', title: 'here', host: '' }),
        occurrence({ id: 'b', title: 'there', host: 'web-1' }),
      ],
    })
    const { container } = render(() => (
      <NotificationsPanel store={store} onActivate={() => {}} canActivate={() => true} />
    ))
    const host = filterSelect(container, 'Host')!
    expect([...host.options].map((o) => o.textContent)).toEqual([
      'All hosts',
      'This machine',
      'web-1',
    ])
    fireEvent.change(host, { target: { value: 'v:' } })
    expect(titles(container)).toEqual(['here'])
    expect(shownCount(container)).toBe('1 of 2 shown')
  })
  it('uses catalogue words and descriptions for badges and kind options', () => {
    const { store } = fakeStore({
      occurrences: [
        occurrence({ id: 'a', kind: 'pane.workFinished' }),
        occurrence({ id: 'b', kind: 'bell' }),
      ],
    })
    const catalogue: NotifyCatalogue = {
      kinds: [
        {
          kind: 'pane.workFinished',
          label: 'Work seems finished',
          description: 'A pane became idle.',
        },
        { kind: 'bell', label: 'Terminal bell', description: 'A program printed BEL.' },
      ],
    }
    const { container } = render(() => (
      <NotificationsPanel
        store={store}
        catalogue={() => catalogue}
        onActivate={() => {}}
        canActivate={() => true}
      />
    ))

    expect(
      [...container.querySelectorAll('.notifications-panel__list .ui-badge')].map(
        (badge) => badge.textContent,
      ),
    ).toEqual(['Work seems finished', 'Terminal bell'])
    expect(container.querySelector('.ui-badge')?.getAttribute('title')).toBe('A pane became idle.')
    expect(
      [...filterSelect(container, 'Kind')!.options].map((option) => option.textContent),
    ).toEqual(['All kinds', 'Work seems finished', 'Terminal bell'])
  })

  it('uses a readable fallback until the catalogue arrives', () => {
    const [catalogue, setCatalogue] = createSignal<NotifyCatalogue | null>(null)
    const { store } = fakeStore({
      occurrences: [
        occurrence({ kind: 'pane.workFinished' }),
        occurrence({ id: 'bell', kind: 'bell' }),
      ],
    })
    const { container } = render(() => (
      <NotificationsPanel
        store={store}
        catalogue={catalogue}
        onActivate={() => {}}
        canActivate={() => true}
      />
    ))

    expect(container.querySelector('.ui-badge')?.textContent).toBe('Pane work finished')
    expect(
      [...filterSelect(container, 'Kind')!.options].map((option) => option.textContent),
    ).toContain('Pane work finished')

    setCatalogue({
      kinds: [
        {
          kind: 'pane.workFinished',
          label: 'Work seems finished',
          description: 'A pane became idle.',
        },
        { kind: 'bell', label: 'Terminal bell', description: 'A program printed BEL.' },
      ],
    })
    expect(container.querySelector('.ui-badge')?.textContent).toBe('Work seems finished')
  })

  it('collapses unnamed sessions into one option and relabels on rename', () => {
    const [names, setNames] = createSignal<Record<string, string | null>>({
      'local/sess-a': null,
      'local/sess-b': null,
      'local/sess-c': 'Build tab',
    })
    const { store } = fakeStore({
      occurrences: [
        occurrence({ id: 'a', sessionId: 'sess-a' }),
        occurrence({ id: 'b', sessionId: 'sess-b' }),
        occurrence({ id: 'c', sessionId: 'sess-c' }),
      ],
    })
    const { container } = render(() => (
      <NotificationsPanel
        store={store}
        sessionNameOf={(backendId, sessionId) => names()[`${backendId}/${sessionId}`] ?? null}
        onActivate={() => {}}
        canActivate={() => true}
      />
    ))

    expect(
      [...filterSelect(container, 'Session')!.options].map((option) => option.textContent),
    ).toEqual(['All sessions', 'Unavailable sessions', 'Build tab'])

    setNames((previous) => ({ ...previous, 'local/sess-a': 'New tab' }))
    expect(
      [...filterSelect(container, 'Session')!.options].map((option) => option.textContent),
    ).toEqual(['All sessions', 'New tab', 'Unavailable sessions', 'Build tab'])
  })

  it('clears a kind filter when that kind becomes hidden', async () => {
    const [hidden, setHidden] = createSignal<ReadonlySet<string>>(new Set())
    const read = vi.fn().mockResolvedValue({
      revision: 1,
      unreadCount: 2,
      occurrences: [
        occurrence({ id: 'bell', kind: 'bell' }),
        occurrence({ id: 'session', title: 'session.ended', kind: 'session.ended' }),
      ],
      dropped: { count: 0, oldest: '', newest: '' },
    })
    const store = createFeedStore(
      { read, markRead: vi.fn().mockResolvedValue({ revision: 2 }) },
      { subscribe: () => () => {}, onConnect: () => () => {} },
      hidden,
    )
    const { container } = render(() => (
      <NotificationsPanel store={store} onActivate={() => {}} canActivate={() => true} />
    ))
    await vi.waitFor(() => expect(filterSelect(container, 'Kind')).not.toBeNull())
    const kind = filterSelect(container, 'Kind')!
    fireEvent.change(kind, { target: { value: 'v:bell' } })
    expect(kind.value).toBe('v:bell')

    setHidden(new Set(['bell']))
    expect(filterSelect(container, 'Kind')).toBeNull()
    expect(titles(container)).toEqual(['session.ended'])
    setHidden(new Set())
    expect(titles(container)).toEqual(['build finished', 'session.ended'])
    store.destroy()
  })
})

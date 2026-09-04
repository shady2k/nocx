import { describe, expect, it, vi } from 'vitest'
import { createFeedStore, type FeedClientLike } from './feed-store'
import type { NotifyFeedRead, Occurrence } from '../generated/notify.feed.read'
import type { NotifyFeedMarkRead } from '../generated/notify.feed.markRead'
import type { NotifyFeedChanged } from '../generated/notify.feed.changed'

const OCC: Occurrence = {
  id: 'occ-1',
  at: '2026-08-22T10:00:00Z',
  title: 'build finished',
  body: 'exit 0',
  kind: 'block.finished',
  level: 'success',
  count: 1,
  read: false,
  backendId: 'local',
  sessionId: 'sess-1',
  host: 'localhost',
  // A lone occurrence is a run of itself: Count == len(Run) + RunDropped
  // holds for every occurrence, which is what lets the panel treat a row of
  // one and a collapsed run as the same shape.
  run: [
    {
      id: 'occ-1',
      at: '2026-08-22T10:00:00Z',
      title: 'build finished',
      read: false,
    },
  ],
  runDropped: 0,
}

/** Every fixture is typed with the GENERATED declaration for its method:
 *  a hand-written shape can want a field the wire does not carry, which is
 *  exactly how vault.status shipped one nobody sent. */
function snapshot(over: Partial<NotifyFeedRead> = {}): NotifyFeedRead {
  return {
    revision: 1,
    unreadCount: 0,
    occurrences: [],
    dropped: { count: 0, oldest: '', newest: '' },
    ...over,
  }
}

const MARKED: NotifyFeedMarkRead = { revision: 6 }

/** The client and the very mocks it is made of. notes-store.test.ts keeps
 *  the spies beside the object for the same reason: reading a method OFF the
 *  client to assert on it is an unbound reference the lint rule refuses. */
function fakeClient(over: Partial<Record<'read' | 'markRead', ReturnType<typeof vi.fn>>> = {}) {
  const read = over.read ?? vi.fn().mockResolvedValue(snapshot())
  const markRead = over.markRead ?? vi.fn().mockResolvedValue(MARKED)
  const client: FeedClientLike = { read, markRead }
  return { client, read, markRead }
}

function fakeDispatcher() {
  const handlers = new Map<string, Set<(p: unknown) => void>>()
  const connectHandlers = new Set<() => void>()
  return {
    subscribe(method: string, h: (p: unknown) => void) {
      let s = handlers.get(method)
      if (!s) handlers.set(method, (s = new Set()))
      s.add(h)
      return () => {
        s.delete(h)
      }
    },
    onConnect(h: () => void) {
      connectHandlers.add(h)
      return () => {
        connectHandlers.delete(h)
      }
    },
    emit(revision: number) {
      const hint: NotifyFeedChanged = { revision }
      handlers.get('notify.feed.changed')?.forEach((h) => h(hint))
    },
    reconnect() {
      connectHandlers.forEach((h) => h())
    },
    subscriberCount() {
      return handlers.get('notify.feed.changed')?.size ?? 0
    },
  }
}

/** A promise the test resolves by hand, to order two reads deliberately. */
function deferred<T>() {
  let resolve!: (v: T) => void
  const promise = new Promise<T>((res) => {
    resolve = res
  })
  return { promise, resolve }
}

const tick = () => new Promise((r) => setTimeout(r, 0))

describe('feed store', () => {
  it('fetches a snapshot on creation and publishes it', async () => {
    const { client, markRead } = fakeClient({
      read: vi.fn().mockResolvedValue(
        snapshot({
          revision: 3,
          unreadCount: 1,
          occurrences: [OCC],
          dropped: { count: 2, oldest: '2026-08-22T09:00:00Z', newest: '2026-08-22T09:30:00Z' },
        }),
      ),
    })
    const d = fakeDispatcher()
    const store = createFeedStore(client, d)

    await vi.waitFor(() => expect(store.unreadCount()).toBe(1))
    expect(store.occurrences()).toEqual([OCC])
    expect(store.dropped().count).toBe(2)
    expect(d.subscriberCount()).toBe(1)
    expect(markRead).not.toHaveBeenCalled()
    store.destroy()
  })

  it('coalesces a burst of change hints into one refetch', async () => {
    const { client, read } = fakeClient({
      read: vi
        .fn()
        .mockResolvedValueOnce(snapshot({ revision: 1, unreadCount: 7 }))
        .mockResolvedValue(snapshot({ revision: 11, unreadCount: 7 })),
    })
    const d = fakeDispatcher()
    const store = createFeedStore(client, d)
    // Wait on the applied snapshot rather than on the call: "read was called"
    // is still true while the first refetch is in flight, and the burst would
    // then be swallowed by it rather than costing the one extra round trip
    // this test is about.
    await vi.waitFor(() => expect(store.readKnown()).toBe(true))
    expect(read).toHaveBeenCalledTimes(1)

    // Ten hints arriving while one refetch is in flight must not become ten
    // round trips: under a flood the hints are exactly what arrives in bulk.
    for (let i = 2; i <= 11; i++) d.emit(i)
    await vi.waitFor(() => expect(read).toHaveBeenCalledTimes(2))
    await tick()
    expect(read).toHaveBeenCalledTimes(2)
    store.destroy()
  })

  it('chases a hint that arrived under an in-flight read, with no further mutation', async () => {
    // The window this closes: the hint for N+1 arrives while the read for N is
    // still open, refetch hands back the in-flight promise, and the hint is
    // gone. On a quiet feed nothing else mutates, so the row the user was just
    // told about never appears — which is the cost nocx-dl1o0 reports.
    const held = deferred<NotifyFeedRead>()
    const read = vi
      .fn()
      .mockResolvedValueOnce(snapshot({ revision: 1, unreadCount: 0 }))
      .mockReturnValueOnce(held.promise)
      .mockResolvedValue(snapshot({ revision: 3, unreadCount: 1, occurrences: [OCC] }))
    const { client } = fakeClient({ read })
    const d = fakeDispatcher()
    const store = createFeedStore(client, d)
    await vi.waitFor(() => expect(store.readKnown()).toBe(true))

    d.emit(2)
    await vi.waitFor(() => expect(read).toHaveBeenCalledTimes(2))
    // The second mutation lands while the read for the first is still open.
    d.emit(3)
    // And the server answers that read from before it: a snapshot that does
    // not carry revision 3.
    held.resolve(snapshot({ revision: 2, unreadCount: 0 }))

    // NOTHING FURTHER IS EMITTED. Reaching the announced revision is the
    // store's own job, because on a quiet feed there is no next hint.
    await vi.waitFor(() => expect(store.occurrences()).toEqual([OCC]))
    expect(read).toHaveBeenCalledTimes(3)
    // And the chase settles: a snapshot that carries the hinted revision ends
    // it, so a flood still costs one extra round trip and not one per hint.
    await tick()
    expect(read).toHaveBeenCalledTimes(3)
    store.destroy()
  })

  it('forgets a pending hint across a reconnect, so a restarted backend cannot loop the chase', async () => {
    // A pending hint is a revision in the OLD backend's numbering. A restart
    // resets that numbering, so every snapshot afterwards is below it — and a
    // chase that compares them would refetch forever.
    const held = deferred<NotifyFeedRead>()
    const read = vi
      .fn()
      .mockResolvedValueOnce(snapshot({ revision: 9, unreadCount: 0 }))
      .mockReturnValueOnce(held.promise)
      .mockResolvedValue(snapshot({ revision: 1, unreadCount: 0 }))
    const { client } = fakeClient({ read })
    const d = fakeDispatcher()
    const store = createFeedStore(client, d)
    await vi.waitFor(() => expect(store.readKnown()).toBe(true))

    d.emit(10)
    await vi.waitFor(() => expect(read).toHaveBeenCalledTimes(2))
    d.emit(11)
    d.reconnect()
    // The restarted backend answers below everything the old one numbered.
    held.resolve(snapshot({ revision: 1, unreadCount: 0 }))

    await tick()
    await tick()
    expect(read).toHaveBeenCalledTimes(2)
    store.destroy()
  })

  it('ignores a hint at or below its own revision', async () => {
    const { client, read } = fakeClient({
      read: vi.fn().mockResolvedValue(snapshot({ revision: 5 })),
    })
    const d = fakeDispatcher()
    const store = createFeedStore(client, d)
    await vi.waitFor(() => expect(read).toHaveBeenCalledTimes(1))

    // A late duplicate of a revision already applied. Refetching on it would
    // turn one dropped-and-resent hint into an endless refetch loop.
    d.emit(5)
    d.emit(4)
    await tick()
    expect(read).toHaveBeenCalledTimes(1)
    store.destroy()
  })

  it('drops a snapshot older than the one it already holds', async () => {
    const late = deferred<NotifyFeedRead>()
    const read = vi
      .fn()
      .mockResolvedValueOnce(snapshot({ revision: 5, unreadCount: 2, occurrences: [OCC] }))
      .mockReturnValueOnce(late.promise)
      .mockResolvedValue(snapshot({ revision: 6, unreadCount: 2, occurrences: [OCC] }))
    const { client } = fakeClient({ read })
    const d = fakeDispatcher()
    const store = createFeedStore(client, d)
    await vi.waitFor(() => expect(store.unreadCount()).toBe(1))

    d.emit(6)
    await vi.waitFor(() => expect(read).toHaveBeenCalledTimes(2))
    // A reordered response, not news. Applying it would move the feed
    // backwards — a row the user has already seen reappearing as unread.
    late.resolve(snapshot({ revision: 4, unreadCount: 0, occurrences: [] }))
    await tick()

    expect(store.unreadCount()).toBe(1)
    expect(store.occurrences()).toEqual([OCC])
    // Discarding it is not the end of the matter: revision 6 was announced
    // and this response did not carry it, so the store still owes that read.
    // Dropping the stale snapshot must not also drop the hint.
    await vi.waitFor(() => expect(read).toHaveBeenCalledTimes(3))
    await tick()
    expect(read).toHaveBeenCalledTimes(3)
    expect(store.occurrences()).toEqual([OCC])
    store.destroy()
  })

  it('markRead applies the revision from the method result, so the hint that follows is a no-op', async () => {
    const { client, read, markRead } = fakeClient({
      read: vi
        .fn()
        .mockResolvedValue(snapshot({ revision: 5, unreadCount: 3, occurrences: [OCC] })),
    })
    const d = fakeDispatcher()
    const store = createFeedStore(client, d)
    await vi.waitFor(() => expect(store.unreadCount()).toBe(1))

    store.markRead()
    await vi.waitFor(() => expect(store.unreadCount()).toBe(0))
    expect(store.occurrences().every((o) => o.read)).toBe(true)
    expect(markRead).toHaveBeenCalledTimes(1)

    // The result was authoritative; the notification for the same revision
    // is then a late duplicate and must not cost a round trip.
    d.emit(MARKED.revision)
    await tick()
    expect(read).toHaveBeenCalledTimes(1)
    store.destroy()
  })

  it('a failed read leaves the last snapshot on screen', async () => {
    const read = vi
      .fn()
      .mockResolvedValueOnce(snapshot({ revision: 3, unreadCount: 4, occurrences: [OCC] }))
      .mockRejectedValueOnce(new Error('not connected'))
    const { client } = fakeClient({ read })
    const d = fakeDispatcher()
    const store = createFeedStore(client, d)
    await vi.waitFor(() => expect(store.unreadCount()).toBe(1))

    d.emit(4)
    await vi.waitFor(() => expect(read).toHaveBeenCalledTimes(2))
    await tick()

    // A bell that blanks itself on a transient error is worse than one that
    // is briefly stale: it says "nothing happened" when it cannot look.
    expect(store.unreadCount()).toBe(1)
    expect(store.occurrences()).toEqual([OCC])

    // And the next hint still retries — the failure is not sticky.
    read.mockResolvedValueOnce(snapshot({ revision: 5, unreadCount: 6, occurrences: [OCC] }))
    d.emit(5)
    await vi.waitFor(() => expect(store.unreadCount()).toBe(1))
    store.destroy()
  })

  it('destroy() unsubscribes', async () => {
    const { client, read } = fakeClient({
      read: vi.fn().mockResolvedValue(snapshot({ revision: 1 })),
    })
    const d = fakeDispatcher()
    const store = createFeedStore(client, d)
    await vi.waitFor(() => expect(read).toHaveBeenCalledTimes(1))

    store.destroy()
    expect(d.subscriberCount()).toBe(0)
    d.emit(99)
    await tick()
    expect(read).toHaveBeenCalledTimes(1)
  })

  it('a reconnect refetches even though the backend revision restarted below ours', async () => {
    // The feed is in memory: a backend restart resets the revision to 0, and
    // the contract says the renderer sees that as a reconnect. Without this
    // the hints that follow are all "at or below ours" and the bell freezes
    // for the life of the window.
    const read = vi
      .fn()
      .mockResolvedValueOnce(snapshot({ revision: 9, unreadCount: 4 }))
      .mockResolvedValueOnce(snapshot({ revision: 1, unreadCount: 1 }))
    const { client } = fakeClient({ read })
    const d = fakeDispatcher()
    const store = createFeedStore(client, d)
    await vi.waitFor(() => expect(store.readKnown()).toBe(true))

    d.reconnect()
    await vi.waitFor(() => expect(read).toHaveBeenCalledTimes(2))
    expect(read).toHaveBeenCalledTimes(2)
    store.destroy()
  })

  it('derives unread count from visible occurrences, not the wire count', async () => {
    const hidden = new Set<string>(['finishedBlock', 'bell'])
    const { client } = fakeClient({
      read: vi.fn().mockResolvedValue(
        snapshot({
          unreadCount: 3,
          occurrences: [
            OCC,
            { ...OCC, id: 'bell-1', kind: 'bell', read: false },
            { ...OCC, id: 'seen-1', kind: 'session.ended', read: true },
          ],
        }),
      ),
    })
    const d = fakeDispatcher()
    const store = createFeedStore(
      client,
      d,
      () => hidden,
      (kind) => (kind === 'block.finished' ? 'finishedBlock' : kind),
    )

    await vi.waitFor(() => expect(store.occurrences()).toHaveLength(3))
    expect(store.visibleOccurrences()).toEqual([
      { ...OCC, id: 'seen-1', kind: 'session.ended', read: true },
    ])
    expect(store.unreadCount()).toBe(0)
    expect(store.readKnown()).toBe(true)
    store.destroy()
  })

  it('keeps the first-read state unknown when the initial read fails', async () => {
    const { client } = fakeClient({
      read: vi.fn().mockRejectedValue(new Error('not connected')),
    })
    const d = fakeDispatcher()
    const store = createFeedStore(client, d)

    await tick()
    expect(store.readKnown()).toBe(false)
    expect(store.unreadCount()).toBe(0)
    store.destroy()
  })
})

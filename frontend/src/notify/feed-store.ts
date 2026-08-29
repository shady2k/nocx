// The one feed every notification surface reads: the bell's badge, the
// panel's rows and the dropped line all read this store and nothing else.
//
// Reconcile-on-revision, the shape settings-observer.ts already uses: the
// notification carries a revision and NOTHING else, so it is droppable
// without loss — a hint the refreshable outbound queue discards costs one
// refetch rather than a row nobody ever learns about (nocx-sb3f). The store
// cannot derive the new state from a hint (there is no occurrence in it), so
// every applied hint is a refetch.
import { createMemo, createSignal } from 'solid-js'
import type { Dispatcher } from '../dispatcher'
import type { Dropped, NotifyFeedRead } from '../generated/notify.feed.read'
import type { NotifyFeedMarkRead } from '../generated/notify.feed.markRead'
import type { NotifyFeedChanged } from '../generated/notify.feed.changed'

/** The subset of NotifyFeedClient the store needs — declared so a test can
 *  substitute a fake without a WebSocket. The same shape NotesClientLike
 *  has, for the same reason. */
export interface FeedClientLike {
  read(): Promise<NotifyFeedRead>
  markRead(): Promise<NotifyFeedMarkRead>
}

/** The subset of Dispatcher the store needs. Narrowed rather than re-declared
 *  so the dispatcher stays the single source of truth for these signatures. */
export type FeedDispatcherLike = Pick<Dispatcher, 'subscribe' | 'onConnect'>

export interface FeedStore {
  occurrences: () => NotifyFeedRead['occurrences']
  visibleOccurrences: () => NotifyFeedRead['occurrences']
  unreadCount: () => number
  readKnown: () => boolean
  dropped: () => Dropped
  markRead: () => void
  destroy: () => void
}

const EMPTY_DROPPED: Dropped = { count: 0, oldest: '', newest: '' }
const EMPTY_HIDDEN = new Set<string>()

export function createFeedStore(
  client: FeedClientLike,
  dispatcher: FeedDispatcherLike,
  hiddenKindIds: () => ReadonlySet<string> = () => EMPTY_HIDDEN,
  kindIdOf: (kind: string) => string = (kind) => kind,
): FeedStore {
  const [occurrences, setOccurrences] = createSignal<NotifyFeedRead['occurrences']>([])
  const visibleOccurrences = createMemo(() => {
    const hidden = hiddenKindIds()
    return occurrences().filter((o) => !hidden.has(kindIdOf(o.kind)))
  })
  const unreadCount = createMemo(() => visibleOccurrences().filter((o) => !o.read).length)
  const [readKnown, setReadKnown] = createSignal(false)
  const [dropped, setDropped] = createSignal<Dropped>(EMPTY_DROPPED)

  let revision = -1
  // One in-flight refetch at a time. Under a flood the hints are exactly what
  // arrives in bulk, and one round trip per hint would turn a noisy pane into
  // a noisy socket. The window this leaves is deliberate and bounded: a hint
  // for a mutation the in-flight read did not see is dropped, and the next
  // mutation's hint — every mutation raises one — refetches it.
  let inFlight: Promise<void> | null = null

  const apply = (snap: NotifyFeedRead) => {
    // A snapshot older than what we already hold is a reordered response, not
    // news. Applying it would move the feed backwards.
    if (snap.revision < revision) return
    revision = snap.revision
    setOccurrences(snap.occurrences)
    setReadKnown(true)
    setDropped(snap.dropped)
  }

  const refetch = (): Promise<void> => {
    if (inFlight) return inFlight
    inFlight = client
      .read()
      .then(apply)
      .catch(() => {
        // A failed read leaves the last snapshot on screen. The next hint
        // retries; a bell that blanks itself on a transient error is worse
        // than one that is briefly stale — it says "nothing happened" when
        // what it means is "I could not look".
      })
      .finally(() => {
        inFlight = null
      })
    return inFlight
  }

  const unsubscribe = dispatcher.subscribe('notify.feed.changed', (params: unknown) => {
    const hint = params as NotifyFeedChanged | null
    // At or below our own revision is a late duplicate. Refetching on it would
    // turn one dropped-and-resent hint into an endless refetch loop.
    if (typeof hint?.revision !== 'number' || hint.revision <= revision) return
    void refetch()
  })

  // The feed is in memory only, so a backend restart resets the revision to
  // zero — which the renderer sees as a reconnect (contracts/notify.feed.read
  // says exactly that). Without dropping our baseline here, every hint after
  // a restart is "at or below ours" and the bell freezes for the life of the
  // window.
  const unsubscribeConnect = dispatcher.onConnect(() => {
    revision = -1
    void refetch()
  })

  void refetch()

  return {
    occurrences,
    visibleOccurrences,
    unreadCount,
    readKnown,
    dropped,
    markRead: () => {
      // The METHOD RESULT is authoritative; the change notification that
      // follows is then a no-op by the rule above.
      void client
        .markRead()
        .then((r) => {
          if (r.revision <= revision) return
          revision = r.revision
          setOccurrences((prev) => prev.map((o) => ({ ...o, read: true })))
        })
        .catch(() => refetch())
    },
    destroy: () => {
      unsubscribe()
      unsubscribeConnect()
    },
  }
}

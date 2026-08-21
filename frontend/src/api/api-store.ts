// ApiStore — the one list every part of the workbench reads (the Files and
// Git store pattern): the open collections, the request in the form, and the
// runs. Plain Solid signals; nothing here renders, and no method is called
// during a render.
//
// Three rules, each with the thing it stops:
//
// 1. SEND WRITES FIRST, BECAUSE THE FILE IS WHAT GETS SENT. `api.request.send`
//    takes a handle and a path — never a request value — so what goes out is
//    what is on disk. A form able to send something the file does not contain
//    would be a second truth beside the one design §6.4 names, and the two
//    would agree until the day they did not. The write happens only when the
//    draft actually differs from what was read: pressing Send on an untouched
//    request must not rewrite the file, because a git diff a person did not
//    cause is how the "shareable through git" claim stops being true.
//    A write that FAILS stops the send: the file never held what would have
//    gone out, so sending would put the OLD request on the wire under the new
//    request's name.
//
// 2. A FAILED SEND IS A RUN, NOT A DISAPPEARANCE. Refused connection, TLS
//    error, a dead handle — each becomes a row in the list saying why. A run
//    that vanishes on failure teaches that nothing happened.
//
// 3. ONE LIST OF COLLECTIONS, WHICHEVER DOOR THE FOLDER CAME THROUGH.
//    `api.collections.open` answers a handle plus a collection, and so does
//    `api.collections.create`; `api.collections.list` answers rows. The
//    adopters in api-model.ts put the first two into the third's shape, so
//    nothing downstream has to ask which call produced the row it is looking
//    at — a collection a person has just made and one they opened an hour ago
//    are the same row, and only the row knows the difference.
//
// The pretty/raw choice belongs to ONE run. A single flag for the list would
// mean opening the raw text of the run you are reading also opens it for the
// nineteen above it.

import { createSignal, untrack } from 'solid-js'
import type { ApiWorkbenchServices } from './api-client'
import type { FilesChanged } from '../generated/files.changed'
import {
  adoptCreatedCollection,
  adoptImportedRequest,
  adoptOpenedCollection,
  type ApiImportNote,
  type ApiOpenCollection,
  type ApiRequest,
  type ApiResponse,
} from './api-model'
import type { Unsupported as PostmanNote } from '../generated/api.import.postman'

/** How a run's body is being read. */
export type ApiRunView = 'pretty' | 'raw'

/** One exchange, as the list holds it. Exactly one of `response` and `error`
 *  is set: an exchange either came back or did not, and a row that could
 *  hold both would have to decide which one to believe. */
export interface ApiRun {
  readonly id: number
  /** The method and URL as the FORM had them when Send was pressed — what
   *  the person asked for. Kept on the run so scrolling back through twenty
   *  of them does not require remembering what the form held at the time.
   *
   *  The REQUEST itself is deliberately not kept. The raw view draws the
   *  request from `response.raw.request` — the backend's own account of what
   *  it put on the socket, with the spans it placed (§11.2) — so a copy of
   *  the model here would be a second, unread answer to "what was sent", and
   *  a field written by two call sites and read by none is the shape this
   *  repo has shipped before. */
  readonly method: string
  readonly url: string
  readonly response: ApiResponse | null
  readonly error: string | null
  readonly view: ApiRunView
}

/** Which request file the form is showing, or null when the draft came from
 *  an import and has no file behind it yet. */
interface ApiSelection {
  readonly handle: string
  readonly relPath: string
}

export interface ApiStore {
  collections(): readonly ApiOpenCollection[]
  /** The handle of the collection the workbench is pointed at — the one just
   *  made, the one just opened, or the one holding the request in the form.
   *  '' when there is none.
   *
   *  Deliberately not `selected()`, which answers a different question: that
   *  one is WHICH FILE THE FORM IS SHOWING, and a folder is not a file. One
   *  signal answering both would have to be read as "a request, unless it is
   *  a collection", and Send is gated on it. */
  activeCollection(): string
  selected(): ApiSelection | null
  draft(): ApiRequest | null
  /** True while the draft differs from what the file last answered. */
  dirty(): boolean
  runs(): readonly ApiRun[]
  notes(): readonly ApiImportNote[]
  /** The last failure, in the words the backend used, or '' when the last
   *  thing attempted worked. On the surface rather than in a log: a degrade
   *  the UI does not show is a feature that does not exist surviving a
   *  release. */
  error(): string
  loading(): boolean
  sending(): boolean

  /** The backend's reported refresh mode for the collection watch set, or
   *  null until the first `files.watch` answers — and for a build that
   *  cannot watch at all. */
  watchMode(): 'watching' | 'polling' | null
  /** Why refresh is degraded: non-null only when a LOCAL watch could not be
   *  established and the backend fell back to polling. The persistent badge
   *  renders from this; designed-mode polling carries no reason and warns
   *  about nothing. */
  watchDegradedReason(): string | null
  /** The watch could not be established, in the backend's words, or '' when
   *  the last attempt worked. Both ends of the interval: it is set from the
   *  moment a `files.open` or `files.watch` is refused until the next
   *  successful `files.watch` — which `refresh()` always sends, so the header
   *  action is the retry. */
  watchFailed(): string

  /** Subscribe to the change stream and begin watching. Called once, by the
   *  pane's mount; `dispose()` is the other end. Idempotent. */
  startWatching(): void
  /** Release the watch binding and drop the subscriptions. The collection
   *  handles are NOT released — they belong to the app's opened-folder list
   *  (design §6.1) and closing the tab must not close the user's folders. */
  dispose(): void

  refresh(): Promise<void>
  openFolder(path: string): Promise<void>
  /** Make a collection under `name` and leave it open, selected and in the
   *  list — one call, because `api.collections.create` answers the same
   *  handle-and-collection an open does. */
  createCollection(name: string): Promise<void>
  closeFolder(handle: string): Promise<void>
  openRequest(handle: string, relPath: string): Promise<void>
  editDraft(next: ApiRequest): void
  send(): Promise<void>
  importCurl(line: string): Promise<void>
  importPostman(path: string, dest: string): Promise<void>
  setRunView(id: number, view: ApiRunView): void
}

function message(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

export function createApiStore(services: ApiWorkbenchServices): ApiStore {
  const [collections, setCollections] = createSignal<readonly ApiOpenCollection[]>([])
  const [activeCollection, setActiveCollection] = createSignal('')
  const [selected, setSelected] = createSignal<ApiSelection | null>(null)
  const [draft, setDraft] = createSignal<ApiRequest | null>(null)
  const [saved, setSaved] = createSignal<ApiRequest | null>(null)
  const [runs, setRuns] = createSignal<readonly ApiRun[]>([])
  const [notes, setNotes] = createSignal<readonly ApiImportNote[]>([])
  const [error, setError] = createSignal('')
  const [loading, setLoading] = createSignal(false)
  const [sending, setSending] = createSignal(false)

  // Run ids come from a counter rather than a clock: `Date.now()` gives two
  // runs fired in the same millisecond one id, and a list keyed by a
  // duplicate id renders one row for two exchanges.
  let nextRunId = 1

  // ── Watching (nocx-19rcp) ───────────────────────────────────────────────
  //
  // A collection is a folder on disk, and it changes underneath us. The
  // product already answers "how does a surface learn a directory changed" —
  // files.watch plus the files.changed invalidation — so this uses that and
  // does not invent a second answer inside api.* (AD-8; AGENTS.md's "look for
  // the existing answer before you write a second one").
  //
  // Three properties come from the contract and are kept HERE rather than
  // hoped for:
  //
  //  * files.watch REPLACES the set. So the set is derived from what the
  //    panel renders and re-published whenever that changes — a collection
  //    that has been closed leaves the set by construction, and cannot leak a
  //    watch because nothing has to remember to remove it.
  //  * The published set is recorded at the moment the paths are READ and is
  //    NOT rolled back when the call fails. A rejected watch is a sticky
  //    failure the user retries through the header's Refresh; re-sending the
  //    identical set on the next listing would erase the message it is meant
  //    to leave up. refresh() forgets the record first, which is what makes
  //    that action the retry.
  //  * A newly added path that fails to establish must not take the healthy
  //    watches down. Nothing here tears anything down on failure: the binding
  //    stays, the subscription stays, and a change on a folder that IS still
  //    watched still re-lists it.
  const watcher = services.watchCollections
  const [watchMode, setWatchMode] = createSignal<'watching' | 'polling' | null>(null)
  const [watchDegradedReason, setWatchDegradedReason] = createSignal<string | null>(null)
  const [watchFailed, setWatchFailed] = createSignal('')

  /** The binding every watch call carries, or null while there is none. */
  let bindingId: string | null = null
  /** The set as last handed to files.watch, or null when the backend's set
   *  must be treated as unknown (never sent; dropped by a reconnect; a
   *  refresh deliberately forcing a re-send). */
  let publishedPaths: readonly string[] | null = null
  /** The change subscription and the reconnect hook, so dispose has both
   *  ends of the interval it closes. */
  let unsubscribes: (() => void)[] = []
  /** True from dispose() onwards: a response that lands afterwards must not
   *  paint, and must not re-open a binding nobody is holding. */
  let disposed = false

  /**
   * The collection roots the panel currently renders.
   *
   * A row minted by `api.collections.create` carries NO path — §13.1 leaves
   * the location to the backend and the result does not spell it — so it is
   * not in the set until the next listing fills it in. That is a real gap of
   * one round trip, and it closes itself: createCollection is followed by the
   * user's next refresh or by any change on a folder that IS watched.
   *
   * Deduplicated, because a set is what the wire wants and two rows can name
   * one folder; ordered by the list, because comparing against the published
   * record has to be stable.
   */
  const watchPaths = (): string[] =>
    // Untracked, and it has to be: every caller is an async continuation or a
    // notification handler, never a tracked scope. A subscription taken here
    // would belong to whatever computation happened to be running when the
    // promise resolved — which is nothing at all, so the reads would simply
    // be ignored while looking like they were watched.
    untrack(() => {
      const out: string[] = []
      for (const c of collections()) {
        if (c.path !== '' && !out.includes(c.path)) out.push(c.path)
      }
      return out
    })

  /** Open the binding the watch set is carried on, or answer null when there
   *  is nothing to open one against.
   *
   *  ROOTED AT '/', and it is a carrier rather than a view: the panel never
   *  lists through it, and a collection folder can sit anywhere. `files.open`
   *  needs a session the connection owns and builds THAT session's provider,
   *  so the port hands us a LOCAL one — a collection is backend-local (§13.1)
   *  and an SSH session's binding would watch the wrong machine. */
  const openBinding = async (): Promise<string | null> => {
    if (watcher === undefined || disposed) return null
    if (bindingId !== null) return bindingId
    const sessionId = watcher.localSession()
    if (sessionId === null) return null
    try {
      const res = await watcher.open(sessionId, '/')
      if (disposed) {
        // Disposed while the open was in flight: releasing it here is the
        // only chance anybody gets — nothing else holds the id.
        void watcher.close(res.bindingId).catch(() => undefined)
        return null
      }
      bindingId = res.bindingId
      return bindingId
    } catch (err) {
      if (!disposed) setWatchFailed(message(err))
      return null
    }
  }

  /** Publish the watch set iff what the panel renders has drifted from what
   *  the backend was last told. Every seam that can change the set calls
   *  this and nothing calls files.watch directly, so a set cannot be sent
   *  twice for one change and cannot be missed by a path that forgot. */
  const syncWatchSet = async (): Promise<void> => {
    if (watcher === undefined || disposed) return
    const want = watchPaths()
    if (
      publishedPaths !== null &&
      publishedPaths.length === want.length &&
      publishedPaths.every((path, i) => path === want[i])
    ) {
      return
    }
    // Nothing open and no binding yet: there is nothing to watch, so no
    // binding is minted for an empty set. The record still moves, so the
    // first collection to arrive is a drift and publishes.
    if (want.length === 0 && bindingId === null) {
      publishedPaths = want
      return
    }
    const id = await openBinding()
    if (id === null || disposed) return
    publishedPaths = want
    try {
      const res = await watcher.watch(id, want)
      if (disposed) return
      setWatchFailed('')
      setWatchMode(res.mode)
      setWatchDegradedReason(res.degradedReason ?? null)
    } catch (err) {
      if (disposed) return
      setWatchFailed(message(err))
    }
  }

  /**
   * The server-initiated invalidation: one dirty path, no entries, so exactly
   * one code path re-reads a collection.
   *
   * Two filters, each for a different defect. A change for a binding this
   * store does not follow is not its business — the Files panel's binding,
   * or one from a previous connection. And a path outside every collection
   * root cannot be a collection of ours changing; the watch set is what
   * decides, not a guess.
   */
  const onCollectionChanged = (p: FilesChanged): void => {
    if (disposed || bindingId === null || p.bindingId !== bindingId) return
    const affected = watchPaths().some((root) => p.path === root || p.path.startsWith(`${root}/`))
    if (!affected) return
    relist()
  }

  /** Re-read the open folders because the DISK said so, not because a person
   *  did. It is the same listing the header's action issues — one code path
   *  renders a collection — and it does not force the watch set, so a change
   *  cannot erase a sticky watch failure the user has not retried.
   *
   *  Serialised, and at most one queued: a burst on one folder must not put
   *  five listings on the wire whose responses can land out of order and
   *  paint an older tree over a newer one. */
  let listingChain: Promise<void> = Promise.resolve()
  let relistQueued = false
  const relist = (): void => {
    if (relistQueued) return
    relistQueued = true
    listingChain = listingChain.then(async () => {
      relistQueued = false
      if (disposed) return
      await readCollections()
      await syncWatchSet()
    })
  }

  /** The draft differs from what the file last answered. Compared by value —
   *  the form replaces the object on every keystroke, so identity would say
   *  "dirty" for a field typed into and typed back. */
  const dirty = (): boolean => {
    const d = draft()
    const s = saved()
    if (d === null || s === null) return false
    return JSON.stringify(d) !== JSON.stringify(s)
  }

  /** Re-read the open folders. The one call that renders the list, whoever
   *  asked for it — a person, an import, or the disk. */
  const readCollections = async (): Promise<void> => {
    setLoading(true)
    try {
      const result = await services.listCollections()
      setCollections(result.collections)
      setError('')
    } catch (err) {
      setError(message(err))
    } finally {
      setLoading(false)
    }
  }

  /** The header's action: re-read the folders AND re-establish the watch,
   *  which is what makes it the retry for a watch that failed. Forgetting the
   *  published record first is the whole of that — an unchanged set would
   *  otherwise be suppressed as "already sent", and the sticky failure would
   *  have no way back. */
  const refresh = async (): Promise<void> => {
    await readCollections()
    publishedPaths = null
    await syncWatchSet()
  }

  const openFolder = async (path: string): Promise<void> => {
    try {
      const result = await services.openCollection(path)
      const row: ApiOpenCollection = {
        handle: result.handle,
        path,
        error: '',
        collection: adoptOpenedCollection(result.collection),
      }
      // The handle identifies the row, not the path: re-opening the folder
      // the user already has open must not put a second copy of it in the
      // tree with a stale listing beside a fresh one.
      setCollections((prev) => [...prev.filter((c) => c.handle !== row.handle), row])
      setActiveCollection(row.handle)
      setError('')
    } catch (err) {
      setError(message(err))
    }
    // Outside the try: a folder that joined the list is watched whether or
    // not something else in this call went wrong, and a watch that is refused
    // is reported through watchFailed rather than as the open's failure.
    await syncWatchSet()
  }

  /**
   * Make one, and adopt what came back.
   *
   * ONE CALL, AND THAT IS THE WHOLE POINT OF THE CONTRACT'S SHAPE.
   * `api.collections.create` answers the same `{handle, collection}` an open
   * does — the schema says it is "api.collections.open's on purpose … so the
   * renderer has one thing to do afterwards rather than two, and there is no
   * moment at which a freshly made collection is not addressable". So this
   * neither re-opens the folder nor re-lists: it puts the result straight
   * into the one list, which is what makes the new collection visible and
   * pointed at before any further round trip.
   *
   * There is NO PATH on the row, because the result carries none: §13.1
   * leaves the location to the backend, so the renderer cannot spell where
   * the folder went and does not pretend to. The next listing fills it in.
   */
  const createCollection = async (name: string): Promise<void> => {
    try {
      const result = await services.createCollection(name)
      const row: ApiOpenCollection = {
        handle: result.handle,
        path: '',
        error: '',
        collection: adoptCreatedCollection(result.collection),
      }
      setCollections((prev) => [...prev.filter((c) => c.handle !== row.handle), row])
      setActiveCollection(row.handle)
      setError('')
    } catch (err) {
      // A refused name — blank, a path separator in it, `.`, `..`, a folder
      // already there — is the backend's sentence, and it goes where every
      // other failure goes so the surface can render it. Swallowing it here
      // is what makes a refusal look like a button that does nothing.
      setError(message(err))
    }
    // A created row carries no path (§13.1), so this publishes nothing new
    // today — it is here because the SET is derived from the list and every
    // seam that changes the list republishes it. A seam that decided for
    // itself whether its change could matter is how a path stops being
    // watched without anybody noticing.
    await syncWatchSet()
  }

  const closeFolder = async (handle: string): Promise<void> => {
    try {
      await services.closeCollection(handle)
      setCollections((prev) => prev.filter((c) => c.handle !== handle))
      // Nothing is pointed at a folder that has left.
      if (activeCollection() === handle) setActiveCollection('')
      // The form was showing a request in the folder that just left. Keeping
      // it would leave a Send pointed at a handle that no longer resolves.
      if (selected()?.handle === handle) {
        setSelected(null)
        setDraft(null)
        setSaved(null)
      }
      setError('')
    } catch (err) {
      setError(message(err))
    }
    // The folder has left the list, so it leaves the watch set — the whole
    // set is re-sent without it, which is what the contract's REPLACE
    // semantics turn into "closing a collection cannot leak a watch".
    await syncWatchSet()
  }

  const openRequest = async (handle: string, relPath: string): Promise<void> => {
    try {
      const result = await services.readRequest(handle, relPath)
      setSelected({ handle, relPath })
      setActiveCollection(handle)
      setDraft(result.request)
      setSaved(result.request)
      setError('')
    } catch (err) {
      // The previous request stays in the form. Clearing it would make one
      // unreadable file look like the whole collection went away.
      setError(message(err))
    }
  }

  const editDraft = (next: ApiRequest): void => {
    setDraft(next)
  }

  const send = async (): Promise<void> => {
    const target = selected()
    const request = draft()
    if (target === null || request === null) return
    setSending(true)
    try {
      if (dirty()) {
        // Rule 1: the file is what gets sent, so it holds the draft before
        // the exchange — and a refused write stops here, never sending the
        // request the file still contains under the draft's name.
        await services.writeRequest(target.handle, target.relPath, request)
        setSaved(request)
      }
      const result = await services.sendRequest(target.handle, target.relPath)
      setRuns((prev) => [
        {
          id: nextRunId++,
          method: request.method,
          url: request.url,
          response: result.response,
          error: null,
          view: 'pretty',
        },
        ...prev,
      ])
      setError('')
    } catch (err) {
      const reason = message(err)
      if (dirty()) {
        // The write is the step that failed: nothing went out, so there is
        // no exchange to record — only a reason the file could not be saved.
        setError(reason)
      } else {
        setRuns((prev) => [
          {
            id: nextRunId++,
            method: request.method,
            url: request.url,
            response: null,
            error: reason,
            view: 'pretty',
          },
          ...prev,
        ])
      }
    } finally {
      setSending(false)
    }
  }

  const importCurl = async (line: string): Promise<void> => {
    try {
      const result = await services.importCurl(line)
      // No file behind it yet, so nothing is selected — Send stays refused
      // until the request is saved into a collection, which is honest: there
      // is nothing on disk for api.request.send to send.
      setSelected(null)
      setSaved(null)
      setDraft(adoptImportedRequest(result.request))
      setNotes(result.unsupported)
      setError('')
    } catch (err) {
      setError(message(err))
    }
  }

  const importPostman = async (path: string, dest: string): Promise<void> => {
    try {
      const result = await services.importPostman(path, dest)
      // Both importers' "what did not come across" is one vocabulary — a
      // feature named, and why — so the surface holds one list of them.
      const carried: ApiImportNote[] = result.unsupported satisfies PostmanNote[]
      setNotes(carried)
      setError('')
      // The folder is on disk now; the listing is what puts it in the tree.
      await refresh()
    } catch (err) {
      setError(message(err))
    }
  }

  const setRunView = (id: number, view: ApiRunView): void => {
    setRuns((prev) => prev.map((r) => (r.id === id ? { ...r, view } : r)))
  }

  const startWatching = (): void => {
    if (watcher === undefined || disposed || unsubscribes.length > 0) return
    unsubscribes.push(watcher.subscribeChanged(onCollectionChanged))
    unsubscribes.push(
      watcher.onConnect(() => {
        // A reconnect is a NEW connection, and a binding is bounded by the
        // connection that minted it — so the id we hold addresses nothing and
        // the set the backend was told about is gone with it. Both records
        // are dropped, which makes the next sync re-open and re-send rather
        // than suppress an unchanged set and leave the panel detached from
        // the change stream (AD-9).
        bindingId = null
        publishedPaths = null
        void syncWatchSet()
      }),
    )
    // No sync here: the set is empty until the first listing says what the
    // open folders are, and files.watch with an empty set is a round trip
    // that establishes nothing. refresh() — which the pane's mount issues
    // next — is what publishes.
  }

  const dispose = (): void => {
    disposed = true
    for (const off of unsubscribes) off()
    unsubscribes = []
    const id = bindingId
    bindingId = null
    publishedPaths = null
    if (id !== null && watcher !== undefined) {
      // Its watches go with it (files.close tears them down), so there is no
      // watch-with-an-empty-set first: one call, and a refusal has nobody
      // left to tell.
      void watcher.close(id).catch(() => undefined)
    }
  }

  return {
    collections,
    activeCollection,
    selected,
    draft,
    dirty,
    runs,
    notes,
    error,
    loading,
    sending,
    watchMode,
    watchDegradedReason,
    watchFailed,
    startWatching,
    dispose,
    refresh,
    openFolder,
    createCollection,
    closeFolder,
    openRequest,
    editDraft,
    send,
    importCurl,
    importPostman,
    setRunView,
  }
}

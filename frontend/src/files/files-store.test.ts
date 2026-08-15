// FilesTreeStore — unit tests for the four rules that make the tree correct:
// root immobility (rule 1), origin-scoped staleness with per-node generation
// ordering (rule 2), canonical cycle detection before rendering (rule 3) and
// the state discriminator (rule 4). The store is exercised directly with a
// fake services seam — no DOM, no WebSocket.
//
// Rule 1 in its current form (the product rule, nocx-r3bz): the root is the
// constant FILESYSTEM ROOT — the verified cwd is never handed to files.open —
// and a verified cwd change on the same session REVEALS instead: the chain
// from the root down to the new cwd is expanded and the target selected.
// Nothing ever collapses; an unverified cwd reveals nothing; an origin with
// no opinion (a viewer tab) reveals nothing.
import { describe, expect, it, vi } from 'vitest'
import type { FilesListEntry, FilesListResult } from '../generated/files.list'
import type { FilesChanged } from '../generated/files.changed'
import type { FilesPanelServices } from './files-client'
import { createFilesTreeStore, FILES_PAGE_SIZE, type FilesTreeStore } from './files-store'
import type { ActiveOrigin } from '../tab-content'

// ── Fixtures ──────────────────────────────────────────────────────────────

/** The local terminal: rooted at the filesystem root, cwd VERIFIED but the
 *  root itself — so the mount reveal selects the root (a no-op: the root
 *  has no row) and no walk is issued. The reveal-specific tests use cwds
 *  below the root. */
const LOCAL_A: ActiveOrigin = {
  tabId: 1,
  sessionId: 'session-a',
  kind: 'local',
  cwd: '/',
  cwdVerified: true,
  cwdFollow: true,
  host: null,
}

const SSH_B: ActiveOrigin = {
  tabId: 2,
  sessionId: 'session-b',
  kind: 'ssh',
  cwd: '/home/bob',
  cwdVerified: false,
  cwdFollow: true,
  host: 'srv-b',
}

const OPEN_RESULT = {
  bindingId: 'b1',
  endpointId: null,
  root: { path: '/', display: '/', inferred: false, inferredReason: '' },
}

const entry = (over: Partial<FilesListEntry>): FilesListEntry => ({
  name: 'file',
  path: '/file',
  kind: 'regular',
  size: 0,
  modTime: '2026-08-06T00:00:00Z',
  mode: 0o644,
  ...over,
})

const listOk = (
  canonical: string,
  entries: FilesListEntry[],
  over: Partial<FilesListResult & { state: 'ok' }> = {},
): FilesListResult => ({
  state: 'ok',
  path: '/',
  canonical,
  entries,
  offset: 0,
  total: entries.length,
  hasMore: false,
  rev: 'r1',
  ...over,
})

type Deferred<T> = { promise: Promise<T>; resolve: (v: T) => void; reject: (e: unknown) => void }

function deferred<T>(): Deferred<T> {
  let resolve!: (v: T) => void
  let reject!: (e: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

function makeServices(over: Partial<FilesPanelServices> = {}): FilesPanelServices {
  return {
    open: vi.fn().mockResolvedValue(OPEN_RESULT),
    list: vi.fn().mockResolvedValue(listOk('C:/', [])),
    read: vi.fn().mockResolvedValue({}),
    watch: vi.fn().mockResolvedValue({ mode: 'watching' }),
    reveal: vi.fn().mockResolvedValue({}),
    subscribeFilesChanged: vi.fn().mockReturnValue(() => {}),
    onConnect: vi.fn().mockReturnValue(() => {}),
    close: vi.fn().mockResolvedValue({}),
    ...over,
  }
}

/** Drain the microtask queue until the store's longest open/list/watch/reveal
 *  chain settles. Each reveal level deliberately waits for its watch update
 *  before issuing the next filesystem operation. */
async function settle(): Promise<void> {
  for (let i = 0; i < 20; i++) await Promise.resolve()
}

function nodeRows(store: FilesTreeStore, name: string) {
  const row = store.rows().find((r) => r.kind === 'entry' && r.node.name === name)
  if (!row || row.kind !== 'entry') throw new Error(`no row named ${name}`)
  return row.node
}

/** The canonical tree the reveal tests walk: / → home → alice → notes.md.
 *  The directory named after the path IS the target's parent, so listing
 *  it answers the walk's "find the child" question. */
function revealTreeList(): (bindingId: string, path: string) => Promise<FilesListResult> {
  return (bindingId: string, path: string) => {
    if (path === '/')
      return Promise.resolve(listOk('C:/', [entry({ name: 'home', path: '/home', kind: 'dir' })]))
    if (path === '/home')
      return Promise.resolve(
        listOk('C:/home', [entry({ name: 'alice', path: '/home/alice', kind: 'dir' })]),
      )
    return Promise.resolve(
      listOk('C:/home/alice', [entry({ name: 'notes.md', path: '/home/alice/notes.md' })]),
    )
  }
}

// ── Tests ─────────────────────────────────────────────────────────────────

describe('files tree store', () => {
  it('opens a binding for the origin, pinned to the filesystem root, and lists the root', async () => {
    const open = vi.fn().mockResolvedValue(OPEN_RESULT)
    const list = vi
      .fn()
      .mockResolvedValue(
        listOk('C:/', [entry({ name: 'a.txt' }), entry({ name: 'docs', kind: 'dir' })]),
      )
    const store = createFilesTreeStore(makeServices({ open, list }))
    store.rescope(LOCAL_A)
    await settle()

    expect(store.phase()).toBe('ready')
    expect(open).toHaveBeenCalledWith('session-a', '/')
    expect(list).toHaveBeenCalledWith('b1', '/', 0, FILES_PAGE_SIZE)
    const names = store
      .rows()
      .filter((r) => r.kind === 'entry')
      .map((r) => (r.kind === 'entry' ? r.node.name : ''))
    expect(names).toEqual(['a.txt', 'docs'])
  })

  it('pins the root to the filesystem root even when the cwd is not verified (D2)', async () => {
    const open = vi.fn().mockResolvedValue(OPEN_RESULT)
    const store = createFilesTreeStore(makeServices({ open }))
    store.rescope({ ...LOCAL_A, cwdVerified: false })
    await settle()
    expect(open).toHaveBeenCalledWith('session-a', '/')
  })

  it('a different tab re-scopes: old binding closed, new one opened', async () => {
    const open = vi.fn().mockResolvedValue(OPEN_RESULT)
    const close = vi.fn().mockResolvedValue({})
    const store = createFilesTreeStore(makeServices({ open, close }))
    store.rescope(LOCAL_A)
    await settle()
    store.rescope(SSH_B)
    await settle()

    expect(open).toHaveBeenCalledTimes(2)
    expect(open).toHaveBeenLastCalledWith('session-b', '/')
    expect(close).toHaveBeenCalledWith('b1')
  })

  it('a viewer tab answering its source session keeps the binding (design §5.4)', async () => {
    // TabManager composes the ACTIVE tab's id into the origin, so a viewer
    // tab opened from tab A answers tab A's session with a NEW tabId. Same
    // machine: the binding must stay open — closing it would kill the
    // viewer's in-flight read and render "source unavailable" for a file
    // that was read successfully (the fm-w12 defect).
    const open = vi.fn().mockResolvedValue(OPEN_RESULT)
    const close = vi.fn().mockResolvedValue({})
    const store = createFilesTreeStore(makeServices({ open, close }))
    store.rescope(LOCAL_A)
    await settle()

    store.rescope({ ...LOCAL_A, tabId: 99 })
    await settle()

    expect(open).toHaveBeenCalledTimes(1)
    expect(close).not.toHaveBeenCalled()
    expect(store.phase()).toBe('ready')
  })

  it('expanding a directory reaches files.list and commits its entries', async () => {
    const list = vi
      .fn()
      .mockImplementation((bindingId: string, path: string) =>
        Promise.resolve(
          path === '/'
            ? listOk('C:/', [entry({ name: 'docs', path: '/docs', kind: 'dir' })])
            : listOk('C:/docs', [entry({ name: 'notes.md', path: '/docs/notes.md' })]),
        ),
      )
    const store = createFilesTreeStore(makeServices({ list }))
    store.rescope(LOCAL_A)
    await settle()

    const docs = nodeRows(store, 'docs')
    store.toggle(docs)
    await settle()

    expect(list).toHaveBeenCalledWith('b1', '/docs', 0, FILES_PAGE_SIZE)
    const names = store
      .rows()
      .filter((r) => r.kind === 'entry')
      .map((r) => (r.kind === 'entry' ? r.node.name : ''))
    expect(names).toEqual(['docs', 'notes.md'])
  })

  it('"show next" fetches the next page and reveals the rest (D10)', async () => {
    const first = entry({ name: 'f1' })
    const list = vi.fn().mockImplementation((bindingId: string, path: string, offset: number) =>
      Promise.resolve(
        offset === 0
          ? listOk('C:/', [first], {
              total: 3,
              hasMore: true,
              rev: 'r1',
              path: '/',
            })
          : listOk('C:/', [entry({ name: 'f2' }), entry({ name: 'f3' })], {
              offset: 1,
              total: 3,
              hasMore: false,
              rev: 'r1',
              path: '/',
            }),
      ),
    )
    const store = createFilesTreeStore(makeServices({ list }))
    store.rescope(LOCAL_A)
    await settle()

    expect(store.rows().filter((r) => r.kind === 'entry')).toHaveLength(1)
    expect(store.rows().some((r) => r.kind === 'more')).toBe(true)

    const more = store.rows().find((r) => r.kind === 'more')
    if (!more || more.kind !== 'more') throw new Error('no more row')
    store.showMore(more.dir)
    await settle()

    expect(list).toHaveBeenCalledWith('b1', '/', 1, FILES_PAGE_SIZE)
    const names = store
      .rows()
      .filter((r) => r.kind === 'entry')
      .map((r) => (r.kind === 'entry' ? r.node.name : ''))
    expect(names).toEqual(['f1', 'f2', 'f3'])
    expect(store.rows().some((r) => r.kind === 'more')).toBe(false)
  })

  // ── Rule 1: the root does not move; a verified cwd REVEALS ──────────
  it('a verified cwd change on the same session reveals and selects the target, without re-opening', async () => {
    const open = vi.fn().mockResolvedValue(OPEN_RESULT)
    const list = vi.fn().mockImplementation(revealTreeList())
    const store = createFilesTreeStore(makeServices({ open, list }))
    store.rescope(LOCAL_A)
    await settle()
    const listsBefore = list.mock.calls.length

    // The cd: same session, new verified cwd. The tree must NOT re-open
    // (rule 1 — the root never moves), and must reveal the new cwd.
    store.rescope({ ...LOCAL_A, cwd: '/home/alice' })
    await settle()

    expect(open).toHaveBeenCalledTimes(1)
    expect(store.root()?.path).toBe('/')
    expect(store.revealTarget()).toBe('/home/alice')
    // The chain / → home → alice is expanded, INCLUDING the target: a cd
    // says what the user is now looking at, so answering with a closed
    // folder they must click makes them do the work twice.
    // The superseded walk's own answer is kept: the row is not left spinning.
    expect(nodeRows(store, 'home').expanded).toBe(true)
    expect(nodeRows(store, 'home').busy).toBe(false)
    expect(nodeRows(store, 'home').state).toBe('ok')
    expect(nodeRows(store, 'alice').expanded).toBe(true)
    // The walk listed the levels it had to descend through: / (to find
    // home — once more than the openScope listing, because the root's
    // first list was still in flight when the walk started) and /home.
    expect(list).toHaveBeenCalledWith('b1', '/home', 0, FILES_PAGE_SIZE)
    expect(list.mock.calls.length).toBeGreaterThan(listsBefore)
  })

  it('reveals once when the binding opens, landing where the terminal is', async () => {
    const list = vi.fn().mockImplementation(revealTreeList())
    const store = createFilesTreeStore(makeServices({ list }))
    // A tab activated with its verified cwd already in place: the open
    // itself must reveal — no second rescope needed.
    store.rescope({ ...LOCAL_A, cwd: '/home/alice' })
    await settle()

    expect(store.phase()).toBe('ready')
    expect(store.revealTarget()).toBe('/home/alice')
    // The superseded walk's own answer is kept: the row is not left spinning.
    expect(nodeRows(store, 'home').expanded).toBe(true)
    expect(nodeRows(store, 'home').busy).toBe(false)
    expect(nodeRows(store, 'home').state).toBe('ok')
  })

  it('reveal never collapses a directory the user expanded', async () => {
    const list = vi.fn().mockImplementation((bindingId: string, path: string) => {
      if (path === '/')
        return Promise.resolve(
          listOk('C:/', [
            entry({ name: 'home', path: '/home', kind: 'dir' }),
            entry({ name: 'docs', path: '/docs', kind: 'dir' }),
          ]),
        )
      if (path === '/docs')
        return Promise.resolve(
          listOk('C:/docs', [entry({ name: 'keep.md', path: '/docs/keep.md' })]),
        )
      if (path === '/home')
        return Promise.resolve(
          listOk('C:/home', [entry({ name: 'alice', path: '/home/alice', kind: 'dir' })]),
        )
      return Promise.resolve(
        listOk('C:/home/alice', [entry({ name: 'notes.md', path: '/home/alice/notes.md' })]),
      )
    })
    const store = createFilesTreeStore(makeServices({ list }))
    store.rescope(LOCAL_A)
    await settle()

    // The user opened docs by hand.
    const docs = nodeRows(store, 'docs')
    store.toggle(docs)
    await settle()
    expect(docs.expanded).toBe(true)

    // A cd elsewhere reveals — and leaves docs open.
    store.rescope({ ...LOCAL_A, cwd: '/home/alice' })
    await settle()

    expect(docs.expanded).toBe(true)
    expect(nodeRows(store, 'keep.md')).toBeDefined()
    expect(store.revealTarget()).toBe('/home/alice')
  })

  it('reveal is idempotent: the same path again does no work and re-selects nothing new', async () => {
    const list = vi.fn().mockImplementation(revealTreeList())
    const store = createFilesTreeStore(makeServices({ list }))
    store.rescope({ ...LOCAL_A, cwd: '/home/alice' })
    await settle()
    const listsAfterFirst = list.mock.calls.length
    expect(store.revealTarget()).toBe('/home/alice')

    // The same cwd again (an OSC 7 re-report, or a duplicate origin
    // notification): no work, no new lists.
    store.rescope({ ...LOCAL_A, cwd: '/home/alice' })
    await settle()
    expect(list.mock.calls.length).toBe(listsAfterFirst)
    expect(store.revealTarget()).toBe('/home/alice')

    // The public operation is idempotent the same way.
    store.revealPath('/home/alice')
    await settle()
    expect(list.mock.calls.length).toBe(listsAfterFirst)
    expect(store.revealTarget()).toBe('/home/alice')
  })

  it('the walk pages a level to find the target beyond the first page', async () => {
    const first = entry({ name: 'f0001', path: '/f0001' })
    const list = vi.fn().mockImplementation((bindingId: string, path: string, offset: number) => {
      // The FIRST page of / holds the root's depth-0 rows; the walk pages
      // the SAME path with a higher offset to find the target.
      if (path === '/' && offset === 0)
        return Promise.resolve(listOk('C:/', [first], { total: 60, hasMore: true, path: '/' }))
      // Any later page holds the target directory.
      return Promise.resolve(
        listOk('C:/', [entry({ name: 'target', path: '/target', kind: 'dir' })], {
          offset: 1,
          total: 60,
          hasMore: false,
          path: '/',
        }),
      )
    })
    const store = createFilesTreeStore(makeServices({ list }))
    store.rescope({ ...LOCAL_A, cwd: '/target' })
    await settle()

    expect(store.revealTarget()).toBe('/target')
    // The target is expanded, not merely selected.
    expect(nodeRows(store, 'target').expanded).toBe(true)
  })

  it('a level that comes back tooLarge stops the reveal and is visible', async () => {
    const list = vi
      .fn()
      .mockImplementation((bindingId: string, path: string) =>
        Promise.resolve(
          path === '/'
            ? listOk('C:/', [entry({ name: 'big', path: '/big', kind: 'dir' })])
            : { state: 'tooLarge' as const, observedCount: 12_345, limit: 1_000 },
        ),
      )
    const store = createFilesTreeStore(makeServices({ list }))
    store.rescope({ ...LOCAL_A, cwd: '/big/deeper' })
    await settle()

    // The walk expanded /big (the last reachable level) and stopped there
    // honestly: the target was never selected, and the tooLarge state row
    // is rendered where the reveal stopped.
    expect(store.revealTarget()).toBeNull()
    expect(nodeRows(store, 'big').expanded).toBe(true)
    const stateRows = store.rows().filter((r) => r.kind === 'state')
    expect(stateRows).toHaveLength(1)
    if (stateRows[0]?.kind !== 'state') throw new Error('no state row')
    expect(stateRows[0].dir.state).toBe('tooLarge')
  })

  it('a path that does not exist under the root stops the reveal honestly', async () => {
    const list = vi.fn().mockImplementation(revealTreeList())
    const store = createFilesTreeStore(makeServices({ list }))
    store.rescope({ ...LOCAL_A, cwd: '/home/nobody' })
    await settle()

    expect(store.revealTarget()).toBeNull()
    // /home is expanded (the walk descended it), nothing deeper.
    // The superseded walk's own answer is kept: the row is not left spinning.
    expect(nodeRows(store, 'home').expanded).toBe(true)
    expect(nodeRows(store, 'home').busy).toBe(false)
    expect(nodeRows(store, 'home').state).toBe('ok')
    expect(store.rows().some((r) => r.kind === 'entry' && r.node.name === 'nobody')).toBe(false)
  })

  it('switching to a viewer tab moves nothing', async () => {
    const list = vi.fn().mockImplementation(revealTreeList())
    const store = createFilesTreeStore(makeServices({ list }))
    store.rescope(LOCAL_A)
    await settle()
    const listsBefore = list.mock.calls.length
    expect(store.revealTarget()).toBe('/')

    // A viewer tab answers the same session with NO opinion (cwdFollow
    // false): the panel keeps its tree and binding, and nothing reveals.
    store.rescope({ ...LOCAL_A, tabId: 99, cwdFollow: false, cwd: '/home/alice' })
    await settle()

    expect(list.mock.calls.length).toBe(listsBefore)
    expect(store.revealTarget()).toBe('/')
  })

  it('an unverified cwd reveals nothing', async () => {
    const list = vi.fn().mockImplementation(revealTreeList())
    const store = createFilesTreeStore(makeServices({ list }))
    store.rescope(LOCAL_A)
    await settle()
    const listsBefore = list.mock.calls.length

    store.rescope({ ...LOCAL_A, cwd: '/home/alice', cwdVerified: false })
    await settle()

    expect(list.mock.calls.length).toBe(listsBefore)
    expect(store.revealTarget()).toBe('/')
    // The root listing's row is there — but nothing was EXPANDED: the
    // reveal walked nothing (an unverified cwd reveals nothing, AD-5).
    expect(store.rows().some((r) => r.kind === 'entry' && r.node.name === 'home')).toBe(true)
    expect(nodeRows(store, 'home').expanded).toBe(false)
  })

  it('a reveal in flight when the cwd changes drops, not paints', async () => {
    // The walk to /home/old hangs on the /home listing; the cwd changes
    // to /home/new before it resolves. The old walk's continuation must
    // not select a stale target — a reveal in flight when the origin
    // changes must drop, not paint.
    const rootList = deferred<FilesListResult>()
    const homeList = deferred<FilesListResult>()
    const list = vi.fn().mockImplementation((bindingId: string, path: string) => {
      if (path === '/') return rootList.promise
      if (path === '/home') return homeList.promise
      return Promise.resolve(
        listOk('C:/home/new', [entry({ name: 'new.md', path: '/home/new/new.md' })]),
      )
    })
    const store = createFilesTreeStore(makeServices({ list }))
    store.rescope({ ...LOCAL_A, cwd: '/home/old' })
    await settle()
    rootList.resolve(listOk('C:/', [entry({ name: 'home', path: '/home', kind: 'dir' })]))
    await settle()

    // The cwd moved while the walk to /home/old was still listing /home.
    store.rescope({ ...LOCAL_A, cwd: '/home/new' })
    await settle()
    // The /home listing that resolves now serves the NEW walk: it holds
    // the new target (and the old cwd as an ordinary row).
    homeList.resolve(
      listOk('C:/home', [
        entry({ name: 'new', path: '/home/new', kind: 'dir' }),
        entry({ name: 'old', path: '/home/old', kind: 'dir' }),
      ]),
    )
    await settle()

    // The selection is the NEW cwd, and the OLD walk never painted: its
    // continuation would have EXPANDED the old cwd's row on the way to
    // selecting it — the row exists (the new walk applied the listing)
    // but stayed collapsed. The NEW target is expanded, so its own child
    // is a row: that is the reveal landing, not the dropped walk painting.
    expect(store.revealTarget()).toBe('/home/new')
    expect(nodeRows(store, 'old').expanded).toBe(false)
    expect(nodeRows(store, 'new').expanded).toBe(true)
    expect(store.rows().some((r) => r.kind === 'entry' && r.node.name === 'new.md')).toBe(true)
  })

  // ── Rule 2: the §0 test, at the store's level ──────────────────────────
  it('drops a listing for tab A that resolves after the user activated tab B', async () => {
    const aRootList = deferred<FilesListResult>()
    const list = vi
      .fn()
      .mockResolvedValueOnce(aRootList.promise) // A's root listing, still in flight
      .mockResolvedValueOnce(listOk('C:/', [entry({ name: 'b-only.txt', path: '/b-only.txt' })]))
    const store = createFilesTreeStore(makeServices({ list }))
    store.rescope(LOCAL_A)
    await settle() // A's binding opens; A's root list hangs

    store.rescope(SSH_B)
    await settle() // B's binding opens; B's root list applies

    // A's listing finally lands — it must not paint A's machine into B's tree.
    aRootList.resolve(listOk('C:/', [entry({ name: 'a-only.txt', path: '/a-only.txt' })]))
    await settle()

    const names = store
      .rows()
      .filter((r) => r.kind === 'entry')
      .map((r) => (r.kind === 'entry' ? r.node.name : ''))
    expect(names).toEqual(['b-only.txt'])
    expect(names).not.toContain('a-only.txt')
  })

  it('drops a response older than what has already been applied to the same node', async () => {
    const oldExpand = deferred<FilesListResult>()
    const refreshList = deferred<FilesListResult>()
    const docsEntry = entry({ name: 'docs', path: '/docs', kind: 'dir' })
    let docsLists = 0
    const list = vi.fn().mockImplementation((bindingId: string, path: string) => {
      if (path === '/') return Promise.resolve(listOk('C:/', [docsEntry]))
      docsLists += 1
      // First docs list = the expand (gen 1, hangs); second = the refresh
      // re-list (gen 2, hangs) — refresh() also re-lists the root, so the
      // mock must key on the path, not on call order.
      return docsLists === 1 ? oldExpand.promise : refreshList.promise
    })
    const store = createFilesTreeStore(makeServices({ list }))
    store.rescope(LOCAL_A)
    await settle()

    const docs = nodeRows(store, 'docs')
    store.toggle(docs)
    await settle() // the expand is in flight

    store.refresh() // supersedes the expand: generation bumps, docs re-listed
    await settle()

    // The NEWER response lands first and is applied...
    refreshList.resolve(
      listOk('C:/docs', [entry({ name: 'new.md', path: '/docs/new.md' })], {
        path: '/docs',
      }),
    )
    await settle()

    // ...then the OLD expand lands: the generation is older than what was
    // applied, so its page must not overwrite the fresh listing.
    oldExpand.resolve(
      listOk('C:/docs', [entry({ name: 'old.md', path: '/docs/old.md' })], {
        path: '/docs',
      }),
    )
    await settle()

    const names = store
      .rows()
      .filter((r) => r.kind === 'entry')
      .map((r) => (r.kind === 'entry' ? r.node.name : ''))
    expect(names).toEqual(['docs', 'new.md'])
    expect(names).not.toContain('old.md')
  })

  // ── Rule 3: cycle detection ────────────────────────────────────────────
  it('marks a symlink whose canonical matches an expanded ancestor cyclic and lists nothing', async () => {
    const list = vi.fn().mockImplementation((bindingId: string, path: string) =>
      Promise.resolve(
        path === '/'
          ? listOk('C:/', [
              entry({
                name: 'loop',
                path: '/loop',
                kind: 'symlink',
                linkKind: 'dir',
                linkTarget: '/',
              }),
            ])
          : listOk('C:/', [entry({ name: 'leak.md', path: '/leak.md' })]),
      ),
    )
    const store = createFilesTreeStore(makeServices({ list }))
    store.rescope(LOCAL_A)
    await settle()

    const loop = nodeRows(store, 'loop')
    store.toggle(loop)
    await settle()

    expect(loop.cyclic).toBe(true)
    expect(loop.expanded).toBe(false)
    // No children were committed — the listing never flashed.
    const names = store
      .rows()
      .filter((r) => r.kind === 'entry')
      .map((r) => (r.kind === 'entry' ? r.node.name : ''))
    expect(names).toEqual(['loop'])

    // It is never requested again.
    const loopLists = list.mock.calls.filter(([, path]) => path === '/loop')
    expect(loopLists).toHaveLength(1)
    store.toggle(loop)
    await settle()
    expect(list.mock.calls.filter(([, path]) => path === '/loop')).toHaveLength(1)
  })

  it('detects a cycle against a non-parent ancestor (root) too', async () => {
    const list = vi.fn().mockImplementation((bindingId: string, path: string) => {
      if (path === '/')
        return Promise.resolve(
          listOk('C:root', [entry({ name: 'sub', path: '/sub', kind: 'dir' })]),
        )
      if (path === '/sub')
        return Promise.resolve(
          listOk('C:sub', [
            entry({
              name: 'up',
              path: '/sub/up',
              kind: 'symlink',
              linkKind: 'dir',
              linkTarget: '/',
            }),
          ]),
        )
      // up resolves to the ROOT's canonical — an expanded ancestor that is
      // not its parent.
      return Promise.resolve(listOk('C:root', [entry({ name: 'root-child' })]))
    })
    const store = createFilesTreeStore(makeServices({ list }))
    store.rescope(LOCAL_A)
    await settle()

    const sub = nodeRows(store, 'sub')
    store.toggle(sub)
    await settle()

    const up = nodeRows(store, 'up')
    store.toggle(up)
    await settle()

    expect(up.cyclic).toBe(true)
    expect(
      store
        .rows()
        .filter((r) => r.kind === 'entry')
        .map((r) => (r.kind === 'entry' ? r.node.name : '')),
    ).toEqual(['sub', 'up'])
  })

  // ── Rule 4: the state discriminator ────────────────────────────────────
  it('renders tooLarge as a real state with no pagination offered (D14)', async () => {
    const list = vi
      .fn()
      .mockImplementation((bindingId: string, path: string) =>
        Promise.resolve(
          path === '/'
            ? listOk('C:/', [entry({ name: 'big', path: '/big', kind: 'dir' })])
            : { state: 'tooLarge' as const, observedCount: 12_345, limit: 1_000 },
        ),
      )
    const store = createFilesTreeStore(makeServices({ list }))
    store.rescope(LOCAL_A)
    await settle()

    const big = nodeRows(store, 'big')
    store.toggle(big)
    await settle()

    expect(big.state).toBe('tooLarge')
    expect(big.tooLargeLimit).toBe(1_000)
    expect(big.observedCount).toBe(12_345)
    expect(big.children).toHaveLength(0)
    expect(big.hasMore).toBe(false)
    expect(store.rows().some((r) => r.kind === 'state')).toBe(true)
    expect(store.rows().some((r) => r.kind === 'more')).toBe(false)
  })

  it('renders timedOut as its own state and retries the same enumeration', async () => {
    let calls = 0
    const list = vi.fn().mockImplementation((bindingId: string, path: string) => {
      calls += 1
      if (path === '/')
        return Promise.resolve(listOk('C:/', [entry({ name: 'slow', path: '/slow', kind: 'dir' })]))
      return calls === 2
        ? Promise.resolve({ state: 'timedOut' as const, timeout: 5_000 })
        : Promise.resolve(listOk('C:/slow', [entry({ name: 'x.md' })]))
    })
    const store = createFilesTreeStore(makeServices({ list }))
    store.rescope(LOCAL_A)
    await settle()

    const slow = nodeRows(store, 'slow')
    store.toggle(slow)
    await settle()

    expect(slow.state).toBe('timedOut')
    expect(slow.timeout).toBe(5_000)
    expect(slow.children).toHaveLength(0)

    store.retry(slow)
    await settle()
    expect(slow.state).toBe('ok')
    expect(
      store
        .rows()
        .filter((r) => r.kind === 'entry')
        .map((r) => (r.kind === 'entry' ? r.node.name : '')),
    ).toEqual(['slow', 'x.md'])
  })

  it('a rejected list is a rendered error state, never a silent empty directory', async () => {
    const list = vi
      .fn()
      .mockImplementation((bindingId: string, path: string) =>
        Promise.resolve(
          path === '/'
            ? listOk('C:/', [entry({ name: 'secret', path: '/secret', kind: 'dir' })])
            : Promise.reject(new Error('permission denied')),
        ),
      )
    const store = createFilesTreeStore(makeServices({ list }))
    store.rescope(LOCAL_A)
    await settle()

    const secret = nodeRows(store, 'secret')
    store.toggle(secret)
    await settle()

    expect(secret.state).toBe('error')
    expect(secret.error).toContain('permission denied')
    expect(secret.children).toHaveLength(0)
  })

  it('a tooLarge ROOT is a state too', async () => {
    const list = vi.fn().mockResolvedValue({ state: 'tooLarge' as const, limit: 1_000 })
    const store = createFilesTreeStore(makeServices({ list }))
    store.rescope(LOCAL_A)
    await settle()

    expect(store.phase()).toBe('ready')
    const stateRows = store.rows().filter((r) => r.kind === 'state')
    expect(stateRows).toHaveLength(1)
    if (stateRows[0]?.kind !== 'state') throw new Error('no state row')
    expect(stateRows[0].dir.state).toBe('tooLarge')
  })

  it('dispose closes the binding and resets; a later rescope re-opens', async () => {
    const close = vi.fn().mockResolvedValue({})
    const store = createFilesTreeStore(makeServices({ close }))
    store.rescope(LOCAL_A)
    await settle()
    expect(store.phase()).toBe('ready')

    store.dispose()
    expect(close).toHaveBeenCalledWith('b1')
    expect(store.phase()).toBe('no-origin')
    expect(store.rows()).toHaveLength(0)
    expect(store.revealTarget()).toBeNull()

    store.rescope(SSH_B)
    await settle()
    expect(store.phase()).toBe('ready')
    expect(close).toHaveBeenCalledTimes(1)
  })

  // ── Watching (fm-w13 part 2) ─────────────────────────────────────────

  it('sends files.watch with the root when the binding opens', async () => {
    const watch = vi.fn().mockResolvedValue({ mode: 'watching' })
    const store = createFilesTreeStore(makeServices({ watch }))
    store.rescope(LOCAL_A)
    await settle()

    // The root's rows are on screen from the first list, so the initial
    // watch set is the root alone (the e2e clause).
    expect(watch).toHaveBeenLastCalledWith('b1', ['/'])
  })

  it('waits for the initial root listing before installing its watch', async () => {
    const rootList = deferred<FilesListResult>()
    const list = vi.fn().mockReturnValue(rootList.promise)
    const watch = vi.fn().mockResolvedValue({ mode: 'watching' })
    const store = createFilesTreeStore(makeServices({ list, watch }))

    store.rescope(LOCAL_A)
    await settle()

    expect(list).toHaveBeenCalledWith('b1', '/', 0, FILES_PAGE_SIZE)
    expect(watch).not.toHaveBeenCalled()

    rootList.resolve(listOk('C:/', []))
    await settle()

    expect(watch).toHaveBeenCalledOnce()
    expect(watch).toHaveBeenCalledWith('b1', ['/'])
  })

  it('does not watch a newly expanded directory before its first listing succeeds', async () => {
    const docsList = deferred<FilesListResult>()
    const list = vi
      .fn()
      .mockResolvedValueOnce(listOk('C:/', [entry({ name: 'docs', path: '/docs', kind: 'dir' })]))
      .mockReturnValueOnce(docsList.promise)
    const watch = vi.fn().mockResolvedValue({ mode: 'watching' })
    const store = createFilesTreeStore(makeServices({ list, watch }))
    store.rescope(LOCAL_A)
    await settle()
    expect(watch).toHaveBeenCalledOnce()

    store.toggle(nodeRows(store, 'docs'))
    await settle()

    expect(list).toHaveBeenLastCalledWith('b1', '/docs', 0, FILES_PAGE_SIZE)
    expect(watch).toHaveBeenCalledOnce()

    docsList.resolve({
      state: 'tooLarge',
      limit: 5000,
      observedCount: 5001,
    })
    await settle()

    expect(watch).toHaveBeenCalledOnce()
    expect(watch).toHaveBeenLastCalledWith('b1', ['/'])
  })

  it('expanding a directory adds it to the watch set and collapsing removes it', async () => {
    const watch = vi.fn().mockResolvedValue({ mode: 'watching' })
    const list = vi
      .fn()
      .mockImplementation((bindingId: string, path: string) =>
        Promise.resolve(
          path === '/'
            ? listOk('C:/', [entry({ name: 'docs', path: '/docs', kind: 'dir' })])
            : listOk('C:/docs', [entry({ name: 'a.md', path: '/docs/a.md' })]),
        ),
      )
    const store = createFilesTreeStore(makeServices({ watch, list }))
    store.rescope(LOCAL_A)
    await settle()
    expect(watch).toHaveBeenLastCalledWith('b1', ['/'])

    const docs = nodeRows(store, 'docs')
    store.toggle(docs)
    await settle()
    expect(watch).toHaveBeenLastCalledWith('b1', ['/', '/docs'])

    store.toggle(docs)
    await settle()
    expect(watch).toHaveBeenLastCalledWith('b1', ['/'])
  })

  it('a reveal walk adds the expanded levels to the watch set', async () => {
    const watch = vi.fn().mockResolvedValue({ mode: 'watching' })
    const list = vi.fn().mockImplementation(revealTreeList())
    const store = createFilesTreeStore(makeServices({ watch, list }))
    store.rescope({ ...LOCAL_A, cwd: '/home/alice' })
    await settle()

    // One baseline after the initial root and one after the whole reveal —
    // never one per level, which would make a deep remote cwd quadratic.
    expect(watch).toHaveBeenCalledTimes(2)
    expect(watch).toHaveBeenLastCalledWith('b1', ['/', '/home', '/home/alice'])
  })

  // The set changes when a listing SUCCEEDS, not when a walk decides to
  // expand — and those are different moments with an await between them. A
  // walk superseded in that gap never reaches its own end, so nothing at the
  // walk level can publish what it opened: reveal /home/alice expands /home
  // and starts listing it, `cd /` supersedes the walk and lands instantly
  // with nothing of its own to add, and /home is left rendered and outside
  // the backend's watch set — a directory the user is looking at that never
  // reports a change again.
  it('a superseded reveal still watches the level it opened, once its listing lands', async () => {
    const homeList = deferred<FilesListResult>()
    const watch = vi.fn().mockResolvedValue({ mode: 'watching' })
    const list = vi.fn().mockImplementation((bindingId: string, path: string) => {
      if (path === '/')
        return Promise.resolve(
          listOk('C:/', [
            entry({ name: 'home', path: '/home', kind: 'dir' }),
            entry({ name: 'a.txt', path: '/a.txt' }),
          ]),
        )
      if (path === '/home') return homeList.promise
      return Promise.resolve(listOk('C:/home/alice', []))
    })
    const store = createFilesTreeStore(makeServices({ watch, list }))
    store.rescope(LOCAL_A)
    await settle()
    expect(watch).toHaveBeenLastCalledWith('b1', ['/'])

    // Walk 1 expands /home and blocks on its listing.
    store.revealPath('/home/alice')
    await settle()
    // Walk 2 lands immediately: a file opens nothing and ends the walk.
    store.revealPath('/a.txt')
    await settle()

    homeList.resolve(
      listOk('C:/home', [entry({ name: 'alice', path: '/home/alice', kind: 'dir' })]),
    )
    await settle()

    // The superseded walk's own answer is kept: the row is not left spinning.
    expect(nodeRows(store, 'home').expanded).toBe(true)
    expect(nodeRows(store, 'home').busy).toBe(false)
    expect(nodeRows(store, 'home').state).toBe('ok')
    expect(watch).toHaveBeenLastCalledWith('b1', ['/', '/home'])
  })

  it('a files.changed for a loaded path triggers exactly one re-list and expansion survives', async () => {
    const list = vi
      .fn()
      .mockImplementation((bindingId: string, path: string) =>
        Promise.resolve(
          path === '/'
            ? listOk('C:/', [entry({ name: 'docs', path: '/docs', kind: 'dir' })])
            : listOk('C:/docs', [entry({ name: 'old.md', path: '/docs/old.md' })]),
        ),
      )
    let handler: ((p: FilesChanged) => void) | null = null
    const subscribeFilesChanged = vi.fn((h: (p: FilesChanged) => void) => {
      handler = h
      return () => {}
    })
    const store = createFilesTreeStore(makeServices({ list, subscribeFilesChanged }))
    store.rescope(LOCAL_A)
    await settle()
    const docs = nodeRows(store, 'docs')
    store.toggle(docs)
    await settle()
    expect(list.mock.calls.filter(([, p]) => p === '/docs')).toHaveLength(1)

    handler!({ bindingId: 'b1', path: '/docs' })
    await settle()

    // Exactly ONE re-list, from the top of the displayed window.
    const docsLists = list.mock.calls.filter(([, p]) => p === '/docs')
    expect(docsLists).toHaveLength(2)
    expect(docsLists[1]?.[2]).toBe(0)
    // Expansion state survives the re-list (mergeChildren keeps identity).
    expect(docs.expanded).toBe(true)
    expect(store.rows().some((r) => r.kind === 'entry' && r.node.name === 'old.md')).toBe(true)
  })

  it('a files.changed whose rev matches the applied rev triggers no re-list', async () => {
    const list = vi
      .fn()
      .mockImplementation((bindingId: string, path: string) =>
        Promise.resolve(
          path === '/'
            ? listOk('C:/', [entry({ name: 'docs', path: '/docs', kind: 'dir' })])
            : listOk('C:/docs', [entry({ name: 'a.md' })], { rev: 'r9' }),
        ),
      )
    let handler: ((p: FilesChanged) => void) | null = null
    const subscribeFilesChanged = vi.fn((h: (p: FilesChanged) => void) => {
      handler = h
      return () => {}
    })
    const store = createFilesTreeStore(makeServices({ list, subscribeFilesChanged }))
    store.rescope(LOCAL_A)
    await settle()
    const docs = nodeRows(store, 'docs')
    store.toggle(docs)
    await settle()

    handler!({ bindingId: 'b1', path: '/docs', rev: 'r9' })
    await settle()

    // The notification's rev is what the applied listing already carries:
    // the change is on screen, nothing is re-listed.
    expect(list.mock.calls.filter(([, p]) => p === '/docs')).toHaveLength(1)
  })

  it('a files.changed for another binding is ignored', async () => {
    const list = vi.fn().mockResolvedValue(listOk('C:/', [entry({ name: 'a.txt' })]))
    let handler: ((p: FilesChanged) => void) | null = null
    const subscribeFilesChanged = vi.fn((h: (p: FilesChanged) => void) => {
      handler = h
      return () => {}
    })
    const store = createFilesTreeStore(makeServices({ list, subscribeFilesChanged }))
    store.rescope(LOCAL_A)
    await settle()

    handler!({ bindingId: 'some-other-binding', path: '/' })
    await settle()

    // Just the open listing — the foreign notification repainted nothing.
    expect(list).toHaveBeenCalledTimes(1)
  })

  it('a files.changed for a path that is not loaded is ignored', async () => {
    const list = vi.fn().mockResolvedValue(listOk('C:/', [entry({ name: 'a.txt' })]))
    let handler: ((p: FilesChanged) => void) | null = null
    const subscribeFilesChanged = vi.fn((h: (p: FilesChanged) => void) => {
      handler = h
      return () => {}
    })
    const store = createFilesTreeStore(makeServices({ list, subscribeFilesChanged }))
    store.rescope(LOCAL_A)
    await settle()

    handler!({ bindingId: 'b1', path: '/not-loaded' })
    await settle()
    expect(list).toHaveBeenCalledTimes(1)
  })

  it('a files.changed during a reveal walk does not truncate the walk (nocx-r3bz)', async () => {
    // The walk is paging a directory whose first page does not hold the
    // target. A files.changed for that directory arrives mid-walk: a
    // refresh issued now would re-list `offset=0, limit=<first-page
    // count>` and, landing after the walk's later pages, replace them —
    // the accumulated rows shrink back to page 1 and the revealed target
    // vanishes. The change must wait for the walk (whose pages are the
    // freshest data for the dir it is walking).
    const rootList = deferred<FilesListResult>()
    const page1 = deferred<FilesListResult>()
    const page2 = deferred<FilesListResult>()
    let bigLists = 0
    const list = vi.fn().mockImplementation((bindingId: string, path: string) => {
      if (path === '/') return rootList.promise
      if (path === '/big') {
        bigLists += 1
        return bigLists === 1 ? page1.promise : page2.promise
      }
      // The revealed target is EXPANDED, so it is listed too — its own
      // listing, which is why bigLists counts only its parent's pages.
      return Promise.resolve(
        listOk('C:/big/target', [
          entry({ name: 'in-target.md', path: '/big/target/in-target.md' }),
        ]),
      )
    })
    let handler: ((p: FilesChanged) => void) | null = null
    const subscribeFilesChanged = vi.fn((h: (p: FilesChanged) => void) => {
      handler = h
      return () => {}
    })
    const store = createFilesTreeStore(makeServices({ list, subscribeFilesChanged }))
    store.rescope({ ...LOCAL_A, cwd: '/big/target' })
    await settle()
    rootList.resolve(listOk('C:/', [entry({ name: 'big', path: '/big', kind: 'dir' })]))
    await settle()
    // The walk is now hanging on /big's first page (bigLists === 1).
    expect(bigLists).toBe(1)

    // A change for /big lands while the walk is mid-pagination. The
    // refresh must NOT be issued: it would truncate the walk.
    handler!({ bindingId: 'b1', path: '/big' })
    await settle()
    expect(bigLists).toBe(1)

    // The walk's pages continue and complete; the target is selected and
    // both pages' rows are present (nothing was replaced by a stale
    // refresh response).
    page1.resolve(
      listOk('C:/big', [entry({ name: 'f0001', path: '/big/f0001' })], {
        total: 60,
        hasMore: true,
        path: '/big',
      }),
    )
    await settle()
    expect(bigLists).toBe(2)
    page2.resolve(
      listOk('C:/big', [entry({ name: 'target', path: '/big/target', kind: 'dir' })], {
        offset: 1,
        total: 60,
        hasMore: false,
        path: '/big',
      }),
    )
    await settle()

    expect(store.revealTarget()).toBe('/big/target')
    expect(nodeRows(store, 'f0001')).toBeDefined()
    expect(nodeRows(store, 'target').expanded).toBe(true)
    expect(bigLists).toBe(2)
  })

  it('reports the refresh mode and a degraded reason for a local fallback', async () => {
    const watch = vi.fn().mockResolvedValue({ mode: 'polling', degradedReason: 'no fsnotify' })
    const store = createFilesTreeStore(makeServices({ watch }))
    store.rescope(LOCAL_A)
    await settle()

    expect(store.watchMode()).toBe('polling')
    expect(store.watchDegradedReason()).toBe('no fsnotify')
    expect(store.watchFailed()).toBeNull()
  })

  it('a rejected files.watch is a sticky failure cleared by the refresh cycle', async () => {
    const watch = vi
      .fn()
      .mockRejectedValueOnce(new Error('not connected'))
      .mockResolvedValue({ mode: 'watching' })
    const store = createFilesTreeStore(makeServices({ watch }))
    store.rescope(LOCAL_A)
    await settle()
    expect(store.watchFailed()).toContain('not connected')

    store.refresh()
    await settle()
    expect(store.watchFailed()).toBeNull()
    expect(store.watchMode()).toBe('watching')
  })

  it('re-sends the watch set when the connection re-establishes', async () => {
    let connHandler: (() => void) | null = null
    const onConnect = vi.fn((h: () => void) => {
      connHandler = h
      return () => {}
    })
    const watch = vi.fn().mockResolvedValue({ mode: 'watching' })
    const store = createFilesTreeStore(makeServices({ onConnect, watch }))
    store.rescope(LOCAL_A)
    await settle()
    const callsBefore = watch.mock.calls.length

    connHandler!()
    await settle()
    expect(watch.mock.calls.length).toBe(callsBefore + 1)
    expect(watch).toHaveBeenLastCalledWith('b1', ['/'])
  })

  it('dispose unsubscribes from the change stream and the reconnect hook', async () => {
    const unsubChanged = vi.fn()
    const unsubConnect = vi.fn()
    const subscribeFilesChanged = vi.fn(() => unsubChanged)
    const onConnect = vi.fn(() => unsubConnect)
    const store = createFilesTreeStore(makeServices({ subscribeFilesChanged, onConnect }))
    store.rescope(LOCAL_A)
    await settle()

    store.dispose()
    expect(unsubChanged).toHaveBeenCalledTimes(1)
    expect(unsubConnect).toHaveBeenCalledTimes(1)
  })
})

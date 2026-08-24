// @vitest-environment jsdom
// The store is pure logic, but it imports POLL_INTERVAL_MS from ports.tsx
// — the shared sidebar cadence, by the brief's rule — and that module's
// component dependencies need a DOM at load time.
// GitStore — unit tests for the §5.4 contract, without a socket: the eight
// render states each reachable; the four D17 races by name (the diff race is
// the view's, proven in git-view.test.tsx — the store's half here is that a
// re-bind supersedes and closes); the amend-token sequences; the mutation
// lane; the stale-open close; the failed poll leaving the last good status on
// screen; cwdFollow:false never re-binding; and D4's answer-decides binding.
// The store is exercised directly with a fake services seam (the files
// pattern, files-store.test.ts).
import { afterEach, describe, expect, it, vi } from 'vitest'
import { RpcError } from '../dispatcher'
import type { ActiveOrigin } from '../pane-content'
import type { Status, GitStatusResult } from '../generated/git.status'
import type { GitOpenResult } from '../generated/git.open'
import type { GitHeadMessageResult } from '../generated/git.headMessage'
import type { GitCommitResult } from '../generated/git.commit'
import type { GitPanelServices } from './git-client'
import type { GitLogResult } from '../generated/git.log'
import type { GitRemoteResult } from '../generated/git.remote'
import { createGitStore, type GitStore } from './git-store'

// ── Fixtures ──────────────────────────────────────────────────────────────

const LOCAL_ORIGIN: ActiveOrigin = {
  paneId: 1,
  sessionId: 's1',
  kind: 'local',
  cwd: '/home/dev/repo',
  cwdVerified: true,
  cwdFollow: true,
  host: null,
}

/** The same session, a NEW verified cwd — the trigger that re-asks
 *  git.open (D4). */
const NEW_CWD_ORIGIN: ActiveOrigin = { ...LOCAL_ORIGIN, cwd: '/home/dev/repo/sub' }

const OTHER_ORIGIN: ActiveOrigin = {
  paneId: 2,
  sessionId: 's2',
  kind: 'local',
  cwd: '/home/dev/other',
  cwdVerified: true,
  cwdFollow: true,
  host: null,
}

const SSH_ORIGIN: ActiveOrigin = {
  paneId: 3,
  sessionId: 's3',
  kind: 'ssh',
  cwd: '/home/bob',
  cwdVerified: true,
  cwdFollow: true,
  host: 'srv',
}

const NO_CWD_ORIGIN: ActiveOrigin = { ...LOCAL_ORIGIN, cwd: null, cwdVerified: false }

/** A diff tab's frozen origin: the same machine, NO opinion about where we
 *  are now — the store must never re-bind for it (design §5.4). */
const FROZEN_ORIGIN: ActiveOrigin = { ...LOCAL_ORIGIN, paneId: 9, cwdFollow: false }

const statusFixture = (over: Partial<Status> = {}): Status => ({
  branch: 'main',
  detached: false,
  unborn: false,
  head: 'abc1234',
  upstream: 'origin/main',
  ahead: 1,
  behind: 0,
  staged: [],
  unstaged: [],
  conflicted: [],
  total: 0,
  completeness: 'complete',
  ...over,
})

const openOk = (over: Partial<GitOpenResult & { state: 'ok' }> = {}): GitOpenResult => ({
  state: 'ok',
  bindingId: 'b1',
  toplevel: '/home/dev/repo',
  gitVersion: '2.55.0',
  envState: 'resolved',
  status: statusFixture(),
  ...over,
})

/** A git.status result. envState defaults to resolved — a machine where
 *  the resolution settled before open — and tests that script the
 *  pre-settle window or a failure override it. */
const statusResult = (
  over: Partial<Status> = {},
  env: Partial<Pick<GitStatusResult, 'envState' | 'envReason'>> = {},
): GitStatusResult => ({
  status: statusFixture(over),
  envState: 'resolved',
  ...env,
})

const logFixture = (over: Partial<GitLogResult['log']> = {}): GitLogResult['log'] => ({
  entries: [
    {
      hash: '5738d62b66777a78af894c0708d3a7e8798a4d8d',
      shortHash: '5738d62',
      subject: 'third',
      authorName: 'Test Author',
      authoredAt: '2026-08-07T12:52:40+03:00',
      refs: ['main'],
    },
  ],
  total: 1,
  completeness: 'complete',
  ...over,
})

const logResult = (over: Partial<GitLogResult['log']> = {}): GitLogResult => ({
  log: logFixture(over),
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

function makeServices(over: Partial<GitPanelServices> = {}): GitPanelServices {
  return {
    open: vi.fn().mockResolvedValue(openOk()),
    status: vi.fn().mockResolvedValue(statusResult()),
    diff: vi.fn().mockResolvedValue({ state: 'ok', text: '', truncated: false }),
    log: vi.fn().mockResolvedValue(logResult()),
    stage: vi.fn().mockResolvedValue(statusResult()),
    unstage: vi.fn().mockResolvedValue(statusResult()),
    stageAll: vi.fn().mockResolvedValue(statusResult()),
    unstageAll: vi.fn().mockResolvedValue(statusResult()),
    commit: vi
      .fn()
      .mockResolvedValue({ state: 'ok', outputTruncated: false, status: statusFixture() }),
    headMessage: vi.fn().mockResolvedValue({ state: 'ok', message: 'subject\n\nbody' }),
    remote: vi.fn().mockResolvedValue({ state: 'none' }),
    openUrl: vi.fn().mockResolvedValue({}),
    close: vi.fn().mockResolvedValue({ closed: true }),
    subscribeGitChanged: vi.fn().mockReturnValue(() => {}),
    ...over,
  }
}

/** The service seam is typed as an interface of METHODS; asserting
 *  `expect(mockHandle(services, 'open'))` would pass an unbound method to expect and trip
 *  @typescript-eslint/unbound-method. Tests reach the mock through this
 *  handle instead — one cast, one place. */
function mockHandle<K extends keyof GitPanelServices>(
  services: GitPanelServices,
  key: K,
): ReturnType<typeof vi.fn> {
  return services[key] as unknown as ReturnType<typeof vi.fn>
}

/** Drain the microtask queue until the store's promise chains settle. */
async function settle(): Promise<void> {
  for (let i = 0; i < 8; i++) await Promise.resolve()
}

/** Open a store on LOCAL_ORIGIN and wait until the open has landed. */
async function openStore(
  over: Partial<GitPanelServices> = {},
  storeOver: { pollIntervalMs?: number } = {},
): Promise<{ store: GitStore; services: GitPanelServices }> {
  const services = makeServices(over)
  const store = createGitStore(services, storeOver)
  store.rescope(LOCAL_ORIGIN)
  await settle()
  return { store, services }
}

const stores: GitStore[] = []
function track(store: GitStore): GitStore {
  stores.push(store)
  return store
}

afterEach(() => {
  for (const s of stores) s.dispose()
  stores.length = 0
  vi.useRealTimers()
})

// ── The eight states ──────────────────────────────────────────────────────

describe('the eight states', () => {
  it('noPane: no origin — nothing is asked of the backend', () => {
    const services = makeServices()
    const store = track(createGitStore(services))
    expect(store.state()).toBe('noPane')
    expect(mockHandle(services, 'open')).not.toHaveBeenCalled()
  })

  it('remote: an SSH tab is decided BEFORE any backend call (D3, D14)', async () => {
    const services = makeServices()
    const store = track(createGitStore(services))
    store.rescope(SSH_ORIGIN)
    await settle()
    expect(store.state()).toBe('remote')
    expect(mockHandle(services, 'open')).not.toHaveBeenCalled()
    // The panel on an SSH tab shows nothing rather than the local repo.
    expect(store.binding()).toBeNull()
  })

  it('noCwd: no verified cwd is decided before any backend call (AD-5)', async () => {
    const services = makeServices()
    const store = track(createGitStore(services))
    store.rescope(NO_CWD_ORIGIN)
    await settle()
    expect(store.state()).toBe('noCwd')
    expect(mockHandle(services, 'open')).not.toHaveBeenCalled()
  })

  it('notARepository: git.open said so, and the old binding is closed', async () => {
    const { store, services } = await openStore()
    expect(store.state()).toBe('ready')
    ;(services.open as ReturnType<typeof vi.fn>).mockResolvedValueOnce({
      state: 'notARepository',
    })
    store.rescope(NEW_CWD_ORIGIN)
    await settle()
    expect(store.state()).toBe('notARepository')
    expect(store.binding()).toBeNull()
    expect(mockHandle(services, 'close')).toHaveBeenCalledWith('b1')
  })

  it('gitUnavailable: no git on PATH', async () => {
    const services = makeServices({
      open: vi.fn().mockResolvedValue({ state: 'gitUnavailable' }),
    })
    const store = track(createGitStore(services))
    store.rescope(LOCAL_ORIGIN)
    await settle()
    expect(store.state()).toBe('gitUnavailable')
  })

  it('gitTooOld: the version found is carried for the panel to compare', async () => {
    const services = makeServices({
      open: vi.fn().mockResolvedValue({ state: 'gitTooOld', gitVersion: '2.20.0' }),
    })
    const store = track(createGitStore(services))
    store.rescope(LOCAL_ORIGIN)
    await settle()
    expect(store.state()).toBe('gitTooOld')
    expect(store.gitVersion()).toBe('2.20.0')
  })

  it('ready: a binding and a status', async () => {
    const { store, services } = await openStore()
    expect(store.state()).toBe('ready')
    expect(store.binding()).toEqual({ bindingId: 'b1', toplevel: '/home/dev/repo' })
    expect(store.status()?.branch).toBe('main')
    expect(mockHandle(services, 'open')).toHaveBeenCalledWith('s1', '/home/dev/repo')
  })

  it('tooManyChanges: capped — exact total, more records than the lists hold', async () => {
    const s = statusFixture({
      total: 6000,
      completeness: 'capped',
      unstaged: [{ path: 'a.txt', x: '.', y: 'M' }],
    })
    const services = makeServices({ open: vi.fn().mockResolvedValue(openOk({ status: s })) })
    const store = track(createGitStore(services))
    store.rescope(LOCAL_ORIGIN)
    await settle()
    expect(store.state()).toBe('tooManyChanges')
    // The lists still hold every retained record — the panel renders them
    // under the cap banner.
    expect(store.status()?.unstaged).toHaveLength(1)
  })

  it('tooManyChanges: cut BELOW the record cap — the length of the lists says nothing', async () => {
    const s = statusFixture({
      total: 3,
      completeness: 'cut',
      unstaged: [{ path: 'a.txt', x: '.', y: 'M' }],
    })
    const services = makeServices({ open: vi.fn().mockResolvedValue(openOk({ status: s })) })
    const store = track(createGitStore(services))
    store.rescope(LOCAL_ORIGIN)
    await settle()
    // A traversal stopped by the work ceiling after 3 records must not
    // render as a complete 3-file status (D9) — completeness is the gate,
    // never the lists' length.
    expect(store.state()).toBe('tooManyChanges')
  })
})

// ── Race 1: two git.opens on a fast A→B tab switch ───────────────────────

describe('race 1 — two opens racing on a tab switch', () => {
  it('the second open wins by generation and the stale successful open is CLOSED, not merely dropped (nocx-myts)', async () => {
    const openA = deferred<GitOpenResult>()
    const openB = deferred<GitOpenResult>()
    const open = vi
      .fn()
      .mockImplementationOnce(() => openA.promise) // tab A's open, still in flight
      .mockImplementationOnce(() => openB.promise) // tab B's open
    const close = vi.fn().mockResolvedValue({ closed: true })
    const services = makeServices({ open, close })
    const store = track(createGitStore(services))

    store.rescope(LOCAL_ORIGIN) // open A issued
    store.rescope(OTHER_ORIGIN) // fast switch: open B issued, A superseded
    // B lands first — it establishes the binding.
    openB.resolve(openOk({ bindingId: 'b2', toplevel: '/home/dev/other' }))
    await settle()
    expect(store.binding()).toEqual({ bindingId: 'b2', toplevel: '/home/dev/other' })
    expect(store.state()).toBe('ready')

    // A lands last — stale by generation. Its binding was registered on the
    // backend; discarding the response without closing it would leak it.
    openA.resolve(openOk({ bindingId: 'b1', toplevel: '/home/dev/repo' }))
    await settle()
    expect(store.binding()).toEqual({ bindingId: 'b2', toplevel: '/home/dev/other' })
    expect(close).toHaveBeenCalledWith('b1')
    expect(close).not.toHaveBeenCalledWith('b2')
  })

  it('a stale open that REFUSES changes nothing and closes nothing', async () => {
    const openA = deferred<GitOpenResult>()
    const openB = deferred<GitOpenResult>()
    const open = vi
      .fn()
      .mockImplementationOnce(() => openA.promise)
      .mockImplementationOnce(() => openB.promise)
    const close = vi.fn().mockResolvedValue({ closed: true })
    const store = track(createGitStore(makeServices({ open, close })))

    store.rescope(LOCAL_ORIGIN)
    store.rescope(OTHER_ORIGIN)
    openB.resolve(openOk({ bindingId: 'b2', toplevel: '/home/dev/other' }))
    await settle()
    openA.resolve({ state: 'notARepository' })
    await settle()
    expect(store.state()).toBe('ready')
    expect(store.binding()?.bindingId).toBe('b2')
    expect(close).not.toHaveBeenCalledWith('b1')
  })
})

// ── Race 2: a poll issued before a mutation landing after it ──────────────

describe('race 2 — a poll issued before a mutation lands after it (the epoch)', () => {
  it('the pre-mutation poll cannot repaint the post-mutation state', async () => {
    const { store, services } = await openStore()
    const poll = deferred<{ status: Status }>()
    const mutation = deferred<{ status: Status }>()
    ;(services.status as ReturnType<typeof vi.fn>).mockImplementationOnce(() => poll.promise)
    ;(services.stage as ReturnType<typeof vi.fn>).mockImplementationOnce(() => mutation.promise)

    store.refresh() // the poll — epoch N
    store.stage(['a.txt']) // the mutation — epoch N+1

    // The mutation lands first and paints.
    mutation.resolve(
      statusResult({ branch: 'post-mutation', staged: [{ path: 'a.txt', x: 'A', y: '.' }] }),
    )
    await settle()
    expect(store.status()?.branch).toBe('post-mutation')

    // The poll — begun BEFORE the mutation, with an OLDER epoch — lands
    // last. A guard that only knew the scope cannot order these two
    // requests; the epoch can.
    poll.resolve(statusResult({ branch: 'pre-mutation' }))
    await settle()
    expect(store.status()?.branch).toBe('post-mutation')
  })
})

// ── Race 3: two mutations overlapping ─────────────────────────────────────

describe('race 3 — the mutation lane (D18)', () => {
  it('refuses a second concurrent mutation: stage while a stage is in flight is a no-op', async () => {
    const { store, services } = await openStore()
    const first = deferred<{ status: Status }>()
    ;(services.stage as ReturnType<typeof vi.fn>).mockImplementationOnce(() => first.promise)

    store.stage(['a.txt'])
    expect(store.mutationInFlight()).toBe(true)
    // Two fast clicks cannot produce two overlapping index writes.
    store.stage(['b.txt'])
    store.stageAll()
    store.unstageAll()
    store.commit() // (nothing typed — refused on subject anyway)
    expect(mockHandle(services, 'stage')).toHaveBeenCalledTimes(1)
    expect(mockHandle(services, 'stageAll')).not.toHaveBeenCalled()
    expect(mockHandle(services, 'unstageAll')).not.toHaveBeenCalled()

    first.resolve(statusResult())
    await settle()
    expect(store.mutationInFlight()).toBe(false)
  })

  it('stage-all and unstage-all are refused while any entry is conflicted (D19)', async () => {
    const conflicted = statusFixture({
      conflicted: [{ path: 'conf.txt', x: 'U', y: 'U' }],
    })
    const services = makeServices({
      open: vi.fn().mockResolvedValue(openOk({ status: conflicted })),
    })
    const store = track(createGitStore(services))
    store.rescope(LOCAL_ORIGIN)
    await settle()
    expect(store.conflictsPresent()).toBe(true)

    store.stageAll()
    store.unstageAll()
    await settle()
    expect(mockHandle(services, 'stageAll')).not.toHaveBeenCalled()
    expect(mockHandle(services, 'unstageAll')).not.toHaveBeenCalled()
  })
  it('a mutation answers unknownBinding (reason unknown-binding) → the store re-resolves through git.open', async () => {
    const { store, services } = await openStore()
    const open = mockHandle(services, 'open')
    const stage = mockHandle(services, 'stage')
    open.mockClear()
    stage.mockRejectedValueOnce(
      new RpcError('git: unknown binding "b1"', -32602, { reason: 'unknown-binding' }),
    )

    store.stage(['a.txt'])
    await settle()
    expect(store.mutationInFlight()).toBe(false)
    // Re-resolved: a fresh git.open against the current origin.
    expect(open).toHaveBeenCalledWith('s1', '/home/dev/repo')
  })
})

// ── the -32602 discriminator (nocx-bpqil) ────────────────────────────────

describe('a -32602 is only an unknown binding when the reason says so', () => {
  it('an unknown binding (reason unknown-binding) re-resolves through git.open', async () => {
    const { store, services } = await openStore()
    const open = mockHandle(services, 'open')
    const stage = mockHandle(services, 'stage')
    open.mockClear()
    stage.mockRejectedValueOnce(
      new RpcError('git: unknown binding "b1"', -32602, { reason: 'unknown-binding' }),
    )

    store.stage(['a.txt'])
    await settle()
    expect(store.mutationInFlight()).toBe(false)
    // Re-resolved: a fresh git.open against the current origin.
    expect(open).toHaveBeenCalledWith('s1', '/home/dev/repo')
    // The refusal was never stored as a mutation error.
    expect(store.mutationError()).toBeNull()
  })

  it('a conflicted refusal (reason conflicted) does NOT re-resolve; it reaches the caller', async () => {
    const { store, services } = await openStore()
    const open = mockHandle(services, 'open')
    const stage = mockHandle(services, 'stage')
    open.mockClear()
    stage.mockRejectedValueOnce(
      new RpcError('git: cannot stage or unstage all while "conf.txt" is conflicted', -32602, {
        reason: 'conflicted',
      }),
    )

    store.stage(['a.txt'])
    await settle()
    // NOT re-resolved: the binding is fine, the repository refused.
    expect(open).not.toHaveBeenCalled()
    // The refusal is stored — the panel maps it to a sentence.
    expect(store.mutationError()?.message).toContain('conflicted')
  })

  it('a nothing-to-commit refusal (reason nothing-to-commit) does NOT re-resolve', async () => {
    const { store, services } = await openStore()
    const open = mockHandle(services, 'open')
    const commit = mockHandle(services, 'commit')
    open.mockClear()
    store.setCommitSubject('doomed')
    commit.mockRejectedValueOnce(
      new RpcError('git: nothing is staged to commit', -32602, {
        reason: 'nothing-to-commit',
      }),
    )

    store.commit()
    await settle()
    expect(open).not.toHaveBeenCalled()
    expect(store.mutationError()?.message).toContain('nothing is staged')
  })

  it('a bare -32602 with no data (a malformed-params refusal) does NOT re-resolve', async () => {
    const { store, services } = await openStore()
    const open = mockHandle(services, 'open')
    const stage = mockHandle(services, 'stage')
    open.mockClear()
    stage.mockRejectedValueOnce(new RpcError('Invalid params: bindingId required', -32602))

    store.stage(['a.txt'])
    await settle()
    expect(open).not.toHaveBeenCalled()
    expect(store.mutationError()?.message).toContain('Invalid params')
  })
})
// ── Race 4: a diff for a row clicked before the panel re-bound ────────────

// The diff fetch itself is worker G's (git-diff/); the panel's half — the
// row's target captures the binding at click time, never a re-bound one —
// is proven in git-view.test.tsx, named "race 4" there. The store's half is
// that a re-bind closes the superseded binding (proven above) and that a
// frozen origin never triggers one:

describe('race 4 half — the frozen origin never re-binds (design §5.4)', () => {
  it('cwdFollow:false keeps the binding, the status and the form untouched', async () => {
    const { store, services } = await openStore()
    store.setCommitSubject('draft')
    const open = mockHandle(services, 'open')
    open.mockClear()

    store.rescope(FROZEN_ORIGIN)
    await settle()
    expect(store.binding()?.bindingId).toBe('b1')
    expect(store.status()?.branch).toBe('main')
    expect(store.commitSubject()).toBe('draft')
    expect(open).not.toHaveBeenCalled()
  })
})

// ── D4: the answer, not the path, decides whether to re-bind ─────────────

describe('git owns repository identity (D4)', () => {
  it('a cwd change that re-answers the SAME toplevel keeps the binding and closes the redundant open', async () => {
    const { store, services } = await openStore()
    const open = mockHandle(services, 'open')
    const close = mockHandle(services, 'close')
    open.mockResolvedValueOnce(openOk({ bindingId: 'b1-again', toplevel: '/home/dev/repo' }))

    store.rescope(NEW_CWD_ORIGIN)
    await settle()
    // The binding diff tabs hold survives the cd — the fresh binding minted
    // by the redundant open is closed, never stored.
    expect(store.binding()).toEqual({ bindingId: 'b1', toplevel: '/home/dev/repo' })
    expect(close).toHaveBeenCalledWith('b1-again')
  })

  it('a cwd change that answers a DIFFERENT toplevel closes the old binding and adopts the new', async () => {
    const { store, services } = await openStore()
    const open = mockHandle(services, 'open')
    open.mockResolvedValueOnce(openOk({ bindingId: 'b2', toplevel: '/home/dev/other' }))
    store.rescope(OTHER_ORIGIN)
    await settle()
    expect(store.binding()).toEqual({ bindingId: 'b2', toplevel: '/home/dev/other' })
    expect(mockHandle(services, 'close')).toHaveBeenCalledWith('b1')
  })
})

// ── Polling (D13, rule 6) ─────────────────────────────────────────────────

describe('polling', () => {
  it('polls only while visible and ready, one in flight, and a failed poll keeps the last good status (stale mark)', async () => {
    vi.useFakeTimers()
    const { store, services } = await openStore(undefined, { pollIntervalMs: 5000 })
    const status = mockHandle(services, 'status')
    status.mockClear()

    store.setVisible(false)
    vi.advanceTimersByTime(60_000)
    expect(status).not.toHaveBeenCalled()

    store.setVisible(true)
    // The immediate status must LAND first — one poll in flight is never
    // queued, so without this settle the advancing tick would skip on
    // pollInFlight and the rejection below would never be consumed.
    await settle()
    expect(status).toHaveBeenCalledTimes(1) // fresh the moment it is seen
    status.mockRejectedValueOnce(new Error('socket dropped'))
    vi.advanceTimersByTime(5000)
    await settle()
    // A poll that errors does not clear the lists — the last good status
    // stays on screen, marked stale (rule 4).
    expect(store.status()?.branch).toBe('main')
    expect(store.statusStale()).toBe(true)
  })

  it('a mutation in flight suppresses the next poll', async () => {
    vi.useFakeTimers()
    const { store, services } = await openStore(undefined, { pollIntervalMs: 5000 })
    const status = mockHandle(services, 'status')
    const mutation = deferred<{ status: Status }>()
    ;(services.stage as ReturnType<typeof vi.fn>).mockImplementationOnce(() => mutation.promise)

    store.setVisible(true)
    status.mockClear()
    store.stage(['a.txt'])
    vi.advanceTimersByTime(10_000)
    expect(status).not.toHaveBeenCalled()

    mutation.resolve(statusResult())
    await settle()
  })
})

// ── The environment warning (nocx-69ey) ─────────────────────────────────
// The interval, stated with both ends (AGENTS.md rule 3): the warning
// appears when the environment is known-degraded and disappears when it
// becomes resolved — it is not "set at open". Open's answer is provisional
// (nocx-6pz0), so the status poll must be able to withdraw it.

describe('the environment warning', () => {
  it('appears when the open landed in the pre-settle window (D6 still holds)', async () => {
    const { store } = await openStore({
      open: vi.fn().mockResolvedValue(
        openOk({
          envState: 'degraded',
          envReason:
            'the shell environment has not been resolved yet; the first commit will wait for it',
        }),
      ),
    })
    expect(store.envState()).toBe('degraded')
    expect(store.envReason()).toBe(
      'the shell environment has not been resolved yet; the first commit will wait for it',
    )
  })

  it('is withdrawn by a poll carrying resolved — without re-opening the repository', async () => {
    const { store, services } = await openStore({
      open: vi.fn().mockResolvedValue(
        openOk({
          envState: 'degraded',
          envReason:
            'the shell environment has not been resolved yet; the first commit will wait for it',
        }),
      ),
    })
    expect(store.envState()).toBe('degraded')

    // The background resolution settles; the NEXT poll carries it. The
    // stale reason must go with it — a resolved state with a degraded
    // reason text would be the same lie in a different costume.
    mockHandle(services, 'status').mockResolvedValueOnce(statusResult({}, { envState: 'resolved' }))
    store.refresh()
    await settle()

    expect(store.envState()).toBe('resolved')
    expect(store.envReason()).toBeNull()
    // The correction came through the poll channel: exactly one open.
    expect(mockHandle(services, 'open')).toHaveBeenCalledTimes(1)
  })

  it('is never shown on a machine where the resolution settled before open — including the first frames', async () => {
    const { store } = await openStore() // the default open result is resolved
    expect(store.envState()).toBe('resolved')
    expect(store.envReason()).toBeNull()
  })
})

// ── The amend prefill token (rule 5) ─────────────────────────────────────

describe('the amend prefill token', () => {
  async function amendStore(): Promise<{
    store: GitStore
    pending: Array<(v: GitHeadMessageResult) => void>
  }> {
    const pending: Array<(v: GitHeadMessageResult) => void> = []
    const headMessage = vi
      .fn()
      .mockImplementation(() => new Promise<GitHeadMessageResult>((res) => pending.push(res)))
    const { store } = await openStore({ headMessage })
    return { store, pending }
  }

  it('tick, untick, reply — the abandoned form stays empty', async () => {
    const { store, pending } = await amendStore()
    store.toggleAmend() // tick — fetch issued
    store.toggleAmend() // untick — token bumps
    expect(store.amend()).toBe(false)
    pending[0]({ state: 'ok', message: 'abandoned' })
    await settle()
    expect(store.commitSubject()).toBe('')
    expect(store.commitBody()).toBe('')
  })

  it('tick, untick, tick — the first reply is discarded, the second fills', async () => {
    const { store, pending } = await amendStore()
    store.toggleAmend() // tick — request 1 (token 1)
    store.toggleAmend() // untick (token 2)
    store.toggleAmend() // tick — request 2 (token 3)
    expect(pending).toHaveLength(2)
    // The first reply satisfies the FIRST request's intent, which the user
    // has abandoned — dropped.
    pending[0]({ state: 'ok', message: 'stale' })
    await settle()
    expect(store.commitSubject()).toBe('')
    // The second reply is current.
    pending[1]({ state: 'ok', message: 'current\n\nbody' })
    await settle()
    expect(store.commitSubject()).toBe('current')
    expect(store.commitBody()).toBe('body')
  })

  it('prefills only into an empty form — never over text the user has typed', async () => {
    const { store, pending } = await amendStore()
    store.setCommitSubject('typed first')
    store.toggleAmend()
    pending[0]({ state: 'ok', message: 'head' })
    await settle()
    expect(store.commitSubject()).toBe('typed first')
  })
})

// ── The commit form (design §5.4, D11) ───────────────────────────────────

describe('the commit form', () => {
  it('a successful commit clears the form and applies the fresh status', async () => {
    const { store } = await openStore()
    const commit = vi.fn().mockResolvedValue({
      state: 'ok',
      outputTruncated: false,
      status: statusFixture({ branch: 'after', staged: [], unstaged: [] }),
    } satisfies GitCommitResult)
    const services = makeServices({ commit })
    // Rebuild with the recorded commit so the assertion is on the SAME call.
    store.dispose()
    const reopened = track(createGitStore(services))
    reopened.rescope(LOCAL_ORIGIN)
    await settle()
    reopened.setCommitSubject('subject')
    reopened.setCommitBody('body')
    reopened.commit()
    await settle()
    expect(commit).toHaveBeenCalledWith('b1', 'subject\n\nbody', false)
    expect(reopened.commitSubject()).toBe('')
    expect(reopened.commitBody()).toBe('')
    expect(reopened.status()?.branch).toBe('after')
  })

  it('a failed commit keeps the message and shows git output with the truncation mark (D11)', async () => {
    const services = makeServices({
      commit: vi.fn().mockResolvedValue({
        state: 'failed',
        output: 'error: pre-commit hook failed\n  hints\n',
        outputTruncated: true,
      } satisfies GitCommitResult),
    })
    const store = track(createGitStore(services))
    store.rescope(LOCAL_ORIGIN)
    await settle()
    store.setCommitSubject('keep me')
    store.commit()
    await settle()
    expect(store.commitState()).toBe('failed')
    expect(store.commitOutput()).toEqual({
      output: 'error: pre-commit hook failed\n  hints\n',
      truncated: true,
    })
    expect(store.commitSubject()).toBe('keep me')
  })

  it('the form belongs to one repository: a re-bind discards the previous draft', async () => {
    const { store, services } = await openStore()
    store.setCommitSubject('draft')
    ;(services.open as ReturnType<typeof vi.fn>).mockResolvedValueOnce(
      openOk({ bindingId: 'b2', toplevel: '/home/dev/other' }),
    )
    store.rescope(OTHER_ORIGIN)
    await settle()
    expect(store.commitSubject()).toBe('')
  })
})

// ── The notification and lifecycle ───────────────────────────────────────

describe('git.changed and dispose', () => {
  it('a session-close notification for our binding drops the panel to noPane', async () => {
    const { store, services } = await openStore()
    const handler = (services.subscribeGitChanged as ReturnType<typeof vi.fn>).mock
      .calls[0][0] as (p: { bindingId: string; reason: 'sessionClosed' }) => void
    handler({ bindingId: 'b1', reason: 'sessionClosed' })
    expect(store.state()).toBe('noPane')
    expect(store.binding()).toBeNull()
  })

  it('a notification for a DIFFERENT binding is ignored', async () => {
    const { store, services } = await openStore()
    const handler = (services.subscribeGitChanged as ReturnType<typeof vi.fn>).mock
      .calls[0][0] as (p: { bindingId: string; reason: 'sessionClosed' }) => void
    handler({ bindingId: 'someone-elses', reason: 'sessionClosed' })
    expect(store.state()).toBe('ready')
  })

  it('dispose closes the binding and makes the store reusable', async () => {
    const { store, services } = await openStore()
    store.dispose()
    expect(mockHandle(services, 'close')).toHaveBeenCalledWith('b1')
    expect(store.state()).toBe('noPane')
    // Reusable: the next rescope re-opens.
    store.rescope(LOCAL_ORIGIN)
    await settle()
    expect(store.state()).toBe('ready')
  })
})

// ── The D7 staleness seam (worker G's GitDiffDeps.onDiffStale) ───────────

describe("onDiffStale — the diff tab's Reload offer (D7)", () => {
  it('fires when an applied status moves the subscribed row, at most once per change', async () => {
    const { store, services } = await openStore()
    const status = mockHandle(services, 'status')
    const stale = vi.fn()
    const unsub = store.onDiffStale('b1', 'a.txt', 'unstaged', stale)

    // The first poll: the row is modified — the diff read under the open's
    // status was for a row that did not exist yet, so this is a move.
    status.mockResolvedValueOnce(statusResult({ unstaged: [{ path: 'a.txt', x: '.', y: 'M' }] }))
    store.refresh()
    await settle()
    expect(stale).toHaveBeenCalledTimes(1)

    // The second poll: identical status — not a move, no fire.
    status.mockResolvedValueOnce(statusResult({ unstaged: [{ path: 'a.txt', x: '.', y: 'M' }] }))
    store.refresh()
    await settle()
    expect(stale).toHaveBeenCalledTimes(1)

    // The row moves again (staged away): fire.
    status.mockResolvedValueOnce(
      statusResult({ staged: [{ path: 'a.txt', x: 'A', y: '.' }], unstaged: [] }),
    )
    store.refresh()
    await settle()
    expect(stale).toHaveBeenCalledTimes(2)

    unsub()
  })

  it('untracked and unstaged are different sides; a subscription is quiet for another binding', async () => {
    const { store, services } = await openStore()
    const status = mockHandle(services, 'status')
    const unstagedStale = vi.fn()
    const untrackedStale = vi.fn()
    const otherBindingStale = vi.fn()
    store.onDiffStale('b1', 'new.txt', 'unstaged', unstagedStale)
    store.onDiffStale('b1', 'new.txt', 'untracked', untrackedStale)
    store.onDiffStale('other-binding', 'new.txt', 'untracked', otherBindingStale)

    // The file appears as untracked: only the untracked-side subscriber moves.
    status.mockResolvedValueOnce(statusResult({ unstaged: [{ path: 'new.txt', x: '?', y: '?' }] }))
    store.refresh()
    await settle()
    expect(untrackedStale).toHaveBeenCalledTimes(1)
    expect(unstagedStale).not.toHaveBeenCalled()
    expect(otherBindingStale).not.toHaveBeenCalled()
  })
})

// ── The commits read (brief, git.log; D13) ────────────────────────────────

describe('the commits read', () => {
  it('reads once when the panel becomes visible — the D13 read, never a poll', async () => {
    const { store, services } = await openStore()
    const log = mockHandle(services, 'log')
    store.setVisible(true)
    await settle()
    expect(log).toHaveBeenCalledTimes(1)
    expect(log).toHaveBeenCalledWith('b1')
    expect(store.logState()).toBe('loaded')
    expect(store.log()?.entries[0]?.subject).toBe('third')
  })

  it('is read again on a manual refresh', async () => {
    const { store, services } = await openStore()
    store.setVisible(true)
    await settle()
    const log = mockHandle(services, 'log')
    const before = log.mock.calls.length
    store.refresh()
    await settle()
    expect(log.mock.calls.length).toBe(before + 1)
  })

  it('is read after a successful commit — the confirmation the section exists for', async () => {
    const { store, services } = await openStore()
    store.setVisible(true)
    await settle()
    const log = mockHandle(services, 'log')
    const before = log.mock.calls.length
    store.setCommitSubject('add second line')
    store.commit()
    await settle()
    expect(log.mock.calls.length).toBe(before + 1)
  })

  it('is NOT read by the poll — history does not change under the user (D13)', async () => {
    vi.useFakeTimers()
    const { store, services } = await openStore(undefined, { pollIntervalMs: 5000 })
    const status = mockHandle(services, 'status')
    const log = mockHandle(services, 'log')
    status.mockClear()
    log.mockClear()
    store.setVisible(true)
    await settle()
    // The open-of-the-view read fires once — that is the D13 read.
    expect(log).toHaveBeenCalledTimes(1)
    log.mockClear()
    status.mockClear()
    vi.advanceTimersByTime(60_000)
    await settle()
    expect(status.mock.calls.length).toBeGreaterThan(0) // the poll ran…
    expect(log).not.toHaveBeenCalled() // …and never took the log with it
  })

  it('a failed read is failed, not silently absent, and retry re-reads', async () => {
    const { store, services } = await openStore()
    store.setVisible(true)
    await settle()
    const log = mockHandle(services, 'log')
    log.mockRejectedValueOnce(new Error('git log: exit 128: fatal: bad object HEAD'))
    store.refresh()
    await settle()
    expect(store.logState()).toBe('failed')
    expect(store.logError()).toContain('bad object HEAD')
    // The retry is a manual refresh.
    store.refresh()
    await settle()
    expect(store.logState()).toBe('loaded')
  })

  it('an unknown-binding failure re-resolves through git.open', async () => {
    const { store, services } = await openStore()
    store.setVisible(true)
    await settle()
    const log = mockHandle(services, 'log')
    log.mockRejectedValueOnce(
      new RpcError('git: unknown binding "b1"', -32602, { reason: 'unknown-binding' }),
    )
    const open = mockHandle(services, 'open')
    const before = open.mock.calls.length
    store.refresh()
    await settle()
    expect(open.mock.calls.length).toBe(before + 1)
  })

  it('a log that lands after the panel re-scoped is dropped (D17)', async () => {
    const { store, services } = await openStore()
    store.setVisible(true)
    await settle()
    const log = mockHandle(services, 'log')
    log.mockClear()
    const d = deferred<GitLogResult>()
    log.mockReturnValueOnce(d.promise) // this read hangs…
    store.refresh()
    // …and the panel re-scopes to another repository before it lands.
    mockHandle(services, 'open').mockResolvedValueOnce(
      openOk({ bindingId: 'b2', toplevel: '/home/dev/other' }),
    )
    store.rescope(OTHER_ORIGIN)
    await settle()
    // The new scope's own read (the mock default) answers first.
    d.resolve(logResult({ entries: [], total: 0 }))
    await settle()
    expect(log).toHaveBeenCalledWith('b2')
    // The stale empty answer was dropped: the list still holds the new
    // scope's read, never the superseded one.
    expect(store.log()?.entries.length).toBe(1)
  })

  it('a log superseded while in flight keeps the last good log — never a stuck loading', async () => {
    const { store, services } = await openStore()
    store.setVisible(true)
    await settle()
    const log = mockHandle(services, 'log')
    log.mockClear()
    const d = deferred<GitLogResult>()
    log.mockReturnValueOnce(d.promise) // this read hangs…
    store.refresh()
    // …and a mutation (never poll-gated) lands while it hangs, carrying a
    // newer epoch that supersedes the hung read's.
    store.setCommitSubject('add second line')
    store.commit()
    await settle()
    d.resolve(logResult({ entries: [], total: 0 }))
    await settle()
    // The superseded answer was dropped; the last good log stays, and the
    // section says loaded — never a loading state no read can end.
    expect(store.logState()).toBe('loaded')
    expect(store.log()?.entries.length).toBe(1)
  })

  it('a log issued before a faster remote is applied when the remote lands first (completion order)', async () => {
    const { store, services } = await openStore()
    store.setVisible(true)
    await settle()
    const log = mockHandle(services, 'log')
    const remote = mockHandle(services, 'remote')
    log.mockClear()
    remote.mockClear()
    const logD = deferred<GitLogResult>()
    const remoteD = deferred<GitRemoteResult>()
    log.mockReturnValueOnce(logD.promise) // the refresh's log hangs…
    remote.mockReturnValueOnce(remoteD.promise) // …and so does its remote
    store.refresh()
    await settle()
    // The remote — issued AFTER the log — completes first: the control
    // plane runs handlers concurrently, so a faster backend command's
    // response overtakes a slower one's (responses travel in completion
    // order, not issue order).
    remoteD.resolve({ state: 'none' })
    await settle()
    // The log — issued BEFORE the remote — is not stale. Its answer must
    // land, or the Commits list sits idle forever with no state text:
    // the e2e failure, where git.log's response arrived on the wire with
    // its entries and the panel never rendered one row.
    const newest = logFixture({
      entries: [
        {
          hash: '92e6c887a923ee21a841f1198f5676855c872f42',
          shortHash: '92e6c88',
          subject: 'newest',
          authorName: 'Test Author',
          authoredAt: '2026-08-08T12:00:00+03:00',
          refs: ['main'],
        },
      ],
      total: 2,
    })
    logD.resolve({ log: newest })
    await settle()
    expect(store.logState()).toBe('loaded')
    expect(store.log()?.entries[0]?.subject).toBe('newest')
    expect(store.log()?.total).toBe(2)
  })

  it('re-binding clears the previous repository log and re-reads under the new binding', async () => {
    const { store, services } = await openStore()
    store.setVisible(true)
    await settle()
    const log = mockHandle(services, 'log')
    log.mockClear()
    mockHandle(services, 'open').mockResolvedValueOnce(
      openOk({ bindingId: 'b2', toplevel: '/home/dev/other' }),
    )
    store.rescope(OTHER_ORIGIN)
    await settle()
    expect(log).toHaveBeenCalledWith('b2')
    expect(store.log()).not.toBeNull()
  })

  it('dispose clears it', async () => {
    const { store } = await openStore()
    store.setVisible(true)
    await settle()
    expect(store.log()).not.toBeNull()
    store.dispose()
    expect(store.log()).toBeNull()
    expect(store.logState()).toBe('idle')
  })
})

// ── The collapsible sections (nocx-nak2) ──────────────────────────────────

describe('the collapsible sections', () => {
  it('a collapse survives a same-repository re-scope — the keep path (a view switch)', async () => {
    const { store } = await openStore()
    store.toggleSection('commits')
    expect(store.sectionOpen('commits')).toBe(false)
    // Same session, same verified cwd, live binding: rescope keeps
    // everything, collapse state included (design §5.5).
    store.rescope({ ...LOCAL_ORIGIN, paneId: 2 })
    await settle()
    expect(store.sectionOpen('commits')).toBe(false)
  })

  it('adopting a different repository resets the collapse — it never leaks across a re-bind', async () => {
    const services = makeServices({
      open: vi
        .fn()
        .mockResolvedValueOnce(openOk({ status: statusFixture({ total: 0 }) })) // repo A
        .mockResolvedValueOnce(openOk({ bindingId: 'b2', toplevel: '/home/dev/other' })), // repo B
    })
    const store = track(createGitStore(services))
    store.rescope(LOCAL_ORIGIN)
    await settle()
    store.toggleSection('unstaged')
    expect(store.sectionOpen('unstaged')).toBe(false)

    store.rescope(OTHER_ORIGIN)
    await settle()
    expect(store.sectionOpen('unstaged')).toBe(true)
  })
})

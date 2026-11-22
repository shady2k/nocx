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
import type { GitChangedNotification } from '../generated/git.changed'
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

const connectionAxis = (
  helper: import('../generated/pane.connection').PaneConnection['helper'],
): NonNullable<ActiveOrigin['connection']> => ({
  paneId: 'backend-pane-3',
  helper,
})

const SSH_ORIGIN: ActiveOrigin = {
  paneId: 3,
  sessionId: 's3',
  kind: 'ssh',
  cwd: '/home/bob',
  cwdVerified: true,
  cwdFollow: true,
  host: 'srv',
  connection: connectionAxis({ state: 'serving', action: 'none' }),
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

/** The service seam is typed as methods; this helper gives callers the mock
 *  API without passing an unbound method to an assertion. */
function mockHandle<K extends keyof GitPanelServices>(services: GitPanelServices, key: K) {
  return vi.mocked(services[key])
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

  it('ssh: an SSH tab WITH a verified cwd reaches git.open, and the answer decides the state (remote-helper design §6)', async () => {
    const services = makeServices()
    const store = track(createGitStore(services))
    store.rescope(SSH_ORIGIN)
    await settle()
    // The old remote guard that never asked is gone: git.open was called
    // with the session and cwd, and the ok answer makes the panel ready —
    // mutation controls present, because the helper serves the repository.
    expect(mockHandle(services, 'open')).toHaveBeenCalledWith('s3', '/home/bob')
    expect(store.state()).toBe('ready')
    expect(store.binding()).toEqual({ bindingId: 'b1', toplevel: '/home/dev/repo' })
  })

  it('ssh: an SSH tab without a verified cwd is noCwd before any backend call (AD-5)', async () => {
    const services = makeServices()
    const store = track(createGitStore(services))
    store.rescope({ ...SSH_ORIGIN, cwd: null, cwdVerified: false })
    await settle()
    expect(store.state()).toBe('noCwd')
    expect(mockHandle(services, 'open')).not.toHaveBeenCalled()
  })

  it('ssh: a machine the selection refuses with no earned state is the failed phase, carrying the reason', async () => {
    const services = makeServices({
      open: vi
        .fn()
        .mockRejectedValue(
          new Error('this connection is set to Raw, which does not run the nocx helper'),
        ),
    })
    const store = track(createGitStore(services))
    store.rescope(SSH_ORIGIN)
    await settle()
    expect(store.phase()).toBe('failed')
    expect(store.openError()).toContain('Raw')
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
    mockHandle(services, 'open').mockResolvedValueOnce({
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
    const poll = deferred<GitStatusResult>()
    const mutation = deferred<{ status: Status }>()
    mockHandle(services, 'status').mockImplementationOnce(() => poll.promise)
    mockHandle(services, 'stage').mockImplementationOnce(() => mutation.promise)

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
    mockHandle(services, 'stage').mockImplementationOnce(() => first.promise)

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
  it('a mutation that did not start is offered its same action once more, not a stale view', async () => {
    const { store, services } = await openStore()
    const stage = mockHandle(services, 'stage')
    stage.mockRejectedValueOnce(
      new RpcError(
        "git.stage did not start: waited 11m0s for this checkout's turn and nothing changed",
        -32005,
        { reason: 'not-started' },
      ),
    )

    store.stage(['a.txt'])
    await settle()
    expect(store.mutationInFlight()).toBe(false)
    expect(store.mutationRefused()).toContain('nothing changed')
    // "Did not start" is not "the view is stale": nothing happened, so the
    // panel must not start claiming a stale status.
    expect(store.mutationError()).toBeNull()

    stage.mockResolvedValueOnce(statusResult())
    store.retryRefusedMutation()
    await settle()
    expect(store.mutationRefused()).toBeNull()
    expect(mockHandle(services, 'stage')).toHaveBeenCalledTimes(2)
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

  it('a typed refusal carries its reason beside the prose — the panel decides by the reason (nocx-oalo1.6.3)', async () => {
    const { store, services } = await openStore()
    const stage = mockHandle(services, 'stage')
    stage.mockRejectedValueOnce(
      new RpcError('git: cannot stage or unstage all while "conf.txt" is conflicted', -32602, {
        reason: 'conflicted',
      }),
    )

    store.stage(['a.txt'])
    await settle()
    // The prose is what a person reads; the reason is what the code reads.
    // Deliberately not the message text: contracts/git.error.schema.json is
    // the vocabulary both ends agree on.
    expect(store.mutationError()?.reason).toBe('conflicted')
    expect(store.mutationError()?.message).toContain('conflicted')
  })

  it('a failure with no typed reason carries none — prose is not turned into a reason', async () => {
    const { store, services } = await openStore()
    const stage = mockHandle(services, 'stage')
    // An invocation failure: git's own output, -32603, no reason payload
    // (contracts/git.error.schema.json: "A git invocation failure carries no
    // data at all").
    stage.mockRejectedValueOnce(new RpcError('index.lock exists', -32603))

    store.stage(['a.txt'])
    await settle()
    expect(store.mutationError()?.message).toContain('index.lock exists')
    expect(store.mutationError()?.reason).toBeNull()
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

// ── D23: polling is coalesced by repository identity, not by tab ─────────

describe('polling is coalesced by repository identity (D23)', () => {
  it('two tabs bound to the SAME remote repository keep ONE poll loop: one status read in flight, no re-open burst on the switch', async () => {
    vi.useFakeTimers()
    const { store, services } = await openStore(undefined, { pollIntervalMs: 5000 })
    const status = mockHandle(services, 'status')
    const open = mockHandle(services, 'open')
    status.mockClear()
    open.mockClear()

    store.rescope(SSH_ORIGIN)
    await settle()
    store.setVisible(true)
    await settle()
    // The immediate status on becoming visible.
    expect(status).toHaveBeenCalledTimes(1)

    // A second pane bound to the same remote repository (same session,
    // same verified cwd — the answer-decides binding of D4): the store
    // keeps the live binding and its one poll loop; nothing re-opens and
    // no second status is issued for the switch itself.
    store.rescope({ ...SSH_ORIGIN, paneId: 4 })
    await settle()
    // Exactly the ONE open that established the repository's binding —
    // the pane switch re-opens nothing, because the store polls by
    // repository identity, not by pane.
    expect(open).toHaveBeenCalledTimes(1)
    expect(status).toHaveBeenCalledTimes(1)

    // After the first base interval the repository's ONE loop backs off;
    // advancing only to the next 5s boundary must not issue a second read
    // or an extra immediate read for the new tab. The advance is stepped
    // so the in-flight flag from the first read can settle before checking
    // the shorter-than-next-rung window.
    vi.advanceTimersByTime(5000)
    await settle()
    vi.advanceTimersByTime(5000)
    await settle()
    expect(status).toHaveBeenCalledTimes(2)
    expect(open).toHaveBeenCalledTimes(1)
  })

  it('the interval backs off when a read is still in flight: a slow status suppresses every tick until it lands', async () => {
    vi.useFakeTimers()
    const { store, services } = await openStore(undefined, { pollIntervalMs: 5000 })
    const status = mockHandle(services, 'status')
    const slow = deferred<GitStatusResult>()
    status.mockClear()
    mockHandle(services, 'status').mockImplementationOnce(() => slow.promise)

    store.rescope(SSH_ORIGIN)
    await settle()
    store.setVisible(true)
    await settle()
    expect(status).toHaveBeenCalledTimes(1)

    // A read whose duration approaches (or exceeds) the interval: every
    // tick while it is in flight is skipped — the repository's poll never
    // stacks a second read on top of the first.
    vi.advanceTimersByTime(15_000)
    expect(status).toHaveBeenCalledTimes(1)
    slow.resolve(statusResult())
    await settle()
    vi.advanceTimersByTime(5000)
    await settle()
    expect(status).toHaveBeenCalledTimes(2)
  })
})

// ── No feature surface may raise the tier (ADR-0068) ──────────────────────

describe('the far helper axis gates git.open (ADR-727g63tr, task .6.3c-1)', () => {
  it('holds a starting helper with zero opens and no failure card, then opens exactly once when it serves', async () => {
    const services = makeServices()
    const store = track(createGitStore(services))
    store.rescope({
      ...SSH_ORIGIN,
      connection: connectionAxis({ state: 'starting', action: 'none' }),
    })
    await settle()
    expect(store.state()).toBe('helperStarting')
    expect(mockHandle(services, 'open')).not.toHaveBeenCalled()
    expect(store.phase()).not.toBe('failed')

    store.rescope(SSH_ORIGIN)
    await settle()
    expect(mockHandle(services, 'open')).toHaveBeenCalledTimes(1)
    expect(store.state()).toBe('ready')

    // A newer observation of a SERVING axis is not a new permission to open:
    // re-opening on every heartbeat would mint a binding per poll.
    store.rescope({
      ...SSH_ORIGIN,
      connection: connectionAxis({ state: 'serving', action: 'none' }),
    })
    await settle()
    expect(mockHandle(services, 'open')).toHaveBeenCalledTimes(1)
  })

  it('closes the binding when the helper is lost, and the next serving transition opens it exactly once more', async () => {
    const services = makeServices()
    const store = track(createGitStore(services))
    store.rescope(SSH_ORIGIN)
    await settle()
    expect(store.state()).toBe('ready')

    store.rescope({
      ...SSH_ORIGIN,
      connection: connectionAxis({
        state: 'unavailable',
        reason: 'helper-lost',
        action: 'refresh',
      }),
    })
    await settle()
    expect(store.state()).toBe('helperBlocked')
    expect(mockHandle(services, 'close')).toHaveBeenCalledWith('b1')
    expect(store.binding()).toBeNull()

    // The redial's hello arrives as the next serving observation.
    store.rescope({
      ...SSH_ORIGIN,
      connection: connectionAxis({ state: 'serving', action: 'none' }),
    })
    await settle()
    expect(mockHandle(services, 'open')).toHaveBeenCalledTimes(2)
    expect(store.state()).toBe('ready')
  })

  it('a git.open still in flight when the axis stops serving cannot revive the binding (success)', async () => {
    // The review's race (ALX-998): leaving serving closes the accepted
    // binding but used to leave the in-flight open current, so its answer
    // adopted a binding and started Git reads while the panel was supposed
    // to be blocked — and a filled binding then suppressed the re-open on
    // the way back to serving.
    const pending = deferred<GitOpenResult>()
    const services = makeServices({ open: vi.fn().mockReturnValue(pending.promise) })
    const store = track(createGitStore(services))
    store.rescope(SSH_ORIGIN)
    await settle()
    expect(store.phase()).toBe('opening')

    store.rescope({
      ...SSH_ORIGIN,
      connection: connectionAxis({
        state: 'unavailable',
        reason: 'helper-lost',
        action: 'refresh',
      }),
    })
    await settle()
    expect(store.state()).toBe('helperBlocked')
    expect(store.phase()).toBe('ready')

    // The answer arrives only now — after the axis stopped serving.
    pending.resolve(openOk())
    await settle()

    expect(store.binding()).toBeNull()
    expect(store.phase()).toBe('ready')
    expect(store.state()).toBe('helperBlocked')
    // Rule 2: the binding the backend minted is closed, never leaked.
    expect(mockHandle(services, 'close')).toHaveBeenCalledWith('b1')

    // The return to serving is what opens the repository — exactly once.
    store.rescope({
      ...SSH_ORIGIN,
      connection: connectionAxis({ state: 'serving', action: 'none' }),
    })
    await settle()
    expect(mockHandle(services, 'open')).toHaveBeenCalledTimes(2)
    expect(store.state()).toBe('ready')
  })

  it('a git.open that rejects after the axis stops serving is not the failed card', async () => {
    const pending = deferred<GitOpenResult>()
    const services = makeServices({ open: vi.fn().mockReturnValue(pending.promise) })
    const store = track(createGitStore(services))
    store.rescope(SSH_ORIGIN)
    await settle()
    expect(store.phase()).toBe('opening')

    store.rescope({
      ...SSH_ORIGIN,
      connection: connectionAxis({ state: 'paused', reason: 'connection', action: 'none' }),
    })
    await settle()

    pending.reject(new Error('the helper went away'))
    await settle()

    // A stale failure never replaces the axis's own reason with the
    // "Could not reach git" card and its Retry.
    expect(store.phase()).toBe('ready')
    expect(store.openError()).toBeNull()
    expect(store.state()).toBe('helperBlocked')
  })

  it('holds an SSH origin whose first observation has not arrived, and mints no state for it', async () => {
    const services = makeServices()
    const store = track(createGitStore(services))
    store.rescope({ ...SSH_ORIGIN, connection: null })
    await settle()
    expect(store.state()).toBe('helperStarting')
    expect(mockHandle(services, 'open')).not.toHaveBeenCalled()
  })

  it('a raw or paused connection stops the repository without asking git', async () => {
    for (const helper of [
      { state: 'unavailable', reason: 'mode-raw', action: 'host-settings' },
      { state: 'paused', reason: 'connection', action: 'none' },
      { state: 'unavailable', reason: 'install-failed', action: 'none' },
    ] as const) {
      const services = makeServices()
      const store = track(createGitStore(services))
      store.rescope({ ...SSH_ORIGIN, connection: connectionAxis(helper) })
      await settle()
      expect(store.state()).toBe('helperBlocked')
      expect(mockHandle(services, 'open'), JSON.stringify(helper)).not.toHaveBeenCalled()
    }
  })

  it('a serving helper whose git.open is refused keeps the failed card with Retry', async () => {
    const services = makeServices({
      open: vi.fn().mockResolvedValue({
        state: 'deployFailed',
        message: 'the helper could not be installed',
      }),
    })
    const store = track(createGitStore(services))
    store.rescope(SSH_ORIGIN)
    await settle()
    expect(store.phase()).toBe('failed')
    expect(store.openError()).toContain('could not be installed')
  })

  it('keeps local Git untouched: no axis, the same open on a verified cwd', async () => {
    const services = makeServices()
    const store = track(createGitStore(services))
    store.rescope(LOCAL_ORIGIN)
    await settle()
    expect(mockHandle(services, 'open')).toHaveBeenCalledTimes(1)
    expect(store.state()).toBe('ready')
  })

  it('keeps local Git out of the axis gate: no epoch, no axis, the rules it always had', async () => {
    // The gate is an SSH rule (a local tab has no connection observation at
    // all), so the generation guard must not touch it: a same-cwd re-scope
    // keeps the live binding, and a verified cwd change is still the only
    // thing that re-asks.
    const services = makeServices()
    const store = track(createGitStore(services))
    store.rescope(LOCAL_ORIGIN)
    await settle()
    expect(mockHandle(services, 'open')).toHaveBeenCalledTimes(1)

    store.rescope({ ...LOCAL_ORIGIN, paneId: 5 })
    await settle()
    expect(mockHandle(services, 'open')).toHaveBeenCalledTimes(1)
    expect(store.binding()).toEqual({ bindingId: 'b1', toplevel: '/home/dev/repo' })

    store.rescope(NEW_CWD_ORIGIN)
    await settle()
    expect(mockHandle(services, 'open')).toHaveBeenCalledTimes(2)
    expect(store.state()).toBe('ready')
  })
})

// ── A wake is not a new open context (ALX-1019) ──────────────────────────
//
// A quality sample republishes the same connection under a new epoch. The
// epoch is the press's, not the open's: it no longer rides the origin
// snapshot, and a repeated origin with the same (session, kind, verified
// cwd) context must record the new origin and leave whatever the open
// answered alone — in flight, ok, notARepository, gitUnavailable, gitTooOld
// or failed. An explicit Retry/Refresh is the one caller that forces an
// open, and a changed context still opens by itself.

describe('a wake that keeps the open context never re-opens Git (ALX-1019)', () => {
  it('S1: a repeated origin while git.open is in flight keeps ONE open, and its answer lands', async () => {
    const pending = deferred<GitOpenResult>()
    const services = makeServices({ open: vi.fn().mockReturnValue(pending.promise) })
    const store = track(createGitStore(services))

    store.rescope(SSH_ORIGIN)
    await settle()
    expect(store.phase()).toBe('opening')

    // A quality sample: the same connection, a new observation object and a
    // new paneId on the tab — nothing git.open was issued for has moved.
    store.rescope({ ...SSH_ORIGIN, paneId: 4 })
    await settle()
    expect(mockHandle(services, 'open')).toHaveBeenCalledTimes(1)

    pending.resolve(openOk())
    await settle()
    expect(store.state()).toBe('ready')
    expect(store.binding()).toEqual({ bindingId: 'b1', toplevel: '/home/dev/repo' })
  })

  it('S2: after notARepository a repeated origin opens nothing and the phase never returns to opening', async () => {
    const services = makeServices({
      open: vi.fn().mockResolvedValue({ state: 'notARepository' }),
    })
    const store = track(createGitStore(services))
    store.rescope(SSH_ORIGIN)
    await settle()
    expect(store.state()).toBe('notARepository')
    expect(store.phase()).toBe('ready')
    expect(mockHandle(services, 'open')).toHaveBeenCalledTimes(1)

    store.rescope({ ...SSH_ORIGIN, paneId: 4 })
    // Synchronous look: the old path re-entered 'opening' before the answer.
    expect(store.phase()).not.toBe('opening')
    await settle()
    store.rescope({ ...SSH_ORIGIN, paneId: 5 })
    await settle()

    expect(mockHandle(services, 'open')).toHaveBeenCalledTimes(1)
    expect(store.state()).toBe('notARepository')
    expect(store.phase()).toBe('ready')
  })

  it('S3: the same for gitUnavailable', async () => {
    const services = makeServices({
      open: vi.fn().mockResolvedValue({ state: 'gitUnavailable' }),
    })
    const store = track(createGitStore(services))
    store.rescope(SSH_ORIGIN)
    await settle()
    expect(store.state()).toBe('gitUnavailable')

    store.rescope({ ...SSH_ORIGIN, paneId: 4 })
    expect(store.phase()).not.toBe('opening')
    await settle()

    expect(mockHandle(services, 'open')).toHaveBeenCalledTimes(1)
    expect(store.state()).toBe('gitUnavailable')
  })

  it('S4: a failed open keeps its Retry card on a repeated origin, without a second open', async () => {
    const services = makeServices({
      open: vi.fn().mockRejectedValue(new Error('the helper refused the open')),
    })
    const store = track(createGitStore(services))
    store.rescope(SSH_ORIGIN)
    await settle()
    expect(store.phase()).toBe('failed')
    expect(store.openError()).toContain('the helper refused')

    store.rescope({ ...SSH_ORIGIN, paneId: 4 })
    await settle()

    expect(mockHandle(services, 'open')).toHaveBeenCalledTimes(1)
    expect(store.phase()).toBe('failed')
    expect(store.openError()).toContain('the helper refused')
  })

  it('S5: Refresh after notARepository forces exactly one new open', async () => {
    const open = vi
      .fn()
      .mockResolvedValueOnce({ state: 'notARepository' })
      .mockResolvedValue(openOk())
    const store = track(createGitStore(makeServices({ open })))
    store.rescope(SSH_ORIGIN)
    await settle()
    const afterFirst = open.mock.calls.length

    store.rescope({ ...SSH_ORIGIN, paneId: 4 })
    await settle()
    store.refresh()
    await settle()

    expect(open.mock.calls.length).toBe(afterFirst + 1)
    expect(store.state()).toBe('ready')
  })

  it('S5: Retry after a failed open forces exactly one new open', async () => {
    const open = vi
      .fn()
      .mockRejectedValueOnce(new Error('the helper refused the open'))
      .mockResolvedValue(openOk())
    const store = track(createGitStore(makeServices({ open })))
    store.rescope(SSH_ORIGIN)
    await settle()
    const afterFirst = open.mock.calls.length
    expect(store.phase()).toBe('failed')

    store.rescope({ ...SSH_ORIGIN, paneId: 4 })
    await settle()
    store.refresh()
    await settle()

    expect(open.mock.calls.length).toBe(afterFirst + 1)
    expect(store.state()).toBe('ready')
  })

  it('S6: a changed verified cwd in the same session still opens by itself', async () => {
    const open = vi.fn().mockResolvedValue({ state: 'notARepository' })
    const store = track(createGitStore(makeServices({ open })))
    store.rescope(SSH_ORIGIN)
    await settle()
    store.rescope({ ...SSH_ORIGIN, paneId: 4 })
    await settle()
    expect(open).toHaveBeenCalledTimes(1)

    store.rescope({ ...SSH_ORIGIN, cwd: '/home/bob/sub' })
    await settle()
    expect(open).toHaveBeenCalledTimes(2)
    expect(open).toHaveBeenLastCalledWith('s3', '/home/bob/sub')
  })

  it('S7: a late answer after helper-lost is closed and the return to serving opens once', async () => {
    const first = deferred<GitOpenResult>()
    const open = vi.fn().mockReturnValueOnce(first.promise).mockResolvedValue(openOk())
    const close = vi.fn().mockResolvedValue({ closed: true })
    const store = track(createGitStore(makeServices({ open, close })))

    store.rescope(SSH_ORIGIN)
    await settle()
    expect(store.phase()).toBe('opening')

    store.rescope({
      ...SSH_ORIGIN,
      connection: connectionAxis({
        state: 'unavailable',
        reason: 'helper-lost',
        action: 'refresh',
      }),
    })
    await settle()
    expect(store.state()).toBe('helperBlocked')

    first.resolve(openOk())
    await settle()
    expect(store.binding()).toBeNull()
    expect(close).toHaveBeenCalledWith('b1')

    store.rescope(SSH_ORIGIN)
    await settle()
    expect(open).toHaveBeenCalledTimes(2)
    expect(store.state()).toBe('ready')
  })
})

// ── The opening context: one key, one generation, one answer ─────────────
//
// ALX-1031. The context git.open is about is a KEY — session, kind, verified
// cwd, an axis that permits opening — and one instance of it is one
// generation. The paneId is not part of it: a viewer tab or a quality sample
// republishes the same context under another one, and an answer to the same
// context is the same answer. A binding a re-open carries belongs to the
// store, so an instance that ends can release it.

const closeCallsWith = (close: ReturnType<typeof vi.fn>, bindingId: string): number =>
  close.mock.calls.filter((c) => c[0] === bindingId).length

describe('the opening context key and the answer it accepts (ALX-1031)', () => {
  it('G1: the verified cwd is lost while git.open is in flight — noCwd stays, the late success is closed and no read starts', async () => {
    const pending = deferred<GitOpenResult>()
    const close = vi.fn().mockResolvedValue({ closed: true })
    const services = makeServices({ open: vi.fn().mockReturnValue(pending.promise), close })
    const store = track(createGitStore(services))
    store.rescope(LOCAL_ORIGIN)
    await settle()
    expect(store.phase()).toBe('opening')

    store.rescope({ ...LOCAL_ORIGIN, cwdVerified: false })
    await settle()
    expect(store.state()).toBe('noCwd')

    pending.resolve(openOk({ bindingId: 'b-late' }))
    await settle()

    // The lost cwd ended the instance: the answer to it cannot re-bind the
    // panel to the directory nobody verified any more...
    expect(store.state()).toBe('noCwd')
    expect(store.phase()).toBe('ready')
    expect(store.binding()).toBeNull()
    // ...and its binding is closed exactly once, never leaked.
    expect(closeCallsWith(close, 'b-late')).toBe(1)
    // And it starts no read under the context it no longer is.
    expect(mockHandle(services, 'status')).not.toHaveBeenCalled()
    expect(mockHandle(services, 'log')).not.toHaveBeenCalled()
    expect(mockHandle(services, 'remote')).not.toHaveBeenCalled()
  })

  it('G2: the same, but the late answer FAILS — noCwd stays and no failure card replaces it', async () => {
    const pending = deferred<GitOpenResult>()
    const services = makeServices({ open: vi.fn().mockReturnValue(pending.promise) })
    const store = track(createGitStore(services))
    store.rescope(LOCAL_ORIGIN)
    await settle()

    store.rescope({ ...LOCAL_ORIGIN, cwdVerified: false })
    await settle()

    pending.reject(new Error('git: could not read the worktree'))
    await settle()

    expect(store.state()).toBe('noCwd')
    expect(store.openError()).toBeNull()
    expect(store.phase()).not.toBe('failed')
  })

  it('G3: the same verified cwd coming back opens exactly once, and only that answer lands', async () => {
    const first = deferred<GitOpenResult>()
    const open = vi
      .fn()
      .mockReturnValueOnce(first.promise)
      .mockResolvedValueOnce(openOk({ bindingId: 'b-second' }))
    const close = vi.fn().mockResolvedValue({ closed: true })
    const store = track(createGitStore(makeServices({ open, close })))

    store.rescope(LOCAL_ORIGIN)
    await settle()
    store.rescope({ ...LOCAL_ORIGIN, cwdVerified: false })
    await settle()
    // The context comes back — a key the panel may open again.
    store.rescope(LOCAL_ORIGIN)
    await settle()
    expect(open).toHaveBeenCalledTimes(2)

    // The superseded answer lands last and cannot touch the live one.
    first.resolve(openOk({ bindingId: 'b-first' }))
    await settle()
    expect(store.binding()).toEqual({ bindingId: 'b-second', toplevel: '/home/dev/repo' })
    expect(store.state()).toBe('ready')
    expect(closeCallsWith(close, 'b-first')).toBe(1)
  })

  it('G4: another paneId while git.open is in flight is the same context — the answer binds and its inline status paints', async () => {
    const pending = deferred<GitOpenResult>()
    const services = makeServices({ open: vi.fn().mockReturnValue(pending.promise) })
    const store = track(createGitStore(services))
    store.rescope(LOCAL_ORIGIN)
    await settle()

    // A viewer tab or a quality sample: the same connection, another pane.
    store.rescope({ ...LOCAL_ORIGIN, paneId: 4 })
    await settle()
    expect(mockHandle(services, 'open')).toHaveBeenCalledTimes(1)

    pending.resolve(openOk({ status: statusFixture({ branch: 'from-the-answer' }) }))
    await settle()

    expect(store.state()).toBe('ready')
    expect(store.binding()).toEqual({ bindingId: 'b1', toplevel: '/home/dev/repo' })
    // The status baked into the open's answer is applied — no separate read.
    expect(store.status()?.branch).toBe('from-the-answer')
    expect(mockHandle(services, 'status')).not.toHaveBeenCalled()
  })

  it('G4 (ssh): the same holds on a serving helper axis', async () => {
    const pending = deferred<GitOpenResult>()
    const services = makeServices({ open: vi.fn().mockReturnValue(pending.promise) })
    const store = track(createGitStore(services))
    store.rescope(SSH_ORIGIN)
    await settle()

    store.rescope({ ...SSH_ORIGIN, paneId: 4 })
    await settle()

    pending.resolve(openOk({ status: statusFixture({ branch: 'over-ssh' }) }))
    await settle()

    expect(store.state()).toBe('ready')
    expect(store.status()?.branch).toBe('over-ssh')
  })

  it('G5: status, log and remote reads keep applying when another paneId arrives under the same key', async () => {
    const { store, services } = await openStore()
    const status = deferred<GitStatusResult>()
    const log = deferred<GitLogResult>()
    const remote = deferred<GitRemoteResult>()
    mockHandle(services, 'status').mockReturnValueOnce(status.promise)
    mockHandle(services, 'log').mockReturnValueOnce(log.promise)
    mockHandle(services, 'remote').mockReturnValueOnce(remote.promise)

    store.refresh()
    await settle()
    store.rescope({ ...LOCAL_ORIGIN, paneId: 4 })
    await settle()

    status.resolve(statusResult({ branch: 'read-status' }))
    log.resolve(logResult({ entries: [], total: 0 }))
    remote.resolve({ state: 'ok', url: 'git@example.com:repo.git' })
    await settle()

    expect(store.status()?.branch).toBe('read-status')
    expect(store.logState()).toBe('loaded')
    expect(store.log()?.total).toBe(0)
    expect(store.remoteState()).toBe('ok')
    expect(store.remoteUrl()).toBe('git@example.com:repo.git')
  })

  it('G6: a mutation in flight lands when another paneId arrives under the same key, and its lane frees', async () => {
    const { store, services } = await openStore()
    const staged = deferred<{ status: Status }>()
    mockHandle(services, 'stage').mockReturnValueOnce(staged.promise)

    store.stage(['a.txt'])
    expect(store.mutationInFlight()).toBe(true)
    store.rescope({ ...LOCAL_ORIGIN, paneId: 4 })
    await settle()

    staged.resolve(statusResult({ branch: 'after-stage' }))
    await settle()

    expect(store.status()?.branch).toBe('after-stage')
    expect(store.mutationInFlight()).toBe(false)
  })

  it('G7: a read in flight when the cwd changes is dropped by the generation', async () => {
    const { store, services } = await openStore()
    const status = deferred<GitStatusResult>()
    mockHandle(services, 'status').mockReturnValueOnce(status.promise)

    store.refresh()
    await settle()
    // The same session, a new verified cwd: a new context instance.
    mockHandle(services, 'open').mockResolvedValueOnce(
      openOk({ bindingId: 'b2', toplevel: '/home/dev/other' }),
    )
    store.rescope(NEW_CWD_ORIGIN)
    await settle()

    status.resolve(statusResult({ branch: 'stale-status' }))
    await settle()

    expect(store.status()?.branch).not.toBe('stale-status')
  })

  it('G9a: a cd re-open in flight, then the cwd is lost — the carried binding closes exactly once', async () => {
    const reopened = deferred<GitOpenResult>()
    const open = vi
      .fn()
      .mockResolvedValueOnce(openOk({ bindingId: 'P' }))
      .mockReturnValueOnce(reopened.promise)
    const close = vi.fn().mockResolvedValue({ closed: true })
    const store = track(createGitStore(makeServices({ open, close })))
    store.rescope(LOCAL_ORIGIN)
    await settle()
    expect(store.binding()?.bindingId).toBe('P')

    store.rescope(NEW_CWD_ORIGIN)
    await settle()
    expect(store.phase()).toBe('opening')

    store.rescope({ ...NEW_CWD_ORIGIN, cwdVerified: false })
    await settle()

    expect(closeCallsWith(close, 'P')).toBe(1)
    expect(store.state()).toBe('noCwd')

    // The superseded answer arrives and closes only what it minted.
    reopened.resolve(openOk({ bindingId: 'P-late', toplevel: '/home/dev/repo/sub' }))
    await settle()
    expect(closeCallsWith(close, 'P')).toBe(1)
    expect(closeCallsWith(close, 'P-late')).toBe(1)
  })

  it('G9b: a cd re-open in flight, then the axis stops serving — the carried binding closes exactly once', async () => {
    const reopened = deferred<GitOpenResult>()
    const open = vi
      .fn()
      .mockResolvedValueOnce(openOk({ bindingId: 'P' }))
      .mockReturnValueOnce(reopened.promise)
    const close = vi.fn().mockResolvedValue({ closed: true })
    const store = track(createGitStore(makeServices({ open, close })))
    store.rescope(SSH_ORIGIN)
    await settle()
    expect(store.binding()?.bindingId).toBe('P')

    store.rescope({ ...SSH_ORIGIN, cwd: '/home/bob/sub' })
    await settle()

    store.rescope({
      ...SSH_ORIGIN,
      cwd: '/home/bob/sub',
      connection: connectionAxis({
        state: 'unavailable',
        reason: 'helper-lost',
        action: 'refresh',
      }),
    })
    await settle()
    expect(store.state()).toBe('helperBlocked')
    expect(closeCallsWith(close, 'P')).toBe(1)

    reopened.resolve(openOk({ bindingId: 'P-late' }))
    await settle()
    expect(closeCallsWith(close, 'P')).toBe(1)
    expect(store.binding()).toBeNull()
  })

  it('G9c: a second cd while the first re-open is in flight — a different toplevel closes the carried binding once', async () => {
    const second = deferred<GitOpenResult>()
    const open = vi
      .fn()
      .mockResolvedValueOnce(openOk({ bindingId: 'P' }))
      .mockReturnValueOnce(new Promise<never>(() => {}))
      .mockReturnValueOnce(second.promise)
    const close = vi.fn().mockResolvedValue({ closed: true })
    const store = track(createGitStore(makeServices({ open, close })))
    store.rescope(LOCAL_ORIGIN)
    await settle()

    store.rescope(NEW_CWD_ORIGIN)
    await settle()
    store.rescope({ ...LOCAL_ORIGIN, cwd: '/home/dev/repo/other' })
    await settle()
    expect(open).toHaveBeenCalledTimes(3)

    second.resolve(openOk({ bindingId: 'B', toplevel: '/home/dev/somewhere-else' }))
    await settle()

    expect(closeCallsWith(close, 'P')).toBe(1)
    expect(store.binding()).toEqual({ bindingId: 'B', toplevel: '/home/dev/somewhere-else' })
  })

  it('G9c (same toplevel): the same second cd keeps the carried binding instead of churning it', async () => {
    const second = deferred<GitOpenResult>()
    const open = vi
      .fn()
      .mockResolvedValueOnce(openOk({ bindingId: 'P' }))
      .mockReturnValueOnce(new Promise<never>(() => {}))
      .mockReturnValueOnce(second.promise)
    const close = vi.fn().mockResolvedValue({ closed: true })
    const store = track(createGitStore(makeServices({ open, close })))
    store.rescope(LOCAL_ORIGIN)
    await settle()

    store.rescope(NEW_CWD_ORIGIN)
    await settle()
    store.rescope({ ...LOCAL_ORIGIN, cwd: '/home/dev/repo/other' })
    await settle()

    // The answer names the repository the carried binding already is.
    second.resolve(openOk({ bindingId: 'B', toplevel: '/home/dev/repo' }))
    await settle()

    expect(store.binding()).toEqual({ bindingId: 'P', toplevel: '/home/dev/repo' })
    expect(closeCallsWith(close, 'P')).toBe(0)
    expect(closeCallsWith(close, 'B')).toBe(1)
  })

  it('G9d: dispose closes a binding a re-open is carrying, exactly once', async () => {
    const reopened = deferred<GitOpenResult>()
    const open = vi
      .fn()
      .mockResolvedValueOnce(openOk({ bindingId: 'P' }))
      .mockReturnValueOnce(reopened.promise)
    const close = vi.fn().mockResolvedValue({ closed: true })
    const store = track(createGitStore(makeServices({ open, close })))
    store.rescope(LOCAL_ORIGIN)
    await settle()

    store.rescope(NEW_CWD_ORIGIN)
    await settle()
    store.dispose()
    expect(closeCallsWith(close, 'P')).toBe(1)

    reopened.resolve(openOk({ bindingId: 'P-late' }))
    await settle()
    expect(closeCallsWith(close, 'P')).toBe(1)
    expect(closeCallsWith(close, 'P-late')).toBe(1)
  })
})

// ── ALX-1043: the key is total, the transition is one comparison ──────────
//
// The class of defect: a state with no key let a separate predicate decide,
// and it looked for signs of a live instance. In a Ready refusal there are
// none, so the loss of the cwd was not a transition and the return to the
// same verified cwd matched the stale instance key — git.open never went out
// and the panel sat on "Not a repository" forever. The key now exists for
// EVERY origin; the transition in scopeTo is one comparison.

describe('ALX-1043: a context that may not open is a context too', () => {
  const G10_CASES: { label: string; origin: ActiveOrigin; answer: GitOpenResult['state'] }[] = [
    { label: 'notARepository (local)', origin: LOCAL_ORIGIN, answer: 'notARepository' },
    { label: 'gitUnavailable (local)', origin: LOCAL_ORIGIN, answer: 'gitUnavailable' },
    { label: 'gitTooOld (local)', origin: LOCAL_ORIGIN, answer: 'gitTooOld' },
    { label: 'notARepository over a serving helper', origin: SSH_ORIGIN, answer: 'notARepository' },
  ]

  for (const c of G10_CASES) {
    it(`G10: ${c.label} — a lost cwd is noCwd, and the same cwd back opens once more`, async () => {
      const open = vi
        .fn()
        .mockResolvedValueOnce({ state: c.answer })
        .mockResolvedValueOnce(
          openOk({
            bindingId: 'b-second',
            status: statusFixture({ branch: 'second-answer' }),
          }),
        )
      const store = track(createGitStore(makeServices({ open })))

      store.rescope(c.origin)
      await settle()
      expect(store.state()).toBe(c.answer)
      expect(store.phase()).toBe('ready')
      expect(open).toHaveBeenCalledTimes(1)

      // The shell reported a cwd nobody verified: a DIFFERENT context, so a
      // transition, and the refusal's answer is reset with it.
      store.rescope({ ...c.origin, cwdVerified: false })
      await settle()
      expect(store.state()).toBe('noCwd')
      expect(store.phase()).toBe('ready')
      expect(open).toHaveBeenCalledTimes(1)

      // The same verified cwd comes back: it is a context key again — not
      // the stale instance's key, and not "no key before".
      store.rescope(c.origin)
      await settle()
      expect(open).toHaveBeenCalledTimes(2)
      expect(store.state()).toBe('ready')
      expect(store.binding()).toEqual({ bindingId: 'b-second', toplevel: '/home/dev/repo' })
      expect(store.status()?.branch).toBe('second-answer')
    })
  }

  it('G11: a repeated noCwd observation is not a transition — no open, no close', async () => {
    const services = makeServices()
    const store = track(createGitStore(services))
    store.rescope(NO_CWD_ORIGIN)
    await settle()
    expect(store.state()).toBe('noCwd')

    // Another paneId, a new observation object, a changed — still
    // unverified — cwd: the same noCwd key.
    store.rescope({ ...NO_CWD_ORIGIN, paneId: 4, cwd: '/somewhere/unverified' })
    await settle()
    store.rescope({ ...NO_CWD_ORIGIN, paneId: 5 })
    await settle()

    expect(mockHandle(services, 'open')).not.toHaveBeenCalled()
    expect(mockHandle(services, 'close')).not.toHaveBeenCalled()
    expect(store.state()).toBe('noCwd')
  })

  it('G11 (axis): a repeated non-serving observation is not a transition either', async () => {
    const helper = {
      state: 'unavailable',
      reason: 'helper-lost',
      action: 'refresh',
    } as const
    const origin: ActiveOrigin = { ...SSH_ORIGIN, connection: connectionAxis(helper) }
    const services = makeServices()
    const store = track(createGitStore(services))
    store.rescope(origin)
    await settle()
    expect(store.state()).toBe('helperBlocked')

    // A new tab paneId and a new CONNECTION epoch (another backend pane id),
    // same axis: nothing git.open is about has moved.
    store.rescope({
      ...origin,
      paneId: 4,
      connection: { paneId: 'backend-pane-9', helper: { ...helper } },
    })
    await settle()

    expect(mockHandle(services, 'open')).not.toHaveBeenCalled()
    expect(mockHandle(services, 'close')).not.toHaveBeenCalled()
    expect(store.state()).toBe('helperBlocked')
  })

  it('G13: a Refresh before the cd re-open lands opens again and keeps the carried binding', async () => {
    const first = deferred<GitOpenResult>()
    const second = deferred<GitOpenResult>()
    const open = vi
      .fn()
      .mockResolvedValueOnce(openOk({ bindingId: 'P' }))
      .mockReturnValueOnce(first.promise)
      .mockReturnValueOnce(second.promise)
    const close = vi.fn().mockResolvedValue({ closed: true })
    const store = track(createGitStore(makeServices({ open, close })))

    store.rescope(LOCAL_ORIGIN)
    await settle()
    expect(store.binding()?.bindingId).toBe('P')

    store.rescope(NEW_CWD_ORIGIN)
    await settle()
    expect(store.phase()).toBe('opening')

    store.refresh()
    await settle()
    expect(open).toHaveBeenCalledTimes(3)

    // The superseded answer closes only what it minted...
    first.resolve(openOk({ bindingId: 'R1', toplevel: '/home/dev/repo' }))
    await settle()
    // ...and the current one names the repository the carried binding is:
    // keep it and close the fresh one.
    second.resolve(openOk({ bindingId: 'R2', toplevel: '/home/dev/repo' }))
    await settle()

    // The carried binding is the one the diff tabs hold: never closed.
    expect(closeCallsWith(close, 'P')).toBe(0)
    expect(closeCallsWith(close, 'R1')).toBe(1)
    expect(closeCallsWith(close, 'R2')).toBe(1)
    expect(store.binding()).toEqual({ bindingId: 'P', toplevel: '/home/dev/repo' })
    expect(store.state()).toBe('ready')
  })

  it('G14: the same with a different toplevel — the carried binding is superseded once', async () => {
    const first = deferred<GitOpenResult>()
    const second = deferred<GitOpenResult>()
    const open = vi
      .fn()
      .mockResolvedValueOnce(openOk({ bindingId: 'P' }))
      .mockReturnValueOnce(first.promise)
      .mockReturnValueOnce(second.promise)
    const close = vi.fn().mockResolvedValue({ closed: true })
    const store = track(createGitStore(makeServices({ open, close })))

    store.rescope(LOCAL_ORIGIN)
    await settle()
    store.rescope(NEW_CWD_ORIGIN)
    await settle()
    store.refresh()
    await settle()

    first.resolve(openOk({ bindingId: 'R1', toplevel: '/home/dev/repo' }))
    await settle()
    second.resolve(openOk({ bindingId: 'R2', toplevel: '/home/dev/other' }))
    await settle()

    expect(closeCallsWith(close, 'P')).toBe(1)
    expect(closeCallsWith(close, 'R2')).toBe(0)
    expect(store.binding()).toEqual({ bindingId: 'R2', toplevel: '/home/dev/other' })
  })

  it('G15 (a): Retry after a refused cd re-open keeps the binding the diff tabs hold', async () => {
    const open = vi
      .fn()
      .mockResolvedValueOnce(openOk({ bindingId: 'P' }))
      .mockRejectedValueOnce(new Error('git.open: the helper refused the open'))
      .mockResolvedValueOnce(openOk({ bindingId: 'R2', toplevel: '/home/dev/repo' }))
    const close = vi.fn().mockResolvedValue({ closed: true })
    const store = track(createGitStore(makeServices({ open, close })))

    store.rescope(LOCAL_ORIGIN)
    await settle()
    store.rescope(NEW_CWD_ORIGIN)
    await settle()
    expect(store.phase()).toBe('failed')

    store.refresh()
    await settle()
    expect(open).toHaveBeenCalledTimes(3)

    expect(closeCallsWith(close, 'P')).toBe(0)
    expect(closeCallsWith(close, 'R2')).toBe(1)
    expect(store.binding()).toEqual({ bindingId: 'P', toplevel: '/home/dev/repo' })
    expect(store.phase()).toBe('ready')
  })

  it('G15 (b): leaving the session after a refused cd re-open closes the carried binding once', async () => {
    const open = vi
      .fn()
      .mockResolvedValueOnce(openOk({ bindingId: 'P' }))
      .mockRejectedValueOnce(new Error('git.open: the helper refused the open'))
      .mockResolvedValueOnce(openOk({ bindingId: 'b-other' }))
    const close = vi.fn().mockResolvedValue({ closed: true })
    const store = track(createGitStore(makeServices({ open, close })))

    store.rescope(LOCAL_ORIGIN)
    await settle()
    store.rescope(NEW_CWD_ORIGIN)
    await settle()
    expect(store.phase()).toBe('failed')

    store.rescope(OTHER_ORIGIN)
    await settle()

    expect(closeCallsWith(close, 'P')).toBe(1)
    expect(store.binding()).toEqual({ bindingId: 'b-other', toplevel: '/home/dev/repo' })
    expect(store.origin()?.sessionId).toBe('s2')
  })
})

// ── THE TRANSITION MATRIX (ALX-1043) ──────────────────────────────────────
//
// One row per row of the gate table: the state the store is in, the event,
// and the requests it may issue. The Files half of the same table is F5–F7
// in files-store.test.ts — a different store, hence a different table.
// Adding a row to the gate table means adding a row here.

interface MatrixOutcome {
  opens: number
  closed: string[]
}

interface MatrixRow {
  name: string
  expected: MatrixOutcome
  run: () => Promise<MatrixOutcome>
}

const closedIds = (close: ReturnType<typeof vi.fn>): string[] =>
  close.mock.calls.map((call) => String(call[0]))

describe('the transition matrix: state, event, requests (ALX-1043)', () => {
  const ROWS: MatrixRow[] = [
    {
      name: 'Opening with a carried binding, the cwd is lost: both bindings close, nothing opens',
      expected: { opens: 2, closed: ['P'] },
      run: async () => {
        const reopened = deferred<GitOpenResult>()
        const open = vi
          .fn()
          .mockResolvedValueOnce(openOk({ bindingId: 'P' }))
          .mockReturnValueOnce(reopened.promise)
        const close = vi.fn().mockResolvedValue({ closed: true })
        const store = track(createGitStore(makeServices({ open, close })))
        store.rescope(LOCAL_ORIGIN)
        await settle()
        store.rescope(NEW_CWD_ORIGIN)
        await settle()
        store.rescope({ ...NEW_CWD_ORIGIN, cwdVerified: false })
        await settle()
        return { opens: open.mock.calls.length, closed: closedIds(close) }
      },
    },
    {
      name: 'Ready-ok with a live binding, the cwd is lost: it closes, nothing opens',
      expected: { opens: 1, closed: ['P'] },
      run: async () => {
        const open = vi.fn().mockResolvedValue(openOk({ bindingId: 'P' }))
        const close = vi.fn().mockResolvedValue({ closed: true })
        const store = track(createGitStore(makeServices({ open, close })))
        store.rescope(LOCAL_ORIGIN)
        await settle()
        store.rescope({ ...LOCAL_ORIGIN, cwdVerified: false })
        await settle()
        return { opens: open.mock.calls.length, closed: closedIds(close) }
      },
    },
    {
      name: 'Ready refusal, the cwd is lost: nothing is held, nothing opens',
      expected: { opens: 1, closed: [] },
      run: async () => {
        const open = vi.fn().mockResolvedValue({ state: 'notARepository' })
        const close = vi.fn().mockResolvedValue({ closed: true })
        const store = track(createGitStore(makeServices({ open, close })))
        store.rescope(LOCAL_ORIGIN)
        await settle()
        store.rescope({ ...LOCAL_ORIGIN, cwdVerified: false })
        await settle()
        return { opens: open.mock.calls.length, closed: closedIds(close) }
      },
    },
    {
      name: 'Failed open, the cwd is lost: nothing is held, nothing opens',
      expected: { opens: 1, closed: [] },
      run: async () => {
        const open = vi.fn().mockRejectedValue(new Error('git.open failed'))
        const close = vi.fn().mockResolvedValue({ closed: true })
        const store = track(createGitStore(makeServices({ open, close })))
        store.rescope(LOCAL_ORIGIN)
        await settle()
        store.rescope({ ...LOCAL_ORIGIN, cwdVerified: false })
        await settle()
        return { opens: open.mock.calls.length, closed: closedIds(close) }
      },
    },
    {
      name: 'noCwd, a repeated observation (new paneId, wake): nothing opens, nothing closes',
      expected: { opens: 0, closed: [] },
      run: async () => {
        const services = makeServices()
        const store = track(createGitStore(services))
        store.rescope(NO_CWD_ORIGIN)
        await settle()
        store.rescope({ ...NO_CWD_ORIGIN, paneId: 4, cwd: '/still/unverified' })
        await settle()
        store.rescope({ ...NO_CWD_ORIGIN, paneId: 5 })
        await settle()
        return {
          opens: mockHandle(services, 'open').mock.calls.length,
          closed: closedIds(mockHandle(services, 'close')),
        }
      },
    },
    {
      name: 'a non-serving axis, a repeated observation (new paneId, new epoch): nothing opens',
      expected: { opens: 0, closed: [] },
      run: async () => {
        const helper = { state: 'unavailable', reason: 'helper-lost', action: 'refresh' } as const
        const origin: ActiveOrigin = { ...SSH_ORIGIN, connection: connectionAxis(helper) }
        const services = makeServices()
        const store = track(createGitStore(services))
        store.rescope(origin)
        await settle()
        store.rescope({
          ...origin,
          paneId: 4,
          connection: { paneId: 'backend-pane-9', helper: { ...helper } },
        })
        await settle()
        return {
          opens: mockHandle(services, 'open').mock.calls.length,
          closed: closedIds(mockHandle(services, 'close')),
        }
      },
    },
    {
      name: 'noCwd, the previous verified cwd comes back: git.open once',
      expected: { opens: 1, closed: [] },
      run: async () => {
        const open = vi.fn().mockResolvedValue(openOk({ bindingId: 'P' }))
        const close = vi.fn().mockResolvedValue({ closed: true })
        const store = track(createGitStore(makeServices({ open, close })))
        store.rescope(NO_CWD_ORIGIN)
        await settle()
        store.rescope(LOCAL_ORIGIN)
        await settle()
        return { opens: open.mock.calls.length, closed: closedIds(close) }
      },
    },
    {
      name: 'Opening with a carried binding, Refresh: one forced open, the carried binding stays',
      expected: { opens: 3, closed: [] },
      run: async () => {
        const open = vi
          .fn()
          .mockResolvedValueOnce(openOk({ bindingId: 'P' }))
          .mockReturnValueOnce(new Promise<never>(() => {}))
          .mockReturnValueOnce(new Promise<never>(() => {}))
        const close = vi.fn().mockResolvedValue({ closed: true })
        const store = track(createGitStore(makeServices({ open, close })))
        store.rescope(LOCAL_ORIGIN)
        await settle()
        store.rescope(NEW_CWD_ORIGIN)
        await settle()
        store.refresh()
        await settle()
        return { opens: open.mock.calls.length, closed: closedIds(close) }
      },
    },
    {
      name: 'Failed open with a carried binding, Retry: one forced open, the carried binding stays',
      expected: { opens: 3, closed: [] },
      run: async () => {
        const open = vi
          .fn()
          .mockResolvedValueOnce(openOk({ bindingId: 'P' }))
          .mockRejectedValueOnce(new Error('git.open failed'))
          .mockReturnValueOnce(new Promise<never>(() => {}))
        const close = vi.fn().mockResolvedValue({ closed: true })
        const store = track(createGitStore(makeServices({ open, close })))
        store.rescope(LOCAL_ORIGIN)
        await settle()
        store.rescope(NEW_CWD_ORIGIN)
        await settle()
        store.refresh()
        await settle()
        return { opens: open.mock.calls.length, closed: closedIds(close) }
      },
    },
    {
      name: 'Ready-ok with a live binding, unknownBinding: it closes and git.open is forced',
      expected: { opens: 2, closed: ['P'] },
      run: async () => {
        const open = vi
          .fn()
          .mockResolvedValueOnce(openOk({ bindingId: 'P' }))
          .mockResolvedValueOnce(openOk({ bindingId: 'R' }))
        const close = vi.fn().mockResolvedValue({ closed: true })
        const services = makeServices({
          open,
          close,
          stage: vi
            .fn()
            .mockRejectedValueOnce(
              new RpcError('git: unknown binding "P"', -32602, { reason: 'unknown-binding' }),
            ),
        })
        const store = track(createGitStore(services))
        store.rescope(LOCAL_ORIGIN)
        await settle()
        store.stage(['a.txt'])
        await settle()
        return { opens: open.mock.calls.length, closed: closedIds(close) }
      },
    },
    {
      name: 'a live binding, git.changed: it closes and the panel drops to no-origin',
      expected: { opens: 1, closed: ['P'] },
      run: async () => {
        let changed: (p: GitChangedNotification) => void = () => {
          throw new Error('not subscribed')
        }
        const open = vi.fn().mockResolvedValue(openOk({ bindingId: 'P' }))
        const close = vi.fn().mockResolvedValue({ closed: true })
        const services = makeServices({
          open,
          close,
          subscribeGitChanged: vi.fn((handler: (p: GitChangedNotification) => void) => {
            changed = handler
            return () => {}
          }),
        })
        const store = track(createGitStore(services))
        store.rescope(LOCAL_ORIGIN)
        await settle()
        changed({ bindingId: 'P', reason: 'sessionClosed' })
        await settle()
        expect(store.state()).toBe('noPane')
        return { opens: open.mock.calls.length, closed: closedIds(close) }
      },
    },
    {
      name: 'a carried binding, git.changed: it closes and the panel drops to no-origin',
      expected: { opens: 2, closed: ['P'] },
      run: async () => {
        let changed: (p: GitChangedNotification) => void = () => {
          throw new Error('not subscribed')
        }
        const open = vi
          .fn()
          .mockResolvedValueOnce(openOk({ bindingId: 'P' }))
          .mockReturnValueOnce(new Promise<never>(() => {}))
        const close = vi.fn().mockResolvedValue({ closed: true })
        const services = makeServices({
          open,
          close,
          subscribeGitChanged: vi.fn((handler: (p: GitChangedNotification) => void) => {
            changed = handler
            return () => {}
          }),
        })
        const store = track(createGitStore(services))
        store.rescope(LOCAL_ORIGIN)
        await settle()
        store.rescope(NEW_CWD_ORIGIN)
        await settle()
        changed({ bindingId: 'P', reason: 'sessionClosed' })
        await settle()
        expect(store.state()).toBe('noPane')
        return { opens: open.mock.calls.length, closed: closedIds(close) }
      },
    },
  ]

  it.each(ROWS)('$name', async (row) => {
    const outcome = await row.run()
    expect(outcome).toEqual(row.expected)
  })
})

describe('a machine with no helper-tier answer', () => {
  // ADR-0068 removed the git panel's consent card: the connection, or the
  // connect-time ask, decides the helper — never a feature surface — so
  // git.open answers the SAME rejected call as raw or a denied answer
  // (internal/app's refusedHelperReason), and the store carries no method
  // to grant it any more (GitPanelServices has no grantConsent).
  it('is the failed phase, naming what would change it, with nothing to accept', async () => {
    const services = makeServices({
      open: vi
        .fn()
        .mockRejectedValue(
          new Error(
            'git.open: this connection has not yet been asked about the nocx helper — reconnect to be asked, or set its Delivery mode to Helper to allow it outright',
          ),
        ),
    })
    const store = track(createGitStore(services))
    store.rescope(SSH_ORIGIN)
    await settle()
    expect(store.phase()).toBe('failed')
    expect(store.openError()).toContain('reconnect')
    expect(store.binding()).toBeNull()
    // Nothing in the store's surface can raise the tier: the accept path
    // (grantConsent) does not exist on GitPanelServices at all.
    expect('grantConsent' in services).toBe(false)
  })
})

// ── Polling (D13, rule 6) ─────────────────────────────────────────────────

describe('polling', () => {
  it('backs off unchanged status to the stated 50s idle tail', async () => {
    vi.useFakeTimers()
    const { store, services } = await openStore(undefined, { pollIntervalMs: 5000 })
    const status = mockHandle(services, 'status')
    status.mockClear()

    store.setVisible(true)
    await settle()
    expect(status).toHaveBeenCalledTimes(1) // fresh the moment it is seen

    await vi.advanceTimersByTimeAsync(5000)
    await settle()
    expect(status).toHaveBeenCalledTimes(2) // 5s responsive rung

    await vi.advanceTimersByTimeAsync(10_000)
    await settle()
    expect(status).toHaveBeenCalledTimes(3) // 10s rung

    await vi.advanceTimersByTimeAsync(20_000)
    await settle()
    expect(status).toHaveBeenCalledTimes(4) // 20s rung

    await vi.advanceTimersByTimeAsync(49_999)
    await settle()
    expect(status).toHaveBeenCalledTimes(4) // no poll before the 50s tail

    await vi.advanceTimersByTimeAsync(1)
    await settle()
    expect(status).toHaveBeenCalledTimes(5) // 50s idle tail
  })

  it('a mutation completion resets the next poll to the responsive rung', async () => {
    vi.useFakeTimers()
    const { store, services } = await openStore(undefined, { pollIntervalMs: 100 })
    const status = mockHandle(services, 'status')
    status.mockClear()

    store.setVisible(true)
    await settle()
    await vi.advanceTimersByTimeAsync(100)
    await settle() // one unchanged poll moves the timer to 2x

    status.mockClear()
    store.stage(['a.txt'])
    await settle()
    expect(status).not.toHaveBeenCalled()

    await vi.advanceTimersByTimeAsync(100)
    await settle()
    expect(status).toHaveBeenCalledTimes(1)
  })

  it('a changed status resets the next poll to the responsive rung', async () => {
    vi.useFakeTimers()
    const { store, services } = await openStore(undefined, { pollIntervalMs: 100 })
    const status = mockHandle(services, 'status')
    status.mockClear()

    store.setVisible(true)
    await settle()
    await vi.advanceTimersByTimeAsync(100)
    await settle() // unchanged: the next rung is 2x
    status.mockClear()
    status.mockResolvedValueOnce(statusResult({ branch: 'feature' }))
    await vi.advanceTimersByTimeAsync(200)
    await settle()
    expect(status).toHaveBeenCalledTimes(1)
    expect(store.status()?.branch).toBe('feature')

    await vi.advanceTimersByTimeAsync(100)
    await settle()
    expect(status).toHaveBeenCalledTimes(2)
  })

  it('a manual refresh resets the next poll to the responsive rung', async () => {
    vi.useFakeTimers()
    const { store, services } = await openStore(undefined, { pollIntervalMs: 100 })
    const status = mockHandle(services, 'status')
    status.mockClear()

    store.setVisible(true)
    await settle()
    await vi.advanceTimersByTimeAsync(100)
    await settle() // unchanged: the next rung is 2x

    status.mockClear()
    store.refresh()
    await settle()
    expect(status).toHaveBeenCalledTimes(1) // the refresh itself is immediate

    await vi.advanceTimersByTimeAsync(100)
    await settle()
    expect(status).toHaveBeenCalledTimes(2)
  })

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
    mockHandle(services, 'stage').mockImplementationOnce(() => mutation.promise)

    store.setVisible(true)
    status.mockClear()
    store.stage(['a.txt'])
    vi.advanceTimersByTime(10_000)
    await settle()
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
    mockHandle(services, 'open').mockResolvedValueOnce(
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
    const handler = mockHandle(services, 'subscribeGitChanged').mock.calls[0][0]
    handler({ bindingId: 'b1', reason: 'sessionClosed' })
    expect(store.state()).toBe('noPane')
    expect(store.binding()).toBeNull()
  })

  it('a session-close notification for a binding a re-open is carrying ends the instance and drops it', async () => {
    const reopened = deferred<GitOpenResult>()
    const open = vi
      .fn()
      .mockResolvedValueOnce(openOk({ bindingId: 'P' }))
      .mockReturnValueOnce(reopened.promise)
    const close = vi.fn().mockResolvedValue({ closed: true })
    const services = makeServices({ open, close })
    const store = track(createGitStore(services))
    store.rescope(LOCAL_ORIGIN)
    await settle()

    // A cd holds P while its re-open is in flight.
    store.rescope(NEW_CWD_ORIGIN)
    await settle()
    expect(store.binding()).toBeNull()

    const handler = mockHandle(services, 'subscribeGitChanged').mock.calls[0][0]
    handler({ bindingId: 'P', reason: 'sessionClosed' })
    expect(store.state()).toBe('noPane')
    expect(closeCallsWith(close, 'P')).toBe(1)

    reopened.resolve(openOk({ bindingId: 'P-late' }))
    await settle()
    expect(closeCallsWith(close, 'P-late')).toBe(1)
    expect(store.binding()).toBeNull()
  })

  it('a notification for a DIFFERENT binding is ignored', async () => {
    const { store, services } = await openStore()
    const handler = mockHandle(services, 'subscribeGitChanged').mock.calls[0][0]
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

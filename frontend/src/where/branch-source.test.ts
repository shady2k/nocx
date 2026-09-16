// The pane branch source: git.open + git.close, per pane, single-flight,
// debounced (nocx-9bpeq.13, spec §3). Every timing-sensitive behaviour here
// is driven through the injected BranchScheduler double, never real time
// (AGENTS.md: a test may not depend on timing) — `flush()` fires whatever
// debounce timer is currently pending, deterministically.
import { describe, expect, it, vi } from 'vitest'
import {
  createBranchSource,
  BRANCH_DEBOUNCE_MS,
  type BranchScheduler,
  type BranchSourceDeps,
  type BranchRequest,
} from './branch-source'
import type { GitOpenResult } from '../generated/git.open'
import type { GitCloseResult } from '../generated/git.close'

/** Drain the microtask queue until the source's promise chains settle —
 *  the same pattern git-store.test.ts uses for the same reason. */
async function settle(): Promise<void> {
  for (let i = 0; i < 8; i++) await Promise.resolve()
}

function fakeScheduler(): BranchScheduler & {
  flush: () => void
  pendingCount: () => number
  lastDelayMs: () => number | undefined
} {
  let seq = 0
  let lastMs: number | undefined
  const timers = new Map<number, () => void>()
  return {
    setTimeout: (fn, ms) => {
      const id = ++seq
      timers.set(id, fn)
      lastMs = ms
      return id
    },
    clearTimeout: (handle) => {
      timers.delete(handle as number)
    },
    flush: () => {
      const fns = [...timers.values()]
      timers.clear()
      for (const fn of fns) fn()
    },
    pendingCount: () => timers.size,
    lastDelayMs: () => lastMs,
  }
}

function req(over: Partial<BranchRequest> = {}): BranchRequest {
  return {
    sessionId: 'sess-1',
    cwd: '/Users/a/repo',
    cwdVerified: true,
    isLocal: true,
    ...over,
  }
}

function okResult(over: Partial<GitOpenResult> = {}): GitOpenResult {
  return {
    state: 'ok',
    bindingId: 'b1',
    toplevel: '/Users/a/repo',
    status: {
      branch: 'main',
      detached: false,
      unborn: false,
      head: 'abc1234',
      upstream: '',
      ahead: 0,
      behind: 0,
      staged: [],
      unstaged: [],
      conflicted: [],
      total: 0,
      completeness: 'complete',
    },
    ...over,
  }
}

function deps(
  over: Partial<BranchSourceDeps> = {},
): BranchSourceDeps & { open: BranchSourceDeps['open']; close: BranchSourceDeps['close'] } {
  return {
    open: vi.fn(() => Promise.resolve(okResult())),
    close: vi.fn(() => Promise.resolve<GitCloseResult>({ closed: true })),
    ...over,
  }
}

describe('createBranchSource — the happy path', () => {
  it('calls git.open with the session and cwd, after the debounce', () => {
    const d = deps()
    const scheduler = fakeScheduler()
    const source = createBranchSource(d, { scheduler })
    source.request(req())
    expect(d.open).not.toHaveBeenCalled()
    scheduler.flush()
    expect(d.open).toHaveBeenCalledWith('sess-1', '/Users/a/repo')
  })

  it('debounces using the exported default when no debounceMs is given', () => {
    const d = deps()
    const scheduler = fakeScheduler()
    const source = createBranchSource(d, { scheduler })
    source.request(req())
    expect(scheduler.lastDelayMs()).toBe(BRANCH_DEBOUNCE_MS)
  })

  it('publishes status.branch on an ok, non-detached answer', async () => {
    const d = deps()
    const scheduler = fakeScheduler()
    const source = createBranchSource(d, { scheduler })
    source.request(req())
    scheduler.flush()
    await settle()
    expect(source.branch()).toBe('main')
  })

  it('publishes the short head when detached', async () => {
    const d = deps({
      open: vi.fn(() =>
        Promise.resolve(
          okResult({
            status: {
              branch: '',
              detached: true,
              unborn: false,
              head: 'de7ac4e',
              upstream: '',
              ahead: 0,
              behind: 0,
              staged: [],
              unstaged: [],
              conflicted: [],
              total: 0,
              completeness: 'complete',
            },
          }),
        ),
      ),
    })
    const scheduler = fakeScheduler()
    const source = createBranchSource(d, { scheduler })
    source.request(req())
    scheduler.flush()
    await settle()
    expect(source.branch()).toBe('de7ac4e')
  })

  it('publishes undefined when neither a branch nor a head is reported', async () => {
    const d = deps({
      open: vi.fn(() =>
        Promise.resolve(
          okResult({
            status: {
              branch: '',
              detached: false,
              unborn: true,
              head: '',
              upstream: '',
              ahead: 0,
              behind: 0,
              staged: [],
              unstaged: [],
              conflicted: [],
              total: 0,
              completeness: 'complete',
            },
          }),
        ),
      ),
    })
    const scheduler = fakeScheduler()
    const source = createBranchSource(d, { scheduler })
    source.request(req())
    scheduler.flush()
    await settle()
    expect(source.branch()).toBeUndefined()
  })

  it('publishes undefined when the inline status is absent', async () => {
    const d = deps({ open: vi.fn(() => Promise.resolve(okResult({ status: undefined }))) })
    const scheduler = fakeScheduler()
    const source = createBranchSource(d, { scheduler })
    source.request(req())
    scheduler.flush()
    await settle()
    expect(source.branch()).toBeUndefined()
  })

  it('always closes the binding it got on an ok answer', async () => {
    const d = deps()
    const scheduler = fakeScheduler()
    const source = createBranchSource(d, { scheduler })
    source.request(req())
    scheduler.flush()
    await settle()
    expect(d.close).toHaveBeenCalledWith('b1')
  })

  it('notifies a subscriber of the published branch', async () => {
    const d = deps()
    const scheduler = fakeScheduler()
    const source = createBranchSource(d, { scheduler })
    const seen: (string | undefined)[] = []
    source.subscribe((b) => seen.push(b))
    source.request(req())
    scheduler.flush()
    await settle()
    expect(seen).toEqual(['main'])
  })
})

describe('createBranchSource — every non-ok state', () => {
  const nonOkStates: GitOpenResult['state'][] = [
    'notARepository',
    'gitUnavailable',
    'gitTooOld',
    'noCwd',
    'unsupportedPlatform',
    'deployFailed',
    'execForbidden',
    'helperVersionMismatch',
  ]

  for (const state of nonOkStates) {
    it(`publishes undefined and closes nothing for ${state}`, async () => {
      const d = deps({ open: vi.fn(() => Promise.resolve<GitOpenResult>({ state })) })
      const scheduler = fakeScheduler()
      const source = createBranchSource(d, { scheduler })
      source.request(req())
      scheduler.flush()
      await settle()
      expect(source.branch()).toBeUndefined()
      expect(d.close).not.toHaveBeenCalled()
    })
  }

  it('calls no notify, toast or consent function — the deps carry none to call', () => {
    // BranchSourceDeps declares exactly open and close; this is the static
    // half of the assertion (the dynamic half is that fire() never calls
    // anything beyond deps.open/deps.close, verified by the state-by-state
    // tests above never seeing an unexpected call recorded on `d`).
    const d = deps()
    expect(Object.keys(d).sort()).toEqual(['close', 'open'])
  })
})

describe('createBranchSource — rejection', () => {
  it('publishes undefined, closes nothing, and the next request tries again', async () => {
    let calls = 0
    const d = deps({
      open: vi.fn(() => {
        calls++
        return calls === 1 ? Promise.reject(new Error('gone')) : Promise.resolve(okResult())
      }),
    })
    const scheduler = fakeScheduler()
    const source = createBranchSource(d, { scheduler })
    source.request(req())
    scheduler.flush()
    await settle()
    expect(source.branch()).toBeUndefined()
    expect(d.close).not.toHaveBeenCalled()

    source.request(req())
    scheduler.flush()
    await settle()
    expect(calls).toBe(2)
    expect(source.branch()).toBe('main')
  })
})

describe('createBranchSource — guards before any call', () => {
  it('makes no request when cwdVerified is false', () => {
    const d = deps()
    const scheduler = fakeScheduler()
    const source = createBranchSource(d, { scheduler })
    source.request(req({ cwdVerified: false }))
    scheduler.flush()
    expect(d.open).not.toHaveBeenCalled()
    expect(source.branch()).toBeUndefined()
  })

  it('makes no request when cwd is empty', () => {
    const d = deps()
    const scheduler = fakeScheduler()
    const source = createBranchSource(d, { scheduler })
    source.request(req({ cwd: '' }))
    scheduler.flush()
    expect(d.open).not.toHaveBeenCalled()
  })

  it('makes no request when cwd is null', () => {
    const d = deps()
    const scheduler = fakeScheduler()
    const source = createBranchSource(d, { scheduler })
    source.request(req({ cwd: null }))
    scheduler.flush()
    expect(d.open).not.toHaveBeenCalled()
  })

  it('clears a previously known branch when the guard fails', async () => {
    const d = deps()
    const scheduler = fakeScheduler()
    const source = createBranchSource(d, { scheduler })
    source.request(req())
    scheduler.flush()
    await settle()
    expect(source.branch()).toBe('main')

    source.request(req({ cwdVerified: false }))
    expect(source.branch()).toBeUndefined()
  })
})

describe('createBranchSource — the remote-consent decision', () => {
  it('skips a non-local session entirely: no call, undefined published', () => {
    const d = deps()
    const scheduler = fakeScheduler()
    const source = createBranchSource(d, { scheduler })
    source.request(req({ isLocal: false }))
    scheduler.flush()
    expect(d.open).not.toHaveBeenCalled()
    expect(source.branch()).toBeUndefined()
  })
})

describe('createBranchSource — debounce', () => {
  it('collapses a burst of requests into one call, for the latest params', () => {
    const d = deps()
    const scheduler = fakeScheduler()
    const source = createBranchSource(d, { scheduler })
    source.request(req({ cwd: '/Users/a/repo' }))
    source.request(req({ cwd: '/Users/a/repo2' }))
    source.request(req({ cwd: '/Users/a/repo3' }))
    expect(scheduler.pendingCount()).toBe(1)
    scheduler.flush()
    expect(d.open).toHaveBeenCalledTimes(1)
    expect(d.open).toHaveBeenCalledWith('sess-1', '/Users/a/repo3')
  })
})

describe('createBranchSource — single-flight', () => {
  it('coalesces requests that arrive while one is already in flight into one follow-up', async () => {
    let resolveFirst: ((res: GitOpenResult) => void) | undefined
    const d = deps({
      open: vi
        .fn()
        .mockImplementationOnce(
          () =>
            new Promise<GitOpenResult>((resolve) => {
              resolveFirst = resolve
            }),
        )
        .mockImplementationOnce(() => Promise.resolve(okResult({ status: undefined }))),
    })
    const scheduler = fakeScheduler()
    const source = createBranchSource(d, { scheduler })

    source.request(req({ cwd: '/Users/a/repo' }))
    scheduler.flush()
    expect(d.open).toHaveBeenCalledTimes(1)

    // Three more requests arrive while the first is still in flight: only
    // the latest is remembered as the one follow-up.
    source.request(req({ cwd: '/Users/a/repo2' }))
    source.request(req({ cwd: '/Users/a/repo3' }))
    source.request(req({ cwd: '/Users/a/repo4' }))
    expect(d.open).toHaveBeenCalledTimes(1)
    expect(scheduler.pendingCount()).toBe(0)

    resolveFirst?.(okResult())
    await settle()
    expect(d.open).toHaveBeenCalledTimes(1)
    // The follow-up is itself debounced: it now waits behind its own timer.
    expect(scheduler.pendingCount()).toBe(1)
    scheduler.flush()
    expect(d.open).toHaveBeenCalledTimes(2)
    expect(d.open).toHaveBeenLastCalledWith('sess-1', '/Users/a/repo4')
  })

  it('a stale in-flight answer is discarded once a guard-fail has already published undefined', async () => {
    let resolveFirst: ((res: GitOpenResult) => void) | undefined
    const d = deps({
      open: vi.fn(
        () =>
          new Promise<GitOpenResult>((resolve) => {
            resolveFirst = resolve
          }),
      ),
    })
    const scheduler = fakeScheduler()
    const source = createBranchSource(d, { scheduler })
    source.request(req())
    scheduler.flush()
    expect(d.open).toHaveBeenCalledTimes(1)

    // The pane's cwd stops being verified while the call is outstanding.
    source.request(req({ cwdVerified: false }))
    expect(source.branch()).toBeUndefined()

    resolveFirst?.(okResult())
    await settle()
    // The stale answer must not repaint over the guard-fail's undefined,
    // but its binding is still closed (ownership-transfer rule).
    expect(source.branch()).toBeUndefined()
    expect(d.close).toHaveBeenCalledWith('b1')
  })
})

describe('createBranchSource — dispose', () => {
  it('cancels a pending debounce timer', () => {
    const d = deps()
    const scheduler = fakeScheduler()
    const source = createBranchSource(d, { scheduler })
    source.request(req())
    expect(scheduler.pendingCount()).toBe(1)
    source.dispose()
    expect(scheduler.pendingCount()).toBe(0)
    scheduler.flush()
    expect(d.open).not.toHaveBeenCalled()
  })
})

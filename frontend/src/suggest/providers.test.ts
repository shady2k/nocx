// @vitest-environment node
// Provider contracts (design §8.5): applicability is part of the contract —
// a provider declares where it applies and is not consulted outside it. The
// local path provider must be inactive on a remote session, and the command
// provider must answer from the running shell's own snapshot.
import { describe, it, expect, vi } from 'vitest'
import {
  commandProvider,
  historyProvider,
  fsProvider,
  createShellProviders,
  shellCompleteProvider,
  MAX_HISTORY_IN_ARGUMENT_POSITION,
  MAX_PROVIDER_CANDIDATES,
  type SuggestContext,
} from './providers'
import { CommandSnapshotStore } from '../command-snapshot'
import type { HistoryQuery } from '../generated/history.query'
import type { FsComplete } from '../generated/fs.complete'
import type { ShellComplete } from '../generated/shell.complete'
import type { Candidate } from './candidate'
import { ProfileClient } from '../profiles'
import type { SSHProfile } from '../profiles'
import { Dispatcher } from '../dispatcher'
import { rankCandidates } from './rank'

const ctx = (over: Partial<SuggestContext> = {}): SuggestContext => ({
  doc: 'git sta',
  token: { text: 'sta', from: 4, to: 7 },
  position: 'argument',
  isLocal: true,
  cwd: '/repo',
  host: '',
  ...over,
})

/** A store holding the SESSION-LOCAL half only (the shell's own tables). */
const snapshotted = (names: string[]): CommandSnapshotStore => {
  const store = new CommandSnapshotStore()
  const nonce = 'a'.repeat(32)
  store.ingest(`H;${nonce}`)
  store.ingest(`S;${nonce};${names.join(';')}`)
  return store
}

/** A store holding both halves, with the shared one in a chosen state. */
const discovered = (
  local: string[],
  shared: string[],
  state: 'ready' | 'stale' | 'timed-out' | 'failed' = 'ready',
  extra: { ageMs?: number; reason?: string } = {},
): CommandSnapshotStore => {
  const store = snapshotted(local)
  store.applySharedNames({
    state,
    names: shared,
    ageMs: extra.ageMs ?? 0,
    reason: extra.reason ?? '',
    truncated: false,
  })
  return store
}
describe('commandProvider', () => {
  const provider = commandProvider(snapshotted(['git', 'gitk', 'gittool']))

  it('is applicable in command position for a bare word', () => {
    expect(
      provider.applicable(ctx({ position: 'command', token: { text: 'git', from: 0, to: 3 } })),
    ).toBe(true)
  })

  it('is not applicable in argument position', () => {
    expect(provider.applicable(ctx({ position: 'argument' }))).toBe(false)
  })

  it('is not applicable for a token containing a slash (a path invocation)', () => {
    expect(
      provider.applicable(ctx({ position: 'command', token: { text: './run', from: 0, to: 5 } })),
    ).toBe(false)
  })

  it('answers names from the snapshot, prefix-filtered', async () => {
    const got = await provider.suggest(
      ctx({ position: 'command', token: { text: 'git', from: 0, to: 3 }, doc: 'git' }),
      new AbortController().signal,
    )
    expect(got.candidates.map((c) => c.insertText)).toEqual(['git', 'gitk', 'gittool'])
    expect(got.candidates[0].id).toBe('cmd:git')
    expect(got.candidates[0].replacement).toEqual({ from: 0, to: 3 })
    expect(got.candidates[0].matchRanges).toEqual([{ from: 0, to: 3 }])
    expect(got.candidates[0].eligibleForGhostText).toBe(true)
  })

  it('offers the union of both halves — the shell tables and the shared PATH set', async () => {
    const provider = commandProvider(discovered(['gitalias'], ['git', 'gitk']))
    const got = await provider.suggest(
      ctx({ position: 'command', token: { text: 'git', from: 0, to: 3 }, doc: 'git' }),
      new AbortController().signal,
    )
    expect(got.candidates.map((c) => c.insertText)).toEqual(['git', 'gitalias', 'gitk'])
  })

  it('an empty snapshot payload cannot apply — the state is named, never "no matches"', async () => {
    // The store rejects an empty name list ("every command is unknown" is a
    // lie), so the snapshot never applies. Nothing has answered the shared
    // half either, so the honest state is the one the request is in.
    const empty = commandProvider(snapshotted([]))
    const got = await empty.suggest(
      ctx({ position: 'command', token: { text: 'git', from: 0, to: 3 }, doc: 'git' }),
      new AbortController().signal,
    )
    expect(got.candidates).toEqual([])
    expect(got.emptyReason).toEqual({
      kind: 'command-names',
      state: 'running',
      ageMs: 0,
      reason: '',
    })
  })

  it('a snapshot that has not arrived yet is named, not hidden', async () => {
    // A fresh store: the OSC 636 hello and snapshot have not been ingested.
    const pending = commandProvider(new CommandSnapshotStore())
    const got = await pending.suggest(
      ctx({ position: 'command', token: { text: 'vi', from: 0, to: 2 }, doc: 'vi' }),
      new AbortController().signal,
    )
    expect(got.candidates).toEqual([])
    expect(got.emptyReason).toEqual({
      kind: 'command-names',
      state: 'running',
      ageMs: 0,
      reason: '',
    })
  })

  // The defect this replaces: every one of these used to render as "command
  // names are still loading". Four of the five are not loading, and three of
  // them never will be.
  it("reports the shared half's own state when nothing matches", async () => {
    const cases = [
      { state: 'ready' as const, extra: {} },
      { state: 'stale' as const, extra: { ageMs: 90_000 } },
      { state: 'timed-out' as const, extra: { reason: 'the scan did not finish in time' } },
      { state: 'failed' as const, extra: { reason: 'remote host refused the exec' } },
    ]
    for (const c of cases) {
      const provider = commandProvider(discovered(['zzz'], ['aaa'], c.state, c.extra))
      const got = await provider.suggest(
        ctx({ position: 'command', token: { text: 'nomatch', from: 0, to: 7 }, doc: 'nomatch' }),
        new AbortController().signal,
      )
      expect(got.candidates).toEqual([])
      expect(got.emptyReason).toEqual({
        kind: 'command-names',
        state: c.state,
        ageMs: c.extra.ageMs ?? 0,
        reason: c.extra.reason ?? '',
      })
    }
  })

  it('a stale shared half still OFFERS its names — only the row says it may be old', async () => {
    const provider = commandProvider(discovered([], ['git'], 'stale', { ageMs: 60_000 }))
    const got = await provider.suggest(
      ctx({ position: 'command', token: { text: 'gi', from: 0, to: 2 }, doc: 'gi' }),
      new AbortController().signal,
    )
    expect(got.candidates.map((c) => c.insertText)).toEqual(['git'])
  })

  it('an empty token asks for nothing — no reason, the line has no intent yet', async () => {
    const pending = commandProvider(new CommandSnapshotStore())
    const got = await pending.suggest(
      ctx({ position: 'command', token: { text: '', from: 0, to: 0 }, doc: '' }),
      new AbortController().signal,
    )
    expect(got.candidates).toEqual([])
    expect(got.emptyReason).toBeUndefined()
  })
})

describe('historyProvider', () => {
  it('completes the whole line from history, newest first, deduped', async () => {
    const query = vi.fn((): Promise<HistoryQuery> =>
      Promise.resolve({
        scope: 'directory',
        exhausted: true,
        source: 'store',
        // The store's horizon (nocx-ms7v.9). Null is the honest value for a
        // fixture: this test is about ranking, not about retention.
        coverage: null,
        entries: [
          {
            id: '2',
            command: 'git status',
            cwd: '/repo',
            host: '',
            status: 'success',
            maskedCount: 0,
            maskedKinds: [],
            endedAt: 200,
          },
          {
            id: '1',
            command: 'git status',
            cwd: '/repo',
            host: '',
            status: 'failure',
            maskedCount: 0,
            maskedKinds: [],
            endedAt: 100,
          },
          {
            id: '3',
            command: 'git stash pop',
            cwd: '/repo',
            host: '',
            status: 'success',
            maskedCount: 0,
            maskedKinds: [],
            endedAt: 300,
          },
          {
            id: '4',
            command: 'ls -la',
            cwd: '/repo',
            host: '',
            status: 'success',
            endedAt: 400,
            maskedCount: 0,
            maskedKinds: [],
          },
        ],
      }),
    )
    const provider = historyProvider({ query })
    const got = await provider.suggest(ctx({ doc: 'git sta' }), new AbortController().signal)

    expect(query).toHaveBeenCalledWith('/repo', '')
    // Only commands starting with the line; the duplicate `git status` keeps
    // the newest row (freshness 200), and the line itself is the replacement.
    expect(got.candidates.map((c) => c.insertText)).toEqual(['git status', 'git stash pop'])
    expect(got.candidates[0].freshness).toBe(200)
    expect(got.candidates[0].replacement).toEqual({ from: 0, to: 7 })
    expect(got.candidates[0].outcome).toEqual({ status: 'success' })
    expect(got.candidates[0].environment?.confidence).toBe('asserted')
  })

  it('is applicable even with a trailing space (the line is non-empty)', () => {
    const provider = historyProvider({ query: vi.fn() })
    expect(provider.applicable(ctx({ doc: 'git ', token: { text: '', from: 4, to: 4 } }))).toBe(
      true,
    )
  })

  it('is not applicable on an empty line', () => {
    const provider = historyProvider({ query: vi.fn() })
    expect(provider.applicable(ctx({ doc: '', token: { text: '', from: 0, to: 0 } }))).toBe(false)
  })

  it('answers nothing when the store errors (one provider never kills the others)', async () => {
    const provider = historyProvider({
      query: vi.fn(() => Promise.reject(new Error('store down'))),
    })
    await expect(provider.suggest(ctx({}), new AbortController().signal)).rejects.toThrow(
      'store down',
    )
  })

  it('argument position on a local session caps history so paths are never crowded', async () => {
    const many = Array.from({ length: MAX_PROVIDER_CANDIDATES + 5 }, (_, i) => ({
      id: String(i),
      command: `cd x${i}`,
      cwd: '/repo',
      host: '',
      status: 'success' as const,
      maskedCount: 0,
      maskedKinds: [],
      endedAt: 100 + i,
    }))
    const provider = historyProvider({
      query: vi.fn((): Promise<HistoryQuery> =>
        Promise.resolve({
          scope: 'directory',
          exhausted: true,
          source: 'store',
          coverage: null,
          entries: many,
        }),
      ),
    })
    const got = await provider.suggest(
      ctx({ doc: 'cd ', token: { text: '', from: 3, to: 3 } }),
      new AbortController().signal,
    )
    expect(got.candidates.length).toBe(MAX_HISTORY_IN_ARGUMENT_POSITION)
  })

  it('command position and remote sessions keep the full provider cap', async () => {
    const makeProvider = (prefix: string) => {
      const entries = Array.from({ length: MAX_PROVIDER_CANDIDATES + 5 }, (_, i) => ({
        id: `${prefix}-${i}`,
        command: `${prefix} x${i}`,
        cwd: '/repo',
        host: '',
        status: 'success' as const,
        maskedCount: 0,
        maskedKinds: [],
        endedAt: 100 + i,
      }))
      return historyProvider({
        query: vi.fn((): Promise<HistoryQuery> =>
          Promise.resolve({
            scope: 'directory',
            exhausted: true,
            source: 'store',
            coverage: null,
            entries,
          }),
        ),
      })
    }
    const inCommand = await makeProvider('git').suggest(
      ctx({ position: 'command', doc: 'git', token: { text: 'git', from: 0, to: 3 } }),
      new AbortController().signal,
    )
    expect(inCommand.candidates.length).toBe(MAX_PROVIDER_CANDIDATES)
    // Remote argument position: the path provider is inactive, so history is
    // the only answer — it keeps its full capacity rather than the
    // argument-position cap, which exists to stop history crowding paths.
    const onRemote = await makeProvider('cd').suggest(
      ctx({ isLocal: false, doc: 'cd ', token: { text: '', from: 3, to: 3 } }),
      new AbortController().signal,
    )
    expect(onRemote.candidates.length).toBe(MAX_PROVIDER_CANDIDATES)
  })
  it('argument position under ssh keeps the full provider cap (paths do not answer there)', async () => {
    const entries = Array.from({ length: MAX_PROVIDER_CANDIDATES + 5 }, (_, i) => ({
      id: `ssh-${i}`,
      command: `ssh myhost -- x${i}`,
      cwd: '/repo',
      host: '',
      status: 'success' as const,
      maskedCount: 0,
      maskedKinds: [],
      endedAt: 100 + i,
    }))
    const provider = historyProvider({
      query: vi.fn((): Promise<HistoryQuery> =>
        Promise.resolve({
          scope: 'directory',
          exhausted: true,
          source: 'store',
          coverage: null,
          entries,
        }),
      ),
    })
    // Under `ssh` the fs provider is inactive (NO_FS_CANDIDATES), so
    // history is the only answer in argument position — it keeps its full
    // capacity rather than the sidebar cap, which exists to stop history
    // crowding paths.
    const got = await provider.suggest(
      ctx({ doc: 'ssh myhost ', token: { text: '', from: 11, to: 11 } }),
      new AbortController().signal,
    )
    expect(got.candidates.length).toBe(MAX_PROVIDER_CANDIDATES)
  })

  it('a history row whose trailing token no longer exists is marked stalePath — demoted, never dropped', async () => {
    // The reported case: the user deleted the file, and the whole-line row
    // `rm zzz-e2e-cmp-msbojbc7` still matches the typed prefix.
    const completeFs = vi.fn((): Promise<FsComplete> => Promise.resolve({ entries: [] }))
    const provider = historyProvider({
      query: vi.fn((): Promise<HistoryQuery> =>
        Promise.resolve({
          scope: 'directory',
          exhausted: true,
          source: 'store',
          coverage: null,
          entries: [
            {
              id: '1',
              command: 'rm zzz-e2e-cmp-msbojbc7',
              cwd: '/repo',
              host: '',
              status: 'success',
              maskedCount: 0,
              maskedKinds: [],
              endedAt: 100,
            },
          ],
        }),
      ),
      completeFs,
    })
    const got = await provider.suggest(
      ctx({
        doc: 'rm zzz-e2e-cmp-msbojbc7',
        token: { text: 'zzz-e2e-cmp-msbojbc7', from: 3, to: 22 },
      }),
      new AbortController().signal,
    )
    // The row is still offered (demotion is not hiding)…
    expect(got.candidates).toHaveLength(1)
    // …but marked stale: the backend answered no entry named exactly the
    // token (soft-empty for a missing path), so the rank sinks it last.
    expect(got.candidates[0].stalePath).toBe(true)
    expect(completeFs).toHaveBeenCalledWith('zzz-e2e-cmp-msbojbc7', '/repo')
  })

  it('an exact entry-name match is existence — the row is not demoted', async () => {
    const completeFs = vi.fn((): Promise<FsComplete> =>
      Promise.resolve({
        entries: [
          { name: 'zzz-e2e-cmp-msbojbc7', path: '/repo/zzz-e2e-cmp-msbojbc7', isDir: false },
        ],
      }),
    )
    const provider = historyProvider({
      query: vi.fn((): Promise<HistoryQuery> =>
        Promise.resolve({
          scope: 'directory',
          exhausted: true,
          source: 'store',
          coverage: null,
          entries: [
            {
              id: '1',
              command: 'rm zzz-e2e-cmp-msbojbc7',
              cwd: '/repo',
              host: '',
              status: 'success',
              maskedCount: 0,
              maskedKinds: [],
              endedAt: 100,
            },
          ],
        }),
      ),
      completeFs,
    })
    const got = await provider.suggest(
      ctx({
        doc: 'rm zzz-e2e-cmp-msbojbc7',
        token: { text: 'zzz-e2e-cmp-msbojbc7', from: 3, to: 22 },
      }),
      new AbortController().signal,
    )
    expect(got.candidates[0].stalePath).toBeUndefined()
  })

  it('an option-looking trailing token is never checked (it is not a path)', async () => {
    const completeFs = vi.fn((): Promise<FsComplete> => Promise.resolve({ entries: [] }))
    const provider = historyProvider({
      query: vi.fn((): Promise<HistoryQuery> =>
        Promise.resolve({
          scope: 'directory',
          exhausted: true,
          source: 'store',
          coverage: null,
          entries: [
            {
              id: '1',
              command: 'ls -la',
              cwd: '/repo',
              host: '',
              status: 'success',
              maskedCount: 0,
              maskedKinds: [],
              endedAt: 100,
            },
          ],
        }),
      ),
      completeFs,
    })
    const got = await provider.suggest(
      ctx({ doc: 'ls -', token: { text: '-', from: 3, to: 4 } }),
      new AbortController().signal,
    )
    expect(got.candidates[0].stalePath).toBeUndefined()
    expect(completeFs).not.toHaveBeenCalled()
  })

  it('a remote session never calls the filesystem — "exists" cannot be known there', async () => {
    const completeFs = vi.fn((): Promise<FsComplete> => Promise.resolve({ entries: [] }))
    const provider = historyProvider({
      query: vi.fn((): Promise<HistoryQuery> =>
        Promise.resolve({
          scope: 'directory',
          exhausted: true,
          source: 'store',
          coverage: null,
          entries: [
            {
              id: '1',
              command: 'rm zzz-e2e-cmp-msbojbc7',
              cwd: '/repo',
              host: 'remote',
              status: 'success',
              maskedCount: 0,
              maskedKinds: [],
              endedAt: 100,
            },
          ],
        }),
      ),
      completeFs,
    })
    const got = await provider.suggest(
      ctx({
        isLocal: false,
        doc: 'rm zzz-e2e-cmp-msbojbc7',
        token: { text: 'zzz-e2e-cmp-msbojbc7', from: 3, to: 22 },
      }),
      new AbortController().signal,
    )
    expect(got.candidates[0].stalePath).toBeUndefined()
    expect(completeFs).not.toHaveBeenCalled()
  })

  it('one fs.complete call per token, cached for the life of the open list', async () => {
    const completeFs = vi.fn((): Promise<FsComplete> => Promise.resolve({ entries: [] }))
    const provider = historyProvider({
      query: vi.fn((): Promise<HistoryQuery> =>
        Promise.resolve({
          scope: 'directory',
          exhausted: true,
          source: 'store',
          coverage: null,
          entries: [
            {
              id: '1',
              command: 'rm zzz-e2e-cmp-msbojbc7',
              cwd: '/repo',
              host: '',
              status: 'success',
              maskedCount: 0,
              maskedKinds: [],
              endedAt: 100,
            },
          ],
        }),
      ),
      completeFs,
    })
    // Two queries within the same interaction (each document extends the
    // previous — the user typed more); the trailing token is unchanged.
    await provider.suggest(
      ctx({ doc: 'rm zzz', token: { text: 'zzz', from: 3, to: 6 } }),
      new AbortController().signal,
    )
    await provider.suggest(
      ctx({
        doc: 'rm zzz-e2e-cmp-msbojbc7',
        token: { text: 'zzz-e2e-cmp-msbojbc7', from: 3, to: 22 },
      }),
      new AbortController().signal,
    )
    expect(completeFs).toHaveBeenCalledTimes(1)
  })
})

describe('fsProvider', () => {
  const complete = vi.fn((text: string): Promise<FsComplete> =>
    Promise.resolve(
      text === './sr'
        ? { entries: [{ name: 'src', path: '/repo/src', isDir: true }] }
        : { entries: [] },
    ),
  )

  const provider = fsProvider({ complete })

  it('is applicable for a local session and a path-looking token', () => {
    expect(provider.applicable(ctx({ token: { text: './sr', from: 3, to: 7 } }))).toBe(true)
    expect(provider.applicable(ctx({ token: { text: '~/Doc', from: 0, to: 5 } }))).toBe(true)
    expect(provider.applicable(ctx({ token: { text: '/usr/lo', from: 0, to: 7 } }))).toBe(true)
  })

  it('is NEVER applicable on a remote session — a local path must not masquerade as a remote one', () => {
    expect(
      provider.applicable(ctx({ isLocal: false, token: { text: './sr', from: 3, to: 7 } })),
    ).toBe(false)
  })

  it('is not applicable in command position for a bare word (a command name, not a path)', () => {
    expect(
      provider.applicable(ctx({ position: 'command', token: { text: 'src', from: 0, to: 3 } })),
    ).toBe(false)
  })

  it('is applicable in argument position for ANY token — including a bare word and the empty token', () => {
    // A bare word in argument position (`ls src`) is an argument, and the
    // argument may be a path — the path provider answers it.
    expect(provider.applicable(ctx({ token: { text: 'src', from: 3, to: 6 } }))).toBe(true)
    // The empty token (`cd ` + Tab) lists the session cwd.
    expect(provider.applicable(ctx({ token: { text: '', from: 4, to: 4 } }))).toBe(true)
  })

  it('is NOT applicable in ssh argument position — a directory is not a destination (nocx-r35s)', () => {
    // The bug: `ssh <TAB>` offered Downloads/, go/, orca/ and repos/ ABOVE the
    // host row — the fs provider answered every argument position, including
    // the one whose answer is a host. ssh's argument is a destination in the
    // remote namespace; the local tree is never the answer there.
    expect(provider.applicable(ctx({ doc: 'ssh ', token: { text: '', from: 4, to: 4 } }))).toBe(
      false,
    )
    expect(
      provider.applicable(ctx({ doc: 'ssh myh', token: { text: 'myh', from: 4, to: 7 } })),
    ).toBe(false)
    // A path FORM under ssh is still an ssh argument — a remote path, which a
    // local path candidate would masquerade as.
    expect(
      provider.applicable(ctx({ doc: 'ssh ./x', token: { text: './x', from: 4, to: 8 } })),
    ).toBe(false)
    // The table grows by addition: a command it has never heard of keeps the
    // default of "both kinds" (`scp`'s first argument IS a local file).
    expect(provider.applicable(ctx({ doc: 'scp ', token: { text: '', from: 4, to: 4 } }))).toBe(
      true,
    )
    expect(provider.applicable(ctx({ doc: 'ls ', token: { text: '', from: 3, to: 3 } }))).toBe(true)
  })

  it('an empty token lists the session cwd (the wire refuses empty text, so it asks for ./)', async () => {
    const complete = vi.fn((text: string): Promise<FsComplete> =>
      Promise.resolve(
        text === './'
          ? {
              entries: [
                { name: 'src', path: '/repo/src', isDir: true },
                { name: 'notes.txt', path: '/repo/notes.txt', isDir: false },
              ],
            }
          : { entries: [] },
      ),
    )
    const provider = fsProvider({ complete })
    const got = await provider.suggest(
      // `ls` keeps both kinds — this test is about the empty-token display,
      // and `cd` would filter the file out by the dirs-only rule.
      ctx({ doc: 'ls ', token: { text: '', from: 3, to: 3 } }),
      new AbortController().signal,
    )
    expect(complete).toHaveBeenCalledWith('./', '/repo')
    // The display keys off the REAL token, so rows show bare names — never a
    // `./` the user did not type.
    expect(got.candidates.map((c) => c.displayText)).toEqual(['src/', 'notes.txt'])
    expect(got.candidates[0].insertText).toBe('src/')
    expect(got.candidates[0].replacement).toEqual({ from: 3, to: 3 })
  })

  it('cd, pushd and rmdir take directories only; everything else keeps both kinds', async () => {
    const complete = vi.fn((): Promise<FsComplete> =>
      Promise.resolve({
        entries: [
          { name: 'docs', path: '/repo/docs', isDir: true },
          { name: 'notes.txt', path: '/repo/notes.txt', isDir: false },
        ],
      }),
    )
    const provider = fsProvider({ complete })
    const forCmd = async (doc: string, token: { text: string; from: number; to: number }) =>
      provider.suggest(ctx({ doc, token }), new AbortController().signal)

    const gotCd = await forCmd('cd ', { text: '', from: 3, to: 3 })
    expect(gotCd.candidates.map((c) => c.insertText)).toEqual(['docs/'])
    const gotPushd = await forCmd('pushd ', { text: '', from: 7, to: 7 })
    expect(gotPushd.candidates.map((c) => c.insertText)).toEqual(['docs/'])
    const gotRmdir = await forCmd('rmdir n', { text: 'n', from: 6, to: 7 })
    expect(gotRmdir.candidates.map((c) => c.insertText)).toEqual(['docs/'])

    // Anything else — including a command the table has never heard of —
    // keeps the documented default of "both": the rule is a promise about
    // the command's argument, and for an unknown command we promise nothing.
    const gotLs = await forCmd('ls ', { text: '', from: 3, to: 3 })
    expect(gotLs.candidates.map((c) => c.insertText)).toEqual(['docs/', 'notes.txt'])
    const gotUnknown = await forCmd('someday n', { text: 'n', from: 8, to: 9 })
    expect(gotUnknown.candidates.map((c) => c.insertText)).toEqual(['docs/', 'notes.txt'])
  })

  it('a dirs-only command whose directory holds no subdirectories names that, not "no matches"', async () => {
    // The owner's exact case: `cd Downloads/` where Downloads holds only a
    // file. The dirs-only filter removes the file, leaving zero candidates —
    // and the reason must say WHY: the directory has no subdirectories.
    const complete = vi.fn((): Promise<FsComplete> =>
      Promise.resolve({
        entries: [
          { name: 'nocx-backup.enc', path: '/repo/Downloads/nocx-backup.enc', isDir: false },
        ],
      }),
    )
    const provider = fsProvider({ complete })
    const got = await provider.suggest(
      ctx({ doc: 'cd Downloads/', token: { text: 'Downloads/', from: 3, to: 13 }, cwd: '/repo' }),
      new AbortController().signal,
    )
    expect(got.candidates).toEqual([])
    expect(got.emptyReason).toEqual({ kind: 'dirs-only-empty', dir: 'Downloads' })
  })

  it('the cwd itself is named as "this folder" when it holds no subdirectories', async () => {
    const complete = vi.fn((): Promise<FsComplete> =>
      Promise.resolve({
        entries: [{ name: 'notes.txt', path: '/repo/notes.txt', isDir: false }],
      }),
    )
    const provider = fsProvider({ complete })
    const got = await provider.suggest(
      ctx({ doc: 'cd ', token: { text: '', from: 3, to: 3 }, cwd: '/repo' }),
      new AbortController().signal,
    )
    expect(got.candidates).toEqual([])
    expect(got.emptyReason).toEqual({ kind: 'dirs-only-empty', dir: '' })
  })

  it('a directory listed with no prefix and holding nothing says so', async () => {
    const complete = vi.fn((): Promise<FsComplete> => Promise.resolve({ entries: [] }))
    const provider = fsProvider({ complete })
    const got = await provider.suggest(
      ctx({ doc: 'ls empty/', token: { text: 'empty/', from: 3, to: 9 } }),
      new AbortController().signal,
    )
    expect(got.candidates).toEqual([])
    // Not the generic no-match, which this used to assert. The token ends in
    // `/`, so the user typed no prefix for anything to fail to match: the
    // listing succeeded and the folder is empty. Tab completing INTO a folder
    // is the common way to arrive here, and "No matches" reads there as if the
    // completion had failed when it had just succeeded (nocx-azxe.5).
    expect(got.emptyReason).toEqual({ kind: 'empty-dir', dir: 'empty' })
  })

  it('a prefix that matches nothing is still the generic no-match', async () => {
    const complete = vi.fn((): Promise<FsComplete> => Promise.resolve({ entries: [] }))
    const provider = fsProvider({ complete })
    const got = await provider.suggest(
      ctx({ doc: 'ls empty/zz', token: { text: 'empty/zz', from: 3, to: 11 } }),
      new AbortController().signal,
    )
    expect(got.candidates).toEqual([])
    // Here the user DID type a prefix, and nothing matched it. The two cases
    // must stay distinguishable — that is the whole point of the new reason.
    expect(got.emptyReason).toBeUndefined()
  })

  it('labels every row with its filesystem kind (Directory / File)', async () => {
    const got = await provider.suggest(
      ctx({ doc: 'cd ./sr', token: { text: './sr', from: 3, to: 7 } }),
      new AbortController().signal,
    )
    expect(got.candidates[0].kind).toBe('directory')
  })

  it('maps backend entries to candidates with display, match and slash-for-dirs', async () => {
    const got = await provider.suggest(
      ctx({ doc: 'cd ./sr', token: { text: './sr', from: 3, to: 7 } }),
      new AbortController().signal,
    )
    expect(complete).toHaveBeenCalledWith('./sr', '/repo')
    expect(got.candidates).toHaveLength(1)
    const c = got.candidates[0]
    // Report 3 — the display is the LAST SEGMENT: the typed `./` prefix is
    // already in the line and is not repeated in the row. insertText (what
    // a pick inserts) still carries the full path the user wrote.
    expect(c.displayText).toBe('src/')
    expect(c.insertText).toBe('./src/')
    expect(c.id).toBe('fs:/repo/src')
    expect(c.replacement).toEqual({ from: 3, to: 7 })
    // The matched prefix `sr` sits INSIDE the segment the row shows.
    expect(c.matchRanges).toEqual([{ from: 0, to: 2 }])
    expect(c.source).toBe('path')
    expect(c.eligibleForGhostText).toBe(true)
  })

  it('report 3: a multi-level typed prefix is not repeated in the rows', async () => {
    const complete = vi.fn((text: string): Promise<FsComplete> =>
      Promise.resolve(
        text === 'repos/meshynet/'
          ? { entries: [{ name: 'bin', path: '/repo/repos/meshynet/bin', isDir: true }] }
          : { entries: [] },
      ),
    )
    const got = await fsProvider({ complete }).suggest(
      ctx({
        doc: 'cd repos/meshynet/',
        token: { text: 'repos/meshynet/', from: 3, to: 19 },
      }),
      new AbortController().signal,
    )
    expect(got.candidates).toHaveLength(1)
    const c = got.candidates[0]
    // The row reads `bin/`, never `repos/meshynet/bin/` — the parent is in
    // the line, and the full path survives in the id and insertText.
    expect(c.displayText).toBe('bin/')
    expect(c.insertText).toBe('repos/meshynet/bin/')
    expect(c.id).toBe('fs:/repo/repos/meshynet/bin')
    // A trailing slash means the segment is complete: nothing is marked.
    expect(c.matchRanges).toEqual([])
  })

  it('report 3: a partial segment marks the typed part inside the name', async () => {
    const complete = vi.fn((text: string): Promise<FsComplete> =>
      Promise.resolve(
        text === 'repos/meshynet/gr'
          ? {
              entries: [
                { name: 'graphify-out', path: '/repo/repos/meshynet/graphify-out', isDir: true },
              ],
            }
          : { entries: [] },
      ),
    )
    const got = await fsProvider({ complete }).suggest(
      ctx({
        doc: 'cd repos/meshynet/gr',
        token: { text: 'repos/meshynet/gr', from: 3, to: 21 },
      }),
      new AbortController().signal,
    )
    expect(got.candidates).toHaveLength(1)
    const c = got.candidates[0]
    expect(c.displayText).toBe('graphify-out/')
    expect(c.matchRanges).toEqual([{ from: 0, to: 2 }])
    expect(c.insertText).toBe('repos/meshynet/graphify-out/')
  })
  it('answers nothing on a provider error', async () => {
    const failing = fsProvider({
      complete: vi.fn(() => Promise.reject(new Error('no such dir'))),
    })
    await expect(
      failing.suggest(
        ctx({ token: { text: './sr', from: 3, to: 7 } }),
        new AbortController().signal,
      ),
    ).rejects.toThrow('no such dir')
  })
})

describe('createShellProviders under ssh (nocx-r35s)', () => {
  const profile = (over: Partial<SSHProfile> = {}): SSHProfile => ({
    id: 'p1',
    type: 'ssh',
    name: 'Prod',
    options: { host: 'prod-db' },
    ...over,
  })
  const client = (): ProfileClient => {
    const pc = new ProfileClient(new Dispatcher())
    vi.spyOn(pc, 'listProfiles').mockResolvedValue([profile()])
    vi.spyOn(pc, 'listSSHAliases').mockResolvedValue({
      aliases: [{ alias: 'staging-db', hostName: 'staging.example.com' }],
      unavailable: null,
    })
    return pc
  }
  const sshCtx = (): SuggestContext =>
    ctx({ doc: 'ssh ', token: { text: '', from: 4, to: 4 }, cwd: '/home/dev' })

  it('`ssh <TAB>` produces no candidate of kind path — the rows are hosts and ssh history', async () => {
    const providers = createShellProviders({
      store: snapshotted([]),
      queryHistory: () =>
        Promise.resolve({
          scope: 'directory',
          exhausted: true,
          source: 'store',
          coverage: null,
          entries: [
            {
              id: '1',
              command: 'ssh prod-db',
              cwd: '/home/dev',
              host: '',
              status: 'success',
              maskedCount: 0,
              maskedKinds: [],
              endedAt: 100,
            },
          ],
        }),
      // The backend answers the local tree here — it must never be consulted
      // for ssh, and the merged rows must prove it (a test that only checked
      // ranking would pass with the paths still in the list, one keystroke
      // from the bug).
      completeFs: () =>
        Promise.resolve({
          entries: [
            { name: 'Downloads', path: '/home/dev/Downloads', isDir: true },
            { name: 'go', path: '/home/dev/go', isDir: true },
          ],
        }),
      profileClient: client(),
    })

    const all: Candidate[] = []
    for (const p of providers) {
      if (!p.applicable(sshCtx())) continue
      const batch = await p.suggest(sshCtx(), new AbortController().signal)
      all.push(...batch.candidates)
    }

    // The user-visible contract: NOTHING of kind path under ssh. The rows
    // that do appear are hosts (profiles + config aliases) and ssh history.
    expect(all.some((c) => c.source === 'path')).toBe(false)
    expect(all.filter((c) => c.source === 'host').map((c) => c.insertText)).toEqual([
      'prod-db',
      'staging-db',
    ])
    expect(all.filter((c) => c.source === 'history').map((c) => c.insertText)).toEqual([
      'ssh prod-db',
    ])
  })
})

describe('createShellProviders and the snippet library (nocx-nlhe)', () => {
  const assembled = (snippets?: {
    snippets: () => { id: string; title: string; body: string }[]
    ensureLoaded: () => void
  }) =>
    createShellProviders({
      store: snapshotted(['git']),
      queryHistory: () =>
        Promise.resolve({
          scope: 'directory',
          exhausted: true,
          source: 'store',
          coverage: null,
          entries: [],
        }),
      completeFs: () => Promise.resolve({ entries: [], truncated: false }),
      snippets,
    })

  it('registers the snippet provider when a library is wired', () => {
    const providers = assembled({ snippets: () => [], ensureLoaded: () => {} })
    expect(providers.map((p) => p.id)).toContain('snippet')
  })

  it('registers nothing when no library is wired — a provider that cannot answer is absent', () => {
    // The same rule the host provider follows: an unwired seam is not a
    // provider that answers nothing, it is a provider that does not exist.
    expect(assembled().map((p) => p.id)).not.toContain('snippet')
  })

  it('a snippet row and a command row come back from ONE query, ranked command-first', async () => {
    // The dropdown a person sees, not the provider in isolation: both
    // sources answer the same keystroke, and the executable keeps its place.
    const providers = assembled({
      snippets: () => [{ id: 's1', title: 'gitsync', body: 'git pull && git push' }],
      ensureLoaded: () => {},
    })
    const c = ctx({ doc: 'git', token: { text: 'git', from: 0, to: 3 }, position: 'command' })
    // Both shipped providers answer synchronously here; the awaits keep the
    // call the same shape the controller makes.
    const batches = providers
      .filter((p) => p.applicable(c))
      .map((p) => p.suggest(c, new AbortController().signal))
    const candidates = (await Promise.all(batches.map(async (b) => await b))).flatMap(
      (b) => b.candidates,
    )
    const ranked = rankCandidates(candidates, { query: 'git', now: 1_750_000_000_000 })
    expect(ranked.map((r) => r.source)).toEqual(['command', 'snippet'])
  })
})

// ── the remote shell adapter: what a pick actually inserts ────────────────
//
// The adapter had no test of its own, and that is how `cd repos/t` shipped
// completing to `cd repos/repos/tabby/` (nocx-yqoy5): the row's insert text is
// the token's prefix PLUS the candidate name, so a backend that answers with
// the whole word rather than the last segment doubles the prefix. Asserting
// the rendered row was never going to catch it — only assembling the line the
// user ends up with does.
describe('shellCompleteProvider — the line a pick produces', () => {
  const answering = (entries: ShellComplete['entries']) =>
    shellCompleteProvider({
      complete: () => Promise.resolve({ entries, truncated: false }),
      sessionId: () => 'sess-1',
    })

  /** The document after accepting the candidate, exactly as the controller
   *  applies it (replacement range + insertText). */
  const accepted = (doc: string, c: Candidate): string =>
    doc.slice(0, c.replacement.from) + c.insertText + doc.slice(c.replacement.to)

  const remote = (doc: string, text: string) =>
    ctx({
      isLocal: false,
      doc,
      token: { text, from: doc.length - text.length, to: doc.length },
      position: 'argument',
      cwd: '/home/dev',
      host: 'dev@192.168.0.25',
    })

  it('keeps the typed directory once when the token is nested', async () => {
    const provider = answering([
      { name: 'tabby', path: '/home/dev/repos/tabby', source: 'path', isDir: true },
    ])
    const c = remote('cd repos/t', 'repos/t')
    const { candidates } = await provider.suggest(c, new AbortController().signal)
    expect(accepted(c.doc, candidates[0])).toBe('cd repos/tabby/')
    // The row shows the segment, not the parent the line already carries.
    expect(candidates[0].displayText).toBe('tabby/')
  })

  it('steps into a directory when the token ends in a slash', async () => {
    const provider = answering([
      { name: 'tabby', path: '/home/dev/repos/tabby', source: 'path', isDir: true },
    ])
    const c = remote('cd repos/', 'repos/')
    const { candidates } = await provider.suggest(c, new AbortController().signal)
    expect(accepted(c.doc, candidates[0])).toBe('cd repos/tabby/')
  })

  it('completes a bare token with no prefix to re-add', async () => {
    const provider = answering([
      { name: 'repos', path: '/home/dev/repos', source: 'path', isDir: true },
    ])
    const c = remote('cd re', 're')
    const { candidates } = await provider.suggest(c, new AbortController().signal)
    expect(accepted(c.doc, candidates[0])).toBe('cd repos/')
  })

  it('inserts a file without a trailing slash', async () => {
    const provider = answering([
      { name: 'notes.md', path: '/home/dev/repos/notes.md', source: 'path', isDir: false },
    ])
    const c = remote('cat repos/n', 'repos/n')
    const { candidates } = await provider.suggest(c, new AbortController().signal)
    expect(accepted(c.doc, candidates[0])).toBe('cat repos/notes.md')
  })

  it('inserts a completion-function word whole — it is already the replacement', async () => {
    // `function` answers come from the remote shell's own completion function
    // (a git branch, say). They are the whole word by construction, so the
    // token prefix must NOT be prepended to them.
    const provider = answering([{ name: 'main', source: 'function' }])
    const c = remote('git checkout ma', 'ma')
    const { candidates } = await provider.suggest(c, new AbortController().signal)
    expect(accepted(c.doc, candidates[0])).toBe('git checkout main')
  })
})

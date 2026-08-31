// @vitest-environment node
// Host provider contracts (bead nocx-n9i6): `ssh <TAB>` offers the hosts
// the quick-connect picker shows — profiles plus live aliases, deduped and
// degraded-resolver-surfaced by the SAME assembly (host-provider.ts routes
// quick-connect-assembly.ts, it does not rebuild it). Node environment:
// the assembly is plain code now, so these tests need no DOM.
import { describe, it, expect, vi } from 'vitest'
import { hostProvider } from './host-provider'
import type { SuggestContext } from './providers'
import { ProfileClient } from '../profiles'
import type { SSHAliasEntry, SSHAliasUnavailable, SSHProfile } from '../profiles'
import { Dispatcher } from '../dispatcher'
import { fixedEndpoint } from '../endpoint'

const ctx = (over: Partial<SuggestContext> = {}): SuggestContext => ({
  doc: 'git sta',
  token: { text: 'sta', from: 4, to: 7 },
  position: 'argument',
  isLocal: true,
  cwd: '/repo',
  host: '',
  ...over,
})

describe('hostProvider', () => {
  const profile = (over: Partial<SSHProfile> = {}): SSHProfile => ({
    id: 'p1',
    type: 'ssh',
    name: 'Prod',
    options: { host: 'prod' },
    ...over,
  })
  const client = (
    over: {
      profiles?: SSHProfile[]
      aliases?: SSHAliasEntry[]
      unavailable?: SSHAliasUnavailable | null
    } = {},
  ): ProfileClient => {
    const pc = new ProfileClient(new Dispatcher(fixedEndpoint(9876)))
    vi.spyOn(pc, 'listProfiles').mockResolvedValue(over.profiles ?? [])
    vi.spyOn(pc, 'listSSHAliases').mockResolvedValue({
      aliases: over.aliases ?? [],
      unavailable: over.unavailable ?? null,
    })
    return pc
  }
  /** `ssh ` with the caret at the end, argument position. */
  const sshCtx = (over: Partial<SuggestContext> = {}): SuggestContext =>
    ctx({ doc: 'ssh ', token: { text: '', from: 4, to: 4 }, ...over })

  it('is applicable only to the ssh command in argument position', () => {
    const provider = hostProvider({ profileClient: client() })
    // `ssh <TAB>` and `ssh myh` — the position this provider exists for.
    expect(provider.applicable(sshCtx())).toBe(true)
    expect(
      provider.applicable(sshCtx({ token: { text: 'myh', from: 4, to: 7 }, doc: 'ssh myh' })),
    ).toBe(true)
    // `ls ` must not offer hosts.
    expect(provider.applicable(ctx({ doc: 'ls ', token: { text: '', from: 3, to: 3 } }))).toBe(
      false,
    )
    // Not for any other command's arguments.
    expect(
      provider.applicable(ctx({ doc: 'git myh', token: { text: 'myh', from: 4, to: 7 } })),
    ).toBe(false)
    // Not in command position.
    expect(
      provider.applicable(
        ctx({ position: 'command', doc: 'ssh', token: { text: 'ssh', from: 0, to: 3 } }),
      ),
    ).toBe(false)
    // Not mid-path: `ssh some/path` completes a path argument, never a host.
    expect(
      provider.applicable(
        ctx({ doc: 'ssh some/path', token: { text: 'some/path', from: 4, to: 13 } }),
      ),
    ).toBe(false)
  })

  it('`ssh <TAB>` offers every host — profiles and live aliases, one row each', async () => {
    const provider = hostProvider({
      profileClient: client({
        profiles: [profile({ id: 'p1', options: { host: 'prod', user: 'root' } })],
        aliases: [{ alias: 'dev', hostName: '10.0.0.5' }],
      }),
    })
    const got = await provider.suggest(sshCtx(), new AbortController().signal)
    expect(got.candidates.map((c) => c.insertText)).toEqual(['root@prod', 'dev'])
    const c = got.candidates[0]
    expect(c.source).toBe('host')
    expect(c.providerId).toBe('host')
    expect(c.replacement).toEqual({ from: 4, to: 4 })
    expect(c.matchRanges).toEqual([{ from: 0, to: 0 }])
    expect(c.eligibleForGhostText).toBe(true)
  })

  it('`ssh myh` filters hosts by prefix — including the bare host of a user@host label', async () => {
    const provider = hostProvider({
      profileClient: client({
        profiles: [
          profile({ id: 'p1', options: { host: 'myhost', user: 'root' } }),
          profile({ id: 'p2', options: { host: 'myhost2' } }),
          profile({ id: 'p3', options: { host: 'elsewhere' } }),
        ],
      }),
    })
    const got = await provider.suggest(
      sshCtx({ doc: 'ssh myh', token: { text: 'myh', from: 4, to: 7 } }),
      new AbortController().signal,
    )
    expect(got.candidates.map((c) => c.insertText)).toEqual(['root@myhost', 'myhost2'])
    // The bare-host match points INTO the label, after the `user@`.
    expect(got.candidates[0].matchRanges).toEqual([{ from: 5, to: 8 }])
    expect(got.candidates[0].displayText).toBe('root@myhost')
    // A user-prefix match starts at the label's beginning.
    const byUser = await provider.suggest(
      sshCtx({ doc: 'ssh roo', token: { text: 'roo', from: 4, to: 7 } }),
      new AbortController().signal,
    )
    expect(byUser.candidates.map((c) => c.insertText)).toEqual(['root@myhost'])
    expect(byUser.candidates[0].matchRanges).toEqual([{ from: 0, to: 3 }])
  })

  it('a host already covered by a saved profile appears once, from the profile', async () => {
    // The quick-connect assembly's dedup: an alias targeted by a saved
    // profile is suppressed, because the profile is ours and wins. The one
    // row is the PROFILE's form (user@host), never the alias's.
    const provider = hostProvider({
      profileClient: client({
        profiles: [profile({ id: 'p1', options: { host: 'prod', user: 'root' } })],
        aliases: [{ alias: 'prod', hostName: 'prod.example.com' }],
      }),
    })
    const got = await provider.suggest(sshCtx(), new AbortController().signal)
    expect(got.candidates).toHaveLength(1)
    expect(got.candidates[0].insertText).toBe('root@prod')
  })

  it('an unavailable ssh -G resolver surfaces the condition rather than an empty list', async () => {
    const provider = hostProvider({
      profileClient: client({
        unavailable: { reason: 'no-ssh-binary', detail: 'ssh not found' },
      }),
    })
    const got = await provider.suggest(sshCtx(), new AbortController().signal)
    expect(got.candidates).toEqual([])
    expect(got.emptyReason).toEqual({
      kind: 'hosts-unavailable',
      reason: 'no-ssh-binary',
      detail: 'ssh not found',
    })
  })

  it('profiles still answer when the resolver is degraded — and the degraded row is never a candidate', async () => {
    const provider = hostProvider({
      profileClient: client({
        profiles: [profile({ id: 'p1', options: { host: 'prod' } })],
        unavailable: { reason: 'parse-failure', detail: 'could not read ~/.ssh/config' },
      }),
    })
    const got = await provider.suggest(sshCtx(), new AbortController().signal)
    expect(got.candidates.map((c) => c.insertText)).toEqual(['prod'])
    expect(got.emptyReason).toBeUndefined()
    expect(got.candidates.some((c) => c.insertText.startsWith('SSH config'))).toBe(false)
  })

  it('nothing matches the prefix — the generic no-match, no condition to name', async () => {
    const provider = hostProvider({
      profileClient: client({
        profiles: [profile({ id: 'p1', options: { host: 'prod' } })],
      }),
    })
    const got = await provider.suggest(
      sshCtx({ doc: 'ssh zzz', token: { text: 'zzz', from: 4, to: 7 } }),
      new AbortController().signal,
    )
    expect(got.candidates).toEqual([])
    expect(got.emptyReason).toBeUndefined()
  })

  it('answers nothing after abort — a provider may not deliver after the query moved on', async () => {
    const provider = hostProvider({
      profileClient: client({
        profiles: [profile({ id: 'p1', options: { host: 'prod' } })],
      }),
    })
    const ac = new AbortController()
    ac.abort()
    const got = await provider.suggest(sshCtx(), ac.signal)
    expect(got.candidates).toEqual([])
  })
})

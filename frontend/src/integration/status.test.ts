import { describe, it, expect, vi } from 'vitest'
import {
  INTEGRATION_EXPLANATION,
  INTEGRATION_REASONS,
  IntegrationSilenceStore,
  integrationMessage,
  isDegraded,
  observationSentence,
  shellFamily,
  subscribeIntegrationChanged,
  type IntegrationReason,
} from './status'
import type { Dispatcher } from '../dispatcher'
import type { SessionIntegrationChanged } from '../generated/session.integrationChanged'

/** Every reason the contract declares, taken from the CONTRACT and not from a
 *  copy of it.
 *
 *  It was a hand-written list once, on the argument that a reason added to the
 *  contract without a message would fail here. It would not: the list and the
 *  message table were two places to forget the same thing, and the check
 *  passed as long as they agreed with each other rather than with the wire.
 *  The carrier design added twenty-four members and that is exactly the size
 *  of mistake a hand-written copy makes silently.
 *
 *  The list then read `contracts/session.integrationChanged.schema.json` off
 *  the disk, which is right on this machine and wrong in the container CI
 *  actually runs: `vitest_containerized` assembles its workspace from
 *  `frontend/` alone — deliberately, so the host's `node_modules` can never
 *  leak in — so the relative path walked out of the workspace to `/` and the
 *  suite died on ENOENT before asserting anything.
 *
 *  So the source is the GENERATED renderer type instead, and the chain to the
 *  schema is unbroken and gated at every link: `npm run contracts:check`
 *  proves the committed generated type matches the schema, `IntegrationReason`
 *  is that type's own enum, and `MESSAGES` is a `Record` over it — so `tsc`
 *  refuses a member with no message and a message with no member.
 *  INTEGRATION_REASONS is that Record's keys, which makes it the wire's
 *  vocabulary rather than a second declaration of it, and it reaches nothing
 *  outside the workspace.
 *
 *  `unknown` is the renderer's fallback and is in the enum like any other. */
const REASONS: IntegrationReason[] = INTEGRATION_REASONS

const fact = (over: Partial<SessionIntegrationChanged> = {}): SessionIntegrationChanged => ({
  sessionId: 's1',
  instanceId: '0123456789abcdef0123456789abcdef',
  sessionEpoch: 1,
  status: 'conventional',
  reason: 'handshake-timeout',
  shell: '/bin/bash',
  ...over,
})

describe('what the product says about a degraded session', () => {
  // The loops below are all `for (const reason of REASONS)`, so a vocabulary
  // that came back empty would make every one of them pass while asserting
  // nothing. The count is stated here so that failure is loud.
  it('reads a non-empty closed vocabulary out of the contract', () => {
    expect(REASONS.length).toBeGreaterThanOrEqual(31)
    expect(REASONS).toContain('generation-unavailable')
    expect(REASONS).toContain('unknown')
  })

  it('has words for every reason the wire can carry', () => {
    for (const reason of REASONS) {
      const m = integrationMessage(fact({ reason }))
      expect(m, reason).not.toBeNull()
      expect(m!.title.length, reason).toBeGreaterThan(0)
      expect(m!.description.length, reason).toBeGreaterThan(0)
      expect(m!.happening.length, reason).toBeGreaterThan(0)
      expect(m!.lastGoodStep.length, reason).toBeGreaterThan(0)
    }
  })

  // The owner's rule, asserted rather than trusted to review: nocx cannot
  // see which program took the shell over — AD-6 forbids reading the byte
  // stream and the process table is a race — so naming one would present a
  // guess as a finding. The Details dialog carries the observation instead,
  // labelled as a guess.
  it('names no third-party program in any message', () => {
    const forbidden = [
      'oh-my-zsh',
      'ohmyzsh',
      'powerlevel',
      'starship',
      'fish',
      'tmux',
      'zplug',
      'nvm',
      'conda',
    ]
    for (const reason of REASONS) {
      const m = integrationMessage(fact({ reason }))!
      const text = [
        m.title,
        m.description,
        m.happening,
        m.lastGoodStep,
        m.fix?.lead ?? '',
        m.fix?.snippet ?? '',
      ]
        .join(' ')
        .toLowerCase()
      for (const name of forbidden) {
        expect(text.includes(name), `${reason} mentions ${name}`).toBe(false)
      }
    }
  })

  // The two reasons are now a pair, and each must keep its own claim.
  // handshake-timeout is what is left when the startup DID return and the
  // shell still never answered, so it must not borrow the startup-file
  // sentence; startup-did-not-return must say it, because that stage is
  // exactly what the backend observed (nocx-yww2).
  it('does not claim an interception it cannot observe', () => {
    const m = integrationMessage(fact({ reason: 'handshake-timeout' }))!
    expect(m.description.toLowerCase()).not.toContain('startup file')
    const s = integrationMessage(fact({ reason: 'startup-did-not-return' }))!
    expect(s.description.toLowerCase()).toContain('startup file')
  })

  it('says nothing about a session that is starting or integrated', () => {
    expect(integrationMessage(fact({ status: 'starting', reason: undefined }))).toBeNull()
    expect(integrationMessage(fact({ status: 'integrated', reason: undefined }))).toBeNull()
    expect(isDegraded(fact({ status: 'starting', reason: undefined }))).toBe(false)
    expect(isDegraded(null)).toBe(false)
  })

  it('treats lost as degraded — an integration that ended is still a plain terminal', () => {
    expect(isDegraded(fact({ status: 'lost', reason: 'channel-lost' }))).toBe(true)
  })

  // An unrenderable reason is still a degraded session. Silence is the
  // defect this whole surface exists to remove, so a reason from a newer
  // backend falls back to "unknown" rather than to nothing.
  it('falls back to unknown for a reason it does not recognise', () => {
    const m = integrationMessage(fact({ reason: 'brand-new' as IntegrationReason }))
    expect(m).not.toBeNull()
    expect(m!.title).toBe('Not integrated')
  })
})

// ── the fix nocx offers (nocx-0mqs) ───────────────────────────────────────
//
// The defect these were written against: a zsh session was shown
// `bash -lic …` and told to bisect ~/.bashrc. Three things wrong at once —
// a shell the session is not running, a file it never reads, and a guessing
// game where nocx's own launcher gives it a measured answer.

describe('the fix a degraded session is offered', () => {
  const fixFor = (shell: string) => integrationMessage(fact({ shell }))!.fix

  // THE bug, stated from the user's side: nothing put in front of a zsh
  // user may be a bash command or a bash file.
  it('never hands a zsh session a bash command or a bash startup file', () => {
    const fix = fixFor('/bin/zsh')!
    const text = `${fix.lead}\n${fix.snippet}`
    expect(text).not.toMatch(/\bbash\b/)
    expect(text).not.toContain('.bashrc')
    expect(text).toContain('/bin/zsh')
    expect(text).toContain('~/.zshrc')
  })

  it('names the bash startup file for a bash session, and no zsh one', () => {
    const fix = fixFor('/opt/homebrew/bin/bash')!
    const text = `${fix.lead}\n${fix.snippet}`
    expect(text).toContain('/opt/homebrew/bin/bash')
    expect(text).toContain('~/.bashrc')
    expect(text).not.toContain('.zshrc')
    expect(text).not.toMatch(/\bzsh\b/)
  })

  // The startup file named is the one nocx's OWN launcher sources —
  // internal/shellintegration/launcher_bash.go sources ~/.bashrc and
  // launcher_zsh.go sources ~/.zshrc. Advice about a file nocx never reads
  // is advice that cannot work.
  it('probes the shell nocx actually started, not a shell it assumed', () => {
    expect(fixFor('/bin/zsh')!.snippet).toContain("/bin/zsh -ic 'echo nocx-reached-a-prompt'")
    expect(fixFor('/bin/bash')!.snippet).toContain("/bin/bash -ic 'echo nocx-reached-a-prompt'")
  })

  // The measured answer replaces the textbook one. nocx exports
  // NOCX_SHELL_INTEGRATION=1 before the user's rc runs (verified in
  // internal/shellintegration), so a block that takes the shell over can be
  // told to stand aside. Telling the user to halve their rc file is what we
  // say when we know nothing, and here we know something.
  it('offers the gate nocx sets rather than telling the user to bisect', () => {
    const fix = fixFor('/bin/zsh')!
    expect(fix.snippet).toContain('NOCX_SHELL_INTEGRATION')
    expect(fix.snippet).toContain('if [ -z "$NOCX_SHELL_INTEGRATION" ]; then')
    const text = `${fix.lead} ${fix.snippet}`.toLowerCase()
    expect(text).not.toContain('bisect')
    expect(text).not.toContain('half')
    expect(text).not.toContain('piece at a time')
  })

  // A shell nocx has no launcher for still gets the gate — the variable is
  // exported for every session — but not a command line invented for it.
  it('gives an unfamiliar shell the gate and no invented invocation', () => {
    const fix = fixFor('/usr/local/bin/fish')!
    expect(fix.snippet).toContain('NOCX_SHELL_INTEGRATION')
    expect(fix.snippet).not.toContain('-ic')
    expect(fix.snippet).not.toContain('.zshrc')
    expect(fix.snippet).not.toContain('.bashrc')
  })

  // A path with a space would otherwise produce a command line that runs the
  // wrong thing when pasted.
  it('quotes a shell path the shell would otherwise split', () => {
    const fix = fixFor('/Users/me/my shells/zsh')!
    expect(fix.snippet).toContain("'/Users/me/my shells/zsh' -ic")
  })

  // A reason with no honest remedy offers none: an empty "How to fix" that
  // says "try again" teaches the user the button never helps.
  it('is absent for a reason nocx cannot advise on', () => {
    expect(
      integrationMessage(fact({ status: 'lost', reason: 'channel-lost' }))!.fix,
    ).toBeUndefined()
    expect(integrationMessage(fact({ reason: 'remote-command' }))!.fix).toBeUndefined()
    expect(integrationMessage(fact({ reason: 'unsupported-shell' }))!.fix).toBeUndefined()
  })
})

describe('which shell nocx started', () => {
  it('reads the family off the path the backend sent', () => {
    expect(shellFamily('/bin/zsh')).toBe('zsh')
    expect(shellFamily('/opt/homebrew/bin/bash')).toBe('bash')
    expect(shellFamily('/usr/local/bin/fish')).toBe('other')
    expect(shellFamily('')).toBe('other')
  })

  // A login shell is conventionally argv[0]-prefixed with a dash. The wire
  // carries a path, but a backend that ever sends "-zsh" must not be read as
  // an unknown shell and lose the user their fix.
  it('sees through the login-shell dash', () => {
    expect(shellFamily('-zsh')).toBe('zsh')
    expect(shellFamily('-bash')).toBe('bash')
  })

  // "bashful" is not bash. A prefix match would put ~/.bashrc in front of a
  // user who has never had one.
  it('does not match a shell whose name merely starts the same way', () => {
    expect(shellFamily('/bin/bashful')).toBe('other')
    expect(shellFamily('/bin/zshx')).toBe('other')
  })
})

describe('the observation, as one sentence with one owner', () => {
  // Every surface that shows the observation reads this one function, so
  // none of them can claim it more strongly than another (AD-8).
  it('labels itself a guess and quotes only the process name', () => {
    const s = observationSentence(fact({ detail: { observedProcess: 'some-tui' } }))!
    expect(s.toLowerCase()).toContain('guess')
    expect(s).toContain('some-tui')
  })

  it('is nothing at all when the backend observed nothing', () => {
    expect(observationSentence(fact())).toBeNull()
  })

  // nocx-aimo, measured by the owner: `zsh (kiro-cli-te` on screen, a word
  // stopped mid-syllable. It is p_comm's fixed width — the same width that
  // makes the value safe to show at all — so the sentence marks the elision
  // and says why, instead of quoting the fragment as a whole name.
  it('marks a name that fills the process table field as possibly short', () => {
    const s = observationSentence(fact({ detail: { observedProcess: 'zsh (kiro-cli-te' } }))!
    expect(s).toContain('"zsh (kiro-cli-te…"')
    expect(s).toContain('cut short')
    expect(s).toContain('16')
  })

  // The hedge is for the names that need it. A name plainly inside the
  // field is quoted as itself — hedging every observation would teach the
  // reader that the hedge means nothing.
  it('leaves a name that fits alone', () => {
    const s = observationSentence(fact({ detail: { observedProcess: 'some-tui' } }))!
    expect(s).toContain('"some-tui"')
    expect(s).not.toContain('…')
    expect(s).not.toContain('cut short')
  })

  // The field is bytes and so is its truncation: a name of sixteen
  // CHARACTERS that is more than sixteen bytes never reached the renderer
  // whole either.
  it('counts the field in bytes, the way the kernel does', () => {
    const wide = 'ααααααααααααααα' // 15 characters, 30 bytes
    const s = observationSentence(fact({ detail: { observedProcess: wide } }))!
    expect(s).toContain('cut short')
  })
})

describe('the explanation the product carries', () => {
  // nocx-qs68: the explanation ships in the build. A link needs the network,
  // a system browser and a URL that survives a rename; the shipped app needs
  // none of them.
  it('is prose the app can show with no network at all', () => {
    expect(INTEGRATION_EXPLANATION.length).toBeGreaterThan(0)
    for (const para of INTEGRATION_EXPLANATION) expect(para.length).toBeGreaterThan(0)
  })

  it('links nowhere — there is no URL left to rot', () => {
    for (const para of INTEGRATION_EXPLANATION) expect(para).not.toContain('http')
  })
})

describe('the subscription seam', () => {
  const dispatcherWith = (capture: { handler?: (params: unknown) => void }): Dispatcher =>
    ({
      subscribe: (_method: string, h: (params: unknown) => void) => {
        capture.handler = h
        return () => undefined
      },
    }) as unknown as Dispatcher

  it('delivers a well-formed fact', () => {
    const capture: { handler?: (params: unknown) => void } = {}
    const seen: SessionIntegrationChanged[] = []
    subscribeIntegrationChanged(dispatcherWith(capture), (f) => seen.push(f))
    capture.handler!(fact())
    expect(seen).toHaveLength(1)
    expect(seen[0].reason).toBe('handshake-timeout')
  })

  // The unsolicited-notification defect class: nothing correlates this
  // frame and nothing checks its shape at the call site, so the boundary
  // does — exactly like files.changed and lifecycle.changed.
  it('drops a payload that is not a fact', () => {
    const capture: { handler?: (params: unknown) => void } = {}
    const seen: SessionIntegrationChanged[] = []
    subscribeIntegrationChanged(dispatcherWith(capture), (f) => seen.push(f))
    capture.handler!(null)
    capture.handler!({ sessionId: 's1' })
    capture.handler!({ shell: '/bin/bash' })
    expect(seen).toHaveLength(0)
  })
})

describe('the shells the user has silenced', () => {
  const memoryStorage = () => {
    const map = new Map<string, string>()
    return {
      getItem: (k: string) => map.get(k) ?? null,
      setItem: (k: string, v: string) => void map.set(k, v),
    }
  }

  const KEY = 'nocx.integration.seen.v1'

  it('has nothing to say about a shell until the user says it', () => {
    const store = new IntegrationSilenceStore(memoryStorage())
    expect(store.isSilenced('/bin/bash')).toBe(false)
  })

  // Per shell, not global: a user who has accepted that their login shell is
  // not integrated has said nothing about the next host they connect to. And
  // every reason for that shell, because the user answered about the shell
  // rather than about one way it failed.
  it('silences every reason for the shell the user silenced, and only that shell', () => {
    const store = new IntegrationSilenceStore(memoryStorage())
    store.silenceShell('/bin/bash')
    expect(store.isSilenced('/bin/bash')).toBe(true)
    expect(store.isSilenced('/bin/zsh')).toBe(false)
  })

  it('survives a restart — the record is what the next run reads', () => {
    const storage = memoryStorage()
    new IntegrationSilenceStore(storage).silenceShell('/bin/bash')
    expect(new IntegrationSilenceStore(storage).isSilenced('/bin/bash')).toBe(true)
  })

  // nocx-wfxz. The record used to hold a line per card DRAWN as well as per
  // shell silenced, and those lines are still in the storage of everyone who
  // ran that build. Drawing a card is not a choice the user made, so those
  // lines must not silence anything — while the one line that IS a choice
  // goes on being honoured.
  it('reads a card that an older build merely drew as nothing at all', () => {
    const storage = memoryStorage()
    storage.setItem(KEY, JSON.stringify(['/bin/bash handshake-timeout']))
    expect(new IntegrationSilenceStore(storage).isSilenced('/bin/bash')).toBe(false)
  })

  it('still honours a silence the user chose on an older build', () => {
    const storage = memoryStorage()
    storage.setItem(KEY, JSON.stringify(['/bin/zsh handshake-timeout', '/bin/zsh *']))
    expect(new IntegrationSilenceStore(storage).isSilenced('/bin/zsh')).toBe(true)
  })

  // A shell path can contain a space, and the record's format puts the
  // reason after one. Silencing `/opt/my shell/bash` must not be read back as
  // a silence of `/opt/my`.
  it('reads back a shell whose path has a space in it', () => {
    const storage = memoryStorage()
    const store = new IntegrationSilenceStore(storage)
    store.silenceShell('/opt/my shell/bash')
    expect(new IntegrationSilenceStore(storage).isSilenced('/opt/my shell/bash')).toBe(true)
    expect(new IntegrationSilenceStore(storage).isSilenced('/opt/my')).toBe(false)
  })

  // The failure paths. Showing a card the user silenced is a nuisance; never
  // showing one is the defect, so every storage failure degrades towards
  // showing.
  it('shows the card when the record is corrupt', () => {
    const storage = memoryStorage()
    storage.setItem(KEY, '{not json')
    expect(new IntegrationSilenceStore(storage).isSilenced('/bin/bash')).toBe(false)
  })

  it('shows the card when the record is the wrong shape', () => {
    const storage = memoryStorage()
    storage.setItem(KEY, '{"seen":true}')
    expect(new IntegrationSilenceStore(storage).isSilenced('/bin/bash')).toBe(false)
  })

  it('shows the card when there is no storage at all', () => {
    const store = new IntegrationSilenceStore(null)
    store.silenceShell('/bin/bash')
    expect(store.isSilenced('/bin/bash')).toBe(false)
  })

  it('shows the card when writing is denied, rather than throwing at the user', () => {
    const denied = {
      getItem: () => null,
      setItem: vi.fn(() => {
        throw new Error('QuotaExceededError')
      }),
    }
    const store = new IntegrationSilenceStore(denied)
    expect(() => store.silenceShell('/bin/bash')).not.toThrow()
    expect(store.isSilenced('/bin/bash')).toBe(false)
  })
})

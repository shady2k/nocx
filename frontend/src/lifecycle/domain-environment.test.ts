// The domain-scoped environment projection (bead nocx-u7uh.11), driven with
// synthesized facts — the backend half of child-domain establishment does
// not need to exist to prove the renderer does the right thing with the
// facts it is handed (ADR-0024 decision 7: the renderer validates legal
// application transitions; it can construct no authority of its own).
//
// Protocol §9 is the contract under test: a lane holds a stack of domains;
// the top is the ACTIVE domain when the lane lifecycle names one. Entering
// a nested environment suspends the parent, restoring it takes an
// authenticated activation, and events from a suspended or closed domain
// are rejected against the active lane. The projections (cwd, host, title)
// follow the active domain: a fresh child starts blank and is populated by
// its OWN reports, the parent's values return only on the parent's
// authenticated activation, and a lane gap (nobody owns the lane) is blank —
// showing the parent's values there would name an identity that is not
// taking the keystrokes.
import { describe, expect, it, vi } from 'vitest'
import { LifecycleKernel, type LifecycleFact } from './state'
import { DomainEnvironmentProjection } from './domain-environment'

function promptReady(domain: string, epoch: number): LifecycleFact {
  return { lane: 'lane-1', lifecycle: 'prompt_ready', domain, epoch }
}

function nativeFact(): LifecycleFact {
  return { lane: 'lane-1', lifecycle: 'native' }
}

/** A completed-attempt fact: the shell's authenticated same-domain
 *  completion for `attemptId` in `domain`. */
function completed(domain: string, epoch: number, attemptId: string): LifecycleFact {
  return {
    lane: 'lane-1',
    lifecycle: 'running',
    domain,
    epoch,
    attempt: {
      id: attemptId,
      state: 'completed',
      exitCode: 0,
      fence: 'a'.repeat(64),
      completedAt: '2026-08-09T00:00:00Z',
    },
  }
}

/** A kernel + projection wired the way terminal-content wires them: the
 *  projection subscribes to the kernel, the kernel is fed facts, and the
 *  change callback records every time the visible environment changed. */
function setup(initial = { cwd: '/home/user', host: '', isLocal: true }) {
  const kernel = new LifecycleKernel()
  const onChange = vi.fn()
  const env = new DomainEnvironmentProjection(kernel, onChange)
  env.seedLane({
    cwd: initial.cwd,
    cwdVerified: false,
    host: initial.host,
    user: '',
    isLocal: initial.isLocal,
    programTitle: '',
  })
  env.attach()
  return { kernel, env, onChange }
}

describe('the environment projection follows the ACTIVE domain (protocol §9)', () => {
  it('a conventional terminal keeps the lane tier: reports land there while no domain is live', () => {
    const { env, onChange } = setup()
    expect(env.view()).toMatchObject({ cwd: '/home/user', cwdVerified: false, isLocal: true })

    // No facts yet: an OSC 7 report belongs to the lane tier and shows.
    env.recordCwd('/tmp')
    expect(env.view()).toMatchObject({ cwd: '/tmp', cwdVerified: true })
    expect(onChange).toHaveBeenCalled()
  })

  it('the root domain is seeded from the lane at establishment, so integration takes over seamlessly', () => {
    const { kernel, env } = setup()
    kernel.applyFact(promptReady('d1', 1))
    expect(env.view()).toMatchObject({ cwd: '/home/user', host: '', isLocal: true })
  })

  it('an ssh session seeds the lane with the session binding, and the root inherits it', () => {
    const { kernel, env } = setup({ cwd: '', host: 'srv-01', isLocal: false })
    kernel.applyFact(promptReady('d1', 1))
    expect(env.view()).toMatchObject({ cwd: '', host: 'srv-01', isLocal: false })
  })
})

describe('nested domains — the renderer half (ADR-0024 §6, protocol §9)', () => {
  it('entering a child blanks the projections; the child is populated only by its own reports', () => {
    const { kernel, env } = setup()
    // Root establishes and reports its own cwd and title.
    kernel.applyFact(promptReady('d1', 1))
    env.recordCwd('/home/user')
    env.recordTitle('bash')
    expect(env.view()).toMatchObject({ cwd: '/home/user', programTitle: 'bash' })

    // The parent suspends: the lane has no active domain. The gap is BLANK —
    // never the parent's values, which would name an identity that is not
    // taking the keystrokes.
    kernel.applyFact(nativeFact())
    expect(env.view()).toMatchObject({ cwd: '', host: '', programTitle: '' })

    // The child establishes: a fresh domain starts blank — a parsed command
    // line never populates an authenticated domain's identity.
    kernel.applyFact(promptReady('d2', 2))
    expect(env.view()).toMatchObject({ cwd: '', host: '', programTitle: '' })

    // The child's OWN reports populate the child's record.
    env.recordCwd('/root')
    env.recordTitle('sudo')
    expect(env.view()).toMatchObject({ cwd: '/root', programTitle: 'sudo' })

    // A hostile report while the child is active can touch the child's
    // record, never the parent's.
    env.recordCwd('/etc')
    expect(env.view().cwd).toBe('/etc')
  })

  it("the parent's values return only on the parent's authenticated activation — never on the child's close alone", () => {
    const { kernel, env } = setup()
    kernel.applyFact(promptReady('d1', 1))
    env.recordCwd('/home/user')
    env.recordTitle('bash')

    // Into the child and back out.
    kernel.applyFact(nativeFact())
    kernel.applyFact(promptReady('d2', 2))
    env.recordCwd('/root')
    expect(env.view()).toMatchObject({ cwd: '/root' })

    // The child closes: the lane has no active domain (a suspended parent
    // below does NOT auto-activate — §9). The projection is blank, not the
    // child's stale values and not the parent's.
    kernel.applyFact(nativeFact())
    expect(env.view()).toMatchObject({ cwd: '', host: '', programTitle: '' })

    // The parent's authenticated activation restores the parent's own
    // record — the values it reported while it was active.
    kernel.applyFact(promptReady('d1', 1))
    expect(env.view()).toMatchObject({ cwd: '/home/user', programTitle: 'bash' })
  })

  it('a stale child fact arriving after the parent was restored is rejected, and the view does not move', () => {
    const { kernel, env } = setup()
    kernel.applyFact(promptReady('d1', 1))
    env.recordCwd('/home/user')
    kernel.applyFact(nativeFact())
    kernel.applyFact(promptReady('d2', 2))
    env.recordCwd('/root')
    kernel.applyFact(nativeFact())
    kernel.applyFact(promptReady('d1', 1))
    expect(env.view()).toMatchObject({ cwd: '/home/user' })

    // The dead child's fact arrives late. The kernel rejects it (the child
    // was closed when the parent re-activated; epochs are never resumed),
    // so the projection never even sees a state change.
    const before = env.view()
    kernel.applyFact(promptReady('d2', 2))
    expect(kernel.domainStack.map((d) => d.id)).toEqual(['d1'])
    expect(env.view()).toBe(before)
    expect(env.view()).toMatchObject({ cwd: '/home/user' })
  })

  it("an attempt cannot complete across domains: a completion for the parent's attempt is rejected while the child holds the lane", () => {
    const { kernel, env } = setup()
    kernel.applyFact(promptReady('d1', 1))
    // The app submits `ssh host`; the parent's attempt opens (running).
    kernel.applyFact({
      lane: 'lane-1',
      lifecycle: 'running',
      domain: 'd1',
      epoch: 1,
      attempt: { id: 'att-1', state: 'open', origin: 'app', command: 'ssh host' },
    })
    // The parent suspends and the child establishes.
    kernel.applyFact(nativeFact())
    kernel.applyFact(promptReady('d2', 2))
    // The child cannot complete the parent's attempt — the attempt belongs
    // to exactly one domain and cannot cross an activation boundary (§7).
    const before = kernel.state
    kernel.applyFact(completed('d2', 2, 'att-1'))
    expect(kernel.state).toBe(before)
    // The child's own completion (its own attempt id) is fine.
    kernel.applyFact(completed('d2', 2, 'att-child'))
    expect(kernel.attempt('att-child')?.state).toBe('completed')
    expect(env.view().cwd).toBe('')
  })

  it('a lost lane clears every record, folding the last view into the lane tier so the conventional terminal continues where the integrated one left off', () => {
    const { kernel, env } = setup()
    kernel.applyFact(promptReady('d1', 1))
    env.recordCwd('/home/user')
    env.recordTitle('bash')
    // Loss kills the domain (§12): the lane falls conventional. The lane
    // tier inherits the last view — the terminal keeps showing where it
    // was, exactly as the pre-projection fields did through loss — and the
    // next OSC 7 (no active domain) updates the lane from there.
    kernel.applyFact({ lane: 'lane-1', lifecycle: 'lost' })
    expect(env.view()).toMatchObject({ cwd: '/home/user', programTitle: 'bash' })
    env.recordCwd('/var')
    expect(env.view()).toMatchObject({ cwd: '/var' })
    // A new establishment is a new epoch — never a resumed one — and its
    // records start fresh from the (now current) lane tier, which still
    // carries the folded title until the new shell reports its own.
    kernel.applyFact(promptReady('d3', 3))
    expect(env.view()).toMatchObject({ cwd: '/var', programTitle: 'bash' })
  })

  // nocx-ax79: inside a hand-typed ssh nothing said which machine the next
  // command would run on. The cwd chip read home/pi, indistinguishable from
  // a local /home/pi, and the answer lived only in the user's memory. The
  // destination now rides the published fact (ADR-0025's three fields, and
  // nothing the user typed), so the projection can state it without
  // inferring anything from the stream (AD-6).
  it('a child domain states the destination its fact carries, and the parent states none', () => {
    const { kernel, env } = setup({ cwd: '/home/me', host: '', isLocal: true })
    kernel.applyFact(promptReady('d1', 1))
    expect(env.view()).toMatchObject({ host: '', user: '', isLocal: true })

    kernel.applyFact(nativeFact()) // the parent suspends for the handshake
    kernel.applyFact({
      lane: 'lane-1',
      lifecycle: 'prompt_ready',
      domain: 'd2',
      epoch: 2,
      destination: { host: '192.168.0.93', user: 'pi', port: 22 },
    })
    expect(env.view()).toMatchObject({ host: '192.168.0.93', user: 'pi' })
    // An authenticated ssh child is not the local machine, whatever the
    // session started as — the flag may only ever get more conservative.
    expect(env.view().isLocal).toBe(false)
    // And nothing was invented about where it is: a child is a place with
    // no directory until it says otherwise.
    expect(env.view().cwd).toBe('')
  })

  it('the destination disappears in the same instant the domain does', () => {
    const { kernel, env } = setup({ cwd: '/home/me', host: '', isLocal: true })
    kernel.applyFact(promptReady('d1', 1))
    kernel.applyFact(nativeFact())
    kernel.applyFact({
      lane: 'lane-1',
      lifecycle: 'prompt_ready',
      domain: 'd2',
      epoch: 2,
      destination: { host: 'far', user: 'pi' },
    })
    expect(env.view().host).toBe('far')

    // The handover interval owns nobody: naming a destination over a gap
    // would name a machine that is not taking the keystrokes.
    kernel.applyFact(nativeFact())
    expect(env.view()).toMatchObject({ host: '', user: '' })

    // The parent reclaims the lane and is local again — its own record, not
    // a cleared child's.
    kernel.applyFact(promptReady('d1', 1))
    expect(env.view()).toMatchObject({ host: '', user: '', cwd: '/home/me', isLocal: true })
  })
})

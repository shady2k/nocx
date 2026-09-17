import { describe, it, expect } from 'vitest'
import {
  deriveActions,
  shellStateFromLifecycle,
  type ActionFacts,
  type DesiredMode,
  type ObservedDelivery,
  type HelperConsent,
} from './capability'
import { LifecycleKernel } from './lifecycle/state'

const facts = (over: Partial<ActionFacts> = {}): ActionFacts => ({
  shellState: 'unsupported',
  presentation: 'terminal',
  observedDelivery: 'none',
  authorized: false,
  eligible: false,
  ...over,
})

describe('three axes (nocx-atyf.1)', () => {
  it('a shell emitting markers while the user deliberately sits in terminal input — the combination the old single axis could not express', () => {
    // Old model: this was 'enhanced-input', which conflated "evidence
    // exists but nocx does not own the prompt" with "command is running"
    // and "alt-screen program owns the pane". In the new model these are
    // separate axes: the shell IS integrated AND the presentation IS
    // terminal — the user chose it.
    // The kernel says the domain is live at a ready prompt — the shell IS
    // integrated (ADR-0024 §6; the old `trusted` boolean is deleted).
    const k = new LifecycleKernel()
    k.applyFact({ lane: 'l', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 })
    expect(shellStateFromLifecycle(k.state, k.domainStack)).toBe('integrated')

    // With authorisation and eligibility resolved, the user should be
    // offered a way back to the editor.
    const actions = deriveActions(
      facts({
        shellState: 'integrated',
        presentation: 'terminal',
        observedDelivery: 'installed-script',
        authorized: true,
        eligible: true,
      }),
    )
    expect(actions).toHaveLength(1)
    expect(actions[0]).toEqual({ kind: 'enable-editor', label: 'Enable command editor' })
  })

  it('returns no actions when prerequisites are absent — never disabled-then-rejected', () => {
    // Not authorized: action is ABSENT, not disabled.
    expect(
      deriveActions(
        facts({
          shellState: 'unsupported',
          presentation: 'terminal',
          authorized: false,
          eligible: true,
        }),
      ),
    ).toHaveLength(0)

    // Not technically eligible: action is ABSENT.
    expect(
      deriveActions(
        facts({
          shellState: 'unsupported',
          presentation: 'terminal',
          authorized: true,
          eligible: false,
        }),
      ),
    ).toHaveLength(0)

    // Both absent: still absent.
    expect(
      deriveActions(facts({ shellState: 'unsupported', authorized: false, eligible: false })),
    ).toHaveLength(0)
  })

  it('the healthy state — integrated + editor — shows nothing', () => {
    expect(
      deriveActions(
        facts({
          shellState: 'integrated',
          presentation: 'editor',
          observedDelivery: 'installed-script',
          authorized: true,
          eligible: true,
        }),
      ),
    ).toHaveLength(0)
  })
})

describe('shellStateFromLifecycle — the kernel is the integration authority (ADR-0024 §6)', () => {
  const promptReady = { lane: 'l', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 } as const

  it('Native with nothing below it is unsupported: no domain ever arrived, a conventional terminal', () => {
    const k = new LifecycleKernel()
    expect(shellStateFromLifecycle(k.state, k.domainStack)).toBe('unsupported')
  })

  it('a live authenticated domain is integrated — at the prompt and while running', () => {
    const k = new LifecycleKernel()
    k.applyFact({ ...promptReady })
    expect(shellStateFromLifecycle(k.state, k.domainStack)).toBe('integrated')
    k.applyFact({
      lane: 'l',
      lifecycle: 'running',
      domain: 'd1',
      epoch: 1,
      attempt: { id: 'a1', state: 'open' },
    })
    expect(shellStateFromLifecycle(k.state, k.domainStack)).toBe('integrated')
  })

  it('lost and desynchronized are lost — neither is live, ownership is revoked', () => {
    const lost = new LifecycleKernel()
    lost.applyFact({ ...promptReady })
    lost.applyFact({ lane: 'l', lifecycle: 'lost' })
    expect(shellStateFromLifecycle(lost.state, lost.domainStack)).toBe('lost')

    const desync = new LifecycleKernel()
    desync.applyFact({ ...promptReady })
    desync.applyFact({ lane: 'l', lifecycle: 'desynchronized', domain: 'd1', epoch: 1 })
    expect(shellStateFromLifecycle(desync.state, desync.domainStack)).toBe('lost')
  })

  // nocx-mlyu: 'native' says three different things, and the pane answered
  // all three as 'a conventional terminal'. A lane hands ownership over
  // twice per nested session — once when the parent suspends for the ssh
  // handshake, once when the child's domain closes — and each time the
  // structured presentation was torn down and the whole buffer shown.
  it('Native with a domain suspended below it is a handover, not a conventional terminal', () => {
    const k = new LifecycleKernel()
    k.applyFact({ ...promptReady }) // the parent integrates
    k.applyFact({ lane: 'l', lifecycle: 'native' }) // it suspends for the ssh handshake
    expect(k.domainStack).toHaveLength(1)
    expect(shellStateFromLifecycle(k.state, k.domainStack)).toBe('handover')
  })

  it('the handover ends when the child claims the lane, and again when the parent takes it back', () => {
    const k = new LifecycleKernel()
    k.applyFact({ ...promptReady })
    k.applyFact({ lane: 'l', lifecycle: 'native' })
    k.applyFact({ lane: 'l', lifecycle: 'prompt_ready', domain: 'd2', epoch: 2 })
    expect(shellStateFromLifecycle(k.state, k.domainStack)).toBe('integrated')

    k.applyFact({ lane: 'l', lifecycle: 'native' }) // the child's domain closed
    expect(shellStateFromLifecycle(k.state, k.domainStack)).toBe('handover')

    k.applyFact({ ...promptReady }) // the parent reclaims the lane
    expect(shellStateFromLifecycle(k.state, k.domainStack)).toBe('integrated')
  })

  it('a lane that fell to Lost is lost, never a handover — its stack is dead, not suspended', () => {
    const k = new LifecycleKernel()
    k.applyFact({ ...promptReady })
    k.applyFact({ lane: 'l', lifecycle: 'lost' })
    expect(shellStateFromLifecycle(k.state, k.domainStack)).toBe('lost')
  })
})

describe('deriveActions per state', () => {
  const authorizedFacts = (over: Partial<ActionFacts> = {}): ActionFacts =>
    facts({ authorized: true, eligible: true, ...over })

  it('integrated + terminal: offer enable-editor', () => {
    const actions = deriveActions(
      authorizedFacts({ shellState: 'integrated', presentation: 'terminal' }),
    )
    expect(actions).toHaveLength(1)
    expect(actions[0].kind).toBe('enable-editor')
  })

  it('integrated + editor: no actions (healthy)', () => {
    const actions = deriveActions(
      authorizedFacts({ shellState: 'integrated', presentation: 'editor' }),
    )
    expect(actions).toHaveLength(0)
  })

  it('lost: offer restore', () => {
    const actions = deriveActions(authorizedFacts({ shellState: 'lost' }))
    expect(actions).toHaveLength(1)
    expect(actions[0].kind).toBe('restore-editor')
  })
})
describe('three delivery axes, never collapsed (nocx-mlm7 §3.5)', () => {
  it('desired mode, observed delivery and helper consent are distinct types', () => {
    // The compile-time contract: each axis has its own closed union, so no
    // value of one axis is assignable to another. Asserting the unions here
    // pins them at runtime too — a collapsed single string would fail.
    // 'helper' legitimately names a desired mode AND an observed delivery —
    // the axes are distinct TYPES, not distinct vocabularies; the compile
    // check (assigning an ObservedDelivery where a DesiredMode is expected
    // fails) is the real guard, no runtime value collapses them.
    const modes: DesiredMode[] = ['raw', 'script', 'helper']
    const observed: ObservedDelivery[] = ['none', 'bootstrap-script', 'installed-script', 'helper']
    const consents: HelperConsent[] = ['unknown', 'granted', 'denied']
    expect(new Set(modes).size).toBe(3)
    expect(new Set(observed).size).toBe(4)
    expect(new Set(consents).size).toBe(3)
  })

  it('deriveActions reads the axes, never a collapsed value', () => {
    // The presentation axis is part of the facts; deriving actions must
    // not need to reconstruct it from a single policy string. The same
    // shellState yields different actions for different presentations —
    // the property the old integrate offer demonstrated, now carried by
    // the surviving editor-presentation actions.
    const enable = deriveActions(
      facts({
        shellState: 'integrated',
        presentation: 'terminal',
        observedDelivery: 'installed-script',
        authorized: true,
        eligible: true,
      }),
    )
    expect(enable).toHaveLength(1)
    expect(enable[0].kind).toBe('enable-editor')

    const healthy = deriveActions(
      facts({
        shellState: 'integrated',
        presentation: 'editor',
        observedDelivery: 'installed-script',
        authorized: true,
        eligible: true,
      }),
    )
    expect(healthy).toHaveLength(0)
  })
})

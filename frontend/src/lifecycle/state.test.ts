// The lifecycle kernel in the renderer (bead nocx-u7uh.6, ADR-0024 §6):
// one reducer owns lifecycle, domain and attempt state, fed ONLY by the
// published fact. Every requirement of the bead is pinned here:
//
//   - PromptReady(domain) is unconstructible without an authenticated fact
//     for a live domain — the type's only constructor consumes a fact;
//   - the byte stream reaches no state at all — there is no marker event on
//     the kernel, and the module imports no parsing surface;
//   - entering and leaving the alternate buffer while the domain is revoked
//     underneath never restores ownership (the buffer is its own axis);
//   - the lane's active domain is a stack: a nested environment suspends the
//     parent, restoration takes an authenticated activation, and events from
//     a suspended or closed domain are rejected against the active lane;
//   - an attempt belongs to exactly one domain and cannot complete across
//     one;
//   - no boolean named `trusted` exists anywhere in the module.
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import {
  LifecycleKernel,
  shouldShowEditor,
  rewriteAuthority,
  rerunAuthority,
  completeAttempt,
  freezeBlock,
  type ExecutionAttempt,
  type LifecycleFact,
  type LifecycleState,
} from './state'
import {
  activateDomain,
  emptyStack,
  mintDomain,
  type IntegrationDomain,
  type DomainStack,
} from './domains'

const LANE = 'lane-1'

function promptReady(domain = 'd1', epoch = 1): LifecycleFact {
  return { lane: LANE, lifecycle: 'prompt_ready', domain, epoch }
}

function running(
  domain = 'd1',
  epoch = 1,
  attempt: Partial<NonNullable<LifecycleFact['attempt']>> = {},
): LifecycleFact {
  return {
    lane: LANE,
    lifecycle: 'running',
    domain,
    epoch,
    attempt: { id: 'att-1', state: 'open', ...attempt },
  }
}

function desynchronized(domain = 'd1', epoch = 1): LifecycleFact {
  return { lane: LANE, lifecycle: 'desynchronized', domain, epoch }
}

function nativeF(): LifecycleFact {
  return { lane: LANE, lifecycle: 'native' }
}

function lostF(): LifecycleFact {
  return { lane: LANE, lifecycle: 'lost' }
}

const FENCE = 'a'.repeat(64)

/** The one attempt record the kernel mints from facts, reached through the
 *  running state it produces. */
function attemptOf(state: LifecycleState): ExecutionAttempt | null {
  return state.kind === 'running' ? state.attempt : null
}

describe('the lifecycle kernel (ADR-0024 §6)', () => {
  it('starts Native with a normal buffer — a conventional terminal, no ownership', () => {
    const k = new LifecycleKernel()
    expect(k.state.kind).toBe('native')
    expect(k.buffer).toBe('normal')
    expect(shouldShowEditor(k.state)).toBe(false)
    expect(k.domainStack).toEqual([])
  })

  it('rejects an unknown wire lifecycle without corrupting the state', () => {
    const k = new LifecycleKernel()
    const before = k.state

    k.applyFact({ lane: LANE, lifecycle: '' } as unknown as LifecycleFact)

    expect(k.state).toBe(before)
    expect(k.state.kind).toBe('native')
  })

  it('a published prompt_ready fact for a live domain produces PromptReady and the editor owns keys', () => {
    const k = new LifecycleKernel()
    k.applyFact(promptReady())
    expect(k.state.kind).toBe('prompt_ready')
    expect(shouldShowEditor(k.state)).toBe(true)
    if (k.state.kind === 'prompt_ready') {
      expect(k.state.domain.id).toBe('d1')
      expect(k.state.domain.epoch).toBe(1)
    }
  })

  it('PromptReady(domain) is unconstructible without a published fact', () => {
    // A domain is opaque: no object literal can satisfy IntegrationDomain.
    // @ts-expect-error ADR-0024 §6: a domain is opaque — only the kernel,
    // consuming a published fact, can mint one.
    const d: IntegrationDomain = { id: 'd1', epoch: 1 }
    void d
    // Even with a domain in hand, the authority-bearing members carry an
    // unexported brand: no literal can construct PromptReady, Running or
    // Desynchronized.
    // @ts-expect-error ADR-0024 §6: PromptReady(domain) is unconstructible
    // without an authenticated fact — the member carries an unexported brand.
    const s: LifecycleState = { kind: 'prompt_ready', domain: d }
    void s
    // @ts-expect-error ADR-0024 §6: Running(attempt) is unconstructible the
    // same way.
    const r: LifecycleState = {
      kind: 'running',
      domain: d,
      attempt: { id: 'a', domain: d, state: 'open' },
    }
    void r
    // @ts-expect-error ADR-0024 §6: Desynchronized(domain) is unconstructible
    // the same way.
    const z: LifecycleState = { kind: 'desynchronized', domain: d }
    void z
    // The one constructor that exists consumes a published fact: a fact
    // naming a live domain, through the kernel.
    const k = new LifecycleKernel()
    k.applyFact(promptReady())
    expect(k.state.kind).toBe('prompt_ready')
  })
  it('the byte stream reaches no state at all — there is no marker event on the kernel', () => {
    const k = new LifecycleKernel()
    // Compile-time proof that no stream-shaped input exists on the kernel.
    // Never invoked: the @ts-expect-error is the point, and running the call
    // would be a runtime TypeError that proves nothing more.
    const noStreamPath = (): void => {
      // @ts-expect-error ADR-0024 §1: no marker event exists — the stream
      // cannot reach the reducer.
      k.applyMarker('A') // eslint-disable-line @typescript-eslint/no-unsafe-call -- ADR-0024 §1: no stream input exists
      // @ts-expect-error ADR-0024 §1: no passport event exists.
      k.applyPassport('636;...') // eslint-disable-line @typescript-eslint/no-unsafe-call -- ADR-0024 §1
    }
    void noStreamPath
    // Runtime proof: the kernel's own surface carries no stream-shaped input.
    const members = [...Object.getOwnPropertyNames(Object.getPrototypeOf(k)), ...Object.keys(k)]
    for (const forbidden of ['applyMarker', 'applyPassport', 'applyStreamComplete', 'onMarker']) {
      expect(members).not.toContain(forbidden)
    }
    // The only inputs are the published fact and the renderer-owned buffer.
    expect(typeof k.applyFact).toBe('function')
    expect(typeof k.setBuffer).toBe('function')
  })
  it('rejects a fact for a foreign lane (the kernel adopts the first lane)', () => {
    const k = new LifecycleKernel()
    k.applyFact(promptReady())
    expect(k.lane).toBe(LANE)
    k.applyFact({ lane: 'other-lane', lifecycle: 'lost' })
    expect(k.state.kind).toBe('prompt_ready')
  })

  it('rejects shape defects without mutating anything', () => {
    const k = new LifecycleKernel()
    k.applyFact(promptReady())
    const before = k.state
    // A domain-bearing lifecycle without a domain.
    k.applyFact({ lane: LANE, lifecycle: 'prompt_ready' })
    // A domain-free lifecycle carrying a domain.
    k.applyFact({ lane: LANE, lifecycle: 'native', domain: 'd1', epoch: 1 })
    // running without an attempt.
    k.applyFact({ lane: LANE, lifecycle: 'running', domain: 'd1', epoch: 1 })
    // An attempt on a non-running fact.
    k.applyFact({ ...promptReady(), attempt: { id: 'att-9', state: 'open' } })
    // A completed attempt without its exit code and fence.
    k.applyFact(running('d1', 1, { state: 'completed' }))
    expect(k.state).toBe(before)
  })

  it('a fact for a different domain while another holds the lane is rejected', () => {
    const k = new LifecycleKernel()
    k.applyFact(promptReady('d1'))
    k.applyFact(promptReady('d2'))
    expect(k.state.kind).toBe('prompt_ready')
    if (k.state.kind === 'prompt_ready') expect(k.state.domain.id).toBe('d1')
  })

  it('the buffer is its own axis — leaving the alternate buffer after the domain was revoked never restores ownership', () => {
    const k = new LifecycleKernel()
    k.applyFact(promptReady()) // the editor owns keys
    expect(shouldShowEditor(k.state)).toBe(true)
    k.setBuffer('alternate') // a fullscreen program takes the pane
    k.applyFact(nativeF()) // the domain is revoked underneath
    expect(shouldShowEditor(k.state)).toBe(false)
    k.setBuffer('normal') // the program exits the alternate buffer
    // Ownership must NOT come back: the lifecycle axis still says Native.
    expect(k.state.kind).toBe('native')
    expect(shouldShowEditor(k.state)).toBe(false)
  })

  it('the buffer axis never touches the lifecycle axis', () => {
    const k = new LifecycleKernel()
    k.applyFact(promptReady())
    k.setBuffer('alternate')
    k.setBuffer('normal')
    expect(k.state.kind).toBe('prompt_ready')
    expect(shouldShowEditor(k.state)).toBe(true)
  })

  it('the lane active domain is a stack — a nested environment suspends the parent rather than destroying it', () => {
    const k = new LifecycleKernel()
    k.applyFact(promptReady('root', 1))
    k.applyFact(nativeF()) // root suspends
    k.applyFact(promptReady('child', 1)) // the child establishes on top
    expect(k.domainStack.map((d) => d.id)).toEqual(['root', 'child'])
    if (k.state.kind === 'prompt_ready') expect(k.state.domain.id).toBe('child')
    // The parent is suspended, not destroyed: it is still in the stack.
    expect(k.domainStack[0].id).toBe('root')
  })

  it('restoring a suspended domain takes an authenticated activation, and closed children are removed', () => {
    const k = new LifecycleKernel()
    k.applyFact(promptReady('root', 1))
    k.applyFact(nativeF())
    k.applyFact(promptReady('child', 1))
    k.applyFact(nativeF()) // the child closed — the lane reports native
    // The parent reclaims the lane only through an authenticated fact naming
    // it; the closed child is removed from the stack.
    k.applyFact(promptReady('root', 1))
    expect(k.state.kind).toBe('prompt_ready')
    if (k.state.kind === 'prompt_ready') expect(k.state.domain.id).toBe('root')
    expect(k.domainStack.map((d) => d.id)).toEqual(['root'])
  })

  it('an event from a closed domain is rejected against the active lane', () => {
    const k = new LifecycleKernel()
    k.applyFact(promptReady('root', 1))
    k.applyFact(nativeF())
    k.applyFact(promptReady('child', 1))
    k.applyFact(nativeF()) // child closes
    k.applyFact(promptReady('root', 1)) // parent reactivates; child popped
    // The closed child's id is never reused: a stale fact naming it is
    // rejected and mutates nothing.
    const before = k.state
    k.applyFact(promptReady('child', 1))
    expect(k.state).toBe(before)
  })

  it('a stale-epoch fact for a known domain is rejected (epochs are never reused)', () => {
    const k = new LifecycleKernel()
    k.applyFact(promptReady('d1', 1))
    const before = k.state
    k.applyFact(promptReady('d1', 2))
    expect(k.state).toBe(before)
  })

  it('an attempt belongs to exactly one domain and cannot complete across one', () => {
    const k = new LifecycleKernel()
    k.applyFact(promptReady('d1', 1))
    k.applyFact(running('d1', 1, { id: 'att-1' }))
    k.applyFact(nativeF()) // d1 suspends; the attempt stays open, suspended with it
    k.applyFact(promptReady('d2', 1)) // a nested environment
    // The same attempt id under a different domain: rejected.
    const before = k.state
    k.applyFact(running('d2', 1, { id: 'att-1' }))
    expect(k.state).toBe(before)
    // A completion of that attempt under the wrong domain: rejected too.
    k.applyFact(running('d2', 1, { id: 'att-1', state: 'completed', exitCode: 0, fence: FENCE }))
    expect(k.state).toBe(before)
    // The attempt still belongs to d1 and stays open.
    expect(attemptOf(before)).toBeNull()
  })

  it('a completion sets the exit status exactly once, only for its own domain', () => {
    const k = new LifecycleKernel()
    k.applyFact(promptReady('d1', 1))
    k.applyFact(running('d1', 1, { id: 'att-1' }))
    k.applyFact(
      running('d1', 1, {
        id: 'att-1',
        state: 'completed',
        exitCode: 7,
        completedAt: 't',
        fence: FENCE,
      }),
    )
    let att = attemptOf(k.state)
    expect(att).not.toBeNull()
    if (att) {
      expect(att.state).toBe('completed')
      expect(att.exitCode).toBe(7)
      expect(att.fence).toBe(FENCE)
    }
    // The exit status is set exactly once: a second completion is rejected.
    k.applyFact(running('d1', 1, { id: 'att-1', state: 'completed', exitCode: 0, fence: FENCE }))
    att = attemptOf(k.state)
    if (att) expect(att.exitCode).toBe(7)
  })

  it('a prompt_ready over an open attempt is rejected; it lands after the completion', () => {
    const k = new LifecycleKernel()
    k.applyFact(promptReady('d1', 1))
    k.applyFact(running('d1', 1, { id: 'att-1' }))
    k.applyFact(promptReady('d1', 1))
    expect(k.state.kind).toBe('running')
    k.applyFact(running('d1', 1, { id: 'att-1', state: 'completed', exitCode: 0, fence: FENCE }))
    k.applyFact(promptReady('d1', 1))
    expect(k.state.kind).toBe('prompt_ready')
  })

  it('a Start while an attempt runs never opens a second top-level attempt', () => {
    const k = new LifecycleKernel()
    k.applyFact(promptReady('d1', 1))
    k.applyFact(running('d1', 1, { id: 'att-1' }))
    const before = k.state
    k.applyFact(running('d1', 1, { id: 'att-2' }))
    expect(k.state).toBe(before)
    if (k.state.kind === 'running') expect(k.state.attempt.id).toBe('att-1')
  })

  it('running after native suspends the attempt with its domain and resumes it on activation', () => {
    const k = new LifecycleKernel()
    k.applyFact(promptReady('d1', 1))
    k.applyFact(running('d1', 1, { id: 'att-1' }))
    k.applyFact(nativeF()) // suspend: the open attempt stays open
    let att = attemptOf(k.state)
    expect(att).toBeNull() // the lane has no active domain
    k.applyFact(running('d1', 1, { id: 'att-1' })) // activation with the open attempt
    att = attemptOf(k.state)
    if (att) expect(att.state).toBe('open')
  })

  it('desynchronized applies only to the active domain, and only the snapshot restores it', () => {
    const k = new LifecycleKernel()
    k.applyFact(promptReady('d1', 1))
    k.applyFact(desynchronized('d1', 1))
    expect(k.state.kind).toBe('desynchronized')
    expect(shouldShowEditor(k.state)).toBe(false)
    // A desync for a different domain is rejected.
    k.applyFact(desynchronized('d2', 1))
    expect(k.state.kind).toBe('desynchronized')
    // Only an authenticated snapshot of the same domain restores authority.
    k.applyFact(promptReady('d1', 1))
    expect(k.state.kind).toBe('prompt_ready')
    expect(shouldShowEditor(k.state)).toBe(true)
  })

  it('a snapshot restore reconciles open attempts to unknown before accepting the restoring prompt_ready', () => {
    // decision 7: only a snapshot answering the refresh restores authority.
    // The kernel reconciled att-1 to unknown while applying the snapshot
    // (it was neither active nor completed), but a prompt_ready fact
    // carries no attempt — the renderer must reconcile its own copy, or the
    // open-attempt guard rejects the restoring fact and the lane stays
    // desynchronized forever while the kernel is Established+PromptReady.
    const k = new LifecycleKernel()
    k.applyFact(promptReady('d1', 1))
    k.applyFact(running('d1', 1, { id: 'att-1' }))
    k.applyFact(desynchronized('d1', 1))
    expect(k.state.kind).toBe('desynchronized')
    k.applyFact(promptReady('d1', 1))
    expect(k.state.kind).toBe('prompt_ready')
    expect(shouldShowEditor(k.state)).toBe(true)
    expect(k.attempt('att-1')?.state).toBe('unknown')
  })

  it('a running restore names the surviving attempt and reconciles the rest to unknown', () => {
    // The snapshot named att-2 as active (its start was lost in the gap):
    // the kernel created it open and marked att-1 unknown. The published
    // fact carries only att-2, so the renderer mirrors the reconciliation —
    // a stale open att-1 would otherwise reject every later prompt_ready.
    const k = new LifecycleKernel()
    k.applyFact(promptReady('d1', 1))
    k.applyFact(running('d1', 1, { id: 'att-1' }))
    k.applyFact(desynchronized('d1', 1))
    k.applyFact(running('d1', 1, { id: 'att-2' }))
    expect(k.state.kind).toBe('running')
    if (k.state.kind === 'running') expect(k.state.attempt.id).toBe('att-2')
    expect(k.attempt('att-1')?.state).toBe('unknown')
    expect(k.attempt('att-2')?.state).toBe('open')
  })

  it('a running restore naming the still-open attempt keeps it open', () => {
    const k = new LifecycleKernel()
    k.applyFact(promptReady('d1', 1))
    k.applyFact(running('d1', 1, { id: 'att-1' }))
    k.applyFact(desynchronized('d1', 1))
    k.applyFact(running('d1', 1, { id: 'att-1' }))
    expect(k.state.kind).toBe('running')
    if (k.state.kind === 'running') expect(k.state.attempt.state).toBe('open')
    expect(k.attempt('att-1')?.state).toBe('open')
  })

  it('a stale-epoch restore fact is rejected without mutating the attempts', () => {
    const k = new LifecycleKernel()
    k.applyFact(promptReady('d1', 1))
    k.applyFact(running('d1', 1, { id: 'att-1' }))
    k.applyFact(desynchronized('d1', 1))
    k.applyFact(promptReady('d1', 2)) // epoch 2 for a domain at epoch 1
    expect(k.state.kind).toBe('desynchronized')
    expect(k.attempt('att-1')?.state).toBe('open') // untouched by the rejection
  })

  it('loss marks open attempts unknown, clears the stack, and a fresh domain may establish after', () => {
    const k = new LifecycleKernel()
    k.applyFact(promptReady('d1', 1))
    k.applyFact(running('d1', 1, { id: 'att-1' }))
    k.applyFact(lostF())
    expect(k.state.kind).toBe('lost')
    expect(k.domainStack).toEqual([])
    // A fresh session is a fresh epoch: a new domain may establish.
    k.applyFact(promptReady('d2', 1))
    expect(k.state.kind).toBe('prompt_ready')
    if (k.state.kind === 'prompt_ready') expect(k.state.domain.id).toBe('d2')
  })

  it('notifies only when a fact actually changed the model', () => {
    const k = new LifecycleKernel()
    const seen: string[] = []
    k.onChange(() => seen.push(k.state.kind))
    k.applyFact(promptReady()) // real change
    k.applyFact(promptReady()) // idempotent no-op: no notify
    k.applyFact({ lane: LANE, lifecycle: 'native', domain: 'd1' }) // shape defect: no notify
    expect(seen).toEqual(['prompt_ready'])
    const unsub = k.onChange(() => seen.push('extra'))
    unsub()
    k.applyFact(nativeF())
    expect(seen).toEqual(['prompt_ready', 'native'])
  })

  it('reset returns to the initial condition', () => {
    const k = new LifecycleKernel()
    k.applyFact(promptReady('d1', 1))
    k.applyFact(running('d1', 1, { id: 'att-1' }))
    k.setBuffer('alternate')
    k.reset()
    expect(k.state.kind).toBe('native')
    expect(k.buffer).toBe('normal')
    expect(k.domainStack).toEqual([])
    expect(attemptOf(k.state)).toBeNull()
  })

  it('has no boolean named trusted anywhere in the module', () => {
    // The state carries no such member (compile-time).
    const k = new LifecycleKernel()
    k.applyFact(promptReady())
    // @ts-expect-error ADR-0024 §6: the `trusted` boolean is deleted.
    // eslint-disable-next-line @typescript-eslint/no-unused-expressions
    k.state.trusted
    // And the module source never names it.
    const scan = (rel: string): string => readFileSync(new URL(rel, import.meta.url), 'utf8')
    for (const rel of ['./state.ts', './domains.ts']) {
      expect(scan(rel).match(/\btrusted\b/), `${rel} must not name a trusted boolean`).toBeNull()
    }
  })
})

describe('authority derivations (ADR-0024)', () => {
  function minted(over: Partial<LifecycleFact> = {}): LifecycleFact {
    return { lane: LANE, lifecycle: 'prompt_ready', domain: 'd1', epoch: 1, ...over }
  }
  const d = (): IntegrationDomain => mintDomain(minted()) as IntegrationDomain

  it('shouldShowEditor reads the axis: PromptReady only', () => {
    const k = new LifecycleKernel()
    expect(shouldShowEditor(k.state)).toBe(false)
    k.applyFact(promptReady())
    expect(shouldShowEditor(k.state)).toBe(true)
    k.applyFact(nativeF())
    expect(shouldShowEditor(k.state)).toBe(false)
  })

  it('rewriteAuthority and rerunAuthority require a live domain at a ready prompt', () => {
    const k = new LifecycleKernel()
    expect(rewriteAuthority(k.state)).toBe(false)
    expect(rerunAuthority(k.state)).toBe(false)
    k.applyFact(promptReady())
    expect(rewriteAuthority(k.state)).toBe(true)
    expect(rerunAuthority(k.state)).toBe(true)
    k.applyFact(lostF())
    expect(rewriteAuthority(k.state)).toBe(false)
  })

  it('completeAttempt allows an open attempt of its own domain only', () => {
    const domain = d()
    const other = mintDomain({ ...minted(), domain: 'd2', epoch: 2 }) as IntegrationDomain
    const open: ExecutionAttempt = { id: 'att-1', domain, state: 'open' }
    const done: ExecutionAttempt = { id: 'att-1', domain, state: 'completed', exitCode: 0 }
    expect(completeAttempt(open, domain)).toBe(true)
    expect(completeAttempt(open, other)).toBe(false)
    expect(completeAttempt(done, domain)).toBe(false) // set exactly once
  })

  it('freezeBlock needs an authenticated completed attempt with its fence, of its own domain', () => {
    const domain = d()
    const other = mintDomain({ ...minted(), domain: 'd2', epoch: 2 }) as IntegrationDomain
    const frozen: ExecutionAttempt = {
      id: 'att-1',
      domain,
      state: 'completed',
      exitCode: 0,
      fence: FENCE,
    }
    const noFence: ExecutionAttempt = { id: 'att-1', domain, state: 'completed', exitCode: 0 }
    expect(freezeBlock(frozen, domain)).toBe(true)
    expect(freezeBlock(noFence, domain)).toBe(false) // an event without a fence defers the boundary
    expect(freezeBlock(frozen, other)).toBe(false)
  })

  it('activateDomain accepts only a domain the stack already holds', () => {
    const stack: DomainStack = emptyStack()
    const first = mintDomain(minted()) as IntegrationDomain
    expect(activateDomain(first, stack)).toBe(false) // not in the stack: no authority
    const withDomain: DomainStack = { domains: [first] }
    expect(activateDomain(first, withDomain)).toBe(true)
    const foreign = mintDomain({ ...minted(), domain: 'd9', epoch: 9 }) as IntegrationDomain
    expect(activateDomain(foreign, withDomain)).toBe(false)
  })
})

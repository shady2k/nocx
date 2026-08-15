// The domain stack (ADR-0024 §2, §6; protocol §9): a lane holds a stack of
// domains, bottom (oldest) to top (newest); the top is the active domain
// when the lane lifecycle names one. Transitions are authenticated events —
// a domain is opaque, and the only constructor consumes a published fact.
import { describe, expect, it } from 'vitest'
import type { LifecycleFact } from './state'
import { activateDomain, emptyStack, mintDomain, type IntegrationDomain } from './domains'

function fact(over: Partial<LifecycleFact> = {}): LifecycleFact {
  return { lane: 'lane-1', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1, ...over }
}

describe('the domain stack (ADR-0024 §2, §6)', () => {
  it('mintDomain is the only constructor and it consumes a published fact', () => {
    const d = mintDomain(fact())
    expect(d).not.toBeNull()
    if (d) {
      expect(d.id).toBe('d1')
      expect(d.epoch).toBe(1)
    }
    // A fact that does not name a live domain mints nothing.
    expect(mintDomain(fact({ lifecycle: 'native' }))).toBeNull()
    expect(mintDomain(fact({ domain: '' }))).toBeNull()
    expect(mintDomain(fact({ epoch: 0 }))).toBeNull()
    expect(mintDomain(fact({ epoch: undefined }))).toBeNull()
  })

  it('a domain is opaque — no object literal can construct one', () => {
    // @ts-expect-error ADR-0024 §6: a domain is opaque — only the kernel,
    // consuming a published fact, can mint one.
    const d: IntegrationDomain = { id: 'd1', epoch: 1 }
    void d
    // A spread of a minted domain carries the brand along, so even a
    // clone can only be built from a real domain — the brand is not
    // forgeable from visible fields.
    const minted = mintDomain(fact()) as IntegrationDomain
    const clone: IntegrationDomain = { ...minted }
    void clone
  })

  it('activateDomain accepts only a domain the stack already holds', () => {
    const stack = emptyStack()
    const d = mintDomain(fact()) as IntegrationDomain
    // Not in the stack: nothing a caller can mint may reclaim the lane.
    expect(activateDomain(d, stack)).toBe(false)
    const withDomain = { domains: [d] }
    expect(activateDomain(d, withDomain)).toBe(true)
    // A foreign domain is not a member.
    const foreign = mintDomain(fact({ domain: 'd9', epoch: 9 })) as IntegrationDomain
    expect(activateDomain(foreign, withDomain)).toBe(false)
  })

  it('epochs are part of domain identity — a stale epoch is a different domain', () => {
    const d1 = mintDomain(fact({ epoch: 1 })) as IntegrationDomain
    const d1Again = mintDomain(fact({ epoch: 1 })) as IntegrationDomain
    const d2 = mintDomain(fact({ epoch: 2 })) as IntegrationDomain
    expect(activateDomain(d1, { domains: [d1Again] })).toBe(true)
    expect(activateDomain(d2, { domains: [d1Again] })).toBe(false)
  })
})

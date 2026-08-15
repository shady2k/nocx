// The domain stack (ADR-0024 §2, §6; docs/lifecycle-protocol.md §9).
//
// A lane holds a stack of domains, bottom (oldest) to top (newest); the top
// is the ACTIVE domain when the lane lifecycle names it. Entering a nested
// environment suspends the parent rather than destroying it, restoring it
// takes an authenticated activation rather than a pop of ambient frontend
// state, and events from a suspended or closed domain are rejected against
// the active lane. Transitions are authenticated events — this module never
// mints a domain from a stream sequence; the only constructor consumes a
// published fact (decision 7: the backend authenticates, the renderer
// validates legal application transitions and can construct no authority of
// its own).

import type { LifecycleChanged } from '../generated/lifecycle.changed'

type DomainLifecycleFact = Omit<LifecycleChanged, 'sessionId'>

const integrationDomainBrand = Symbol('integrationDomain')

/** One authenticated shell or helper instance — logical, never an alias for
 *  a transport (ADR-0024 decision 2). Carries an opaque authentication
 *  brand: no object literal can satisfy it, so a domain exists only where
 *  the kernel, consuming a published fact, put one (decision 6 — that is
 *  what makes `PromptReady(domain)` unconstructible without a fact). */
export type IntegrationDomain = {
  readonly id: string
  /** Where this domain IS, when the fact that minted it said so: an ssh
   *  child carries the destination its parent's request named (ADR-0025),
   *  a local domain carries none (nocx-ax79). Descriptive only — the
   *  authority is still the id and the epoch, and a domain minted without
   *  one never acquires it later. */
  readonly destination?: { readonly host: string; readonly user: string }
  /** The generation of the domain — monotonic per kernel instance, never
   *  reused, never resumed. A new establishment is a new domain with a new
   *  epoch, which is how a stale projection is recognised. */
  readonly epoch: number
  readonly [integrationDomainBrand]: true
}

/** A lane's domain stack, bottom → top. The top is the active domain when
 *  the lane lifecycle names one; a suspended parent below it does not
 *  auto-activate — it needs its own authenticated activation (§9). */
export interface DomainStack {
  readonly domains: readonly IntegrationDomain[]
}

export function emptyStack(): DomainStack {
  return { domains: [] }
}

/** The ONLY constructor of an IntegrationDomain: it consumes a published
 *  fact's domain id and epoch. Authentication terminates in the backend;
 *  this records what the kernel concluded, it creates no authority of its
 *  own. A fact that does not name a live domain (no id, no epoch) mints
 *  nothing. */
export function mintDomain(fact: DomainLifecycleFact): IntegrationDomain | null {
  if (
    fact.lifecycle !== 'prompt_ready' &&
    fact.lifecycle !== 'running' &&
    fact.lifecycle !== 'desynchronized'
  )
    return null
  if (typeof fact.domain !== 'string' || fact.domain === '') return null
  if (typeof fact.epoch !== 'number' || fact.epoch < 1) return null
  const d = fact.destination
  const destination =
    d !== undefined && typeof d.host === 'string' && d.host !== ''
      ? { host: d.host, user: typeof d.user === 'string' ? d.user : '' }
      : undefined
  return { id: fact.domain, epoch: fact.epoch, destination, [integrationDomainBrand]: true }
}

/** ADR-0024 §2, §6: the domain stack transitions only on authenticated
 *  events. Activation is the only way a suspended domain returns (§9): the
 *  named domain must already be a member of the stack — established by an
 *  authenticated event, never minted by the caller — and it must be the top,
 *  because the chain is linear and only the top reclaims the lane. A caller
 *  that hands over a fresh candidate (a passport, a stream sequence, a UI
 *  guess) gets false. */
export function activateDomain(domain: IntegrationDomain, stack: DomainStack): boolean {
  return stack.domains.some((d) => d.id === domain.id && d.epoch === domain.epoch)
}

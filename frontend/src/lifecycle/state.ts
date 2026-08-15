// The lifecycle kernel in the renderer (bead nocx-u7uh.6, ADR-0024 §6).
//
// One reducer owns lifecycle, domain and attempt state, fed ONLY by the
// published fact (frontend/src/lifecycle/client.ts → lifecycle.changed).
// It is the single authoritative model; blocks, the ledger, history and the
// editor become projections of it. Two orthogonal axes, and the orthogonality
// is the point:
//
//   Lifecycle: Native | PromptReady(domain) | Running(attempt) |
//              Desynchronized(domain) | Lost
//   Buffer:    Normal | Alternate
//
// The byte stream reaches no state here: there is no event for a stream
// sequence, and the eslint Rule 9 boundary (frontend/eslint.config.js)
// forbids the import that would create one. A `PromptReady(domain)` value is
// unconstructible without an authenticated fact for a live domain — the
// authority-bearing union members carry an unexported brand that only this
// module's fact-consuming transitions can satisfy, and a domain itself is
// opaque (src/lifecycle/domains.ts). The buffer is its own axis so that
// presentation can never restore authority: entering and leaving the
// alternate buffer while the domain was revoked underneath leaves the
// lifecycle axis untouched.
//
// Authentication terminates in the backend (decision 7). This reducer
// validates legal application transitions and can construct no authority of
// its own; invalid or stale facts mutate nothing.

import type { LifecycleChanged } from '../generated/lifecycle.changed'

/** The authenticated lifecycle projection after its transport address has
 *  routed it to one session. sessionId belongs to the shared-WebSocket
 *  envelope; the per-session state machine owns only the fact body. */
export type LifecycleFact = Omit<LifecycleChanged, 'sessionId'>
import {
  activateDomain,
  emptyStack,
  mintDomain,
  type DomainStack,
  type IntegrationDomain,
} from './domains'

/** The buffer axis (ADR-0024 §6): a renderer-owned presentation fact, never
 *  an authority. Deliberately absent from the published fact and from the
 *  kernel's lifecycle axis. */
export type BufferState = 'normal' | 'alternate'

/** One execution attempt. It belongs to exactly one domain and cannot
 *  complete across one (decision 5): every completion is checked against the
 *  attempt's domain, and the exit status is set exactly once, only by an
 *  authenticated same-domain completion. */
export interface ExecutionAttempt {
  readonly id: string
  readonly domain: IntegrationDomain
  /** open: running or awaiting its authenticated start. completed: an
   *  authenticated same-domain completion set the exit status exactly once.
   *  unknown: abandoned (native-mode escape), lost with its domain, or
   *  unrecoverable by an authenticated snapshot. */
  readonly state: 'open' | 'completed' | 'unknown'
  readonly command?: string
  readonly origin?: 'app' | 'shell'
  readonly startedAt?: string
  /** Present exactly when state is completed. */
  readonly exitCode?: number
  readonly completedAt?: string
  /** The completion's render fence (decision 7 carve-out): 64 lowercase hex
   *  chars the shell wrote to the pty after the command's output. A fence
   *  with no authenticated event behind it does nothing at all. Present
   *  exactly when state is completed. */
  readonly fence?: string
}

const authorityBrand = Symbol('authority')

/** The per-lane authority axis (ADR-0024 §6): Native | PromptReady(domain) |
 *  Running(attempt) | Desynchronized(domain) | Lost. The three
 *  authority-bearing members carry an unexported brand — no object literal
 *  can construct one; the only constructors are this module's
 *  fact-consuming transitions, and a domain is itself opaque. */
export type LifecycleState =
  | { readonly kind: 'native' }
  | { readonly kind: 'lost' }
  | {
      readonly kind: 'prompt_ready'
      readonly domain: IntegrationDomain
      readonly [authorityBrand]: true
    }
  | {
      readonly kind: 'running'
      readonly domain: IntegrationDomain
      readonly attempt: ExecutionAttempt
      readonly [authorityBrand]: true
    }
  | {
      readonly kind: 'desynchronized'
      readonly domain: IntegrationDomain
      readonly [authorityBrand]: true
    }

function isAttemptFact(a: LifecycleFact['attempt']): a is NonNullable<LifecycleFact['attempt']> {
  return (
    a !== undefined &&
    typeof a.id === 'string' &&
    a.id !== '' &&
    (a.state === 'open' || a.state === 'completed' || a.state === 'unknown')
  )
}

/** The completion fields ride the attempt exactly when it completed: a
 *  completed attempt must carry its exit status and render fence, and an
 *  open or abandoned one must carry neither. Checked before any transition
 *  mutates, so an invalid fact changes nothing. */
function attemptShapeValid(a: NonNullable<LifecycleFact['attempt']>): boolean {
  if (a.state === 'completed') {
    return typeof a.exitCode === 'number' && typeof a.fence === 'string' && a.fence !== ''
  }
  return a.exitCode === undefined && a.fence === undefined
}

/**
 * The two-axis reducer: one kernel per terminal tab (one lane). Feeds on
 * published facts and renderer-owned buffer events; everything else is
 * rejected or absent.
 */
export class LifecycleKernel {
  private _state: LifecycleState = { kind: 'native' }
  private _buffer: BufferState = 'normal'
  private _lane: string | null = null
  private _stack: DomainStack = emptyStack()
  /** Every attempt this lane has seen, keyed by id. */
  private readonly _attempts = new Map<string, ExecutionAttempt>()
  /** Domain ids that have ended (closed, or lost with their lane). Ids are
   *  never reused, so a fact naming a closed id is stale and rejected. */
  private readonly _closed = new Set<string>()
  /** How many domains of this lane have ENDED. Only ever grows, and it is
   *  deliberately a count rather than a set: the projections need to know
   *  THAT one ended, not which — a block opened at an app submit carries no
   *  domain until an attempt binds it, and the whole point is the case
   *  where no attempt ever arrives (nocx-mlyu). A suspension never touches
   *  it: suspend and close are the two readings of the same 'native' fact,
   *  and only one of them makes an unfinished block unfinishable. */
  private _ended = 0
  private readonly _subs = new Set<(k: LifecycleKernel) => void>()

  get state(): LifecycleState {
    return this._state
  }

  get buffer(): BufferState {
    return this._buffer
  }

  /** The lane the kernel adopted — the first fact's lane. Facts for any
   *  other lane are rejected. */
  get lane(): string | null {
    return this._lane
  }

  /** The lane's domain stack, bottom → top (protocol §9). */
  get domainStack(): readonly IntegrationDomain[] {
    return this._stack.domains
  }

  /** How many domains of this lane have ended. The projections watch it for
   *  change: a domain ending is the moment anything still unfinished under
   *  it became unfinishable, including a block that never reached an
   *  attempt at all. */
  get endedDomains(): number {
    return this._ended
  }

  /** One attempt this lane has seen, by id — read-only kernel state for
   *  the projections (the ledger, history, the block model). The attempt
   *  carries the authority brand through its domain, so a projection can
   *  hand it back to an authority surface; no caller can mint one. */
  attempt(id: string): ExecutionAttempt | undefined {
    return this._attempts.get(id)
  }

  /** Subscribe to state changes; returns the unsubscribe. Fires only when a
   *  fact or buffer event actually changed the model. */
  onChange(cb: (k: LifecycleKernel) => void): () => void {
    this._subs.add(cb)
    return () => {
      this._subs.delete(cb)
    }
  }

  /** Apply one published lifecycle fact. This is the ONLY input to the
   *  authority axis. Invalid or stale facts mutate nothing. */
  applyFact(fact: LifecycleFact): void {
    if (this._lane === null) this._lane = fact.lane
    else if (fact.lane !== this._lane) return

    // Shape: a domain-bearing lifecycle names one with a positive epoch; a
    // domain-free lifecycle carries neither; the attempt rides running only,
    // and its completion fields sit exactly where its state says they do.
    const namesDomain =
      fact.lifecycle === 'prompt_ready' ||
      fact.lifecycle === 'running' ||
      fact.lifecycle === 'desynchronized'
    if (namesDomain) {
      if (
        typeof fact.domain !== 'string' ||
        fact.domain === '' ||
        typeof fact.epoch !== 'number' ||
        fact.epoch < 1
      )
        return
    } else if (fact.domain !== undefined || fact.epoch !== undefined) {
      return
    }
    if (fact.lifecycle === 'running') {
      if (!isAttemptFact(fact.attempt) || !attemptShapeValid(fact.attempt)) return
    } else if (fact.attempt !== undefined) {
      return
    }

    const next = this.transition(fact)
    if (next === null || next === this._state) return
    this._state = next
    this.notify()
  }

  /** The buffer axis is a renderer-owned presentation fact: it moves
   *  independently of the lifecycle and can never restore authority. */
  setBuffer(buffer: BufferState): void {
    if (buffer === this._buffer) return
    this._buffer = buffer
    this.notify()
  }

  /** Return to the initial condition (session exit / reset). Closed domain
   *  ids stay closed — a new session is a new epoch, and a stale fact for a
   *  dead domain stays rejected. */
  reset(): void {
    const changed =
      this._state.kind !== 'native' ||
      this._buffer !== 'normal' ||
      this._stack.domains.length > 0 ||
      this._attempts.size > 0
    this._state = { kind: 'native' }
    this._buffer = 'normal'
    this._stack = emptyStack()
    this._attempts.clear()
    if (changed) this.notify()
  }

  // ── transitions ────────────────────────────────────────────────────────

  /** Returns the next state, `this._state` for an accepted no-op, or null
   *  when the fact is invalid. Every rejection check runs before any
   *  mutation, so an invalid fact changes nothing. */
  private transition(fact: LifecycleFact): LifecycleState | null {
    switch (fact.lifecycle) {
      case 'native': {
        // The active domain suspended or closed (protocol §9): the lane has
        // no active domain. The stack is preserved — a suspended parent does
        // not auto-activate — and an open attempt stays open, suspended with
        // its domain (the renderer cannot tell suspend from close on this
        // fact, and §9 keeps open attempts open on suspend).
        return this._state.kind === 'native' ? this._state : { kind: 'native' }
      }
      case 'lost': {
        // Transport loss (protocol §12): every domain of the lane is dead,
        // open attempts become unknown and never successful, and the lane
        // falls to Lost. A new session gets a fresh epoch — never resumed.
        if (this._state.kind === 'lost') return this._state
        const dead = this._stack.domains
        for (const d of dead) {
          this._closed.add(d.id)
          this._ended++
          this.unknownAttemptsOf(d.id)
        }
        this._stack = emptyStack()
        return { kind: 'lost' }
      }
      case 'prompt_ready': {
        const cur = this._state
        if (cur.kind === 'prompt_ready') {
          if (cur.domain.id === fact.domain) return this._state
          return null // a different domain became active without a native in between
        }
        if (cur.kind === 'running') {
          if (cur.domain.id !== fact.domain) return null
          if (cur.attempt.state !== 'completed') return null // prompt over an open attempt
        }
        if (cur.kind === 'desynchronized' && cur.domain.id !== fact.domain) return null
        if (cur.kind === 'desynchronized') {
          // decision 7: only a snapshot answering nocx's own refresh
          // request restores authority. resolveDomain with mint=false is
          // pure validation here (the domain is already on the stack — the
          // desync fact resolved it), so it runs before any mutation: an
          // invalid restoring fact changes nothing.
          const domain = this.resolveDomain(fact, false)
          if (domain === null) return null
          // The kernel reconciled the domain's open attempts while applying
          // the snapshot — anything it did not name active or completed
          // became unknown there, and a prompt_ready fact carries no
          // attempt (derive publishes only the lane's current attempt).
          // Mirror the reconciliation BEFORE the open-attempt guard, or the
          // restoring fact is rejected as "prompt over an open attempt" and
          // the lane stays desynchronized forever while the kernel is
          // Established+PromptReady.
          this.unknownAttemptsOf(fact.domain!)
          if (this.openAttemptOf(fact.domain!) !== null) return null
          return { kind: 'prompt_ready', domain, [authorityBrand]: true }
        }
        if (this.openAttemptOf(fact.domain!) !== null) return null // prompt over an open attempt
        const domain = this.resolveDomain(fact, true)
        if (domain === null) return null
        return { kind: 'prompt_ready', domain, [authorityBrand]: true }
      }
      case 'running': {
        return this.applyRunning(fact)
      }
      case 'desynchronized': {
        const cur = this._state
        if (cur.kind !== 'prompt_ready' && cur.kind !== 'running') return null
        if (cur.domain.id !== fact.domain) return null // only the active domain desyncs
        const domain = this.resolveDomain(fact, false)
        if (domain === null) return null
        return { kind: 'desynchronized', domain, [authorityBrand]: true }
      }
    }
    return null
  }

  private applyRunning(fact: LifecycleFact): LifecycleState | null {
    const a = fact.attempt!
    const existing = this._attempts.get(a.id)
    // An attempt belongs to exactly one domain: the fact may only carry an
    // attempt this lane knows under the SAME domain.
    if (existing !== undefined && existing.domain.id !== fact.domain) return null

    const cur = this._state
    if (cur.kind === 'running') {
      if (cur.domain.id !== fact.domain) return null // a second top-level attempt
      if (existing === undefined) return null // a Start while an attempt runs: violation
      if (a.state === 'completed') {
        if (existing.state !== 'open') return null // exit status is set exactly once
        if (!completeAttempt(existing, cur.domain)) return null
        const done = this.completedRecord(existing, a)
        if (done === null) return null
        this._attempts.set(done.id, done)
        return { kind: 'running', domain: cur.domain, attempt: done, [authorityBrand]: true }
      }
      if (a.state === 'unknown') {
        if (existing.state !== 'open') return null
        const abandoned = { ...existing, state: 'unknown' as const }
        this._attempts.set(abandoned.id, abandoned)
        return { kind: 'running', domain: cur.domain, attempt: abandoned, [authorityBrand]: true }
      }
      if (existing.state !== 'open') return null // a terminal attempt cannot resume
      return this._state // open while open: the same fact again
    }

    // From a non-running state: a start at the ready prompt, an activation
    // with an open attempt, or a replayed current state the renderer missed
    // (attach / desync snapshot). A fact naming a different domain while
    // another holds the lane is a stale event and is rejected.
    if (cur.kind === 'prompt_ready' || cur.kind === 'desynchronized') {
      if (cur.domain.id !== fact.domain) return null
    }
    const domain = this.resolveDomain(fact, cur.kind !== 'desynchronized')
    if (domain === null) return null

    let attempt: ExecutionAttempt | null = existing ?? null
    if (attempt === null) {
      attempt = this.attemptFromFact(fact, domain)
    } else {
      if (attempt.domain.id !== domain.id) return null
      if (a.state === 'open') {
        if (attempt.state !== 'open') return null
      } else {
        if (attempt.state !== 'open') return null // terminal attempts cannot change state twice
        if (a.state === 'completed') {
          const done = this.completedRecord(attempt, a)
          if (done === null) return null
          attempt = done
        } else {
          attempt = { ...attempt, state: 'unknown' as const }
        }
      }
    }
    if (attempt === null) return null
    if (cur.kind === 'desynchronized') {
      // decision 7: the kernel reconciled the domain's open attempts while
      // applying the snapshot — only the attempt it named active survived
      // as open; every other open attempt became unknown. The published
      // fact carries only the named attempt (derive publishes the lane's
      // current attempt), so mirror the reconciliation here — after the
      // named attempt validated, so an invalid fact mutates nothing. A
      // stale open attempt would otherwise reject every later prompt_ready
      // for the domain and the lane would never leave Running.
      for (const [id, att] of this._attempts) {
        if (att.domain.id === fact.domain && att.state === 'open' && id !== a.id) {
          this._attempts.set(id, { ...att, state: 'unknown' as const })
        }
      }
    }
    this._attempts.set(attempt.id, attempt)
    return { kind: 'running', domain, attempt, [authorityBrand]: true }
  }

  /** Resolve the fact's domain against the lane: a known domain (epoch
   *  checked, closed domains rejected) with closed children removed, or —
   *  when `mint` — a new domain pushed onto the stack (root when empty,
   *  child otherwise; the parent below is suspended). Returns null when the
   *  fact names nothing valid. */
  private resolveDomain(fact: LifecycleFact, mint: boolean): IntegrationDomain | null {
    const id = fact.domain!
    const epoch = fact.epoch!
    if (this._closed.has(id)) return null // stale event for a closed domain
    const idx = this._stack.domains.findIndex((d) => d.id === id)
    if (idx === -1) {
      if (!mint) return null
      const minted = mintDomain(fact)
      if (minted === null) return null
      this._stack = { domains: [...this._stack.domains, minted] }
      return minted
    }
    const known = this._stack.domains[idx]
    if (known.epoch !== epoch) return null // epochs are never reused: stale projection
    if (!activateDomain(known, this._stack)) return null
    // Domains above a re-activated ancestor ended (the chain is linear; only
    // the top reclaims the lane). Pop them; their open attempts become
    // unknown (§9: a closed domain's open attempts are never successful).
    if (idx < this._stack.domains.length - 1) {
      const closed = this._stack.domains.slice(idx + 1)
      this._stack = { domains: this._stack.domains.slice(0, idx + 1) }
      for (const c of closed) {
        this._closed.add(c.id)
        this._ended++
        this.unknownAttemptsOf(c.id)
      }
    }
    return known
  }

  /** Build an attempt record from the fact, bound to the resolved domain.
   *  A completed fact for an unknown attempt is the authenticated
   *  completion itself (its start was missed or replayed); the backend
   *  reported the status, so the record carries it. */
  private attemptFromFact(fact: LifecycleFact, domain: IntegrationDomain): ExecutionAttempt | null {
    const a = fact.attempt!
    if (a.state === 'completed') {
      if (typeof a.exitCode !== 'number' || typeof a.fence !== 'string' || a.fence === '')
        return null
      return {
        id: a.id,
        domain,
        state: 'completed',
        command: a.command,
        origin: a.origin,
        startedAt: a.startedAt,
        exitCode: a.exitCode,
        completedAt: a.completedAt,
        fence: a.fence,
      }
    }
    if (a.exitCode !== undefined || a.fence !== undefined) return null // present exactly when completed
    return {
      id: a.id,
      domain,
      state: a.state,
      command: a.command,
      origin: a.origin,
      startedAt: a.startedAt,
    }
  }

  private completedRecord(
    attempt: ExecutionAttempt,
    a: NonNullable<LifecycleFact['attempt']>,
  ): ExecutionAttempt | null {
    if (typeof a.exitCode !== 'number' || typeof a.fence !== 'string' || a.fence === '') return null
    return {
      ...attempt,
      state: 'completed' as const,
      exitCode: a.exitCode,
      completedAt: a.completedAt,
      fence: a.fence,
    }
  }

  private openAttemptOf(domainId: string): ExecutionAttempt | null {
    for (const att of this._attempts.values()) {
      if (att.domain.id === domainId && att.state === 'open') return att
    }
    return null
  }

  private unknownAttemptsOf(domainId: string): void {
    for (const [id, att] of this._attempts) {
      if (att.domain.id === domainId && att.state === 'open') {
        this._attempts.set(id, { ...att, state: 'unknown' as const })
      }
    }
  }

  private notify(): void {
    for (const cb of this._subs) cb(this)
  }
}

// ── Authority derivations (ADR-0024) ──────────────────────────────────────

/** ADR-0024 §6: the editor owns keys because the lifecycle axis says
 *  PromptReady — not because a second boolean does. The buffer axis gates
 *  presentation only; it can never restore authority, which is why it is a
 *  separate axis. */
export function shouldShowEditor(state: LifecycleState): boolean {
  return state.kind === 'prompt_ready'
}

/** ADR-0024 §1: integration-sensitive command rewriting is enabled only by
 *  a live authenticated domain at a ready prompt — never by a stream latch.
 *  The capability rail's ShellState derivation is rewired to this by the
 *  projection bead. */
export function rewriteAuthority(state: LifecycleState): boolean {
  return state.kind === 'prompt_ready'
}

/** ADR-0024 §1: a re-run is authorized by the attempt/domain, never by a
 *  block the stream forged — it needs the same live prompt the first run
 *  did. */
export function rerunAuthority(state: LifecycleState): boolean {
  return state.kind === 'prompt_ready'
}

/** ADR-0024 §5: an attempt is open from submit or authenticated start until
 *  an authenticated same-domain completion. This is the completion check: an
 *  attempt may complete only under the domain it belongs to, and only while
 *  open — nothing may mark it successful twice, and nothing may assign it an
 *  exit code it did not report. The kernel applies it to every completion
 *  fact; the block projection calls it again at freeze time. */
export function completeAttempt(attempt: ExecutionAttempt, domain: IntegrationDomain): boolean {
  return attempt.domain.id === domain.id && attempt.state === 'open'
}

/** ADR-0024 §7 render ordering: the visual freeze is authorized only by an
 *  authenticated completion for the attempt's own domain that carries the
 *  render fence. A fence alone does nothing; an event without a fence
 *  completes the attempt logically and defers the output boundary. */
export function freezeBlock(attempt: ExecutionAttempt, domain: IntegrationDomain): boolean {
  return (
    attempt.state === 'completed' && attempt.fence !== undefined && attempt.domain.id === domain.id
  )
}

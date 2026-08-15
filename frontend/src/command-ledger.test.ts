// Command ledger (ADR-0008) — the completion projection (ADR-0024, bead
// nocx-u7uh.7). The marker cycle (onMarker), the `trusted` boolean and the
// N6 environment-transition machinery are deleted: no OSC 133 kind may
// populate or complete a record, assign an exit status or persist history.
// These tests pin the app-owned half — open at submit with its start time
// (ADR-0024 §5), records, dispose, resolveID — and the authenticated
// completion half: bindAttempt ties the pending record to the published
// attempt, and complete applies its verdict exactly once (an abandoned
// attempt is `unknown` and never successful).
import { describe, it, expect, beforeEach } from 'vitest'
import { CommandLedger } from './command-ledger'
import { mintDomain, type IntegrationDomain } from './lifecycle/domains'
import type { ExecutionAttempt, LifecycleFact } from './lifecycle/state'

const FACT: LifecycleFact = { lane: 'l', lifecycle: 'prompt_ready', domain: 'd1', epoch: 1 }
const domain = mintDomain(FACT) as IntegrationDomain
const FENCE = 'a'.repeat(64)

function attempt(overrides: Partial<ExecutionAttempt> = {}): ExecutionAttempt {
  return { id: 'att-1', domain, state: 'completed', exitCode: 0, fence: FENCE, ...overrides }
}

// Fake lineOf that returns the number we feed it. The ledger never caches
// the result, so tests call this through the ledger's own API.
const fakeLineOf = (val: number) => () => val

describe('CommandLedger (severed)', () => {
  let ledger: CommandLedger
  beforeEach(() => {
    ledger = new CommandLedger({ now: () => 500 })
  })

  it('starts with no records', () => {
    expect(ledger.records()).toEqual([])
  })

  it('open creates a running record stamped with the app-owned start time', () => {
    const rec = ledger.open('ls', '/', '', fakeLineOf(3))
    expect(rec.id).toBe(1)
    expect(rec.command).toBe('ls')
    expect(rec.cwd).toBe('/')
    expect(rec.host).toBe('')
    // ADR-0024 §5: the app-owned submit is the attempt start — it exists
    // before any bytes that could cause the shell's own start event.
    expect(rec.status).toBe('running')
    expect(rec.startedAt).toBe(500)
    expect(rec.endedAt).toBeNull()
    expect(rec.exitCode).toBeNull()
    expect(rec.disposed).toBe(false)
  })

  it('open assigns incrementing ids', () => {
    expect(ledger.open('a', '/', '', fakeLineOf(1)).id).toBe(1)
    expect(ledger.open('b', '/', '', fakeLineOf(2)).id).toBe(2)
  })

  it('open fails with an empty command', () => {
    expect(() => ledger.open('', '/', '', fakeLineOf(1))).toThrow('command must not be empty')
  })

  it('records() returns all records oldest first, defensively copied', () => {
    ledger.open('first', '/a', '', fakeLineOf(1))
    ledger.open('second', '/b', '', fakeLineOf(2))
    const records = ledger.records()
    expect(records.map((r) => r.command)).toEqual(['first', 'second'])
    expect(records).not.toBe(ledger.records())
    expect(records).toEqual(ledger.records())
  })

  it('dispose marks a record disposed, idempotently', () => {
    const rec = ledger.open('ls', '/', '', fakeLineOf(1))
    expect(rec.disposed).toBe(false)
    ledger.dispose(rec.id)
    expect(rec.disposed).toBe(true)
    ledger.dispose(rec.id) // no throw
    expect(rec.disposed).toBe(true)
  })

  it('dispose of an unknown id is a no-op', () => {
    ledger.dispose(99)
    expect(ledger.records()).toEqual([])
  })

  it('resolveID finds the record or returns undefined', () => {
    const rec = ledger.open('ls', '/', '', fakeLineOf(1))
    expect(ledger.resolveID(rec.id)).toBe(rec)
    expect(ledger.resolveID(999)).toBeUndefined()
  })
  it('has no stream entry point — markers, transitions and trust are gone (compile-time)', () => {
    const rec = ledger.open('ls', '/', '', fakeLineOf(1))
    // The severed surface is proven by the type system, never by calling it
    // — these lines must fail to compile, so they live in a function that is
    const proveAbsent = () => {
      // @ts-expect-error ADR-0024 consequences: onMarker is deleted.
      // eslint-disable-next-line @typescript-eslint/no-unsafe-call
      ledger.onMarker('A')
      // @ts-expect-error ADR-0024 consequences: completeTransition is
      // deleted with the environment-transition machinery.
      // eslint-disable-next-line @typescript-eslint/no-unsafe-call
      ledger.completeTransition(0)
      // @ts-expect-error ADR-0024 consequences: the trusted boolean is
      // deleted.
      // eslint-disable-next-line @typescript-eslint/no-unused-expressions
      rec.trusted
    }
    void proveAbsent
    expect(ledger.records()).toHaveLength(1)
  })

  it('bindAttempt ties the pending running record to the attempt — idempotent', () => {
    const rec = ledger.open('make', '/', '', fakeLineOf(1))
    const bound = ledger.bindAttempt('att-1')
    expect(bound).toBe(rec)
    expect(ledger.recordForAttempt('att-1')).toBe(rec)
    // A repeated binding returns the same record.
    expect(ledger.bindAttempt('att-1')).toBe(rec)
    // A second pending record stays unbound: the kernel allows one open
    // attempt per domain, so the projection binds the oldest pending.
    const second = ledger.open('ls', '/', '', fakeLineOf(2))
    expect(ledger.bindAttempt('att-2')).toBe(second)
  })

  it('bindAttempt with no pending record returns null — a shell-originated attempt', () => {
    expect(ledger.bindAttempt('att-9')).toBeNull()
  })

  it('complete sets the exit status exactly once from the authenticated completion', () => {
    const rec = ledger.open('make', '/repo', '', fakeLineOf(1))
    ledger.bindAttempt('att-1')
    const done = ledger.complete(attempt({ exitCode: 0 }))
    expect(done).toBe(rec)
    expect(rec.status).toBe('success')
    expect(rec.exitCode).toBe(0)
    expect(rec.endedAt).toBe(500)
    // The exit status is set exactly once: a second completion is refused.
    expect(ledger.complete(attempt({ exitCode: 7 }))).toBeNull()
    expect(rec.status).toBe('success')
    expect(rec.exitCode).toBe(0)
  })

  it('complete paints failure from a non-zero authenticated exit code', () => {
    const rec = ledger.open('make', '/repo', '', fakeLineOf(1))
    ledger.bindAttempt('att-1')
    ledger.complete(attempt({ exitCode: 2 }))
    expect(rec.status).toBe('failure')
    expect(rec.exitCode).toBe(2)
  })

  it('an abandoned attempt is unknown and never successful', () => {
    const rec = ledger.open('sleep 100', '/', '', fakeLineOf(1))
    ledger.bindAttempt('att-1')
    ledger.complete(attempt({ state: 'unknown' }))
    expect(rec.status).toBe('unknown')
    expect(rec.exitCode).toBeNull()
    expect(rec.endedAt).toBe(500)
  })

  it('complete resolves the single pending record when no binding happened (reconnect replay)', () => {
    const rec = ledger.open('make', '/', '', fakeLineOf(1))
    expect(ledger.complete(attempt({ exitCode: 0 }))).toBe(rec)
    expect(rec.status).toBe('success')
  })

  it('complete with nothing pending returns null — shell-originated attempts persist nothing', () => {
    expect(ledger.complete(attempt())).toBeNull()
  })

  it('the record keeps the app-owned command text — the attempt command never lands in the record', () => {
    const rec = ledger.open('make {{secret:ci}}', '/', '', fakeLineOf(1))
    ledger.bindAttempt('att-1')
    ledger.complete(attempt({ command: 'make sk-live-1234' }))
    expect(rec.command).toBe('make {{secret:ci}}')
  })
})

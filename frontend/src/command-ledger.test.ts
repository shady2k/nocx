// @vitest-environment jsdom
import { describe, it, expect, beforeEach } from 'vitest'
import { CommandLedger } from './command-ledger'

// Fake lineOf that returns the number we feed it. The ledger never caches
// the result, so tests call this through the ledger's own API.
function fakeLineOf(n: number): () => number | undefined {
  const disposed = false
  const fn = () => (disposed ? undefined : n)
  // eslint-disable-next-line @typescript-eslint/no-unnecessary-type-assertion
  return Object.defineProperty(fn, 'disposed', {
    get() {
      return disposed
    },
  }) as unknown as () => number | undefined
}

// Deterministic clock.
function fixtureNow(ms: number): () => number {
  return () => ms
}

describe('CommandLedger', () => {
  let ledger: CommandLedger
  let now: () => number

  beforeEach(() => {
    now = fixtureNow(1000)
    ledger = new CommandLedger({ now })
  })

  // ── open ──────────────────────────────────────────────────────────────

  it('open creates a pending record with status unknown', () => {
    const rec = ledger.open('echo hi', '/home', '', fakeLineOf(5))
    expect(rec.command).toBe('echo hi')
    expect(rec.cwd).toBe('/home')
    expect(rec.host).toBe('')
    expect(rec.status).toBe('unknown')
    expect(rec.exitCode).toBeNull()
    expect(rec.trusted).toBe(false)
    expect(rec.startedAt).toBeNull()
    expect(rec.endedAt).toBeNull()
    expect(rec.lineOf()).toBe(5)
    expect(rec.disposed).toBe(false)
  })

  it('open assigns incrementing ids', () => {
    const r1 = ledger.open('a', '/', '', fakeLineOf(0))
    const r2 = ledger.open('b', '/', '', fakeLineOf(1))
    expect(r1.id).toBeLessThan(r2.id)
  })

  it('open records appear in records() newest last', () => {
    ledger.open('cmd1', '/', '', fakeLineOf(0))
    ledger.open('cmd2', '/', '', fakeLineOf(1))
    const recs = ledger.records()
    expect(recs).toHaveLength(2)
    expect(recs[0].command).toBe('cmd1')
    expect(recs[1].command).toBe('cmd2')
  })

  it('dispose marks the record disposed', () => {
    const rec = ledger.open('x', '/', '', fakeLineOf(0))
    expect(rec.disposed).toBe(false)
    ledger.dispose(rec.id)
    expect(rec.disposed).toBe(true)
  })

  it('dispose is idempotent', () => {
    const rec = ledger.open('x', '/', '', fakeLineOf(0))
    ledger.dispose(rec.id)
    ledger.dispose(rec.id)
    expect(rec.disposed).toBe(true)
  })

  it('open fails with empty command', () => {
    expect(() => ledger.open('', '/', '', fakeLineOf(0))).toThrow()
  })

  // ── clean cycle: A→B→C→D (success) ───────────────────────────────────

  it('clean A→B marks record as pending (not running yet)', () => {
    const l = new CommandLedger({ now: fixtureNow(100) })
    l.open('ls', '/', '', fakeLineOf(3))
    l.onMarker('A')
    expect(l.records()[0].status).toBe('unknown')
    l.onMarker('B')
    expect(l.records()[0].status).toBe('unknown') // still not running
  })

  it('clean A→B→C transitions to running, sets startedAt, trusted=true', () => {
    const l = new CommandLedger({ now: fixtureNow(500) })
    l.open('ls', '/', '', fakeLineOf(3))
    l.onMarker('A')
    l.onMarker('B')
    // Need the C to actually transition the OPEN record to running.
    // The model: on open, we have a pending record at status 'unknown'.
    // A sets our internal state to prompt-ready; B confirms ownership; C starts
    // the pending record running.
    l.onMarker('C')
    const rec = l.records()[0]
    expect(rec.status).toBe('running')
    expect(rec.startedAt).toBe(500)
    expect(rec.trusted).toBe(true)
  })

  it('C→D with exit 0 → success', () => {
    const l = new CommandLedger({ now: fixtureNow(500) })
    l.open('ls', '/', '', fakeLineOf(3))
    l.onMarker('A')
    l.onMarker('B')
    l.onMarker('C')
    // endedAt = fixtureNow(500) since we don't mutate the clock mid-test.
    l.onMarker('D', 0)
    const rec = l.records()[0]
    expect(rec.status).toBe('success')
    expect(rec.exitCode).toBe(0)
    expect(rec.endedAt).toBe(500)
    expect(rec.trusted).toBe(true)
  })

  it('C→D with exit 1 → failure', () => {
    const l = new CommandLedger({ now: fixtureNow(500) })
    l.open('cmd', '/', '', fakeLineOf(3))
    l.onMarker('A')
    l.onMarker('B')
    l.onMarker('C')
    l.onMarker('D', 1)
    const rec = l.records()[0]
    expect(rec.status).toBe('failure')
    expect(rec.exitCode).toBe(1)
    expect(rec.endedAt).toBe(500)
  })

  it('C→D with exit 127 (command not found) → failure', () => {
    const l = new CommandLedger({ now: fixtureNow(500) })
    l.open('bogus', '/', '', fakeLineOf(3))
    l.onMarker('A')
    l.onMarker('B')
    l.onMarker('C')
    l.onMarker('D', 127)
    expect(l.records()[0].status).toBe('failure')
  })

  // ── orphan / untrusted paths ──────────────────────────────────────────

  it('orphan C (no preceding A→B) transitions to running but untrusted', () => {
    const l = new CommandLedger({ now: fixtureNow(500) })
    l.open('cmd', '/', '', fakeLineOf(0))
    // No A or B — just a C marker.
    l.onMarker('C')
    const rec = l.records()[0]
    expect(rec.status).toBe('running')
    expect(rec.trusted).toBe(false)
  })

  it('orphan D (no preceding C) is ignored for status', () => {
    const l = new CommandLedger({ now: fixtureNow(500) })
    l.open('cmd', '/', '', fakeLineOf(0))
    l.onMarker('A')
    l.onMarker('B')
    // No C — D comes from e.g. empty Enter.
    l.onMarker('D', 0)
    expect(l.records()[0].status).toBe('unknown')
  })

  it('D with exit!=0 on an untrusted run → failure + untrusted', () => {
    const l = new CommandLedger({ now: fixtureNow(500) })
    l.open('cmd', '/', '', fakeLineOf(0))
    l.onMarker('C') // orphan — untrusted
    l.onMarker('D', 1)
    expect(l.records()[0].status).toBe('failure')
    expect(l.records()[0].trusted).toBe(false)
  })

  // ── interruption ──────────────────────────────────────────────────────

  it('A interrupting a running trusted command → previous is interrupted', () => {
    const l = new CommandLedger({ now: fixtureNow(500) })
    // Start first command
    const rec1 = l.open('cmd1', '/', '', fakeLineOf(5))
    l.onMarker('A')
    l.onMarker('B')
    l.onMarker('C')
    expect(rec1.status).toBe('running')
    expect(rec1.trusted).toBe(true)

    // A interrupts — starts a new prompt before D arrived
    l.onMarker('A')
    expect(rec1.status).toBe('interrupted')
    expect(rec1.trusted).toBe(true)

    // A second command can start normally
    const rec2 = l.open('cmd2', '/', '', fakeLineOf(10))
    l.onMarker('A')
    l.onMarker('B')
    l.onMarker('C')
    expect(rec2.status).toBe('running')
  })

  it('A interrupting an untrusted running command → previous is unknown', () => {
    const l = new CommandLedger({ now: fixtureNow(500) })
    const rec = l.open('cmd', '/', '', fakeLineOf(0))
    l.onMarker('C') // orphan → untrusted running
    l.onMarker('A') // interrupt
    expect(rec.status).toBe('unknown')
    expect(rec.trusted).toBe(false)
  })

  // ── D completes the most-recently-opened running record ─────────────

  it('D finishes the most recent running record', () => {
    const l = new CommandLedger({ now: fixtureNow(500) })
    const r1 = l.open('cmd1', '/', '', fakeLineOf(0))
    l.onMarker('A')
    l.onMarker('B')
    l.onMarker('C')
    l.onMarker('D', 0) // finishes r1
    expect(r1.status).toBe('success')

    const r2 = l.open('cmd2', '/', '', fakeLineOf(10))
    l.onMarker('A')
    l.onMarker('B')
    l.onMarker('C')
    l.onMarker('D', 1)
    expect(r2.status).toBe('failure')
    expect(r1.status).toBe('success') // unchanged
  })

  // ── edge cases ────────────────────────────────────────────────────────

  it('B,B from RAW does not grant trust (mirrors input-state.ts)', () => {
    const l = new CommandLedger({ now: fixtureNow(500) })
    l.open('cmd', '/', '', fakeLineOf(0))
    l.onMarker('B') // no A
    l.onMarker('C')
    expect(l.records()[0].trusted).toBe(false)
  })

  it('records() returns a defensive copy', () => {
    const l = new CommandLedger({ now: fixtureNow(500) })
    l.open('cmd', '/', '', fakeLineOf(0))
    const r1 = l.records()
    const r2 = l.records()
    expect(r1).not.toBe(r2) // different array references
    expect(r1[0]).toBe(r2[0]) // but same record object references
  })

  it('starts with empty records', () => {
    expect(ledger.records()).toHaveLength(0)
  })

  it('A before any open is a no-op', () => {
    // Should not throw.
    expect(() => ledger.onMarker('A')).not.toThrow()
    expect(ledger.records()).toHaveLength(0)
  })

  it('resolveID returns undefined for unknown id', () => {
    expect(ledger.resolveID(999)).toBeUndefined()
  })
})

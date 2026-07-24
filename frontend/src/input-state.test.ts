import { describe, it, expect } from 'vitest'
import {
  initialMachine,
  reduce,
  InputStateController,
  type Machine,
  type InputEvent,
} from './input-state'

const run = (evs: InputEvent[], m: Machine = initialMachine()) => evs.reduce(reduce, m)

describe('input-state clean cycle', () => {
  it('walks RAW → PROMPT_READY → RUNNING_RAW → RAW', () => {
    const a = reduce(initialMachine(), { type: 'marker', kind: 'A' })
    expect(a).toEqual({ state: 'PROMPT_READY', trusted: true, owned: false })
    const b = reduce(a, { type: 'marker', kind: 'B' })
    expect(b).toEqual({ state: 'PROMPT_READY', trusted: true, owned: true })
    const c = reduce(b, { type: 'marker', kind: 'C' })
    expect(c.state).toBe('RUNNING_RAW')
    const d = reduce(c, { type: 'marker', kind: 'D' })
    expect(d.state).toBe('RAW')
    const a2 = reduce(d, { type: 'marker', kind: 'A' })
    expect(a2.state).toBe('PROMPT_READY')
  })

  it('alt-buffer wins from any state and normal returns to RAW', () => {
    const alt = run([
      { type: 'marker', kind: 'A' },
      { type: 'buffer', buffer: 'alternate' },
    ])
    expect(alt.state).toBe('ALT_SCREEN')
    const back = reduce(alt, { type: 'buffer', buffer: 'normal' })
    expect(back.state).toBe('RAW')
  })

  it('submit from PROMPT_READY enters RUNNING_RAW', () => {
    const pr = reduce(initialMachine(), { type: 'marker', kind: 'A' })
    expect(reduce(pr, { type: 'submit' }).state).toBe('RUNNING_RAW')
  })

  it('does not mutate its input', () => {
    const m = initialMachine()
    reduce(m, { type: 'marker', kind: 'A' })
    expect(m).toEqual({ state: 'RAW', trusted: false, owned: false })
  })
})

describe('input-state hardening / resync', () => {
  it('orphan C (no prior A) is RUNNING_RAW but untrusted', () => {
    const c = reduce(initialMachine(), { type: 'marker', kind: 'C' })
    expect(c).toEqual({ state: 'RUNNING_RAW', trusted: false, owned: false })
  })

  it('orphan D (empty Enter, not running) leaves state unchanged', () => {
    const pr = reduce(initialMachine(), { type: 'marker', kind: 'A' }) // PROMPT_READY
    const d = reduce(pr, { type: 'marker', kind: 'D' })
    expect(d.state).toBe('PROMPT_READY')
  })

  it('A interrupting a running command yields untrusted PROMPT_READY', () => {
    const running = run([
      { type: 'marker', kind: 'A' },
      { type: 'marker', kind: 'C' },
    ])
    const a = reduce(running, { type: 'marker', kind: 'A' })
    expect(a).toEqual({ state: 'PROMPT_READY', trusted: false, owned: false })
  })

  it('B without a prompt is untrusted PROMPT_READY', () => {
    const b = reduce(initialMachine(), { type: 'marker', kind: 'B' })
    expect(b).toEqual({ state: 'PROMPT_READY', trusted: false, owned: false })
  })
})

describe('A→B ownership gate', () => {
  it('A alone does not grant ownership; A then B does', () => {
    const a = reduce(initialMachine(), { type: 'marker', kind: 'A' })
    expect(a.owned).toBe(false)
    const b = reduce(a, { type: 'marker', kind: 'B' })
    expect(b.owned).toBe(true)
    expect(b.state).toBe('PROMPT_READY')
  })
  it('B without a prompt does not grant ownership', () => {
    expect(reduce(initialMachine(), { type: 'marker', kind: 'B' }).owned).toBe(false)
  })
  it('B,B from RAW never latches ownership without an A (nocx-4ff.11)', () => {
    const b1 = reduce(initialMachine(), { type: 'marker', kind: 'B' })
    expect(b1.owned).toBe(false)
    const b2 = reduce(b1, { type: 'marker', kind: 'B' })
    expect(b2.owned).toBe(false) // currently FAILS: reduce grants owned on the 2nd B
  })
  it('an untrusted resync A→B does not grant ownership (fail-open)', () => {
    const running = [
      { type: 'marker', kind: 'A' } as InputEvent,
      { type: 'marker', kind: 'C' } as InputEvent,
    ].reduce(reduce, initialMachine())
    const a = reduce(running, { type: 'marker', kind: 'A' }) // untrusted PROMPT_READY
    expect(a.trusted).toBe(false)
    expect(reduce(a, { type: 'marker', kind: 'B' }).owned).toBe(false)
  })
  it('C, submit, alt-buffer, reset clear ownership', () => {
    const owned = [
      { type: 'marker', kind: 'A' } as InputEvent,
      { type: 'marker', kind: 'B' } as InputEvent,
    ].reduce(reduce, initialMachine())
    expect(owned.owned).toBe(true)
    expect(reduce(owned, { type: 'marker', kind: 'C' }).owned).toBe(false)
    expect(reduce(owned, { type: 'submit' }).owned).toBe(false)
    expect(reduce(owned, { type: 'buffer', buffer: 'alternate' }).owned).toBe(false)
    expect(reduce(owned, { type: 'reset' }).owned).toBe(false)
  })
})

describe('InputStateController', () => {
  it('tracks state and fires onChange only on real changes', () => {
    const c = new InputStateController()
    const seen: string[] = []
    c.onChange((m) => seen.push(m.state))
    c.dispatch({ type: 'marker', kind: 'A' }) // -> PROMPT_READY, owned:false
    c.dispatch({ type: 'marker', kind: 'B' }) // PROMPT_READY still, but owned→true (fires)
    c.dispatch({ type: 'marker', kind: 'C' }) // -> RUNNING_RAW
    expect(c.state).toBe('RUNNING_RAW')
    expect(seen).toEqual(['PROMPT_READY', 'PROMPT_READY', 'RUNNING_RAW'])
  })
})

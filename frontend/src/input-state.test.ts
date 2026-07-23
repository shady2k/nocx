import { describe, it, expect } from 'vitest'
import { initialMachine, reduce, InputStateController, type Machine, type InputEvent } from './input-state'

const run = (evs: InputEvent[], m: Machine = initialMachine()) =>
  evs.reduce(reduce, m)

describe('input-state clean cycle', () => {
  it('walks RAW → PROMPT_READY → RUNNING_RAW → RAW', () => {
    const a = reduce(initialMachine(), { type: 'marker', kind: 'A' })
    expect(a).toEqual({ state: 'PROMPT_READY', trusted: true })
    const b = reduce(a, { type: 'marker', kind: 'B' })
    expect(b).toEqual({ state: 'PROMPT_READY', trusted: true })
    const c = reduce(b, { type: 'marker', kind: 'C' })
    expect(c.state).toBe('RUNNING_RAW')
    const d = reduce(c, { type: 'marker', kind: 'D' })
    expect(d.state).toBe('RAW')
    const a2 = reduce(d, { type: 'marker', kind: 'A' })
    expect(a2.state).toBe('PROMPT_READY')
  })

  it('alt-buffer wins from any state and normal returns to RAW', () => {
    const alt = run([{ type: 'marker', kind: 'A' }, { type: 'buffer', buffer: 'alternate' }])
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
    expect(m).toEqual({ state: 'RAW', trusted: false })
  })
})

describe('input-state hardening / resync', () => {
  it('orphan C (no prior A) is RUNNING_RAW but untrusted', () => {
    const c = reduce(initialMachine(), { type: 'marker', kind: 'C' })
    expect(c).toEqual({ state: 'RUNNING_RAW', trusted: false })
  })

  it('orphan D (empty Enter, not running) leaves state unchanged', () => {
    const pr = reduce(initialMachine(), { type: 'marker', kind: 'A' }) // PROMPT_READY
    const d = reduce(pr, { type: 'marker', kind: 'D' })
    expect(d.state).toBe('PROMPT_READY')
  })

  it('A interrupting a running command yields untrusted PROMPT_READY', () => {
    const running = run([{ type: 'marker', kind: 'A' }, { type: 'marker', kind: 'C' }])
    const a = reduce(running, { type: 'marker', kind: 'A' })
    expect(a).toEqual({ state: 'PROMPT_READY', trusted: false })
  })

  it('B without a prompt is untrusted PROMPT_READY', () => {
    const b = reduce(initialMachine(), { type: 'marker', kind: 'B' })
    expect(b).toEqual({ state: 'PROMPT_READY', trusted: false })
  })
})

describe('InputStateController', () => {
  it('tracks state and fires onChange only on real changes', () => {
    const c = new InputStateController()
    const seen: string[] = []
    c.onChange((m) => seen.push(m.state))
    c.dispatch({ type: 'marker', kind: 'A' }) // -> PROMPT_READY
    c.dispatch({ type: 'marker', kind: 'B' }) // stays PROMPT_READY (no change)
    c.dispatch({ type: 'marker', kind: 'C' }) // -> RUNNING_RAW
    expect(c.state).toBe('RUNNING_RAW')
    expect(seen).toEqual(['PROMPT_READY', 'RUNNING_RAW'])
  })
})

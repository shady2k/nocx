import { describe, it, expect } from 'vitest'
import { initialMachine, reduce, type Machine, type InputEvent } from './input-state'

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

/**
 * PolicyClient — the renderer's end of the ONE global agent policy.
 *
 * Two things are worth pinning here. The matrix adapts from the generated
 * per-key row types into the one uniform editor shape, and `live` — which
 * effect classes a declared tool actually carries — travels beside it
 * untouched. `live` is the backend's answer, not the renderer's: the page
 * must never derive "does this row govern anything" from a tool name
 * (ADR-0028 decision 4), which is exactly what it would have to do if this
 * client dropped the field.
 */
import { describe, expect, it, vi } from 'vitest'
import { PolicyClient, EFFECT_KEYS, blankPolicy, type EffectKey } from './policy-client'
import type { Dispatcher } from './dispatcher'

/** A dispatcher double: records every call and answers from a queue. */
function fakeDispatcher(answers: unknown[]): {
  dispatcher: Dispatcher
  calls: { method: string; params: unknown }[]
} {
  const calls: { method: string; params: unknown }[] = []
  const call = vi.fn((method: string, params: unknown) => {
    calls.push({ method, params })
    const next = answers.shift()
    if (next instanceof Error) return Promise.reject(next)
    return Promise.resolve(next)
  })
  return { dispatcher: { call } as unknown as Dispatcher, calls }
}

/** The wire's policy object: all seven rows, as the backend always sends it. */
function wirePolicy(overrides: Partial<Record<EffectKey, unknown>> = {}) {
  const p: Record<string, unknown> = {}
  for (const k of EFFECT_KEYS) p[k] = { decision: 'ask', scopes: [] }
  return { ...p, ...overrides }
}

describe('PolicyClient.get', () => {
  it('adapts the wire matrix into the uniform editor shape', async () => {
    const { dispatcher, calls } = fakeDispatcher([
      {
        policy: wirePolicy({
          observe: { decision: 'permit', scopes: [{ kind: 'path', id: '/workspace' }] },
        }),
        live: ['observe', 'mutate-destructive'],
      },
    ])

    const view = await new PolicyClient(dispatcher).get()

    expect(calls[0]?.method).toBe('policy.get')
    expect(view.matrix.observe).toEqual({
      decision: 'permit',
      scopes: [{ kind: 'path', id: '/workspace' }],
    })
    expect(Object.keys(view.matrix)).toEqual([...EFFECT_KEYS])
  })

  it('carries the live list through: the page is told which rows govern anything', async () => {
    // The renderer cannot work this out. Seven rows are drawn and only the
    // ones a declared tool carries govern anything; deriving that here would
    // mean mapping a tool name to an effect, which is the one thing no
    // configuration path may do.
    const { dispatcher } = fakeDispatcher([
      { policy: wirePolicy(), live: ['observe', 'mutate-destructive'] },
    ])

    const view = await new PolicyClient(dispatcher).get()

    expect(view.live).toEqual(['observe', 'mutate-destructive'])
  })

  it('a longer live list is passed through unchanged, not filtered to today’s two', async () => {
    // The point of putting it on the wire is that a tool declared tomorrow
    // changes this list with no renderer edit. A client that hard-coded
    // today's answer would look identical until that day.
    const { dispatcher } = fakeDispatcher([
      { policy: wirePolicy(), live: ['observe', 'disclose', 'delegate'] },
    ])

    const view = await new PolicyClient(dispatcher).get()

    expect(view.live).toEqual(['observe', 'disclose', 'delegate'])
  })

  it('an empty live list is an empty list, not a missing one', async () => {
    const { dispatcher } = fakeDispatcher([{ policy: wirePolicy(), live: [] }])

    const view = await new PolicyClient(dispatcher).get()

    expect(view.live).toEqual([])
  })
})

describe('PolicyClient.set', () => {
  it('sends the matrix as it stands', async () => {
    const { dispatcher, calls } = fakeDispatcher([{ ok: true }])
    const matrix = blankPolicy()
    matrix.observe = { decision: 'permit', scopes: [{ kind: 'path', id: '/workspace' }] }

    await new PolicyClient(dispatcher).set(matrix)

    expect(calls[0]?.method).toBe('policy.set')
    expect(calls[0]?.params).toEqual({ policy: matrix })
  })
})

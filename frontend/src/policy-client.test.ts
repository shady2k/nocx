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
import {
  PolicyClient,
  EFFECT_KEYS,
  blankPolicy,
  type EffectKey,
  type PolicyRule,
  type PolicyRuleWrite,
  type PolicyExplanation,
  type PolicyExplanationStep,
  type PolicyExplanationResource,
  type PolicyClassification,
  type PolicyCommandFeature,
} from './policy-client'
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

/** One rule as the wire sends it back, with the provenance a page needs to
 *  say what it is taking back. */
function wireRule(overrides: Partial<PolicyRule> = {}): PolicyRule {
  return {
    id: '0123456789abcdef0123456789abcdef',
    selector: { exact: [['df', '-h']] },
    decision: 'permit',
    createdAt: '2026-09-04T10:00:00Z',
    source: 'answered',
    evaluatorVersion: 2,
    ...overrides,
  }
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

  it('carries the rules through with their provenance', async () => {
    // policy.get has carried rules since they landed and nothing read them,
    // so an answer a person gave at a prompt was visible only to the code
    // enforcing it. Provenance is the half that makes a rule takeable back:
    // a page cannot say WHAT it is revoking without where the rule came from.
    const { dispatcher } = fakeDispatcher([
      {
        policy: { ...wirePolicy(), rules: [wireRule(), wireRule({ id: 'b', source: 'written' })] },
        live: ['observe'],
      },
    ])

    const view = await new PolicyClient(dispatcher).get()

    expect(view.rules).toHaveLength(2)
    expect(view.rules[0]).toEqual(wireRule())
    expect(view.rules[1]?.source).toBe('written')
  })

  it('a policy with no rules reads as an empty list, not undefined', async () => {
    // The wire omits the key when there are none. A surface that had to tell
    // absent from empty would grow a second answer to "are there rules".
    const { dispatcher } = fakeDispatcher([{ policy: wirePolicy(), live: [] }])

    const view = await new PolicyClient(dispatcher).get()

    expect(view.rules).toEqual([])
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

  it('sends no rules key at all — a matrix save cannot delete a standing answer', async () => {
    // This is the regression, at the renderer's end. A matrix-only save used
    // to send the whole document, and the guard that saved us read "absent
    // means nothing to say" — which holds only while nothing sends the key.
    // `rules: []` is what serialising an empty list produces, it is not
    // absent, and it deleted every rule a person had approved (nocx-39bly).
    const { dispatcher, calls } = fakeDispatcher([{ ok: true }])

    await new PolicyClient(dispatcher).set(blankPolicy())

    const params = calls[0]?.params as { policy: Record<string, unknown> }
    expect(Object.keys(params)).toEqual(['policy'])
    expect('rules' in params.policy).toBe(false)
    expect(Object.keys(params.policy)).toEqual([...EFFECT_KEYS])
  })
})

describe('PolicyClient.setRule', () => {
  it('sends ONE rule and answers with the id the backend minted', async () => {
    const { dispatcher, calls } = fakeDispatcher([{ id: 'minted-id', added: true }])
    const rule: PolicyRuleWrite = { selector: { exact: [['df', '-h']] }, decision: 'permit' }

    const answer = await new PolicyClient(dispatcher).setRule(rule)

    expect(calls[0]?.method).toBe('policy.setRule')
    expect(calls[0]?.params).toEqual({ rule })
    expect(answer).toEqual({ id: 'minted-id', added: true })
  })

  it('says nothing about where a rule came from', async () => {
    // createdAt, source and evaluatorVersion are facts the backend records
    // about the write. A renderer that could set them could dress a rule it
    // wrote as one a person answered at a prompt.
    const { dispatcher, calls } = fakeDispatcher([{ id: 'minted-id', added: true }])

    await new PolicyClient(dispatcher).setRule({ selector: { program: 'df' }, decision: 'refuse' })

    const { rule } = calls[0]?.params as { rule: Record<string, unknown> }
    expect(Object.keys(rule).sort()).toEqual(['decision', 'selector'])
  })

  it('names the rule it replaces by its id', async () => {
    const { dispatcher, calls } = fakeDispatcher([{ id: 'existing', added: false }])

    const answer = await new PolicyClient(dispatcher).setRule({
      id: 'existing',
      selector: { exact: [['df', '-k']] },
      decision: 'refuse',
    })

    expect((calls[0]?.params as { rule: { id: string } }).rule.id).toBe('existing')
    expect(answer.added).toBe(false)
  })
})

describe('PolicyClient.forgetRule', () => {
  it('sends the id and answers whether a rule was there', async () => {
    const { dispatcher, calls } = fakeDispatcher([{ removed: true }])

    const answer = await new PolicyClient(dispatcher).forgetRule('rule-1')

    expect(calls[0]?.method).toBe('policy.forgetRule')
    expect(calls[0]?.params).toEqual({ id: 'rule-1' })
    expect(answer.removed).toBe(true)
  })

  it('an id naming nothing RESOLVES with removed:false rather than rejecting', async () => {
    // Forgetting is idempotent: the rule is already not there, which is what
    // was asked for. A rejection would raise a danger toast about a state the
    // person wanted.
    const { dispatcher } = fakeDispatcher([{ removed: false }])

    await expect(new PolicyClient(dispatcher).forgetRule('gone')).resolves.toEqual({
      removed: false,
    })
  })
})

describe('PolicyClient.explain', () => {
  it('asks the backend why, and passes the steps through untouched', async () => {
    // Typed as the wire declares it, so a fixture cannot describe a shape the
    // contract does not carry — the defect the generated types exist for.
    const steps: [PolicyExplanationStep, ...PolicyExplanationStep[]] = [
      { kind: 'effect-row', effect: 'observe', decision: 'ask' },
      { kind: 'rule-matched', ruleId: 'rule-1', decision: 'permit' },
      { kind: 'resource-inside', effect: 'observe', decision: 'permit' },
    ]
    const explanation: PolicyExplanation = {
      effect: 'observe',
      decision: 'permit',
      trace: steps,
    }
    const { dispatcher, calls } = fakeDispatcher([explanation])

    const answer = await new PolicyClient(dispatcher).explain('df -h', 'observe')

    expect(calls[0]?.method).toBe('policy.explain')
    expect(calls[0]?.params).toEqual({ command: 'df -h', effect: 'observe' })
    // The order is the explanation. A client that re-sorted, filtered or
    // summarised would be deciding what a person is allowed to be told.
    expect(answer).toEqual(explanation)
  })

  it('carries the cause and the resource that fell outside', async () => {
    const outside: PolicyExplanationResource = { kind: 'path', id: '/etc/hosts' }
    const { dispatcher } = fakeDispatcher([
      {
        effect: 'observe',
        decision: 'ask',
        cause: 'row-scope',
        resource: outside,
        trace: [
          { kind: 'effect-row', effect: 'observe', decision: 'permit' },
          { kind: 'resource-outside-row-scope', effect: 'observe', decision: 'ask' },
        ],
      } satisfies PolicyExplanation,
    ])

    const answer = await new PolicyClient(dispatcher).explain('cat /etc/hosts', 'observe')

    // 'row-scope' is a question a person can answer and 'fence' is not, so the
    // two may never reach a surface as one "out of scope".
    expect(answer.cause).toBe('row-scope')
    expect(answer.resource).toEqual(outside)
  })
})

describe('PolicyClient.classify', () => {
  it('asks the backend to READ one command, and passes the reading through untouched', async () => {
    // Typed as the wire declares it, so a fixture cannot describe a shape the
    // contract does not carry.
    const reading: PolicyClassification = {
      program: 'df',
      commands: [['df', '-h']],
      effect: 'observe',
      features: [],
      eligible: true,
      reason: '',
    }
    const { dispatcher, calls } = fakeDispatcher([reading])

    const answer = await new PolicyClient(dispatcher).classify('df -h')

    expect(calls[0]?.method).toBe('policy.classify')
    // The command and NOTHING else. An effect in these params would be the
    // renderer claiming to know what a command does, which is the one claim
    // that makes a permit typed.
    expect(calls[0]?.params).toEqual({ command: 'df -h' })
    expect(answer).toEqual(reading)
  })

  it('carries a refusal with the reason, and no effect to mint a permit from', async () => {
    const refused: PolicyClassification = {
      program: '',
      commands: [],
      features: [],
      eligible: false,
      reason: 'the command uses an indirect wrapper or shell feature',
    }
    const { dispatcher } = fakeDispatcher([refused])

    const answer = await new PolicyClient(dispatcher).classify('sudo df -h')

    expect(answer.eligible).toBe(false)
    expect(answer.reason).toContain('indirect wrapper')
    // No effect at all: a permit is bound to the effect the reading found, so
    // a refused reading must leave a surface with nothing to bind to.
    expect(answer.effect).toBeUndefined()
  })

  it('carries the semantic facts a narrowing rule can match', async () => {
    const feature: PolicyCommandFeature = 'writes-option-named-path'
    const { dispatcher } = fakeDispatcher([
      {
        program: 'sort',
        commands: [['sort', '-o', '/tmp/out', '/tmp/in']],
        effect: 'mutate-reversible',
        features: [feature],
        eligible: true,
        reason: '',
      } satisfies PolicyClassification,
    ])

    const answer = await new PolicyClient(dispatcher).classify('sort -o /tmp/out /tmp/in')

    // A refusal matches this FACT and never the spelling of the token that
    // carried it, so it has to arrive as a fact.
    expect(answer.features).toEqual(['writes-option-named-path'])
  })
})

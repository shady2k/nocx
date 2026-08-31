import { describe, expect, it } from 'vitest'
import {
  FLAG_ON,
  hasSecretReference,
  needsForm,
  resolveBody,
  visibleFields,
  type SessionFacts,
} from './resolve'
import { parse } from './parse'

const FULL: SessionFacts = {
  cwd: '/home/dev/repos/nocx',
  host: 'vm-agents',
  user: 'dev',
  branch: 'main',
}
const NONE = new Map<string, string>()

describe('resolveBody', () => {
  it('substitutes env keys from live facts', () => {
    const out = resolveBody('cd {{env:cwd}} on {{env:host}}', FULL, NONE)
    expect(out).toEqual({ kind: 'resolved', text: 'cd /home/dev/repos/nocx on vm-agents' })
  })

  // Unavailable is not the empty string. `cd {{env:cwd}}` becoming `cd` is
  // the failure this rule exists for.
  it('refuses rather than substituting an empty string', () => {
    const out = resolveBody(
      'cd {{env:cwd}} @ {{env:branch}}',
      { ...FULL, cwd: null, branch: null },
      NONE,
    )
    expect(out).toEqual({ kind: 'refused', reason: 'env-unavailable', keys: ['cwd', 'branch'] })
  })

  // The env table is closed (design §7.4): a key that is not a row is as
  // unanswerable as a null fact, and refuses the same way.
  it('refuses an env key that is not in the closed table', () => {
    const out = resolveBody('say {{env:last_command}}', FULL, NONE)
    expect(out).toEqual({ kind: 'refused', reason: 'env-unavailable', keys: ['last_command'] })
  })

  it('asks for its fields before resolving', () => {
    const out = resolveBody('run -p {{port=8080}}', FULL, NONE)
    expect(out).toEqual({
      kind: 'needs-fields',
      fields: [{ name: 'port', kind: 'text', defaultValue: '8080', options: [], inside: null }],
    })
  })

  it('substitutes answered fields, including a name used twice', () => {
    const answers = new Map([['port', '9000']])
    const out = resolveBody('{{port}} and {{port}}', FULL, answers)
    expect(out).toEqual({ kind: 'resolved', text: '9000 and 9000' })
  })

  it('leaves a secret reference intact — it is not ours to resolve', () => {
    const out = resolveBody('psql {{secret:prod-db}}', FULL, NONE)
    expect(out).toEqual({ kind: 'resolved', text: 'psql {{secret:prod-db}}' })
  })

  it('resolves a plain body to itself', () => {
    expect(resolveBody('just text', FULL, NONE)).toEqual({ kind: 'resolved', text: 'just text' })
    expect(needsForm('just text')).toBe(false)
  })
})

describe('hasSecretReference', () => {
  it('reports a vault reference', () => {
    expect(hasSecretReference('psql {{secret:prod-db}}')).toBe(true)
    expect(hasSecretReference('psql local')).toBe(false)
  })
})

const on = (...names: string[]): Map<string, string> => new Map(names.map((n) => [n, FLAG_ON]))

describe('conditions', () => {
  const BODY = 'a\n{% if fast %}\nquick\n{% endif %}\nz'

  it('a switched-on block keeps its text and loses its tag lines', () => {
    expect(resolveBody(BODY, FULL, on('fast'))).toEqual({
      kind: 'resolved',
      text: 'a\nquick\nz',
    })
  })

  it('a switched-off block leaves no blank line behind', () => {
    expect(resolveBody(BODY, FULL, new Map([['fast', '']]))).toEqual({
      kind: 'resolved',
      text: 'a\nz',
    })
  })

  it('"not" inverts it', () => {
    const body = '{% if not fast %}slow{% endif %}'
    expect(resolveBody(body, FULL, new Map([['fast', '']]))).toEqual({
      kind: 'resolved',
      text: 'slow',
    })
    expect(resolveBody(body, FULL, on('fast'))).toEqual({ kind: 'resolved', text: '' })
  })

  it('a tag sharing its line loses only the tag', () => {
    expect(resolveBody('x {% if f %}y{% endif %} z', FULL, on('f'))).toEqual({
      kind: 'resolved',
      text: 'x y z',
    })
  })

  it('a field inside a switched-off block is not asked', () => {
    const body = '{% if f %}{{n=3}}{% endif %}'
    expect(visibleFields(parse(body), new Map([['f', '']])).map((x) => x.name)).toEqual(['f'])
    expect(resolveBody(body, FULL, new Map([['f', '']]))).toEqual({ kind: 'resolved', text: '' })
  })

  it('an env key inside a switched-off block no longer refuses the fire', () => {
    const body = '{% if f %}{{env:branch}}{% endif %}ok'
    const noBranch: SessionFacts = { ...FULL, branch: null }
    expect(resolveBody(body, noBranch, new Map([['f', '']]))).toEqual({
      kind: 'resolved',
      text: 'ok',
    })
    expect(resolveBody(body, noBranch, on('f'))).toEqual({
      kind: 'refused',
      reason: 'env-unavailable',
      keys: ['branch'],
    })
  })

  it('{%% arrives as a literal {%', () => {
    expect(resolveBody('write {%% if x %}', FULL, NONE)).toEqual({
      kind: 'resolved',
      text: 'write {% if x %}',
    })
  })
})

describe('a malformed body refuses before anything else', () => {
  it('names its diagnostics', () => {
    const out = resolveBody('{% if x %}unclosed', FULL, NONE)
    expect(out).toMatchObject({ kind: 'refused', reason: 'malformed' })
    expect(out.kind === 'refused' && out.reason === 'malformed' && out.diagnostics[0].kind).toBe(
      'unclosed-block',
    )
  })
})

describe('an answer that is itself a secret reference (design §9)', () => {
  it('stays a live reference — the destination policy governs it, not this module', () => {
    const out = resolveBody('psql {{db}}', FULL, new Map([['db', '{{secret:prod}}']]))
    expect(out).toEqual({ kind: 'resolved', text: 'psql {{secret:prod}}' })
  })
})

describe('an answer is never re-parsed', () => {
  it('template notation typed into a field arrives as text', () => {
    const answers = new Map([['a', '{{b}} {%% x']])
    expect(resolveBody('{{a}}', FULL, answers)).toEqual({
      kind: 'resolved',
      text: '{{b}} {%% x',
    })
  })
})

describe('needsForm', () => {
  it('is false for a body with nothing to fill in', () => {
    expect(needsForm('git status')).toBe(false)
  })
  it('is true for a parameter and for a flag', () => {
    expect(needsForm('{{p}}')).toBe(true)
    expect(needsForm('{% if f %}x{% endif %}')).toBe(true)
  })
})

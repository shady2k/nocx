import { describe, expect, it } from 'vitest'
import { askFields, hasSecretReference, resolveBody, type SessionFacts } from './resolve'

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

  it('asks for ask fields before resolving', () => {
    const out = resolveBody('run -p {{port=8080}}', FULL, NONE)
    expect(out).toEqual({ kind: 'needs-fields', fields: [{ name: 'port', defaultValue: '8080' }] })
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
    expect(askFields('just text')).toEqual([])
  })
})

describe('askFields', () => {
  it('deduplicates by name and keeps the first default', () => {
    expect(askFields('{{p=1}} {{p=2}} {{q}}')).toEqual([
      { name: 'p', defaultValue: '1' },
      { name: 'q', defaultValue: '' },
    ])
  })
})

describe('hasSecretReference', () => {
  it('reports a vault reference', () => {
    expect(hasSecretReference('psql {{secret:prod-db}}')).toBe(true)
    expect(hasSecretReference('psql local')).toBe(false)
  })
})

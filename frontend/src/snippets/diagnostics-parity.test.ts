/**
 * The legend an author reads in Settings and the refusal a person gets at
 * the fire are ONE computation, and this is what says so (AD-8, plan Task
 * 10).
 *
 * Nothing here is expected to fail, and that is the point: `describeBody`
 * and `resolveBody` both read `parse` and neither derives anything of its
 * own, so a body the preview calls broken must be a body the fire refuses,
 * with the same words. A failure is a report that one of the two has started
 * reading the grammar for itself — the fix belongs in that derivation, never
 * in this table.
 *
 * The table is checked against parse.ts's own union below, so a
 * DiagnosticKind added without a row here fails rather than passing
 * silently. A test that covers "the kinds somebody remembered" is the shape
 * this whole feature exists to avoid.
 */
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { describeBody } from './preview'
import { resolveBody, type SessionFacts } from './resolve'

const FACTS: SessionFacts = { cwd: '/w', host: 'h', user: 'u', branch: 'main' }

const MALFORMED = [
  ['unclosed-block', '{% if x %}body'],
  ['stray-endif', 'body{% endif %}'],
  ['nested-block', '{% if a %}{% if b %}x{% endif %}{% endif %}'],
  ['unterminated-tag', '{% if x'],
  ['unknown-tag', '{% for x %}{% endif %}'],
  ['condition-on-parameter', '{{w}}{% if w %}x{% endif %}'],
  ['conflicting-declaration', '{{w=a}} {{w=b|c}}'],
] as const

describe('the legend and the refusal come from one computation', () => {
  it('has a row for every DiagnosticKind the parser can report', () => {
    const source = readFileSync(new URL('./parse.ts', import.meta.url), 'utf8')
    const union = /type DiagnosticKind =([\s\S]*?)\n\n/.exec(source)
    expect(union, 'DiagnosticKind is no longer a union literal in parse.ts').not.toBeNull()
    const declared = [...(union?.[1] ?? '').matchAll(/'([a-z-]+)'/g)].map((m) => m[1])
    expect(declared.length).toBeGreaterThan(0)
    expect([...declared].sort()).toEqual([...MALFORMED.map(([kind]) => kind)].sort())
  })

  it.each(MALFORMED)('%s: the preview says problem and the fire refuses', (kind, body) => {
    const problems = describeBody(body).filter((p) => p.kind === 'problem')
    expect(problems.length).toBeGreaterThan(0)

    const out = resolveBody(body, FACTS, new Map())
    expect(out).toMatchObject({ kind: 'refused', reason: 'malformed' })
    if (out.kind !== 'refused' || out.reason !== 'malformed') return

    // The row names the kind so a body that stops producing the defect it
    // was written for fails here rather than passing on a different one.
    expect(out.diagnostics.map((d) => d.kind)).toContain(kind)
    // And the two surfaces say the same words, not merely both complain.
    expect(out.diagnostics.map((d) => d.detail).sort()).toEqual(
      problems.map((p) => (p.kind === 'problem' ? p.detail : '')).sort(),
    )
  })
})

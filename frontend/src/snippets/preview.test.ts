// The span preview (design §10.4, bead nocx-gjnr): what the parser
// recognised in a body, and — the reason this exists — what it did not. A
// mistyped `{{ask:port}` matches nothing, and without this line the author
// has no signal at all until a malformed literal is fired into somebody's
// agent session.
import { describe, it, expect } from 'vitest'
import { describeBody } from './preview'

describe('describeBody (design §10.4)', () => {
  it('names an env span and what it will become', () => {
    expect(describeBody('cd {{env:cwd}}')).toEqual([
      { kind: 'env', text: '{{env:cwd}}', key: 'cwd', known: true },
    ])
  })

  it('names an ask field and its default', () => {
    expect(describeBody('curl :{{port=8080}}')).toEqual([
      { kind: 'ask', text: '{{port=8080}}', name: 'port', defaultValue: '8080' },
    ])
  })

  it('reports a secret reference as the vault namespace, not as unrecognised', () => {
    // Saving a snippet that carries one is allowed and unremarked (§11.1);
    // the preview still has to say the span was RECOGNISED, because the
    // author needs to know it is not literal text.
    expect(describeBody('psql {{secret:prod-db}}')).toEqual([
      { kind: 'secret', text: '{{secret:prod-db}}', name: 'prod-db' },
    ])
  })

  it('a mistyped span with one closing brace is unrecognised, never a substitution', () => {
    expect(describeBody('curl :{{ask:port}')).toEqual([
      { kind: 'unrecognised', text: '{{ask:port}' },
    ])
  })

  it('a colon is what decides: a bare name is a field, a misspelt namespace is literal', () => {
    // This case reversed when `ask` was retired, and the reversal IS the
    // grammar: `{{cwd}}` used to be unrecognised because it named no
    // namespace, and is now a field precisely because it names none. What
    // did not move is the other half — a colon commits the span to the
    // registry, and a namespace nobody owns stays literal text.
    expect(describeBody('{{cwd}} {{evn:cwd}}')).toEqual([
      { kind: 'ask', text: '{{cwd}}', name: 'cwd', defaultValue: '' },
      { kind: 'unrecognised', text: '{{evn:cwd}}' },
    ])
  })

  it('an env key outside the table is recognised as a span and reported as unknown', () => {
    // It parses, so it is not a typo the scan can miss — but it can never be
    // answered, and the fire refuses on it (§11.2). The author should learn
    // that here rather than at the first fire.
    expect(describeBody('{{env:nope}}')).toEqual([
      { kind: 'env', text: '{{env:nope}}', key: 'nope', known: false },
    ])
  })

  it('reports every span in the order it occurs, across namespaces', () => {
    const parts = describeBody('{{name}} in {{env:cwd}} then {{secret:k}} and {{evn:oops}}')
    expect(parts.map((p) => p.kind)).toEqual(['ask', 'env', 'secret', 'unrecognised'])
  })

  it('a body with nothing to substitute describes nothing', () => {
    expect(describeBody('git status')).toEqual([])
  })

  it('does not mistake a single brace pair for a span', () => {
    expect(describeBody('echo {ok} ${HOME}')).toEqual([])
  })

  it('an unterminated {{ at the end of the body is unrecognised, not dropped', () => {
    expect(describeBody('tail {{env:cw')).toEqual([{ kind: 'unrecognised', text: '{{env:cw' }])
  })
})

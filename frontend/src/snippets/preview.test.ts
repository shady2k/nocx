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

  it('names a parameter field and its default', () => {
    expect(describeBody('curl :{{port=8080}}')).toEqual([
      { kind: 'param', text: '{{port=8080}}', name: 'port', defaultValue: '8080', options: [] },
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

  it('a bare name is a parameter now, not a mistake', () => {
    // This case reversed when `ask` was retired, and the reversal IS the
    // grammar: `{{cwd}}` used to be unrecognised because it named no
    // namespace, and is now a field precisely because it names none.
    expect(describeBody('{{cwd}}')).toEqual([
      { kind: 'param', text: '{{cwd}}', name: 'cwd', defaultValue: '', options: [] },
    ])
  })

  it('a misspelt namespace is still unrecognised, and so is the retired ask:', () => {
    // The other half of the rule did not move: a colon commits the span to
    // the registry, and a namespace nobody owns stays literal text.
    expect(describeBody('{{evn:cwd}} {{ask:port}}')).toEqual([
      { kind: 'unrecognised', text: '{{evn:cwd}}' },
      { kind: 'unrecognised', text: '{{ask:port}}' },
    ])
  })

  it('an option list reports what it will offer', () => {
    expect(describeBody('{{w=a|b}}')).toEqual([
      { kind: 'param', text: '{{w=a|b}}', name: 'w', defaultValue: 'a', options: ['a', 'b'] },
    ])
  })

  it('a condition reports as a flag', () => {
    const parts = describeBody('{% if fast %}x{% endif %}')
    expect(parts).toContainEqual({
      kind: 'flag',
      text: '{% if fast %}',
      name: 'fast',
      negated: false,
    })
  })

  it('a malformed body reports its problem', () => {
    const parts = describeBody('{% if x %}no end')
    expect(parts.some((p) => p.kind === 'problem')).toBe(true)
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
    expect(parts.map((p) => p.kind)).toEqual(['param', 'env', 'secret', 'unrecognised'])
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

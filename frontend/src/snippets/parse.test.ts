import { describe, expect, it } from 'vitest'
import { REFERENCE_NAMESPACES } from './reference-namespaces'
import { parse } from './parse'

describe('parse — value spans', () => {
  it('a bare name with no colon is a parameter', () => {
    expect(parse('run {{worker}}').spans).toEqual([
      { from: 4, to: 14, kind: 'param', arg: 'worker' },
    ])
  })

  it('a declaration keeps its default and its option list intact', () => {
    expect(parse('{{w=claude|omp}} {{p=8080}}').spans).toEqual([
      { from: 0, to: 16, kind: 'param', arg: 'w=claude|omp' },
      { from: 17, to: 27, kind: 'param', arg: 'p=8080' },
    ])
  })

  it('a registered namespace is its own kind, and the vault owns its name', () => {
    expect(parse('cd {{env:cwd}} && psql {{secret:prod-db}}').spans).toEqual([
      { from: 3, to: 14, kind: 'env', arg: 'cwd' },
      { from: 23, to: 41, kind: 'secret', arg: 'prod-db' },
    ])
  })

  it('a colon with an unregistered namespace stays literal — ask: included', () => {
    expect(parse('{{evn:cwd}} {{ask:port}}').spans).toEqual([
      { from: 0, to: 11, kind: 'unrecognised', arg: '{{evn:cwd}}' },
      { from: 12, to: 24, kind: 'unrecognised', arg: '{{ask:port}}' },
    ])
  })

  it('one closing brace is not a span, and is quoted to that brace', () => {
    expect(parse('curl :{{ask:port}').spans).toEqual([
      { from: 6, to: 17, kind: 'unrecognised', arg: '{{ask:port}' },
    ])
  })

  it('the registry lists only the two owners left', () => {
    expect(Object.keys(REFERENCE_NAMESPACES).sort()).toEqual(['env', 'secret'])
  })
})

describe('parse — blocks', () => {
  it('an if/endif pair is one block, with its offsets', () => {
    const p = parse('a{% if fast %}b{% endif %}c')
    expect(p.blocks).toEqual([
      { openFrom: 1, openTo: 14, closeFrom: 15, closeTo: 26, name: 'fast', negated: false },
    ])
    expect(p.diagnostics).toEqual([])
  })

  it('"not" negates, and is not part of the name', () => {
    expect(parse('{% if not fast %}x{% endif %}').blocks[0]).toMatchObject({
      name: 'fast',
      negated: true,
    })
  })

  it('two sibling blocks are two blocks', () => {
    expect(parse('{% if a %}1{% endif %}{% if b %}2{% endif %}').blocks).toHaveLength(2)
  })

  it('{%% is an escape and never opens a tag', () => {
    const p = parse('write {%% if x %} literally')
    expect(p.escapes).toEqual([{ from: 6, to: 9, text: '{%' }])
    expect(p.blocks).toEqual([])
    expect(p.diagnostics).toEqual([])
  })
})

describe('parse — structural diagnostics', () => {
  const kinds = (body: string): string[] => parse(body).diagnostics.map((d) => d.kind)

  it('an unclosed block is reported at its opening', () => {
    expect(kinds('{% if x %}body')).toEqual(['unclosed-block'])
    expect(parse('{% if x %}body').diagnostics[0]).toMatchObject({ from: 0, to: 10 })
  })

  it('an endif with nothing open is reported', () => {
    expect(kinds('body{% endif %}')).toEqual(['stray-endif'])
  })

  it('a nested block is reported and is not supported', () => {
    expect(kinds('{% if a %}{% if b %}x{% endif %}{% endif %}')).toContain('nested-block')
  })

  it('a tag with no closing %} is reported', () => {
    expect(kinds('{% if x')).toEqual(['unterminated-tag'])
  })

  it('a tag that is neither if nor endif is reported', () => {
    expect(kinds('{% for x %}{% endif %}')).toContain('unknown-tag')
  })
})

describe('parse — fields', () => {
  it('a bare name is a text field with no default', () => {
    expect(parse('{{who}}').fields).toEqual([
      { name: 'who', kind: 'text', defaultValue: '', options: [], inside: null },
    ])
  })

  it('an option list is a select whose first option is the default', () => {
    expect(parse('{{w=claude|omp|codex}}').fields).toEqual([
      {
        name: 'w',
        kind: 'select',
        defaultValue: 'claude',
        options: ['claude', 'omp', 'codex'],
        inside: null,
      },
    ])
  })

  it('only the first = separates, so a default may contain one', () => {
    expect(parse('{{q=a=b}}').fields[0]).toMatchObject({ defaultValue: 'a=b', options: [] })
  })

  it('a name only ever named by a condition is a flag', () => {
    expect(parse('{% if fast %}go{% endif %}').fields).toEqual([
      { name: 'fast', kind: 'flag', defaultValue: '', options: [], inside: null },
    ])
  })

  it('a field inside a block carries the block it lives in', () => {
    expect(parse('{% if fast %}{{n=3}}{% endif %}').fields).toEqual([
      { name: 'fast', kind: 'flag', defaultValue: '', options: [], inside: null },
      {
        name: 'n',
        kind: 'text',
        defaultValue: '3',
        options: [],
        inside: { name: 'fast', negated: false },
      },
    ])
  })

  it('one entry per name, in first-occurrence order', () => {
    expect(parse('{{b}} {{a=1}} {{a}}').fields.map((f) => f.name)).toEqual(['b', 'a'])
  })

  it('a repeated use is not a redeclaration', () => {
    expect(parse('{{w=a|b}} again {{w}}').diagnostics).toEqual([])
  })
})

describe('parse — semantic diagnostics', () => {
  const kinds = (body: string): string[] => parse(body).diagnostics.map((d) => d.kind)

  it('a condition on a substituted name is refused, not read as always-true', () => {
    expect(kinds('{{worker}} {% if worker %}x{% endif %}')).toEqual(['condition-on-parameter'])
  })

  it('two declarations that disagree are refused', () => {
    expect(kinds('{{w=claude}} {{w=a|b}}')).toEqual(['conflicting-declaration'])
  })
})

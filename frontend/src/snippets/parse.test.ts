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

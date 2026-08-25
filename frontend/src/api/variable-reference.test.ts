// The renderer's copy of the backend's variable rule. Every case here is one
// the backend decides (internal/apicoll/substitute.go); the point of the test
// is that the two agree, because a highlight over text the backend sends
// literally is a lie told in the one place a person checks before sending.
import { describe, expect, it } from 'vitest'
import { findVariables, isVariableName, MAX_VAR_NAME_LENGTH } from './variable-reference'

describe('findVariables', () => {
  it('finds a reference and covers its braces', () => {
    expect(findVariables('{{baseUrl}}/zen')).toEqual([{ from: 0, to: 11, name: 'baseUrl' }])
  })

  it('finds every reference in order', () => {
    expect(findVariables('{{a}}/x/{{b}}').map((v) => v.name)).toEqual(['a', 'b'])
  })

  it('trims the name the way the backend does', () => {
    expect(findVariables('{{ baseUrl }}')).toEqual([{ from: 0, to: 13, name: 'baseUrl' }])
  })

  it('a JSON object is not a reference', () => {
    expect(findVariables('{{"a":1}}')).toEqual([])
  })

  it('an unterminated open brace is text', () => {
    expect(findVariables('{{baseUrl')).toEqual([])
  })

  it('resumes INSIDE a rejected pair, so a nested reference is still found', () => {
    // The backend writes the opening braces and carries on from after them;
    // without that rule the inner name would be swallowed by the outer pair.
    const found = findVariables('{{{{baseUrl}}')
    expect(found.map((v) => v.name)).toEqual(['baseUrl'])
    expect('{{{{baseUrl}}'.slice(found[0].from, found[0].to)).toBe('{{baseUrl}}')
  })

  it('a vault reference is not a variable — the colon is not in the set', () => {
    expect(findVariables('{{secret:TOKEN}}')).toEqual([])
  })

  it('an empty name is not a reference', () => {
    expect(findVariables('{{}}')).toEqual([])
  })
})

describe('isVariableName', () => {
  it('accepts the character set the backend accepts', () => {
    expect(isVariableName('base_Url-2.x')).toBe(true)
  })

  it('refuses a space, which is the typo the backend also sends literally', () => {
    expect(isVariableName('my token')).toBe(false)
  })

  it('refuses at the length cap and accepts one below it', () => {
    expect(isVariableName('a'.repeat(MAX_VAR_NAME_LENGTH))).toBe(true)
    expect(isVariableName('a'.repeat(MAX_VAR_NAME_LENGTH + 1))).toBe(false)
  })
})

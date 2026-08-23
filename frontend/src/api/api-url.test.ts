// The URL field and the parameter table are one fact in two shapes, and this
// is the derivation between them. Every case here is a rule api-url.ts
// states, exercised from the direction a person reaches it.
import { describe, expect, it } from 'vitest'
import { applyTypedUrl, foldQueryIntoParams, splitTypedUrl, urlWithParams } from './api-url'
import type { ApiParam, ApiRequest } from './api-model'

const p = (name: string, value: string, enabled = true): ApiParam => ({ name, value, enabled })

const request = (over: Partial<ApiRequest> = {}): ApiRequest => ({
  id: 'r',
  name: 'R',
  method: 'GET',
  url: '{{baseUrl}}/users',
  headers: [],
  query: [],
  variables: [],
  body: { kind: 'none', text: '', fileRef: '' },
  auth: { kind: 'none', token: '', password: '', user: '' },
  ...over,
})

describe('what the URL field shows', () => {
  it('puts the enabled parameters after the address', () => {
    expect(urlWithParams('{{baseUrl}}/users', [p('page', '2'), p('sort', 'name')])).toBe(
      '{{baseUrl}}/users?page=2&sort=name',
    )
  })

  it('leaves a disabled row out — it is a row the user keeps, not one they send', () => {
    expect(urlWithParams('/users', [p('page', '2'), p('debug', '1', false)])).toBe('/users?page=2')
  })

  it('writes a value-less parameter as the bare name, the way it was typed', () => {
    expect(urlWithParams('/users', [p('verbose', '')])).toBe('/users?verbose')
  })

  it('encodes nothing: {{variables}} must survive the field they are typed in', () => {
    expect(urlWithParams('{{baseUrl}}/s', [p('q', '{{term}} and more')])).toBe(
      '{{baseUrl}}/s?q={{term}} and more',
    )
  })

  it('shows no ? when nothing is enabled', () => {
    expect(urlWithParams('/users', [p('page', '2', false)])).toBe('/users')
  })
})

describe('what typing in the URL does', () => {
  it('makes a row per parameter', () => {
    const next = applyTypedUrl(request(), '/search?q=bruno&sort=stars')
    expect(next.url).toBe('/search')
    expect(next.query).toEqual([p('q', 'bruno'), p('sort', 'stars')])
  })

  it('keeps the rows a person switched off, at the foot', () => {
    const before = request({ query: [p('page', '2'), p('debug', '1', false)] })
    const next = applyTypedUrl(before, '/users?page=3')
    expect(next.query).toEqual([p('page', '3'), p('debug', '1', false)])
  })

  it('round-trips what it renders', () => {
    const typed = '/s?q=bruno&verbose&order=desc'
    const next = applyTypedUrl(request(), typed)
    expect(urlWithParams(next.url, next.query)).toBe(typed)
  })

  it('an address with no query clears the enabled rows', () => {
    const before = request({ query: [p('page', '2')] })
    expect(applyTypedUrl(before, '/users').query).toEqual([])
  })
})

describe('adopting a file that carries its query in the URL', () => {
  it('folds it into rows, in the order the wire already had', () => {
    const folded = foldQueryIntoParams(request({ url: '/s?a=1', query: [p('b', '2')] }))
    expect(folded.url).toBe('/s')
    expect(folded.query).toEqual([p('a', '1'), p('b', '2')])
  })

  it('leaves a request with no query in its URL exactly as it was', () => {
    const before = request({ query: [p('a', '1')] })
    // The SAME object: adopting must not report a file as edited, and the
    // store compares the draft with the saved snapshot by value.
    expect(foldQueryIntoParams(before)).toBe(before)
  })
})

describe('splitting', () => {
  it('takes the first ? and leaves later ones in the value', () => {
    expect(splitTypedUrl('/a?q=x?y')).toEqual({ base: '/a', pairs: [p('q', 'x?y')] })
  })

  it('ignores empty pieces from a trailing or doubled &', () => {
    expect(splitTypedUrl('/a?x=1&&').pairs).toEqual([p('x', '1')])
  })
})

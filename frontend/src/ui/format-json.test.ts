import { describe, expect, it } from 'vitest'
import { JSON_LAYOUT_LIMIT, layOutJSON } from './format-json'

/** The laid-out text, or a failure naming what came back instead — so a test
 *  about the text never reads `undefined` off a refusal and passes. */
function laidOut(text: string): string {
  const result = layOutJSON(text)
  if (result.kind !== 'laid-out') throw new Error(`expected a layout, got ${result.kind}`)
  return result.text
}

describe('laying out JSON', () => {
  it('puts one field on each line and indents the nesting', () => {
    const text = laidOut('{"email":"a@b.c","tags":["x","y"],"meta":{"n":1}}')
    expect(text.split('\n')[0]).toBe('{')
    expect(text).toContain('\n  "email": "a@b.c"')
    // Nesting is indented AGAIN, which is the whole reason a person asks for
    // this: depth is what a one-line document does not show.
    expect(text).toContain('\n    "n": 1')
  })

  // ONLY WHITESPACE MOVES. This is the assertion the request body depends on:
  // whatever is sent afterwards has to be the same document.
  it('changes only whitespace — the result parses to the same value', () => {
    const original = '{"b":2,"a":[1,{"c":null},true],"s":"  spaced  "}'
    expect(JSON.parse(laidOut(original))).toEqual(JSON.parse(original))
  })

  it('is idempotent: laying out a laid-out document moves nothing', () => {
    const once = laidOut('{"a":1,"b":[2,3]}')
    expect(laidOut(once)).toBe(once)
  })

  it('lays out the documents that are not objects, because JSON is not only objects', () => {
    expect(laidOut('[1,2]')).toBe('[\n  1,\n  2\n]')
    expect(laidOut('"just a string"')).toBe('"just a string"')
    expect(laidOut('null')).toBe('null')
  })

  // REFUSES RATHER THAN MANGLES. The caller is holding text a person is about
  // to send, so a best effort is the one answer that must not exist.
  it('refuses text that is not JSON, and says nothing about what it should be', () => {
    for (const text of ['', 'not json at all', "{'single':'quotes'}", '{"a":1,}', '{"a":1']) {
      expect(layOutJSON(text)).toEqual({ kind: 'unreadable' })
    }
  })

  // AND "TOO BIG" IS A DIFFERENT ANSWER FROM "NOT JSON", because a caller
  // says different things about them: one body is invalid and the other is
  // valid and expensive.
  it('refuses a document past the limit by name, not as an invalid one', () => {
    // Valid JSON, one byte over. Built rather than stated so the test cannot
    // drift from the constant it is about.
    const filler = 'x'.repeat(JSON_LAYOUT_LIMIT - '{"k":""}'.length + 1)
    const oversize = `{"k":"${filler}"}`
    expect(oversize.length).toBe(JSON_LAYOUT_LIMIT + 1)
    expect(layOutJSON(oversize)).toEqual({ kind: 'too-large', limit: JSON_LAYOUT_LIMIT })

    // And the same document one byte shorter is laid out, which is what keeps
    // the limit a limit rather than a refusal of everything large.
    const atLimit = `{"k":"${filler.slice(1)}"}`
    expect(atLimit.length).toBe(JSON_LAYOUT_LIMIT)
    expect(layOutJSON(atLimit).kind).toBe('laid-out')
  })

  it('names the threshold rather than hiding it in the check', () => {
    expect(JSON_LAYOUT_LIMIT).toBe(262144)
  })
})

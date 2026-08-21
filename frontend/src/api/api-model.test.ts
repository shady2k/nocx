// Walking the spans (design §11.1, §11.2).
//
// `Raw.spans` tile `Raw.text` in order with neither gap nor overlap, and the
// contract's own sentence is the acceptance criterion: "a renderer draws the
// whole payload by walking them". So the property under test is TOTALITY —
// what comes out of the walk reads as the whole text — and the three kinds
// stay three, because §11.1's middle row is the one that makes the feature
// safe rather than decorative.
//
// Two things the walker must survive that a naive `spans.map(slice)` does not:
// a side with nothing to mark (`spans: []`, which the contract calls the
// ordinary empty case) and a `from`/`to` that are BYTE offsets into UTF-8, not
// JavaScript string indices. A response body is arbitrary decoded text; if the
// walk sliced by UTF-16 code units, every span after the first non-ASCII
// character would land in the wrong place.
import { describe, expect, it } from 'vitest'
import { rawSegments } from './api-model'
import type { Raw } from '../generated/api.request.send'

/** What the walk reads as, end to end — the check that nothing was dropped.
 *  A secret run contributes its NAME, which is all a segment carries: there is
 *  no shape in which a value could be reached, so there is none to spell. */
const reading = (raw: Raw): string =>
  rawSegments(raw)
    .map((seg) => (seg.kind === 'text' ? seg.text : seg.name))
    .join('')

describe('rawSegments — the walk is total', () => {
  it('a side with nothing to mark still renders its whole text', () => {
    const raw: Raw = { text: 'HTTP/1.1 204 No Content\r\n\r\n', spans: [] }
    expect(rawSegments(raw)).toEqual([{ kind: 'text', text: 'HTTP/1.1 204 No Content\r\n\r\n' }])
    expect(reading(raw)).toBe(raw.text)
  })

  it('an empty side renders nothing at all', () => {
    expect(rawSegments({ text: '', spans: [] })).toEqual([])
  })

  it('spans that tile the text produce one segment each, in order', () => {
    const text = 'Authorization: Bearer <API_TOKEN>\r\n'
    const raw: Raw = {
      text,
      spans: [
        { from: 0, to: 22, kind: 'text', name: '', damage: '' },
        { from: 22, to: 33, kind: 'secret', name: 'API_TOKEN', damage: '' },
        { from: 33, to: 35, kind: 'text', name: '', damage: '' },
      ],
    }
    expect(rawSegments(raw)).toEqual([
      { kind: 'text', text: 'Authorization: Bearer ' },
      { kind: 'secret', name: 'API_TOKEN' },
      { kind: 'text', text: '\r\n' },
    ])
  })

  it('a damaged span carries the SHAPE of the damage and no bytes', () => {
    const text = 'Authorization: Bearer <API_TOKEN>'
    const raw: Raw = {
      text,
      spans: [
        { from: 0, to: 22, kind: 'text', name: '', damage: '' },
        {
          from: 22,
          to: 33,
          kind: 'secret-damaged',
          name: 'API_TOKEN',
          damage: 'truncated, 24 of 214 bytes',
        },
      ],
    }
    expect(rawSegments(raw)[1]).toEqual({
      kind: 'secret-damaged',
      name: 'API_TOKEN',
      damage: 'truncated, 24 of 214 bytes',
    })
  })

  // The two cases a backend can under-declare. Neither is allowed by the
  // contract, and both must lose nothing: text a renderer drops is text the
  // person needed, and the raw view is the one surface whose entire purpose
  // is to show everything.
  it('text before the first span is not dropped', () => {
    const raw: Raw = {
      text: 'GET / HTTP/1.1\r\n',
      spans: [{ from: 6, to: 16, kind: 'text', name: '', damage: '' }],
    }
    expect(reading(raw)).toBe('GET / HTTP/1.1\r\n')
  })

  it('text after the last span is not dropped', () => {
    const raw: Raw = {
      text: 'GET / HTTP/1.1\r\n',
      spans: [{ from: 0, to: 6, kind: 'text', name: '', damage: '' }],
    }
    expect(reading(raw)).toBe('GET / HTTP/1.1\r\n')
  })

  it('a gap between two spans is not dropped', () => {
    const raw: Raw = {
      text: 'abcdef',
      spans: [
        { from: 0, to: 2, kind: 'text', name: '', damage: '' },
        { from: 4, to: 6, kind: 'text', name: '', damage: '' },
      ],
    }
    expect(reading(raw)).toBe('abcdef')
  })

  it('offsets are BYTES, so a multi-byte character does not shift the spans', () => {
    // 'café' is five BYTES and four characters: slicing by string index would
    // put the secret one character early and split the é.
    const text = 'café: <TOKEN>'
    const encoder = new TextEncoder()
    const before = encoder.encode('café: ').length // 7 bytes, 6 characters
    const raw: Raw = {
      text,
      spans: [
        { from: 0, to: before, kind: 'text', name: '', damage: '' },
        {
          from: before,
          to: encoder.encode(text).length,
          kind: 'secret',
          name: 'TOKEN',
          damage: '',
        },
      ],
    }
    expect(rawSegments(raw)[0]).toEqual({ kind: 'text', text: 'café: ' })
    expect(rawSegments(raw)[1]).toEqual({ kind: 'secret', name: 'TOKEN' })
  })

  it('an out-of-range span is clamped rather than producing a bogus read', () => {
    const raw: Raw = {
      text: 'ab',
      spans: [{ from: 0, to: 99, kind: 'text', name: '', damage: '' }],
    }
    expect(reading(raw)).toBe('ab')
  })
})

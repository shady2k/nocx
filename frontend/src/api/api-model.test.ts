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
import { acceptedUntrusted, connectionRawText, rawSegments, untrustedSentence } from './api-model'
import type { Raw, Trust } from '../generated/api.request.send'

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

describe('connectionRawText', () => {
  const base = { remoteAddr: '', dnsAddresses: [] as string[] }

  it('prints what the resolver answered, under the address that took the call', () => {
    expect(
      connectionRawText({
        ...base,
        remoteAddr: '3.223.1.2:443',
        dnsAddresses: ['3.223.1.2', '54.10.0.9'],
      }),
    ).toBe('3.223.1.2:443\nresolved  3.223.1.2, 54.10.0.9')
  })

  it('says WHERE a name was resolved when it was not resolved here', () => {
    // A request routed through a connection cannot resolve on this side —
    // the far side does it — so an absent line would read as "we did not
    // look" instead of "it was not ours to look".
    expect(connectionRawText({ ...base, routedThrough: 'bastion' })).toBe(
      'resolved  on bastion, which is where this request left from',
    )
  })

  it('drops the zero address a tunnelled connection reports', () => {
    // `0.0.0.0:0` looks like an answer and is the absence of one.
    expect(connectionRawText({ ...base, remoteAddr: '0.0.0.0:0', routedThrough: 'bastion' })).toBe(
      'resolved  on bastion, which is where this request left from',
    )
    expect(connectionRawText({ ...base, remoteAddr: '[::]:0' })).toBe('')
  })

  it('keeps a real address that merely ends in a zero digit', () => {
    expect(connectionRawText({ ...base, remoteAddr: '10.0.0.10:8080' })).toBe('10.0.0.10:8080')
  })

  it('says nothing at all when there is nothing to say', () => {
    expect(connectionRawText(base)).toBe('')
  })
})

// ── What the run says about the chain it accepted (nocx-6hg2w.19) ──────────
//
// Four states, and only ONE of them is a warning. The badge used to be drawn
// from the environment's setting, which is true of every run under that
// environment — so it sat on a public host with an ordinary chain in the
// same words a self-signed development host would get. These are the three
// that must NOT produce one, and the one that must.

const CONNECTION = { remoteAddr: '10.0.3.17:443', dnsAddresses: [] as readonly string[] }

describe('acceptedUntrusted — the one state a warning is for', () => {
  const cases: Array<[Trust['state'], boolean]> = [
    ['none', false],
    ['verified', false],
    ['unchecked-trusted', false],
    ['unchecked-untrusted', true],
  ]
  for (const [state, want] of cases) {
    it(`${state} ${want ? 'warns' : 'does not warn'}`, () => {
      expect(acceptedUntrusted({ state, reason: '' })).toBe(want)
    })
  }

  it('a run with no response at all warns about nothing', () => {
    // A failed handshake carries no response, so there is no verdict — and
    // "no verdict" must read as "nothing to warn about" rather than throw.
    expect(acceptedUntrusted(undefined)).toBe(false)
  })
})

describe('untrustedSentence — what the badge says', () => {
  it("names the verifier's reason, without the package that spoke", () => {
    expect(
      untrustedSentence({
        state: 'unchecked-untrusted',
        reason: 'x509: certificate signed by unknown authority',
      }),
    ).toBe('unverified TLS — certificate signed by unknown authority')
  })

  it('says the bare thing when the backend gave no reason', () => {
    // Never an empty badge and never a dangling dash: a warning that says
    // nothing is the warning this change replaced.
    expect(untrustedSentence({ state: 'unchecked-untrusted', reason: '' })).toBe('unverified TLS')
  })
})

describe('connectionRawText — the quiet case is a fact, not a warning', () => {
  it('says verification was off when the chain would have passed anyway', () => {
    const text = connectionRawText({
      ...CONNECTION,
      tlsVersion: 'TLS 1.3',
      trust: { state: 'unchecked-trusted', reason: '' },
    })
    expect(text).toContain('TLS 1.3')
    expect(text).toContain('not checked')
    // …and it is stated as a fact rather than as an alarm: the words a
    // warning would use are absent.
    expect(text).not.toContain('unverified TLS')
  })

  it('says nothing extra for a chain that was verified', () => {
    const text = connectionRawText({
      ...CONNECTION,
      tlsVersion: 'TLS 1.3',
      trust: { state: 'verified', reason: '' },
    })
    expect(text).not.toContain('not checked')
  })

  it('says nothing extra for the state a badge already carries', () => {
    // The untrusted case is the badge's, and printing it twice would be two
    // owners of one sentence.
    const text = connectionRawText({
      ...CONNECTION,
      tlsVersion: 'TLS 1.3',
      trust: { state: 'unchecked-untrusted', reason: 'x509: certificate has expired' },
    })
    expect(text).not.toContain('not checked')
    expect(text).not.toContain('expired')
  })

  it('says nothing extra where there was no chain', () => {
    const text = connectionRawText({ ...CONNECTION, trust: { state: 'none', reason: '' } })
    expect(text).not.toContain('not checked')
  })
})

// A decoder refusal, in a person's words. Every case here is a rejection the
// strict decoder can produce for a request file, rendered the way a person
// reads it; the negative cases exist so the module's sentences stop being
// "the Go error, minus some punctuation" and start being sentences.
import { describe, expect, it } from 'vitest'
import { malformedReason } from './malformed-reason'

describe('a field the format does not know', () => {
  it('suggests the field an abbreviation was shortening', () => {
    // "var" — the owner's example — is `variables` cut short. No edit-distance
    // reaches it (six edits from spelling it out); its first letters match the
    // real field exactly. This is why the prefix rule exists.
    expect(malformedReason('json: unknown field "var"')).toBe(
      'The file uses a field the request format does not know: "var" — did you mean "variables"?',
    )
  })

  it('suggests the real field when the misspelling is one small edit away', () => {
    // The prefix rule must not have eaten the typo rule: "valur" prefixes no
    // field, and the edit-distance rule names "value" within two edits.
    expect(malformedReason('json: unknown field "valur"')).toBe(
      'The file uses a field the request format does not know: "valur" — did you mean "value"?',
    )
  })

  it("recognises a case-deviant abbreviation as the real field's eigen prefix", () => {
    // "FILEREF" is the start of `fileRef` in another case. The exact-match
    // check is case-INSENSITIVE, the same way the prefix rule is: so
    // `FILEREF` IS the real field `fileRef`, spelled case-deviantly — it is
    // not a guess at all, and gets the place-is-wrong sentence, not a
    // suggestion. This intentionally supersedes the earlier expectation that
    // `FILEREF` suggests `fileRef` (BRIEF-2's case-variation; BRIEF-3's
    // exact-match makes that unreachable by design).
    expect(malformedReason('json: unknown field "FILEREF"')).toBe(
      'The field "FILEREF" exists in the request format, but not at this point in the file.',
    )
  })

  it('does not pick one of two fields a guess prefixes', () => {
    // "va" is the beginning of both "value" and "variables", so naming either
    // would be a coin flip. It is not an exact match (no field is exactly
    // "va"), so the exact-match prior question does not catch it — and it is
    // two characters, so the LENGTH floor returns first, before either
    // suggestion rule runs. What is asserted is that a person gets no guess,
    // whichever fence answers.
    expect(malformedReason('json: unknown field "va"').includes('did you mean')).toBe(false)
  })

  it('names the field without inventing a suggestion when nothing is close', () => {
    expect(malformedReason('json: unknown field "totallyMadeUp"')).toBe(
      'The file uses a field the request format does not know: "totallyMadeUp".',
    )
  })

  it('gives a real field at the wrong place its own sentence, not a suggestion', () => {
    // The head page case: `name` is real SOMEWHERE but decodeStrict says it
    // is unknown HERE. The decoder never says which object — so the sentence
    // says the name is a real field and the place is wrong, without claiming
    // to know the place. And it must not suggest the field to itself.
    const sentence = malformedReason('json: unknown field "name"')
    expect(sentence).toBe(
      'The field "name" exists in the request format, but not at this point in the file.',
    )
    expect(sentence).not.toContain('did you mean')
  })
  it('names the real field when the same refusal names another real field', () => {
    // "enabled" is real at one nesting level (a header's / a param's), and is
    // refused at the point this file put it. The decoder's message never says
    // which object the field was in, so the test cannot claim one — it only
    // shows a second real field answering with the same depth-free sentence,
    // still never suggesting the field to itself.
    const sentence = malformedReason('json: unknown field "enabled"')
    expect(sentence).toBe(
      'The field "enabled" exists in the request format, but not at this point in the file.',
    )
    expect(sentence).not.toContain('did you mean')
  })

  it('answers `id` by the exact-match check, not by the length floor', () => {
    // `id` is real and two characters, so BOTH the length floor and the
    // exact-match prior question would silence a suggestion. The prior
    // question runs FIRST (before the floor), so the sentence is the
    // real-field place-is-wrong one, never a guess.
    expect(malformedReason('json: unknown field "id"')).toBe(
      'The field "id" exists in the request format, but not at this point in the file.',
    )
  })
})

describe('a cut-off file', () => {
  it('says the file is cut off or empty', () => {
    expect(malformedReason('unexpected end of JSON input')).toBe(
      'The file ends mid-way — it is cut off or empty.',
    )
  })
})

describe('a value of the wrong shape', () => {
  it('names the field and what it expects there, in Go-free words', () => {
    expect(
      malformedReason(
        'json: cannot unmarshal string into Go struct field Request.auth.enabled of type bool',
      ),
    ).toBe('The field "enabled" expects a true or false value here.')
  })

  it('names the text a string field wants when one is given', () => {
    expect(
      malformedReason(
        'json: cannot unmarshal number into Go struct field Request.body.text of type string',
      ),
    ).toBe('The field "text" expects text here.')
  })

  it('names a list field by its value', () => {
    expect(
      malformedReason(
        'json: cannot unmarshal string into Go struct field Request.headers.0.value of type []string',
      ),
    ).toBe('The field "value" expects a list of text here.')
  })
})

describe('a refusal that is already a sentence', () => {
  it('passes the symlink sentence through unchanged', () => {
    expect(malformedReason('not a regular file; symlinks are not followed')).toBe(
      'not a regular file; symlinks are not followed',
    )
  })

  it('passes the trailing-content sentence through unchanged', () => {
    // version.go writes this as a person's sentence, not a Go error.
    expect(malformedReason('trailing content after the JSON document')).toBe(
      'trailing content after the JSON document',
    )
  })
})

describe('an unrecognised reason', () => {
  it('answers with a neutral sentence that never pastes the raw text through', () => {
    expect(malformedReason('some random decoder complaint')).toBe(
      'This file could not be read as a request.',
    )
  })
})

describe('the gate', () => {
  it("never leaks a Go error's mechanics into a person's sentence", () => {
    const madeUp = [
      'json: unknown field "var"',
      'unexpected end of JSON input',
      'json: cannot unmarshal string into Go struct field Request.auth.enabled of type bool',
      'json: cannot unmarshal array into Go struct field Request.url of type string',
      'apicoll: list collection X: y',
      'not a regular file; symlinks are not followed',
      'trailing content after the JSON document',
      'some other error a future decoder might raise',
    ]
    const leaked = ['json:', 'Go struct', 'unmarshal', 'apicoll:']
    for (const reason of madeUp) {
      const sentence = malformedReason(reason)
      for (const fragment of leaked) {
        expect(sentence).not.toContain(fragment)
      }
    }
  })
})

describe('the empty string', () => {
  it('still answers with a sane sentence', () => {
    expect(malformedReason('')).toBe('This file could not be read as a request.')
  })
})

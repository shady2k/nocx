import { describe, it, expect } from 'vitest'
import { EFFECT_KEYS } from './policy-client'
import { EFFECT_LABEL, effectHeading } from './effect-labels'

describe('effect labels', () => {
  it('names every effect class the wire can carry', () => {
    for (const key of EFFECT_KEYS) {
      expect(EFFECT_LABEL[key]).toBeTruthy()
    }
  })

  it("speaks the product's words, never the enum's", () => {
    expect(EFFECT_LABEL.observe).toBe('read and inspect')
    expect(EFFECT_LABEL['mutate-destructive']).toBe('make changes that cannot be undone')
    for (const key of EFFECT_KEYS) {
      expect(EFFECT_LABEL[key]).not.toContain(key)
    }
  })

  // The canonical form is the mid-sentence one, because a heading is derivable
  // from it and the reverse is not: lower-casing a heading would maul a label
  // that ever begins with a proper noun. The prompt reads the label straight
  // ("wants to read and inspect"); the page asks for the heading.
  it('a heading is the same words, capitalised — one wording, two registers', () => {
    for (const key of EFFECT_KEYS) {
      const label = EFFECT_LABEL[key]
      expect(effectHeading(key)).toBe(label[0].toUpperCase() + label.slice(1))
    }
    expect(effectHeading('observe')).toBe('Read and inspect')
  })
})

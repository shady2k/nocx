import { describe, it, expect } from 'vitest'
import { EFFECT_KEYS } from './policy-client'
import { EFFECT_LABEL } from './effect-labels'

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

  // Every caller reads a label INSIDE a sentence, on both surfaces, so there
  // is one register and no capitalised variant to keep in step with it.
  it('reads mid-sentence, so no caller has to lower-case it', () => {
    for (const key of EFFECT_KEYS) {
      expect(EFFECT_LABEL[key][0]).toBe(EFFECT_LABEL[key][0].toLowerCase())
    }
  })
})

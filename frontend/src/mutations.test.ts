import { describe, expect, it } from 'vitest'

import {
  assessmentOf,
  isMutationMethod,
  newMutationIdentity,
  MutationNotReplayedError,
} from './mutations'

describe('mutation identity', () => {
  it('is minted per call and is unique', () => {
    const a = newMutationIdentity()
    const b = newMutationIdentity()
    expect(a).not.toBe(b)
    expect(a.length).toBeGreaterThan(8)
    expect(a.length).toBeLessThanOrEqual(128)
  })

  it('is carried only by a call the catalogue says may have changed something', () => {
    // The catalogue's own statement: a deadline leaves this outcome unknown.
    expect(isMutationMethod('settings.set')).toBe(true)
    expect(isMutationMethod('git.commit')).toBe(true)
    expect(isMutationMethod('vault.createSecret')).toBe(true)
    // Reads: their expiry says ask again, and nothing may have changed.
    expect(isMutationMethod('vault.status')).toBe(false)
    expect(isMutationMethod('profiles.list')).toBe(false)
    // And a method the catalogue does not know carries nothing.
    expect(isMutationMethod('test.not-a-method')).toBe(false)
  })
})

describe('the three answers a mutation gives a person', () => {
  it('says it did not start, and that nothing changed', () => {
    const assessment = assessmentOf({ outcome: 'not-started', operation: 'settings.set' })
    expect(assessment.checking).toBe(false)
    expect(assessment.sentence).toBe('It did not start — nothing was changed.')
  })

  it('says it finished', () => {
    const assessment = assessmentOf({ outcome: 'done', operation: 'settings.set' })
    expect(assessment.checking).toBe(false)
    expect(assessment.sentence).toBe('It finished.')
  })

  it('says it is checking, and that checking may go on', () => {
    const assessment = assessmentOf({ outcome: 'outcome-unknown', operation: 'git.commit' })
    expect(assessment.checking).toBe(true)
    expect(assessment.sentence).toBe('Checking what happened…')
  })

  it('names what to look at where checking ends, never a guess', () => {
    const assessment = assessmentOf({ outcome: 'unknown', operation: 'settings.set' })
    expect(assessment.checking).toBe(false)
    expect(assessment.sentence).toContain('The outcome is unknown')
    expect(assessment.sentence).toContain('Saving your settings')
    expect(assessment.sentence).toContain('before trying again')
  })

  it('says the same without an operation to name', () => {
    const assessment = assessmentOf({ outcome: 'unknown', operation: '' })
    expect(assessment.sentence).toContain('the change')
  })

  it('carries the assessment in the error a forbidden replay rejects with', () => {
    const assessment = assessmentOf({ outcome: 'done', operation: 'git.commit' })
    const error = new MutationNotReplayedError(assessment)
    expect(error.name).toBe('MutationNotReplayedError')
    expect(error.message).toBe(assessment.sentence)
    expect(error.assessment).toBe(assessment)
  })
})

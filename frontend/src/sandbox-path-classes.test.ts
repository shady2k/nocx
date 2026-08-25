import { describe, expect, it } from 'vitest'
import { classConflict, isWithin, writableSubsuming } from './sandbox-path-classes'

describe('isWithin', () => {
  it('recognises the directory itself and its descendants', () => {
    expect(isWithin('/a', '/a')).toBe(true)
    expect(isWithin('/a', '/a/b')).toBe(true)
    expect(isWithin('/', '/anything')).toBe(true)
  })

  it('does not mistake a shared prefix for containment', () => {
    expect(isWithin('/a', '/ab')).toBe(false)
    expect(isWithin('/a/b', '/a')).toBe(false)
  })
})

describe('writableSubsuming', () => {
  it('names the writable grant that swallows a read-only one', () => {
    expect(writableSubsuming('/a/b', ['/x', '/a'])).toBe('/a')
    expect(writableSubsuming('/a', ['/a'])).toBe('/a')
  })

  it('is silent when nothing contains it', () => {
    expect(writableSubsuming('/a', ['/b', '/a/c'])).toBeNull()
  })
})

describe('classConflict', () => {
  it('refuses a read-only folder inside a read & write one — the backend does', () => {
    expect(classConflict('readOnly', '/a/b', [], ['/a'])).toContain('/a')
  })

  it('refuses the same folder in both classes, either way round', () => {
    expect(classConflict('readOnly', '/a', [], ['/a'])).not.toBeNull()
    expect(classConflict('readWrite', '/a', ['/a'], [])).not.toBeNull()
  })

  // ADR-0039's whole point: a writable island in a read-only tree.
  it('permits a read & write folder inside a read-only one', () => {
    expect(classConflict('readWrite', '/a/b', ['/a'], [])).toBeNull()
  })

  it('permits unrelated folders', () => {
    expect(classConflict('readOnly', '/x', ['/y'], ['/z'])).toBeNull()
    expect(classConflict('readWrite', '/x', ['/y'], ['/z'])).toBeNull()
  })
})

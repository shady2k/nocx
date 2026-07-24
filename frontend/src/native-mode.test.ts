import { describe, it, expect } from 'vitest'
import { shouldShowEditor } from './native-mode'

describe('shouldShowEditor', () => {
  it('shows only when owned and not in native mode', () => {
    expect(shouldShowEditor(true, false)).toBe(true)
    expect(shouldShowEditor(true, true)).toBe(false) // escape latched
    expect(shouldShowEditor(false, false)).toBe(false)
  })
})

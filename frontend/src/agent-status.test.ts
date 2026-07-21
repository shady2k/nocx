import { describe, expect, it } from 'vitest'
import { detectAgentStatus } from './agent-status'

describe('detectAgentStatus', () => {
  it('reads a braille spinner frame as work in progress', () => {
    for (const frame of ['⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏']) {
      expect(detectAgentStatus(`${frame} Building the thing`)).toBe('working')
    }
  })

  it("reads Claude Code's ✳ prefix as waiting on the user", () => {
    expect(detectAgentStatus('✳ Продолжить работу')).toBe('idle')
    expect(detectAgentStatus('✳ Claude Code')).toBe('idle')
    expect(detectAgentStatus('✳')).toBe('idle')
  })

  it('prefers working over idle when a title carries both', () => {
    // A spinner frame is live evidence; a ✳ can linger in a title that has
    // not been repainted yet.
    expect(detectAgentStatus('⠹ ✳ still going')).toBe('working')
  })

  // Returning null is a real answer, not a default: a title that never
  // mentions an agent is not an idle agent, and the caller must fall back to
  // its own rules rather than reporting a state nobody claimed.
  it.each([
    ['a plain shell title', 'shady@wemosd1-1: ~/repos/nocx'],
    ['a bare path', '~/Documents/repos/nocx'],
    ['a program name', 'vim main.go'],
    ['an empty title', ''],
    ['whitespace only', '   '],
    ['an asterisk that is not the marker', '* not the claude marker'],
  ])('returns null for %s', (_label, title) => {
    expect(detectAgentStatus(title)).toBeNull()
  })
})

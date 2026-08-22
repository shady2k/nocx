import { describe, expect, it } from 'vitest'
import type { ActiveOrigin } from './pane-content'
import { shieldState } from './sandbox-shield'

const localOrigin: ActiveOrigin = {
  paneId: 1,
  sessionId: 'session-1',
  kind: 'local',
  cwd: '/repo',
  cwdVerified: true,
  cwdFollow: true,
  host: null,
}

const base = {
  enabled: true,
  statusAvailable: true,
  origin: localOrigin,
  sandboxed: false,
}

describe('shieldState', () => {
  it.each([
    [{ ...base, enabled: false }, { kind: 'hidden' }],
    [
      { ...base, statusAvailable: null },
      { kind: 'disabled', reason: 'Sandbox unavailable (status-unavailable)' },
    ],
    [
      { ...base, statusAvailable: false },
      { kind: 'disabled', reason: 'Sandbox unavailable (status-unavailable)' },
    ],
    [
      { ...base, origin: null },
      { kind: 'disabled', reason: 'Switch to a local tab to sandbox it' },
    ],
    [
      { ...base, origin: { ...localOrigin, kind: 'ssh' as const } },
      { kind: 'disabled', reason: 'Switch to a local tab to sandbox it' },
    ],
    [
      { ...base, origin: { ...localOrigin, cwdFollow: false } },
      { kind: 'disabled', reason: 'Switch to a local tab to sandbox it' },
    ],
    [
      { ...base, origin: { ...localOrigin, cwdVerified: false } },
      { kind: 'disabled', reason: 'Wait for the shell to report its folder' },
    ],
    [
      { ...base, origin: { ...localOrigin, cwd: null } },
      { kind: 'disabled', reason: 'Wait for the shell to report its folder' },
    ],
    [base, { kind: 'ready', workspace: '/repo', action: 'apply' }],
    [
      { ...base, enabled: false, statusAvailable: false, sandboxed: true },
      { kind: 'ready', workspace: '/repo', action: 'remove' },
    ],
  ])('maps eligibility %#', (input, expected) => {
    expect(shieldState(input)).toEqual(expected)
  })
})

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

describe('shieldState', () => {
  it.each([
    [{ enabled: false, statusAvailable: true, origin: localOrigin }, { kind: 'hidden' }],
    [
      { enabled: true, statusAvailable: null, origin: localOrigin },
      { kind: 'disabled', reason: 'Sandbox unavailable (status-unavailable)' },
    ],
    [
      { enabled: true, statusAvailable: false, origin: localOrigin },
      { kind: 'disabled', reason: 'Sandbox unavailable (status-unavailable)' },
    ],
    [
      { enabled: true, statusAvailable: true, origin: null },
      { kind: 'disabled', reason: 'Switch to a local tab to sandbox it' },
    ],
    [
      { enabled: true, statusAvailable: true, origin: { ...localOrigin, kind: 'ssh' as const } },
      { kind: 'disabled', reason: 'Switch to a local tab to sandbox it' },
    ],
    [
      { enabled: true, statusAvailable: true, origin: { ...localOrigin, cwdFollow: false } },
      { kind: 'disabled', reason: 'Switch to a local tab to sandbox it' },
    ],
    [
      { enabled: true, statusAvailable: true, origin: { ...localOrigin, cwdVerified: false } },
      { kind: 'disabled', reason: 'Wait for the shell to report its folder' },
    ],
    [
      { enabled: true, statusAvailable: true, origin: { ...localOrigin, cwd: null } },
      { kind: 'disabled', reason: 'Wait for the shell to report its folder' },
    ],
    [
      { enabled: true, statusAvailable: true, origin: localOrigin },
      { kind: 'ready', workspace: '/repo' },
    ],
  ])('maps eligibility %#', (input, expected) => {
    expect(shieldState(input)).toEqual(expected)
  })
})

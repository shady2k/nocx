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
  status: { available: true },
  origin: localOrigin,
  sandboxed: false,
}

describe('shieldState', () => {
  it.each([
    [{ ...base, enabled: false }, { kind: 'hidden' }],
    [
      { ...base, status: null },
      { kind: 'disabled', reason: 'Sandbox unavailable (status-unavailable)' },
    ],
    [
      { ...base, status: { available: false, reason: 'landlock-abi-too-old' } },
      { kind: 'disabled', reason: 'Sandbox unavailable (landlock-abi-too-old)' },
    ],
    [
      {
        ...base,
        status: {
          available: false,
          reason: 'landlock-abi-too-old',
          detail: 'kernel Landlock ABI 2 is below the required floor of 3',
        },
      },
      {
        kind: 'disabled',
        reason: 'Sandbox unavailable (kernel Landlock ABI 2 is below the required floor of 3)',
      },
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
      { ...base, enabled: false, status: { available: false }, sandboxed: true },
      { kind: 'ready', workspace: '/repo', action: 'remove' },
    ],
  ])('maps eligibility %#', (input, expected) => {
    expect(shieldState(input)).toEqual(expected)
  })
})

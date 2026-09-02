import { describe, expect, it } from 'vitest'
import type { ActiveOrigin } from './pane-content'
import { shieldState } from './sandbox-shield'
import type { SandboxStatus } from './ipc'

const localOrigin: ActiveOrigin = {
  paneId: 1,
  sessionId: 'session-1',
  kind: 'local',
  cwd: '/repo',
  cwdVerified: true,
  cwdFollow: true,
  host: null,
}

const availableStatus: SandboxStatus = {
  learn: { available: true, backend: 'landlock', state: 'available', coverage: [] },
  enforce: { available: true, backend: 'landlock', state: 'available', coverage: [] },
}

const base = {
  enabled: true,
  status: availableStatus,
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
      {
        ...base,
        status: {
          ...availableStatus,
          enforce: {
            ...availableStatus.enforce,
            available: false,
            reason: 'landlock-abi-too-old',
            state: 'unavailable' as const,
          },
        },
      },
      { kind: 'disabled', reason: 'Sandbox unavailable (landlock-abi-too-old)' },
    ],
    [
      {
        ...base,
        status: {
          ...availableStatus,
          enforce: {
            ...availableStatus.enforce,
            available: false,
            reason: 'landlock-abi-too-old',
            detail: 'kernel Landlock ABI 2 is below the required floor of 3',
            state: 'unavailable' as const,
          },
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
      { ...base, enabled: false, sandboxed: true },
      { kind: 'ready', workspace: '/repo', action: 'remove' },
    ],
  ])('maps eligibility %#', (input, expected) => {
    expect(shieldState(input)).toEqual(expected)
  })
})

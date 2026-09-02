import type { ActiveOrigin } from './pane-content'
import type { SandboxStatus } from './ipc'

export interface ShieldStateInput {
  enabled: boolean
  status: SandboxStatus | null
  origin: ActiveOrigin | null
  sandboxed: boolean
}

export type ShieldState =
  | { kind: 'hidden' }
  | { kind: 'disabled'; reason: string }
  | { kind: 'ready'; workspace: string; action: 'apply' | 'remove' }

export function shieldState(input: ShieldStateInput): ShieldState {
  if (!input.enabled && !input.sandboxed) return { kind: 'hidden' }
  if (input.origin?.kind !== 'local' || !input.origin.cwdFollow) {
    return { kind: 'disabled', reason: 'Switch to a local tab to sandbox it' }
  }
  if (!input.origin.cwdVerified || !input.origin.cwd) {
    return { kind: 'disabled', reason: 'Wait for the shell to report its folder' }
  }
  if (input.sandboxed) {
    return { kind: 'ready', workspace: input.origin.cwd, action: 'remove' }
  }
  const availability = input.status?.enforce
  if (availability?.available !== true) {
    const reason = availability?.detail || availability?.reason || 'status-unavailable'
    return { kind: 'disabled', reason: `Sandbox unavailable (${reason})` }
  }
  return { kind: 'ready', workspace: input.origin.cwd, action: 'apply' }
}

import type { ActiveOrigin } from './pane-content'

export interface ShieldStateInput {
  enabled: boolean
  statusAvailable: boolean | null
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
  if (input.statusAvailable !== true) {
    return { kind: 'disabled', reason: 'Sandbox unavailable (status-unavailable)' }
  }
  return { kind: 'ready', workspace: input.origin.cwd, action: 'apply' }
}

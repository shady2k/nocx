import type { ActiveOrigin } from './pane-content'

export interface ShieldStateInput {
  enabled: boolean
  statusAvailable: boolean | null
  origin: ActiveOrigin | null
}

export type ShieldState =
  { kind: 'hidden' } | { kind: 'disabled'; reason: string } | { kind: 'ready'; workspace: string }

export function shieldState(input: ShieldStateInput): ShieldState {
  if (!input.enabled) return { kind: 'hidden' }
  if (input.statusAvailable !== true) {
    return { kind: 'disabled', reason: 'Sandbox unavailable (status-unavailable)' }
  }
  if (input.origin?.kind !== 'local' || !input.origin.cwdFollow) {
    return { kind: 'disabled', reason: 'Switch to a local tab to sandbox it' }
  }
  if (!input.origin.cwdVerified || !input.origin.cwd) {
    return { kind: 'disabled', reason: 'Wait for the shell to report its folder' }
  }
  return { kind: 'ready', workspace: input.origin.cwd }
}

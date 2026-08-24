/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/sandbox.access.resolve.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Resolved sandbox access event returned after dismissing it or atomically promoting its backend-owned directory to the event's workspace profile.
 */
export interface SandboxAccessResolve {
  id: string
  sessionId: string
  instanceId: string
  sessionEpoch: number
  paneId: string
  workspaceId: string
  shell?: string
  executable?: string
  path: string
  directory: string
  canGrant: boolean
  grantReason?: string
  access: 'readOnly' | 'readWrite'
  operation?: string
  source: 'linux-seccomp-user-notify' | 'darwin-seatbelt-log'
  firstSeen: string
  lastSeen: string
  count: number
  state: 'pending' | 'dismissed' | 'granted' | 'expired'
  decision?: 'dismiss' | 'workspaceReadOnly' | 'workspaceReadWrite'
  profileRevision?: number
}

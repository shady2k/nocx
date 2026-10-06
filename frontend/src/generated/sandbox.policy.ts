/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/sandbox.policy.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

export interface SandboxPolicy {
  version: number
  backend: 'linux-landlock' | 'macos-seatbelt'
  backendVersion: number
  workspaceId: string
  workspaceRoot: string
  standardRevision: number
  workspaceRevision: number
  shell: string
  runner: string
  runtime: {
    root: string
    home: string
    config: string
    data: string
    cache: string
    state: string
    temp: string
  }
  roots: {
    path: string
    access: 'ro' | 'rw'
    kind: 'directory' | 'artifact' | 'device'
    provenance:
      | 'workspace'
      | 'standard'
      | 'workspace-profile'
      | 'launch-delta'
      | 'git'
      | 'runtime'
      | 'system'
      | 'dependency'
      | 'trusted-artifact'
      | 'writable-device'
    identity: {
      device: number
      inode: number
    }
  }[]
}

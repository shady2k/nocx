/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/sandbox.workspace.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */
import type { SandboxProfileRoots } from './sandbox.roots'

export interface SandboxWorkspaceProfile {
  workspaceId: string
  revision: number
  override: SandboxProfileRoots | null
}

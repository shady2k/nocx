/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/sandbox.profile.get.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Effective sandbox profile for the workspace of the pane named in the request (design 2026-08-23 §4.3). The backend resolves pane → workspace and reports either an explicit workspace profile (source=workspace) or the inherited standard profile (source=standard, inherited=true). Paths are PrivateMetadata and appear only in this explicit result.
 */
export interface SandboxProfileGet {
  /**
   * The layout workspace the pane belongs to, resolved by the backend.
   */
  workspaceId: string
  /**
   * Which profile sourced these roots: an explicit workspace profile, or the inherited standard profile.
   */
  source: 'standard' | 'workspace'
  /**
   * The optimistic-concurrency token for the reported profile: the settings revision for a standard source, the per-workspace sandboxProfile.revision for a workspace source.
   */
  revision: number
  /**
   * Whether the workspace falls back to the standard profile rather than carrying an explicit one.
   */
  inherited: boolean
  /**
   * Canonical existing directories granted read-write in addition to the workspace.
   */
  writablePaths: string[]
  /**
   * Canonical existing directories granted read-only.
   */
  readOnlyPaths: string[]
}

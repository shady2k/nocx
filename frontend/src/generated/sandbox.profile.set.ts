/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/sandbox.profile.set.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the sandbox.profile.set JSON-RPC method: the workspace's newly written explicit profile (design 2026-08-23 §4.3). The revision is the new per-workspace optimistic-concurrency token. Paths are PrivateMetadata and appear only in this explicit result.
 */
export interface SandboxProfileSet {
  /**
   * The named workspace whose profile was written.
   */
  workspaceId: string
  /**
   * The new monotonic per-workspace revision after the write.
   */
  revision: number
  /**
   * The canonicalized, validated read-write paths as stored.
   */
  writablePaths: string[]
  /**
   * The canonicalized, validated read-only paths as stored.
   */
  readOnlyPaths: string[]
}

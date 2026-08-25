/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/sandbox.profile.delete.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the sandbox.profile.delete JSON-RPC method: the named workspace whose explicit profile was removed, returning it to live standard-profile inheritance (design 2026-08-23 §7). Running grants are unaffected.
 */
export interface SandboxProfileDelete {
  /**
   * The named workspace returned to standard-profile inheritance.
   */
  workspaceId: string
}

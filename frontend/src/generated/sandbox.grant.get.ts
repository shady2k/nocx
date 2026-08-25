/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/sandbox.grant.get.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the sandbox.grant.get JSON-RPC method: the immutable grant minted for the named pane, or null when the pane carries none (an ordinary tab). realized is the enforced policy; provenance records which profile sourced it. Paths are PrivateMetadata and appear only in this explicit result.
 */
export type SandboxGrantGet = null | {
  /**
   * Backend wall clock when the grant was minted, in Unix milliseconds.
   */
  issuedAt: number
  /**
   * The realized enforcement metadata surfaced to the tab.
   */
  realized: {
    backend: 'landlock' | 'seatbelt'
    workspace: string
    writableRoots: string[]
    readOnlyRoots: string[]
    /**
     * @maxItems 129
     */
    homeProjections: {
      hostPath: string
      relativePath: string
    }[]
  }
  /**
   * Which profile sourced this grant. profileRevision is the settings revision for a standard profile, the workspace revision for a workspace profile, and null only for a legacy grant.
   */
  provenance: {
    workspaceId: string
    profileSource: 'standard' | 'workspace' | 'legacy'
    profileRevision: number | null
  }
}

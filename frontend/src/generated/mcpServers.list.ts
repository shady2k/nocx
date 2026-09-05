/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/mcpServers.list.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Bounded MCP server summaries without tool schemas, secret material, or opaque secret references.
 */
export interface MCPServersListResult {
  /**
   * @maxItems 64
   */
  servers: Summary[]
}
export interface Summary {
  id: string
  revision: number
  name: string
  enabled: boolean
  transport: 'stdio' | 'streamable-http'
  catalogState: 'missing' | 'fresh' | 'stale'
  toolCount: number
  enabledToolCount: number
  oauthStatus: 'missing' | 'connected' | 'expired' | null
}

/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/mcpServers.changed.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Lightweight invalidation notification. It deliberately carries no configuration, catalog schemas, or secret metadata.
 */
export interface MCPServersChangedParams {
  id: string
  revision: number
  change: 'created' | 'updated' | 'deleted' | 'catalog' | 'tools' | 'oauth'
}

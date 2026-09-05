/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/mcpServers.get.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * One editable MCP server and its persisted catalog. Secret material and opaque secret references never appear in this shape.
 */
export interface MCPServersGetResult {
  server: Server
}
export interface Server {
  id: string
  revision: number
  name: string
  enabled: boolean
  transport: 'stdio' | 'streamable-http'
  stdio: Stdio | null
  http: Http | null
  limits: Limits
  catalog: Catalog
}
export interface Stdio {
  command: string
  argv: string[]
  cwd: string
  env: {
    name: string
    value: ValueBinding
  }[]
}
export interface ValueBinding {
  kind: 'literal' | 'secret'
  literal: string | null
  secretSet: boolean
  owned: boolean
}
export interface Http {
  endpoint: string
  auth: 'none' | 'bearer' | 'oauth'
  headers: {
    name: string
    value: ValueBinding
  }[]
  bearer: SecretBinding
  oauth: Oauth | null
}
export interface SecretBinding {
  secretSet: boolean
  owned: boolean
}
export interface Oauth {
  registration: 'dynamic' | 'preregistered'
  clientId: string
  clientSecret: SecretBinding
  scopes: string[]
  sessionSet: boolean
  status: 'missing' | 'connected' | 'expired'
  issuer: string
  grantedScopes: string[]
  accessTokenExpires: string | null
}
export interface Limits {
  startupTimeoutMs: number
  callTimeoutMs: number
  idleTimeoutMs: number
  maxResultBytes: number
}
export interface Catalog {
  state: 'missing' | 'fresh' | 'stale'
  serverName: string
  serverVersion: string
  protocolVersion: string
  refreshedAt: string | null
  digest: string
  tools: Tool[]
}
export interface Tool {
  name: string
  description: string
  inputSchema: {}
  outputSchema: {} | null
  descriptorDigest: string
  enabled: boolean
  status: 'unchanged' | 'new' | 'changed'
}

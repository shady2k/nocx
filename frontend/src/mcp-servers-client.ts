import { Dispatcher } from './dispatcher'
import type { MCPServersListResult, Summary } from './generated/mcpServers.list'
import type { MCPServersGetResult, Server, ValueBinding } from './generated/mcpServers.get'
import type { MCPServersCreateResult } from './generated/mcpServers.create'
import type { MCPServersUpdateResult } from './generated/mcpServers.update'
import type { MCPServersDeleteResult } from './generated/mcpServers.delete'
import type { MCPServersRefreshResult } from './generated/mcpServers.refresh'
import type { MCPServersSetToolsEnabledResult } from './generated/mcpServers.setToolsEnabled'
import type { MCPServersOAuthAuthorizeResult } from './generated/mcpServers.oauthAuthorize'
import type { MCPServersOAuthForgetResult } from './generated/mcpServers.oauthForget'
import type * as MCPChanged from './generated/mcpServers.changed'
import type * as MCPCreate from './generated/mcpServers.create'
import type * as MCPDelete from './generated/mcpServers.delete'
import type * as MCPGet from './generated/mcpServers.get'
import type * as MCPOAuthAuthorize from './generated/mcpServers.oauthAuthorize'
import type * as MCPOAuthForget from './generated/mcpServers.oauthForget'
import type * as MCPRefresh from './generated/mcpServers.refresh'
import type * as MCPSetToolsEnabled from './generated/mcpServers.setToolsEnabled'
import type * as MCPUpdate from './generated/mcpServers.update'

type MCPGeneratedContractTypes =
  | MCPChanged.MCPServersChangedParams
  | MCPCreate.Catalog
  | MCPCreate.Http
  | MCPCreate.Limits
  | MCPCreate.Oauth
  | MCPCreate.SecretBinding
  | MCPCreate.Server
  | MCPCreate.Stdio
  | MCPCreate.Tool
  | MCPCreate.ValueBinding
  | MCPDelete.MCPServersDeleteResult
  | MCPGet.Catalog
  | MCPGet.Http
  | MCPGet.Limits
  | MCPGet.Oauth
  | MCPGet.SecretBinding
  | MCPGet.Server
  | MCPGet.Stdio
  | MCPGet.Tool
  | MCPGet.ValueBinding
  | MCPOAuthAuthorize.Catalog
  | MCPOAuthAuthorize.Http
  | MCPOAuthAuthorize.Limits
  | MCPOAuthAuthorize.Oauth
  | MCPOAuthAuthorize.SecretBinding
  | MCPOAuthAuthorize.Server
  | MCPOAuthAuthorize.Stdio
  | MCPOAuthAuthorize.Tool
  | MCPOAuthAuthorize.ValueBinding
  | MCPOAuthForget.Catalog
  | MCPOAuthForget.Http
  | MCPOAuthForget.Limits
  | MCPOAuthForget.Oauth
  | MCPOAuthForget.SecretBinding
  | MCPOAuthForget.Server
  | MCPOAuthForget.Stdio
  | MCPOAuthForget.Tool
  | MCPOAuthForget.ValueBinding
  | MCPRefresh.Catalog
  | MCPRefresh.Http
  | MCPRefresh.Limits
  | MCPRefresh.Oauth
  | MCPRefresh.SecretBinding
  | MCPRefresh.Server
  | MCPRefresh.Stdio
  | MCPRefresh.Tool
  | MCPRefresh.ValueBinding
  | MCPSetToolsEnabled.Catalog
  | MCPSetToolsEnabled.Http
  | MCPSetToolsEnabled.Limits
  | MCPSetToolsEnabled.Oauth
  | MCPSetToolsEnabled.SecretBinding
  | MCPSetToolsEnabled.Server
  | MCPSetToolsEnabled.Stdio
  | MCPSetToolsEnabled.Tool
  | MCPSetToolsEnabled.ValueBinding
  | MCPUpdate.Catalog
  | MCPUpdate.Http
  | MCPUpdate.Limits
  | MCPUpdate.Oauth
  | MCPUpdate.SecretBinding
  | MCPUpdate.Server
  | MCPUpdate.Stdio
  | MCPUpdate.Tool
  | MCPUpdate.ValueBinding

type MCPGeneratedContractWitness = Partial<Record<never, MCPGeneratedContractTypes>>

export type MCPServerSummary = Summary
export type MCPServer = Server & MCPGeneratedContractWitness
export type MCPValueBinding = ValueBinding

export interface MCPSecretBindingInput {
  secret: string | null
  secretValue: string | null
  keep: boolean
}

export interface MCPValueBindingInput extends MCPSecretBindingInput {
  kind: 'literal' | 'secret'
  literal: string | null
}

export interface MCPServerWrite {
  name: string
  enabled: boolean
  transport: 'stdio' | 'streamable-http'
  stdio: {
    command: string
    argv: string[]
    cwd: string
    env: { name: string; value: MCPValueBindingInput }[]
  } | null
  http: {
    endpoint: string
    auth: 'none' | 'bearer' | 'oauth'
    headers: { name: string; value: MCPValueBindingInput }[]
    bearer: MCPSecretBindingInput | null
    oauth: {
      registration: 'dynamic' | 'preregistered'
      clientId: string
      clientSecret: MCPSecretBindingInput | null
      scopes: string[]
    } | null
  } | null
  limits: {
    startupTimeoutMs: number
    callTimeoutMs: number
    idleTimeoutMs: number
    maxResultBytes: number
  }
}

/** Typed JSON-RPC client for the MCP Settings control plane. */
export class MCPServerClient {
  constructor(private readonly dispatcher: Dispatcher) {}
  subscribeChanged(handler: (params: MCPChanged.MCPServersChangedParams) => void): () => void {
    return this.dispatcher.subscribe('mcpServers.changed', (params) => {
      handler(params as MCPChanged.MCPServersChangedParams)
    })
  }

  onConnect(handler: () => void): () => void {
    return this.dispatcher.onConnect(handler)
  }

  list(): Promise<MCPServerSummary[]> {
    return this.dispatcher
      .call<MCPServersListResult>('mcpServers.list', {})
      .then((result) => result.servers)
  }

  get(id: string): Promise<MCPServer> {
    return this.dispatcher
      .call<MCPServersGetResult>('mcpServers.get', { id })
      .then((result) => result.server)
  }

  create(input: MCPServerWrite): Promise<MCPServer> {
    return this.dispatcher
      .call<MCPServersCreateResult>('mcpServers.create', input)
      .then((result) => result.server)
  }

  update(id: string, revision: number, input: MCPServerWrite): Promise<MCPServer> {
    return this.dispatcher
      .call<MCPServersUpdateResult>('mcpServers.update', { id, revision, ...input })
      .then((result) => result.server)
  }

  delete(id: string, revision: number): Promise<MCPServersDeleteResult> {
    return this.dispatcher.call<MCPServersDeleteResult>('mcpServers.delete', { id, revision })
  }

  refresh(id: string, revision: number): Promise<MCPServer> {
    return this.dispatcher
      .call<MCPServersRefreshResult>('mcpServers.refresh', { id, revision })
      .then((result) => result.server)
  }

  setToolsEnabled(id: string, revision: number, tools: string[]): Promise<MCPServer> {
    return this.dispatcher
      .call<MCPServersSetToolsEnabledResult>('mcpServers.setToolsEnabled', {
        id,
        revision,
        tools,
      })
      .then((result) => result.server)
  }

  oauthAuthorize(id: string, revision: number): Promise<MCPServer> {
    return this.dispatcher
      .call<MCPServersOAuthAuthorizeResult>('mcpServers.oauthAuthorize', { id, revision })
      .then((result) => result.server)
  }

  oauthForget(id: string, revision: number): Promise<MCPServer> {
    return this.dispatcher
      .call<MCPServersOAuthForgetResult>('mcpServers.oauthForget', { id, revision })
      .then((result) => result.server)
  }
}

import type {
  MCPServer,
  MCPServerSummary,
  MCPServerWrite,
  MCPSecretBindingInput,
  MCPValueBinding,
  MCPValueBindingInput,
} from './mcp-servers-client'
import { boundSecret } from './secret-reference'

export interface BindingDraft {
  name: string
  kind: 'literal' | 'secret'
  value: string
  keep: boolean
}

export interface ServerDraft {
  name: string
  enabled: boolean
  transport: 'stdio' | 'streamable-http'
  command: string
  argv: { value: string }[]
  cwd: string
  env: BindingDraft[]
  endpoint: string
  auth: 'none' | 'bearer' | 'oauth'
  headers: BindingDraft[]
  bearer: string
  keepBearer: boolean
  registration: 'dynamic' | 'preregistered'
  clientId: string
  clientSecret: string
  keepClientSecret: boolean
  scopes: { value: string }[]
  startupTimeoutMs: string
  callTimeoutMs: string
  idleTimeoutMs: string
  maxResultBytes: string
}

export const blankDraft = (): ServerDraft => ({
  name: '',
  enabled: true,
  transport: 'stdio',
  command: '',
  argv: [],
  cwd: '',
  env: [],
  endpoint: '',
  auth: 'none',
  headers: [],
  bearer: '',
  keepBearer: false,
  registration: 'dynamic',
  clientId: '',
  clientSecret: '',
  keepClientSecret: false,
  scopes: [],
  startupTimeoutMs: '10000',
  callTimeoutMs: '60000',
  idleTimeoutMs: '30000',
  maxResultBytes: '262144',
})

function bindingDraft(name: string, value: MCPValueBinding): BindingDraft {
  return {
    name,
    kind: value.kind,
    value: value.kind === 'literal' ? (value.literal ?? '') : '',
    keep: value.kind === 'secret' && value.secretSet,
  }
}

export function fromServer(server: MCPServer): ServerDraft {
  const draft = blankDraft()
  draft.name = server.name
  draft.enabled = server.enabled
  draft.transport = server.transport
  draft.startupTimeoutMs = String(server.limits.startupTimeoutMs)
  draft.callTimeoutMs = String(server.limits.callTimeoutMs)
  draft.idleTimeoutMs = String(server.limits.idleTimeoutMs)
  draft.maxResultBytes = String(server.limits.maxResultBytes)
  if (server.stdio) {
    draft.command = server.stdio.command
    draft.argv = server.stdio.argv.map((value) => ({ value }))
    draft.cwd = server.stdio.cwd
    draft.env = server.stdio.env.map((row) => bindingDraft(row.name, row.value))
  }
  if (server.http) {
    draft.endpoint = server.http.endpoint
    draft.auth = server.http.auth
    draft.headers = server.http.headers.map((row) => bindingDraft(row.name, row.value))
    draft.keepBearer = server.http.bearer.secretSet
    if (server.http.oauth) {
      draft.registration = server.http.oauth.registration
      draft.clientId = server.http.oauth.clientId
      draft.keepClientSecret = server.http.oauth.clientSecret.secretSet
      draft.scopes = server.http.oauth.scopes.map((value) => ({ value }))
    }
  }
  return draft
}

function secretInput(value: string, keep: boolean): MCPSecretBindingInput {
  const reference = boundSecret(value)
  if (reference !== undefined) return { secret: reference, secretValue: null, keep: false }
  if (value !== '') return { secret: null, secretValue: value, keep: false }
  return { secret: null, secretValue: null, keep }
}

function valueInput(row: BindingDraft): MCPValueBindingInput {
  if (row.kind === 'literal') {
    return { kind: 'literal', literal: row.value, secret: null, secretValue: null, keep: false }
  }
  return { kind: 'secret', literal: null, ...secretInput(row.value, row.keep) }
}

export function toWrite(draft: ServerDraft): MCPServerWrite {
  const limits = {
    startupTimeoutMs: Number(draft.startupTimeoutMs),
    callTimeoutMs: Number(draft.callTimeoutMs),
    idleTimeoutMs: Number(draft.idleTimeoutMs),
    maxResultBytes: Number(draft.maxResultBytes),
  }
  if (draft.transport === 'stdio') {
    return {
      name: draft.name.trim(),
      enabled: draft.enabled,
      transport: 'stdio',
      stdio: {
        command: draft.command.trim(),
        argv: draft.argv.map((row) => row.value),
        cwd: draft.cwd.trim(),
        env: draft.env.map((row) => ({ name: row.name.trim(), value: valueInput(row) })),
      },
      http: null,
      limits,
    }
  }
  const oauth =
    draft.auth === 'oauth'
      ? {
          registration: draft.registration,
          clientId: draft.registration === 'preregistered' ? draft.clientId.trim() : '',
          clientSecret:
            draft.registration === 'preregistered'
              ? secretInput(draft.clientSecret, draft.keepClientSecret)
              : null,
          scopes: draft.scopes.map((row) => row.value.trim()).filter(Boolean),
        }
      : null
  return {
    name: draft.name.trim(),
    enabled: draft.enabled,
    transport: 'streamable-http',
    stdio: null,
    http: {
      endpoint: draft.endpoint.trim(),
      auth: draft.auth,
      headers: draft.headers.map((row) => ({ name: row.name.trim(), value: valueInput(row) })),
      bearer: draft.auth === 'bearer' ? secretInput(draft.bearer, draft.keepBearer) : null,
      oauth,
    },
    limits,
  }
}

export function summaryOf(server: MCPServer): MCPServerSummary {
  return {
    id: server.id,
    revision: server.revision,
    name: server.name,
    enabled: server.enabled,
    transport: server.transport,
    catalogState: server.catalog.state,
    toolCount: server.catalog.tools.length,
    enabledToolCount: server.catalog.tools.filter((tool) => tool.enabled).length,
    oauthStatus: server.http?.oauth?.status ?? null,
  }
}

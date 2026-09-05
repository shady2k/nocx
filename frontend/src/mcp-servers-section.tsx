import { For, Show, createMemo, createSignal, onCleanup, onMount, untrack } from 'solid-js'
import { RpcError } from './dispatcher'
import {
  MCPServerClient,
  type MCPServer,
  type MCPServerSummary,
  type MCPServerWrite,
  type MCPSecretBindingInput,
  type MCPValueBinding,
  type MCPValueBindingInput,
} from './mcp-servers-client'
import { SecretTextField, secretMarks } from './api/secret-text-field'
import { boundSecret } from './secret-reference'
import type { VaultController } from './vault'
import type { SecretPickerSource } from './ui/secret-picker'
import { Button } from './ui/button'
import { Checkbox } from './ui/checkbox'
import { CollectionView } from './ui/collection-view'
import { Dialog, showConfirm } from './ui/dialog'
import { EditableRowList } from './ui/row-list'
import { EmptyState } from './ui/empty-state'
import { Field } from './ui/field'
import { RecordRow } from './ui/record-row'
import { Section } from './ui/section'
import { Select } from './ui/select'
import { Spinner } from './ui/spinner'
import { Stack } from './ui/stack'
import { StatusCard } from './ui/status-card'
import { TextField } from './ui/text-field'
import { showToast } from './ui/toast'

interface BindingDraft {
  name: string
  kind: 'literal' | 'secret'
  value: string
  keep: boolean
}

interface ServerDraft {
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

const blankDraft = (): ServerDraft => ({
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

function fromServer(server: MCPServer): ServerDraft {
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

function numberValue(value: string): number {
  return Number.parseInt(value, 10)
}

function toWrite(draft: ServerDraft): MCPServerWrite {
  const limits = {
    startupTimeoutMs: numberValue(draft.startupTimeoutMs),
    callTimeoutMs: numberValue(draft.callTimeoutMs),
    idleTimeoutMs: numberValue(draft.idleTimeoutMs),
    maxResultBytes: numberValue(draft.maxResultBytes),
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

function summaryOf(server: MCPServer): MCPServerSummary {
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

function rpcReason(error: unknown): string | undefined {
  if (!(error instanceof RpcError) || typeof error.data !== 'object' || error.data === null) return
  if (!('reason' in error.data) || typeof error.data.reason !== 'string') return
  return error.data.reason
}

function errorSentence(action: string, error: unknown): string {
  switch (rpcReason(error)) {
    case 'conflict':
      return 'This server changed elsewhere. The latest version has been loaded; review it and try again.'
    case 'not-found':
      return 'This server no longer exists.'
    case 'tool-not-found':
      return 'The tool catalog changed. Refresh tools before changing enablement.'
    case 'runtime-unavailable':
      return 'MCP is not available in this window.'
    default:
      return `Could not ${action}: ${error instanceof Error ? error.message : String(error)}`
  }
}

function catalogStatus(summary: MCPServerSummary): {
  tone: 'neutral' | 'warning' | 'ok'
  text: string
} {
  if (!summary.enabled) return { tone: 'neutral', text: 'Disabled' }
  if (summary.oauthStatus === 'missing' || summary.oauthStatus === 'expired') {
    return { tone: 'warning', text: 'Needs sign-in' }
  }
  if (summary.catalogState !== 'fresh') return { tone: 'warning', text: 'Needs refresh' }
  return { tone: 'ok', text: 'Ready' }
}

export interface MCPServersSectionProps {
  client: MCPServerClient
  vaultController?: VaultController
  secretSource?: SecretPickerSource
}

export function MCPServersSection(props: MCPServersSectionProps) {
  const [servers, setServers] = createSignal<MCPServerSummary[]>([])
  const [loadState, setLoadState] = createSignal<'loading' | 'ready' | 'failed'>('loading')
  const [loadError, setLoadError] = createSignal('')
  const [search, setSearch] = createSignal('')
  const [dialogOpen, setDialogOpen] = createSignal(false)
  const [editing, setEditing] = createSignal<MCPServer | null>(null)
  const [draft, setDraft] = createSignal<ServerDraft>(blankDraft())
  const [dialogBusy, setDialogBusy] = createSignal(false)
  const [operationError, setOperationError] = createSignal('')
  const [editingStale, setEditingStale] = createSignal(false)
  const [openingId, setOpeningId] = createSignal<string | null>(null)
  let refreshGeneration = 0

  const vaultState = () => props.vaultController?.status()?.state ?? 'unknown'
  const marks = (value: string) => secretMarks(value, [], vaultState())

  const filtered = createMemo(() => {
    const query = search().trim().toLowerCase()
    if (!query) return servers()
    return servers().filter((server) =>
      `${server.name} ${server.transport}`.toLowerCase().includes(query),
    )
  })

  async function load(): Promise<void> {
    const generation = ++refreshGeneration
    setLoadState('loading')
    try {
      const next = await props.client.list()
      if (generation !== refreshGeneration) return
      setServers(next)
      setLoadError('')
      setLoadState('ready')
    } catch (error) {
      if (generation !== refreshGeneration) return
      setLoadError(errorSentence('load MCP servers', error))
      setLoadState('failed')
    }
  }

  async function refreshSelected(): Promise<void> {
    const current = untrack(editing)
    if (!current) return
    try {
      const latest = await props.client.get(current.id)
      if (latest.revision <= current.revision) return
      if (untrack(dialogOpen) || untrack(dialogBusy)) {
        setEditingStale(true)
        setOperationError(
          'This server changed elsewhere. Close and reopen it to review the latest version before saving.',
        )
        return
      }
      setEditing(latest)
      setDraft(fromServer(latest))
      setEditingStale(false)
    } catch (error) {
      if (rpcReason(error) === 'not-found') {
        setEditing(null)
        setDialogOpen(false)
        setEditingStale(false)
      }
    }
  }

  async function refreshChanged(id: string): Promise<void> {
    await load()
    if (untrack(editing)?.id !== id) return
    await refreshSelected()
  }

  /* eslint-disable solid/reactivity -- dispatcher notifications and reconnect
     callbacks are event handlers; they intentionally read current UI state
     only when the backend reports a change. */
  onMount(() => {
    const unsubscribeChanged = props.client.subscribeChanged((params) => {
      void refreshChanged(params.id)
    })
    const unsubscribeConnect = props.client.onConnect(() => {
      void load().then(() => refreshSelected())
    })
    void load()
    onCleanup(() => {
      unsubscribeChanged()
      unsubscribeConnect()
    })
  })
  /* eslint-enable solid/reactivity */

  function replaceServer(server: MCPServer): void {
    const summary = summaryOf(server)
    setServers((current) => {
      const index = current.findIndex((item) => item.id === server.id)
      if (index < 0) return [...current, summary]
      return current.map((item) => (item.id === server.id ? summary : item))
    })
    if (editing()?.id === server.id) {
      setEditing(server)
      setEditingStale(false)
    }
  }

  function openNew(): void {
    setEditing(null)
    setEditingStale(false)
    setDraft(blankDraft())
    setOperationError('')
    setDialogOpen(true)
  }

  async function openServer(id: string): Promise<void> {
    if (openingId() !== null) return
    setOpeningId(id)
    try {
      const server = await props.client.get(id)
      setEditing(server)
      setEditingStale(false)
      setDraft(fromServer(server))
      setOperationError('')
      setDialogOpen(true)
    } catch (error) {
      showToast({ level: 'danger', message: errorSentence('open the MCP server', error) })
      if (rpcReason(error) === 'not-found') await load()
    } finally {
      setOpeningId(null)
    }
  }

  async function recoverConflict(id: string): Promise<void> {
    try {
      const latest = await props.client.get(id)
      replaceServer(latest)
      setEditingStale(false)
      setDraft(fromServer(latest))
    } catch {
      await load()
      setDialogOpen(false)
    }
  }

  async function save(): Promise<void> {
    if (dialogBusy()) return
    if (editingStale()) {
      setOperationError(
        'This server changed elsewhere. Close and reopen it to review the latest version before saving.',
      )
      return
    }
    const value = draft()
    if (!value.name.trim()) {
      setOperationError('Name is required.')
      return
    }
    if (value.transport === 'stdio' && !value.command.trim()) {
      setOperationError('Command is required for a stdio server.')
      return
    }
    if (value.transport === 'streamable-http' && !value.endpoint.trim()) {
      setOperationError('Endpoint is required for a Streamable HTTP server.')
      return
    }
    setDialogBusy(true)
    setOperationError('')
    try {
      const current = editing()
      const saved = current
        ? await props.client.update(current.id, current.revision, toWrite(value))
        : await props.client.create(toWrite(value))
      replaceServer(saved)
      setDialogOpen(false)
      showToast({ level: 'success', message: `Saved MCP server “${saved.name}”.` })
    } catch (error) {
      const message = errorSentence('save the MCP server', error)
      setOperationError(message)
      if (rpcReason(error) === 'conflict' && editing()) await recoverConflict(editing()!.id)
    } finally {
      setDialogBusy(false)
    }
  }

  async function remove(): Promise<void> {
    const current = editing()
    if (!current || dialogBusy()) return
    if (!(await showConfirm(`Delete MCP server “${current.name}”?`, 'Delete'))) return
    setDialogBusy(true)
    setOperationError('')
    try {
      await props.client.delete(current.id, current.revision)
      setServers((items) => items.filter((item) => item.id !== current.id))
      setDialogOpen(false)
      showToast({ level: 'success', message: `Deleted MCP server “${current.name}”.` })
    } catch (error) {
      const message = errorSentence('delete the MCP server', error)
      setOperationError(message)
      if (rpcReason(error) === 'conflict') await recoverConflict(current.id)
    } finally {
      setDialogBusy(false)
    }
  }

  async function refreshServer(id: string, revision: number): Promise<void> {
    if (dialogBusy()) return
    setDialogBusy(true)
    setOperationError('')
    try {
      const refreshed = await props.client.refresh(id, revision)
      replaceServer(refreshed)
      if (editing()?.id === id) setDraft(fromServer(refreshed))
      showToast({ level: 'success', message: `Refreshed tools for “${refreshed.name}”.` })
    } catch (error) {
      const message = errorSentence('refresh tools', error)
      if (editing()?.id === id) setOperationError(message)
      else showToast({ level: 'danger', message })
      if (rpcReason(error) === 'conflict') await recoverConflict(id)
    } finally {
      setDialogBusy(false)
    }
  }

  async function setEnabledTools(names: string[]): Promise<void> {
    const current = editing()
    if (!current || current.catalog.state !== 'fresh' || dialogBusy()) return
    setDialogBusy(true)
    setOperationError('')
    try {
      const updated = await props.client.setToolsEnabled(current.id, current.revision, names)
      replaceServer(updated)
      setDraft(fromServer(updated))
    } catch (error) {
      setOperationError(errorSentence('change tool enablement', error))
      if (rpcReason(error) === 'conflict' || rpcReason(error) === 'tool-not-found') {
        await recoverConflict(current.id)
      }
    } finally {
      setDialogBusy(false)
    }
  }

  async function changeOAuth(forget: boolean): Promise<void> {
    const current = editing()
    if (!current || dialogBusy()) return
    setDialogBusy(true)
    setOperationError('')
    try {
      const updated = forget
        ? await props.client.oauthForget(current.id, current.revision)
        : await props.client.oauthAuthorize(current.id, current.revision)
      replaceServer(updated)
      setDraft(fromServer(updated))
    } catch (error) {
      setOperationError(errorSentence(forget ? 'forget OAuth' : 'connect OAuth', error))
      if (rpcReason(error) === 'conflict') await recoverConflict(current.id)
    } finally {
      setDialogBusy(false)
    }
  }

  function updateDraft<K extends keyof ServerDraft>(key: K, value: ServerDraft[K]): void {
    setDraft((current) => ({ ...current, [key]: value }))
  }

  function updateBinding(
    key: 'env' | 'headers',
    index: number,
    patch: Partial<BindingDraft>,
  ): void {
    updateDraft(
      key,
      draft()[key].map((row, rowIndex) => (rowIndex === index ? { ...row, ...patch } : row)),
    )
  }

  function secretField(
    id: string,
    label: string,
    value: string,
    keep: boolean,
    onInput: (value: string) => void,
  ) {
    return (
      <SecretTextField
        id={id}
        label={label}
        type="password"
        value={value}
        placeholder={keep ? 'Stored secret — leave blank to keep' : 'Enter or choose a secret'}
        marks={marks(value)}
        source={props.secretSource}
        onInput={onInput}
      />
    )
  }

  const currentTools = () => editing()?.catalog.tools ?? []
  const enabledToolNames = () =>
    currentTools()
      .filter((tool) => tool.enabled)
      .map((tool) => tool.name)

  return (
    <div class="mcp-servers-root">
      <CollectionView
        searchValue={search()}
        onSearch={setSearch}
        searchPlaceholder="Search MCP servers"
        searchLabel="Search MCP servers"
        actions={<Button onClick={openNew}>New MCP server</Button>}
        hasItems={loadState() !== 'loading' && (filtered().length > 0 || loadState() === 'failed')}
        empty={
          <EmptyState
            icon={loadState() === 'loading' ? <Spinner label="Loading MCP servers" /> : undefined}
            title={loadState() === 'loading' ? 'Loading MCP servers…' : 'No MCP servers'}
            description={
              loadState() === 'loading'
                ? undefined
                : search()
                  ? 'No servers match this search.'
                  : 'Add a server, then use Refresh tools when you are ready to connect to it.'
            }
            action={
              loadState() === 'ready' && !search() ? (
                <Button onClick={openNew}>Add server</Button>
              ) : undefined
            }
          />
        }
      >
        <Show when={loadState() === 'failed'}>
          <StatusCard
            tone="danger"
            title="MCP servers could not be loaded"
            description={loadError()}
            action={<Button onClick={() => void load()}>Try again</Button>}
          />
        </Show>
        <For each={filtered()}>
          {(server) => {
            const status = () => catalogStatus(server)
            return (
              <RecordRow
                title={server.name}
                kind={{ label: server.transport === 'stdio' ? 'stdio' : 'HTTP' }}
                meta={`${server.enabledToolCount} of ${server.toolCount} tools enabled`}
                status={status()}
                onActivate={() => void openServer(server.id)}
                actions={
                  <Button
                    size="sm"
                    disabled={dialogBusy() || openingId() !== null}
                    onClick={() => void refreshServer(server.id, server.revision)}
                  >
                    Refresh tools
                  </Button>
                }
              />
            )
          }}
        </For>
      </CollectionView>

      <Dialog
        open={dialogOpen()}
        onClose={() => !dialogBusy() && setDialogOpen(false)}
        title={editing() ? `Edit ${editing()!.name}` : 'New MCP server'}
        size="lg"
        onSubmit={() => void save()}
        footer={
          <>
            <Show when={editing()}>
              <Button variant="danger" disabled={dialogBusy()} onClick={() => void remove()}>
                Delete
              </Button>
            </Show>
            <Button variant="ghost" disabled={dialogBusy()} onClick={() => setDialogOpen(false)}>
              Cancel
            </Button>
            <Button variant="primary" disabled={dialogBusy()} onClick={() => void save()}>
              Save
            </Button>
          </>
        }
      >
        <Stack gap="loose">
          <Show when={operationError()}>
            <StatusCard
              tone="danger"
              title="The action was not completed"
              description={operationError()}
            />
          </Show>

          <Section title="General">
            <TextField
              id="mcp-server-name"
              label="Name"
              value={draft().name}
              required
              onInput={(value) => updateDraft('name', value)}
            />
            <Checkbox
              variant="switch"
              checked={draft().enabled}
              label="Enabled"
              onChange={(value) => updateDraft('enabled', value)}
            />
            <Field for="mcp-server-transport" label="Transport">
              <Select
                id="mcp-server-transport"
                value={draft().transport}
                options={[
                  { value: 'stdio', label: 'stdio' },
                  { value: 'streamable-http', label: 'Streamable HTTP' },
                ]}
                onChange={(value) => updateDraft('transport', value as ServerDraft['transport'])}
              />
            </Field>
          </Section>

          <Show when={draft().transport === 'stdio'}>
            <Section title="stdio">
              <TextField
                id="mcp-command"
                label="Command"
                value={draft().command}
                required
                description="Executable path or name. It is launched directly, without a shell."
                onInput={(value) => updateDraft('command', value)}
              />
              <TextField
                id="mcp-cwd"
                label="Working directory"
                value={draft().cwd}
                placeholder="Optional absolute path"
                onInput={(value) => updateDraft('cwd', value)}
              />
              <Field for="mcp-argv" label="Arguments">
                <EditableRowList
                  rows={draft().argv}
                  ariaLabel="Command arguments"
                  emptyLabel="No arguments"
                  addLabel="Add argument"
                  removeLabel={(index) => `Remove argument ${index + 1}`}
                  onAdd={() => updateDraft('argv', [...draft().argv, { value: '' }])}
                  onRemove={(index) =>
                    updateDraft(
                      'argv',
                      draft().argv.filter((_, i) => i !== index),
                    )
                  }
                  renderRow={(row, index) => (
                    <TextField
                      ariaLabel={`Argument ${index + 1}`}
                      value={row().value}
                      onInput={(value) =>
                        updateDraft(
                          'argv',
                          draft().argv.map((item, i) => (i === index ? { value } : item)),
                        )
                      }
                    />
                  )}
                />
              </Field>
              <Field for="mcp-env" label="Environment">
                <EditableRowList
                  rows={draft().env}
                  ariaLabel="Environment bindings"
                  emptyLabel="No environment bindings"
                  addLabel="Add environment variable"
                  removeLabel={(index) => `Remove environment variable ${index + 1}`}
                  onAdd={() =>
                    updateDraft('env', [
                      ...draft().env,
                      { name: '', kind: 'literal', value: '', keep: false },
                    ])
                  }
                  onRemove={(index) =>
                    updateDraft(
                      'env',
                      draft().env.filter((_, i) => i !== index),
                    )
                  }
                  renderRow={(row, index) => (
                    <div class="mcp-binding-row">
                      <TextField
                        ariaLabel={`Environment variable ${index + 1} name`}
                        value={row().name}
                        placeholder="Name"
                        onInput={(value) => updateBinding('env', index, { name: value })}
                      />
                      <Select
                        ariaLabel={`Environment variable ${index + 1} source`}
                        value={row().kind}
                        options={[
                          { value: 'literal', label: 'Literal' },
                          { value: 'secret', label: 'Secret' },
                        ]}
                        onChange={(value) =>
                          updateBinding('env', index, {
                            kind: value as BindingDraft['kind'],
                            value: '',
                            keep: value === 'secret' && row().keep,
                          })
                        }
                      />
                      <Show
                        when={row().kind === 'secret'}
                        fallback={
                          <TextField
                            ariaLabel={`Environment variable ${index + 1} value`}
                            value={row().value}
                            placeholder="Value"
                            onInput={(value) => updateBinding('env', index, { value })}
                          />
                        }
                      >
                        {secretField(
                          `mcp-env-secret-${index}`,
                          `Environment variable ${index + 1} secret`,
                          row().value,
                          row().keep,
                          (value) => updateBinding('env', index, { value }),
                        )}
                      </Show>
                    </div>
                  )}
                />
              </Field>
            </Section>
          </Show>

          <Show when={draft().transport === 'streamable-http'}>
            <Section title="Streamable HTTP">
              <TextField
                id="mcp-http-endpoint"
                label="Endpoint"
                value={draft().endpoint}
                required
                placeholder="https://example.com/mcp"
                onInput={(value) => updateDraft('endpoint', value)}
              />
              <Field for="mcp-http-auth" label="Authentication">
                <Select
                  id="mcp-http-auth"
                  value={draft().auth}
                  options={[
                    { value: 'none', label: 'None' },
                    { value: 'bearer', label: 'Bearer token' },
                    { value: 'oauth', label: 'OAuth 2.1' },
                  ]}
                  onChange={(value) => updateDraft('auth', value as ServerDraft['auth'])}
                />
              </Field>
              <Show when={draft().auth === 'bearer'}>
                {secretField(
                  'mcp-bearer-token',
                  'Bearer token',
                  draft().bearer,
                  draft().keepBearer,
                  (value) => updateDraft('bearer', value),
                )}
              </Show>
              <Show when={draft().auth === 'oauth'}>
                <Field for="mcp-oauth-registration" label="OAuth registration">
                  <Select
                    id="mcp-oauth-registration"
                    value={draft().registration}
                    options={[
                      { value: 'dynamic', label: 'Dynamic registration' },
                      { value: 'preregistered', label: 'Pre-registered client' },
                    ]}
                    onChange={(value) =>
                      updateDraft('registration', value as ServerDraft['registration'])
                    }
                  />
                </Field>
                <Show when={draft().registration === 'preregistered'}>
                  <TextField
                    id="mcp-oauth-client-id"
                    label="Client ID"
                    value={draft().clientId}
                    onInput={(value) => updateDraft('clientId', value)}
                  />
                  {secretField(
                    'mcp-oauth-client-secret',
                    'Client secret (optional)',
                    draft().clientSecret,
                    draft().keepClientSecret,
                    (value) => updateDraft('clientSecret', value),
                  )}
                </Show>
                <Field for="mcp-oauth-scopes" label="Scopes">
                  <EditableRowList
                    rows={draft().scopes}
                    ariaLabel="OAuth scopes"
                    emptyLabel="No requested scopes"
                    addLabel="Add scope"
                    removeLabel={(index) => `Remove scope ${index + 1}`}
                    onAdd={() => updateDraft('scopes', [...draft().scopes, { value: '' }])}
                    onRemove={(index) =>
                      updateDraft(
                        'scopes',
                        draft().scopes.filter((_, i) => i !== index),
                      )
                    }
                    renderRow={(row, index) => (
                      <TextField
                        ariaLabel={`OAuth scope ${index + 1}`}
                        value={row().value}
                        onInput={(value) =>
                          updateDraft(
                            'scopes',
                            draft().scopes.map((item, i) => (i === index ? { value } : item)),
                          )
                        }
                      />
                    )}
                  />
                </Field>
                <Show when={editing()?.http?.oauth}>
                  {(oauth) => (
                    <StatusCard
                      tone={oauth().status === 'connected' ? 'ok' : 'warning'}
                      title={
                        oauth().status === 'connected'
                          ? 'OAuth connected'
                          : oauth().status === 'expired'
                            ? 'OAuth expired'
                            : 'OAuth not connected'
                      }
                      description={
                        oauth().issuer
                          ? `Issuer: ${oauth().issuer}. Tokens and secret references are never shown.`
                          : 'Tokens and secret references are never shown.'
                      }
                      action={
                        oauth().status === 'connected' ? (
                          <Button disabled={dialogBusy()} onClick={() => void changeOAuth(true)}>
                            Forget OAuth
                          </Button>
                        ) : (
                          <Button disabled={dialogBusy()} onClick={() => void changeOAuth(false)}>
                            Connect OAuth
                          </Button>
                        )
                      }
                    />
                  )}
                </Show>
              </Show>
              <Field for="mcp-headers" label="Headers">
                <EditableRowList
                  rows={draft().headers}
                  ariaLabel="HTTP headers"
                  emptyLabel="No custom headers"
                  addLabel="Add header"
                  removeLabel={(index) => `Remove header ${index + 1}`}
                  onAdd={() =>
                    updateDraft('headers', [
                      ...draft().headers,
                      { name: '', kind: 'literal', value: '', keep: false },
                    ])
                  }
                  onRemove={(index) =>
                    updateDraft(
                      'headers',
                      draft().headers.filter((_, i) => i !== index),
                    )
                  }
                  renderRow={(row, index) => (
                    <div class="mcp-binding-row">
                      <TextField
                        ariaLabel={`Header ${index + 1} name`}
                        value={row().name}
                        placeholder="Name"
                        onInput={(value) => updateBinding('headers', index, { name: value })}
                      />
                      <Select
                        ariaLabel={`Header ${index + 1} source`}
                        value={row().kind}
                        options={[
                          { value: 'literal', label: 'Literal' },
                          { value: 'secret', label: 'Secret' },
                        ]}
                        onChange={(value) =>
                          updateBinding('headers', index, {
                            kind: value as BindingDraft['kind'],
                            value: '',
                            keep: value === 'secret' && row().keep,
                          })
                        }
                      />
                      <Show
                        when={row().kind === 'secret'}
                        fallback={
                          <TextField
                            ariaLabel={`Header ${index + 1} value`}
                            value={row().value}
                            placeholder="Value"
                            onInput={(value) => updateBinding('headers', index, { value })}
                          />
                        }
                      >
                        {secretField(
                          `mcp-header-secret-${index}`,
                          `Header ${index + 1} secret`,
                          row().value,
                          row().keep,
                          (value) => updateBinding('headers', index, { value }),
                        )}
                      </Show>
                    </div>
                  )}
                />
              </Field>
            </Section>
          </Show>

          <Section title="Advanced">
            <div class="mcp-limit-grid">
              <TextField
                id="mcp-startup-timeout"
                label="Startup timeout"
                type="number"
                min={100}
                max={120000}
                unit="ms"
                value={draft().startupTimeoutMs}
                onInput={(value) => updateDraft('startupTimeoutMs', value)}
              />
              <TextField
                id="mcp-call-timeout"
                label="Call timeout"
                type="number"
                min={100}
                max={300000}
                unit="ms"
                value={draft().callTimeoutMs}
                onInput={(value) => updateDraft('callTimeoutMs', value)}
              />
              <TextField
                id="mcp-idle-timeout"
                label="Idle timeout"
                type="number"
                min={0}
                max={120000}
                unit="ms"
                value={draft().idleTimeoutMs}
                onInput={(value) => updateDraft('idleTimeoutMs', value)}
              />
              <TextField
                id="mcp-max-result"
                label="Maximum result"
                type="number"
                min={1024}
                max={262144}
                unit="bytes"
                value={draft().maxResultBytes}
                onInput={(value) => updateDraft('maxResultBytes', value)}
              />
            </div>
          </Section>

          <Show when={editing()}>
            {(server) => (
              <Section
                title="Tools"
                actions={
                  <Button
                    size="sm"
                    disabled={dialogBusy()}
                    onClick={() => void refreshServer(server().id, server().revision)}
                  >
                    Refresh tools
                  </Button>
                }
              >
                <Show
                  when={server().catalog.state === 'fresh'}
                  fallback={
                    <StatusCard
                      tone="warning"
                      title="Needs refresh"
                      description="Tools are unavailable until you explicitly refresh this server. Opening Settings never connects automatically."
                    />
                  }
                >
                  <Show
                    when={currentTools().length > 0}
                    fallback={
                      <EmptyState
                        title="No tools"
                        description="The server returned a fresh catalog with no tools."
                      />
                    }
                  >
                    <div class="mcp-tool-actions">
                      <Button
                        size="sm"
                        disabled={dialogBusy()}
                        onClick={() =>
                          void setEnabledTools(currentTools().map((tool) => tool.name))
                        }
                      >
                        Enable all
                      </Button>
                      <Button
                        size="sm"
                        disabled={dialogBusy()}
                        onClick={() => void setEnabledTools([])}
                      >
                        Disable all
                      </Button>
                    </div>
                    <div class="mcp-tool-list">
                      <For each={currentTools()}>
                        {(tool) => (
                          <RecordRow
                            title={tool.name}
                            meta={tool.description || 'No description provided'}
                            status={
                              tool.status === 'unchanged'
                                ? undefined
                                : {
                                    tone: 'warning',
                                    text:
                                      tool.status === 'new'
                                        ? 'New — disabled by default'
                                        : 'Changed — disabled by default',
                                  }
                            }
                            actions={
                              <Checkbox
                                checked={tool.enabled}
                                ariaLabel={`Enable ${tool.name}`}
                                disabled={dialogBusy()}
                                onChange={(enabled) => {
                                  const names = new Set(enabledToolNames())
                                  if (enabled) names.add(tool.name)
                                  else names.delete(tool.name)
                                  void setEnabledTools([...names])
                                }}
                              />
                            }
                          />
                        )}
                      </For>
                    </div>
                  </Show>
                </Show>
              </Section>
            )}
          </Show>
        </Stack>
      </Dialog>
    </div>
  )
}

import { For, Index, Show, createMemo, createSignal, onCleanup, onMount, untrack } from 'solid-js'
import { RpcError } from './dispatcher'
import { MCPServerClient, type MCPServer, type MCPServerSummary } from './mcp-servers-client'
import {
  blankDraft,
  fromServer,
  summaryOf,
  toWrite,
  type BindingDraft,
  type ServerDraft,
} from './mcp-server-draft'
import { SecretTextField, secretMarks } from './api/secret-text-field'
import { boundSecret } from './secret-reference'
import { VaultOperationCancelledError, type VaultController } from './vault'
import type { VaultClient } from './vault-client'
import type { SecretEntry, SecretPickerSource } from './ui/secret-picker'
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
import { Stack } from './ui/stack'
import { StatusCard } from './ui/status-card'
import { TextField } from './ui/text-field'
import { createFormValidation, required, absoluteHttpUrl } from './ui/validation'
import { createSubmitGate } from './ui/submit-gate'
import { showToast } from './ui/toast'

export interface MCPServersSectionProps {
  client: MCPServerClient
  vaultController?: VaultController
  vaultClient?: VaultClient
  secretSource?: SecretPickerSource
}

/** Configuration is a draft; catalog/auth actions operate only on the saved revision. */
export function MCPServersSection(props: MCPServersSectionProps) {
  const [servers, setServers] = createSignal<MCPServerSummary[]>([])
  const [loading, setLoading] = createSignal(true)
  const [listError, setListError] = createSignal('')
  const [search, setSearch] = createSignal('')
  const [open, setOpen] = createSignal(false)
  const [opening, setOpening] = createSignal(false)
  const [requestedId, setRequestedId] = createSignal<string | undefined>()
  const [editing, setEditing] = createSignal<MCPServer | null>(null)
  const [draft, setDraft] = createSignal<ServerDraft>(blankDraft())
  const [busy, setBusy] = createSignal(false)
  const [error, setError] = createSignal('')
  const [conflict, setConflict] = createSignal<number | null>(null)
  const [toolSearch, setToolSearch] = createSignal('')
  const [enabledOnly, setEnabledOnly] = createSignal(false)
  const [secrets, setSecrets] = createSignal<SecretEntry[]>([])
  let alive = true
  let generation = 0
  let listGeneration = 0
  let notifiedRevision = 0
  let pending: { generation: number; id: string; notifiedRevision: number } | undefined
  const current = (g: number) => alive && generation === g
  const message = (e: unknown) => (e instanceof Error ? e.message : String(e))

  async function load() {
    const request = ++listGeneration
    try {
      const rows = await props.client.list()
      if (!alive || request !== listGeneration) return
      setServers(rows)
      setListError('')
    } catch (e) {
      if (alive && request === listGeneration) setListError(message(e))
    } finally {
      if (alive && request === listGeneration) setLoading(false)
    }
  }

  function close() {
    ++generation
    pending = undefined
    setOpen(false)
    setOpening(false)
    setRequestedId(undefined)
    notifiedRevision = 0
    setBusy(false)
    setEditing(null)
    setDraft(blankDraft())
    setSecrets([])
    setError('')
    setConflict(null)
  }

  async function openEditor(id?: string) {
    close()
    const g = generation
    validation.reset()
    setToolSearch('')
    setEnabledOnly(false)
    setOpen(true)
    setRequestedId(id)
    if (!id) return
    setOpening(true)
    try {
      const server = await props.client.get(id)
      if (!current(g)) return
      setEditing(server)
      setDraft(fromServer(server))
      if (notifiedRevision > server.revision) setConflict(notifiedRevision)
    } catch (e) {
      if (current(g)) setError(message(e))
    } finally {
      if (current(g)) setOpening(false)
    }
  }

  onMount(() => {
    void load()
    // Transport callbacks read the editor at delivery time, not as reactive
    // dependencies of the subscription's owner.
    const unsubscribe = props.client.subscribeChanged((notice) =>
      untrack(() => {
        if (!alive) return
        void load()
        if (notice.id !== requestedId()) return
        notifiedRevision = Math.max(notifiedRevision, notice.revision)
        if (notice.change === 'deleted') {
          setConflict(notice.revision)
          setError('This server was deleted elsewhere. Your draft has not been saved.')
          return
        }
        const server = editing()
        if (!server || notice.revision <= server.revision) return
        if (pending?.generation === generation && pending.id === notice.id) {
          pending.notifiedRevision = Math.max(pending.notifiedRevision, notice.revision)
        } else {
          setConflict((revision) => Math.max(revision ?? 0, notice.revision))
        }
      }),
    )
    const disconnect = props.client.onConnect(() =>
      untrack(() => {
        void load()
        const server = editing()
        const g = generation
        if (server)
          void props.client
            .get(server.id)
            .then((latest) =>
              untrack(() => {
                if (!current(g)) return
                notifiedRevision = Math.max(notifiedRevision, latest.revision)
                if (pending?.generation === g)
                  pending.notifiedRevision = Math.max(pending.notifiedRevision, latest.revision)
                else if (latest.revision > (editing()?.revision ?? 0)) setConflict(latest.revision)
              }),
            )
            .catch((e: unknown) => {
              if (current(g)) setError(message(e))
            })
      }),
    )
    onCleanup(() => {
      alive = false
      close()
      ++listGeneration
      unsubscribe()
      disconnect()
    })
  })

  const filtered = createMemo(() =>
    servers().filter((s) =>
      `${s.name} ${s.transport}`.toLowerCase().includes(search().trim().toLowerCase()),
    ),
  )
  const tools = createMemo(() =>
    (editing()?.catalog.tools ?? []).filter(
      (tool) =>
        (!enabledOnly() || tool.enabled) &&
        `${tool.name} ${tool.description}`
          .toLowerCase()
          .includes(toolSearch().trim().toLowerCase()),
    ),
  )

  function change<K extends keyof ServerDraft>(key: K, value: ServerDraft[K]) {
    setDraft((d) => ({ ...d, [key]: value }))
  }
  function changeBinding(key: 'env' | 'headers', index: number, patch: Partial<BindingDraft>) {
    change(
      key,
      draft()[key].map((row, i) => (i === index ? { ...row, ...patch } : row)),
    )
  }

  const limits = [
    { key: 'startupTimeoutMs', label: 'Startup timeout (ms)', min: 100, max: 120000 },
    { key: 'callTimeoutMs', label: 'Call timeout (ms)', min: 100, max: 300000 },
    { key: 'idleTimeoutMs', label: 'Idle timeout (ms)', min: 0, max: 120000 },
    { key: 'maxResultBytes', label: 'Maximum result bytes', min: 1024, max: 262144 },
  ] as const
  const validation = createFormValidation(
    {
      name: () => required('Name')(draft().name),
      command: () =>
        draft().transport === 'stdio' ? required('Executable')(draft().command) : undefined,
      endpoint: () =>
        draft().transport === 'streamable-http'
          ? (required('Endpoint')(draft().endpoint) ?? absoluteHttpUrl()(draft().endpoint))
          : undefined,
      clientId: () =>
        draft().transport === 'streamable-http' &&
        draft().auth === 'oauth' &&
        draft().registration === 'preregistered'
          ? required('Client ID')(draft().clientId)
          : undefined,
      bindings: () => {
        const rows = draft().transport === 'stdio' ? draft().env : draft().headers
        if (rows.some((r) => !r.name.trim())) return 'Every binding needs a name'
        if (
          new Set(
            rows.map((r) =>
              draft().transport === 'stdio' ? r.name.trim() : r.name.trim().toLowerCase(),
            ),
          ).size !== rows.length
        )
          return 'Binding names must be unique'
        if (rows.some((r) => r.kind === 'secret' && !r.keep && !r.value))
          return 'Enter or select a secret for each secret binding'
        return undefined
      },
      limits: () => {
        const invalid = limits.find(
          (l) =>
            !/^\d+$/.test(draft()[l.key]) ||
            Number(draft()[l.key]) < l.min ||
            Number(draft()[l.key]) > l.max,
        )
        return invalid
          ? `${invalid.label} must be an integer between ${invalid.min} and ${invalid.max}`
          : undefined
      },
    },
    {
      controlId: (field) => {
        if (field === 'bindings')
          return `mcp-${draft().transport === 'stdio' ? 'env' : 'headers'}-0-name`
        if (field === 'limits')
          return `mcp-${limits.find((l) => !/^\d+$/.test(draft()[l.key]) || Number(draft()[l.key]) < l.min || Number(draft()[l.key]) > l.max)?.key ?? 'startupTimeoutMs'}`
        return `mcp-${field}`
      },
    },
  )
  const gate = createSubmitGate(validation)

  async function fail(e: unknown, g: number) {
    if (!current(g) || e instanceof VaultOperationCancelledError) return
    setError(message(e))
    if (
      e instanceof RpcError &&
      typeof e.data === 'object' &&
      e.data !== null &&
      'reason' in e.data &&
      e.data.reason === 'conflict'
    ) {
      setConflict(editing()?.revision ?? 0)
      const id = editing()?.id
      if (id) {
        try {
          const latest = await props.client.get(id)
          if (current(g)) setConflict(latest.revision)
        } catch {
          /* The original failure remains visible; Reload can retry. */
        }
      }
    }
  }

  async function save() {
    if (!open() || busy() || opening() || (requestedId() && !editing()) || conflict() !== null)
      return
    const g = generation
    if (!(await gate()) || !current(g) || busy() || conflict() !== null) return
    const server = editing()
    const input = toWrite(draft())
    setBusy(true)
    setError('')
    const operation = { generation: g, id: server?.id ?? '', notifiedRevision: 0 }
    pending = operation
    const persist = async () => {
      // A dismissed setup sheet may finish after the editor was replaced.
      if (!current(g)) throw new VaultOperationCancelledError()
      if (server) await props.client.update(server.id, server.revision, input)
      else await props.client.create(input)
    }
    const fresh =
      [...(input.stdio?.env ?? []), ...(input.http?.headers ?? [])].some(
        (r) => r.value.secretValue !== null,
      ) ||
      input.http?.bearer?.secretValue != null ||
      input.http?.oauth?.clientSecret?.secretValue != null
    try {
      if (fresh && !props.vaultController)
        throw new Error(
          'Vault setup is not available in this window. Select an existing secret or configure Vault in another window.',
        )
      if (fresh)
        await props.vaultController!.saveSecretWithVault(persist, 'save these MCP credentials')
      else await persist()
      if (!current(g)) return
      close()
      void load()
      showToast({
        level: 'success',
        message: 'MCP server saved. Refresh explicitly to discover tools.',
      })
    } catch (e) {
      await fail(e, g)
      if (current(g) && server && operation.notifiedRevision > server.revision)
        setConflict(operation.notifiedRevision)
    } finally {
      if (current(g)) {
        pending = undefined
        setBusy(false)
      }
    }
  }

  async function immediate(
    action: 'refresh' | 'oauthAuthorize' | 'oauthForget' | 'setToolsEnabled',
    names?: string[],
  ) {
    const server = editing()
    if (!server || busy() || conflict() !== null) return
    const g = generation
    setBusy(true)
    setError('')
    const operation = { generation: g, id: server.id, notifiedRevision: 0 }
    pending = operation
    try {
      const next =
        action === 'setToolsEnabled'
          ? await props.client.setToolsEnabled(server.id, server.revision, names ?? [])
          : await props.client[action](server.id, server.revision)
      if (!current(g)) return
      setEditing(next)
      // Only persisted facts advance. Never overwrite the configuration draft.
      if (operation.notifiedRevision > next.revision) setConflict(operation.notifiedRevision)
      setServers((rows) =>
        rows.map((row) =>
          row.id === next.id && row.revision <= next.revision ? summaryOf(next) : row,
        ),
      )
      void load()
      showToast({
        level: 'success',
        message:
          action === 'refresh'
            ? 'Catalog refreshed. New and changed tools are disabled.'
            : 'MCP change saved. Configuration draft kept.',
      })
    } catch (e) {
      await fail(e, g)
      if (current(g) && operation.notifiedRevision > server.revision)
        setConflict(operation.notifiedRevision)
    } finally {
      if (current(g)) {
        pending = undefined
        setBusy(false)
      }
    }
  }

  async function reconcile(keepDraft: boolean) {
    const server = editing()
    if (!server || busy()) return
    const g = generation
    setBusy(true)
    try {
      const latest = await props.client.get(server.id)
      if (!current(g)) return
      setEditing(latest)
      if (!keepDraft) {
        setDraft(fromServer(latest))
        validation.reset()
      }
      setConflict(notifiedRevision > latest.revision ? notifiedRevision : null)
      setError('')
    } catch (e) {
      await fail(e, g)
    } finally {
      if (current(g)) setBusy(false)
    }
  }

  async function remove(server: MCPServerSummary) {
    const g = generation
    if (!(await showConfirm(`Delete "${server.name}" and its catalog?`)) || !current(g)) return
    try {
      await props.client.delete(server.id, server.revision)
      if (!current(g)) return
      void load()
      showToast({ level: 'success', message: 'MCP server deleted.' })
    } catch (e) {
      if (current(g)) setListError(message(e))
    }
  }

  const pickerSource = (): SecretPickerSource | undefined => {
    const base = props.secretSource
    if (!base) return undefined
    return {
      ...base,
      list: async () => {
        const g = generation
        const entries = props.vaultClient
          ? (await props.vaultClient.inventory()).entries
              .filter((entry) => entry.kind === 'password')
              .map((entry) => ({ id: entry.id, name: entry.name }))
          : await base.list()
        if (current(g)) setSecrets(entries)
        return entries
      },
      requestCreate: async (name, value) => {
        const g = generation
        const entry = await base.requestCreate(name, value)
        if (entry && current(g))
          setSecrets((entries) => [...entries.filter((row) => row.id !== entry.id), entry])
        return entry
      },
    }
  }
  const marks = (value: string) =>
    secretMarks(value, secrets(), props.vaultController?.status()?.state ?? 'unknown')

  function bindingRows(key: 'env' | 'headers') {
    return (
      <EditableRowList
        rows={draft()[key]}
        ariaLabel={key === 'env' ? 'Environment bindings' : 'HTTP headers'}
        addLabel={key === 'env' ? 'Add environment variable' : 'Add header'}
        disabled={busy()}
        onAdd={() =>
          change(key, [...draft()[key], { name: '', kind: 'literal', value: '', keep: false }])
        }
        onRemove={(i) =>
          change(
            key,
            draft()[key].filter((_, index) => index !== i),
          )
        }
        error={validation.error('bindings')}
        renderRow={(row, i) => (
          <Stack>
            <TextField
              id={`mcp-${key}-${i}-name`}
              label="Name"
              value={row().name}
              onInput={(name) =>
                changeBinding(key, i, { name, keep: name === row().name && row().keep })
              }
            />
            <Field label="Value source" for={`mcp-${key}-${i}-kind`}>
              <Select
                id={`mcp-${key}-${i}-kind`}
                value={row().kind}
                options={[
                  { value: 'literal', label: 'Literal' },
                  { value: 'secret', label: 'Vault secret' },
                ]}
                onChange={(kind) =>
                  changeBinding(key, i, {
                    kind: kind as BindingDraft['kind'],
                    value: '',
                    keep: false,
                  })
                }
              />
            </Field>
            <Show
              when={row().kind === 'secret'}
              fallback={
                <TextField
                  label="Value"
                  value={row().value}
                  onInput={(value) => changeBinding(key, i, { value })}
                />
              }
            >
              <SecretTextField
                id={`mcp-${key}-${i}-secret`}
                label="Secret value"
                type={boundSecret(row().value) === undefined ? 'password' : 'text'}
                value={row().value}
                onInput={(value) => changeBinding(key, i, { value })}
                source={pickerSource()}
                marks={marks(row().value)}
                description={
                  row().keep
                    ? 'Stored in Vault. Leave blank to keep; enter or select a replacement.'
                    : 'Enter a new secret or select one from Vault.'
                }
              />
            </Show>
          </Stack>
        )}
      />
    )
  }

  function stringRows(key: 'argv' | 'scopes', label: string) {
    return (
      <EditableRowList
        rows={draft()[key]}
        ariaLabel={label}
        addLabel={`Add ${key === 'argv' ? 'argument' : 'scope'}`}
        disabled={busy()}
        onAdd={() => change(key, [...draft()[key], { value: '' }])}
        onRemove={(i) =>
          change(
            key,
            draft()[key].filter((_, index) => index !== i),
          )
        }
        renderRow={(row, i) => (
          <TextField
            label={`${key === 'argv' ? 'Argument' : 'Scope'} ${i + 1}`}
            value={row().value}
            onInput={(value) =>
              change(
                key,
                draft()[key].map((r, index) => (index === i ? { value } : r)),
              )
            }
          />
        )}
      />
    )
  }

  return (
    <div class="mcp-servers">
      <Show when={listError()}>
        <StatusCard
          tone="danger"
          title="Could not load or change MCP servers"
          description={listError()}
          action={<Button onClick={() => void load()}>Retry</Button>}
        />
      </Show>
      <CollectionView
        searchValue={search()}
        onSearch={setSearch}
        searchLabel="Filter MCP servers"
        searchPlaceholder="Filter MCP servers"
        actions={
          <Button variant="primary" onClick={() => void openEditor()}>
            New server
          </Button>
        }
        hasItems={servers().length > 0}
        empty={
          <EmptyState
            title={loading() ? 'Loading MCP servers' : 'No MCP servers'}
            description="Servers stay dormant until an explicit Refresh or an approved tool call."
          />
        }
      >
        <div role="list" aria-label="MCP servers">
          <For each={filtered()}>
            {(server) => (
              <RecordRow
                title={server.name}
                kind={{ label: server.transport }}
                meta={`${server.enabledToolCount}/${server.toolCount} tools enabled · ${server.catalogState} catalog${server.oauthStatus ? ` · OAuth ${server.oauthStatus}` : ''}`}
                status={{
                  tone: server.enabled ? 'neutral' : 'warning',
                  text: server.enabled ? 'Enabled' : 'Disabled',
                }}
                onActivate={() => void openEditor(server.id)}
                actions={
                  <>
                    <Button onClick={() => void openEditor(server.id)}>Edit {server.name}</Button>
                    <Button variant="danger" onClick={() => void remove(server)}>
                      Delete {server.name}
                    </Button>
                  </>
                }
              />
            )}
          </For>
        </div>
        <Show when={servers().length > 0 && filtered().length === 0}>
          <EmptyState title="No matching servers" />
        </Show>
      </CollectionView>
      <Dialog
        open={open()}
        onClose={close}
        onSubmit={() => void save()}
        title={editing() ? `Edit MCP server: ${editing()!.name}` : 'New MCP server'}
        size="lg"
        footer={
          <>
            <Button onClick={close}>Cancel</Button>
            <Button
              variant="primary"
              disabled={
                busy() ||
                opening() ||
                (requestedId() !== undefined && !editing()) ||
                conflict() !== null
              }
              onClick={() => void save()}
            >
              Save server
            </Button>
          </>
        }
      >
        <Stack>
          <Show when={busy()}>
            <div role="status">
              <StatusCard
                title="Waiting for MCP operation"
                description="Configuration is not saved by catalog or OAuth actions. You can dismiss this editor; a submitted operation may still finish."
              />
            </div>
          </Show>
          <Show when={error()}>
            <div role="alert">
              <StatusCard
                tone="danger"
                title="MCP operation did not complete"
                description={error()}
              />
            </div>
          </Show>
          <Show when={conflict() !== null}>
            <StatusCard
              tone="warning"
              title="Server changed elsewhere"
              description={`Persisted revision ${conflict()}. Your configuration draft is intact. Reload to discard it, or review the latest revision before deliberately resubmitting.`}
              action={
                <div class="mcp-servers-actions">
                  <Button disabled={busy()} onClick={() => void reconcile(false)}>
                    Discard draft and reload
                  </Button>
                  <Button disabled={busy()} onClick={() => void reconcile(true)}>
                    Keep draft for resubmission
                  </Button>
                </div>
              }
            />
          </Show>
          <Show
            when={!opening() && (!requestedId() || editing())}
            fallback={
              <EmptyState
                title={opening() ? 'Loading server' : 'Server could not be opened'}
                action={
                  <Show when={!opening()}>
                    <Button onClick={() => void openEditor(requestedId())}>Reload server</Button>
                  </Show>
                }
              />
            }
          >
            <Section title="Configuration draft">
              <TextField
                id="mcp-name"
                label="Name"
                required
                value={draft().name}
                onInput={(v) => change('name', v)}
                error={validation.error('name')}
                onBlur={() => validation.touch('name')}
              />
              <Checkbox
                label="Server enabled"
                checked={draft().enabled}
                onChange={(v) => change('enabled', v)}
              />
              <Field label="Transport" for="mcp-transport">
                <Select
                  id="mcp-transport"
                  value={draft().transport}
                  options={[
                    { value: 'stdio', label: 'stdio — local executable' },
                    { value: 'streamable-http', label: 'Streamable HTTP' },
                  ]}
                  onChange={(v) => change('transport', v as ServerDraft['transport'])}
                />
              </Field>
              <Show
                when={draft().transport === 'stdio'}
                fallback={
                  <>
                    <TextField
                      id="mcp-endpoint"
                      label="Endpoint URL"
                      required
                      value={draft().endpoint}
                      onInput={(v) => change('endpoint', v)}
                      error={validation.error('endpoint')}
                      description="HTTPS, or explicitly configured local/private HTTP. No implicit proxy credentials."
                    />
                    <Field label="Authentication" for="mcp-auth">
                      <Select
                        id="mcp-auth"
                        value={draft().auth}
                        onChange={(v) => change('auth', v as ServerDraft['auth'])}
                        options={[
                          { value: 'none', label: 'None' },
                          { value: 'bearer', label: 'Bearer token' },
                          { value: 'oauth', label: 'OAuth' },
                        ]}
                      />
                    </Field>
                    <Show when={draft().auth === 'bearer'}>
                      <SecretTextField
                        id="mcp-bearer"
                        label="Bearer token"
                        type={boundSecret(draft().bearer) === undefined ? 'password' : 'text'}
                        value={draft().bearer}
                        onInput={(v) => change('bearer', v)}
                        source={pickerSource()}
                        marks={marks(draft().bearer)}
                        description={
                          draft().keepBearer
                            ? 'Stored in Vault. Leave blank to keep.'
                            : 'Enter a token or select a Vault secret.'
                        }
                      />
                    </Show>
                    <Show when={draft().auth === 'oauth'}>
                      <Field label="Client registration" for="mcp-registration">
                        <Select
                          id="mcp-registration"
                          value={draft().registration}
                          onChange={(v) => change('registration', v as ServerDraft['registration'])}
                          options={[
                            { value: 'dynamic', label: 'Dynamic registration' },
                            { value: 'preregistered', label: 'Preregistered client' },
                          ]}
                        />
                      </Field>
                      <Show when={draft().registration === 'preregistered'}>
                        <TextField
                          id="mcp-clientId"
                          label="Client ID"
                          value={draft().clientId}
                          onInput={(v) => change('clientId', v)}
                          error={validation.error('clientId')}
                        />
                        <SecretTextField
                          id="mcp-client-secret"
                          label="Client secret (optional)"
                          type={
                            boundSecret(draft().clientSecret) === undefined ? 'password' : 'text'
                          }
                          value={draft().clientSecret}
                          onInput={(v) => change('clientSecret', v)}
                          source={pickerSource()}
                          marks={marks(draft().clientSecret)}
                          description={
                            draft().keepClientSecret
                              ? 'Stored in Vault. Leave blank to keep.'
                              : 'Leave blank for a public client.'
                          }
                        />
                        <Show when={editing()?.http?.oauth?.clientSecret.secretSet}>
                          <Checkbox
                            label="Keep stored client secret"
                            checked={draft().keepClientSecret}
                            onChange={(v) => change('keepClientSecret', v)}
                          />
                        </Show>
                      </Show>
                      {stringRows('scopes', 'OAuth scopes')}
                    </Show>
                    {bindingRows('headers')}
                  </>
                }
              >
                <TextField
                  id="mcp-command"
                  label="Executable"
                  required
                  value={draft().command}
                  onInput={(v) => change('command', v)}
                  error={validation.error('command')}
                  description="Executed directly, without a shell."
                />
                {stringRows('argv', 'Arguments')}
                <TextField
                  label="Working directory"
                  value={draft().cwd}
                  onInput={(v) => change('cwd', v)}
                  description="Optional absolute path on this backend."
                />
                {bindingRows('env')}
              </Show>
            </Section>
            <Section title="Lifecycle limits">
              <div class="mcp-servers-limits">
                <For each={limits}>
                  {(limit) => (
                    <TextField
                      id={`mcp-${limit.key}`}
                      label={limit.label}
                      value={draft()[limit.key]}
                      onInput={(v) => change(limit.key, v)}
                      description={`${limit.min}–${limit.max}${limit.key === 'idleTimeoutMs' ? '; zero closes after every call' : ''}`}
                    />
                  )}
                </For>
              </div>
              <Show when={validation.error('limits')}>
                <div role="alert">
                  <StatusCard
                    tone="warning"
                    title="Check lifecycle limits"
                    description={validation.error('limits')}
                  />
                </div>
              </Show>
            </Section>
            <Show when={editing()}>
              {(server) => (
                <Section title="Saved server — immediate actions">
                  <StatusCard
                    title={`Catalog ${server().catalog.state}`}
                    tone={server().catalog.state === 'fresh' ? 'neutral' : 'warning'}
                    description={`Revision ${server().revision}. ${server().catalog.refreshedAt ? `Last refresh: ${server().catalog.refreshedAt}.` : 'Never refreshed.'} This is a saved snapshot, not a live health check. Refresh starts this saved executable or contacts its saved endpoint; it does not save the draft.`}
                    action={
                      <Button
                        disabled={busy() || conflict() !== null}
                        onClick={() => void immediate('refresh')}
                      >
                        Refresh tools
                      </Button>
                    }
                  />
                  <Show when={server().http?.auth === 'oauth'}>
                    <StatusCard
                      title={`OAuth ${server().http?.oauth?.status ?? 'missing'}`}
                      description={`Connect explicitly opens authorization in the host browser. Issuer: ${server().http?.oauth?.issuer || 'not connected'}. Granted scopes: ${server().http?.oauth?.grantedScopes.join(', ') || 'none'}. Access token expiry: ${server().http?.oauth?.accessTokenExpires ?? 'not available'}. Renewable sessions may renew silently on an approved call.`}
                    />
                    <div class="mcp-servers-actions">
                      <Button
                        disabled={busy() || conflict() !== null}
                        onClick={() => void immediate('oauthAuthorize')}
                      >
                        Connect OAuth
                      </Button>
                      <Button
                        disabled={
                          busy() || conflict() !== null || !server().http?.oauth?.sessionSet
                        }
                        onClick={() => void immediate('oauthForget')}
                      >
                        Forget OAuth
                      </Button>
                    </div>
                  </Show>
                  <TextField label="Search catalog" value={toolSearch()} onInput={setToolSearch} />
                  <Checkbox
                    label="Enabled tools only"
                    checked={enabledOnly()}
                    onChange={setEnabledOnly}
                  />
                  <p>
                    {server().catalog.tools.filter((tool) => tool.enabled).length}/
                    {server().catalog.tools.length} tools enabled
                  </p>
                  <Index each={tools()}>
                    {(tool) => (
                      <RecordRow
                        title={tool().name}
                        meta={tool().description}
                        kind={{ label: tool().status }}
                        actions={
                          <Checkbox
                            variant="switch"
                            label={`Enable ${tool().name}`}
                            checked={tool().enabled}
                            disabled={
                              busy() || conflict() !== null || server().catalog.state !== 'fresh'
                            }
                            onChange={(enabled) => {
                              const names = server()
                                .catalog.tools.filter((t) =>
                                  t.name === tool().name ? enabled : t.enabled,
                                )
                                .map((t) => t.name)
                              void immediate('setToolsEnabled', names)
                            }}
                          />
                        }
                      />
                    )}
                  </Index>
                  <Show when={tools().length === 0}>
                    <EmptyState
                      title={
                        server().catalog.tools.length ? 'No matching tools' : 'No discovered tools'
                      }
                    />
                  </Show>
                </Section>
              )}
            </Show>
          </Show>
        </Stack>
      </Dialog>
    </div>
  )
}

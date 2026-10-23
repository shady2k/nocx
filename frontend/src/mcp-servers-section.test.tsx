// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@solidjs/testing-library'
import { Dispatcher, RpcError, VaultOperationCancelledError } from './dispatcher'
import { fixedEndpoint } from './endpoint'
import { MCPServersSection, type MCPServersSectionProps } from './mcp-servers-section'
import { MCPServerClient, type MCPServer } from './mcp-servers-client'
import type { MCPServersChangedParams } from './generated/mcpServers.changed'
import { summaryOf } from './mcp-server-draft'
import { clearToasts, toasts } from './ui/toast'
import type { VaultController } from './vault'
import type { VaultClient } from './vault-client'
import { bindSecretFromLock } from './secret-field-test-helpers'

function server(overrides: Partial<MCPServer> = {}): MCPServer {
  return {
    id: 'mcp:weather',
    revision: 4,
    name: 'Weather',
    enabled: true,
    transport: 'stdio',
    stdio: { command: 'weather-mcp', argv: [], cwd: '', env: [] },
    http: null,
    limits: {
      startupTimeoutMs: 10000,
      callTimeoutMs: 60000,
      idleTimeoutMs: 30000,
      maxResultBytes: 262144,
    },
    catalog: {
      state: 'fresh',
      serverName: 'weather',
      serverVersion: '1.0',
      protocolVersion: '2025-06-18',
      refreshedAt: '2026-10-23T12:00:00Z',
      digest: 'catalog',
      tools: [
        {
          name: 'forecast',
          description: 'Forecast a city',
          inputSchema: {},
          outputSchema: null,
          descriptorDigest: 'forecast',
          enabled: false,
          status: 'new',
        },
      ],
    },
    ...overrides,
  }
}

function httpServer(auth: 'bearer' | 'oauth' = 'oauth'): MCPServer {
  return server({
    transport: 'streamable-http',
    stdio: null,
    http: {
      endpoint: 'https://mcp.example.test/tools',
      auth,
      headers: [],
      bearer: { secretSet: auth === 'bearer', owned: true },
      oauth:
        auth === 'oauth'
          ? {
              registration: 'preregistered',
              clientId: 'nocx-client',
              clientSecret: { secretSet: true, owned: true },
              scopes: ['weather.read'],
              sessionSet: true,
              status: 'connected',
              issuer: 'https://auth.example.test',
              grantedScopes: ['weather.read'],
              accessTokenExpires: '2026-10-24T12:00:00Z',
            }
          : null,
    },
  })
}

function deferred<T>() {
  let resolve!: (value: T) => void
  // ES2021 is the renderer's declared floor; withResolvers is not in its library.
  const promise = new Promise<T>((yes) => {
    resolve = yes
  })
  return { promise, resolve }
}

function mount(record = server(), props: Omit<MCPServersSectionProps, 'client'> = {}) {
  let stored = record
  const client = new MCPServerClient(new Dispatcher(fixedEndpoint(9876)))
  const list = vi
    .spyOn(client, 'list')
    .mockImplementation(() => Promise.resolve([summaryOf(stored)]))
  const get = vi.spyOn(client, 'get').mockImplementation(() => Promise.resolve(stored))
  const refresh = vi.spyOn(client, 'refresh').mockImplementation(() => {
    stored = { ...stored, revision: stored.revision + 1 }
    return Promise.resolve(stored)
  })
  const setToolsEnabled = vi
    .spyOn(client, 'setToolsEnabled')
    .mockImplementation((_id, revision, names) => {
      stored = {
        ...stored,
        revision: revision + 1,
        catalog: {
          ...stored.catalog,
          tools: stored.catalog.tools.map((tool) => ({
            ...tool,
            enabled: names.includes(tool.name),
          })),
        },
      }
      return Promise.resolve(stored)
    })
  const oauthAuthorize = vi.spyOn(client, 'oauthAuthorize').mockImplementation(() => {
    stored = { ...stored, revision: stored.revision + 1 }
    return Promise.resolve(stored)
  })
  const oauthForget = vi.spyOn(client, 'oauthForget').mockImplementation(() => {
    stored = { ...stored, revision: stored.revision + 1 }
    return Promise.resolve(stored)
  })
  const create = vi.spyOn(client, 'create').mockResolvedValue(record)
  const update = vi.spyOn(client, 'update').mockResolvedValue(record)
  const remove = vi.spyOn(client, 'delete').mockResolvedValue({ deleted: true })
  let changed: ((params: MCPServersChangedParams) => void) | undefined
  const unsubscribe = vi.fn()
  const disconnect = vi.fn()
  vi.spyOn(client, 'subscribeChanged').mockImplementation((handler) => {
    changed = handler
    return unsubscribe
  })
  vi.spyOn(client, 'onConnect').mockReturnValue(disconnect)
  const view = render(() => <MCPServersSection client={client} {...props} />)
  return {
    ...view,
    client,
    list,
    get,
    refresh,
    setToolsEnabled,
    oauthAuthorize,
    oauthForget,
    create,
    update,
    remove,
    unsubscribe,
    disconnect,
    notify: (revision: number, change: MCPServersChangedParams['change'] = 'updated') =>
      changed?.({ id: record.id, revision, change }),
    replace: (next: MCPServer) => {
      stored = next
    },
  }
}

async function edit() {
  fireEvent.click(await screen.findByRole('button', { name: 'Edit Weather' }))
  await vi.waitFor(() =>
    expect(screen.getByRole<HTMLInputElement>('textbox', { name: 'Name' }).value).toBe('Weather'),
  )
}

function fill(label: string, value: string) {
  fireEvent.input(
    screen.getByLabelText((text) => text.replace(/\s+\*$/, '') === label),
    { target: { value } },
  )
}

async function waitIdle() {
  await vi.waitFor(() =>
    expect(screen.getByRole<HTMLButtonElement>('button', { name: 'Save server' }).disabled).toBe(
      false,
    ),
  )
}

afterEach(() => {
  cleanup()
  clearToasts()
  vi.restoreAllMocks()
  document.body.replaceChildren()
})

describe('MCP Settings', () => {
  it('reads and searches locally without activating a server or OAuth', async () => {
    const h = mount()
    await edit()
    fill('Search catalog', 'city')
    expect(screen.getByRole('switch', { name: 'Enable forecast' })).toBeTruthy()
    fireEvent.click(screen.getByLabelText('Enabled tools only'))
    expect(screen.queryByRole('switch', { name: 'Enable forecast' })).toBeNull()
    expect(h.refresh).not.toHaveBeenCalled()
    expect(h.oauthAuthorize).not.toHaveBeenCalled()
    expect(h.update).not.toHaveBeenCalled()
  })

  it('preserves a dirty draft across refresh and enablement, saving the advanced revision', async () => {
    const h = mount()
    await edit()
    fill('Name', 'My unsaved name')
    fireEvent.click(screen.getByRole('button', { name: 'Refresh tools' }))
    await waitIdle()
    expect(h.refresh).toHaveBeenCalledWith('mcp:weather', 4)
    expect(screen.getByRole<HTMLInputElement>('textbox', { name: 'Name' }).value).toBe(
      'My unsaved name',
    )
    fireEvent.click(screen.getByRole('switch', { name: 'Enable forecast' }))
    await waitIdle()
    expect(h.setToolsEnabled).toHaveBeenCalledWith('mcp:weather', 5, ['forecast'])
    expect(screen.getByRole<HTMLInputElement>('textbox', { name: 'Name' }).value).toBe(
      'My unsaved name',
    )
    fireEvent.click(screen.getByRole('button', { name: 'Save server' }))
    await vi.waitFor(() =>
      expect(h.update).toHaveBeenCalledWith(
        'mcp:weather',
        6,
        expect.objectContaining({ name: 'My unsaved name' }),
      ),
    )
  })

  it('treats own notifications before and after a response as the same revision', async () => {
    const h = mount()
    const result = deferred<MCPServer>()
    h.refresh.mockReturnValueOnce(result.promise)
    await edit()
    fill('Name', 'Unsaved')
    fireEvent.click(screen.getByRole('button', { name: 'Refresh tools' }))
    h.notify(5)
    result.resolve(server({ revision: 5 }))
    await waitIdle()
    h.notify(5)
    h.notify(4)
    expect(screen.queryByText('Server changed elsewhere')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Save server' }))
    await vi.waitFor(() =>
      expect(h.update).toHaveBeenCalledWith(
        'mcp:weather',
        5,
        expect.objectContaining({ name: 'Unsaved' }),
      ),
    )
  })

  it('does not mistake a later external write for its own notification', async () => {
    const h = mount()
    const result = deferred<MCPServer>()
    h.refresh.mockReturnValueOnce(result.promise)
    await edit()
    fill('Name', 'Unsaved')
    fireEvent.click(screen.getByRole('button', { name: 'Refresh tools' }))
    h.notify(6)
    result.resolve(server({ revision: 5 }))
    await screen.findByText('Server changed elsewhere')
    expect(screen.getByRole<HTMLInputElement>('textbox', { name: 'Name' }).value).toBe('Unsaved')
    expect(screen.getByRole<HTMLButtonElement>('button', { name: 'Save server' }).disabled).toBe(
      true,
    )
  })

  it('keeps a conflicted draft until deliberate review and resubmission', async () => {
    const h = mount()
    await edit()
    fill('Name', 'Local draft')
    h.replace(server({ revision: 7, name: 'Other window' }))
    h.notify(7)
    await screen.findByText('Server changed elsewhere')
    expect(screen.getByRole<HTMLInputElement>('textbox', { name: 'Name' }).value).toBe(
      'Local draft',
    )
    expect(h.update).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Keep draft for resubmission' }))
    await waitIdle()
    expect(screen.getByRole<HTMLInputElement>('textbox', { name: 'Name' }).value).toBe(
      'Local draft',
    )
    fireEvent.click(screen.getByRole('button', { name: 'Save server' }))
    await vi.waitFor(() =>
      expect(h.update).toHaveBeenCalledWith(
        'mcp:weather',
        7,
        expect.objectContaining({ name: 'Local draft' }),
      ),
    )
  })

  it('offers deliberate reload after a CAS refusal without automatic retry', async () => {
    const h = mount()
    await edit()
    fill('Name', 'Local draft')
    h.replace(server({ revision: 8, name: 'Concurrent change' }))
    h.update.mockRejectedValueOnce(
      new RpcError('revision conflict', -32000, { reason: 'conflict' }),
    )
    fireEvent.click(screen.getByRole('button', { name: 'Save server' }))
    await screen.findByText('Server changed elsewhere')
    expect(screen.getByRole<HTMLInputElement>('textbox', { name: 'Name' }).value).toBe(
      'Local draft',
    )
    await vi.waitFor(() =>
      expect(
        screen.getByRole<HTMLButtonElement>('button', { name: 'Discard draft and reload' })
          .disabled,
      ).toBe(false),
    )
    fireEvent.click(screen.getByRole('button', { name: 'Discard draft and reload' }))
    await waitIdle()
    expect(screen.getByRole<HTMLInputElement>('textbox', { name: 'Name' }).value).toBe(
      'Concurrent change',
    )
    expect(h.update).toHaveBeenCalledOnce()
  })

  it('ignores a refresh completion after closing and reopening the same server', async () => {
    const h = mount()
    const result = deferred<MCPServer>()
    h.refresh.mockReturnValueOnce(result.promise)
    await edit()
    fireEvent.click(screen.getByRole('button', { name: 'Refresh tools' }))
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    await edit()
    fill('Name', 'Reopened draft')
    result.resolve(server({ revision: 99 }))
    await result.promise
    expect(screen.getByRole<HTMLInputElement>('textbox', { name: 'Name' }).value).toBe(
      'Reopened draft',
    )
    fireEvent.click(screen.getByRole('button', { name: 'Save server' }))
    await vi.waitFor(() =>
      expect(h.update).toHaveBeenCalledWith(
        'mcp:weather',
        4,
        expect.objectContaining({ name: 'Reopened draft' }),
      ),
    )
  })

  it('ignores stale get completion after closing and reopening the same ID', async () => {
    const h = mount()
    const result = deferred<MCPServer>()
    h.get.mockReturnValueOnce(result.promise)
    fireEvent.click(await screen.findByRole('button', { name: 'Edit Weather' }))
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    await edit()
    fill('Name', 'Reopened draft')
    result.resolve(server({ revision: 1, name: 'Old answer' }))
    await result.promise
    expect(screen.getByRole<HTMLInputElement>('textbox', { name: 'Name' }).value).toBe(
      'Reopened draft',
    )
  })

  it('ignores an old save completion rather than dismissing a reopened draft', async () => {
    const h = mount()
    const result = deferred<MCPServer>()
    h.update.mockReturnValueOnce(result.promise)
    await edit()
    fireEvent.click(screen.getByRole('button', { name: 'Save server' }))
    await vi.waitFor(() => expect(h.update).toHaveBeenCalledOnce())
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    await edit()
    fill('Name', 'New draft')
    result.resolve(server({ revision: 5 }))
    await result.promise
    expect(screen.getByRole<HTMLInputElement>('textbox', { name: 'Name' }).value).toBe('New draft')
    expect(toasts().some((toast) => toast.level === 'success')).toBe(false)
  })

  it('does not turn a failed edit read into a new-server save', async () => {
    const h = mount()
    h.get.mockRejectedValueOnce(new Error('server was deleted'))
    fireEvent.click(await screen.findByRole('button', { name: 'Edit Weather' }))
    await screen.findByText('server was deleted')
    expect(screen.getByRole<HTMLButtonElement>('button', { name: 'Save server' }).disabled).toBe(
      true,
    )
    expect(h.create).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Reload server' }))
    await waitIdle()
    expect(screen.getByRole<HTMLInputElement>('textbox', { name: 'Name' }).value).toBe('Weather')
  })

  it('creates stdio with separate argv, literal environment and lifecycle values without discovery', async () => {
    const h = mount()
    fireEvent.click(screen.getByRole('button', { name: 'New server' }))
    fill('Name', 'Local tool')
    fill('Executable', '/usr/bin/node')
    fireEvent.click(screen.getByRole('button', { name: 'Add argument' }))
    fill('Argument 1', '/tmp/my tool.mjs')
    fill('Working directory', '/tmp')
    fill('Idle timeout (ms)', '0')
    fireEvent.click(screen.getByRole('button', { name: 'Save server' }))
    await vi.waitFor(() =>
      expect(h.create.mock.calls).toMatchObject([
        [
          {
            name: 'Local tool',
            transport: 'stdio',
            http: null,
            stdio: { command: '/usr/bin/node', argv: ['/tmp/my tool.mjs'], cwd: '/tmp', env: [] },
            limits: { idleTimeoutMs: 0 },
          },
        ],
      ]),
    )
    expect(h.refresh).not.toHaveBeenCalled()
  })

  it('validates required fields and exact integer limits before sending', async () => {
    const h = mount()
    fireEvent.click(screen.getByRole('button', { name: 'New server' }))
    fireEvent.click(screen.getByRole('button', { name: 'Save server' }))
    await screen.findByText('Name is required')
    fill('Name', 'Name')
    fill('Executable', '/bin/example')
    fill('Startup timeout (ms)', '100oops')
    fireEvent.click(screen.getByRole('button', { name: 'Save server' }))
    await screen.findByText('Check lifecycle limits')
    expect(h.create).not.toHaveBeenCalled()
  })

  it('retains fresh secret material when Vault setup is cancelled, then clears it on dismissal', async () => {
    const saveSecretWithVault = vi.fn().mockRejectedValue(new VaultOperationCancelledError())
    const h = mount(httpServer('bearer'), {
      vaultController: { saveSecretWithVault, status: () => null } as unknown as VaultController,
    })
    await edit()
    fill('Bearer token', 'mcp-only-credential')
    fireEvent.click(screen.getByRole('button', { name: 'Save server' }))
    await vi.waitFor(() => expect(saveSecretWithVault).toHaveBeenCalledOnce())
    await waitIdle()
    expect(saveSecretWithVault).toHaveBeenCalledOnce()
    expect(h.update).not.toHaveBeenCalled()
    expect(screen.getByLabelText<HTMLInputElement>('Bearer token').value).toBe(
      'mcp-only-credential',
    )
    expect(screen.queryByRole('alert')).toBeNull()
    expect(toasts().length).toBe(0)
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    await edit()
    expect(screen.getByLabelText<HTMLInputElement>('Bearer token').value).toBe('')
  })

  it('keeps stored secrets without exposing handles or starting Vault setup', async () => {
    const saveSecretWithVault = vi.fn()
    const h = mount(httpServer('bearer'), {
      vaultController: { saveSecretWithVault, status: () => null } as unknown as VaultController,
    })
    await edit()
    expect(screen.getByLabelText<HTMLInputElement>('Bearer token').value).toBe('')
    expect(document.body.textContent).toContain('Stored in Vault')
    fireEvent.click(screen.getByRole('button', { name: 'Save server' }))
    await vi.waitFor(() =>
      expect(h.update.mock.calls).toMatchObject([
        [
          'mcp:weather',
          4,
          {
            http: { bearer: { secret: null, secretValue: null, keep: true } },
          },
        ],
      ]),
    )
    expect(saveSecretWithVault).not.toHaveBeenCalled()
  })

  it('names a selected Vault row using the inventory and saves only its row handle', async () => {
    const saveSecretWithVault = vi.fn()
    const inventory = vi.fn().mockResolvedValue({
      entries: [{ id: 'secrow:weather', name: 'Weather credential', kind: 'password' }],
    })
    const h = mount(httpServer('bearer'), {
      vaultController: {
        saveSecretWithVault,
        status: () => ({ state: 'unsealed' }),
      } as unknown as VaultController,
      vaultClient: { inventory } as unknown as VaultClient,
      secretSource: {
        status: () => Promise.resolve({ state: 'unsealed' }),
        list: () => Promise.resolve([]),
        requestUnseal: () => Promise.resolve(),
        requestSetup: () => Promise.resolve(true),
        requestCreate: () => Promise.resolve(undefined),
      },
    })
    await edit()
    await bindSecretFromLock(
      screen.getByLabelText<HTMLInputElement>('Bearer token'),
      'Weather credential',
    )
    expect(document.querySelector('.ui-text-field__mark')?.textContent).toBe('Weather credential')
    expect(document.body.textContent).not.toContain('Secret not on this machine')
    fireEvent.click(screen.getByRole('button', { name: 'Save server' }))
    await vi.waitFor(() =>
      expect(h.update.mock.calls).toMatchObject([
        [
          'mcp:weather',
          4,
          {
            http: { bearer: { secret: 'secrow:weather', secretValue: null, keep: false } },
          },
        ],
      ]),
    )
    expect(saveSecretWithVault).not.toHaveBeenCalled()
  })

  it('preserves HTTP drafts across explicit OAuth actions and reports browser unavailability honestly', async () => {
    const h = mount(httpServer())
    await edit()
    fill('Endpoint URL', 'https://different.example.test/mcp')
    h.oauthAuthorize.mockRejectedValueOnce(new Error('Native URL presentation unavailable'))
    fireEvent.click(screen.getByRole('button', { name: 'Connect OAuth' }))
    await screen.findByText('Native URL presentation unavailable')
    await waitIdle()
    expect(h.oauthAuthorize).toHaveBeenCalledWith('mcp:weather', 4)
    expect(screen.getByRole<HTMLInputElement>('textbox', { name: 'Endpoint URL' }).value).toBe(
      'https://different.example.test/mcp',
    )
    fireEvent.click(screen.getByRole('button', { name: 'Forget OAuth' }))
    await waitIdle()
    expect(h.oauthForget).toHaveBeenCalledWith('mcp:weather', 4)
    expect(screen.getByRole<HTMLInputElement>('textbox', { name: 'Endpoint URL' }).value).toBe(
      'https://different.example.test/mcp',
    )
    expect(h.update).not.toHaveBeenCalled()
  })

  it('discards only configuration on Cancel, leaving immediate tool changes persisted', async () => {
    const h = mount()
    await edit()
    fill('Name', 'Discard me')
    fireEvent.click(screen.getByRole('switch', { name: 'Enable forecast' }))
    await waitIdle()
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    await edit()
    expect(screen.getByRole<HTMLInputElement>('switch', { name: 'Enable forecast' }).checked).toBe(
      true,
    )
    expect(h.update).not.toHaveBeenCalled()
  })

  it('creates HTTP OAuth configuration without authorizing or refreshing implicitly', async () => {
    const h = mount()
    fireEvent.click(screen.getByRole('button', { name: 'New server' }))
    fill('Name', 'HTTP tools')
    fireEvent.change(screen.getByLabelText('Transport'), { target: { value: 'streamable-http' } })
    fill('Endpoint URL', 'https://mcp.example.test/api')
    fireEvent.change(screen.getByLabelText('Authentication'), { target: { value: 'oauth' } })
    fireEvent.change(screen.getByLabelText('Client registration'), {
      target: { value: 'preregistered' },
    })
    fill('Client ID', 'registered-client')
    fireEvent.click(screen.getByRole('button', { name: 'Add scope' }))
    fill('Scope 1', 'tools.read')
    fireEvent.click(screen.getByRole('button', { name: 'Add header' }))
    fireEvent.input(document.getElementById('mcp-headers-0-name')!, {
      target: { value: 'X-Workspace' },
    })
    fill('Value', 'test-workspace')
    fireEvent.click(screen.getByRole('button', { name: 'Save server' }))
    await vi.waitFor(() =>
      expect(h.create).toHaveBeenCalledWith(
        expect.objectContaining({
          name: 'HTTP tools',
          transport: 'streamable-http',
          stdio: null,
          http: {
            endpoint: 'https://mcp.example.test/api',
            auth: 'oauth',
            bearer: null,
            headers: [
              {
                name: 'X-Workspace',
                value: {
                  kind: 'literal',
                  literal: 'test-workspace',
                  secret: null,
                  secretValue: null,
                  keep: false,
                },
              },
            ],
            oauth: {
              registration: 'preregistered',
              clientId: 'registered-client',
              clientSecret: { secret: null, secretValue: null, keep: false },
              scopes: ['tools.read'],
            },
          },
        }),
      ),
    )
    expect(h.oauthAuthorize).not.toHaveBeenCalled()
    expect(h.refresh).not.toHaveBeenCalled()
  })

  it('sends fresh environment secrets only through the Vault save operation', async () => {
    const saveSecretWithVault = vi.fn((persist: () => Promise<void>) => persist())
    const h = mount(server(), {
      vaultController: {
        saveSecretWithVault,
        status: () => ({ state: 'unsealed' }),
      } as unknown as VaultController,
    })
    await edit()
    fireEvent.click(screen.getByRole('button', { name: 'Add environment variable' }))
    fireEvent.input(document.getElementById('mcp-env-0-name')!, { target: { value: 'MCP_TOKEN' } })
    fireEvent.change(screen.getByLabelText('Value source'), { target: { value: 'secret' } })
    fill('Secret value', 'only-this-mcp')
    fireEvent.click(screen.getByRole('button', { name: 'Save server' }))
    await vi.waitFor(() =>
      expect(h.update.mock.calls).toMatchObject([
        [
          'mcp:weather',
          4,
          {
            stdio: {
              env: [
                {
                  name: 'MCP_TOKEN',
                  value: {
                    kind: 'secret',
                    literal: null,
                    secret: null,
                    secretValue: 'only-this-mcp',
                    keep: false,
                  },
                },
              ],
            },
          },
        ],
      ]),
    )
    expect(saveSecretWithVault).toHaveBeenCalledOnce()
    await vi.waitFor(() => expect(screen.queryByLabelText('Secret value')).toBeNull())
  })

  it('requires confirmation before deleting the selected revision', async () => {
    const h = mount()
    fireEvent.click(await screen.findByRole('button', { name: 'Delete Weather' }))
    expect(h.remove).not.toHaveBeenCalled()
    fireEvent.click(await screen.findByRole('button', { name: 'Cancel' }))
    expect(h.remove).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Delete Weather' }))
    fireEvent.click(await screen.findByRole('button', { name: 'OK' }))
    await vi.waitFor(() => expect(h.remove).toHaveBeenCalledWith('mcp:weather', 4))
  })

  it('retains the draft when another window deletes its current revision', async () => {
    const h = mount()
    await edit()
    fill('Name', 'Keep this text')
    h.notify(4, 'deleted')
    await screen.findByText('This server was deleted elsewhere. Your draft has not been saved.')
    expect(screen.getByRole<HTMLInputElement>('textbox', { name: 'Name' }).value).toBe(
      'Keep this text',
    )
    expect(screen.getByRole<HTMLButtonElement>('button', { name: 'Save server' }).disabled).toBe(
      true,
    )
    expect(h.update).not.toHaveBeenCalled()
  })

  it('unsubscribes and ignores outstanding reads after unmount', async () => {
    const h = mount()
    const result = deferred<MCPServer>()
    h.get.mockReturnValueOnce(result.promise)
    fireEvent.click(await screen.findByRole('button', { name: 'Edit Weather' }))
    h.unmount()
    expect(h.unsubscribe).toHaveBeenCalledOnce()
    expect(h.disconnect).toHaveBeenCalledOnce()
    result.resolve(server())
    await result.promise
    expect(screen.queryByRole('dialog')).toBeNull()
  })
})

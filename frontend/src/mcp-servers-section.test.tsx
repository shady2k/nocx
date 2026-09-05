// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render } from '@solidjs/testing-library'
import { Dispatcher, RpcError } from './dispatcher'
import { fixedEndpoint } from './endpoint'
import { MCPServersSection } from './mcp-servers-section'
import { MCPServerClient, type MCPServer, type MCPServerSummary } from './mcp-servers-client'
import type { MCPServersChangedParams } from './generated/mcpServers.changed'
import { clearToasts } from './ui'

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
      serverVersion: '1.0.0',
      protocolVersion: '2025-06-18',
      refreshedAt: '2026-09-04T12:00:00Z',
      digest: 'catalog-digest',
      tools: [
        {
          name: 'forecast',
          description: 'Forecast a city',
          inputSchema: {},
          outputSchema: null,
          descriptorDigest: 'forecast-digest',
          enabled: false,
          status: 'new',
        },
      ],
    },
    ...overrides,
  }
}

function summary(record: MCPServer): MCPServerSummary {
  return {
    id: record.id,
    revision: record.revision,
    name: record.name,
    enabled: record.enabled,
    transport: record.transport,
    catalogState: record.catalog.state,
    toolCount: record.catalog.tools.length,
    enabledToolCount: record.catalog.tools.filter((tool) => tool.enabled).length,
    oauthStatus: record.http?.oauth?.status ?? null,
  }
}

function mount(record = server(), records: MCPServer[] = [record]) {
  const client = new MCPServerClient(new Dispatcher(fixedEndpoint(9876)))
  const list = vi.spyOn(client, 'list').mockResolvedValue(records.map(summary))
  const get = vi.spyOn(client, 'get').mockResolvedValue(record)
  const refresh = vi.spyOn(client, 'refresh').mockResolvedValue({
    ...record,
    revision: record.revision + 1,
  })
  let changedHandler: ((params: MCPServersChangedParams) => void) | undefined
  vi.spyOn(client, 'subscribeChanged').mockImplementation((handler) => {
    changedHandler = handler
    return () => {
      changedHandler = undefined
    }
  })
  const setToolsEnabled = vi
    .spyOn(client, 'setToolsEnabled')
    .mockImplementation((_id, revision, names) =>
      Promise.resolve({
        ...record,
        revision: revision + 1,
        catalog: {
          ...record.catalog,
          tools: record.catalog.tools.map((tool) => ({
            ...tool,
            enabled: names.includes(tool.name),
          })),
        },
      }),
    )
  const oauthAuthorize = vi.spyOn(client, 'oauthAuthorize').mockResolvedValue(record)
  const oauthForget = vi.spyOn(client, 'oauthForget').mockResolvedValue(record)
  const update = vi.spyOn(client, 'update').mockResolvedValue(record)
  const container = document.body.appendChild(document.createElement('div'))
  render(() => <MCPServersSection client={client} />, { container })
  return {
    container,
    list,
    get,
    refresh,
    setToolsEnabled,
    oauthAuthorize,
    oauthForget,
    update,
    changed(params: MCPServersChangedParams) {
      changedHandler?.(params)
    },
  }
}

function button(label: string): HTMLButtonElement {
  const match = Array.from(document.querySelectorAll<HTMLButtonElement>('button')).find(
    (candidate) => candidate.textContent?.trim() === label,
  )
  if (!match) throw new Error(`button not found: ${label}`)
  return match
}

afterEach(() => {
  clearToasts()
  vi.clearAllMocks()
  cleanup()
  document.body.innerHTML = ''
})

describe('MCPServersSection', () => {
  it('loads summaries without refreshing tools or starting OAuth, including after local search', async () => {
    const harness = mount()
    await vi.waitFor(() => expect(harness.list).toHaveBeenCalledOnce())

    expect(harness.refresh).not.toHaveBeenCalled()
    expect(harness.container.textContent).toContain('Refresh starts only the selected server')
    expect(harness.oauthAuthorize).not.toHaveBeenCalled()
    expect(harness.get).not.toHaveBeenCalled()

    const search = harness.container.querySelector<HTMLInputElement>(
      '[aria-label="Search MCP servers"]',
    )!
    fireEvent.input(search, { target: { value: 'weather' } })
    expect(harness.container.textContent).toContain('Weather')
    expect(harness.refresh).not.toHaveBeenCalled()

    fireEvent.click(button('Refresh tools'))
    await vi.waitFor(() => expect(harness.refresh).toHaveBeenCalledWith('mcp:weather', 4))
  })

  it('shows fresh new tools disabled and enables one only through the explicit checkbox', async () => {
    const harness = mount()
    await vi.waitFor(() => expect(harness.container.textContent).toContain('Weather'))

    fireEvent.click(button('Weather'))
    await vi.waitFor(() => expect(harness.get).toHaveBeenCalledWith('mcp:weather'))

    const checkbox = document.querySelector<HTMLInputElement>('[aria-label="Enable forecast"]')!
    expect(checkbox).not.toBeNull()
    expect(checkbox.checked).toBe(false)
    expect(document.body.textContent).toContain('New — disabled by default')
    expect(harness.setToolsEnabled).not.toHaveBeenCalled()

    fireEvent.change(checkbox, { target: { checked: true } })
    await vi.waitFor(() =>
      expect(harness.setToolsEnabled).toHaveBeenCalledWith('mcp:weather', 4, ['forecast']),
    )
  })

  it('keeps configuration draft intact across refresh and immediate tool enablement', async () => {
    const harness = mount()
    await vi.waitFor(() => expect(harness.container.textContent).toContain('Weather'))
    fireEvent.click(button('Weather'))
    await vi.waitFor(() => expect(harness.get).toHaveBeenCalledWith('mcp:weather'))

    const name = harness.container.querySelector<HTMLInputElement>('#mcp-server-name')!
    fireEvent.input(name, { target: { value: 'Unsaved weather name' } })
    const refreshButtons = Array.from(
      document.querySelectorAll<HTMLButtonElement>('button'),
    ).filter((candidate) => candidate.textContent?.trim() === 'Refresh tools')
    const refreshButton = refreshButtons[refreshButtons.length - 1]
    if (!refreshButton) throw new Error('Refresh tools button not found')
    fireEvent.click(refreshButton)
    await vi.waitFor(() => expect(harness.refresh).toHaveBeenCalled())
    expect(name.value).toBe('Unsaved weather name')

    expect(document.body.textContent).toContain('saved immediately')
    expect(document.body.textContent).toContain('Cancel draft will not undo them')
    fireEvent.change(document.querySelector<HTMLInputElement>('[aria-label="Enable forecast"]')!, {
      target: { checked: true },
    })
    await vi.waitFor(() => expect(harness.setToolsEnabled).toHaveBeenCalled())
    expect(name.value).toBe('Unsaved weather name')
    fireEvent.click(button('Cancel draft'))
  })
  it('ignores a revision check for a server after switching to another draft', async () => {
    const weather = server()
    const alerts = server({ id: 'mcp:alerts', name: 'Alerts', revision: 8 })
    const harness = mount(weather, [weather, alerts])
    let resolveWeather!: (value: MCPServer) => void
    const pendingWeather = new Promise<MCPServer>((resolve) => {
      resolveWeather = resolve
    })
    harness.get
      .mockResolvedValueOnce(weather)
      .mockReturnValueOnce(pendingWeather)
      .mockResolvedValueOnce(alerts)
    await vi.waitFor(() => expect(harness.list).toHaveBeenCalledOnce())

    fireEvent.click(button('Weather'))
    await vi.waitFor(() => expect(harness.get).toHaveBeenCalledTimes(1))
    harness.changed({ id: weather.id, revision: weather.revision + 1, change: 'updated' })
    await vi.waitFor(() => expect(harness.get).toHaveBeenCalledTimes(2))

    fireEvent.click(button('Cancel draft'))
    fireEvent.click(button('Alerts'))
    await vi.waitFor(() => expect(harness.get).toHaveBeenCalledTimes(3))
    await vi.waitFor(() =>
      expect(harness.container.querySelector<HTMLInputElement>('#mcp-server-name')?.value).toBe(
        'Alerts',
      ),
    )

    resolveWeather({ ...weather, revision: weather.revision + 1 })
    await vi.waitFor(() => {
      expect(harness.container.querySelector<HTMLInputElement>('#mcp-server-name')?.value).toBe(
        'Alerts',
      )
      expect(document.body.textContent).not.toContain('Configuration changed elsewhere')
    })
  })

  it('filters tools locally by name or description and shows enabled-only counts', async () => {
    const record = server({
      catalog: {
        ...server().catalog,
        tools: [
          ...server().catalog.tools,
          {
            ...server().catalog.tools[0],
            name: 'daily_summary',
            description: 'Summarize a forecast',
            enabled: true,
            status: 'unchanged',
          },
        ],
      },
    })
    const harness = mount(record)
    await vi.waitFor(() => expect(harness.container.textContent).toContain('Weather'))
    fireEvent.click(button('Weather'))
    await vi.waitFor(() => expect(harness.get).toHaveBeenCalledWith('mcp:weather'))
    fireEvent.input(harness.container.querySelector<HTMLInputElement>('#mcp-tool-search')!, {
      target: { value: 'summarize' },
    })
    const toolList = harness.container.querySelector('.mcp-tool-list')
    if (!toolList) throw new Error('MCP tool list not found')
    expect(toolList.textContent).toContain('daily_summary')
    expect(
      Array.from(toolList.querySelectorAll('.ui-record-row__title')).map((node) =>
        node.textContent?.trim(),
      ),
    ).toEqual(['daily_summary'])
    fireEvent.click(button('Enabled only'))
    expect(harness.container.textContent).toContain('1 of 2 tools enabled')
    expect(toolList.textContent).toContain('daily_summary')
  })

  it('retains the draft and exposes a retry after a failed catalog refresh', async () => {
    const harness = mount()
    await vi.waitFor(() => expect(harness.container.textContent).toContain('Weather'))
    fireEvent.click(button('Weather'))
    await vi.waitFor(() => expect(harness.get).toHaveBeenCalledWith('mcp:weather'))
    const name = harness.container.querySelector<HTMLInputElement>('#mcp-server-name')!
    fireEvent.input(name, { target: { value: 'Keep this draft' } })
    harness.refresh.mockRejectedValueOnce(new RpcError('server unavailable', -32000))
    const refreshButtons = Array.from(
      document.querySelectorAll<HTMLButtonElement>('button'),
    ).filter((candidate) => candidate.textContent?.trim() === 'Refresh tools')
    const refreshButton = refreshButtons[refreshButtons.length - 1]
    if (!refreshButton) throw new Error('Refresh tools button not found')
    fireEvent.click(refreshButton)
    await vi.waitFor(() => expect(document.body.textContent).toContain('Last refresh failed'))
    expect(name.value).toBe('Keep this draft')
    expect(button('Retry refresh')).toBeTruthy()
  })
  it('clears a refresh warning after another window refreshes the catalog', async () => {
    const harness = mount()
    await vi.waitFor(() => expect(harness.container.textContent).toContain('Weather'))
    fireEvent.click(button('Weather'))
    await vi.waitFor(() => expect(harness.get).toHaveBeenCalledWith('mcp:weather'))
    harness.refresh.mockRejectedValueOnce(new RpcError('server unavailable', -32000))
    const refreshButtons = Array.from(
      document.querySelectorAll<HTMLButtonElement>('button'),
    ).filter((candidate) => candidate.textContent?.trim() === 'Refresh tools')
    const refreshButton = refreshButtons[refreshButtons.length - 1]
    if (!refreshButton) throw new Error('Refresh tools button not found')
    fireEvent.click(refreshButton)
    await vi.waitFor(() => expect(document.body.textContent).toContain('Last refresh failed'))

    const refreshed = server({
      revision: 5,
      catalog: { ...server().catalog, refreshedAt: '2026-09-04T12:05:00Z' },
    })
    harness.list.mockResolvedValueOnce([summary(refreshed)])
    harness.changed({ id: refreshed.id, revision: refreshed.revision, change: 'catalog' })
    await vi.waitFor(() => expect(document.body.textContent).not.toContain('Last refresh failed'))
  })

  it('preserves configuration draft across immediate OAuth connect and forget', async () => {
    const record = server({
      transport: 'streamable-http',
      stdio: null,
      http: {
        endpoint: 'https://example.test/mcp',
        auth: 'oauth',
        headers: [],
        bearer: { secretSet: false, owned: false },
        oauth: {
          registration: 'dynamic',
          clientId: '',
          clientSecret: { secretSet: false, owned: false },
          scopes: [],
          sessionSet: false,
          status: 'missing',
          issuer: '',
          grantedScopes: [],
          accessTokenExpires: null,
        },
      },
    })
    const connected = {
      ...record,
      revision: record.revision + 1,
      http: {
        ...record.http!,
        oauth: { ...record.http!.oauth!, sessionSet: true, status: 'connected' as const },
      },
    }
    const harness = mount(record)
    harness.oauthAuthorize.mockResolvedValue(connected)
    harness.oauthForget.mockResolvedValue({
      ...record,
      revision: record.revision + 2,
    })
    await vi.waitFor(() => expect(harness.list).toHaveBeenCalled())
    fireEvent.click(button('Weather'))
    await vi.waitFor(() => expect(harness.get).toHaveBeenCalledWith('mcp:weather'))
    const name = harness.container.querySelector<HTMLInputElement>('#mcp-server-name')!
    fireEvent.input(name, { target: { value: 'Unsaved OAuth server name' } })
    expect(document.body.textContent).toContain('saved immediately')
    fireEvent.click(button('Connect OAuth'))
    await vi.waitFor(() => expect(harness.oauthAuthorize).toHaveBeenCalled())
    expect(name.value).toBe('Unsaved OAuth server name')
    fireEvent.click(button('Forget OAuth'))
    await vi.waitFor(() => expect(harness.oauthForget).toHaveBeenCalled())
    expect(name.value).toBe('Unsaved OAuth server name')
  })

  it('offers explicit discard or reapply after a revision conflict without claiming success', async () => {
    const record = server()
    const latest = { ...record, revision: 5, name: 'Remote name' }
    const harness = mount(record)
    harness.get.mockResolvedValueOnce(record).mockResolvedValueOnce(latest)
    harness.update.mockRejectedValueOnce(
      new RpcError('revision conflict', -32000, { reason: 'conflict' }),
    )
    await vi.waitFor(() => expect(harness.list).toHaveBeenCalled())
    fireEvent.click(button('Weather'))
    await vi.waitFor(() => expect(harness.get).toHaveBeenCalledOnce())
    const name = harness.container.querySelector<HTMLInputElement>('#mcp-server-name')!
    fireEvent.input(name, { target: { value: 'My preserved draft' } })
    fireEvent.click(button('Save configuration'))
    await vi.waitFor(() => expect(button('Reapply my draft')).toBeTruthy())
    expect(name.value).toBe('My preserved draft')
    expect(document.body.textContent).toContain('has not been saved')
    fireEvent.click(button('Reapply my draft'))
    expect(name.value).toBe('My preserved draft')
    expect(document.body.textContent).toContain('nothing has been saved yet')
    fireEvent.click(button('Save configuration'))
    await vi.waitFor(() => expect(harness.update).toHaveBeenCalledTimes(2))
    expect(harness.update).toHaveBeenLastCalledWith(
      'mcp:weather',
      latest.revision,
      expect.objectContaining({ name: 'My preserved draft' }),
    )
  })

  it('lets a user discard a conflicted draft and load the current server revision', async () => {
    const record = server()
    const latest = { ...record, revision: 5, name: 'Remote name' }
    const harness = mount(record)
    harness.get.mockResolvedValueOnce(record).mockResolvedValueOnce(latest)
    harness.update.mockRejectedValueOnce(
      new RpcError('revision conflict', -32000, { reason: 'conflict' }),
    )
    await vi.waitFor(() => expect(harness.list).toHaveBeenCalled())
    fireEvent.click(button('Weather'))
    await vi.waitFor(() => expect(harness.get).toHaveBeenCalledOnce())
    fireEvent.input(harness.container.querySelector<HTMLInputElement>('#mcp-server-name')!, {
      target: { value: 'Discard me' },
    })
    fireEvent.click(button('Save configuration'))
    await vi.waitFor(() => expect(button('Discard draft and reload')).toBeTruthy())
    fireEvent.click(button('Discard draft and reload'))
    expect(harness.container.querySelector<HTMLInputElement>('#mcp-server-name')!.value).toBe(
      'Remote name',
    )
  })
})

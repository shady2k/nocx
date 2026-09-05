// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render } from '@solidjs/testing-library'
import { Dispatcher } from './dispatcher'
import { fixedEndpoint } from './endpoint'
import { MCPServersSection } from './mcp-servers-section'
import { MCPServerClient, type MCPServer, type MCPServerSummary } from './mcp-servers-client'
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

function mount(record = server()) {
  const client = new MCPServerClient(new Dispatcher(fixedEndpoint(9876)))
  const list = vi.spyOn(client, 'list').mockResolvedValue([summary(record)])
  const get = vi.spyOn(client, 'get').mockResolvedValue(record)
  const refresh = vi.spyOn(client, 'refresh').mockResolvedValue({
    ...record,
    revision: record.revision + 1,
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
  const container = document.body.appendChild(document.createElement('div'))
  render(() => <MCPServersSection client={client} />, { container })
  return { container, list, get, refresh, setToolsEnabled, oauthAuthorize, oauthForget }
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
})

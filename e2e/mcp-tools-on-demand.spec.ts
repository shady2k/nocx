/** ADR-0102: Settings discovery is explicit; local search is not activation,
 * and only the exact admitted delegate call may start the saved executable. */
import { randomUUID } from 'node:crypto'
import { existsSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import {
  standalone as test,
  expect,
  type Page,
  type ControlPlane,
  type BackendEndpoint,
  appReadyForInput,
  VaultBackend,
  bindEndpoint,
  createAiEndpoint,
  documentDir,
  openControlPlane,
  resolveBackend,
  setDefaultModel,
  settingsReady,
} from './harness'
import { FakeOpenAI, type ScriptedToolCall } from './fake-openai'
import { bindSecretFromLock, fieldChip } from './secret-field'
import { readStand } from './stand'

const serverBin = () => readStand().server
const INPUT = '.pane.active .nocx-editor-input'
const REMOTE_TOOL = 'fetch_fixture_report'
const OTHER_TOOL = 'unused_fixture_tool'
const TOPIC = 'approval-proof'
const APPROVAL_TITLE = 'This action needs your approval'
const DISCOVERY_EVENTS = ['start', 'list', 'list', 'stop']

interface MCPEvent {
  event: 'start' | 'list' | 'call' | 'stop'
  pid: number
  cursor?: string | null
  name?: string
  arguments?: Record<string, unknown>
  nonce?: string
}
interface ModelBody {
  tools?: { function?: { name?: string } }[]
  messages?: { role?: string; content?: unknown }[]
}
interface SavedServer {
  id: string
  name: string
  revision: number
  catalog: { state: string; tools: { name: string; enabled: boolean }[] }
}
interface LedgerDetail {
  entry: unknown
  artifacts: { id: string }[]
  caused: {
    entryId: string
    kind: string
    intent: string
    args: unknown
    effect: string | null
    resource: { kind: string; id: string } | null
  }[]
}

let root = ''
let eventsPath = ''
let backend: VaultBackend | undefined
let endpoint: BackendEndpoint
let fake: FakeOpenAI
let nonce: string
let serverName: string
let endpointName: string
let secretName: string
let secretValue: string
let providerKey: string

function events(): MCPEvent[] {
  if (!eventsPath || !existsSync(eventsPath)) return []
  const text = readFileSync(eventsPath, 'utf8')
  // An append in progress is not yet an event; consume complete JSONL records.
  return text
    .slice(0, text.lastIndexOf('\n') + 1)
    .split('\n')
    .filter(Boolean)
    .map((line) => JSON.parse(line) as MCPEvent)
}
function eventNames(): string[] {
  return events().map((event) => event.event)
}
function processGone(pid: number): boolean {
  try {
    process.kill(pid, 0)
    return false
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === 'ESRCH') return true
    throw error
  }
}
async function childrenExited(): Promise<void> {
  const pids = [...new Set(events().map((event) => event.pid))]
  await expect.poll(() => pids.every(processGone), { timeout: 15_000 }).toBe(true)
}
function assertPrivate(value: unknown): void {
  expect(typeof value === 'string' ? value : JSON.stringify(value)).not.toContain(secretValue)
}

function offeredMCPNames(body: string): string[] {
  return ((JSON.parse(body) as ModelBody).tools ?? [])
    .map((tool) => tool.function?.name ?? '')
    .filter((name) => /^mcp_[a-z2-7]{32}$/.test(name))
}
function toolResults(body: string): string[] {
  return ((JSON.parse(body) as ModelBody).messages ?? [])
    .filter((message) => message.role === 'tool' && typeof message.content === 'string')
    .map((message) => message.content as string)
}
function unframe(result: string): unknown {
  const framed = result.match(/<tool-output>\n([\s\S]*)\n<\/tool-output>/)
  return JSON.parse(framed ? framed[1] : result) as unknown
}
function searchResult(body: string): { tools: { name: string }[] } {
  for (const result of toolResults(body).toReversed()) {
    const parsed = unframe(result) as { tools?: { name: string }[] }
    if (Array.isArray(parsed.tools)) return { tools: parsed.tools }
  }
  throw new Error('The provider received no tools.search result')
}
function answerBlock(page: Page, question: string) {
  return page.locator('.cmd-block[data-block-kind="ask"]').filter({ hasText: question })
}
async function ask(page: Page, question: string): Promise<void> {
  const input = page.locator(INPUT)
  await input.click()
  const indicator = page.locator('.pane.active .ui-mode-indicator:visible')
  if ((await indicator.getAttribute('data-target')) !== 'agent') {
    await page.keyboard.press('ControlOrMeta+Enter')
    await expect(indicator).toHaveAttribute('data-target', 'agent', { timeout: 10_000 })
  }
  await input.fill(question)
  await page.keyboard.press('Enter')
}
async function finished(page: Page, question: string, answer: string): Promise<void> {
  const block = answerBlock(page, question)
  await expect(block).toHaveAttribute('data-outcome', 'success', { timeout: 45_000 })
  await expect(block.locator('[data-answer-body]')).toContainText(answer)
}
async function terminal(page: Page): Promise<void> {
  await page.locator('.nocx-tab-title').first().click()
  await expect(page.locator(INPUT)).toBeVisible({ timeout: 10_000 })
  await appReadyForInput(page)
}
async function settings(page: Page, section: string): Promise<void> {
  await page.keyboard.press('Meta+,')
  await settingsReady(page)
  await page.locator(`[data-item="${section}"]`).click()
}
async function editor(page: Page) {
  await page.getByRole('button', { name: `Edit ${serverName}`, exact: true }).click()
  const dialog = page.getByRole('dialog', { name: `Edit MCP server: ${serverName}`, exact: true })
  await expect(dialog.locator('#mcp-name')).toHaveValue(serverName)
  return dialog
}
async function getServer(wire: ControlPlane, id: string): Promise<SavedServer> {
  const result = (await wire.call('mcpServers.get', { id })) as { server: SavedServer }
  assertPrivate(result)
  expect(JSON.stringify(result)).not.toContain('secretRef')
  return result.server
}

// The executable and all mutable artifacts belong to this standalone test's
// backend. Nothing reads the developer's HOME or nocx-dev log.
test.beforeAll(async () => {
  root = mkdtempSync(join(tmpdir(), 'nocx-mcp-e2e-'))
  nonce = randomUUID()
  serverName = `E2E MCP server ${nonce}`
  endpointName = `E2E MCP endpoint ${nonce}`
  secretName = `Dedicated MCP secret ${nonce}`
  secretValue = `mcp-sensitive-${randomUUID()}-value`
  providerKey = `e2e-provider-key-${randomUUID()}`
  fake = new FakeOpenAI()
  await fake.start()
  backend = new VaultBackend(serverBin(), { root })
  endpoint = await backend.start()
  eventsPath = join(documentDir(backend.isolatedHome), 'mcp-e2e-events.jsonl')
})

test.afterAll(async () => {
  backend?.stop()
  await fake?.stop()
  // Failed journeys also leave no fixture behind. Successful cleanup is proved
  // in the test BEFORE this safety cleanup can intervene.
  for (const pid of new Set(events().map((event) => event.pid))) {
    if (!processGone(pid)) process.kill(pid, 'SIGTERM')
  }
  await childrenExited()
  if (root) rmSync(root, { recursive: true, force: true })
})

test.describe('MCP tools activate only after approval (nocx-ga29v.7)', () => {
  test.use({ viewport: { width: 1280, height: 900 } })

  test('dormant catalog, refusal, exact approval, private nonce result and child cleanup', async ({
    page,
  }) => {
    test.setTimeout(240_000)
    const received: string[] = []
    page.on('websocket', (socket) =>
      socket.on('framereceived', ({ payload }) => {
        if (typeof payload === 'string') received.push(payload)
      }),
    )
    await bindEndpoint(page, endpoint)
    await page.goto('/')
    expect(await resolveBackend(page)).toEqual(endpoint)
    await appReadyForInput(page)
    await expect(page.locator(INPUT)).toBeVisible({ timeout: 15_000 })

    await settings(page, 'endpoints')
    await createAiEndpoint(page, {
      name: endpointName,
      baseUrl: fake.baseUrl(),
      models: ['e2e-model'],
      key: providerKey,
      vaultPassphrase: `vault-pass-${nonce}`,
    })
    await page.locator('[data-item="roles"]').click()
    await setDefaultModel(page, endpointName, 'e2e-model')
    await page.locator('[data-item="secrets"]').click()
    await page.getByRole('button', { name: '+ Add a secret', exact: true }).click()
    const addSecret = page.getByRole('dialog', { name: 'Add secret', exact: true })
    await addSecret.locator('#sr-add-name').fill(secretName)
    await addSecret.locator('#sr-add-value').fill(secretValue)
    await addSecret.getByRole('button', { name: 'Add secret', exact: true }).click()
    await expect(addSecret).not.toBeVisible({ timeout: 10_000 })

    await page.locator('[data-item="mcpServers"]').click()
    await page.getByRole('button', { name: 'New server', exact: true }).click()
    const create = page.getByRole('dialog', { name: 'New MCP server', exact: true })
    await create.locator('#mcp-name').fill(serverName)
    await create.locator('#mcp-command').fill(process.execPath)
    await create.getByRole('button', { name: 'Add argument', exact: true }).click()
    await create
      .getByLabel('Argument 1', { exact: true })
      .fill(resolve(process.cwd(), 'e2e/fixtures/fake-mcp-stdio.mjs'))
    await create.getByRole('button', { name: 'Add environment variable', exact: true }).click()
    await create.locator('#mcp-env-0-name').fill('NOCX_MCP_EVENTS')
    await create.getByLabel('Value', { exact: true }).fill(eventsPath)
    await create.getByRole('button', { name: 'Add environment variable', exact: true }).click()
    await create.locator('#mcp-env-1-name').fill('NOCX_MCP_SECRET')
    await create.locator('#mcp-env-1-kind').selectOption('secret')
    const secretField = create.locator('#mcp-env-1-secret')
    await bindSecretFromLock(page, secretField, secretName)
    await expect(fieldChip(secretField)).toContainText(secretName)
    await create.getByRole('button', { name: 'Save server', exact: true }).click()
    await expect(create).not.toBeVisible({ timeout: 10_000 })
    await expect(page.getByRole('list', { name: 'MCP servers', exact: true })).toContainText(
      '0/0 tools enabled',
    )
    expect(events()).toEqual([])

    const wire = await openControlPlane(endpoint.port, endpoint.token)
    try {
      const listed = (await wire.call('mcpServers.list', {})) as {
        servers: { id: string; name: string }[]
      }
      assertPrivate(listed)
      const saved = listed.servers.find((server) => server.name === serverName)
      expect(saved).toBeDefined()
      const id = saved!.id
      const beforeRefresh = await getServer(wire, id)
      expect(beforeRefresh.catalog.tools).toEqual([])
      const currentPolicy = (await wire.call('policy.get', {})) as {
        policy: Record<string, unknown>
      }
      const policy = { ...currentPolicy.policy }
      delete policy.rules
      policy.delegate = {
        decision: 'ask',
        scopes: [{ kind: 'destination', id: `mcp+stdio:${id}` }],
      }
      await wire.call('policy.set', { policy })

      // Opening, reading, cancelling, and reloading Settings never discovers.
      let dialog = await editor(page)
      await expect(dialog.getByText('No discovered tools', { exact: true })).toBeVisible()
      await dialog.getByRole('button', { name: 'Cancel', exact: true }).click()
      await page.reload()
      expect(await resolveBackend(page)).toEqual(endpoint)
      await appReadyForInput(page)
      await terminal(page)
      fake.setScript({
        chunks: (body) => {
          expect(offeredMCPNames(body)).toEqual([])
          return ['The unrefreshed MCP server has no offered tools.']
        },
      })
      const dormantQuestion = `List the available tools without running anything, ${nonce}.`
      await ask(page, dormantQuestion)
      await finished(page, dormantQuestion, 'The unrefreshed MCP server has no offered tools.')
      expect(events(), 'setup and assistant initialization must remain dormant').toEqual([])

      await settings(page, 'mcpServers')
      dialog = await editor(page)
      // Immediate actions must preserve a dirty draft and use the saved config.
      const dirtyName = `${serverName} unsaved`
      await dialog.locator('#mcp-name').fill(dirtyName)
      await dialog.getByRole('button', { name: 'Refresh tools', exact: true }).click()
      await expect(dialog.getByText('Catalog fresh', { exact: true })).toBeVisible({
        timeout: 30_000,
      })
      await expect.poll(eventNames, { timeout: 20_000 }).toEqual(DISCOVERY_EVENTS)
      await childrenExited()
      expect(
        events()
          .filter((event) => event.event === 'list')
          .map((event) => event.cursor),
      ).toEqual([null, 'remaining-tools'])
      const enabled = dialog.getByRole('switch', { name: `Enable ${REMOTE_TOOL}`, exact: true })
      await expect(enabled).not.toBeChecked()
      await expect(
        dialog.getByRole('switch', { name: `Enable ${OTHER_TOOL}`, exact: true }),
      ).not.toBeChecked()
      await expect(dialog.locator('#mcp-name')).toHaveValue(dirtyName)
      expect((await getServer(wire, id)).name).toBe(serverName)
      await enabled.click()
      await expect(enabled).toBeChecked({ timeout: 10_000 })
      await expect(dialog.locator('#mcp-name')).toHaveValue(dirtyName)
      await expect(dialog.getByText('Server changed elsewhere', { exact: true })).not.toBeVisible()
      await dialog.getByLabel('Search catalog', { exact: true }).fill(REMOTE_TOOL)
      await expect(
        dialog.getByRole('switch', { name: `Enable ${OTHER_TOOL}`, exact: true }),
      ).toHaveCount(0)
      await dialog.getByRole('checkbox', { name: 'Enabled tools only', exact: true }).check()
      await expect(enabled).toBeVisible()
      await dialog.getByRole('button', { name: 'Cancel', exact: true }).click()

      // A second client's revision change is visible and never erases the draft.
      dialog = await editor(page)
      await dialog.locator('#mcp-name').fill(dirtyName)
      const current = await getServer(wire, id)
      await wire.call('mcpServers.setToolsEnabled', { id, revision: current.revision, tools: [] })
      await expect(dialog.getByText('Server changed elsewhere', { exact: true })).toBeVisible()
      await expect(dialog.locator('#mcp-name')).toHaveValue(dirtyName)
      await expect(dialog.getByRole('button', { name: 'Save server', exact: true })).toBeDisabled()
      await dialog.getByRole('button', { name: 'Discard draft and reload', exact: true }).click()
      await expect(dialog.locator('#mcp-name')).toHaveValue(serverName)
      await expect(enabled).not.toBeChecked()
      await enabled.click()
      await expect(enabled).toBeChecked({ timeout: 10_000 })
      await dialog.getByRole('button', { name: 'Cancel', exact: true }).click()
      await page.reload()
      await appReadyForInput(page)
      await settings(page, 'mcpServers')
      await expect(page.getByRole('list', { name: 'MCP servers', exact: true })).toContainText(
        '1/2 tools enabled',
      )
      const persisted = readFileSync(
        join(documentDir(backend!.isolatedHome), 'profiles.json'),
        'utf8',
      )
      assertPrivate(persisted)
      expect(persisted).toContain(REMOTE_TOOL)
      expect(persisted).toContain('secretRef')
      expect(eventNames()).toEqual(DISCOVERY_EVENTS)
      await terminal(page)

      let modelTool = ''
      const search = () =>
        fake.setScript({
          chunks: [],
          toolCalls: [{ name: 'tools.search', arguments: { query: REMOTE_TOOL } }],
        })
      const selectTool = (body: string): ScriptedToolCall[] => {
        const names = offeredMCPNames(body)
        expect(names).toHaveLength(1)
        modelTool = names[0]
        expect(searchResult(body).tools.map((tool) => tool.name)).toContain(modelTool)
        return [{ name: modelTool, id: 'call_mcp_report', arguments: { topic: TOPIC } }]
      }
      search()
      fake.setScript({
        chunks: (body) => {
          selectTool(body)
          return ['The saved local catalog offers the report tool; I have not invoked it.']
        },
      })
      const searchQuestion = `Search the local catalog only, ${nonce}.`
      await ask(page, searchQuestion)
      await finished(
        page,
        searchQuestion,
        'The saved local catalog offers the report tool; I have not invoked it.',
      )
      expect(eventNames(), 'local catalog search must not reconnect').toEqual(DISCOVERY_EVENTS)

      search()
      fake.setScript({ chunks: [], toolCalls: selectTool })
      fake.setScript({
        chunks: (body) => {
          expect(toolResults(body).join('\n')).toContain('REFUSED')
          return ['The call was refused; no report was fetched.']
        },
      })
      const deniedQuestion = `Propose fetching the report so I can decline it, ${nonce}.`
      await ask(page, deniedQuestion)
      const approval = page.getByRole('dialog', { name: APPROVAL_TITLE, exact: true })
      await expect(approval).toBeVisible({ timeout: 30_000 })
      await expect(approval).toContainText(modelTool)
      await expect(approval).toContainText(TOPIC)
      expect(eventNames(), 'waiting for approval must not start a child').toEqual(DISCOVERY_EVENTS)
      await approval.getByRole('button', { name: /^Deny once\b/ }).click()
      await finished(page, deniedQuestion, 'The call was refused; no report was fetched.')
      expect(eventNames(), 'a refused proposal must not start a child').toEqual(DISCOVERY_EVENTS)

      const firstApprovedRequest = fake.requests().length
      search()
      fake.setScript({ chunks: [], toolCalls: selectTool })
      fake.setScript({
        chunks: (body) => {
          assertPrivate(body)
          const envelopes = toolResults(body)
            .filter((result) => result.includes('"serverId"'))
            .map(unframe) as {
            tool?: string
            isError?: boolean
            text?: string[]
            structuredContent?: { summary: string; nonce: string; credentials: { apiKey: string } }
          }[]
          const result = envelopes.findLast((item) => item.tool === REMOTE_TOOL)
          expect(result?.isError).toBe(false)
          expect(result?.structuredContent?.nonce).toMatch(/^[0-9a-f-]{36}$/)
          // Only provider input is consulted here, never the event file or an
          // expected nonce supplied by the test.
          return [`MCP proof: ${result!.structuredContent!.summary}; credential redacted.`]
        },
      })
      const approvedQuestion = `Fetch the report in one approved call, ${nonce}.`
      await ask(page, approvedQuestion)
      await expect(approval).toBeVisible({ timeout: 30_000 })
      await expect(approval).toContainText(modelTool)
      await expect(approval).toContainText(TOPIC)
      expect(eventNames()).toEqual(DISCOVERY_EVENTS)
      await approval.getByRole('button', { name: /^Allow once\b/ }).click()
      await expect
        .poll(eventNames, { timeout: 30_000 })
        .toEqual([...DISCOVERY_EVENTS, 'start', 'list', 'list', 'call', 'stop'])
      await childrenExited()
      const calls = events().filter((event) => event.event === 'call')
      expect(calls).toHaveLength(1)
      expect(calls[0]).toMatchObject({ name: REMOTE_TOOL, arguments: { topic: TOPIC } })
      expect(calls[0].nonce).toMatch(/^[0-9a-f-]{36}$/)
      const summary = `fixture-report:${TOPIC}:${calls[0].nonce}`
      await finished(page, approvedQuestion, `MCP proof: ${summary}; credential redacted.`)
      expect(new Set(events().map((event) => event.pid)).size).toBe(2)

      const turn = answerBlock(page, approvedQuestion)
      await expect(
        turn.locator(`.cmd-block[data-block-kind="tool"][data-tool="${modelTool}"]`),
      ).toHaveCount(1)
      const rows = (await wire.call('ledger.query', { scope: 'everywhere', limit: 200 })) as {
        entries: { id: string; intent: string }[]
      }
      const entry = rows.entries.find((row) => row.intent === approvedQuestion)
      expect(entry).toBeDefined()
      const detail = (await wire.call('ledger.get', { id: entry!.id })) as LedgerDetail
      const recordedCall = detail.caused.filter((child) => child.intent === modelTool)
      expect(recordedCall).toEqual([
        expect.objectContaining({
          kind: 'action',
          args: { topic: TOPIC },
          effect: 'delegate',
          resource: { kind: 'destination', id: `mcp+stdio:${id}` },
        }),
      ])
      assertPrivate(rows)
      assertPrivate(detail)
      for (const child of detail.caused) {
        const childDetail = (await wire.call('ledger.get', { id: child.entryId })) as LedgerDetail
        assertPrivate(childDetail)
        for (const artifact of childDetail.artifacts) {
          assertPrivate(await wire.call('ledger.artifact', { id: artifact.id }))
        }
      }

      const approvedRequests = fake
        .requests()
        .slice(firstApprovedRequest)
        .filter((request) => request.path.endsWith('/chat/completions'))
      expect(approvedRequests).toHaveLength(3)
      expect(approvedRequests[0].body).not.toContain(calls[0].nonce!)
      expect(approvedRequests[1].body).not.toContain(calls[0].nonce!)
      expect(approvedRequests[2].body).toContain(summary)
      expect(approvedRequests[2].body).toContain('[REDACTED]')
      expect(approvedRequests.map((request) => request.authorization)).toEqual(
        Array(3).fill(`Bearer ${providerKey}`),
      )
      assertPrivate(fake.requests())
      assertPrivate(received)
      const notices = received
        .map(
          (frame) =>
            JSON.parse(frame) as {
              method?: string
              params?: Record<string, unknown>
            },
        )
        .filter((frame) => frame.method === 'mcpServers.changed')
      expect(notices.length).toBeGreaterThan(0)
      for (const notice of notices) {
        expect(Object.keys(notice.params!).sort()).toEqual(['change', 'id', 'revision'])
        expect(notice.params!.id).toBe(id)
      }
      assertPrivate(readFileSync(join(documentDir(backend!.isolatedHome), 'profiles.json'), 'utf8'))
      assertPrivate(readFileSync(backend!.logFile, 'utf8'))
    } finally {
      wire.close()
    }
  })
})

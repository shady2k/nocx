/**
 * Browser proof for ADR-0057's activation boundary.
 *
 * A person creates and refreshes a real stdio MCP server in Settings, enables
 * its discovered tool, and asks the assistant to use it. The fake model first
 * calls tools.search, then calls the provider name the product exposed. The
 * MCP child records process/list/call/stop events to a file outside the app.
 * That file must remain empty through the approval prompt and contain exactly
 * one invocation after Allow once. The final answer is derived from the actual
 * tool result sent back to the model, not from a fixed fixture response.
 */
import { test as base, expect, type Page } from '@playwright/test'
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import {
  appReadyForInput,
  VaultBackend,
  bindEndpoint,
  createAiEndpoint,
  documentDir,
  openControlPlane,
  setDefaultModel,
  settingsReady,
} from './harness'
import { FakeOpenAI, type ScriptedToolCall } from './fake-openai'
import { bindSecretFromLock } from './secret-field'
import { readStand } from './stand'

const test = base
const serverBin = () => readStand().server

const TITLE = '.nocx-tab-title'
const INPUT = '.pane.active .nocx-editor-input'
const SETTINGS_AI_NAV = '.ui-grouped-nav__item[data-item="endpoints"]'
const SETTINGS_SECRETS_NAV = '.ui-grouped-nav__item[data-item="secrets"]'
const SETTINGS_ROLES_NAV = '.ui-grouped-nav__item[data-item="roles"]'
const SETTINGS_MCP_NAV = '.ui-grouped-nav__item[data-item="mcpServers"]'
const APPROVAL_TITLE = 'This action needs your approval'
const REMOTE_TOOL = 'fetch_fixture_report'
const TOPIC = 'approval-proof'
const RESULT_MARKER = `fixture-report:${TOPIC}`

const nonce = Date.now().toString(36)
const endpointName = `E2E MCP endpoint ${nonce}`
const serverName = `E2E MCP server ${nonce}`
const providerKey = `e2e-provider-key-${nonce}`
const secretName = `E2E MCP secret ${nonce}`
const secretValue = `mcp-sensitive-${nonce}-value`
const root = mkdtempSync(join(tmpdir(), 'nocx-mcp-e2e-'))
const eventsPath = join(root, 'mcp-events.jsonl')
const fixturePath = resolve(process.cwd(), 'e2e/fixtures/fake-mcp-stdio.mjs')

let backend: VaultBackend
let endpoint: { port: number; token: string }
let fake: FakeOpenAI
let modelMCPTool = ''

test.describe.configure({ mode: 'serial' })

test.beforeAll(async () => {
  fake = new FakeOpenAI()
  await fake.start()
  backend = new VaultBackend(serverBin(), { root })
  endpoint = await backend.start()
})

test.afterAll(async () => {
  backend?.stop()
  try {
    await fake?.stop()
  } finally {
    rmSync(root, { recursive: true, force: true })
  }
})

interface MCPEvent {
  event: 'start' | 'list' | 'call' | 'stop'
  pid: number
  name?: string
  arguments?: Record<string, unknown>
}

function events(): MCPEvent[] {
  if (!existsSync(eventsPath)) return []
  return readFileSync(eventsPath, 'utf8')
    .split('\n')
    .filter(Boolean)
    .map((line) => JSON.parse(line) as MCPEvent)
}

function eventNames(): string[] {
  return events().map((event) => event.event)
}

async function openApp(page: Page): Promise<void> {
  await bindEndpoint(page, endpoint)
  await page.goto('/')
  await expect(page.locator(TITLE).first()).not.toHaveText('', { timeout: 15_000 })
  await appReadyForInput(page)
}

async function askFromPrompt(page: Page, question: string): Promise<void> {
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

function answerBlock(page: Page, question: string) {
  return page.locator('.cmd-block').filter({ hasText: question })
}

function callDiscoveredMCPTool(body: string): ScriptedToolCall[] {
  const parsed = JSON.parse(body) as {
    tools?: { function?: { name?: string; description?: string } }[]
  }
  const declaration = (parsed.tools ?? []).find((tool) =>
    tool.function?.description?.includes('activated only after nocx validates and authorizes'),
  )
  const name = declaration?.function?.name
  if (!name?.startsWith('mcp_')) {
    throw new Error('tools.search did not load the configured MCP tool')
  }
  modelMCPTool = name
  return [{ name, arguments: { topic: TOPIC } }]
}

function answerFromMCPResult(body: string): string[] {
  const parsed = JSON.parse(body) as { messages?: { role?: string; content?: unknown }[] }
  const results = (parsed.messages ?? [])
    .filter((message) => message.role === 'tool' && typeof message.content === 'string')
    .map((message) => message.content as string)
  const result = results.find((content) => content.includes(RESULT_MARKER)) ?? ''
  if (result === '') return [`MCP result missing: ${results.join(' | ')}`]
  if (result.includes(secretValue)) return ['The MCP result leaked its credential.']
  if (!result.includes('[REDACTED]')) return ['The MCP result was not structurally redacted.']
  return [`MCP proof: ${RESULT_MARKER}; credential redacted.`]
}

async function configureAssistant(page: Page): Promise<void> {
  await page.keyboard.press('Meta+,')
  await settingsReady(page)
  await page.locator(SETTINGS_AI_NAV).click()
  await expect(page.locator('.ep-root')).toBeVisible({ timeout: 10_000 })
  await createAiEndpoint(page, {
    name: endpointName,
    baseUrl: fake.baseUrl(),
    models: ['e2e-model'],
    key: providerKey,
    vaultPassphrase: `vault-pass-${nonce}`,
  })
  await page.locator(SETTINGS_ROLES_NAV).click()
  await setDefaultModel(page, endpointName, 'e2e-model')
}

async function createMCPSecret(page: Page): Promise<void> {
  await page.locator(SETTINGS_SECRETS_NAV).click()
  await expect(page.locator('.sr-root')).toBeVisible({ timeout: 10_000 })
  await page.getByRole('button', { name: '+ Add a secret', exact: true }).click()

  const dialog = page.getByRole('dialog', { name: 'Add secret' })
  await expect(dialog).toBeVisible()
  await dialog.getByRole('textbox', { name: 'Name', exact: true }).fill(secretName)
  await dialog.getByRole('textbox', { name: 'Value', exact: true }).fill(secretValue)
  await dialog.getByRole('button', { name: 'Add secret', exact: true }).click()
  await expect(dialog).not.toBeVisible({ timeout: 10_000 })
  await expect(page.locator('.ui-collection-row').filter({ hasText: secretName })).toBeVisible()
}

async function createMCPServer(page: Page): Promise<void> {
  await page.locator(SETTINGS_MCP_NAV).click()
  await expect(page.locator('.mcp-servers-root')).toBeVisible({ timeout: 10_000 })
  await page
    .getByRole('button', { name: /New MCP server|Add server/ })
    .first()
    .click()

  const dialog = page.getByRole('dialog', { name: 'New MCP server' })
  await expect(dialog).toBeVisible()
  await dialog.locator('#mcp-server-name').fill(serverName)
  await dialog.locator('#mcp-command').fill(process.execPath)
  await dialog.getByRole('button', { name: 'Add argument' }).click()
  await dialog.getByRole('textbox', { name: 'Argument 1', exact: true }).fill(fixturePath)

  await dialog.getByRole('button', { name: 'Add environment variable' }).click()
  await dialog.getByLabel('Environment variable 1 name').fill('NOCX_MCP_EVENTS')
  await dialog.getByLabel('Environment variable 1 value').fill(eventsPath)

  await dialog.getByRole('button', { name: 'Add environment variable' }).click()
  await dialog.getByLabel('Environment variable 2 name').fill('NOCX_MCP_SECRET')
  await dialog.getByLabel('Environment variable 2 source').selectOption('secret')
  const secretField = dialog.getByLabel('Environment variable 2 secret')
  await bindSecretFromLock(page, secretField, secretName)

  await dialog.getByRole('button', { name: 'Save', exact: true }).click()
  await expect(dialog).not.toBeVisible({ timeout: 10_000 })
}

async function refreshAndEnableTool(page: Page): Promise<void> {
  const row = page.locator('.ui-collection-row').filter({ hasText: serverName })
  await expect(row).toBeVisible({ timeout: 10_000 })
  await row.getByRole('button', { name: 'Refresh tools' }).click()
  await expect(row).toContainText('0 of 1 tools enabled', { timeout: 20_000 })
  await expect.poll(eventNames, { timeout: 20_000 }).toEqual(['start', 'list', 'stop'])

  writeFileSync(eventsPath, '', { encoding: 'utf8', mode: 0o600 })
  await row.locator('.ui-record-row__title').click()
  const dialog = page.getByRole('dialog', { name: `Edit ${serverName}` })
  await expect(dialog).toBeVisible({ timeout: 10_000 })
  const enabled = dialog.getByRole('checkbox', { name: `Enable ${REMOTE_TOOL}` })
  await enabled.check()
  await expect(enabled).toBeChecked({ timeout: 10_000 })
  await dialog.getByRole('button', { name: 'Cancel', exact: true }).click()
  await expect(dialog).not.toBeVisible({ timeout: 10_000 })
  await expect(row).toContainText('1 of 1 tools enabled', { timeout: 10_000 })
}

test.describe('MCP tools activate only after approval (nocx-ga29v.7)', () => {
  test.use({ viewport: { width: 1280, height: 900 } })

  test('Settings to final answer uses one approved call and redacts its secret', async ({
    page,
  }) => {
    test.setTimeout(180_000)
    await openApp(page)
    await configureAssistant(page)
    await createMCPSecret(page)
    await createMCPServer(page)

    const wire = await openControlPlane(endpoint.port, endpoint.token)
    try {
      const listed = (await wire.call('mcpServers.list', {})) as {
        servers: { id: string; name: string }[]
      }
      const saved = listed.servers.find((server) => server.name === serverName)
      expect(saved).toBeDefined()
      const got = await wire.call('mcpServers.get', { id: saved!.id })
      expect(JSON.stringify({ listed, got })).not.toContain(secretValue)
      expect(JSON.stringify({ listed, got })).not.toContain('secretRef')
    } finally {
      wire.close()
    }

    await refreshAndEnableTool(page)
    expect(events()).toEqual([])

    const profiles = readFileSync(join(documentDir(backend.isolatedHome), 'profiles.json'), 'utf8')
    expect(profiles).not.toContain(secretValue)
    expect(profiles).toContain('secretRef')

    await page.locator(TITLE).first().click()
    await expect(page.locator(INPUT)).toBeVisible({ timeout: 10_000 })

    fake.setScript({
      chunks: [],
      toolCalls: [{ name: 'tools.search', arguments: { query: REMOTE_TOOL } }],
    })
    fake.setScript({ chunks: [], toolCalls: callDiscoveredMCPTool })
    fake.setScript({ chunks: answerFromMCPResult })

    const question = 'Use the configured fixture tool to get the approval proof status.'
    expect(question).not.toContain(RESULT_MARKER)
    const firstRequest = fake.requests().length
    await askFromPrompt(page, question)

    await fake.waitForRequests(firstRequest + 2, 30_000)
    const prompt = page.getByRole('dialog', { name: APPROVAL_TITLE })
    await expect(prompt).toBeVisible({ timeout: 30_000 })
    expect(modelMCPTool).toMatch(/^mcp_/)
    await expect(prompt).toContainText(modelMCPTool)

    expect(events(), 'the MCP child started before the person approved the exact call').toEqual([])
    await prompt.getByRole('button', { name: 'Allow once' }).click()

    const answer = answerBlock(page, question)
    await expect(answer.locator('.cmd-header-exit')).toHaveText('completed', { timeout: 45_000 })
    await expect(answer.locator('[data-answer-body]')).toContainText(
      `MCP proof: ${RESULT_MARKER}; credential redacted.`,
    )

    await expect.poll(eventNames, { timeout: 30_000 }).toEqual(['start', 'list', 'call', 'stop'])
    const recorded = events()
    const calls = recorded.filter((event) => event.event === 'call')
    expect(calls).toEqual([
      expect.objectContaining({ name: REMOTE_TOOL, arguments: { topic: TOPIC } }),
    ])
    expect(new Set(recorded.map((event) => event.pid)).size).toBe(1)

    const toolBlocks = answer.locator(
      `:scope > .cmd-children > .cmd-block[data-block-kind="tool"][data-tool="${modelMCPTool}"]`,
    )
    await expect(toolBlocks).toHaveCount(1)

    const runRequests = fake
      .requests()
      .slice(firstRequest)
      .filter((request) => request.body.includes('"messages"'))
    const runBodies = runRequests.map((request) => request.body)
    expect(runBodies).toHaveLength(3)
    expect(runBodies[0]).not.toContain(RESULT_MARKER)
    expect(runBodies[1]).not.toContain(RESULT_MARKER)
    expect(runBodies[2]).toContain(RESULT_MARKER)
    expect(runBodies[2]).toContain('[REDACTED]')
    expect(runBodies.join('\n')).not.toContain(secretValue)
    expect(runRequests.map((request) => request.authorization).join('\n')).not.toContain(
      secretValue,
    )
    expect(readFileSync(backend.logFile, 'utf8')).not.toContain(secretValue)
  })
})

/**
 * e2e: a person's bare input has an explicit meaning (nocx-p5rjv).
 *
 * Each journey starts from the ordinary assistant composer and watches the
 * real backend use the meaning stated by the system prompt: a bare URL is
 * fetched, a bare paragraph becomes a note, and an unclear command gets one
 * question without a tool-result follow-up.
 */
import { test as base, expect, type Page } from '@playwright/test'
import { createServer, type Server } from 'node:http'
import { mkdtempSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'

import {
  VaultBackend,
  bindEndpoint,
  createAiEndpoint,
  setDefaultModel,
  settingsReady,
  showSidebarView,
} from './harness'
import { readStand } from './stand'
import { FakeOpenAI, type FakeRequest } from './fake-openai'

const test = base
const nonce = Date.now().toString(36)
const INPUT = '.pane.active .nocx-editor-input'
const TITLE = '.nocx-tab-title'
const ENDPOINTS_NAV = '.ui-grouped-nav__item[data-item="endpoints"]'
const ROLES_NAV = '.ui-grouped-nav__item[data-item="roles"]'
const POLICY_NAV = '.ui-grouped-nav__item[data-item="policy"]'
const MUTATE_REVERSIBLE_ROW = '.st-policy__row[data-effect="mutate-reversible"]'
const APPROVAL_TITLE = 'This action needs your approval'
const PAGE_MARKER = `ASSISTANT_INTAKE_PAGE_${nonce}`
const NOTE_TEXT = `Remember this paragraph ${nonce}.`
const UNCLEAR_INPUT = 'Do the thing.'

let backend: VaultBackend
let fake: FakeOpenAI
let pageServer: Server
let pageURL: string
let endpoint: { port: number; token: string }

test.describe.configure({ mode: 'serial' })

test.beforeAll(async () => {
  fake = new FakeOpenAI()
  await fake.start()
  pageServer = createServer((req, res) => {
    if (req.url !== '/page') {
      res.writeHead(404).end()
      return
    }
    res.writeHead(200, { 'Content-Type': 'text/plain; charset=utf-8' })
    res.end(`${PAGE_MARKER}: the fixture says fetch works`)
  })
  await new Promise<void>((resolve) => pageServer.listen(0, '127.0.0.1', resolve))
  const address = pageServer.address()
  if (!address || typeof address === 'string') throw new Error('page fixture did not bind')
  pageURL = `http://127.0.0.1:${address.port}/page`

  const root = mkdtempSync(join(tmpdir(), 'nocx-p5rjv-e2e-'))
  backend = new VaultBackend(readStand().server, { root })
  endpoint = await backend.start()
})

test.afterAll(async () => {
  backend?.stop()
  await fake?.stop()
  await new Promise<void>((resolve) => pageServer?.close(() => resolve()))
})

async function openApp(page: Page): Promise<void> {
  await bindEndpoint(page, endpoint)
  await page.goto('/')
  await expect(page.locator(TITLE).first()).not.toHaveText('', { timeout: 15_000 })
}

async function configureAssistant(
  page: Page,
  endpointName: string,
  allowMutations = false,
): Promise<void> {
  await page.keyboard.press('Meta+,')
  await settingsReady(page)
  await page.locator(ENDPOINTS_NAV).click()
  await expect(page.locator('.ep-root')).toBeVisible({ timeout: 10_000 })
  await createAiEndpoint(page, {
    name: endpointName,
    baseUrl: fake.baseUrl(),
    models: ['e2e-model'],
    key: `e2e-key-${nonce}`,
    vaultPassphrase: `vault-pass-${nonce}`,
  })

  await page.locator(ROLES_NAV).click()
  await setDefaultModel(page, endpointName, 'e2e-model')

  if (allowMutations) {
    await page.locator(POLICY_NAV).click()
    const row = page.locator(MUTATE_REVERSIBLE_ROW)
    await expect(row).toBeVisible({ timeout: 10_000 })
    await row.locator('select').first().selectOption({ label: 'Allowed' })
    await expect(row.locator('.st-policy__state')).toContainText('Allowed', {
      timeout: 10_000,
    })
  }

  await page.locator(TITLE).first().click()
  await expect(page.locator(INPUT)).toBeVisible({ timeout: 10_000 })
}

async function askFromPrompt(page: Page, inputText: string): Promise<void> {
  const input = page.locator(INPUT)
  await input.click()
  const indicator = page.locator('.pane.active .ui-mode-indicator:visible')
  if ((await indicator.getAttribute('data-target')) !== 'agent') {
    await page.keyboard.press('ControlOrMeta+Enter')
    await expect(indicator).toHaveAttribute('data-target', 'agent', { timeout: 10_000 })
  }
  await input.fill(inputText)
  await page.keyboard.press('Enter')
}

function chatRequests(): FakeRequest[] {
  return fake.requests().filter((request) => request.path.endsWith('/chat/completions'))
}

async function waitForChatRequests(count: number): Promise<FakeRequest[]> {
  await expect.poll(() => chatRequests().length, { timeout: 30_000 }).toBe(count)
  return chatRequests()
}

async function expectCompletedAnswer(page: Page, inputText: string, answer: string): Promise<void> {
  const turn = page.locator('.pane.active .cmd-block').filter({ hasText: inputText }).last()
  await expect(turn.locator(':scope > .cmd-header .cmd-header-exit')).toHaveText('completed', {
    timeout: 30_000,
  })
  await expect(turn.locator('[data-answer-body]')).toContainText(answer, { timeout: 15_000 })
}

test.describe('bare input follows the system prompt (nocx-p5rjv)', () => {
  test.use({ viewport: { width: 1280, height: 900 } })
  test.setTimeout(120_000)

  test('a bare link is fetched and summarised', async ({ page }) => {
    await openApp(page)
    await configureAssistant(page, `E2E Intake Link ${nonce}`)

    const base = chatRequests().length
    fake.setScript({
      chunks: [],
      toolCalls: [{ name: 'fetch.url', arguments: { url: pageURL }, id: 'fetch-page' }],
    })
    fake.setScript({
      chunks: (body) =>
        body.includes(PAGE_MARKER)
          ? ['The page says ', PAGE_MARKER]
          : ['The fetch result did not reach the model.'],
    })

    await askFromPrompt(page, pageURL)
    const approval = page.getByRole('dialog', { name: APPROVAL_TITLE })
    await expect(approval).toBeVisible({ timeout: 20_000 })
    await expect(approval).toContainText(pageURL)
    await approval.getByRole('button', { name: /Allow once/ }).click()
    await expect(approval).not.toBeVisible({ timeout: 15_000 })

    const requests = await waitForChatRequests(base + 2)
    expect(requests[base].body).toContain(pageURL)
    expect(requests[base + 1].body).toContain(PAGE_MARKER)
    await expectCompletedAnswer(page, pageURL, PAGE_MARKER)
  })

  test('a bare paragraph becomes a note the person can find', async ({ page }) => {
    await openApp(page)
    await configureAssistant(page, `E2E Intake Note ${nonce}`, true)

    const base = chatRequests().length
    fake.setScript({
      chunks: [],
      toolCalls: [{ name: 'notes.create', arguments: { body: NOTE_TEXT }, id: 'create-note' }],
    })
    fake.setScript({
      chunks: (body) =>
        body.includes(NOTE_TEXT)
          ? ['I remembered it: ', NOTE_TEXT]
          : ['The note result did not reach the model.'],
    })

    await askFromPrompt(page, NOTE_TEXT)
    const requests = await waitForChatRequests(base + 2)
    expect(requests[base].body).toContain(NOTE_TEXT)
    expect(requests[base + 1].body).toContain(NOTE_TEXT)
    await expectCompletedAnswer(page, NOTE_TEXT, NOTE_TEXT)

    await showSidebarView(page, 'notes')
    await expect(page.locator('.notes-panel')).toBeVisible({ timeout: 10_000 })
    await page.getByRole('searchbox', { name: 'Search notes' }).fill(NOTE_TEXT)
    const row = page.locator('.notes-panel .ui-record-row__title').filter({ hasText: NOTE_TEXT })
    await expect(row).toBeVisible({ timeout: 15_000 })
    await row.click()
    await expect(page.locator('.note-tab__editor .cm-content')).toContainText(NOTE_TEXT, {
      timeout: 15_000,
    })
  })

  test('unclear input produces one question and no tool call', async ({ page }) => {
    await openApp(page)
    await configureAssistant(page, `E2E Intake Unclear ${nonce}`)

    const base = chatRequests().length
    const question = 'What would you like me to do?'
    fake.setScript({ chunks: [question] })

    await askFromPrompt(page, UNCLEAR_INPUT)
    const requests = await waitForChatRequests(base + 1)
    await expectCompletedAnswer(page, UNCLEAR_INPUT, question)

    expect(requests).toHaveLength(base + 1)
    expect(requests[base].state).toBe('done')
    expect(requests[base].body).toContain(UNCLEAR_INPUT)
    expect(requests[base].body).toContain(
      'When the intent is not plain, ask one question and stop.',
    )
    const payload = JSON.parse(requests[base].body) as {
      messages?: { role?: string; content?: unknown }[]
    }
    expect(payload.messages?.some((message) => message.role === 'tool')).toBe(false)
  })
})

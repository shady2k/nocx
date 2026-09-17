/**
 * e2e: a person asks the assistant to fetch a URL and gets an answer from the
 * page after one approval (nocx-iqywv).
 *
 * This is written from the bead's acceptance criteria, not from the tool
 * executor: the person configures an endpoint, asks in the composer, sees that
 * the call reaches the network from this machine, allows it once, and reads an
 * answer derived from a local page fixture. The fake model only says the page
 * marker when the real fetch result reached its second request.
 */
import { expect, type Page } from '@playwright/test'
import { createServer, type Server } from 'node:http'
import { mkdtempSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import {
  standalone as base,
  VaultBackend,
  bindEndpoint,
  createAiEndpoint,
  setDefaultModel,
  settingsReady,
} from './harness'
import { readStand } from './stand'
import { FakeOpenAI } from './fake-openai'

const serverBin = () => readStand().server
const TITLE = '.nocx-tab-title'
const INPUT = '.pane.active .nocx-editor-input'
const SETTINGS_AI_NAV = '.ui-grouped-nav__item[data-item="endpoints"]'
const SETTINGS_ROLES_NAV = '.ui-grouped-nav__item[data-item="roles"]'
const APPROVAL_TITLE = 'This action needs your approval'
const PAGE_MARKER = 'FETCH_URL_E2E_PAGE_MARKER'
const nonce = Date.now().toString(36)

const test = base
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
    res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' })
    res.end(`<html><body>${PAGE_MARKER}: the fixture says fetch works</body></html>`)
  })
  await new Promise<void>((resolve) => pageServer.listen(0, '127.0.0.1', resolve))
  const address = pageServer.address()
  if (!address || typeof address === 'string') throw new Error('page fixture did not bind')
  pageURL = `http://127.0.0.1:${address.port}/page`

  const root = mkdtempSync(join(tmpdir(), 'nocx-iqywv-e2e-'))
  backend = new VaultBackend(serverBin(), { root })
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

test.describe('fetch.url reaches the page after one approval (nocx-iqywv)', () => {
  test.use({ viewport: { width: 1280, height: 900 } })

  test('one approval produces an answer from a local page', async ({ page }) => {
    await openApp(page)
    await page.keyboard.press('Meta+,')
    await settingsReady(page)
    await page.locator(SETTINGS_AI_NAV).click()
    await expect(page.locator('.ep-root')).toBeVisible({ timeout: 10_000 })
    const endpointName = `E2E Fetch ${nonce}`
    await createAiEndpoint(page, {
      name: endpointName,
      baseUrl: fake.baseUrl(),
      models: ['e2e-model'],
      key: `e2e-key-${nonce}`,
      vaultPassphrase: `vault-pass-${nonce}`,
    })
    await page.locator(SETTINGS_ROLES_NAV).click()
    await setDefaultModel(page, endpointName, 'e2e-model')
    await page.locator(TITLE).first().click()
    await expect(page.locator(INPUT)).toBeVisible({ timeout: 10_000 })

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
    const before = fake.requests().length
    await askFromPrompt(page, 'Fetch this page and tell me what it says.')

    const prompt = page.getByRole('dialog', { name: APPROVAL_TITLE })
    await expect(prompt).toBeVisible({ timeout: 20_000 })
    await expect(prompt).toContainText(pageURL)
    await expect(prompt).toContainText('reaches the network from this machine')
    await prompt.getByRole('button', { name: /Allow once/ }).click()
    await expect(prompt).not.toBeVisible({ timeout: 15_000 })

    const requests = await fake.waitForRequests(before + 2)
    expect(requests[before + 1].body).toContain(PAGE_MARKER)
    await expect(page.locator('.cmd-output[data-answer-body]').last()).toContainText(PAGE_MARKER, {
      timeout: 20_000,
    })
  })
})

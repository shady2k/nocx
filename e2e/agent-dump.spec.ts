/**
 * e2e: a finished assistant turn exposes the provider's raw request and
 * response dump (nocx-0mvpy.4).
 *
 * This watches the feature through the real Settings surface, WebSocket
 * transport, ledger capture, and the finished turn's overflow menu. The fake
 * model is owned by this spec process so the backend must reach the same
 * request that the browser caused. The request assertion proves the captured
 * body contains the model-facing messages; the response assertion proves the
 * provider bytes, not a reconstructed answer, reached the panel.
 *
 * The API key is deliberately real-looking and is asserted absent from the
 * whole panel. It is sent as Authorization, never as a request-body field, and
 * the capture owner records bodies only.
 */
import { test as base, expect, type Page } from '@playwright/test'
import { mkdtempSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import {
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
const test = base
const INPUT = '.pane.active .nocx-editor-input'
const SETTINGS_AI_NAV = '.ui-grouped-nav__item[data-item="endpoints"]'
const SETTINGS_ROLES_NAV = '.ui-grouped-nav__item[data-item="roles"]'
const nonce = Date.now().toString(36)
const ENDPOINT_NAME = `E2E Dump ${nonce}`
const API_KEY = `sk-dump-${nonce}-secret`

let backend: VaultBackend
let fake: FakeOpenAI
let endpoint: { port: number; token: string }

async function openApp(page: Page): Promise<void> {
  await bindEndpoint(page, endpoint)
  await page.goto('/')
  await expect(page.locator(TITLE).first()).not.toHaveText('', { timeout: 15_000 })
}

async function configureAssistant(page: Page): Promise<void> {
  await page.keyboard.press('Meta+,')
  await settingsReady(page)
  await page.locator(SETTINGS_AI_NAV).click()
  await expect(page.locator('.ep-root')).toBeVisible({ timeout: 10_000 })
  await createAiEndpoint(page, {
    name: ENDPOINT_NAME,
    baseUrl: fake.baseUrl(),
    models: ['e2e-model'],
    key: API_KEY,
    vaultPassphrase: `vault-dump-${nonce}`,
  })
  await page.locator(SETTINGS_ROLES_NAV).click()
  await setDefaultModel(page, ENDPOINT_NAME, 'e2e-model')
  await page.locator(TITLE).first().click()
  await expect(page.locator(INPUT)).toBeVisible({ timeout: 10_000 })
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

base.describe('a finished turn can show its raw model dump (nocx-0mvpy.4)', () => {
  base.describe.configure({ mode: 'serial' })
  base.beforeAll(async () => {
    fake = new FakeOpenAI()
    await fake.start()
    const root = mkdtempSync(join(tmpdir(), 'nocx-0mvpy-4-e2e-'))
    backend = new VaultBackend(serverBin(), { root })
    endpoint = await backend.start()
  })
  base.beforeEach(({ browserName }) => {
    base.skip(
      browserName !== 'chromium',
      'clipboard permissions are Chromium-only; check WebKit by hand',
    )
  })
  base.afterAll(async () => {
    backend?.stop()
    await fake?.stop()
  })

  base(
    'reads both raw directions, omits the key, and copies from the panel',
    async ({ page, context }) => {
      test.setTimeout(120_000)
      await context.grantPermissions(['clipboard-read', 'clipboard-write'])
      await openApp(page)
      await configureAssistant(page)

      const question = `Show me the raw request ${nonce}`
      const response = `provider response ${nonce}`
      fake.setScript({ chunks: [response] })
      await askFromPrompt(page, question)

      const block = page.locator('.pane.active .cmd-block').filter({ hasText: question })
      await expect(block.locator(':scope > .cmd-header .cmd-header-exit')).toHaveText('completed', {
        timeout: 30_000,
      })
      await block.locator('.cmd-overflow-btn').click()
      await page.getByRole('button', { name: 'Show dump' }).click()

      const dialog = page.getByRole('dialog', { name: 'Model dump' })
      await expect(dialog).toBeVisible()
      const request = dialog.locator('pre[aria-label="Request drive 1"]')
      const received = dialog.locator('pre[aria-label="Response drive 1"]')
      await expect(request).toContainText(question)
      await expect(request).toContainText('"messages"')
      await expect(received).toContainText(response)
      await expect(dialog).not.toContainText(API_KEY)

      const requestCopy = request.locator('xpath=..').getByRole('button', { name: 'Copy code' })
      await requestCopy.click()
      await expect
        .poll(() => page.evaluate(() => navigator.clipboard.readText()))
        .toContain(question)
      expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(
        await request.textContent(),
      )
    },
  )
})

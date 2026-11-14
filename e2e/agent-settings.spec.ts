/**
 * Settings reads the real pi connector in a disposable backend home.
 * models.json points at the suite's FakeOpenAI rather than a public provider.
 * Removing the provider and refreshing must replace Ready and its model list
 * with Not configured, while agents without a connector keep static detection.
 */
import { expect, type Page } from '@playwright/test'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import {
  appReadyForInput,
  bindEndpoint,
  settingsReady,
  standalone as base,
  VaultBackend,
} from './harness'
import { readStand } from './stand'
import { FakeOpenAI } from './fake-openai'

const test = base

const TITLE = '.nocx-tab-title'
/** The rail row, addressed by the page's own id — the settings rail's
 *  `data-item` is the registry's id and never a translated title, which is
 *  what makes this locator survive the language. */
const NAV = '.ui-grouped-nav__item[data-item="agent-installed"] button'
const PI_ROW = '[data-agent="pi"]'
const CLAUDE_ROW = '[data-agent="claude"]'

let backend: VaultBackend
let endpoint: { port: number; token: string }
let modelFile = ''
const fake = new FakeOpenAI()

test.beforeAll(async () => {
  const root = mkdtempSync(join(tmpdir(), 'nocx-a62cl-14-4-e2e-'))
  const baseUrl = await fake.start()
  // VaultBackend's boundary owns <root>/home. Configure pi before backend
  // startup, so even a first catalogue request sees only this provider.
  const agentDir = join(root, 'home', '.pi', 'agent')
  mkdirSync(agentDir, { recursive: true })
  modelFile = join(agentDir, 'models.json')
  writeFileSync(
    modelFile,
    JSON.stringify({
      providers: {
        'nocx-e2e': {
          baseUrl,
          api: 'openai-completions',
          apiKey: 'disposable-e2e-key',
          models: ['e2e-model', 'e2e-alternate'].map((id) => ({
            id,
            name: id === 'e2e-model' ? 'Disposable E2E model' : 'Alternate E2E model',
            reasoning: false,
            contextWindow: 128000,
            maxTokens: 4096,
            cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
          })),
        },
      },
    }),
    { mode: 0o600 },
  )
  backend = new VaultBackend(readStand().server, { root })
  endpoint = await backend.start({ PI_CODING_AGENT_DIR: agentDir })
})

test.afterAll(async () => {
  backend?.stop()
  await fake.stop()
})

async function openAgentSection(page: Page): Promise<void> {
  await bindEndpoint(page, endpoint)
  await page.goto('/')
  await expect(page.locator(TITLE).first()).not.toHaveText('', { timeout: 15_000 })
  // Up before the chord is pressed: a ⌘, that lands before the binding is
  // wired opens nothing, and the wait after it burns the whole budget on a
  // page that is never coming (snippets.spec.ts records the same).
  await appReadyForInput(page)
  await page.keyboard.press('ControlOrMeta+,')
  await settingsReady(page)
  await page.locator(NAV).click()
}

test.describe('Settings lists connector readiness and models (nocx-a62cl.14.4)', () => {
  test.use({ viewport: { width: 1280, height: 900 } })
  test.setTimeout(120_000)

  test('chooses an answering model from pi and the answer names that model (nocx-a62cl.5.3)', async ({
    page,
  }) => {
    await openAgentSection(page)
    const answering = page.locator(PI_ROW).getByRole('combobox', { name: 'Answering' })
    await expect(answering).toBeEnabled({ timeout: 20_000 })
    await answering.selectOption('nocx-e2e/e2e-alternate')
    await expect(answering).toHaveValue('nocx-e2e/e2e-alternate')
    await expect(answering).toBeEnabled()
    await page.locator(TITLE).first().click()
    const input = page.locator('.pane.active .nocx-editor-input')
    await expect(input).toBeVisible()
    await input.click()
    await page.keyboard.press('ControlOrMeta+Enter')
    await expect(page.locator('.pane.active .ui-mode-indicator:visible')).toHaveAttribute(
      'data-target',
      'agent',
    )
    fake.setScript({ chunks: ['The selected model answered.'] })
    await input.fill('Which selected model answers this question?')
    await page.keyboard.press('Enter')
    const answer = page
      .locator('.cmd-block')
      .filter({ hasText: 'Which selected model answers this question?' })
      .first()
    await expect(answer).toHaveAttribute('data-outcome', 'success', { timeout: 30_000 })
    await expect(answer).toContainText('The selected model answered.')
    await expect(answer).toHaveAttribute('data-answered-by', 'nocx-e2e/e2e-alternate')
    await expect(answer.locator('.cmd-answer-provenance')).toContainText('nocx-e2e/e2e-alternate')
  })

  test('shows pi ready with its configured model, then not configured without a provider', async ({
    page,
  }) => {
    // Hold only the real refresh result at the wire so the in-flight state
    // can be checked without racing a fast probe or adding a timed delay.
    let refreshId: string | number | undefined
    let releaseRefresh!: () => void
    let resultHeld!: () => void
    const held = new Promise<void>((resolve) => {
      resultHeld = resolve
    })
    await page.routeWebSocket(
      (url) => url.port === String(endpoint.port),
      (socket) => {
        const server = socket.connectToServer()
        socket.onMessage((message) => {
          if (typeof message === 'string') {
            const request = JSON.parse(message) as {
              id?: string | number
              method?: string
              params?: { refresh?: boolean }
            }
            if (request.method === 'agentRecords.catalogue' && request.params?.refresh === true) {
              refreshId = request.id
            }
          }
          server.send(message)
        })
        server.onMessage((message) => {
          if (typeof message === 'string') {
            const response = JSON.parse(message) as { id?: string | number }
            if (refreshId !== undefined && response.id === refreshId) {
              releaseRefresh = () => socket.send(message)
              resultHeld()
              return
            }
          }
          socket.send(message)
        })
      },
    )
    await openAgentSection(page)

    const pi = page.locator(PI_ROW)
    await expect(pi).toContainText('Ready', { timeout: 20_000 })
    await expect(pi).toContainText('Disposable E2E model')
    await expect(pi).toContainText('nocx-e2e/e2e-model')
    // The other half of the same answer, and the one a person reads as "nocx
    // cannot use this agent at all": no claude on the login shell's PATH.
    await expect(page.locator(CLAUDE_ROW)).toContainText('Not installed')

    rmSync(modelFile)
    const refresh = page.getByRole('button', { name: 'Refresh' })
    await refresh.click()
    await held
    const list = page.getByRole('list', { name: 'Agents on this computer' })
    await expect(list).toHaveAttribute('aria-busy', 'true')
    await expect(
      page
        .getByRole('status')
        .filter({ hasText: 'Refreshing… Shown agents are from the previous result.' }),
    ).toBeVisible()
    await expect(pi).toContainText('Disposable E2E model')
    await expect(pi).toContainText('Ready')
    await expect(refresh).toBeDisabled()
    releaseRefresh()

    await expect(list).toHaveAttribute('aria-busy', 'false')
    await expect(
      page.getByRole('status').filter({ hasText: 'Agent check complete.' }),
    ).toBeVisible()
    await expect(pi).toContainText('Not configured', { timeout: 20_000 })
    await expect(pi).not.toContainText('Disposable E2E model')
    await expect(pi).not.toContainText('Models available to this agent:')
  })
})

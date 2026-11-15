/** Real pi readiness, through the composer's chip and Settings (nocx-a62cl.5.4). */
import { expect, type Page } from '@playwright/test'
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  symlinkSync,
  writeFileSync,
} from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import {
  standalone as test,
  appReadyForInput,
  bindEndpoint,
  settingsReady,
  VaultBackend,
  type BackendEndpoint,
} from './harness'
import { readStand } from './stand'
import { FakeOpenAI } from './fake-openai'
import { configurePiHome } from './pi-home'

const TITLE = '.nocx-tab-title'
const INPUT = '.pane.active .nocx-editor-input'
const CHIPS = '.pane.active [data-control^="model"]:visible'
const AGENT_NAV = '.ui-grouped-nav__item[data-item="agent-installed"]'

async function openApp(page: Page, endpoint: BackendEndpoint): Promise<void> {
  await bindEndpoint(page, endpoint)
  await page.goto('/')
  await expect(page.locator(TITLE).first()).not.toHaveText('', { timeout: 15_000 })
  await appReadyForInput(page)
  await page.locator(INPUT).click()
  await page.keyboard.press('ControlOrMeta+Enter')
  await expect(page.locator('.pane.active .ui-mode-indicator:visible')).toHaveAttribute(
    'data-target',
    'agent',
  )
}

async function agentSection(page: Page): Promise<void> {
  await settingsReady(page)
  await expect(page.locator(AGENT_NAV)).toHaveAttribute('data-selected', 'true')
  await expect(page.getByRole('heading', { name: 'Agents on this computer' })).toBeVisible()
  await expect(page.getByText('Agent check complete.', { exact: true })).toBeVisible()
}

async function backToTerminal(page: Page): Promise<void> {
  await page.locator(TITLE).first().click()
  await expect(page.locator(INPUT)).toBeVisible()
}

async function backToTerminalAfterSummon(page: Page): Promise<void> {
  await page.locator(TITLE).first().click()
  await expect(page.locator(INPUT)).toBeHidden()
  await page.locator('.pane.active .terminal-live-container').click()
  await page.keyboard.press('Control+C')
  await expect(page.locator('.pane.active .cmd-block-running')).toHaveCount(0)
}

function emptyPiHome(root: string): NodeJS.ProcessEnv {
  const agentDir = join(root, 'home', '.pi', 'agent')
  mkdirSync(agentDir, { recursive: true })
  expect(existsSync(join(agentDir, 'models.json'))).toBe(false)
  return { PI_CODING_AGENT_DIR: agentDir }
}

/** Restrict the LOGIN shell, not the backend's inherited PATH. Keep system
 * tools available through a private bin directory, but omit pi even when npm
 * installed it in /usr/bin (rather than /usr/local/bin). */
function withoutPiOnLoginPath(root: string): void {
  const home = join(root, 'home')
  const bin = join(root, 'bin-without-pi')
  mkdirSync(bin)
  for (const name of readdirSync('/usr/bin')) {
    if (name !== 'pi') symlinkSync(join('/usr/bin', name), join(bin, name))
  }
  for (const rc of ['.zshrc', '.zprofile', '.bashrc', '.bash_profile', '.zlogin']) {
    writeFileSync(join(home, rc), `export PATH='${bin}'\n`)
  }
}

test.describe('the assistant reports its agent readiness', () => {
  test.use({ viewport: { width: 1280, height: 900 } })

  for (const state of ['not installed', 'not configured'] as const) {
    test(`${state}: the sentence and chip lead to the agent section`, async ({ page }) => {
      const root = mkdtempSync(join(tmpdir(), 'nocx-readiness-'))
      const env = emptyPiHome(root)
      if (state === 'not installed') withoutPiOnLoginPath(root)
      const backend = new VaultBackend(readStand().server, { root })
      try {
        await openApp(page, await backend.start(env))
        const sentence =
          state === 'not installed'
            ? 'Install pi to use this agent.'
            : 'Configure a provider in pi, then check again.'
        const chips = page.locator(CHIPS)
        await expect(chips).toHaveText([sentence])
        await chips.click()
        await agentSection(page)
        const pi = page.locator('[data-agent="pi"]')
        await expect(pi).toContainText(
          state === 'not installed' ? 'Not installed' : 'Not configured',
        )
        await expect(pi).toContainText(sentence)

        // Asking from the composer refuses BEFORE an answer block is created.
        await backToTerminal(page)
        const question = 'Can you help me configure this pane?'
        await page.locator(INPUT).fill(question)
        await page.keyboard.press('Enter')
        await agentSection(page)
        await expect(page.locator('.pane.active [data-agent="pi"]')).toContainText(sentence)
        await backToTerminal(page)
        await expect(page.locator('.cmd-block').filter({ hasText: question })).toHaveCount(0)

        // The same refusal applies when the ordinary composer is gone and a
        // person summons help over a running program.
        await page.locator(INPUT).fill('')
        await page.locator(INPUT).click()
        await page.keyboard.press('ControlOrMeta+Enter')
        await expect(page.locator('.pane.active .ui-mode-indicator:visible')).toHaveAttribute(
          'data-target',
          'shell',
        )
        await page.locator(INPUT).fill('cat')
        await page.keyboard.press('Enter')
        await expect(page.locator('.pane.active .cmd-block-running')).toHaveCount(1)
        await expect(page.locator(INPUT)).toBeHidden()
        await page.locator('.pane.active .terminal-live-container').click()
        await page.keyboard.press('ControlOrMeta+Enter')
        await agentSection(page)
        await expect(page.locator('.pane.active [data-agent="pi"]')).toContainText(sentence)
        await backToTerminalAfterSummon(page)
      } finally {
        backend.stop()
      }
    })
  }

  test('ready: the agent default answers and the chip opens the agent section', async ({
    page,
  }) => {
    const fake = new FakeOpenAI()
    await fake.start()
    const root = mkdtempSync(join(tmpdir(), 'nocx-readiness-ready-'))
    const backend = new VaultBackend(readStand().server, { root })
    try {
      await openApp(page, await backend.start(configurePiHome(root, fake.baseUrl())))
      const chips = page.locator(CHIPS)
      await expect(chips).toHaveText(['pi', 'Agent default'])
      await chips.last().click()
      await agentSection(page)
      const pi = page.locator('[data-agent="pi"]')
      await expect(pi).toContainText('Ready')
      await expect(pi).toContainText('pi is ready to answer.')
      await expect(pi).toContainText('Disposable E2E model (nocx-e2e/e2e-model)')
      await backToTerminal(page)

      const question = 'Which model is answering?'
      const answer = 'The configured pi model answered.'
      fake.setScript({ chunks: ['The configured pi ', 'model answered.'] })
      const before = fake.requests().length
      await page.locator(INPUT).fill(question)
      await page.keyboard.press('Enter')
      const block = page.locator('.cmd-block').filter({ hasText: question }).first()
      await expect(block).toContainText(answer, { timeout: 30_000 })
      await expect(block).toHaveAttribute('data-outcome', 'success')
      const requests = await fake.waitForRequests(before + 1)
      expect(JSON.parse(requests[before].body).model).toBe('e2e-model')
      expect(requests[before].authorization).toBe('Bearer disposable-e2e-key')
    } finally {
      backend.stop()
      await fake.stop()
    }
  })
})

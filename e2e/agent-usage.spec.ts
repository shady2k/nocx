/** Real pi reports provider usage; the run keeps it through a backend restart. */
import { expect, type Locator, type Page } from '@playwright/test'
import { mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import {
  standalone as test,
  appReadyForInput,
  bindEndpoint,
  ensureAssistantVault,
  VaultBackend,
} from './harness'
import { FakeOpenAI } from './fake-openai'
import { configurePiHome } from './pi-home'
import { readStand } from './stand'

const INPUT = '.pane.active .nocx-editor-input'
const USAGE = '.cmd-agent-usage'

function turn(page: Page, question: string) {
  return page
    .locator('.pane.active .cmd-block[data-block-kind="ask"]')
    .filter({ hasText: question })
}

async function ask(page: Page, question: string): Promise<void> {
  const input = page.locator(INPUT)
  await input.click()
  const indicator = page.locator('.pane.active .ui-mode-indicator:visible')
  if ((await indicator.getAttribute('data-target')) !== 'agent') {
    await page.keyboard.press('ControlOrMeta+Enter')
    await expect(indicator).toHaveAttribute('data-target', 'agent')
  }
  await input.fill(question)
  await page.keyboard.press('Enter')
  await expect(turn(page, question)).toHaveAttribute('data-outcome', 'success', {
    timeout: 30_000,
  })
}

const cases = [
  {
    name: 'pi token counts remain visible after a backend restart',
    answer: 'The counted answer.',
    usage: {
      prompt_tokens: 144,
      completion_tokens: 27,
      total_tokens: 171,
      prompt_tokens_details: { cached_tokens: 40 },
    },
    expected: '104 input · 27 output · 40 from cache',
  },
  {
    name: 'an answer whose provider stated no usage shows none, live and after restart',
    answer: 'The provider omitted usage.',
    usage: undefined,
    expected: null,
  },
]

async function expectUsage(block: Locator, expected: string | null): Promise<void> {
  if (expected === null) {
    await expect(block.locator(USAGE)).toHaveCount(0)
    return
  }
  await expect(block.locator(USAGE)).toHaveText(expected)
}

for (const scenario of cases) {
  test(scenario.name, async ({ page }) => {
    const fake = new FakeOpenAI()
    await fake.start()
    const root = mkdtempSync(join(tmpdir(), 'nocx-agent-usage-'))
    const backend = new VaultBackend(readStand().server, { root })
    const password = 'usage-e2e-vault-password'
    let endpoint = await backend.start(configurePiHome(root, fake.baseUrl()))
    const question = 'How many tokens did this answer use?'
    try {
      await bindEndpoint(page, endpoint)
      await page.goto('/')
      await appReadyForInput(page)
      await ensureAssistantVault(page, endpoint, password)
      await page.locator('.nocx-tab-title').first().click()

      // OpenAI prompt tokens include cache reads. Pi separates the uncached input.
      // Pi 1.5.2 fills zeros when the provider omits usage; the pi connector reads
      // all-zero usage as not stated, so the block shows no usage at all.
      fake.setScript({ chunks: [scenario.answer], usage: scenario.usage })
      await ask(page, question)
      await expect(turn(page, question)).toContainText(scenario.answer)
      await expectUsage(turn(page, question), scenario.expected)

      endpoint = await backend.restart()
      await bindEndpoint(page, endpoint)
      await page.reload()
      await appReadyForInput(page)
      await ensureAssistantVault(page, endpoint, password)
      await page.locator('.nocx-tab-title').first().click()
      await expect(turn(page, question)).toHaveAttribute('data-restored', 'true')
      await expect(turn(page, question)).toContainText(scenario.answer)
      await expectUsage(turn(page, question), scenario.expected)
    } finally {
      await backend.stop()
      await fake.stop()
    }
  })
}

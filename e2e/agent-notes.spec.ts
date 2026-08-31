/**
 * e2e: the assistant creates a note through the shipped backend, and the
 * person finds that note in the Notes panel (nocx-klqjo).
 *
 * This is deliberately a separate journey from notes.spec.ts. That file
 * proves the human note editor; this one proves the assistant's notes.create
 * tool reaches the real composition root and the resulting record reaches the
 * user-facing panel. The fake's follow-up answer is derived from the tool
 * result, so a missing operation cannot be hidden by a fixed model answer.
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
  showSidebarView,
} from './harness'
import { readStand } from './stand'
import { FakeOpenAI } from './fake-openai'

const test = base
const nonce = Date.now().toString(36)
const ENDPOINT_NAME = `E2E Notes ${nonce}`
const NOTE_TITLE = 'Assistant-created note'
const NOTE_BODY = `${NOTE_TITLE}\nassistant-note-${nonce}`
const QUESTION = `Create a note for me, ${nonce}.`
const INPUT = '.pane.active .nocx-editor-input'
const TITLE = '.nocx-tab-title'
const MUTATE_REVERSIBLE_ROW = '.st-policy__row[data-effect="mutate-reversible"]'

let backend: VaultBackend
let fake: FakeOpenAI
let endpoint: { port: number; token: string }

test.beforeAll(async () => {
  fake = new FakeOpenAI()
  await fake.start()
  const root = mkdtempSync(join(tmpdir(), 'nocx-klqjo-e2e-'))
  backend = new VaultBackend(readStand().server, { root })
  endpoint = await backend.start()
})

test.afterAll(async () => {
  backend?.stop()
  await fake?.stop()
})

async function openApp(page: Page): Promise<void> {
  await bindEndpoint(page, endpoint)
  await page.goto('/')
  await expect(page.locator(TITLE).first()).not.toHaveText('', { timeout: 15_000 })
}

async function configureAssistant(page: Page): Promise<void> {
  await page.keyboard.press('Meta+,')
  await settingsReady(page)
  await page.locator('.ui-grouped-nav__item[data-item="endpoints"]').click()
  await expect(page.locator('.ep-root')).toBeVisible({ timeout: 10_000 })
  await createAiEndpoint(page, {
    name: ENDPOINT_NAME,
    baseUrl: fake.baseUrl(),
    models: ['e2e-model'],
    key: `e2e-key-${nonce}`,
    vaultPassphrase: `vault-pass-${nonce}`,
  })

  await page.locator('.ui-grouped-nav__item[data-item="roles"]').click()
  await setDefaultModel(page, ENDPOINT_NAME, 'e2e-model')

  await page.locator('.ui-grouped-nav__item[data-item="policy"]').click()
  const row = page.locator(MUTATE_REVERSIBLE_ROW)
  await expect(row).toBeVisible({ timeout: 10_000 })
  await row.locator('select').first().selectOption({ label: 'Allowed' })
  await expect(row.locator('.st-policy__state')).toContainText('Allowed', { timeout: 10_000 })

  await page.locator(TITLE).first().click()
  await expect(page.locator(INPUT)).toBeVisible({ timeout: 10_000 })
}

async function askFromPrompt(page: Page): Promise<void> {
  const input = page.locator(INPUT)
  await input.click()
  const indicator = page.locator('.pane.active .ui-mode-indicator:visible')
  if ((await indicator.getAttribute('data-target')) !== 'agent') {
    await page.keyboard.press('ControlOrMeta+Enter')
    await expect(indicator).toHaveAttribute('data-target', 'agent', { timeout: 10_000 })
  }
  await input.fill(QUESTION)
  await page.keyboard.press('Enter')
}

function noteAnswer(body: string): string[] {
  try {
    const parsed = JSON.parse(body) as {
      messages?: { role?: string; content?: unknown }[]
    }
    const result = (parsed.messages ?? [])
      .filter((message) => message.role === 'tool' && typeof message.content === 'string')
      .map((message) => message.content as string)
      .find((content) => content.includes(`assistant-note-${nonce}`))
    return result
      ? [`The note was created through the backend: ${NOTE_BODY}`]
      : ['The note tool result was missing.']
  } catch {
    return ['The note tool result was malformed.']
  }
}

test.describe('assistant notes operations (nocx-klqjo)', () => {
  test.use({ viewport: { width: 1280, height: 900 } })
  test.setTimeout(120_000)

  test('creates a note through the backend and shows it in Notes', async ({ page }) => {
    await openApp(page)
    await configureAssistant(page)

    const base = fake.requests().length
    fake.setScript({
      chunks: [],
      toolCalls: [
        {
          name: 'notes.create',
          id: 'call_note_create',
          arguments: { body: NOTE_BODY },
        },
      ],
    })
    fake.setScript({ chunks: noteAnswer })

    await askFromPrompt(page)
    const requests = await fake.waitForRequests(base + 2)
    const proposal = requests[base]
    const followUp = requests[base + 1]
    // The first model request declares the tool; its arguments arrive in the
    // fake response, and the follow-up request is the wire evidence that the
    // backend executed that proposal and returned the created note.
    expect(proposal.body).toContain('notes.create')
    const followUpPayload = JSON.parse(followUp.body) as {
      messages?: { role?: string; content?: unknown }[]
    }
    const toolResult = (followUpPayload.messages ?? []).find(
      (message) => message.role === 'tool' && typeof message.content === 'string',
    )?.content as string | undefined
    expect(toolResult).toContain('"status":"created"')

    const turn = page.locator('.pane.active .cmd-block').filter({ hasText: QUESTION }).first()
    await expect(turn.locator(':scope > .cmd-header .cmd-header-exit')).toHaveText('completed', {
      timeout: 30_000,
    })
    await expect(turn.locator('[data-answer-body]')).toContainText(NOTE_TITLE, {
      timeout: 15_000,
    })
    await expect(turn.locator('[data-answer-body]')).toContainText(`assistant-note-${nonce}`)

    await showSidebarView(page, 'notes')
    await expect(page.locator('.notes-panel')).toBeVisible({ timeout: 10_000 })
    await page.getByRole('searchbox', { name: 'Search notes' }).fill(`assistant-note-${nonce}`)
    const row = page.locator('.notes-panel .ui-record-row__title').filter({ hasText: NOTE_TITLE })
    await expect(row).toBeVisible({ timeout: 15_000 })
    await row.click()
    await expect(page.locator('.note-tab__editor .cm-content')).toContainText(NOTE_TITLE, {
      timeout: 15_000,
    })
    await expect(page.locator('.note-tab__editor .cm-content')).toContainText(
      `assistant-note-${nonce}`,
    )
  })
})

/**
 * e2e: a person teaches the assistant a skill in one pane and uses it in
 * another (nocx-hzd6t).
 *
 * This is the epic's happy-path proof from the design's acceptance sentence,
 * not a test of implementation details: pane A asks the assistant to remember
 * a procedure, the person approves the generated proposal, and pane B sees the
 * enabled skill in its prompt index, reads it, and answers from its content.
 *
 * The fake model is scripted in the same order as the real calls. The first
 * response proposes skills.create, the second response is the summarizing
 * model's generated JSON, and the third is the answer after the approved
 * write. The model's proposal deliberately disagrees with the generated
 * fields: the approval must show what the summarizer produced, because the
 * kernel replaces the model's own create arguments before validation and
 * approval.
 *
 * The pane-B answer is derived from the actual skills.read tool message in the
 * follow-up request. A fixed answer could pass while the prompt omitted the
 * skill, the read was refused, or the result was dropped.
 *
 * Every wait is an observable state change: the approval dialog, recorded fake
 * requests, the completed ledger block, or the new tab. This spec deliberately
 * contains no waitForTimeout.
 */
import { test as base, expect, type Page } from '@playwright/test'
import { mkdtempSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import {
  answerPermission,
  appReadyForInput,
  bindEndpoint,
  createAiEndpoint,
  setDefaultModel,
  settingsReady,
  VaultBackend,
} from './harness'
import { readStand } from './stand'
import { FakeOpenAI } from './fake-openai'

const test = base
const nonce = Date.now().toString(36)

const TITLE = '.nocx-tab-title'
const INPUT = '.pane.active .nocx-editor-input'
const NEW_TAB = '[aria-label="New tab"]'
const SETTINGS_AI_NAV = '.ui-grouped-nav__item[data-item="endpoints"]'
const SETTINGS_ROLES_NAV = '.ui-grouped-nav__item[data-item="roles"]'
const SETTINGS_POLICY_NAV = '.ui-grouped-nav__item[data-item="policy"]'
const APPROVAL_TITLE = 'This action needs your approval'

const ENDPOINT_NAME = `E2E Skills ${nonce}`
const SKILL_NAME = `release-procedure-${nonce}`
const SKILL_DESCRIPTION = `How to release this service safely ${nonce}`
const SKILL_BODY = `Run make release, then verify the deployment ${nonce}.`
const MODEL_NAME = `model-proposed-${nonce}`
const MODEL_DESCRIPTION = `model-proposed-description-${nonce}`
const MODEL_BODY = `model-proposed-body-${nonce}`
const QUESTION_A = `Remember how we release this service, ${nonce}.`
const QUESTION_B = `Use the remembered release procedure, ${nonce}.`

let backend: VaultBackend
let fake: FakeOpenAI
let endpoint: { port: number; token: string }

test.describe.configure({ mode: 'serial' })
test.setTimeout(120_000)

test.beforeAll(async () => {
  fake = new FakeOpenAI()
  await fake.start()
  const root = mkdtempSync(join(tmpdir(), `nocx-hzd6t-e2e-${nonce}-`))
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
  await appReadyForInput(page)
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
    key: `e2e-key-${nonce}`,
    vaultPassphrase: `vault-pass-${nonce}`,
  })

  await page.locator(SETTINGS_ROLES_NAV).click()
  await setDefaultModel(page, ENDPOINT_NAME, 'e2e-model')

  // skills.read is an observe operation. Allow it through the person-facing
  // policy page so pane B can demonstrate the read and answer without opening
  // a second approval prompt; skills.create still always asks by contract.
  await page.locator(SETTINGS_POLICY_NAV).click()
  await answerPermission(page, 'observe', 'Allowed')

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

function answerBlock(page: Page, question: string) {
  return page.locator('.pane.active .cmd-block').filter({ hasText: question }).first()
}

async function answerFinished(page: Page, question: string): Promise<void> {
  const turn = answerBlock(page, question)
  await expect(turn).toBeVisible({ timeout: 30_000 })
  await expect(turn.locator(':scope > .cmd-header .cmd-header-exit')).toHaveText('completed', {
    timeout: 30_000,
  })
}

function answerFromSkillRead(body: string): string[] {
  try {
    const payload = JSON.parse(body) as {
      messages?: { role?: string; content?: unknown }[]
    }
    const toolResult = (payload.messages ?? [])
      .filter((message) => message.role === 'tool' && typeof message.content === 'string')
      .map((message) => message.content as string)
      .find((content) => content.includes(SKILL_BODY))
    return toolResult
      ? [`I followed the remembered procedure: ${toolResult}`]
      : ['The skills.read result was missing.']
  } catch {
    return ['The skills.read result was malformed.']
  }
}

test.describe('a skill written in pane A is followed in pane B (nocx-hzd6t)', () => {
  test.use({ viewport: { width: 1280, height: 900 } })

  test('the approved skill is indexed, read, and used in another pane', async ({ page }) => {
    await openApp(page)
    await configureAssistant(page)

    // Pane A: the model proposes deliberately different values. The next
    // response is consumed by the summarizing model, and the final response
    // is consumed after the person approves the generated create proposal.
    fake.setScript({
      chunks: [],
      toolCalls: [
        {
          name: 'skills.create',
          id: 'call_skill_create',
          arguments: {
            name: MODEL_NAME,
            description: MODEL_DESCRIPTION,
            body: MODEL_BODY,
          },
        },
      ],
    })
    fake.setScript({
      chunks: [
        JSON.stringify({
          name: SKILL_NAME,
          description: SKILL_DESCRIPTION,
          body: SKILL_BODY,
        }),
      ],
    })
    fake.setScript({ chunks: [`Saved ${SKILL_NAME}.`] })

    const requestBaseA = fake.requests().length
    await askFromPrompt(page, QUESTION_A)

    const approval = page.getByRole('dialog', { name: APPROVAL_TITLE })
    await expect(approval).toBeVisible({ timeout: 30_000 })
    // The approval is about the generated draft, not the fields the first
    // model proposed. All three generated fields are person-visible facts.
    await expect(approval).toContainText(SKILL_NAME)
    await expect(approval).toContainText(SKILL_DESCRIPTION)
    await expect(approval).toContainText(SKILL_BODY)
    await expect(approval).not.toContainText(MODEL_NAME)
    await expect(approval).not.toContainText(MODEL_DESCRIPTION)
    await expect(approval).not.toContainText(MODEL_BODY)

    await approval.getByRole('button', { name: 'Allow once' }).click()
    await answerFinished(page, QUESTION_A)

    // The first model request really proposed the declared mutation, and the
    // ledger row is complete after the approval resumes the run.
    const requestsA = await fake.waitForRequests(requestBaseA + 3)
    expect(requestsA[requestBaseA].body).toContain('skills.create')
    expect(JSON.parse(requestsA[requestBaseA].body)).toMatchObject({ stream: true })
    expect(JSON.parse(requestsA[requestBaseA + 1].body).stream).not.toBe(true)
    expect(JSON.parse(requestsA[requestBaseA + 2].body)).toMatchObject({ stream: true })
    await expect(answerBlock(page, QUESTION_A).locator('[data-answer-body]')).toContainText(
      `Saved ${SKILL_NAME}`,
    )

    // Pane B is a newly created terminal session, not another way to address
    // pane A's active editor. The prompt index and read are both observed from
    // the model requests belonging to this pane.
    await page.locator(NEW_TAB).click()
    await expect(page.locator(INPUT)).toBeVisible({ timeout: 15_000 })
    await expect(page.locator(INPUT)).toBeFocused({ timeout: 15_000 })

    fake.setScript({
      chunks: [],
      toolCalls: [
        {
          name: 'skills.read',
          id: 'call_skill_read',
          arguments: { name: SKILL_NAME },
        },
      ],
    })
    fake.setScript({ chunks: answerFromSkillRead })

    const requestBaseB = fake.requests().length
    await askFromPrompt(page, QUESTION_B)
    const requestsB = await fake.waitForRequests(requestBaseB + 2)
    const proposal = requestsB[requestBaseB]
    const followUp = requestsB[requestBaseB + 1]

    // The enabled skill is in the prompt index with its description, and the
    // model then proposes the real read by name rather than merely repeating
    // a fixed answer.
    expect(proposal.body).toContain(SKILL_NAME)
    expect(proposal.body).toContain(SKILL_DESCRIPTION)
    expect(proposal.body).toContain('skills.read')
    const followUpPayload = JSON.parse(followUp.body) as {
      messages?: {
        role?: string
        content?: unknown
        tool_calls?: { function?: { name?: string } }[]
      }[]
    }
    const followUpMessages = followUpPayload.messages ?? []
    expect(
      followUpMessages.some((message) =>
        message.tool_calls?.some((call) => call.function?.name === 'skills.read'),
      ),
    ).toBe(true)
    const toolResult = followUpMessages.find(
      (message) => message.role === 'tool' && typeof message.content === 'string',
    )?.content as string | undefined
    expect(toolResult).toContain(SKILL_BODY)

    await answerFinished(page, QUESTION_B)
    await expect(answerBlock(page, QUESTION_B).locator('[data-answer-body]')).toContainText(
      SKILL_BODY,
    )
  })
})

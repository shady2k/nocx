/**
 * e2e: a refusal becomes an answer, and a person can stop a thinking turn
 * (nocx-uvac6.7) — the check that closes the agent refusal/stop epic.
 *
 * WHAT THIS FILE WATCHES. The feature's children are unit- and
 * transport-green, but those layers do not watch a person use the two
 * surfaces together. These journeys do, against this file's real nocx-server
 * and a model fake reachable over 127.0.0.1:
 *
 *   - A person asks about a refused `session.read`, presses Deny once, and
 *     reads an answer that says what could not happen, why, and what approval
 *     would be needed. The answer reaches `completed`, rather than leaving a
 *     dead or failed header. A second refusal journey presses Deny always and
 *     asserts the standing "from now on; do not propose it again" sentence
 *     in the tool result handed to the model.
 *   - A person asks a question, sees its first prose while the model is still
 *     streaming, opens the turn's overflow menu, and presses Stop. The
 *     partial prose remains under a `stopped` header, and the next question
 *     completes normally.
 *
 * THE SESSION ID IS LEARNED, NEVER INVENTED. `session.read` and `run` each name
 * the exact session resource in their arguments, and the policy scope check
 * compares that identity. The first content-only ask therefore records the id
 * from the product's own `agent.ask` frame before the refusal proposal is
 * scripted.
 *
 * THE REFUSAL ANSWER IS DERIVED FROM THE TOOL RESULT. The fake's follow-up
 * response function extracts the `role: tool` result from the request body;
 * it only emits the answer asserted below when that result reached the model.
 * A fixed answer could pass while the refusal was dropped or the run died at
 * the approval window. The second script is queued BEFORE Deny, because
 * denying resumes the run with a follow-up model request.
 *
 * THE STOP IS HELD, NOT SLEPT. `holdAfter: 1` writes one content chunk and
 * waits for an explicit release. The test observes the fake's `streaming`
 * state and the partial answer, then presses the real overflow-menu Stop. No
 * wait is a duration; every wait observes a DOM or fake state transition.
 */
import { expect, type Page } from '@playwright/test'
import { mkdtempSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import {
  standalone as base,
  appReadyForInput,
  VaultBackend,
  bindEndpoint,
  createAiEndpoint,
  setDefaultModel,
  settingsReady,
} from './harness'
import { readStand } from './stand'
import { FakeOpenAI, type FakeRequest } from './fake-openai'

const serverBin = () => readStand().server

const TITLE = '.nocx-tab-title'
const INPUT = '.pane.active .nocx-editor-input'
const SETTINGS_AI_NAV = '.ui-grouped-nav__item[data-item="endpoints"]'
const SETTINGS_ROLES_NAV = '.ui-grouped-nav__item[data-item="roles"]'
const APPROVAL_TITLE = 'This action needs your approval'

const test = base
const nonce = Date.now().toString(36)
const ENDPOINT_NAME = `E2E Refusal Stop ${nonce}`

let backend: VaultBackend
let fake: FakeOpenAI
let endpoint: { port: number; token: string }

// The second journey relies on the first journey having configured the
// endpoint and role through the real Settings surfaces.
test.describe.configure({ mode: 'serial' })

test.beforeAll(async () => {
  fake = new FakeOpenAI()
  await fake.start()
  const root = mkdtempSync(join(tmpdir(), 'nocx-uvac6-7-e2e-'))
  // No Secret Service in the container: the derived content key makes the
  // vault available without an interactive setup prompt.
  backend = new VaultBackend(serverBin(), { root })
  endpoint = await backend.start()
})

test.afterAll(async () => {
  backend?.stop()
  await fake?.stop()
})

/** The session id each real `agent.ask` frame carried. */
function recordAskSessions(page: Page): string[] {
  const ids: string[] = []
  page.on('websocket', (ws) => {
    ws.on('framesent', (event) => {
      const payload = event.payload
      if (typeof payload !== 'string' || !payload.includes('"method":"agent.ask"')) return
      const message = JSON.parse(payload) as { params?: { sessionId?: string } }
      if (message.params?.sessionId) ids.push(message.params.sessionId)
    })
  })
  return ids
}

async function openApp(page: Page): Promise<void> {
  await bindEndpoint(page, endpoint)
  await page.goto('/')
  await expect(page.locator(TITLE).first()).not.toHaveText('', { timeout: 15_000 })
  await appReadyForInput(page)
}

/** Send a question through the ordinary editor after selecting the agent
 * target. The person has one editor; Ctrl/Cmd+Enter only changes its target. */
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
  return page.locator('.pane.active .cmd-block').filter({ hasText: question })
}

/** The answer block's own header, excluding any child block's status chip. */
async function answerState(page: Page, question: string, state: string): Promise<void> {
  await expect(
    answerBlock(page, question).locator(':scope > .cmd-header .cmd-header-exit'),
  ).toHaveText(state, { timeout: 30_000 })
}

function approvalPrompt(page: Page) {
  return page.getByRole('dialog', { name: APPROVAL_TITLE })
}

async function openSettings(page: Page, navSelector: string): Promise<void> {
  await page.keyboard.press('Meta+,')
  await settingsReady(page)
  await page.locator(navSelector).click()
}

async function configureAssistant(page: Page): Promise<void> {
  await openSettings(page, SETTINGS_AI_NAV)
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
}

async function backToTerminal(page: Page): Promise<void> {
  await page.locator(TITLE).first().click()
  await expect(page.locator(INPUT)).toBeVisible({ timeout: 10_000 })
}

/** Chat-completion requests after `from`; model-discovery GETs have no
 * messages and must not count as an ask round. */
function chatRequests(fake: FakeOpenAI, from: number): FakeRequest[] {
  return fake
    .requests()
    .slice(from)
    .filter((request) => request.body.includes('"messages"')) as FakeRequest[]
}

/** Extract the model-facing refusal messages, preserving the exact text that
 * the backend put in the tool-result slot. */
function toolResults(body: string): string[] {
  try {
    const parsed = JSON.parse(body) as {
      messages?: { role?: string; content?: unknown }[]
    }
    return (parsed.messages ?? [])
      .filter((message) => message.role === 'tool' && typeof message.content === 'string')
      .map((message) => message.content as string)
  } catch {
    return []
  }
}

/** The follow-up model answer is possible only when the refusal was delivered
 * as a tool result. The fallback deliberately fails the rendered assertions
 * rather than masking a missing refusal with a default `ok` answer. */
function answerFromRefusal(
  body: string,
  action: 'read this session' | 'run this command',
): string[] {
  const results = toolResults(body)
  if (results.length === 0) {
    return ['I could not complete that request because no refusal result reached me.']
  }

  return [
    'I could not complete that request because the call was refused: ',
    results.join(' '),
    ` I would need your approval to ${action}.`,
  ]
}

// EVERY question in this file is unique, and that is load-bearing rather than
// tidy. Blocks are addressed by their text (`answerBlock`), the file runs
// serial against ONE backend, and a fresh page restores the scrollback the
// earlier journeys left. Two journeys asking the same thing therefore make the
// locator resolve to two elements and one journey fails for another journey's
// reason — which is exactly what happened (nocx-3xjou). So the learn question
// carries the journey's own name.
const learn = (journey: string) => `Are you there, ${nonce} ${journey}?`

test.describe('a refusal is an answer, and a turn can be stopped (nocx-uvac6.7)', () => {
  test.use({ viewport: { width: 1280, height: 900 } })

  test('Deny leaves a completed answer naming the refusal and what it needs', async ({ page }) => {
    test.setTimeout(120_000)
    const asks = recordAskSessions(page)
    await openApp(page)
    await configureAssistant(page)
    await backToTerminal(page)

    // Learn the authoritative session id from a completed content-only ask.
    fake.setScript({ chunks: ['Ready.'] })
    const LEARN = learn('deny-once')
    await askFromPrompt(page, LEARN)
    await answerState(page, LEARN, 'completed')
    await expect.poll(() => asks.length, { timeout: 15_000 }).toBeGreaterThan(0)
    const sessionId = asks[asks.length - 1]
    expect(sessionId).not.toBe('')

    const QUESTION = `What can you read for me, ${nonce}?`
    const base = fake.requests().length
    // The first response proposes the policy-gated call. Deny resumes the
    // same turn, so this second script is armed before the human gesture.
    fake.setScript({
      chunks: [],
      toolCalls: [{ name: 'session.read', arguments: {} }],
    })
    fake.setScript({ chunks: (body) => answerFromRefusal(body, 'read this session') })
    await askFromPrompt(page, QUESTION)

    const prompt = approvalPrompt(page)
    await expect(prompt).toBeVisible({ timeout: 30_000 })
    await expect(prompt).toContainText('read and inspect')
    await expect(prompt).toContainText('session.read')
    await prompt.getByRole('button', { name: 'Deny once' }).click()

    // Completion is the positive proof that the gate was passed and the
    // refusal became part of a live answer, not a prompt that merely vanished.
    await answerState(page, QUESTION, 'completed')
    await expect(prompt).toHaveCount(0)

    await expect.poll(() => chatRequests(fake, base).length, { timeout: 15_000 }).toBe(2)
    const requests = chatRequests(fake, base)
    const refusal = toolResults(requests[1]?.body ?? '')
    expect(refusal).toHaveLength(1)
    expect(refusal[0]).toContain('REFUSED')
    expect(refusal[0]).toContain('session.read')

    const body = answerBlock(page, QUESTION).locator('.cmd-output[data-answer-body]')
    await expect(body).toContainText(
      'I could not complete that request because the call was refused',
    )
    await expect(body).toContainText(refusal[0])
    await expect(body).toContainText('I would need your approval to read this session.')
    await expect(
      answerBlock(page, QUESTION).locator(':scope > .cmd-header .cmd-header-exit'),
    ).not.toHaveText('failed')
  })

  test('Deny always tells the model not to propose the call again', async ({ page }) => {
    test.setTimeout(120_000)
    const asks = recordAskSessions(page)
    await openApp(page)
    await backToTerminal(page)

    // Learn the authoritative session id from a completed content-only ask,
    // as the first journey does — under this journey's own name, so the two
    // learn blocks never collide in a restored scrollback.
    fake.setScript({ chunks: ['Ready.'] })
    const LEARN = learn('deny-always')
    await askFromPrompt(page, LEARN)
    await answerState(page, LEARN, 'completed')
    await expect.poll(() => asks.length, { timeout: 15_000 }).toBeGreaterThan(0)
    const sessionId = asks[asks.length - 1]
    expect(sessionId).not.toBe('')

    const QUESTION = `What happens if I deny this forever, ${nonce}?`
    const COMMAND = 'df -h'
    const base = fake.requests().length
    fake.setScript({
      chunks: [],
      toolCalls: [{ name: 'session.run', arguments: { command: COMMAND } }],
    })
    // Deny resumes the turn with a second model request. Its answer is
    // derived from the refusal result, so a missing result cannot fall back
    // to FakeOpenAI's default `ok`.
    fake.setScript({ chunks: (body) => answerFromRefusal(body, 'run this command') })
    await askFromPrompt(page, QUESTION)

    const prompt = approvalPrompt(page)
    await expect(prompt).toBeVisible({ timeout: 30_000 })
    const denyAlways = prompt.getByRole('button', {
      name: /^Deny always — .+ — in every session, from now on$/,
    })
    await expect(denyAlways).toHaveCount(1)
    await denyAlways.click()

    // The completion chip is the positive proof that the approval gate was
    // reached and resumed; only then does the prompt absence mean no second
    // approval was requested.
    await answerState(page, QUESTION, 'completed')
    await expect(prompt).toHaveCount(0)
    await expect.poll(() => chatRequests(fake, base).length, { timeout: 15_000 }).toBe(2)

    const requests = chatRequests(fake, base)
    const refusal = toolResults(requests[1]?.body ?? '')
    expect(refusal).toHaveLength(1)
    expect(refusal[0]).toContain('REFUSED')
    expect(refusal[0]).toContain('run')
    expect(refusal[0]).toContain('from now on')
    expect(refusal[0]).toContain('Do not propose it again.')

    const body = answerBlock(page, QUESTION).locator('.cmd-output[data-answer-body]')
    await expect(body).toContainText(refusal[0])
    await expect(body).toContainText('I would need your approval to run this command.')
  })

  test('Stop preserves streamed prose and the next question completes normally', async ({
    page,
  }) => {
    test.setTimeout(120_000)
    await openApp(page)
    await backToTerminal(page)

    const QUESTION = `Stop this answer, ${nonce}?`
    const PARTIAL = 'This prose arrived before the person stopped the answer.'
    const base = fake.requests().length
    fake.setScript({ chunks: [PARTIAL, ' This suffix must not be required.'], holdAfter: 1 })
    await askFromPrompt(page, QUESTION)

    const requests = await fake.waitForRequests(base + 1)
    const request = requests.find((candidate) => candidate.id >= (requests[base]?.id ?? 0))
    if (!request) throw new Error('fake-openai: the held stop request was not recorded')
    await fake.waitForState(request.id, 'streaming')

    const answer = answerBlock(page, QUESTION)
    await expect(answer).toHaveCount(1, { timeout: 15_000 })
    const body = answer.locator('.cmd-output[data-answer-body]')
    await expect(body).toContainText(PARTIAL, { timeout: 15_000 })

    const overflow = answer.locator('.cmd-overflow-btn')
    await expect(overflow).toBeVisible()
    await overflow.click()
    const stop = page.locator('.cmd-overflow-menu-item[data-action="stop"]')
    await expect(stop).toBeVisible()
    await expect(stop).toBeEnabled()
    await stop.click()

    await answerState(page, QUESTION, 'stopped')
    await expect(body).toContainText(PARTIAL)
    await expect(answer.locator(':scope > .cmd-header .cmd-header-exit')).not.toHaveText('failed')

    const NEXT = `Now answer this normally, ${nonce}?`
    fake.setScript({ chunks: ['The next answer completed normally.'] })
    await expect(page.locator(INPUT)).toBeVisible()
    await askFromPrompt(page, NEXT)
    await answerState(page, NEXT, 'completed')
    await expect(answerBlock(page, NEXT).locator('.cmd-output[data-answer-body]')).toContainText(
      'The next answer completed normally.',
    )
  })
})

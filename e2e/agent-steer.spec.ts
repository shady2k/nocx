/**
 * e2e: a message typed while the agent answers reaches the agent, and stays in
 * the same turn (nocx-a62cl.6.1).
 *
 * THE JOURNEY. A person asks a question; the answer starts streaming; while it
 * is still being written they type "also check the second log" and press
 * Enter. One turn, one block (the coordinator's decision of 2026-11-15), so the
 * message does not open a block of its own: it is drawn inside the running one,
 * where it was sent, and the agent continues below it.
 *
 * WHAT MAKES IT A FACT ABOUT THE PRODUCT rather than about the fixture: the
 * model stream is HELD after its first chunk (fake-openai's holdAfter), so the
 * person really types during an open model call; and the assertion that the
 * message reached the agent is read off the NEXT model request's body, which is
 * only made after pi accepted the steer (ADR-anntnxdj decision 4: pi answers
 * `queued` and delivers it before its next model call). The first request is
 * asserted NOT to carry the words, because it was sent before they were typed.
 *
 * The next model call needs something the agent had to do first, so the first
 * response proposes nocx's session.read (agent-answer-stream.spec.ts's tool):
 * the proposal is only written after the hold is released, which is also what
 * makes the ordering deterministic. `observe` is Allowed through Settings, the
 * surface a person uses, so the call runs instead of asking.
 *
 * NOTHING HERE WAITS OUT A DURATION: the hold is released explicitly, request
 * state is polled, and the turn's own outcome chip is the end.
 */
import { expect, type Page } from '@playwright/test'
import { mkdtempSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import {
  standalone as base,
  answerPermission,
  appReadyForInput,
  bindEndpoint,
  ensureAssistantVault,
  settingsReady,
  VaultBackend,
} from './harness'
import { readStand } from './stand'
import { FakeOpenAI, type FakeRequest } from './fake-openai'
import { configurePiHome, readScreenTool } from './pi-home'

const serverBin = () => readStand().server

const TITLE = '.nocx-tab-title'
const INPUT = '.pane.active .nocx-editor-input'
const SETTINGS_POLICY_NAV = '.ui-grouped-nav__item[data-item="policy"]'

const test = base

let backend: VaultBackend
let fake: FakeOpenAI
let endpoint: { port: number; token: string }

test.beforeAll(async () => {
  fake = new FakeOpenAI()
  await fake.start()
  const root = mkdtempSync(join(tmpdir(), 'nocx-a62cl-61-e2e-'))
  backend = new VaultBackend(serverBin(), { root })
  endpoint = await backend.start(configurePiHome(root, fake.baseUrl()))
})

test.afterAll(async () => {
  backend?.stop()
  await fake?.stop()
})

/** Every agent.* frame this page sent, in order, as {method, params}. */
function recordAgentFrames(page: Page): { method: string; params: Record<string, unknown> }[] {
  const frames: { method: string; params: Record<string, unknown> }[] = []
  page.on('websocket', (ws) => {
    ws.on('framesent', (event) => {
      const payload = event.payload
      if (typeof payload !== 'string' || !payload.includes('"method":"agent.')) return
      const parsed = JSON.parse(payload) as {
        method?: string
        params?: Record<string, unknown>
      }
      if (parsed.method) frames.push({ method: parsed.method, params: parsed.params ?? {} })
    })
  })
  return frames
}

async function openApp(page: Page): Promise<void> {
  await bindEndpoint(page, endpoint)
  await page.goto('/')
  await expect(page.locator(TITLE).first()).not.toHaveText('', { timeout: 15_000 })
  await appReadyForInput(page)
  await ensureAssistantVault(page, endpoint, 'vault-pass-steer')
  await page.locator(TITLE).first().click()
  await expect(page.locator(INPUT)).toBeVisible({ timeout: 10_000 })
}

async function chooseTarget(page: Page, target: 'agent' | 'run'): Promise<void> {
  const input = page.locator(INPUT)
  await input.click()
  const indicator = page.locator('.pane.active .ui-mode-indicator:visible')
  if ((await indicator.getAttribute('data-target')) === target) return
  await page.keyboard.press('ControlOrMeta+Enter')
  await expect(indicator).toHaveAttribute('data-target', target, { timeout: 10_000 })
}

async function typeAndSubmit(page: Page, text: string): Promise<void> {
  const input = page.locator(INPUT)
  await input.click()
  await input.fill(text)
  await page.keyboard.press('Enter')
  await expect(input).toHaveText('')
}

/** The turn one question opened, in the pane it was asked in. */
function turn(page: Page, question: string) {
  return page.locator('.pane.active .cmd-block').filter({ hasText: question })
}

/** Chat-completion requests, in arrival order, with model discovery left out:
 *  the readiness check may reach the fake's /models and that is not a model
 *  call a steer could be in. */
function chatRequests(): FakeRequest[] {
  return fake.requests().filter((request) => request.body.includes('"messages"')) as FakeRequest[]
}

/** Wait until n model calls have arrived; answers with them all, oldest first. */
async function waitForChatRequests(n: number): Promise<FakeRequest[]> {
  await expect.poll(() => chatRequests().length, { timeout: 30_000 }).toBeGreaterThanOrEqual(n)
  return chatRequests()
}

test.describe('a message typed while the agent answers (nocx-a62cl.6.1)', () => {
  test.use({ viewport: { width: 1280, height: 900 } })

  test('reaches the next model call and stays in the same answer block', async ({ page }) => {
    const frames = recordAgentFrames(page)
    await openApp(page)

    // The observe row Allowed, through the page a person uses: the scripted
    // session.read then RUNS rather than asking, which is what makes the
    // second model call arrive.
    await page.keyboard.press('ControlOrMeta+,')
    await settingsReady(page)
    await page.locator(SETTINGS_POLICY_NAV).click()
    await answerPermission(page, 'observe', 'Allowed')
    await page.locator(TITLE).first().click()
    await expect(page.locator(INPUT)).toBeVisible({ timeout: 10_000 })

    const QUESTION = 'What is on the screen, and is anything unusual?'
    const MESSAGE = 'also check the second log too'
    const ANSWER_START = 'Reading the screen '
    const ANSWER_MORE = 'now. '
    const ANSWER_AFTER = 'Continuing with what you asked.'

    // A real tool-calling run is TWO model responses: the proposal and the
    // answer written from its result. The first is held after its opening
    // words, so the person is typing while a model call is genuinely open.
    // Two chunks with the hold after the first: fake-openai holds only when
    // more of the response is still to come, so the stream stays genuinely
    // open while the person types.
    fake.setScript({ chunks: [ANSWER_START, ANSWER_MORE], holdAfter: 1, toolCalls: readScreenTool })
    fake.setScript({ chunks: [ANSWER_AFTER] })

    await chooseTarget(page, 'agent')
    const before = chatRequests().length
    await typeAndSubmit(page, QUESTION)

    const requests = await waitForChatRequests(before + 1)
    const first = requests[before]
    await fake.waitForState(first.id, 'streaming')
    const block = turn(page, QUESTION)
    await expect(block.locator('[data-answer-body]').last()).toContainText(ANSWER_START, {
      timeout: 30_000,
    })

    // THE PERSON SPEAKS WHILE THE ANSWER IS BEING WRITTEN.
    await typeAndSubmit(page, MESSAGE)

    // The message is drawn INSIDE the running turn, named as the person's —
    // one turn, one block — and the turn is still open.
    const said = block.locator(':scope > .cmd-children > .ui-person-message')
    await expect(said).toHaveCount(1)
    await expect(said.locator('.ui-person-message__text')).toHaveText(MESSAGE)
    await expect(block).not.toHaveAttribute('data-outcome', 'success')
    // No second block opened for it: the pane holds exactly one turn with this
    // question, and the message is not a question of its own.
    await expect(page.locator('.pane.active .cmd-block').filter({ hasText: MESSAGE })).toHaveCount(
      1,
    )

    // Release the held call: pi proposes the tool, runs it, and makes the next
    // model call — the one the person's message must be in.
    fake.release(first.id)
    const after = await waitForChatRequests(before + 2)
    const next = after[before + 1]
    expect(next.body).toContain(MESSAGE)
    expect(first.body).not.toContain(MESSAGE)

    // The answer continues in the SAME block, below the message: the person
    // reads their own words and then the agent's reply to them.
    await expect(block).toHaveAttribute('data-outcome', 'success', { timeout: 30_000 })
    await expect(block.locator('[data-answer-body]').last()).toContainText(ANSWER_AFTER)
    const children = await block.locator(':scope > .cmd-children > *').evaluateAll((els) =>
      els.map((el) => {
        const kind = (el as HTMLElement).dataset.blockKind
        if (kind) return kind
        return el.classList.contains('ui-person-message') ? 'said' : 'chrome'
      }),
    )
    expect(children).toContain('said')
    expect(children.lastIndexOf('text')).toBeGreaterThan(children.indexOf('said'))

    // The wire says the same thing: one ask, one steer, and no second ask.
    expect(frames.filter((frame) => frame.method === 'agent.ask')).toHaveLength(1)
    const steers = frames.filter((frame) => frame.method === 'agent.steer')
    expect(steers).toHaveLength(1)
    expect(steers[0].params.text).toBe(MESSAGE)
    expect(steers[0].params.runId).toEqual(expect.any(Number))
  })
})

/**
 * e2e: an ask decision BLOCKS the very tool call, and the block shows the
 * question until it is answered (nocx-a62cl.3.1).
 *
 * What this file is for. Until this stage a policy question ended the drive:
 * the run suspended, the question was published after the suspension unwound,
 * and an answer started a NEW drive from eino's checkpoint. The service
 * (internal/transport/ws_agent_approval_service.go) makes the call itself wait,
 * so the DONE WHEN is a person's journey and nothing less:
 *
 *   a call under an ask row shows the approval card, its tool has not run (no
 *   side effect), approving runs it ONCE and puts its result in the same
 *   block, and declining runs nothing and the block says declined.
 *
 * THE SEAMS, AND WHY EACH IS THE ONE A PERSON REACHES:
 *
 * - The proposal is scripted: `e2e/fake-openai.ts` writes one
 *   `delta.tool_calls` frame naming `session.run` with a command that echoes a
 *   marker. `session.run` OPENS A BLOCK (internal/agenttools/registry.go), so
 *   the command block the call opens IS the account of the call: its presence
 *   is the side effect, and its absence is "nothing ran".
 * - THE COMMAND REALLY RUNS, and the proof is the model's own prose: the
 *   follow-up script is a FUNCTION of the request body and says its sentence
 *   only when the command's real output came back through the tool result. A
 *   fixed string would say it with no command having run.
 * - THE DECLINE is read the same way: the follow-up says its sentence only
 *   when a `REFUSED: the person declined` tool result reached the model, so
 *   "the block says declined" is a fact about the backend rather than about
 *   this file's hopes.
 * - The question and its answer travel through the REAL prompt and the REAL
 *   buttons; nothing here calls `agent.approve` or touches the store.
 *
 * NOTHING WAITS OUT A DURATION. The two "it has not run" assertions are made
 * AFTER the prompt is visible — the gate was reached, so the absence is a fact
 * about the product rather than about t=0 — and they are made twice: no command
 * block in the turn, and exactly one chat request to the fake (the tool result
 * never reached the model, so the model was never asked to continue).
 */
import { expect, type Locator, type Page } from '@playwright/test'
import { mkdtempSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import {
  standalone as base,
  appReadyForInput,
  bindEndpoint,
  createAiEndpoint,
  setDefaultModel,
  settingsReady,
  VaultBackend,
} from './harness'
import { readStand } from './stand'
import { FakeOpenAI, type FakeRequest } from './fake-openai'

/** Lazily, not at module scope: the stand is started by globalSetup, which
 *  runs after Playwright has collected this file. */
const serverBin = () => readStand().server

const TITLE = '.nocx-tab-title'
const INPUT = '.pane.active .nocx-editor-input'
const SETTINGS_AI_NAV = '.ui-grouped-nav__item[data-item="endpoints"]'
const SETTINGS_ROLES_NAV = '.ui-grouped-nav__item[data-item="roles"]'
const APPROVAL_TITLE = 'This action needs your approval'

const test = base

/** One nonce per file, so this file's endpoint name and every question and
 *  command in it are unique in the whole run. */
const nonce = Date.now().toString(36)
const ENDPOINT_NAME = `E2E Approval Block ${nonce}`

/** The approved round's command, its output marker, and the sentence the model
 *  may say only if that output came back through the tool. */
const RUN_CMD = `echo approved-${nonce}`
const RUN_MARKER = `approved-${nonce}`
/** The declined round's command. It must never reach the shell. */
const DENY_CMD = `echo declined-${nonce}`
/** What the model says when the person's refusal is what it was handed. */
const DECLINED_SENTENCE = `The person declined it, so ${DENY_CMD} did not run.`

let backend: VaultBackend
let fake: FakeOpenAI
let endpoint: { port: number; token: string }

test.describe.configure({ mode: 'serial' })

test.beforeAll(async () => {
  fake = new FakeOpenAI()
  await fake.start()
  const root = mkdtempSync(join(tmpdir(), 'nocx-a62cl-3-1-e2e-'))
  // `true` = no Secret Service for this backend: the container has no
  // keychain to ask, and the derived content key makes the vault available
  // without user setup — the arrangement agent-ask.spec.ts uses.
  backend = new VaultBackend(serverBin(), { root })
  endpoint = await backend.start()
})

test.afterAll(async () => {
  backend?.stop()
  await fake?.stop()
})

/** Point the page at this file's backend, open the app, wait for the first
 *  tab. */
async function openApp(page: Page): Promise<void> {
  await bindEndpoint(page, endpoint)
  await page.goto('/')
  await expect(page.locator(TITLE).first()).not.toHaveText('', { timeout: 15_000 })
  await appReadyForInput(page)
}

/** Send the drafted line to the ASSISTANT: ⌘/Ctrl+Enter flips where Enter
 *  goes, then Enter is the one send key. Idempotent on the flip. */
async function askFromPrompt(page: Page, question: string): Promise<void> {
  const input = page.locator(INPUT)
  await input.click()
  // `:visible` on purpose: CM6 keeps a hidden measurement spacer beside the
  // real marker, carrying an identical button.
  const indicator = page.locator('.pane.active .ui-mode-indicator:visible')
  if ((await indicator.getAttribute('data-target')) !== 'agent') {
    await page.keyboard.press('ControlOrMeta+Enter')
    await expect(indicator).toHaveAttribute('data-target', 'agent', { timeout: 10_000 })
  }
  await input.fill(question)
  await page.keyboard.press('Enter')
}

/** The turn one question opened, in the pane it was asked in. */
function turn(page: Page, question: string): Locator {
  return page.locator('.pane.active .cmd-block').filter({ hasText: question })
}

/** The approval prompt, by the title a policy question carries. */
function approvalPrompt(page: Page): Locator {
  return page.getByRole('dialog', { name: APPROVAL_TITLE })
}

/** The command block the assistant's call opened, inside that turn.
 *
 *  `session.run` OPENS A BLOCK, so it draws no tool child of its own: the block
 *  the command opened IS the account of the call (ADR-0040). Its presence is
 *  the side effect; its absence is the call not having run. */
function commandBlock(page: Page, question: string): Locator {
  return turn(page, question).locator(
    ':scope > .cmd-children > .cmd-block[data-block-kind="command"]',
  )
}

/** The answer a person reads, which is derived from what the tool returned.
 *
 *  The LAST one in the turn: the prose before the call ("I will check the
 *  shell.") is a block of its own, sealed where the call arrived, and the
 *  answer written from the call's result opens the next one (ADR-0040). The
 *  claim is about what the person reads after the call, so it is the later
 *  block. */
function answerBody(page: Page, question: string): Locator {
  return turn(page, question).locator('.cmd-output[data-answer-body]').last()
}

/** Wait until the answer to `question` has FINISHED — the turn's own completion
 *  chip, never a child's. A run suspended at the policy gate never gets one. */
async function answerFinished(page: Page, question: string): Promise<void> {
  await expect(turn(page, question)).toHaveAttribute('data-outcome', 'success', {
    timeout: 60_000,
  })
}

/** Chat-completion requests after `from`. The endpoint's model-discovery GET
 *  has no messages and must not count as a round. */
function chatRequests(from: number): FakeRequest[] {
  return fake
    .requests()
    .slice(from)
    .filter((request) => request.body.includes('"messages"')) as FakeRequest[]
}

/** The tool-result slots of one model request, verbatim — what the backend
 *  actually handed back for the calls it was asked to make. */
function toolResults(body: string): string[] {
  try {
    const parsed = JSON.parse(body) as { messages?: { role?: string; content?: unknown }[] }
    return (parsed.messages ?? [])
      .filter((message) => message.role === 'tool' && typeof message.content === 'string')
      .map((message) => message.content as string)
  } catch {
    return []
  }
}

/** The follow-up prose, DERIVED from what came back through the tool: the
 *  sentence is said only when the tool result carries `want`. */
function proseFromToolResult(want: string, sentence: string): (body: string) => string[] {
  return (body: string) =>
    toolResults(body).some((result) => result.includes(want))
      ? [sentence]
      : [`Nothing carrying ${want} reached me, so I cannot say that.`]
}

/** Create the endpoint and give its model the answering role, through the
 *  surfaces a person uses. A fresh home has no vault, so the first save stops
 *  on the setup sheet and is retried once the vault exists; createAiEndpoint
 *  reads which of the two happened rather than assuming. */
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
}

test.describe('an ask blocks the call, and the answer releases it (nocx-a62cl.3.1)', () => {
  test.use({ viewport: { width: 1280, height: 900 } })

  test('approving runs the call once and its result lands in the same block', async ({ page }) => {
    test.setTimeout(180_000)
    await openApp(page)
    await configureAssistant(page)
    await page.locator(TITLE).first().click()
    await expect(page.locator(INPUT)).toBeVisible({ timeout: 10_000 })

    const base = fake.requests().length
    fake.setScript({
      chunks: ['I will check the shell.'],
      toolCalls: [{ name: 'session.run', id: 'call_run', arguments: { command: RUN_CMD } }],
    })
    fake.setScript({ chunks: proseFromToolResult(RUN_MARKER, `The shell printed ${RUN_MARKER}.`) })

    const QUESTION = `What does ${RUN_MARKER} print, ${nonce}?`
    await askFromPrompt(page, QUESTION)

    // ── The question is on screen, and it names the call: a command question
    //    quotes the command verbatim (it does not print the tool's name — the
    //    words a person reads are the command, not our registry row).
    const prompt = approvalPrompt(page)
    await expect(prompt).toBeVisible({ timeout: 60_000 })
    await expect(prompt).toContainText(RUN_CMD)

    // ── AND NOTHING HAS RUN. Two independent readings, both taken after the
    //    gate was reached: the turn opened no command block, and the model was
    //    asked exactly once — the tool's result never came back to it.
    await expect(commandBlock(page, QUESTION)).toHaveCount(0)
    expect(chatRequests(base).length).toBe(1)

    // ── The person says yes, once.
    await prompt.getByRole('button', { name: 'Allow once' }).click()
    await expect(prompt).toHaveCount(0, { timeout: 15_000 })

    // ── The call ran ONCE, in the SAME turn, and the shell said so.
    const block = commandBlock(page, QUESTION)
    await expect(block).toHaveCount(1, { timeout: 60_000 })
    await expect(block.locator('.cmd-header-text')).toContainText(RUN_CMD)
    await expect(block.locator('.cmd-output')).toContainText(RUN_MARKER, { timeout: 30_000 })
    await expect(block).toHaveAttribute('data-outcome', 'success', { timeout: 30_000 })

    // ── Its result reached the model as that call's result, and the answer the
    //    person reads is derived from it — in the same block as the question.
    await answerFinished(page, QUESTION)
    await expect(answerBody(page, QUESTION)).toContainText(`The shell printed ${RUN_MARKER}.`)

    // And the command block exists exactly once: the tool's body ran once, not
    // once per answer or once per drive.
    await expect(commandBlock(page, QUESTION)).toHaveCount(1)
  })

  test('declining runs nothing and the block says declined', async ({ page }) => {
    test.setTimeout(180_000)
    // A fresh page, the SAME backend: the endpoint and the answering role the
    // test above configured are settings, and they survive a tab.
    await openApp(page)

    const base = fake.requests().length
    fake.setScript({
      chunks: ['I will check the shell.'],
      toolCalls: [{ name: 'session.run', id: 'call_deny', arguments: { command: DENY_CMD } }],
    })
    fake.setScript({
      chunks: proseFromToolResult('REFUSED: the person declined', DECLINED_SENTENCE),
    })

    const QUESTION = `What does ${DENY_CMD} print, ${nonce}?`
    await askFromPrompt(page, QUESTION)

    const prompt = approvalPrompt(page)
    await expect(prompt).toBeVisible({ timeout: 60_000 })
    await expect(prompt).toContainText(DENY_CMD)
    await expect(commandBlock(page, QUESTION)).toHaveCount(0)
    expect(chatRequests(base).length).toBe(1)

    await prompt.getByRole('button', { name: 'Deny once' }).click()
    await expect(prompt).toHaveCount(0, { timeout: 15_000 })

    // ── Nothing ran: no command block, ever.
    await answerFinished(page, QUESTION)
    await expect(commandBlock(page, QUESTION)).toHaveCount(0)
    // ── And the block says declined: the sentence the model could only write
    //    from the person's refusal it was handed.
    await expect(answerBody(page, QUESTION)).toContainText(DECLINED_SENTENCE)
  })
})

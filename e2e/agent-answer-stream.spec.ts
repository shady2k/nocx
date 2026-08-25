/**
 * e2e: what the assistant DID is visible, and it is visible IN ORDER
 * (nocx-shxv0, nocx-bshm2).
 *
 * WHAT THIS FILE IS FOR. On 2026-08-22 the owner asked the assistant "what
 * command did I run?". It read the screen, got an empty one, ran a command
 * through the run tool and answered from its output. On screen: the
 * screen-reading call left NO TRACE AT ALL; the run tool's block sat BELOW
 * the finished answer written from it, so the flow read "answered first, ran
 * the command afterwards"; and the only sign a tool had run was a raw JSON
 * blob rendered inside the answer as though the assistant had spoken it.
 *
 * Every unit around that was green. This is the check that watches a person
 * see it fixed, which is the only kind that could have reported it
 * (AGENTS.md testing rules 1 and 2).
 *
 * The seam, and where each half is decided:
 *
 * - The PROPOSAL is scripted by e2e/fake-openai.ts: one `delta.tool_calls`
 *   frame naming `session.read`, then a SECOND scripted response carrying
 *   the answer written from the result. Two scripts, because that is what a
 *   real tool-calling run is — the model is called again with the tool's
 *   result. `session.read` with no item id is "the screen now"
 *   (contracts/tools/session.read.schema.json): the tool that replaced the
 *   deleted screen reader in nocx-2ryxf.1, and a name the registry does not
 *   hold is not a failing call but a run that cannot start (policy.go's
 *   ErrMalformedModelOutput).
 * - The GATE is set to Allowed for the `observe` row through Settings →
 *   Agent policy, the surface a person uses, so the call EXECUTES rather
 *   than suspending. This spec is about what a completed call looks like;
 *   agent-policy.spec.ts owns the asking.
 * - THE SESSION ID IS LEARNED, NEVER INVENTED, for the reason
 *   agent-tool-approval.spec.ts records at length: the policy's scope check
 *   compares a session resource for exact identity against the run's grant
 *   scope, so a made-up id is refused before the call can run at all.
 * - The ORDER is read off the DOM, not off a screenshot: the TURN's own
 *   children in document order. Since ADR-0040 a turn carries what it did as
 *   `.cmd-children`, each child declaring its `data-block-kind` — a call that
 *   opens no block is a `tool` child naming the tool and the arguments it ran
 *   on, and a run of prose is a `text` child. So "the call precedes the text
 *   written from it" is an index comparison over those kinds.
 *
 * NOTHING HERE WAITS OUT A DURATION. The synchronisation point is the
 * answer's `completed` chip — the run reached a terminal state, so the call
 * has happened and the answer is whole — and only then is the order read.
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

const devharnessBin = () => readStand().devharness

const TITLE = '.nocx-tab-title'
const INPUT = '.pane.active .nocx-editor-input'
const SETTINGS_AI_NAV = '.ui-grouped-nav__item[data-item="endpoints"]'
const SETTINGS_ROLES_NAV = '.ui-grouped-nav__item[data-item="roles"]'
const SETTINGS_POLICY_NAV = '.ui-grouped-nav__item[data-item="policy"]'
const OBSERVE_ROW = '.st-policy__row[data-effect="observe"]'
const APPROVAL_TITLE = 'This action needs your approval'

const test = base
const nonce = Date.now().toString(36)

let backend: VaultBackend
let fake: FakeOpenAI
let endpoint: { port: number; token: string }

test.beforeAll(async () => {
  fake = new FakeOpenAI()
  await fake.start()
  const root = mkdtempSync(join(tmpdir(), 'nocx-shxv0-e2e-'))
  backend = new VaultBackend(devharnessBin(), { root }, true)
  endpoint = await backend.start()
})

test.afterAll(async () => {
  backend?.stop()
  await fake?.stop()
})

function recordAskSessions(page: Page): string[] {
  const ids: string[] = []
  page.on('websocket', (ws) => {
    ws.on('framesent', (e) => {
      const p = e.payload
      if (typeof p !== 'string' || !p.includes('"method":"agent.ask"')) return
      const parsed = JSON.parse(p) as { params?: { sessionId?: string } }
      if (parsed.params?.sessionId) ids.push(parsed.params.sessionId)
    })
  })
  return ids
}

async function openApp(page: Page): Promise<void> {
  await bindEndpoint(page, endpoint)
  await page.goto('/')
  await expect(page.locator(TITLE).first()).not.toHaveText('', { timeout: 15_000 })
}

async function openSettings(page: Page, navSelector: string): Promise<void> {
  await page.keyboard.press('Meta+,')
  await settingsReady(page)
  await page.locator(navSelector).click()
}

async function backToTerminal(page: Page): Promise<void> {
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
  return page.locator('.cmd-block').filter({ hasText: question })
}

async function answerFinished(page: Page, question: string): Promise<void> {
  await expect(answerBlock(page, question).locator('.cmd-header-exit')).toHaveText('completed', {
    timeout: 30_000,
  })
}

async function configureAssistant(page: Page, endpointName: string): Promise<void> {
  await openSettings(page, SETTINGS_AI_NAV)
  await expect(page.locator('.ep-root')).toBeVisible({ timeout: 10_000 })
  await createAiEndpoint(page, {
    name: endpointName,
    baseUrl: fake.baseUrl(),
    models: ['e2e-model'],
    key: `e2e-key-${nonce}`,
    vaultPassphrase: `vault-pass-${nonce}`,
  })
  await page.locator(SETTINGS_ROLES_NAV).click()
  await setDefaultModel(page, endpointName, 'e2e-model')
}

const ENDPOINT_NAME = `E2E Answer Stream ${nonce}`

test.describe('the assistant’s tool call is visible, where it happened (nocx-shxv0)', () => {
  test.use({ viewport: { width: 1280, height: 900 } })

  test('the call appears in the answer, before the text written from it', async ({ page }) => {
    const asks = recordAskSessions(page)
    await openApp(page)
    await configureAssistant(page, ENDPOINT_NAME)

    // The `observe` row is set to Allowed through the page a person uses, so
    // the proposed session.read EXECUTES instead of asking. (An unset row asks
    // — the zero matrix is a policy — which is agent-policy.spec.ts's
    // subject, not this file's.)
    await page.locator(SETTINGS_POLICY_NAV).click()
    const observeRow = page.locator(OBSERVE_ROW)
    await expect(observeRow).toBeVisible({ timeout: 15_000 })
    await observeRow.locator('select').first().selectOption({ label: 'Allowed' })
    await expect(observeRow.locator('.st-policy__state')).toContainText('Allowed', {
      timeout: 15_000,
    })
    await backToTerminal(page)

    // The session the question is asked in, learned from the product's own
    // frame — a scripted session.read must name it exactly (AD-7).
    const before = asks.length
    const FIRST = 'Is anything on the screen?'
    await askFromPrompt(page, FIRST)
    await answerFinished(page, FIRST)
    await expect.poll(() => asks.length, { timeout: 15_000 }).toBeGreaterThan(before)
    const sessionId = asks[asks.length - 1]
    expect(sessionId).not.toBe('')

    // A real tool-calling run is TWO model responses: the proposal, then the
    // answer written from the result.
    fake.setScript({ chunks: [], toolCalls: [{ name: 'session.read', arguments: { sessionId } }] })
    fake.setScript({ chunks: ['The screen ', 'is empty.'] })

    const QUESTION = 'What command did I run?'
    await askFromPrompt(page, QUESTION)
    await answerFinished(page, QUESTION)
    // Nobody was asked: the row is Allowed, so the run went straight
    // through. Asserted AFTER the terminal chip, so the absence is a fact
    // about the product rather than a race the test wins at t=0.
    await expect(page.getByRole('dialog', { name: APPROVAL_TITLE })).toHaveCount(0)

    // THE TURN'S OWN CHILDREN, in the seats the run put them in (ADR-0040).
    const turn = answerBlock(page, QUESTION)
    const children = turn.locator(':scope > .cmd-children > .cmd-block')

    // 1. The call left a trace at all — the defect was that the screen read
    //    left none. A call that opens no block is drawn as its own `tool`
    //    child, named by the tool and the arguments it ran on.
    const call = turn.locator(':scope > .cmd-children > .cmd-block[data-block-kind="tool"]')
    await expect(call).toHaveCount(1)
    await expect(call).toHaveAttribute('data-tool', 'session.read')
    const title = call.locator('.cmd-header-text')
    await expect(title).toContainText('session.read')
    // The session is NAMED, never numbered (nocx-vnzek): a 32-hex handle
    // says nothing to a person. The name is asserted against the tab strip's,
    // because that is the point — one derivation for "what is this session
    // called", read by both surfaces, so they cannot drift apart the way two
    // copies would.
    await expect(title).not.toContainText(sessionId)
    await expect(title).toContainText(await page.locator(TITLE).first().innerText())
    // And it is not a command block pretending: a call that opened no block
    // has no output of its own — the run tool's block is the other case, and
    // agent-turn.spec.ts owns it.
    await expect(call.locator('.cmd-output')).toHaveCount(0)

    // 2. It precedes the answer written from it. Read off the DOM order of
    //    the turn's children, which is what a person sees top to bottom.
    const order = await children.evaluateAll((els) =>
      els.map((el) => (el as HTMLElement).dataset.blockKind ?? 'other'),
    )
    expect(order.indexOf('tool')).toBeGreaterThanOrEqual(0)
    expect(order.indexOf('text')).toBeGreaterThan(order.indexOf('tool'))

    // 3. And the tool's raw return is not in the answer's words. The
    //    session.read result is a JSON object with a "sessionId" key and a
    //    "text" key; the answer is the model's sentence.
    const text = (
      await turn
        .locator(':scope > .cmd-children > .cmd-block[data-block-kind="text"] .term-line')
        .allTextContents()
    ).join('\n')
    expect(text).toContain('The screen is empty.')
    expect(text).not.toContain('"sessionId"')
    expect(text).not.toContain('"window"')
  })
})

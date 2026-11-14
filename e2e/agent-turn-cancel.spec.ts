/**
 * e2e: cancelling the assistant's turn stops the tool call it is running, and
 * nothing follows it (nocx-a62cl.4.1, ADR-anntnxdj §3 "Cancel reaches the
 * call", §4 and quality scenario 3).
 *
 * WHAT THIS FILE IS FOR. Every layer under it is green in its own tests — the
 * pipeline refuses a call whose turn ended, the kernel closes the attempt as
 * the person's stop, the transport cancels the drive — and none of that is
 * evidence that a person pressing Stop on a running turn gets their terminal
 * back. The sentence this stage is judged on is one journey through the real
 * backend:
 *
 *   A person asks for something that runs a long command. The assistant
 *   proposes it, they answer the approval, the command runs — and while it is
 *   running they press Stop on the answer's own block. The block reads
 *   `stopped`, no event about that turn follows, no tool result ever reaches
 *   the model, and the next question in the same pane completes normally.
 *
 * THE CALL IS REAL AND LONG. `session.run` with `sleep 60` is proposed by the
 * scripted model; the renderer types it into the pane exactly as a person
 * would, so the backend's `run` tool is blocked inside its executor waiting for
 * a command that will not finish on its own. The Stop is pressed while that
 * block is on screen and running (`.cmd-block-running`), which is the state the
 * stage is about — a call already executing, not one still being decided.
 *
 * TWO BOUNDS, AND THEY ARE DIFFERENT ONES (ADR-anntnxdj §3, scenario 3). The
 * CALL stops within a second: the tool's own context ends the moment the turn
 * does, which is what TestAgentCancel_StopsACallBlockedInItsExecutor measures
 * on a call blocked inside its executor (measured at ~90 µs there). The BLOCK's
 * cancelled state is written after the pane program's walk, because a command
 * running under `session.run` is stopped by its run lease's INT → TERM → KILL
 * and the run terminalizes only when that stop has answered — deliberately, so
 * the block can say the command is still running where signalling is
 * unsupported. Over a helper-hosted session the walk's existence probe cannot
 * answer at all (nocx-01um8: the helper refuses signal 0), so each rung pays
 * its whole grace and the block reads cancelled in about a second. The
 * assertion below is that cost, measured and annotated, not a tidy timeout:
 * nothing else here waits out a duration, and every other wait is a poll on a
 * DOM state or a fake-provider state transition.
 *
 * "NOTHING ARRIVES AFTER" IS ASSERTED OVER A CLOSED INTERVAL. Every frame the
 * page receives is recorded in arrival order. The interval opens at the
 * turn's `cancelled` runState and closes at the NEXT turn's `completed`
 * runState, so a late delta, a late tool call or a second state for the
 * stopped run would be inside the window rather than racing an arbitrary
 * sleep. The model's own half is the same fact on the other side of the wire:
 * no chat-completions request after the stop carries a tool message, because a
 * tool result that left the tool would be the only thing that could put one
 * there.
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
const ENDPOINT_NAME = `E2E Turn Cancel ${nonce}`

/** The command the assistant proposes, and the marker the running block is
 *  found by. Sixty seconds is long enough that nothing but the person's Stop
 *  can end it inside this test. */
const COMMAND = 'sleep 60'

let backend: VaultBackend
let fake: FakeOpenAI
let endpoint: { port: number; token: string }

test.beforeAll(async () => {
  fake = new FakeOpenAI()
  await fake.start()
  const root = mkdtempSync(join(tmpdir(), 'nocx-a62cl-4-1-e2e-'))
  backend = new VaultBackend(serverBin(), { root })
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

/** Send a question through the ordinary editor after selecting the agent
 *  target — the same gesture a person makes. */
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

/** The answer block's settled state: `data-outcome`, and the word too when the
 *  outcome states one — `completed` is silent, so it asserts only the
 *  attribute. */
async function answerState(page: Page, question: string, word: string): Promise<void> {
  const outcome = word === 'completed' ? 'success' : word === 'stopped' ? 'cancelled' : 'failure'
  const block = answerBlock(page, question)
  await expect(block).toHaveAttribute('data-outcome', outcome, { timeout: 30_000 })
  if (outcome === 'success') return
  await expect(
    block.locator(':scope > .cmd-header .cmd-header-right > .ui-meta:not([data-column])'),
  ).toHaveText(word, { timeout: 30_000 })
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
 *  messages and must not count as an ask round. */
function chatRequests(fake: FakeOpenAI, from: number): FakeRequest[] {
  return fake
    .requests()
    .slice(from)
    .filter((request) => request.body.includes('"messages"')) as FakeRequest[]
}

/** The model-facing tool messages in a request body. */
function toolMessages(body: string): string[] {
  try {
    const parsed = JSON.parse(body) as { messages?: { role?: string; content?: unknown }[] }
    return (parsed.messages ?? [])
      .filter((message) => message.role === 'tool' && typeof message.content === 'string')
      .map((message) => message.content as string)
  } catch {
    return []
  }
}

interface WireEvent {
  readonly method: string
  readonly runId: number
  readonly state: string
  readonly at: number
}

/** Every frame the page receives, in arrival order: the `agent.ask` run ids
 *  the backend minted and every notification that named a run. This is the
 *  only place "nothing arrived after" can be read from. */
function recordWire(page: Page): { asks: number[]; events: WireEvent[] } {
  const asks: number[] = []
  const events: WireEvent[] = []
  page.on('websocket', (ws) => {
    ws.on('framereceived', (event) => {
      const payload = event.payload
      if (typeof payload !== 'string') return
      let frame: {
        method?: string
        params?: { runId?: number; state?: string }
        result?: { runId?: number }
      }
      try {
        frame = JSON.parse(payload) as typeof frame
      } catch {
        return
      }
      if (typeof frame.method === 'string') {
        if (typeof frame.params?.runId === 'number') {
          events.push({
            method: frame.method,
            runId: frame.params.runId,
            state: frame.params.state ?? '',
            at: Date.now(),
          })
        }
        return
      }
      if (typeof frame.result?.runId === 'number') asks.push(frame.result.runId)
    })
  })
  return { asks, events }
}

test.describe('cancelling a turn stops the call it is running (nocx-a62cl.4.1)', () => {
  test.use({ viewport: { width: 1280, height: 900 } })

  test('Stop stops a running command call, nothing follows, and the next question works', async ({
    page,
  }) => {
    test.setTimeout(180_000)
    const wire = recordWire(page)
    await openApp(page)
    await configureAssistant(page)
    await backToTerminal(page)

    // The first ask's scripted response proposes the long command; the
    // approval the default matrix asks for is answered on the prompt, so the
    // call really executes and really blocks.
    const QUESTION = `Run a long job and tell me when it ends, ${nonce}?`
    const base = fake.requests().length
    fake.setScript({
      chunks: [],
      toolCalls: [{ name: 'session.run', id: 'call_long', arguments: { command: COMMAND } }],
    })
    await askFromPrompt(page, QUESTION)

    const prompt = page.getByRole('dialog', { name: APPROVAL_TITLE })
    await expect(prompt).toBeVisible({ timeout: 60_000 })
    await expect(prompt).toContainText(COMMAND)
    // ONE ANSWER FOR ONE CALL. A standing answer is not offered for this
    // proposal (no rule can express "this sleep"), and none is needed: the
    // call runs once, which is the call the person then stops.
    await prompt.getByRole('button', { name: /^Allow once/ }).click()
    await expect(prompt).toHaveCount(0)

    // THE CALL IS RUNNING: the renderer typed the command into the pane and
    // its block is on screen, still running.
    const running = page.locator('.pane.active .cmd-block-running').filter({ hasText: COMMAND })
    await expect(running).toHaveCount(1, { timeout: 30_000 })
    await expect.poll(() => wire.asks.length, { timeout: 15_000 }).toBeGreaterThan(0)
    const runId = wire.asks[0]
    expect(runId).toBeGreaterThan(0)

    // ── THE PERSON STOPS THE ANSWER, from its own block, while the call runs.
    const answer = answerBlock(page, QUESTION)
    // THE TURN'S OWN header control: the command block inside it has one too,
    // and the child's is not the answer's Stop.
    const overflow = answer.locator(':scope > .cmd-header [data-block-actions]')
    await expect(overflow).toBeVisible()
    await overflow.click()
    const stop = page.locator('.ui-context-menu__item[data-item-id="stop"]')
    await expect(stop).toBeVisible()
    await expect(stop).toBeEnabled()
    const stopClickedAt = Date.now()
    await stop.click()

    // THE BLOCK READS CANCELLED, and the bound is the LEASE LADDER's, not a
    // tidy timeout. The call is a pane command, so agent.cancel walks the
    // lease's INT → TERM → KILL before the run's terminal state is written, and
    // over a helper-hosted session the existence probe cannot answer at all
    // (nocx-01um8: the helper refuses signal 0), so each rung pays its whole
    // grace. ADR-anntnxdj §3 accepts exactly that cost for the pane PROGRAM
    // ("it can take over two seconds"); the CALL's own one-second bound is the
    // Go test's (TestAgentCancel_StopsACallBlockedInItsExecutor), which
    // measures the executor's context directly.
    await expect(answer).toHaveAttribute('data-outcome', 'cancelled', { timeout: 2000 })
    await expect(
      answer.locator(':scope > .cmd-header .cmd-header-right > .ui-meta:not([data-column])'),
    ).toHaveText('stopped', { timeout: 2000 })

    // The command itself stops by the lease's own walk, so the pane is usable
    // again — the block that was running is no longer running.
    await expect(
      page.locator('.pane.active .cmd-block-running').filter({ hasText: COMMAND }),
    ).toHaveCount(0, {
      timeout: 10_000,
    })

    // ── THE NEXT QUESTION IN THE SAME PANE WORKS. This is also the end of the
    //    observation window: every frame between the stop and this turn's
    //    completion has been recorded.
    const NEXT = `Now answer this normally, ${nonce}?`
    fake.setScript({ chunks: ['The next answer completed normally.'] })
    await askFromPrompt(page, NEXT)
    await answerState(page, NEXT, 'completed')
    await expect(answerBlock(page, NEXT).locator('.cmd-output[data-answer-body]')).toContainText(
      'The next answer completed normally.',
    )

    // ── NOTHING FOLLOWED THE STOP, over the closed interval.
    const stoppedAt = wire.events.findIndex(
      (event) =>
        event.method === 'agent.runState' && event.runId === runId && event.state === 'cancelled',
    )
    expect(stoppedAt, 'the stopped run published no cancelled state').toBeGreaterThanOrEqual(0)
    // The measured cost, so a reader of the run knows what the ladder bought
    // rather than guessing from a passing test.
    test.info().annotations.push({
      type: 'cancel-latency',
      description: `Stop → cancelled runState: ${wire.events[stoppedAt].at - stopClickedAt} ms`,
    })
    const nextRunId = wire.asks[wire.asks.length - 1]
    const closedAt = wire.events.findIndex(
      (event) =>
        event.method === 'agent.runState' &&
        event.runId === nextRunId &&
        event.state === 'completed',
    )
    expect(closedAt, 'the next turn never completed').toBeGreaterThan(stoppedAt)
    const after = wire.events
      .slice(stoppedAt + 1, closedAt)
      .filter((event) => event.runId === runId)
    expect(
      after,
      `events for the stopped run after its cancelled state: ${JSON.stringify(after)}`,
    ).toEqual([])

    // ── NO TOOL RESULT REACHED THE MODEL. The stopped turn's call is the only
    //    thing that could have produced one, and a request carrying a tool
    //    message is that result leaving.
    const requests = chatRequests(fake, base)
    const withToolResults = requests.filter((request) => toolMessages(request.body).length > 0)
    expect(
      withToolResults.map((request) => request.body),
      'a tool result was sent to the model after the person stopped the turn',
    ).toEqual([])
    // One model round for the stopped turn (the proposal) and one for the
    // next question: the stop ended the turn rather than resuming it.
    expect(requests).toHaveLength(2)
  })
})

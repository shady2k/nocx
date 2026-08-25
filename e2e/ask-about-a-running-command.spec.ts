/**
 * e2e: a person asks the assistant about a command that is STILL RUNNING,
 * and gets an answer while it runs (nocx-92gfl).
 *
 * THIS IS THE SENTENCE THE BEAD EXISTS FOR, and it is the one thing no unit
 * test can report. The units around it are green either way: the summon is a
 * flag and a visibility call, the ask is an RPC, the tool call is a broker
 * round trip. What none of them can say is that the whole chain holds while
 * a command has the pty — that the editor comes back at all, that the
 * question reaches the model without the busy shell being in the way, and
 * that the answer is written from what is on the screen AT THAT MOMENT
 * rather than from a transcript of something finished.
 *
 * Why it needs so little product, and why that is the point: asking is not a
 * shell command. `agent.ask` travels the control plane and never touches the
 * pty, so a busy shell is no obstacle to it — the only thing in the way was
 * that the editor is hidden while a command runs, with no way to bring it
 * back. ⌘/Ctrl+Enter is that way, and this watches a person use it.
 *
 * THE ANSWER IS DERIVED, NOT SCRIPTED. The fake model's second response is a
 * function of the request body it is answering (fake-openai.ts StreamScript
 * chunks), so it can only name the marker if the marker actually reached the
 * model in the tool result. A fixed script would say it because this file
 * typed it into the fixture, which would be a test of the fixture.
 *
 * WHAT IS ON THE SCREEN, AND WHY IT STAYS THERE. Unlike
 * agent-reads-the-screen.spec.ts — which had to arrange for output to arrive
 * AFTER a freeze, because a finished command's rows leave the grid at its
 * freeze — this spec's command has not finished. Its output is on the live
 * grid because it is live. The job prints the marker, raises an OSC 9, and
 * then blocks on a file this spec creates only at the end, so:
 *
 *  - THE MARKER IS ON THE GRID BEFORE THE ASK. The OSC 9 follows the marker
 *    on the same byte stream through the same parser, so the notify.raise
 *    the renderer sends for it cannot precede the cells it follows. The
 *    raise is the signal — an ordering fact, not a duration.
 *  - THE COMMAND IS STILL RUNNING AFTER THE ANSWER. Nothing can end it but
 *    the flag file, and this spec does not write one until every assertion
 *    has been made. So "still running" is a fact about the fixture, not a
 *    race this test happens to win.
 *
 * Nothing here sleeps and nothing waits out a clock (AGENTS.md: "a test may
 * not depend on timing").
 */
import { test as base, expect, type Page } from '@playwright/test'
import { mkdtempSync, writeFileSync } from 'node:fs'
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
const GRID = '.pane.active .xterm-live-container'
const RUNNING_BLOCK = '.pane.active .cmd-block.cmd-block-running'
const SETTINGS_AI_NAV = '.ui-grouped-nav__item[data-item="endpoints"]'
const SETTINGS_ROLES_NAV = '.ui-grouped-nav__item[data-item="roles"]'
const SETTINGS_POLICY_NAV = '.ui-grouped-nav__item[data-item="policy"]'
const OBSERVE_ROW = '.st-policy__row[data-effect="observe"]'

const test = base
const nonce = Date.now().toString(36)

/** The string that exists in exactly one place in this run: on that pane's
 *  live screen, put there by a command that has not finished. It is never
 *  typed into a question, so the only route by which it can reach the model
 *  is a frame the renderer captured while the command was running. */
const MARKER = `NOCX-RUNNING-${nonce}`
/** The OSC 9 body the job writes AFTER the marker — the ordering signal that
 *  says the marker is on the grid. Never a delay. */
const SIGNAL = `running-ready-${nonce}`
const ENDPOINT_NAME = `E2E Ask While Running ${nonce}`

let backend: VaultBackend
let fake: FakeOpenAI
let endpoint: { port: number; token: string }
let fixtureDir = ''
let scriptPath = ''
let flagPath = ''

test.beforeAll(async () => {
  fake = new FakeOpenAI()
  await fake.start()
  const root = mkdtempSync(join(tmpdir(), 'nocx-92gfl-e2e-'))
  backend = new VaultBackend(devharnessBin(), { root }, true)
  endpoint = await backend.start()

  fixtureDir = mkdtempSync(join(tmpdir(), 'nocx-92gfl-job-'))
  scriptPath = join(fixtureDir, 'slow-job.sh')
  flagPath = join(fixtureDir, 'finish')
  // POSIX sh, deliberately dull, and run in the FOREGROUND: this is the
  // command the person is asking about, so it must own the pty.
  //
  // The marker is printed with NO trailing newline so the cursor stays on
  // its row — a captured frame is the window around the cursor, so a row the
  // cursor is on is a row the frame contains, whatever the live region's
  // height happens to be.
  //
  // The wait is a CONDITION, not a delay: it ends when this spec creates the
  // file, which it does only after every assertion.
  writeFileSync(
    scriptPath,
    [
      `printf '%s' '${MARKER}'`,
      `printf '\\033]9;%s\\007' '${SIGNAL}'`,
      `while [ ! -e '${flagPath}' ]; do sleep 1; done`,
      '',
    ].join('\n'),
  )
})

test.afterAll(async () => {
  // Let the job go even if a test failed before releasing it, so the shell
  // is not left with a foreground process for the backend's teardown.
  writeFileSync(flagPath, 'go\n')
  backend?.stop()
  await fake?.stop()
})

/** One frame of the renderer's own answer to a readScreen request — the
 *  evidence a Go test cannot produce, because there the renderer is the
 *  test. */
interface ResolvedCell {
  char?: string
}
interface ResolvedRow {
  cells?: ResolvedCell[]
}
interface ResolvedFrame {
  requestId?: string
  outcome?: string
  error?: string
  rows?: ResolvedRow[]
}

interface SpecRecord {
  asks: string[]
  raised: string[]
  resolved: ResolvedFrame[]
}

declare global {
  interface Window {
    __nocxRunningAskSpec?: SpecRecord
  }
}

/**
 * Record the three frames this spec reads, off the app's OWN socket — the
 * same technique agent-reads-the-screen.spec.ts and notification-osc.spec.ts
 * use, and for the same reason: what is observed is the real client's
 * traffic, never a fixture the spec built.
 */
async function recordFrames(page: Page): Promise<void> {
  await page.addInitScript(() => {
    window.__nocxRunningAskSpec = { asks: [], raised: [], resolved: [] }
    const send = WebSocket.prototype.send
    WebSocket.prototype.send = function (this: WebSocket, data: Parameters<typeof send>[0]) {
      if (typeof data === 'string') {
        try {
          const msg = JSON.parse(data) as { method?: string; params?: Record<string, unknown> }
          const rec = window.__nocxRunningAskSpec!
          if (msg.method === 'agent.ask' && typeof msg.params?.sessionId === 'string') {
            rec.asks.push(msg.params.sessionId)
          }
          if (msg.method === 'notify.raise' && typeof msg.params?.body === 'string') {
            rec.raised.push(msg.params.body)
          }
          if (msg.method === 'agent.readScreenResolved' && msg.params) {
            rec.resolved.push(msg.params as ResolvedFrame)
          }
        } catch {
          // Not JSON, or not ours. The data plane is binary and never lands
          // here.
        }
      }
      return send.call(this, data)
    }
  })
}

async function recorded(page: Page): Promise<SpecRecord> {
  return page.evaluate(() => window.__nocxRunningAskSpec ?? { asks: [], raised: [], resolved: [] })
}

/** The frame's text as a person would read it: rows of cells, joined. */
function frameText(frame: ResolvedFrame): string {
  return (frame.rows ?? [])
    .map((row) => (row.cells ?? []).map((c) => c.char ?? '').join(''))
    .join('\n')
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

/** The mode indicator, which is the product's own account of where Enter
 *  goes. `:visible` because CM6 keeps a hidden measurement spacer carrying an
 *  identical button (agent-ask.spec.ts records this). */
function modeIndicator(page: Page) {
  return page.locator('.pane.active .ui-mode-indicator:visible')
}

async function useTarget(page: Page, target: 'shell' | 'agent'): Promise<void> {
  const input = page.locator(INPUT)
  await input.click()
  const indicator = modeIndicator(page)
  if ((await indicator.getAttribute('data-target')) !== target) {
    await page.keyboard.press('ControlOrMeta+Enter')
    await expect(indicator).toHaveAttribute('data-target', target, { timeout: 10_000 })
  }
}

function answerBlock(page: Page, question: string) {
  return page.locator('.cmd-block').filter({ hasText: question })
}

async function answerFinished(page: Page, question: string): Promise<void> {
  await expect(answerBlock(page, question).locator('.cmd-header-exit')).toHaveText('completed', {
    timeout: 30_000,
  })
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
  // The `observe` row is set to Allowed through the page a person uses, so
  // the proposed session.read EXECUTES rather than suspending on an approval.
  // The asking is agent-policy.spec.ts's subject, not this file's.
  await page.locator(SETTINGS_POLICY_NAV).click()
  const observeRow = page.locator(OBSERVE_ROW)
  await expect(observeRow).toBeVisible({ timeout: 15_000 })
  await observeRow.locator('select').first().selectOption({ label: 'Allowed' })
  await expect(observeRow.locator('.st-policy__state')).toContainText('Allowed', {
    timeout: 15_000,
  })
}

/**
 * The answer the fake model writes FROM the request it is answering.
 *
 * It looks for the marker only in what the PRODUCT told the model — every
 * message that is not the system prompt and not the person's own words — so
 * a marker found there arrived in a tool result and nowhere else.
 */
function answerFromWhatTheModelWasSent(body: string): string[] {
  let seen = ''
  try {
    const parsed = JSON.parse(body) as { messages?: { role?: string; content?: string }[] }
    seen = (parsed.messages ?? [])
      .filter((m) => m.role !== 'system' && m.role !== 'user')
      .map((m) => m.content ?? '')
      .join('\n')
  } catch {
    seen = ''
  }
  return seen.includes(MARKER)
    ? ['The command is showing ', MARKER, '.']
    : ['I could not see the screen.']
}

test.describe('asking about a command that is still running (nocx-92gfl)', () => {
  test.use({ viewport: { width: 1280, height: 900 } })

  test('⌘/Ctrl+Enter summons the editor over a running command, and the answer names what is on its screen', async ({
    page,
  }) => {
    // Vault setup, an endpoint, a role, a policy row, a real shell command
    // and two asks — each a wait on an observable state, none of them a
    // clock. The number is a hang detector for a run that has already gone
    // wrong, never an expectation about how fast the machine is.
    test.setTimeout(120_000)
    await recordFrames(page)
    await openApp(page)
    await configureAssistant(page)
    await backToTerminal(page)

    // ── The session the question is asked in ─────────────────────────────
    // Learned from the product's own frame, never invented: session.read's
    // sessionId is matched for exact identity against the run's grant, so an
    // invented id is refused before the call can run at all.
    const FIRST = 'Are you there?'
    await useTarget(page, 'agent')
    await page.locator(INPUT).fill(FIRST)
    await page.keyboard.press('Enter')
    await answerFinished(page, FIRST)
    await expect.poll(async () => (await recorded(page)).asks.length).toBeGreaterThan(0)
    const sessionId = (await recorded(page)).asks.at(-1)!
    expect(sessionId).not.toBe('')

    // ── Start the command, and leave it running ──────────────────────────
    await useTarget(page, 'shell')
    await page.keyboard.type(`sh ${scriptPath}`)
    await page.keyboard.press('Enter')

    // Two facts, and neither is a duration. The block is running, so the
    // pane has an execution; and the marker's OSC 9 has arrived, so the
    // marker itself is already on the grid ahead of it.
    await expect(page.locator(RUNNING_BLOCK)).toHaveCount(1, { timeout: 20_000 })
    await expect
      .poll(async () => (await recorded(page)).raised.includes(SIGNAL), {
        timeout: 45_000,
        message: 'the marker never reached the terminal (no OSC 9 followed it)',
      })
      .toBe(true)

    // THE STATE THE BEAD IS ABOUT: the editor is gone, and until this change
    // there was nothing a person could do about it.
    await expect(page.locator(INPUT)).toBeHidden()

    // ── The summon ───────────────────────────────────────────────────────
    // Pressed where a person presses it: on the grid, which is what holds
    // the keyboard while a command runs.
    await page.locator(GRID).click()
    await page.keyboard.press('ControlOrMeta+Enter')
    await expect(page.locator(INPUT)).toBeVisible({ timeout: 10_000 })
    // In ASK mode, and that is the whole of "no second command can be
    // started over a running one": the shell target is not reachable from
    // here, so there is no state in which Enter would run something.
    await expect(modeIndicator(page)).toHaveAttribute('data-target', 'agent', { timeout: 10_000 })

    // ── The question, while it is still running ──────────────────────────
    // Two model responses, because a real tool-calling run is two: the
    // proposal, then the answer written from the result. The second is
    // DERIVED — it can only name the marker if the marker reached the model.
    // NO ITEM ID, and that is the whole point of this file: `session.read`
    // without one is "the screen now" (contracts/tools/session.read.schema
    // .json), which for a RUNNING command is its live grid rather than a
    // transcript of something finished.
    fake.setScript({ chunks: [], toolCalls: [{ name: 'session.read', arguments: { sessionId } }] })
    fake.setScript({ chunks: answerFromWhatTheModelWasSent })

    const QUESTION = 'What is this command showing right now?'
    expect(QUESTION).not.toContain(MARKER)
    const firstRequestOfTheRun = fake.requests().length
    await page.locator(INPUT).fill(QUESTION)
    await page.keyboard.press('Enter')
    await answerFinished(page, QUESTION)

    const body = answerBlock(page, QUESTION).locator('[data-answer-body]')

    // 1. THE ANSWER A PERSON READS names what is on the screen of the
    //    command they asked about. This is the bead's sentence.
    const answer = (await body.locator('.term-line').allTextContents()).join('\n')
    expect(answer).toContain(MARKER)

    // 2. AND THE COMMAND IS STILL RUNNING. Asserted after the answer, which
    //    is the whole difference between this and asking about a finished
    //    command: nothing can end this job but the flag file below, and it
    //    does not exist yet.
    await expect(page.locator(RUNNING_BLOCK)).toHaveCount(1)

    // 3. It got there by READING THE SCREEN, and the frame was the real
    //    renderer's — the app's own answer to the broker, off its own
    //    socket, carrying the marker in its cells.
    // Since ADR-0040 the call is a `tool` CHILD of the turn, named by the
    // tool and the arguments it ran on (scrollback/tool-call-title.ts).
    const call = answerBlock(page, QUESTION).locator(
      ':scope > .cmd-children > .cmd-block[data-block-kind="tool"]',
    )
    await expect(call).toHaveCount(1)
    await expect(call).toHaveAttribute('data-tool', 'session.read')
    await expect(call.locator('.cmd-header-text')).toContainText('session.read')
    const { resolved } = await recorded(page)
    expect(resolved.length).toBe(1)
    const frame = resolved[0]
    expect(frame.outcome, frame.error ?? 'the renderer reported no error').toBe('frame')
    expect(frameText(frame)).toContain(MARKER)

    // 4. And the model was told the marker by the TOOL and by nothing else,
    //    which is what makes the derived answer above evidence rather than
    //    an echo of the fixture. The first request of this run carries the
    //    system prompt and the person's question; the second adds the tool
    //    result.
    const runBodies = fake
      .requests()
      .slice(firstRequestOfTheRun)
      // Chat requests only: the endpoint's connection check is a GET to
      // /models with an empty body and is recorded like any other request.
      .filter((r) => r.body.includes('"messages"'))
      .map((r) => r.body)
    expect(runBodies.length).toBeGreaterThanOrEqual(2)
    expect(runBodies[0]).not.toContain(MARKER)
    expect(runBodies[1]).toContain(MARKER)

    // 5. And Escape gives the keys back to the process: the job ends because
    //    the file appears, and the block freezes — which it cannot do while
    //    the editor owns input and the grid is read-only.
    await page.keyboard.press('Escape')
    await expect(page.locator(INPUT)).toBeHidden({ timeout: 10_000 })
    writeFileSync(flagPath, 'go\n')
    await expect(page.locator(RUNNING_BLOCK)).toHaveCount(0, { timeout: 30_000 })
    await expect(page.locator(INPUT)).toBeVisible({ timeout: 20_000 })
    // The prompt is back and it is the SHELL's again: the summon is spent,
    // and the empty draft is what allows the target to go back.
    await expect(modeIndicator(page)).toHaveAttribute('data-target', 'shell', { timeout: 10_000 })
  })
})

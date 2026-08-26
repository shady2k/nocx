/**
 * e2e: a person asks the assistant to look at THEIR screen, and the answer
 * names what was on it (nocx-z4hgm, closing nocx-avogl).
 *
 * WHAT THIS FILE IS FOR, AND WHY THE GO TEST BESIDE IT IS NOT ENOUGH.
 * internal/transport/ws_agent_locates_itself_test.go already watches the
 * whole loop over the real socket: the model reads the session id out of the
 * system prompt, calls session.read with it, and the answer reaches the
 * block.
 * But in that test THE RENDERER IS THE TEST — it answers
 * agent.readScreenRequest with a frame it built itself, because a Go test
 * has no grid. So the one thing it cannot report is the one thing the epic's
 * sentence is about: that the frame a person's own pane produces is what the
 * assistant reads.
 *
 * That half is here. The grid is a real xterm on a real PTY, the frame is
 * minted by frontend/src/renderers/xterm.ts captureLiveFrame, and the marker
 * the answer names exists in exactly one place in this run — on that pane's
 * screen (AGENTS.md testing rules 1 and 2).
 *
 * THE ANSWER IS DERIVED, NOT SCRIPTED. The fake model's second response is a
 * function of the request body it is answering (fake-openai.ts StreamScript
 * chunks), so it can only say the marker if the marker actually reached the
 * model in the tool result. A fixed script would say it because this file
 * typed it into the fixture, which would be a test of the fixture.
 *
 * PUTTING SOMETHING ON THE SCREEN IS NOT FREE, and the reason is worth
 * writing down because it is the whole shape of this spec. An idle nocx pane
 * has a BLANK grid: the prompt is suppressed to an invisible marker while
 * the line editor owns the line (internal/shellintegration/scripts/nocx.bash
 * sets PS1 to the B marker alone), and every finished command's rows leave
 * the grid at its freeze — scrollback/controller.ts _clearFrozenRows calls
 * clearViewport so the DOM block owns them and nothing is drawn twice
 * (nocx-m87n). So "run a command and ask about its output" cannot work: by
 * the time the editor is back, the output is DOM and the screen is empty.
 *
 * What CAN be on the screen is output that arrives after the last freeze,
 * from something the shell is no longer waiting on. So a background job
 * prints the marker — and the two orderings that could make this a timing
 * bet are both closed by an observable:
 *
 * - IT MUST PRINT AFTER THE CLEAR, or the clear wipes it. The job waits for
 *   a file this spec creates, and this spec creates it only once the block
 *   has frozen and the editor is back — and those are the same event:
 *   _settleFrozen runs clearViewport and setIdle in ONE callback, so an
 *   editor that has the line again is an editor whose clear has happened.
 * - THE ASK MUST HAPPEN AFTER IT LANDS, or the read returns a blank grid.
 *   The job writes the marker and then an OSC 9, on the same byte stream,
 *   through the same parser: the notify.raise the renderer sends for that
 *   OSC cannot precede the cells it follows. So the raise is the signal that
 *   the marker is on the grid — an ordering fact, not a duration.
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
const SETTINGS_AI_NAV = '.ui-grouped-nav__item[data-item="endpoints"]'
const SETTINGS_ROLES_NAV = '.ui-grouped-nav__item[data-item="roles"]'
const SETTINGS_POLICY_NAV = '.ui-grouped-nav__item[data-item="policy"]'
const OBSERVE_ROW = '.st-policy__row[data-effect="observe"]'

const test = base
const nonce = Date.now().toString(36)

/** The string that exists in exactly one place in this run: on that pane's
 *  screen. It is never typed into a question, so the only route by which it
 *  can reach the model is a frame the renderer captured. */
const MARKER = `NOCX-SCREEN-${nonce}`
/** The OSC 9 body the background job writes AFTER the marker — the ordering
 *  signal, never a delay. */
const SIGNAL = `screen-ready-${nonce}`
const ENDPOINT_NAME = `E2E Read Screen ${nonce}`

let backend: VaultBackend
let fake: FakeOpenAI
let endpoint: { port: number; token: string }
/** Where the background job and its release flag live. Its own mkdtemp, not
 *  the backend's disposable root: the root is the isolated HOME's parent and
 *  nothing else should be writing into it. */
let fixtureDir = ''
let scriptPath = ''
let flagPath = ''

test.beforeAll(async () => {
  fake = new FakeOpenAI()
  await fake.start()
  const root = mkdtempSync(join(tmpdir(), 'nocx-z4hgm-e2e-'))
  backend = new VaultBackend(devharnessBin(), { root }, true)
  endpoint = await backend.start()

  fixtureDir = mkdtempSync(join(tmpdir(), 'nocx-z4hgm-screen-'))
  scriptPath = join(fixtureDir, 'put-marker-on-screen.sh')
  flagPath = join(fixtureDir, 'release')
  // POSIX sh, and deliberately dull. The wait is a CONDITION, not a delay:
  // it ends when this spec creates the file, which it does only after the
  // grid has been cleared. The marker is printed with NO trailing newline so
  // the cursor stays on its row — a captured frame is the window around the
  // cursor, so a row the cursor is on is a row the frame contains, whatever
  // the live region's height happens to be.
  writeFileSync(
    scriptPath,
    [
      `while [ ! -e '${flagPath}' ]; do sleep 1; done`,
      `printf '%s' '${MARKER}'`,
      `printf '\\033]9;%s\\007' '${SIGNAL}'`,
      '',
    ].join('\n'),
  )
})

test.afterAll(async () => {
  backend?.stop()
  await fake?.stop()
})

/** One frame of the renderer's own answer to a readScreen request — the
 *  thing this spec exists to observe. */
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
    __nocxScreenSpec?: SpecRecord
  }
}

/**
 * Record the three frames this spec reads, off the app's OWN socket.
 *
 * Wrapping send rather than reading a transcript keeps it honest, exactly as
 * notification-osc.spec.ts records notify.raise: what is observed is the real
 * client's traffic, never a fixture the spec built. Installed as an init
 * script so no frame is missed.
 *
 * - agent.ask carries the session the question was asked in, which is the id
 *   a scripted session.read must name (an invented one is refused for identity
 *   before the call can run — fake-openai.ts records why at length).
 * - notify.raise is the OSC 9 the background job writes after the marker.
 * - agent.readScreenResolved is the RENDERER'S FRAME. It is the evidence the
 *   Go test cannot produce.
 */
async function recordFrames(page: Page): Promise<void> {
  await page.addInitScript(() => {
    window.__nocxScreenSpec = { asks: [], raised: [], resolved: [] }
    const send = WebSocket.prototype.send
    WebSocket.prototype.send = function (this: WebSocket, data: Parameters<typeof send>[0]) {
      if (typeof data === 'string') {
        try {
          const msg = JSON.parse(data) as {
            method?: string
            params?: Record<string, unknown>
          }
          const rec = window.__nocxScreenSpec!
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
  return page.evaluate(() => window.__nocxScreenSpec ?? { asks: [], raised: [], resolved: [] })
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

/** ⌘/Ctrl+Enter FLIPS where Enter goes, so it is the way to both targets.
 *  Idempotent: with the wanted target already active, nothing is pressed. */
async function useTarget(page: Page, target: 'shell' | 'agent'): Promise<void> {
  const input = page.locator(INPUT)
  await input.click()
  const indicator = modeIndicator(page)
  if ((await indicator.getAttribute('data-target')) !== target) {
    await page.keyboard.press('ControlOrMeta+Enter')
    await expect(indicator).toHaveAttribute('data-target', target, { timeout: 10_000 })
  }
}

async function askFromPrompt(page: Page, question: string): Promise<void> {
  await useTarget(page, 'agent')
  await page.locator(INPUT).fill(question)
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
 * a marker found there arrived in a tool result and nowhere else. Not found:
 * the model says so, and this spec's assertion on the answer text is what
 * turns that into a red run with the reason on it.
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
    ? ['The screen shows ', MARKER, '.']
    : ['I could not see the screen.']
}

test.describe('the assistant reads the screen of the pane it was asked in (nocx-avogl)', () => {
  test.use({ viewport: { width: 1280, height: 900 } })

  test('the answer names what was on that pane’s screen, and nothing else could have told it', async ({
    page,
  }) => {
    // Vault setup, an endpoint, a role, a policy row, a real shell command
    // and two asks — each of them a wait on an observable state, none of
    // them a clock. The number is a hang detector for a run that has
    // already gone wrong, never an expectation about how fast the machine
    // is. (Inside the test, which is the only place Playwright accepts it.)
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
    await askFromPrompt(page, FIRST)
    await answerFinished(page, FIRST)
    await expect.poll(async () => (await recorded(page)).asks.length).toBeGreaterThan(0)
    const asks = (await recorded(page)).asks
    const sessionId = asks[asks.length - 1]
    expect(sessionId).not.toBe('')

    // ── Put the marker on that pane's screen ─────────────────────────────
    // The job is started and then WAITS; it prints nothing until this spec
    // releases it, which is only after the grid has been cleared.
    await useTarget(page, 'shell')
    await page.keyboard.type(`sh ${scriptPath} &`)
    await page.keyboard.press('Enter')

    // The block froze — and with it the grid was cleared, because
    // _settleFrozen does both in one callback: no block is still running,
    // and the editor has the line back.
    await expect(
      page
        .locator('.pane.active .cmd-block')
        .filter({ hasText: 'put-marker-on-screen.sh' })
        .first(),
    ).toBeVisible({ timeout: 20_000 })
    await expect(page.locator('.pane.active .cmd-block.cmd-block-running')).toHaveCount(0, {
      timeout: 20_000,
    })
    await expect(page.locator(INPUT)).toBeVisible({ timeout: 10_000 })
    await expect(page.locator(INPUT)).toBeFocused({ timeout: 10_000 })

    // Release it. The marker is written first and the OSC 9 after it, on one
    // byte stream through one parser, so the raise below cannot arrive
    // before the cells it follows.
    writeFileSync(flagPath, 'go\n')
    await expect
      .poll(async () => (await recorded(page)).raised.includes(SIGNAL), {
        timeout: 45_000,
        message: 'the marker never reached the terminal (no OSC 9 followed it)',
      })
      .toBe(true)

    // ── The question ─────────────────────────────────────────────────────
    // Two model responses, because a real tool-calling run is two: the
    // proposal, then the answer written from the result. The second is
    // DERIVED — it can only name the marker if the marker reached the model.
    // `session.read` with no item id is "the screen now" (contracts/tools/
    // session.read.schema.json) — the tool that replaced the deleted screen
    // reader in nocx-2ryxf.1.
    fake.setScript({ chunks: [], toolCalls: [{ name: 'session.read', arguments: { sessionId } }] })
    fake.setScript({ chunks: answerFromWhatTheModelWasSent })

    const QUESTION = 'What is on my screen right now?'
    expect(QUESTION).not.toContain(MARKER)
    // Where this run's requests start, so assertion 4 below can name the two
    // that belong to it rather than counting across the whole file.
    const firstRequestOfTheRun = fake.requests().length
    await askFromPrompt(page, QUESTION)
    await answerFinished(page, QUESTION)

    const body = answerBlock(page, QUESTION).locator('[data-answer-body]')

    // 1. THE ANSWER A PERSON READS names what was on their screen. This is
    //    the epic's sentence, and it is the assertion the whole file is for.
    const answer = (await body.locator('.term-line').allTextContents()).join('\n')
    expect(answer).toContain(MARKER)

    // 2. It got there by READING THE SCREEN, and the call is visible where
    //    it happened, aimed at this pane's session.
    // Since ADR-0040 the call is a `tool` CHILD of the turn — the block is
    // the account of the call — named by the tool and the arguments it ran
    // on (scrollback/tool-call-title.ts).
    const call = answerBlock(page, QUESTION).locator(
      ':scope > .cmd-children > .cmd-block[data-block-kind="tool"]',
    )
    await expect(call).toHaveCount(1)
    await expect(call).toHaveAttribute('data-tool', 'session.read')
    // The session is the PANE'S OWN NAME, never the id (nocx-vnzek): a
    // 32-hex handle says nothing to a person. Asserted against the tab
    // strip's own text, because that is the point — one derivation for "what
    // is this session called", read by both surfaces, so they cannot drift
    // apart the way two copies would.
    const title = call.locator('.cmd-header-text')
    await expect(title).toContainText('session.read')
    await expect(title).not.toContainText(sessionId)
    await expect(title).toContainText(await page.locator(TITLE).first().innerText())

    // 3. AND THE FRAME WAS THE REAL RENDERER'S — the half the Go test
    //    (ws_agent_locates_itself_test.go) cannot report, because there the
    //    renderer is the test. This is the app's own answer to the broker,
    //    off its own socket: a frame outcome for this session, carrying the
    //    marker in its cells.
    const { resolved } = await recorded(page)
    expect(resolved.length).toBe(1)
    const frame = resolved[0]
    expect(frame.outcome, frame.error ?? 'the renderer reported no error').toBe('frame')
    expect(frame.requestId).toBeTruthy()
    expect(frameText(frame)).toContain(MARKER)

    // 4. And the model was told the marker by the TOOL and by nothing else.
    //    This run sent two requests: the first carries the system prompt and
    //    the person's question, the second adds the tool result. The marker
    //    is absent from the first and present in the second — which is what
    //    makes the derived answer above evidence rather than an echo of the
    //    fixture.
    const runBodies = fake
      .requests()
      .slice(firstRequestOfTheRun)
      // Chat requests only. The endpoint's connection check is a GET to
      // /models with an empty body and is recorded like any other request;
      // counting it here would shift the pair by one.
      .filter((r) => r.body.includes('"messages"'))
      .map((r) => r.body)
    expect(runBodies.length).toBeGreaterThanOrEqual(2)
    expect(runBodies[0]).not.toContain(MARKER)
    expect(runBodies[1]).toContain(MARKER)
  })
})

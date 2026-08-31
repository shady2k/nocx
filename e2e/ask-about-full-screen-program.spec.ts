/**
 * e2e: a person asks about a full-screen program without leaving it
 * (nocx-7l4ex.5/.6, nocx-92gfl.4).
 *
 * This is deliberately a real alternate-buffer journey, not the running normal
 * buffer case in ask-about-a-running-command.spec.ts. The fixture owns a pty,
 * enters the alternate screen, and records SIGWINCH so a resize cannot hide in
 * a screenshot. It also writes a second marker only after the summoned frame
 * is pinned. That marker must be absent from the answer's frame and become
 * visible in the live grid after Escape thaws it. The first terminal answer
 * returns the same composer; a held follow-up proves both answers remain in a
 * non-overlapping ordered stack before Escape and seat once after normal exit.
 * A second real-PTY act covers the incident boundary: Escape explicitly
 * focuses the live grid so q cannot disappear with the hidden composer, and
 * Stop captures lifecycle, block, presentation and signal outcome together.
 *
 * The first answer is derived from the model request. A fixed response could repeat a
 * marker typed into this fixture and would prove only the fixture. The answer
 * names the initial marker only when the renderer's pinned frame reached the
 * model, and it refuses to name the later marker when the frame was captured
 * before that output existed.
 *
 * Every wait below observes a DOM state, a fake-provider state, an OSC signal,
 * or a fixture file. The shell's wait loops are gates opened by this spec, not
 * elapsed-time assertions.
 */
import { test as base, expect, type Page } from '@playwright/test'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import {
  appReadyForInput,
  VaultBackend,
  bindEndpoint,
  createAiEndpoint,
  promptReady,
  setDefaultModel,
  settingsReady,
} from './harness'
import { readStand } from './stand'
import { FakeOpenAI } from './fake-openai'

const serverBin = () => readStand().server

const TITLE = '.nocx-tab-title'
const INPUT = '.pane.active .nocx-editor-input'
const GRID = '.pane.active .xterm-live-container'
const FREEZE = '.pane.active .nocx-freeze-frame'
const RUNNING_BLOCK = '.pane.active .cmd-block.cmd-block-running'
const SETTINGS_AI_NAV = '.ui-grouped-nav__item[data-item="endpoints"]'
const SETTINGS_ROLES_NAV = '.ui-grouped-nav__item[data-item="roles"]'
const SETTINGS_POLICY_NAV = '.ui-grouped-nav__item[data-item="policy"]'
const OBSERVE_ROW = '.st-policy__row[data-effect="observe"]'

const test = base
const nonce = Date.now().toString(36)
const INITIAL = `NOCX-FULLSCREEN-${nonce}`
const DURING = `NOCX-DURING-FREEZE-${nonce}`
const READY = `fullscreen-ready-${nonce}`
const ARMED = `fullscreen-armed-${nonce}`
const DURING_READY = `during-freeze-ready-${nonce}`
const ENDPOINT_NAME = `E2E Full Screen Ask ${nonce}`
const KEY_READY = `fullscreen-key-ready-${nonce}`
const KEY_QUESTION = 'Can I still control this full-screen program?'

let backend: VaultBackend
let fake: FakeOpenAI
let endpoint: { port: number; token: string }
let fixtureDir = ''
let scriptPath = ''
let probePath = ''
let probeResultPath = ''
let armPath = ''
let summonPath = ''
let summonRequestPath = ''
let baselinePath = ''
let exitPath = ''
let resizePath = ''
let baselineSizePath = ''
let summonedSizePath = ''
let finalSizePath = ''
let keyScriptPath = ''
let keyResultPath = ''

function fileText(path: string): string {
  try {
    return readFileSync(path, 'utf8')
  } catch {
    return ''
  }
}

test.beforeAll(async () => {
  fake = new FakeOpenAI()
  await fake.start()
  const root = mkdtempSync(join(tmpdir(), 'nocx-7l4ex-e2e-'))
  backend = new VaultBackend(serverBin(), { root })
  fixtureDir = mkdtempSync(join(tmpdir(), 'nocx-7l4ex-fullscreen-'))
  endpoint = await backend.start()

  scriptPath = join(fixtureDir, 'fullscreen-job.sh')
  probePath = join(fixtureDir, 'probe')
  probeResultPath = join(fixtureDir, 'probe-result')
  armPath = join(fixtureDir, 'arm')
  summonPath = join(fixtureDir, 'summon')
  summonRequestPath = join(fixtureDir, 'summon-request')
  baselinePath = join(fixtureDir, 'resize-baseline')
  exitPath = join(fixtureDir, 'exit')
  resizePath = join(fixtureDir, 'sigwinch')
  baselineSizePath = join(fixtureDir, 'baseline-size')
  summonedSizePath = join(fixtureDir, 'summoned-size')
  finalSizePath = join(fixtureDir, 'final-size')
  keyScriptPath = join(fixtureDir, 'fullscreen-key-job.sh')
  keyResultPath = join(fixtureDir, 'fullscreen-key-result')
  writeFileSync(resizePath, '')

  // This is a small full-screen program, not a normal-buffer command. It
  // enters the alternate buffer, waits until the test has a real summoned
  // overlay, and then writes output while that overlay is pinned. A trap makes
  // an accidental PTY resize observable instead of inferred from the DOM.
  writeFileSync(
    scriptPath,
    [
      `trap 'printf "%s\n" WINCH >> ${resizePath}' WINCH`,
      `printf '\\033[?1049h'`,
      `printf '%s' '${INITIAL}'`,
      `printf '\\033]9;%s\\007' '${READY}'`,
      `while [ ! -e '${armPath}' ]; do sleep 0.1; done`,
      `printf '\\033]9;%s\\007' '${ARMED}'`,
      `while [ ! -e '${summonRequestPath}' ]; do sleep 0.1; done`,
      `printf '%s\n' "$(stty size)" > '${baselineSizePath}'`,
      `cp '${resizePath}' '${baselinePath}'`,
      `while [ ! -e '${summonPath}' ]; do sleep 0.1; done`,
      `printf '%s\n' "$(stty size)" > '${summonedSizePath}'`,
      `while [ ! -e '${probePath}' ]; do sleep 0.1; done`,
      `if cmp -s '${resizePath}' '${baselinePath}'; then printf '%s' no-resize > '${probeResultPath}'; else printf '%s' resized > '${probeResultPath}'; fi`,
      `printf '%s' '${DURING}'`,
      `printf '\\033]9;%s\\007' '${DURING_READY}'`,
      `while [ ! -e '${exitPath}' ]; do sleep 0.1; done`,
      `printf '\\033[?1049l'`,
      `printf '\\n%s\\n' 'FULLSCREEN-THAWED-${nonce}'`,
      `printf '%s\\n' "$(stty size)" > '${finalSizePath}'`,
      'exit 0',
      '',
    ].join('\n'),
  )
  // A key-owning full-viewport normal-buffer program for the recovery/Stop
  // trace. It mirrors `top`'s screen ownership rather than using a sleep: raw
  // mode gives it the next byte, q exits, and INT/TERM restore the terminal.
  writeFileSync(
    keyScriptPath,
    [
      'old=$(stty -g)',
      `cleanup() { stty "$old"; exit 0; }`,
      'trap cleanup INT TERM HUP',
      `printf '\\033[2J\\033[H'`,
      `i=1; while [ "$i" -le 24 ]; do printf 'TUI ROW %02d ${INITIAL}-KEY\\r\\n' "$i"; i=$((i+1)); done`,
      `printf '\\033]9;%s\\007' '${KEY_READY}'`,
      'stty -echo -icanon min 1 time 0',
      'key=$(dd bs=1 count=1 2>/dev/null)',
      `printf '%s' "$key" > '${keyResultPath}'`,
      'cleanup',
      '',
    ].join('\n'),
  )
})
test.beforeEach(() => {
  for (const path of [
    armPath,
    summonPath,
    summonRequestPath,
    baselinePath,
    probePath,
    probeResultPath,
    exitPath,
    baselineSizePath,
    summonedSizePath,
    finalSizePath,
    keyResultPath,
  ]) {
    rmSync(path, { force: true })
  }
  writeFileSync(resizePath, '')
})

test.afterAll(async () => {
  writeFileSync(probePath, 'release\n')
  writeFileSync(exitPath, 'release\n')
  backend?.stop()
  await fake?.stop()
})

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
interface LifecycleTrace {
  sessionId?: string
  lifecycle?: string
  domain?: string
  epoch?: number
  attempt?: { id?: string; state?: string; origin?: string }
}
interface SignalTrace {
  id: string
  sessionId?: string
  signal?: string
  outcome?: string
}
interface SpecRecord {
  asks: string[]
  raised: string[]
  resolved: ResolvedFrame[]
  lifecycles: LifecycleTrace[]
  signalRequests: SignalTrace[]
  signalResults: SignalTrace[]
}

declare global {
  interface Window {
    __nocxFullScreenAskSpec?: SpecRecord
  }
}

/** Record the renderer's own ask/read traffic off the app's WebSocket. */
async function recordFrames(page: Page): Promise<void> {
  await page.addInitScript(() => {
    window.__nocxFullScreenAskSpec = {
      asks: [],
      raised: [],
      resolved: [],
      lifecycles: [],
      signalRequests: [],
      signalResults: [],
    }
    const send = WebSocket.prototype.send
    const add = WebSocket.prototype.addEventListener
    const recordedSockets = new WeakSet<WebSocket>()
    WebSocket.prototype.addEventListener = function (
      this: WebSocket,
      type: string,
      listener: EventListenerOrEventListenerObject,
      options?: boolean | AddEventListenerOptions,
    ): void {
      if (type === 'message' && !recordedSockets.has(this)) {
        recordedSockets.add(this)
        add.call(
          this,
          'message',
          (rawEvent: Event) => {
            const event = rawEvent as MessageEvent
            if (typeof event.data !== 'string') return
            try {
              const msg = JSON.parse(event.data) as {
                id?: string | number
                method?: string
                params?: LifecycleTrace
                result?: { outcome?: string }
              }
              const rec = window.__nocxFullScreenAskSpec!
              if (msg.method === 'lifecycle.changed' && msg.params) {
                rec.lifecycles.push(msg.params)
              }
              if (msg.id !== undefined) {
                const id = String(msg.id)
                const request = rec.signalRequests.find((candidate) => candidate.id === id)
                if (request && typeof msg.result?.outcome === 'string') {
                  rec.signalResults.push({ ...request, outcome: msg.result.outcome })
                }
              }
            } catch {
              // Binary PTY frames and unrelated text frames are not this trace.
            }
          },
          options,
        )
      }
      add.call(this, type, listener, options)
    }
    WebSocket.prototype.send = function (this: WebSocket, data: Parameters<typeof send>[0]) {
      if (typeof data === 'string') {
        try {
          const msg = JSON.parse(data) as {
            id?: string | number
            method?: string
            params?: Record<string, unknown>
          }
          const rec = window.__nocxFullScreenAskSpec!
          if (msg.method === 'agent.ask' && typeof msg.params?.sessionId === 'string') {
            rec.asks.push(msg.params.sessionId)
          }
          if (msg.method === 'notify.raise' && typeof msg.params?.body === 'string') {
            rec.raised.push(msg.params.body)
          }
          if (msg.method === 'agent.readScreenResolved' && msg.params) {
            rec.resolved.push(msg.params as ResolvedFrame)
          }
          if (
            msg.method === 'session.signal' &&
            (typeof msg.id === 'string' || typeof msg.id === 'number')
          ) {
            rec.signalRequests.push({
              id: String(msg.id),
              sessionId:
                typeof msg.params?.sessionId === 'string' ? msg.params.sessionId : undefined,
              signal: typeof msg.params?.signal === 'string' ? msg.params.signal : undefined,
            })
          }
        } catch {
          // Binary PTY frames and unrelated text frames are not this record.
        }
      }
      return send.call(this, data)
    }
  })
}

async function recorded(page: Page): Promise<SpecRecord> {
  return page.evaluate(
    () =>
      window.__nocxFullScreenAskSpec ?? {
        asks: [],
        raised: [],
        resolved: [],
        lifecycles: [],
        signalRequests: [],
        signalResults: [],
      },
  )
}

function frameText(frame: ResolvedFrame): string {
  return (frame.rows ?? [])
    .map((row) => (row.cells ?? []).map((cell) => cell.char ?? '').join(''))
    .join('\n')
}

async function openApp(page: Page): Promise<void> {
  await bindEndpoint(page, endpoint)
  await page.goto('/')
  await expect(page.locator(TITLE).first()).not.toHaveText('', { timeout: 15_000 })
  await appReadyForInput(page)
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

function modeIndicator(page: Page) {
  return page.locator('.pane.active .ui-mode-indicator:visible')
}
async function logFollowProbe(page: Page, moment: string): Promise<void> {
  const sample = await page.evaluate(() => {
    const pane = document.querySelector<HTMLElement>('.pane.active')
    const area = pane?.querySelector<HTMLElement>('.scrollback-area')
    const sentinel = pane?.querySelector<HTMLElement>('.scrollback-follow-sentinel')
    if (!area || !sentinel) return null
    const areaRect = area.getBoundingClientRect()
    const sentinelRect = sentinel.getBoundingClientRect()
    return {
      sentinelIntersects:
        sentinelRect.top < areaRect.bottom &&
        sentinelRect.bottom > areaRect.top &&
        sentinelRect.left < areaRect.right &&
        sentinelRect.right > areaRect.left,
      atBottom: area.scrollTop + area.clientHeight >= area.scrollHeight - 2,
      scrollTop: area.scrollTop,
      clientHeight: area.clientHeight,
      scrollHeight: area.scrollHeight,
    }
  })
  console.log(
    `FOLLOW-PROBE moment=${moment} ` +
      (sample
        ? Object.entries(sample)
            .map(([key, value]) => `${key}=${value}`)
            .join(' ')
        : 'elements=missing'),
  )
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
  return page.locator('.cmd-block').filter({ hasText: question }).first()
}

async function answerFinished(page: Page, question: string): Promise<void> {
  await expect(answerBlock(page, question).locator('.cmd-header-exit')).toHaveText('completed', {
    timeout: 30_000,
  })
}

function summonedAnswers(page: Page) {
  return page.locator('.pane.active .nocx-summon-answers > .cmd-block[data-block-kind="ask"]')
}

async function summonStackGeometry(page: Page): Promise<{
  position: string
  answersTop: number
  answersBottom: number
  lastAnswerBottom: number
  composerTop: number
}> {
  return page.locator('.pane.active .nocx-summon-stack').evaluate((stack) => {
    const answers = stack.querySelector<HTMLElement>('.nocx-summon-answers')
    const composer = stack.querySelector<HTMLElement>(
      '.nocx-editor[data-placement="overlay"]:not([style*="display: none"])',
    )
    const lastAnswer = answers?.lastElementChild as HTMLElement | null
    if (!answers || !composer || !lastAnswer) {
      throw new Error('settled summon stack is incomplete')
    }
    const answerListRect = answers.getBoundingClientRect()
    return {
      position: getComputedStyle(stack).position,
      answersTop: answerListRect.top,
      answersBottom: answerListRect.bottom,
      lastAnswerBottom: lastAnswer.getBoundingClientRect().bottom,
      composerTop: composer.getBoundingClientRect().top,
    }
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
  await page.locator(SETTINGS_POLICY_NAV).click()
  const observeRow = page.locator(OBSERVE_ROW)
  await expect(observeRow).toBeVisible({ timeout: 15_000 })
  await observeRow.locator('select').first().selectOption({ label: 'Allowed' })
  await expect(observeRow.locator('.st-policy__state')).toContainText('Allowed', {
    timeout: 15_000,
  })
}

function answerFromPinnedFrame(body: string): string[] {
  let seen = ''
  try {
    const parsed = JSON.parse(body) as { messages?: { role?: string; content?: string }[] }
    seen = (parsed.messages ?? [])
      .filter((message) => message.role !== 'system' && message.role !== 'user')
      .map((message) => message.content ?? '')
      .join('\n')
  } catch {
    seen = ''
  }
  if (!seen.includes(INITIAL) || seen.includes(DURING)) {
    return ['The pinned screen was not the expected frame.']
  }
  return Array.from(
    { length: 80 },
    (_, index) => `The pinned screen shows ${INITIAL} (line ${index + 1}).\n`,
  )
}

test.describe('asking about a full-screen program without leaving it (nocx-7l4ex)', () => {
  test.use({ viewport: { width: 1280, height: 900 } })

  test('freezes, answers twice with one composer, thaws, and quits normally', async ({ page }) => {
    test.setTimeout(120_000)
    await recordFrames(page)
    await openApp(page)
    await configureAssistant(page, `${ENDPOINT_NAME} — stack`)
    await backToTerminal(page)
    await promptReady(page)
    // Obtain the real session id from the product's own ask payload. The
    // scripted session.read below must name this pane, not an invented id.
    await useTarget(page, 'agent')
    const warmup = 'Are you ready?'
    await page.locator(INPUT).fill(warmup)
    await page.keyboard.press('Enter')
    await answerFinished(page, warmup)
    await expect.poll(async () => (await recorded(page)).asks.length).toBeGreaterThan(0)
    const sessionId = (await recorded(page)).asks.at(-1)!
    expect(sessionId).not.toBe('')

    // Start a true alternate-buffer foreground program and prove that state
    // before using the summon chord. A normal-buffer command must not satisfy
    // this acceptance by accident.
    await useTarget(page, 'shell')
    await page.keyboard.type(`sh '${scriptPath}'`)
    await page.keyboard.press('Enter')
    await expect(page.locator(RUNNING_BLOCK)).toHaveCount(1, { timeout: 20_000 })
    await expect
      .poll(async () => (await recorded(page)).raised.includes(READY), {
        timeout: 45_000,
        message: 'the alternate-buffer fixture never reached its initial marker',
      })
      .toBe(true)
    await expect(page.locator(GRID)).toHaveClass(/live-fullscreen/, { timeout: 20_000 })
    await expect(page.locator(INPUT)).toBeHidden({ timeout: 10_000 })
    await logFollowProbe(page, 'fullscreen')
    // Arm the resize probe only after the alternate screen and its initial
    // fit have reached observable state. The fixture snapshots, rather than
    // clears, all earlier signals; only new signals after this boundary count.
    writeFileSync(armPath, 'arm\n')
    await expect
      .poll(async () => (await recorded(page)).raised.includes(ARMED), {
        timeout: 15_000,
        message: 'the fixture never armed its post-summon resize probe',
      })
      .toBe(true)

    // Request the geometry snapshot immediately before the summon action.
    // WebKit may finish an earlier fit after the grid is already visible; a
    // snapshot taken at arm time would turn that delayed setup work into a
    // false summon-resize failure.
    writeFileSync(summonRequestPath, 'summon-request\n')

    // The user presses the summon chord on the full-screen grid. The overlay
    // and static frame must appear without moving the real terminal geometry.
    await page.locator(GRID).click()
    await page.keyboard.press('ControlOrMeta+Enter')
    await expect(page.locator('.pane.active .nocx-editor[data-placement="overlay"]')).toBeVisible({
      timeout: 15_000,
    })
    await expect(page.locator(INPUT)).toBeVisible({ timeout: 15_000 })
    await expect(page.locator(FREEZE)).toBeVisible({ timeout: 15_000 })
    await expect(page.locator(FREEZE)).toContainText(INITIAL)
    await expect(modeIndicator(page)).toHaveAttribute('data-target', 'agent', {
      timeout: 10_000,
    })
    writeFileSync(summonPath, 'summoned\n')
    await expect.poll(() => fileText(summonedSizePath).trim()).not.toBe('')
    const dimensionsAtSummon = fileText(baselineSizePath).trim()
    expect(dimensionsAtSummon).not.toBe('')
    expect(fileText(summonedSizePath).trim()).toBe(dimensionsAtSummon)
    await expect(page.locator(GRID)).toHaveClass(/live-fullscreen/)

    // Ask while the full-screen process still owns its pty. The second model
    // response is held after its first delta so output written after capture
    // can be observed as genuinely happening under the pinned frame.
    fake.setScript({ chunks: [], toolCalls: [{ name: 'session.read', arguments: {} }] })
    fake.setScript({ chunks: answerFromPinnedFrame, holdAfter: 1 })
    const requestBase = fake.requests().length
    const question = 'What was on the screen?'
    expect(question).not.toContain(INITIAL)
    expect(question).not.toContain(DURING)
    await page.locator(INPUT).fill(question)
    await page.keyboard.press('Enter')

    const answer = answerBlock(page, question)
    await expect(answer).toBeVisible({ timeout: 15_000 })
    const answerBody = answer.locator('[data-answer-body]')
    await expect(answerBody).toContainText('The pinned screen shows', { timeout: 15_000 })
    const requests = await fake.waitForRequests(requestBase + 2, 30_000)
    const chatRequests = requests
      .slice(requestBase)
      .filter((request) => request.body.includes('"messages"'))
    expect(chatRequests.length).toBeGreaterThanOrEqual(2)
    const answerRequest = chatRequests.at(-1)!
    await fake.waitForState(answerRequest.id, 'streaming', 15_000)

    // Open the fixture's probe only after the answer has been accepted and is
    // visibly streaming. It writes DURING after the captured frame exists.
    writeFileSync(probePath, 'probe\n')
    await expect.poll(() => fileText(probeResultPath)).toBe('no-resize')
    await expect
      .poll(async () => (await recorded(page)).raised.includes(DURING_READY), {
        timeout: 30_000,
        message: 'the fixture did not write output while the frame was pinned',
      })
      .toBe(true)

    // The answer is still under the frozen view, and its model input proves
    // the session.read result was the initial frame rather than live output.
    await expect(page.locator(FREEZE)).toContainText(INITIAL)
    await expect(page.locator(FREEZE)).not.toContainText(DURING)
    await expect(answerBody).not.toContainText(DURING)
    const { resolved } = await recorded(page)
    expect(resolved).toHaveLength(1)
    expect(resolved[0].outcome, resolved[0].error ?? 'renderer read failed').toBe('frame')
    expect(frameText(resolved[0])).toContain(INITIAL)
    expect(frameText(resolved[0])).not.toContain(DURING)
    expect(chatRequests[0].body).not.toContain(INITIAL)
    expect(chatRequests[1].body).toContain(INITIAL)
    expect(chatRequests[1].body).not.toContain(DURING)

    // Terminalizing the first answer returns the SAME composer while the TUI
    // remains active. The pinned frame and first answer stay in the one
    // absolute stack; terminal settlement, not elapsed time or text, is the
    // return seam.
    fake.release(answerRequest.id)
    await answerFinished(page, question)
    await expect(page.locator(INPUT)).toBeVisible({ timeout: 15_000 })
    await expect(page.locator(FREEZE)).toBeVisible()
    await expect(answer).toBeVisible()
    await expect(answer).toHaveClass(/nocx-answer-overlay/)
    await expect(modeIndicator(page)).toHaveAttribute('data-target', 'agent')
    const firstGeometry = await summonStackGeometry(page)
    expect(firstGeometry.position).toBe('absolute')
    expect(firstGeometry.answersBottom).toBeLessThanOrEqual(firstGeometry.composerTop + 1)
    expect(firstGeometry.lastAnswerBottom).toBeLessThanOrEqual(firstGeometry.answersBottom + 1)
    expect(firstGeometry.lastAnswerBottom).toBeGreaterThan(firstGeometry.answersTop)
    expect(fileText(resizePath)).toBe(fileText(baselinePath))

    // Submit and finish a follow-up through that returned composer. Hold its
    // stream so the product must hide the composer for a current turn, retain
    // the first answer, and then reconcile the composer from the terminal
    // runState while the alternate-buffer program still owns its PTY.
    const followUpQuestion = 'What should I inspect next?'
    const followUpText = `The follow-up remains over ${INITIAL}.`
    fake.setScript({ chunks: [followUpText, ' The composer is still usable.'], holdAfter: 1 })
    const followUpRequestBase = fake.requests().length
    await page.locator(INPUT).fill(followUpQuestion)
    await page.keyboard.press('Enter')
    const followUpAnswer = answerBlock(page, followUpQuestion)
    await expect(followUpAnswer).toBeVisible({ timeout: 15_000 })
    await expect(followUpAnswer.locator('[data-answer-body]')).toContainText(followUpText, {
      timeout: 15_000,
    })
    const followUpRequests = await fake.waitForRequests(followUpRequestBase + 1, 30_000)
    const followUpRequest = followUpRequests.at(-1)!
    await fake.waitForState(followUpRequest.id, 'streaming', 15_000)
    await expect(page.locator(INPUT)).toBeHidden()
    const overlayAnswers = summonedAnswers(page)
    await expect(overlayAnswers).toHaveCount(2)
    await expect(overlayAnswers.nth(0)).toContainText(question)
    await expect(overlayAnswers.nth(1)).toContainText(followUpQuestion)

    fake.release(followUpRequest.id)
    await answerFinished(page, followUpQuestion)
    await expect(page.locator(INPUT)).toBeVisible({ timeout: 15_000 })
    await expect(page.locator(FREEZE)).toBeVisible()
    await expect(overlayAnswers).toHaveCount(2)
    const followUpGeometry = await summonStackGeometry(page)
    expect(followUpGeometry.position).toBe('absolute')
    expect(followUpGeometry.answersBottom).toBeLessThanOrEqual(followUpGeometry.composerTop + 1)
    expect(followUpGeometry.lastAnswerBottom).toBeLessThanOrEqual(
      followUpGeometry.answersBottom + 1,
    )
    expect(followUpGeometry.lastAnswerBottom).toBeGreaterThan(followUpGeometry.answersTop)
    expect(fileText(resizePath)).toBe(fileText(baselinePath))
    await expect(page.locator(GRID)).toHaveClass(/live-fullscreen/)

    // Owner decision, nocx-7l4ex.18: Escape returns the pane to the
    // foreground program by seating both answers in scrollback immediately.
    // The overlay is empty, while the exact answer nodes remain in ask order.
    await page.keyboard.press('Escape')
    await expect(page.locator(FREEZE)).toHaveCount(0, { timeout: 10_000 })
    await logFollowProbe(page, 'thaw')
    await expect(page.locator(INPUT)).toBeHidden({ timeout: 10_000 })
    await expect(page.locator(GRID)).toHaveClass(/live-fullscreen/)
    await expect(page.locator(GRID)).toBeVisible()
    await expect(overlayAnswers).toHaveCount(0)
    await expect(answer).toHaveCount(1)
    await expect(followUpAnswer).toHaveCount(1)
    const escapedSeating = await followUpAnswer.evaluate(
      (second, expected) => {
        const inner = second.parentElement
        if (!inner) return null
        const children = Array.from(inner.children)
        const first = children.find((el) => el.textContent?.includes(expected.first))
        const secondAnswer = children.find((el) => el.textContent?.includes(expected.second))
        return {
          seated: inner.classList.contains('scrollback-inner'),
          first: first ? children.indexOf(first) : -1,
          second: secondAnswer ? children.indexOf(secondAnswer) : -1,
        }
      },
      { first: question, second: followUpQuestion },
    )
    expect(escapedSeating).toMatchObject({ seated: true })
    expect(escapedSeating!.first).toBeLessThan(escapedSeating!.second)
    expect(fileText(resizePath)).toBe(fileText(baselinePath))

    // The program exits normally. Both exact answer nodes then take one
    // consecutive seat after the command, preserving ask order, and ordinary
    // shell/editor ownership returns.
    writeFileSync(exitPath, 'exit\n')
    await expect(page.locator(RUNNING_BLOCK)).toHaveCount(0, { timeout: 30_000 })
    await expect(page.locator(INPUT)).toBeVisible({ timeout: 20_000 })
    await expect(modeIndicator(page)).toHaveAttribute('data-target', 'shell', { timeout: 10_000 })
    await expect.poll(() => fileText(finalSizePath).trim()).not.toBe('')
    await expect(
      page
        .locator('.cmd-block')
        .filter({ hasText: `FULLSCREEN-THAWED-${nonce}` })
        .first(),
    ).toContainText(`FULLSCREEN-THAWED-${nonce}`)
    await expect(answer).toBeVisible({ timeout: 15_000 })
    await expect(followUpAnswer).toBeVisible({ timeout: 15_000 })
    await expect(answer).not.toHaveClass(/nocx-answer-overlay/)
    await expect(followUpAnswer).not.toHaveClass(/nocx-answer-overlay/)
    await expect(answer.locator('[data-answer-body]')).toContainText(INITIAL)
    await expect(followUpAnswer.locator('[data-answer-body]')).toContainText(followUpText)
    await expect(page.locator('.cmd-block').filter({ hasText: question })).toHaveCount(1)
    await expect(page.locator('.cmd-block').filter({ hasText: followUpQuestion })).toHaveCount(1)

    const seating = await followUpAnswer.evaluate(
      (second, expected) => {
        const inner = second.parentElement
        if (!inner) return null
        const children = Array.from(inner.children)
        const command = children.find((el) => el.textContent?.includes(expected.command))
        const first = children.find((el) => el.textContent?.includes(expected.first))
        const secondAnswer = children.find((el) => el.textContent?.includes(expected.second))
        return {
          command: command ? children.indexOf(command) : -1,
          first: first ? children.indexOf(first) : -1,
          second: secondAnswer ? children.indexOf(secondAnswer) : -1,
          firstCount: children.filter((el) => el.textContent?.includes(expected.first)).length,
          secondCount: children.filter((el) => el.textContent?.includes(expected.second)).length,
        }
      },
      { command: scriptPath, first: question, second: followUpQuestion },
    )
    expect(seating).not.toBeNull()
    expect(seating).toMatchObject({ firstCount: 1, secondCount: 1 })
    expect(seating!.first).toBe(seating!.command + 1)
    expect(seating!.second).toBe(seating!.first + 1)

    await expect
      .poll(() =>
        followUpAnswer.evaluate((el) => {
          const area = el.closest('.pane')?.querySelector<HTMLElement>('.scrollback-area')
          if (!area) return { seated: false, atBottom: false, tailVisible: false }
          const answerRect = el.getBoundingClientRect()
          const areaRect = area.getBoundingClientRect()
          return {
            seated: el.parentElement?.classList.contains('scrollback-inner') ?? false,
            atBottom: area.scrollTop + area.clientHeight >= area.scrollHeight - 2,
            tailVisible:
              area.clientHeight > 0 &&
              answerRect.height > 0 &&
              answerRect.bottom > areaRect.top &&
              answerRect.bottom <= areaRect.bottom + 2,
          }
        }),
      )
      .toMatchObject({ seated: true, atBottom: true, tailVisible: true })
  })

  test('returns q and Stop to a full-screen foreground after Ask, with all owners agreeing', async ({
    page,
  }) => {
    test.setTimeout(120_000)
    await recordFrames(page)
    await openApp(page)
    await configureAssistant(page, `${ENDPOINT_NAME} — recovery`)
    await backToTerminal(page)
    await promptReady(page)

    const startProgram = async (): Promise<void> => {
      const readyBefore = (await recorded(page)).raised.filter((body) => body === KEY_READY).length
      rmSync(keyResultPath, { force: true })
      await useTarget(page, 'shell')
      // JOB CONTROL OFF, which is the whole point of this act (nocx-7l4ex.11).
      // `set +m` makes bash run the command in its OWN process group instead
      // of creating one for it — the topology ADR-0024 names, and the one the
      // owner's `top` was in when Stop answered "nothing is running". Without
      // it the job gets an independent group, the ordinary TIOCGPGRP ladder
      // handles it, and this act would pass with the protected-group
      // mechanism deleted.
      await page.keyboard.type(`set +m; sh '${keyScriptPath}'`)
      await page.keyboard.press('Enter')
      await expect(page.locator(RUNNING_BLOCK)).toHaveCount(1, { timeout: 20_000 })
      await expect
        .poll(async () => (await recorded(page)).raised.filter((body) => body === KEY_READY).length)
        .toBeGreaterThan(readyBefore)
      await expect(page.locator(GRID)).toBeVisible({ timeout: 20_000 })
    }

    const askAndReturn = async (question: string): Promise<void> => {
      await page.locator(GRID).click()
      await page.keyboard.press('ControlOrMeta+Enter')
      await expect(page.locator(INPUT)).toBeVisible({ timeout: 15_000 })
      fake.setScript({ chunks: ['The full-screen program is waiting for one key.'] })
      await page.locator(INPUT).fill(question)
      await page.keyboard.press('Enter')
      await answerFinished(page, question)
      await expect(page.locator(INPUT)).toBeVisible({ timeout: 15_000 })
      await page.keyboard.press('Escape')
      await expect(page.locator(FREEZE)).toHaveCount(0, { timeout: 10_000 })
      await expect(page.locator(INPUT)).toBeHidden({ timeout: 10_000 })
      await expect(page.locator(GRID)).toBeVisible()
    }

    // Keyboard recovery: the first program owns one byte and q is that byte.
    await startProgram()
    await askAndReturn(`${KEY_QUESTION} — q`)
    await page.keyboard.press('q')
    await expect.poll(() => fileText(keyResultPath)).toBe('q')
    await expect(page.locator(RUNNING_BLOCK)).toHaveCount(0, { timeout: 30_000 })
    await expect(page.locator(INPUT)).toBeVisible({ timeout: 20_000 })

    // Stop recovery: repeat the same real full-screen journey, then capture
    // every authority immediately before the established Stop path answers.
    await startProgram()
    await askAndReturn(`${KEY_QUESTION} — Stop`)
    const running = page.locator(RUNNING_BLOCK)
    const beforeStop = await page.evaluate(
      ({ blockSelector, inputSelector, freezeSelector, gridSelector }) => {
        const block = document.querySelector<HTMLElement>(blockSelector)
        const input = document.querySelector<HTMLElement>(inputSelector)
        const grid = document.querySelector<HTMLElement>(gridSelector)
        return {
          blockCount: document.querySelectorAll(blockSelector).length,
          blockEntryId: block?.dataset.entryId ?? '',
          blockKind: block?.dataset.blockKind ?? '',
          inputVisible: input !== null && input.getClientRects().length > 0,
          freezePresent: document.querySelector(freezeSelector) !== null,
          gridFullscreen: grid?.classList.contains('live-fullscreen') ?? false,
        }
      },
      {
        blockSelector: RUNNING_BLOCK,
        inputSelector: INPUT,
        freezeSelector: FREEZE,
        gridSelector: GRID,
      },
    )
    const traceBefore = await recorded(page)
    const lifecycle = [...traceBefore.lifecycles]
      .reverse()
      .find((candidate) => candidate.lifecycle === 'running')
    expect(
      lifecycle,
      JSON.stringify({ beforeStop, lifecycles: traceBefore.lifecycles }),
    ).toBeDefined()
    expect(beforeStop).toMatchObject({
      blockCount: 1,
      blockEntryId: lifecycle?.attempt?.id,
      inputVisible: false,
      freezePresent: false,
      gridFullscreen: false,
    })

    await running.locator('.cmd-overflow-btn').click()
    await page.locator('.cmd-overflow-menu-item[data-action="stop"]').click()
    await expect.poll(async () => (await recorded(page)).signalResults.length).toBeGreaterThan(0)
    const signal = (await recorded(page)).signalResults.at(-1)
    const fourFacts = { lifecycle, beforeStop, signal }
    expect(signal?.outcome, JSON.stringify(fourFacts)).toBe('delivered')
    await expect(page.locator(RUNNING_BLOCK)).toHaveCount(0, { timeout: 30_000 })
    await expect(page.locator(INPUT)).toBeVisible({ timeout: 20_000 })
  })
})

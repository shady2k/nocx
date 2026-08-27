/**
 * e2e: a person asks about a full-screen program without leaving it
 * (nocx-7l4ex.5/.6).
 *
 * This is deliberately a real alternate-buffer journey, not the running normal
 * buffer case in ask-about-a-running-command.spec.ts. The fixture owns a pty,
 * enters the alternate screen, and records SIGWINCH so a resize cannot hide in
 * a screenshot. It also writes a second marker only after the summoned frame
 * is pinned. That marker must be absent from the answer's frame and become
 * visible in the live grid after Escape thaws it.
 *
 * The answer is derived from the model request. A fixed response could repeat a
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
import { mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import {
  VaultBackend,
  bindEndpoint,
  createAiEndpoint,
  promptReady,
  setDefaultModel,
  settingsReady,
} from './harness'
import { readStand } from './stand'
import { FakeOpenAI } from './fake-openai'

const devharnessBin = () => readStand().devharness

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

let backend: VaultBackend
let fake: FakeOpenAI
let endpoint: { port: number; token: string }
let fixtureDir = ''
let scriptPath = ''
let probePath = ''
let probeResultPath = ''
let armPath = ''
let summonPath = ''
let baselinePath = ''
let exitPath = ''
let resizePath = ''
let baselineSizePath = ''
let summonedSizePath = ''
let finalSizePath = ''

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
  backend = new VaultBackend(devharnessBin(), { root }, true)
  fixtureDir = mkdtempSync(join(tmpdir(), 'nocx-7l4ex-fullscreen-'))
  endpoint = await backend.start()

  scriptPath = join(fixtureDir, 'fullscreen-job.sh')
  probePath = join(fixtureDir, 'probe')
  probeResultPath = join(fixtureDir, 'probe-result')
  armPath = join(fixtureDir, 'arm')
  summonPath = join(fixtureDir, 'summon')
  baselinePath = join(fixtureDir, 'resize-baseline')
  exitPath = join(fixtureDir, 'exit')
  resizePath = join(fixtureDir, 'sigwinch')
  baselineSizePath = join(fixtureDir, 'baseline-size')
  summonedSizePath = join(fixtureDir, 'summoned-size')
  finalSizePath = join(fixtureDir, 'final-size')
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
      `printf '%s\n' "$(stty size)" > '${baselineSizePath}'`,
      `cp '${resizePath}' '${baselinePath}'`,
      `printf '\\033]9;%s\\007' '${ARMED}'`,
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
interface SpecRecord {
  asks: string[]
  raised: string[]
  resolved: ResolvedFrame[]
}

declare global {
  interface Window {
    __nocxFullScreenAskSpec?: SpecRecord
  }
}

/** Record the renderer's own ask/read traffic off the app's WebSocket. */
async function recordFrames(page: Page): Promise<void> {
  await page.addInitScript(() => {
    window.__nocxFullScreenAskSpec = { asks: [], raised: [], resolved: [] }
    const send = WebSocket.prototype.send
    WebSocket.prototype.send = function (this: WebSocket, data: Parameters<typeof send>[0]) {
      if (typeof data === 'string') {
        try {
          const msg = JSON.parse(data) as { method?: string; params?: Record<string, unknown> }
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
    () => window.__nocxFullScreenAskSpec ?? { asks: [], raised: [], resolved: [] },
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

  test('freezes, answers, thaws, and quits the alternate-buffer program normally', async ({
    page,
  }) => {
    test.setTimeout(120_000)
    await recordFrames(page)
    await openApp(page)
    await configureAssistant(page)
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
    fake.setScript({ chunks: [], toolCalls: [{ name: 'session.read', arguments: { sessionId } }] })
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

    // Escape is the door back to the program. The static frame disappears,
    // the live xterm becomes visible, and the output written under the pin is
    // now on screen. The answer is still streaming until explicitly released.
    await page.keyboard.press('Escape')
    await expect(page.locator(FREEZE)).toHaveCount(0, { timeout: 10_000 })
    await expect(page.locator(INPUT)).toBeHidden({ timeout: 10_000 })
    await expect(page.locator(GRID)).toHaveClass(/live-fullscreen/)
    await expect(page.locator(GRID)).toBeVisible()
    fake.release(answerRequest.id)
    await answerFinished(page, question)

    // The program exits normally after the answer is released. The final
    // dimensions are recorded for diagnostics; the summon boundary above is
    // the no-resize assertion because returning the editor may change layout.
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
    await expect(answer).not.toHaveClass(/nocx-answer-overlay/)
    await expect(answer.locator('[data-answer-body]')).toContainText(INITIAL)
    await expect
      .poll(() =>
        answer.evaluate((el) => {
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
})

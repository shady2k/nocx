/** Real packaged engines, selected through Settings, sharing the Go approval and history paths. */
import { expect, type Locator, type Page } from '@playwright/test'
import { execFile } from 'node:child_process'
import {
  chmodSync,
  copyFileSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  statSync,
} from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { promisify } from 'node:util'
import {
  standalone as test,
  appReadyForInput,
  bindEndpoint,
  createAiEndpoint,
  promptReady,
  setDefaultModel,
  settingsReady,
  VaultBackend,
} from './harness'
import { FakeOpenAI } from './fake-openai'
import { readStand } from './stand'

const INPUT = '.pane.active .nocx-editor-input'
const TITLE = '.nocx-tab-title'
const ENGINE_LABEL = 'Pydantic agent (Experimental)'
const PYDANTIC = 'pydantic-deepagents'
const nonce = Date.now().toString(36)
const target =
  process.platform === 'linux' && process.arch === 'x64'
    ? 'linux-amd64'
    : process.platform === 'darwin' && (process.arch === 'x64' || process.arch === 'arm64')
      ? `darwin-${process.arch === 'x64' ? 'amd64' : 'arm64'}`
      : ''
const runtimeEnv = {
  NOCX_AGENT_RUNTIME_ROOT: undefined,
  NOCX_AGENT_RUNTIME_DIGEST: undefined,
  NOCX_AGENT_RUNTIME_LEASE_FD: undefined,
}
let root: string
let backend: VaultBackend
let endpoint: { port: number; token: string }
let fake: FakeOpenAI

// Building is fixture preparation, not a runtime install: the application must
// discover this shipped layout itself and stage the sealed bundle normally.
test.beforeAll(async () => {
  test.skip(
    !target,
    'The packaged Python runtime ships for Linux amd64 and Darwin amd64/arm64 only.',
  )
  test.setTimeout(300_000)
  root = mkdtempSync(join(tmpdir(), 'nocx-engine-e2e-'))
  const binary = target.startsWith('darwin-')
    ? join(root, 'Contents/MacOS/nocx-server')
    : join(root, 'bin/nocx-server')
  const bundle = target.startsWith('darwin-')
    ? join(root, 'Contents/Resources/agent-runtime', target)
    : join(root, 'lib/nocx/agent-runtime', target)
  mkdirSync(dirname(binary), { recursive: true })
  const source = readStand().server
  copyFileSync(source, binary)
  chmodSync(binary, statSync(source).mode)
  await promisify(execFile)(
    'python3',
    [
      resolve(__dirname, '../scripts/package-agent-runtime.py'),
      '--target',
      target,
      '--output',
      bundle,
    ],
    { timeout: 240_000, maxBuffer: 8 * 1024 * 1024 },
  )
  fake = new FakeOpenAI()
  await fake.start()
  backend = new VaultBackend(binary, { root })
  endpoint = await backend.start(runtimeEnv)
})

test.afterAll(async () => {
  backend?.stop()
  await fake?.stop()
  if (root) rmSync(root, { recursive: true, force: true })
})

type Receipt = { method: string; params: Record<string, unknown>; result: Record<string, unknown> }

/** Observe the real UI client's replies; never call Ask, Test, or approval ourselves. */
function receipts(page: Page): Receipt[] {
  const completed: Receipt[] = []
  page.on('websocket', (ws) => {
    const sent = new Map<string | number, { method: string; params: Record<string, unknown> }>()
    ws.on('framesent', ({ payload }) => {
      if (typeof payload !== 'string') return
      const frame = JSON.parse(payload) as {
        id?: string | number
        method?: string
        params?: Record<string, unknown>
      }
      if (frame.id !== undefined && frame.method) {
        sent.set(frame.id, { method: frame.method, params: frame.params ?? {} })
      }
    })
    ws.on('framereceived', ({ payload }) => {
      if (typeof payload !== 'string') return
      const frame = JSON.parse(payload) as {
        id?: string | number
        result?: Record<string, unknown>
      }
      if (frame.id === undefined) return
      const request = sent.get(frame.id)
      sent.delete(frame.id)
      if (request && frame.result) completed.push({ ...request, result: frame.result })
    })
  })
  return completed
}

async function openApp(page: Page): Promise<void> {
  await bindEndpoint(page, endpoint)
  await page.goto('/')
  await appReadyForInput(page)
  await expect(page.locator(TITLE).first()).not.toHaveText('', { timeout: 15_000 })
}

async function settings(page: Page, item: string): Promise<void> {
  await page.keyboard.press('Meta+,')
  await settingsReady(page)
  await page.locator(`.ui-grouped-nav__item[data-item="${item}"]`).click()
}

async function terminal(page: Page): Promise<void> {
  await page.locator(TITLE).first().click()
  await expect(page.locator(INPUT)).toBeVisible()
}

function turn(page: Page, question: string): Locator {
  return page.locator('.pane.active .cmd-block').filter({
    has: page.locator(':scope > .cmd-header .cmd-header-text', { hasText: question }),
  })
}

async function askMode(page: Page): Promise<void> {
  await page.locator(INPUT).click()
  const indicator = page.locator('.pane.active .ui-mode-indicator:visible')
  if ((await indicator.getAttribute('data-target')) !== 'agent') {
    await page.keyboard.press('ControlOrMeta+Enter')
    await expect(indicator).toHaveAttribute('data-target', 'agent')
  }
}

async function ask(page: Page, question: string): Promise<void> {
  await askMode(page)
  await page.locator(INPUT).fill(question)
  await page.keyboard.press('Enter')
}

async function complete(
  page: Page,
  question: string,
  answer: string,
  engine: string,
): Promise<void> {
  const block = turn(page, question)
  await expect(block).toHaveCount(1)
  await expect(block).toHaveAttribute('data-engine', engine)
  await expect(block).toHaveAttribute('data-outcome', 'success', { timeout: 30_000 })
  await expect(block).toContainText(answer)
}

/** Same DOM selection gesture as agent-ask: the UI offer grants this real block. */
async function pointAt(page: Page, block: Locator): Promise<void> {
  await askMode(page)
  await block.evaluate((element) => {
    const rows = Array.from(element.querySelectorAll('.cmd-output .term-grid-row'))
    if (!rows.length) throw new Error('finished terminal block has no output rows')
    const range = document.createRange()
    range.setStartBefore(rows[0])
    range.setEndAfter(rows[rows.length - 1])
    const selection = window.getSelection()
    selection?.removeAllRanges()
    selection?.addRange(range)
    document.dispatchEvent(new Event('selectionchange'))
  })
  const offer = page.locator('.mark-affordance .ui-button')
  await expect(offer).toBeVisible()
  await expect
    .poll(() => page.evaluate(() => document.activeElement?.className ?? ''))
    .not.toContain('nocx-editor-input')
  await offer.click()
  await expect(block).toHaveAttribute('data-granted', 'true')
  await expect(page.locator('.pane.active [data-control="grant"]')).toHaveCount(1)
}

/** Read-only persistence barrier, so restart cannot race the asynchronous ledger writer. */
async function ledger(
  page: Page,
  method: 'ledger.query' | 'ledger.get',
  params: unknown,
): Promise<Record<string, unknown>> {
  return page.evaluate(
    ({ endpoint, method, params }) => {
      const { promise, resolve, reject } = Promise.withResolvers<Record<string, unknown>>()
      const ws = new WebSocket(
        `ws://127.0.0.1:${endpoint.port}/session`,
        `nocx.token.${endpoint.token}`,
      )
      const timer = setTimeout(() => {
        ws.close()
        reject(new Error(`${method} timed out`))
      }, 15_000)
      ws.onopen = () => ws.send(JSON.stringify({ jsonrpc: '2.0', id: 1, method, params }))
      ws.onerror = () => {
        clearTimeout(timer)
        ws.close()
        reject(new Error(`${method} socket error`))
      }
      ws.onmessage = ({ data }) => {
        if (typeof data !== 'string') return
        const frame = JSON.parse(data) as {
          id?: number
          result?: Record<string, unknown>
          error?: unknown
        }
        if (frame.id !== 1) return
        clearTimeout(timer)
        ws.close()
        if (frame.error) reject(new Error(JSON.stringify(frame.error)))
        else resolve(frame.result ?? {})
      }
      return promise
    },
    { endpoint, method, params },
  )
}

test.use({ viewport: { width: 1280, height: 900 } })

test('the experimental engine is opt-in, keeps parked runs pinned, stops, and restores once', async ({
  page,
  context,
}) => {
  test.setTimeout(180_000)
  // Attach the Settings window FIRST: restoring a second renderer transfers
  // terminal ownership. The originating Ask window must attach last and keep
  // its connection through approval; Settings-only RPCs need no terminal.
  const preferences = await context.newPage()
  await openApp(preferences)
  await settings(preferences, 'Answers')
  const otherToggle = preferences.getByRole('switch', { name: ENGINE_LABEL, exact: true })
  await expect(otherToggle).not.toBeChecked()
  const observed = receipts(page)
  await openApp(page)
  await settings(page, 'Answers')
  const toggle = page.getByRole('switch', { name: ENGINE_LABEL, exact: true })
  await expect(toggle).not.toBeChecked()

  await page.locator('.ui-grouped-nav__item[data-item="endpoints"]').click()
  const endpointName = `Engine acceptance ${nonce}`
  await createAiEndpoint(page, {
    name: endpointName,
    baseUrl: fake.baseUrl(),
    models: ['e2e-model'],
    key: `engine-key-${nonce}`,
    vaultPassphrase: `engine-vault-${nonce}`,
  })
  await page.locator('.ui-grouped-nav__item[data-item="roles"]').click()
  await setDefaultModel(page, endpointName, 'e2e-model')
  await terminal(page)
  await promptReady(page)
  const sourceMarker = `ENGINE-CONTEXT-${nonce}`
  const command = `echo ${sourceMarker}`
  await page.locator(INPUT).fill(command)
  await page.keyboard.press('Enter')
  const sourceBlock = turn(page, command)
  await expect(sourceBlock).toHaveAttribute('data-outcome', 'success', { timeout: 15_000 })
  await expect(sourceBlock.locator('.cmd-output .term-grid-row').first()).toBeVisible()

  const defaultQuestion = `Which default engine answers ${nonce}?`
  fake.setScript({ chunks: ['Default Eino answer.'] })
  await ask(page, defaultQuestion)
  await complete(page, defaultQuestion, 'Default Eino answer.', 'eino')

  await settings(page, 'Answers')
  await toggle.check()
  await expect(toggle).toBeChecked()
  await page.locator('.ui-grouped-nav__item[data-item="endpoints"]').click()
  await page.getByRole('button', { name: `Edit ${endpointName}`, exact: true }).click()
  const edit = page.getByRole('dialog').filter({ hasText: 'Edit Endpoint' })
  fake.setScript({ chunks: ['Experimental endpoint test answer.'] })
  const probeCount = observed.filter(
    (r) => r.method === 'endpoints.probe' && r.result.kind === 'model',
  ).length
  await expect(edit.getByRole('button', { name: 'Test endpoint' })).toBeEnabled()
  await edit.getByRole('button', { name: 'Test endpoint' }).click()
  await expect(edit).toContainText(/e2e-model answered in \d+ ms/, { timeout: 30_000 })
  await expect
    .poll(
      () =>
        observed.filter((r) => r.method === 'endpoints.probe' && r.result.kind === 'model').length,
    )
    .toBe(probeCount + 1)
  const probe = observed
    .filter((r) => r.method === 'endpoints.probe' && r.result.kind === 'model')
    .at(-1)!
  expect(probe.result).toMatchObject({ engine: PYDANTIC, ok: true })
  expect(probe.result.runtimeVersion).toEqual(expect.any(String))
  expect(probe.result.runtimeVersion).not.toBe('')
  const runtimeVersion = probe.result.runtimeVersion as string
  await edit.getByRole('button', { name: 'Cancel', exact: true }).click()
  await terminal(page)

  // Approval remains on its originating window while Settings changes elsewhere.
  await expect(otherToggle).toBeChecked()

  const effectPath = join(root, `approved-effect-${nonce}`)
  const effectMarker = `GO-EFFECT-${nonce}`
  const effectCommand = `echo ${effectMarker} >> '${effectPath}'`
  const question = `Use this terminal block and write the approved marker ${nonce}.`
  const answer = `Approved command completed, ${nonce}.`
  const callId = `approved_call_${nonce}`
  fake.setScript({
    chunks: [],
    toolCalls: [{ id: callId, name: 'session_run', arguments: { command: effectCommand } }],
  })
  fake.setScript({
    chunks: (body) => {
      const request = JSON.parse(body) as { messages?: { role?: string; tool_call_id?: string }[] }
      return request.messages?.some((m) => m.role === 'tool' && m.tool_call_id === callId)
        ? [answer]
        : ['Missing original approved tool result.']
    },
  })
  const requestBase = fake.requests().length
  await pointAt(page, sourceBlock)
  await ask(page, question)
  const approval = page.getByRole('dialog', {
    name: 'This action needs your approval',
    exact: true,
  })
  await expect(approval).toBeVisible({ timeout: 30_000 })
  await expect(approval).toContainText(effectCommand)
  expect(existsSync(effectPath)).toBe(false)
  await expect(turn(page, question)).toHaveAttribute('data-engine', PYDANTIC)
  await expect(turn(page, question)).toHaveAttribute('data-runtime-version', runtimeVersion)
  expect(
    fake
      .requests()
      .slice(requestBase)
      .some((r) => r.body.includes(sourceMarker)),
  ).toBe(true)

  await otherToggle.uncheck()
  await expect(otherToggle).not.toBeChecked()
  await expect(approval).toBeVisible()
  expect(existsSync(effectPath)).toBe(false)
  await approval.getByRole('button', { name: /^Allow once/ }).click()
  await expect(approval).not.toBeVisible()
  await complete(page, question, answer, PYDANTIC)
  await expect
    .poll(() => (existsSync(effectPath) ? readFileSync(effectPath, 'utf8') : ''))
    .toBe(`${effectMarker}\n`)
  const effectBlock = turn(page, effectCommand)
  await expect(effectBlock).toHaveCount(1)
  await expect(
    effectBlock.locator(':scope > .cmd-header .ui-badge[data-author="agent"]'),
  ).toBeVisible()
  await expect(
    turn(page, question).getByText('Pydantic agent · Experimental', { exact: true }),
  ).toBeVisible()

  const fallbackQuestion = `After opting out, answer ${nonce}.`
  fake.setScript({ chunks: ['Next Ask uses Eino.'] })
  await ask(page, fallbackQuestion)
  await complete(page, fallbackQuestion, 'Next Ask uses Eino.', 'eino')

  await otherToggle.check()
  await expect(otherToggle).toBeChecked()
  const stopQuestion = `Stop this experimental answer ${nonce}.`
  const partial = `Experimental partial answer ${nonce}.`
  fake.setScript({ chunks: [partial, ' This suffix must never appear.'], holdAfter: 1 })
  await ask(page, stopQuestion)
  const stopped = turn(page, stopQuestion)
  await expect(stopped).toHaveAttribute('data-engine', PYDANTIC)
  await expect(stopped).toContainText(partial, { timeout: 30_000 })
  await stopped.locator(':scope > .cmd-header [data-block-actions]').click()
  await page.locator('.ui-context-menu__item[data-item-id="stop"]').click()
  await expect(stopped).toHaveAttribute('data-outcome', 'cancelled', { timeout: 30_000 })
  await expect(stopped).not.toContainText('This suffix must never appear.')
  fake.releaseAll()

  await expect
    .poll(
      async () => {
        const listed = (await ledger(page, 'ledger.query', {
          scope: 'everywhere',
          limit: 200,
        })) as { entries?: { id: string; intent: string }[] }
        const entry = listed.entries?.find((e) => e.intent === question)
        if (!entry) return false
        const detail = (await ledger(page, 'ledger.get', { id: entry.id })) as {
          caused?: { entryId: string; kind: string }[]
        }
        const prose = detail.caused?.find((e) => e.kind === 'text')
        if (!prose) return false
        const child = (await ledger(page, 'ledger.get', { id: prose.entryId })) as {
          artifacts?: { mediaType: string }[]
        }
        return child.artifacts?.some((a) => a.mediaType === 'text/plain') ?? false
      },
      { timeout: 60_000, message: 'approved answer must be persisted before restart' },
    )
    .toBe(true)

  await preferences.close()
  // stop() already waits for the owned process to exit. No timed restart sleep.
  backend.stop()
  endpoint = await backend.start(runtimeEnv)
  await bindEndpoint(page, endpoint)
  await page.reload()
  await appReadyForInput(page)
  const restored = turn(page, question)
  await expect(restored).toHaveCount(1, { timeout: 90_000 })
  await expect(restored).toHaveAttribute('data-restored', 'true', { timeout: 90_000 })
  await expect(restored).toHaveAttribute('data-engine', PYDANTIC)
  await expect(restored).toHaveAttribute('data-runtime-version', runtimeVersion)
  await expect(restored.getByText('Pydantic agent · Experimental', { exact: true })).toBeVisible()
  await expect(restored.getByText(answer, { exact: true })).toHaveCount(1)
  expect(readFileSync(effectPath, 'utf8')).toBe(`${effectMarker}\n`)
})

/**
 * e2e: a person reads every byte they are being asked about (nocx-872jc).
 *
 * WHAT THIS WATCHES. In the terminal, the assistant proposes a command that
 * names a script, and the approval window shows the WHOLE of that script
 * beside the verbatim command — labelled as a reading — while the command
 * that finally runs is byte-for-byte the one that was proposed, and nothing
 * gates the approval on having read anything.
 *
 * WHY IT CAN ONLY BE AN E2E CHECK. The reading is produced inside an
 * escalation, by a source that reaches the real filesystem provider for the
 * session the run belongs to, and there is no seam short of the whole product
 * where "the window shows this file" is a question that can be asked.
 *
 * THE PROPOSED COMMAND NAMES AN ABSOLUTE PATH, and that is a deliberate
 * narrowing. `bash deploy.sh` resolves against the run's cwd, and the run's cwd
 * is only an absolute directory once the pane's shell has reported one over
 * OSC 7 — a different capability's promise, whose absence would show up here as
 * an unreadable file and read like a defect in this window. An absolute path
 * asks this window's own question and cannot silently resolve to a different
 * file. So what is NOT proven here is the cwd-relative arm of
 * `absoluteScriptPath`; `internal/transport/ws_script_test.go` owns that.
 *
 * WHAT WOULD MAKE THIS LIE. A comparison against an element that is not there
 * passes by measuring nothing, so every `data-state` is asserted BEFORE the
 * text it qualifies. And a negative — "no checkbox gates the approval" —
 * passes at t=0 against a window that has not rendered yet, so the absence is
 * only ever asserted after the window is visible AND its approve button is
 * enabled.
 *
 * Every wait is an observable state change: a dialog, a `data-state`, a
 * recorded model request, a completed command block. This spec deliberately
 * contains no waitForTimeout.
 */
import { expect, type Locator, type Page } from '@playwright/test'
import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs'
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
import { FakeOpenAI } from './fake-openai'

const test = base
const nonce = Date.now().toString(36)

const TITLE = '.nocx-tab-title'
const INPUT = '.pane.active .nocx-editor-input'
const SETTINGS_AI_NAV = '.ui-grouped-nav__item[data-item="endpoints"]'
const SETTINGS_ROLES_NAV = '.ui-grouped-nav__item[data-item="roles"]'
const APPROVAL_TITLE = 'This action needs your approval'

const ENDPOINT_NAME = `E2E Bytes ${nonce}`

/** The script the PROPOSED COMMAND names. */
const RUN_MARKER = `deployed-${nonce}`
const DEPLOY_SCRIPT = [
  '#!/bin/sh',
  `# The deployment drill for ${nonce}.`,
  `echo "${RUN_MARKER}"`,
  '',
].join('\n')
const QUESTION = `Deploy the service, ${nonce}.`

let backend: VaultBackend
let fake: FakeOpenAI
let endpoint: { port: number; token: string }
/** The absolute path the proposed command writes, learnt from the backend's
 *  own disposable home once it has started. */
let deployPath = ''

test.describe.configure({ mode: 'serial' })
test.setTimeout(180_000)

test.beforeAll(async () => {
  fake = new FakeOpenAI()
  await fake.start()
  const root = mkdtempSync(join(tmpdir(), `nocx-872jc-e2e-${nonce}-`))
  backend = new VaultBackend(readStand().server, { root })
  endpoint = await backend.start()

  // The script the command will name, on the machine the command runs on.
  const workspace = join(backend.isolatedHome, 'workspace')
  mkdirSync(workspace, { recursive: true })
  deployPath = join(workspace, 'deploy.sh')
  writeFileSync(deployPath, DEPLOY_SCRIPT)
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

/** The assistant this spec needs, through the surfaces a person uses.
 *  Not what is under test — equipment — so it is the same arrangement
 *  agent-tool-approval.spec.ts makes. The policy is deliberately NOT touched:
 *  an unset policy is the zero matrix, which ASKS, and being asked is the
 *  whole of what this spec watches. */
async function configureAssistant(page: Page): Promise<void> {
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

/** The exact characters a `<pre>` holds. `toHaveText` normalises whitespace,
 *  and "the whole of that file" is a claim about bytes: a viewer that dropped
 *  a blank line or a trailing newline would be showing something other than
 *  the file, and normalised text cannot report it. */
function exactText(pre: Locator): Promise<string> {
  return pre.evaluate((el) => el.textContent ?? '')
}

/** One file's readout, addressed by the accessible name its own surface gives
 *  the block of bytes — which is also the assertion that the reader is
 *  labelling the file it is showing rather than the one that was asked for. */
// THE LABEL IS PASSED WHOLE. The approval window builds "What <path> holds
// right now" (agent-approval-prompt.tsx). This helper used to append
// ", verbatim" to whatever it was given, which made the selector end in a
// suffix the DOM never had; a locator that cannot match reports the same
// words as a control that is missing: "element(s) not found".
function readoutFor(scope: Locator, ariaLabel: string): { readout: Locator; pre: Locator } {
  const selector = `pre.ui-code-block[aria-label="${ariaLabel}"]`
  // The `has:` locator is resolved RELATIVE to each candidate, so it is built
  // off the page rather than off `scope` — a scoped one would carry its own
  // ancestor chain into the filter and match nothing.
  return {
    readout: scope.locator('.ui-file-readout').filter({ has: scope.page().locator(selector) }),
    pre: scope.locator(selector),
  }
}

test.describe('a person reads every byte they are being asked about (nocx-872jc)', () => {
  test.use({ viewport: { width: 1280, height: 900 } })

  test('read the script the proposed command names, and run what was proposed', async ({
    page,
  }) => {
    await openApp(page)
    await page.keyboard.press('Meta+,')
    await settingsReady(page)
    await configureAssistant(page)

    // ── The script the proposed command names ─────────────────────────────
    await page.locator(TITLE).first().click()
    await expect(page.locator(INPUT)).toBeVisible({ timeout: 10_000 })

    const command = `bash ${deployPath}`
    fake.setScript({
      chunks: [],
      toolCalls: [{ name: 'session.run', id: 'call_deploy', arguments: { command } }],
    })
    fake.setScript({ chunks: ['Done.'] })

    const requestBase = fake.requests().length
    await askFromPrompt(page, QUESTION)
    const proposal = await fake.waitForRequests(requestBase + 1)
    expect(proposal[requestBase].body).toContain('session.run')

    const prompt = page.getByRole('dialog', { name: APPROVAL_TITLE })
    await expect(prompt).toBeVisible({ timeout: 30_000 })

    // ── THE VERBATIM COMMAND IS ON SCREEN ─────────────────────────────────
    // Addressed by its BYTES rather than by the element that happens to carry
    // them, and that is deliberate twice over. The criterion asks that the
    // script be shown BESIDE THE COMMAND, which is a claim about what a person
    // can read and not about which kit component states it; and this window
    // currently states a one-line command as a fact row rather than as the
    // labelled block `agent-approval-prompt.tsx` writes for a command
    // proposal, because of the defect this file's header names. Reading the
    // command off the window — instead of comparing the run below against the
    // constant above — is what makes the last assertion in this test a
    // relationship rather than two constants agreeing.
    const shown = prompt.getByText(command, { exact: true })
    await expect(shown).toHaveCount(1, { timeout: 15_000 })
    const shownCommand = await exactText(shown)
    expect(shownCommand).toBe(command)

    // ── AND THE WHOLE OF THE FILE IT NAMES, BESIDE IT ─────────────────────
    const reading = readoutFor(prompt, `What ${deployPath} holds right now`)
    await expect(reading.readout).toHaveAttribute('data-state', 'text', { timeout: 20_000 })
    // The path is the one the COMMAND wrote, and the verb says what the
    // command does with the file — a person is owed the difference between
    // running a script and sourcing one.
    await expect(reading.readout).toContainText(deployPath)
    await expect(reading.readout).toContainText('the command runs this file as a script')
    expect(await exactText(reading.pre)).toBe(DEPLOY_SCRIPT)
    await expect(reading.readout.locator('input, textarea, [contenteditable="true"]')).toHaveCount(
      0,
    )
    // It is labelled as a READING and not as what runs. Without this the
    // window would be offering two things that look equally like the act.
    await expect(prompt).toContainText('This is a reading, not what is sent')

    // ── NOTHING IS COMPULSORY ─────────────────────────────────────────────
    // Asserted only now, with the window rendered and its answer enabled, so
    // it is a fact about the product rather than a race won before paint.
    // Not `exact`: the button's accessible name is its label plus the coverage
    // sentence the window appends to every answer, and the label is the part a
    // person reads off it.
    const allowOnce = prompt.getByRole('button', { name: 'Allow once' })
    await expect(allowOnce).toHaveCount(1)
    await expect(allowOnce).toBeEnabled()
    await expect(prompt.getByRole('checkbox')).toHaveCount(0)
    await expect(prompt.locator('input[type="checkbox"]')).toHaveCount(0)

    // ── AND WHAT RUNS IS WHAT WAS PROPOSED ────────────────────────────────
    // Approving is the FIRST interaction with this window: the reading was
    // never touched, scrolled or acknowledged.
    await allowOnce.click()

    const runBlock = page
      .locator('.pane.active .cmd-block[data-block-kind="command"]')
      .filter({ hasText: RUN_MARKER })
    await expect(runBlock).toHaveCount(1, { timeout: 60_000 })
    // Byte for byte the line the window showed — the reading beside it changed
    // nothing, which is the property the whole design rests on.
    expect(await exactText(runBlock.locator('.cmd-header-text').first())).toBe(shownCommand)
    // And it really ran the file that was read: the marker is the script's own
    // output, off the pane's real shell.
    await expect(runBlock.locator('.cmd-output')).toContainText(RUN_MARKER, { timeout: 30_000 })
  })
})

/**
 * e2e: a call BUILT ON an earlier one is marked in the finished turn, and an
 * adjacent independent call is not (nocx-d6gn4.9).
 *
 * WHY THIS FILE EXISTS, WHEN THREE SUITES ARE GREEN. The derivation edge
 * shipped three times and was invisible in the product all three times. Each
 * round had a Go test proving the backend recorded the edge, a contract test
 * proving it went over the real socket, and a vitest proving the renderer set
 * the mark — and each round was found by the owner running the product and
 * photographing a turn with no marker in it. The three defects were:
 *
 *   1. the matcher compared WHOLE argument values, so a command line — which
 *      never appears inside an earlier command's output — could never match;
 *   2. the mark was drawn in the child-drawing branch, which `run` (the one
 *      tool that opens a block of its own, and the tool the whole experiment
 *      turns on) never reaches;
 *   3. the mark was set on the RUNNING element, and `freezeBlock` REPLACES
 *      that element when the command exits — so it was true only while the
 *      command ran and gone by the time a person could read the turn.
 *
 * Every one of them lived in the same unchecked row: all of it together, in a
 * live browser, on a turn that has FINISHED. Each suite tested the seam its
 * author was writing at rather than the seam a person reads. This spec is that
 * seam and nothing else — the DOM of a completed turn.
 *
 * THE TURN THIS DRIVES. Four model rounds, three `run` calls, one real
 * dependency:
 *
 *     run  `ls <dir>`                       — discovers a file name
 *     run  `head -n 1 <dir>/<file>`         — DERIVED: its argument is the
 *                                             name the previous call printed
 *     run  `echo unrelated-<nonce>`         — adjacent and INDEPENDENT
 *     text                                   the final sentence
 *
 * The third call is the half that makes the second one evidence. ADR-0019 §2
 * says ingest order is commit order and not causality, so a marker that
 * appeared on every call in a row would say nothing at all; the acceptance
 * criterion is explicitly "two invocations are adjacent and NOT dependent, and
 * the record says so". Here that is a fact about pixels: the middle block
 * carries the mark and the two around it do not.
 *
 * THE DEPENDENCY IS REAL, NOT NARRATED. The fixture directory holds one file
 * whose name this spec never types into the second command as a literal — it
 * builds it from the same constant, and the CHAIN is what the product
 * executes: the first command's output is the file name, and the second
 * command's argument contains it. The backend's evidence rule is a verbatim
 * token of a later argument appearing in an earlier RESULT
 * (internal/assistant/derivation.go), so the second block is marked only if
 * the first command genuinely printed that name into a result the model was
 * given back.
 *
 * WHY THE POLICY ROW IS SET TO ALLOWED. `run` is mutate-destructive
 * (registry.go), and an unstated row asks. The approval prompt has its own
 * coverage (agent-tool-approval.spec.ts, agent-policy.spec.ts); asking three
 * times here would test that instead of this.
 *
 * PROVED ABLE TO FAIL, which is the only reason to believe a green run here
 * when three green suites meant nothing. Each of the three shipped defects was
 * reverted in turn on this tree and this spec was run against it in the
 * container; then a fourth mutant asked the opposite question. All four are
 * red, each at the assertion that owns it:
 *
 *   revert the freeze carry (blocks.ts, 5bacd223)   → data-derived "" at §2
 *   revert the claim-side mark (blocks.ts, 9e9b16b4) → data-derived "" at §2
 *   whole-value matcher (derivation.go, 72f4e1d2)    → data-derived "" at §2
 *   mark EVERY claimed block                         → §3, the neighbours
 *
 * The last one is what makes the first three mean anything: a spec that only
 * asserted the presence of a marker would stay green against a surface that
 * marked everything, and a marker on every call is the same as no marker.
 *
 * NOTHING HERE WAITS OUT A DURATION. Every wait is a poll on an observable
 * state change — the turn's own `completed` chip, an attribute on a frozen
 * block — never a sleep.
 */
import { test as base, expect, type Page } from '@playwright/test'
import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs'
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
/** BOTH rows, and the reason is the lowering. `run` is declared
 *  mutate-destructive (internal/agenttools/registry.go), but the class a call
 *  is judged under is the EFFECTIVE one after lowering — the approval prompt
 *  this spec first tripped over named `ls …` as "read and inspect", not as a
 *  destructive change. So a chain of read-only command lines asks on
 *  `observe`, and leaving that row unstated is how three scripted calls
 *  become three questions nobody answers. */
const OBSERVE_ROW = '.st-policy__row[data-effect="observe"]'
const DESTRUCTIVE_ROW = '.st-policy__row[data-effect="mutate-destructive"]'
const APPROVAL_TITLE = 'This action needs your approval'

const test = base
const nonce = Date.now().toString(36)

const ENDPOINT_NAME = `E2E Derived ${nonce}`

/** The fixture the chain discovers: one directory, one file, one line.
 *  Outside the backend's HOME on purpose — the commands name it absolutely,
 *  so the chain does not depend on where a fresh session's shell starts. */
const fixtureDir = mkdtempSync(join(tmpdir(), 'nocx-d6gn4-fixture-'))
const HARVEST_DIR = join(fixtureDir, `harvest-${nonce}`)
/** The name the FIRST command prints and the SECOND command is built from.
 *  Long enough to stand as evidence — the matcher drops tokens under six
 *  characters, so the words every command line shares cannot pass for one. */
const HARVEST_FILE = `harvested-${nonce}.txt`
/** What that file says — the second command's real output, and this spec's
 *  proof that the chain EXECUTED rather than merely being scripted. */
const HARVEST_LINE = `line-from-the-harvested-file-${nonce}`

/** 1. Discovery: prints the file name and nothing else. */
const CMD_LIST = `ls ${HARVEST_DIR}`
/** 2. DERIVED: its argument contains the name call 1 printed. */
const CMD_HEAD = `head -n 1 ${join(HARVEST_DIR, HARVEST_FILE)}`
/** 3. Adjacent and INDEPENDENT: no token of it occurs in any earlier result.
 *      `echo` is four characters and is dropped as evidence; the marker token
 *      is unique to this command. */
const INDEPENDENT_MARKER = `unrelated-${nonce}`
const CMD_ECHO = `echo ${INDEPENDENT_MARKER}`

const PROSE_AFTER = `That is the whole chain, ${nonce}.`
/** The turn under test. Every assertion is scoped to it — the confirming
 *  turn below also lives in the pane. */
const QUESTION = `What is in the harvest, ${nonce}?`
/** Asked FIRST purely to learn the session id the product itself spelled
 *  (AD-7). The policy's scope check compares a session resource for exact
 *  identity, so an invented id is refused before a call can run. */
const CONFIRM = `Are you there, ${nonce}?`

/** The hover text markDerived writes for a single edge
 *  (frontend/src/scrollback/blocks.ts) — the hedge the glyph cannot carry. */
const DERIVED_TITLE =
  'The arguments of this call appear in the result of an earlier call in this answer.'

let backend: VaultBackend
let fake: FakeOpenAI
let endpoint: { port: number; token: string }

test.describe.configure({ mode: 'serial' })

test.beforeAll(async () => {
  mkdirSync(HARVEST_DIR, { recursive: true })
  writeFileSync(join(HARVEST_DIR, HARVEST_FILE), `${HARVEST_LINE}\n`)
  fake = new FakeOpenAI()
  await fake.start()
  const root = mkdtempSync(join(tmpdir(), 'nocx-d6gn4-e2e-'))
  // `true` = no Secret Service for this backend: the container has no
  // keychain to ask, and the derived content key makes the vault available
  // without user setup.
  backend = new VaultBackend(devharnessBin(), { root }, true)
  endpoint = await backend.start()
})

test.afterAll(async () => {
  backend?.stop()
  await fake?.stop()
})

/** Every `agent.ask` this page sends, as it went over the socket — the
 *  session id the assistant's own lane used. Installed BEFORE the navigation:
 *  a listener attached afterwards would miss the socket the app opens on
 *  load. */
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

/** The one `.cmd-block` whose header IS the question. */
function turnBlock(page: Page, question: string) {
  return page.locator('.pane.active .cmd-block').filter({ hasText: question })
}

/**
 * Wait until the turn has FINISHED.
 *
 * The turn's OWN chip — `:scope > .cmd-header` — never a run child's `ok`
 * chip nested inside it (both are `.cmd-header-exit`). This is the
 * synchronisation point every assertion below stands on, and it is also the
 * defect: a completed turn is a turn whose command blocks have all been
 * FROZEN, and the freeze is what dropped the mark in round three. Asserting
 * before this would pass against a product that loses the marker a second
 * later.
 */
async function completed(page: Page, question: string): Promise<void> {
  await expect(
    turnBlock(page, question).locator(':scope > .cmd-header .cmd-header-exit'),
  ).toHaveText('completed', { timeout: 60_000 })
}

/** A command block of the turn, by the command its header shows. */
function commandBlock(page: Page, question: string, command: string) {
  return turnBlock(page, question)
    .locator(':scope > .cmd-children > .cmd-block[data-block-kind="command"]')
    .filter({ hasText: command })
}

/** The assistant is usable end to end, and `run` executes rather than asks. */
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
  for (const row of [OBSERVE_ROW, DESTRUCTIVE_ROW]) {
    const r = page.locator(row)
    await expect(r).toBeVisible({ timeout: 15_000 })
    await r.locator('select').first().selectOption({ label: 'Allowed' })
    await expect(r.locator('.st-policy__state')).toContainText('Allowed', { timeout: 15_000 })
  }
  await backToTerminal(page)
}

test.describe('a derived call says so in the finished turn (nocx-d6gn4.9)', () => {
  test.use({ viewport: { width: 1280, height: 900 } })

  test('the call built on an earlier result is marked; its neighbours are not', async ({
    page,
  }) => {
    // Four model rounds and three real commands, on top of creating the
    // endpoint, the vault and the policy through the surfaces a person uses.
    // A budget for the whole test, not a wait on a duration: every step below
    // still polls an observable state change.
    test.setTimeout(120_000)
    const asks = recordAskSessions(page)
    await openApp(page)
    await configureAssistant(page)

    // ── The session this person is working in, learned from the product's
    //    own first ask. Content-only: the fake has no script queued, so it
    //    answers with its default single 'ok' chunk.
    fake.setScript({ chunks: ['Yes, I am here.'] })
    await askFromPrompt(page, CONFIRM)
    await completed(page, CONFIRM)
    await expect.poll(() => asks.length, { timeout: 15_000 }).toBeGreaterThan(0)
    const sessionId = asks[asks.length - 1]
    expect(sessionId).not.toBe('')

    // ── THE turn: four model rounds. A run continues only while a response
    //    ends in a tool proposal, so the three calls ride three responses and
    //    the fourth closes the turn. Distinct call ids: each round's first
    //    call would otherwise default to `call_1`, and the renderer dedupes
    //    by that id — a reused id drops the later call.
    fake.setScript({
      chunks: [],
      toolCalls: [{ name: 'run', id: 'call_list', arguments: { sessionId, command: CMD_LIST } }],
    })
    fake.setScript({
      chunks: [],
      toolCalls: [{ name: 'run', id: 'call_head', arguments: { sessionId, command: CMD_HEAD } }],
    })
    fake.setScript({
      chunks: [],
      toolCalls: [{ name: 'run', id: 'call_echo', arguments: { sessionId, command: CMD_ECHO } }],
    })
    fake.setScript({ chunks: [PROSE_AFTER] })

    await askFromPrompt(page, QUESTION)
    await completed(page, QUESTION)
    // Executed straight through — asserted AFTER the terminal chip, so the
    // absence is a fact about the product and not a race won at t=0.
    await expect(page.getByRole('dialog', { name: APPROVAL_TITLE })).toHaveCount(0)

    const listBlock = commandBlock(page, QUESTION, CMD_LIST)
    const headBlock = commandBlock(page, QUESTION, CMD_HEAD)
    const echoBlock = commandBlock(page, QUESTION, CMD_ECHO)

    // ── 1. The chain really ran. The first command printed the file name,
    //       the second printed that file's line, the third printed its own
    //       marker. Without this the marker assertions below could pass over
    //       three commands that failed.
    await expect(listBlock.locator('.cmd-output')).toContainText(HARVEST_FILE)
    await expect(headBlock.locator('.cmd-output')).toContainText(HARVEST_LINE)
    await expect(echoBlock.locator('.cmd-output')).toContainText(INDEPENDENT_MARKER)

    // ── 2. THE POINT. The call whose argument came out of the previous
    //       call's result is MARKED, on the FROZEN block a person reads, with
    //       the hover text that carries the hedge.
    await expect(headBlock).toHaveAttribute('data-derived', '1')
    await expect(headBlock).toHaveAttribute('title', DERIVED_TITLE)

    // ── 3. And the mark MEANS something, which it would not if it appeared
    //       on every call in a row. The first call had nothing to derive
    //       from; the third stood beside a dependency without being one.
    await expect(listBlock).not.toHaveAttribute('data-derived')
    await expect(echoBlock).not.toHaveAttribute('data-derived')

    // ── The screenshot, because a screenshot is what found all three of the
    //    defects this file exists to close. Stored in test-results/
    //    (git-ignored, uploaded on red).
    const shotPath = join('test-results', `agent-derived-call-${nonce}.png`)
    await page.screenshot({ path: shotPath, fullPage: true })
    console.log(`E2E derived-call screenshot: ${shotPath}`)
  })
})

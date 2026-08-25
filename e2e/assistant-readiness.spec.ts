/**
 * e2e: a person reaches a working assistant without discovering Roles
 * unaided (nocx-rikz5) — the epic's proof, end to end.
 *
 * The sentence this file exists to make true: a person installs an endpoint,
 * chooses a model once, and asks — never having to find the Roles page on
 * their own, and never being told "Ready" by an assistant that cannot
 * answer.
 *
 * THE LOAD-BEARING ASSERTION IS THE MIDDLE ONE. Before this epic
 * `agentStatusLine` opened on `endpointConfigured`, so an endpoint with a
 * valid key reported **Ready** while no model had been chosen, and the
 * refusal arrived at the person's first question. A spec that only walked
 * the happy path would have been green through all of that. So the run here
 * stops at the state between the two writes — endpoint saved, model
 * unchosen — and asserts the chip reads "Choose a model" and that NOTHING
 * in the row resembles readiness. That state is observable at all only
 * because `createAiEndpoint` (harness.ts) deliberately stops at the
 * endpoint: until this epic there was no way to have one without the other.
 *
 * WHAT ONLY A REAL ENGINE CAN SAY. The second test carries the layout claim
 * Task 6 could not make in vitest: jsdom computes no layout, so
 * `getBoundingClientRect` returns zeroes there and a height assertion would
 * have passed while meaning nothing. Here a real engine measures — the
 * chrome row's height in Run, then the same row in Ask with a forty-
 * character model id in it — and the claim is that the composer does not
 * grow a second line.
 *
 * THE FIXTURES ARE THE SUITE'S, not new ones. `FakeOpenAI`
 * (e2e/fake-openai.ts) is a real local server answering
 * `/chat/completions`: without it there is no model to answer and "an
 * answer arrives" cannot be made true by `cmd/devharness` alone.
 * `VaultBackend` is this file's OWN devharness on a disposable home, so the
 * first state a person is ever in — no endpoints at all — is real here and
 * nothing this file configures leaks into the shard's shared stand.
 *
 * `bindEndpoint` is the PAGE→backend binding and nothing else; the AI
 * endpoint is created through the dialog a person uses (`createAiEndpoint`).
 * The two names collide and the plan for this task read one as the other.
 *
 * SERIAL, AND THE SECOND TEST USES THE FIRST'S ENDPOINT — one backend, one
 * home, in order (the arrangement agent-ask.spec.ts already runs on). The
 * endpoint is created once, offering two models: the short one the first
 * test chooses and the forty-character one the second test measures.
 *
 * Every wait below is on an observable state change — a chip's text, a row's
 * badge, a request arriving at the fake — never on a duration.
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
  type BackendEndpoint,
} from './harness'
import { readStand } from './stand'
import { FakeOpenAI } from './fake-openai'

/** Lazily, not at module scope: the stand is started by globalSetup, which
 *  runs after Playwright has collected this file. */
const devharnessBin = () => readStand().devharness

const TITLE = '.nocx-tab-title'
const INPUT = '.pane.active .nocx-editor-input'
/** The model chips, VISIBLE ones only. Both buttons live in the chrome row
 *  at all times and are hidden with `display: none` (editor.ts
 *  setModelChip), so a bare class locator would match two elements in every
 *  state and strict mode would fail the wait rather than the assertion.
 *  Filtering to what is on screen also makes the COUNT meaningful: one chip
 *  is a rung of the ladder, two is the ready pair. */
const CHIPS = `.pane.active .nocx-editor-model:visible`

const test = base

/** One nonce per file: unique in the whole run, so a leftover from another
 *  file can never satisfy an assertion here. */
const nonce = Date.now().toString(36)

const ENDPOINT_NAME = `Readiness Fake ${nonce}`
const MODEL = 'e2e-model'
/** Forty characters, asserted below rather than counted by eye: the chip's
 *  truncation (style.css .nocx-editor-model) exists for ids this long, and
 *  the claim under test is that one of them does not wrap the row. */
const LONG_MODEL = 'openrouter/qwen3-235b-a22b-thinking-2507'

let backend: VaultBackend
let fake: FakeOpenAI
let endpoint: BackendEndpoint

test.describe.configure({ mode: 'serial' })

test.beforeAll(async () => {
  fake = new FakeOpenAI()
  await fake.start()
  const root = mkdtempSync(join(tmpdir(), 'nocx-rikz5-e2e-'))
  // `true` = no Secret Service for this backend, whatever the session around
  // the suite is running: the container has no keychain to ask, and stating
  // the premise explicitly is what keeps this arrangement identical on both
  // platforms (the same reason agent-ask.spec.ts passes it).
  backend = new VaultBackend(devharnessBin(), { root }, true)
  endpoint = await backend.start()
})

test.afterAll(async () => {
  backend?.stop()
  await fake?.stop()
})

/** Point the page at THIS file's backend, open the app, wait for the first
 *  tab. */
async function openApp(page: Page): Promise<void> {
  await bindEndpoint(page, endpoint)
  await page.goto('/')
  await expect(page.locator(TITLE).first()).not.toHaveText('', { timeout: 15_000 })
}

/** Send Enter to the ASSISTANT rather than the shell: ⌘/Ctrl+Enter flips
 *  where Enter goes and the indicator is the confirmation. Idempotent, as a
 *  person experiences it — with Ask already active the target stays. */
async function switchToAsk(page: Page): Promise<void> {
  const input = page.locator(INPUT)
  await expect(input).toBeVisible({ timeout: 15_000 })
  await input.click()
  // `:visible` on purpose: CM6 keeps a hidden measurement spacer beside the
  // real marker, carrying an identical button. The visible one is the
  // person's.
  const indicator = page.locator('.pane.active .ui-mode-indicator:visible')
  if ((await indicator.getAttribute('data-target')) !== 'agent') {
    await page.keyboard.press('ControlOrMeta+Enter')
    await expect(indicator).toHaveAttribute('data-target', 'agent', { timeout: 10_000 })
  }
}

/** Back to the terminal tab. Settings is a tab like any other and the first
 *  tab is the terminal; coming back to the front is also the moment the
 *  pane re-reads `agent.status` (terminal-content setVisible), which is how
 *  the chip catches up with what was just configured on another pane. */
async function backToTerminal(page: Page): Promise<void> {
  await page.locator(TITLE).first().click()
  await expect(page.locator(INPUT)).toBeVisible({ timeout: 10_000 })
}

/** Open Settings on one of the rail's pages, the way a person does. */
async function openSettingsPage(page: Page, item: 'endpoints' | 'roles'): Promise<void> {
  await page.keyboard.press('Meta+,')
  await settingsReady(page)
  await page.locator(`.ui-grouped-nav__item[data-item="${item}"]`).click()
}

/** Type a question and send it — Ask is already the target. */
async function askAQuestion(page: Page, question: string): Promise<void> {
  const input = page.locator(INPUT)
  await input.click()
  await input.fill(question)
  await page.keyboard.press('Enter')
}

test.describe('the assistant says what it needs, one rung at a time (nocx-rikz5)', () => {
  test.use({ viewport: { width: 1280, height: 900 } })

  test('a person reaches a working assistant without discovering Roles unaided', async ({
    page,
  }) => {
    await openApp(page)
    await switchToAsk(page)
    const chips = page.locator(CHIPS)

    // ── Rung one: nothing is configured ─────────────────────────────────
    // Not "choose a model": sending a person to an empty picker is the one
    // answer worse than saying nothing.
    await expect(chips).toHaveText(['Add an endpoint first'])

    // The chip is a DOOR, not a label. Clicking it lands on Endpoints with
    // a blank endpoint up — a sentence with nowhere to go is how a person
    // concludes the feature is broken rather than unconfigured.
    await chips.first().click()
    await expect(page.locator('.ep-root')).toBeVisible({ timeout: 15_000 })
    await expect(page.getByRole('dialog').filter({ hasText: 'New Endpoint' })).toBeVisible({
      timeout: 15_000,
    })

    await createAiEndpoint(page, {
      name: ENDPOINT_NAME,
      // Loopback, which is exactly the address rule
      // internal/assistant/httpguard.go permits.
      baseUrl: fake.baseUrl(),
      models: [MODEL, LONG_MODEL],
      key: `e2e-key-${nonce}`,
      vaultPassphrase: `vault-pass-${nonce}`,
    })
    await backToTerminal(page)

    // ── THE ASSERTION THIS EPIC EXISTS FOR ──────────────────────────────
    // An endpoint with a valid key is NOT readiness. Before this work the
    // line here read "Ready" and the refusal arrived at the first question.
    await expect(chips).toHaveText(['Choose a model'])
    // And it says nothing that resembles ready — the count above already
    // rules out the ready PAIR, and this rules out the word wherever it
    // might come from. Both halves matter: the defect was a true-looking
    // sentence, not a missing one.
    await expect(page.locator(CHIPS, { hasText: /ready/i })).toHaveCount(0)

    // ── The one choice the ladder leads to ──────────────────────────────
    // Through the chip's OWN destination: this is the whole claim about
    // never having to discover the Roles page unaided.
    await chips.first().click()
    await expect(page.locator('.roles-default')).toBeVisible({ timeout: 15_000 })
    await setDefaultModel(page, ENDPOINT_NAME, MODEL)
    await backToTerminal(page)

    // Ready means the model is NAMED, not that a box was ticked: the pair
    // that will answer, in the order the row shows it.
    await expect(chips).toHaveText([ENDPOINT_NAME, MODEL])

    // ── And the answer arrives ──────────────────────────────────────────
    // The fake is scripted so the answer's TEXT is decided by this test; a
    // container merely becoming visible is exactly what a broken stream
    // produces. The request base is captured before the ask because the
    // endpoint form's own probes have already reached the fake.
    const answer = `The default model answered ${nonce}.`
    const base = fake.requests().length
    fake.setScript({ chunks: ['The default model ', `answered ${nonce}.`] })
    await askAQuestion(page, 'hello')

    const answerBlock = page.locator('[data-answered-by]')
    await expect(answerBlock).toContainText(answer, { timeout: 15_000 })
    // Which model answered is the run's PINNED fact (nocx-e6kn2), and it is
    // the one the chip named — the default actually drove the ask rather
    // than some other pair resolving underneath it.
    await expect(answerBlock).toHaveAttribute('data-answered-by', MODEL)

    // The same fact at the other end of the wire: the request the backend
    // sent names that model, and carries the key as the Bearer it was
    // stored as.
    const reqs = await fake.waitForRequests(base + 1)
    const ask = reqs[base]
    expect(ask.path.endsWith('/chat/completions')).toBe(true)
    expect(ask.body).toContain(`"model":"${MODEL}"`)
    expect(ask.authorization).toBe(`Bearer e2e-key-${nonce}`)
  })

  test('the model chip does not add a line to the composer', async ({ page }) => {
    // THE LAYOUT CLAIM jsdom CANNOT MAKE (Task 6). A real engine measures
    // here: in jsdom getBoundingClientRect returns zeroes, so this would
    // have passed while meaning nothing.
    expect(LONG_MODEL).toHaveLength(40)

    await openApp(page)
    // Move the default to the long id — the endpoint from the first test
    // offers it. Roles is reached the ordinary way here; the chip-as-door
    // claim belongs to the test above.
    await openSettingsPage(page, 'roles')
    await setDefaultModel(page, ENDPOINT_NAME, LONG_MODEL)
    await backToTerminal(page)

    const chrome = page.locator('.pane.active .nocx-editor-chrome')
    const cwd = page.locator('.pane.active .nocx-editor-cwd')
    // Run: no chip at all, because no model answers anything here.
    await expect(page.locator(CHIPS)).toHaveCount(0)
    const before = (await chrome.boundingBox())?.height

    await switchToAsk(page)
    const chips = page.locator(CHIPS)
    await expect(chips).toHaveText([ENDPOINT_NAME, LONG_MODEL])

    // The row is one chip row, always (style.css .nocx-editor-chrome): two
    // chips appearing must not move the composer, because the scrollback
    // hangs from it.
    expect((await chrome.boundingBox())?.height).toBe(before)
    // One line, ellipsised — the chip is exactly as tall as the chip that
    // was already in the row. A wrapped id would be taller and the height
    // above would have moved with it.
    expect((await chips.last().boundingBox())?.height).toBe((await cwd.boundingBox())?.height)
  })
})

/**
 * e2e: a person stops being asked, and can start being asked again
 * (nocx-fc4ab) — the check that closes the agent-policy epic.
 *
 * WHAT THIS FILE IS FOR. Eight tasks built the feature and every one of them
 * is unit-green; none of that is evidence that a person can use it
 * (AGENTS.md testing rule 2). The sentence the epic is judged on is one
 * journey through the real backend, and it is the first test here:
 *
 *   A person asks the assistant about a failed build. The assistant asks to
 *   read the screen; they answer Allow always. They ask a second question in
 *   the same pane and are NOT asked again. Settings → Agent policy shows the
 *   standing decision; they revoke it; the next question asks again.
 *
 * The second test is the other half of the spec's interval — an answer given
 * "in this session" is in force from the moment it is given and gone from the
 * moment that session ends — because an invariant with only one end is a
 * moment, not an interval (AGENTS.md testing rule 3).
 *
 * The seam, and where each half is decided:
 *
 * - The PROPOSAL is scripted: `e2e/fake-openai.ts` writes one
 *   `delta.tool_calls` frame naming `session.read`. `session.read` is declared
 *   with `Effect: observe` and `ResourceArg: "sessionId"`
 *   (internal/agenttools/registry.go), so the escalation is a POLICY ask
 *   over the `observe` row — which is the row Settings → Agent policy draws
 *   as "Read and inspect" and the prompt says as "read and inspect"
 *   (frontend/src/effect-labels.ts, one owner of both).
 * - The SESSION ID IS LEARNED, NEVER INVENTED, for the reason
 *   agent-tool-approval.spec.ts records at length: the policy's scope check
 *   compares a session resource for exact identity against the run's grant
 *   scope (internal/assistant/policy.go inScope), so a made-up id is refused
 *   BEFORE it can ask and the prompt would never appear. Every ask below
 *   therefore reads the id off the real socket first.
 * - The WIDTH of an answer is applied by the BACKEND
 *   (internal/transport/ws_agent.go applyStandingAnswer): "always" writes one
 *   row of the global matrix through the same store the settings page writes
 *   through; "in this session" writes the per-session overlay that every
 *   session teardown drops (ws_sessionpolicy.go). This spec never touches
 *   either store — it drives the two surfaces a person drives, which is the
 *   only way the wiring between them can be observed at all.
 *
 * NOTHING HERE WAITS OUT A DURATION (AGENTS.md). The subtle one is asserting
 * that the prompt does NOT appear: a bare negative assertion passes at t=0,
 * before the run has even reached the gate, and would go on passing if the
 * feature were deleted. So the absence is only ever asserted AFTER the
 * answer block has gained its `completed` chip — the run terminalized, so
 * the gate has been passed rather than not yet reached.
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

/** Lazily, not at module scope: the stand is started by globalSetup, which
 *  runs after Playwright has collected this file. */
const devharnessBin = () => readStand().devharness

const TITLE = '.nocx-tab-title'
const INPUT = '.pane.active .nocx-editor-input'
const NEW_TAB = '[aria-label="New tab"]'
const SETTINGS_AI_NAV = '.ui-grouped-nav__item[data-item="endpoints"]'
const SETTINGS_ROLES_NAV = '.ui-grouped-nav__item[data-item="roles"]'
const SETTINGS_POLICY_NAV = '.ui-grouped-nav__item[data-item="policy"]'
/** The approval prompt is a kit Prompt: a role="dialog" carrying the title
 *  AgentApprovalPrompt gives a POLICY question. Named, not bare: Settings is
 *  a tab rather than a dialog, but the vault sheets and the confirmations are
 *  dialogs too, and "no dialog is open" is not the claim being made — "the
 *  person was not asked to approve" is. */
const APPROVAL_TITLE = 'This action needs your approval'
/** The row of the matrix `session.read` is classified under. */
const OBSERVE_ROW = '.st-policy__row[data-effect="observe"]'

const test = base

/** One nonce per file, so this file's endpoint name is unique in the whole
 *  run. */
const nonce = Date.now().toString(36)

let backend: VaultBackend
let fake: FakeOpenAI
let endpoint: { port: number; token: string }

// Serial: the second test reuses the endpoint, the model role and the vault
// the first one created through the surfaces — and it also depends on the
// first having put the `observe` row BACK to "Ask every time", which is its
// last assertion. If the first test fails, the second is skipped rather than
// run against a policy nobody knows the shape of.
test.describe.configure({ mode: 'serial' })

test.beforeAll(async () => {
  fake = new FakeOpenAI()
  await fake.start()
  const root = mkdtempSync(join(tmpdir(), 'nocx-fc4ab-e2e-'))
  // `true` = no Secret Service for this backend: the container has no
  // keychain to ask, and the derived content key makes the vault available
  // without user setup — the arrangement agent-ask.spec.ts uses.
  backend = new VaultBackend(devharnessBin(), { root }, true)
  endpoint = await backend.start()
})

test.afterAll(async () => {
  backend?.stop()
  await fake?.stop()
})

/**
 * The session id of every `agent.ask` this page sends, in order, as it went
 * over the socket.
 *
 * Installed BEFORE the navigation, because a listener attached afterwards
 * would miss the socket the app opens on load. The control plane is JSON text
 * and the data plane is binary (AD-1), so the string check is also the plane
 * filter.
 */
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

/** Point the page at this file's backend, open the app, wait for the first
 *  tab. */
async function openApp(page: Page): Promise<void> {
  await bindEndpoint(page, endpoint)
  await page.goto('/')
  await expect(page.locator(TITLE).first()).not.toHaveText('', { timeout: 15_000 })
}

/** Send the drafted line to the ASSISTANT: ⌘/Ctrl+Enter flips where Enter
 *  goes, then Enter is the one send key. Idempotent on the flip, and scoped
 *  to the ACTIVE pane — this file asks in two different panes. */
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

/**
 * Wait until the answer to `question` has FINISHED.
 *
 * The completion chip, not the body text and not the block's existence: the
 * block is created when the question is sent, and its body fills as the
 * stream arrives, so neither says the run reached a terminal state. The chip
 * is the surface's own word for "the answer finished" (scrollback/blocks.ts),
 * and a run that was suspended at the policy gate never gets one.
 *
 * This is the synchronisation point every "and was not asked" below stands
 * on. Without it the negative assertion would be a race the test always wins
 * for the wrong reason.
 */
async function answerFinished(page: Page, question: string): Promise<void> {
  const block = page.locator('.cmd-block').filter({ hasText: question })
  await expect(block.locator('.cmd-header-exit')).toHaveText('completed', { timeout: 30_000 })
}

/**
 * Ask a content-only question and hand back the session id it was asked in.
 *
 * Two jobs in one gesture, and the second is why it exists: a scripted
 * `session.read` must name the run's OWN session, and that id is
 * server-authoritative (AD-7), so the only honest source is a frame the
 * product itself sent. The fake has no script queued for this one, so it
 * answers with its default single 'ok' chunk.
 */
async function askAndLearnSession(page: Page, asks: string[], question: string): Promise<string> {
  const before = asks.length
  await askFromPrompt(page, question)
  await answerFinished(page, question)
  await expect.poll(() => asks.length, { timeout: 15_000 }).toBeGreaterThan(before)
  const sessionId = asks[asks.length - 1]
  expect(sessionId).not.toBe('')
  return sessionId
}

/** The approval prompt, by the title a policy question carries. */
function approvalPrompt(page: Page) {
  return page.getByRole('dialog', { name: APPROVAL_TITLE })
}

/** Open Settings and select a page in the rail — Settings is a tab like any
 *  other, and the keyboard shortcut is how a person gets there. */
async function openSettings(page: Page, navSelector: string): Promise<void> {
  await page.keyboard.press('Meta+,')
  await settingsReady(page)
  await page.locator(navSelector).click()
}

/** Back to the terminal, which is the first tab. */
async function backToTerminal(page: Page): Promise<void> {
  await page.locator(TITLE).first().click()
  await expect(page.locator(INPUT)).toBeVisible({ timeout: 10_000 })
}

/** Create the endpoint and give its model the answering role, through the
 *  surfaces a person uses. A fresh home has no vault, so the first save stops
 *  on the setup sheet and is retried once the vault exists; createAiEndpoint
 *  reads which of the two happened rather than assuming. */
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

const ENDPOINT_NAME = `E2E Policy ${nonce}`

test.describe('a person answers "stop asking me this", and can undo it (nocx-fc4ab)', () => {
  test.use({ viewport: { width: 1280, height: 900 } })

  test('Allow always stops the question, and the Agent policy page brings it back', async ({
    page,
  }) => {
    const asks = recordAskSessions(page)
    await openApp(page)
    await configureAssistant(page, ENDPOINT_NAME)
    await backToTerminal(page)

    // ── The session this person is working in, learned from the product's
    // own first ask. Content-only, so it also re-establishes that a script
    // without toolCalls behaves exactly as it always did.
    const sessionId = await askAndLearnSession(page, asks, 'What is on the screen right now?')

    // ── 1. The first question escalates: nothing has been decided about
    // `observe`, and an unstated row asks.
    fake.setScript({ chunks: [], toolCalls: [{ name: 'session.read', arguments: { sessionId } }] })
    await askFromPrompt(page, 'What went wrong in that build?')
    const prompt = approvalPrompt(page)
    await expect(prompt).toBeVisible({ timeout: 30_000 })

    // ── 2. It names the effect in the PRODUCT's words, not the tool's — the
    // words the standing answer will later be shown in.
    await expect(prompt).toContainText('read and inspect')
    await expect(prompt).toContainText('session.read')

    // ── 3. Allow always. The suspended run resumes on the same binding, the
    // tool runs, and the answer finishes.
    await prompt.getByRole('button', { name: 'Allow always' }).click()
    await answerFinished(page, 'What went wrong in that build?')

    // ── 4. The second question in the same pane is NOT asked about. The
    // fake proposes exactly the same call; the standing answer is what
    // decides it this time. The chip is waited for FIRST — the run reached a
    // terminal state, so the gate was passed and not merely not-yet-reached
    // — and only then is the prompt's absence a fact about the product.
    fake.setScript({ chunks: [], toolCalls: [{ name: 'session.read', arguments: { sessionId } }] })
    await askFromPrompt(page, 'And if I fix the type?')
    await answerFinished(page, 'And if I fix the type?')
    await expect(approvalPrompt(page)).toHaveCount(0)

    // ── 5. The page shows the standing decision, in the same words the
    // question used. Asserted on the row's STATE line, never on the row: a
    // row's text contains every option of its select, so "the row mentions
    // Allowed" is true whatever the row is set to.
    await openSettings(page, SETTINGS_POLICY_NAV)
    const observeRow = page.locator(OBSERVE_ROW)
    await expect(observeRow).toBeVisible({ timeout: 15_000 })
    await expect(observeRow.locator('select').first()).toHaveValue('permit')
    await expect(observeRow.locator('.st-policy__state')).toContainText(
      'Read and inspect — Allowed',
    )

    // ── 6. Revoking is the SAME control, not a second one. There is no Save
    // button: the select writes, and the page adopts what a fresh read
    // answers — so the state line going away is the store's answer, never
    // the draft's.
    await observeRow.locator('select').first().selectOption({ label: 'Ask every time' })
    await expect(observeRow.locator('.st-policy__state')).toHaveCount(0, { timeout: 15_000 })
    await expect(observeRow.locator('select').first()).toHaveValue('ask')

    // ── 7. And the question comes back on the next one.
    await backToTerminal(page)
    fake.setScript({ chunks: [], toolCalls: [{ name: 'session.read', arguments: { sessionId } }] })
    await askFromPrompt(page, 'And now what should I try?')
    await expect(approvalPrompt(page)).toBeVisible({ timeout: 30_000 })
    // Left answered rather than hanging: the run is terminalized by the
    // narrowest refusal, which is what Escape and the scrim also send.
    await prompt.getByRole('button', { name: 'Deny once' }).click()
    await expect(approvalPrompt(page)).toHaveCount(0, { timeout: 15_000 })
  })

  test('an allow given in one session does not outlive that session', async ({ page }) => {
    const asks = recordAskSessions(page)
    await openApp(page)
    await backToTerminal(page)

    const sessionA = await askAndLearnSession(page, asks, 'What is in this pane?')

    // ── Asked, because the previous test put the row back to "Ask every
    // time" and nothing else has been decided.
    fake.setScript({
      chunks: [],
      toolCalls: [{ name: 'session.read', arguments: { sessionId: sessionA } }],
    })
    await askFromPrompt(page, 'Why did that command fail?')
    const prompt = approvalPrompt(page)
    await expect(prompt).toBeVisible({ timeout: 30_000 })

    // ── Allow in this session: the narrower standing answer. It writes the
    // session's overlay, never the matrix.
    await prompt.getByRole('button', { name: 'Allow in this session' }).click()
    await answerFinished(page, 'Why did that command fail?')

    // ── In force for the rest of THIS session.
    fake.setScript({
      chunks: [],
      toolCalls: [{ name: 'session.read', arguments: { sessionId: sessionA } }],
    })
    await askFromPrompt(page, 'What about the second error?')
    await answerFinished(page, 'What about the second error?')
    await expect(approvalPrompt(page)).toHaveCount(0)

    // ── ...and gone with the session. A new tab is a new terminal session,
    // and the session the answer was given in is then CLOSED — the teardown
    // that drops the overlay (ws.go → sessionPolicy.Drop). Both halves are
    // needed: opening a second session shows the answer does not reach it,
    // and closing the first is the event the permission promised not to
    // outlive.
    //
    // Nothing else is open at this point ON PURPOSE. Settings is a tab like
    // any other and it is opened at the END of this test rather than here:
    // with one terminal and one Settings tab, "close the tab that holds the
    // session" becomes a question about which tab is which, and a test that
    // has to reason about tab order is a test that will one day close the
    // wrong one.
    await page.locator(NEW_TAB).click()
    await expect(page.locator('.nocx-tab')).toHaveCount(2, { timeout: 15_000 })
    // The FIRST tab is the one session A belongs to — the tab strip's DOM
    // order is creation order — and closing it is the ordinary gesture: the
    // close control on the tab.
    await page.locator('.nocx-tab').first().locator('[aria-label="Close tab"]').click()
    await expect(page.locator('.nocx-tab')).toHaveCount(1, { timeout: 15_000 })
    await expect(page.locator(INPUT)).toBeVisible({ timeout: 15_000 })

    const sessionB = await askAndLearnSession(page, asks, 'What is in this new pane?')
    // The premise of everything below: this really is another session.
    expect(sessionB).not.toBe(sessionA)

    // ── The question comes back. Same tool, same effect, a session that was
    // never answered for.
    fake.setScript({
      chunks: [],
      toolCalls: [{ name: 'session.read', arguments: { sessionId: sessionB } }],
    })
    await askFromPrompt(page, 'Why did that command fail again?')
    await expect(approvalPrompt(page)).toBeVisible({ timeout: 30_000 })
    // Answered rather than left hanging: the narrowest refusal, which is what
    // Escape and the scrim send too.
    await approvalPrompt(page).getByRole('button', { name: 'Deny once' }).click()
    await expect(approvalPrompt(page)).toHaveCount(0, { timeout: 15_000 })

    // ── And none of it reached the global matrix: the row a standing
    // "always" would have flipped is still on the default. This is the half
    // that tells the two widths apart — without it, "in this session" and
    // "always" would look identical from inside the session they were given
    // in.
    await openSettings(page, SETTINGS_POLICY_NAV)
    const observeRow = page.locator(OBSERVE_ROW)
    await expect(observeRow).toBeVisible({ timeout: 15_000 })
    await expect(observeRow.locator('select').first()).toHaveValue('ask')
    await expect(observeRow.locator('.st-policy__state')).toHaveCount(0)
  })
})

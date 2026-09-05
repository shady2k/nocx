/**
 * e2e: a person answers a question once, and can take the answer back
 * (nocx-0dzqq) — the check that closes nocx-4yjwk.
 *
 * WHAT THIS FILE IS FOR. Thirteen tasks built the feature and every one of
 * them is green in its own layer; none of that is evidence that a person can
 * use it (AGENTS.md testing rule 2). The sentence the epic is judged on is one
 * journey through the real backend, and it is the only test here:
 *
 *   A person asks the assistant about disk space. It proposes `df -h`; they
 *   answer Allow always. The scrollback says what was saved. They ask again in
 *   a new session and are not asked. Settings → Assistant permissions shows
 *   the answer; they widen it to *any `df` command* and `df --output=source`
 *   then runs unasked. They write a refusal — *any command that writes a file
 *   to a path named by an option* — and `curl -o /tmp/proof https://example.com`
 *   is refused, with the page naming which answer refused it. They forget
 *   both, and are asked again.
 *
 * IT IS ONE TEST BECAUSE IT IS ONE SENTENCE. Every step is a claim about the
 * state the step before it left behind — a widening is only a widening if the
 * same command asked a moment ago, and a refusal is only a refusal if the same
 * call was being ASKED about before it was written. Split into six tests those
 * premises would be assumed rather than watched, which is the failure this
 * whole file exists to rule out.
 *
 * EACH HALF IS ESTABLISHED BEFORE IT IS CHANGED, and that is the load-bearing
 * discipline. `df --output=source` is proposed and ASKED about before the
 * widening, and `curl -o …` is proposed and ASKED about before the refusal is
 * written; both are answered `Deny once`, which decides that proposal and
 * writes nothing anywhere. Without those two rounds, "it runs unasked" and
 * "it is refused" would each also be true of a product where the standing
 * answers did nothing and something else — a fence, a row, a bug — decided it.
 *
 * THE SEAMS, AND WHY EACH IS THE ONE A PERSON REACHES:
 *
 * - The PROPOSAL is scripted: `e2e/fake-openai.ts` writes one
 *   `delta.tool_calls` frame naming `session.run`, whose `command` is the
 *   whole of what the policy reads. `df -h` and `df --output=source` classify
 *   `observe`; `curl -o /tmp/proof https://example.com` classifies
 *   `cross-boundary` and carries the one semantic feature content's closed
 *   vocabulary has — `writes-option-named-path` (internal/content/rules.go).
 *   None of those three is derived here: they are the classifier's, and the
 *   words this file asserts are the product's own (frontend/src/effect-labels.ts).
 * - THE COMMAND REALLY RUNS, and the proof is the model's own prose. Each
 *   follow-up script is a FUNCTION of the request body and says its sentence
 *   only when `df`'s real output came back through the tool result. A fixed
 *   string would say it with no command having run at all.
 * - THE REFUSAL IS READ OFF THE WIRE the same way: the follow-up prose is
 *   emitted only when a `REFUSED` tool result reached the model, so "it was
 *   refused" is a fact about the backend rather than about this file's hopes.
 *   `/tmp/proof` is checked for absence beside it — corroboration, not the
 *   claim: `curl -o` creates its output file before it ever reaches the
 *   network, so a curl that ran would leave one behind.
 * - THE STANDING ANSWERS are written only through the two surfaces a person
 *   uses — the approval prompt's six buttons, and the Assistant permissions
 *   page's `+ Allow a command…` / `+ Write a refusal` / `Forget`. Nothing here
 *   touches `policy.set`, `policy.setRule` or the store; the wiring between
 *   the prompt and the page is exactly what would be unobservable if it did.
 *
 * NOTHING WAITS OUT A DURATION (AGENTS.md). The subtle one is asserting that
 * the prompt does NOT appear: a bare negative assertion passes at t=0, before
 * the run has reached the gate, and would go on passing if the feature were
 * deleted. So every absence below is asserted only AFTER the turn has gained
 * its `completed` chip — the run terminalized, so the gate was passed rather
 * than not yet reached — and, for the two runs that must execute, after the
 * command block has its own exit chip.
 *
 * WHAT COULD NOT BE ASSERTED, and why it is written down rather than quietly
 * dropped. "The page names which answer refused it" is asserted as the page
 * LISTING that answer — the sentence, the `Never` badge, and the same facts
 * restated in its own `Why` panel. The backend's decision TRACE is not
 * asserted over the refused command line, because no surface lets a person ask
 * for one: `Why` asks `policy.explain` about the answer's own subject, and a
 * feature answer's subject is the bare command word (`curl`), which carries no
 * feature and therefore matches nothing. That is a real gap in the surface,
 * not a gap in this check, and it is a defect of the page rather than of the
 * evaluator.
 *
 * ═══ THE ONE DEFECT THIS FILE FOUND, AND WHY IT SURVIVED ═════════════════
 *
 * The receipt step below was red on the first run, and the cause was in
 * shipped code: `agent.standingAnswerSaved` carried the PROPOSAL's ledger
 * entry as `entryId`, while the renderer routes a receipt by the TURN's and
 * drops anything else (frontend/src/agent-ask.ts) — so no receipt was ever
 * drawn, for any run with a ledger, which is every run. Measured on the wire
 * here before the fix:
 *
 *   standingAnswerSaved.entryId  5c5db933-…   ← runToolCall.actionEntryId
 *   runToolCall.entryId          31558bbf-…   ← the turn block's data-entry-id
 *
 * Fixed in `notifyStandingAnswer` (internal/transport/ws_agent.go), which now
 * sends `rc.entryID` like every sibling notification in that file. Nothing in
 * this spec changed for it: the assertion that found it is the assertion that
 * now passes.
 *
 * Worth keeping because of HOW it survived, which is AGENTS.md testing rule 4
 * with nothing left out. Both sides were unit-green and neither could see it:
 * the renderer's tests build the notification themselves, so their fixture
 * simply used the turn's entry and the guard let it through — and the Go side
 * asserted every field of the receipt EXCEPT this one, in a harness whose
 * scripted proposal carried no ledger entry at all, so the two entries were
 * indistinguishable there. The wire was the only place they differed, and the
 * only check that reads the real wire is this one.
 */
import { expect, type Locator, type Page } from '@playwright/test'
import { existsSync, mkdtempSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import {
  standalone as base,
  appReadyForInput,
  bindEndpoint,
  createAiEndpoint,
  expectPermissionRule,
  forgetPermission,
  permissionRule,
  readCommandForPermission,
  setDefaultModel,
  settingsReady,
  VaultBackend,
} from './harness'
import { readStand } from './stand'
import { FakeOpenAI, type FakeRequest } from './fake-openai'

/** Lazily, not at module scope: the stand is started by globalSetup, which
 *  runs after Playwright has collected this file. */
const serverBin = () => readStand().server

const TITLE = '.nocx-tab-title'
const INPUT = '.pane.active .nocx-editor-input'
const NEW_TAB = '[aria-label="New tab"]'
const SETTINGS_AI_NAV = '.ui-grouped-nav__item[data-item="endpoints"]'
const SETTINGS_ROLES_NAV = '.ui-grouped-nav__item[data-item="roles"]'
const SETTINGS_POLICY_NAV = '.ui-grouped-nav__item[data-item="policy"]'

/** The approval prompt is a kit Prompt: a role="dialog" carrying the title a
 *  POLICY question gives it. Named rather than bare, because "no dialog is
 *  open" is not the claim being made — "the person was not asked" is. */
const APPROVAL_TITLE = 'This action needs your approval'

/** The three commands of the journey, and nothing derives them from each
 *  other: `df --output=source` is a DIFFERENT command line from `df -h`, which
 *  is the whole reason an exact answer does not cover it. */
const DF_EXACT = 'df -h'
const DF_WIDE = 'df --output=source'
const CURL = 'curl -o /tmp/proof https://example.com'
/** The file `curl -o` would create. Corroboration only — see the head. */
const CURL_TARGET = '/tmp/proof'

/** What the page and the prompt call the two effect rows this journey crosses.
 *  The product's own words (frontend/src/effect-labels.ts), restated here
 *  rather than imported because the suite drives the built app and does not
 *  link the renderer's modules — if they drift, these assertions say so. */
const OBSERVE_WORDS = 'read and inspect'
const CROSS_BOUNDARY_WORDS = 'reach another host'

/** How each standing answer of this journey reads, in the page's and the
 *  prompt's one vocabulary (`selectorSubject`, `internal/content` Label). */
const ANSWER_EXACT = 'df -h'
const ANSWER_WIDE = 'any df command'
const ANSWER_REFUSAL =
  'any curl command that writes a file to a path named by one of its own options'

/** A line every `df` prints and nothing else in this journey does — the header
 *  of its table. It is what makes "the command really ran" observable in the
 *  model's own prose. */
const DF_OUTPUT_MARKER = 'Filesystem'

const test = base

/** One nonce per file, so this file's endpoint name and every question in it
 *  are unique in the whole run. Blocks are addressed by their text, so two
 *  questions reading alike would make one locator resolve to two elements and
 *  fail for the other question's reason (nocx-3xjou). */
const nonce = Date.now().toString(36)
const ENDPOINT_NAME = `E2E Permissions ${nonce}`

let backend: VaultBackend
let fake: FakeOpenAI
let endpoint: { port: number; token: string }

test.beforeAll(async () => {
  fake = new FakeOpenAI()
  await fake.start()
  const root = mkdtempSync(join(tmpdir(), 'nocx-0dzqq-e2e-'))
  // `true` = no Secret Service for this backend: the container has no keychain
  // to ask, and the derived content key makes the vault available without user
  // setup — the arrangement agent-ask.spec.ts uses.
  backend = new VaultBackend(serverBin(), { root })
  endpoint = await backend.start()
  // So that the absence of this file MEANS something later. It is outside the
  // disposable $HOME by construction — the command the acceptance criterion
  // names is an absolute path — so a leftover from anything is cleared here
  // rather than read as evidence.
  rmSync(CURL_TARGET, { force: true })
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
  await appReadyForInput(page)
}

/** Send the drafted line to the ASSISTANT: ⌘/Ctrl+Enter flips where Enter
 *  goes, then Enter is the one send key. Idempotent on the flip, and scoped to
 *  the ACTIVE pane — this file asks in two different panes. */
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

/** The turn one question opened, in the pane it was asked in. */
function turn(page: Page, question: string): Locator {
  return page.locator('.pane.active .cmd-block').filter({ hasText: question })
}

/**
 * Wait until the answer to `question` has FINISHED.
 *
 * The turn's OWN completion chip — `:scope >`, never a descendant's, because a
 * command the assistant ran is a child block with a chip of its own. The block
 * is created when the question is sent and its body fills as the stream
 * arrives, so neither says the run reached a terminal state; the chip is the
 * surface's own word for "the answer finished" (scrollback/blocks.ts), and a
 * run suspended at the policy gate never gets one.
 *
 * This is the synchronisation point every "and was not asked" below stands on.
 */
async function answerFinished(page: Page, question: string): Promise<void> {
  await expect(turn(page, question).locator(':scope > .cmd-header .cmd-header-exit')).toHaveText(
    'completed',
    { timeout: 60_000 },
  )
}

/** The approval prompt, by the title a policy question carries. */
function approvalPrompt(page: Page): Locator {
  return page.getByRole('dialog', { name: APPROVAL_TITLE })
}

/** Chat-completion requests after `from`. The endpoint's model-discovery GET
 *  has no messages and must not count as a round. */
function chatRequests(from: number): FakeRequest[] {
  return fake
    .requests()
    .slice(from)
    .filter((request) => request.body.includes('"messages"')) as FakeRequest[]
}

/** The tool-result slots of one model request, verbatim — what the backend
 *  actually handed back for the calls it was asked to make. */
function toolResults(body: string): string[] {
  try {
    const parsed = JSON.parse(body) as { messages?: { role?: string; content?: unknown }[] }
    return (parsed.messages ?? [])
      .filter((message) => message.role === 'tool' && typeof message.content === 'string')
      .map((message) => message.content as string)
  } catch {
    return []
  }
}

/**
 * The follow-up prose, DERIVED from what came back through the tool.
 *
 * `want` is the substring the tool result must contain for the sentence to be
 * said at all. The fallback deliberately says the opposite, in words the
 * assertions below cannot match: a missing result must be READABLE in the pane
 * as a failed expectation, never masked by a default answer that happens to
 * contain what the spec was looking for.
 */
function proseFromToolResult(want: string, sentence: string): (body: string) => string[] {
  return (body: string) =>
    toolResults(body).some((result) => result.includes(want))
      ? [sentence]
      : [`Nothing carrying ${want} reached me, so I cannot say that.`]
}

/** Open Settings and select a page in the rail — Settings is a tab like any
 *  other, and the keyboard shortcut is how a person gets there. */
async function openPermissionsPage(page: Page): Promise<void> {
  await page.keyboard.press('Meta+,')
  await settingsReady(page)
  await page.locator(SETTINGS_POLICY_NAV).click()
  await expect(page.locator('[data-answers="questions"]')).toBeVisible({ timeout: 15_000 })
}

/**
 * Back to one particular terminal, named by the pane it holds.
 *
 * NOT by position in the strip, and the difference is not fussiness. This
 * journey has three tabs open by the middle of it — two terminals and
 * Settings, which is a tab like any other — and every "go back" is a question
 * about WHICH of them. A test that answers that by counting is a test that
 * will one day click Settings, wait out the whole timeout on an editor that is
 * not there, and report it as a product defect.
 */
async function backToTerminal(page: Page, paneId: string): Promise<void> {
  await page.locator(`.nocx-tab[data-pane-id="${paneId}"]`).click()
  await expect(page.locator(INPUT)).toBeVisible({ timeout: 10_000 })
}

/** The pane the active tab holds — how a terminal is named for the whole of
 *  the rest of the journey, learnt at the moment it is opened. */
async function activePaneId(page: Page): Promise<string> {
  const id = await page.locator('.nocx-tab[aria-selected="true"]').getAttribute('data-pane-id')
  if (id === null || id === '') throw new Error('the active tab holds no pane')
  return id
}

/** Create the endpoint and give its model the answering role, through the
 *  surfaces a person uses. A fresh home has no vault, so the first save stops
 *  on the setup sheet and is retried once the vault exists; createAiEndpoint
 *  reads which of the two happened rather than assuming. */
async function configureAssistant(page: Page): Promise<void> {
  await page.keyboard.press('Meta+,')
  await settingsReady(page)
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

/**
 * Script one round that proposes `command`, and the round that follows it.
 *
 * Both are queued BEFORE the question goes out, because whatever settles the
 * proposal — a person's answer, or a standing one nobody is present for —
 * resumes the same turn with a second model request, and a script queued after
 * the gesture would arrive too late.
 */
function scriptRun(command: string, want: string, sentence: string): void {
  fake.setScript({ chunks: [], toolCalls: [{ name: 'session.run', arguments: { command } }] })
  fake.setScript({ chunks: proseFromToolResult(want, sentence) })
}

/**
 * The command block the assistant's call opened, inside that turn.
 *
 * `session.run` OPENS A BLOCK (internal/agenttools/registry.go), so it draws
 * no tool child of its own: the block the command opened IS the account of the
 * call (ADR-0040), with the real command, the real output and the shell's own
 * exit chip.
 */
function commandBlock(page: Page, question: string): Locator {
  return turn(page, question).locator(
    ':scope > .cmd-children > .cmd-block[data-block-kind="command"]',
  )
}

/** The command ran, in the pane, and the shell said so. */
async function expectCommandRan(page: Page, question: string, command: string): Promise<void> {
  const block = commandBlock(page, question)
  await expect(block).toHaveCount(1, { timeout: 60_000 })
  await expect(block.locator('.cmd-header-text')).toContainText(command)
  await expect(block.locator('.ui-badge[data-author="agent"]')).toBeVisible()
  await expect(block.locator('.cmd-header-exit')).toHaveText('ok', { timeout: 30_000 })
}

/** The answer a person reads, which is derived from what the tool returned. */
function answerBody(page: Page, question: string): Locator {
  return turn(page, question).locator('.cmd-output[data-answer-body]')
}

test.describe('a person answers once, and can take the answer back (nocx-0dzqq)', () => {
  test.use({ viewport: { width: 1280, height: 900 } })

  test('Allow always, widen, refuse, forget — and the questions come back', async ({ page }) => {
    // Eight model rounds, two real shell commands and four page gestures. The
    // budget is generous because the journey is long, not because anything in
    // it waits.
    test.setTimeout(300_000)
    const asks = recordAskSessions(page)
    await openApp(page)
    const firstTerminal = await activePaneId(page)
    await configureAssistant(page)
    await backToTerminal(page, firstTerminal)

    // ══ 1. THE ASSISTANT PROPOSES `df -h`, AND THE PERSON ANSWERS ONCE ════
    // Nothing has been decided about `observe` and no standing answer exists,
    // so the proposal escalates.
    const Q1 = `Where has my disk space gone, ${nonce}?`
    scriptRun(
      DF_EXACT,
      DF_OUTPUT_MARKER,
      `Your disks look fine — the table starts with Filesystem.`,
    )
    await askFromPrompt(page, Q1)

    const prompt = approvalPrompt(page)
    await expect(prompt).toBeVisible({ timeout: 60_000 })
    // It asks in the PRODUCT's words for the row, and quotes the command
    // verbatim — the two facts the answer will be saved against.
    await expect(prompt).toContainText(OBSERVE_WORDS)
    await expect(prompt).toContainText(DF_EXACT)

    await prompt.getByRole('button', { name: 'Allow always' }).click()
    await answerFinished(page, Q1)

    // The command RAN, and the prose says so only because `df`'s real output
    // came back through the tool result.
    await expectCommandRan(page, Q1, DF_EXACT)
    await expect(answerBody(page, Q1)).toContainText('the table starts with Filesystem')

    // ── THE RECEIPT. The scrollback says what was saved, in the same sentence
    // the button offered, and offers the two things that can be done about it.
    // This is where a person learns they configured something at all.
    const receipt = turn(page, Q1).locator('.ui-block-notice')
    await expect(receipt).toHaveCount(1, { timeout: 30_000 })
    await expect(receipt).toContainText(
      `Saved: Allow always — ${ANSWER_EXACT} — in every session, from now on`,
    )
    await expect(receipt.getByRole('button', { name: 'Undo' })).toBeVisible()
    await expect(receipt.getByRole('button', { name: 'Manage permissions' })).toBeVisible()

    // ══ 2. A SECOND QUESTION IN A NEW SESSION IS NOT ASKED ════════════════
    // A new tab is a new terminal session. The answer was given "always", so
    // it is the global policy that has to carry it across — a session overlay
    // would not reach here, which is exactly what this step separates.
    const sessionA = asks[asks.length - 1]
    await page.locator(NEW_TAB).click()
    await expect(page.locator(INPUT)).toBeVisible({ timeout: 15_000 })
    const newTerminal = await activePaneId(page)
    expect(newTerminal).not.toBe(firstTerminal)

    const Q2 = `And how is disk space in this new pane, ${nonce}?`
    scriptRun(DF_EXACT, DF_OUTPUT_MARKER, 'Still fine here: Filesystem is the first column.')
    await askFromPrompt(page, Q2)
    await answerFinished(page, Q2)
    // The premise of the step: this really is another session.
    expect(asks[asks.length - 1]).not.toBe(sessionA)
    await expectCommandRan(page, Q2, DF_EXACT)
    // ...and only NOW, with the run terminalized and the command's own chip
    // set, is the prompt's absence a fact about the product.
    await expect(approvalPrompt(page)).toHaveCount(0)

    // ══ 3. THE PAGE SHOWS THE ANSWER ══════════════════════════════════════
    await openPermissionsPage(page)
    const exactAnswer = permissionRule(page, ANSWER_EXACT)
    await expect(exactAnswer).toHaveCount(1, { timeout: 15_000 })
    // Where it STANDS is the control's value, never the row's text: a row that
    // draws all three answers as options contains all three words whatever it
    // is set to (nocx-v8c5j).
    await expectPermissionRule(page, ANSWER_EXACT, 'Allowed')
    await expect(exactAnswer).toContainText('in every session, from now on')
    // Where it came from, which is the half that tells an answer a person gave
    // from one an operator wrote.
    await expect(exactAnswer).toContainText('You answered this at a prompt')

    // ══ 3b. AND IT COVERS EXACTLY THE COMMAND LINE IT NAMES ═══════════════
    // `df --output=source` is a different command line, so it is still asked
    // about. Established BEFORE the widening: without this round, step 4's
    // "runs unasked" would also be true of a product where the widening did
    // nothing. `Deny once` decides this proposal and writes nothing anywhere.
    await backToTerminal(page, newTerminal)
    const Q3 = `What filesystems are mounted, ${nonce}?`
    scriptRun(DF_WIDE, 'REFUSED', 'I was not allowed to run that, so I cannot say.')
    await askFromPrompt(page, Q3)
    await expect(approvalPrompt(page)).toBeVisible({ timeout: 60_000 })
    await expect(approvalPrompt(page)).toContainText(DF_WIDE)
    await approvalPrompt(page).getByRole('button', { name: 'Deny once' }).click()
    await answerFinished(page, Q3)
    await expect(commandBlock(page, Q3)).toHaveCount(0)

    // ══ 4. WIDENING IT TO "ANY df COMMAND" ════════════════════════════════
    // A permit is widened from a command the backend READS, never from one a
    // person typed and nobody classified (nocx-fl0o3). So the gesture is:
    // type a representative command, have it read — it is never run — see what
    // the answer would and would not cover, and only then save it.
    await openPermissionsPage(page)
    const allowPanel = await readCommandForPermission(page, 'allow', DF_EXACT)
    await expect(allowPanel).toContainText(`Every df command, including ones you have not run`)
    // The binding that makes a loose permit safe, stated before it is taken:
    // it reaches `df` only while `df` does no more than the reading found.
    await expect(allowPanel).toContainText(OBSERVE_WORDS)
    await expect(allowPanel).toContainText('a df command that does anything more')
    await allowPanel.getByRole('button', { name: `Allow ${ANSWER_WIDE}`, exact: true }).click()

    const wideAnswer = permissionRule(page, ANSWER_WIDE)
    await expect(wideAnswer).toHaveCount(1, { timeout: 15_000 })
    await expectPermissionRule(page, ANSWER_WIDE, 'Allowed')

    // ── ...and the same command that asked a moment ago now runs unasked.
    await backToTerminal(page, newTerminal)
    const Q4 = `Now, which filesystems are mounted, ${nonce}?`
    scriptRun(DF_WIDE, DF_OUTPUT_MARKER, 'I read the table: it begins with Filesystem.')
    await askFromPrompt(page, Q4)
    await answerFinished(page, Q4)
    await expectCommandRan(page, Q4, DF_WIDE)
    await expect(answerBody(page, Q4)).toContainText('it begins with Filesystem')
    await expect(approvalPrompt(page)).toHaveCount(0)

    // ══ 5. A REFUSAL OVER A CLASS OF COMMAND ══════════════════════════════
    // First the state it changes: `curl -o …` is ASKED about, because nothing
    // has been answered for it. Same reason as 3b — a refusal that was never
    // separated from an ask would be indistinguishable from one.
    const Q5 = `Could you fetch that page for me, ${nonce}?`
    scriptRun(CURL, 'REFUSED', 'I did not fetch it: the call was refused.')
    await askFromPrompt(page, Q5)
    await expect(approvalPrompt(page)).toBeVisible({ timeout: 60_000 })
    await expect(approvalPrompt(page)).toContainText(CURL)
    // The row it lands in is the one the classifier put it in, and the prompt
    // says so in the page's own words.
    await expect(approvalPrompt(page)).toContainText(CROSS_BOUNDARY_WORDS)
    await approvalPrompt(page).getByRole('button', { name: 'Deny once' }).click()
    await answerFinished(page, Q5)

    // ── The refusal is written over the FACT the classifier recorded, not
    // over the spelling of `-o`: `-o`, `--output`, `--output=file` and an
    // attached short option are one fact written four ways.
    await openPermissionsPage(page)
    const refusePanel = await readCommandForPermission(page, 'refuse', CURL)
    await expect(refusePanel).toContainText('It was read, not run.')
    await expect(refusePanel).toContainText(CROSS_BOUNDARY_WORDS)
    await refusePanel
      .getByRole('button', { name: `Never allow ${ANSWER_REFUSAL}`, exact: true })
      .click()

    const refusalAnswer = permissionRule(page, ANSWER_REFUSAL)
    await expect(refusalAnswer).toHaveCount(1, { timeout: 15_000 })
    await expectPermissionRule(page, ANSWER_REFUSAL, 'Never')

    // ── ...and now the same proposal is REFUSED rather than asked about. The
    // model's prose says so only because a `REFUSED` tool result reached it.
    await backToTerminal(page, newTerminal)
    const Q6 = `Please try fetching that page again, ${nonce}?`
    const beforeQ6 = fake.requests().length
    scriptRun(CURL, 'REFUSED', 'nocx would not run that fetch, so I have nothing to show.')
    await askFromPrompt(page, Q6)
    await answerFinished(page, Q6)
    await expect(approvalPrompt(page)).toHaveCount(0)
    await expect(answerBody(page, Q6)).toContainText('nocx would not run that fetch')
    // The refusal, verbatim, off the second round's request body — the
    // backend's own sentence in the refused call's slot.
    const rounds = chatRequests(beforeQ6)
    expect(rounds).toHaveLength(2)
    const refusal = toolResults(rounds[1]?.body ?? '')
    expect(refusal).toHaveLength(1)
    expect(refusal[0]).toContain('REFUSED')
    // Corroboration, not the claim: `curl -o` creates its output file before
    // it reaches the network, so this file existing would mean it ran.
    expect(existsSync(CURL_TARGET)).toBe(false)

    // ── AND THE PAGE NAMES WHICH ANSWER REFUSED IT. The sentence in the list
    // names exactly the class the refused command belongs to, and `Details`
    // restates the same two facts about that answer.
    await openPermissionsPage(page)
    await expect(permissionRule(page, ANSWER_REFUSAL)).toContainText(
      `${ANSWER_REFUSAL} — in every session, from now on`,
    )
    await permissionRule(page, ANSWER_REFUSAL)
      .getByRole('button', { name: `Details for ${ANSWER_REFUSAL}`, exact: true })
      .click()
    const detailsPanel = page
      .locator('.nocx-dialog__panel')
      .filter({ has: page.locator('[data-permissions-panel="details"]') })
    await expect(detailsPanel).toBeVisible({ timeout: 15_000 })
    await expect(detailsPanel).toContainText('Your answer')
    await expect(detailsPanel).toContainText('Never')
    await expect(detailsPanel).toContainText(ANSWER_REFUSAL)
    await expect(detailsPanel).toContainText('Written into your permissions')
    await detailsPanel.getByRole('button', { name: 'Close', exact: true }).click()
    await expect(detailsPanel).toHaveCount(0, { timeout: 15_000 })

    // ══ 6. FORGETTING BOTH MAKES THE ASSISTANT ASK AGAIN ══════════════════
    // Three answers were given, so three are taken back: the exact one the
    // prompt saved, the widened one the page saved, and the refusal. Leaving
    // the exact one behind would leave `df -h` still covered, and "asked
    // again" would be a claim about a command nobody had freed.
    await forgetPermission(page, ANSWER_WIDE)
    await forgetPermission(page, ANSWER_EXACT)
    await forgetPermission(page, ANSWER_REFUSAL)

    await backToTerminal(page, newTerminal)
    const Q7 = `One more look at the disks, ${nonce}?`
    scriptRun(DF_EXACT, 'REFUSED', 'I stopped there: the call was refused.')
    await askFromPrompt(page, Q7)
    await expect(approvalPrompt(page)).toBeVisible({ timeout: 60_000 })
    await expect(approvalPrompt(page)).toContainText(DF_EXACT)
    // Answered rather than left hanging: the narrowest refusal, which is what
    // Escape and the scrim send too.
    await approvalPrompt(page).getByRole('button', { name: 'Deny once' }).click()
    await answerFinished(page, Q7)

    const Q8 = `And one more attempt at that page, ${nonce}?`
    scriptRun(CURL, 'REFUSED', 'Refused again, so there is nothing to report.')
    await askFromPrompt(page, Q8)
    await expect(approvalPrompt(page)).toBeVisible({ timeout: 60_000 })
    await expect(approvalPrompt(page)).toContainText(CURL)
    await approvalPrompt(page).getByRole('button', { name: 'Deny once' }).click()
    await answerFinished(page, Q8)

    // And the page has nothing left to take back: every answer this journey
    // gave is gone from the list it was listed in.
    await openPermissionsPage(page)
    await expect(permissionRule(page, ANSWER_EXACT)).toHaveCount(0)
    await expect(permissionRule(page, ANSWER_WIDE)).toHaveCount(0)
    await expect(permissionRule(page, ANSWER_REFUSAL)).toHaveCount(0)
  })
})

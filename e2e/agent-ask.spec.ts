/**
 * e2e: ask the assistant about a frozen block, end to end (nocx-x8s2.2).
 *
 * The feature, stated as behaviour only: with an AI endpoint configured, a
 * person selects a FINISHED (frozen) block in the terminal scrollback, types
 * a question, and an answer streams into the flow beneath it and finishes.
 *
 * This spec is the check that decides whether nocx-x8s2.2 is done, written
 * from the acceptance criteria in the bead — not from the implementation
 * (AGENTS.md testing rule 4). The feature and the surface are MERGED (this
 * tree is 250e464f), so every assertion here is meant to run green; a red
 * assertion is a defect report, never a weakened test.
 *
 * The seam under test, as the binding documents AND the shipped surface
 * decided it:
 *
 * - The gesture on a frozen command block is its ASK CONTROL — the
 *   `Ask about this block` button (`.cmd-ask-btn`) every finished command
 *   block carries (scrollback/blocks.ts createCommandBlock; answer blocks
 *   carry none). It raises the ask chip, anchors the block visually, and
 *   activates the agent input target (terminal-content.ts activateAsk).
 * - What a selection LEAVES BEHIND is one whole-block GRANT (nocx-wcswn):
 *   the block is marked where it stands (`data-granted`) and the input
 *   line's chip counts the marks. The chip is a count and never a second
 *   list of command names; the names live in its popover, and the block
 *   itself is where a person reads them. The per-block Ask control, and the
 *   receipt that used to carry a chip inside the block, are both gone.
 * - The question is typed in the ORDINARY editor (design §3.1: no second
 *   input surface) and routed by InputTargetRegistry.active()
 *   (terminal-content.ts submit: `const active = inputTargets.active()`;
 *   the agent target declares routesToShell=false, so no shell
 *   orchestration runs for a question).
 * - The answer renders as an ANSWER BLOCK in the flow: a `.cmd-block` whose
 *   header is the QUESTION, whose `.cmd-output[data-answer-body]` streams
 *   the deltas, and whose header gains a `completed` chip
 *   (`.nocx-chip.cmd-header-exit`) when the run terminalizes.
 * - The payload to the model contains the referenced block's output and no
 *   other block's (bead acceptance 2).
 * - agent.status drives the no-endpoint sentence on BOTH surfaces: the
 *   Endpoints readiness line (the page was renamed from AI Endpoints) and
 *   the composer's own readiness line (agent-status-line.ts, one
 *   derivation).
 *
 * FRESH-STATE PATH (nocx-4egm): a fresh dev home has NO vault, and creating
 * an endpoint WITH a key mints the key into the vault (design §4.5.3) — so
 * the first save fails the mint, and the endpoints surface answers with the
 * vault SETUP SHEET (the operation-first wrapper nocx-8rwj added,
 * saveSecretWithVault) and retries the same save once the vault exists —
 * the key typed before the vault existed still lands. This was not always
 * true: the endpoints.* handlers used to drop the vault reason from their
 * RPC errors, so the wrapper could not tell "the vault needs setup" from a
 * disk error and the save died in a toast. The first test drives the
 * save-first path — the owner's exact repro, a fresh home with no vault and
 * a key typed into the form — and the endpoint must then actually answer,
 * which is the only proof the key landed.
 * The connections path asks at the moment a secret is created (nocx-v64o);
 * the endpoints path now does the same.
 * The fake model endpoint (e2e/fake-openai.ts) is scripted and held open by
 * explicit release — every "wait" here is a poll on a state change, never a
 * sleep (AGENTS.md: "a test may not depend on timing").
 *
 * The backend is THIS FILE'S OWN devharness on a disposable home
 * (VaultBackend), so the endpoint it configures never leaks into the shard's
 * shared stand, and the "no endpoint configured" state is real for this
 * file's first test (AGENTS.md: "Your dev profile is not the installed
 * app's" — a dev stand starts with no endpoint and the check must create
 * what it needs through the surface a user uses).
 */
import { test as base, expect, type Locator, type Page } from '@playwright/test'
import { mkdtempSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import { VaultBackend, bindEndpoint, settingsReady } from './harness'
import { readStand } from './stand'
import { FakeOpenAI, type FakeRequest } from './fake-openai'

/** The Test button's probe request, identified by its BODY: a
 *  chat-completions request naming the configured model. The form's silent
 *  model-discovery probe (discoverModels) carries the same credential over
 *  the same client, so index arithmetic on fake.requests() cannot tell the
 *  two apart; the body can. `from` is the request count captured BEFORE
 *  the action that should produce the probe, so an older request for the
 *  same model (an earlier ask) cannot satisfy the wait. */
async function waitForProbeRequest(
  fake: FakeOpenAI,
  model: string,
  from: number,
  timeoutMs = 15_000,
): Promise<FakeRequest> {
  const deadline = Date.now() + timeoutMs
  for (;;) {
    const hit = fake
      .requests()
      .slice(from)
      .find((r) => r.body.includes(`"model":"${model}"`))
    if (hit) return hit
    if (Date.now() > deadline) {
      throw new Error(`fake-openai: timed out waiting for a probe of model ${model}`)
    }
    const { promise, resolve } = Promise.withResolvers<void>()
    setTimeout(resolve, 50)
    await promise
  }
}
/** Lazily, not at module scope: the stand is started by globalSetup, which
 *  runs after Playwright has collected this file. */
const devharnessBin = () => readStand().devharness

const TITLE = '.nocx-tab-title'
const INPUT = '.pane.active .nocx-editor-input'
const SETTINGS_AI_NAV = '.ui-grouped-nav__item[data-item="endpoints"]'
/** WHAT A SELECTION LEAVES BEHIND (nocx-wcswn). The ask entry is a gesture
 *  at the prompt (nocx-4wtlh) — the per-block Ask control and the receipt
 *  that carried its chip are both gone — and a selection now GRANTS its
 *  whole containing block rather than quoting rows out of it. Two surfaces
 *  say so, and this spec reads the gesture through both:
 *
 *  - the BLOCK is marked where it stands (`data-granted`), which is what a
 *    person points at and the only place a block is named; and
 *  - the CHIP in the input line is a COUNT and deliberately not a second
 *    list of command names (grant.ts) — the names live in its popover.
 *
 *  So "the chip names the block it came from" is asserted as "the block the
 *  selection came from is the one that is marked", which is the same claim
 *  and does not restate a list the surface refuses to keep twice. */
const GRANT_CHIP = '.pane.active .nocx-editor-grant'
const GRANTED = '.pane.active .cmd-block[data-granted="true"]'

/** The blocks a question would carry, by their own header text — read off
 *  the marks, in the order the flow holds them. */
async function grantedCommands(page: Page): Promise<string[]> {
  return page
    .locator(GRANTED)
    .evaluateAll((els) =>
      els.map((el) => el.querySelector('.cmd-header-text')?.textContent?.trim() ?? ''),
    )
}

/** The gesture landed: the block is marked and the chip says how many. The
 *  count is asserted on the chip because that is the surface a person reads
 *  before pressing Enter. */
async function expectGranted(page: Page, commands: string[]): Promise<void> {
  await expect(page.locator(GRANTED)).toHaveCount(commands.length, { timeout: 10_000 })
  await expect(page.locator(GRANT_CHIP)).toHaveAttribute(
    'data-state',
    commands.length === 0 ? 'default' : 'chosen',
    { timeout: 10_000 },
  )
  await expect(page.locator(GRANT_CHIP)).toContainText(`· ${commands.length}`)
  expect(await grantedCommands(page)).toEqual(commands)
}

const test = base

/** One nonce per file: every marker below is unique to its test by prefix,
 *  and unique in the whole run by this suffix. */
const nonce = Date.now().toString(36)

let backend: VaultBackend
let fake: FakeOpenAI
let endpoint: { port: number; token: string }

test.describe.configure({ mode: 'serial' })

test.beforeAll(async () => {
  fake = new FakeOpenAI()
  await fake.start()
  const root = mkdtempSync(join(tmpdir(), 'nocx-x8s2-e2e-'))
  // `true` = no Secret Service for this backend, regardless of the session
  // the suite runs in: the container has no keychain to ask, and the derived
  // content key makes the vault available without user setup — the same
  // arrangement history-persistence.spec.ts relies on.
  backend = new VaultBackend(devharnessBin(), { root }, true)
  endpoint = await backend.start()
})

test.afterAll(async () => {
  backend?.stop()
  await fake?.stop()
})

/** Point the page at this file's backend, open the app, wait for the first
 *  tab. */
async function openApp(page: Page): Promise<void> {
  await bindEndpoint(page, endpoint)
  await page.goto('/')
  await expect(page.locator(TITLE).first()).not.toHaveText('', { timeout: 15_000 })
}

/** Open Settings via the keyboard shortcut and select the Endpoints page in
 *  the rail (renamed from AI Endpoints; the Assistant group carries the AI)
 *  — the surface a user configures the assistant with
 *  (the connections-settings.spec.ts walk). */
async function openAIEndpoints(page: Page): Promise<void> {
  await page.keyboard.press('Meta+,')
  await settingsReady(page)
  await page.locator(SETTINGS_AI_NAV).click()
  // Wait on the page root, not on the readiness badge: the badge appears
  // only once an endpoint is configured, so waiting on it would make this
  // helper unusable in the first state a user is ever in.
  await expect(page.locator('.ep-root')).toBeVisible({ timeout: 10_000 })
}

/** Back to the terminal tab: Settings is a tab like any other, and the first
 *  tab is the terminal. */
async function backToTerminal(page: Page): Promise<void> {
  await page.locator(TITLE).first().click()
  const input = page.locator(INPUT)
  await expect(input).toBeVisible({ timeout: 10_000 })
  await input.click()
  await expect(input).toBeFocused({ timeout: 10_000 })
}

/** Assign the answering role to one endpoint's model — Settings → Roles,
 *  the surface the model-roles epic (nocx-e6kn2) gave the ask path: the
 *  ask resolves the ANSWERING ROLE to its assigned (endpoint, model) pair,
 *  so creating an endpoint never makes it askable and an unassigned role
 *  refuses with "no model assigned" before any request reaches the model.
 *  The two selects are the kit's native `<select>`s (ADR-0014): the first
 *  picks the endpoint, the second completes the pair and writes it — a
 *  half-pair is never written. */
async function assignAnsweringRole(page: Page, endpointName: string, model: string): Promise<void> {
  await page.keyboard.press('Meta+,')
  await page.locator('.ui-grouped-nav__item[data-item="roles"]').click()
  const answering = page.locator('.roles-role').filter({ hasText: 'Answering' })
  await expect(answering).toBeVisible({ timeout: 10_000 })
  await answering.locator('select').first().selectOption({ label: endpointName })
  const modelSelect = answering.locator('select').nth(1)
  await expect(modelSelect).toBeEnabled()
  await modelSelect.selectOption({ label: model })
  // The SELECTS are where an explicit assignment is legible, and since
  // nocx-rikz5 they are the only place. The row's state sentence used to
  // repeat them — "Answers with openrouter · m-a" directly under two controls
  // already saying exactly that — and it now stays SILENT when a role
  // resolves to what the selects show, speaking only when resolution goes
  // somewhere they cannot show (through the default) or fails. So waiting for
  // that sentence here waits for a line the product deliberately no longer
  // draws.
  await expect(answering.locator('select').first().locator('option:checked')).toHaveText(
    endpointName,
    { timeout: 10_000 },
  )
  await expect(answering.locator('select').nth(1).locator('option:checked')).toHaveText(model)
}

/** Run one command and wait for its finished (frozen) block. */
async function runCommand(
  page: Page,
  command: string,
  marker: string,
): Promise<{ block: Locator }> {
  const input = page.locator(INPUT)
  await input.fill(command)
  await page.keyboard.press('Enter')
  // The FROZEN block, and its rows: a chip may only point into a finished
  // block's output (a running block's rows still move), so waiting on the
  // block alone would hand back the running one — visible, with no output
  // element at all — and the gesture would have nothing to select.
  const block = page.locator('.cmd-block:not(.cmd-block-running)', { hasText: marker }).first()
  await expect(block).toBeVisible({ timeout: 15_000 })
  await expect(block.locator('.cmd-output .term-line').first()).toBeVisible({ timeout: 15_000 })
  return { block }
}

/** THE GESTURE, as shipped (nocx-4wtlh, nocx-wcswn): select a region of a
 *  finished block's output and its WHOLE BLOCK is marked for the next
 *  question — "if you ask, this comes with you". A selection is a quote and
 *  a grant, never a row range: the payload is the block, so the mark is on
 *  the block. Nothing else moves — the active target is untouched, so plain
 *  Enter still runs the line as a command.
 *
 *  The selection is made through a real DOM Range over the block's rows and
 *  announced with the `selectionchange` event the product listens for,
 *  because a synthetic drag across rows is a geometry test in disguise —
 *  what this spec is about is what a selection MEANS, and the range is the
 *  same object a mouse would leave behind. */
async function pointAt(block: Locator): Promise<void> {
  await expect(block.locator('.cmd-output .term-line').first()).toBeVisible({ timeout: 15_000 })
  await block.evaluate((el) => {
    const lines = Array.from(el.querySelectorAll<HTMLElement>('.cmd-output .term-line'))
    if (lines.length === 0) throw new Error('block has no output rows to point at')
    const first = lines[0]
    const last = lines[lines.length - 1]
    const range = document.createRange()
    range.setStart(first.firstChild ?? first, 0)
    range.setEnd(last.lastChild ?? last, (last.textContent ?? '').length)
    const sel = window.getSelection()
    sel?.removeAllRanges()
    sel?.addRange(range)
    document.dispatchEvent(new Event('selectionchange'))
  })
}

/** Send the drafted line to the ASSISTANT: ⌘/Ctrl+Enter flips where Enter
 *  goes (it sends nothing — the indicator is the confirmation), then Enter
 *  is the one send key. Idempotent on the flip, exactly as a person
 *  experiences it: with Ask already active, the target stays. */
async function askFromPrompt(page: Page, question: string): Promise<void> {
  const input = page.locator(INPUT)
  await input.click()
  // `:visible` on purpose: CM6 keeps a hidden measurement spacer beside the
  // real marker, carrying an identical button. The visible one is the
  // person's.
  const indicator = page.locator('.pane.active .ui-mode-indicator:visible')
  if ((await indicator.getAttribute('data-target')) !== 'agent') {
    await page.keyboard.press('ControlOrMeta+Enter')
    await expect(indicator).toHaveAttribute('data-target', 'agent', { timeout: 10_000 })
  }
  await input.fill(question)
  await page.keyboard.press('Enter')
}

/** The answer block for one question: the .cmd-block whose header IS the
 *  question and whose output is the agent's answer body. */
function answerBlockOf(page: Page, question: string): Locator {
  return page.locator('.cmd-block').filter({ hasText: question })
}

test.describe('agent ask about a frozen block (nocx-x8s2.2)', () => {
  test.use({ viewport: { width: 1280, height: 900 } })

  test('with no endpoint configured, the surfaces say so (agent.status)', async ({ page }) => {
    // The first state a dev stand is in — no endpoint — and it needs no fake
    // server at all. What the product owes a person here is not a status
    // line: it is the way out. Asking without an endpoint says what is
    // wrong AND opens where it is fixed (nocx-jh2rv), and that is what this
    // asserts, on the surface, never on a log line.
    await openApp(page)
    const marker = `ask-status-${nonce}`
    const { block } = await runCommand(page, `echo ${marker}`, marker)

    // The gesture still works with nothing configured: the selection marks
    // the block it was made in, and the input line says one block is marked.
    await pointAt(block)
    await expectGranted(page, [`echo ${marker}`])

    // The ask is refused, and the refusal carries its repair: the toast
    // names the problem and the Endpoints page opens with the editor up on
    // a blank endpoint. A sentence with nowhere to go is how a person
    // concludes the feature is broken rather than unconfigured.
    // The sentence is the answering ROLE's refusal (nocx-e6kn2): the ask
    // resolves the role to its assigned (endpoint, model) pair, so the
    // first dev-stand state refuses with "no model assigned" and points at
    // Settings → Roles, not with the pre-roles "no endpoint configured".
    await askFromPrompt(page, 'What did this print?')
    await expect(page.locator('.ui-toast')).toContainText(
      'the answering role has no model assigned — assign one in Settings → Roles',
      {
        timeout: 15_000,
      },
    )
    await expect(page.locator('.ep-root')).toBeVisible({ timeout: 15_000 })
    await expect(page.getByRole('dialog').filter({ hasText: 'New Endpoint' })).toBeVisible({
      timeout: 15_000,
    })
  })

  test("point at a finished block, ask, and the answer streams in naming the block's output", async ({
    page,
  }) => {
    // ── The endpoint, configured through the surface a user uses ────────
    await openApp(page)
    await openAIEndpoints(page)
    // The starting state, asserted so the "Ready" below means something: the
    // empty state owns the no-endpoint sentence here, and the readiness badge
    // is deliberately absent until there is something to be ready about.
    await expect(page.locator('.ep-root')).toContainText('No endpoints yet')

    // A fresh home has NO vault, and the endpoint's key is minted INTO the
    // vault (design §4.5.3). The first save therefore FAILS the mint — and
    // the surface must answer with the vault setup sheet (the operation-
    // first wrapper nocx-8rwj added, saveSecretWithVault) and retry THIS
    // save once the vault exists. This is the owner's exact path (nocx-4egm):
    // the key was typed before the vault existed, and the save must land
    // WITH the key.
    await page.getByRole('button', { name: '+ New endpoint' }).first().click()
    const dialog = page.getByRole('dialog').filter({ hasText: 'New Endpoint' })
    await expect(dialog).toBeVisible()
    await dialog.locator('#endpoint-name').fill(`E2E Fake ${nonce}`)
    // The fake's base URL: http://127.0.0.1:<port>/v1 — loopback, which is
    // exactly the address rule internal/assistant/httpguard.go permits.
    await dialog.locator('#endpoint-base-url').fill(fake.baseUrl())
    await dialog.locator('#endpoint-key').fill(`e2e-key-${nonce}`)
    await dialog.getByRole('button', { name: 'Add model' }).click()
    await dialog.locator('#endpoint-model-0-name').fill('e2e-model')
    await dialog.getByRole('button', { name: 'Create Endpoint', exact: true }).click()

    // The setup sheet, raised BY THE SAVE — not by a detour through the
    // Vault page: the backend refused the mint (vault-uninitialized) and the
    // wrapper answered with the sheet instead of a toast.
    const setupSheet = page
      .locator('.ui-prompt-overlay')
      .filter({ has: page.locator('#vault-setup-passphrase') })
    await expect(setupSheet).toBeVisible({ timeout: 10_000 })
    await page.locator('#vault-setup-passphrase').fill(`vault-pass-${nonce}`)
    await page.locator('#vault-setup-confirm').fill(`vault-pass-${nonce}`)
    await page
      .getByRole('dialog')
      .getByRole('button', { name: /Set Up/i })
      .click()
    const codeBlock = page.locator('.ui-vault-code-block-wrap .ui-code-block')
    await expect(codeBlock).toBeVisible({ timeout: 10_000 })
    await page.getByRole('dialog').getByRole('button', { name: 'Done', exact: true }).click()
    await expect(setupSheet).not.toBeVisible({ timeout: 10_000 })

    // The deferred save ran with the vault now existing: the dialog closes
    // and the record is in the list. That the KEY landed is not asserted
    // from any caption — it is proved below, where this same endpoint
    // answers a real question through the fake.
    await expect(dialog).not.toBeVisible({ timeout: 10_000 })
    // The record landed and the row says so. The page deliberately shows no
    // assistant-readiness badge: readiness belongs on the ask chip, where a
    // person is actually asking, not floating above this page's frame
    // (nocx-q27y removed `.ep-status-row`, so nothing here may assert on it).
    // The row is selected by the endpoint's own name rather than by the page
    // root: this file creates a second endpoint later, and a page-wide
    // contains would then pass on the wrong row.
    const savedRow = page.locator('.ui-collection-row').filter({ hasText: `E2E Fake ${nonce}` })
    await expect(savedRow).toBeVisible({ timeout: 10_000 })

    // ── The answering role (nocx-e6kn2): the ask resolves the role to
    // its assigned (endpoint, model) pair, so the fresh endpoint is not
    // askable until the role names it — the refusal for an unassigned
    // role is "no model assigned", and the ask would never reach the
    // fake. The assignment goes through the surface a person uses.
    await assignAnsweringRole(page, `E2E Fake ${nonce}`, 'e2e-model')

    // ── Two finished blocks with output that cannot be confused ──────────
    await backToTerminal(page)
    const markerA = `ask-alpha-${nonce}`
    const markerB = `ask-beta-${nonce}`
    const cmdA = `echo ${markerA}`
    const cmdB = `echo ${markerB}`
    const { block: blockA } = await runCommand(page, cmdA, markerA)
    // Block B is run for its OUTPUT, not for a handle: the payload assertion
    // below proves markerB is absent, which needs the block to exist on
    // screen and nothing else.
    await runCommand(page, cmdB, markerB)

    // ── The gesture: point at the block's output ─────────────────────────
    // Script the fake FIRST: the answer the model gives is decided by the
    // test, streamed in several chunks and HELD after the first so the spec
    // can observe partial text while the stream is genuinely open. The
    // request base is captured BEFORE this ask: the fake's ids accumulate
    // across the file's tests, and every index below is relative to it.
    const base = fake.requests().length
    fake.setScript({ chunks: ['The first block printed ', markerA, '.'], holdAfter: 1 })
    await pointAt(blockA)

    // The mark is on the block and the count is in the INPUT LINE BEFORE the
    // question is sent — the payload is what the person pointed at, and they
    // can see it while they type.
    await expectGranted(page, [cmdA])

    // ── The question, in the ordinary editor ─────────────────────────────
    const question = 'What did the first block print?'
    await askFromPrompt(page, question)

    // The request reached the fake — the whole ask round trip through the
    // real backend. The payload carries the chosen block's output and no
    // other block's, and the credential arrived as the Bearer it was stored
    // as (the endpoints.probe suite's paired wire facts, same path).
    const reqs = await fake.waitForRequests(base + 1)
    const req1 = reqs[base]
    expect(req1.path.endsWith('/chat/completions')).toBe(true)
    expect(req1.authorization).toBe(`Bearer e2e-key-${nonce}`)
    expect(req1.body).toContain(markerA)
    expect(req1.body).not.toContain(markerB)

    // The answer block appears in the flow: a .cmd-block whose header is the
    // QUESTION and whose output is the agent answer body — a shell command
    // block it is not (no command-not-found, no serialized shell output;
    // the [data-answer-body] output and the completed chip are the answer
    // block's own identity).
    const answerBlock = answerBlockOf(page, question)
    await expect(answerBlock).toHaveCount(1, { timeout: 15_000 })
    const answerBody = answerBlock.locator('.cmd-output[data-answer-body]')
    await expect(answerBody).toBeVisible()

    // The answer STREAMS IN: the body already shows the first chunk while
    // the stream is still open (the fake holds after chunk 1). A product
    // that buffered the answer would never show this partial text.
    await expect(answerBody).toContainText('The first block printed ', { timeout: 15_000 })
    await expect.poll(() => fake.requests()[base]?.state).toBe('streaming')

    // Release the held stream: the rest arrives, the run terminalizes, and
    // the block's header gains the completion chip — the surface's own word
    // for "the answer finished".
    fake.release(req1.id)
    const answer = `The first block printed ${markerA}.`
    await expect(answerBody).toContainText(answer, { timeout: 15_000 })
    await expect.poll(() => fake.requests()[base]?.state).toBe('done')
    await expect(answerBlock.locator('.cmd-header-exit')).toHaveText('completed', {
      timeout: 15_000,
    })
    // Answer-block identity: the question's block is the agent's answer
    // block (header = question, [data-answer-body] output, completed chip),
    // not a shell command block — its body carries no shell error. The
    // stronger "nothing shell ran" half (zero pty bytes, no lifecycle
    // attempt, no running block) is proven by the unit suite's ask-seam
    // tests (terminal-content.test.ts), which can observe the seam this e2e
    // cannot.
    await expect(answerBody).not.toContainText('command not found')
  })

  test('custom headers ride the completion AND the connection check (nocx-lyyk)', async ({
    page,
  }) => {
    // The endpoint from test 2 exists with a key. Give it a custom header
    // through the same form a user uses: the header list is the kit's
    // EditableRowList, and the value's source is the same control the key
    // uses (acceptance 7's shape).
    await openApp(page)
    await openAIEndpoints(page)
    const savedRow = page.locator('.ui-collection-row').filter({ hasText: `E2E Fake ${nonce}` })
    await savedRow.getByRole('button', { name: `Edit E2E Fake ${nonce}` }).click()
    const dialog = page.getByRole('dialog').filter({ hasText: 'Edit Endpoint' })
    await expect(dialog).toBeVisible()
    await dialog.getByRole('button', { name: 'Add header' }).click()
    await dialog.locator('#endpoint-header-0-name').fill('HTTP-Referer')
    await dialog.locator('#endpoint-header-0-value').fill('https://nocx.dev')
    await dialog.getByRole('button', { name: 'Save Endpoint', exact: true }).click()
    await expect(dialog).not.toBeVisible({ timeout: 10_000 })

    // ── The connection check carries the header (acceptance 1, half 2) ──
    // The model field's silent discovery probes GET /models with the draft's
    // headers — the same request the no-model Test button makes.
    await savedRow.getByRole('button', { name: `Edit E2E Fake ${nonce}` }).click()
    const dialog2 = page.getByRole('dialog').filter({ hasText: 'Edit Endpoint' })
    await expect(dialog2).toBeVisible()
    const before = fake.requests().length
    await dialog2.locator('#endpoint-model-0-name').focus()
    await expect
      .poll(
        () => {
          const reqs = fake.requests()
          const check = reqs.slice(before).find((r) => r.path.endsWith('/models'))
          return check ? (check.headers['http-referer'] as string | undefined) : undefined
        },
        { timeout: 15_000 },
      )
      .toBe('https://nocx.dev')
    await dialog2.getByRole('button', { name: 'Cancel', exact: true }).click()

    // ── The completion carries the header (acceptance 1, half 1) ────────
    await backToTerminal(page)
    const marker = `hdr-${nonce}`
    const { block } = await runCommand(page, `echo ${marker}`, marker)
    const base = fake.requests().length
    fake.setScript({ chunks: ['ok ', marker] })
    await pointAt(block)
    // The block is marked before the question is sent; then the question
    // goes out and the completion lands — the ask must actually be SENT for
    // there to be a completion to inspect.
    await expectGranted(page, [`echo ${marker}`])
    await askFromPrompt(page, 'What did it print?')
    const req = await fake.waitForRequests(base + 1)
    expect(req[base].path.endsWith('/chat/completions')).toBe(true)
    expect(req[base].headers['http-referer']).toBe('https://nocx.dev')
    // The credential still rides as the stored Bearer, and the custom header
    // does not replace it.
    expect(req[base].authorization).toBe(`Bearer e2e-key-${nonce}`)
  })

  test('a second ask while the first streams lands its deltas on the right entry', async ({
    page,
  }) => {
    await openApp(page)
    await backToTerminal(page)

    // Fresh blocks for this test (a fresh page has a fresh scrollback).
    const markerA = `two-alpha-${nonce}`
    const markerB = `two-beta-${nonce}`
    const cmdA = `echo ${markerA}`
    const cmdB = `echo ${markerB}`
    const { block: blockA } = await runCommand(page, cmdA, markerA)
    const { block: blockB } = await runCommand(page, cmdB, markerB)

    // Request 1 is held after its first chunk; request 2 answers at once.
    const answerA = `Answer one: ${markerA}`
    const answerB = `Answer two: ${markerB}`
    // The fake's request ids accumulate across the file's tests; every index
    // and release below is relative to this test's first request.
    const base = fake.requests().length
    fake.setScript({ chunks: ['Answer one: ', markerA], holdAfter: 1 })
    fake.setScript({ chunks: ['Answer two: ', markerB] })

    // Ask about A; the stream opens and is held.
    await pointAt(blockA)
    await expectGranted(page, [cmdA])
    const q1 = 'Question one about the first block?'
    await askFromPrompt(page, q1)
    const reqs1 = await fake.waitForRequests(base + 1)
    const req1 = reqs1[base]
    await expect.poll(() => fake.requests()[base]?.state).toBe('streaming')
    const blockQ1 = answerBlockOf(page, q1)
    await expect(blockQ1.locator('.cmd-output[data-answer-body]')).toContainText('Answer one: ', {
      timeout: 15_000,
    })

    // WHILE the first answer is still streaming, point at B: A's mark was
    // CONSUMED by the question that carried it, so exactly one block is
    // marked now and it is B. (Under the shipped gesture a question can
    // carry several blocks at once — that is the point of a count — but each
    // question takes the ones it rode with it.)
    await pointAt(blockB)
    await expectGranted(page, [cmdB])
    const q2 = 'Question two about the second block?'
    // The editor is still up: a question is not a handoff, so the agent
    // path never hides it and the next one can be typed straight away
    // (nocx-wmy4). This used to be the file's known defect — the first
    // submit hid the editor and nothing on the agent path re-showed it,
    // making the acceptance's own scenario unreachable through the shipped
    // surface. It is asserted here rather than assumed, because that is the
    // half a unit test with an explicit show() cannot see.
    await expect(page.locator(INPUT)).toBeVisible({ timeout: 10_000 })
    await askFromPrompt(page, q2)

    const reqs2 = await fake.waitForRequests(base + 2)
    const req2 = reqs2[base + 1]
    // Each payload carries its own block's output and no other's.
    expect(req1.body).toContain(markerA)
    expect(req1.body).not.toContain(markerB)
    expect(req2.body).toContain(markerB)
    expect(req2.body).not.toContain(markerA)
    // The second ask was made while the first stream was GENUINELY open —
    // a state fact, asserted directly: nothing has released request 1.
    expect(fake.requests()[base]?.state).toBe('streaming')

    // Release the first stream; both runs terminalize.
    fake.release(req1.id)
    await expect.poll(() => fake.requests()[base]?.state).toBe('done')
    await expect.poll(() => fake.requests()[base + 1]?.state).toBe('done')

    // The deltas landed on the RIGHT entries: two distinct answer blocks,
    // each holding its own answer and not the other's.
    const blockQ2 = answerBlockOf(page, q2)
    await expect(blockQ1).toHaveCount(1)
    await expect(blockQ2).toHaveCount(1)
    const body1 = blockQ1.locator('.cmd-output[data-answer-body]')
    const body2 = blockQ2.locator('.cmd-output[data-answer-body]')
    await expect(body1).toContainText(answerA, { timeout: 15_000 })
    await expect(body2).toContainText(answerB, { timeout: 15_000 })
    await expect(body1).not.toContainText(answerB)
    await expect(body2).not.toContainText(answerA)
    await expect(blockQ1.locator('.cmd-header-exit')).toHaveText('completed')
    await expect(blockQ2.locator('.cmd-header-exit')).toHaveText('completed')
  })

  test('the Test button on a saved endpoint probes with the STORED credential, and a typed key wins (nocx-reu5)', async ({
    page,
  }) => {
    // The serial describe shares one backend; the first endpoint-creating
    // test above set up the vault and minted a key into it, so a save with
    // a key works from here without the setup sheet.
    await openApp(page)
    await openAIEndpoints(page)

    const name = `E2E Probe ${nonce}`
    const storedKey = `stored-key-${nonce}`
    await page.getByRole('button', { name: '+ New endpoint' }).first().click()
    const newDialog = page.getByRole('dialog').filter({ hasText: 'New Endpoint' })
    await expect(newDialog).toBeVisible()
    await newDialog.locator('#endpoint-name').fill(name)
    await newDialog.locator('#endpoint-base-url').fill(fake.baseUrl())
    await newDialog.locator('#endpoint-key').fill(storedKey)
    await newDialog.getByRole('button', { name: 'Add model' }).click()
    await newDialog.locator('#endpoint-model-0-name').fill('e2e-model')
    await newDialog.getByRole('button', { name: 'Create Endpoint', exact: true }).click()
    await expect(newDialog).not.toBeVisible({ timeout: 10_000 })

    // ── Stored credential: the key SOURCE is the bound row ──────────────
    // Open the saved endpoint for editing. The key material never crosses
    // back (ADR-0030 §3): the source control (nocx-rzjw) opens on "Use
    // existing secret" with the bound row — there is no key input at all,
    // and the probe must still authenticate with the credential the
    // endpoint OWNS, resolved by the backend from the vault.
    await page.getByRole('button', { name: `Edit ${name}` }).click()
    const editDialog = page.getByRole('dialog').filter({ hasText: 'Edit Endpoint' })
    await expect(editDialog).toBeVisible()
    const picker = editDialog.locator('select')
    await expect(picker).toBeVisible()
    await expect(picker).toHaveValue(/^secrow:/)
    // The button must be ENABLED before the click: it is disabled while a
    // probe is in flight (testDisabled = probing()), and a click on a
    // disabled button is silently swallowed — leaving the dialog without a
    // verdict. The silent model-discovery probe (discoverModels) can be
    // mid-flight here, so wait on the observable state, never a duration.
    const testButton = editDialog.getByRole('button', { name: 'Test endpoint' })
    await expect(testButton).toBeEnabled()
    const probeBase = fake.requests().length
    await testButton.click()
    // The probe request, identified by its body: a chat-completions call
    // naming the stored model. The discovery probe carries the same
    // credential, so index arithmetic could read the wrong request.
    const probeReq = await waitForProbeRequest(fake, 'e2e-model', probeBase)
    expect(probeReq.authorization).toBe(`Bearer ${storedKey}`)
    // The probe succeeded end to end — a streamed answer through the real
    // backend, not merely a request that arrived.
    await expect(editDialog).toContainText(/e2e-model answered in \d+ ms/, { timeout: 15_000 })
    // The material was never sent back to the renderer: the source is still
    // the bound row after a probe that resolved the stored material.
    await expect(picker).toHaveValue(/^secrow:/)

    // ── A key typed into the form WINS over the stored one ──────────────
    const typedKey = `typed-key-${nonce}`
    // Switching the source to "Type a new one" reveals the EMPTY password
    // field — an input, never a stored value.
    await editDialog.getByRole('radio', { name: 'Type a new one' }).click()
    const keyInput = editDialog.locator('#endpoint-key')
    await expect(keyInput).toHaveValue('')
    await keyInput.fill(typedKey)
    await expect(testButton).toBeEnabled()
    const typedBase = fake.requests().length
    await testButton.click()
    const typedProbe = await waitForProbeRequest(fake, 'e2e-model', typedBase)
    expect(typedProbe.authorization).toBe(`Bearer ${typedKey}`)
    await expect(editDialog).toContainText(/e2e-model answered in \d+ ms/, { timeout: 15_000 })

    // ── A DECLARED keyless endpoint (a local model) probes without one ──
    // Keyless is a DECLARATION now, not an empty field (nocx-8e6v5): an
    // endpoint with no key and no declaration is a credential that has not
    // been supplied yet, and the two used to be one silent state. So the
    // person ticks the box, which is the gesture this half is about.
    await editDialog.getByRole('button', { name: 'Cancel' }).click()
    const localName = `E2E Local ${nonce}`
    await page.getByRole('button', { name: '+ New endpoint' }).first().click()
    const localDialog = page.getByRole('dialog').filter({ hasText: 'New Endpoint' })
    await expect(localDialog).toBeVisible()
    await localDialog.locator('#endpoint-name').fill(localName)
    await localDialog.locator('#endpoint-base-url').fill(fake.baseUrl())
    await localDialog
      .getByRole('checkbox', { name: 'This endpoint does not require an API key' })
      .check()
    // The key field goes away with the declaration — there is nothing left
    // to type, which is the point of saying so once.
    await expect(localDialog.locator('#endpoint-key')).toHaveCount(0)
    await localDialog.getByRole('button', { name: 'Add model' }).click()
    await localDialog.locator('#endpoint-model-0-name').fill('e2e-model')
    await localDialog.getByRole('button', { name: 'Create Endpoint', exact: true }).click()
    await expect(localDialog).not.toBeVisible({ timeout: 10_000 })
    await page.getByRole('button', { name: `Edit ${localName}` }).click()
    const localEdit = page.getByRole('dialog').filter({ hasText: 'Edit Endpoint' })
    await expect(localEdit).toBeVisible()
    await expect(localEdit.getByRole('button', { name: 'Test endpoint' })).toBeEnabled()
    const localBase = fake.requests().length
    await localEdit.getByRole('button', { name: 'Test endpoint' }).click()
    const localProbe = await waitForProbeRequest(fake, 'e2e-model', localBase)
    expect(localProbe.authorization).toBe('')
    await expect(localEdit).toContainText(/e2e-model answered in \d+ ms/, { timeout: 15_000 })
  })

  test('an endpoint CREATED through a SEALED vault raises the unlock sheet and lands WITH its key (nocx-4egm)', async ({
    page,
  }) => {
    // The serial file shares one backend whose passphrase vault was set up
    // by the first endpoint test above. Restarting the backend leaves that
    // vault SEALED (the file provider's data key derives from the
    // passphrase), and the app comes up normally — the derived content key
    // needs no startup unlock, so the sealed state is the state the save
    // meets. This is the create path: a NEW endpoint whose key must be
    // minted through the vault, not a rotation of an existing one.
    endpoint = await backend.restart()
    await openApp(page)
    await openAIEndpoints(page)

    const name = `E2E Sealed ${nonce}`
    const sealedKey = `sealed-key-${nonce}`
    await page.getByRole('button', { name: '+ New endpoint' }).first().click()
    const dialog = page.getByRole('dialog').filter({ hasText: 'New Endpoint' })
    await expect(dialog).toBeVisible()
    await dialog.locator('#endpoint-name').fill(name)
    await dialog.locator('#endpoint-base-url').fill(fake.baseUrl())
    await dialog.locator('#endpoint-key').fill(sealedKey)
    await dialog.getByRole('button', { name: 'Add model' }).click()
    await dialog.locator('#endpoint-model-0-name').fill('e2e-model')
    await dialog.getByRole('button', { name: 'Create Endpoint', exact: true }).click()

    // The unlock sheet, raised by the save: the mint failed with
    // vault-sealed (the reason now rides the wire, ws_endpoints.go) and the
    // dispatcher's sealed seam keeps the request pending behind the prompt.
    const unlockSheet = page
      .locator('.ui-prompt-overlay')
      .filter({ has: page.locator('#vault-unlock-passphrase') })
    await expect(unlockSheet).toBeVisible({ timeout: 10_000 })
    await page.locator('#vault-unlock-passphrase').fill(`vault-pass-${nonce}`)
    await page.getByRole('dialog').getByRole('button', { name: 'Unlock', exact: true }).click()
    await expect(unlockSheet).not.toBeVisible({ timeout: 10_000 })

    // The re-sent request carried the key: the save waits for it, so the
    // dialog closes only after the record exists. Before the closeDialog fix
    // this closed and toasted "Saved" while the create was still in flight,
    // leaving no row and no error — so the row's EXISTENCE is the assertion,
    // and the key itself is proved two steps down, where the Test resolves
    // the STORED credential and the fake reports the material it received.
    await expect(dialog).not.toBeVisible({ timeout: 10_000 })
    const savedRow = page.locator('.ui-collection-row').filter({ hasText: name })
    await expect(savedRow).toBeVisible({ timeout: 10_000 })
    // The stored material is the key the form was filled with — the Test
    // button resolves the STORED credential and the fake records it.
    await page.getByRole('button', { name: `Edit ${name}` }).click()
    const editDialog = page.getByRole('dialog').filter({ hasText: 'Edit Endpoint' })
    await expect(editDialog).toBeVisible()
    await expect(editDialog.getByRole('button', { name: 'Test endpoint' })).toBeEnabled()
    const sealedBase = fake.requests().length
    await editDialog.getByRole('button', { name: 'Test endpoint' }).click()
    const sealedProbe = await waitForProbeRequest(fake, 'e2e-model', sealedBase)
    expect(sealedProbe.authorization).toBe(`Bearer ${sealedKey}`)
    await expect(editDialog).toContainText(/e2e-model answered in \d+ ms/, { timeout: 15_000 })
  })

  test('a sealed vault raises unlock from an ask and the same run completes', async ({ page }) => {
    endpoint = await backend.restart()
    await openApp(page)
    await backToTerminal(page)

    const question = `sealed ask ${nonce}`
    const base = fake.requests().length
    fake.setScript({ chunks: ['answered after unlock'] })
    await askFromPrompt(page, question)

    const unlock = page
      .locator('.ui-prompt-overlay')
      .filter({ has: page.locator('#vault-unlock-passphrase') })
    await expect(unlock).toBeVisible({ timeout: 10_000 })
    expect(fake.requests()).toHaveLength(base)
    await page.locator('#vault-unlock-passphrase').fill(`vault-pass-${nonce}`)
    await unlock.getByRole('button', { name: 'Unlock', exact: true }).click()
    await expect(unlock).not.toBeVisible({ timeout: 10_000 })

    await fake.waitForRequests(base + 1)
    const answer = answerBlockOf(page, question)
    await expect(answer.locator('.cmd-output[data-answer-body]')).toContainText(
      'answered after unlock',
      { timeout: 15_000 },
    )
    await expect(answer.locator('.cmd-header-exit')).toHaveText('completed', {
      timeout: 15_000,
    })
  })

  test('cancelling an ask unlock stops its durable run without calling the model', async ({
    page,
  }) => {
    endpoint = await backend.restart()
    await openApp(page)
    await backToTerminal(page)

    const question = `cancel sealed ask ${nonce}`
    const base = fake.requests().length
    await askFromPrompt(page, question)
    const unlock = page
      .locator('.ui-prompt-overlay')
      .filter({ has: page.locator('#vault-unlock-passphrase') })
    await expect(unlock).toBeVisible({ timeout: 10_000 })
    await unlock.getByRole('button', { name: 'Cancel', exact: true }).click()
    await expect(unlock).not.toBeVisible({ timeout: 10_000 })

    const answer = answerBlockOf(page, question)
    await expect(answer.locator('.cmd-header-exit')).toHaveText('stopped', {
      timeout: 15_000,
    })
    expect(fake.requests()).toHaveLength(base)
    await expect(answer).not.toContainText('failed')
  })
})

/**
 * e2e: ask about a frozen block through real pi in a disposable home.
 * A marked block grants its whole output; the scripted model reads the
 * announced item through session.read and answers from that result.
 */
import { expect, type Locator, type Page } from '@playwright/test'
import { mkdtempSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import {
  standalone as base,
  answerPermission,
  appReadyForInput,
  bindEndpoint,
  ensureAssistantVault,
  settingsReady,
  VaultBackend,
} from './harness'
import { readStand } from './stand'
import { configurePiHome } from './pi-home'
import { FakeOpenAI } from './fake-openai'
import { attachedItemID, terminalContext, toolResults } from './ask-gesture'

/** Lazily, not at module scope: the stand is started by globalSetup, which
 *  runs after Playwright has collected this file. */
const serverBin = () => readStand().server

const TITLE = '.nocx-tab-title'
const INPUT = '.pane.active .nocx-editor-input'
const SETTINGS_POLICY_NAV = '.ui-grouped-nav__item[data-item="policy"]'
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
const GRANT_CHIP = '.pane.active [data-control="grant"]'
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

test.describe.configure({ mode: 'default' })

test.beforeAll(async () => {
  fake = new FakeOpenAI()
  await fake.start()
  const root = mkdtempSync(join(tmpdir(), 'nocx-x8s2-e2e-'))
  // This backend and real pi share the disposable home.
  backend = new VaultBackend(serverBin(), { root })
  endpoint = await backend.start(configurePiHome(root, fake.baseUrl()))
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
  await appReadyForInput(page)
}

/** Open Settings via the keyboard shortcut and select the Endpoints page in
 *  the rail (renamed from AI Endpoints; the Assistant group carries the AI)
 *  — the surface a user configures the assistant with
 *  (the connections-settings.spec.ts walk). */
async function backToTerminal(page: Page): Promise<void> {
  await page.locator(TITLE).first().click()
  const input = page.locator(INPUT)
  await expect(input).toBeVisible({ timeout: 10_000 })
  await input.click()
  await expect(input).toBeFocused({ timeout: 10_000 })
}

/** Create this file's endpoint through the surface a user uses, and the vault
 *  its key is minted into. The Endpoints page must already be open.
 *
 *  A fresh home has NO vault, and the endpoint's key is minted INTO the vault
 *  (design §4.5.3). The first save therefore FAILS the mint — and the surface
 *  must answer with the vault setup sheet (the operation-first wrapper nocx-8rwj
 *  added, saveSecretWithVault) and retry THIS save once the vault exists. This
 *  is the owner's exact path (nocx-4egm): the key was typed before the vault
 *  existed, and the save must land WITH the key.
 *
 *  The assertions are part of the walk rather than left to the caller: a helper
 *  that only clicked would let a silent refusal through, and every asking case
 *  depends on this having worked. */
async function ensureAskingPrerequisites(page: Page): Promise<void> {
  await ensureAssistantVault(page, endpoint, `vault-pass-${nonce}`)
  await page.locator(TITLE).first().click()
  await page.keyboard.press('Meta+,')
  await settingsReady(page)
  await page.locator(SETTINGS_POLICY_NAV).click()
  await answerPermission(page, 'observe', 'Allowed')
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
  const block = page
    .locator('.cmd-block[data-block-kind="command"]:not(.cmd-block-running)', { hasText: marker })
    .first()
  await expect(block).toBeVisible({ timeout: 15_000 })
  await expect(block.locator('.cmd-output .term-grid-row').first()).toBeVisible({ timeout: 15_000 })
  return { block }
}

/** The real target chord, as a person presses it — the same gesture
 *  askFromPrompt uses, on its own, for specs that need Ask active as setup
 *  rather than as the thing under test. */
async function switchToAsk(page: Page): Promise<void> {
  const indicator = page.locator('.pane.active .ui-mode-indicator:visible')
  if ((await indicator.getAttribute('data-target')) === 'agent') return
  await page.locator(INPUT).click()
  await page.keyboard.press('ControlOrMeta+Enter')
  await expect(indicator).toHaveAttribute('data-target', 'agent', { timeout: 10_000 })
}

/** RUN OFFERS NOTHING, AND THE CHORD IS WHAT CHANGES THAT (nocx-a7mw7.5,
 * nocx-a7mw7.6). Asserts the pane really is in Run, that neither the block
 * menu nor a real scrollback selection produces a grant there, and that the
 * REAL cross-platform target chord — pressed with the focus wherever the
 * selection left it — reveals the offer over the same Range, with no second
 * selection event.
 *
 * NOT A BRANCH INSIDE pointAt. It used to be one, read off the live
 * indicator, so at the call sites where Ask was already active every Run
 * assertion silently did nothing — and had the pane ever started in Ask, the
 * whole half would have gone on passing while testing nothing. */
async function selectWholeOutput(block: Locator): Promise<void> {
  await block.evaluate((el) => {
    const lines = Array.from(el.querySelectorAll<HTMLElement>('.cmd-output .term-grid-row'))
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

/** THE OFFER MUST OUTLIVE THE COMPOSER, NOT OUTRUN IT (nocx-45vkz).
 *
 *  CodeMirror restores the DOM selection into its own document while its
 *  contentDOM is the active element, and a selection made without taking
 *  focus off it was collapsed a frame or two later — the offer vanished, and
 *  this helper passed only because it clicked within a few milliseconds. The
 *  scrollback takes the focus when it claims the selection, so the observable
 *  to wait on is that transfer: with the composer unfocused there is nothing
 *  left to restore. Then a pair of frames, which is when the restore would
 *  have run, and the offer is still there to press. Frames and a DOM state,
 *  never a duration. */
async function confirmOffer(page: Page): Promise<void> {
  const offer = page.locator('.mark-affordance .ui-button')
  await expect(offer).toBeVisible({ timeout: 10_000 })
  await expect
    .poll(() => page.evaluate(() => String(document.activeElement?.className ?? '')), {
      timeout: 10_000,
    })
    .not.toContain('nocx-editor-input')
  await page.evaluate(
    () =>
      new Promise<void>((done) => requestAnimationFrame(() => requestAnimationFrame(() => done()))),
  )
  await expect(offer).toBeVisible()
  await offer.click()
}

/** THE GESTURE (nocx-4wtlh, nocx-wcswn, nocx-a7mw7.4): with Ask active,
 * select a region of a finished block's output and the product OFFERS to
 * mark it; pressing the offer marks the WHOLE BLOCK for the next question —
 * "if you ask, this comes with you". A selection is a quote and a grant,
 * never a row range: the payload is the block, so the mark is on the block.
 *
 * THE SELECTION ALONE MARKS NOTHING, and the confirmation is the second half
 * of this helper (nocx-a7mw7.4). Selecting output to read it, or to copy it,
 * used to mark it with nothing confirmed; a spec that skipped the button
 * would go on passing after the offer stopped being offered. */
async function pointAt(block: Locator): Promise<void> {
  const page = block.page()
  await expect(block.locator('.cmd-output .term-grid-row').first()).toBeVisible({ timeout: 15_000 })
  await expect(page.locator('.pane.active .ui-mode-indicator:visible')).toHaveAttribute(
    'data-target',
    'agent',
  )
  // The state a person points FROM: mid-draft, so the composer holds the
  // focus. Put there deliberately rather than left to whatever the previous
  // step happened to focus, because it is the whole difficulty — see
  // confirmOffer.
  await page.locator(INPUT).click()
  await selectWholeOutput(block)
  await confirmOffer(page)
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

/** Derive the scripted answer only from non-system, non-user messages. The
 *  marker therefore has to arrive in session.read's result, not merely in the
 *  command label the prompt announces. */
function answerFromAttachedItem(body: string, marker: string): string[] {
  const toolMessages = toolResults(body).join('\n')
  return toolMessages.includes(marker)
    ? ['The first block output contains ', marker, '.']
    : ['The marked block output was not delivered.']
}

test.describe('agent ask about a frozen block (nocx-x8s2.2)', () => {
  test.describe.configure({ mode: 'serial' })
  test.use({ viewport: { width: 1280, height: 900 } })

  test('a restored tool mentioning a command does not hide its frozen output', async ({ page }) => {
    await openApp(page)
    await backToTerminal(page)
    const marker = `tool-collision-${nonce}`
    // A restored session.read tool block can precede a new command and name
    // that same command in its header. It is not the command's output block.
    await page.evaluate((text) => {
      const tool = document.createElement('div')
      tool.className = 'cmd-block'
      tool.dataset.blockKind = 'tool'
      tool.textContent = `session.read session="echo ${text}"`
      document.querySelector('.pane.active')!.prepend(tool)
    }, marker)
    const { block } = await runCommand(page, `echo ${marker}`, marker)
    await expect(block).toHaveAttribute('data-block-kind', 'command')
    await expect(block.locator('.cmd-output .term-grid-row').first()).toContainText(marker)
  })

  test("point at a finished block, ask, and the answer streams in naming the block's output", async ({
    page,
  }) => {
    await openApp(page)
    await ensureAskingPrerequisites(page)

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
    // Script the fake to call session.read with the id the product actually
    // announces, then derive the answer from that tool result. A fixed answer
    // would pass even if the grant disappeared before the model request.
    const base = fake.requests().length
    fake.setScript({
      chunks: [],
      toolCalls: (body) => [{ name: 'session.read', arguments: { id: attachedItemID(body) } }],
    })
    fake.setScript({
      chunks: (body) => answerFromAttachedItem(body, markerA),
      holdAfter: 1,
    })
    // Ask has to be active before the offer exists (nocx-a7mw7.5). The Run
    // half of that rule has its own coverage in pointAtFromRun; here the
    // flip is setup, made through the same real chord.
    await switchToAsk(page)
    await pointAt(blockA)

    // The mark is on the block and the count is in the INPUT LINE BEFORE the
    // question is sent — the payload is what the person pointed at, and they
    // can see it while they type.
    await expectGranted(page, [cmdA])

    // ── The question, in the ordinary editor ─────────────────────────────
    const question = 'What did the first block print?'
    await askFromPrompt(page, question)

    // Two model requests are required: the first proposes session.read and
    // the second receives its result. The backend must resolve the announced
    // id; otherwise the derived answer below says the read was absent.
    const reqs = await fake.waitForRequests(base + 2)
    const askRequests = reqs.slice(base).filter((request) => request.body.includes('"messages"'))
    expect(askRequests).toHaveLength(2)
    const requestBeforeRead = askRequests[0]
    const requestAfterRead = askRequests[1]
    expect(requestBeforeRead.path.endsWith('/chat/completions')).toBe(true)
    expect(requestBeforeRead.authorization).toBe('Bearer disposable-e2e-key')
    const beforeContext = terminalContext(requestBeforeRead.body)
    const afterContext = terminalContext(requestAfterRead.body)
    expect(beforeContext).toContain('Attached terminal content')
    expect(beforeContext).toContain('state: exited')
    expect(beforeContext).toContain(cmdA)
    expect(beforeContext).not.toContain(markerB)
    expect(afterContext).toContain(markerA)
    expect(afterContext).not.toContain(markerB)

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
    await expect(answerBody).toContainText('The first block output contains ', { timeout: 15_000 })
    await expect
      .poll(() => fake.requests().find((request) => request.id === requestAfterRead.id)?.state)
      .toBe('streaming')

    // Release the held stream: the rest arrives, the run terminalizes, and
    // the block settles to the success outcome — silently, the surface's own
    // way of saying "the answer finished" (spec 2026-09-14 §3.1).
    fake.release(requestAfterRead.id)
    const answer = `The first block output contains ${markerA}.`
    await expect(answerBody).toContainText(answer, { timeout: 15_000 })
    await expect
      .poll(() => fake.requests().find((request) => request.id === requestAfterRead.id)?.state)
      .toBe('done')
    await expect(answerBlock).toHaveAttribute('data-outcome', 'success', {
      timeout: 15_000,
    })
    // Answer-block identity: the question's block is the agent's answer
    // block (header = question, [data-answer-body] output, success outcome),
    // not a shell command block — its body carries no shell error. The
    // stronger "nothing shell ran" half (zero pty bytes, no lifecycle
    // attempt, no running block) is proven by the unit suite's ask-seam
    // tests (terminal-content.test.ts), which can observe the seam this e2e
    // cannot.
    await expect(answerBody).not.toContainText('command not found')
  })

  test('a second ask waits for the first to end and both answers land in order', async ({
    page,
  }) => {
    await openApp(page)
    // Set this journey's read permission independently of the other tests.
    await ensureAskingPrerequisites(page)
    await backToTerminal(page)

    // Fresh blocks for this test (a fresh page has a fresh scrollback).
    const markerA = `two-alpha-${nonce}`
    const markerB = `two-beta-${nonce}`
    const cmdA = `echo ${markerA}`
    const cmdB = `echo ${markerB}`
    const { block: blockA } = await runCommand(page, cmdA, markerA)
    const { block: blockB } = await runCommand(page, cmdB, markerB)

    // Request 1 stays open until released. Request 2 must wait behind it.
    const answerA = `Answer one: ${markerA}`
    const answerB = `Answer two: ${markerB}`
    // The fake's request ids accumulate across the file's tests; every index
    // and release below is relative to this test's first request.
    const base = fake.requests().length
    let secondStartedAfterFirstFinished = false
    fake.setScript({ chunks: ['Answer one: ', markerA], holdAfter: 1 })
    fake.setScript({
      chunks: () => {
        secondStartedAfterFirstFinished = fake.requests()[base]?.state === 'done'
        return ['Answer two: ', markerB]
      },
    })

    // Ask about A; the stream opens and is held.
    // Ask has to be active before the offer exists (nocx-a7mw7.5). The Run
    // half of that rule has its own coverage in pointAtFromRun; here the
    // flip is setup, made through the same real chord.
    await switchToAsk(page)
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

    // The second question has been accepted into the pane, but cannot reach
    // the model while the first turn is open. No steering is requested here.
    const blockQ2 = answerBlockOf(page, q2)
    await expect(blockQ2).toHaveCount(1)
    await expect(page.locator(INPUT)).toHaveText('')
    expect(fake.requests()[base]?.state).toBe('streaming')
    expect(fake.requests()).toHaveLength(base + 1)
    await expect(blockQ1).not.toHaveAttribute('data-outcome', 'success')
    await expect(blockQ2.locator('.cmd-output[data-answer-body]')).toHaveCount(0)

    // Ending the first turn releases the queued question. Each new user
    // message carries only its own attachment; the conversation may still
    // include the earlier question and answer as history.
    fake.release(req1.id)
    await expect(blockQ1).toHaveAttribute('data-outcome', 'success')
    const reqs2 = await fake.waitForRequests(base + 2)
    const req2 = reqs2[base + 1]
    expect(terminalContext(req1.body)).toContain(markerA)
    expect(terminalContext(req1.body)).not.toContain(markerB)
    expect(terminalContext(req2.body)).toContain(q2)
    expect(terminalContext(req2.body)).toContain(markerB)
    expect(terminalContext(req2.body)).not.toContain(markerA)
    await expect.poll(() => fake.requests()[base]?.state).toBe('done')
    await expect.poll(() => fake.requests()[base + 1]?.state).toBe('done')
    expect(
      secondStartedAfterFirstFinished,
      'the second model call started before the first ended',
    ).toBe(true)

    // The deltas landed on the RIGHT entries: two distinct answer blocks,
    // each holding its own answer and not the other's.
    await expect(blockQ1).toHaveCount(1)
    await expect(blockQ2).toHaveCount(1)
    const body1 = blockQ1.locator('.cmd-output[data-answer-body]')
    const body2 = blockQ2.locator('.cmd-output[data-answer-body]')
    await expect(body1).toContainText(answerA, { timeout: 15_000 })
    await expect(body2).toContainText(answerB, { timeout: 15_000 })
    await expect(body1).not.toContainText(answerB)
    await expect(body2).not.toContainText(answerA)
    await expect(blockQ1).toHaveAttribute('data-outcome', 'success')
    await expect(blockQ2).toHaveAttribute('data-outcome', 'success')
    const questions = page
      .locator('.pane.active .cmd-block')
      .filter({ hasText: /Question (one|two) about/ })
    await expect(questions).toHaveCount(2)
    await expect(questions.nth(0)).toContainText(q1)
    await expect(questions.nth(1)).toContainText(q2)
  })
})

// Subject leaves with endpoints/eino. Keep their serial dependencies separate
// so a removed endpoint surface cannot skip the surviving assistant journeys.

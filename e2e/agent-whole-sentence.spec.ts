/**
 * e2e: THE EPIC'S OWN GATE (nocx-hp8p2.2) — one person, one run, the whole
 * sentence nocx-hp8p2 promises:
 *
 *   mark a block → ask about it → the ask carries that item → the assistant
 *   announces a tool and runs it → the person stops it → a seated answer whose
 *   header says stopped
 *
 * WHY THIS FILE EXISTS WHEN FOUR SPECS ARE ALREADY GREEN. The sentence is
 * covered in pieces today: agent-ask.spec.ts marks, asks, and checks the
 * payload; agent-turn.spec.ts watches a tool announced and run inside a turn;
 * agent-refusal-stop.spec.ts stops a streaming answer and keeps its prose; the
 * stopped-header assertions are spread across several files. Four green pieces
 * do not prove one journey — nothing establishes that the SAME marked block
 * reaches the model, that the tool the model then calls is the one announced,
 * and that stopping THAT turn seats THAT answer with a stopped header. AGENTS.md
 * testing rule 2 is explicit that this is the only check that can report a
 * missing feature; a suite of units can only confirm that what was written does
 * what it was written to do.
 *
 * ONE FILE, ONE TEST, on purpose. A second test would mean the sentence had
 * been split again, which is the thing this bead exists to stop.
 *
 * THE SIX PROMISES, and how each is made able to fail on its own. Every
 * assertion below carries a `PROMISE n` message, so a red run names which one
 * broke without anybody opening a trace.
 *
 *   1  MARKED     — the mark is on THAT block (`data-granted`) and the input
 *                   line's chip counts it, before a word is typed.
 *   2  CARRIED    — asserted on the REAL request body reaching the fake
 *                   endpoint: the system prompt names the marked block's
 *                   announced id and its command, and the OTHER block on
 *                   screen is ABSENT from it. The absent half is the whole
 *                   assertion — without it, "everything is attached" passes.
 *   3  ANNOUNCED  — the tool child names the tool and the arguments it ran on,
 *                   AND the prose the person reads is DERIVED from the tool's
 *                   real return. A fixed string would say the marker because
 *                   the spec typed it into the fixture; a derived one says it
 *                   only if the marker came back through the tool.
 *   4  STOPPED BY — the real gesture: the turn's own ⋮ → Stop. Never a client
 *      THE PERSON   call. And the held model response is asserted STILL OPEN
 *                   afterwards, so the turn terminalized because the person
 *                   stopped it and not because the model finished.
 *   5  SEATED     — the answer is a TOP-LEVEL block (its parent is the flow,
 *                   not another block's `.cmd-children`), it sits below the two
 *                   command blocks that preceded it, and it still holds its
 *                   five children in seat order.
 *   6  SAYS       — the ask kind's cancelled word ("stopped") on the turn's OWN
 *      STOPPED      header chip. The turn contains a shell command block with
 *                   its own `.cmd-header-exit` reading "ok", so the `:scope >`
 *                   scoping is load-bearing and is asserted to be: reading the
 *                   chip unscoped inside this turn is ambiguous by
 *                   construction. (assistant-intake once shipped three cases
 *                   waiting on `.cmd-header-exit` where the block had none, so
 *                   all three passed whether or not the feature worked —
 *                   44b0dfc9. A chip assertion that cannot tell you WHICH chip
 *                   it read is the same defect wearing a different hat.)
 *
 * THE TURN THIS DRIVES — three model rounds, the interleave agent-turn.spec.ts
 * proved (the run continues only while a response ends in a tool proposal, so a
 * content-only round between tools would terminate it):
 *
 *   round 1  prose  + session.read(id = the MARKED block's announced id)
 *   round 2  prose derived from that read's result + session.run(echo <marker>)
 *   round 3  prose derived from the run's result, HELD after its first chunk
 *            ── the person stops the turn here ──
 *
 * which seats five children: text, tool, text, command, text. The command child
 * is what gives promise 6 something to be confused with, and the read of the
 * marked block is what ties promises 1, 2 and 3 into one claim rather than
 * three adjacent ones.
 *
 * NOTHING HERE WAITS OUT A DURATION. Every wait polls an observable state — a
 * DOM state, a recorded request, a rendered box. A spec that needs a slow
 * machine to pass is broken on a fast one too (AGENTS.md).
 *
 * THE BACKEND IS THIS FILE'S OWN nocx-server on a disposable home, so the
 * endpoint it configures never leaks into the shard's shared stand and the
 * fresh-dev-stand state (no endpoint, no vault) is real here.
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
  createAiEndpoint,
  setDefaultModel,
  settingsReady,
  VaultBackend,
} from './harness'
import { readStand } from './stand'
import { FakeOpenAI } from './fake-openai'
import {
  INPUT,
  askFromPrompt,
  attachedItemID,
  chatRequests,
  expectGranted,
  markBlock,
  runCommand,
  switchToAsk,
  systemPrompt,
  toolResults,
} from './ask-gesture'

/** Lazily, not at module scope: the stand is started by globalSetup, which runs
 *  after Playwright has collected this file. */
const serverBin = () => readStand().server

const TITLE = '.nocx-tab-title'
const SETTINGS_AI_NAV = '.ui-grouped-nav__item[data-item="endpoints"]'
const SETTINGS_ROLES_NAV = '.ui-grouped-nav__item[data-item="roles"]'
const SETTINGS_POLICY_NAV = '.ui-grouped-nav__item[data-item="policy"]'
const APPROVAL_TITLE = 'This action needs your approval'

/** The effect classes session.run declares (internal/agenttools/registry.go)
 *  plus `observe` for session.read. Allowed through the real Settings surface
 *  so the two proposed calls EXECUTE rather than raise an approval sheet — the
 *  sheet has its own coverage (agent-tool-approval.spec.ts) and is not the
 *  sentence under test here. Which class a given command is classified as is
 *  the safety analyser's business (nocx-hv53g), so the whole set session.run can
 *  land in is allowed rather than the one class `echo` looks like today. */
const ALLOWED_EFFECTS = [
  'observe',
  'mutate-reversible',
  'mutate-destructive',
  'cross-boundary',
  'delegate',
] as const

const test = base
const nonce = Date.now().toString(36)

const ENDPOINT_NAME = `E2E Sentence ${nonce}`
/** The block the person MARKS, and the marker only its output carries. */
const MARKED_MARKER = `sentence-marked-${nonce}`
const MARKED_CMD = `echo ${MARKED_MARKER}`
/** A second block on screen that is marked by NOTHING. Promise 2's absent half
 *  is asserted against it: over-attachment is invisible unless something that
 *  was not pointed at is proven to have stayed behind. */
const OTHER_MARKER = `sentence-other-${nonce}`
const OTHER_CMD = `echo ${OTHER_MARKER}`
/** What the assistant's own command prints — the marker its tool RESULT must
 *  carry back for round 3's prose to be derivable. */
const RUN_MARKER = `sentence-ran-${nonce}`
const RUN_CMD = `echo ${RUN_MARKER}`

const QUESTION = `What did the block I marked print, ${nonce}?`
const ANNOUNCE = `Let me read the block you marked, ${nonce}.`
/** The suffix round 3 would go on to write. The person stops the turn first, so
 *  it must never appear — which is what makes the stop a fact rather than a
 *  gesture that happened to precede the end of a stream. */
const NEVER = ` And this sentence must never arrive, ${nonce}.`

let backend: VaultBackend
let fake: FakeOpenAI
let endpoint: { port: number; token: string }

test.beforeAll(async () => {
  fake = new FakeOpenAI()
  await fake.start()
  const root = mkdtempSync(join(tmpdir(), 'nocx-hp8p2-2-e2e-'))
  // No Secret Service for this backend regardless of the session the suite runs
  // in: the container has no keychain to ask, and the derived content key makes
  // the vault available without user setup.
  backend = new VaultBackend(serverBin(), { root })
  endpoint = await backend.start()
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

async function openSettings(page: Page, navSelector: string): Promise<void> {
  await page.keyboard.press('Meta+,')
  await settingsReady(page)
  await page.locator(navSelector).click()
}

async function backToTerminal(page: Page): Promise<void> {
  await page.locator(TITLE).first().click()
  const input = page.locator(INPUT)
  await expect(input).toBeVisible({ timeout: 10_000 })
  await input.click()
  await expect(input).toBeFocused({ timeout: 10_000 })
}

/** The assistant, configured entirely through the surfaces a person uses: the
 *  Endpoints form (which mints the key into a vault that does not exist yet and
 *  is created by the same save), the Roles default, and the policy matrix. */
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
  for (const effect of ALLOWED_EFFECTS) {
    // answerPermission waits on the LISTED answer, which is the store's own
    // word for what it took — not the draft this test just clicked.
    await answerPermission(page, effect, 'Allowed')
  }
  await backToTerminal(page)
}

/** The turn: the one top-level `.cmd-block` whose header IS the question. */
function turnBlock(page: Page): Locator {
  return page.locator('.pane.active .cmd-block').filter({ hasText: QUESTION })
}

/** The turn's own direct children, in seat order (ADR-0040). */
function turnChildren(page: Page): Locator {
  return turnBlock(page).locator(':scope > .cmd-children > .cmd-block')
}

/** The rendered TOP of each named block, in the order given. A flex column can
 *  reorder visually without reordering the DOM (CSS `order`), so "the answer is
 *  seated below what preceded it" is asserted off the boxes a person actually
 *  sees rather than off the DOM alone. A block scrolled above the viewport
 *  still has a box, with a smaller (possibly negative) top, which is the
 *  ordering fact this wants. */
async function topsOf(named: [string, Locator][]): Promise<number[]> {
  const tops: number[] = []
  for (const [name, locator] of named) {
    const box = await locator.boundingBox()
    if (!box) {
      throw new Error(
        `PROMISE 5 (the answer is seated): ${name} is not rendered at all, so nothing can be said about its seat`,
      )
    }
    tops.push(box.y)
  }
  return tops
}

/** Round 2's prose, derived ONLY from what session.read handed back. The
 *  fallback is deliberately a different sentence rather than a default `ok`:
 *  a missing tool result must be READABLE in the pane, not masked. */
function proseFromRead(body: string): string[] {
  const delivered = toolResults(body).some((result) => result.includes(MARKED_MARKER))
  return delivered
    ? [`The marked block printed ${MARKED_MARKER}. Now I will run a command.`]
    : ['The marked block never reached me, so I cannot say what it printed.']
}

/** Round 3's prose, derived ONLY from what session.run handed back — its
 *  result's `text` is the command's real output (contracts/tools/
 *  session.run.schema.json). Two chunks so the response can be HELD after the
 *  first: the person reads that first chunk and stops the turn, and the second
 *  is the sentence that proves the stop landed by never arriving. */
function proseFromRun(body: string): string[] {
  const delivered = toolResults(body).some((result) => result.includes(RUN_MARKER))
  const head = delivered
    ? `The command I ran printed ${RUN_MARKER}.`
    : 'The command result never reached me.'
  return [head, NEVER]
}

test.describe('one person, one run, the whole assistant sentence (nocx-hp8p2.2)', () => {
  test.use({ viewport: { width: 1280, height: 900 } })

  test('mark a block, ask about it, watch a tool run, stop it, and read a stopped answer', async ({
    page,
  }) => {
    test.setTimeout(180_000)
    await openApp(page)
    await configureAssistant(page)

    // ── The screen a person is looking at: two finished blocks whose outputs
    //    cannot be confused. Only ONE of them will be marked.
    const markedBlock = await runCommand(page, MARKED_CMD, MARKED_MARKER)
    const otherBlock = await runCommand(page, OTHER_CMD, OTHER_MARKER)

    // ══ PROMISE 1 — a block is marked ═════════════════════════════════════
    // The gesture: with Ask active, select the block's output and press the
    // offer. Selecting alone marks nothing — reading output is not asking
    // about it (nocx-a7mw7.4).
    await switchToAsk(page)
    await markBlock(markedBlock)
    await expectGranted(page, [MARKED_CMD], 'PROMISE 1 (a block is marked)')
    await expect(
      markedBlock,
      'PROMISE 1 (a block is marked): the mark is not on the block the selection came from',
    ).toHaveAttribute('data-granted', 'true')
    await expect(
      otherBlock,
      'PROMISE 1 (a block is marked): a block nobody pointed at came back marked',
    ).not.toHaveAttribute('data-granted', 'true')

    // ── The three rounds, scripted before the question goes out. Distinct call
    //    ids: each round's first call defaults to `call_1`, and the renderer
    //    dedupes by id — a reused id would drop the second call.
    const base = fake.requests().length
    fake.setScript({
      chunks: [ANNOUNCE],
      // The id the PRODUCT announced, read back off the request body — never
      // one this spec invented. A made-up id is refused for identity before
      // the call can run, so a green read is itself proof the grant survived
      // the frontend-to-ledger join.
      toolCalls: (body) => [
        { name: 'session.read', id: 'call_read', arguments: { id: attachedItemID(body) } },
      ],
    })
    fake.setScript({
      chunks: proseFromRead,
      toolCalls: [{ name: 'session.run', id: 'call_run', arguments: { command: RUN_CMD } }],
    })
    fake.setScript({ chunks: proseFromRun, holdAfter: 1 })

    // ── The question, typed into the ordinary editor (design §3.1) ─────────
    await askFromPrompt(page, QUESTION)

    // ══ PROMISE 2 — the ask carries that item ═════════════════════════════
    // Asserted on the REAL body that reached the fake, not on anything the
    // renderer says about itself.
    await expect
      .poll(() => chatRequests(fake, base).length, {
        timeout: 30_000,
        message: 'PROMISE 2 (the ask carries the marked item): no ask reached the model at all',
      })
      .toBeGreaterThan(0)
    const firstRound = chatRequests(fake, base)[0]
    expect(
      firstRound.path.endsWith('/chat/completions'),
      'PROMISE 2 (the ask carries the marked item): the ask did not go to /chat/completions',
    ).toBe(true)
    expect(
      firstRound.authorization,
      'PROMISE 2 (the ask carries the marked item): the stored key did not ride the ask',
    ).toBe(`Bearer e2e-key-${nonce}`)
    const askPrompt = systemPrompt(firstRound.body)
    expect(
      askPrompt,
      'PROMISE 2 (the ask carries the marked item): the ask carried no attached terminal content',
    ).toContain('Attached terminal content')
    expect(
      askPrompt,
      'PROMISE 2 (the ask carries the marked item): the marked block is not named in the ask',
    ).toContain(MARKED_CMD)
    expect(
      askPrompt,
      'PROMISE 2 (the ask carries the marked item): the marked block is not carried as an exited item',
    ).toContain('state: exited')
    // THE ABSENT HALF, which is the whole assertion: a block nobody pointed at
    // must not be in the payload. Without this, "everything is attached" passes.
    expect(
      askPrompt,
      'PROMISE 2 (the ask carries the marked item): an UNMARKED block was attached too — the ask over-attaches',
    ).not.toContain(OTHER_MARKER)
    // ...and the attached list holds exactly ONE item. The absence of a
    // particular marker is a claim about that marker; this is the claim about
    // the LIST, and it is what catches an ask that attaches whatever it can
    // find rather than what was pointed at.
    expect(
      (askPrompt.match(/\n- id: /g) ?? []).length,
      'PROMISE 2 (the ask carries the marked item): the ask attached more than the one block that was marked',
    ).toBe(1)
    // And the id the tool will be called with is the one the ASK announced —
    // read back off the body, so the tool below names the product's own id and
    // never one this spec invented. `attachedItemID` throws when the prompt
    // carries no item at all, which is promise 2 failing at its root.
    const announcedId = attachedItemID(firstRound.body)

    // ══ PROMISE 3 — the assistant announces a tool and runs it ════════════
    // First the ANNOUNCEMENT, in the pane: the tool child names the tool and
    // the argument that tells two calls of one tool apart (ADR-0040).
    const turn = turnBlock(page)
    await expect(
      turn,
      'PROMISE 3 (a tool is announced and run): the question opened no turn in the flow',
    ).toHaveCount(1, { timeout: 30_000 })
    const children = turnChildren(page)
    await expect(
      children,
      'PROMISE 3 (a tool is announced and run): the turn never seated its five children',
    ).toHaveCount(5, { timeout: 60_000 })

    const announceChild = children.nth(0)
    await expect(
      announceChild,
      'PROMISE 3 (a tool is announced and run): the turn does not open with the assistant saying what it is about to do',
    ).toContainText(ANNOUNCE)

    const toolChild = children.nth(1)
    await expect(
      toolChild,
      'PROMISE 3 (a tool is announced and run): the seat after the prose is not a tool announcement',
    ).toHaveAttribute('data-block-kind', 'tool')
    await expect(
      toolChild,
      'PROMISE 3 (a tool is announced and run): the announcement does not name session.read',
    ).toHaveAttribute('data-tool', 'session.read')
    // The ARGUMENTS are what tell two calls of one session-scoped tool apart
    // (ADR-0040), so the announcement has to say which item was read — and the
    // id it says must be the one the ask announced.
    await expect(
      toolChild.locator('.cmd-header-text'),
      'PROMISE 3 (a tool is announced and run): the announcement does not say WHICH item it read',
    ).toContainText(`id=${announcedId}`)

    // Then that it RAN, proved by the prose the person reads: round 2's text is
    // derived from the session.read RESULT and says the marker only if the
    // marker came back through the tool. A fixed string could have said it with
    // no tool at all.
    await expect(
      children.nth(2),
      "PROMISE 3 (a tool is announced and run): the prose is not derived from session.read's real return — the read never delivered the marked block",
    ).toContainText(`The marked block printed ${MARKED_MARKER}.`, { timeout: 60_000 })

    // The second tool OPENS A BLOCK, so it draws no child of its own: the
    // command block IS the account of the call (ADR-0040), with the agent's own
    // badge, the real command, the real output and the shell's exit chip.
    const runChild = children.nth(3)
    await expect(
      runChild,
      'PROMISE 3 (a tool is announced and run): session.run did not open a command block',
    ).toHaveAttribute('data-block-kind', 'command')
    await expect(
      runChild.locator('.cmd-header-text'),
      'PROMISE 3 (a tool is announced and run): the command block is not the command the model asked for',
    ).toContainText(RUN_CMD)
    await expect(
      runChild.locator('.cmd-output'),
      'PROMISE 3 (a tool is announced and run): the command block carries no real output',
    ).toContainText(RUN_MARKER, { timeout: 30_000 })
    await expect(
      runChild.locator('.ui-badge[data-author="agent"]'),
      'PROMISE 3 (a tool is announced and run): the command block does not say the assistant ran it',
    ).toBeVisible()
    await expect(
      runChild.locator('.cmd-header-exit'),
      'PROMISE 3 (a tool is announced and run): the command block has no exit status of its own',
    ).toHaveText('ok', { timeout: 30_000 })

    // Nothing above needed a person's approval, and that absence is asserted
    // only now — after the calls have demonstrably executed — so it is a fact
    // about the product rather than a race won at t=0.
    await expect(
      page.getByRole('dialog', { name: APPROVAL_TITLE }),
      'PROMISE 3 (a tool is announced and run): an approval sheet interrupted the run',
    ).toHaveCount(0)

    // ── The third round opens and is HELD after its first chunk. The partial
    //    prose on screen is what the person is reading when they decide to
    //    stop, and it is derived from the run's real return.
    await expect
      .poll(() => chatRequests(fake, base).length, {
        timeout: 60_000,
        message:
          'PROMISE 3 (a tool is announced and run): the two tool results never brought the model back for a third round',
      })
      .toBe(3)
    const thirdRound = chatRequests(fake, base)[2]
    const runResults = toolResults(thirdRound.body)
    expect(
      runResults.some((result) => result.includes(RUN_MARKER)),
      "PROMISE 3 (a tool is announced and run): session.run's real output never reached the model as a tool result",
    ).toBe(true)
    await fake.waitForState(thirdRound.id, 'streaming')
    const partialChild = children.nth(4)
    await expect(
      partialChild,
      "PROMISE 3 (a tool is announced and run): the answer prose is not derived from session.run's real return",
    ).toContainText(`The command I ran printed ${RUN_MARKER}.`, { timeout: 60_000 })

    // ══ PROMISE 4 — the person stops it ═══════════════════════════════════
    // Through the real gesture: the TURN's own ⋮ (`:scope > .cmd-header`, never
    // the command child's, which carries an identical button), then Stop.
    const overflow = turn.locator(':scope > .cmd-header .cmd-overflow-btn')
    await expect(
      overflow,
      'PROMISE 4 (the person stops it): the turn has no ⋮ of its own to stop it from',
    ).toBeVisible()
    await overflow.click()
    // The menu renders at document.body level so it floats above every scroll
    // container — it is deliberately NOT a descendant of the block.
    const stop = page.locator('.cmd-overflow-menu-item[data-action="stop"]')
    await expect(
      stop,
      'PROMISE 4 (the person stops it): a live turn offers no Stop in its menu',
    ).toBeVisible()
    await expect(
      stop,
      'PROMISE 4 (the person stops it): the Stop control is present but cannot be pressed',
    ).toBeEnabled()
    await stop.click()

    // ══ PROMISE 6 — the header says stopped ═══════════════════════════════
    // The ask kind's cancelled vocabulary, on the TURN's own chip.
    await expect(
      turn.locator(':scope > .cmd-header .cmd-header-exit'),
      'PROMISE 6 (the header says stopped): the turn did not settle on the stopped word',
    ).toHaveText('stopped', { timeout: 60_000 })
    // And the chip that was read is the turn's, not a nested one. The turn
    // CONTAINS a shell command block whose header carries its own
    // `.cmd-header-exit`, so an unscoped read inside this turn is ambiguous by
    // construction — which is what makes the scoping above an assertion rather
    // than a style. (assistant-intake shipped three cases waiting on a chip the
    // block did not have; all three passed whether or not the feature worked.)
    await expect(
      turn.locator('.cmd-header-exit'),
      'PROMISE 6 (the header says stopped): the turn no longer contains both its own chip and the command block’s, so the scoped read above proves nothing',
    ).toHaveCount(2)
    await expect(
      runChild.locator('.cmd-header-exit'),
      'PROMISE 6 (the header says stopped): the shell command block’s chip was overwritten with the turn’s outcome',
    ).toHaveText('ok')
    await expect(
      turn,
      'PROMISE 6 (the header says stopped): the stopped block is not the assistant turn',
    ).toHaveAttribute('data-block-kind', 'ask')

    // The stop is what ended it: the model's response is STILL OPEN — this test
    // never released the hold — and the sentence it would have gone on to write
    // never arrived. Without this, "the header says stopped" is satisfied by a
    // turn that simply finished.
    expect(
      fake.requests().find((request) => request.id === thirdRound.id)?.state,
      'PROMISE 4 (the person stops it): the model response completed on its own, so the turn was not ended by the person',
    ).toBe('streaming')
    await expect(
      turn,
      'PROMISE 4 (the person stops it): prose written after the stop still reached the answer',
    ).not.toContainText(NEVER)

    // ══ PROMISE 5 — the answer is seated in scrollback ════════════════════
    // A top-level block: its parent is the flow, not another block's children.
    expect(
      await turn.evaluate((el) => el.parentElement?.className ?? ''),
      'PROMISE 5 (the answer is seated): the answer is nested inside another block instead of standing in the flow',
    ).not.toContain('cmd-children')
    // In order: below the two command blocks that preceded it, as a person
    // reads down the pane. Off the rendered boxes, because a flex column can
    // reorder visually without reordering the DOM.
    const tops = await topsOf([
      ['the marked block', markedBlock],
      ['the unmarked block', otherBlock],
      ['the answer', turn],
    ])
    expect(
      tops,
      `PROMISE 5 (the answer is seated): the answer is not seated below the blocks that preceded it (tops: ${tops.join(', ')})`,
    ).toEqual([...tops].sort((a, b) => a - b))
    // And it survived the stop AS A BLOCK: five children, still in seat order,
    // still holding the prose the person had already read.
    await expect(
      children,
      'PROMISE 5 (the answer is seated): the stop took the turn’s children with it',
    ).toHaveCount(5)
    const seats = await children.evaluateAll((els) =>
      els.map((el) => (el as HTMLElement).dataset.blockKind ?? 'command'),
    )
    expect(
      seats,
      'PROMISE 5 (the answer is seated): the turn’s children are not in the order they happened',
    ).toEqual(['text', 'tool', 'text', 'command', 'text'])
    await expect(
      partialChild,
      'PROMISE 5 (the answer is seated): the prose the person had already read did not survive the stop',
    ).toContainText(`The command I ran printed ${RUN_MARKER}.`)
  })
})

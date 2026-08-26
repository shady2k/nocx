/**
 * e2e: a restored pane knows what each of its blocks WAS (nocx-4em1z).
 *
 * WHAT THIS FILE IS FOR. Three things can end up in a pane's scrollback and
 * they are not the same thing: a command a person typed, a command the
 * ASSISTANT ran, and a dialogue — a question with the assistant's prose under
 * it. Before this bead a restart lost the difference twice over. The agent's
 * attribution was dropped, because `createCommandBlock` took its author as an
 * optional argument and the restore path never passed one, so a restored tab
 * said a person had typed what the assistant ran. And a dialogue did not come
 * back at all: `SubmitAgentAsk` anchored its entries to a `session_id` while
 * the restore reads `WHERE pane_id = ?`, and a session is exactly the thing a
 * restart destroys (D5).
 *
 * Every unit around both halves was green. This is the check that watches a
 * person see all three come back as themselves, which is the only kind that
 * could have reported either (AGENTS.md testing rules 1 and 2).
 *
 * The seam, and where each half is decided:
 *
 * - THE BACKEND IS THIS FILE'S OWN, like restore.spec.ts, precisely so it can
 *   be killed and restarted mid-test. What survives has to survive through
 *   the encrypted store, not through a process that still remembers writing
 *   it.
 * - THE ASSISTANT'S TWO ACTS are scripted by `e2e/fake-openai.ts`: a
 *   content-only response for the dialogue, and a `run` tool proposal
 *   followed by the answer written from its output for the agent command.
 * - THE GATE for `run` is `mutate-destructive`, set to Allowed through
 *   Settings → Agent policy, the surface a person uses, so the proposal
 *   EXECUTES rather than suspending. agent-tool-approval.spec.ts owns the
 *   asking; this file needs the command to have actually run.
 * - THE SESSION ID IS LEARNED, NEVER INVENTED (AD-7): the policy's scope
 *   check compares a session resource for exact identity against the run's
 *   grant, so a made-up id is refused before the tool can run. The spec
 *   reads the id the product itself spelled, off the first ask's own frame.
 * - WHAT IS ASSERTED IS ON SCREEN. `data-block-kind` is the block's declared
 *   grammar (nocx-ex636) and `.ui-badge[data-author]` is the provenance mark
 *   the header paints — both are what a person sees, read as attributes so
 *   the claim is exact.
 *
 * NOTHING HERE WAITS OUT A DURATION. Before the backend is killed the spec
 * waits on the STORE holding each of the three, because the block appears at
 * submit and the body is written afterwards — a restart that does not wait
 * for the write is measuring that race rather than the restore
 * (restore.spec.ts learned this the hard way, and `storedWith` below is that
 * lesson).
 */
import { test as base, expect, type Page } from '@playwright/test'
import { mkdtempSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import {
  VaultBackend,
  bindEndpoint,
  clickIntoEditor,
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
const SETTINGS_AI_NAV = '.ui-grouped-nav__item[data-item="endpoints"]'
const SETTINGS_ROLES_NAV = '.ui-grouped-nav__item[data-item="roles"]'
const SETTINGS_POLICY_NAV = '.ui-grouped-nav__item[data-item="policy"]'
/** `run` is declared mutate-destructive in internal/agenttools/registry.go. */
const DESTRUCTIVE_ROW = '.st-policy__row[data-effect="mutate-destructive"]'
const APPROVAL_TITLE = 'This action needs your approval'
const RESTORED = '.pane.active .cmd-block[data-restored="true"]'

const test = base
const nonce = Date.now().toString(36)

/** What each of the three is, in the product's own words. Unique per run so
 *  a marker cannot be matched against another spec's leftovers. */
const SHELL_COMMAND = `echo TYPED-BY-A-PERSON-${nonce}`
const AGENT_COMMAND = `echo RAN-BY-THE-ASSISTANT-${nonce}`
const QUESTION = `Are you there, ${nonce}?`
const ANSWER = `Yes — THIS-ANSWER-MUST-SURVIVE-${nonce}`
const SECOND_QUESTION = `How much disk is free, ${nonce}?`

let backend: VaultBackend
let fake: FakeOpenAI
let endpoint: { port: number; token: string }

test.describe.configure({ mode: 'serial' })

test.beforeAll(async () => {
  fake = new FakeOpenAI()
  await fake.start()
  const root = mkdtempSync(join(tmpdir(), 'nocx-4em1z-e2e-'))
  // `true` = no Secret Service for this backend: the container has no
  // keychain to ask, and the derived content key makes the vault available
  // without user setup — the arrangement the other agent specs use.
  backend = new VaultBackend(devharnessBin(), { root }, true)
  endpoint = await backend.start()
})

test.afterAll(async () => {
  backend?.stop()
  await fake?.stop()
})

/** Every `agent.ask` this page sends, as it went over the socket. Installed
 *  BEFORE the navigation: a listener attached afterwards would miss the
 *  socket the app opens on load. */
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

/** One JSON-RPC call on a socket of the test's own, against the backend the
 *  page is bound to. The page's client is the product's and is not reachable
 *  from here; this asks the same server the same question. */
async function rpc(
  page: Page,
  ep: { port: number; token: string },
  method: string,
  params: unknown,
): Promise<Record<string, unknown>> {
  return page.evaluate(
    (a: { p: number; t: string; m: string; par: unknown }) =>
      new Promise<Record<string, unknown>>((resolve, reject) => {
        const ws = new WebSocket(`ws://127.0.0.1:${a.p}/session`, `nocx.token.${a.t}`)
        const timer = setTimeout(() => reject(new Error('rpc timeout')), 15_000)
        ws.onopen = () =>
          ws.send(JSON.stringify({ jsonrpc: '2.0', id: 1, method: a.m, params: a.par }))
        ws.onerror = () => {
          clearTimeout(timer)
          reject(new Error('rpc socket error'))
        }
        ws.onmessage = (ev) => {
          if (typeof ev.data !== 'string') return
          const msg = JSON.parse(ev.data) as Record<string, unknown>
          if (msg.id !== 1) return
          clearTimeout(timer)
          ws.close()
          resolve((msg.result ?? { error: msg.error }) as Record<string, unknown>)
        }
      }),
    { p: ep.port, t: ep.token, m: method, par: params },
  )
}

/**
 * Wait until the STORE holds this entry with the body a restore draws from.
 *
 * Nothing here is asserted: this is the precondition the restore is about
 * ("what was stored comes back"), and everything the test CLAIMS is read off
 * the screen afterwards. The media type is the parameter because it is also
 * the fact the restore reads the block's grammar from — a command has an
 * `application/vt` grid, a turn has a `text/plain` original and never a grid.
 */
async function storedWith(
  page: Page,
  ep: { port: number; token: string },
  intent: string,
  mediaType: string,
): Promise<void> {
  await expect
    .poll(
      async () => {
        const listed = (await rpc(page, ep, 'ledger.query', {
          scope: 'everywhere',
          limit: 200,
        })) as { entries?: { id: string; intent: string }[] }
        const row = (listed.entries ?? []).find((e) => e.intent === intent)
        if (!row) return false
        const detail = (await rpc(page, ep, 'ledger.get', { id: row.id })) as {
          artifacts?: { mediaType: string }[]
        }
        return (detail.artifacts ?? []).some((a) => a.mediaType === mediaType)
      },
      { timeout: 60_000, message: `the store never took "${intent}" as ${mediaType}` },
    )
    .toBe(true)
}

/** Wait until the STORE holds a turn and its PROSE text child carries a
 *  text/plain body (ADR-0040: the answer is a `text` child, and the child
 *  owns its own artifact). The command/agent halves still use `storedWith`
 *  — their bodies hang on the entry or an execution. */
async function storedProseChild(
  page: Page,
  ep: { port: number; token: string },
  intent: string,
): Promise<void> {
  await expect
    .poll(
      async () => {
        const listed = (await rpc(page, ep, 'ledger.query', {
          scope: 'everywhere',
          limit: 200,
        })) as { entries?: { id: string; intent: string }[] }
        const turn = (listed.entries ?? []).find((e) => e.intent === intent)
        if (!turn) return false
        const detail = (await rpc(page, ep, 'ledger.get', { id: turn.id })) as {
          caused?: { entryId: string; kind: string }[]
        }
        const prose = (detail.caused ?? []).find((c) => c.kind === 'text')
        if (!prose) return false
        const child = (await rpc(page, ep, 'ledger.get', { id: prose.entryId })) as {
          artifacts?: { mediaType: string }[]
        }
        return (child.artifacts ?? []).some((a) => a.mediaType === 'text/plain')
      },
      { timeout: 60_000, message: `the store never held "${intent}"'s prose child body` },
    )
    .toBe(true)
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

function blockFor(page: Page, text: string) {
  return page.locator('.pane.active .cmd-block').filter({ hasText: text })
}

async function finished(page: Page, text: string): Promise<void> {
  // The turn's OWN chip — `:scope > .cmd-header`, never a child command's
  // nested `ok` chip (both are `.cmd-header-exit`).
  await expect(blockFor(page, text).locator(':scope > .cmd-header .cmd-header-exit')).toHaveText(
    'completed',
    {
      timeout: 30_000,
    },
  )
}

test.describe('a restored pane knows what each block was (nocx-4em1z)', () => {
  test.use({ viewport: { width: 1280, height: 900 } })

  test('a command, an agent command and a dialogue all come back as themselves', async ({
    page,
  }) => {
    const asks = recordAskSessions(page)
    await bindEndpoint(page, endpoint)
    await page.goto('/')
    await expect(page.locator(TITLE).first()).not.toHaveText('', { timeout: 15_000 })

    // ── The assistant is configured, and `run` is allowed to run ──────────
    await openSettings(page, SETTINGS_AI_NAV)
    await expect(page.locator('.ep-root')).toBeVisible({ timeout: 10_000 })
    const endpointName = `E2E Restore ${nonce}`
    await createAiEndpoint(page, {
      name: endpointName,
      baseUrl: fake.baseUrl(),
      models: ['e2e-model'],
      key: `e2e-key-${nonce}`,
      vaultPassphrase: `vault-pass-${nonce}`,
    })
    await page.locator(SETTINGS_ROLES_NAV).click()
    await setDefaultModel(page, endpointName, 'e2e-model')

    await page.locator(SETTINGS_POLICY_NAV).click()
    const destructive = page.locator(DESTRUCTIVE_ROW)
    await expect(destructive).toBeVisible({ timeout: 15_000 })
    await destructive.locator('select').first().selectOption({ label: 'Allowed' })
    await expect(destructive.locator('.st-policy__state')).toContainText('Allowed', {
      timeout: 15_000,
    })
    await backToTerminal(page)

    // ── 1. A command a person typed ───────────────────────────────────────
    // First, while the editor is still sending to the shell: asking flips
    // the mode and it stays flipped.
    await promptReady(page)
    await clickIntoEditor(page)
    await page.keyboard.type(SHELL_COMMAND)
    await page.keyboard.press('Enter')
    await expect(blockFor(page, SHELL_COMMAND).first()).toBeVisible({ timeout: 30_000 })

    // ── 2. A dialogue ─────────────────────────────────────────────────────
    fake.setScript({ chunks: [ANSWER] })
    const before = asks.length
    await askFromPrompt(page, QUESTION)
    await finished(page, QUESTION)
    await expect(blockFor(page, QUESTION).locator('[data-answer-body]')).toContainText(ANSWER)

    // The session this pane is, spelled by the product itself — a scripted
    // `run` must name it exactly or the grant refuses the call (AD-7).
    await expect.poll(() => asks.length, { timeout: 15_000 }).toBeGreaterThan(before)
    const sessionId = asks[asks.length - 1]
    expect(sessionId).not.toBe('')

    // ── 3. A command the ASSISTANT ran ────────────────────────────────────
    // A real tool-calling run is two model responses: the proposal, then the
    // answer written from the tool's result.
    fake.setScript({
      chunks: ['Let me check.'],
      toolCalls: [{ name: 'run', arguments: { sessionId, command: AGENT_COMMAND } }],
    })
    fake.setScript({ chunks: ['Plenty.'] })
    await askFromPrompt(page, SECOND_QUESTION)
    await finished(page, SECOND_QUESTION)
    // Nobody was asked: the row is Allowed. Asserted after the terminal chip,
    // so the absence is a fact about the product and not a race won at t=0.
    await expect(page.getByRole('dialog', { name: APPROVAL_TITLE })).toHaveCount(0)

    const liveAgentBlock = blockFor(page, AGENT_COMMAND).first()
    await expect(liveAgentBlock).toBeVisible({ timeout: 30_000 })

    // ── The turn reads in the order it happened (ADR-0040) ─────────────
    // The owner's report was this sequence inverted: the answer finished
    // above the evidence it was distilled from. Now the turn is ONE block
    // whose OWN header is the question and whose children are the causal
    // sequence — the run's command block between the prose before it and
    // the prose written from it — with no fragment, no `continued` badge
    // and no repeated question.
    const liveTurn = blockFor(page, SECOND_QUESTION)
    await expect(liveTurn).toHaveCount(1)
    // The question is its own header, and appears exactly once (criterion 4).
    const liveHeaders = await liveTurn
      .locator(':scope > .cmd-header .cmd-header-text')
      .allTextContents()
    expect(liveHeaders.filter((h) => h === SECOND_QUESTION)).toHaveLength(1)
    // The children, in seat order: the prose run, the command block, the
    // prose written from it. The command block is INSIDE the turn.
    const liveChildren = liveTurn.locator(':scope > .cmd-children > .cmd-block')
    await expect(liveChildren).toHaveCount(3)
    await expect(liveChildren.nth(0)).toHaveAttribute('data-block-kind', 'text')
    await expect(liveChildren.nth(1)).toHaveAttribute('data-block-kind', 'command')
    await expect(liveChildren.nth(1).locator('.cmd-header-text')).toContainText(AGENT_COMMAND)
    await expect(liveChildren.nth(2)).toHaveAttribute('data-block-kind', 'text')
    await expect(liveChildren.nth(2).locator('[data-answer-body]')).toContainText('Plenty.')
    // A turn is one block: no fragment, no continuation badge anywhere.
    await expect(page.locator('.pane.active [data-turn-continuation]')).toHaveCount(0)
    await expect(page.locator('.pane.active [data-turn-fragment]')).toHaveCount(0)
    // Criterion 2: no surface restates the command. The `run` call left no
    // line at all — the block is the account of it — and this pane's other
    // turn made no calls, so nothing anywhere claims one.
    await expect(page.locator('.pane.active .ui-tool-call')).toHaveCount(0)
    // It says so BEFORE the restart too — otherwise a restore that painted
    // no badge anywhere would pass the assertion below by matching a live
    // surface that never had one either.
    await expect(liveAgentBlock.locator('.ui-badge[data-author="agent"]')).toBeVisible()

    // ── All three are in the store, with their bodies, before anything is
    //    killed. Without this the restart races the write.
    await storedWith(page, endpoint, SHELL_COMMAND, 'application/vt')
    await storedWith(page, endpoint, AGENT_COMMAND, 'application/vt')
    await storedProseChild(page, endpoint, QUESTION)

    // ── The application restarts ──────────────────────────────────────────
    // Nothing in the first process survives it, including the shell and the
    // session the dialogue used to be anchored to.
    const second = await backend.restart()
    await bindEndpoint(page, second)
    await page.reload()
    await expect(page.locator(RESTORED).first()).toBeVisible({ timeout: 90_000 })

    // ── What came back, one claim per kind ────────────────────────────────

    // A person's command: a command block, with the grid it printed, and NO
    // provenance mark — a human's block carries none, and a badge appearing
    // here would be the same defect pointing the other way.
    const shell = blockFor(page, SHELL_COMMAND).first()
    await expect(shell).toHaveAttribute('data-restored', 'true')
    await expect(shell).toHaveAttribute('data-block-kind', 'command')
    await expect(shell.locator('.cmd-output')).toContainText(`TYPED-BY-A-PERSON-${nonce}`)
    await expect(shell.locator('.ui-badge[data-author]')).toHaveCount(0)

    // The assistant's command: still a command block, still carrying its
    // output, and it still says who ran it. This is the badge half.
    // BY ITS OWN HEADER, not by containing the text (ADR-0040). A command
    // the assistant ran is a CHILD of its turn now, so the turn contains
    // that text too and `blockFor(...).first()` picks the turn — whose kind
    // is `ask`, which is what this used to fail on. A block whose own
    // header is the command is exactly one block, and it is this one.
    const agent = page.locator('.pane.active .cmd-block').filter({
      has: page.locator(':scope > .cmd-header .cmd-header-text', { hasText: AGENT_COMMAND }),
    })
    await expect(agent).toHaveAttribute('data-restored', 'true')
    await expect(agent).toHaveAttribute('data-block-kind', 'command')
    await expect(agent.locator('.cmd-output')).toContainText(`RAN-BY-THE-ASSISTANT-${nonce}`)
    await expect(agent.locator('.ui-badge[data-author="agent"]')).toBeVisible()

    // The dialogue: back at all — it used to be absent entirely — and back as
    // the turn it was. The question is the block's own line and the answer
    // is a `text` CHILD (ADR-0040: a turn carries the prose it wrote as a
    // child in seat order, and a restored prose child carries its text).
    // The kind is `ask` rather than `command`, which is what keeps the prose
    // wrapping instead of being held to a grid.
    const turn = blockFor(page, QUESTION).first()
    await expect(turn).toHaveAttribute('data-restored', 'true')
    await expect(turn).toHaveAttribute('data-block-kind', 'ask')
    await expect(
      turn.locator(':scope > .cmd-children > .cmd-block[data-block-kind="text"] .term-line'),
    ).toContainText(ANSWER)
    // And the sentence reserved for a body retention has evicted is NOT what
    // a turn whose answer is right there gets (ADR-0019 §7).
    await expect(turn).not.toHaveAttribute('data-output-evicted', 'true')

    // The turn that RAN something comes back arranged exactly as it was
    // drawn (ADR-0040 criterion 9): the relation and the prose anchors are
    // stored, so the restored view of one turn is the live one it came
    // from — ONE turn block, its question once, its children in the same
    // seats, and the restored prose child carrying its text.
    const restoredTurnBlock = blockFor(page, SECOND_QUESTION)
    await expect(restoredTurnBlock).toHaveCount(1)
    await expect(restoredTurnBlock).toHaveAttribute('data-restored', 'true')
    await expect(restoredTurnBlock).toHaveAttribute('data-block-kind', 'ask')

    const restoredHeaders = await restoredTurnBlock
      .locator(':scope > .cmd-header .cmd-header-text')
      .allTextContents()
    expect(restoredHeaders.filter((h) => h === SECOND_QUESTION)).toHaveLength(1)
    const restoredChildren = restoredTurnBlock.locator(':scope > .cmd-children > .cmd-block')
    await expect(restoredChildren).toHaveCount(3)
    await expect(restoredChildren.nth(0)).toHaveAttribute('data-block-kind', 'text')
    await expect(restoredChildren.nth(0).locator('.term-line')).toContainText('Let me check.')
    await expect(restoredChildren.nth(1)).toHaveAttribute('data-block-kind', 'command')
    await expect(restoredChildren.nth(1).locator('.cmd-header-text')).toContainText(AGENT_COMMAND)
    await expect(restoredChildren.nth(1).locator('.cmd-output')).toContainText(
      `RAN-BY-THE-ASSISTANT-${nonce}`,
    )
    await expect(restoredChildren.nth(2)).toHaveAttribute('data-block-kind', 'text')
    await expect(restoredChildren.nth(2).locator('.term-line')).toContainText('Plenty.')
    // No fragment, no continuation badge — yes, a turn is one block.
    await expect(page.locator('.pane.active [data-turn-continuation]')).toHaveCount(0)
    await expect(page.locator('.pane.active [data-turn-fragment]')).toHaveCount(0)
    await expect(page.locator('.pane.active .ui-tool-call')).toHaveCount(0)
  })
})

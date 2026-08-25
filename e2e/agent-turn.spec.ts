/**
 * e2e: one multi-step assistant turn reads as the causal sequence it was —
 * live, and again after a backend restart (ADR-0040, nocx-dc2fr).
 *
 * WHY THIS FILE, WHEN THE UNITS ARE GREEN. ADR-0040 turned `entries` into an
 * ordered tree: a turn CARRIES the blocks it caused as one `.cmd-children`
 * list, in the exact order the store recorded, and a `text` block is a run of
 * assistant prose born closed and successful. The unit tests assert the store,
 * the wire and the DOM separately. None watches a person read ONE finished
 * turn top to bottom and see the sequence as it happened — the sentence before
 * a command, the command with its real output, the sentence written from it, a
 * tool call that opens no block, the final sentence. That is AGENTS.md testing
 * rule 2's "watch a person do the thing", and it is the only check that could
 * have reported the fixed arrangement this ADR exists to kill (ADR-0040
 * §Context: every prior arrangement put all the prose below all the children,
 * so a turn read as though the command came after the conclusion it was
 * evidence for).
 *
 * THE TURN THIS DRIVES. Five children, in seat order:
 *
 *     text    — a sentence written before the command
 *     command — the `run` tool's command block, with its real header, output,
 *               exit status and agent badge (the block IS the account of
 *               the call)
 *     text    — a sentence written from that output
 *     tool    — session.read, a call that opens NO block: its own child
 *               naming the tool and the arguments it ran on
 *     text    — the final sentence
 *
 * THE SEAM IS THREE MODEL ROUNDS. The engine re-invokes the model after `run`
 * executes and again after `session.read` executes, consuming one fake script
 * per round — a real tool-calling run is exactly that interleave
 * (agent-answer-stream.spec.ts and agent-restore.spec.ts each drive one
 * round; this is the first spec to drive TWO in one turn).
 *
 * WHAT IS ASSERTED OFF THE SAME SEAM a person reads. The children's order and
 * text come off the DOM, and their VERTICAL POSITIONS off their rendered
 * bounding boxes — because a flex column can reorder visually (CSS `order`)
 * without reordering the DOM, and the specific regression this ADR kills (all
 * prose at the bottom) is a LAYOUT claim. The spec fails if a turn draws its
 * prose below all its children: the direct children must be the five
 * interleaved blocks in DOM order AND in strictly increasing `top`.
 *
 * THE RESTART HALF is why the backend is THIS FILE'S OWN: it is killed and
 * restarted mid-test, and what survives has to survive through the encrypted
 * store, not through a process that remembers writing it (agent-restore.spec
 * is the same shape). Before the restart the spec waits on the STORE holding
 * every child's body — the block appears at submit, its body is written
 * afterwards, and a restart that raced the write measures that race rather
 * than the restore.
 *
 * NOTHING HERE WAITS OUT A DURATION. Every wait is a poll on an observable
 * state change — the turn's `completed` chip, a store row — never a sleep.
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

const devharnessBin = () => readStand().devharness

const TITLE = '.nocx-tab-title'
const INPUT = '.pane.active .nocx-editor-input'
const SETTINGS_AI_NAV = '.ui-grouped-nav__item[data-item="endpoints"]'
const SETTINGS_ROLES_NAV = '.ui-grouped-nav__item[data-item="roles"]'
const SETTINGS_POLICY_NAV = '.ui-grouped-nav__item[data-item="policy"]'
/* `run` is mutate-destructive and session.read observe (registry.go). */
const DESTRUCTIVE_ROW = '.st-policy__row[data-effect="mutate-destructive"]'
const OBSERVE_ROW = '.st-policy__row[data-effect="observe"]'
const APPROVAL_TITLE = 'This action needs your approval'

const test = base
const nonce = Date.now().toString(36)

const ENDPOINT_NAME = `E2E Turn ${nonce}`
/** The command the run call executes — its own block's header. */
const RUN_CMD = `echo ran-${nonce}`
/** What the command prints — the marker the block's real output must show. */
const RUN_MARKER = `ran-${nonce}`
/** The window session.read is asked for — its arguments, so the tool child's
 *  header asserts more than a bare tool name. `start`/`count` is the tool's
 *  own vocabulary (contracts/tools/session.read.schema.json). */
const READ_WINDOW = { start: 0, count: 24 }
const PROSE_BEFORE = `Let me check the disk, ${nonce}.`
const PROSE_MIDDLE = `The command printed its marker.`
const PROSE_AFTER = `The screen reads clear.`
/** The turn under test. Its header is this question; every assertion is
 *  scoped to it, because the confirming turn below also lives in the pane. */
const QUESTION = `What is on the disk, ${nonce}?`
/** A one-turn dialogue asked FIRST purely to learn the session id the
 *  product's own mechanics spell (AD-7): the run/session.read scripts must name
 *  it exactly, and a made-up id is refused for identity before the call can
 *  run. Scripted before the real turn, so the id is a fact already when the
 *  real turn's tools are proposed. */
const CONFIRM = `Are you there, ${nonce}?`

let backend: VaultBackend
let fake: FakeOpenAI
let endpoint: { port: number; token: string }

test.describe.configure({ mode: 'serial' })

test.beforeAll(async () => {
  fake = new FakeOpenAI()
  await fake.start()
  const root = mkdtempSync(join(tmpdir(), 'nocx-dc2fr-e2e-'))
  // `true` = no Secret Service: the container has no keychain to ask, and
  // the derived content key makes the vault available without user setup.
  backend = new VaultBackend(devharnessBin(), { root }, true)
  endpoint = await backend.start()
})

test.afterAll(async () => {
  backend?.stop()
  await fake?.stop()
})

/** Every `agent.ask` this page sends, as it went over the socket — the
 *  session id the assistant's own lane used. */
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
 *  page is bound to — the same server the same question answers. */
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
 * Wait until the STORE holds the turn and every child body the restore draws
 * from — the precondition the restart is about, never an assertion.
 *
 * `seats` names the child kinds in order. A `text` child's body is its
 * text/plain artifact; the command's (kind `shell` — the run tool submits
 * through the same path a person's line takes, and `source` carries who ran
 * it) is application/vt; an
 * `action` has no artifact (its facts are in the entry's payload).
 */
async function storedTurn(
  page: Page,
  ep: { port: number; token: string },
  question: string,
  seats: { kind: string; intent?: string }[],
): Promise<void> {
  await expect
    .poll(
      async () => {
        const listed = (await rpc(page, ep, 'ledger.query', {
          scope: 'everywhere',
          limit: 200,
        })) as { entries?: { id: string; intent: string }[] }
        const row = (listed.entries ?? []).find((e) => e.intent === question)
        if (!row) return false
        const detail = (await rpc(page, ep, 'ledger.get', { id: row.id })) as {
          caused?: { entryId: string; kind: string; intent?: string }[]
        }
        const caused = detail.caused ?? []
        if (caused.length !== seats.length) return false
        for (let i = 0; i < caused.length; i++) {
          // The seat's KIND (and, for a call, its INTENT) came back exactly
          // as the turn stored it — the wire, under the engine's own record.
          if (caused[i].kind !== seats[i].kind) return false
          const spec = seats[i]
          if (spec.intent !== undefined && caused[i].intent !== spec.intent) return false
          if (spec.kind === 'action') continue // no artifact; facts live in payload
          const child = (await rpc(page, ep, 'ledger.get', {
            id: caused[i].entryId,
          })) as { artifacts?: { mediaType: string }[] }
          const mediaType = spec.kind === 'text' ? 'text/plain' : 'application/vt'
          if (!(child.artifacts ?? []).some((a) => a.mediaType === mediaType)) return false
        }
        return true
      },
      { timeout: 60_000, message: `the store never held ${question}'s children at all seats` },
    )
    .toBe(true)
}

/** Send the draft line to the ASSISTANT: ⌘/Ctrl+Enter flips where Enter
 *  goes, then Enter is the one send key. Idempotent on the flip. */
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

/** The one `.cmd-block` whose header IS the question. Scoped to the active
 *  pane's scrollback; the confirming turn is the only other block and it sits
 *  above, so `.filter({ hasText: question })` resolves to the turn itself. */
function turnBlock(page: Page, question: string) {
  return page.locator('.pane.active .cmd-block').filter({ hasText: question })
}

async function completed(page: Page, question: string): Promise<void> {
  // The turn's OWN chip — `:scope > .cmd-header`, never the run child's
  // `ok` chip nested inside the turn (both are `.cmd-header-exit`).
  await expect(
    turnBlock(page, question).locator(':scope > .cmd-header .cmd-header-exit'),
  ).toHaveText('completed', {
    timeout: 30_000,
  })
}

/** The turn's own children, in DOM order, as `kind|what a person reads`.
 *  Text from its body rows; a command and a tool from their headers. */
async function turnRows(page: Page, question: string): Promise<string[]> {
  const turn = turnBlock(page, question)
  return turn.locator(':scope > .cmd-children > .cmd-block').evaluateAll((els) =>
    els.map((el) => {
      const kind = (el as HTMLElement).dataset.blockKind ?? 'command'
      if (kind === 'text') {
        const rows = Array.from(el.querySelectorAll('.term-line')).map((r) => r.textContent ?? '')
        return `text:${rows.join('\n')}`
      }
      const header = el.querySelector('.cmd-header .cmd-header-text')?.textContent ?? ''
      return `${kind}:${header}`
    }),
  )
}

/** The rendered TOP of each direct child, in DOM order. A flex column MUST
 *  run in increasing top; assert it directly so a CSS reorder is red. */
async function childTops(page: Page, question: string): Promise<number[]> {
  const children = turnBlock(page, question).locator(':scope > .cmd-children > .cmd-block')
  const n = await children.count()
  const tops: number[] = []
  for (let i = 0; i < n; i++) {
    const box = await children.nth(i).boundingBox()
    if (!box) throw new Error(`turn child ${i} has no bounding box`)
    tops.push(box.y)
  }
  return tops
}

/** The assistant is usable end to end: endpoint, default model, and both
 *  rows Allowed so the proposed run and session.read execute rather than ask. */
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

test.describe('a multi-step turn reads in order, live and after a restart (nocx-dc2fr)', () => {
  test.use({ viewport: { width: 1280, height: 900 } })

  test('prose → run → prose → tool → prose, in the live flow and the restored page', async ({
    page,
  }) => {
    const asks = recordAskSessions(page)
    await openApp(page)
    await configureAssistant(page)

    // The confirming turn cannot need its tools because its script must be
    // set BEFORE it is asked; the real turn's tools are scripted after the
    // id is known.
    fake.setScript({ chunks: ['Yes, I am here.'] })
    await askFromPrompt(page, CONFIRM)
    await completed(page, CONFIRM)
    await expect.poll(() => asks.length, { timeout: 15_000 }).toBeGreaterThan(0)
    const sessionId = asks[asks.length - 1]
    expect(sessionId).not.toBe('')

    // ── THE turn: three model rounds. A run of prose and the TOOL CALL that
    //    follows it ride ONE response — the run continues only while a
    //    response ends in a tool proposal, so a content-only (stop) round
    //    between tools would terminate the run. Distinct call ids keep the
    //    two calls from colliding (each round's first call defaults to
    //    `call_1`, and the renderer dedupes by that id — a reused id drops
    //    the second call).
    fake.setScript({
      chunks: [PROSE_BEFORE],
      toolCalls: [{ name: 'run', id: 'call_run', arguments: { sessionId, command: RUN_CMD } }],
    })
    fake.setScript({
      chunks: [PROSE_MIDDLE],
      toolCalls: [
        { name: 'session.read', id: 'call_read', arguments: { sessionId, ...READ_WINDOW } },
      ],
    })
    fake.setScript({ chunks: [PROSE_AFTER] })
    await askFromPrompt(page, QUESTION)
    await completed(page, QUESTION)
    // executed straight through. Asserted after the terminal chip, so the
    // absence is a fact about the product and not a race won at t=0.
    await expect(page.getByRole('dialog', { name: APPROVAL_TITLE })).toHaveCount(0)

    // ── 1. Order and TEXT, as a person reads top to bottom ──────────────
    const liveRows = await turnRows(page, QUESTION)
    expect(liveRows[0]).toBe(`text:${PROSE_BEFORE}`)
    expect(liveRows[1]).toBe(`command:${RUN_CMD}`)
    expect(liveRows[2]).toBe(`text:${PROSE_MIDDLE}`)
    expect(liveRows[3]).toMatch(/^tool:session\.read/)
    expect(liveRows[4]).toBe(`text:${PROSE_AFTER}`)

    // ── 2. The `run` child is a REAL command block: its own header, its
    //       real output, its exit status (echo exits 0 → ‘ok’), its agent
    //       badge.
    const children = turnBlock(page, QUESTION).locator(':scope > .cmd-children > .cmd-block')
    const runBlock = children.nth(1)
    await expect(runBlock).toHaveAttribute('data-block-kind', 'command')
    await expect(runBlock.locator('.cmd-header-text')).toContainText(RUN_CMD)
    await expect(runBlock.locator('.cmd-output')).toContainText(RUN_MARKER)
    await expect(runBlock.locator('.cmd-header-exit')).toHaveText('ok')
    await expect(runBlock.locator('.ui-badge[data-author="agent"]')).toBeVisible()

    // ── 3. The two tool calls read differently — the run's block says what
    //       it ran; the read-only tool child says the tool and its argument.
    //       (They are different tools; ADR-0040's same-tool-two-args is the
    //       unit suite's acceptance 2.) The argument that makes THIS call
    //       distinguishable is the WINDOW it asked for, and its header must
    //       show it.
    const toolBlock = children.nth(3)
    await expect(toolBlock).toHaveAttribute('data-block-kind', 'tool')
    await expect(toolBlock).toHaveAttribute('data-tool', 'session.read')
    await expect(toolBlock.locator('.cmd-header-text')).toContainText('session.read')
    const toolHeader = await toolBlock.locator('.cmd-header-text').innerText()
    expect(toolHeader).toContain(`start=${READ_WINDOW.start}`)
    expect(toolHeader).toContain(`count=${READ_WINDOW.count}`)
    // And it is NOT a command block pretending: no output, no exit chip.
    await expect(toolBlock.locator('.cmd-output')).toHaveCount(0)

    // ── 4. The question appears exactly once — no continued badge. ─────
    // The turn's own header is the only render of the question. Under the
    // removed arrangement every continuation repeated it under a `continued`
    // badge.
    const headerCount = await turnBlock(page, QUESTION)
      .locator('.cmd-header-text')
      .filter({ hasText: QUESTION })
      .count()
    expect(headerCount).toBe(1)
    await expect(page.locator('.pane.active [data-turn-continuation]')).toHaveCount(0)
    await expect(page.locator('.pane.active [data-turn-fragment]')).toHaveCount(0)

    // ── 5a. POSITION: the children render in increasing top. This is the
    //        layout half the DOM order cannot see: a CSS `order` reverse, or
    //        the fixed arrangement that drew all prose below all children,
    //        is a red here before it is ever a text mismatch. The prose runs
    //        are INTERLEAVED — sentence-above-command and sentence-below —
    //        which a body dumped below all children would violate.
    const tops = await childTops(page, QUESTION)
    expect(tops).toHaveLength(5)
    for (let i = 1; i < tops.length; i++) {
      expect(tops[i]).toBeGreaterThan(tops[i - 1])
    }
    expect(tops[0]).toBeLessThan(tops[1])
    expect(tops[1]).toBeLessThan(tops[2])
    expect(tops[3]).toBeLessThan(tops[4])

    // ── THE SCREENSHOT the brief asks for: what the finished turn actually
    //    looks like. Stored in test-results/ (git-ignored, uploaded on red).
    const shotPath = join('test-results', `agent-turn-${nonce}.png`)
    await page.screenshot({ path: shotPath, fullPage: true })
    console.log(`E2E multi-step turn screenshot: ${shotPath}`)

    // ── PRE-RACE: the STORE holds the turn's children with their bodies
    //    before anything is killed. The block appears at submit, its body is
    //    written afterwards; a restart that raced the write measures that
    //    race rather than the restore.
    await storedTurn(page, endpoint, QUESTION, [
      { kind: 'text' },
      { kind: 'action', intent: 'run' },
      { kind: 'shell' },
      { kind: 'text' },
      { kind: 'action', intent: 'session.read' },
      { kind: 'text' },
    ])

    // ── THE APPLICATION RESTARTS. The shell and the session die with it;
    //    only the encrypted store keeps the turn.
    const second = await backend.restart()
    await bindEndpoint(page, second)
    await page.reload()
    await expect(page.locator('.pane.active .cmd-block[data-restored="true"]').first()).toBeVisible(
      { timeout: 90_000 },
    )

    // ── 5b. The same order and the same text, on the restored page.
    //      The TOOL child is normalized: its live header names the session by
    //      the pane's display title, and after a restart that session is
    //      gone (D5), so the renderer drops the unnameable id — the call's
    //      OTHER arguments, the window, are what survive, and the row must
    //      still read as the tool at its seat. Every other child must match
    //      byte-for-byte.
    const restoredRows = await turnRows(page, QUESTION)
    expect(restoredRows[0]).toBe(liveRows[0])
    expect(restoredRows[1]).toBe(liveRows[1])
    expect(restoredRows[2]).toBe(liveRows[2])
    expect(restoredRows[3]).toMatch(/^tool:session\.read/)
    expect(restoredRows[3]).toContain(`count=${READ_WINDOW.count}`)
    expect(restoredRows[4]).toBe(liveRows[4])
    const restoredTops = await childTops(page, QUESTION)
    expect(restoredTops).toHaveLength(5)
    for (let i = 1; i < restoredTops.length; i++) {
      expect(restoredTops[i]).toBeGreaterThan(restoredTops[i - 1])
    }
    expect(restoredTops[0]).toBeLessThan(restoredTops[1])
    expect(restoredTops[1]).toBeLessThan(restoredTops[2])
    expect(restoredTops[3]).toBeLessThan(restoredTops[4])
    // Question once, still no voice badge.
    const restoredHeaderCount = await turnBlock(page, QUESTION)
      .locator('.cmd-header-text')
      .filter({ hasText: QUESTION })
      .count()
    expect(restoredHeaderCount).toBe(1)
    await expect(page.locator('.pane.active [data-turn-continuation]')).toHaveCount(0)
    await expect(page.locator('.pane.active [data-turn-fragment]')).toHaveCount(0)
    // The restored turn still reads as one turn WITH its children: it is
    // restored, a turn, and carries the five children.
    const restoredTurn = turnBlock(page, QUESTION)
    await expect(restoredTurn).toHaveAttribute('data-restored', 'true')
    await expect(restoredTurn).toHaveAttribute('data-block-kind', 'ask')
    await expect(restoredTurn.locator(':scope > .cmd-children > .cmd-block')).toHaveCount(5)
  })
})

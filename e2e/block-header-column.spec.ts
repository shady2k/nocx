/**
 * A COMMAND'S HEADER AND ITS OUTPUT START ON ONE COLUMN (nocx-4y82c.1.13).
 *
 * The owner, on nightly v0.8.1-nightly.202611091128 (main 313866933, after
 * PR #752): a running local command showed its output on the transcript's left
 * gutter while the running header's chevron and command started about one cell
 * further right. "The output now looks right; the command is what moved."
 *
 * WHICH EDGE IS THE RULE, decided from the frozen card and ADR-92zzg2y2: the
 * column's left edge is `--pane-inline-padding` from the pane, and the frozen
 * card puts three things on it — the header's chevron, the body's first output
 * column, and (at the prompt) the composer's first glyph. The command TEXT
 * begins one chevron-and-gap after that edge, in the frozen card and in the
 * running one alike; it is not itself the column. ADR-92zzg2y2 decision 6 says
 * the header, its output and the composer's text are one column, and decision 1
 * says the running body and the frozen body both wear `--pane-inline-padding`.
 * So the rule asserted here is: chevron == output's first column == the gutter,
 * for a running block and for the frozen card, and the header keeps that x when
 * the command ends.
 *
 * WHY THE RUNNING HEADER WAS ONE GUTTER RIGHT: PR #752 stopped the running
 * block from cancelling the stack's row gutter (`nocx-9bpeq.8` — every row of
 * `.scrollback-inner` wears `--pane-inline-padding`), which is what fixed the
 * output. The running header had kept an inline padding of its own from
 * ADR-3kqrem7v, when the running block paid no gutter at all, so it paid the
 * gutter twice: 48px against the output's 24px. Nothing in the frozen card
 * pays it twice.
 *
 * THE SECOND SEAT IS A COMMAND NESTED IN A TURN (PR #781 review, P2). An
 * assistant's `run` call opens an ordinary command block inside the turn's
 * `.cmd-children`, and while it runs the controller widens that block to the
 * pane (`_sizeRunningSeat`, ADR-3kqrem7v: placement only, never the desired
 * PTY size) so the live grid is drawn at the pane's column width. The widening
 * cancels the turn's insets, so that seat pays the transcript's gutter itself:
 * its content box is the column the grid is fitted into — the same box a
 * top-level running command has — and its header's chevron is on the same edge
 * as its own output. Its frozen card keeps the turn's indent (the turn indents
 * its children, ADR-3kqrem7v's accepted consequence), and the header is on its
 * output's edge there too.
 *
 * The scenario is a command that prints two marked rows and then waits, so the
 * same rows are measurable while the block runs (in the live region) and after
 * it freezes (in the block's own body). Ctrl+C is the end of the command, and
 * every wait is on a row the command printed — never on a duration.
 */
import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'
import type { Page } from '@playwright/test'
import {
  test,
  standalone,
  expect,
  promptReady,
  clickIntoEditor,
  resolveBackend,
  answerPermission,
  appReadyForInput,
  bindEndpoint,
  createAiEndpoint,
  openControlPlane,
  readVaultState,
  setDefaultModel,
  settingsReady,
  VaultBackend,
} from './harness'
import { startSshd, rpc } from './sshd-fixture'
import { readStand } from './stand'
import { FakeOpenAI } from './fake-openai'

/** The two rows the measurement is taken from, and a wait long enough to look
 *  at them before Ctrl+C ends the command. */
const COMMAND = "printf 'COL-A\\nCOL-B\\n'; sleep 300"
const FIRST_ROW = 'COL-A'

/** The ledger's inset, as the stylesheet names it; the column's own left edge. */
const GUTTER_TOKEN = '--pane-inline-padding'
/** Sub-pixel layout: a fractional x is still one column. */
const TOLERANCE = 1

interface Columns {
  /** The header's chevron box: the first glyph of the header's row. */
  chevron: number
  /** The command text, one chevron and one gap after the chevron. */
  command: number
  /** The first output column of the row the command printed. */
  output: number
  /** The pane's own left edge, for the rule's absolute statement. */
  paneLeft: number
  /** `--pane-inline-padding` as the stylesheet computes it here. */
  gutter: number
  /** The row the output column was read from, so a failure names it. */
  rowText: string
}

/**
 * Read one block's three columns. `running` selects the running block, the
 * other reads the frozen card that replaced it; both are read in the SAME
 * viewport, so a difference is the product's and not the pane's.
 */
async function columns(page: Page, running: boolean): Promise<Columns | null> {
  return page.evaluate(
    ({ runningNow, firstRow, gutterToken }) => {
      const pane = document.querySelector<HTMLElement>('.pane.active')
      if (!pane) return null
      const block = runningNow
        ? pane.querySelector<HTMLElement>('.cmd-block.cmd-block-running')
        : ([...pane.querySelectorAll<HTMLElement>('.scrollback-inner > .cmd-block')].find(
            (candidate) => !candidate.classList.contains('cmd-block-running'),
          ) ?? null)
      if (!block) return null
      const header = block.querySelector<HTMLElement>(':scope > .cmd-header')
      const chevron = header?.querySelector<HTMLElement>('.cmd-header-sigil') ?? null
      const text = header?.querySelector<HTMLElement>('.cmd-header-text') ?? null
      const rows = [...block.querySelectorAll<HTMLElement>('.term-grid-row')]
      const row =
        rows.find((candidate) => (candidate.textContent ?? '').trim() === firstRow) ?? null
      if (!chevron || !text || !row) return null
      const r2 = (value: number): number => Math.round(value * 100) / 100
      const gutter = parseFloat(
        getComputedStyle(pane.querySelector<HTMLElement>('.scrollback-area')!).getPropertyValue(
          gutterToken,
        ),
      )
      return {
        chevron: r2(chevron.getBoundingClientRect().left),
        command: r2(text.getBoundingClientRect().left),
        output: r2(row.getBoundingClientRect().left),
        paneLeft: r2(pane.getBoundingClientRect().left),
        gutter: r2(gutter),
        rowText: (row.textContent ?? '').trim(),
      }
    },
    { runningNow: running, firstRow: FIRST_ROW, gutterToken: GUTTER_TOKEN },
  )
}

/** Wait until the running block has painted the command's first row. */
async function runningReady(page: Page): Promise<void> {
  await expect
    .poll(async () => (await columns(page, true))?.rowText ?? '', { timeout: 30_000 })
    .toBe(FIRST_ROW)
}

/** Wait until the frozen card has painted the same row. */
async function frozenReady(page: Page): Promise<void> {
  await expect
    .poll(async () => (await columns(page, false))?.rowText ?? '', { timeout: 30_000 })
    .toBe(FIRST_ROW)
}

/**
 * The whole claim, for one session: the running header sits on the column, the
 * frozen card sits on it, and the header does not move when the command ends.
 */
async function expectOneColumn(page: Page, where: string): Promise<void> {
  await clickIntoEditor(page)
  await page.keyboard.type(COMMAND)
  await page.keyboard.press('Enter')
  await runningReady(page)
  const running = await columns(page, true)
  expect(
    running,
    `${where}: the running block has a header and the command's first row`,
  ).not.toBeNull()

  // THE COMMAND IS OVER: the block freezes with the rows it printed.
  await page.keyboard.press('Control+C')
  await promptReady(page)
  await frozenReady(page)
  const frozen = await columns(page, false)
  expect(frozen, `${where}: the frozen card has the same header and row`).not.toBeNull()

  const said = `${where}: running ${JSON.stringify(running)}, frozen ${JSON.stringify(frozen)}`

  // THE RULE, FROM THE FROZEN CARD: the chevron and the output's first column
  // sit on the transcript's gutter, and they sit on the SAME edge.
  expect(
    Math.abs(frozen!.chevron - (frozen!.paneLeft + frozen!.gutter)),
    `${said} -- the frozen chevron is on the gutter`,
  ).toBeLessThanOrEqual(TOLERANCE)
  expect(
    Math.abs(frozen!.chevron - frozen!.output),
    `${said} -- the frozen card puts its chevron and its output on one column`,
  ).toBeLessThanOrEqual(TOLERANCE)
  // The command text follows the chevron, in the frozen card and in the
  // running one: the column is the chevron's edge, not the text's.
  const frozenAdvance = frozen!.command - frozen!.chevron
  expect(frozenAdvance, `${said} -- the frozen command text follows its chevron`).toBeGreaterThan(0)

  // THE RUNNING BLOCK IS THE SAME COLUMN, and its command text begins at the
  // same offset from it.
  expect(
    Math.abs(running!.chevron - running!.output),
    `${said} -- a running block's chevron and its output are on one column`,
  ).toBeLessThanOrEqual(TOLERANCE)
  expect(
    Math.abs(running!.chevron - (running!.paneLeft + running!.gutter)),
    `${said} -- the running chevron is on the gutter`,
  ).toBeLessThanOrEqual(TOLERANCE)
  expect(
    Math.abs(running!.command - running!.chevron - frozenAdvance),
    `${said} -- the running command text follows its chevron exactly as the frozen one does`,
  ).toBeLessThanOrEqual(TOLERANCE)

  // NOTHING MOVES WHEN THE COMMAND ENDS: the same header, on the same x, over
  // the same first column.
  expect(
    Math.abs(running!.chevron - frozen!.chevron),
    `${said} -- the header does not move when the block freezes`,
  ).toBeLessThanOrEqual(TOLERANCE)
  expect(
    Math.abs(running!.command - frozen!.command),
    `${said} -- the command text does not move when the block freezes`,
  ).toBeLessThanOrEqual(TOLERANCE)
  expect(
    Math.abs(running!.output - frozen!.output),
    `${said} -- the output's first column does not move when the block freezes`,
  ).toBeLessThanOrEqual(TOLERANCE)
}

test('a running local command’s header is on the column its output is on', async ({ page }) => {
  test.setTimeout(120_000)
  await page.setViewportSize({ width: 1280, height: 800 })
  await page.goto('/')
  await promptReady(page)
  await expectOneColumn(page, 'local')
})

test('a running ssh command’s header is on the column its output is on', async ({ page }) => {
  test.setTimeout(240_000)
  const remoteHome = mkdtempSync(path.join(tmpdir(), 'header-column-remote-'))
  const remoteCwd = mkdtempSync(path.join(tmpdir(), 'header-column-cwd-'))
  const fixture = await startSshd({ home: remoteHome, cwd: remoteCwd })
  let createdId: string | null = null
  try {
    // The far host's key, trusted in the home the BACKEND runs under.
    const sshDir = path.join(readStand().home, '.ssh')
    mkdirSync(sshDir, { recursive: true, mode: 0o700 })
    writeFileSync(path.join(sshDir, 'known_hosts'), `${fixture.knownHosts}\n`)
    await page.setViewportSize({ width: 1280, height: 800 })
    await page.goto('/')
    await promptReady(page)
    const info = await resolveBackend(page)
    const profileName = `header-column-${Date.now()}`
    const created = await rpc<{ id: string }>(page, info, 'profiles.create', {
      type: 'ssh',
      name: profileName,
      options: {
        host: fixture.host,
        port: fixture.port,
        user: fixture.user,
        keyPath: fixture.userKey,
        // Shell integration, so the remote command is a block like a local
        // one: the report is about a command's header, and a markerless shell
        // has no block at all.
        desiredMode: 'script',
      },
    })
    createdId = created.id
    await page.keyboard.press('Control+Shift+P')
    const search = page.locator('.quick-connect__search input')
    await expect(search).toBeVisible({ timeout: 30_000 })
    await search.fill(profileName)
    await expect(page.locator('.quick-connect__item', { hasText: profileName })).toBeVisible({
      timeout: 30_000,
    })
    await page.keyboard.press('Enter')
    // The composer is the product's statement that the remote shell reached
    // an authenticated, integrated prompt.
    await expect(page.locator('.pane.active .nocx-editor-input')).toBeVisible({ timeout: 180_000 })
    await expectOneColumn(page, 'ssh')
  } finally {
    try {
      const info = await resolveBackend(page)
      if (createdId !== null) await rpc(page, info, 'profiles.delete', { id: createdId })
    } catch {
      // A cleanup that throws would replace the real failure with its own.
    }
    fixture.proc.kill('SIGKILL')
  }
})

// ── The same column for a command nested in a turn ────────────────────────

const agentTest = standalone
const AGENT_TITLE = '.nocx-tab-title'
const AGENT_INPUT = '.pane.active .nocx-editor-input'
const SETTINGS_AI_NAV = '.ui-grouped-nav__item[data-item="endpoints"]'
const SETTINGS_ROLES_NAV = '.ui-grouped-nav__item[data-item="roles"]'
const SETTINGS_POLICY_NAV = '.ui-grouped-nav__item[data-item="policy"]'

const agentNonce = Date.now().toString(36)
/** One per run of the nested test, so a repeat is a fresh run. */
let agentRun = 0
const AGENT_ENDPOINT = `E2E Header Column ${agentNonce}`
/** A one-turn dialogue asked first only to let the product's own mechanics
 *  settle the session the run call executes in (ADR-7). */
const AGENT_CONFIRM = `Are you there, ${agentNonce}?`
/** The turn under test; its header is this question. */
const AGENT_QUESTION = `What is on the disk, ${agentNonce}?`
/** The `run` tool's command: the reviewer's own case, plus a marked row that
 *  is unambiguous in both presentations. */
const NESTED_COMMAND = "df -h; printf 'NEST-A\\n'; sleep 300"
const NESTED_ROW = 'NEST-A'
/** Every effect `session.run` can reach, answered Allowed so the call executes
 *  rather than raising the approval sheet. `privilege-change` and `disclose`
 *  are not on the page's list. */
const ALLOWED_EFFECTS = [
  'observe',
  'mutate-reversible',
  'mutate-destructive',
  'cross-boundary',
  'delegate',
] as const

interface NestedColumns {
  chevron: number
  command: number
  output: number
  /** The header's CONTENT box: where the chevron and the command begin. */
  headerContentLeft: number
  headerContentRight: number
  /** The block's own content box — the frozen card's column. */
  blockContentLeft: number
  /** The live region's BOX and its CONTENT box. The box still spans the pane
   *  (ADR-3kqrem7v's placement); its content is the column the grid is fitted
   *  into. Null on the frozen card, which has no live region. */
  liveBoxLeft: number | null
  liveBoxWidth: number | null
  liveContentLeft: number | null
  liveContentRight: number | null
  paneLeft: number
  paneRight: number
  areaWidth: number
  gutter: number
  rowText: string
}

/**
 * Read the running (or frozen) `run` child of the turn: the same three columns
 * as `columns`, plus the content box the header and the grid share.
 */
async function nestedColumns(
  page: Page,
  running: boolean,
  question: string,
): Promise<NestedColumns | null> {
  return page.evaluate(
    ({ runningNow, question, row, gutterToken }) => {
      const pane = document.querySelector<HTMLElement>('.pane.active')
      if (!pane) return null
      const turn = [...pane.querySelectorAll<HTMLElement>('.cmd-block')].find(
        (candidate) =>
          (candidate.querySelector(':scope > .cmd-header .cmd-header-text')?.textContent ?? '') ===
          question,
      )
      const block =
        turn?.querySelector<HTMLElement>(
          ':scope > .cmd-children > .cmd-block[data-block-kind="command"]',
        ) ?? null
      if (!block) return null
      if (block.classList.contains('cmd-block-running') !== runningNow) return null
      const header = block.querySelector<HTMLElement>(':scope > .cmd-header')
      const chevron = header?.querySelector<HTMLElement>('.cmd-header-sigil') ?? null
      const text = header?.querySelector<HTMLElement>('.cmd-header-text') ?? null
      const painted =
        [...block.querySelectorAll<HTMLElement>('.term-grid-row')].find(
          (candidate) => (candidate.textContent ?? '').trim() === row,
        ) ?? null
      if (!chevron || !text || !painted || !header) return null
      const r2 = (value: number): number => Math.round(value * 100) / 100
      const box = block.getBoundingClientRect()
      const headerBox = header.getBoundingClientRect()
      const headerStyle = getComputedStyle(header)
      const paneBox = pane.getBoundingClientRect()
      const area = pane.querySelector<HTMLElement>('.scrollback-area')!
      const live = block.querySelector<HTMLElement>(':scope > .terminal-live-container')
      const liveBox = live ? live.getBoundingClientRect() : null
      const liveStyle = live ? getComputedStyle(live) : null
      const gutter = parseFloat(getComputedStyle(area).getPropertyValue(gutterToken))
      return {
        chevron: r2(chevron.getBoundingClientRect().left),
        command: r2(text.getBoundingClientRect().left),
        output: r2(painted.getBoundingClientRect().left),
        headerContentLeft: r2(headerBox.left + parseFloat(headerStyle.paddingLeft)),
        headerContentRight: r2(headerBox.right - parseFloat(headerStyle.paddingRight)),
        blockContentLeft: r2(box.left + parseFloat(getComputedStyle(block).paddingLeft)),
        liveBoxLeft: liveBox ? r2(liveBox.left) : null,
        liveBoxWidth: liveBox ? r2(liveBox.width) : null,
        liveContentLeft:
          liveBox && liveStyle ? r2(liveBox.left + parseFloat(liveStyle.paddingLeft)) : null,
        liveContentRight:
          liveBox && liveStyle ? r2(liveBox.right - parseFloat(liveStyle.paddingRight)) : null,
        paneLeft: r2(paneBox.left),
        paneRight: r2(paneBox.right),
        areaWidth: area.clientWidth,
        gutter: r2(gutter),
        rowText: (painted.textContent ?? '').trim(),
      }
    },
    { runningNow: running, question, row: NESTED_ROW, gutterToken: GUTTER_TOKEN },
  )
}

/** Every `agent.ask` this page sends, as it went over the socket. */
function recordAskSessions(page: Page): string[] {
  const ids: string[] = []
  page.on('websocket', (ws) => {
    ws.on('framesent', (event) => {
      const payload = event.payload
      if (typeof payload !== 'string' || !payload.includes('"method":"agent.ask"')) return
      const message = JSON.parse(payload) as { params?: { sessionId?: string } }
      if (message.params?.sessionId) ids.push(message.params.sessionId)
    })
  })
  return ids
}

/** Send the draft line to the ASSISTANT: Ctrl/Cmd+Enter flips where Enter goes,
 *  then Enter is the one send key. Idempotent on the flip. */
async function askFromPrompt(page: Page, question: string): Promise<void> {
  const input = page.locator(AGENT_INPUT)
  await input.click()
  const indicator = page.locator('.pane.active .ui-mode-indicator:visible')
  if ((await indicator.getAttribute('data-target')) !== 'agent') {
    await page.keyboard.press('ControlOrMeta+Enter')
    await expect(indicator).toHaveAttribute('data-target', 'agent', { timeout: 10_000 })
  }
  await input.fill(question)
  await page.keyboard.press('Enter')
}

/** The turn's own block, the one whose header IS the question. */
function turnBlock(page: Page, question: string) {
  return page.locator('.pane.active .cmd-block').filter({ hasText: question })
}

async function completed(page: Page, question: string): Promise<void> {
  await expect(turnBlock(page, question)).toHaveAttribute('data-outcome', 'success', {
    timeout: 30_000,
  })
}

async function openAgentSettings(page: Page, navSelector: string): Promise<void> {
  await page.keyboard.press('Meta+,')
  await settingsReady(page)
  await page.locator(navSelector).click()
}

async function configureAssistant(page: Page, baseUrl: string, name: string): Promise<void> {
  await openAgentSettings(page, SETTINGS_AI_NAV)
  await expect(page.locator('.ep-root')).toBeVisible({ timeout: 10_000 })
  await createAiEndpoint(page, {
    name,
    baseUrl,
    models: ['e2e-model'],
    key: `e2e-key-${agentNonce}`,
    vaultPassphrase: `vault-pass-${agentNonce}`,
  })
  await page.locator(SETTINGS_ROLES_NAV).click()
  await setDefaultModel(page, name, 'e2e-model')
  await page.locator(SETTINGS_POLICY_NAV).click()
  for (const effect of ALLOWED_EFFECTS) {
    await answerPermission(page, effect, 'Allowed')
  }
  await page.locator(AGENT_TITLE).first().click()
  await expect(page.locator(AGENT_INPUT)).toBeVisible({ timeout: 10_000 })
}

agentTest.describe('a running command nested in a turn', () => {
  let backend: VaultBackend
  let fake: FakeOpenAI
  let endpoint: { port: number; token: string }

  /**
   * THE VAULT SEALS WHEN THE LAST CLIENT LEAVES (design D9), so a repeat of this
   * test finds it sealed and the endpoint save would wait on an unlock sheet
   * `createAiEndpoint` does not answer. Unsealing through the control plane is
   * the same door agent-turn.spec.ts uses for its second phase.
   */
  async function unsealIfSealed(page: Page): Promise<void> {
    if ((await readVaultState(endpoint)) !== 'sealed') return
    const wire = await openControlPlane(endpoint.port, endpoint.token)
    try {
      await wire.call('vault.unseal', { means: 'passphrase', secret: `vault-pass-${agentNonce}` })
    } finally {
      wire.close()
    }
    await expect(page.getByRole('dialog', { name: 'Unlock the vault' })).toHaveCount(0)
  }

  agentTest.beforeAll(async () => {
    fake = new FakeOpenAI()
    await fake.start()
    const root = mkdtempSync(path.join(tmpdir(), 'nocx-header-column-turn-'))
    // `true` = no Secret Service: the container has no keychain to ask, and the
    // derived content key makes the vault available without user setup.
    backend = new VaultBackend(readStand().server, { root })
    endpoint = await backend.start()
  })

  agentTest.afterAll(async () => {
    backend?.stop()
    await fake?.stop()
  })

  agentTest('puts its header on the column its own output is on', async ({ page }) => {
    agentTest.setTimeout(240_000)
    // A REPEAT IS A FRESH RUN against the same backend: the endpoint's name and
    // the turn's words are the run's own, so nothing this file left behind can
    // be mistaken for it, and the vault it sealed on its way out is unsealed.
    const run = ++agentRun
    const endpointName = `${AGENT_ENDPOINT} ${run}`
    const confirm = `${AGENT_CONFIRM} ${run}`
    const question = `${AGENT_QUESTION} ${run}`
    const asks = recordAskSessions(page)
    await page.setViewportSize({ width: 1280, height: 800 })
    await bindEndpoint(page, endpoint)
    await page.goto('/')
    await expect(page.locator(AGENT_TITLE).first()).not.toHaveText('', { timeout: 15_000 })
    await appReadyForInput(page)
    await unsealIfSealed(page)
    await configureAssistant(page, fake.baseUrl(), endpointName)

    // The confirming turn settles the session; the real turn's run call then
    // executes in it, exactly as agent-turn.spec.ts drives it.
    fake.setScript({ chunks: ['Yes, I am here.'] })
    await askFromPrompt(page, confirm)
    await completed(page, confirm)
    await expect.poll(() => asks.length, { timeout: 15_000 }).toBeGreaterThan(0)

    fake.setScript({
      chunks: [],
      toolCalls: [{ name: 'session.run', id: 'call_run', arguments: { command: NESTED_COMMAND } }],
    })
    fake.setScript({ chunks: ['The disk reads fine.'] })
    await askFromPrompt(page, question)

    // THE RUNNING SEAT: the run child is the running block, and its command has
    // printed the marked row into the live region.
    await expect
      .poll(async () => (await nestedColumns(page, true, question))?.rowText ?? '', {
        timeout: 60_000,
      })
      .toBe(NESTED_ROW)
    const running = await nestedColumns(page, true, question)
    expect(running, 'the turn has a running command block').not.toBeNull()

    // THE COMMAND IS OVER: the block freezes with the rows it printed.
    await page.keyboard.press('Control+C')
    await expect
      .poll(async () => (await nestedColumns(page, false, question))?.rowText ?? '', {
        timeout: 60_000,
      })
      .toBe(NESTED_ROW)
    const frozen = await nestedColumns(page, false, question)
    expect(frozen, 'the turn has a frozen command block with the same row').not.toBeNull()

    const said = `nested: running ${JSON.stringify(running)}, frozen ${JSON.stringify(frozen)}`

    // THE RUNNING SEAT'S CONTENT PAYS THE GUTTER. The controller widens the
    // block to the pane so the active rectangle spans it (ADR-3kqrem7v's
    // placement); that widening cancels the turn's insets, so the content of
    // each part pays the gutter itself, and that content is exactly the column
    // the grid is fitted into.
    expect(
      Math.abs(running!.headerContentLeft - (running!.paneLeft + running!.gutter)),
      `${said} -- the running header's content starts on the transcript's gutter`,
    ).toBeLessThanOrEqual(TOLERANCE)
    expect(
      running!.paneRight - running!.headerContentRight,
      `${said} -- and it stops short of the pane's right edge`,
    ).toBeGreaterThanOrEqual(running!.gutter - TOLERANCE)
    expect(
      Math.abs(running!.chevron - running!.headerContentLeft),
      `${said} -- the chevron is the first thing in the header's content`,
    ).toBeLessThanOrEqual(TOLERANCE)
    expect(
      Math.abs(running!.chevron - running!.output),
      `${said} -- the running chevron and its own output are one column`,
    ).toBeLessThanOrEqual(TOLERANCE)
    // The active rectangle still spans the pane, and the grid is drawn inside
    // its content — the column, not the pane's edge.
    expect(
      running!.liveBoxLeft,
      `${said} -- the live region's box starts at the pane`,
    ).not.toBeNull()
    expect(
      Math.abs(running!.liveBoxLeft! - running!.paneLeft),
      `${said} -- the live region's box still spans the pane`,
    ).toBeLessThanOrEqual(TOLERANCE)
    expect(
      Math.abs(running!.liveBoxWidth! - running!.areaWidth),
      `${said} -- and its width is the scroller's`,
    ).toBeLessThanOrEqual(TOLERANCE)
    expect(
      Math.abs(running!.liveContentLeft! - running!.chevron),
      `${said} -- the grid's column is the header's column`,
    ).toBeLessThanOrEqual(TOLERANCE)
    expect(
      running!.paneRight - running!.liveContentRight!,
      `${said} -- and the grid stops short of the pane's right edge`,
    ).toBeGreaterThanOrEqual(running!.gutter - TOLERANCE)

    // THE FROZEN CARD KEEPS THE TURN'S INDENT — the turn indents its children —
    // and is one column there too, with both edges clear of the pane.
    expect(
      frozen!.blockContentLeft - frozen!.paneLeft,
      `${said} -- the frozen card keeps an inset from the pane's left edge`,
    ).toBeGreaterThanOrEqual(frozen!.gutter - TOLERANCE)
    expect(
      frozen!.paneRight - frozen!.headerContentRight,
      `${said} -- and stops short of the pane's right edge`,
    ).toBeGreaterThanOrEqual(frozen!.gutter - TOLERANCE)
    expect(
      Math.abs(frozen!.chevron - frozen!.output),
      `${said} -- the frozen chevron and its own output are one column`,
    ).toBeLessThanOrEqual(TOLERANCE)
    expect(
      Math.abs(frozen!.chevron - frozen!.blockContentLeft),
      `${said} -- the frozen chevron is the first thing in the card's column`,
    ).toBeLessThanOrEqual(TOLERANCE)

    // The command text follows the chevron by the same advance in both seats.
    const runningAdvance = running!.command - running!.chevron
    const frozenAdvance = frozen!.command - frozen!.chevron
    expect(frozenAdvance, `${said} -- the frozen command text follows its chevron`).toBeGreaterThan(
      0,
    )
    expect(
      Math.abs(runningAdvance - frozenAdvance),
      `${said} -- the running command text follows its chevron the same way`,
    ).toBeLessThanOrEqual(TOLERANCE)
  })
})

/**
 * A COMMAND'S HEADER AND ITS OUTPUT START ON ONE COLUMN (nocx-4y82c.1.13).
 *
 * The owner, on nightly v0.8.1-nightly.202611091128 (main 313866933, after
 * PR #752): a running local command showed its output on the transcript's left
 * gutter while the running header's chevron and command started about 24 CSS px
 * further right. "The output now looks right; the command is what moved."
 *
 * WHICH EDGE IS THE RULE, decided from the frozen card and ADR-2yzsq8mk: the
 * column's left edge is `--pane-inline-padding` from the pane, and the frozen
 * card puts three things on it — the header's chevron, the body's first output
 * column, and (at the prompt) the composer's first glyph. The command TEXT
 * begins one chevron-and-gap after that edge, in the frozen card and in the
 * running one alike; it is not itself the column. ADR-2yzsq8mk decision 6 says
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
 * The scenario is a command that prints two marked rows and then waits, so the
 * same rows are measurable while the block runs (in the live region) and after
 * it freezes (in the block's own body). Ctrl+C is the end of the command, and
 * every wait is on a row the command printed — never on a duration.
 */
import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'
import type { Page } from '@playwright/test'
import { test, expect, promptReady, clickIntoEditor, resolveBackend } from './harness'
import { startSshd, rpc } from './sshd-fixture'
import { readStand } from './stand'

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

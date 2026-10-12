/**
 * THE FEATURE'S ACCEPTANCE TEST (nocx-2v80t, task nocx-2v80t.4).
 *
 * What a user can do that they could not before: run a command, close nocx
 * before it prints anything, and find its finished block — with every line it
 * printed — in the window they open afterwards.
 *
 * Before the backend owned blocks this was impossible: a block's body was
 * serialised from the renderer's own terminal buffer, so a command whose
 * output arrived while no renderer existed had no body anywhere.
 *
 * WHY A WINDOW TYPES THE COMMAND. The ordinary way a person starts a command
 * is typing it, and the property the criterion guards is that the body is
 * never taken from a client's buffer. That is excluded by construction: the
 * command waits on a flag file the test creates only after the window's
 * browser CONTEXT is closed and the backend reports the session unattached, so
 * the first renderer is gone before the first output byte exists.
 *
 * WHY THE FRESH CLIENT IS A NEW CONTEXT. A reload keeps storage and, on a fast
 * machine, the same socket; a new context has neither, so whatever it paints
 * it can only have been sent.
 *
 * NOTHING HERE WAITS ON A DURATION. The shell's wait is on the flag file, and
 * the test's waits are on the backend's own statements: the session is
 * unattached, the block's rows artifact is sealed.
 *
 * WHY THIS SPEC OWNS ITS BACKEND. The fresh client restores every tab the
 * backend holds, and on the shared stand that would be every other spec's.
 */
import { expect, type Browser, type Locator, type Page } from '@playwright/test'
import { mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { BASE_URL } from './base-url'
import {
  standalone as base,
  VaultBackend,
  bindEndpoint,
  clickIntoEditor,
  openControlPlane,
  promptReady,
  type BackendEndpoint,
  type DisposableRoot,
} from './harness'
import { readStand } from './stand'

const test = base

const MARKER_COUNT = 300
const COMMAND_SLEEP_SECONDS = 2

interface LiveSession {
  sessionId: string
  attached: boolean
}

interface LedgerEntry {
  id: string
  intent: string
}

interface Artifact {
  id: string
  mediaType: string
  state: string
  truncated: string | null
}

async function ask<T>(ep: BackendEndpoint, method: string, params: unknown): Promise<T> {
  const wire = await openControlPlane(ep.port, ep.token)
  try {
    return (await wire.call(method, params)) as T
  } finally {
    wire.close()
  }
}

async function freshClient(browser: Browser, ep: BackendEndpoint): Promise<Page> {
  const context = await browser.newContext({ baseURL: BASE_URL })
  const page = await context.newPage()
  await page.setViewportSize({ width: 1280, height: 900 })
  await bindEndpoint(page, ep)
  await page.goto('/')
  return page
}

/** The exact duration remains on the header chip's title after it is rounded for display. */
async function expectDurationAtLeast(block: Locator, minimumMs: number): Promise<void> {
  const duration = block.locator('.ui-meta[data-column="duration"]')
  await expect(duration).toBeVisible()
  const title = await duration.getAttribute('title')
  expect(Number.parseInt(title ?? '', 10)).toBeGreaterThanOrEqual(minimumMs)
}

/** The ledger entry for exactly this command line, once the backend has one. */
async function entryFor(ep: BackendEndpoint, command: string): Promise<LedgerEntry | undefined> {
  const page = await ask<{ entries: LedgerEntry[] }>(ep, 'ledger.query', {
    scope: 'everywhere',
    limit: 100,
  })
  return page.entries.find((e) => e.intent === command)
}

async function rowsArtifact(ep: BackendEndpoint, entryId: string): Promise<Artifact | undefined> {
  const detail = await ask<{ artifacts: Artifact[] }>(ep, 'ledger.get', { id: entryId })
  return detail.artifacts.find((a) => a.mediaType === 'application/x-nocx-rows')
}

test.describe('a command nobody watched still has its output', () => {
  let root: DisposableRoot
  let backend: VaultBackend

  test.beforeEach(() => {
    root = { root: mkdtempSync(join(tmpdir(), 'nocx-unwatched-')) }
    backend = new VaultBackend(readStand().server, root)
  })

  test.afterEach(() => {
    backend?.stop()
  })

  test('a block whose output arrived with no window open is shown whole in the next window', async ({
    browser,
  }) => {
    test.setTimeout(180_000)
    const ep = await backend.start()

    const nonce = Date.now().toString(36)
    const markers = Array.from(
      { length: MARKER_COUNT },
      (_, i) => `UNWATCHED-${nonce}-${String(i + 1).padStart(3, '0')}`,
    )
    const flag = join(mkdtempSync(join(tmpdir(), 'nocx-unwatched-flag-')), 'go')
    // POSIX sh: the pane runs the host's login shell, bash or zsh.
    const command =
      `while [ ! -e '${flag}' ]; do sleep 0.1; done; ` +
      `sleep ${COMMAND_SLEEP_SECONDS}; ` +
      `i=1; while [ "$i" -le ${MARKER_COUNT} ]; do printf 'UNWATCHED-${nonce}-%03d\\n' "$i"; i=$((i+1)); done`

    // ── a window types the command, and it starts ─────────────────────────
    const first = await freshClient(browser, ep)
    await promptReady(first)
    await clickIntoEditor(first)
    await first.keyboard.type(command)
    await first.keyboard.press('Enter')
    await expect(first.locator('.pane.active .cmd-block.cmd-block-running')).toHaveCount(1, {
      timeout: 30_000,
    })
    await expect.poll(() => entryFor(ep, command), { timeout: 30_000 }).toBeTruthy()

    // ── the window goes away before the command has printed anything ──────
    await first.context().close()
    await expect
      .poll(
        async () => {
          const live = await ask<{ sessions: LiveSession[] }>(ep, 'sessions.live', {})
          return live.sessions.map((s) => s.attached)
        },
        { timeout: 30_000 },
      )
      .toEqual([false])

    writeFileSync(flag, '')

    // ── the command prints and ends with nobody attached ──────────────────
    const entry = (await entryFor(ep, command))!
    await expect
      .poll(async () => (await rowsArtifact(ep, entry.id))?.state, { timeout: 60_000 })
      .toBe('sealed')

    const artifact = (await rowsArtifact(ep, entry.id))!
    expect(artifact.truncated).toBeNull()
    const body = await ask<{ body: string }>(ep, 'ledger.artifact', { id: artifact.id })
    const storedRows = body.body
      .split('\n')
      .filter(Boolean)
      .map((line) => (JSON.parse(line) as { row: { text: string } }).row.text)
    expect(storedRows).toEqual(markers)

    // ── a stranger opens nocx and sees the whole block ────────────────────
    const second = await freshClient(browser, ep)
    await promptReady(second)
    const block = second.locator(`.pane.active .cmd-block[data-entry-id="${entry.id}"]`)
    await expect(block).toBeVisible({ timeout: 60_000 })
    await expect(block).not.toHaveClass(/\bcmd-block-running\b/)
    await expect(block).toContainText(markers[MARKER_COUNT - 1], { timeout: 30_000 })
    const shown =
      (await block.locator('.cmd-output').textContent())?.match(
        new RegExp(`UNWATCHED-${nonce}-\\d{3}`, 'g'),
      ) ?? []
    expect(shown).toEqual(markers)
    await expect(block.locator('[data-output-incomplete]')).toHaveCount(0)
    await expect(second.locator('.pane.active [data-restore-boundary="true"]')).toHaveCount(0)
    await expectDurationAtLeast(block, COMMAND_SLEEP_SECONDS * 1000)

    // The same duration is visible when a window watches the command finish.
    const watchedCommand = `sleep ${COMMAND_SLEEP_SECONDS}; echo WATCHED-${nonce}`
    await clickIntoEditor(second)
    await second.keyboard.type(watchedCommand)
    await second.keyboard.press('Enter')
    await expect.poll(() => entryFor(ep, watchedCommand), { timeout: 30_000 }).toBeTruthy()
    const watchedEntry = (await entryFor(ep, watchedCommand))!
    const watchedBlock = second.locator(
      `.pane.active .cmd-block[data-entry-id="${watchedEntry.id}"]`,
    )
    await expect(watchedBlock).toBeVisible()
    await expect(watchedBlock).toHaveClass(/\bcmd-block-running\b/)
    await expect(watchedBlock).not.toHaveClass(/\bcmd-block-running\b/)
    await expectDurationAtLeast(watchedBlock, COMMAND_SLEEP_SECONDS * 1000)

    await second.context().close()
  })
})

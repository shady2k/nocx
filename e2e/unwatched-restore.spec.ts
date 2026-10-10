/**
 * THE STAGE'S USER CRITERION (nocx-zg3k3.5.6), watched end to end: a person
 * runs a command in a pane nobody is watching, restarts nocx, and reads its
 * whole output in the restored card — every line the command printed, the
 * rows that scrolled away included, in order, first and last, with no notice
 * that rows are missing.
 *
 * Both halves of that sentence existed and were watched apart:
 * e2e/unwatched-block.spec.ts proves a command whose output arrived with no
 * window open is whole in the next window, and e2e/restore.spec.ts proves a
 * watched one-line echo comes back after backend.restart(). Nothing checked
 * them together, with output longer than one screen — the case that can
 * actually lose rows, because the card must then be fed from the store's
 * rows artifact rather than from anything a live process remembers.
 *
 * WHY A WINDOW TYPES THE COMMAND. The ordinary way a person starts a command
 * is typing it, and the property the criterion guards is that the body is
 * never taken from a client's buffer. That is excluded by construction: the
 * command waits on a flag file the test creates only after the window's
 * browser CONTEXT is closed and the backend reports the session unattached,
 * so the first renderer is gone before the first output byte exists.
 *
 * WHY THE RESTART. Sealing proves the store took the output; the restart is
 * what turns the second window's card into a read of what a PREVIOUS PROCESS
 * wrote. The client that reads it back shares nothing with the process that
 * captured the rows but the disposable home on disk — if the whole output
 * reached the screen only because the writer was still alive, this is the
 * test that notices.
 *
 * NOTHING HERE WAITS ON A DURATION. The shell's wait is on the flag file;
 * the test's waits are on the backend's own statements (the session is
 * unattached, the artifact is sealed, the ledger is served again) and on the
 * screen itself (the restored card's rows, the running mark leaving it).
 *
 * WHY THIS SPEC OWNS ITS BACKEND. It restarts that backend mid-test, and a
 * fresh client restores every tab the backend holds — on the shared stand
 * that would be every other spec's.
 */
import { expect, type Browser, type Page } from '@playwright/test'
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

// Well beyond one screen at any ordinary pane size: the viewport is 1280x900,
// so no pane the window opens with shows two hundred rows at once, and the
// card can only be whole if the rows that scrolled away came back too.
const MARKER_COUNT = 200

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

test.describe('a command nobody watched still has its output after a restart', () => {
  let root: DisposableRoot
  let backend: VaultBackend

  test.beforeEach(() => {
    root = { root: mkdtempSync(join(tmpdir(), 'nocx-unwatched-restore-')) }
    backend = new VaultBackend(readStand().server, root)
  })

  test.afterEach(() => {
    backend?.stop()
  })

  /**
   * The whole scenario, for a command that prints `markers` and nothing
   * else. Both tests below run this one shape — one with two hundred lines,
   * one with a single `echo` — so the short case is exercised by the same
   * steps and not by a shortcut of its own.
   */
  async function unwatchedPrintRestartRead(
    browser: Browser,
    nonce: string,
    flag: string,
    command: string,
    markers: string[],
  ): Promise<void> {
    const ep1 = await backend.start()

    // ── a window types the command, and it starts ─────────────────────────
    const first = await freshClient(browser, ep1)
    await promptReady(first)
    await clickIntoEditor(first)
    await first.keyboard.type(command)
    await first.keyboard.press('Enter')
    await expect(first.locator('.pane.active .cmd-block.cmd-block-running')).toHaveCount(1, {
      timeout: 30_000,
    })
    await expect.poll(() => entryFor(ep1, command), { timeout: 30_000 }).toBeTruthy()

    // ── the window goes away before the command has printed anything ──────
    await first.context().close()
    await expect
      .poll(
        async () => {
          const live = await ask<{ sessions: LiveSession[] }>(ep1, 'sessions.live', {})
          return live.sessions.map((s) => s.attached)
        },
        { timeout: 30_000 },
      )
      .toEqual([false])

    // ── the command prints and ends with nobody attached ──────────────────
    writeFileSync(flag, '')
    const entry = (await entryFor(ep1, command))!
    await expect
      .poll(async () => (await rowsArtifact(ep1, entry.id))?.state, { timeout: 60_000 })
      .toBe('sealed')
    // The store says the body is whole — nothing was cut at a ceiling while
    // nobody watched. The screen assertion below is the user-facing half;
    // this names the store's half if the two ever disagree.
    const artifact = (await rowsArtifact(ep1, entry.id))!
    expect(artifact.truncated).toBeNull()

    // ── the restart: the process that captured the rows is gone ───────────
    const ep2 = await backend.restart()
    // Precondition, not the claim: the new process serves the same ledger.
    // Without it, "the card never appeared" could not tell the store losing
    // the entry apart from the renderer failing to paint it.
    await expect.poll(() => entryFor(ep2, command), { timeout: 30_000 }).toBeTruthy()

    // ── a stranger opens nocx and reads the whole output in the card ──────
    const second = await freshClient(browser, ep2)
    await promptReady(second)
    // `[data-restored="true"]` is the point: after a restart this block can
    // only be a restored card, and a build that showed it any other way must
    // fail here rather than pass as a live block.
    const block = second.locator(
      `.pane.active .cmd-block[data-entry-id="${entry.id}"][data-restored="true"]`,
    )
    await expect(block).toBeVisible({ timeout: 60_000 })
    // The card is finished, and the rule under it says the shell below is a
    // new one (ADR-0019 §3).
    await expect(block).not.toHaveClass(/\bcmd-block-running\b/)
    await expect(second.locator('.pane.active [data-restore-boundary="true"]')).toBeVisible({
      timeout: 30_000,
    })
    // Every line, in order, first and last included: the card's own output
    // region read back against what the command printed. The poll is what
    // waits for the paint — a restored card is visible before its artifact
    // read answers, so visibility alone would race the rows in.
    const pattern = new RegExp(`UNWATCHED-${nonce}-\\d{3}`, 'g')
    await expect
      .poll(async () => (await block.locator('.cmd-output').textContent())?.match(pattern) ?? [], {
        timeout: 60_000,
        message: 'the restored card never showed every row',
      })
      .toEqual(markers)
    // And no notice that rows are missing.
    await expect(block.locator('[data-output-incomplete]')).toHaveCount(0)

    await second.context().close()
  }

  test('the whole transcript, rows that scrolled away included, is on the restored card', async ({
    browser,
  }) => {
    test.setTimeout(180_000)
    const nonce = Date.now().toString(36)
    const markers = Array.from(
      { length: MARKER_COUNT },
      (_, i) => `UNWATCHED-${nonce}-${String(i + 1).padStart(3, '0')}`,
    )
    const flag = join(mkdtempSync(join(tmpdir(), 'nocx-unwatched-restore-flag-')), 'go')
    // POSIX sh: the pane runs the host's login shell, bash or zsh.
    const command =
      `while [ ! -e '${flag}' ]; do sleep 0.1; done; ` +
      `i=1; while [ "$i" -le ${MARKER_COUNT} ]; do printf 'UNWATCHED-${nonce}-%03d\\n' "$i"; i=$((i+1)); done`

    await unwatchedPrintRestartRead(browser, nonce, flag, command, markers)
  })

  test('a one-line command from the same unwatched run restores the same way', async ({
    browser,
  }) => {
    test.setTimeout(180_000)
    const nonce = Date.now().toString(36)
    const markers = [`UNWATCHED-${nonce}-001`]
    const flag = join(mkdtempSync(join(tmpdir(), 'nocx-unwatched-restore-flag-')), 'go')
    const command = `while [ ! -e '${flag}' ]; do sleep 0.1; done; echo UNWATCHED-${nonce}-001`

    await unwatchedPrintRestartRead(browser, nonce, flag, command, markers)
  })
})

// A visual BOARD, not an assertion suite (nocx-9bpeq.18) — screenshots for a
// human to hold up against the mockups named in
// .internal/specs/2026-09-15-terminal-screen-register-mockup-pass.md
// (`output/imagegen/nocx-concepts-v2/01-command-states.png`,
// `03-split-and-files.png`, `nocx-concepts-v4/background-input-request.png`).
//
// Every `expect` below checks an element EXISTS before the shot is taken, so
// a selector rename fails loudly instead of silently painting a blank pane —
// it is not a claim that the layout matches the mockup. That comparison is
// made by a person reading the PNGs this file writes to
// test-results/visual-board/, one per theme and moment:
//
//   <theme>-running.png   — the composer hidden, a live block with Stop
//   <theme>-composer.png  — the composer back, focused, with a draft typed
//
// Modelled on terminal-screen-register-mockup-pass.spec.ts: promptReady, the
// INPUT selector and the SETTLED/hasText wait-for-a-command idiom are its own.
import { execFileSync } from 'node:child_process'

import { clickIntoEditor, expect, openControlPlane, promptReady, test, type Page } from './harness'
import { readStand } from './stand'

const INPUT = '.pane.active .nocx-editor-input'
const SETTLED = '.pane.active .cmd-block:not(.cmd-block-running)'
const RUNNING = '.pane.active .cmd-block.cmd-block-running'
const COMPOSER_PROMPT = '.pane.active .nocx-editor-chrome .ui-prompt-context'

test.use({ viewport: { width: 1400, height: 900 } })

/** Whether `git` is on this stand's PATH — the same guard the register spec
 *  uses, for the same reason: e2e/Dockerfile installs no `git` package. */
function detectGit(): boolean {
  try {
    execFileSync('git', ['--version'], { stdio: 'ignore' })
    return true
  } catch {
    return false
  }
}
const GIT_AVAILABLE = detectGit()

/** Switch the running stand's theme over the control plane — no Settings
 *  navigation needed for a board that only wants two themes. */
async function setTheme(page: Page, id: string): Promise<void> {
  const stand = readStand()
  const wire = await openControlPlane(stand.port, stand.token)
  try {
    await wire.call('settings.set', { key: 'ui.theme', value: id })
  } finally {
    wire.close()
  }
  await page.waitForFunction((t) => document.documentElement.getAttribute('data-theme') === t, id)
}

async function runAndSettle(page: Page, command: string, marker: string): Promise<void> {
  await page.locator(INPUT).fill(command)
  await page.keyboard.press('Enter')
  await expect(page.locator(SETTLED, { hasText: marker })).toHaveCount(1, { timeout: 15_000 })
  await promptReady(page)
}

/** Paint one theme's board: a `git diff --stat`-shaped block, a failed test
 *  block, a running block with Stop, then the composer back with a draft. */
async function paintBoard(page: Page, prefix: string): Promise<void> {
  if (GIT_AVAILABLE) {
    await runAndSettle(
      page,
      'mkdir -p ~/repo && cd ~/repo && git init -q -b main && ' +
        'git -c user.email=t@t -c user.name=t commit -q --allow-empty -m init # board-repo-init',
      'board-repo-init',
    )
    const branchShown = await page
      .locator(COMPOSER_PROMPT)
      .filter({ hasText: 'main' })
      .first()
      .isVisible()
      .catch(() => false)
    if (!branchShown) {
      // nocx-9bpeq.13/.16 wire the branch source; if it is not in yet the
      // board still paints, just without "main" in the prompt line — this
      // is a report, not a failure, per the coordinator's instruction.
      console.log(
        `visual-board (${prefix}): "main" did not appear in the composer's prompt line — ` +
          'the branch source may still be landing (nocx-9bpeq.13/.16).',
      )
    }
  } else {
    console.log(`visual-board (${prefix}): git is not on PATH, skipping the branch block.`)
  }

  // A colourised `git diff --stat`-shaped block — the evidence image's own
  // shape (01-command-states.png): a bold path, green/red counts.
  await runAndSettle(
    page,
    "printf '\\033[1mfrontend/src/styles/tokens.css\\033[0m | " +
      "\\033[32m6 ++++\\033[0m\\033[31m--\\033[0m\\n1 file changed, 4 insertions(+), 2 deletions(-)\\n' " +
      '# board-diff-stat',
    'board-diff-stat',
  )

  // A failed block: the register's own "Exit 1" shape.
  await runAndSettle(
    page,
    'sh -c \'echo "--- FAIL: TestSessionReconnect (0.08s)"; echo FAIL; exit 1\' # board-fail',
    'board-fail',
  )

  // A running block: the mockup's `Running · Ns` plus a visible Stop.
  await page.locator(INPUT).fill('sleep 30 # board-running')
  await page.keyboard.press('Enter')
  const running = page.locator(RUNNING, { hasText: 'board-running' })
  await expect(running).toHaveCount(1, { timeout: 15_000 })
  await expect(running.getByRole('button', { name: 'Stop' })).toBeVisible()
  // The composer is hidden while a command owns input (spec §6's own rule) —
  // this is the shot where it stays hidden.
  await page.screenshot({ path: `test-results/visual-board/${prefix}-running.png` })

  // Stop it, bring the composer back and type a draft — the mockup's second
  // moment: "Run ▾ │ git checkout -b feat/fonts" in a focused field.
  await running.getByRole('button', { name: 'Stop' }).click()
  await expect(running).toHaveCount(0, { timeout: 15_000 })
  await promptReady(page)
  await clickIntoEditor(page)
  await page.locator(INPUT).fill('git checkout -b feat/fonts')
  await page.screenshot({ path: `test-results/visual-board/${prefix}-composer.png` })
}

test.describe('visual board (nocx-9bpeq.18)', () => {
  test.afterEach(async () => {
    // Leave the stand exactly as terminal-screen-register.spec.ts's own
    // afterEach does, so a board run does not strand a later spec on
    // whichever theme this file last painted.
    const stand = readStand()
    const wire = await openControlPlane(stand.port, stand.token)
    try {
      await wire.call('settings.set', { key: 'ui.theme', value: 'tokyo-night' })
    } finally {
      wire.close()
    }
  })

  test('tokyo-night', async ({ page }) => {
    await page.goto('/')
    await promptReady(page)
    await paintBoard(page, 'tokyo-night')
  })

  test('light', async ({ page }) => {
    await page.goto('/')
    await promptReady(page)
    await setTheme(page, 'light')
    await paintBoard(page, 'light')
  })
})

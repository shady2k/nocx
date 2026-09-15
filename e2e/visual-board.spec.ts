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
//   tokyo-night-empty.png          — a fresh pane, nothing run: the composer
//                                    alone at `~`, exactly the owner's own
//                                    screenshot
//   <theme>-owner-scenario.png     — the owner's own two-command scenario
//                                    (`ls`, then an unknown command) with the
//                                    pane context strip above it (decision
//                                    2026-09-15-terminal-screen-mockup-
//                                    decision.md §1 items 1 and 3)
//   <theme>-running.png            — the composer hidden, a live block with
//                                    Stop
//   <theme>-process-bar.png        — the SAME running moment, framed on the
//                                    running-state footer (decision §1,
//                                    "Running-state gap"): Send input,
//                                    Interrupt, Stop
//   <theme>-composer.png           — the composer back, focused, with a
//                                    draft typed
//
// NOT extended with a two-pane split shot, despite the decision record's D
// task asking for one: `.internal/specs/2026-09-15-terminal-screen-mockup-
// decision.md` §1 item 3 and its completion criterion assume a side-by-side
// split pane already exists to mount a PaneContext INSIDE. It does not —
// `frontend/src/panes.ts`'s `Pane`/`PaneManager` stack panes one at a time
// behind `.pane.active` (`styles/base.css`'s `.pane { position: absolute;
// inset: 0; visibility: hidden }`), there is no layout code that renders two
// `.pane` elements side by side in one tab, and `br search "split pane"` /
// `"split view"` returns no bead for building one. Inventing a fake split
// for one screenshot would be exactly the "invented activity" AGENTS.md
// forbids. PaneContext's own API already supports the split identity
// variant (`data-split`, `data-active` — `ui/pane-context.ts`) so mounting a
// second one needs no further kit work once a real split layout exists.
// Modelled on terminal-screen-register-mockup-pass.spec.ts: promptReady and
// the INPUT selector are its own. Waiting is by SETTLED COUNT rather than by
// a `# marker` comment in the typed command: round 1 embedded a marker in a
// long printf one-liner, and the real terminal (not just CSS) wrapped that
// long line mid-word — the wrapped tail then bled into the block's own
// output in the screenshot ("d-diff-stat" as a stray first line). Counting
// settled blocks keeps every typed command short and realistic, closer to
// what the mockups themselves show.
import { execFileSync } from 'node:child_process'

import { clickIntoEditor, expect, openControlPlane, promptReady, test, type Page } from './harness'
import { readStand } from './stand'

const INPUT = '.pane.active .nocx-editor-input'
const SETTLED = '.pane.active .cmd-block:not(.cmd-block-running)'
const RUNNING = '.pane.active .cmd-block.cmd-block-running'
const COMPOSER_PROMPT = '.pane.active .nocx-editor-chrome .ui-prompt-context'
const PANE_CONTEXT = '.pane.active .ui-pane-context'
const PROCESS_BAR = '.pane.active .ui-process-bar'

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

/** Type one command and wait for it to settle — by the SETTLED count
 *  growing by one, not by text, so the command itself can stay exactly what
 *  a person would type (see the file header on why that matters here). */
async function runAndSettle(page: Page, command: string): Promise<void> {
  const before = await page.locator(SETTLED).count()
  await page.locator(INPUT).fill(command)
  await page.keyboard.press('Enter')
  await expect(page.locator(SETTLED)).toHaveCount(before + 1, { timeout: 15_000 })
  await promptReady(page)
}

/** The owner's own two-command scenario (decision record's §"Completion
 *  criterion": "the owner's two-command scenario at the top under context
 *  chrome") — `ls`, then a command nothing resolves, so the board shows
 *  both a success and the unresolved-command underline in one short
 *  transcript directly below the pane context strip (decision §1 items 1
 *  and 3), with idle space below it rather than above the first prompt. */
async function paintOwnerScenario(page: Page, prefix: string): Promise<void> {
  await expect(page.locator(PANE_CONTEXT)).toBeVisible()
  await runAndSettle(page, 'ls')
  // Not a real command anywhere on this stand's PATH — the same unresolved
  // shape the decision record's own evidence image shows (a normal-coloured,
  // dotted-underlined command word), never a red "command not found" panel.
  await runAndSettle(page, 'thiscommanddoesnotexist')
  await page.screenshot({ path: `test-results/visual-board/${prefix}-owner-scenario.png` })
}

/** Paint one theme's board: a real `git diff --stat`, a failed test block, a
 *  running block with Stop, then the composer back with a draft. */
async function paintBoard(page: Page, prefix: string): Promise<void> {
  if (GIT_AVAILABLE) {
    // Four SHORT commands rather than one long chain: the terminal itself
    // (not just CSS) wraps a line past its column width, and round 2's
    // single 175-character setup line wrapped mid-word with the tail
    // bleeding into the block's own output — "ame=t commit -q -m init" as a
    // stray line. Every command below stays under 90 characters, comfortably
    // inside even a narrow pane at this viewport.
    // `rm -rf` first: both themes' tests run against the SAME backend home
    // (tokyo-night then light), and without this the second run's `git
    // init` hit an already-initialised ~/repo — "warning: re-init: ignored
    // --initial-branch=main" and a commit with nothing to commit, since
    // notes.txt already carried the first run's content. Found the same way
    // as the wrapping bug above: by reading the light theme's screenshot,
    // not by reasoning about the harness.
    await runAndSettle(page, 'rm -rf ~/repo && mkdir -p ~/repo && cd ~/repo && git init -q -b main')
    await runAndSettle(page, 'git config user.email t@t && git config user.name t')
    // A tracked file with something to change, so the diff block below has
    // real content — both a `+` and a `-` line, the mockup's own shape.
    await runAndSettle(page, "printf 'a\\nb\\nc\\n' > n && git add n && git commit -qm i")
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

    // The evidence image's own shape (01-command-states.png): a real
    // coloured `git diff --stat`, not a synthesised one.
    await runAndSettle(
      page,
      "printf 'a\\nB\\nc\\nd\\n' > n && git add -A && git -c color.ui=always diff --cached --stat",
    )
  } else {
    console.log(`visual-board (${prefix}): git is not on PATH, skipping the git blocks.`)
  }

  // A failed block: the register's own "Exit 1" shape.
  await runAndSettle(
    page,
    'sh -c \'echo "--- FAIL: TestSessionReconnect (0.08s)"; echo FAIL; exit 1\'',
  )

  // A running block: the mockup's `Running · Ns` plus a visible Stop.
  await page.locator(INPUT).fill('sleep 30')
  await page.keyboard.press('Enter')
  const running = page.locator(RUNNING)
  await expect(running).toHaveCount(1, { timeout: 15_000 })
  await expect(running.getByRole('button', { name: 'Stop' })).toBeVisible()
  // The composer is hidden while a command owns input (spec §6's own rule) —
  // this is the shot where it stays hidden.
  await page.screenshot({ path: `test-results/visual-board/${prefix}-running.png` })

  // The running-state footer (decision §1, "Running-state gap"): shown for
  // ordinary running on the normal buffer, replacing the composer rather
  // than sitting beside it — never both surfaces visible at once. Stop here
  // is the SAME stop owner as the block header's own Stop; Interrupt is the
  // deliberately DIFFERENT `Ctrl+C` intent (`signalActiveCommand`
  // ('interrupt')), never claimed as Stop's own shortcut.
  const processBar = page.locator(PROCESS_BAR)
  await expect(processBar).toBeVisible()
  await expect(processBar.getByRole('button', { name: 'Send input' })).toBeVisible()
  await expect(processBar.getByRole('button', { name: 'Interrupt' })).toBeVisible()
  await expect(processBar.getByRole('button', { name: 'Stop' })).toBeVisible()
  await page.screenshot({ path: `test-results/visual-board/${prefix}-process-bar.png` })

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
    // The owner's own screenshot: a fresh pane, nothing run yet — the
    // composer alone at `~`, before the board types anything into it.
    await page.screenshot({ path: 'test-results/visual-board/tokyo-night-empty.png' })
    await paintOwnerScenario(page, 'tokyo-night')
    await paintBoard(page, 'tokyo-night')
  })

  test('light', async ({ page }) => {
    await page.goto('/')
    await promptReady(page)
    await setTheme(page, 'light')
    await paintOwnerScenario(page, 'light')
    await paintBoard(page, 'light')
  })
})

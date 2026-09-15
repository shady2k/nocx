// The mockup pass's additive half, as assertions (nocx-9bpeq.14, spec
// .internal/specs/2026-09-15-terminal-screen-register-mockup-pass.md §2, §4, §6).
//
// Written from the spec, not from the children's code (AGENTS.md testing rule 4):
// nocx-9bpeq.12, .13, .15 and .16 are being built alongside this file, in sibling
// worktrees, and none of their output is read here.
//
// A sibling of terminal-screen-register.spec.ts rather than an extension of it:
// that file iterates all twelve themes for its own assertions, and every check
// below needs only the one theme a fresh session opens in.
//
// Round 2 (against the merged tree at c00d1067): nocx-9bpeq.12's status-group
// reorder landed, so that test's `test.fail()` is gone and its assertion now
// reads the right-hand group rather than the isolated status word (see
// below). Every `test.fail()` marker is removed from this round on, on the
// coordinator's instruction: nocx-9bpeq.15/.16 are still landing in parallel,
// and a red test here should report red rather than "expected failure" — the
// spec is meant to state the truth about the merged tree at all times, not
// carry a marker somebody has to remember to delete.
import { execFileSync } from 'node:child_process'

import { clickIntoEditor, expect, promptReady, test, type Page } from './harness'

const INPUT = '.pane.active .nocx-editor-input'
const SETTLED = '.pane.active .cmd-block:not(.cmd-block-running)'
const COMPOSER_CHROME = '.pane.active .nocx-editor-chrome'
// The names nocx-9bpeq.12/.15/.16 produce (spec §2, §4, §6). One line each, so
// a rename during their work is one edit here.
const PROMPT_CONTEXT = '.ui-prompt-context'
const STATUS = '.cmd-header .ui-meta[data-tone="danger"]'

const normalise = (text: string | null): string => (text ?? '').replace(/\s+/g, ' ').trim()

/** Whether `git` is on this stand's PATH. Checked once, from the Node process
 *  that shares the container's PATH with the backend the suite drives — the
 *  same assumption e2e/git-fixture.ts already makes for every other git spec,
 *  none of which guards it. e2e/Dockerfile installs no `git` package itself
 *  (checked 2026-09-15), so the guard is real rather than decorative: it is
 *  the base Playwright image that must be carrying it. */
function detectGit(): boolean {
  try {
    execFileSync('git', ['--version'], { stdio: 'ignore' })
    return true
  } catch {
    return false
  }
}
const GIT_AVAILABLE = detectGit()

test.beforeEach(async ({ page }) => {
  await page.goto('/')
  await promptReady(page)
})

test("a finished block reads the pane's real branch, and the path is never absolute", async ({
  page,
}) => {
  test.skip(
    !GIT_AVAILABLE,
    "git is not on this stand's PATH (e2e/Dockerfile installs no git package)",
  )
  // nocx-9bpeq.16 wires the branch and home sources into the prompt line;
  // depends on nocx-9bpeq.12 (PromptContext exists) and nocx-9bpeq.13 (the
  // sources themselves). Still landing — expect this red until it is in.

  await page
    .locator(INPUT)
    .fill(
      'mkdir -p ~/repo && cd ~/repo && git init -q -b main && ' +
        'git -c user.email=t@t -c user.name=t commit -q --allow-empty -m init # t14-repo-init',
    )
  await page.keyboard.press('Enter')
  await expect(page.locator(SETTLED, { hasText: 't14-repo-init' })).toHaveCount(1, {
    timeout: 15_000,
  })
  await promptReady(page)

  // The branch source asks on a debounce after the verified cwd changes
  // (spec §3), so it is not yet known the instant this block settles — a
  // person sees it land in the composer before they type the next command,
  // and the NEXT block records exactly what was known at ITS submit (§3: "a
  // block records the branch the pane knew when the command was submitted").
  // Waiting for the composer rather than a duration is what that spec text
  // means in test form.
  await expect(page.locator(`${COMPOSER_CHROME} ${PROMPT_CONTEXT}`)).toContainText('main', {
    timeout: 15_000,
  })

  await page.locator(INPUT).fill('true # t14-branch-probe')
  await page.keyboard.press('Enter')
  const block = page.locator(SETTLED, { hasText: 't14-branch-probe' })
  await expect(block).toHaveCount(1, { timeout: 15_000 })
  await promptReady(page)

  const context = block.locator(`.cmd-header ${PROMPT_CONTEXT}`)
  await expect(context).toBeVisible()
  const text = normalise(await context.textContent())
  expect(text).toContain('~/repo')
  await expect(context).toHaveAttribute('title', /main/)

  const pathPart = context.locator('[data-part="path"]')
  const pathText = normalise(await pathPart.textContent())
  expect(pathText.startsWith('/')).toBe(false)

  const composerContext = page.locator(`${COMPOSER_CHROME} ${PROMPT_CONTEXT}`)
  await expect(composerContext).toBeVisible()
  expect(normalise(await composerContext.textContent())).toContain('main')
})

test('Stop is a visible, keyboard-reachable button on the running row', async ({ page }) => {
  // nocx-9bpeq.12: the running row's always-visible Stop button (spec §4).
  // Still landing — expect this red until it is in.
  test.setTimeout(60_000)

  await page.locator(INPUT).fill('sleep 30')
  await page.keyboard.press('Enter')
  const block = page.locator('.pane.active .cmd-block').filter({ hasText: 'sleep 30' }).last()
  await expect(block).toHaveClass(/cmd-block-running/, { timeout: 15_000 })

  const stop = block.getByRole('button', { name: 'Stop' })
  await expect(stop).toBeVisible()
  await stop.focus()
  await expect(stop).toBeFocused()
  await page.keyboard.press('Enter')

  await expect(block).not.toHaveClass(/cmd-block-running/, { timeout: 15_000 })
  const outcome = await block.getAttribute('data-outcome')
  expect(outcome).not.toBeNull()
  expect(outcome).not.toBe('success')
  // nocx-9bpeq.19: a command stopped through this button is cancelled, never
  // failed — the danger tint and the bar are for a program's OWN failure,
  // and SIGINT's exit status is not that just because it is nonzero.
  expect(outcome).not.toBe('failure')
  expect(outcome).toBe('cancelled')
  await expect(block.getByRole('button', { name: 'Stop' })).toHaveCount(0)
})

test('a failed block states "Exit 1" and its duration, and the word is danger-toned', async ({
  page,
}) => {
  await page.locator(INPUT).fill('false')
  await page.keyboard.press('Enter')
  const block = page.locator(SETTLED, { hasText: 'false' }).last()
  await expect(block).toHaveAttribute('data-outcome', 'failure', { timeout: 15_000 })

  // The status word is its own ui-meta, danger-toned — a separate assertion
  // from the group reading below.
  const status = block.locator(STATUS)
  await expect(status).toHaveText('Exit 1')

  // What a person reads is the whole right-hand group, IN DOM ORDER — never
  // composed by the test itself, which would pass on any order including
  // the wrong one. Round 3: verified in frontend/src/scrollback/blocks.ts
  // (settleBlockOutcome, BLOCK_KIND_RULES.command.headerRight.chips) that
  // today's DOM order is duration then word, joined by a flex gap with no
  // separator character at all — a defect against spec §4, now being fixed
  // by nocx-9bpeq.12 alongside the `ui-meta__sep` separator this regex
  // expects. So this is RED until that lands, and rightly so: no
  // test.fail() masks it. Buttons (Stop while running, ⋮ always) are
  // excluded by cloning the group and removing them before reading text;
  // whitespace is normalised so the separator's own spacing does not
  // matter.
  const groupText = await block.locator('.cmd-header .cmd-header-right').evaluate((el) => {
    const clone = el.cloneNode(true) as HTMLElement
    clone.querySelectorAll('button').forEach((b) => b.remove())
    return clone.textContent ?? ''
  })
  expect(normalise(groupText)).toMatch(/^Exit 1\s*·\s*\S+$/)
})

/** The composer's input box: whichever element is the nearest common ancestor
 *  of the mode indicator and the CM6 root, inside `.nocx-editor` — computed
 *  structurally rather than by a class name nocx-9bpeq.15 has not chosen yet
 *  (spec §6: "the CM6 editor and the mode switch sit inside one box"). */
async function editorInputBoxBorder(page: Page): Promise<{ width: number; color: string }> {
  return page.evaluate(() => {
    const root = document.querySelector('.pane.active .nocx-editor')
    if (!root) throw new Error('composer: no .nocx-editor')
    const indicator = root.querySelector('.ui-mode-indicator')
    const cm = root.querySelector('.cm-editor')
    if (!indicator || !cm) throw new Error('composer: missing mode indicator or .cm-editor')
    const ancestors: Element[] = []
    for (let n: Element | null = indicator; n && root.contains(n); n = n.parentElement) {
      ancestors.push(n)
    }
    let common: Element | null = cm
    while (common && !ancestors.includes(common)) common = common.parentElement
    if (!common) throw new Error('composer: no common ancestor of the indicator and .cm-editor')
    const style = getComputedStyle(common)
    return { width: parseFloat(style.borderTopWidth), color: style.borderTopColor }
  })
}

test("the composer's card contains an open input row", async ({ page }) => {
  // nocx-9bpeq.15 moves the CM6 editor and the mode switch inside one
  // bordered box (spec §6) — landed as ComposerFrame's field
  // (composer-frame.css), the common ancestor `editorInputBoxBorder`
  // computes structurally above.

  await page.locator(INPUT).fill('echo t14-field-probe')
  await page.keyboard.press('Enter')
  await expect(page.locator(SETTLED, { hasText: 't14-field-probe' })).toHaveCount(1, {
    timeout: 15_000,
  })
  await promptReady(page)

  await clickIntoEditor(page)
  const focused = await editorInputBoxBorder(page)
  expect(focused.width).toBe(0)
  const card = page.locator('.pane.active .ui-composer-frame')
  expect(await card.evaluate((el) => parseFloat(getComputedStyle(el).borderTopWidth))).toBe(1)

  // Blur by clicking a settled block, as the spec's own phrase for it.
  await page.locator(SETTLED).first().click()
  const blurred = await editorInputBoxBorder(page)
  expect(blurred.width).toBe(0)
})

test('the mode indicator opens a target menu; choosing Ask switches it; Escape closes it', async ({
  page,
}) => {
  // nocx-9bpeq.15 makes ModeIndicator's click open the kit ContextMenu
  // instead of toggling directly (spec §6). Landed — this passes.

  const indicator = page.locator('.pane.active .ui-mode-indicator:visible')
  await expect(indicator).toHaveText('Run')

  await indicator.click()
  const menu = page.getByRole('menu')
  await expect(menu).toBeVisible()
  await expect(menu.getByRole('menuitem', { name: 'Run', exact: true })).toBeVisible()
  const ask = menu.getByRole('menuitem', { name: 'Ask', exact: true })
  await expect(ask).toBeVisible()
  await ask.click()
  await expect(indicator).toHaveText('Ask')

  await indicator.click()
  await expect(page.getByRole('menu')).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page.getByRole('menu')).toHaveCount(0)
})

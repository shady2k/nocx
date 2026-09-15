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
// Every test is `test.fail()`, naming the bead whose commit turns it green and
// deletes the marker — the same discipline nocx-9bpeq.9 used. `test.fail()`
// rather than `.skip()`/`.fixme()` because it still runs the body in the
// container and in CI, so a marker that outlives its bead reports "Expected to
// fail, but passed" instead of staying silently green.
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
  test.fail() // nocx-9bpeq.16 wires the branch and home sources into the prompt
  // line; depends on nocx-9bpeq.12 (PromptContext exists at all) and
  // nocx-9bpeq.13 (the sources themselves). That commit deletes this line.

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

  await page.locator(INPUT).fill('true # t14-branch-probe')
  await page.keyboard.press('Enter')
  const block = page.locator(SETTLED, { hasText: 't14-branch-probe' })
  await expect(block).toHaveCount(1, { timeout: 15_000 })
  await promptReady(page)

  const context = block.locator(`.cmd-header ${PROMPT_CONTEXT}`)
  await expect(context).toBeVisible()
  const text = normalise(await context.textContent())
  expect(text).toContain('~/repo')
  expect(text).toContain('main')

  const pathPart = context.locator('[data-part="path"]')
  const pathText = normalise(await pathPart.textContent())
  expect(pathText.startsWith('/')).toBe(false)

  const composerContext = page.locator(`${COMPOSER_CHROME} ${PROMPT_CONTEXT}`)
  await expect(composerContext).toBeVisible()
  expect(normalise(await composerContext.textContent())).toContain('main')
})

test('Stop is a visible, keyboard-reachable button on the running row', async ({ page }) => {
  test.fail() // nocx-9bpeq.12 grows the running row's always-visible Stop
  // button beside the spinner (spec §4); today Stop is only a ⋮ menu item.
  // That commit deletes this line.
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
  await expect(block.getByRole('button', { name: 'Stop' })).toHaveCount(0)
})

test('a failed block states "Exit 1" before its duration, not a bare word', async ({ page }) => {
  test.fail() // nocx-9bpeq.12 reorders the status group to "status word ·
  // duration" (spec §4), one mono ui-meta rather than the old duration-first
  // line. That commit deletes this line.

  await page.locator(INPUT).fill('false')
  await page.keyboard.press('Enter')
  const block = page.locator(SETTLED, { hasText: 'false' }).last()
  await expect(block).toHaveAttribute('data-outcome', 'failure', { timeout: 15_000 })

  const status = block.locator(STATUS)
  await expect(status).toBeVisible()
  expect(normalise(await status.textContent())).toMatch(/^Exit 1 · /)
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

test("the composer's input box has a real border that changes with focus", async ({ page }) => {
  test.fail() // nocx-9bpeq.15 moves the CM6 editor and the mode switch inside
  // one bordered box (spec §6); today the composer paints no border at all.
  // That commit deletes this line.

  await page.locator(INPUT).fill('echo t14-field-probe')
  await page.keyboard.press('Enter')
  await expect(page.locator(SETTLED, { hasText: 't14-field-probe' })).toHaveCount(1, {
    timeout: 15_000,
  })
  await promptReady(page)

  await clickIntoEditor(page)
  const focused = await editorInputBoxBorder(page)
  expect(focused.width).toBeGreaterThanOrEqual(1)

  // Blur by clicking a settled block, as the spec's own phrase for it.
  await page.locator(SETTLED).first().click()
  const blurred = await editorInputBoxBorder(page)
  expect(blurred.color).not.toBe(focused.color)
})

test('the mode indicator opens a target menu; choosing Ask switches it; Escape closes it', async ({
  page,
}) => {
  test.fail() // nocx-9bpeq.15 makes ModeIndicator's click open the kit
  // ContextMenu instead of toggling directly (spec §6); today a click toggles
  // the target immediately. That commit deletes this line.

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

import { test, expect, promptReady, settingsReady } from './harness'

// The pet, end to end (nocx-q4qeh.1).
//
// jsdom cannot see any of this: there is no layout, so no block has a top
// edge to stand on, and the animal's whole existence is geometry over a real
// scrollback. What the unit tests prove is that the rules are right; what
// this proves is that a person gets a pet.
//
// The layer is the WINDOW's, not a pane's: one animal, over the whole shell,
// so switching tabs does not make it vanish and reappear.
//
// Assertions read `data-doing` / `data-mood` / `data-watching` on the layer
// rather than the sprite's background image. The image pins the PACK — rename
// a sheet, give a clip a second take, and every assertion breaks with nothing
// wrong. The attributes name the behaviour, which is what the feature owes.

const LAYER = '.pet-layer'
const SPRITE = '.pet-sprite'
const BLOCK = '.pane.active .cmd-block'

/** A command that takes seconds using shell builtins only — the stand's PATH
 *  is not guaranteed to carry coreutils, and `sleep` is not present on every
 *  machine this suite runs on. */
const SLOW = 'for ((i=0;i<20000000;i++)); do :; done'

async function run(page: import('./harness').Page, command: string): Promise<void> {
  await page.keyboard.type(command)
  await page.keyboard.press('Enter')
}

test('a pet arrives and stands on a finished command block', async ({ page }) => {
  await page.goto('/')
  await promptReady(page)

  await expect(page.locator(LAYER)).toBeAttached()
  await run(page, 'echo hello')
  await expect(page.locator(BLOCK).first()).toBeVisible()

  // It falls in from above and comes to rest. Waiting on the state, never on
  // a duration: the animal is on the ground when it stops falling.
  await expect
    .poll(async () => (await page.locator(LAYER).getAttribute('data-doing')) ?? '', {
      timeout: 15_000,
    })
    .not.toContain('fall')

  // Its feet are on something the terminal drew, not floating in the middle
  // of the pane. Every ledge is the top edge of a block or of a chip the
  // block wears.
  const feet = await page.locator(SPRITE).evaluate((el) => el.getBoundingClientRect().bottom)
  const edges = await page
    .locator(`${BLOCK}, .pane.active .nocx-chip, .pane.active .scrollback-area`)
    .evaluateAll((els) => els.map((e) => e.getBoundingClientRect()).map((r) => [r.top, r.bottom]))
  const standsOnSomething = edges.some(
    ([top, bottom]) => Math.abs(feet - top) < 2 || Math.abs(feet - bottom) < 2,
  )
  expect(standsOnSomething).toBe(true)
})

test('a command that succeeds is answered, and one that fails is answered differently', async ({
  page,
}) => {
  await page.goto('/')
  await promptReady(page)

  await run(page, 'echo hello')
  await expect
    .poll(async () => (await page.locator(LAYER).getAttribute('data-doing')) ?? '', {
      timeout: 20_000,
    })
    .toContain('meow')
  await expect(page.locator(LAYER)).toHaveAttribute('data-mood', 'pleased')

  // `false` rather than `exit 3`: exiting is the SHELL leaving, which ends the
  // session and freezes no block at all.
  await run(page, 'false')
  await expect
    .poll(async () => (await page.locator(LAYER).getAttribute('data-mood')) ?? '', {
      timeout: 20_000,
    })
    .toBe('worried')
})

test('while a command is running the pet settles down and watches it', async ({ page }) => {
  await page.goto('/')
  await promptReady(page)

  await run(page, SLOW)

  // Watching is the state, and it lasts as long as the command does.
  await expect
    .poll(async () => (await page.locator(LAYER).getAttribute('data-watching')) ?? '', {
      timeout: 20_000,
    })
    .toBe('shell')

  // And it does not wander off or doze while it waits: the watching menu has
  // no running and no sleeping in it, whatever the mood.
  for (let i = 0; i < 12; i++) {
    const doing = (await page.locator(LAYER).getAttribute('data-doing')) ?? ''
    if ((await page.locator(LAYER).getAttribute('data-watching')) !== 'shell') break
    expect(doing).not.toContain('run')
    expect(doing).not.toContain('sleep')
    await page.waitForTimeout(250)
  }
})

test('switching pets off in Settings takes the animal away, and back on returns it', async ({
  page,
}) => {
  await page.goto('/')
  await promptReady(page)
  await expect(page.locator(LAYER)).toBeAttached()

  await page.locator('button[data-action="settings"]').click()
  await settingsReady(page)
  // The rail renders one section at a time, so the row does not exist until
  // its section is chosen.
  await page.getByText('Pets', { exact: false }).first().click()
  const toggle = page.locator('[data-key="pets.enabled"] input[type="checkbox"]')
  await expect(toggle).toBeChecked()

  await toggle.uncheck()
  // Off means gone from the document, not merely hidden: a decoration
  // somebody declined should not go on running behind a display:none.
  await expect(page.locator('.pet-layer')).toHaveCount(0)

  await toggle.check()
  await expect(page.locator('.pet-layer').first()).toBeAttached()
})

test('the pet belongs to the window, not to a tab', async ({ page }) => {
  await page.goto('/')
  await promptReady(page)
  await expect(page.locator(LAYER)).toHaveCount(1)

  // It hangs from the application shell, above the panes, so a tab that goes
  // away cannot take it with it.
  await expect(page.locator('#app > .pet-layer')).toBeAttached()

  await page.keyboard.press('Control+t')
  await expect(page.locator('.nocx-tab')).toHaveCount(2)
  await promptReady(page)

  // Still exactly one, still the window's.
  await expect(page.locator(LAYER)).toHaveCount(1)
  await expect(page.locator(SPRITE)).toBeAttached()
})

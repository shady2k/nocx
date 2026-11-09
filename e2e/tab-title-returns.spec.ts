import { promptReady, test, expect } from './harness'

// nocx-ouru5.1.1: a tab kept the title a program set after the program exited.
//
// OSC 0/2 names the tab, and the runtime keeps the latest title as state. What
// it did not keep was WHOSE title it was: omp announced 'π > tmp' on the way
// in and the tab still read that after omp had quit, beside tabs that read
// their directory. The title belongs to the program's run, so when the command
// ends and the prompt is back the tab returns to what the shell shows — and a
// title the SHELL draws at its prompt is not the program's, so it stays.
//
// Both halves are watched through the strip's own title, which is what a
// person reads: the first case holds the program running long enough for the
// title to be seen while it is true, then watches it go; the second sets the
// shell's own title the way a shell does — from its prompt command — and then
// runs a command that must not take it away.

const TAB_TITLE = '.nocx-tab .nocx-tab-title'

test('a title a program sets goes back to the directory when the command ends', async ({
  page,
}) => {
  await page.goto('/')
  await expect(page.locator('.nocx-tab')).toHaveCount(1, { timeout: 15_000 })
  await promptReady(page)

  const title = page.locator(TAB_TITLE).first()
  // Home is the tab's directory title, and '~' is the abbreviation only a
  // known home produces — so "the directory title came back" is a claim about
  // the derived label rather than about a string the shell echoed.
  await expect(title).toHaveText('~', { timeout: 15_000 })

  // A program that names itself and then keeps running: the title is watched
  // WHILE it is true, and only then watched to go.
  await page.keyboard.type("printf '\\033]0;PROGRAM-TITLE\\007'; sleep 4")
  await page.keyboard.press('Enter')
  await expect(title).toHaveText('PROGRAM-TITLE', { timeout: 20_000 })

  // The command ends, the prompt returns, and the tab is its directory again.
  await expect(title).toHaveText('~', { timeout: 25_000 })
})

test('a title the shell draws at its prompt stays across a command', async ({ page }) => {
  await page.goto('/')
  await expect(page.locator('.nocx-tab')).toHaveCount(1, { timeout: 15_000 })
  await promptReady(page)

  const title = page.locator(TAB_TITLE).first()
  await expect(title).toHaveText('~', { timeout: 15_000 })

  // THE SHELL'S OWN TITLE, in the shape a shell actually sets one: its prompt
  // command writes OSC 0 on every prompt, outside any command's own output.
  // nocx's integration chains the user's prompt command rather than replacing
  // it, so the wrapper is kept in front and the title is drawn after it.
  await page.keyboard.type(
    'PROMPT_COMMAND=\'__nocx_prompt_command; printf "\\033]0;SHELL-TITLE\\007"\'',
  )
  await page.keyboard.press('Enter')
  await expect(title).toHaveText('SHELL-TITLE', { timeout: 20_000 })

  // And a command does not take it away: no program set a title of its own, so
  // the fence that closes the run leaves the shell's title alone.
  await page.keyboard.type('echo A-COMMAND-WITH-NO-TITLE')
  await page.keyboard.press('Enter')
  await expect(
    page.locator('.pane.active .cmd-block', { hasText: 'A-COMMAND-WITH-NO-TITLE' }),
  ).toBeVisible({ timeout: 20_000 })
  await expect(title).toHaveText('SHELL-TITLE', { timeout: 20_000 })
})

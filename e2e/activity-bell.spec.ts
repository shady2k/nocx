import { test, expect } from './harness'

const TAB = '.tab'
const ACTIVITY = '.tab-indicator.tab-activity'

// A full-screen TUI repaints constantly in the alternate buffer, and those
// repaints deliberately do not light the indicator (nocx-5mf). A bell is the
// program explicitly asking for attention, so it must light it even there —
// that is the whole escape hatch, and it is what tells you Claude Code wants
// you back. If this fails, a background agent is silent and the feature is
// useless in the case it was built for.
test('a bell lights the indicator from inside the alternate buffer', async ({ page }) => {
  await page.goto('/')
  await expect(page.locator(TAB)).toHaveCount(1)

  // Enter the alternate screen, wait, ring the bell, then block forever.
  // The shell `sleep 3` is a genuine ordering constraint that cannot be
  // expressed as a Playwright condition: the bell must fire AFTER the tab
  // is backgrounded, but the bell is part of the same shell command and
  // keystrokes route to the active tab.  `cat` replaces the original
  // `sleep 30` — it blocks indefinitely instead of for a fixed duration,
  // so the test never races a deadline.
  await page.keyboard.type("printf '\\033[?1049h'; sleep 3; printf '\\a'; cat")
  await page.keyboard.press('Enter')

  // Wait for alt-screen to be active before backgrounding (replaces
  // waitForTimeout(2000)).
  await expect(page.locator('#app')).toHaveClass(/alt-screen/, {
    timeout: 5000,
  })

  // Use keyboard shortcut — the .tab-add button is hidden in alt-screen
  // mode (CSS: #app.alt-screen .tab-add { display: none }).
  await page.keyboard.press('Meta+t')
  await expect(page.locator(TAB)).toHaveCount(2)
  await expect(page.locator(TAB).first()).not.toHaveClass(/active/)

  // Wait for the activity indicator — replaces waitForTimeout(6000).
  // Playwright's expect polls every ~100ms until found.
  await expect(page.locator(TAB).first().locator(ACTIVITY)).toBeAttached({ timeout: 10000 })
})

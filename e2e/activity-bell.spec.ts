import { test, expect } from './harness'

const ACTIVITY = '.nocx-tab-indicator[data-activity="true"]'

// A full-screen TUI repaints constantly in the alternate buffer, and those
// repaints deliberately do not light the indicator (nocx-5mf). A bell is the
// program explicitly asking for attention, so it must light it even there —
// that is the whole escape hatch, and it is what tells you Claude Code wants
// you back. If this fails, a background agent is silent and the feature is
// useless in the case it was built for.
test('a bell lights the indicator from inside the alternate buffer', async ({ page }) => {
  await page.goto('/')
  await expect(page.getByRole('tab')).toHaveCount(1)

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
  //
  // Asked of the PANE, not of `#app`. `#app.alt-screen` is gone: it existed to
  // empty the window chrome so a viewport-sized fullscreen xterm would not
  // paint through it, and nocx-6w4z moved the fullscreen region inside the pane
  // precisely so the chrome could stay. The class went with it, and this wait
  // silently became a 5s timeout on a class nothing sets any more (nocx-42lb).
  // `live-fullscreen` is what `enterFullscreen()` writes, on the alt-screen
  // path, so it states the same condition against the code that survived.
  await expect(page.locator('.pane.active .xterm-live-container')).toHaveClass(/live-fullscreen/, {
    timeout: 5000,
  })

  await page.keyboard.press('Meta+t')
  await expect(page.getByRole('tab')).toHaveCount(2)
  await expect(page.getByRole('tab').first()).toHaveAttribute('aria-selected', 'false')

  // Wait for the activity indicator — replaces waitForTimeout(6000).
  // Playwright's expect polls every ~100ms until found.
  await expect(page.getByRole('tab').first().locator(ACTIVITY)).toBeAttached({ timeout: 10000 })
})

import { test, expect, promptReady } from './harness'

// Sidebar e2e — activity bar zones, toolbar keyboard, panel behaviour.
// These tests run against the full app so layout, focus and ARIA are real.

const TOOLBAR = '[role="toolbar"]'
// A native <button> carries the button role implicitly, so there is no
// role="button" to select on: the activity bar's zones render IconButtons, and
// the kit's no-role-impersonation rule is what forbids the attribute these
// selectors used to name (nocx-pp3y.1).
const VIEW_BTN = 'button[data-view]'
const ACTION_BTN = 'button[data-action]'
const VIEWS_GROUP = '[role="group"][aria-label="Views"]'
const TAB_ACTIONS_GROUP = '[role="group"][aria-label="Active tab actions"]'
const ACTIONS_GROUP = '[role="group"][aria-label="Actions"]'

// These three tests used to assert the REGISTRY — "exactly one action, zero
// views" — which was true on the day they were written and stopped being true
// the day Ports became a view in the bar (b26bb62, nocx-wzc4.7). Nothing was
// wrong with the product; the tests had pinned a census instead of a rule, so
// shipping a feature turned them red. What follows asserts the rules the
// sidebar actually owes a user, each of which survives the next view being
// registered.

test('the activity bar separates view navigation, active-tab actions, and global actions', async ({
  page,
}) => {
  await page.goto('/')

  // The activity bar toolbar exists
  await expect(page.locator(TOOLBAR)).toBeAttached()

  // All three semantic groups exist.
  await expect(page.locator(VIEWS_GROUP)).toBeAttached()
  await expect(page.locator(TAB_ACTIONS_GROUP)).toBeAttached()
  await expect(page.locator(ACTIONS_GROUP)).toBeAttached()

  const viewsInViewGroup = page.locator(`${VIEWS_GROUP} button`)
  await expect(viewsInViewGroup).toHaveCount(await page.locator(VIEW_BTN).count())

  const tabActions = page.locator(`${TAB_ACTIONS_GROUP} button[data-action]`)
  const globalActions = page.locator(`${ACTIONS_GROUP} button[data-action]`)
  await expect(tabActions).toHaveCount(1)
  await expect(globalActions).toHaveCount(1)
  expect((await tabActions.count()) + (await globalActions.count())).toBe(
    await page.locator(ACTION_BTN).count(),
  )
  await expect(
    page.locator(`${TAB_ACTIONS_GROUP} button[data-testid="sandbox-shield"]`),
  ).toHaveCount(1)
  await expect(page.locator(`${ACTIONS_GROUP} button[data-action="settings"]`)).toBeAttached()
})

test('the panel is collapsed exactly when no view is active', async ({ page }) => {
  await page.goto('/')
  // The app takes focus into the editor as it comes up, and it does so AFTER
  // the toolbar is in the DOM. Driving the bar before that races the startup
  // and loses: the click lands, the app then moves focus, and the assertion
  // reads a half-mounted page. promptReady is the app saying it is done.
  await promptReady(page)

  const panel = page.locator('#sidebar')
  const firstView = page.locator(VIEW_BTN).first()

  // The rule (sidebar.tsx: `collapsed || !activeDesc()`): the panel is
  // collapsed when the store says so OR when nothing is selected to show.
  // Whether it STARTS collapsed therefore depends on what is registered, which
  // is exactly what a test must not hard-code — so drive the transition
  // instead, which is the part a user can feel.
  if ((await firstView.count()) === 0) {
    await expect(panel).toHaveClass(/collapsed/)
    return
  }

  // With a view registered and active, the panel shows it.
  await expect(panel).not.toHaveClass(/collapsed/)

  // Activating the active view collapses the panel; activating it again
  // restores it. The button is a toggle, and the class follows it.
  await firstView.click()
  await expect(panel).toHaveClass(/collapsed/)
  await firstView.click()
  await expect(panel).not.toHaveClass(/collapsed/)
})

test('the settings gear is keyboard-reachable', async ({ page }) => {
  await page.goto('/')
  await promptReady(page)

  const gear = page.locator(`${ACTION_BTN}[data-action="settings"]`)
  await expect(gear).toBeAttached()

  // The bar is one Tab stop with a roving tabindex, so the gear is reached by
  // ARROWING to it, not by carrying tabindex=0 itself — which it only did back
  // when it was the sole button. Tab into the bar, then walk.
  await page.locator(`${TOOLBAR} button[tabindex="0"]`).focus()
  const total = await page.locator(`${TOOLBAR} button`).count()
  for (let i = 0; i < total; i++) {
    if (await gear.evaluate((el) => el === document.activeElement)) break
    await page.keyboard.press('ArrowDown')
  }
  await expect(gear).toBeFocused()

  // And having arrived, it is the one that is tabbable — the roving index
  // followed the focus rather than staying where it started.
  await expect(gear).toHaveAttribute('tabindex', '0')
})

test('the toolbar has aria-label="Activity bar"', async ({ page }) => {
  await page.goto('/')
  await expect(page.locator(TOOLBAR)).toHaveAttribute('aria-label', 'Activity bar')
})

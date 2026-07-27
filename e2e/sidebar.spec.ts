import { test, expect } from './harness'

// Sidebar e2e — activity bar zones, toolbar keyboard, panel behaviour.
// These tests run against the full app so layout, focus and ARIA are real.

const TOOLBAR = '[role="toolbar"]'
const VIEW_BTN = '[role="button"][data-view]'
const ACTION_BTN = '[role="button"][data-action]'
const VIEWS_GROUP = '[role="group"][aria-label="Views"]'
const ACTIONS_GROUP = '[role="group"][aria-label="Actions"]'

test('the activity bar renders as a toolbar with views and actions groups', async ({ page }) => {
  await page.goto('/')

  // The activity bar toolbar exists
  await expect(page.locator(TOOLBAR)).toBeAttached()

  // Both zone groups exist (even if empty)
  await expect(page.locator(VIEWS_GROUP)).toBeAttached()
  await expect(page.locator(ACTIONS_GROUP)).toBeAttached()

  // In production (the state this bead ships), only the Settings gear action
  // is registered — no view buttons.
  await expect(page.locator(ACTION_BTN)).toHaveCount(1)
  await expect(page.locator(VIEW_BTN)).toHaveCount(0)
})

test('the panel starts collapsed when no views are registered', async ({ page }) => {
  await page.goto('/')

  // #sidebar should have the collapsed class because there are no views.
  const panel = page.locator('#sidebar')
  await expect(panel).toHaveClass(/collapsed/)
})

test('the settings gear is keyboard-reachable', async ({ page }) => {
  await page.goto('/')

  // The gear button in the actions zone should be the sole focusable button
  // (tabindex="0") since there are no views.
  const gear = page.locator(`${ACTION_BTN}[data-action="settings"]`)
  await expect(gear).toBeAttached()
  await expect(gear).toHaveAttribute('tabindex', '0')
})

test('the toolbar has aria-label="Activity bar"', async ({ page }) => {
  await page.goto('/')
  await expect(page.locator(TOOLBAR)).toHaveAttribute('aria-label', 'Activity bar')
})

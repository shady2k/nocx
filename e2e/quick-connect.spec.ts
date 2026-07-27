import { test, expect } from './harness'

const CARET = '.tab-caret'
const QUICK_CONNECT_LIST = '.quick-connect__list'
const QUICK_CONNECT_ITEM = '.quick-connect__item'
const QUICK_CONNECT_SEARCH = '.quick-connect__search input'

test.describe('quick-connect picker', () => {
  test('opens the picker when the caret is clicked', async ({ page }) => {
    await page.goto('/')
    await expect(page.locator('.tab')).toHaveCount(1)

    // Click the caret beside +.
    await page.locator(CARET).click()

    // The picker dialog is open.
    await expect(page.locator(QUICK_CONNECT_LIST)).toBeVisible()
    // "Local shell" is the first item.
    await expect(page.locator(QUICK_CONNECT_ITEM).first()).toContainText('Local shell')
  })

  test('Escape closes the picker and restores focus to the caret', async ({ page }) => {
    await page.goto('/')
    await expect(page.locator('.tab')).toHaveCount(1)

    // Click the caret to open the picker.
    await page.locator(CARET).click()
    await expect(page.locator(QUICK_CONNECT_LIST)).toBeVisible()

    // Press Escape to close.
    await page.keyboard.press('Escape')

    // Picker is closed.
    await expect(page.locator(QUICK_CONNECT_LIST)).not.toBeVisible()

    // Focus returns to the caret (Dialog's overlay stack restores focus).
    await expect(page.locator(CARET)).toBeFocused()
  })

  test('typing filters the list', async ({ page }) => {
    await page.goto('/')
    await expect(page.locator('.tab')).toHaveCount(1)

    await page.locator(CARET).click()
    await expect(page.locator(QUICK_CONNECT_LIST)).toBeVisible()

    // Type something that doesn't match "Local shell".
    await page.locator(QUICK_CONNECT_SEARCH).fill('zzzzz')

    // The list should show "No matches".
    await expect(page.locator('.quick-connect__empty')).toBeVisible()
    await expect(page.locator('.quick-connect__empty')).toContainText('No matches')

    // Clear filter: all items visible again.
    await page.locator(QUICK_CONNECT_SEARCH).fill('')
    await expect(page.locator(QUICK_CONNECT_ITEM).first()).toContainText('Local shell')
  })

  test('Enter on "Local shell" opens a new tab', async ({ page }) => {
    await page.goto('/')
    await expect(page.locator('.tab')).toHaveCount(1)

    await page.locator(CARET).click()
    await expect(page.locator(QUICK_CONNECT_LIST)).toBeVisible()

    // "Local shell" is already selected by default. Press Enter.
    await page.keyboard.press('Enter')

    // A new tab opens.
    await expect(page.locator('.tab')).toHaveCount(2)

    // The picker closes.
    await expect(page.locator(QUICK_CONNECT_LIST)).not.toBeVisible()
  })

  test('keyboard shortcut Ctrl+Shift+P opens the picker', async ({ page }) => {
    await page.goto('/')
    await expect(page.locator('.tab')).toHaveCount(1)

    // Use the keyboard shortcut.
    await page.keyboard.press('Control+Shift+P')

    // The picker is open.
    await expect(page.locator(QUICK_CONNECT_LIST)).toBeVisible()

    // Close with Escape.
    await page.keyboard.press('Escape')
    await expect(page.locator(QUICK_CONNECT_LIST)).not.toBeVisible()
  })

  test('terminal host element persists through picker open/close', async ({ page }) => {
    await page.goto('/')
    await expect(page.locator('.tab')).toHaveCount(1)

    // The terminal host element exists.
    const pane = page.locator('.pane.active')
    await expect(pane).toBeVisible()

    // Open the picker.
    await page.locator(CARET).click()
    await expect(page.locator(QUICK_CONNECT_LIST)).toBeVisible()

    // The terminal host is still in the DOM.
    await expect(pane).toBeVisible()

    // Close the picker.
    await page.keyboard.press('Escape')
    await expect(page.locator(QUICK_CONNECT_LIST)).not.toBeVisible()

    // Terminal host still present.
    await expect(pane).toBeVisible()
  })
})

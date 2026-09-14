import { expect, promptReady, test } from './harness'

/**
 * e2e: A BLOCK'S ACTIONS ARE A KEYBOARD CONTROL (nocx-9bpeq.5, spec 2026-09-14 §3.1).
 *
 * ADR-0008 makes blocks a keyboard-first ledger, so the ⋮ that holds a block's
 * actions has to work without a pointer: it is a real button with a name, Enter
 * opens the kit's menu with the first row focused, the arrow keys walk it, Escape
 * closes it and gives focus back to the button.
 *
 * How focus ARRIVES on the button is block navigation's job (nocx-4ff.5) and is
 * not what this proves, so the button is focused programmatically — every step
 * after that is a key.
 */

const BLOCK = '.pane.active .cmd-block:not(.cmd-block-running)'
const ACTIONS = '[data-block-actions]'
const MENU = '[data-testid="block-actions-menu"]'
const ITEM = `${MENU} .ui-context-menu__item`

async function runCommand(page: import('./harness').Page, text: string): Promise<void> {
  await page.keyboard.type(text)
  await page.keyboard.press('Enter')
}

test.describe('block actions from the keyboard', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/')
    await promptReady(page)
  })

  test('Enter opens the menu, arrows walk it, Escape closes it and returns focus', async ({
    page,
  }) => {
    await runCommand(page, 'echo block-actions-keyboard')
    const block = page.locator(BLOCK).filter({ hasText: 'echo block-actions-keyboard' }).last()
    await expect(block).toBeVisible({ timeout: 15_000 })

    const actions = block.locator(ACTIONS)
    await expect(actions).toHaveAttribute('aria-label', 'Block actions')
    await actions.focus()
    await expect(actions).toBeFocused()

    await page.keyboard.press('Enter')
    const items = page.locator(ITEM)
    await expect(page.locator(MENU)).toBeVisible()
    await expect(items.first()).toBeFocused()
    await expect(items.first()).toHaveAttribute('data-item-id', 'copy-command')

    await page.keyboard.press('ArrowDown')
    await expect(items.nth(1)).toBeFocused()
    await page.keyboard.press('End')
    await expect(items.last()).toHaveAttribute('data-item-id', 'wrap')
    await expect(items.last()).toBeFocused()

    await page.keyboard.press('Escape')
    await expect(page.locator(MENU)).toHaveCount(0)
    await expect(actions).toBeFocused()

    // Every row carries its mark (check-menu-icons' rule, seen in a browser).
    await page.keyboard.press('Enter')
    const marks = await page.$$eval(`${ITEM} .ui-context-menu__icon svg`, (svgs) => svgs.length)
    expect(marks).toBe(await items.count())
    await page.keyboard.press('Escape')
  })

  test('opened from the bottom of a short viewport, the whole menu stays inside it', async ({
    page,
  }) => {
    await page.setViewportSize({ width: 900, height: 420 })
    for (let i = 0; i < 6; i++) await runCommand(page, `echo filler-${i}`)
    await runCommand(page, 'echo bottom-block')
    const block = page.locator(BLOCK).filter({ hasText: 'echo bottom-block' }).last()
    await expect(block).toBeVisible({ timeout: 15_000 })

    const actions = block.locator(ACTIONS)
    await actions.focus()
    await page.keyboard.press('Enter')
    const menu = page.locator(MENU)
    await expect(menu).toBeVisible()

    const box = (await menu.boundingBox())!
    const viewport = page.viewportSize()!
    expect(box.x).toBeGreaterThanOrEqual(8)
    expect(box.y).toBeGreaterThanOrEqual(8)
    expect(box.x + box.width).toBeLessThanOrEqual(viewport.width - 8)
    expect(box.y + box.height).toBeLessThanOrEqual(viewport.height - 8)
    await page.keyboard.press('Escape')
  })
})

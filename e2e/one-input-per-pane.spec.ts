// e2e: one input per pane (nocx-7jd59).
//
// The pane has two real textareas: xterm's helper textarea, which carries the
// accessible name 'Terminal input' and no keystroke path any more, and the
// pane's own IME anchor (ui/terminal-input-element.ts), which takes the
// keyboard now. The anchor used to carry that same name AND role, so a pane
// answered a screen reader with two identically named textboxes and nothing
// said which one the person's typing reaches.
//
// THE CONTRACT ASSERTED IS THE NAME, not the node: exactly one textbox in the
// active pane answers to 'Terminal input', read while the anchor holds the
// keyboard and while it does not. Chromium's own tree is read as well, because
// Playwright's ARIA model is not the engine: the model drops the focused
// anchor, Chromium does not — the element is ignored while it is unfocused and
// exposed, with NO name, while it holds focus. That limit is the IME's, not a
// free choice: an IME measures the caret rect of a focused editable, so the
// anchor must stay focusable, and no attribute hides a focused element from
// Chromium. The element's own comment carries the decision and the reading it
// rejects.
import { test, expect, promptReady } from './harness'
import type { Page } from '@playwright/test'

/** The textboxes CHROMIUM's own accessibility tree holds for this page.
 *  Engine-native on purpose: `getByRole` is Playwright's ARIA model, which
 *  hides a focused `aria-hidden` element that Chromium still reports. */
async function engineTextboxes(page: Page): Promise<Array<{ name: string; ignored: boolean }>> {
  const cdp = await page.context().newCDPSession(page)
  const { nodes } = (await cdp.send('Accessibility.getFullAXTree')) as {
    nodes: Array<{ ignored?: boolean; role?: { value?: string }; name?: { value?: string } }>
  }
  return nodes
    .filter((node) => (node.role?.value ?? '') === 'textbox')
    .map((node) => ({ name: node.name?.value ?? '', ignored: node.ignored === true }))
}

test('the pane names one input, with the anchor holding the keyboard and without', async ({
  page,
  browserName,
}) => {
  await page.goto('/')
  await expect(page.locator('.nocx-tab')).toHaveCount(1)
  await promptReady(page)

  const pane = page.locator('.pane.active')
  const named = pane.getByRole('textbox', { name: 'Terminal input' })

  // The pane's own input element exists, and the name belongs to one textbox.
  await expect(pane.locator('.ui-terminal-input')).toHaveCount(1)
  await expect(named).toHaveCount(1)

  // Submitting hands the keyboard to the grid — the anchor is what the product
  // focuses for that (takeKeyboardToGrid) — and a command that waits holds the
  // state long enough to read it.
  await page.keyboard.type('sleep 12')
  await page.keyboard.press('Enter')
  await expect
    .poll(async () => page.evaluate(() => document.activeElement?.className ?? ''), {
      timeout: 15_000,
    })
    .toContain('ui-terminal-input')
  await expect(named).toHaveCount(1)

  if (browserName === 'chromium') {
    // The engine's own answer, in the state that matters: the anchor may still
    // be listed (it holds focus), and it must carry no name.
    const namedInEngine = (await engineTextboxes(page)).filter(
      (box) => box.name === 'Terminal input',
    )
    expect(namedInEngine).toHaveLength(1)
  }
})

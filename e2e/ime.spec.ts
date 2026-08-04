import { test, expect, promptReady } from './harness'

// IME composition (ADR-0010 W1 check 3): while a composition is in progress,
// the key stream belongs to the IME, and the editor's capture-phase keydown
// guard (editor.ts: `e.isComposing || e.keyCode === 229` returns early) must
// not interpret a composing Enter as submit or a composing Ctrl-C as cancel —
// either would destroy the composition and the composed text with it.
//
// End-to-end proof: a composition sequence lands the composed text in the
// document exactly once, Enter while composing does not submit, and the final
// Enter executes the command containing the composed text exactly once.
//
// Chromium headless has no IME, so the sequence is driven synthetically the
// way Chromium fires it at a contenteditable: compositionstart, the composed
// text appears in the DOM, compositionupdate + an input event carrying
// inputType 'insertCompositionText' with isComposing set, then
// compositionend. CM6 reads the DOM and applies the composition as one
// 'input.type.compose' transaction — verified against the live app before
// this spec was written.

const INPUT = '.pane.active .nocx-editor-input'

// The composed text is a real shell command whose output contains the marker,
// so "exactly once" is observable both in the editor document and in the
// executed command's block.
const MARKER = 'こんにちは'
const COMPOSED = `echo ${MARKER}`

test('a composition produces its text exactly once and survives Enter while composing', async ({
  page,
}) => {
  await page.goto('/')
  await expect(page.getByRole('tab')).toHaveCount(1)
  await promptReady(page)

  // Start the composition but do not end it yet.
  await page.locator(INPUT).evaluate(async (input, composedText) => {
    const el = input as HTMLElement
    const line = el.querySelector('.cm-line') as HTMLElement | null
    if (!line) throw new Error('no .cm-line in the editor surface')
    el.dispatchEvent(new CompositionEvent('compositionstart', { data: '', bubbles: true }))
    line.textContent = composedText
    el.dispatchEvent(
      new CompositionEvent('compositionupdate', { data: composedText, bubbles: true }),
    )
    el.dispatchEvent(
      new InputEvent('input', {
        inputType: 'insertCompositionText',
        data: composedText,
        isComposing: true,
        bubbles: true,
      }),
    )
    const { promise, resolve } = Promise.withResolvers<void>()
    setTimeout(resolve, 20)
    await promise
  }, COMPOSED)

  // Enter while composing: a real IME emits keyCode 229 with isComposing set.
  // The guard must let it through without submitting or clearing the draft.
  await page.locator(INPUT).evaluate((input) => {
    const el = input as HTMLElement
    el.dispatchEvent(
      new KeyboardEvent('keydown', {
        key: 'Enter',
        code: 'Enter',
        keyCode: 229,
        which: 229,
        bubbles: true,
        cancelable: true,
        isComposing: true,
      }),
    )
  })

  // The editor is still on screen with the draft intact — no submit happened.
  await expect(page.locator('.pane.active .nocx-editor')).toBeVisible()
  await expect(page.locator(INPUT)).toHaveText(COMPOSED, { timeout: 5000 })

  await page.locator(INPUT).evaluate((input, composedText) => {
    const el = input as HTMLElement
    el.dispatchEvent(new CompositionEvent('compositionend', { data: composedText, bubbles: true }))
  }, COMPOSED)

  // The composed text is in the document exactly once — not duplicated by the
  // composition handoff, and not cleared by the composing Enter.
  await expect
    .poll(() => page.locator(INPUT).evaluate((el) => el.textContent), { timeout: 5000 })
    .toBe(COMPOSED)

  // Submit for real. The command must execute with the composed text exactly
  // once: a submit that fired during composition would have produced a second
  // block (or none).
  await page.keyboard.press('Enter')
  await expect(page.locator('.cmd-block').filter({ hasText: MARKER })).toHaveCount(1, {
    timeout: 8000,
  })
  const blockText = await page.locator('.cmd-block').filter({ hasText: MARKER }).textContent()
  expect(blockText).toContain(MARKER)
})

/**
 * The ask/grant GESTURE, in one place (nocx-hp8p2.2).
 *
 * WHY THIS MODULE EXISTS. Four specs drive the same conversation with the
 * assistant — agent-ask.spec.ts, agent-turn.spec.ts, agent-refusal-stop.spec.ts
 * and agent-answer-stream.spec.ts — and each carries its own copy of the same
 * six or seven helpers: flip the input target with the real chord, select a
 * block's output, press the offer, read the grant chip, send the line, read the
 * model's system prompt back off the fake. The copies agree today. A second
 * implementation of one concept is a regression with a delay fuse (AGENTS.md,
 * "Look for the existing answer before you write a second one"), and the epic
 * gate this module was extracted for would have been the fifth copy.
 *
 * So this is the home. The four specs above have NOT been migrated onto it,
 * deliberately: they are green, large, and the brief for nocx-hp8p2.2 scopes
 * the verification budget to one new spec — a mechanical edit to four files
 * nobody may run is a worse trade than one tracked follow-up. nocx-hp8p2.3
 * carries that migration.
 *
 * WHAT IS EXPORTED IS WHAT A CALLER USES TODAY, and nothing is exported for
 * the migration that has not happened yet. An export nobody imports is dead
 * code the ratchet cannot see (AGENTS.md), and this module's whole reason for
 * existing is that a second copy of a concept agrees with the first everywhere
 * you look. nocx-hp8p2.3 widens the surface when it needs it; the selection,
 * the offer and the chip's own reads are internal until then.
 *
 * EVERY WAIT HERE OBSERVES A STATE, NEVER A DURATION (AGENTS.md: "a test may
 * not depend on timing"). Where a wait is subtle, the comment says what state
 * it actually observes — because a wait nobody understood is how three
 * assistant-intake cases waited on a chip their block never had and passed
 * whether or not the feature worked (44b0dfc9).
 */
import { expect, type Locator, type Page } from '@playwright/test'
import type { FakeOpenAI, FakeRequest } from './fake-openai'

/** The ordinary editor — the one input surface (design §3.1). */
export const INPUT = '.pane.active .nocx-editor-input'

/** WHAT A SELECTION LEAVES BEHIND (nocx-wcswn, nocx-a7mw7.4). Pressing the
 *  offer marks the WHOLE containing block: the block is marked where it
 *  stands (`data-granted`) and the input line's chip COUNTS the marks. The
 *  chip is deliberately not a second list of command names — the names live
 *  in its popover, and the block itself is where a person reads them. */
const GRANT_CHIP = '.pane.active .nocx-editor-grant'
const GRANTED = '.pane.active .cmd-block[data-granted="true"]'

/** `:visible` on purpose: CM6 keeps a hidden measurement spacer beside the
 *  real marker, carrying an identical button. The visible one is the
 *  person's. */
const MODE_INDICATOR = '.pane.active .ui-mode-indicator:visible'

/** The blocks a question would carry, by their own header text — read off the
 *  marks, in the order the flow holds them. */
async function grantedCommands(page: Page): Promise<string[]> {
  return page
    .locator(GRANTED)
    .evaluateAll((els) =>
      els.map((el) => el.querySelector('.cmd-header-text')?.textContent?.trim() ?? ''),
    )
}

/**
 * The gesture landed: exactly these blocks are marked, and the chip says how
 * many. The count is asserted on the CHIP because that is the surface a person
 * reads before pressing Enter, and the identity on the BLOCKS because that is
 * the only place a marked block is named.
 *
 * `what` names the promise this call is standing for, so a red run says which
 * one broke without opening a trace.
 */
export async function expectGranted(
  page: Page,
  commands: string[],
  what = 'the marked blocks',
): Promise<void> {
  await expect(page.locator(GRANTED), `${what}: wrong number of blocks marked`).toHaveCount(
    commands.length,
    { timeout: 10_000 },
  )
  await expect(
    page.locator(GRANT_CHIP),
    `${what}: the grant chip is not in its chosen state`,
  ).toHaveAttribute('data-state', commands.length === 0 ? 'default' : 'chosen', {
    timeout: 10_000,
  })
  await expect(
    page.locator(GRANT_CHIP),
    `${what}: the grant chip does not count ${commands.length}`,
  ).toContainText(`· ${commands.length}`)
  expect(await grantedCommands(page), `${what}: the wrong blocks are marked`).toEqual(commands)
}

/**
 * The selection a mouse drag leaves behind, made as a real DOM Range over the
 * block's output rows and announced with the event the product listens for. A
 * synthetic drag across rows would be a geometry test in disguise; what these
 * specs are about is what a selection MEANS.
 */
async function selectWholeOutput(block: Locator): Promise<void> {
  await block.evaluate((el) => {
    const lines = Array.from(el.querySelectorAll<HTMLElement>('.cmd-output .term-line'))
    if (lines.length === 0) throw new Error('block has no output rows to point at')
    const first = lines[0]
    const last = lines[lines.length - 1]
    const range = document.createRange()
    range.setStart(first.firstChild ?? first, 0)
    range.setEnd(last.lastChild ?? last, (last.textContent ?? '').length)
    const sel = window.getSelection()
    sel?.removeAllRanges()
    sel?.addRange(range)
    document.dispatchEvent(new Event('selectionchange'))
  })
}

/**
 * THE OFFER MUST OUTLIVE THE COMPOSER, NOT OUTRUN IT (nocx-45vkz).
 *
 * CodeMirror restores the DOM selection into its own document while its
 * contentDOM is the active element, and a selection made without taking focus
 * off it was collapsed a frame or two later — the offer vanished, and a
 * helper that clicked within a few milliseconds passed by luck. The scrollback
 * TAKES the focus when it claims the selection, so the observable to wait on is
 * that transfer: with the composer unfocused there is nothing left to restore.
 *
 * What the two waits actually observe, in order:
 *   1. `document.activeElement` is no longer the composer — the transfer
 *      happened, so no restore is pending;
 *   2. two animation frames — the moment the restore WOULD have run — and the
 *      offer is still on screen to press.
 * Frames and a DOM state, never a duration.
 */
async function confirmOffer(page: Page): Promise<void> {
  const offer = page.locator('.mark-affordance .ui-button')
  await expect(offer, 'the selection raised no offer to mark the block').toBeVisible({
    timeout: 10_000,
  })
  await expect
    .poll(() => page.evaluate(() => String(document.activeElement?.className ?? '')), {
      timeout: 10_000,
      message: 'the scrollback never took focus off the composer, so the offer is still racing it',
    })
    .not.toContain('nocx-editor-input')
  await page.evaluate(
    () =>
      new Promise<void>((done) => requestAnimationFrame(() => requestAnimationFrame(() => done()))),
  )
  await expect(
    offer,
    'the offer was collapsed by the composer before it could be pressed',
  ).toBeVisible()
  await offer.click()
}

/**
 * The real cross-platform target chord, as a person presses it. Idempotent, and
 * the confirmation is the INDICATOR — the chord sends nothing, it only changes
 * where Enter goes.
 */
export async function switchToAsk(page: Page): Promise<void> {
  const indicator = page.locator(MODE_INDICATOR)
  if ((await indicator.getAttribute('data-target')) === 'agent') return
  await page.locator(INPUT).click()
  await page.keyboard.press('ControlOrMeta+Enter')
  await expect(
    indicator,
    'the target chord did not put the editor on the assistant',
  ).toHaveAttribute('data-target', 'agent', { timeout: 10_000 })
}

/**
 * MARK ONE BLOCK, as a person does it (nocx-4wtlh, nocx-wcswn, nocx-a7mw7.4):
 * with Ask active, select a region of a finished block's output and the product
 * OFFERS to mark it; pressing the offer marks the WHOLE block for the next
 * question — "if you ask, this comes with you".
 *
 * THE SELECTION ALONE MARKS NOTHING. Selecting output to read it, or to copy
 * it, used to mark it with nothing confirmed; pressing the offer is the second
 * half of the gesture and a helper that skipped it would go on passing after
 * the offer stopped being offered.
 */
export async function markBlock(block: Locator): Promise<void> {
  const page = block.page()
  await expect(
    block.locator('.cmd-output .term-line').first(),
    'the block to mark has no output rows yet',
  ).toBeVisible({ timeout: 15_000 })
  await expect(
    page.locator(MODE_INDICATOR),
    'the offer only exists with Ask active (nocx-a7mw7.5)',
  ).toHaveAttribute('data-target', 'agent')
  // The state a person points FROM: mid-draft, so the composer holds the
  // focus. Put there deliberately rather than left to whatever the previous
  // step happened to focus, because it is the whole difficulty — see
  // confirmOffer.
  await page.locator(INPUT).click()
  await selectWholeOutput(block)
  await confirmOffer(page)
}

/**
 * Send the drafted line to the ASSISTANT: ⌘/Ctrl+Enter flips where Enter goes
 * (it sends nothing — the indicator is the confirmation), then Enter is the one
 * send key.
 */
export async function askFromPrompt(page: Page, question: string): Promise<void> {
  const input = page.locator(INPUT)
  await input.click()
  await switchToAsk(page)
  await input.fill(question)
  await page.keyboard.press('Enter')
}

/** Run one command through the shell and wait for its FROZEN block.
 *
 *  The wait is on `:not(.cmd-block-running)` AND on the first output row: a
 *  selection may only be made inside a finished block (a running block's rows
 *  still move), so waiting on the block alone would hand back the running one
 *  — visible, with no output element at all — and the gesture would have
 *  nothing to select. */
export async function runCommand(page: Page, command: string, marker: string): Promise<Locator> {
  const input = page.locator(INPUT)
  await input.fill(command)
  await page.keyboard.press('Enter')
  const block = page
    .locator('.pane.active .cmd-block:not(.cmd-block-running)', { hasText: marker })
    .first()
  await expect(block, `the command \`${command}\` never froze into a block`).toBeVisible({
    timeout: 15_000,
  })
  await expect(
    block.locator('.cmd-output .term-line').first(),
    `the block for \`${command}\` froze with no output rows`,
  ).toBeVisible({ timeout: 15_000 })
  return block
}

/** The system prompt of one chat-completions request, as the backend sent it. */
export function systemPrompt(body: string): string {
  const request = JSON.parse(body) as { messages?: { role?: string; content?: string }[] }
  return (request.messages ?? []).find((message) => message.role === 'system')?.content ?? ''
}

/**
 * The attached item id, read from the model's ACTUAL system prompt. A scripted
 * model must call tools with this id and never with one the spec invented, so a
 * successful call proves the grant survived the frontend-to-ledger join.
 */
export function attachedItemID(body: string): string {
  const match = systemPrompt(body).match(/\n- id: ([^;]+); state: (?:running|exited)/)
  if (!match) throw new Error('model request has no attached terminal item id')
  return match[1]
}

/** The model-facing tool results in one request body, verbatim. */
export function toolResults(body: string): string[] {
  try {
    const parsed = JSON.parse(body) as { messages?: { role?: string; content?: unknown }[] }
    return (parsed.messages ?? [])
      .filter((message) => message.role === 'tool' && typeof message.content === 'string')
      .map((message) => message.content as string)
  } catch {
    return []
  }
}

/** Chat-completion requests after `from`. The form's silent model-discovery
 *  probe GETs /models and carries no messages, so it must never count as a
 *  round of the ask. */
export function chatRequests(fake: FakeOpenAI, from: number): FakeRequest[] {
  return fake
    .requests()
    .slice(from)
    .filter((request) => request.body.includes('"messages"')) as FakeRequest[]
}

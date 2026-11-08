import type { Locator } from '@playwright/test'

import {
  appReadyForInput,
  bindEndpoint,
  expect,
  promptReady,
  test,
  VaultBackend,
  type Page,
} from './harness'
import { mkdtempSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'

import { readStand } from './stand'

/**
 * e2e: a snippet a person saved reaches a running program, filled in, and
 * unsubmitted (nocx-7ude, plan Task 13 — the snippets epic's gate).
 *
 * The whole sentence the epic promises, walked through the product's own
 * surfaces: the settings page authors the snippet, the chord opens the
 * palette while a program owns the pane and no command editor exists, the
 * ask form fills the blank, and the resolved text arrives at the program's
 * stdin WITHOUT a newline.
 *
 * What is observable, and what is not. Live xterm output is a WebGL canvas:
 * completed command output is frozen into a DOM scrollback block, and that
 * is the only thing the DOM can be asked about. So "the program received
 * the resolved text" is proved by running a program that echoes what it
 * read — the block then contains what actually crossed the seam — and "no
 * newline was sent" is proved by the absence of a new block plus the editor
 * still being hidden at the moment of the fire. The two halves are one
 * assertion; neither is enough alone.
 */

const INPUT = '.pane.active .nocx-editor-input'
/** The grid's own input element: where the pane reads keys while a program
 *  holds the pty and the command editor is hidden. */
const GRID_INPUT = '.pane.active .ui-terminal-input'
// The snippets list is a VARIANT of the quick-connect palette — the same
// surface the server list, the command palette and the secret picker are
// (owner review: a second surface for one job is the shape this repo has
// deleted before).
// A kit Dialog KEEPS its node when it closes, so every locator here is
// scoped to `dialog[open]`: without it "closed" is indistinguishable from
// "open", and the settings page's own snippet editor — closed, still in the
// DOM — answers to the same class as the ask form.
const PANEL = '.nocx-dialog[open] .quick-connect[data-variant="snippets"]'
const ROW = `${PANEL} .quick-connect__item`
/** A refused fire says why in a toast — the kit's one notification
 *  affordance. It used to re-open the palette with the sentence above the
 *  list, on the argument that the surface the refusal was about was still
 *  there; that argument was about the way out the palette carried, and the
 *  way out lives on every row now (nocx-8rtr.2). */
const REFUSAL = '.ui-toast__message'
/** The form that asks for a snippet's parameter fields — all of them at
 *  once, with the palette closed behind it. */
const ASK_FORM = '.nocx-dialog[open]'

async function expectPaletteClosed(page: Page): Promise<void> {
  await expect(page.locator(PANEL)).toHaveCount(0, { timeout: 10_000 })
}

/** Open the palette and pick one snippet by name. */
async function pickSnippet(page: Page, title: string): Promise<void> {
  await pressChord(page)
  await expect(page.locator(PANEL)).toBeVisible({ timeout: 10_000 })
  await page.locator(`${PANEL} .quick-connect__search input`).fill(title)
  await expect(page.locator(ROW, { hasText: title })).toHaveCount(1)
  await page.keyboard.press('Enter')
}

/** The chord is matched on the physical key (snippets/chord.ts), which is
 *  what a Playwright key press produces. */
async function pressChord(page: Page): Promise<void> {
  await page.keyboard.press('Alt+Meta+p')
}

/** Author a snippet through the settings page — the only surface that can
 *  create one, and deliberately the route this check takes rather than
 *  seeding the document behind the product's back. */
async function createSnippet(page: Page, title: string, body: string): Promise<void> {
  await page.keyboard.press('Meta+,')
  await page.locator('.ui-grouped-nav__item[data-item="snippets"]').click()
  await expect(page.locator('.sn-root')).toBeVisible({ timeout: 10_000 })

  // The library is NOT empty on a fresh stand — the service seeds two
  // records when it first writes the document — so the create affordance is
  // the toolbar's, not the empty state's.
  await page.locator('[role="toolbar"]').getByRole('button', { name: '+ New snippet' }).click()
  const dialog = page.getByRole('dialog').filter({ hasText: 'New snippet' })
  await expect(dialog).toBeVisible()
  await dialog.locator('#snippet-title').fill(title)
  // The body is a CM6 editor: click into its content and type, the way a
  // person does. `fill` has nothing to fill — there is no input element.
  await dialog.locator('.sn-body-editor .cm-content').click()
  await page.keyboard.type(body)
  await dialog.getByRole('button', { name: 'Create snippet' }).click()
  await expect(dialog).toHaveCount(0, { timeout: 10_000 })
  await expect(page.locator('.ui-record-row__title', { hasText: title })).toBeVisible({
    timeout: 10_000,
  })
}

/** Open a terminal tab and leave a program waiting on stdin: `read` holds
 *  the pane, so the command editor is hidden and the pty owns input — the
 *  state the palette exists for.
 *
 *  It answers with the block THIS program opened, by the attempt id it bound
 *  to, plus the number of blocks at that moment (what "nothing was
 *  submitted" is measured against).
 *
 *  WHY THE BLOCK'S OWN ID AND NOT A CLASS. `.cmd-block` + a text the block
 *  happens to contain cannot say who it belongs to. Every program in this
 *  file PRINTS 'got-' and also carries it in its command line, and the
 *  scrollback from an earlier test in this file is restored after
 *  `page.goto('/')` — so `page.locator('.cmd-block', { hasText: 'got-' })`
 *  was satisfied by a previous test's frozen block, and even by the running
 *  block's own command line, while this test's program had not returned
 *  (nocx-xn63t.6.43, CI run 39066448948). The running element carries the
 *  authenticated attempt's id (`BlockManager.bindAttempt`, blocks.ts) and
 *  the freeze keeps it (`freezeBlock` → `createCommandBlock`), so ONE
 *  locator spans the same block running and settled, and restored history
 *  — different attempts, different ids — can never answer for it.
 */
async function programWaitingOnStdin(
  page: Page,
  command: string,
): Promise<{ attemptId: string; blocksBefore: number }> {
  // Back to the terminal tab: Settings opened as a second tab, and the
  // first one is the shell this check fires into.
  await page.locator('.nocx-tab').first().click()
  await promptReady(page)
  await page.keyboard.type(command)
  await page.keyboard.press('Enter')
  await expect(page.locator(INPUT)).not.toBeVisible({ timeout: 10_000 })
  // The count is taken once the program's OWN block exists: a block opens on
  // the backend's authenticated running fact, which lands after the input has
  // hidden, so counting at the hide can miss it and see the block arrive later
  // as a phantom "extra" (nocx-2v80t.3.59).
  const running = page.locator('.pane.active .cmd-block.cmd-block-running')
  await expect(running).toHaveCount(1, { timeout: 10_000 })
  const attemptId = await running.getAttribute('data-entry-id')
  expect(attemptId, 'the running block is bound to an attempt').toBeTruthy()
  return { attemptId: attemptId ?? '', blocksBefore: await page.locator('.cmd-block').count() }
}

/** This invocation's own block, running or settled. */
function attemptBlock(page: Page, attemptId: string): Locator {
  return page.locator(`.pane.active .cmd-block[data-entry-id="${attemptId}"]`)
}

/** Wait until the PANE owns the keyboard again.
 *
 *  The palette is a modal dialog: it takes the keyboard on open, and the
 *  browser's own focus return on close lands one task later — MEASURED in
 *  the container under webkit (nocx-xn63t.6.43): the palette's search input
 *  holds focus for the dialog's life, `dialog.close()` drops focus to
 *  <body>, and the grid's input element holds it back. So
 *  `expectPaletteClosed` — which watches the dialog's `open` attribute —
 *  says nothing about the keyboard: it can return while the keyboard is
 *  still on <body>, where a keystroke reaches nobody (measured: with the
 *  keyboard blurred to <body>, the same Enter leaves the program running).
 *  That is the state CI run 39066448948 left the refused-snippet test in —
 *  its own `read x` never received the Enter, and the failure surfaced in
 *  afterEach as a prompt that never came back. The gap itself did not
 *  reproduce here (three repetitions of the exact sequence lost none), so
 *  what this waits for is the observable a keystroke needs and never an
 *  assumed timing. */
async function gridOwnsKeyboard(page: Page): Promise<void> {
  await expect(page.locator(GRID_INPUT)).toBeFocused({ timeout: 10_000 })
}

/** The person's Enter ends the program this block belongs to: wait for the
 *  pane to own the keyboard, press it, and then wait for the three facts
 *  that say the key arrived at the pty and the program returned —
 *
 *    - this invocation's block settles (`data-outcome`, written by the one
 *      outcome owner, blocks.ts settleBlockOutcome);
 *    - its OUTPUT carries the text this test's own program prints;
 *    - the prompt owns the pane again, which restored history cannot fake
 *      and which is exactly what the afterEach needs.
 *
 *  A program still waiting leaves `data-outcome` absent and the editor
 *  hidden, so every one of the three fails — which is the failure the old
 *  `.first()`-over-all-blocks assertion missed. */
async function finishProgramWithEnter(
  page: Page,
  attemptId: string,
  output: string,
): Promise<Locator> {
  const own = attemptBlock(page, attemptId)
  await gridOwnsKeyboard(page)
  await page.keyboard.press('Enter')
  await expect(own).toHaveAttribute('data-outcome', 'success', { timeout: 10_000 })
  await expect(own.locator('.cmd-output')).toContainText(output, { timeout: 10_000 })
  await promptReady(page)
  return own
}

test.describe('a saved snippet reaches a running program', () => {
  test.use({ viewport: { width: 1280, height: 800 } })

  let backend: VaultBackend

  test.beforeEach(async ({ page }) => {
    // Each case owns its document. UI cleanup can observe the list before its
    // asynchronous load finishes and leave a same-named snippet for the next case.
    const root = mkdtempSync(join(tmpdir(), 'nocx-snippets-'))
    backend = new VaultBackend(readStand().server, { root })
    const endpoint = await backend.start()
    await bindEndpoint(page, endpoint)
  })

  test.afterEach(() => {
    backend?.stop()
  })

  test('fires filled in, without a newline, into the program reading stdin', async ({ page }) => {
    await page.goto('/')
    await expect(page.locator('.nocx-tab')).toHaveCount(1)
    await appReadyForInput(page)

    // One env span and one ask span, exactly as the epic's criterion asks.
    // cwd rather than user: `user` is the SSH user and a local shell has
    // none, so {{env:user}} would REFUSE here — correctly, and that refusal
    // is the subject of the resolver's own tests, not of this one.
    await createSnippet(page, 'e2e fill', 'in-{{env:cwd}}-{{tag}}')

    const { attemptId, blocksBefore } = await programWaitingOnStdin(
      page,
      'read x; printf \'got-%s\\n\' "$x"',
    )

    // The chord reaches the palette even though no command editor exists —
    // the case the whole surface was built for.
    await pickSnippet(page, 'e2e fill')

    // A body with parameters closes the palette and asks for every field at
    // once, in a form (owner review: a field that filters a list cannot
    // also be where a value is typed).
    const form = page.locator(ASK_FORM).filter({ hasText: 'e2e fill' })
    await expect(form).toBeVisible({ timeout: 10_000 })
    await form.locator('#snippet-ask-tag').fill('alpha')
    await form.getByRole('button', { name: 'Insert', exact: true }).click()
    await expect(form).toHaveCount(0, { timeout: 10_000 })

    // Half one: nothing was submitted. The palette closed on delivery, no
    // new completed block appeared, and the editor is still hidden — the
    // program is still the one holding input.
    await expectPaletteClosed(page)
    await expect(page.locator('.cmd-block')).toHaveCount(blocksBefore)
    await expect(page.locator(INPUT)).not.toBeVisible()

    // Half two: what was sent. The person presses Enter themselves, and the
    // program echoes back what it read — in THIS invocation's block, whose
    // output no earlier test's restored block carries.
    const block = await finishProgramWithEnter(page, attemptId, 'got-in-')
    // The ask answer arrived, and no span survived as literal text — the
    // env value is the session's own user, which the test does not pretend
    // to know, so it asserts what it can: the braces are gone.
    await expect(block).toContainText('-alpha')
    await expect(block).not.toContainText('{{')
  })

  test('a multi-line body is refused when the program has not enabled bracketed paste', async ({
    page,
  }) => {
    await page.goto('/')
    await expect(page.locator('.nocx-tab')).toHaveCount(1)
    await appReadyForInput(page)
    await createSnippet(page, 'e2e two lines', 'first line\nsecond line')

    // This program prints its OWN word. `got-` would not have been enough:
    // the previous test in this file prints `got-in-…` into the same session,
    // and its frozen block is restored when this page loads. A refusal this
    // test's Enter did not end could therefore look like a pass against
    // history that had nothing to do with it (nocx-xn63t.6.43).
    const { attemptId, blocksBefore } = await programWaitingOnStdin(
      page,
      'read x; printf \'refused-%s\\n\' "$x"',
    )

    await pickSnippet(page, 'e2e two lines')

    // The palette closes and the reason arrives as a toast: a newline would be
    // read as Return and run half the phrase, so nothing is sent at all.
    // Filtered by its own words rather than asserted as the only toast: a
    // toast from an earlier case in this file may still be on screen, and
    // "the only one" is a claim about the others, not about this refusal.
    await expect(page.locator(REFUSAL).filter({ hasText: 'bracketed paste' })).toBeVisible({
      timeout: 10_000,
    })
    await expectPaletteClosed(page)
    await expect(page.locator('.cmd-block')).toHaveCount(blocksBefore)
    // And the program is still waiting: Enter ends it with the empty line
    // the person typed, not with anything the refused fire sent. The wait is
    // on THIS program's own block settling with its own output and the prompt
    // coming back — so a program that is still running cannot be answered for
    // by the restored block of an earlier test, and the afterEach cannot be
    // the first thing to notice (nocx-xn63t.6.43).
    const own = await finishProgramWithEnter(page, attemptId, 'refused-')
    // Nothing was inserted, so the refused body never reached the program: an
    // empty line is what `read x` returned.
    await expect(own.locator('.cmd-output')).not.toContainText('first line')
  })

  // The OTHER multi-line branch: bracketed paste ON, body delivered. It could
  // not be arranged here until nocx-8rtr.1 was root-caused, and the cause was
  // not the stand — the wire carries the program's DECSET (an ON/off/ON probe
  // recorded it), while renderer.write() is fire-and-forget and insertSnippet
  // read the mode a parse pass too early. The product fences on the parse now,
  // so this branch is reachable, and it is the check that keeps it that way.
  test('a multi-line body is delivered when the program HAS enabled bracketed paste', async ({
    page,
  }) => {
    await page.goto('/')
    await expect(page.locator('.nocx-tab')).toHaveCount(1)
    await createSnippet(page, 'e2e two lines', 'first line\nsecond line')

    // The program turns mode 2004 on itself and then holds the pane on stdin.
    // `read -r` keeps the backslashes the two-line body does not have, and
    // keeps the whole paste as ONE value, which is what makes the assertion
    // below about delivery rather than about the shell's word splitting.
    const { attemptId, blocksBefore } = await programWaitingOnStdin(
      page,
      "printf '\\033[?2004h'; read -r x; printf 'got-%s\\n' \"$x\"",
    )

    // WHY THIS RETRIES, and why retrying is not a disguised sleep.
    //
    // The DECSET the program sends crosses the wire AFTER the editor hides:
    // the editor hides on the shell-integration marker that says a command
    // started, and the program's printf runs after that marker. So the moment
    // this spec can first act is, by construction, a moment at which the byte
    // may still be in flight. The product's fence covers bytes that have
    // ARRIVED and are not yet parsed; nothing can make it wait for a byte
    // nobody has announced, and it should not try.
    //
    // So the wait is on the observable outcome rather than on a duration
    // (AGENTS.md: a test may not depend on timing). Each refused attempt is
    // provably side-effect-free — the OFF-branch test above asserts a refusal
    // writes nothing and submits nothing — so re-attempting costs a keystroke
    // and proves the same thing a person's slower hand would.
    await expect
      .poll(
        async () => {
          // Counted per attempt rather than merely looked for: a toast from
          // the previous attempt is still on screen while this one runs, and
          // reading it as this one's answer would make the poll never finish.
          const toastsBefore = await page.locator(REFUSAL).count()
          await pickSnippet(page, 'e2e two lines')
          await expectPaletteClosed(page)
          return (await page.locator(REFUSAL).count()) > toastsBefore ? 'refused' : 'delivered'
        },
        { timeout: 15_000 },
      )
      .toBe('delivered')

    // Nothing was submitted — the fire never sends a newline (§9.3), even when
    // the destination could carry one.
    await expect(page.locator('.cmd-block')).toHaveCount(blocksBefore)

    // What the program received is read from THIS invocation's own block:
    // restored history carries other attempts' ids, so an earlier `read`
    // block cannot answer for it (nocx-xn63t.6.43).
    await page.keyboard.press('Enter')
    const block = attemptBlock(page, attemptId)
    await expect(block).toHaveAttribute('data-outcome', 'success', { timeout: 10_000 })
    await expect(block).toContainText('got-first line', { timeout: 10_000 })
    await expect(block).toContainText('second line')
  })

  // The epic's own sentence (nocx-92kes): a body whose CHOICES and whose
  // PARAGRAPHS both vary, fired by a person who picks one and leaves the
  // other switched off.
  //
  // What makes it an end-to-end check rather than a third rendering of the
  // resolver's unit tests is the program on the other side. `read -r a` then
  // `read -r b` reads exactly two lines and prints them bracketed, so the
  // assertion discriminates the failures that matter and that a unit test
  // cannot see: a blank line left where the dropped paragraph was arrives as
  // an empty `b`, and a paragraph that was not dropped arrives as the wrong
  // one. The person's own Enter is what completes the second read, which is
  // how "no trailing newline was sent" is observed — the fire left the
  // program still waiting.
  test('a chosen option and an un-ticked condition reach the program', async ({ page }) => {
    await page.goto('/')
    await expect(page.locator('.nocx-tab')).toHaveCount(1)

    // The tags stand on their own lines here, which is the case that needs
    // watching: each takes its whole line with it, so switching `parallel`
    // off has to leave `run codex\ndone` and NOT a blank line between them.
    await createSnippet(
      page,
      'e2e choice',
      'run {{worker=claude|omp|codex}}\n{% if parallel %}\nin parallel\n{% endif %}\ndone',
    )

    const { attemptId, blocksBefore } = await programWaitingOnStdin(
      page,
      'printf \'\\033[?2004h\'; read -r a; read -r b; printf \'got-[%s][%s]\\n\' "$a" "$b"',
    )

    // Same shape as the bracketed-paste test above, and for the same reason:
    // the program's DECSET may still be in flight at the first moment this
    // spec can act, and a refused fire is provably side-effect-free, so the
    // poll re-attempts rather than waiting on a duration.
    await expect
      .poll(
        async () => {
          const toastsBefore = await page.locator(REFUSAL).count()
          await pressChord(page)
          await expect(page.locator(PANEL)).toBeVisible({ timeout: 10_000 })
          await page.locator(`${PANEL} .quick-connect__search input`).fill('e2e choice')
          await expect(page.locator(ROW, { hasText: 'e2e choice' })).toHaveCount(1)
          await page.keyboard.press('Enter')

          // The form asks both questions at once: a dropdown for the choice
          // and a tick for the paragraph.
          const form = page.locator(ASK_FORM).filter({ hasText: 'e2e choice' })
          await expect(form).toBeVisible({ timeout: 10_000 })
          const flag = form.locator('label.ui-checkbox', { hasText: 'parallel' })
          await expect(flag).toBeVisible()
          await expect(flag.locator('input')).not.toBeChecked()
          await form.locator('#snippet-ask-worker').selectOption('codex')
          await form.getByRole('button', { name: 'Insert', exact: true }).click()
          await expect(form).toHaveCount(0, { timeout: 10_000 })

          await expectPaletteClosed(page)
          return (await page.locator(REFUSAL).count()) > toastsBefore ? 'refused' : 'delivered'
        },
        { timeout: 20_000 },
      )
      .toBe('delivered')

    // Nothing was submitted: the fire never sends a newline of its own, so
    // the second `read` is still waiting for the person.
    await expect(page.locator('.cmd-block')).toHaveCount(blocksBefore)

    // The whole assertion in one string, on this program's own block: the
    // chosen option is there, the un-ticked paragraph is not, there is no
    // blank line where it was, and no {{…}} survived as literal text.
    await page.keyboard.press('Enter')
    const block = attemptBlock(page, attemptId)
    await expect(block).toHaveAttribute('data-outcome', 'success', { timeout: 10_000 })
    await expect(block).toContainText('got-[run codex][done]', { timeout: 10_000 })
  })

  test('a snippet whose secret cannot be resolved refuses, and writes nothing', async ({
    page,
  }) => {
    await page.goto('/')
    await expect(page.locator('.nocx-tab')).toHaveCount(1)
    await appReadyForInput(page)
    // This case owns a fresh disposable backend, so it cannot inherit a vault
    // from the shared stand or another case. Its uninitialized vault makes the
    // reference genuinely unresolved. The rule under test is the one §11.1 states:
    // an unresolved name refuses the whole fire — the literal {{secret:…}}
    // must never reach a running program's stdin.
    await createSnippet(page, 'e2e fill', 'psql {{secret:e2e-absent}}')

    const { attemptId, blocksBefore } = await programWaitingOnStdin(
      page,
      'read x; printf \'redacted-%s\\n\' "$x"',
    )

    await pickSnippet(page, 'e2e fill')

    await expect(page.locator(REFUSAL).filter({ hasText: 'could not be resolved' })).toBeVisible({
      timeout: 10_000,
    })
    await expectPaletteClosed(page)
    await expect(page.locator('.cmd-block')).toHaveCount(blocksBefore)

    // No Escape: the palette closed itself on the refusal, and an Escape with
    // nothing to dismiss would reach the program as an ESC byte and land in
    // the very value this then asserts on.
    const own = await finishProgramWithEnter(page, attemptId, 'redacted-')
    await expect(own.locator('.cmd-output')).not.toContainText('secret:')
  })
})

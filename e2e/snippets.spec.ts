import { test, expect, promptReady, type Page } from './harness'

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
/** The form that asks for a snippet's {{ask:…}} fields — all of them at
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

/** Remove a snippet the same way, so a second run starts where this one
 *  did. Tolerant of it being gone already: an interrupted run must not
 *  leave the next one failing in its cleanup. */
async function removeSnippet(page: Page, title: string): Promise<void> {
  await page.keyboard.press('Meta+,')
  await page.locator('.ui-grouped-nav__item[data-item="snippets"]').click()
  await expect(page.locator('.sn-root')).toBeVisible({ timeout: 10_000 })
  const del = page.locator(`[aria-label="Delete ${title}"]`)
  if ((await del.count()) === 0) return
  await del.first().click()
  await page
    .getByRole('dialog')
    .filter({ hasText: `Delete "${title}"?` })
    .getByRole('button', { name: 'OK', exact: true })
    .click()
  await expect(page.locator(`[aria-label="Delete ${title}"]`)).toHaveCount(0, { timeout: 10_000 })
}

/** Open a terminal tab and leave a program waiting on stdin: `read` holds
 *  the pane, so the command editor is hidden and the pty owns input — the
 *  state the palette exists for. Returns the number of completed blocks at
 *  that moment, which is what "nothing was submitted" is measured against.
 */
async function programWaitingOnStdin(page: Page, command: string): Promise<number> {
  // Back to the terminal tab: Settings opened as a second tab, and the
  // first one is the shell this check fires into.
  await page.locator('.nocx-tab').first().click()
  await promptReady(page)
  await page.keyboard.type(command)
  await page.keyboard.press('Enter')
  await expect(page.locator(INPUT)).not.toBeVisible({ timeout: 10_000 })
  return await page.locator('.cmd-block').count()
}

test.describe('a saved snippet reaches a running program', () => {
  test.use({ viewport: { width: 1280, height: 800 } })

  test.afterEach(async ({ page }) => {
    await page.goto('/')
    // The chord removeSnippet presses goes to a page that has just loaded, so
    // wait for the app to be up before pressing it. On webkit it was landing
    // before the shortcut was wired: Settings never opened, the click on its
    // rail waited on an element that was never going to appear, and the whole
    // test failed in its own cleanup. Waiting on the observable state — the
    // prompt owning input — is the fix; a bigger budget only moves it.
    await promptReady(page)
    await removeSnippet(page, 'e2e fill')
    await removeSnippet(page, 'e2e two lines')
  })

  test('fires filled in, without a newline, into the program reading stdin', async ({ page }) => {
    await page.goto('/')
    await expect(page.locator('.nocx-tab')).toHaveCount(1)

    // One env span and one ask span, exactly as the epic's criterion asks.
    // cwd rather than user: `user` is the SSH user and a local shell has
    // none, so {{env:user}} would REFUSE here — correctly, and that refusal
    // is the subject of the resolver's own tests, not of this one.
    await createSnippet(page, 'e2e fill', 'in-{{env:cwd}}-{{ask:tag}}')

    const blocksBefore = await programWaitingOnStdin(page, 'read x; printf \'got-%s\\n\' "$x"')

    // The chord reaches the palette even though no command editor exists —
    // the case the whole surface was built for.
    await pickSnippet(page, 'e2e fill')

    // A body with {{ask:…}} closes the palette and asks for every field at
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
    // program echoes back what it read.
    await page.keyboard.press('Enter')
    const block = page.locator('.cmd-block', { hasText: 'got-in-' }).first()
    await expect(block).toBeVisible({ timeout: 10_000 })
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
    await createSnippet(page, 'e2e two lines', 'first line\nsecond line')

    const blocksBefore = await programWaitingOnStdin(page, 'read x; printf \'got-%s\\n\' "$x"')

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
    // the person typed, not with anything the refused fire sent.
    await page.keyboard.press('Enter')
    await expect(page.locator('.cmd-block', { hasText: 'got-' }).first()).toBeVisible({
      timeout: 10_000,
    })
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
    const blocksBefore = await programWaitingOnStdin(
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

    // The person presses Enter themselves, and the program echoes what it
    // read: both lines arrived, as one bracketed paste.
    await page.keyboard.press('Enter')
    const block = page.locator('.cmd-block', { hasText: 'got-first line' }).first()
    await expect(block).toBeVisible({ timeout: 10_000 })
    await expect(block).toContainText('second line')
  })

  test('a snippet whose secret cannot be resolved refuses, and writes nothing', async ({
    page,
  }) => {
    await page.goto('/')
    await expect(page.locator('.nocx-tab')).toHaveCount(1)
    // This stand has no vault set up, so the reference cannot resolve. The
    // rule under test is the one §11.1 states: an unresolved name refuses
    // the whole fire — the literal {{secret:…}} must never reach a running
    // program's stdin.
    await createSnippet(page, 'e2e fill', 'psql {{secret:e2e-absent}}')

    const blocksBefore = await programWaitingOnStdin(page, 'read x; printf \'got-%s\\n\' "$x"')

    await pickSnippet(page, 'e2e fill')

    await expect(page.locator(REFUSAL).filter({ hasText: 'could not be resolved' })).toBeVisible({
      timeout: 10_000,
    })
    await expectPaletteClosed(page)
    await expect(page.locator('.cmd-block')).toHaveCount(blocksBefore)

    // No Escape: the palette closed itself on the refusal, and an Escape with
    // nothing to dismiss would reach the program as an ESC byte and land in
    // the very value this then asserts on.
    await page.keyboard.press('Enter')
    const block = page.locator('.cmd-block', { hasText: 'got-' }).first()
    await expect(block).toBeVisible({ timeout: 10_000 })
    await expect(block).not.toContainText('secret:')
  })
})

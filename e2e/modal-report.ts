/**
 * When a test fails, say which modal was standing over it.
 *
 * # The failure this exists for
 *
 * nocx-76wyh: `connections-settings.spec.ts` timed out clicking a tab INSIDE
 * the connection editor, every one of ~56 retries refused with
 *
 *     <div class="ui-prompt-overlay" data-placement="top-sheet">
 *       from <div> subtree intercepts pointer events
 *
 * That report names the SHAPE of the thing in the way and nothing else. Six
 * surfaces in this app mount a top-sheet Prompt, and the message cannot tell
 * you which — so the question "what was on screen" becomes a bisect over the
 * run's file order. It was one: `Unlock the vault to use its secrets`, raised
 * because the shared stand's vault had sealed (design D9; the cause and its
 * fix are in `resetStand`, harness.ts). One accessible name would have said
 * so on the first sighting.
 *
 * So this reads the modals still standing whenever a test FAILS, and prints
 * them with their names. Playwright shows a failing test's output in the
 * failure block, next to the interception message it explains.
 *
 * # Why it reports rather than asserts
 *
 * The first version of this was the obvious one — fail any test that ENDS
 * with a modal open, so a leak is reported in the file that left it. It was
 * built, and measured over a full webkit run: 24 tests failed and 10 more
 * never ran, and not one of them was the defect. Two reasons, and both are
 * facts about this suite rather than opinions about instrumentation:
 *
 *   - Playwright gives every test a fresh context, so DOM cannot travel
 *     between tests at all. A modal open at the end of a test is gone before
 *     the next one starts, and blaming a spec for it blames it for nothing.
 *   - What DID travel was backend state, and NO SPEC LEFT IT. The vault seals
 *     when the last client leaves — which happens while a spec that brought
 *     its own backend is running, long after the last shared-stand spec ended
 *     clean. There is no spec to fail.
 *
 * The specs it failed were doing legitimate work: `prompt-height.spec.ts`
 * builds a synthetic `.ui-prompt-overlay` as its subject, `quick-connect`,
 * `secret-panel-position` and `field-focus-stability` end on the surface they
 * are about, and `agent-tool-approval` ends holding the approval prompt it
 * exists to raise. Making twenty specs press Escape to satisfy a guard that
 * cannot catch the defect is ceremony, and ceremony that fails ten unrelated
 * tests is worse than none.
 *
 * A diagnostic on the failing test has no false positives by construction: it
 * speaks only where something is already wrong, and it says the one fact the
 * report was missing.
 */
import { type Page, type TestInfo } from '@playwright/test'

/** Every modal surface in the kit, as one selector.
 *
 *  `Prompt` (`.ui-prompt-overlay`, either placement) and `Dialog`, which is a
 *  native `<dialog>` opened with `showModal()` and therefore observable by
 *  its `open` attribute. `dialog.ui-connection-overlay` matches too and is
 *  worth reporting for the same reason: an app waiting to reconnect is inert,
 *  and a click into an inert document fails exactly like this. */
const MODAL = '.ui-prompt-overlay, dialog[open]'

/**
 * Read the modals standing in `page`, each as the sentence a person would
 * have read on it.
 *
 * One `evaluate`, no retrying assertion: this runs in teardown and asks about
 * the page as the test left it — one instant, no waiting. It is a diagnostic,
 * so it must never fail: a closed page, a crashed browser or a navigation
 * mid-read all answer "nothing to say" rather than replacing the test's own
 * error with one about the machinery.
 */
async function standingModals(page: Page): Promise<string[]> {
  if (page.isClosed()) return []
  return page
    .evaluate((selector) => {
      return Array.from(document.querySelectorAll(selector)).map((el) => {
        const panel = el.matches('dialog') ? el : (el.querySelector('[role="dialog"]') ?? el)
        const labelled = panel.getAttribute('aria-labelledby')
        const title = labelled ? document.getElementById(labelled)?.textContent : null
        const name =
          panel.getAttribute('aria-label') ??
          title ??
          panel.querySelector('h2')?.textContent ??
          '(unnamed)'
        const kind = el.matches('dialog') ? 'dialog' : 'prompt'
        const placement = el.getAttribute('data-placement') ?? 'modal'
        return `${kind} "${name}" (placement=${placement}, class=${el.className})`
      })
    }, MODAL)
    .catch(() => [])
}

/**
 * Name the modals standing over a test that has just failed.
 *
 * Silent on a passing test — see the header for the run that measured why —
 * and silent when nothing is standing, so the only time this speaks is the
 * time its sentence is the answer.
 */
export async function reportStandingModals(page: Page, info: TestInfo): Promise<void> {
  if (info.status === 'passed') return
  const modals = await standingModals(page)
  if (modals.length === 0) return
  process.stderr.write(
    `\nMODAL OVER THE PAGE — ${info.titlePath.slice(1).join(' › ')} failed with ` +
      `${modals.length} modal(s) standing: ${modals.join(', ')}.\n` +
      `A modal owns the pointer for the whole document, so any click under it ` +
      `is refused with "intercepts pointer events" whatever it was aimed at. ` +
      `If this test did not open that modal, the product raised it — read its ` +
      `name for why (nocx-76wyh).\n`,
  )
}

/**
 * e2e: a toast never covers the activity bar's global actions (nocx-nbxm6).
 *
 * The notification area is `position: fixed` in the viewport's bottom-right
 * corner, and once the activity bar moved to the trailing edge (nocx-crjft)
 * that corner is the rail's bottom zone — the API workbench and Settings. A
 * `danger` toast is sticky, so an overlap there is not a flicker: it is two
 * global actions unreachable until somebody dismisses it.
 *
 * The unit assertion that ships with the offset (`toast.test.tsx`) reads the
 * stylesheet SOURCE, because jsdom loads no CSS. That makes it a ratchet
 * against re-hardcoding the number and nothing more — it passes for a
 * stylesheet that is right and a layout that is wrong. This is the check that
 * watches a user see it, and the overlap existed in the first place because
 * nothing did.
 *
 * The toast is raised the way a user raises one — the Git panel's copy
 * affordance, which answers with a confirmation toast — rather than by
 * reaching into the app. A toast the test injected would prove the CSS rather
 * than the product.
 *
 * Only the HORIZONTAL overlap is asserted. Both surfaces live at the bottom of
 * the window, so a vertical overlap is expected and says nothing.
 */
import { appReadyForInput, test, expect, promptReady } from './harness'
import { createRepo, cleanupRepo } from './git-fixture'

const TAB_TITLE = '.nocx-tab-title'
const VIEW_GIT = 'button[data-view="git"]'
const BRANCH = '[data-testid="git-branch"]'
const COPY_BRANCH = '[data-testid="git-copy-branch"]'
const TOAST = '.ui-toast'
const SETTINGS = '.activity-bar button[data-action="settings"]'

test('a toast does not cover the activity bar (nocx-nbxm6)', async ({ page }) => {
  const repo = createRepo({ file: 'a.txt' })
  try {
    await page.goto('/')
    await appReadyForInput(page)
    // A NON-EMPTY TAB TITLE IS NOT READINESS TO TYPE. It says the pane exists;
    // it says nothing about which element has the keyboard, so keystrokes sent
    // on the strength of it can land before the editor is focused and lose
    // their leading characters. That is what failed here: `cd <repo>` arrived
    // as `t-e2e-dNwLvP`, the shell ran the remains as a command, the cwd never
    // changed, and the wait below timed out against a title that was still the
    // home directory (nocx-b0k9a's sibling — a spec asserting a state the app
    // never promised). promptReady is the question actually being asked, and
    // it is what every other spec that types a `cd` already uses.
    await promptReady(page)

    // Park the shell in the repo — OSC 7 makes the cwd verified, and the tab
    // title is the frontend's own word that it processed it.
    await page.keyboard.type(`cd ${repo.root}`)
    await page.keyboard.press('Enter')
    await expect(page.locator(TAB_TITLE).first()).toContainText(repo.basename, { timeout: 20_000 })

    await page.locator(VIEW_GIT).click()
    await expect(page.locator(BRANCH)).toBeVisible({ timeout: 20_000 })

    await page.locator(COPY_BRANCH).click()
    const toast = page.locator(TOAST).first()
    await expect(toast).toBeVisible({ timeout: 10_000 })

    const toastBox = await toast.boundingBox()
    const settingsBox = await page.locator(SETTINGS).boundingBox()
    expect(toastBox).not.toBeNull()
    expect(settingsBox).not.toBeNull()

    expect(toastBox!.x + toastBox!.width).toBeLessThanOrEqual(settingsBox!.x)
  } finally {
    cleanupRepo(repo)
  }
})

/**
 * e2e: the three things the workbench's TREE gained in one afternoon, each
 * watched through the seam a person actually reaches (beads nocx-8v1fu,
 * nocx-rmjj8, nocx-aug1m).
 *
 * WHY THIS FILE EXISTS. AGENTS.md testing rule 2: every epic that is not a
 * chore proves its happy path, and it closes only when one automated check
 * has watched a user do it end to end. Three deliverables landed with unit
 * coverage and nothing that had ever watched a person use them:
 *
 *   1. A collection can be given a FOLDER from the tree (nocx-8v1fu) — one
 *      inside an open collection, another inside that one, and a request
 *      saved into the inner one.
 *   2. A request's ACTS are behind the right button (nocx-rmjj8) — the row's
 *      menu duplicates, and the header's ⋮ offers the same list.
 *   3. The tree says WHICH REQUEST IS OPEN (nocx-aug1m) — the open row wears
 *      the kit's selected mark, and the mark moves.
 *
 * Coverage could not have reported any of them missing: rule 2 again — a
 * ratchet says written code is used, never that a feature is wired. Every
 * step below goes through a door a person has: the activity-bar entry, a
 * right-click on a row, an item in the menu it opens, the ask that item
 * raises, the header's rename field, the Save button. Nothing here calls the
 * store, and nothing arranges through the control plane a state the product
 * is supposed to arrange.
 *
 * ONE FILE, THREE TESTS, because the setup is one setup: a stand whose
 * built-in collection is open with two requests in it. Splitting it would be
 * three copies of the same eleven lines. A BACKEND PER TEST rather than per
 * file, the arrangement api-import.spec.ts settled on: the first test makes
 * folders inside Playground and the second writes a copy into it, so a shared
 * home would make each test's starting tree depend on the one before it.
 *
 * ITS OWN BACKEND, not the shared stand, for the reason the other three api
 * specs give: these tests WRITE into the collections folder of the home the
 * backend resolved — folders, a request file, a duplicate — and doing that on
 * the stand every other spec shares would leave them behind for all of them.
 * `withoutSecretService` is true for the reason harness.ts states: it makes
 * "no OS keystore" an explicit premise on both platforms rather than a
 * property of whatever happens to be running around the test. No vault is set
 * up here because nothing in these three walks carries a secret.
 *
 * WHAT IS DISK TRUTH AND WHAT IS ONLY THE SCREEN. Worth stating, because
 * "the row appeared" is not by itself a claim that anything was written:
 *
 *   - A FOLDER row is disk truth. `api.collections.createFolder` answers the
 *     collection as it is now, from the backend's own walk (api-store.ts), so
 *     the row is drawn from a listing rather than from an optimistic insert.
 *   - A DUPLICATE's row is disk truth. `duplicateRequest` re-reads the folder
 *     before it opens the copy — "the tree learns about the copy the way it
 *     learns about a colleague's".
 *   - A SAVE is not, and that is why the first test ends by re-reading the
 *     open folders through the menu: `saveDraft` writes the file and does not
 *     re-list, so the tree goes on showing the name the FILE had when it was
 *     listed. The renamed row appearing after the re-read is therefore the
 *     observable event that says the write landed — and it says it about the
 *     path it was written to, inside the nested folder.
 *
 * NOTHING HERE WAITS ON A DURATION. There is no `waitForTimeout` in this
 * file: every wait is on an observable state — a menu on screen, a dialog
 * gone, a row in the tree, a name in the header, an attribute on a row. A
 * spec that needs a slow machine to pass is broken on a fast one too; it has
 * only not been caught yet.
 *
 * GEOMETRY IS NOT ASSERTED HERE. api-testing.spec.ts holds the permanent
 * "nothing from the editor up to the pane is wider than its own box" check,
 * and it is one owner of that question. A second copy in this file would be
 * two, agreeing until the day one of them learnt about a new ancestor.
 */
import { test as base, expect, type Locator, type Page } from '@playwright/test'
import { existsSync, mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { bindEndpoint, collectionsDir, VaultBackend, type DisposableRoot } from './harness'
import { readStand } from './stand'

const test = base

/** Lazily, not at module scope: the stand is started by globalSetup, which
 *  runs after Playwright has collected this file. */
const devharnessBin = (): string => readStand().devharness

/**
 * The built-in collection and its two seeded requests
 * (internal/apicoll/starter.go). They are the setup these tests would
 * otherwise have to build: a collection that is already open, with two
 * requests whose names are far enough apart to tell one row from another.
 *
 * Named as constants rather than typed into each assertion so that a rename
 * of the seed is one edit here, and so a reader can see at a glance that
 * nothing in this file invented a fixture.
 */
const PLAYGROUND = 'Playground'
const ZEN = 'Zen'
const RATE_LIMIT = 'Rate limit'

/** What a copy is called: `freeCopyName` in api-store.ts, whose rule is
 *  "<name> copy", then "<name> copy 2". Spelled here because the whole point
 *  of the duplicate act is that the second row is one a person can TELL
 *  APART, and a test that accepted any name would not be checking that. */
const ZEN_COPY = `${ZEN} copy`

/** The two folders the first test makes, and the name it gives the request it
 *  saves into the inner one. */
const OUTER_FOLDER = 'reports'
const INNER_FOLDER = 'weekly'
const SAVED_NAME = 'Weekly summary'
const SAVED_URL = 'https://example.invalid/weekly'

/** The malformed request file the fourth test seeds, and the decoder's own
 *  words for it. The owner's real file used `var` where the format wants
 *  `variables`, and this is that case: a file the decoder reads as `json:
 *  unknown field "var"`, which the tree must show a person rather than the
 *  machine. Seeded into Playground's root so it lists beside Zen and Rate
 *  limit, and deleted at the end of the walk so the tree and the disk agree
 *  again. */
const MALFORMED_NAME = 'post-broker-access.json'
const MALFORMED_REASON = 'json: unknown field "var"'

/**
 * A row in the tree, by the name it shows.
 *
 * By the name span's `title`, which the kit sets to the row's name — or,
 * on a malformed row, to the reason it could not be read (ui/tree-row.tsx
 * takes a `hint`, and api-pane.tsx passes the malformed file's reason;
 * the title is the exception to the "name and nothing else" rule). So
 * this matches a request or folder row EXACTLY, and a malformed row is
 * found by its `data-row-key` (`${handle}:!${relPath}`) instead — which
 * is how the fourth test locates one. A text filter would not: `Zen` is
 * a prefix of `Zen copy`, and the whole subject of the second test is
 * that both are on screen at once.
 */
function treeRow(page: Page, workbench: Locator, name: string): Locator {
  return workbench
    .locator('.api-tree__row')
    .filter({ has: page.locator(`.ui-tree-row__name[title="${name}"]`) })
}

/** What a menu is offering, once it is on screen. The wait is part of the
 *  read: `allTextContents` does not retry, so asking before the popover has
 *  rendered would answer `[]` and read as "the menu is empty". */
async function menuLabels(menu: Locator): Promise<string[]> {
  await expect(menu).toBeVisible()
  return menu.locator('.ui-context-menu__label').allTextContents()
}

test.describe('the collections tree: folders, a row’s acts, and the mark that says which is open', () => {
  // Three columns — tree, request, runs — and the header's ⋮ lives in the
  // second one, so all three have to be on screen. The container's default
  // viewport is narrower than the shipped app's window.
  test.use({ viewport: { width: 1400, height: 900 } })

  let disposable: DisposableRoot
  let backend: VaultBackend

  test.beforeEach(() => {
    disposable = { root: mkdtempSync(join(tmpdir(), 'nocx-e2e-api-tree-')) }
    backend = new VaultBackend(devharnessBin(), disposable, true)
  })

  test.afterEach(() => {
    backend?.stop()
  })

  /**
   * Reach the workbench the way a person does, and answer with it.
   *
   * The waits are preconditions rather than assertions about the subject: the
   * tab title is the app having started, and the two seeded rows are the
   * first listing having landed. Waiting on `Zen` specifically rather than on
   * "some row" is what makes a later `Zen copy` a second row rather than a
   * race with the first one arriving.
   */
  const openWorkbench = async (page: Page): Promise<Locator> => {
    const ep = await backend.start()
    await bindEndpoint(page, ep)
    await page.goto('/')
    await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('', { timeout: 15_000 })

    // The activity-bar entry, which design §9.2 says opens or focuses the
    // pane. Reaching the workbench any other way would prove the workbench
    // works and say nothing about whether it can be got to.
    await page.locator('.activity-bar button[data-action="api"]').click()
    const workbench = page.locator('.api-workbench')
    await expect(workbench).toBeVisible({ timeout: 15_000 })
    await expect(treeRow(page, workbench, PLAYGROUND)).toBeVisible({ timeout: 15_000 })
    await expect(treeRow(page, workbench, ZEN)).toBeVisible({ timeout: 15_000 })
    await expect(treeRow(page, workbench, RATE_LIMIT)).toBeVisible({ timeout: 15_000 })
    return workbench
  }

  test('a person makes a folder in a collection, another inside it, and saves a request into the inner one', async ({
    page,
  }) => {
    const workbench = await openWorkbench(page)
    const folderMenu = page.getByTestId('api-folder-row-menu')

    // ── The door: the right button on the collection's own row ────────────
    //
    // A collection IS a folder (design §6.1), so the acts are the same acts
    // and the list is ONE list — asserted here in full, because "two menus
    // that agree until somebody adds an item to one" is the defect the single
    // list exists to prevent, and only a whole-list comparison can see it.
    // `Close collection` is in it HERE and must be absent below: it is an act
    // on what the app has open, and there is no such act on a folder inside
    // one.
    await treeRow(page, workbench, PLAYGROUND).click({ button: 'right' })
    expect(await menuLabels(folderMenu)).toEqual(['New request', 'New folder…', 'Close collection'])

    // ── The ask, which names the place the folder is going ─────────────────
    await folderMenu.getByRole('menuitem', { name: 'New folder…' }).click()
    const folderAsk = page.getByRole('dialog').filter({ hasText: `New folder in ${PLAYGROUND}` })
    await expect(folderAsk).toBeVisible()
    await page.locator('#api-new-folder-name').fill(OUTER_FOLDER)
    await folderAsk.getByRole('button', { name: 'Create folder' }).click()

    // THE ASK CLOSING is the success, and it is the state to wait on: it
    // stays open holding the name when the backend refuses (collection-
    // dialog.tsx), so a dialog gone is the call having been accepted.
    await expect(folderAsk).toBeHidden()

    // …and the row is inside the collection rather than beside it. `aria-level`
    // is the kit's own account of depth (1-based, ui/tree-row.tsx), so this
    // asserts the STRUCTURE and not merely that a row with that name exists
    // somewhere in the tree.
    const outer = treeRow(page, workbench, OUTER_FOLDER)
    await expect(outer).toBeVisible()
    await expect(outer.locator('.ui-tree-row')).toHaveAttribute('aria-level', '2')

    // ── And another inside THAT one, which is the nesting claim ────────────
    //
    // Repeated calls, never a path this spec joined: the parent is a folder
    // that already exists, addressed by the row a person aimed at. That is
    // the whole of how nesting works in this feature.
    await outer.click({ button: 'right' })
    // The SAME list minus the one act that does not apply, which is this
    // surface's rule for every door: absent, not present and refusing.
    expect(await menuLabels(folderMenu)).toEqual(['New request', 'New folder…'])
    await folderMenu.getByRole('menuitem', { name: 'New folder…' }).click()

    // Titled by the PATH within the collection rather than by the leaf, which
    // is what tells two folders called `weekly` apart.
    const innerAsk = page.getByRole('dialog').filter({ hasText: `New folder in ${OUTER_FOLDER}` })
    await expect(innerAsk).toBeVisible()
    await page.locator('#api-new-folder-name').fill(INNER_FOLDER)
    await innerAsk.getByRole('button', { name: 'Create folder' }).click()
    await expect(innerAsk).toBeHidden()

    const inner = treeRow(page, workbench, INNER_FOLDER)
    await expect(inner).toBeVisible()
    await expect(inner.locator('.ui-tree-row')).toHaveAttribute('aria-level', '3')

    // ── A request, made from the inner folder's own menu ───────────────────
    //
    // No ask: a person pressing "new request" has already said what they
    // want, so the request arrives called `Untitled request` and is named in
    // the header once they know better (api-store.ts). The row's `data-rel-
    // path` is the file's path INSIDE the collection, which is the assertion
    // that it was allocated into the folder that was aimed at.
    await inner.click({ button: 'right' })
    await folderMenu.getByRole('menuitem', { name: 'New request' }).click()

    const madeHere = workbench.locator(
      `.api-tree__row[data-rel-path^="${OUTER_FOLDER}/${INNER_FOLDER}/"]`,
    )
    await expect(madeHere).toHaveCount(1)
    await expect(madeHere.locator('.ui-tree-row')).toHaveAttribute('aria-level', '4')

    // ── Named, addressed and SAVED ────────────────────────────────────────
    //
    // The name first and the address second, deliberately: a request names
    // itself from its address while nobody has named it, and renaming spends
    // that offer for good (api-store.ts). Doing it the other way round would
    // have the URL propose a name and this spec would be asserting the
    // proposal rather than the person's own answer.
    await page
      .locator('.api-crumbs__name')
      .getByRole('button', { name: 'Untitled request' })
      .click()
    await page.locator('#api-request-name').fill(SAVED_NAME)
    await page.keyboard.press('Enter')
    await page.locator('#api-url').fill(SAVED_URL)

    // SAVE IS GONE (nocx-2aunx). The draft reaches its file when typing
    // stops, so there is no button to press and nothing to assert enabled.
    // What replaces it is the same guarantee stated as an observable event:
    // Send offers to save first only while the draft is dirty, so the title
    // going back to `Send this request` IS the write having landed. Waiting
    // on that rather than on the coalescing interval is what keeps this spec
    // off a duration — and asserting it before the re-read below is what
    // still makes a later failure read as "the write was refused" rather
    // than "the bytes had not been written yet".
    await expect(workbench.getByRole('button', { name: 'Send', exact: true })).toHaveAttribute(
      'title',
      'Send this request',
      { timeout: 15_000 },
    )

    // ── The re-read, which is what makes the save a claim about the DISK ───
    //
    // `saveDraft` writes the file and does not re-list, so the tree is still
    // showing the name the file had when it was last listed — `Untitled
    // request`. Re-reading the open folders walks the collection again
    // (apicoll's List holds no cache: "a cache would be a second copy of a
    // truth that already has an owner — the files"), so the renamed row is
    // the observable event that says the bytes landed, and its `data-rel-
    // path` says where.
    await workbench.locator('#api-collections-menu').click()
    await page
      .getByTestId('api-collections-menu-popover')
      .getByRole('menuitem', { name: 'Re-read the open folders' })
      .click()

    const saved = treeRow(page, workbench, SAVED_NAME)
    await expect(saved).toBeVisible({ timeout: 10_000 })
    await expect(saved).toHaveAttribute(
      'data-rel-path',
      new RegExp(`^${OUTER_FOLDER}/${INNER_FOLDER}/[^/]+\\.json$`),
    )
    await expect(saved.locator('.ui-tree-row')).toHaveAttribute('aria-level', '4')
    // And the folders came back from the same walk, which is the other half
    // of "the folder is really there": a directory the backend re-listed
    // rather than a row this renderer remembered.
    await expect(treeRow(page, workbench, OUTER_FOLDER).locator('.ui-tree-row')).toHaveAttribute(
      'aria-level',
      '2',
    )
    await expect(treeRow(page, workbench, INNER_FOLDER).locator('.ui-tree-row')).toHaveAttribute(
      'aria-level',
      '3',
    )
  })

  test('the right button on a request row offers its acts, the header’s ⋮ offers the same list, and each aims at its own request', async ({
    page,
  }) => {
    const workbench = await openWorkbench(page)
    const requestMenu = page.getByTestId('api-request-row-menu')

    // ── The row's own menu, where the webview's used to be ────────────────
    //
    // Right-clicking a request handed over reload/save-image-as before this
    // landed. The list is read in full for the reason the folder list is:
    // "two doors, one list" is only checked by comparing the whole thing.
    // `Move to folder…` is the act nocx-8aczn.2 added beside these two.
    await treeRow(page, workbench, ZEN).click({ button: 'right' })
    const fromTheRow = await menuLabels(requestMenu)
    // `Close request` is NOT here, and its absence is the assertion: the act
    // nocx-8aczn.10 added is offered only by a menu aimed at the request
    // that is currently in the form, and this row is not that request. An
    // item to put down something you are not holding is a door onto nothing.
    expect(fromTheRow).toEqual(['Duplicate', 'Move to folder…', 'Delete request…'])

    // ── Duplicate makes a copy that appears in the tree ───────────────────
    await requestMenu.getByRole('menuitem', { name: 'Duplicate' }).click()

    // The copy is DISK TRUTH, not an optimistic row: `duplicateRequest`
    // re-reads the folder before it opens the copy, so this row was drawn
    // from a listing of the directory the file was written into.
    const copy = treeRow(page, workbench, ZEN_COPY)
    await expect(copy).toBeVisible({ timeout: 10_000 })
    // …beside the original, which is still there. A "duplicate" that moved
    // the request would satisfy every assertion above.
    await expect(treeRow(page, workbench, ZEN)).toHaveCount(1)
    await expect(copy).toHaveCount(1)
    // …and it is the copy that is now in the form, which is what makes the
    // act finish where a person expects to carry on working.
    await expect(page.locator('.api-crumbs__name')).toHaveText(ZEN_COPY)

    // ── The header's ⋮ offers the SAME list ───────────────────────────────
    //
    // Compared against the row menu of the OPEN request rather than the one
    // captured above. "Two doors, one list" is a claim about ONE request seen
    // through two surfaces, and the list captured earlier was aimed at a
    // different one — so comparing them would now assert that two doors
    // pointing at two requests offer the same acts, which is not the promise
    // and is not true. Aimed at the copy, both doors carry `Close request`,
    // because for this request there is something to close.
    await copy.click({ button: 'right' })
    const fromTheOpenRow = await menuLabels(requestMenu)
    expect(fromTheOpenRow).toEqual([
      'Duplicate',
      'Move to folder…',
      'Close request',
      'Delete request…',
    ])
    await page.keyboard.press('Escape')
    await expect(requestMenu).toBeHidden()

    await page.locator('#api-request-menu').click()
    expect(await menuLabels(requestMenu)).toEqual(fromTheOpenRow)
    await page.keyboard.press('Escape')
    await expect(requestMenu).toBeHidden()

    // ── Each door aims at its OWN request ─────────────────────────────────
    //
    // The reason the row menu keeps a target of its own rather than reading
    // the open request: "a Delete reading the open request would name one
    // file in its question and remove another". The confirm NAMES what goes,
    // so the question is the assertion — and it is asked about a row that is
    // not the one in the form, which is the only arrangement in which the two
    // answers differ.
    await treeRow(page, workbench, RATE_LIMIT).click({ button: 'right' })
    await requestMenu.getByRole('menuitem', { name: 'Delete request…' }).click()
    const question = page.locator('.nocx-dialog__message')
    await expect(question).toHaveText(
      `Delete ${RATE_LIMIT}? The file is removed from the collection folder.`,
    )
    // CANCELLED. This test is about which request each door aims at; deleting
    // one would be a second act, and the row it removed is the row the next
    // assertion needs.
    await page.getByRole('button', { name: 'Cancel', exact: true }).click()
    await expect(question).toBeHidden()
    await expect(treeRow(page, workbench, RATE_LIMIT)).toHaveCount(1)

    // …while the header's door asks the OPEN request, which is still the copy.
    await page.locator('#api-request-menu').click()
    await requestMenu.getByRole('menuitem', { name: 'Delete request…' }).click()
    await expect(question).toHaveText(
      `Delete ${ZEN_COPY}? The file is removed from the collection folder.`,
    )
    await page.getByRole('button', { name: 'Cancel', exact: true }).click()
    await expect(question).toBeHidden()
    await expect(treeRow(page, workbench, ZEN_COPY)).toHaveCount(1)
  })

  test('the tree marks the request that is open, and the mark moves with it', async ({ page }) => {
    const workbench = await openWorkbench(page)

    /** Every REQUEST row wearing the kit's selected mark. Scoped by
     *  `data-rel-path`, which only a request row carries: a collection row is
     *  marked too, and it means something else — "this is where new requests
     *  go" rather than "this is the one in the form". */
    const marked = workbench.locator('.api-tree__row[data-rel-path] .ui-tree-row[data-selected]')

    // Nothing is open yet, so nothing is marked. Worth asserting: a mark that
    // was always on some row would pass every assertion below.
    await expect(marked).toHaveCount(0)

    await treeRow(page, workbench, ZEN).click()
    // ONE row, and it is that one. The count is what makes it a mark rather
    // than a decoration every row grew.
    await expect(marked).toHaveCount(1)
    await expect(treeRow(page, workbench, ZEN).locator('.ui-tree-row')).toHaveAttribute(
      'data-selected',
      'true',
    )
    // The mark's whole job is to agree with the header, which is the other —
    // and until this landed, only — statement of which request is open. A row
    // marked while the header names a different request would be two owners
    // of one fact, which is the defect this repo pays for most often.
    await expect(page.locator('.api-crumbs__name')).toHaveText(ZEN)

    // ── And it MOVES ──────────────────────────────────────────────────────
    await treeRow(page, workbench, RATE_LIMIT).click()
    await expect(treeRow(page, workbench, RATE_LIMIT).locator('.ui-tree-row')).toHaveAttribute(
      'data-selected',
      'true',
    )
    // The old one let go — asserted as its own line rather than inferred from
    // the count, so a failure says which half broke.
    await expect(treeRow(page, workbench, ZEN).locator('.ui-tree-row')).not.toHaveAttribute(
      'data-selected',
      'true',
    )
    await expect(marked).toHaveCount(1)
    await expect(page.locator('.api-crumbs__name')).toHaveText(RATE_LIMIT)
  })

  test('a file the format does not recognise is a row that says why, and can be deleted', async ({
    page,
  }) => {
    const workbench = await openWorkbench(page)

    // ── The file, seeded on disk AFTER the first listing ──────────────────
    //
    // The malformed row is the product answering a listing that already
    // happened, exactly as a colleague's git pull lands one: drop the bad
    // file on disk, then re-read the open folders the way a person asks the
    // tree to look again (the same door the first test's save uses). The
    // seed is the owner's case — a request with `var` where the format wants
    // `variables` — so the decoder's reason is `json: unknown field "var"`.
    writeFileSync(
      join(collectionsDir(backend.isolatedHome, PLAYGROUND), MALFORMED_NAME),
      '{"var": true}',
    )
    await workbench.locator('#api-collections-menu').click()
    await page
      .getByTestId('api-collections-menu-popover')
      .getByRole('menuitem', { name: 'Re-read the open folders' })
      .click()

    // ── THE ROW IS THERE (not hidden), by its file name ───────────────────
    //
    // Not found by `treeRow`: a malformed row's title is its reason, not its
    // name (the corrected contract on `treeRow`), so this locates the row by
    // its `data-row-key` — `api-tree.ts` builds one for a malformed file as
    // `${handle}:!${relPath}`, and the '!' prefix means it cannot collide
    // with the request or folder key of the same path.
    const malformedKey = workbench.locator(`.api-tree__row[data-row-key$=":!${MALFORMED_NAME}"]`)
    await expect(malformedKey).toHaveCount(1)
    await expect(malformedKey).toBeVisible({ timeout: 10_000 })

    // ── WHAT HOVERING IT SAYS — a person's sentence, not the decoder's ────
    //
    // The name span's `title` carries the row's `hint` (ui/tree-row.tsx),
    // which api-pane.tsx fills with `malformedReason(row.reason)`. Only the
    // CONTAINS is asserted — `var` named, `variables` suggested — so a
    // rewording in malformed-reason.ts is not a red e2e run.
    const hint = await malformedKey.locator('.ui-tree-row__name').getAttribute('title')
    expect(hint).toContain('"var"')
    expect(hint).toContain('"variables"')
    await expect(workbench).not.toContainText(MALFORMED_REASON)

    // ── THE RIGHT BUTTON OFFERS EXACTLY THE TWO FILE ACTS ─────────────────
    const malformedMenu = page.getByTestId('api-malformed-row-menu')
    // The row's own text is STILL the file's name — a row whose visible text
    // were the reason would be a tree that lost the name it lists by.
    await expect(malformedKey.locator('.ui-tree-row__name')).toHaveText(MALFORMED_NAME)
    await malformedKey.click({ button: 'right' })
    expect(await menuLabels(malformedMenu)).toEqual(['Delete…', 'Copy Absolute Path'])

    // ── DELETE: the confirm is asked and answered, and BOTH sides go ──────
    await malformedMenu.getByRole('menuitem', { name: 'Delete…' }).click()
    await expect(page.locator('.nocx-dialog__message')).toHaveText(
      `Delete ${MALFORMED_NAME}? The file is removed from the collection folder.`,
    )
    // The only undo is a working tree somebody may not have committed, so
    // the question is asked before the act — and answered here.
    await page.getByRole('button', { name: 'Delete', exact: true }).click()

    // Both, and each is its own assertion: the row could vanish because the
    // listing broke, and the file could vanish while the row lingered. The
    // row read is on an observable state, the file read on the disk — delete
    // re-lists (api-store.ts's `deleteRequest` refreshes), so the row going
    // from the tree and the file going from the folder are one act's two
    // witnesses.
    await expect(malformedKey).toHaveCount(0)
    await expect
      .poll(() =>
        existsSync(join(collectionsDir(backend.isolatedHome, PLAYGROUND), MALFORMED_NAME)),
      )
      .toBe(false)
    // The seed's good neighbours came back from the same re-listing, which
    // is the other half of "the row vanished because it was deleted": a
    // listing that stopped seeing everything would satisfy the two asserts
    // above.
    await expect(treeRow(page, workbench, ZEN)).toHaveCount(1)
    await expect(treeRow(page, workbench, RATE_LIMIT)).toHaveCount(1)
  })
})

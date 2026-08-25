/**
 * e2e: a request can be put into a folder — nocx-8aczn.4, the epic's happy
 * path as the check that proves it (AGENTS.md testing rule 2).
 *
 * The feature landed as `api.request.move` (nocx-8aczn.1) and the gesture
 * "Move to folder…" on the request row's right-button menu (nocx-8aczn.2).
 * This file is not written by the implementer: it is aimed at the criteria
 * and the product, and reads the on-disk truth rather than the row.
 *
 * WHAT A PERSON DOES, THROUGH THE DOORS THE PRODUCT HAS. Open the workbench.
 * Make a folder (`reports`) from the collection's row menu. Right-click a
 * request (the built-in `Zen`), choose "Move to folder…", pick the folder in
 * the chooser, press "Move here". The request row now sits under `reports`,
 * the open request is still open (its crumb names `reports`), and on disk
 * the file moved from the collection root to `reports/zen.json`. Nothing
 * here calls `api.request.move` or the control plane to arrange a state the
 * gesture is supposed to arrange.
 *
 * THE ASSERTION IS ON BOTH THE TREE AND THE BYTES (the epic's criterion): a
 * tree that re-lists could still lie about a file, and a file that moved
 * could still leave a stale row. So after the row lands under the folder we
 * assert the file is at its new path, byte-for-byte the request nobody
 * edited, and absent from the old one — the backend's word that the move
 * was a rename and not a copy-then-delete.
 *
 * THE REQUEST THAT WAS OPEN IS STILL OPEN. `moveRequest` re-points the
 * selection at the path the result carries and re-reads the moved file
 * (api-store.ts), so the crumb gains a folder segment and the request is the
 * current one. That is asserted as the move's other observable door: a move
 * that quietly closed the form would satisfy the tree but lose the person's
 * place.
 *
 * ITS OWN BACKEND, like the other api-* specs, and for the same reason: the
 * walk reads the collection folder off the isolated home this backend
 * resolved, which is only knowable for a backend the spec owns.
 *
 * NOTHING HERE WAITS ON A DURATION. Every wait is on an observable state — a
 * row in the tree, a dialog gone, a file on disk. A spec that needs a slow
 * machine to pass is broken on a fast one too.
 */
import { test as base, expect, type Locator, type Page } from '@playwright/test'
import { existsSync, mkdtempSync, readFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { bindEndpoint, collectionsDir, VaultBackend, type DisposableRoot } from './harness'
import { readStand } from './stand'

const test = base

/** Lazily, not at module scope: the stand is started by globalSetup. */
const devharnessBin = (): string => readStand().devharness

/**
 * The built-in collection and its seed (internal/apicoll/starter.go). A fresh
 * home gets `Playground` opened with `Zen` and `Rate limit` at its root. They
 * are the setup this spec would otherwise have to build, and the request it
 * moves is `Zen` — a name far enough from the other to tell one row from
 * another after the move.
 */
const PLAYGROUND = 'Playground'
const ZEN = 'Zen'

/** The folder the spec makes and moves into. */
const FOLDER = 'reports'

/**
 * A row in the tree, by the name it shows — the same exact-name contract
 * api-tree.spec.ts uses (the kit sets the name span's title to the row
 * name — or, on a malformed row, to the reason it could not be read, since
 * ui/tree-row.tsx takes a `hint` that api-pane.tsx fills with a malformed
 * file's reason; find one by its `data-row-key` `${handle}:!${relPath}`
 * instead of by title).
 */
function treeRow(page: Page, workbench: Locator, name: string): Locator {
  return workbench
    .locator('.api-tree__row')
    .filter({ has: page.locator(`.ui-tree-row__name[title="${name}"]`) })
}

test.describe('a request can be moved into a folder, and stays open', () => {
  // Three columns — tree, request, runs — all need to be on screen, and the
  // move is a row gesture in the first. The container viewport is narrower
  // than the shipped app's window.
  test.use({ viewport: { width: 1400, height: 900 } })

  let disposable: DisposableRoot
  let backend: VaultBackend

  test.beforeEach(() => {
    disposable = { root: mkdtempSync(join(tmpdir(), 'nocx-e2e-api-move-')) }
    backend = new VaultBackend(devharnessBin(), disposable, true)
  })

  test.afterEach(() => {
    backend?.stop()
  })

  test('make a folder, move a request into it: the row moves, the file moves, the request stays open', async ({
    page,
  }) => {
    const ep = await backend.start()
    await bindEndpoint(page, ep)
    await page.goto('/')
    await expect(page.locator('.nocx-tab-title').first()).not.toHaveText('', { timeout: 15_000 })

    await page.locator('.activity-bar button[data-action="api"]').click()
    const workbench = page.locator('.api-workbench')
    await expect(workbench).toBeVisible({ timeout: 15_000 })
    await expect(treeRow(page, workbench, PLAYGROUND)).toBeVisible({ timeout: 15_000 })
    await expect(treeRow(page, workbench, ZEN)).toBeVisible({ timeout: 15_000 })
    // ── THE REQUEST IS OPEN FIRST — the move's other half is "stays open" ─
    //
    // The criterion is that the request that WAS open is still open
    // afterwards, so this walk opens it before anything else moves: the
    // crumb names it and the form shows its URL. What the move must not do
    // is close the form around it.
    await treeRow(page, workbench, ZEN).click()
    await expect(page.locator('.api-crumbs__name')).toHaveText(ZEN)
    await expect(workbench.locator('#api-url')).toHaveValue('{{baseUrl}}/zen')

    const root = collectionsDir(backend.isolatedHome, PLAYGROUND)
    const oldFile = join(root, 'zen.json')
    const movedFile = join(root, FOLDER, 'zen.json')
    // The bytes the request held before anything moves — asserted as the
    // target of the move, so "the file moved" means "these bytes moved".
    const zenBytes = readFileSync(oldFile)
    expect(zenBytes.length, 'the seeded Zen request should exist on disk').toBeGreaterThan(0)

    // ── THE FOLDER, from the collection row's own menu ────────────────────
    //
    // A collection IS a folder (design §6.1), so the acts are the same acts
    // and the row's right button wears them (api-pane.tsx's rowMenuItems).
    const folderMenu = page.getByTestId('api-folder-row-menu')
    await treeRow(page, workbench, PLAYGROUND).click({ button: 'right' })
    await folderMenu.getByRole('menuitem', { name: 'New folder…' }).click()
    const folderAsk = page.getByRole('dialog').filter({ hasText: `New folder in ${PLAYGROUND}` })
    await expect(folderAsk).toBeVisible()
    await page.locator('#api-new-folder-name').fill(FOLDER)
    await folderAsk.getByRole('button', { name: 'Create folder' }).click()
    // The ask closing IS the success — it stays open holding the name when
    // the backend refuses (collection-dialog.tsx).
    await expect(folderAsk).toBeHidden()
    const folderRow = treeRow(page, workbench, FOLDER)
    await expect(folderRow).toBeVisible({ timeout: 10_000 })
    // A folder inside the collection is depth 2 (the collection is 1).
    await expect(folderRow.locator('.ui-tree-row')).toHaveAttribute('aria-level', '2')

    // ── THE MOVE, through the request row's right-button menu ─────────────
    //
    // The gesture nocx-8aczn.2 added beside Duplicate and Delete. The
    // chooser is a dialog whose radios are the collection's folders and its
    // root; this spec picks the folder it just made.
    const requestMenu = page.getByTestId('api-request-row-menu')
    await treeRow(page, workbench, ZEN).click({ button: 'right' })
    // The menu's list is read in full here: the move item's presence is the
    // product offering the act, and a menu missing it is the door not being
    // there.
    expect(await requestMenu.locator('.ui-context-menu__label').allTextContents()).toEqual([
      'Duplicate',
      'Move to folder…',
      // The act nocx-8aczn.10 added: a request can be put down, not only
      // deleted. Read in full on purpose, so an act appearing is as visible
      // here as an act going missing.
      'Close request',
      'Delete request…',
    ])
    await requestMenu.getByRole('menuitem', { name: 'Move to folder…' }).click()

    const chooser = page.getByRole('dialog').filter({ hasText: `Move ${ZEN} to…` })
    await expect(chooser).toBeVisible()
    // The chooser lists the collection's own folder — its radios carry the
    // folder's path. `data-rel-path` on the FOLDER isn't a thing; the radio
    // is keyed by its value, which for a folder is its path. Assert the
    // folder row is offered and pick it.
    await chooser.getByRole('radio', { name: FOLDER }).check()
    await chooser.getByRole('button', { name: 'Move here' }).click()
    // The chooser closing IS the acceptance — it stays open showing the
    // refusal when the backend refuses (move-dialog.tsx).
    await expect(chooser).toBeHidden()

    // ── THE TREE: the request is under the folder now ─────────────────────
    //
    // `data-rel-path` is what a REQUEST row carries (api-pane.tsx), and it is
    // the request's path WITHIN the collection — asserted rather than derived
    // so the move's own destination is what is shown. The row still shows the
    // name `Zen`.
    const movedRow = treeRow(page, workbench, ZEN)
    await expect(movedRow).toHaveCount(1)
    await expect(movedRow).toHaveAttribute('data-rel-path', `${FOLDER}/zen.json`)
    // The folder row is still there beside it, still depth 2.
    await expect(folderRow.locator('.ui-tree-row')).toHaveAttribute('aria-level', '2')
    // And the old root entry is gone — a request at the root would keep
    // `data-rel-path="zen.json"`, so "no row under the root" is asserted by
    // scope rather than by name. The only `zen.json`-carrying row is under
    // the folder.
    const rootZen = workbench.locator('.api-tree__row[data-rel-path="zen.json"]')
    await expect(rootZen).toHaveCount(0)

    // ── STILL OPEN: the crumb names the folder the file now lives in ──────
    //
    // The open request's crumb trail gains a folder segment between the
    // collection and the name (request-crumbs.tsx), which is the header's own
    // account of where the open request is. This is a move that keeps the
    // person's place rather than closing the form around it.
    await expect(page.locator('.api-crumbs__name')).toHaveText(ZEN)
    await expect(page.locator('.api-crumbs__folder')).toHaveText(FOLDER)

    // ── THE BYTES: the file moved, and the bytes that moved are Zen's ─────
    //
    // The epic's criterion — the assertion is on the file, not just the row.
    // waits on observable disk state: the moved file exists. Its contents are
    // byte-for-byte what the file held before the move (the request was never
    // edited, so nothing should have changed them), and the old path is gone.
    await expect.poll(() => existsSync(movedFile), { timeout: 10_000 }).toBe(true)
    expect(readFileSync(movedFile).equals(zenBytes), 'the moved file should hold Zen').toBe(true)
    expect(existsSync(oldFile), 'the old root file should be gone after the move').toBe(false)

    // And the request is STILL the one in the form — the same URL it opened
    // with, which is the moved file's own content re-read after the move
    // (api-store.ts re-opens the selection at the result's path). A move
    // that closed the form would leave `#api-url` empty or showing a fresh
    // request; the crumb above already says it stayed Zen, and this says the
    // file behind it came along.
    await expect(workbench.locator('#api-url')).toHaveValue('{{baseUrl}}/zen')
  })
})

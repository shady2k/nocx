import { test, expect, promptReady, type Page } from './harness'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

// Tab completion through the real UI (nocx-w7h.2/.3, nocx-4ff.23, completion
// pass 2): a user types a prefix, presses Tab, sees candidates, cycles with
// Tab, and Enter puts the choice in the line. Driven through the real
// transport (OSC 636 snapshot, history.query, fs.complete) against a real
// shell, not a fixture.
//
// FIXTURE DISCIPLINE (nocx-yqmy): every fixture directory is a mkdtemp THIS
// run owns, and the session `cd`s there. Nothing is ever created in the
// developer's home or the harness home's cwd — an earlier run wrote
// zzz-e2e-cmp-* files into the real $HOME and the owner found them offered
// as completions in his terminal.

const INPUT = '.nocx-editor-input'
const DROPDOWN = '.ui-floating-panel[data-variant="completion"]'

/** The row the selection currently sits on. */
const selectedRow = (page: Page) =>
  page.locator(`${DROPDOWN} .ui-floating-panel__row[data-selected="true"]`)

/** A fixture directory this run owns; the session cds into it. */
const fixtureDir = (): string => fs.mkdtempSync(path.join(os.tmpdir(), 'nocx-e2e-cmp-'))

/** cd the session into a fixture dir and wait for the prompt (OSC 7 brings
 *  the new cwd with it). */
const cdInto = async (page: Page, dir: string) => {
  await page.keyboard.type(`cd ${dir}`)
  await page.keyboard.press('Enter')
  await promptReady(page)
}

test.describe('tab completion', () => {
  test('a real command completes: Tab opens the dropdown, arrows pick, Enter inserts', async ({
    page,
  }) => {
    await page.goto('/')
    await expect(page.getByRole('tab')).toHaveCount(1)
    await promptReady(page)

    // `pri` is a prefix of `printf` — a bash BUILTIN, so the OSC 636
    // snapshot (compgen -c) always contains it on this shell. History may
    // add rows; whatever the ranking, `printf` is in the list.
    await page.keyboard.type('pri')
    await page.keyboard.press('Tab')

    // The dropdown opens and the row list includes a command the shell
    // actually has.
    const dropdown = page.locator(DROPDOWN).first()
    await expect(dropdown).toBeVisible({ timeout: 5000 })
    await expect(dropdown).toContainText('printf', { timeout: 5000 })

    // Arrow keys move the selection; Enter accepts whatever row is selected
    // — read its display text first (the info cell, not the row's innerText,
    // which also carries the source badge), so the assertion does not depend
    // on ranking.
    await page.keyboard.press('ArrowDown')
    const chosen = (await selectedRow(page).locator('.ui-collection-row__info').innerText()).trim()
    expect(chosen.length).toBeGreaterThan(0)
    await page.keyboard.press('Enter')
    // The accepted candidate is in the line, and the dropdown is gone —
    // Enter inserted it, nothing was submitted.
    await expect(page.locator(INPUT)).toHaveText(chosen, { timeout: 5000 })
    await expect(dropdown).not.toBeVisible()

    // Nothing was submitted: the shell is still at its prompt.
    await promptReady(page)
  })

  test('ghost text: the top candidate renders inline and Right accepts it', async ({ page }) => {
    await page.goto('/')
    await expect(page.getByRole('tab')).toHaveCount(1)
    await promptReady(page)

    await page.keyboard.type('pri')

    // The inline ghost (the completion tail of the top candidate) appears at
    // the caret. Its content is ranking-dependent; the accept is not.
    const ghost = page.locator(`${INPUT} .nocx-editor-ghost`).first()
    await expect(ghost).toBeVisible({ timeout: 5000 })
    const tail = (await ghost.innerText()).trim()
    expect(tail.length).toBeGreaterThan(0)

    await page.keyboard.press('ArrowRight')
    await expect(page.locator(INPUT)).toHaveText(`pri${tail}`, { timeout: 5000 })
  })

  test('local paths complete through fs.complete — and only on a local session', async ({
    page,
  }) => {
    const fixture = fixtureDir()
    try {
      await page.goto('/')
      await expect(page.getByRole('tab')).toHaveCount(1)
      await promptReady(page)

      // The probe is a DIRECTORY: `cd` takes directories only (the
      // dirs-only table), so a file would be filtered out of its completion
      // and this test would fail for the wrong reason.
      const run = Date.now().toString(36)
      const probe = `zzz-e2e-cmp-${run}-probe`
      fs.mkdirSync(path.join(fixture, probe))

      await cdInto(page, fixture)

      // `cd ./zzz-e2e-cmp-<run>` + Tab: the local path provider asks the
      // backend (fs.complete) and lists the probe. The typed prefix carries
      // the run's own random, so a previous run's recorded history line can
      // never start with it — no cross-provider pollution.
      await page.keyboard.type(`cd ./${probe}`)
      await page.keyboard.press('Tab')
      const dropdown = page.locator(DROPDOWN).first()
      await expect(dropdown).toBeVisible({ timeout: 5000 })
      await expect(dropdown).toContainText(probe, { timeout: 5000 })

      await page.keyboard.press('Enter')
      // A directory keeps its trailing slash.
      await expect(page.locator(INPUT)).toHaveText(`cd ./${probe}/`, { timeout: 5000 })
    } finally {
      fs.rmSync(fixture, { recursive: true, force: true })
    }
  })

  test('no candidates: Tab opens a row that says nothing matched — never silence', async ({
    page,
  }) => {
    await page.goto('/')
    await expect(page.getByRole('tab')).toHaveCount(1)
    await promptReady(page)

    // A prefix that matches no command, no history row and no path.
    await page.keyboard.type('zzznocxe2enope')

    // The command snapshot may still be loading on a slow machine — the
    // honest row names that ("command names are still loading"); retry Tab
    // until the snapshot has arrived and the row says the generic no-match.
    const dropdown = page.locator(DROPDOWN).first()
    await expect
      .poll(
        async () => {
          await page.keyboard.press('Tab')
          await page.waitForTimeout(400)
          if (!(await dropdown.isVisible())) return 'not-visible'
          const text = await dropdown.innerText()
          if (text.includes('Command names are still loading')) {
            // Esc closes exactly the panel; the draft survives.
            await page.keyboard.press('Escape')
            return 'still-loading'
          }
          return text
        },
        { timeout: 20_000, intervals: [1_000] },
      )
      .toContain('No matches')

    // One non-selectable row: never the selected variance, no hint footer.
    const rows = dropdown.locator('.ui-floating-panel__row')
    await expect(rows).toHaveCount(1)
    await expect(rows.first()).toHaveAttribute('data-empty', 'true')
    await expect(rows.first()).not.toHaveAttribute('aria-selected', 'true')
    await expect(page.locator(INPUT)).toHaveText('zzznocxe2enope')
    await page.screenshot({ path: '/tmp/nocx-c3-no-matches.png' })

    // Enter falls through to the editor's submit — nothing was selected and
    // nothing blocked the key; the shell runs the line.
    await page.keyboard.press('Enter')
    await promptReady(page)
  })

  test('cd onto a directory holding only a file names why there is nothing to choose', async ({
    page,
  }) => {
    const fixture = fixtureDir()
    // The owner's exact case: `~/Downloads` holds one entry, a file, and cd
    // takes directories only — zero candidates, and the reason is specific.
    const downloads = path.join(fixture, 'Downloads')
    fs.mkdirSync(downloads)
    fs.writeFileSync(path.join(downloads, 'nocx-backup.enc'), 'x')
    try {
      await page.goto('/')
      await expect(page.getByRole('tab')).toHaveCount(1)
      await promptReady(page)
      await cdInto(page, fixture)

      await page.keyboard.type(`cd ${downloads}/`)
      await page.keyboard.press('Tab')
      const dropdown = page.locator(DROPDOWN).first()
      await expect(dropdown).toBeVisible({ timeout: 5000 })
      const empty = dropdown.locator('.ui-floating-panel__row[data-empty="true"]')
      await expect(empty).toHaveCount(1)
      await expect(empty.first()).toContainText('No subdirectories in Downloads')
      await page.screenshot({ path: '/tmp/nocx-c3-cd-onto-files.png' })

      // Enter is not blocked by the row: the line submits as typed.
      await page.keyboard.press('Enter')
      await promptReady(page)
    } finally {
      fs.rmSync(fixture, { recursive: true, force: true })
    }
  })

  test('erasing the line closes the panel entirely — no footer-only panel', async ({ page }) => {
    const fixture = fixtureDir()
    fs.mkdirSync(path.join(fixture, 'alpha'))
    try {
      await page.goto('/')
      await expect(page.getByRole('tab')).toHaveCount(1)
      await promptReady(page)
      await cdInto(page, fixture)

      await page.keyboard.type('cd ')
      await page.keyboard.press('Tab')
      const dropdown = page.locator(DROPDOWN).first()
      await expect(dropdown).toBeVisible({ timeout: 5000 })
      await expect(dropdown.locator('.ui-floating-panel__row').first()).toContainText('alpha/')

      // Erase the whole line: the panel must CLOSE, not hang showing only
      // its footer.
      await page.keyboard.press('Control+a')
      await page.keyboard.press('Backspace')
      await expect(dropdown).not.toBeVisible({ timeout: 5000 })
      await expect(page.locator(INPUT)).toHaveText('')
      await page.screenshot({ path: '/tmp/nocx-c3-erased-closed.png' })
    } finally {
      fs.rmSync(fixture, { recursive: true, force: true })
    }
  })

  test('acceptance: cd + Tab lists directories with kind and trailing slash, Tab cycles and previews', async ({
    page,
  }) => {
    const fixture = fixtureDir()
    // The owner's exact scenario: a directory containing a file and two
    // subdirectories.
    fs.writeFileSync(path.join(fixture, 'notes.txt'), 'x')
    fs.mkdirSync(path.join(fixture, 'alpha'))
    fs.mkdirSync(path.join(fixture, 'beta'))
    try {
      await page.goto('/')
      await expect(page.getByRole('tab')).toHaveCount(1)
      await promptReady(page)

      await cdInto(page, fixture)

      // `cd ` + Tab — the empty token, the case that used to offer history
      // rows only, every one labelled "history".
      await page.keyboard.type('cd ')
      // The ghost shows the top candidate BEFORE the Tab: what the user is
      // looking at is what the first Tab must settle on (completion pass
      // 4's "the first Tab takes what is shown").
      const ghost = page.locator(`${INPUT} .nocx-editor-ghost`).first()
      await expect(ghost).toBeVisible({ timeout: 5000 })
      const ghostText = (await ghost.innerText()).trim()
      expect(ghostText.length).toBeGreaterThan(0)

      await page.keyboard.press('Tab')
      const dropdown = page.locator(DROPDOWN).first()
      await expect(dropdown).toBeVisible({ timeout: 5000 })

      // The two directories, each marked Directory with a trailing slash;
      // the file is absent. History rows may sit below the paths (the
      // argument rung puts paths first; the argument cap bounds history).
      const rows = dropdown.locator('.ui-floating-panel__row')
      const first = rows.nth(0)
      const second = rows.nth(1)
      await expect(first).toContainText('alpha/')
      await expect(first).toContainText('Directory')
      await expect(second).toContainText('beta/')
      await expect(second).toContainText('Directory')
      await expect(dropdown).not.toContainText('notes.txt')
      // The FIRST Tab SETTLED on the ghosted candidate: the row the
      // dropdown opened on is the row the ghost was showing — it did not
      // advance to the next folder.
      const settled = (
        await selectedRow(page).locator('.ui-collection-row__info').innerText()
      ).trim()
      expect(settled).toBe(ghostText)
      // Let the list settle (a late history batch may still merge in — a
      // list that grows legitimately re-measures), then pin the width.
      await page.waitForTimeout(500)
      const widthAtOpen = (await dropdown.boundingBox())!.width
      await page.screenshot({ path: '/tmp/nocx-c4-tab-settled.png' })

      // The next Tab moves to the second candidate and previews it in the
      // line — and the panel's width does not change between the presses
      // (the owner's "every Tab press makes the window narrower").
      await page.keyboard.press('Tab')
      await expect(second).toHaveAttribute('aria-selected', 'true')
      const ghost2 = page.locator(`${INPUT} .nocx-editor-ghost`).first()
      await expect(ghost2).toContainText('beta/')
      const widthAfterCycle = (await dropdown.boundingBox())!.width
      expect(widthAfterCycle).toBe(widthAtOpen)
      await page.screenshot({ path: '/tmp/nocx-c4-tab-cycled.png' })

      // Content-sized: the panel hugs its rows, it never spans the pane.
      const box = await dropdown.boundingBox()
      const pane = await page.locator('.pane.active').first().boundingBox()
      expect(box).not.toBeNull()
      expect(pane).not.toBeNull()
      expect(box!.width).toBeLessThan(pane!.width * 0.75)
      expect(box!.width).toBeGreaterThanOrEqual(300)
      expect(box!.width).toBeLessThanOrEqual(640)

      // Screenshot — the acceptance evidence the owner asked for.
      await page.screenshot({ path: '/tmp/nocx-c3-acceptance.png' })

      // Enter accepts the cycled-to candidate; nothing was submitted.
      await page.keyboard.press('Enter')
      await expect(page.locator(INPUT)).toHaveText('cd beta/', { timeout: 5000 })
      await expect(dropdown).not.toBeVisible()
    } finally {
      fs.rmSync(fixture, { recursive: true, force: true })
    }
  })

  test('reports 2-4: last-segment rows, a readable match chip, and no clipped glyphs', async ({
    page,
  }) => {
    const fixture = fixtureDir()
    // The owner's session: a multi-level path with a long directory name —
    // the case that used to repeat `repos/meshynet/` in every row and clip
    // `repos/meshynet/graphify-ou…` at the panel's ceiling.
    fs.mkdirSync(path.join(fixture, 'repos', 'meshynet'), { recursive: true })
    fs.mkdirSync(path.join(fixture, 'repos', 'meshynet', 'bin'))
    fs.mkdirSync(path.join(fixture, 'repos', 'meshynet', 'graphify-output'))
    try {
      await page.goto('/')
      await expect(page.getByRole('tab')).toHaveCount(1)
      await promptReady(page)
      await cdInto(page, fixture)

      // ── report 3: the row shows the LAST SEGMENT, never the typed
      //    prefix repeated — `graphify-output/`, not
      //    `repos/meshynet/graphify-output/`. Typing a PARTIAL segment
      //    (`gr`) also leaves a match to assert report 2 against. ──
      await page.keyboard.type('cd repos/meshynet/gr')
      await page.keyboard.press('Tab')
      const dropdown = page.locator(DROPDOWN).first()
      await expect(dropdown).toBeVisible({ timeout: 8000 })
      const rows = dropdown.locator('.ui-floating-panel__row')
      await expect(rows.first()).toBeVisible({ timeout: 5000 })
      // A PARTIAL segment (`gr`) narrows the listing to the matching entry:
      // one row, its last segment shown — never `repos/meshynet/…` repeated.
      await expect(rows.nth(0)).toContainText('graphify-output/')
      await expect(dropdown).not.toContainText('repos/meshynet/graphify-output')
      const box = await dropdown.boundingBox()
      expect(box).not.toBeNull()
      // Content-sized and never the pane (report 5's rule, this shape).
      const pane = await page.locator('.pane.active').first().boundingBox()
      expect(box!.width).toBeLessThan(pane!.width * 0.75)
      expect(box!.width).toBeLessThanOrEqual(640)
      console.log(`E2E completion panel width: ${box!.width}px (report 3 shape)`)

      // ── report 2: the match is a real highlight, asserted from the
      //    browser's own computed styles.
      //
      //    The channel changed on 2026-08-03 and the assertion follows it.
      //    It used to be a background wash, which sits BEHIND the row's own
      //    glyphs: raising its alpha to make it brighter darkens the
      //    letters on it, and measured across the theme catalogue there is
      //    no alpha that reads as emphasis (40% leaves 3.45:1, 55% is
      //    already 2.44:1). The accent went into the TEXT instead. So the
      //    proof is that the matched glyphs differ in COLOUR from the row's
      //    text — and that <mark>'s user-agent background is off, which is
      //    a real trap: dropping the background declaration does not remove
      //    a background, it reveals the browser's yellow one. ──
      const match = rows.nth(0).locator('mark.ui-floating-panel__match').first()
      await expect(match).toBeVisible({ timeout: 5000 })
      await expect(match).toHaveText('gr')
      const [markColor, rowColor, markBg] = await match.evaluate((el) => {
        const mark = el as HTMLElement
        const row = mark.closest('.ui-floating-panel__row') as HTMLElement
        return [
          getComputedStyle(mark).color,
          getComputedStyle(row).color,
          getComputedStyle(mark).backgroundColor,
        ]
      })
      expect(markColor).not.toBe(rowColor)
      expect(markBg).toBe('rgba(0, 0, 0, 0)')
      console.log(`E2E match: colour ${markColor} vs row ${rowColor}, bg ${markBg} (report 2)`)

      // ── report 4: a row longer than the ceiling ellipsises inside the
      //    panel — the panel never grows past the ceiling to fit it. ──
      const infoOverflow = await rows
        .nth(0)
        .locator('.ui-collection-row__info')
        .evaluate((el) => getComputedStyle(el as HTMLElement).textOverflow)
      expect(infoOverflow).toBe('ellipsis')
      await page.screenshot({ path: '/tmp/nocx-reports-234.png' })
    } finally {
      fs.rmSync(fixture, { recursive: true, force: true })
    }
  })
})

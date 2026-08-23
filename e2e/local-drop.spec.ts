/**
 * The local branch of the drop gesture (nocx-9le.5.23): a person drops a file
 * onto the terminal of a LOCAL tab in a browser, and it arrives in that tab's
 * current directory.
 *
 * ## Why this spec exists at all
 *
 * The remote branch is the epic's happy path and has e2e/upload.spec.ts. The
 * local branch was judged trivial twice and was broken twice in one session,
 * both times found by the owner running the product and both times with every
 * unit test green. First it inserted a bare base name where D9 promised a
 * path; then it REFUSED an upload D9 should never have refused, because D9
 * had been reasoned from the desktop build and was simply wrong about the
 * browser.
 *
 * Both times the unit tests were correct and useless: they asserted
 * conformance to D9, and D9 was the thing that was wrong. A unit test cannot
 * notice that the rule it checks describes the wrong world. Only a check that
 * watches the product from outside can, which is this one.
 *
 * ## Which half of D9 this watches, and which half nothing here can
 *
 * D9 reads: whoever has the path inserts it, whoever has only the bytes
 * uploads them. A browser drop yields a `File` — a name, a size and no
 * location — so there is nothing to insert and the bytes go into the tab's
 * cwd, on the backend's own machine, which is the machine that tab's shell is
 * on. That is the half `make dev-web` and a networked backend use, it is the
 * half that broke, and it is the half asserted below.
 *
 * THE PATH-INSERT HALF CANNOT BE WATCHED HERE. It needs the Wails runtime
 * handing Go a real OS path, and the headless stand is cmd/devharness plus
 * vite — there is no Wails in it, so nothing in this repository sees that
 * happen. It is covered by unit tests on both sides of the wire and it
 * remains a MANUAL step on the desktop app. Said here, where the next reader
 * of this spec is looking, rather than only in a design document they are not
 * reading: the failure AGENTS.md records is a gap that was known and
 * unrecorded.
 *
 * ## Two assertions, and the second is the one that counts
 *
 * A row in the Files tree proves a directory listing. Only the bytes prove an
 * upload, so the file is read back and its SHA-256 compared with what was
 * dropped. The payload deliberately crosses the transfer's 256 KiB chunk
 * boundary: a one-chunk file cannot tell a working loop from one that sends
 * its first chunk and stops.
 *
 * The local case is easier than the remote in exactly one way, and this spec
 * spends the whole saving: the destination is the harness's own filesystem,
 * so `fs` reads it back directly. No sshd fixture is started, because nothing
 * here is remote and a second machine would only be a slower way to ask the
 * same question.
 */
import { createHash, randomBytes } from 'node:crypto'
import { existsSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import path from 'node:path'

import { dropFileOnActivePane } from './drop-gesture'
import { test, expect, promptReady } from './harness'
import { readStand } from './stand'

const FILES_PANEL = '[data-testid="files-panel"]'
const TREE_ROW = '.ui-tree-row'

/** 256 KiB is the transfer's chunk; the payload is deliberately larger. */
const PAYLOAD_BYTES = 256 * 1024 + 4096

test('a file dropped on a local tab arrives in that tab’s directory', async ({ page }) => {
  // A cold renderer, a shell that has to start and report OSC 7, and a
  // transfer do not reliably fit the suite's 30 s default. Every WAIT inside
  // is still on observable state; this is only the ceiling.
  test.setTimeout(120_000)

  const stand = readStand()
  // INSIDE the stand's disposable home, which is the whole point of the
  // directory: the drop writes real bytes to real disk, and the backend's own
  // cwd is the CHECKOUT. A local tab that has not been told otherwise would
  // put the file there. Unique per run so a rerun cannot read the previous
  // run's directory listing.
  const destDir = mkdtempSync(path.join(stand.home, 'local-drop-'))
  const destBase = path.basename(destDir)

  try {
    await page.goto('/')
    await promptReady(page)

    // ── The premise of the gesture: this tab knows where it is ─────────────
    // An upload refuses an UNVERIFIED cwd (terminal-drop.ts) — putting a file
    // into a guessed directory is the one outcome worth refusing a gesture
    // over, on this machine as much as on anybody else's. So the drop cannot
    // be attempted until the shell's OSC 7 has landed, and the cwd chip is
    // where the renderer says it has. Asserted separately from the panel so a
    // failure here reads as "the shell never reported its directory" rather
    // than as a tree bug.
    await page.keyboard.type(`cd ${destDir}`)
    await page.keyboard.press('Enter')
    await expect(page.locator('.pane.active .nocx-editor-cwd')).toContainText(destBase, {
      timeout: 60_000,
    })

    // The Files panel is open on Files from cold start, roots at `/`, and
    // REVEALS the tab's cwd — which is what puts the destination directory's
    // rows on screen for the assertion below. `data-selected` is the reveal's
    // own account of having landed, and it is only set for a cwd the frontend
    // VERIFIED — so it is also the strongest observable that the drop's
    // precondition holds.
    const panel = page.locator(FILES_PANEL)
    await expect(panel).toBeVisible({ timeout: 15_000 })
    await expect(panel).toHaveAttribute('data-root', '/', { timeout: 30_000 })
    const destRow = page.locator(TREE_ROW, { hasText: destBase })
    await expect(destRow).toHaveAttribute('data-selected', 'true', { timeout: 60_000 })
    await expect(destRow).toHaveAttribute('data-disclosure', 'expanded', { timeout: 30_000 })

    // ── The gesture ───────────────────────────────────────────────────────
    const payload = randomBytes(PAYLOAD_BYTES)
    const expected = createHash('sha256').update(payload).digest('hex')
    const fileName = `dropped-${Date.now()}.bin`
    // Shared with e2e/upload.spec.ts. The gesture is one behaviour and has
    // one owner; what differs between the two specs is where the bytes are
    // then looked for, which is the part each is actually about.
    await dropFileOnActivePane(page, fileName, payload)

    // ── 1: the row appears, with nobody pressing anything ─────────────────
    await expect(page.locator('.ui-tree-row__name').filter({ hasText: fileName })).toBeVisible({
      timeout: 60_000,
    })

    // ── 2: and the bytes on disk are the bytes that were sent ─────────────
    // The destination is this process's own filesystem — the backend runs
    // here — so the far side is readable directly and no fixture server is
    // needed to ask it. Polled rather than read once: a state that arrives,
    // not a duration waited out.
    const dest = path.join(destDir, fileName)
    await expect
      .poll(
        () =>
          existsSync(dest) ? createHash('sha256').update(readFileSync(dest)).digest('hex') : null,
        { timeout: 30_000 },
      )
      .toBe(expected)
    expect(readFileSync(dest).byteLength).toBe(PAYLOAD_BYTES)
  } finally {
    // The stand's home is shared by every spec in the run AND by both browser
    // projects, so what this spec made is this spec's to remove.
    rmSync(destDir, { recursive: true, force: true })
  }
})

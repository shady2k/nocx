/**
 * The epic's happy path (nocx-9le.5.11): a person drops a file onto the
 * terminal of an SSH tab and the file arrives on that host.
 *
 * Eleven tasks of this epic are merged and every suite is green, and none of
 * that is evidence a person can upload a file — each of those tests was
 * written by whoever wrote the layer it tests, against that person's own fake.
 * This spec is written from the outside: it opens a real SSH tab against a
 * real sshd, performs the gesture a browser performs, and then asks the FAR
 * SIDE what it holds.
 *
 * ## Two assertions, and the second is the one that counts
 *
 * A row in the Files tree proves a directory listing. Only the bytes prove an
 * upload — so the file is read back from the fixture's filesystem and its
 * SHA-256 compared with what was dropped. The payload deliberately crosses the
 * transfer's 256 KiB chunk boundary: a one-chunk file cannot tell a working
 * loop from one that sends its first chunk and stops.
 *
 * ## Which source this exercises, and why there is no choice
 *
 * The STREAM source — the renderer holds the bytes. That is forced, not
 * chosen: the suite runs the headless stand (cmd/devharness plus vite), where
 * there is no Wails runtime, therefore no EnableFileDrop and no backend-minted
 * source ticket. Playwright builds a `File` and a `DataTransfer` in the page,
 * which is exactly what a browser drop produces, so the source this watches is
 * the source `dev-web` uses.
 *
 * THE PATH SOURCE HAS NO END-TO-END CHECK, HERE OR ANYWHERE. It is covered by
 * unit tests on both sides of the wire; what nothing in this repository can
 * watch is Wails handing Go a real OS path, and that remains a manual step on
 * the desktop app. Written down rather than left implied — the failure
 * AGENTS.md records is a gap that was known and unrecorded.
 *
 * ## Why the fixture gets its own HOME and its own CWD
 *
 * Both are stated in e2e/sshd-fixture.ts. The short version: sharing the
 * backend's HOME makes the remote shell source the LOCAL integration by
 * accident, and inheriting Playwright's cwd would make the tab's cwd the
 * checkout — which is where the dropped file would then land.
 */
import { createHash, randomBytes } from 'node:crypto'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import path from 'node:path'

import { dropFileOnActivePane } from './drop-gesture'
import { test, expect } from './harness'
import { readStand } from './stand'
import { rpc, startSshd, type SshdFixture } from './sshd-fixture'

const FILES_PANEL = '[data-testid="files-panel"]'
const TREE_ROW = '.ui-tree-row'

/** 256 KiB is the transfer's chunk; the payload is deliberately larger. */
const PAYLOAD_BYTES = 256 * 1024 + 4096

test('a file dropped on an SSH tab arrives on the far host', async ({ page }) => {
  // The whole journey — a go build, a real SSH handshake, an SFTP publish of
  // the integration bundle, a login shell and a transfer — does not fit the
  // suite's 30 s default. Every WAIT inside is still on observable state.
  test.setTimeout(180_000)

  const stand = readStand()
  // Beside the backend's disposable home, never inside it: this is a second
  // machine's home and the two must not share ~/.nocx. Unique per run so a
  // rerun cannot read the previous run's directory listing.
  const remoteHome = mkdtempSync(path.join(path.dirname(stand.home), 'upload-remote-'))
  const fixture: SshdFixture = await startSshd({ home: remoteHome, cwd: remoteHome })
  let profileId: string | null = null

  try {
    // The backend's ssh client must accept this spawn's host key. REPLACED,
    // not appended: every spawn mints a fresh key and a stale line for a dead
    // one makes the backend refuse the connection.
    const sshDir = path.join(stand.home, '.ssh')
    mkdirSync(sshDir, { recursive: true, mode: 0o700 })
    writeFileSync(path.join(sshDir, 'known_hosts'), fixture.knownHosts + '\n')

    await page.goto('/')
    await expect(page.locator('.nocx-tab')).toHaveCount(1, { timeout: 15_000 })

    // Seed the connection the way Settings would. The name is unique per run:
    // the stand's document store persists across runs in this home, and a
    // stale profile would dial a dead fixture.
    const profileName = `e2e-upload-${Date.now()}`
    const created = await rpc<{ id: string }>(page, stand, 'profiles.create', {
      type: 'ssh',
      name: profileName,
      options: {
        host: fixture.host,
        port: fixture.port,
        user: 'e2e',
        keyPath: fixture.userKey,
      },
    })
    profileId = created.id

    // Open it the way a person does: quick connect finds the saved profile and
    // Enter opens it (a file-based key needs no vault preflight).
    await page.keyboard.press('Control+Shift+P')
    const search = page.locator('.quick-connect__search input')
    await expect(search).toBeVisible({ timeout: 10_000 })
    await search.fill(profileName)
    await page.keyboard.press('Enter')

    // ── The premise of the gesture: this tab knows where it is ─────────────
    // An upload refuses an UNVERIFIED cwd (terminal-drop.ts) — putting a file
    // into a guessed directory on somebody's server is the one outcome worth
    // refusing a gesture over. So the drop cannot even be attempted until the
    // remote shell's OSC 7 has landed, and the cwd chip is where the renderer
    // says it has. Asserted separately from the panel so a failure here reads
    // as "the shell never reported its directory" rather than as a tree bug.
    const remoteBase = path.basename(remoteHome)
    await expect(page.locator('.pane.active .nocx-editor-cwd')).toContainText(remoteBase, {
      timeout: 90_000,
    })

    // The Files panel is open on Files from cold start; it rescopes to the SSH
    // session, roots at `/`, and REVEALS the tab's cwd — which is what puts
    // the destination directory's rows on screen for the assertion below.
    // `data-selected` is the reveal's own account of having landed, and it is
    // only set for a cwd the frontend VERIFIED.
    const panel = page.locator(FILES_PANEL)
    await expect(panel).toBeVisible({ timeout: 15_000 })
    await expect(panel).toHaveAttribute('data-root', '/', { timeout: 30_000 })
    const remoteRow = page.locator(TREE_ROW, { hasText: remoteBase })
    await expect(remoteRow).toHaveAttribute('data-selected', 'true', { timeout: 60_000 })
    await expect(remoteRow).toHaveAttribute('data-disclosure', 'expanded', { timeout: 30_000 })

    // ── The gesture ───────────────────────────────────────────────────────
    const payload = randomBytes(PAYLOAD_BYTES)
    const expected = createHash('sha256').update(payload).digest('hex')
    const fileName = `dropped-${Date.now()}.bin`
    // The gesture is e2e/drop-gesture.ts — one owner, shared with the local
    // branch's spec, which asserts against a different filesystem entirely.
    await dropFileOnActivePane(page, fileName, payload)

    // ── 1: the row appears, with nobody pressing anything ─────────────────
    await expect(page.locator('.ui-tree-row__name').filter({ hasText: fileName })).toBeVisible({
      timeout: 60_000,
    })

    // ── 2: and the bytes on the far side are the bytes that were sent ─────
    // Read from the filesystem the fixture's SFTP server serves, which is the
    // far side: `startSFTP` operates on the real filesystem exactly as
    // sshd's internal-sftp does. Polled rather than read once — a state that
    // arrives, not a duration waited out.
    const dest = path.join(remoteHome, fileName)
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
    // projects: a profile left behind becomes the next spec's starting state,
    // and quick-connect.spec.ts asserts the saved-server list is empty.
    if (profileId !== null) {
      try {
        await rpc(page, readStand(), 'profiles.delete', { id: profileId })
      } catch {
        // A cleanup that throws would replace the real failure with its own.
      }
    }
    fixture.proc.kill('SIGKILL')
  }
})

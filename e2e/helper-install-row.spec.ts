/**
 * e2e: a first open's helper install is ONE row in Operations for its whole
 * duration, it says what it is doing over a slow link, Cancel ends the open,
 * and a repeat open — the helper already complete — shows no row at all
 * (nocx-3an1s.2.10, ADR-8eej3e66 §8).
 *
 * THE THREE CONDITIONS THIS PROVES, and why each needs the real thing:
 *
 *  1. "for its whole duration" is not a threshold. The install is made slow
 *     by the FIXTURE's own SFTP pacing and is releaseable from the spec, so
 *     the row is asserted while the transfer is provably mid-flight — and
 *     `transferredMB` waits for a byte count that, at the fixture's rate,
 *     cannot arrive inside the two-second call threshold. A row that only
 *     appeared past some timer would be caught here, and so would the
 *     open's own "Opening your terminal" row doubling it: the list is
 *     asserted to hold exactly one row.
 *  2. Cancel ends the open AND leaves nothing trusted. A cancelled install
 *     leaves a markerless tree, and the completeness check is the marker —
 *     so the spec asserts no completed helper in the fixture account's home
 *     and then opens again and watches the install happen again.
 *  3. A complete install shows no row. The assertion is on the WIRE: every
 *     operation.row frame the product's socket carries is recorded, and the
 *     third open must add none. A DOM-only negative check could miss a row
 *     that came and went.
 *
 * The fixture is `cmd/e2e-sshd` — the real sshd, the real SFTP subsystem,
 * the real pty — and the backend is `cmd/nocx-server`, so the install, the
 * consent ask, the cancellation and the second install are the product's own
 * code paths.
 */
import path from 'node:path'
import { existsSync, mkdirSync, mkdtempSync, readdirSync, rmSync, writeFileSync } from 'node:fs'

import {
  appReadyForInput,
  expect,
  promptReady,
  resolveBackend,
  showSidebarView,
  test,
  type BackendEndpoint,
  type Page,
} from './harness'
import { rpc, startSshd, type SshdFixture } from './sshd-fixture'
import { readStand } from './stand'

const TAB = '.nocx-tab'
const ROW = '.ui-operation-row'
const COPYING = 'Copying the nocx helper'

/** The fixture's slow link: 0.5 MB/s while the gate file is absent. A 15.3 MB
 *  helper then takes ~30 s, which is "minutes" for the purpose of a person
 *  looking at a row and comfortably longer than any threshold in the product. */
const SLOW_BYTES_PER_SECOND = 500_000

/** How much must have crossed the link before the row is judged — at the
 *  fixture's rate this cannot happen inside two seconds, which is what makes
 *  "the row exists past the call threshold, and is still the only row" a
 *  state rather than a sleep. */
const PAST_THE_THRESHOLD_MB = 1.5

function seedKnownHost(fixture: SshdFixture): void {
  const sshDir = path.join(readStand().home, '.ssh')
  mkdirSync(sshDir, { recursive: true, mode: 0o700 })
  writeFileSync(path.join(sshDir, 'known_hosts'), `${fixture.knownHosts}\n`)
}

async function openProfile(page: Page, name: string): Promise<void> {
  await page.keyboard.press('Control+Shift+P')
  const search = page.locator('.quick-connect__search input')
  await expect(search).toBeVisible({ timeout: 10_000 })
  await search.fill(name)
  await expect(page.locator('.quick-connect__item', { hasText: name })).toBeVisible({
    timeout: 10_000,
  })
  await page.keyboard.press('Enter')
}

/** The connect-time ask (ADR-0069): a saved AUTO connection to a machine with
 *  no answer is asked before it may use the helper, and choosing Helper is
 *  what puts the binary there. */
async function answerWithHelper(page: Page): Promise<void> {
  const dialog = page.getByRole('dialog').filter({ hasText: 'Choose how nocx connects' })
  await expect(dialog).toBeVisible({ timeout: 30_000 })
  await dialog.getByRole('radio', { name: 'Helper' }).click()
  await dialog.getByRole('button', { name: 'Continue' }).click()
  await expect(dialog).not.toBeVisible()
}

/** Completed helper installs under the fixture account's home. The remote
 *  filesystem is this one, so this is a far-side fact read locally — the
 *  same reading `remote-coordinator-reclaim.spec.ts` does for its markers. */
function completeHelpers(home: string): number {
  const root = path.join(home, '.nocx', 'helper')
  if (!existsSync(root)) return 0
  let complete = 0
  for (const entry of readdirSync(root, { withFileTypes: true })) {
    if (!entry.isDirectory()) continue
    if (existsSync(path.join(root, entry.name, '.install-complete'))) complete++
  }
  return complete
}

/** Every operation.row title the product's socket carried, in order. Attached
 *  before navigation, because the socket the product opens is the one that
 *  will carry them. */
function recordOperationRows(page: Page): string[] {
  const titles: string[] = []
  page.on('websocket', (socket) => {
    socket.on('framereceived', (data) => {
      const text = typeof data.payload === 'string' ? data.payload : data.payload.toString('utf8')
      if (!text.startsWith('{')) return
      let frame: { method?: string; params?: { title?: unknown } }
      try {
        frame = JSON.parse(text) as { method?: string; params?: { title?: unknown } }
      } catch {
        return
      }
      if (frame.method === 'operation.row' && typeof frame.params?.title === 'string') {
        titles.push(frame.params.title)
      }
    })
  })
  return titles
}

/** The transferred amount in MB, read off the row's own progress line.
 *  format-bytes.ts words it as "1.2 MB of 15.3 MB". */
async function transferredMB(row: ReturnType<Page['locator']>): Promise<number> {
  const text = (await row.locator('.ui-operation-row__progress').textContent()) ?? ''
  const match = /([\d.]+) (B|kB|MB|GB)/.exec(text)
  if (match === null) return 0
  const value = Number(match[1])
  const unit = match[2]
  if (unit === 'B') return value / 1_000_000
  if (unit === 'kB') return value / 1_000
  if (unit === 'GB') return value * 1_000
  return value
}

test('a first open installs the helper as one row, cancellable, and a repeat open takes none', async ({
  page,
}) => {
  test.setTimeout(420_000)

  const scratch = mkdtempSync(path.join(readStand().home, 'nocx-install-row-'))
  const remoteHome = path.join(scratch, 'home')
  const remoteCwd = path.join(scratch, 'cwd')
  mkdirSync(remoteHome, { recursive: true, mode: 0o700 })
  mkdirSync(remoteCwd, { recursive: true, mode: 0o700 })
  // Absent: the link is slow. Created when the spec has seen what it needs.
  const fast = path.join(scratch, 'full-speed')

  let fixture: SshdFixture | null = null
  let profileId: string | null = null
  let endpoint: BackendEndpoint | null = null
  const profileName = `e2e-install-row-${Date.now()}`
  const rowTitles = recordOperationRows(page)

  try {
    fixture = await startSshd({
      home: remoteHome,
      cwd: remoteCwd,
      slowSftp: { until: fast, bytesPerSecond: SLOW_BYTES_PER_SECOND },
    })
    seedKnownHost(fixture)

    await page.goto('/')
    await expect(page.locator(TAB)).toHaveCount(1, { timeout: 15_000 })
    await appReadyForInput(page)
    endpoint = await resolveBackend(page)

    const created = await rpc<{ id?: string }>(page, endpoint, 'profiles.create', {
      type: 'ssh',
      name: profileName,
      options: {
        host: fixture.host,
        port: fixture.port,
        user: fixture.user,
        keyPath: fixture.userKey,
      },
    })
    profileId = created.id ?? null
    expect(profileId).not.toBeNull()

    // The list a person watches, before anything is running.
    await page.locator('button[data-view="operations"]').click()
    await expect(page.getByTestId('operations-panel')).toContainText('Nothing is running')

    // ── the first open: the install's own row, for its whole duration ────
    await openProfile(page, profileName)
    await answerWithHelper(page)

    // THE PANEL IS THE TAB'S (ADR-x7cvh8e1), and the open put a NEW tab in
    // front — one that has never opened a panel, so the rail is collapsed and
    // the list above went off screen with it. A person watching the install
    // clicks Operations again on the tab they are now in; this does the same.
    await showSidebarView(page, 'operations')

    const row = page.locator(ROW).filter({ hasText: COPYING })
    await expect(row).toHaveCount(1, { timeout: 30_000 })
    await expect(row).toContainText('@')
    await expect(row).toContainText(fixture.host)

    // It is DOING something, and how far it has got, while it waits: the
    // fixture's slow link guarantees these numbers are still moving.
    await expect
      .poll(() => transferredMB(row), { timeout: 120_000, intervals: [200] })
      .toBeGreaterThanOrEqual(PAST_THE_THRESHOLD_MB)

    // ONE row, and it is the install's: past the call threshold the person's
    // own "Opening your terminal" row would otherwise double it.
    await expect(page.locator(ROW)).toHaveCount(1)
    await expect(page.locator(ROW)).toContainText(COPYING)
    await expect(page.locator(TAB)).toHaveCount(2)

    // ── a person's Cancel ────────────────────────────────────────────────
    await page.getByRole('button', { name: `Cancel ${COPYING}` }).click()
    // The row goes, and so does the tab that was opening: the open ended
    // cleanly rather than failing into a notice.
    await expect(page.locator(ROW)).toHaveCount(0, { timeout: 60_000 })
    await expect(page.getByTestId('operations-panel')).toContainText('Nothing is running')
    await expect(page.locator(TAB)).toHaveCount(1, { timeout: 60_000 })
    expect(await page.locator('.pane-error').count()).toBe(0)
    // AND NOTHING A LATER OPEN COULD TRUST: the tree may exist, the marker
    // (the only thing the completeness check reads) does not.
    await expect
      .poll(() => completeHelpers(remoteHome), { timeout: 60_000, intervals: [200] })
      .toBe(0)

    // ── the next open installs again, and finishes ────────────────────────
    await openProfile(page, profileName)
    // No consent ask the second time: the machine's answer is recorded.
    await expect(
      page.getByRole('dialog').filter({ hasText: 'Choose how nocx connects' }),
    ).toHaveCount(0)
    // This open brought its own tab to the front as well: the same click.
    await showSidebarView(page, 'operations')
    const second = page.locator(ROW).filter({ hasText: COPYING })
    await expect(second).toHaveCount(1, { timeout: 30_000 })
    await expect(second.locator('.ui-operation-row__percent')).toBeVisible()

    // The link comes up to speed; the same install the row is describing
    // finishes, the row goes, and the pane arrives.
    writeFileSync(fast, '')
    await expect(page.locator(ROW)).toHaveCount(0, { timeout: 120_000 })
    await expect(page.locator(TAB)).toHaveCount(2, { timeout: 120_000 })
    await promptReady(page)
    await expect
      .poll(() => completeHelpers(remoteHome), { timeout: 60_000, intervals: [200] })
      .toBe(1)

    // ── a repeat open: no row at all ─────────────────────────────────────
    const rowsBefore = rowTitles.length
    await openProfile(page, profileName)
    await expect(page.locator(TAB)).toHaveCount(3, { timeout: 120_000 })
    await promptReady(page)
    // Opened on the new tab too, or "no row" would be a claim about a closed
    // panel rather than about the wire — and `rowTitles` (below) records what
    // the LIST drew.
    await showSidebarView(page, 'operations')
    await expect(page.locator(ROW)).toHaveCount(0)
    // The wire, not the DOM: an install row that appeared and vanished
    // between two looks could not hide here.
    expect(rowTitles.slice(rowsBefore)).toEqual([])
  } finally {
    if (fixture !== null) {
      fixture.proc.kill()
      await new Promise<void>((resolve) => {
        if (fixture?.proc.exitCode !== null) resolve()
        else fixture?.proc.once('exit', () => resolve())
      })
    }
    if (endpoint !== null && profileId !== null) {
      await rpc(page, endpoint, 'profiles.delete', { id: profileId }).catch(() => {})
    }
    rmSync(scratch, { recursive: true, force: true })
  }
})

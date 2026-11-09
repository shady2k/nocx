/**
 * e2e: a slow ssh open SAYS what it is doing in the pane, offers Cancel, and
 * draws no cursor until the pane has a grid to draw it in (nocx-q9od5).
 *
 * The owner, on a nightly: "opening an ssh connection shows an empty pane for
 * 20 to 30 seconds with only a blinking cursor in the top-left corner, cut by
 * the pane's top edge, then works." The 20 to 30 seconds are the helper's
 * upload — 15.3 MB over their link, remeasured here on 2026-11-08 as the one
 * step of an open that can take that long (install 1.15 s over loopback and
 * 30 s at the fixture's 0.5 MB/s, against every other stage at 150 ms or
 * less). The blank pane was true and unusable, and the cursor was the
 * painter's own overlay placed before any frame had given it a grid.
 *
 * WHAT THIS SPEC PROVES, and each clause is a state rather than a sleep:
 *
 *  1. The pane states the wait, in the pane, within three seconds of the
 *     click. The fixture's link is paced at 0.5 MB/s and released by this
 *     spec, so "the install is still running" is a fact it controls — a card
 *     that only appeared after some timer, or only after the install, is
 *     caught.
 *  2. It names the same work the Operations row names, and its byte count
 *     MOVES: the number asserted is one that cannot have arrived inside the
 *     three seconds the card had to appear.
 *  3. Cancel is the person's, and it ends the open: the tab goes, nothing is
 *     left trusted on the host, and the pane has no error to read.
 *  4. The cursor is NOT drawn while the pane has no grid: the painter's
 *     overlay is hidden from the first sample of the wait until a frame with
 *     a measured grid arrives. This is the clause the owner's cut cursor
 *     fails, and it is asserted on the ELEMENT rather than by pixel, because
 *     "drawn" for an absolutely positioned overlay is its own box.
 *  5. Once the session is up the cursor IS drawn, fully inside the pane —
 *     a fix that hid it for good would pass 4 and fail here.
 */
import path from 'node:path'
import { existsSync, mkdirSync, mkdtempSync, readdirSync, rmSync, writeFileSync } from 'node:fs'

import {
  appReadyForInput,
  expect,
  promptReady,
  resolveBackend,
  test,
  type BackendEndpoint,
  type Page,
} from './harness'
import { rpc, startSshd, type SshdFixture } from './sshd-fixture'
import { readStand } from './stand'

const TAB = '.nocx-tab'
const ROW = '.ui-operation-row'
const COPYING = 'Copying the nocx helper'
/** The pane's own statement of the wait. In the pane, not the sidebar: the
 *  person is looking at the pane. */
const PANE_WAIT = '.pane.active .ui-status-card'
const CURSOR = '.pane.active .term-grid-cursor'

/** The fixture's slow link: 0.5 MB/s while the gate file is absent. A 15.3 MB
 *  helper then takes ~30 s — long enough that nothing asserted inside three
 *  seconds can be an artefact of the transfer finishing. */
const SLOW_BYTES_PER_SECOND = 500_000

/** How much must have crossed the link before the card's count is judged: at
 *  the fixture's rate this cannot arrive inside three seconds. */
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

async function answerWithHelper(page: Page): Promise<void> {
  const dialog = page.getByRole('dialog').filter({ hasText: 'Choose how nocx connects' })
  await expect(dialog).toBeVisible({ timeout: 30_000 })
  await dialog.getByRole('radio', { name: 'Helper' }).click()
  await dialog.getByRole('button', { name: 'Continue' }).click()
  await expect(dialog).not.toBeVisible()
}

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

/** The MB the card's own progress line names, "1.2 MB of 15.3 MB". */
async function cardMB(card: ReturnType<Page['locator']>): Promise<number> {
  const text = (await card.textContent()) ?? ''
  const match = /([\d.]+) (B|kB|MB|GB)/.exec(text)
  if (match === null) return 0
  const value = Number(match[1])
  const unit = match[2]
  if (unit === 'B') return value / 1_000_000
  if (unit === 'kB') return value / 1_000
  if (unit === 'GB') return value * 1_000
  return value
}

test('a slow open states its wait in the pane, cancels, and draws no cursor without a grid', async ({
  page,
}) => {
  test.setTimeout(420_000)

  const scratch = mkdtempSync(path.join(readStand().home, 'nocx-open-wait-'))
  const remoteHome = path.join(scratch, 'home')
  const remoteCwd = path.join(scratch, 'cwd')
  mkdirSync(remoteHome, { recursive: true, mode: 0o700 })
  mkdirSync(remoteCwd, { recursive: true, mode: 0o700 })
  // Absent: the link is slow. Created when the spec has seen what it needs.
  const fast = path.join(scratch, 'full-speed')

  let fixture: SshdFixture | null = null
  let second: SshdFixture | null = null
  let profileId: string | null = null
  let secondProfileId: string | null = null
  let thirdProfileId: string | null = null
  let endpoint: BackendEndpoint | null = null
  const profileName = `e2e-open-wait-${Date.now()}`

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

    // ── the open, and the pane's own account of it ───────────────────────
    await openProfile(page, profileName)
    await answerWithHelper(page)

    const card = page.locator(PANE_WAIT)
    await expect(card).toHaveCount(1, { timeout: 3_000 })
    // It names the work, and the machine it is for.
    await expect(card).toContainText(COPYING, { timeout: 10_000 })
    await expect(card).toContainText(fixture.host)

    // ── the cursor is not drawn while there is no grid ───────────────────
    // Read across the wait, not once: a cursor that appears for a frame and
    // is hidden again is the owner's defect too.
    for (let reading = 0; reading < 3; reading++) {
      await expect(page.locator(CURSOR)).toBeHidden()
    }

    // ── the count moves, and the wait is still the wait ──────────────────
    await expect
      .poll(() => cardMB(card), { timeout: 120_000, intervals: [200] })
      .toBeGreaterThanOrEqual(PAST_THE_THRESHOLD_MB)
    await expect(card).toContainText(COPYING)
    await expect(page.locator(CURSOR)).toBeHidden()

    // ── Cancel, from the pane ────────────────────────────────────────────
    await card.getByRole('button', { name: /Cancel/ }).click()
    await expect(card).toHaveCount(0, { timeout: 60_000 })
    await expect(page.locator(TAB)).toHaveCount(1, { timeout: 60_000 })
    await expect(page.locator(ROW)).toHaveCount(0, { timeout: 60_000 })
    expect(await page.locator('.pane-error').count()).toBe(0)
    // Nothing a later open could trust: the marker is the whole check.
    await expect
      .poll(() => completeHelpers(remoteHome), { timeout: 60_000, intervals: [200] })
      .toBe(0)

    // ── another host, at full speed: the pane arrives, the cursor is drawn,
    //    it is inside the pane, and the open's record names its first prompt
    //
    // A SECOND fixture, not the first one un-paced: the cancelled open may
    // have left a live helper session behind, and the next open of that
    // destination would ADOPT it — no open call, no record, and an assertion
    // about "the open that just happened" measuring the cancelled one.
    writeFileSync(fast, '')
    const fastHome = path.join(scratch, 'home2')
    const fastCwd = path.join(scratch, 'cwd2')
    mkdirSync(fastHome, { recursive: true, mode: 0o700 })
    mkdirSync(fastCwd, { recursive: true, mode: 0o700 })
    second = await startSshd({ home: fastHome, cwd: fastCwd })
    seedKnownHost(second)
    const secondName = `${profileName}-full`
    const secondProfile = await rpc<{ id?: string }>(page, endpoint, 'profiles.create', {
      type: 'ssh',
      name: secondName,
      options: {
        host: second.host,
        port: second.port,
        user: second.user,
        keyPath: second.userKey,
        // Script is the default destination mode and needs no connect-time
        // ask, so this open is the one a person gets without answering
        // anything: it must still name its first prompt.
        desiredMode: 'script',
      },
    })
    secondProfileId = secondProfile.id ?? null
    await openProfile(page, secondName)
    await expect(page.locator(TAB)).toHaveCount(2, { timeout: 120_000 })
    await promptReady(page)
    const cursor = page.locator(CURSOR)
    await expect(cursor).toBeVisible({ timeout: 60_000 })
    const inside = await page.evaluate(() => {
      const pane = document.querySelector<HTMLElement>('.pane.active')
      const caret = document.querySelector<HTMLElement>('.pane.active .term-grid-cursor')
      if (pane === null || caret === null) return false
      const p = pane.getBoundingClientRect()
      const c = caret.getBoundingClientRect()
      return c.width > 0 && c.height > 0 && c.top >= p.top && c.bottom <= p.bottom
    })
    expect(inside).toBe(true)

    // ── the record names the step that used to be silent ─────────────────
    // The open's own stages end in milliseconds; the last step of the walk —
    // the session's first prompt — is recorded on the same open, and a
    // finished open's duration no longer grows with the process (nocx-q9od5).
    const readOpens = async (): Promise<string[]> => {
      const text = (await rpc<{ text: string }>(page, endpoint!, 'app.diagnostics', {})).text
      return text
        .split('\n')
        .filter((line) => line.includes('[connection ') && line.includes('] ssh '))
    }
    await expect
      .poll(async () => (await readOpens()).some((line) => line.includes('first_prompt=')), {
        timeout: 30_000,
        intervals: [200],
      })
      .toBe(true)

    /**
     * A FINISHED open, by the request it names: `4/open#38 -> 3570ms`.
     *
     * The key is the request id and the value is the duration, because that is
     * the claim — a record that already carries a `duration=` never grows it.
     * `first_prompt=` is deliberately not part of it: an open whose first
     * prompt lands after the record was written GAINS that field, which is the
     * record doing its job rather than growing (nocx-q9od5).
     *
     * ONLY the records finished at the first read are compared (nocx-evj4b.1.7).
     * The first version compared the whole list twice around a fixed two-second
     * sleep, so an open that finished in between appeared as an extra line and
     * the spec failed under load — the CANCELLED open here lands two records,
     * and which of them has been written by a given moment is the open's
     * business, not this assertion's.
     */
    const finishedOpens = (lines: string[]): Array<[string, string]> => {
      const out: Array<[string, string]> = []
      for (const line of lines) {
        const request = /request=([^ ]+)/.exec(line)?.[1]
        const duration = /duration=([^ ]+)/.exec(line)?.[1]
        if (request !== undefined && duration !== undefined) out.push([request, duration])
      }
      return out
    }
    const before = finishedOpens(await readOpens())
    expect(
      before.length,
      'no open has finished, so there is no record to hold still',
    ).toBeGreaterThan(0)
    const beforeDurations = new Map(before)

    // A LATER OPEN IS THE CLOCK (nocx-evj4b.1.7). The claim is about a record
    // that has already finished, so the second read must happen after
    // something observable — not after a sleep. A third profile on the same
    // full-speed host opens a NEW connection (a different pool key, so it
    // cannot adopt the second one's helper), and its own finished record is
    // the evidence that the process moved on.
    const thirdName = `${profileName}-later`
    const thirdProfile = await rpc<{ id?: string }>(page, endpoint, 'profiles.create', {
      type: 'ssh',
      name: thirdName,
      options: {
        host: second.host,
        port: second.port,
        user: 'e2e-later',
        keyPath: second.userKey,
        desiredMode: 'script',
      },
    })
    thirdProfileId = thirdProfile.id ?? null
    await openProfile(page, thirdName)
    await expect
      .poll(
        async () =>
          finishedOpens(await readOpens()).filter(([request]) => !beforeDurations.has(request)),
        { timeout: 60_000, intervals: [200] },
      )
      .not.toEqual([])

    const afterDurations = new Map(finishedOpens(await readOpens()))
    // The same opens, the same durations. A record that finished before the
    // first read is still exactly as long now, however much has happened
    // since; a record that finishes later is simply not in the set.
    //
    // AND A RECORD CAN LEAVE THE SET BY BEING EVICTED, not only by growing:
    // the finished records live in a ring of 32 (internal/diagnostics,
    // recentOpenCap), and a stand that has served a dozen opens drops the
    // oldest. Eviction is not the defect under test, so the comparison covers
    // the records still present — and the NEWEST one read the first time must
    // be among them, or nothing was really checked.
    const newest = before[before.length - 1]
    expect(
      afterDurations.has(newest[0]),
      `the newest finished record (${newest[0]}) was evicted from the ring before the second ` +
        'read, so this run cannot say anything about it',
    ).toBe(true)
    let compared = 0
    for (const [request, duration] of before) {
      const later = afterDurations.get(request)
      if (later === undefined) continue
      compared++
      expect(
        later,
        `the record for ${request} read ${JSON.stringify(duration)} first and ` +
          `${JSON.stringify(later)} later`,
      ).toBe(duration)
    }
    expect(
      compared,
      'no record from the first read was still in the ring to compare',
    ).toBeGreaterThan(0)
  } finally {
    if (fixture !== null) {
      fixture.proc.kill()
      await new Promise<void>((resolve) => {
        if (fixture?.proc.exitCode !== null) resolve()
        else fixture?.proc.once('exit', () => resolve())
      })
    }
    for (const id of [profileId, secondProfileId, thirdProfileId]) {
      if (endpoint !== null && id !== null) {
        await rpc(page, endpoint, 'profiles.delete', { id }).catch(() => {})
      }
    }
    second?.proc.kill()
    rmSync(scratch, { recursive: true, force: true })
  }
})

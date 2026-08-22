import { test as base, expect, type Page } from '@playwright/test'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { VaultBackend, bindEndpoint, promptReady, settingsReady } from './harness'
import { readStand } from './stand'

/**
 * e2e: you decide which notifications reach you, and by which channel
 * (nocx-3mniv — epic 3's gate).
 *
 * The sentence the epic exists for, walked through the product's own
 * surfaces: a person opens Settings, turns the OS banner OFF for program
 * notifications, leaves the in-app toast ON, and then a program prints OSC 9
 * — and what arrives is a toast, with no banner attempted.
 *
 * Nothing here writes a setting through the store or the wire. The routing
 * cell is found in the grid where its row meets its column and CLICKED,
 * because the whole epic is that a person can do this; a spec that flipped
 * the toggle underneath the surface would pass on a build whose Settings page
 * offers nothing at all.
 *
 * ── THE ABSENCE HALF, AND WHY IT IS OBSERVED WHERE IT IS ────────────────
 *
 * "A toast arrived" alone passes on a build that ignores the routing table
 * entirely, so the claim with teeth is the other one: the banner was not
 * reached. That is not observable in the browser on this stand, and the
 * reason is structural rather than a gap somebody should close. The OS banner
 * is the AttentionHost port, `SetAttentionHost` is called from main.go and
 * nowhere else (internal/app/app.go), so a backend with no Wails host —
 * cmd/devharness, which is what this suite runs — holds `notify.HostHolder`'s
 * unbound state and every banner delivery returns `ErrUnavailable`. The
 * composition root's result handler then deliberately files NO feed row for
 * that one error ("a row per notification would say 'this build has no
 * banner' once per notification, forever"), so the notification centre cannot
 * answer this question either. What it does emit, before that exemption, is
 * one `notification delivery failed target=banner` line per banner route the
 * router actually resolved.
 *
 * So the observable is that line, used as a COUNTER, and it is fenced at both
 * ends rather than trusted:
 *
 *   - A CONTROL announcement goes first, with the shipped routing untouched,
 *     and the count is waited on until it reads exactly one. That is what
 *     makes the detector demonstrably live: an absence measured with a
 *     detector nobody proved was working is not evidence of anything.
 *   - The final count is read only AFTER the backend process has exited, so
 *     every line it was ever going to write is written. A read while it runs
 *     would be a race with the result handler, which logs after the sinks
 *     return — and the toast sink is the one this test is watching. Process
 *     death is the one fence that needs no duration.
 *
 * The slice stops at "shutting down application" for the same reason: closing
 * the transport ends the sessions, and `session.ended` reaches the banner by
 * default (notify.DefaultCatalogue), so teardown legitimately writes lines
 * this test did not cause.
 *
 * ── THREE TRAPS, EACH ALREADY PAID FOR ─────────────────────────────────
 *
 *  1. THE DEBOUNCE IS REAL. The policy is keyed on {session, kind} with an
 *     eight-second leading-edge window (internal/app/app.go,
 *     notifyDebounceWindow), so a second OSC 9 from the SAME tab would be
 *     coalesced and never delivered on its own. The control announcement and
 *     the routed one therefore come from two different tabs, which is two
 *     sessions and two debounce streams. Waiting the window out instead would
 *     be a test that depends on a duration.
 *
 *  2. SUPPRESSION. `Policy.suppressed` drops an event for the tab the user is
 *     looking at in a focused window. Nothing calls `notify.FocusHolder.Set`
 *     today — the renderer does not report focus yet (nocx-jiwq.2) — so
 *     suppression currently never fires, and this test does not lean on that.
 *     Both announcements are parked behind a gate file and released only once
 *     the person is IN SETTINGS, which is where the epic's sentence puts them
 *     anyway, and which is the arrangement that stays correct on the day
 *     focus reporting lands.
 *
 *  3. THE STAND IS SHARED AND ITS SETTINGS PERSIST. Turning the banner off is
 *     a write to the settings document, and a write to the shard's own stand
 *     would be the next spec's starting state. So this file brings its OWN
 *     devharness on a disposable home (the arrangement agent-ask.spec.ts and
 *     connection-password.spec.ts use), which also gives it a log stream
 *     carrying nobody else's notifications.
 *
 * The toast is read off the RENDERED product rather than off the wire, but
 * with a memory: a toast auto-dismisses after four seconds
 * (frontend/src/ui/toast.tsx), so a bare `toBeVisible` is a bet on a machine
 * being fast enough to look before it goes. A MutationObserver installed
 * before any application script records every `.ui-toast` the product ever
 * draws, so the wait is on an observable that cannot expire.
 */

const test = base

/** Lazily, not at module scope: the stand is started by globalSetup, which
 *  runs after Playwright has collected this file. */
const devharnessBin = () => readStand().devharness

const TAB = '.nocx-tab'
const TITLE = '.nocx-tab-title'
/** The Notifications section in the settings rail. The id is the section name
 *  the backend declared (`notify.RouteSettingSection`), because a generated
 *  page's id IS its title (settings.tsx, settingsPages). */
const NOTIFICATIONS_NAV = '.ui-grouped-nav__item[data-item="Notifications"]'

/** One cell of the kit's grid, by the coordinates it is placed at. Addressed
 *  this way rather than by the setting key alone because "where a row meets a
 *  column" is the claim the matrix makes; the key is asserted separately, on
 *  the cell those coordinates found. */
const cellAt = (row: string, column: string) =>
  `.ui-toggle-matrix__cell[data-row="${row}"][data-column="${column}"]`

/** The catalogue's ids and labels (internal/notify/catalogue.go). Written out
 *  because they are what a person reads and what the key persists — the two
 *  halves this surface is supposed to keep in step. */
const KIND = { id: 'programNotify', label: 'A program asked for a notification' }
const BANNER = { id: 'banner', label: 'OS banner' }
const TOAST = { id: 'toast', label: 'In-app toast' }
const cellLabel = (channel: { label: string }) => `${KIND.label} → ${channel.label}`
const settingKey = (channel: { id: string }) => `notifications.route.${KIND.id}.${channel.id}`

/** One nonce per run: the stand's vite and this file's backend are both
 *  reused across projects, and a body that reads the same in two runs cannot
 *  say which run drew it. */
const nonce = Date.now().toString(36)
const CONTROL = `control announcement ${nonce}`
const ROUTED = `routed announcement ${nonce}`

declare global {
  interface Window {
    __nocxToastMessages?: string[]
  }
}

/**
 * Remember every toast the product draws, from before the first application
 * script runs.
 *
 * It observes the RENDERED element — the kit's own `.ui-toast__message`, the
 * thing a person sees — and not the `notify.toast` frame, so a build that
 * received the push and failed to present it is still a failure here.
 */
async function recordToasts(page: Page): Promise<void> {
  await page.addInitScript(() => {
    window.__nocxToastMessages = []
    const harvest = (node: Node): void => {
      if (!(node instanceof HTMLElement)) return
      const toasts = node.matches('.ui-toast') ? [node] : [...node.querySelectorAll('.ui-toast')]
      for (const el of toasts) {
        window.__nocxToastMessages!.push(el.querySelector('.ui-toast__message')?.textContent ?? '')
      }
    }
    const observe = (): void => {
      new MutationObserver((records) => {
        for (const record of records) record.addedNodes.forEach(harvest)
      }).observe(document.documentElement, { childList: true, subtree: true })
    }
    if (document.documentElement) observe()
    else document.addEventListener('DOMContentLoaded', observe)
  })
}

/** Wait until the product has drawn a toast carrying `text`. The budget is a
 *  hang detector, not a claim about how fast a shared runner is: this
 *  predicate spans a real pty, the OSC parser, a round trip to the backend's
 *  ingress, the router, the toast sink and a push back to the renderer. */
async function expectToast(page: Page, text: string): Promise<void> {
  await expect
    .poll(async () => page.evaluate(() => window.__nocxToastMessages ?? []), {
      timeout: 30_000,
      message: `no toast ever carried ${JSON.stringify(text)}`,
    })
    .toContain(text)
}

/**
 * How many banner deliveries the backend attempted during the RUN.
 *
 * Only what was logged before shutdown began: `Transport.Stop` ends the
 * sessions and `session.ended` reaches the banner by default, so lines after
 * that marker are teardown's and not this test's. When the process was killed
 * without ever logging it, the whole file is the run.
 */
function bannerAttempts(logPath: string): number {
  const whole = readFileSync(logPath, 'utf8')
  const run = whole.split('msg="shutting down application"')[0]
  return run
    .split('\n')
    .filter(
      (line) => line.includes('notification delivery failed') && line.includes('target=banner'),
    ).length
}

/** Park a shell on a gate file, then have it print OSC 9. Typed and
 *  submitted; it returns with the command still running, which is the point —
 *  nothing is announced until the gate is opened. */
async function parkAnnouncement(page: Page, gate: string, body: string): Promise<void> {
  await page.keyboard.type(
    `while [ ! -e ${gate} ]; do sleep 0.1; done; printf '\\033]9;${body}\\007'`,
  )
  await page.keyboard.press('Enter')
}

test.use({ viewport: { width: 1280, height: 900 } })

test('turning the OS banner off leaves the toast on, and the banner is never reached', async ({
  page,
}) => {
  // Well past the suite's 30 s ceiling, and for the reason the ceiling's
  // comment allows for: this spec starts a backend of its own, drives two
  // real ptys and walks the Settings page. Every WAIT inside it is still on
  // an observable.
  test.setTimeout(150_000)

  const gates = mkdtempSync(join(tmpdir(), 'nocx-e2e-channels-'))
  const controlGate = join(gates, 'control')
  const routedGate = join(gates, 'routed')
  // `true` = no Secret Service for this backend regardless of the session the
  // suite runs in, so the vault comes up the same way in the container and on
  // a developer's machine (the arrangement agent-ask.spec.ts relies on).
  const backend = new VaultBackend(devharnessBin(), { root: gates }, true)

  try {
    const endpoint = await backend.start()
    await recordToasts(page)
    await bindEndpoint(page, endpoint)
    await page.goto('/')
    await expect(page.locator(TITLE).first()).not.toHaveText('', { timeout: 15_000 })
    // The page reports the backend it was meant to reach. bindEndpoint's own
    // doc comment asks for this: a spec that quietly measures the wrong
    // backend is a green run about nothing (nocx-w4vy).
    const reachedPort = await page.evaluate(() =>
      (
        window as unknown as { go: { main: { WailsApp: { GetWSPort: () => Promise<number> } } } }
      ).go.main.WailsApp.GetWSPort(),
    )
    expect(reachedPort).toBe(endpoint.port)

    // ── two tabs, two parked announcements ──────────────────────────────
    // Two, because the debounce is keyed on {session, kind}: a second
    // announcement from the same tab inside the window would be coalesced
    // rather than delivered, and this test needs both of them delivered.
    await expect(page.locator(TAB)).toHaveCount(1)
    // The precondition that is not "a tab exists": the editor must own the
    // keyboard, or the line below is typed into nothing (nocx-z9s9.15).
    await promptReady(page)
    await parkAnnouncement(page, controlGate, CONTROL)

    await page.keyboard.press('Meta+t')
    await expect(page.locator(TAB)).toHaveCount(2)
    await promptReady(page)
    await parkAnnouncement(page, routedGate, ROUTED)

    // ── the person opens Settings ───────────────────────────────────────
    // Settings is a pane of its own, so from here neither terminal is the
    // tab in front — which is the state the epic's sentence describes and
    // the state suppression would care about if the renderer reported focus.
    await page.keyboard.press('Meta+,')
    await settingsReady(page)
    await page.locator(`${NOTIFICATIONS_NAV} button`).click()

    // ── the surface reads as kind × channel ─────────────────────────────
    const matrix = page.locator('.ui-toggle-matrix')
    await expect(matrix).toBeVisible()
    await expect(
      matrix.locator(`.ui-toggle-matrix__column[data-column="${BANNER.id}"]`),
    ).toHaveText(BANNER.label)
    await expect(matrix.locator(`.ui-toggle-matrix__column[data-column="${TOAST.id}"]`)).toHaveText(
      TOAST.label,
    )
    await expect(
      matrix.locator(`.ui-toggle-matrix__row[data-row="${KIND.id}"] .ui-toggle-matrix__row-header`),
    ).toHaveText(KIND.label)

    const bannerCell = page.locator(cellAt(KIND.id, BANNER.id))
    const toastCell = page.locator(cellAt(KIND.id, TOAST.id))
    // The coordinates found the cell the KEY names. This is the join the
    // whole surface rests on — the grid derives its axes from the key
    // convention `internal/notify/catalogue.go` owns (RouteSettingKey), and
    // a grid that placed a control under the wrong headers would still look
    // like a grid.
    await expect(bannerCell.locator('.ui-settings-matrix-cell')).toHaveAttribute(
      'data-key',
      settingKey(BANNER),
    )
    await expect(toastCell.locator('.ui-settings-matrix-cell')).toHaveAttribute(
      'data-key',
      settingKey(TOAST),
    )

    const bannerSwitch = bannerCell.locator('input.ui-checkbox__control')
    const toastSwitch = toastCell.locator('input.ui-checkbox__control')
    // The control names what it does, not merely where it sits: the headers
    // locate it, and a screen reader landing on the switch itself must still
    // hear the whole cell.
    await expect(bannerSwitch).toHaveAttribute('aria-label', cellLabel(BANNER))
    await expect(toastSwitch).toHaveAttribute('aria-label', cellLabel(TOAST))
    // The shipped default, asserted rather than assumed: this is the state
    // the control announcement below is about to be routed by.
    await expect(bannerSwitch).toBeChecked()
    await expect(toastSwitch).toBeChecked()

    // ── the control: with the shipped routing, the banner IS reached ────
    writeFileSync(controlGate, '')
    await expectToast(page, CONTROL)
    // One announcement, one banner route, one attempt. Waited for rather
    // than read once, because the router logs it after the sinks return.
    await expect
      .poll(() => bannerAttempts(backend.logFile), {
        timeout: 30_000,
        message: `the banner was never attempted for the control announcement.\n${backend.logTail()}`,
      })
      .toBe(1)

    // ── the person turns the OS banner off ──────────────────────────────
    await bannerSwitch.click()
    // The fence, and it is a backend fence rather than a paint: the reset
    // affordance is drawn from the mirror `saveSetting` applies only AFTER
    // `settings.set` resolved (settings.tsx), and the registry runs its
    // notifiers — which is what rebuilds the router's table — inside that
    // call. So this appearing means the live routing has already changed.
    await expect(bannerCell.locator('.ui-icon-button')).toBeVisible()
    await expect(bannerSwitch).not.toBeChecked()
    // And the toast is left ALONE. Half the epic's sentence is that the
    // other channel keeps working, so it is asserted here as well as
    // observed below.
    await expect(toastSwitch).toBeChecked()

    // ── the program announces again ─────────────────────────────────────
    writeFileSync(routedGate, '')
    await expectToast(page, ROUTED)

    // ── and the banner was never reached for it ─────────────────────────
    // Read after the process is gone, so there is no line still to come.
    // Still one: the control's. Had the routing table been ignored, this
    // would be two.
    backend.stop()
    expect(
      bannerAttempts(backend.logFile),
      `the banner was attempted after it was turned off.\n${backend.logTail()}`,
    ).toBe(1)
  } finally {
    backend.stop()
    rmSync(gates, { recursive: true, force: true })
  }
})

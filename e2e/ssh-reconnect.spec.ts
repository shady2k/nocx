/**
 * e2e: what a person sees when an ssh connection dies — silently, and loudly
 * (nocx-oalo1.4.3, the design's §5.3).
 *
 * ## The silent death no longer ends the session, and that is a decision
 *
 * Until stage 2 a silent death was the ONE loss only the product could notice:
 * the keepalive prober missed twice, reported the host unreachable, closed the
 * transport, and the session ended — which is what put the reconnect card on
 * the tab. The design reversed the closing and kept the reporting:
 *
 * > Silence never ends a shell that would die with the connection. That is the
 * > person's choice (owner, 2026-11-06 …). An unanswered probe closes nothing
 * > by itself. … A connection carrying any plain shell has no such budget: it
 * > stays `stalled` until it answers, the transport errors, or a person
 * > presses Reconnect.
 * >
 * > — .internal/specs/2026-11-06-ssh-foundation-design.md §5.3
 *
 * So the journey this file used to assert for a silent death — the session
 * ends, the pane offers a way back, the reconnect keeps the scrollback — is no
 * longer the journey the product takes for THAT fault. Asserting it against a
 * blackhole now asserts the old product (it failed for 60 s on the branch that
 * brought stage 2, e2e/ssh-reconnect.spec.ts:215, on 2026-11-14). The first
 * test below asserts what replaced it, and it is a stronger statement about a
 * person's work: the shell is not thrown away, the pane is still told the host
 * stopped answering, and the tab is marked — not closed.
 *
 * ## The way back is still a promise, so it is still tested — with a cut
 *
 * A transport that ENDS is the loud death, and nothing about it is a guess:
 * the session's channel is gone, the pane says so, and the reconnect opens a
 * new shell while the old one's scrollback stays readable behind a divider.
 * That is the second test, and it is the same assertion the silent test used to
 * make, driven by the fault that still reaches it. Each fault now asserts what
 * the product promises for THAT fault instead of one fault standing in for
 * both; neither can pass by the other's behaviour.
 *
 * ## Why the silent test does not wait for the connection to come back
 *
 * It cannot, honestly: `blackhole` READS the bytes and drops them, so the
 * kernel of the proxy acknowledges what the far side never saw, and the
 * keepalive already sent stays parked on the mux's send lock with nothing left
 * to retransmit (design §5.3: "A flow lost in a firewall or NAT stays silent
 * and asks"). The recovery the design describes for that case — the head
 * saying the server answers new connections and offering Reconnect, which ends
 * that shell — is `pane.connection` and the head, which is stage 3's
 * (design §5.6, §5.8, §11). Waiting for it here would be asserting a surface
 * that does not exist yet; the test states the part that does: the finding
 * reaches the pane and nothing else happens.
 *
 * ## Why the third test says `slow` and not `unreachable`
 *
 * A host answering late is a host that ANSWERS: nothing has ended, nothing may
 * be replaced, and the pane says so with the measurement behind it. `slow` is
 * the durable half of that statement — a `unreachable` that arrives as the
 * session's last word is separated from the loss by one WebSocket frame, and a
 * Playwright assertion cannot sit inside that window without depending on
 * timing.
 *
 * ## The probe's cadence
 *
 * The profile below states it, and the ONE prober obeys it whichever holder
 * dialled the connection first — the shell-integration publish does, in this
 * open order, and it states nothing (design §5.3, and the supervisor's rule
 * that the smallest STATED interval wins). A soft deadline equals the cadence
 * when the cadence is under the design's 15 s, so silence is reported two
 * cadences after the last answer. That is what the polls support, and none of
 * them derives a wait from the numbers: they wait on states.
 */
import { mkdirSync, writeFileSync } from 'node:fs'
import path from 'node:path'

import { appReadyForInput, test, expect, resolveBackend, type Page } from './harness'
import { startFaultProxy, type FaultProxy } from './fault-proxy'
import { startSshd, rpc, type SshdFixture } from './sshd-fixture'
import { readStand } from './stand'

const TAB = '.nocx-tab'
const EDITOR = '.pane.active .nocx-editor-input'
const OFFER = '.pane.active .nocx-reconnect-offer'
const INDICATOR = '.pane.active .ui-connection-indicator'
const BOUNDARY = '[data-reconnect-boundary="true"]'
const RECONNECT_SETTING = '.ui-settings-row[data-key="ssh.reconnect"] select'

/**
 * The connection's probe cadence, as a person's profile states it — and it is
 * the cadence this spec gets, whichever holder dialled first (§5.3: "a
 * profile's keepalive settings still apply and map onto these").
 *
 * TWO SECONDS, not one: a probe's soft deadline equals the cadence whenever
 * the cadence is under the design's 15 s, so a one-second cadence would give
 * this spec's own slow fault (400 ms each way, about 800 ms of round trip)
 * only 200 ms of head room before a late answer read as silence. Two seconds
 * keeps the fault comfortably inside its deadline and still detects silence in
 * four, against the fifteen-second default this used to be stuck with.
 */
const KEEPALIVE_INTERVAL_MS = 2000

/** Rewrite the fixture's known_hosts line onto the port the client will
 *  actually dial. The line is keyed by host:port and the client reaches the
 *  fixture through the relay, so the fixture's own port would never match. */
function knownHostsFor(fixture: SshdFixture, port: number): string {
  const rest = fixture.knownHosts.slice(fixture.knownHosts.indexOf(' ') + 1)
  return `[127.0.0.1]:${port} ${rest}`
}

/** Trust that key in the home the stand's backend runs under. REPLACED, never
 *  appended: every fixture spawn mints a fresh host key, and a stale line for
 *  a dead one makes the backend refuse the connection. */
function trustHostKey(line: string): void {
  const sshDir = path.join(readStand().home, '.ssh')
  mkdirSync(sshDir, { recursive: true, mode: 0o700 })
  writeFileSync(path.join(sshDir, 'known_hosts'), line + '\n')
}

/**
 * State the reconnect policy through Settings, as a person would.
 *
 * Not assumed from the default: the stand's home is shared by every spec in
 * the run and by both browser projects, so what this setting holds on arrival
 * is whatever the last spec left. The policy decides whether the pane waits
 * for a click or reconnects itself, which is the difference between the two
 * journeys below.
 */
async function setReconnectPolicy(page: Page, value: 'ask' | 'auto' | 'never'): Promise<void> {
  await page.keyboard.press('Meta+,')
  await expect(page.locator('[aria-label="Settings sections"]')).toBeVisible({ timeout: 15_000 })
  // Navigating the rail is not optional: Settings renders only the section it
  // is on, so the control is in the DOM and never visible without this click
  // (e2e/tabs.spec.ts records the timeout that taught it).
  await page.locator('.ui-grouped-nav__item[data-item="Interface"] button').click()
  await expect(page.locator(RECONNECT_SETTING)).toBeVisible({ timeout: 10_000 })
  await page.selectOption(RECONNECT_SETTING, value)
  await expect(page.locator(RECONNECT_SETTING)).toHaveValue(value)
  await page.keyboard.press('Escape')
}

/** Open the seeded profile through quick connect and wait for a real prompt.
 *  The editor coming up is what says the remote shell reached one. */
async function openConnection(page: Page, profileName: string): Promise<void> {
  await page.keyboard.press('Control+Shift+P')
  const search = page.locator('.quick-connect__search input')
  await expect(search).toBeVisible()
  await search.fill(profileName)
  // Wait for the SAVED row before Enter — and say saved, not "some row whose
  // text contains the name".
  //
  // The palette renders its search box before its providers have answered, and
  // on an EMPTY list Enter used to reach the ad-hoc "Connect to <host>"
  // fallback — dialling root@<the profile name> with no credential, which is
  // what CI caught (nocx-k1691). The old guard here was
  // `.quick-connect__item:has-text(<name>)`, and the fallback row's own label
  // CONTAINS that name: `Connect to e2e-reconnect-<n>`. So the guard passed on
  // exactly the row it existed to exclude, Enter dialled the profile's name,
  // and the run died in DNS with
  // `lookup e2e-reconnect-<n> ... no such host` while the editor stayed hidden
  // (nocx-atpw5.1.31). The saved row is the non-system one, and it must also be
  // the SELECTED one, because Enter activates the selection.
  const savedRow = page.locator('.quick-connect__item:not(.quick-connect__item--system)', {
    hasText: profileName,
  })
  await expect(savedRow).toBeVisible({ timeout: 10_000 })
  await expect(savedRow).toHaveAttribute('aria-selected', 'true')
  await page.keyboard.press('Enter')
  await expect(page.locator(EDITOR)).toBeVisible({ timeout: 30_000 })
}

/** Run one command through the nocx editor and wait for its block. */
async function runCommand(page: Page, command: string): Promise<void> {
  const editor = page.locator(EDITOR)
  await editor.click()
  await editor.pressSequentially(command)
  await editor.press('Enter')
  await expect(
    page
      .locator('.pane.active .cmd-block', { hasText: command.split(' ').at(-1) ?? command })
      .first(),
  ).toBeVisible({ timeout: 30_000 })
}

interface Wired {
  fixture: SshdFixture
  proxy: FaultProxy
  profileId: string
  profileName: string
}

/**
 * Raise a fixture behind a relay, trust its key, and seed a profile pointing
 * at the RELAY. Everything the app dials from here on crosses a network the
 * test can change.
 */
async function wire(page: Page, home: string): Promise<Wired> {
  const fixture = await startSshd({ home, cwd: home })
  const proxy = await startFaultProxy(fixture.host, fixture.port)
  trustHostKey(knownHostsFor(fixture, proxy.port))

  await page.goto('/')
  await appReadyForInput(page)
  await expect(page.locator(TAB)).toHaveCount(1)

  const endpoint = await resolveBackend(page)
  // Unique per run: the stand's store persists across runs in this home, and a
  // stale profile would dial a relay that no longer exists. This spec's
  // subject is the silent-death reconnect, not the connect-time ask
  // (ADR-0069) — desiredMode is declared explicitly as script so the
  // connection is never asked.
  const profileName = `e2e-reconnect-${Date.now()}`
  const created = await rpc<{ id: string }>(page, endpoint, 'profiles.create', {
    type: 'ssh',
    name: profileName,
    options: {
      host: '127.0.0.1',
      port: proxy.port,
      user: 'e2e',
      keyPath: fixture.userKey,
      keepaliveInterval: KEEPALIVE_INTERVAL_MS,
      desiredMode: 'script',
    },
  })
  return { fixture, proxy, profileId: created.id, profileName }
}

/** Take everything back out. The stand's home is shared with every other spec
 *  in the run, so a profile left behind becomes their starting state. */
async function unwire(page: Page, w: Wired | null): Promise<void> {
  if (!w) return
  try {
    const endpoint = await resolveBackend(page)
    await rpc(page, endpoint, 'profiles.delete', { id: w.profileId })
  } catch {
    // A cleanup that throws would replace the real failure with its own.
  }
  await w.proxy.close()
  w.fixture.proc.kill('SIGKILL')
}

test('a silently dead connection is reported to the pane, and the shell is not thrown away', async ({
  page,
}, testInfo) => {
  test.setTimeout(150_000)
  const home = testInfo.outputPath('remote-home')
  mkdirSync(home, { recursive: true })
  let w: Wired | null = null

  try {
    w = await wire(page, home)
    await setReconnectPolicy(page, 'ask')
    await openConnection(page, w.profileName)

    // Work the dead connection will have done. Its output is what must stay
    // readable: the design keeps the shell, so nothing may take it away.
    await runCommand(page, 'echo before-the-loss')
    // And the shell is back at its prompt before the network dies — the
    // composer is the product's own statement that it is, and a command still
    // running when the silence starts would make "the block is still here"
    // describe a block that had not finished rather than a shell that
    // survived.
    await expect(page.locator(EDITOR)).toBeVisible()

    const tabsBeforeLoss = await page.locator(TAB).count()

    // The laptop closes. Sockets stay open, writes keep succeeding, and
    // nothing comes back — the loss only the connection's own prober can
    // notice, and the one the design says may not end a plain shell.
    w.proxy.blackhole()

    // Nothing is offered while nothing has ended. The offer is the pane's
    // statement that this session is GONE, and this session is not: it may be
    // alive on the far side with whatever the person was running on it.
    await expect(page.locator(OFFER)).toHaveCount(0)

    // Then the prober gives up and the pane says so — the mark, and the tab's
    // own word for the same fact. One wait on the whole statement rather than
    // the badge alone, so a red run says which part the pane failed to make.
    // The poll's budget is the design's worst case (30 s) with room, and it is
    // a state arrived at, never a duration waited out.
    await expect
      .poll(
        async () => ({
          condition: await page
            .locator(INDICATOR)
            .first()
            .getAttribute('data-condition')
            .catch(() => null),
          tabWarnings: await page.locator('.nocx-tab-warning').count(),
          offers: await page.locator(OFFER).count(),
          boundaries: await page.locator(BOUNDARY).count(),
        }),
        { timeout: 60_000 },
      )
      .toEqual({ condition: 'unreachable', tabWarnings: 1, offers: 0, boundaries: 0 })
    await expect(page.locator(`${TAB}[aria-selected="true"] .nocx-tab-warning`)).toHaveAttribute(
      'aria-label',
      'Host not answering',
    )

    // THE PROMISE THIS TEST EXISTS FOR: silence ended nothing. The tab is
    // still there with its shell, the shell's output is still readable, and
    // the pane never claimed the session was gone.
    await expect(page.locator(TAB)).toHaveCount(tabsBeforeLoss)
    await expect(page.locator(EDITOR)).toBeVisible()
    await expect(
      page.locator('.pane.active .cmd-block', { hasText: 'before-the-loss' }).first(),
    ).toBeVisible()
    await expect(page.locator(`${OFFER} .ui-status-card[data-tone="danger"]`)).toHaveCount(0)
    await expect(page.locator('.nocx-tab-warning[aria-label="Connection lost"]')).toHaveCount(0)
  } finally {
    await unwire(page, w)
  }
})

test('a connection that is cut is offered a way back, and the reconnect keeps the scrollback', async ({
  page,
}, testInfo) => {
  test.setTimeout(150_000)
  const home = testInfo.outputPath('cut-home')
  mkdirSync(home, { recursive: true })
  let w: Wired | null = null

  try {
    w = await wire(page, home)
    // `ask` is the journey being watched: the person is offered the choice and
    // takes it. `auto` is a different journey and would race this one.
    await setReconnectPolicy(page, 'ask')
    await openConnection(page, w.profileName)

    // Work the dead session will have done. Its output is what must survive.
    await runCommand(page, 'echo before-the-loss')

    const tabsBeforeLoss = await page.locator(TAB).count()

    // The loud death: every socket is destroyed, so the transport ends, the
    // session's channel ends with it, and nothing here is an inference the
    // prober has to make.
    w.proxy.cut()

    // The pane says this session is GONE and offers the one action that can
    // act — the mark, the card and the divider are one claim, so a red run
    // says which part of it the pane failed to make. The tab's own word for it
    // is the loss, not the reachability mark.
    await expect
      .poll(
        async () => ({
          condition: await page
            .locator(INDICATOR)
            .first()
            .getAttribute('data-condition')
            .catch(() => null),
          offers: await page.locator(OFFER).count(),
          boundaries: await page.locator(BOUNDARY).count(),
        }),
        { timeout: 60_000 },
      )
      .toEqual({ condition: 'lost', offers: 1, boundaries: 0 })

    // The tab is marked and NOT closed — a lost session is still a place the
    // person was working, and closing it would throw away the scrollback this
    // test is about to assert survives.
    //
    // Counted against what was open a moment ago rather than against 1: the
    // stand is shared, and Settings opens a tab of its own that stays behind
    // (setReconnectPolicy above). Asserting an absolute number here would be
    // asserting the suite's history, not this product promise.
    await expect(page.locator(TAB)).toHaveCount(tabsBeforeLoss)
    await expect(page.locator(`${TAB}[aria-selected="true"] .nocx-tab-warning`)).toHaveAttribute(
      'aria-label',
      'Connection lost',
    )

    // The card says what a reconnect actually is. This is the product's
    // promise and it is deliberately not a resurrection: a NEW shell, and
    // whatever the old one was running may still be going on the host.
    await expect(page.locator(`${OFFER} .ui-status-card[data-tone="danger"]`)).toBeVisible()
    await expect(page.locator(`${OFFER} .ui-status-card__title`)).toContainText('is gone')

    // The network is whole again — the relay is still listening, and only the
    // sockets it carried were destroyed. Without a working relay the reconnect
    // would dial a deaf proxy, which is a different test.
    w.proxy.pass()

    await page.getByRole('button', { name: 'Reconnect', exact: true }).click()

    // A working shell at the same endpoint: the prompt returns and a command
    // runs on it.
    await expect(page.locator(EDITOR)).toBeVisible({ timeout: 60_000 })
    await runCommand(page, 'echo after-the-reconnect')

    // And the whole point of doing it this way: the dead session's work is
    // still readable, with a divider saying where it ended.
    const boundary = page.locator(BOUNDARY)
    await expect(boundary).toHaveCount(1)
    await expect(boundary).toHaveText('New shell — the one above is gone')
    await expect(
      page.locator('.pane.active .cmd-block', { hasText: 'before-the-loss' }).first(),
    ).toBeVisible()
    // The mark is gone: the pane has a session again.
    await expect(page.locator(OFFER)).toHaveCount(0)
    await expect(page.locator(INDICATOR)).toHaveCount(0)
  } finally {
    await unwire(page, w)
  }
})

test('a host that is merely answering late is marked slow and offered nothing', async ({
  page,
}, testInfo) => {
  test.setTimeout(150_000)
  const home = testInfo.outputPath('slow-home')
  mkdirSync(home, { recursive: true })
  let w: Wired | null = null

  try {
    w = await wire(page, home)
    await setReconnectPolicy(page, 'ask')
    await openConnection(page, w.profileName)

    // 400ms each way puts the probe's round trip around 800ms — past the
    // 500ms the backend grades as slow (internal/session/liveness.go), and
    // well inside the 15s a probe is given before it reads as silence, so the
    // host is answering. Nothing here is a duration the assertions wait on:
    // the poll below waits on the grade the backend published.
    w.proxy.slow(400)

    await expect(page.locator(`${INDICATOR}[data-condition="slow"]`)).toBeVisible({
      timeout: 60_000,
    })

    // The promise this test exists for. The host is struggling, the session is
    // alive, and the product must not offer to replace it — nor mark the tab
    // as lost.
    await expect(page.locator(OFFER)).toHaveCount(0)
    await expect(page.locator(BOUNDARY)).toHaveCount(0)
    await expect(page.locator('.nocx-tab-warning[aria-label="Connection lost"]')).toHaveCount(0)

    // And it is still a shell, which is what makes "slow, not gone" a
    // statement about the product rather than about the indicator.
    await runCommand(page, 'echo still-working')

    // Recovery clears it: the grade leaves at a lower threshold than it
    // enters, so a link that improves stops being described as struggling.
    //
    // A MEASUREMENT THAT DOES NOT SURVIVE THE WIRE CANNOT CLEAR IT, and this
    // is where that was measured (2026-11-14): a loopback answer is a few
    // hundred microseconds, the wire carries whole milliseconds, and zero on
    // that field means "nothing measured" — so the 800 ms that made the host
    // slow stood for as long as the link answered quickly. The fix is the
    // helper's (internal/helper/session: roundTripMillis), and this assertion
    // is the one that watches a person's mark go away.
    w.proxy.pass()
    await expect(page.locator(`${INDICATOR}[data-condition="slow"]`)).toHaveCount(0, {
      timeout: 60_000,
    })
  } finally {
    await unwire(page, w)
  }
})

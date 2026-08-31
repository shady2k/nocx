/**
 * e2e: a person whose ssh connection dies SILENTLY gets a shell back, and the
 * work the dead session printed is still there (nocx-rtzo4.1, the epic
 * nocx-rtzo4's first success criterion).
 *
 * The epic shipped in PR #139 proven entirely on fakes: reconnect-setting
 * covers the ask/auto/never policy, terminal-content rebinds against a fake
 * session, and internal/ssh's netfault table stages the transport faults
 * backend-side. Nothing watched a person do it end to end, which is the
 * nocx-rtg0 shape AGENTS.md rule 2 was bought by — every unit correct, the
 * user's journey unproven.
 *
 * ## Why the loss has to be silent
 *
 * A killed connection is the LOUD loss, and the product already handled it:
 * the channel EOFs, the session ends, the tab is marked. The whole defect this
 * epic fixed was the quiet one — a suspended laptop, a NAT that dropped the
 * flow — where the socket stays open, writes succeed and nothing ever comes
 * back. Only the keepalive prober can notice that, so only a silent death
 * exercises what was built. `e2e/fault-proxy.ts` stages it, and its header
 * says why it is a second implementation of internal/ssh/netfault_test.go
 * rather than a reuse of it.
 *
 * ## Why the second test says `slow` and not `unreachable`
 *
 * The bead asks for a case proving no reconnect is offered for a host that has
 * merely stopped answering — a session that may still be alive on the far side
 * must not be replaced. That promise is real and it is asserted here, but
 * through `slow` rather than `unreachable`, because a DURABLE `unreachable` is
 * not reachable through the prober at all:
 *
 * internal/ssh/ssh_keepalive.go's silent-probe branch reports
 * `Reachability{Responsive: false}` — which becomes `unknown`, which the
 * renderer draws as `unreachable` — and then closes the transport on the very
 * next statement, because a parked SendRequest can only be freed by closing
 * the connection. So `unreachable` is the session's last word before it ends,
 * separated from `lost` by microseconds on the backend and by one WebSocket
 * frame at the renderer. A Playwright assertion cannot sit inside that window,
 * and a test that tried would be timing-dependent — which AGENTS.md forbids
 * outright, and rightly: it would pass on a slow machine and fail on a fast
 * one, which is the failure this suite has already paid for twice.
 *
 * What IS durable, and is the same promise, is `slow`: a host answering late
 * is answering, nothing has ended, and no reconnect may be offered. The
 * unreachable-specific half stays covered where it can be — unit level, with a
 * clock the test owns.
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
 * A probe every second, and a silent one given two seconds to come back.
 *
 * internal/connection/resolver.go defaults these to 30s and 3, which is right
 * for a product and would make this spec wait a minute and a half. They are
 * profile options precisely so the cadence can be stated per connection
 * (contracts/profiles.create.params.schema.json), so the spec states it rather
 * than sleeping. Detection lands at interval + interval*countMax ≈ 3s.
 */
const KEEPALIVE_INTERVAL_MS = 1000
const KEEPALIVE_COUNT_MAX = 2

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
  // stale profile would dial a relay that no longer exists.
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
      keepaliveCountMax: KEEPALIVE_COUNT_MAX,
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

test('a silently dead ssh connection is offered a way back, and the reconnect keeps the scrollback', async ({
  page,
}, testInfo) => {
  test.setTimeout(150_000)
  const home = testInfo.outputPath('remote-home')
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

    // The laptop closes. Sockets stay open, writes keep succeeding, and
    // nothing comes back — the only loss the keepalive prober can notice, and
    // the one nothing noticed before this epic.
    w.proxy.blackhole()

    // Nothing is offered while nothing has ended. The offer is the pane's
    // statement that this session is GONE, and a session on a host that has
    // merely gone quiet may still be alive on the far side — replacing it
    // would abandon whatever it is running.
    await expect(page.locator(OFFER)).toHaveCount(0)

    // Then the prober gives up and the pane says so. One wait on the whole
    // statement rather than on the badge alone: the mark, the card and the
    // divider are one claim, and a red run should say which part of it the
    // pane failed to make.
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

    // The network comes back — the lid opens. Without this the reconnect would
    // dial a deaf relay, which is a different test.
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
    // well inside the 2s a silent probe is given, so the host is answering.
    // Nothing here is a duration the assertions wait on: the poll below waits
    // on the grade the backend published.
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
    w.proxy.pass()
    await expect(page.locator(`${INDICATOR}[data-condition="slow"]`)).toHaveCount(0, {
      timeout: 60_000,
    })
  } finally {
    await unwire(page, w)
  }
})

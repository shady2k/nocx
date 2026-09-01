import { execFileSync, spawn, type ChildProcess } from 'node:child_process'
import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import path from 'node:path'

import { test, expect, promptReady, showSidebarView, resolveBackend } from './harness'
import type { Page } from './harness'
import { readStand } from './stand'

/**
 * e2e: a run of notifications is ONE row that opens, and narrowing the feed
 * does not quieten the bell (nocx-ctl6q — epic 2's gate).
 *
 * The sentence the epic exists for, walked through the product's own
 * surfaces. One session announces three things in a row; the feed shows one
 * row carrying a count, and opening it shows the three, each with its own
 * instant. A second HOST has announced something too, so the panel offers a
 * way to narrow — and narrowing hides the other host's rows, says how much of
 * the feed is left, and leaves the bell counting everything.
 *
 * That last one is the decision the epic turns on (D3, and the plan's
 * §"Task 4"): `feed.unreadCount` is the single source of truth for the bell
 * and the dock badge, so a bell that went quiet because you narrowed a list
 * would be lying about what is waiting. It is asserted here rather than only
 * in the panel's unit test because the bell and the panel are two surfaces
 * over one store, and nothing below this level has both of them on screen at
 * once.
 *
 * Nothing here reads the store. It types into a real shell, clicks the
 * disclosure, works the filter and reads the rendered rows — a feed that is
 * only true in a signal is a feature nobody has.
 *
 * TWO TRAPS, both already paid for by somebody:
 *
 *  - A tab the user closes raises NOTHING, deliberately (`takeCloseRequested`,
 *    internal/transport/ws.go). So no session here ends by having its tab
 *    closed; every occurrence in this test is a program asking nocx to present
 *    a message (ADR-0047), which is the source that needs no ending at all.
 *  - The feed is IN-MEMORY in a shared stand and `resetStand` does not clear
 *    it — it resets panes, ui state, notes and snippets. Rows from earlier
 *    spec files are legitimately still there, so this test ESTABLISHES its
 *    starting state instead of assuming it: everything is marked read first,
 *    which makes "unread" mean "raised since this test began", and every
 *    absolute number below is either a count this test raised or a number read
 *    off the panel before the filter was applied. Nothing here passes only
 *    when this file runs first (the discipline notification-centre.spec.ts
 *    established).
 *
 * The second host is a real SSH connection to cmd/e2e-sshd, because `host` is
 * stamped from the session registry (`sess.Host()`, internal/transport/
 * ws_notify.go) and a local session's host is genuinely the empty string.
 * There is no way to put two hosts in this feed without two hosts.
 */

const TAB = '.nocx-tab'
/** The bell's rail button. Addressed by `data-view`, which is the sidebar's
 *  OWN vocabulary for a view button and what every other view is found by —
 *  this spec used to carry a `data-testid` of its own until the merge with
 *  main, where `SidebarViewDescriptor` grew `status` and dropped the parallel
 *  test hook. Two ways to name one button is the defect, whichever one a
 *  reader happens to find first. */
const BELL = 'button[data-view="notifications"]'
const BADGE = '.ui-badge'
const INPUT = '.pane.active .nocx-editor-input'
const LIST = '.notifications-panel__list'
/** The panel's OWN rows. A run member is a RecordRow too and sits INSIDE its
 *  row's disclosed region, so `${LIST} .ui-collection-row` starts counting
 *  members as rows of the list the moment anything is expanded. The child
 *  combinator is what keeps "how many rows are there" a question about the
 *  list. */
const ROW = `${LIST} > .ui-collection-row`
/** Meta lines, safe to read unscoped only while nothing is expanded — same
 *  reason. Every assertion using it below is made with the run collapsed. */
const META = `${LIST} .ui-record-row__meta-text`
const RUN = '.notifications-panel__run'
const SHOWN = 'notifications-shown-count'
/** The panel's own rows that you have NOT seen. quietBell marks everything read
 *  before this test does anything, so an unread row is one this test caused —
 *  the same discipline trap 2 states, addressed by the kit's own account of the
 *  panel's `selected={!o.read}` (ui/collection-view.tsx). */
const UNREAD_ROW = `${LIST} > .ui-collection-row[data-selected="true"]`

/** The rows one SOURCE raised. The panel puts the event's kind on every row as
 *  a badge, in the same words its own kind filter offers
 *  (notify/notifications-panel.tsx) — the panel's own vocabulary for "what
 *  raised this", not a hook invented for a test.
 *
 *  Necessary rather than tidy. A `block.finished` row's title is THE COMMAND
 *  TEXT (internal/transport/ws_ledger_notify.go, blockSubject), so the row that
 *  the announcement's own `printf` raises BY ENDING carries the words of the
 *  announcement, and a match on those words alone finds both rows. */
const rowsOfKind = (page: Page, kind: string) =>
  page.locator(ROW).filter({ has: page.getByText(kind, { exact: true }) })

/** Three announcements from one session, inside the 30 s collapse window
 *  (internal/app/app.go: notifyFeedCollapseWindow). Their titles DIFFER on
 *  purpose: collapse keeps the newest one for the row, and an expansion whose
 *  members all read the same is not evidence that the members are real. */
/**
 * The titles carry the PROJECT NAME, and that is load-bearing rather than
 * decorative.
 *
 * Both browser projects run this file against ONE nocx-server and one $HOME
 * (playwright.config.ts declares chromium and webkit; workers is 1, so they
 * run in sequence), and marking the feed read does not empty it. So the
 * second project starts with the first project's rows already in the feed.
 * The totals below are READ rather than assumed for exactly that reason
 * (trap 2) — but a `hasText` filter cannot be read, it has to MATCH, and an
 * untagged 'build three ×3' matched chromium's row as well as webkit's and
 * came back with 2.
 *
 * CI hid it: `ci-e2e` runs one job per browser IN PARALLEL, so each browser
 * gets its own backend there and the collision cannot happen. The container
 * runs both in sequence against one, which is why it is the thing that saw
 * this. Neither environment is lying — the spec had a precondition (a feed
 * holding only its own rows) that it never established, and tagging the
 * titles is what establishes it without inventing a "clear the feed" the
 * product does not offer.
 */
const runTitles = (project: string) =>
  [`build one ${project}`, `build two ${project}`, `build three ${project}`] as const
/** What the row reads as: the newest title and the count (notifications-panel.tsx). */
const collapsedLabel = (titles: readonly string[]) =>
  `${titles[titles.length - 1]} ×${titles.length}`
const remoteTitle = (project: string) => `deploy remote ${project}`
/** An ordinary command whose whole job is to put a command's ENDING in the feed
 *  before the announcements arrive — the reasoning is at its first use below.
 *  Tagged per PANE because two sessions settle separately (the collapse key
 *  names the session), and per PROJECT for the reason runTitles is: both
 *  browser projects share one feed and a `hasText` has to match. */
const settlingCommand = (project: string, where: string) =>
  `true settling the ${where} feed ${project}`

// ── the second host ───────────────────────────────────────────────────────
// The in-process SSH server, spawned and trusted exactly as shell-mode.spec.ts
// and ports-row-width.spec.ts already do. It is duplicated here rather than
// shared because e2e/ has no ssh fixture module and creating one would edit
// three spec files this task does not own; it is reported as such rather than
// done quietly.

interface Fixture {
  proc: ChildProcess
  addr: string
  userKey: string
  knownHosts: string
  ready: Promise<void>
}

/** The home the BACKEND resolved, from the stand that started it — not a guess
 *  at it. NOCX_E2E_HOME_DIR lives in the backend's environment, not in
 *  Playwright's, and a known_hosts written to the wrong home is a host key the
 *  backend never sees (nocx-z9s9.6). */
const e2eHome = () => readStand().home

function startSshd(): Fixture {
  const bin = path.resolve(
    process.env.TMPDIR ?? '/tmp',
    `nocx-e2e-sshd-${process.pid}-${Date.now()}`,
  )
  if (!existsSync(bin)) {
    execFileSync('go', ['build', '-o', bin, './cmd/e2e-sshd'], {
      cwd: path.resolve(__dirname, '..'),
    })
  }
  // The fixture's HOME is the REMOTE host's home, separate from the backend's.
  // Sharing one would have the remote shell source the local ~/.nocx hooks and
  // come up as something no real remote host is (nocx-z9s9.8).
  const remoteHome = path.join(path.dirname(e2eHome()), 'grouping-remote-home')
  mkdirSync(remoteHome, { recursive: true, mode: 0o700 })
  const proc = spawn(bin, [], {
    stdio: ['ignore', 'pipe', 'inherit'],
    env: {
      ...process.env,
      HOME: remoteHome,
      ZDOTDIR: remoteHome,
      XDG_CONFIG_HOME: path.join(remoteHome, '.config'),
      XDG_DATA_HOME: path.join(remoteHome, '.local', 'share'),
      XDG_CACHE_HOME: path.join(remoteHome, '.cache'),
    },
  })
  const lines: string[] = []
  let addr = ''
  let userKey = ''
  let knownHosts = ''
  const deadline = Date.now() + 15_000
  // The fixture prints ADDR/USERKEY/KNOWNHOSTS/READY then serves forever.
  const ready = new Promise<void>((resolve, reject) => {
    proc.stdout?.on('data', (chunk: Buffer) => {
      for (const line of chunk.toString().split('\n')) {
        const trimmed = line.trim()
        if (!trimmed) continue
        lines.push(trimmed)
        if (trimmed.startsWith('ADDR=')) addr = trimmed.slice(5)
        if (trimmed.startsWith('USERKEY=')) userKey = trimmed.slice(8)
        if (trimmed.startsWith('KNOWNHOSTS=')) knownHosts = trimmed.slice(11)
        if (trimmed === 'READY') resolve()
      }
      if (Date.now() > deadline)
        reject(new Error(`e2e-sshd did not print READY: ${lines.join('|')}`))
    })
    proc.on('exit', (code) =>
      reject(new Error(`e2e-sshd exited early (${code}): ${lines.join('|')}`)),
    )
  })
  return {
    proc,
    ready,
    get addr() {
      return addr
    },
    get userKey() {
      return userKey
    },
    get knownHosts() {
      return knownHosts
    },
  }
}

/** Seed the isolated home's known_hosts so the backend's ssh client accepts
 *  the fixture's host key. REPLACED, not appended: every spawn mints fresh
 *  keys and a stale line for a dead key makes the backend refuse. */
function trustHostKey(fixture: Fixture): void {
  const sshDir = path.join(e2eHome(), '.ssh')
  mkdirSync(sshDir, { recursive: true, mode: 0o700 })
  writeFileSync(path.join(sshDir, 'known_hosts'), fixture.knownHosts + '\n')
}

/** One JSON-RPC method over the real backend socket, as the app does. */
async function rpc<T>(
  page: Page,
  port: number,
  token: string,
  method: string,
  params: unknown,
): Promise<T> {
  return page.evaluate(
    ({ port, token, method, params }) =>
      new Promise<T>((resolve, reject) => {
        const ws = new WebSocket(`ws://127.0.0.1:${port}/session`, [`nocx.token.${token}`])
        const timer = setTimeout(() => reject(new Error(`rpc ${method} timed out`)), 10_000)
        ws.onopen = () => {
          ws.send(JSON.stringify({ jsonrpc: '2.0', id: 1, method, params }))
        }
        ws.onmessage = (ev: MessageEvent) => {
          const msg = JSON.parse(String(ev.data)) as { result?: T; error?: { message?: string } }
          clearTimeout(timer)
          ws.close()
          if (msg.error) reject(new Error(`${method}: ${msg.error.message ?? 'rpc error'}`))
          else resolve(msg.result as T)
        }
        ws.onerror = () => {
          clearTimeout(timer)
          reject(new Error(`${method}: websocket error`))
        }
      }),
    { port, token, method, params },
  )
}

// ── the surfaces ──────────────────────────────────────────────────────────

/** The keyboard belongs to the editor again after a click on the chrome.
 *  promptReady only WAITS for focus; something has to give it back. */
async function backToTheTerminal(page: Page): Promise<void> {
  await page.locator(INPUT).click()
  await promptReady(page)
}

/**
 * Leave the bell quiet and the keyboard back in the terminal.
 *
 * Marking read is the product's own way to say "I have seen these", so every
 * count below is about a TRANSITION this test caused rather than about which
 * spec files ran first. Nothing is weakened: "nothing has happened yet" is
 * still asserted, it is simply established rather than assumed.
 *
 * The panel is left OPEN, unlike notification-centre.spec.ts, which collapses
 * it so that opening it again is part of what that test proves. Here the list
 * being on screen while the rows arrive is the observable this test waits on,
 * and toggling a panel shut in order to toggle it back is two more clicks that
 * can go wrong for no assertion gained.
 */
async function quietBell(page: Page): Promise<void> {
  await showSidebarView(page, 'notifications')
  const markRead = page.getByTestId('notifications-mark-read')
  if (await markRead.isEnabled()) await markRead.click()
  await expect(page.locator(BELL).locator(BADGE)).toHaveCount(0)
  await backToTheTerminal(page)
}

/**
 * Write one shell line, press Enter, and WAIT FOR THE PROMPT TO COME BACK.
 *
 * The wait is the fix rather than politeness (notification-osc.spec.ts):
 * submitting hands the composer's box to the command, and the next line typed
 * into a surface that is still busy reaches the shell as whatever survived.
 * The failure then reports as a missing notification rather than at the line
 * that was dropped.
 */
async function run(page: Page, line: string): Promise<void> {
  await page.keyboard.type(line)
  await page.keyboard.press('Enter')
  await promptReady(page)
}

/** A program in this pane asks nocx to present a message (ADR-0047) — the one
 *  source that needs no session to end, which is what keeps trap 1 out of this
 *  test entirely. */
function osc777(title: string, body: string): string {
  return `printf '\\033]777;notify;${title};${body}\\007'`
}

/** One of the panel's narrowing controls, by the label a person reads. The
 *  kit's Select is a native <select> (ADR-0014) and takes no id of its own, so
 *  the Field that labels it is what identifies it. */
function filterFor(page: Page, label: string) {
  return page
    .locator('.notifications-panel__filters .ui-field')
    .filter({ has: page.getByText(label, { exact: true }) })
    .locator('select.ui-select')
}

test.use({ viewport: { width: 1280, height: 900 } })

test('a run collapses into one row that opens, and narrowing the feed leaves the bell alone', async ({
  page,
}, testInfo) => {
  // Tagged per project so this test's rows are ITS OWN — see runTitles above.
  const RUN_TITLES = runTitles(testInfo.project.name)
  const COLLAPSED = collapsedLabel(RUN_TITLES)
  const REMOTE = remoteTitle(testInfo.project.name)
  // Well past the suite's 30 s ceiling, and for a reason the ceiling's comment
  // allows for: this spec builds a Go binary, completes a real SSH handshake
  // and drives two real ptys. Every WAIT inside it is still on an observable.
  test.setTimeout(150_000)

  const fixture = startSshd()
  let profileId: string | null = null
  try {
    await fixture.ready
    expect(fixture.addr).not.toBe('')
    trustHostKey(fixture)
    const remoteHost = fixture.addr.split(':')[0]
    const remotePort = Number(fixture.addr.split(':')[1])

    await page.goto('/')
    await expect(page.locator(TAB)).toHaveCount(1)
    // The precondition that is not "a tab exists": the editor must own the
    // keyboard, or the lines below are typed into nothing.
    await promptReady(page)
    await quietBell(page)

    // One ordinary command first, and wait until the feed carries its ENDING.
    //
    // A command that finished is itself something to catch up on
    // (`block.finished`, nocx-n3nfg), so every line this test types puts a row
    // in the feed — and the feed collapses a run per session, kind and level
    // (internal/notify/feed.go, collapseKeyOf), so once THIS row exists the
    // announcements' own endings join it instead of adding rows of their own.
    // That is what makes the rise below a fact about the announcements rather
    // than arithmetic over however many sources a command happens to have. The
    // arithmetic is what broke: it read 1 while the centre's only sources were
    // OSC and a session ending, and 2 the day a command's ending was wired,
    // with nothing about this test's own subject changed.
    const localSettling = settlingCommand(testInfo.project.name, 'local')
    await run(page, localSettling)
    await expect(page.locator(UNREAD_ROW).filter({ hasText: localSettling })).toHaveCount(1, {
      timeout: 30_000,
    })
    const badge = page.locator(BELL).locator(BADGE)
    await expect(badge).toHaveText(/^\d+$/)
    const waitingBefore = Number(await badge.textContent())

    // ── one session, three announcements, ONE row ────────────────────────
    for (const title of RUN_TITLES) {
      await run(page, osc777(title, 'step'))
    }

    // The row is the observable, and it is the whole first claim: three
    // notifications, one row, and the row carries the count. An explicit
    // budget because this predicate spans three real ptys, the OSC parser, a
    // round trip to the backend's ingress and a change notification back —
    // a hang detector, not a claim about how fast a shared runner is.
    const collapsed = page.locator(ROW).filter({ hasText: COLLAPSED })
    await expect(collapsed).toHaveCount(1, { timeout: 30_000 })

    // And the bell counts ROWS, so three announcements that collapsed are ONE
    // more thing waiting than there was before them — not three, and not one
    // per command that ran. Measured as a rise from a count this test READ,
    // which is what keeps it a fact about the announcements: an absolute number
    // here would be arithmetic over every source a command has, and it is that
    // arithmetic, never this claim, that changed.
    await expect(badge).toHaveText(String(waitingBefore + 1))

    // ── opening the row shows what it stands for ─────────────────────────
    const disclosure = collapsed.locator('.ui-record-row__disclosure')
    await expect(disclosure).toHaveAttribute('aria-expanded', 'false')
    await disclosure.click()
    await expect(disclosure).toHaveAttribute('aria-expanded', 'true')

    // Newest first, as the wire gave them (runToDTO reverses the feed's tail).
    // Read as an ordered list rather than a set: the order IS the claim, and a
    // count alone would pass on three copies of one member.
    const members = collapsed.locator(`${RUN} .ui-record-row__title`)
    await expect(members).toHaveText([...RUN_TITLES].reverse())

    // Each member carries its OWN instant — the reason an expansion is worth
    // opening at all (D2). The panel renders it at MINUTE granularity
    // (notifications-panel.tsx, formatWhen), so three announcements a second
    // apart legitimately read the same; what is asserted is that every member
    // has a time of its own on it, not that the three differ, because a
    // difference this surface cannot show is not a difference to assert.
    const times = collapsed.locator(`${RUN} .ui-record-row__meta-text`)
    await expect(times).toHaveCount(RUN_TITLES.length)
    for (let i = 0; i < RUN_TITLES.length; i++) {
      await expect(times.nth(i)).toHaveText(/\d{1,2}:\d{2}/)
    }
    // The tail holds twenty (notifyFeedMaxRunRetained), so nothing was
    // dropped, and the panel must not claim a truncation it did not make.
    await expect(page.getByTestId('notifications-run-dropped')).toHaveCount(0)

    // Shut again, so every count below is about the panel's own rows: a run
    // member is a RecordRow too, and an open row would put three more of them
    // inside the list.
    await disclosure.click()
    await expect(collapsed.locator(RUN)).toHaveCount(0)

    // ── a second host announces something ────────────────────────────────
    await backToTheTerminal(page)
    const ws = await resolveBackend(page)
    // Unique per run: the stand's profile store persists across runs in this
    // home, and a stale profile would dial a fixture that is long dead.
    const profileName = `e2e-grouping-${Date.now()}`
    const created = await rpc<{ id: string }>(page, ws.port, ws.token, 'profiles.create', {
      type: 'ssh',
      name: profileName,
      options: { host: remoteHost, port: remotePort, user: 'e2e', keyPath: fixture.userKey },
    })
    profileId = created.id

    // Opened the way a person opens it: quick connect finds the saved profile
    // and Enter opens it directly (the profile's key is file-based, so there
    // is no vault preflight).
    await page.keyboard.press('Control+Shift+P')
    const search = page.locator('.quick-connect__search input')
    await expect(search).toBeVisible()
    await search.fill(profileName)
    await page.keyboard.press('Enter')
    await expect(page.locator(TAB)).toHaveCount(2)

    // The connection is up and the editor is taking input — a `script`-mode
    // connection to a bash host arrives already integrated (nocx-mlm7), so the
    // healthy state is the recovery chrome offering nothing.
    await expect(page.locator('.pane.active .nocx-editor-recovery')).not.toBeVisible({
      timeout: 20_000,
    })
    const remoteEditor = page.locator(INPUT)
    await expect(remoteEditor).toBeVisible({ timeout: 10_000 })
    await remoteEditor.click()

    // This pane settles separately from the local one, for the reason the
    // collapse key gives: it names the SESSION, so a second session's endings
    // are rows of their own however quiet the first pane is. Same command, same
    // reason, and waiting on its row is also what proves the remote shell has
    // finished a command before the next line is typed at it.
    const remoteSettling = settlingCommand(testInfo.project.name, 'remote')
    await remoteEditor.pressSequentially(remoteSettling)
    await remoteEditor.press('Enter')
    await showSidebarView(page, 'notifications')
    await expect(page.locator(UNREAD_ROW).filter({ hasText: remoteSettling })).toHaveCount(1, {
      timeout: 30_000,
    })
    await expect(badge).toHaveText(/^\d+$/)
    const waitingBeforeRemote = Number(await badge.textContent())

    await remoteEditor.click()
    await remoteEditor.pressSequentially(osc777(REMOTE, 'done'))
    await remoteEditor.press('Enter')

    // One more thing is waiting than a moment ago, and it is this announcement:
    // the remote command's ending joined the row above. Waiting on the badge
    // rather than on the row keeps the wait on the count that the last
    // assertion of this test is about.
    await expect(badge).toHaveText(String(waitingBeforeRemote + 1), { timeout: 30_000 })

    const remoteRow = rowsOfKind(page, 'Program notification request').filter({ hasText: REMOTE })
    await expect(remoteRow).toHaveCount(1)
    // It says where it came from, which is the axis the filter narrows on.
    await expect(remoteRow.locator('.ui-record-row__meta-text')).toContainText(remoteHost)
    // One occurrence is a LEAF, not a row that never heard of the disclosure:
    // it reserves the chevron's width so both titles stand in one column.
    await expect(remoteRow.locator('.ui-record-row')).toHaveAttribute('data-disclosure', 'leaf')

    // ── narrowing to one host ────────────────────────────────────────────
    // The totals are READ, never assumed: rows from earlier spec files are
    // legitimately in this feed (trap 2), and every claim below is about what
    // the filter did to whatever was there.
    const total = await page.locator(ROW).count()
    expect(total).toBeGreaterThanOrEqual(2)
    // Unnarrowed, the panel says nothing: "12 of 12 shown" would be noise.
    await expect(page.getByTestId(SHOWN)).toHaveCount(0)
    // What the bell is counting with nothing narrowed — READ, like the totals
    // above and for the same reason. The claim the two assertions below make is
    // that this number DOES NOT CHANGE, and the number itself is about the
    // whole feed, which is the one thing this test does not own.
    const counted = (await badge.textContent()) ?? ''
    expect(counted).toMatch(/^\d+$/)

    await filterFor(page, 'Host').selectOption({ label: remoteHost })

    // The other host's rows are gone — including the one this test raised, so
    // the claim is about a row whose absence is this test's own doing.
    await expect(page.locator(ROW).filter({ hasText: COLLAPSED })).toHaveCount(0)
    await expect(remoteRow).toHaveCount(1)

    const shown = await page.locator(ROW).count()
    expect(shown).toBeGreaterThanOrEqual(1)
    expect(shown).toBeLessThan(total)
    // And what SURVIVED is from the host we narrowed to — every one of them.
    // "The row I looked for is still here" would pass with the filter doing
    // nothing at all; this is the assertion that cannot.
    const metas = await page.locator(META).allTextContents()
    expect(metas).toHaveLength(shown)
    for (const meta of metas) {
      expect(meta.startsWith(`${remoteHost} ·`)).toBe(true)
    }
    // The panel states the narrowed count itself, because the bell will not.
    await expect(page.getByTestId(SHOWN)).toHaveText(`${shown} of ${total} shown`)

    // ── THE assertion the epic turns on (D3) ─────────────────────────────
    // The bell counts everything, always. A bell that quietened itself because
    // you narrowed a list would be lying about what is waiting — and it is
    // asserted here, with both surfaces on screen, because nothing below this
    // level has both.
    await expect(badge).toHaveText(counted)

    // Clearing it puts every row back, which is what makes the narrowing a
    // VIEW over the feed rather than something done to it.
    await filterFor(page, 'Host').selectOption('')
    await expect(page.locator(ROW)).toHaveCount(total)
    await expect(page.getByTestId(SHOWN)).toHaveCount(0)
    await expect(badge).toHaveText(counted)
  } finally {
    // Take the profile back out. The stand's home is shared by every spec in
    // the run AND by both browser projects, so a profile left here becomes the
    // next spec's starting state — quick-connect's picker asserts the plain
    // server list is EMPTY and has gone red across that boundary before
    // (nocx-8rda).
    try {
      if (profileId) {
        const ws = await resolveBackend(page)
        await rpc(page, ws.port, ws.token, 'profiles.delete', { id: profileId })
      }
    } catch {
      // A cleanup that throws would replace the real failure with its own.
    }
    fixture.proc.kill('SIGKILL')
  }
})

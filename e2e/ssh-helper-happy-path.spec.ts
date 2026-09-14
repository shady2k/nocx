/**
 * e2e: ONE SSH connection per host serves everything, it answers a program with
 * nobody watching, and it survives the coordinator being replaced
 * (nocx-50w7p.6 — the epic's happy path, AGENTS.md testing rule 2).
 *
 * # What a person can do that they could not before
 *
 * Open a saved password host, get a shell AND a Files panel on the same
 * machine, and have that cost the far host exactly ONE authenticated
 * connection; then let a program in that pane ask its terminal where the
 * cursor is while nocx's window is closed, and get the answer; then quit nocx
 * entirely, open it again, and find the same shell, the same screen content and
 * a pane that still takes input.
 *
 * # The three criteria and where each is watched
 *
 * 1. **One connection.** `cmd/e2e-sshd` keeps a counter of connections that
 *    completed authentication and prints it as `AUTH=<n>`; this spec reads the
 *    last value after BOTH consumers have been served and requires exactly 1.
 *    The counter is the far host's own account — not a reading of the
 *    fixture's chatter, and not a fact either nocx or the test wrote down
 *    about itself. That the pane really is this machine's helper's dial is
 *    structural rather than asserted through a second RPC: an ssh pane exists
 *    only because a helper claimed the destination
 *    (internal/transport/session_open.go refuses every other route for a
 *    remote kind by name). `sessions.inventory` is deliberately NOT used: it
 *    walks the registry only a FAR helper host fills and answers "no active
 *    helper" for exactly the panes this epic is about.
 * 2. **Answered with no browser client attached.** The pane runs a program that
 *    writes `ESC[6n` (DSR) to its terminal and READS the reply back off its own
 *    pty, then writes those bytes to a file on the far host and prints them.
 *    The probe parks on a file this spec creates, and the spec creates it only
 *    AFTER `sessions.live` reports the session unattached — so the question was
 *    asked and answered while no renderer existed. With the browser gone there
 *    is no xterm to answer it and no client to route a reply through: the
 *    answer can only be the runtime beside the PTY, which is ADR-0066.
 *    The read is the far program's own, and nothing here asks the renderer
 *    anything (`session.read` is deliberately NOT used: its contract says a
 *    running item and the current screen are answered by the RENDERER, which is
 *    the dependency this criterion exists to exclude).
 * 3. **The coordinator is replaceable.** The backend is stopped and a second
 *    one started over the same home; the same session id comes back, the same
 *    far process is still behind it, the content the pane is drawn from is the
 *    same content, and a command typed into the pane afterwards round-trips.
 *
 * # One spec, not three
 *
 * The stand does not differ between the criteria: all three are claims about
 * ONE pane, ONE connection and ONE far host, and criteria 2 and 3 are
 * statements about the pane criterion 1 opened. A second fixture and a second
 * backend would be a second journey, whose pane would be a different window,
 * a different shell and a different screen — so this is one spec with three
 * labelled stages.
 *
 * # No timing
 *
 * Every wait is on an observable state: the fixture's counter, a row in the
 * Files tree, the files the FAR program writes (its marker, its cursor report,
 * its pid and its own trace), `sessions.live`'s own `attached`, and the
 * coordinator's recording. Nothing is waited out, and the two places a program
 * paces itself (parking on a trigger file, and being polled through its own
 * artifacts) are the far side's clock rather than a duration this test chose.
 */
import {
  mkdirSync,
  mkdtempSync,
  existsSync,
  readdirSync,
  readFileSync,
  writeFileSync,
} from 'node:fs'
import { execFileSync } from 'node:child_process'
import { tmpdir } from 'node:os'
import path from 'node:path'

import { expect, type Browser, type Page } from '@playwright/test'

import { BASE_URL } from './base-url'
import {
  standalone as base,
  bindEndpoint,
  clickIntoEditor,
  openControlPlane,
  promptReady,
  showSidebarView,
  type BackendEndpoint,
  type DisposableRoot,
  VaultBackend,
} from './harness'
import { startSshd, type SshdFixture } from './sshd-fixture'
import { readStand } from './stand'

const test = base

const FILES_PANEL = '[data-testid="files-panel"]'
const TREE_ROW = '.ui-tree-row'
const QUICK_CONNECT_SEARCH = '.quick-connect__search input'
const QUICK_CONNECT_ITEM = '.quick-connect__item'

/**
 * The vault passphrase for THIS spec's own backend. Not the harness's shared
 * constant: that one is the shared stand's, and this one is disposable with the
 * backend (harness.ts says so where it declares the other).
 */
const VAULT_PASSPHRASE = 'e2e-ssh-happy-path-passphrase'

/** The password the far host accepts. cmd/e2e-sshd is spawned with
 *  `-password`, so this is the only way in: the fixture refuses every key. */
const HOST_PASSWORD = 'e2e-password-42'

const PROFILE_NAME = 'Helper Happy Path'

/** The far-side artifacts of stage 2. All under the fixture's HOME, which is a
 *  directory this spec created and can read — the far host's filesystem is
 *  this one, exactly as it is for e2e/remote-coordinator-reclaim.spec.ts. */
const WAITING = 'nocx-dsr-waiting'
const TRIGGER = 'nocx-dsr-go'
const ANSWER = 'nocx-dsr-answer'
const PROBE = 'nocx-dsr-probe.sh'
/** The far shell's own pid, reported by the shell itself. */
const PID = 'nocx-pid'

/** The probe's own account of what it did, step by step. It exists because a
 *  far-side program that exits early leaves nothing else behind: the byte it
 *  read, the byte it never read, and where it got to are all invisible from
 *  this side of the pty. */
const TRACE = 'nocx-dsr-trace'

/**
 * The probe: it PARKS, then asks its terminal for the cursor position and reads
 * the reply off its own pty.
 *
 * Both halves of the read are load-bearing, and both are copied from the Go
 * gate that proves the same property one layer down
 * (internal/helper/session/ssh_spawn_test.go's `decReplyProgram`):
 *
 *  - `stty -icanon -echo` before the query. `icanon` would make a read wait for
 *    a newline the reply does not carry, and `echo` would put the answer back
 *    into the terminal's OUTPUT — where a test could not tell the terminal's
 *    reply from its own echo of it.
 *  - one byte at a time until `R`, because a cursor report is
 *    `ESC [ row ; col R` and a fixed-length read would truncate the moment the
 *    pane is wide enough for a two-digit column.
 *
 * The park is first: `nocx-dsr-waiting` says the program is running and has not
 * asked anything yet, which is what lets the spec close the browser and only
 * THEN release it.
 */
const PROBE_SCRIPT = `L="$HOME/${TRACE}"
echo "probe start: home=$HOME pwd=$(pwd) tty=$(tty 2>&1)" >>"$L"
: > "$HOME/${WAITING}"
echo "parked, waiting for ${TRIGGER}" >>"$L"
ticks=0
while [ ! -f "$HOME/${TRIGGER}" ]; do ticks=$((ticks+1)); sleep 0.2; done
echo "released after $ticks ticks" >>"$L"
stty -icanon -echo min 1 time 0 2>>"$L"
echo "stty done" >>"$L"
printf '\\033[6n'
echo "query sent" >>"$L"
i=0
r=''
while [ "$i" -lt 32 ]; do
	c=$(dd bs=1 count=1 2>>"$L")
	if [ -z "$c" ]; then echo "dd returned nothing at byte $i" >>"$L"; break; fi
	r="$r$c"
	i=$((i+1))
	if [ "$c" = R ]; then echo "terminator at byte $i" >>"$L"; break; fi
done
echo "read: $r" >>"$L"
stty icanon echo 2>>"$L"
printf '%s' "$r" >"$HOME/${ANSWER}"
printf 'NOCXDSR[%s]\\n' "$r"
`

/** A cursor report: the runtime's answer, and nothing else. The ESC is the
 *  point of the pattern, so the rule that notices it is silenced here the way
 *  frontend/src/scrollback/sgr-read.ts silences it for the same escape. */
// eslint-disable-next-line no-control-regex
const CURSOR_REPORT = /^\u001b\[\d{1,3};\d{1,3}R$/

/** What the far shell prints when it reads the report, and what goes into the
 *  coordinator's recording of this session. */
const DSR_LINE = 'NOCXDSR['

/** The round-trip command of stage 3, and the marker is deliberately NOT
 *  a substring of the command text: the pane shows the command in its block
 *  header, so a marker that appeared in the command could be matched by the
 *  header rather than by the OUTPUT. `2121*2` is 4242 and the arithmetic is
 *  the far SHELL's. */
const ROUND_TRIP_COMMAND = 'echo AFTER-$((2121*2))'
const ROUND_TRIP_OUTPUT = 'AFTER-4242'

interface LiveSession {
  sessionId: string
  instanceId: string
  sessionEpoch: number
  paneId: string | null
  attached: boolean
}

interface SessionOutput {
  produced: number
  effectiveSize: { cols: number; rows: number }
  runs: { offset: number; body: string }[]
}

/** One JSON-RPC call on a socket of the test's own. It never attaches to a
 *  session — `attach` is an explicit call and this makes none — so asking a
 *  question cannot itself change the state the question is about. */
async function ask<T>(ep: BackendEndpoint, method: string, params: unknown): Promise<T> {
  const wire = await openControlPlane(ep.port, ep.token)
  try {
    return (await wire.call(method, params)) as T
  } finally {
    wire.close()
  }
}

async function liveSessions(ep: BackendEndpoint): Promise<LiveSession[]> {
  const answer = await ask<{ sessions: LiveSession[] }>(ep, 'sessions.live', {})
  return answer.sessions
}

/**
 * The fixture's own count of authenticated connections, read off its `AUTH=`
 * line: the LAST value, because the line carries a running count rather than
 * one line per event.
 */
function authenticatedConnections(fixture: SshdFixture): number {
  const printed = fixture.lines().filter((line) => line.startsWith('AUTH='))
  if (printed.length === 0) return 0
  const last = printed[printed.length - 1]
  return Number(last.slice('AUTH='.length))
}

/**
 * What the FAR host holds, as the value a poll reports when it is not what the
 * spec expected.
 *
 * A program on the far side that exits early leaves nothing on this side of the
 * pty: no output a spec can read, no exit code, no reason. The files it wrote
 * and the trace it keeps are the only account of what it did — so they ARE the
 * poll's failure value, and Playwright prints them as "Received:" beside the
 * expectation. A message string cannot serve here: it is fixed when the poll is
 * configured, and this has to be read at the moment the poll gives up.
 */
function farSideReport(remoteHome: string): string {
  let names: string[] = []
  try {
    names = readdirSync(remoteHome).sort()
  } catch (err) {
    return `the far home could not be read (${String(err)})`
  }
  const tracePath = path.join(remoteHome, TRACE)
  const trace = existsSync(tracePath) ? readFileSync(tracePath, 'utf8') : '(no trace)'
  return `far home holds [${names.join(', ')}] | probe trace: ${JSON.stringify(trace.slice(-600))}`
}

/**
 * Is this pid there, asked of the KERNEL? `kill(pid, 0)` sends no signal and
 * reports whether the process exists — the criterion's "still running", read
 * from the operating system rather than from anything nocx recorded about
 * itself. The far host of this journey is this container, so the pid a far
 * shell reported is visible here.
 */
function aliveFromThisMachine(pid: number): boolean {
  try {
    process.kill(pid, 0)
    return true
  } catch {
    return false
  }
}

/** Everything the coordinator has recorded for one session, decoded, in stream
 *  order. Paged, because one answer is bounded and a recording larger than it
 *  arrives over several calls. */
async function recorded(ep: BackendEndpoint, sessionId: string): Promise<string> {
  let text = ''
  let from = 0
  for (;;) {
    const page = await ask<SessionOutput>(ep, 'session.output', { sessionId, from })
    for (const run of page.runs) {
      text += Buffer.from(run.body, 'base64').toString('utf8')
    }
    if (page.produced <= from || page.runs.length === 0) return text
    from = page.produced
  }
}

/** The session's own size, from the same read the renderer draws it from. */
async function effectiveSize(
  ep: BackendEndpoint,
  sessionId: string,
): Promise<{ cols: number; rows: number }> {
  return (await ask<SessionOutput>(ep, 'session.output', { sessionId, from: 0 })).effectiveSize
}

/** Open a page on a context of its own, bound to this backend. Two of these
 *  never share storage, a renderer or a socket — which is what makes the
 *  return in stage 3 a stranger's return. */
async function freshClient(browser: Browser, ep: BackendEndpoint): Promise<Page> {
  // baseURL explicitly: a context made by hand inherits nothing from the
  // config's `use`, so `goto('/')` would have no origin to resolve against.
  const context = await browser.newContext({ baseURL: BASE_URL })
  const page = await context.newPage()
  await bindEndpoint(page, ep)
  await page.goto('/')
  return page
}

/** Open the saved profile the way a person does: quick connect finds it and
 *  Enter opens it. */
async function openSavedProfile(page: Page, profileName: string): Promise<void> {
  await page.keyboard.press('Control+Shift+P')
  const search = page.locator(QUICK_CONNECT_SEARCH)
  await expect(search).toBeVisible({ timeout: 30_000 })
  await search.fill(profileName)
  // The row is the observable that says Enter will open the SAVED profile
  // rather than the ad-hoc "Connect to <host>" fallback (nocx-k1691).
  await expect(page.locator(QUICK_CONNECT_ITEM, { hasText: profileName })).toBeVisible({
    timeout: 30_000,
  })
  await page.keyboard.press('Enter')
}

/**
 * Seed the one connection this spec is about, over the control plane: a vault,
 * a stored password, and a saved host bound to it.
 *
 * THE PASSWORD IS STORED rather than typed into the ask, and that is a
 * precondition of the criterion rather than a shortcut. Criterion 1 is about a
 * pane and Files sharing ONE connection, and two consumers can only share a
 * pool entry if they authenticate as the same principal: the binding is what
 * makes the Files lease resolve to the same credential reference the pane's
 * shell was opened with. A prompt answer is use-once by design
 * (internal/connection/password_asker.go) and would make the two consumers two
 * principals — which is a different scenario, and not the one the epic claims.
 */
async function seedPasswordConnection(ep: BackendEndpoint, fixture: SshdFixture): Promise<void> {
  await ask(ep, 'vault.setup', { passphrase: VAULT_PASSPHRASE })
  const minted = await ask<{ row: string }>(ep, 'secrets.savePassword', {
    password: HOST_PASSWORD,
    name: `e2e@${fixture.host}:${fixture.port}`,
  })
  // The renderer's vocabulary is the row HANDLE (ADR-0011 §2): the backend
  // resolves it to the stored reference, and a reference never crosses the
  // wire.
  await ask(ep, 'profiles.create', {
    type: 'ssh',
    name: PROFILE_NAME,
    options: {
      host: fixture.host,
      port: fixture.port,
      user: 'e2e',
      auth: 'password',
      passwordSecret: minted.row,
    },
  })
}

/**
 * A vault that sealed while nobody was attached is opened again, the way
 * harness.ts's own `resetStand` does it for the shared stand and for the same
 * reason (design D9): the seal is the PRODUCT's, it is right to set it, and a
 * surface that meets a locked vault raises an unlock sheet over whatever the
 * spec was driving.
 */
async function unsealIfSealed(ep: BackendEndpoint): Promise<void> {
  const status = await ask<{ state: string }>(ep, 'vault.status', {})
  if (status.state === 'sealed') {
    await ask(ep, 'vault.unseal', { means: 'passphrase', secret: VAULT_PASSPHRASE })
  }
}

/**
 * The helper daemon this spec's backend started, ended — SIGTERM first and
 * SIGKILL only for what ignored it, each phase waited for on the OS's own
 * answer rather than on a duration.
 *
 * It is here rather than in harness.ts because it is not shared: the daemon is
 * DETACHED by design (D1), and a spec that stopped only its own `nocx-server`
 * would leave a process holding a far shell under a home the run is finished
 * with. e2e/local-session-reattach.spec.ts states the long version of this for
 * the local case; this is the same rule applied to this spec's own home, and
 * it is deliberately not a second answer to anything a shared module already
 * owns — nothing shared owns it yet.
 */
async function endHelperDaemon(isolatedHome: string): Promise<void> {
  const pidsUnder = (): number[] => {
    try {
      return execFileSync('pgrep', ['-f', path.join(isolatedHome, '.nocx', 'helper')], {
        encoding: 'utf8',
      })
        .split('\n')
        .map((line) => Number(line.trim()))
        .filter((pid) => Number.isInteger(pid) && pid > 0)
    } catch {
      // pgrep exits non-zero when nothing matches, which is an answer.
      return []
    }
  }
  const gone = async (withinMs: number): Promise<boolean> => {
    const deadline = Date.now() + withinMs
    for (;;) {
      if (pidsUnder().length === 0) return true
      if (Date.now() > deadline) return false
      const { promise, resolve: resume } = Promise.withResolvers<void>()
      setTimeout(resume, 50)
      await promise
    }
  }
  for (const signal of ['SIGTERM', 'SIGKILL'] as const) {
    const survivors = pidsUnder()
    if (survivors.length === 0) return
    for (const pid of survivors) {
      try {
        process.kill(pid, signal)
      } catch {
        /* already gone */
      }
    }
    if (await gone(15_000)) return
  }
  throw new Error(
    `this spec's helper daemon(s) ${pidsUnder().join(', ')} outlived SIGTERM and SIGKILL`,
  )
}

test.describe('one ssh connection per host, answered unwatched, surviving the coordinator', () => {
  test('one pane and one Files panel ride one connection, a detached DSR is answered, and a replaced coordinator keeps the pane', async ({
    browser,
  }) => {
    // A go build of the fixture, a real SSH handshake through the helper, an
    // SFTP listing, a detached DSR readback and a coordinator restart. Every
    // wait inside is still on an observable state.
    test.setTimeout(900_000)

    const home: DisposableRoot = { root: mkdtempSync(path.join(tmpdir(), 'nocx-ssh-happy-')) }
    const hostRoot = mkdtempSync(path.join(tmpdir(), 'nocx-ssh-happy-host-'))
    const remoteHome = path.join(hostRoot, 'home')
    mkdirSync(remoteHome, { recursive: true, mode: 0o700 })

    const backend = new VaultBackend(readStand().server, home)
    let fixture: SshdFixture | null = null
    let isolatedHome = ''

    try {
      fixture = await startSshd({
        home: remoteHome,
        cwd: remoteHome,
        args: ['-password', HOST_PASSWORD],
      })
      let endpoint = await backend.start()
      isolatedHome = backend.isolatedHome

      // The far host's key, trusted in the home this backend runs under: the
      // helper asks the COORDINATOR whether to trust it, and the coordinator
      // reads its own known_hosts. REPLACED, never appended — every fixture
      // spawn mints a fresh key.
      const sshDir = path.join(isolatedHome, '.ssh')
      mkdirSync(sshDir, { recursive: true, mode: 0o700 })
      writeFileSync(path.join(sshDir, 'known_hosts'), `${fixture.knownHosts}\n`)

      await seedPasswordConnection(endpoint, fixture)

      // ── STAGE 1: the pane, and Files, on the same password host ──────────
      const first = await freshClient(browser, endpoint)
      await promptReady(first)

      // The pane the application opened for itself, before anything of ours.
      // Its id is what tells the profile's pane apart a moment later: EVERY
      // pane carries a backend session id (AD-7), so "the active pane has an
      // id" is true of the starter tab too and gates nothing.
      const starterSessions = (await liveSessions(endpoint)).map((s) => s.sessionId)
      expect(starterSessions).toHaveLength(1)

      await openSavedProfile(first, PROFILE_NAME)

      // THE PANE, identified by the session that did not exist before we asked
      // for the profile — and read off the pane element the renderer built,
      // NOT off the cwd chip, which carries `📁 ~` from construction
      // (frontend/src/editor.ts) and therefore says nothing about whether a
      // shell reported anything.
      const activePane = first.locator('.pane.active')
      await expect
        .poll(
          async () => {
            const opened = (await liveSessions(endpoint)).find(
              (session) => !starterSessions.includes(session.sessionId),
            )
            if (opened === undefined) return false
            return (await activePane.getAttribute('data-session-id')) === opened.sessionId
          },
          { timeout: 180_000, message: 'the profile never opened a pane of its own' },
        )
        .toBe(true)
      const paneSessionId = await activePane.getAttribute('data-session-id')
      expect(paneSessionId).not.toBeNull()

      // WHAT OPENED THIS PANE is structural rather than a second RPC: an ssh
      // pane exists ONLY because a helper claimed the destination
      // (internal/transport/session_open.go refuses every other route for a
      // remote kind by name), so the pane above IS the helper's dial and not a
      // conventional one wearing the same tab.

      // Files on the SAME host, and the evidence is the panel's own claim about
      // what it is showing plus a listing it could only have got by asking:
      // `data-root="/"` is the rescope to a remote session (a local one roots
      // at its own cwd), and the rows below are the SFTP enumeration.
      //
      // The row is asserted, NOT its `data-selected` reveal: the reveal lands
      // only on a cwd the frontend VERIFIED, which a pane with no shell
      // integration never has — and requiring it would make this spec a test
      // of shell integration, which is not what any of its three criteria are
      // about (the pane's integration state is the product's, and it is
      // reported rather than assumed here).
      await showSidebarView(first, 'files')
      const panel = first.locator(FILES_PANEL)
      await expect(panel).toBeVisible({ timeout: 30_000 })
      await expect(panel).toHaveAttribute('data-root', '/', { timeout: 60_000 })
      // A listing that really happened: the SFTP enumeration put rows on
      // screen. `remoteBase` is deliberately NOT required to be among them —
      // that row's presence depended on the reveal, which needs the verified
      // cwd this pane does not have.
      await expect(first.locator(TREE_ROW).first()).toBeVisible({ timeout: 120_000 })

      // ── CRITERION 1: exactly ONE authenticated connection ───────────────
      //
      // Read AFTER both consumers have been served, because the number means
      // nothing before that: the pane's shell and Files' SFTP lease are two
      // channels, and this is the far host saying whether they arrived on one
      // connection or two. The rows above are the observable that says Files
      // really got a listing rather than being about to ask.
      expect(
        authenticatedConnections(fixture),
        'the far host authenticated more than one connection for one pane and one Files panel: the consumers did not share the helper pool',
      ).toBe(1)

      // ── STAGE 2: the runtime answers with no browser client attached ─────
      //
      // The program is started while a renderer exists (that is the only way a
      // person starts anything) and asks its question only after a trigger this
      // spec writes, which it writes only after `sessions.live` says the
      // session is unattached.
      writeFileSync(path.join(remoteHome, PROBE), PROBE_SCRIPT)
      const waiting = path.join(remoteHome, WAITING)
      const trigger = path.join(remoteHome, TRIGGER)
      const answerPath = path.join(remoteHome, ANSWER)
      const pidFile = path.join(remoteHome, PID)

      // THE FAR SHELL'S OWN PID, asked of the shell while a client is still
      // attached. This is the process the replacement coordinator must still be
      // holding at the end, and it is the one reading that needs no coordinator
      // to survive: the file is on the far host.
      await clickIntoEditor(first)
      await first.keyboard.type(`echo NOCXJOB=$$ > $HOME/${PID}`)
      await first.keyboard.press('Enter')
      await expect
        .poll(() => (existsSync(pidFile) ? 'written' : farSideReport(remoteHome)), {
          timeout: 60_000,
          intervals: [250],
        })
        .toBe('written')
      const shellPid = Number(/NOCXJOB=(\d+)/.exec(readFileSync(pidFile, 'utf8'))?.[1] ?? 0)
      expect(shellPid).toBeGreaterThan(0)

      await first.keyboard.type(`bash ${path.join(remoteHome, PROBE)}`)
      await first.keyboard.press('Enter')
      await expect
        .poll(() => (existsSync(waiting) ? 'parked' : farSideReport(remoteHome)), {
          timeout: 120_000,
        })
        .toBe('parked')

      // ── the window closes, and the coordinator says so ──────────────────
      await first.context().close()
      await expect
        .poll(
          async () =>
            (await liveSessions(endpoint)).find((s) => s.sessionId === paneSessionId)?.attached,
          {
            timeout: 60_000,
            message:
              'the client context closed but the coordinator still reports the session attached',
          },
        )
        .toBe(false)

      // Nothing has answered anything yet, and that is asserted rather than
      // implied: the answer file is the program's and it cannot exist before
      // the program asked.
      expect(existsSync(answerPath)).toBe(false)

      // ── the question, asked with nobody watching ────────────────────────
      writeFileSync(trigger, 'go\n')
      await expect
        .poll(() => (existsSync(answerPath) ? 'answered' : farSideReport(remoteHome)), {
          timeout: 120_000,
          intervals: [100],
        })
        .toBe('answered')

      const report = readFileSync(answerPath, 'utf8')
      expect(report).toMatch(CURSOR_REPORT)

      // And the report is the RUNTIME's own state rather than a constant: it
      // names a position inside the geometry this session actually has.
      const size = await effectiveSize(endpoint, paneSessionId!)
      // eslint-disable-next-line no-control-regex
      const [, row, column] = /^\u001b\[(\d+);(\d+)R$/.exec(report)!.map(Number)
      expect(row).toBeGreaterThanOrEqual(1)
      expect(column).toBeGreaterThanOrEqual(1)
      expect(row).toBeLessThanOrEqual(size.rows)
      expect(column).toBeLessThanOrEqual(size.cols)

      // The same fact from the wire side: what the program printed with the
      // answer in it is in the coordinator's recording of this session.
      const contentBefore = await recorded(endpoint, paneSessionId!)
      expect(contentBefore).toContain(`${DSR_LINE}${report}]`)

      // ── STAGE 3: the coordinator is replaced ────────────────────────────
      // The pane the session is the pipe of, read BEFORE the replacement: the
      // pairing is the coordinator's (AD-7), and the claim below is that the
      // replacing one kept it rather than attaching a bare shell nobody owns.
      const paneIdBefore = (await liveSessions(endpoint)).find(
        (session) => session.sessionId === paneSessionId,
      )?.paneId
      expect(paneIdBefore).not.toBeNull()

      backend.stop()
      expect(backend.running).toBe(false)
      endpoint = await backend.start()
      await unsealIfSealed(endpoint)

      // THE SAME SESSION, listed by the NEW coordinator, still keyed to the
      // pane it was the pipe of. A replacement that opened a fresh shell would
      // answer with a different id here — and everything below would be about
      // a different session.
      await expect
        .poll(
          async () => (await liveSessions(endpoint)).some((s) => s.sessionId === paneSessionId),
          {
            timeout: 180_000,
            intervals: [500],
            message:
              'the replacing coordinator does not list the helper-held session, so no restored pane can claim it',
          },
        )
        .toBe(true)
      const liveAfter = (await liveSessions(endpoint)).find((s) => s.sessionId === paneSessionId)!
      expect(liveAfter.paneId).toBe(paneIdBefore)

      // ── the same CONTENT, read through the new coordinator ──────────────
      //
      // The pane is drawn from this session's own stream, and the stream the
      // replacement reads still carries what the screen showed before it:
      // the far program's cursor report. Read from the coordinator's recording
      // because the terminal grid is a canvas — there is no DOM text to read
      // (e2e/coordinator-reclaim.spec.ts says the same thing about the same
      // surface).
      const contentAfter = await recorded(endpoint, paneSessionId!)
      expect(contentAfter).toContain(`${DSR_LINE}${report}]`)

      // ── the pane is back, and it still takes input ───────────────────────
      const returned = await freshClient(browser, endpoint)
      const pane = returned.locator(`.pane[data-session-id="${paneSessionId}"]`)
      await expect(pane).toBeVisible({ timeout: 180_000 })
      await promptReady(returned)
      await clickIntoEditor(returned)

      // THE SAME FAR SHELL, asked of the far side itself. The shell reports its
      // pid again, and the two lines of the file it writes must agree: a
      // replacement that had opened a fresh shell would put ITS pid there, and
      // a session whose runtime was recreated could not answer at all. The
      // kernel is then asked directly that the pid is still running — the far
      // host is this container, which is what makes that question askable from
      // here rather than only from the far side.
      await returned.keyboard.type(`echo NOCXJOB=$$ >> $HOME/${PID}`)
      await returned.keyboard.press('Enter')
      await expect
        .poll(
          () =>
            existsSync(pidFile) && readFileSync(pidFile, 'utf8').trim().split('\n').length >= 2
              ? 'written'
              : farSideReport(remoteHome),
          { timeout: 120_000, intervals: [250] },
        )
        .toBe('written')
      const reported = readFileSync(pidFile, 'utf8')
        .trim()
        .split('\n')
        .map((line) => Number(/NOCXJOB=(\d+)/.exec(line)?.[1] ?? 0))
      expect(reported).toEqual([shellPid, shellPid])
      expect(aliveFromThisMachine(shellPid)).toBe(true)

      // A LIVE WRITE THAT ROUND-TRIPS: typed into the pane the replacement
      // coordinator adopted, executed by the SAME far shell (which is why the
      // marker is arithmetic), and answered back through the helper.
      await returned.keyboard.type(ROUND_TRIP_COMMAND)
      await returned.keyboard.press('Enter')

      // The far side ran it, read through the coordinator's own recording...
      await expect
        .poll(async () => recorded(endpoint, paneSessionId!), {
          timeout: 120_000,
          intervals: [500],
          message: 'the far shell never ran the command typed after the coordinator was replaced',
        })
        .toContain(ROUND_TRIP_OUTPUT)
      // ...and the pane shows its output, which is the frozen block's own text
      // and cannot be the echoed command (the command spells `2121*2`).
      await expect(
        returned.locator('.pane.active .cmd-block').filter({ hasText: ROUND_TRIP_OUTPUT }),
      ).toBeVisible({ timeout: 120_000 })
    } finally {
      fixture?.proc.kill('SIGKILL')
      backend.stop()
      if (isolatedHome !== '') {
        await endHelperDaemon(isolatedHome)
      }
    }
  })
})

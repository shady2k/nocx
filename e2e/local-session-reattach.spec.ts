/**
 * e2e: a local pane's shell outlives the coordinator that opened it
 * (nocx-ie23r.5 — the last criterion of "quit nocx mid-build, open it again").
 *
 * WHAT A USER CAN DO THAT THEY COULD NOT BEFORE, and it is what this spec
 * watches: start something long in a local pane, quit nocx entirely, open it
 * again, and the SAME shell is there — same process, same session, the output
 * continuing where it left off, and the pane the pipe of it.
 *
 * # Why this is an e2e spec and not a Go test
 *
 * The criterion names `cmd/nocx-server`, the SHIPPED coordinator, and the
 * reason is the one internal/coordinator/launcher_test.go wrote down: building
 * that binary from a unit test makes the unit suite depend on a compiler run,
 * and its local helper artifact (`make helper-local`, which needs the pinned
 * Zig) is a build prerequisite of every target that produces a runnable
 * binary. This suite is where that binary exists and where a backend can be
 * restarted mid-test. The MECHANISM has a gate that runs everywhere —
 * internal/app/restart_screen_test.go, over the shipped App, two instances,
 * one home, a real helper and a real PTY — and this is the journey.
 *
 * # What is read, and from where
 *
 * The pid comes off the OS: the program prints its own pid, and Node asks the
 * kernel whether that pid is still there (`process.kill(pid, 0)`). Nothing
 * here trusts a record nocx wrote about itself, and the tick counter is the
 * second half of the same claim — a pid that is alive and a program that has
 * stopped printing are different failures, and a zombie would satisfy the
 * first alone.
 *
 * The output is read over the WIRE, not off the screen: `session.output` is
 * the backend's own recording of the session's byte stream, so what the spec
 * asserts is a pane's output continuing rather than a canvas happening to
 * repaint. That is also what makes the journey independent of which renderer
 * the browser chose.
 */
import { expect } from '@playwright/test'
import { execFileSync, spawnSync } from 'node:child_process'
import { mkdtempSync } from 'node:fs'
import { join } from 'node:path'
import { tmpdir } from 'node:os'
import {
  standalone as base,
  VaultBackend,
  bindEndpoint,
  openControlPlane,
  promptReady,
  type ControlPlane,
  type DisposableRoot,
} from './harness'
import { createHomeIsolation } from './home-isolation'
import { readStand } from './stand'

/** Lazily: the stand is started by globalSetup, which runs after Playwright
 *  has collected this file. */
const serverBin = () => readStand().server

const TABS = '.nocx-tab'

interface LiveSession {
  sessionId: string
  paneId: string | null
}

interface SessionOutput {
  runs: { body: string }[]
  /** The recording's end offset: where the next page starts. */
  produced: number
}

const test = base

/**
 * The program the pane runs, and it is written so BOTH facts are its own:
 * `$$` is the pid the shell reports for itself, and the tick is a number that
 * can only grow while it keeps running.
 *
 * The marker is split across the shell's own string concatenation so the pty's
 * ECHO of the typed line cannot be mistaken for the program's output — the
 * same trick internal/app/local_pane_test.go uses, and for the same reason.
 */
const TICKER =
  'echo "NOCXJOB=""$$"; i=0; while :; do i=$((i+1)); echo "NOCXTICK=""$i"; sleep 0.2; done'

/**
 * Everything the backend recorded for one session, as text.
 *
 * PAGED, because one answer is bounded by a per-answer byte budget and a
 * recording larger than it arrives over several calls: `produced` is the
 * recording's end offset, and a loop that stopped at the first answer would
 * search a prefix of the stream and call a tick that had not arrived yet
 * absent. `body` is Go `[]byte`, so it crosses as base64 and is decoded here.
 */
async function recorded(wire: ControlPlane, sessionId: string): Promise<string> {
  let from = 0
  let text = ''
  for (;;) {
    const answer = (await wire.call('session.output', { sessionId, from })) as SessionOutput
    text += answer.runs.map((run) => Buffer.from(run.body, 'base64').toString('utf8')).join('')
    if (answer.produced <= from || answer.runs.length === 0) return text
    from = answer.produced
  }
}

/** The pid the program reported for itself, and the highest tick so far. */
function progress(text: string): { pid: number; tick: number } {
  const pid = /NOCXJOB=(\d+)/.exec(text)
  let tick = 0
  for (const match of text.matchAll(/NOCXTICK=(\d+)/g)) {
    tick = Math.max(tick, Number(match[1]))
  }
  return { pid: pid ? Number(pid[1]) : 0, tick }
}

/**
 * Is this pid there, asked of the KERNEL?
 *
 * `process.kill(pid, 0)` sends no signal and reports whether the process
 * exists, which is the criterion's "read the pid from the operating system"
 * literally: nothing here consults a file, a log or a record nocx keeps.
 */
function aliveFromTheOS(pid: number): boolean {
  try {
    process.kill(pid, 0)
    return true
  } catch {
    return false
  }
}

/** The pids of this machine's helper daemons started under one home. */
function daemonsFor(isolatedHome: string): number[] {
  try {
    return execFileSync('pgrep', ['-f', join(isolatedHome, '.nocx', 'helper')], {
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

/**
 * End the helper daemons started under one home, the way the Go tests end
 * theirs — and for the same reason: the daemon is DETACHED by design (D1) and
 * nothing retires a generation yet (D2 is unimplemented), so a spec that
 * stopped only its own `nocx-server` would leave a process holding a PTY under
 * a home the run is finished with. `stopStand` does not do this either: it
 * signals vite and the stand's backend and nothing else.
 *
 * SIGTERM FIRST AND SIGKILL ONLY FOR WHAT IGNORED IT. A helper's SIGTERM path
 * is the one it ships with, `VaultBackend.stop` takes the same two steps for
 * the coordinator, and the escalation is what makes the guarantee real rather
 * than a request: neither daemon may outlive this spec.
 *
 * AND EACH PHASE IS WAITED FOR, on the OS's own answer (is this pid still
 * there) rather than on a duration, so a daemon that will not go is REPORTED —
 * a leak nobody is told about is the thing this function exists to prevent.
 * An empty home is a legitimate argument: a spec whose backend never started
 * started no daemon either.
 */
async function endDaemonsUnder(isolatedHome: string): Promise<void> {
  if (isolatedHome === '') return
  for (const signal of ['SIGTERM', 'SIGKILL'] as const) {
    const survivors = daemonsFor(isolatedHome)
    if (survivors.length === 0) return
    for (const pid of survivors) {
      try {
        process.kill(pid, signal)
      } catch {
        /* already gone */
      }
    }
    if (await daemonsGone(isolatedHome, 15_000)) return
  }
  throw new Error(
    `this spec's helper daemon(s) ${daemonsFor(isolatedHome).join(', ')} outlived SIGTERM and SIGKILL`,
  )
}

/** Whether every helper daemon under a home has gone, bounded by a deadline. */
async function daemonsGone(isolatedHome: string, withinMs: number): Promise<boolean> {
  const deadline = Date.now() + withinMs
  for (;;) {
    if (daemonsFor(isolatedHome).length === 0) return true
    if (Date.now() > deadline) return false
    const { promise, resolve: resume } = Promise.withResolvers<void>()
    setTimeout(resume, 50)
    await promise
  }
}

test.describe('a local pane survives the coordinator being replaced (nocx-ie23r.5)', () => {
  let home: DisposableRoot
  let backend: VaultBackend

  test.beforeEach(() => {
    home = { root: mkdtempSync(join(tmpdir(), 'nocx-local-reattach-')) }
    backend = new VaultBackend(serverBin(), home)
  })

  test.afterEach(async () => {
    backend?.stop()
    let isolatedHome = ''
    try {
      isolatedHome = backend.isolatedHome
    } catch {
      /* the backend never started, so it started no daemon either */
    }
    await endDaemonsUnder(isolatedHome)
  })

  test('the same shell is still running in the pane after the coordinator is replaced', async ({
    page,
  }) => {
    const ep1 = await backend.start()
    await bindEndpoint(page, ep1)
    await page.goto('/')
    await promptReady(page)
    await expect(page.locator(TABS)).toHaveCount(1, { timeout: 30_000 })

    // The pane the renderer opened. Its id is the renderer's and the session id
    // is the backend's (AD-7), and the criterion is about the SECOND: the shell
    // the daemon minted must be the one the replacement takes back, keyed to
    // the pane it was the pipe of.
    const wire1 = await openControlPlane(ep1.port, ep1.token)
    const live1 = (await wire1.call('sessions.live', {})) as { sessions: LiveSession[] }
    expect(live1.sessions).toHaveLength(1)
    const { sessionId, paneId } = live1.sessions[0]

    // START THE LONG-RUNNING PROGRAM IN THE LOCAL PANE, the way a person does.
    await page.keyboard.type(TICKER)
    await page.keyboard.press('Enter')

    // WAIT ON THE OBSERVABLE, never on a duration: the pid the program printed
    // for itself and at least one tick beside it.
    await expect
      .poll(async () => progress(await recorded(wire1, sessionId)).pid, { timeout: 30_000 })
      .toBeGreaterThan(0)
    const before = progress(await recorded(wire1, sessionId))
    expect(before.tick).toBeGreaterThan(0)
    expect(aliveFromTheOS(before.pid)).toBe(true)

    // Let it run past the point where the first ticks were read, so "the output
    // continues" afterwards is a claim about NEW bytes rather than a repeat of
    // what was already recorded.
    const seen = before.tick
    await expect
      .poll(async () => progress(await recorded(wire1, sessionId)).tick, { timeout: 30_000 })
      .toBeGreaterThan(seen)

    // ── THE COORDINATOR IS REPLACED, and nothing of it survives ────────────
    //
    // The first control plane is CLOSED first: it points at a port the next
    // coordinator will not have, and a socket left open across the restart
    // would be a reader of a process that is gone.
    wire1.close()
    const ep2 = await backend.restart()
    await bindEndpoint(page, ep2)
    await page.reload()
    await promptReady(page)

    // THE PANE CAME BACK ON THE SAME SESSION, listed by the NEW coordinator and
    // still keyed to the pane it was the pipe of. A replacement that opened a
    // fresh shell instead would answer with a different session id here (and no
    // further ticks below).
    const wire2 = await openControlPlane(ep2.port, ep2.token)
    const live2 = (await wire2.call('sessions.live', {})) as { sessions: LiveSession[] }
    expect(live2.sessions).toHaveLength(1)
    expect(live2.sessions[0].sessionId).toBe(sessionId)
    expect(live2.sessions[0].paneId).toBe(paneId)

    // THE SAME PROCESS, read from the operating system, and still producing:
    // the pid is the one the FIRST coordinator's pane started, and the tick
    // counter has moved past everything recorded before the restart.
    expect(await recorded(wire2, sessionId)).not.toBe('')
    expect(aliveFromTheOS(before.pid)).toBe(true)
    await expect
      .poll(async () => progress(await recorded(wire2, sessionId)).tick, { timeout: 30_000 })
      .toBeGreaterThan(seen)
    expect(progress(await recorded(wire2, sessionId)).pid).toBe(before.pid)

    // ── A SECOND COORDINATOR FOR THE SAME GENERATION IS REFUSED ───────────
    //
    // Two nocx against one home is the documented refusal and not a race: the
    // app-directory lock makes the second one exit 3 — "another nocx-server
    // already holds this app directory", which a launcher reads as a daemon
    // already running and not as a crash to recover from — and the coordinator
    // that IS serving goes on serving: the same session, the same pane, the
    // same process. What a second one must never do is raise a second helper
    // beside the daemon holding the shells, and the session still being spoken
    // for by its own pid is the observable that says it did not.
    const second = spawnSync(serverBin(), [], {
      env: createHomeIsolation({ inheritedEnv: process.env, root: home.root })
        .env as NodeJS.ProcessEnv,
      timeout: 60_000,
      encoding: 'utf8',
    })
    expect(second.status).toBe(3)
    expect((await wire2.call('sessions.live', {})) as { sessions: LiveSession[] }).toMatchObject({
      sessions: [{ sessionId, paneId }],
    })
    expect(aliveFromTheOS(before.pid)).toBe(true)

    wire2.close()
  })
})

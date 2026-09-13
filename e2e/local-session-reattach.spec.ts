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

/** Everything the backend recorded for one session, as text. */
async function recorded(wire: ControlPlane, sessionId: string): Promise<string> {
  const answer = (await wire.call('session.output', { sessionId, from: 0 })) as SessionOutput
  return answer.runs.map((run) => Buffer.from(run.body, 'base64').toString('utf8')).join('')
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

/** Every process on this machine running this backend's own helper daemon. */
function daemonsFor(isolatedHome: string): string[] {
  const pattern = join(isolatedHome, '.nocx', 'helper')
  try {
    return execFileSync('pgrep', ['-f', pattern], { encoding: 'utf8' })
      .split('\n')
      .map((line) => line.trim())
      .filter((line) => line !== '')
  } catch {
    // pgrep exits non-zero when nothing matches, which is an answer.
    return []
  }
}

test.describe('a local pane survives the coordinator being replaced (nocx-ie23r.5)', () => {
  let home: DisposableRoot
  let backend: VaultBackend

  test.beforeEach(() => {
    home = { root: mkdtempSync(join(tmpdir(), 'nocx-local-reattach-')) }
    backend = new VaultBackend(serverBin(), home)
  })

  test.afterEach(() => {
    backend?.stop()
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

    // ── TWO COORDINATORS, ONE SERVING DAEMON, NO ERROR SURFACE ────────────
    //
    // A second `nocx-server` over the same home. It must refuse with the
    // documented already-running status (3, which is the lock working and not a
    // crash a launcher would try to recover from), it must NOT raise a second
    // daemon beside the one holding the shells, and the coordinator that is
    // serving must go on serving with the pane's process untouched.
    const second = spawnSync(serverBin(), [], {
      env: createHomeIsolation({ inheritedEnv: process.env, root: home.root })
        .env as NodeJS.ProcessEnv,
      timeout: 60_000,
      encoding: 'utf8',
    })
    expect(second.status).toBe(3)
    expect(daemonsFor(backend.isolatedHome)).toHaveLength(1)
    expect((await wire2.call('sessions.live', {})) as { sessions: LiveSession[] }).toMatchObject({
      sessions: [{ sessionId, paneId }],
    })
    expect(aliveFromTheOS(before.pid)).toBe(true)

    wire1.close()
    wire2.close()
  })
})

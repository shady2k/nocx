/**
 * THE EPIC'S ACCEPTANCE TEST (nocx-p2q1q, task nocx-gpyxp; design §7).
 *
 * What a user can do that they could not before: start a long command, quit
 * nocx entirely, reopen it, and find that same pane still running, with the
 * output produced while the app was closed.
 *
 * With `nocx-server` running headless and the client being a page, "the client
 * went away" is literal — the browser CONTEXT is closed, not reloaded, so the
 * renderer that held the session map is gone the way a quit window is gone.
 * A reload would prove nothing: the same profile, the same storage and, on a
 * fast machine, the same socket coming back before anything noticed.
 *
 * THE THREE CONDITIONS, without which this run is green and proves nothing
 * (design §7, and each is asserted below rather than assumed):
 *
 *  1. The command produces MORE THAN 256 KiB while detached. The replay ring
 *     is 256 KiB (internal/transport/ring.go) and blocks its writer when full,
 *     with the acks coming from a client — so with nobody attached, a session
 *     that stayed inside the ring would prove only that a small buffer holds
 *     small output. Crossing the ring is what exercises the recorder, and it
 *     is what makes the reclaim have to join a recording to a ring replay.
 *  2. The command LEAVES A TRACE IN TIME — one marker per second — and the
 *     markers spanning the detached window are counted AFTER the return. That
 *     is what proves the process RAN while nothing was attached, rather than
 *     that a pane rendered afterwards. A byte count alone cannot tell a
 *     process that ran for twenty seconds from one that was resumed and
 *     flushed.
 *  3. The second page is GENUINELY FRESH. An empty renderer `sessions` map is
 *     precisely the state that was broken before this task: `frontend/src/
 *     ipc.ts`'s map is renderer process memory and the reconnect pass
 *     reattaches only what is in it, so a fresh window reattached nothing and
 *     the live PTYs were orphaned with no way to find them.
 *
 * WHY THE FILLER IS OVERWRITTEN RATHER THAN PRINTED. Each second the command
 * writes one marker LINE and then several hundred short chunks each ending in
 * a carriage return, which advance the stream offset without advancing the
 * screen. The bytes are what the ring and the recorder count, and they are
 * real; the rows are what the browser pays for. Printing a quarter of a
 * megabyte as lines would put ~4000 rows in the page for no assertion's
 * benefit.
 *
 * WHERE EACH HALF IS ASSERTED, and why it is not all on screen. The terminal
 * grid is drawn to a CANVAS — there is no DOM text in it, and a locator
 * cannot read a row. What the page does carry is the pane's own statements
 * about itself: `data-session-id` on the pane element (written by the content
 * that holds the session), and the command block with its header and its
 * running mark. Those are what say "this tab is the pane that never stopped",
 * and they are asserted on screen. The BYTES and the MARKERS are read off the
 * backend's recording — which is not a second source: it is exactly what the
 * reclaim feeds into the pane, read through `session.output`, the method the
 * renderer itself uses for the same purpose.
 *
 * WHY THIS SPEC OWNS ITS BACKEND. It closes and reopens a client against one
 * long-lived server, and it needs a chain holding exactly its own pane — the
 * shared stand accumulates every other spec's tabs, and a restore that put a
 * dozen of them on screen would be measuring somebody else's window.
 */
import { test as base, expect, type Browser, type Page } from '@playwright/test'
import { mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { BASE_URL } from './base-url'
import {
  VaultBackend,
  bindEndpoint,
  clickIntoEditor,
  openControlPlane,
  promptReady,
  type BackendEndpoint,
  type DisposableRoot,
} from './harness'
import { readStand } from './stand'

const test = base

/** The ring's size, and therefore the floor condition 1 sets. */
const RING_BYTES = 256 * 1024

/** A block the pane says is running, in the tab in front. The command text is
 *  in its header, which IS DOM — unlike the terminal grid it prints into. */
const RUNNING_BLOCK = '.pane.active .cmd-block.cmd-block-running'

/** How far the command's own per-second counter must advance while nobody is
 *  attached. Not a duration this test waits out — it waits on the BYTE count
 *  (condition 1), which takes longer than this — but the floor below which
 *  "the process ran" would be indistinguishable from "it was resumed". */
const MARKERS_WHILE_DETACHED = 5

/**
 * One marker line a second, then filler that costs bytes and not rows.
 *
 * POSIX sh, because the pane runs the host's real login shell — bash in the
 * container, zsh on a stock Mac — and nothing here may depend on which.
 */
const FILL = 'x'.repeat(60)
const LONG_COMMAND =
  `i=0; while [ "$i" -lt 400 ]; do printf 'NOCXMARK-%03d\\n' "$i"; ` +
  `j=0; while [ "$j" -lt 260 ]; do printf '%s\\r' '${FILL}'; j=$((j+1)); done; ` +
  `i=$((i+1)); sleep 1; done`

interface LiveSession {
  sessionId: string
  instanceId: string
  sessionEpoch: number
  paneId: string | null
  replayFrom: number
  attached: boolean
}

/**
 * A control-plane socket of the TEST'S own, opened for one question and shut
 * again.
 *
 * It is not the pane's client and never attaches to a session: `attach` is an
 * explicit call and this makes none, so the session stays unattached
 * throughout the detached window. What it is for is measuring — asking the
 * backend how much the session has produced — because during that window
 * there is no page to ask through, which is the whole point of the window.
 */
async function ask<T>(ep: BackendEndpoint, method: string, params: unknown): Promise<T> {
  const wire = await openControlPlane(ep.port, ep.token)
  try {
    return (await wire.call(method, params)) as T
  } finally {
    wire.close()
  }
}

async function liveSessions(ep: BackendEndpoint): Promise<LiveSession[]> {
  const result = await ask<{ sessions: LiveSession[] }>(ep, 'sessions.live', {})
  return result.sessions
}

interface Recording {
  produced: number
  runs: { offset: number; body: string }[]
}

/** What the backend has recorded for this session from `from` onward, and how
 *  far its stream has got. `produced` is the recording's end offset, in the
 *  same coordinate the ring and the ack speak. */
function recording(ep: BackendEndpoint, live: LiveSession, from: number): Promise<Recording> {
  return ask<Recording>(ep, 'session.output', {
    sessionId: live.sessionId,
    instanceId: live.instanceId,
    sessionEpoch: live.sessionEpoch,
    from,
  })
}

/** The recording, whole, decoded, in stream order. Used once — after the
 *  return — to count what happened while nothing was attached. */
async function recordedText(ep: BackendEndpoint, live: LiveSession): Promise<string> {
  let text = ''
  let at = 0
  for (;;) {
    const page = await recording(ep, live, at)
    let cursor = at
    for (const run of page.runs) {
      text += Buffer.from(run.body, 'base64').toString('utf8')
      cursor = run.offset + Buffer.from(run.body, 'base64').length
    }
    if (cursor <= at || cursor >= page.produced) return text
    at = cursor
  }
}

/** Every marker index the recording carries, in the order it carries them. */
function markersIn(text: string): number[] {
  return [...text.matchAll(/NOCXMARK-(\d{3})/g)].map((m) => Number(m[1]))
}

/**
 * The highest marker the recording has reached, or -1 before the first one.
 *
 * THE INDEX, NOT THE COUNT, and that distinction is the whole of condition 2.
 * The index is the command's own counter and advances once a second no matter
 * what anybody records; a count is a property of the RECORDING, which AD-10
 * permits to have holes and which `session.output` reports gaps for. Asserting
 * a contiguous count would be asserting that the recorder is lossless — a
 * different claim, owned by nocx-22k1c, and one this test would fail for the
 * wrong reason.
 */
function lastMarker(text: string): number {
  const seen = markersIn(text)
  return seen.length === 0 ? -1 : Math.max(...seen)
}

/** Open a page on a context of its own, bound to this backend. Two of these
 *  never share storage, a renderer or a socket — which is condition 3. */
async function freshClient(browser: Browser, ep: BackendEndpoint): Promise<Page> {
  // baseURL explicitly: a context made by hand inherits nothing from the
  // config's `use`, so `goto('/')` would have no origin to resolve against.
  const context = await browser.newContext({ baseURL: BASE_URL })
  const page = await context.newPage()
  await bindEndpoint(page, ep)
  await page.goto('/')
  return page
}

test.describe('the coordinator outlives its window', () => {
  let root: DisposableRoot
  let backend: VaultBackend

  test.beforeEach(() => {
    root = { root: mkdtempSync(join(tmpdir(), 'nocx-reclaim-')) }
    backend = new VaultBackend(readStand().server, root)
  })

  test.afterEach(() => {
    backend?.stop()
  })

  test('a fresh client reclaims the pane, and the work ran while nobody watched', async ({
    browser,
  }) => {
    // Generous, and spent on the DETACHED WINDOW rather than on waiting: the
    // run below waits on the backend's own byte count, so what this bounds is
    // how long a quarter of a megabyte may take to appear at ~15 KB/s, not a
    // sleep anybody chose.
    test.setTimeout(300_000)

    const ep = await backend.start()

    // ── the window a person had open ──────────────────────────────────────
    const first = await freshClient(browser, ep)
    await promptReady(first)
    await clickIntoEditor(first)
    await first.keyboard.type(LONG_COMMAND)
    await first.keyboard.press('Enter')

    // The command is RUNNING before anything is closed — "it was typed" and
    // "it started" are different facts and only the second one makes the rest
    // of this test mean anything. Both halves: the pane says it is running
    // something, and the backend has bytes to show for it.
    await expect(first.locator(RUNNING_BLOCK).filter({ hasText: 'NOCXMARK' })).toBeVisible({
      timeout: 60_000,
    })

    // The session's server-issued identity, and the pane it is the pipe of.
    // This is what the fresh client will reclaim BY, and the association is
    // the backend's — the renderer minted the pane id, the backend recorded
    // it at open, and neither half is guessed here.
    const before = await liveSessions(ep)
    expect(before).toHaveLength(1)
    const live = before[0]
    expect(live.paneId).not.toBeNull()
    expect(live.attached).toBe(true)

    const paneOnFirstPage = first.locator(`.pane[data-session-id="${live.sessionId}"]`)
    await expect(paneOnFirstPage).toBeVisible()

    // It has actually printed a marker, so "it ran" has a floor to be measured
    // from. Polled on the recording rather than on the screen, for the reason
    // in the header: the grid is a canvas.
    await expect
      .poll(async () => lastMarker(await recordedText(ep, live)), { timeout: 60_000 })
      .toBeGreaterThanOrEqual(0)

    const producedAtDetach = (await recording(ep, live, 0)).produced
    const markerAtDetach = lastMarker(await recordedText(ep, live))

    // ── the window closes, ENTIRELY ───────────────────────────────────────
    await first.context().close()
    const detachedAt = Date.now()

    // Nobody is attached now, and the backend says so rather than the test
    // assuming it. Without this the rest could hold with a client still on
    // the session, which is the ordinary case and not this one.
    await expect
      .poll(async () => (await liveSessions(ep))[0]?.attached, { timeout: 30_000 })
      .toBe(false)

    // ── condition 1: more than the ring, produced while detached ──────────
    //
    // WAITED ON AS A STATE, not as a duration: the poll asks the backend how
    // far the stream has got and returns the moment it has crossed the ring.
    // A `sleep 20` would be the same number on a fast machine and the wrong
    // one on a slow one, which is exactly the test AGENTS.md forbids.
    await expect
      .poll(async () => (await recording(ep, live, producedAtDetach)).produced - producedAtDetach, {
        timeout: 240_000,
        intervals: [1_000],
      })
      .toBeGreaterThan(RING_BYTES)
    const detachedFor = Date.now() - detachedAt

    // ── the client comes back, and it is a stranger ───────────────────────
    //
    // A NEW CONTEXT: new storage, new renderer, an empty `sessions` map. It
    // has never seen this session and cannot reattach from memory — the only
    // route to the pane is `sessions.live` and the claim, which is condition
    // 3 and the step this task exists to build.
    const second = await freshClient(browser, ep)

    // THE SAME SESSION, IN THE SAME PANE. `data-session-id` is written by the
    // pane that holds the session, so this is the product's own statement
    // that the tab in front is running the process that never stopped — not a
    // new shell wearing the old tab's name.
    await expect(second.locator(`.pane[data-session-id="${live.sessionId}"]`)).toBeVisible({
      timeout: 120_000,
    })
    const after = await liveSessions(ep)
    expect(after).toHaveLength(1)
    expect(after[0].sessionId).toBe(live.sessionId)
    expect(after[0].sessionEpoch).toBe(live.sessionEpoch)
    expect(after[0].instanceId).toBe(live.instanceId)
    expect(after[0].paneId).toBe(live.paneId)
    expect(after[0].attached).toBe(true)

    // ── condition 2: the markers, counted after the return ────────────────
    //
    // The command's own counter is the trace in time: it advances once a
    // second, so the distance it covered across the detached window is how
    // many seconds the process was RUNNING with nobody attached. A process
    // that had been suspended and resumed, or one whose pane merely rendered
    // afterwards, cannot move it.
    const text = await recordedText(ep, live)
    const markers = markersIn(text)
    const markerAtReturn = lastMarker(text)
    // In stream order, which is what says these are one process's output and
    // not two runs' spliced together.
    for (let i = 1; i < markers.length; i++) expect(markers[i]).toBeGreaterThan(markers[i - 1])
    // The window really was long enough for this many seconds to pass — it was
    // bounded by the BYTE count above, not chosen, so this states what that
    // wait bought rather than assuming it.
    expect(detachedFor).toBeGreaterThan(MARKERS_WHILE_DETACHED * 1000)
    expect(markerAtReturn - markerAtDetach).toBeGreaterThanOrEqual(MARKERS_WHILE_DETACHED)

    // AND THE PERSON CAN SEE IT. The epic's sentence is "find that same pane
    // STILL RUNNING", so the fresh window has to show the work, not merely
    // hold the session id: the command block is back, with its header and its
    // running mark, drawn from the recording the reclaim fed into the pane.
    await expect(second.locator(RUNNING_BLOCK).filter({ hasText: 'NOCXMARK' })).toBeVisible({
      timeout: 120_000,
    })

    // Still running THROUGH the reclaim, not merely up to it: markers the
    // session had not printed when the fresh client attached keep arriving.
    await expect
      .poll(async () => lastMarker(await recordedText(ep, live)), {
        timeout: 60_000,
        intervals: [1_000],
      })
      .toBeGreaterThan(markerAtReturn)

    await second.context().close()
  })
})

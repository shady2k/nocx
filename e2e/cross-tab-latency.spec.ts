/**
 * HEAVY ACTIVITY IN ONE TAB MUST NOT SLOW THE TAB A PERSON IS USING
 * (nocx-07ymy, GitHub #692).
 *
 * ── What was measured, and where the time went ───────────────────────────
 *
 * Not the backend. With a real App, a real helper and two real shells, a
 * producer in A pushing ~300 frames/s at B's command round trip moved it from
 * 9.7 ms to 17.0 ms at the wire (p50), and B's own intent answer from 1.3 ms
 * to 3.6 ms — a few milliseconds on a box where A's ingest and the producer
 * between them were using most of two cores.
 *
 * The page's ONE main thread was the layer. Every screen frame of every pane
 * is applied there, hidden or not: the cell model decodes the whole 100×30
 * grid at 0.88 ms a frame (JSON.parse is 0.10 ms of it), so a tab repainting
 * 300 times a second spent 27–32 % of the thread on a pane nobody was
 * looking at — and the tab beside it saw a keystroke wait 20 → 35 ms and a
 * command's output arrive 2.2× later.
 *
 * The fix is in the renderer: a hidden pane applies the FIRST frame of a
 * burst at once and then at most one per animation frame, keeping the newest
 * (frames are whole snapshots and explicitly coalescable, ADR-0066).
 *
 * ── Why the bound is a ratio ─────────────────────────────────────────────
 *
 * An absolute duration here would measure the CI runner, not the product.
 * Every number below is compared with the same page's own behaviour moments
 * earlier, while A is idle, in the same browser and the same run — the shape
 * `e2e/frame-budget.mts` uses for the same reason. The premise that A is
 * actually loading the page (frames per second received) is asserted first,
 * so a fixture that produced nothing cannot pass by being quiet.
 */
import { readdirSync, readFileSync, writeFileSync } from 'node:fs'
import { test, expect, promptReady, clickIntoEditor } from './harness'
import { readStand } from './stand'

const TAB = '.nocx-tab'
const TAB_ADD = '[aria-label="New tab"]'
const GRID = '.pane.active [role="grid"][aria-label="Terminal output"]'

/** Repaints per second the producing tab runs at. A `cat` of a build log or
 *  an agent streaming output is measured in hundreds of frames a second; 200
 *  is the heavy end of ordinary, not a synthetic maximum. */
const PRODUCER_HZ = 200
/** A tab that is not loading the page proves nothing, so the phase refuses to
 *  be quiet about it. Ten frames a second is the floor internal/app's wire
 *  check uses for the same reason: the producer's own rate is the machine's,
 *  and a floor near it asserts the runner (measured here: 71-130 frames/s on
 *  the 2-CPU cap, and one run printed 91 against a floor of 100 while A was
 *  plainly repainting). What the scenario needs at any speed is stated beside
 *  the assertion: A repainted faster than the page could display. */
const MIN_PRODUCER_FRAMES_PER_SECOND = 10

interface Sample {
  kind: 'key' | 'command'
  /** The instrument's own sequence number, so a command reads ITS sample and
   *  never a neighbour's (see `__xtStart`). */
  seq: number
  ms: number
}

/** What the instrument puts on `window`, so the spec reads it typed. */
interface XtPage extends Window {
  __xtSamples: Sample[]
  __xtFrames: Record<string, FrameCount>
  __xtRaf: number[]
  __xtRafStart: () => void
  /** Begin one command's wait and return its sequence number. */
  __xtStart: (marker: string) => number
}

interface FrameCount {
  n: number
  bytes: number
  ms: number
  /** Each frame's handler duration, in arrival order. */
  each: number[]
}

/** The instrument, installed before the app's own scripts.
 *
 *  keyMs:     a keydown to the next task — how long the main thread was
 *             unavailable after taking the key, which is the input lag a
 *             person feels.
 *  commandMs: the Enter keydown to the moment the command's marker is in the
 *             grid's DOM — the visible answer.
 *  frames:    binary frames received per session and the time the socket
 *             handler spent on them. The setter wrap is the only place that
 *             cost is visible without changing the product.
 */
async function installInstrument(page: import('@playwright/test').Page): Promise<void> {
  await page.addInitScript(() => {
    const w = window as unknown as XtPage
    w.__xtSamples = []
    w.__xtFrames = {}
    let sampleSeq = 0
    // THE COMMAND WHOSE ENTER HAS NOT LANDED YET, with that Enter's own time.
    // It used to be one global `__xtEnter` plus a `__xtMarker` string, and a
    // tick that outlived its command then read whatever the NEXT command had
    // set: a sample computed from another command's Enter, or from 0 —
    // `performance.now()` is the page's uptime, and those arrived as
    // 9129 ms and 10732 ms "latencies" in the idle phase of runs that finished
    // in 49 s. One wait per command, owned by that command, removes the path.
    let pending: { marker: string; seq: number; enter: number } | null = null
    window.addEventListener(
      'keydown',
      (event) => {
        const t0 = performance.now()
        setTimeout(
          () => w.__xtSamples.push({ kind: 'key', seq: ++sampleSeq, ms: performance.now() - t0 }),
          0,
        )
        if (pending !== null && pending.enter === 0 && (event as KeyboardEvent).key === 'Enter') {
          pending.enter = performance.now()
        }
      },
      true,
    )
    // THE PAGE'S OWN CADENCE. A page whose animation frames stretch while
    // another tab is busy is not doing work for that tab — it is waiting for
    // a core. The two are different defects and this is what tells them
    // apart.
    ;(w as unknown as { __xtRaf: number[] }).__xtRaf = []
    // ONE cadence loop at a time: the phase reads this array as the page's own
    // display rate, and a loop left running from the previous phase would push
    // a second sample per frame and double the count the premise reads.
    let rafGeneration = 0
    ;(w as unknown as { __xtRafStart: () => void }).__xtRafStart = () => {
      const generation = ++rafGeneration
      const samples = (w as unknown as { __xtRaf: number[] }).__xtRaf
      samples.length = 0
      let last = performance.now()
      const tick = (): void => {
        if (generation !== rafGeneration) return
        const now = performance.now()
        samples.push(now - last)
        last = now
        if (samples.length < 4000) requestAnimationFrame(tick)
      }
      requestAnimationFrame(tick)
    }
    w.__xtStart = (marker: string): number => {
      const state = { marker, seq: ++sampleSeq, enter: 0 }
      pending = state
      const tick = (): void => {
        // A later command owns the wait now; this tick must not report for it.
        if (pending !== state) return
        const grid = document.querySelector(
          '.pane.active [role="grid"][aria-label="Terminal output"]',
        )
        if (!(grid?.textContent ?? '').includes(marker)) {
          requestAnimationFrame(tick)
          return
        }
        // The text can reach the grid before the Enter keydown has been
        // dispatched. Reporting now would read 0, which is the page's uptime,
        // so the wait belongs to a key that has not arrived yet.
        if (state.enter === 0) {
          requestAnimationFrame(tick)
          return
        }
        w.__xtSamples.push({ kind: 'command', seq: state.seq, ms: performance.now() - state.enter })
        pending = null
      }
      requestAnimationFrame(tick)
      return state.seq
    }
    const proto = WebSocket.prototype
    const desc = Object.getOwnPropertyDescriptor(proto, 'onmessage')
    if (desc?.set && desc.get) {
      Object.defineProperty(proto, 'onmessage', {
        configurable: true,
        get() {
          return desc.get!.call(this)
        },
        set(fn: ((ev: MessageEvent) => void) | null) {
          if (fn === null) return desc.set!.call(this, null)
          desc.set!.call(this, function (this: WebSocket, ev: MessageEvent) {
            const t0 = performance.now()
            try {
              fn.call(this, ev)
            } finally {
              if (ev.data instanceof ArrayBuffer) {
                const bytes = new Uint8Array(ev.data)
                // Frame header: version, msg-type, then the 16-byte session id.
                const sid = Array.from(bytes.slice(2, 18))
                  .map((b) => b.toString(16).padStart(2, '0'))
                  .join('')
                const rec = (w.__xtFrames[sid] ??= { n: 0, bytes: 0, ms: 0, each: [] })
                const spent = performance.now() - t0
                rec.n++
                rec.bytes += bytes.length
                rec.ms += spent
                if (rec.each.length < 4000) rec.each.push(spent)
              }
            }
          })
        },
      })
    }
  })
}

/** The producing tab's program: omp's own shape — a whole 100×30 grid per
 *  frame, no scrolling, inside synchronized output — at a chosen cadence. */
const PRODUCER = String.raw`
const rows = 30, cols = 100
let n = 0
const hz = Number(process.argv[2] ?? '200')
const draw = () => {
  let s = '\x1b[?2026h\x1b[H\x1b[2J'
  s += 'OMP 18.10.4  turn ' + (n++) + '  GLM-5.3 /work/nocx 1M\r\n'
  for (let i = 0; i < rows - 2; i++) s += ('\u2502 working line ' + i + ': internal/sessionruntime/runtime.go').padEnd(cols) + '\r\n'
  s += '\x1b[?2026l'
  process.stdout.write(s)
}
process.stdout.write('\x1b[?1049h')
setInterval(draw, Math.round(1000 / hz))
`

/** CPU seconds per process name, read from /proc. Every process here shares
 *  the container's cores, so "who was busy" is the first question a latency
 *  difference asks. */
function cpuByProcess(): Record<string, number> {
  const out: Record<string, number> = {}
  for (const pid of readdirSync('/proc')) {
    if (!/^[0-9]+$/.test(pid)) continue
    try {
      const stat = readFileSync(`/proc/${pid}/stat`, 'utf8')
      const name = stat.slice(stat.indexOf('(') + 1, stat.lastIndexOf(')'))
      const fields = stat
        .slice(stat.lastIndexOf(')') + 1)
        .trim()
        .split(/\s+/)
      out[name] = (out[name] ?? 0) + (Number(fields[11]) + Number(fields[12])) / 100
    } catch {
      // A process that exited between readdir and read is not a finding.
    }
  }
  return out
}

/** What the handler spends on one frame, in seconds, per second of wall time —
 *  the share of the page's one thread a session costs. */
const percentile = (xs: number[], fraction: number): number => {
  const sorted = [...xs].sort((a, b) => a - b)
  return sorted[Math.min(sorted.length - 1, Math.floor(fraction * sorted.length))] ?? 0
}

const samplesOf = async (
  page: import('@playwright/test').Page,
  kind: Sample['kind'],
): Promise<Sample[]> => {
  const all = await page.evaluate(() => (window as unknown as XtPage).__xtSamples)
  return all.filter((s) => s.kind === kind)
}

const framesOf = (page: import('@playwright/test').Page): Promise<Record<string, FrameCount>> =>
  page.evaluate(() => (window as unknown as XtPage).__xtFrames)

/** The draft in the active composer, with the completion's ghost removed —
 *  what the person typed, not what the editor renders. The same reader
 *  e2e/command-editor.spec.ts uses, and for the same reason: filling a locator
 *  would bypass the keyboard this check is measuring. */
const draft = (page: import('@playwright/test').Page): Promise<string | null> =>
  page.evaluate(() => {
    const content = document.querySelector<HTMLElement>('.pane.active .nocx-editor-input')
    if (content === null) return null
    const copy = content.cloneNode(true) as HTMLElement
    for (const ghost of copy.querySelectorAll('.nocx-editor-ghost')) ghost.remove()
    return copy.textContent ?? ''
  })

/** Type `n` keys into the editor and return each one's main-thread lag.
 *
 *  EACH KEY IS WAITED FOR, and the line is cleared by the product's own
 *  Escape. Typing a burst and then pressing Ctrl-U left the tail of the burst
 *  in the composer, and the next command arrived at the shell as one word:
 *  `abcdprintf 'XT-%s-%s\n' IDLE 0` and `abcdefgprintf …` both measured here,
 *  bash answered "command not found", and the marker the check waits for never
 *  appeared. The editor commits input asynchronously; the clear ran between
 *  two commits. Waiting for the draft first makes the fixture's own
 *  precondition true — the line is exactly what was typed — and the number
 *  below is still each keydown's wait for its next task, not the wait for the
 *  draft.
 */
async function keyboardLag(page: import('@playwright/test').Page, n: number): Promise<number[]> {
  const typed = 'abcdefghijklmnopqrstuvwxyz'
  await expect.poll(() => draft(page), { timeout: 10_000 }).toBe('')
  const before = (await samplesOf(page, 'key')).length
  for (let i = 0; i < n; i++) {
    await page.keyboard.type(typed[i % typed.length])
    await expect.poll(() => draft(page), { timeout: 10_000 }).toBe(typed.slice(0, i + 1))
  }
  await page.keyboard.press('Escape')
  await expect.poll(() => draft(page), { timeout: 10_000 }).toBe('')
  return (await samplesOf(page, 'key')).slice(before, before + n).map((s) => s.ms)
}

/** Run one command and return the Enter-to-visible latency.
 *
 *  THE MARKER IS NOT IN THE TYPED LINE: the shell echoes what was typed into
 *  the grid at once, so a command carrying its own marker would be "seen"
 *  before it ran. `printf` assembles the marker from two words instead.
 *
 *  THE SAMPLE IS THIS COMMAND'S OWN, and it is waited for. The page reports it
 *  from an animation-frame tick after the marker is in the grid; reading the
 *  list the moment Playwright saw the text returned the command before it — or
 *  nothing. A misread sample is how a p50 of eight moved by an order of
 *  magnitude between two runs of the same tree.
 */
async function commandLatency(
  page: import('@playwright/test').Page,
  marker: string,
): Promise<number> {
  const [, word, index] = marker.split('-')
  const seq = await page.evaluate((m) => (window as unknown as XtPage).__xtStart(m), marker)
  await page.keyboard.type(`printf 'XT-%s-%s\\n' ${word} ${index}`)
  await page.keyboard.press('Enter')
  await expect(page.locator(GRID)).toContainText(marker, { timeout: 30_000 })
  await page.waitForFunction(
    (wanted) =>
      (window as unknown as XtPage).__xtSamples.some(
        (s) => s.kind === 'command' && s.seq === wanted,
      ),
    seq,
    { timeout: 10_000 },
  )
  const sample = (await samplesOf(page, 'command')).find((s) => s.seq === seq)
  return sample?.ms ?? -1
}

test('a hidden tab repainting 200 times a second does not slow the tab in use (nocx-07ymy)', async ({
  page,
}) => {
  // Two measured phases of real commands, on a container that also runs the
  // backend, the helper and the producer: a budget, not an expectation.
  test.setTimeout(240_000)
  await installInstrument(page)
  await page.goto('/')
  await expect(page.locator(TAB)).toHaveCount(1)
  await promptReady(page)

  // The second tab is the one the person uses; the first is the busy one.
  await page.locator(TAB_ADD).click()
  await expect(page.locator(TAB)).toHaveCount(2)
  await promptReady(page)
  await clickIntoEditor(page)
  expect(
    await page.evaluate(() => {
      const pane = document.querySelector('.pane.active[data-session-id]')
      return pane?.getAttribute('data-session-id') ?? ''
    }),
  ).not.toBe('')

  await keyboardLag(page, 10)
  await page.evaluate(() => (window as unknown as XtPage).__xtRafStart())
  const idleKeys = await keyboardLag(page, 20)
  const idleRaf = await page.evaluate(() => (window as unknown as XtPage).__xtRaf)
  const idleCommands: number[] = []
  for (let i = 0; i < 8; i++) idleCommands.push(await commandLatency(page, `XT-IDLE-${i}`))

  // Tab A runs the producer and stays in the background from here on.
  const script = `${readStand().home}/xt-producer.mjs`
  writeFileSync(script, PRODUCER)
  await page.locator(TAB).first().click()
  await promptReady(page)
  await clickIntoEditor(page)
  const producerPane = await page.evaluate(() => {
    const pane = document.querySelector('.pane.active[data-session-id]')
    return pane?.getAttribute('data-session-id') ?? ''
  })
  expect(producerPane).not.toBe('')
  await page.keyboard.type(`node ${script} ${PRODUCER_HZ}`)
  await page.keyboard.press('Enter')
  await page.locator(TAB).nth(1).click()
  await promptReady(page)
  await clickIntoEditor(page)

  await page.evaluate(() => (window as unknown as XtPage).__xtRafStart())
  const framesBefore = await framesOf(page)
  const cpuBefore = cpuByProcess()
  const phaseStart = Date.now()
  const busyKeys = await keyboardLag(page, 20)
  const busyCommands: number[] = []
  for (let i = 0; i < 8; i++) busyCommands.push(await commandLatency(page, `XT-BUSY-${i}`))
  const phaseSeconds = (Date.now() - phaseStart) / 1000
  const framesAfter = await framesOf(page)

  const cpuAfter = cpuByProcess()
  const busyRaf = await page.evaluate(() => (window as unknown as XtPage).__xtRaf)
  const empty = { n: 0, bytes: 0, ms: 0, each: [] as number[] }
  const producer = framesAfter[producerPane] ?? empty
  const producerBefore = framesBefore[producerPane] ?? empty
  const frames = producer.n - producerBefore.n
  const handlerMs = producer.ms - producerBefore.ms
  const framesPerSecond = frames / phaseSeconds
  const handlerShare = handlerMs / 1000 / phaseSeconds

  const idleKeyP50 = percentile(idleKeys, 0.5)
  const busyKeyP50 = percentile(busyKeys, 0.5)
  const idleCommandP50 = percentile(idleCommands, 0.5)
  const busyCommandP50 = percentile(busyCommands, 0.5)

  console.log(
    [
      `XT producer ${framesPerSecond.toFixed(0)} frames/s (${(producer.bytes / 1024 / phaseSeconds).toFixed(0)} KiB/s)`,
      `XT main thread in the other tab's frames: ${(handlerShare * 100).toFixed(1)} % of the phase`,
      `XT key lag idle p50=${idleKeyP50.toFixed(1)}ms busy p50=${busyKeyP50.toFixed(1)}ms` +
        ` p95 idle=${percentile(idleKeys, 0.95).toFixed(1)}ms busy=${percentile(busyKeys, 0.95).toFixed(1)}ms`,
      `XT command idle p50=${idleCommandP50.toFixed(1)}ms p95=${percentile(idleCommands, 0.95).toFixed(1)}ms` +
        ` busy p50=${busyCommandP50.toFixed(1)}ms p95=${percentile(busyCommands, 0.95).toFixed(1)}ms`,
      `XT commands (ms, in order) idle ${idleCommands.map((x) => Math.round(x)).join(',')}` +
        ` busy ${busyCommands.map((x) => Math.round(x)).join(',')}`,
      `XT CPU during the busy phase (cores): ${Object.keys(cpuAfter)
        .filter((name) => (cpuAfter[name] ?? 0) - (cpuBefore[name] ?? 0) > 0.2 * phaseSeconds)
        .map(
          (name) =>
            `${name}=${(((cpuAfter[name] ?? 0) - (cpuBefore[name] ?? 0)) / phaseSeconds).toFixed(2)}`,
        )
        .join(' ')}`,
      `XT frame cadence idle p50=${percentile(idleRaf, 0.5).toFixed(1)}ms p95=${percentile(idleRaf, 0.95).toFixed(1)}ms` +
        ` busy p50=${percentile(busyRaf, 0.5).toFixed(1)}ms p95=${percentile(busyRaf, 0.95).toFixed(1)}ms`,
    ].join('\n'),
  )

  // THE PREMISE FIRST: a producer that did not load the page cannot pass,
  // and one that repainted slower than the page could display was not the
  // scenario. The second half is the rate-free form of "A was busy": it
  // compares A's frames with this page's own display cadence in the same
  // phase, so a box that throttles both does not redden it, while a fixture
  // whose producer died or crawled does. The absolute floor is the wire
  // check's ten, not the runner's hundred (the header says why).
  expect(frames).toBeGreaterThan(0)
  expect(busyRaf.length).toBeGreaterThan(0)
  expect(framesPerSecond).toBeGreaterThan(MIN_PRODUCER_FRAMES_PER_SECOND)
  expect(framesPerSecond).toBeGreaterThan(busyRaf.length / phaseSeconds)

  // THE CEILING IS SET BY THE REGRESSION THIS EXISTS TO CATCH, not by what
  // the product does today (nocx-5q0ej, #534): one shared worker put A's work
  // in front of B's, and a command in B waited 7.5 s for what takes 10 ms — a
  // factor of ~750. A cross-tab delay of that shape is what must never come
  // back, so the ceiling is 10x this page's own idle, two orders of magnitude
  // under that regression and x3.5 above the worst ratio ever measured here
  // (2.8x, with a compiler running beside the test on a 2-core VM).
  //
  // ANYTHING TIGHTER IS NOISE, and a noisy bound reddens somebody else's pull
  // request: the same tree measures 1.45x to 2.8x depending on what else this
  // machine is doing, `ci-e2e` repeats a changed spec ten times, and a test
  // may not depend on timing. So this asserts only that B did not WAIT FOR A.
  // What a fixed ceiling cannot say — that a hidden pane applies at most one
  // frame per display frame — is asserted, deterministically, by
  // frontend/src/terminal-content.test.ts ("applies one hidden frame per
  // animation frame and keeps the newest"). The numbers above are printed for
  // the CI log, which is where a trend is read.
  const CEILING = 10
  expect(busyKeyP50).toBeLessThanOrEqual(idleKeyP50 * CEILING)
  expect(busyCommandP50).toBeLessThanOrEqual(idleCommandP50 * CEILING)
})

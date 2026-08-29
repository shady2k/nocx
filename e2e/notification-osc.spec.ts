import { test, expect, promptReady } from './harness'
import type { Page } from './harness'

// The epic's end-to-end check (nocx-jiwq): a program running in a pane asks
// nocx to present a message, and that request reaches the backend as exactly
// one notify.raise carrying what the program wrote and the session it wrote
// it from.
//
// This is the one link the unit suites cannot reach. The parser is covered by
// osc-notification.test.ts, the renderer fan-out by xterm.test.ts driving real
// OSC bytes through the real xterm parser, and notify.raise itself by the Go
// over-the-wire conformance test. What none of them exercises is the join in
// terminal-content.ts — and it is invisible there by construction, because the
// frontend suite substitutes its own renderer, which has no onNotification, so
// the optional call is skipped and the line never runs. Only a real renderer
// on a real pty runs it.
//
// The banner itself is deliberately out of scope. This stand has no Wails, so
// the attention host reports itself unavailable and the delivery fails
// visibly; what is asserted here is that the REQUEST crossed correctly, which
// is the half a Linux container can prove.

interface RaisedNotification {
  sessionId: string
  title: string
  body: string
}

declare global {
  interface Window {
    __nocxRaises?: RaisedNotification[]
  }
}

/** Record every notify.raise the app sends on its own socket. Installed
 *  before any application script runs, so no frame is missed. Wrapping send
 *  rather than reading the transcript keeps this honest: it observes the real
 *  client, not a fixture the test built. */
async function recordRaises(page: Page): Promise<void> {
  await page.addInitScript(() => {
    window.__nocxRaises = []
    const send = WebSocket.prototype.send
    WebSocket.prototype.send = function (this: WebSocket, data: Parameters<typeof send>[0]) {
      if (typeof data === 'string') {
        try {
          const msg = JSON.parse(data) as { method?: string; params?: RaisedNotification }
          if (msg.method === 'notify.raise' && msg.params) window.__nocxRaises!.push(msg.params)
        } catch {
          // Not JSON, or not ours. The data plane is binary and never lands
          // here; anything else is another client's business.
        }
      }
      return send.call(this, data)
    }
  })
}

async function raises(page: Page): Promise<RaisedNotification[]> {
  return page.evaluate(() => window.__nocxRaises ?? [])
}

/**
 * Write one shell line, press Enter, and WAIT FOR THE PROMPT TO COME BACK.
 *
 * The wait is the fix rather than politeness. Submitting hands the composer's
 * box to the command, so the editor is not taking input while it runs — and
 * this helper is called several times in a row. Without the wait the next
 * line is typed into a surface that is busy, and the keystrokes that reach
 * the shell are whatever survived; the sentinel that proves the pipeline was
 * awake then never arrives, and the failure is reported as a timeout on the
 * sentinel rather than at the line that was dropped.
 */
async function run(page: Page, line: string): Promise<void> {
  await page.keyboard.type(line)
  await page.keyboard.press('Enter')
  await promptReady(page)
}

// The sentinel discipline the unit tests use, for the same reason: a claim
// that NOTHING was raised needs a later raise to prove the pipeline was awake.
// Waiting on a duration instead would pass on a slow machine for the wrong
// reason (AGENTS.md: a test may not depend on timing).
const SENTINEL_TITLE = 'sentinel'

async function waitForSentinel(page: Page): Promise<void> {
  await run(page, `printf '\\033]777;notify;${SENTINEL_TITLE};flush\\007'`)
  // An explicit budget, because the default expect timeout is 5 s and this
  // predicate spans a real pty, the OSC parser and a round trip to the
  // backend's notifier. The predicate is the observable and the number is a
  // hang detector — not an expectation about how fast a shared runner is.
  await expect
    .poll(async () => (await raises(page)).some((r) => r.title === SENTINEL_TITLE), {
      timeout: 30_000,
      message: 'the sentinel notification never reached the backend',
    })
    .toBe(true)
}

test('a program asking for a notification reaches the backend once, with its session', async ({
  page,
}) => {
  await recordRaises(page)
  await page.goto('/')
  await promptReady(page)

  await run(page, `printf '\\033]9;build finished\\007'`)
  await expect.poll(async () => (await raises(page)).length).toBe(1)

  const [raised] = await raises(page)
  // OSC 9 carries one field: the payload is the body verbatim and there is no
  // title to invent.
  expect(raised.body).toBe('build finished')
  expect(raised.title).toBe('')
  // Addressing, not attribution (ADR-0047 §2.2): the record says WHICH
  // terminal parsed the sequence, and the backend derives everything it
  // attributes from its own registry entry for that id. An empty one would be
  // refused, so a non-empty id is the assertion that the join wired it at all.
  expect(raised.sessionId).not.toBe('')
})

test('the ConEmu progress form of OSC 9 raises nothing', async ({ page }) => {
  await recordRaises(page)
  await page.goto('/')
  await promptReady(page)

  // ESC]9;4;… is a progress report, not a notification, and a progress bar
  // emits it continuously. If it reached the pipeline, any `npm install`
  // would be a notification storm — the trap the whole parser exists to
  // disarm, asserted here through a real pty rather than against the parser.
  await run(page, `printf '\\033]9;4;1;10\\007'`)
  await run(page, `printf '\\033]9;4;1;50\\007'`)
  await run(page, `printf '\\033]9;4;0\\007'`)

  await waitForSentinel(page)

  const progressRaises = (await raises(page)).filter((r) => r.title !== SENTINEL_TITLE)
  expect(progressRaises).toEqual([])
})

test('OSC 777 arrives split into title and body', async ({ page }) => {
  await recordRaises(page)
  await page.goto('/')
  await promptReady(page)

  // A body containing a semicolon is the case a greedy split truncates, and
  // it is ordinary shell text rather than a contrived one.
  await run(page, `printf '\\033]777;notify;deploy;staging; then prod\\007'`)
  await expect.poll(async () => (await raises(page)).length).toBe(1)

  const [raised] = await raises(page)
  expect(raised.title).toBe('deploy')
  expect(raised.body).toBe('staging; then prod')
})

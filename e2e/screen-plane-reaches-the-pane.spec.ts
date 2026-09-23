// e2e: the screen plane reaches the pane (nocx-zg3k3.2.8).
//
// The backend publishes a full screen frame per revision to the attached
// client as a binary data-plane frame (msg-type 0x02); the client routes it
// OUTSIDE the byte offset and ack accounting to the pane's cell model. The
// model is read through the pane's own test seam — window.__nocxPaneScreen()
// over the ACTIVE pane — never through xterm's DOM, which is the byte
// path's surface and stays exactly as it was.
//
// The marker is split across two shell strings ('NOCX''-…'), so the text
// the model must contain exists ONLY in the command's output: the prompt
// echo shows the split spelling, and no contiguous match can come from it.
// Every wait is on the model's revision or rows, never on a duration.

import { test, expect, promptReady } from './harness'

interface PaneScreenReading {
  revision: number | null
  rows: string[]
}

declare global {
  interface Window {
    __nocxPaneScreen?: () => PaneScreenReading | null
  }
}

const nonce = Date.now().toString(36)
/** The output string only a frame the model installed can carry. */
const OUT_ONE = `NOCX-OUT-A-${nonce}`
const OUT_TWO = `NOCX-OUT-B-${nonce}`

test('a command’s output reaches the pane’s cell model over the screen plane', async ({ page }) => {
  await page.goto('/')
  await promptReady(page)

  await page.keyboard.type(`printf '%s\\n' 'NOCX''${OUT_ONE.slice(4)}'`)
  await page.keyboard.press('Enter')

  // The model installs nothing until a frame arrives; its first reading
  // names a real revision the backend published.
  await expect
    .poll(
      async () => (await page.evaluate(() => window.__nocxPaneScreen?.() ?? null))?.revision ?? -1,
      { timeout: 15_000, message: 'the pane’s model never installed a frame' },
    )
    .toBeGreaterThanOrEqual(0)
  const first = await page.evaluate(() => window.__nocxPaneScreen?.() ?? null)

  // The rows the model serves are the screen the backend published — the
  // output line, on a row of its own.
  const modelText = () => page.evaluate(() => (window.__nocxPaneScreen?.()?.rows ?? []).join('\n'))
  await expect
    .poll(modelText, { timeout: 15_000, message: `the model never served ${OUT_ONE}` })
    .toContain(OUT_ONE)

  // The feed is live, not one stale snapshot: a second command moves the
  // model to a strictly newer revision carrying the new output.
  await page.keyboard.type(`printf '%s\\n' 'NOCX''${OUT_TWO.slice(4)}'`)
  await page.keyboard.press('Enter')
  await expect
    .poll(
      async () => {
        const reading = await page.evaluate(() => window.__nocxPaneScreen?.() ?? null)
        if (
          reading === null ||
          reading.revision === null ||
          first === null ||
          first.revision === null
        )
          return false
        return reading.revision > first.revision && reading.rows.join('\n').includes(OUT_TWO)
      },
      {
        timeout: 15_000,
        message: 'the second command never reached the model at a newer revision',
      },
    )
    .toBe(true)
})

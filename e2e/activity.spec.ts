import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

import { test, expect, promptReady } from './harness'

// Drives the real app against the stand (cmd/nocx-server plus vite), so this
// exercises the real transport, PTY and renderer. The activity indicator is
// invisible to jsdom — no layout, no GPU, no focus.

const TAB = '.nocx-tab'
const ACTIVITY = '.nocx-tab-indicator[data-activity="true"]'

test('a background tab lights the activity indicator on normal-buffer output', async ({ page }) => {
  // The shell waits for THIS file, and the test creates it only once the tab is
  // demonstrably in the background.
  //
  // It used to submit `sleep 3; echo PROBE-OUTPUT` and open the second tab
  // inside those three seconds, with a comment calling that "a genuine ordering
  // constraint … not a test synchronization issue". It was a race: the clock
  // starts at Enter, not when the second tab exists, so on a cold CI container
  // — first spec of the run, vite still compiling the new tab's modules, two
  // cores — the echo fired while tab 1 was still in the FOREGROUND. The
  // indicator then has no reason to light at any later point, and the
  // assertion's 10 seconds expire on an event that already happened
  // (nocx-z9s9.14).
  //
  // A file the test controls turns the ordering into a fact. The stand's
  // backend runs beside this process, so the path it polls is the path written
  // here.
  const gate = path.join(fs.mkdtempSync(path.join(os.tmpdir(), 'nocx-e2e-activity-')), 'go')

  try {
    await page.goto('/')
    await expect(page.locator(TAB)).toHaveCount(1)
    await promptReady(page)

    // The first tab is activated and focused on load, so typing goes straight
    // in. The shell blocks here until the gate appears, and only then produces
    // normal-buffer output.
    // Output REPEATS, and that is the second half of the fix.
    //
    // Opening a tab resizes every pane, and a resize makes the shell redraw its
    // prompt — output the user did not cause. terminal-content suppresses
    // attention for RESIZE_ECHO_MS (400ms) afterwards so an inactive tab does
    // not light up for something done to the window (nocx-6w4z). A single echo
    // released right after the tab opens lands inside that window and is
    // correctly ignored — which is how the first attempt at this fix traded a
    // race it could lose for one it lost every time.
    //
    // The product's claim is that output to a background tab lights the
    // indicator, not that any particular instant does. Repeating for ten
    // seconds makes the observation independent of a suppression window that
    // exists for an unrelated reason, and the loop ends on its own.
    await page.keyboard.type(
      `while [ ! -e ${gate} ]; do sleep 0.1; done; ` +
        `for _ in $(seq 1 20); do echo PROBE-OUTPUT; sleep 0.5; done`,
    )
    await page.keyboard.press('Enter')

    // Open a second tab; the first drops to the background.
    await page.locator('[aria-label="New tab"]').click()
    await expect(page.locator(TAB)).toHaveCount(2)
    await expect(page.locator(TAB).first()).toHaveAttribute('aria-selected', 'false')

    // Backgrounded, and only now is there anything to see. Nothing before this
    // line can have lit the indicator, which is the property the test is about.
    fs.writeFileSync(gate, '')

    await expect(page.locator(TAB).first().locator(ACTIVITY)).toBeAttached({ timeout: 10_000 })
  } finally {
    fs.rmSync(path.dirname(gate), { recursive: true, force: true })
  }
})

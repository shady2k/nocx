import { test, expect, promptReady, openControlPlane } from './harness'
import { runCommand } from './ask-gesture'
import { readStand } from './stand'

// nocx-2v80t.3.17: a `clear` boundary hides the blocks before it as a
// backend fact, over the real backend. The client no longer decides a
// clear by the command's text — it reacts to the backend's own
// block.cleared notification, sent once the emulator sighted the real
// erase (ED3) inside the command's authenticated interval.
test('clear hides the earlier block, and the record still holds it', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  await page.goto('/')
  await promptReady(page)

  const nonce = Date.now().toString(36)
  const marker = `CLEAR-BOUNDARY-${nonce}`
  const input = page.locator('.pane.active .nocx-editor-input')

  // Command A: prints a marker and finishes. This is the block the clear
  // boundary must hide.
  // …and it must have CLOSED before `clear` is typed, not merely started. A
  // block is visible from its command's first frame, and the failing run
  // (trace 39b3ca8d98598465c26db4da8ef7d6ca) was in exactly that window: the
  // erase was sighted while A's interval was still the one the backend's block
  // stream knew, so block.cleared named A as the interval to keep and the
  // client kept the one block the test was waiting to see go. The client's
  // prompt returns on its lifecycle fact, which can precede the block stream's
  // own statement of the interval end; the block leaving `.cmd-block-running`
  // is the state that completion produces, and it is what runCommand waits on.
  const frozenA = await runCommand(page, `echo ${marker}`, marker)
  const entryIdA = await frozenA.getAttribute('data-entry-id')
  expect(entryIdA).toBeTruthy()

  // Command B: `clear` itself. Its own erase is what sights the boundary,
  // inside its own still-running interval.
  await input.fill('clear')
  await page.keyboard.press('Enter')

  // The live view: command A's block is gone the moment the boundary
  // arrives — never merely scrolled out of view, actually removed from the
  // DOM (blocks.ts's applyClearBoundary).
  await expect(page.locator(`.pane.active .cmd-block[data-entry-id="${entryIdA}"]`)).toHaveCount(
    0,
    { timeout: 15_000 },
  )

  // `clear`'s own block survives its own report of the erase and goes on
  // to close normally — the running command containing the erase is never
  // hidden by it.
  await expect(page.locator('.pane.active .cmd-block-running')).toHaveCount(0, {
    timeout: 15_000,
  })

  // The durable half: the record never deletes (nocx-zg3k3.10.3's owner
  // decision). The store still answers for the hidden entry directly by
  // id, off the real socket.
  const stand = readStand()
  const wire = await openControlPlane(stand.port, stand.token)
  try {
    const detail = (await wire.call('ledger.get', { id: entryIdA })) as { entry: { id: string } }
    expect(detail.entry.id).toBe(entryIdA)
  } finally {
    wire.close()
  }

  // A reload reads the same boundary through the restore path: the hidden
  // block does not come back, and nothing else in the pane broke.
  await page.reload()
  await promptReady(page)
  await expect(page.locator(`.pane.active .cmd-block[data-entry-id="${entryIdA}"]`)).toHaveCount(
    0,
    { timeout: 15_000 },
  )
})

// The paired acceptance criterion: a program that erases the display alone
// (ED2, no ED3 — a full-screen redraw) hides nothing. `tput` is on every
// machine this suite runs on and `civis`/`clear` together draw exactly that
// shape without a curses program to install.
test('erasing the display alone hides nothing', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  await page.goto('/')
  await promptReady(page)

  const nonce = Date.now().toString(36)
  const marker = `ED2-ALONE-${nonce}`
  const input = page.locator('.pane.active .nocx-editor-input')

  // The same premise as the case above: the ED2 probe is the NEXT command, and
  // `clear`'s boundary is only sighted correctly when the interval it lands in
  // is the one the backend has actually reached (nocx-atpw5.1.12).
  const frozen = await runCommand(page, `echo ${marker}`, marker)
  const entryId = await frozen.getAttribute('data-entry-id')
  expect(entryId).toBeTruthy()

  // ED2 alone: printf the raw escape, never ED3. A shell builtin `clear`
  // always emits both, which is exactly why this bypasses it.
  await input.fill(String.raw`printf '\033[H\033[2J'`)
  await page.keyboard.press('Enter')

  // The earlier block is still there — ED2 alone is a redraw, not a
  // discard, and must hide no block.
  await expect(page.locator('.pane.active .cmd-block-running')).toHaveCount(0, {
    timeout: 15_000,
  })
  await expect(page.locator(`.pane.active .cmd-block[data-entry-id="${entryId}"]`)).toHaveCount(1)
})

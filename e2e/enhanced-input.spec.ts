import { test, expect, promptReady } from './harness'

// nocx-4ff.4: verify that raw input routing works after an enhanced-input
// submit — the editor must stay hidden while a program runs, and typed keys
// must reach the PTY rather than the editor.

const INPUT = '.pane.active .nocx-editor-input'
/** The grid's own input element: where the pane reads keys while a program
 *  holds the pty and the composer is hidden. */
const GRID_INPUT = '.pane.active .ui-terminal-input'

test.describe('enhanced input raw routing', () => {
  test('read command receives input after enhanced submit', async ({ page }) => {
    await page.goto('/')
    await expect(page.locator('.nocx-tab')).toHaveCount(1)

    await promptReady(page)

    // Read a line into x, then print got-<x>. Completed command output is
    // frozen into a DOM scrollback block even though live xterm output is a
    // WebGL canvas.
    await page.keyboard.type('read x; printf \'got-%s\\n\' "$x"')
    await page.keyboard.press('Enter')
    await expect(page.locator(INPUT)).not.toBeVisible({ timeout: 5000 })

    // A HIDDEN COMPOSER IS NOT A KEYBOARD OWNER. The composer's display:none
    // and the authenticated start that makes the grid writable are two
    // different facts, and the handoff that focuses the grid crosses the
    // submitAttempt round trip (nocx-yb5y). When a key is typed before that
    // focus lands it reaches <body> and is gone — the letters are lost while
    // the Enter after them is kept (measured in CI shard 1: this spec's
    // `read x` never received `hello`). The observable a keystroke needs is
    // focus, not visibility, so the premise is established rather than
    // assumed. Same gate as e2e/snippets.spec.ts.
    await expect(page.locator(GRID_INPUT)).toBeFocused({ timeout: 5000 })

    // The `read` builtin is now waiting for stdin. Typing must reach the running
    // program (RUNNING_RAW → editor hidden), not the editor.
    await page.keyboard.type('hello')
    await page.keyboard.press('Enter')

    // The completed block proves the input reached `read`, not the editor.
    await expect(page.locator('.cmd-block', { hasText: 'got-hello' }).first()).toBeVisible({
      timeout: 5000,
    })
  })

  // nocx-1ebrs.66.1.19: the residual of 66.1.16. That fix reclaims the keyboard
  // when the lifecycle publishes `running`; between the submit and that fact the
  // pane still relies on the focus handoff the commit makes synchronously, and
  // the same commit's display:none host (and the live region's move) can undo it
  // on WebKit. A letter typed in that window reaches <body> and is gone while the
  // Enter after it is kept — the command never sees the answer a person typed.
  //
  // THIS TEST DOES NOT WAIT FOR FOCUS, and that is its subject: the assertion is
  // that the keys reach the program wherever the keyboard happens to be.
  //
  // WHERE THE UNCLAIMED STATE IS MADE IS THE WHOLE OF 66.1.27. The product takes
  // the keyboard back after Enter, deliberately and twice — at the running fact
  // and again when the deferred write lands (terminal-content.ts,
  // `takeKeyboardToGridAfterSubmit` and `submitCommand`'s focusGrid). A blur
  // inside that window is therefore undone at a moment the test cannot see: on
  // chromium the state between a blur and the very next round trip came back
  // claimed, so this test's own premise assertion failed while the adoption
  // itself was fine (CI run 40866954718, `Received: false`; 3 of 20 locally for
  // the same reason). The old version asserted a state that scheduling decided.
  //
  // So the state is made where nothing is left to undo it: the command prints a
  // marker first, and the marker's presence proves the write — and its repair —
  // has already landed. Only then is the keyboard taken away. A keyboard lost to
  // a later DOM operation is exactly this state, and it is the one 66.1.19 is
  // about.
  test('keys typed while the keyboard is unclaimed still reach the program', async ({ page }) => {
    await page.goto('/')
    await expect(page.locator('.nocx-tab')).toHaveCount(1)

    await promptReady(page)

    // The marker is assembled by the shell, so it does not occur in the command
    // text, and it is printed BEFORE `read` blocks: seeing it proves the command
    // reached the pty, which is after the last repair the submit brings.
    await page.keyboard.type("printf 'G%s-%s\\n' O zz; read x; printf 'got-%s\\n' \"$x\"")
    await page.keyboard.press('Enter')
    await expect(page.locator('.cmd-block', { hasText: 'GO-zz' }).first()).toBeVisible()

    // The keyboard was handed to the grid at commit; take it away again exactly
    // as a display:none host or the live region's move does. NO FOCUS GATE AFTER
    // THIS — the person believes the keys are theirs, and the product is what has
    // to be right about it.
    await page.evaluate(() => {
      const holder = document.activeElement
      if (holder instanceof HTMLElement) holder.blur()
    })

    // AND THE PREMISE IS READ WHERE IT IS CONSUMED. An observer on the document
    // records who held the keyboard at each keydown the browser dispatches, so
    // the claim this test makes — the keys were typed at a keyboard nobody held
    // — is checked at the keystroke rather than a round trip earlier. Without
    // it, a keyboard claimed in between would let the test pass on the ordinary
    // path and measure nothing.
    await page.evaluate(() => {
      const record = window as unknown as { __unclaimedKeys: string[] }
      record.__unclaimedKeys = []
      document.addEventListener(
        'keydown',
        (e) => {
          const holder = document.activeElement
          record.__unclaimedKeys.push(holder === document.body ? e.key : `claimed:${e.key}`)
        },
        true,
      )
    })

    await page.keyboard.type('hello')
    await page.keyboard.press('Enter')

    // The first letter was dispatched at <body>: the keyboard really was
    // unclaimed when the answer began.
    const first = await page.evaluate(
      () => (window as unknown as { __unclaimedKeys: string[] }).__unclaimedKeys[0],
    )
    expect(first).toBe('h')

    // The completed block proves `read` saw the letters, not that a DOM
    // property moved: the bytes a person typed reached the pty.
    await expect(page.locator('.cmd-block', { hasText: 'got-hello' }).first()).toBeVisible({
      timeout: 5000,
    })
  })

  test('Ctrl-C at a prompt does not trap input', async ({ page }) => {
    await page.goto('/')
    await expect(page.locator('.nocx-tab')).toHaveCount(1)

    await promptReady(page)

    // THE PREMISE, and it is not free — this test is "Ctrl-C AT A PROMPT".
    //
    // promptReady is a DOM statement: the editor is visible and focused. It
    // says nothing about the shell, which at that moment may still be sourcing
    // its rc. For ordinary keystrokes that does not matter — they queue in the
    // tty input buffer and readline reads them when it gets there, which is
    // why the neighbouring tests submit immediately and pass. \x03 is not a
    // keystroke: the line discipline turns it into SIGINT the instant it
    // arrives, and it lands on whatever the shell is doing right then. On a
    // fast machine that is always readline; at the runner's 4 vCPU it is
    // sometimes the rc, and the session never recovers (nocx-xplc).
    //
    // So the prompt is established rather than assumed: a completed block for
    // a real command proves the shell reached readline, ran something and came
    // back. `true` is a builtin, so it cannot fail for want of a PATH entry.
    await page.keyboard.type("true && printf 'READY-%s\\n' up")
    await page.keyboard.press('Enter')
    await expect(page.locator('.cmd-block', { hasText: 'READY-up' }).first()).toBeVisible({
      timeout: 15_000,
    })
    await promptReady(page)

    // Type partial input then Ctrl-C to cancel.
    await page.keyboard.type('echo partial')
    await page.keyboard.press('Control+c')

    // What Ctrl-C must do, stated as the two things a USER can observe.
    //
    // This used to install a `blur` listener on the editor and wait for the
    // attribute it set, on the reasoning that the fresh prompt "briefly hides
    // (and blurs) the editor" — and an empty line alone would pass before the
    // shell had answered. The first half is an implementation detail the
    // product does not promise: `cancel` sends \x03 and clears the draft
    // locally (terminal-content.ts), and nothing guarantees a hide/blur cycle
    // around the shell's new prompt. It held on a fast machine and stopped
    // holding at the runner's 4 vCPU, which is how this failed on both
    // engines in CI while passing locally.
    //
    // The concern behind it was sound, so it is answered rather than dropped
    // — by assertions on the product's contract instead of on a DOM event:
    await promptReady(page)
    await expect(page.locator(INPUT)).toHaveText('', { timeout: 5000 })

    // (1) Input is not trapped: a command typed after Ctrl-C runs. Reaching a
    // completed output block proves the keystrokes went to the shell, the
    // shell was at a prompt to receive them, and the result came back.
    //
    // SUBMITTED UNDER A RETRY, and that is the whole difficulty of this test.
    // Nothing in the DOM marks the instant the shell finishes processing \x03:
    // the editor never lost focus and `cancel()` cleared the draft locally
    // (terminal-content.ts), so promptReady and an empty line are both already
    // true while the shell has not yet answered. Submitting into that gap
    // reaches a shell that is not at a prompt and the keystrokes are simply
    // gone — measured, as five session-data events for the whole test where a
    // healthy one has fifteen.
    //
    // The previous version bridged the gap by waiting for a `blur` event, an
    // implementation detail the product does not promise; it held on a fast
    // machine and stopped holding at the runner's 4 vCPU. Retrying asserts the
    // contract instead of the timing: if the first Enter raced the interrupt
    // it produced nothing and the next attempt lands, and if it did land the
    // poll sees the block and stops. A genuinely trapped input still fails,
    // just at the bound rather than on the first try.
    const suffix = Date.now().toString(36)
    const marker = `RW-${suffix}`
    const block = page.locator('.cmd-block', { hasText: marker }).first()
    const submit = async () => {
      await page.keyboard.type(`printf 'RW-%s\\n' '${suffix}'`)
      await page.keyboard.press('Enter')
      return block
        .waitFor({ state: 'visible', timeout: 4_000 })
        .then(() => true)
        .catch(() => false)
    }
    await expect.poll(submit, { timeout: 20_000, intervals: [250] }).toBe(true)

    // (2) And Ctrl-C CANCELLED rather than submitted — the abandoned draft
    // never ran. Checked after (1) rather than before, and that ordering is
    // what makes it a real check: a completed block for a later command
    // exists, so the transcript is populated and "no block says partial" is
    // an observation rather than a race with an empty transcript. This is
    // new; the blur trick never asserted it.
    await expect(page.locator('.cmd-block', { hasText: 'echo partial' })).toHaveCount(0)
  })

  test('multiple submits in succession all route raw', async ({ page }) => {
    await page.goto('/')
    await expect(page.locator('.nocx-tab')).toHaveCount(1)

    await promptReady(page)

    // Run several commands back-to-back — each submit must leave the state
    // machine in RUNNING_RAW (owned:false) so the next prompt returns via
    // markers, and each paste must NOT leak bracketed-paste wrappers into the
    // command. Each command prints a marker assembled by the shell so it does
    // not occur verbatim in the command text.
    for (let i = 0; i < 3; i++) {
      const marker = `MS-${i}`
      await page.keyboard.type(`printf 'MS-%s\\n' ${i}`)
      await page.keyboard.press('Enter')
      // Wait for this command's completed output before sending the next.
      // Without this gate the keystrokes for iteration i+1 can arrive
      // while the shell is still executing iteration i — the editor
      // input buffer and PTY stdin are not synchronised, and rapid
      // submission races.  A duration wait is not correct here either:
      // the only contract that matters is that each command has
      // finished when we send the next.  expect() polls, so this
      // converges as fast as the shell does.
      await expect(page.locator('.cmd-block', { hasText: marker }).first()).toBeVisible({
        timeout: 5000,
      })
      await promptReady(page)
    }
  })
})

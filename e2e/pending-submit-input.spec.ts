// An authenticated running publication can precede its submit RPC response.
// Keep that real response pending until the person finishes a quoted multiline
// command. Layout must not lose the final Enter while it seats the live input.
import { test, expect, promptReady } from './harness'

type SubmitReplyGate = { held: boolean; release: () => void }
type SubmitReplyHost = Window & { __submitReplyGate?: SubmitReplyGate }

test('continuation Enter survives block opening while the submit reply is pending', async ({
  page,
}) => {
  await page.addInitScript(() => {
    const host = window as SubmitReplyHost
    const held: Array<() => void> = []
    let armed = true
    let submitId: string | number | undefined
    const gate: SubmitReplyGate = {
      held: false,
      release: () => {
        armed = false
        gate.held = false
        for (const deliver of held.splice(0)) deliver()
      },
    }
    host.__submitReplyGate = gate
    const send = WebSocket.prototype.send
    WebSocket.prototype.send = function (data) {
      if (armed && typeof data === 'string') {
        try {
          const message = JSON.parse(data) as { id?: string | number; method?: string }
          if (message.method === 'lifecycle.submitAttempt') submitId = message.id
        } catch {
          // The client owns unrelated binary/control traffic.
        }
      }
      return send.call(this, data)
    }
    // Wrap the client's actual message listener, as the alternate-screen
    // ordering probe does. Notifications are delivered normally; only the
    // matching successful RPC response is held, with no timer or retry.
    const add = WebSocket.prototype.addEventListener
    const remove = WebSocket.prototype.removeEventListener
    const wrappers = new WeakMap<EventListenerOrEventListenerObject, EventListener>()
    WebSocket.prototype.addEventListener = function (
      this: WebSocket,
      type: string,
      listener: EventListenerOrEventListenerObject,
      options?: boolean | AddEventListenerOptions,
    ) {
      if (type !== 'message' || typeof listener !== 'function') {
        return add.call(this, type, listener, options)
      }
      let wrapped = wrappers.get(listener)
      if (wrapped === undefined) {
        wrapped = (event: Event) => {
          const deliver = () => listener.call(this, event)
          const data = (event as MessageEvent).data as unknown
          if (armed && submitId !== undefined && typeof data === 'string') {
            try {
              const message = JSON.parse(data) as { id?: string | number; result?: unknown }
              if (message.id === submitId && 'result' in message) {
                gate.held = true
                held.push(deliver)
                return
              }
            } catch {
              // Refusals and unrelated traffic still reach the client.
            }
          }
          deliver()
        }
        wrappers.set(listener, wrapped)
      }
      return add.call(this, type, wrapped, options)
    }
    WebSocket.prototype.removeEventListener = function (
      this: WebSocket,
      type: string,
      listener: EventListenerOrEventListenerObject,
      options?: boolean | EventListenerOptions,
    ) {
      return remove.call(this, type, wrappers.get(listener) ?? listener, options)
    }
  })
  await page.goto('/')
  await promptReady(page)
  const pane = page.locator('.pane.active')
  const marker = 'NOCX-CONTINUATION-DONE'
  try {
    // This is a literal newline inside an unfinished quoted format string.
    // Playwright types it as Enter, submitting the prefix from the composer;
    // the suffix and the last Enter then belong to terminal continuation input.
    await page.keyboard.type("printf 'NOCX-CONTINUATION-%s\n' DONE")
    await page.waitForFunction(
      () =>
        (window as SubmitReplyHost).__submitReplyGate?.held === true &&
        document.querySelector('.pane.active .cmd-block-running') !== null,
    )
    // Do not focus or click a surface here: use the actual owner the completed
    // block-opening transaction left at the keyboard.
    await page.keyboard.press('Enter')
    await page.evaluate(() => (window as SubmitReplyHost).__submitReplyGate?.release())
    await expect(pane.getByRole('grid', { name: 'Terminal output' })).toContainText(marker)
    await promptReady(page)
  } finally {
    await page.evaluate(() => (window as SubmitReplyHost).__submitReplyGate?.release())
    // Release an incomplete shell continuation on failure as well. Explicit
    // focus is cleanup only; the acceptance path above never repairs focus.
    await pane.locator('.ui-terminal-input').focus()
    await page.keyboard.press('Control+c')
  }
})

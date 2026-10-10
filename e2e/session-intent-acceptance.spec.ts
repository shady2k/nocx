import { clickIntoEditor, expect, promptReady, test } from './harness'

type EffectRecord = {
  effects: number
  effectPayloads: Array<{ kind?: string; title?: string; body?: string }>
  replayedFrames: number
  raises: Array<{ title: string; body: string }>
  clipboardWrites: string[]
}

declare global {
  interface Window {
    __intentAcceptance?: EffectRecord
  }
}

const GRID = '.pane.active .xterm-live-container'
const INTENT_INPUT = '.pane.active .ui-terminal-input'

async function command(page: import('./harness').Page, source: string): Promise<void> {
  await promptReady(page)
  await clickIntoEditor(page)
  await page.keyboard.type(source)
  await page.keyboard.press('Enter')
}

async function focusLiveGrid(page: import('./harness').Page): Promise<void> {
  // A person clicks the visible terminal. Its own focus path gives the real
  // input element the keyboard; the test never calls the intent encoder.
  await page.locator(GRID).click()
  await expect(page.locator(INTENT_INPUT)).toBeFocused()
}

async function outputContains(page: import('./harness').Page, text: string): Promise<void> {
  await expect(page.locator(GRID)).toContainText(text, { timeout: 15_000 })
}

// These are true browser events on the pane's mounted input element. The
// runtime sees their committed text only through the ordinary WebSocket
// session.intent route. Chromium has no hardware IME, so its native event
// sequence is represented explicitly, including the provisional input event.
async function compose(page: import('./harness').Page, committed: string): Promise<void> {
  await page.locator(INTENT_INPUT).evaluate((node, value) => {
    const input = node as HTMLTextAreaElement
    input.dispatchEvent(new CompositionEvent('compositionstart', { data: '', bubbles: true }))
    input.dispatchEvent(new CompositionEvent('compositionupdate', { data: value, bubbles: true }))
    input.dispatchEvent(
      new InputEvent('input', {
        inputType: 'insertCompositionText',
        data: value,
        isComposing: true,
        bubbles: true,
      }),
    )
    input.dispatchEvent(new CompositionEvent('compositionend', { data: value, bubbles: true }))
  }, committed)
}

async function cancelComposition(
  page: import('./harness').Page,
  provisional: string,
): Promise<void> {
  await page.locator(INTENT_INPUT).evaluate((node, value) => {
    const input = node as HTMLTextAreaElement
    input.dispatchEvent(new CompositionEvent('compositionstart', { data: '', bubbles: true }))
    input.dispatchEvent(new CompositionEvent('compositionupdate', { data: value, bubbles: true }))
    input.dispatchEvent(
      new InputEvent('input', {
        inputType: 'insertCompositionText',
        data: value,
        isComposing: true,
        bubbles: true,
      }),
    )
    input.dispatchEvent(new CompositionEvent('compositionend', { data: '', bubbles: true }))
  }, provisional)
}

async function recordAndReplayEffects(page: import('./harness').Page): Promise<void> {
  await page.addInitScript(() => {
    const state: EffectRecord = {
      effects: 0,
      effectPayloads: [],
      replayedFrames: 0,
      raises: [],
      clipboardWrites: [],
    }
    window.__intentAcceptance = state
    // This spec checks effect dispatch and replay deduplication, not browser
    // permission UI or the OS clipboard. Stub the browser API at its seam so
    // Chromium and WebKit observe the same write calls.
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: {
        readText: async () => '',
        writeText: async (text: string) => {
          state.clipboardWrites.push(text)
        },
      },
    })

    // Duplicate each arriving metadata screen frame after its first delivery,
    // and each identity-bearing effect notification. This models the replay
    // boundary the frontend must tolerate, without fabricating either payload.
    const descriptor = Object.getOwnPropertyDescriptor(WebSocket.prototype, 'onmessage')
    if (descriptor?.get && descriptor.set) {
      Object.defineProperty(WebSocket.prototype, 'onmessage', {
        configurable: true,
        enumerable: descriptor.enumerable,
        get: descriptor.get,
        set(handler: ((event: MessageEvent) => void) | null) {
          if (typeof handler !== 'function') {
            descriptor.set!.call(this, handler)
            return
          }
          descriptor.set!.call(this, function (this: WebSocket, event: MessageEvent) {
            handler.call(this, event)
            if (event.data instanceof ArrayBuffer && new Uint8Array(event.data)[1] === 2) {
              state.replayedFrames++
              handler.call(this, event)
            }
          })
        },
      })
    }
    const add = WebSocket.prototype.addEventListener
    WebSocket.prototype.addEventListener = function (
      type: string,
      listener: EventListenerOrEventListenerObject | null,
      options?: boolean | AddEventListenerOptions,
    ) {
      if (type !== 'message' || typeof listener !== 'function')
        return Reflect.apply(add, this, [type, listener, options])
      return add.call(
        this,
        type,
        function (this: WebSocket, event: Event) {
          listener.call(this, event)
          const data = (event as MessageEvent).data
          if (typeof data !== 'string') return
          try {
            const frame = JSON.parse(data) as {
              method?: string
              params?: { kind?: string; title?: string; body?: string }
            }
            if (frame.method === 'session.effect') {
              state.effects++
              if (frame.params && typeof frame.params === 'object') {
                const params = frame.params as { kind?: string; title?: string; body?: string }
                state.effectPayloads.push({
                  kind: params.kind,
                  title: params.title,
                  body: params.body,
                })
              }
              listener.call(this, event)
            }
          } catch {
            /* non-JSON control data is not an effect */
          }
        },
        options,
      )
    }

    const send = WebSocket.prototype.send
    WebSocket.prototype.send = function (
      this: WebSocket,
      data: string | ArrayBufferLike | Blob | ArrayBufferView,
    ) {
      if (typeof data === 'string') {
        try {
          const msg = JSON.parse(data) as {
            method?: string
            params?: { title?: string; body?: string }
          }
          if (msg.method === 'notify.raise' && msg.params)
            state.raises.push({ title: msg.params.title ?? '', body: msg.params.body ?? '' })
        } catch {
          /* binary and unrelated control traffic */
        }
      }
      return Reflect.apply(send, this, [data])
    }
  })
}

test('browser input crosses the session transport once and replayed effects stay once', async ({
  page,
}) => {
  await recordAndReplayEffects(page)
  await page.goto('/')
  await promptReady(page)

  // The same physical Up key is encoded by the session runtime according to
  // the mode set by the running program. The browser sends only a key intent.
  const appMode = '1b 4f 41'
  await command(
    page,
    "printf '\\033[?1h'; stty -echo -icanon; dd bs=1 count=3 2>/dev/null | od -An -tx1; stty sane",
  )
  await focusLiveGrid(page)
  await page.keyboard.press('ArrowUp')
  await outputContains(page, appMode)
  await promptReady(page)

  const normalMode = '1b 5b 41'
  await command(
    page,
    "printf '\\033[?1l'; stty -echo -icanon; dd bs=1 count=3 2>/dev/null | od -An -tx1; stty sane",
  )
  await focusLiveGrid(page)
  await page.keyboard.press('ArrowUp')
  await outputContains(page, normalMode)
  await promptReady(page)

  const committed = 'IME-COMMIT-世界'
  await command(page, 'IFS= read -r value; printf \'IME-RESULT=<%s>\\n\' "$value"')
  await focusLiveGrid(page)
  await compose(page, committed)
  await focusLiveGrid(page)
  await page.keyboard.press('Enter')
  await outputContains(page, `IME-RESULT=<${committed}>`)
  await expect(page.locator(GRID)).toContainText(`IME-RESULT=<${committed}>`)
  await promptReady(page)

  const cancelled = 'IME-CANCEL-MUST-NOT-ARRIVE'
  await command(page, 'IFS= read -r value; printf \'CANCEL-RESULT=<%s>\\n\' "$value"')
  await focusLiveGrid(page)
  await cancelComposition(page, cancelled)
  await focusLiveGrid(page)
  await page.keyboard.type('survivor')
  await page.keyboard.press('Enter')
  await outputContains(page, 'CANCEL-RESULT=<survivor>')
  await expect(page.locator(GRID)).not.toContainText(`CANCEL-RESULT=<${cancelled}>`)
  await promptReady(page)

  // A delivered OSC 777 notification takes its ordinary runtime-effect
  // route with the exact ADR-0047 title/body pair. The init hook repeats the
  // identical full frame/effect at the WebSocket seam; it must still raise
  // exactly once.
  const title = `replay-${Date.now().toString(36)}`
  const body = `message-${title}`
  await command(page, `printf '\\033]777;notify;${title};${body}\\007'`)
  await expect
    .poll(() => page.evaluate(() => window.__intentAcceptance?.raises.length ?? 0))
    .toBe(1)
  await expect
    .poll(() => page.evaluate(() => window.__intentAcceptance?.effects ?? 0))
    .toBeGreaterThan(0)
  expect(await page.evaluate(() => window.__intentAcceptance?.raises)).toEqual([{ title, body }])
  expect(
    await page.evaluate(() =>
      window.__intentAcceptance?.effectPayloads.filter((effect) => effect.kind === 'notification'),
    ),
  ).toEqual([{ kind: 'notification', title, body }])

  // OSC 52 is also an identity-bearing effect. Granting browser permission
  // does not bypass nocx's own one-time gate; accept its visible banner, then
  // observe that replay of the same WebSocket event writes the text only once.
  const clipboardText = `clipboard-${title}`
  const encodedClipboard = Buffer.from(clipboardText).toString('base64')
  await command(page, `printf '\\033]52;c;${encodedClipboard}\\007'`)
  const clipboardBanner = page.locator('.clipboard-banner')
  await expect(clipboardBanner).toBeVisible()
  await clipboardBanner.getByRole('button', { name: 'Allow clipboard writes' }).click()
  await expect
    .poll(() => page.evaluate(() => window.__intentAcceptance?.clipboardWrites.length ?? 0))
    .toBe(1)
  expect(await page.evaluate(() => window.__intentAcceptance?.clipboardWrites)).toEqual([
    clipboardText,
  ])

  await expect
    .poll(() => page.evaluate(() => window.__intentAcceptance?.replayedFrames ?? 0))
    .toBeGreaterThan(0)
})

/** A composer submit survives a socket loss BEFORE its first session intent.
 * The fault drops the real lifecycle attempt response, closes the real socket,
 * and gates the next real attach response. No pane, handle or queue is mocked.
 * The attach gate makes the detached interval observable without a sleep.
 * nocx-evj4b.1.14
 */
import { test, expect, promptReady, type Page } from './harness'

interface WireProof {
  armed: boolean
  cut: boolean
  releaseAttach: boolean
  attachWaiting: boolean
  opens: number
  submittedSession: string
  attachedSession: string
  pastes: string[]
  release(): void
}

async function installPreSendDrop(page: Page): Promise<void> {
  await page.addInitScript(() => {
    interface Request {
      id?: number
      method?: string
      params?: { sessionId?: string; kind?: string; payload?: string }
    }
    const requests = new Map<number, Request>()
    let waiting: { socket: WebSocket; data: string } | null = null
    const proof: WireProof = {
      armed: false,
      cut: false,
      releaseAttach: false,
      attachWaiting: false,
      opens: 0,
      submittedSession: '',
      attachedSession: '',
      pastes: [],
      release() {
        proof.releaseAttach = true
        if (waiting) {
          const { socket, data } = waiting
          waiting = null
          socket.dispatchEvent(new MessageEvent('message', { data }))
        }
      },
    }
    ;(window as unknown as { __heldSubmitProof: WireProof }).__heldSubmitProof = proof
    const Native = window.WebSocket
    window.WebSocket = class extends Native {
      constructor(url: string | URL, protocols?: string | string[]) {
        super(url, protocols)
        // Installed before the dispatcher registers its response listener.
        this.addEventListener('message', (event: MessageEvent) => {
          if (typeof event.data !== 'string') return
          const response = JSON.parse(event.data) as { id?: number }
          if (response.id === undefined) return
          const request = requests.get(response.id)
          if (proof.armed && !proof.cut && request?.method === 'lifecycle.submitAttempt') {
            event.stopImmediatePropagation()
            proof.cut = true
            this.close()
            return
          }
          if (proof.cut && !proof.releaseAttach && request?.method === 'attach') {
            event.stopImmediatePropagation()
            proof.attachedSession = request.params?.sessionId ?? ''
            proof.attachWaiting = true
            waiting = { socket: this, data: event.data }
          }
        })
      }

      override send(data: Parameters<WebSocket['send']>[0]): void {
        if (typeof data === 'string') {
          const request = JSON.parse(data) as Request
          if (request.id !== undefined) requests.set(request.id, request)
          if (request.method === 'open') proof.opens++
          if (request.method === 'session.intent' && request.params?.kind === 'paste') {
            proof.submittedSession = request.params.sessionId ?? ''
            proof.pastes.push(request.params.payload ?? '')
          }
        }
        super.send(data)
      }
    }
  })
}

const proof = (page: Page) =>
  page.evaluate(() => {
    const state = (window as unknown as { __heldSubmitProof: WireProof }).__heldSubmitProof
    return {
      cut: state.cut,
      attachWaiting: state.attachWaiting,
      opens: state.opens,
      submittedSession: state.submittedSession,
      attachedSession: state.attachedSession,
      pastes: state.pastes,
    }
  })

test('a submit whose socket drops before sending runs once after same-session reattach', async ({
  page,
}) => {
  await installPreSendDrop(page)
  await page.goto('/')
  await promptReady(page)
  const initial = await proof(page)
  const editor = page.locator('.pane.active .nocx-editor-input')
  // Shell-local state makes a duplicate execution visible, not merely a second
  // rendering of the same output. Split markers cannot match the command echo.
  const command =
    'nocx_held_count=$(( ${nocx_held_count:-0} + 1 )); printf "HELD-RUN-%s\\n" "$nocx_held_count"'
  await editor.fill(command)
  await page.evaluate(() => {
    ;(window as unknown as { __heldSubmitProof: WireProof }).__heldSubmitProof.armed = true
  })
  await editor.press('Enter')

  // The composer accepted Enter before the connection vanished. The real
  // backend has answered attach, but the renderer has not seen that answer:
  // none of the command's bytes may have crossed the wire yet.
  await expect.poll(async () => (await proof(page)).attachWaiting).toBe(true)
  const detached = await proof(page)
  expect(detached.cut).toBe(true)
  expect(detached.pastes).toEqual([])
  expect(detached.opens).toBe(initial.opens)
  await expect(editor).toHaveText('')

  await page.evaluate(() => {
    ;(window as unknown as { __heldSubmitProof: WireProof }).__heldSubmitProof.release()
  })
  const pane = page.locator('.pane.active')
  await expect(pane).toContainText('HELD-RUN-1')
  await promptReady(page)
  const resumed = await proof(page)
  expect(resumed.submittedSession).toBe(detached.attachedSession)
  expect(resumed.submittedSession).not.toBe('')
  expect(resumed.opens).toBe(initial.opens)
  expect(resumed.pastes.map((payload) => Buffer.from(payload, 'base64').toString('utf8'))).toEqual([
    command,
  ])

  // A later command in the SAME shell confirms the held line ran exactly once.
  await editor.fill('printf "HELD-COUNT-%s\\n" "$nocx_held_count"')
  await editor.press('Enter')
  await expect(pane).toContainText('HELD-COUNT-1')
  await promptReady(page)
  await expect(pane).not.toContainText('HELD-RUN-2')
  await expect(page.getByText(/will not be sent again/)).toHaveCount(0)
})

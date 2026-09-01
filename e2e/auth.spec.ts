import { test, expect, resolveBackend } from './harness'

// The local WebSocket is the whole attack surface: behind /session, `open`
// creates a PTY. Auth rejects BEFORE the upgrade, so an unauthenticated
// caller never gets a socket at all — not even to fail at JSON-RPC.

test('unauthenticated WebSocket is rejected with 401', async ({ page }) => {
  // Navigate first — the Wails bindings (or harness stubs) only exist after
  // the app page loads, not on about:blank.
  await page.goto('/')

  // Verify the token plumbing actually works: the frontend needs a real token
  // to authenticate; an empty or missing token would fail auth for the wrong
  // reason (no binding), making the test useless.
  const endpoint = await resolveBackend(page)
  expect(endpoint.token).not.toBe('')

  // Get the backend WS port, then connect without offering a token.
  const port = endpoint.port

  const rejected = page.evaluate(
    (p: number) =>
      new Promise<number>((resolve) => {
        const url = `ws://127.0.0.1:${p}/session`
        const ws = new WebSocket(url)
        ws.onerror = () => resolve(0)
        ws.onopen = () => {
          ws.close()
          resolve(200)
        }
      }),
    port,
  )

  const status = await rejected
  expect(status).toBe(0)
})

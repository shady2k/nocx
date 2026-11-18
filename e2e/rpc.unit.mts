import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import ts from 'typescript'

// Load the real helper without starting Playwright or a browser: `rpc` needs
// nothing from the page but `evaluate`, and the file's Playwright import is a
// type. The transpile-and-strip shape is ask-gesture.unit.mts's.
const source = readFileSync(new URL('./sshd-fixture.ts', import.meta.url), 'utf8')
const compiled = ts
  .transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ESNext },
  })
  .outputText.replace(/import .*? from .*?;\n/g, '')
const { rpc } = await import(`data:text/javascript,${encodeURIComponent(compiled)}`)

interface Frame {
  jsonrpc: '2.0'
  id?: number
  method?: string
  params?: unknown
  result?: unknown
  error?: { message?: string }
}

/** A socket shaped like the backend's control plane: it delivers the frames it
 *  is given, in order, once the helper has attached its handlers. */
class ScriptedSocket {
  static frames: Frame[] = []
  onopen: (() => void) | null = null
  onmessage: ((event: { data: unknown }) => void) | null = null
  onerror: (() => void) | null = null
  constructor() {
    setTimeout(() => {
      this.onopen?.()
      for (const frame of ScriptedSocket.frames) this.onmessage?.({ data: JSON.stringify(frame) })
    }, 0)
  }
  send(): void {}
  close(): void {}
}
Object.defineProperty(globalThis, 'WebSocket', {
  value: ScriptedSocket,
  configurable: true,
  writable: true,
})

/** The page is only a carrier for `evaluate`; run the callback here, with
 *  ScriptedSocket standing in for the browser's socket. */
const page = { evaluate: (fn: (arg: unknown) => unknown, arg: unknown) => fn(arg) } as never

test('rpc answers the frame carrying its own id, not the first frame the socket delivers', async () => {
  ScriptedSocket.frames = [
    // A server-initiated notification: it has no id and answers nothing.
    { jsonrpc: '2.0', method: 'session.exit', params: { sessionId: 's' } },
    // Somebody else's answer, on the same socket.
    { jsonrpc: '2.0', id: 7, result: { foreign: true } },
    // Ours.
    { jsonrpc: '2.0', id: 1, result: { ok: true } },
  ]
  const answer = await rpc(page, { port: 1, token: 'token' }, 'probe.answer', {})
  assert.deepEqual(answer, { ok: true })
})

test('rpc surfaces the error the answer to its own request carries', async () => {
  ScriptedSocket.frames = [
    { jsonrpc: '2.0', method: 'session.ack', params: {} },
    { jsonrpc: '2.0', id: 1, error: { message: 'nope' } },
  ]
  await assert.rejects(
    rpc(page, { port: 1, token: 'token' }, 'probe.fail', {}),
    /probe\.fail: nope/,
  )
})

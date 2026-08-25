/**
 * A server that answers only when the test lets it — what "a request is in
 * flight" is made of.
 *
 * IT IS THE ONLY WAY TO OBSERVE A PENDING RUN HONESTLY. The alternative is
 * to send at something slow and hope the assertion lands inside the window,
 * which is a test that depends on timing and that AGENTS.md forbids outright:
 * it would pass on a loaded machine and fail on a fast one, or the reverse,
 * and either way it would be reporting the machine. Here the exchange is
 * outstanding because this server is holding it, for exactly as long as the
 * spec chooses, so every wait in the spec is a wait on an observable state.
 *
 * IT LIVES IN THE SPEC's PROCESS, like e2e/fixtures/api-test-server.ts and
 * e2e/fake-openai.ts, and for the same reason those two state: the process
 * that dials it is the DEVHARNESS the spec starts, a separate binary, so the
 * server has to belong to the Playwright worker that shares a network
 * namespace with it. It binds 127.0.0.1 on port 0 and reads the port back off
 * the listener, so two workers cannot collide on a number somebody chose.
 *
 * It also reports what happened to the connections it held, which is the
 * other half of a Stop: a run that came back "stopped" while the request was
 * still being served would be a button that lies.
 */
import { createServer, type IncomingMessage, type Server, type ServerResponse } from 'node:http'
import type { AddressInfo } from 'node:net'

export interface StallingServer {
  /** `http://127.0.0.1:<port>` */
  readonly baseUrl: string
  /** How many requests have arrived and are being held. */
  holding(): number
  /** How many of the held requests the CLIENT gave up on — the observable
   *  proof that a Stop reached the socket rather than only the renderer. */
  abandoned(): number
  /** Let every held request answer. */
  release(): void
  stop(): Promise<void>
}

export async function startStallingServer(): Promise<StallingServer> {
  let arrived = 0
  let aborted = 0
  let released = false
  const waiting: Array<() => void> = []
  const open = new Set<ServerResponse>()

  const server: Server = createServer((req: IncomingMessage, res: ServerResponse) => {
    arrived += 1
    open.add(res)
    const answer = (): void => {
      if (res.writableEnded) return
      res.writeHead(200, { 'content-type': 'application/json' })
      res.end('{"finally":true}')
    }
    // A client that hangs up while we are holding its request is the thing
    // the Stop test is about. Counted rather than ignored, because "the run
    // says stopped" and "the exchange actually ended" are two claims and a
    // spec that made only the first would pass on a renderer that lied.
    req.on('aborted', () => {
      aborted += 1
      open.delete(res)
    })
    res.on('close', () => open.delete(res))
    if (released) {
      answer()
      return
    }
    waiting.push(answer)
  })

  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve))
  const { port } = server.address() as AddressInfo

  return {
    baseUrl: `http://127.0.0.1:${port}`,
    holding: () => arrived,
    abandoned: () => aborted,
    release: () => {
      released = true
      while (waiting.length > 0) waiting.shift()?.()
    },
    stop: async () => {
      released = true
      while (waiting.length > 0) waiting.shift()?.()
      for (const res of open) res.destroy()
      await new Promise<void>((resolve) => server.close(() => resolve()))
    },
  }
}

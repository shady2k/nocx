/**
 * The server the request-owned path-variable e2e sends to.
 *
 * The route is registered under the resolved whole path. Anything else,
 * including `/users/:id` and `/users/{{id}}`, answers 404, so the happy response
 * proves substitution rather than merely proving that a request was sent.
 */
import { createServer, type IncomingMessage, type Server, type ServerResponse } from 'node:http'
import type { AddressInfo } from 'node:net'

export interface PathVariableServer {
  /** `http://127.0.0.1:<port>` — the base URL pasted into the export. */
  readonly baseUrl: string
  /** Every request path received, in arrival order. */
  paths(): readonly string[]
  stop(): Promise<void>
}

export async function startPathVariableServer(opts: {
  /** The exact value the request-owned variable must resolve to. */
  expectedID: string
}): Promise<PathVariableServer> {
  const received: string[] = []
  const expectedPath = `/users/${opts.expectedID}`

  const server: Server = createServer((req: IncomingMessage, res: ServerResponse) => {
    const path = req.url ?? ''
    received.push(path)
    if (req.method !== 'GET' || path !== expectedPath) {
      res.writeHead(404, { 'content-type': 'application/json' })
      res.end('{"error":"no such resolved path"}')
      return
    }
    res.writeHead(200, { 'content-type': 'application/json' })
    res.end('{"ok":true}')
  })

  await new Promise<void>((resolve, reject) => {
    server.once('error', reject)
    server.listen(0, '127.0.0.1', () => resolve())
  })

  const port = (server.address() as AddressInfo).port
  return {
    baseUrl: `http://127.0.0.1:${port}`,
    paths: () => received,
    stop: () =>
      new Promise<void>((resolve) => {
        server.closeAllConnections()
        server.close(() => resolve())
      }),
  }
}

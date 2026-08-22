/**
 * The server the "a secret in the path" e2e sends to, and the reason it
 * exists rather than reusing api-test-server.ts: what it checks is the PATH.
 *
 * THE ASSERTION IS THIS SERVER'S. A spec that only looked at the run would
 * pass on a request that carried the literal `{{token}}` — the workbench
 * would show a 200 and a body, and nothing about it would be true. So the
 * route is registered under the REAL token's path and anything else answers
 * 404: reaching the 200 at all is the proof that the value was substituted,
 * and the recorded path is the proof stated a second way.
 *
 * It lives in the Playwright worker for the reason its sibling states: the
 * process that dials it is the devharness the spec starts, a separate
 * binary, so the server has to belong to the worker that shares a network
 * namespace with it. Port 0, read back off the listener.
 */
import { createServer, type IncomingMessage, type Server, type ServerResponse } from 'node:http'
import type { AddressInfo } from 'node:net'

/** The body the happy path answers with — asserted verbatim by the spec. */
export const SENT_MESSAGE_BODY = '{"ok":true,"result":{"message_id":42}}'

export interface PathTokenServer {
  /** `http://127.0.0.1:<port>` — what the export's `baseUrl` is set to. */
  readonly baseUrl: string
  /** Every path that arrived, in order. */
  paths(): readonly string[]
  stop(): Promise<void>
}

export async function startPathTokenServer(opts: {
  /** The exact token that must appear as a path segment. */
  expectedToken: string
}): Promise<PathTokenServer> {
  const seen: string[] = []
  const want = `/bot${opts.expectedToken}/sendMessage`

  const server: Server = createServer((req: IncomingMessage, res: ServerResponse) => {
    const path = req.url ?? ''
    seen.push(path)
    if (path !== want) {
      // Deliberately NOT the happy answer. A request that arrived with the
      // reference unsubstituted, or with a truncated token, lands here — and
      // the spec's 200 assertion is what catches it.
      res.writeHead(404, { 'content-type': 'application/json' })
      res.end('{"ok":false,"description":"Not Found: bot token"}')
      return
    }
    res.writeHead(200, { 'content-type': 'application/json' })
    res.end(SENT_MESSAGE_BODY)
  })

  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve))
  const { port } = server.address() as AddressInfo

  return {
    baseUrl: `http://127.0.0.1:${port}`,
    paths: () => seen,
    stop: () => new Promise<void>((resolve) => server.close(() => resolve())),
  }
}

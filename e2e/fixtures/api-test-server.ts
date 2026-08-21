/**
 * The server the API-testing e2e sends its request to (design §1: "a test
 * server is started locally").
 *
 * WHY THE SPEC OWNS IT RATHER THAN internal/apisend's httptest fakes. Those
 * live and die inside one `go test` process and nothing outside it can dial
 * them. The process that has to reach this one is the DEVHARNESS the spec
 * starts — a separate binary — so the server has to belong to the Playwright
 * worker, which shares its host network namespace with the child it spawns.
 * e2e/fake-openai.ts is owned for the same reason and states it the same way.
 *
 * IT NEVER REACHES THE NETWORK. It binds 127.0.0.1 on port 0 and the port is
 * read back off the listener, so two specs — or two workers — cannot collide
 * on a number somebody picked by hand (nocx-z9s9.11 is the run where six of
 * them did).
 *
 * THE STATUS IS THE CREDENTIAL CHECK, and that is the point of it. `POST
 * /users` answers 201 only when the Authorization header carries exactly the
 * token the export declared; anything else is 401. So "a run appears with
 * 201" is not merely "the request arrived" — it is "the value the import put
 * in the vault came back out and went onto the wire", which is the half of
 * this feature that a weaker check cannot see. A request that went out with
 * no credential at all still gets a run row, and that row says 401.
 */
import { createServer, type IncomingMessage, type Server, type ServerResponse } from 'node:http'
import type { AddressInfo } from 'node:net'

/** One request the server received, verbatim. */
export interface RecordedRequest {
  readonly method: string
  readonly path: string
  /** The Authorization header as it arrived, '' when the client sent none. */
  readonly authorization: string
  readonly body: string
}

export interface ApiTestServer {
  /** `http://127.0.0.1:<port>` — what the export's `baseUrl` is set to. */
  readonly baseUrl: string
  /** Everything received so far, in arrival order. */
  requests(): readonly RecordedRequest[]
  stop(): Promise<void>
}

/** The body `POST /users` answers with. Asserted verbatim by the spec, so it
 *  is declared once here rather than spelled in two files that agree until
 *  somebody edits one. */
export const CREATED_USER_BODY = '{"id":"usr_8f21","email":"a@b.c"}'

export async function startApiTestServer(opts: {
  /** The exact bearer token that earns a 201. */
  expectedToken: string
}): Promise<ApiTestServer> {
  const received: RecordedRequest[] = []

  const server: Server = createServer((req: IncomingMessage, res: ServerResponse) => {
    const chunks: Buffer[] = []
    req.on('data', (c: Buffer) => chunks.push(c))
    req.on('end', () => {
      const authorization = String(req.headers.authorization ?? '')
      received.push({
        method: req.method ?? '',
        path: req.url ?? '',
        authorization,
        body: Buffer.concat(chunks).toString('utf8'),
      })

      if (req.method !== 'POST' || (req.url ?? '') !== '/users') {
        res.writeHead(404, { 'content-type': 'application/json' })
        res.end('{"error":"no such route"}')
        return
      }
      if (authorization !== `Bearer ${opts.expectedToken}`) {
        // Deliberately NOT a 201. A send that reaches the server without the
        // credential is a different failure from a send that never left, and
        // a server that answered 201 either way would hide the one this
        // feature is about.
        res.writeHead(401, { 'content-type': 'application/json' })
        res.end('{"error":"unauthorized"}')
        return
      }
      res.writeHead(201, { 'content-type': 'application/json' })
      res.end(CREATED_USER_BODY)
    })
  })

  await new Promise<void>((resolve, reject) => {
    server.once('error', reject)
    server.listen(0, '127.0.0.1', () => resolve())
  })

  const port = (server.address() as AddressInfo).port
  return {
    baseUrl: `http://127.0.0.1:${port}`,
    requests: () => received,
    stop: () =>
      new Promise<void>((resolve) => {
        server.closeAllConnections()
        server.close(() => resolve())
      }),
  }
}

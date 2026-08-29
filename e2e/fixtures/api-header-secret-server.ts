/**
 * The HTTP server for the header-secret e2e. It answers only when the
 * request carries the vault value in the named header, so a run that sends
 * the literal reference cannot satisfy the happy-path assertion.
 *
 * The server belongs to the Playwright worker because the caller is the
 * separate nocx-server process. Port 0 keeps parallel workers isolated.
 */
import { createServer, type IncomingMessage, type Server, type ServerResponse } from 'node:http'
import type { AddressInfo } from 'node:net'

export interface HeaderSecretServer {
  readonly baseUrl: string
  headerValues(): readonly string[]
  authorizationValues(): readonly string[]
  stop(): Promise<void>
}

export async function startHeaderSecretServer(opts: {
  expectedHeader: string
  expectedValue: string
}): Promise<HeaderSecretServer> {
  const values: string[] = []
  const authorizationValues: string[] = []
  const expectedPath = '/header-secret'
  const headerName = opts.expectedHeader.toLowerCase()

  const server: Server = createServer((req: IncomingMessage, res: ServerResponse) => {
    const received = req.headers[headerName]
    const value = Array.isArray(received) ? received.join(', ') : (received ?? '')
    const receivedAuthorization = req.headers.authorization
    const authorization = Array.isArray(receivedAuthorization)
      ? receivedAuthorization.join(', ')
      : (receivedAuthorization ?? '')
    values.push(value)
    authorizationValues.push(authorization)

    if (req.method !== 'GET' || req.url !== expectedPath || value !== opts.expectedValue) {
      res.writeHead(401, { 'content-type': 'application/json' })
      res.end('{"ok":false,"error":"secret header was not substituted"}')
      return
    }

    res.writeHead(200, { 'content-type': 'application/json' })
    res.end('{"ok":true}')
  })

  await new Promise<void>((resolve, reject) => {
    server.once('error', reject)
    server.listen(0, '127.0.0.1', () => resolve())
  })
  const { port } = server.address() as AddressInfo

  return {
    baseUrl: `http://127.0.0.1:${port}`,
    headerValues: () => values,
    authorizationValues: () => authorizationValues,
    stop: () =>
      new Promise<void>((resolve) => {
        server.closeAllConnections()
        server.close(() => resolve())
      }),
  }
}

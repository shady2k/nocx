export interface Endpoint {
  host: string
  port: number
  token: string
  /**
   * The W3C `traceparent` this connection should open under, if the caller
   * that resolved the endpoint already has one.
   *
   * Empty in production: the desktop app has never had an exchange to
   * continue before it opens its first socket. The e2e harness is the
   * producer (nocx-n14oo.11) — one Playwright test mints one trace id and
   * needs the backend lines its own connection causes to carry it, so it can
   * print exactly those lines when the test fails instead of a shared
   * backend log nobody can attribute. Dispatcher forwards this verbatim as a
   * query parameter on the WebSocket URL; nothing here parses or validates
   * it; the backend already does (log.ContinueTrace) and treats anything
   * malformed as absent rather than as a reason to refuse the connection.
   */
  traceparent?: string
}

export type EndpointFailureKind =
  | 'profile-unusable'
  | 'server-binary-unusable'
  | 'incompatible-coordinator'
  | 'not-ready'
  | 'no-server'
  | 'token-refused'

export interface EndpointFailure {
  kind: EndpointFailureKind
  message: string
  remedy: string
}

export type EndpointResult =
  { ok: true; endpoint: Endpoint } | { ok: false; failure: EndpointFailure }

/** One attempt to learn where the backend is. Never throws. */
export interface EndpointProvider {
  resolve(): Promise<EndpointResult>
}

/** An EndpointProvider that always answers with the same endpoint.
 * For tests and for any caller that genuinely knows where the backend is. */
export function fixedEndpoint(port: number, host = '127.0.0.1', token = ''): EndpointProvider {
  return {
    resolve: () =>
      Promise.resolve({
        ok: true,
        endpoint: { host, port, token },
      }),
  }
}

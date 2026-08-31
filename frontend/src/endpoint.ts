export interface Endpoint {
  host: string
  port: number
  token: string
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

import { ResolveBackend } from '../bindings/github.com/shady2k/nocx/wailsapp'
import type { EndpointFailureKind, EndpointProvider, EndpointResult } from './endpoint'

type BackendResolution = {
  ok: boolean
  host: string
  port: number
  token: string
  kind: string
  message: string
  remedy: string
  /** See Endpoint.traceparent. Absent on the real wails binding; the e2e
   *  shim (e2e/harness.ts) is the only producer today. */
  traceparent?: string
}

const NO_SERVER_FAILURE = {
  kind: 'no-server' as const,
  message: 'The desktop backend could not be reached.',
  remedy: 'Retry when the backend is available.',
}

function failureKind(kind: string): EndpointFailureKind {
  switch (kind) {
    case 'profile-unusable':
    case 'server-binary-unusable':
    case 'incompatible-coordinator':
    case 'not-ready':
    case 'no-server':
    case 'token-refused':
      return kind
    default:
      return 'no-server'
  }
}

function mapResolution(result: BackendResolution): EndpointResult {
  if (result.ok) {
    return {
      ok: true,
      endpoint: {
        host: result.host,
        port: result.port,
        token: result.token,
        traceparent: result.traceparent,
      },
    }
  }
  return {
    ok: false,
    failure: {
      kind: failureKind(result.kind),
      message: result.message,
      remedy: result.remedy,
    },
  }
}

/** One desktop discovery attempt, including a possible daemon launch. */
export function createDesktopEndpointProvider(): EndpointProvider {
  return {
    async resolve(): Promise<EndpointResult> {
      try {
        return mapResolution(await ResolveBackend())
      } catch {
        return { ok: false, failure: NO_SERVER_FAILURE }
      }
    },
  }
}

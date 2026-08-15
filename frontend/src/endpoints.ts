/**
 * AI endpoint model + IPC client (bead nocx-kn9q, design §4.5.4, ADR-0030).
 *
 * Mirrors the backend internal/profile Endpoint record and the four
 * endpoints.* JSON-RPC control-plane methods wired in the transport. The
 * wire shapes are declared once, in contracts/endpoints.*.schema.json; the
 * renderer's types are generated from them and this module is their single
 * consumer.
 *
 * The API key is an INPUT only: it rides the create/update params once, is
 * minted or rotated by the backend, and never survives a result (ADR-0030
 * §3). Nothing here reads a key back.
 */
import { Dispatcher } from './dispatcher'
import type { EndpointsListResult, Endpoint as ListEndpoint } from './generated/endpoints.list'
import type {
  EndpointsCreateResult,
  Endpoint as CreatedEndpoint,
} from './generated/endpoints.create'
import type {
  EndpointsUpdateResult,
  Endpoint as UpdatedEndpoint,
} from './generated/endpoints.update'
import type { EndpointsDeleteResult } from './generated/endpoints.delete'
import type { EndpointsProbeResult } from './generated/endpoints.probe'

/**
 * The stored endpoint as the wire declares it. The schema declares the
 * endpoint shape once (endpoints.list.schema.json's $defs, referenced
 * cross-file by create and update), and each generated file re-exports its
 * own `Endpoint` — structurally identical by construction. This union names
 * all three declarations so the dead-export ratchet sees every generated
 * file consumed; the surface reads the list's.
 */
export type Endpoint = ListEndpoint | CreatedEndpoint | UpdatedEndpoint

/**
 * The create/update params: the key is an input, never read back (ADR-0030
 * §3). There is deliberately NO schema field: the form has no dialect
 * control while one schema exists (design §4.5, decision 2), so the
 * backend owns the value and completes it at the wire seam
 * (internal/transport/ws_endpoints.go resolveEndpointSchema). A renderer
 * that sent "openai-compatible" would be stating a fact it never decided,
 * and would become a second owner of the value the moment a second dialect
 * appears (AD-8). When a dialect select lands, this type grows the field
 * and the backend default comes out.
 */
export interface EndpointWrite {
  name: string
  baseUrl: string
  /** A key typed fresh: an input, sent once, minted or rotated by the
   *  backend, never read back (ADR-0030 §3). */
  key: string
  /** The row handle of an existing vault secret, when the key's source is
   *  "use an existing secret" (nocx-rzjw). Mutually exclusive with a typed
   *  key: one source per credential. */
  credential: string
  /** Custom HTTP headers (nocx-lyyk): exactly one of value (literal) and
   *  secret (row handle) per row. */
  headers: { name: string; value: string | null; secret: string | null }[]
  models: { name: string; alias: string | null }[]
}

export class EndpointClient {
  constructor(private readonly dispatcher: Dispatcher) {}

  listEndpoints(): Promise<EndpointsListResult['endpoints']> {
    return this.dispatcher.call<EndpointsListResult>('endpoints.list', {}).then((r) => r.endpoints)
  }

  createEndpoint(input: EndpointWrite): Promise<EndpointsCreateResult['endpoint']> {
    return this.dispatcher
      .call<EndpointsCreateResult>('endpoints.create', input)
      .then((r) => r.endpoint)
  }

  /** Full-replace update. An empty key keeps the existing material
   *  (design §4.5.4) — a blank key cannot erase a saved key. */
  updateEndpoint(id: string, input: EndpointWrite): Promise<EndpointsUpdateResult['endpoint']> {
    return this.dispatcher
      .call<EndpointsUpdateResult>('endpoints.update', { id, ...input })
      .then((r) => r.endpoint)
  }

  /** Nothing to return; the list is the state (like vault.deleteSecret). */
  deleteEndpoint(id: string): Promise<EndpointsDeleteResult> {
    return this.dispatcher.call<EndpointsDeleteResult>('endpoints.delete', { id })
  }

  /**
   * The Test button (nocx-edio, design §4.5; nocx-reu5): probes the form's
   * DRAFT values with a real streaming completion — what will actually be
   * used, not one cheap completion. The key is an input that rides the
   * params once and never crosses back (ADR-0030). The model tested is the
   * form's first model.
   *
   * endpointId names the record when the form is editing a SAVED endpoint
   * and the key field is blank: the BACKEND then resolves the credential
   * that endpoint owns, exactly as connections.test resolves a profile by
   * its id. A key typed into the form always wins over the stored one (the
   * backend's rule) — the renderer never reads a key back, so it never has
   * a stored key to send.
   */
  probeEndpoint(input: {
    name: string
    baseUrl: string
    key: string
    model: string
    endpointId?: string
    headers?: { name: string; value: string | null; secret: string | null }[]
  }): Promise<EndpointsProbeResult> {
    return this.dispatcher.call<EndpointsProbeResult>('endpoints.probe', input)
  }
}

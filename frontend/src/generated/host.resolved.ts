/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/host.resolved.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Params of the host.resolved RPC (nocx-uo1k6, design D3): the client answers one host.request with a closed outcome. ok means the effect was performed — for a picker it carries the chosen absolute path in path; cancelled means the person dismissed a picker, which is an outcome and not a failure; failed carries why the client could not perform the effect, so the coordinator answers its caller honestly instead of hanging. requestId is the broker-minted id from the request, echoed back.
 */
export interface HostResolved {
  /**
   * The request id from host.request.
   */
  requestId: string
  /**
   * The closed outcome of the asked-for effect.
   */
  outcome: 'ok' | 'cancelled' | 'failed'
  /**
   * ok, for a picker only: the chosen ABSOLUTE path.
   */
  path?: string
  /**
   * failed only: why the client could not perform the effect.
   */
  error?: string
}

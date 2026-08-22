/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/api.request.cancel.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Result of the api.request.cancel JSON-RPC method: stop the exchange running under a token. It answers EMPTY, and the emptiness is the design — the outcome of a stopped exchange is not reported here but on api.request.send's own result, which comes back as `outcome: "stopped"` on the very request that was stopped. Two methods reporting one exchange's end would be two accounts of it, and the renderer would have to decide which to believe. A token that names no running exchange is REFUSED by name (-32602) rather than answered with this, because "there was nothing to stop" and "it is stopped" are different facts and a caller that cannot tell them apart cannot report either.
 */
export interface ApiRequestCancelResult {}

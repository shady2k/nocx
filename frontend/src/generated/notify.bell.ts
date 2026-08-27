/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/notify.bell.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Params of the notify.bell JSON-RPC request: a program printed BEL (0x07) and the renderer's terminal parsed it. The record carries ADDRESSING AND NOTHING ELSE — sessionId, and no presentation fields at all, because BEL carries no text (ADR-0029 §2.2: provenance is structural, not validated). This method exists rather than a kind argument on notify.raise for exactly that reason: kind is stamped from the METHOD INVOKED, so the only way a caller can select KindBell without being able to select KindBlockFinished is for the selection to be the method name. Ingress authority stays closed — like notify.raise, this method is renderer-callable and therefore always programRequest, and there is no argument, header or method variant by which it produces an attested event (design §3). title, body, kind, trust, level, attribution and at are all absent from the wire and stamped by the backend from the method invoked and its own session registry; a schema proves a record's shape, never who assigned a field. sessionId is ADDRESSING, not attribution: one WebSocket multiplexes many server-assigned sessions (AD-1), so the record must say which terminal parsed the BEL, and the backend rejects an id not live on that connection. There is no bound to declare here because there is no untrusted text: a BEL is one byte and the whole payload is a session id the backend already knows. The result of the method is the empty object.
 */
export interface NotifyBell {
  /**
   * Which terminal parsed the BEL — a session id the server assigned to a session this connection opened or reattached to. Addressing, not attribution: every attributed field is derived from the backend's registry entry for this id, never from the record. An id not live on this connection is rejected with a JSON-RPC error.
   */
  sessionId: string
}

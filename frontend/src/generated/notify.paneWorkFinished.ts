/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/notify.paneWorkFinished.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Params of the notify.paneWorkFinished JSON-RPC request: the renderer's state machine over detectAgentStatus saw a pane go working → idle and stay idle for the settle window (design §3.4). Like notify.bell the record carries ADDRESSING AND NOTHING ELSE — sessionId, and no presentation fields at all — because the only text this source could supply is the program's own title, and a program writing the words on a notification is what ADR-0047 §2.2 keeps off the wire; the backend stamps the title from its own session registry. This is a THIRD renderer-callable method rather than a kind argument on notify.raise for the reason the other two exist: kind is stamped from the METHOD INVOKED, so the only way a caller can select KindPaneWorkFinished without also being able to select KindBlockFinished is for the selection to be the method name. Its trust is heuristic and the method is what stamps it — BRAILLE_SPINNER matches any braille glyph in any title, so npm install under ora, docker pull and half of all TUIs produce this event, and it is an inference about a pane and never a claim about an agent. Heuristic is the trust class that may reach local attention only (toast, dock badge, tab dot) and never push (design §3.1); the router enforces that, and no argument here can widen it. title, body, kind, trust, level, attribution and at are all absent from the wire and stamped by the backend; a schema proves a record's shape, never who assigned a field. sessionId is ADDRESSING, not attribution: one WebSocket multiplexes many server-assigned sessions (AD-1), so the record must say which pane's session settled, and the backend rejects an id not live on that connection. The result of the method is the empty object.
 */
export interface NotifyPaneWorkFinished {
  /**
   * Which pane's session settled — a session id the server assigned to a session this connection opened or reattached to. Addressing, not attribution: every attributed field is derived from the backend's registry entry for this id, never from the record. An id not live on this connection is rejected with a JSON-RPC error, which is also what happens to a settle timer that fires against a session the pane has since replaced.
   */
  sessionId: string
}

/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/session.focus.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Params of the session.focus server-to-client notification: the backend asks the renderer to bring the pane holding this session to the front. It carries a session id and NOTHING else, and that absence is the decision (nocx-jiwq.1, plan D1). The renderer owns session -> tab — PaneManager.findBySession is the one lookup — and the backend cannot do it at all: Attribution.Tab is a WebSocket connection id rather than a tab (nocx-wyp3p). A tab id on this notification would therefore be a second addressing identity that no part of the backend can own. sessionId is the server-authoritative addressing of AD-7, which the renderer already resolves for a feed row's activation, so a banner click and a feed row land through one path. The renderer does nothing when no pane holds the session: a pane that is gone is not an error, and there is nothing to focus.
 */
export interface SessionFocus {
  /**
   * The session whose pane should be focused. Server-assigned and server-authoritative (AD-7).
   */
  sessionId: string
}

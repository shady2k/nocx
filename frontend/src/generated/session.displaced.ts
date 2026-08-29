/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/session.displaced.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Params of the session.displaced server-to-client notification: another client has claimed a session this client was holding, and this client no longer has it (the nocx-server design D8). One client owns a session at a time. That was already true — the transport has a single subscriber slot per session — but the slot was replaced SILENTLY, so the loser went on rendering a stream it no longer owned and offering a keyboard whose bytes the backend would refuse. A surface that goes on advertising what it can no longer deliver is the defect AGENTS.md names, and this notification is the fix: the displacement becomes an event the loser can act on. It is the last thing this connection is told about the session — the backend has already stopped sending it output and will refuse its input — so a receiver should drop the session from its own map rather than wait for anything further. Read-only observers and real multi-window ownership are later work; D8 is the minimum honest model.
 */
export interface SessionDisplaced {
  /**
   * The session that was taken. One WebSocket carries several panes, so the renderer routes by session id before any pane may act on it (AD-7).
   */
  sessionId: string
  /**
   * The backend instance holding the session. Compared against the pair the open ack or the sessions.live entry carried, so a notification out of a previous backend instance is refused rather than applied (nocx-3oupk).
   */
  instanceId: string
  /**
   * Which incarnation of this session id was taken. Minted from 1.
   */
  sessionEpoch: number
}

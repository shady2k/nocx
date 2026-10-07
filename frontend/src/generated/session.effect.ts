/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/session.effect.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * One identity-bearing non-visual effect emitted by the session runtime. A full session.frame never carries or replays effects; the client deduplicates this event across replay and reconnect by generation and effectId.
 */
export type SessionEffect = {
  [k: string]: unknown
} & {
  sessionId: string
  generation: string
  effectId: string
  kind: 'bell' | 'notification' | 'clipboard' | 'title' | 'cwd' | 'promptBoundary' | 'recovery'
  title: string
  body: string
  episodeId?: string
}

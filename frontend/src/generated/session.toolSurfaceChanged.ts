/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/session.toolSurfaceChanged.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Params of the session.toolSurfaceChanged server-to-client notification: whether the launch-owned nocx worker tool surface is reachable. The fact is emitted only after the endpoint admits a launch and either receives tools.catalogue or reaches its deadline/refusal, so absence before settlement is not a failure shown to the user.
 */
export type SessionToolSurfaceChanged = {
  [k: string]: unknown
} & {
  /**
   * The session whose launch owns this tool surface.
   */
  sessionId: string
  /**
   * The backend instance that minted the session.
   */
  instanceId: string
  /**
   * The session incarnation within the backend instance.
   */
  sessionEpoch: number
  /**
   * Whether the launch-owned worker tool surface is available.
   */
  status: 'available' | 'unavailable'
  /**
   * Why the worker tool surface is unavailable. Required only for unavailable facts and never sent for available facts.
   */
  reason?: string
}

/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/session.observationChanged.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Params of the session.observationChanged server-to-client notification: what an ENROLLED agent pane's screen is currently inviting, as the agent's driver classified it (nocx-szb40.3). It is derived in the backend because that is where the grid lives — the AD-6 amendment permits a live VT grid for an enrolled pane and grants it exactly two powers, and this notification carries the answer to one of them across as a typed fact under AD-1, never as bytes. Sent only when the answer CHANGES: an agent pane repaints continuously (its token counter moves on every response chunk) and a notification per repaint would tell a renderer nothing it could act on. Because it is a change, it is also replayed on reattach — a state is not an event, and for a settled idle pane no further change is coming. A pane that is not enrolled produces nothing at all: absence means 'nocx is not watching this pane', which is the ordinary case for almost every pane in the product.
 */
export interface SessionObservationChanged {
  /**
   * The session whose pane this observation is about. Server-authoritative (AD-7); the renderer attaches it to the tab that owns this id and filters nothing.
   */
  sessionId: string
  /**
   * The backend instance that minted the session (AD-7). Same vocabulary as session.integrationChanged: the renderer compares it against the pair the open ack carried, so an observation for this sessionId out of a previous backend instance is refused rather than applied to the current tab.
   */
  instanceId: string
  /**
   * The session's epoch within its backend instance, as minted at open. Together with instanceId this is what binds the observation to an incarnation — a late observation from a previous one cannot overwrite a current one, which is the failure a single status field with source priority produces.
   */
  sessionEpoch: number
  /**
   * The agent named by the enrolment act, verbatim. The renderer needs it to tell 'this agent's driver could not read the screen' from 'nocx has no driver for this agent at all' — both answer 'unknown', and only the second is permanent.
   */
  agent: string
  /**
   * What the screen is inviting, from a CLOSED set. 'free_text' means an input box is on screen and waiting, and it is the only state nocx may type into. 'permission_choice' means the agent raised a tool-approval dialog and is waiting on a human, so answering it answers the agent. 'modal_choice' means a menu the agent did not raise is up — a user-opened one such as /model. 'working' means work is in progress: a turn in flight, or a background agent the main turn may be blocked on, which Claude Code shows with the input box still live and no spinner at all. 'unknown' means the driver could not positively identify the state, or the agent has no driver; every caller treats it as busy, because refusal is the containment here rather than mitigation — a mistimed keystroke does not merely fail to arrive, it answers whatever modal is on screen. 'exited' is a fact about the PROCESS and is never read off the screen; no driver returns it.
   */
  state: 'free_text' | 'permission_choice' | 'modal_choice' | 'working' | 'unknown' | 'exited'
}

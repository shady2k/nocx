/**
 * WHAT AN ENROLLED PANE IS EMITTING, and what its rule reads on it
 * (nocx-02uci; contracts/agent.emitting.schema.json).
 *
 * A PULL, deliberately, and that is the whole of the view's interval. The
 * request IS the looking: a surface that has closed asks nothing, and nothing
 * in the backend keeps reading a pane nobody is watching. There is no
 * subscription to forget to close, which is the defect `nocx-8hdia` is an epic
 * about, and there is no backend state this client could leak.
 *
 * The types are the generated ones and are never restated here: a hand-written
 * type can want a field the wire does not carry, a generated one cannot.
 */
import type { Dispatcher } from './dispatcher'
import type { AgentEmitting } from './generated/agent.emitting'

/** One pane nocx is watching, as the enrolment act named it. */
export type EmittingPane = AgentEmitting['panes'][number]

/** A pane's frame together with the rule's reading of it. Absent from an
 *  answer when no pane was named, and when the pane named is no longer
 *  watched — which is a race a polling surface passes through rather than an
 *  error, because an observation closes when its session ends. */
export type EmittingReading = NonNullable<AgentEmitting['reading']>

/** One branch's part in the ordered walk. */
export type EmittingBranch = EmittingReading['branches'][number]

export class EmittingClient {
  constructor(private readonly dispatcher: Dispatcher) {}

  /**
   * Ask what nocx is watching, and — when `sessionId` names one of those
   * panes — what its rule reads on the pane's current screen.
   *
   * The pane list rides with every answer rather than living in a method of
   * its own, so one round trip refreshes both: a pane whose observation
   * closed leaves the list on the next answer, which is how the surface
   * learns it has nothing left to show.
   */
  read(sessionId?: string): Promise<AgentEmitting> {
    return this.dispatcher.call<AgentEmitting>(
      'agent.emitting',
      sessionId === undefined ? {} : { sessionId },
    )
  }
}

/**
 * THE GUIDED CALIBRATION: nocx asks for a state, the person produces it, and
 * the frame is labelled with the state that was asked for
 * (nocx-etejh; contracts/agent.calibration.schema.json).
 *
 * There is no `label` argument anywhere in this file, and its absence is the
 * design rather than an oversight. A label pointed at a frame the person did
 * not produce for it proves nothing, so the label is never a value this side
 * supplies: it comes from the walk's PENDING step in the backend, and the
 * frame it lands on is read from the pane's live grid at the instant the
 * answer arrives. All this client can say is which pane, what the person did,
 * and which step it believes it is showing — the last of those a staleness
 * guard, so a view that redrew late is refused rather than answered into the
 * wrong label.
 *
 * A PULL, like the emitting view beside it: the request is the looking, so a
 * surface that has closed asks nothing.
 */
import type { Dispatcher } from './dispatcher'
import type { AgentCalibration, AgentCalibrationRecord } from './generated/agent.calibration'

/** One pane nocx is watching, as the enrolment act named it. */
export type CalibrationPane = AgentCalibration['panes'][number]

/** A pane's calibration: the closed step list, the walk in progress if there
 *  is one, and the labelled set already on disk. */
export type CalibrationState = NonNullable<AgentCalibration['calibration']>

/** One thing the person is asked to produce. */
export type CalibrationStep = CalibrationState['steps'][number]

/** What the agent's rule has EARNED against the labelled set (nocx-jse6x):
 *  whether nocx may type into a pane running it, how much of the set it was
 *  checked against, and every label it reads differently from the person who
 *  produced it. Always present — an agent with no set has an unverified
 *  verdict rather than none, so there is no absence for a surface to invent
 *  the safe reading of. */
export type CalibrationVerdict = CalibrationState['verification']

/** One step's outcome. Absent means nobody was ever asked; `skipped` means
 *  they were asked and declined, and only one of those is a decision. */
export type CalibrationRecord = AgentCalibrationRecord

/** What the person did about the step they are being asked. `begin` and
 *  `abandon` are the walk's ends; the other three answer a step. */
export type CalibrationAction = 'begin' | 'capture' | 'skip' | 'redo' | 'abandon'

export class CalibrationClient {
  constructor(private readonly dispatcher: Dispatcher) {}

  /** Ask what nocx is watching, and — when `sessionId` names one of those
   *  panes — the state of that agent's calibration. */
  read(sessionId?: string): Promise<AgentCalibration> {
    return this.dispatcher.call<AgentCalibration>(
      'agent.calibration',
      sessionId === undefined ? {} : { sessionId },
    )
  }

  /**
   * Drive the walk one action forward, and get the state that action
   * produced. `step` is required for the three actions that answer a step and
   * refused for the two that do not, which is the wire saying the same thing
   * this client's callers have to: begin and abandon answer no question.
   */
  answer(sessionId: string, action: CalibrationAction, step?: number): Promise<AgentCalibration> {
    return this.dispatcher.call<AgentCalibration>('agent.calibration.answer', {
      sessionId,
      action,
      ...(step === undefined ? {} : { step }),
    })
  }
}

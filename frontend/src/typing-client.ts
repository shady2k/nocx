/**
 * NOCX TYPES INTO A PANE, AND ONLY WHEN THE DRIVER SAYS IT MAY
 * (nocx-dkawo.1; contracts/agent.type.schema.json).
 *
 * There is no `agent` argument anywhere in this file, and no state, and their
 * absence is the design. A caller that could name the agent could name one
 * whose rule verifies while the pane runs something else, and the frame would
 * then be read by a rule that was never about it. Both are read in the backend
 * — the agent from the enrolment act, the state from the pane's own live grid
 * at the instant the request is handled — so all this client can say is which
 * pane, what text, and whether the key that submits is pressed.
 *
 * THE REFUSAL COMES BACK AS A RESULT, not as an error. A submission into a
 * pane that is asking the person to approve a tool is not a malformed request:
 * the params were well formed and the pane was real. So callers branch on
 * `outcome` and show `reason`; a rejected promise here means the request was
 * wrong, never that the rule declined.
 */
import type { Dispatcher } from './dispatcher'
import type { AgentType } from './generated/agent.type'

/** What happened to a submission: the outcome a surface branches on, the state
 *  that decided it, and why in the words a person reads. `typed` is the one
 *  partial state this has — the text is in the input region and nothing pressed
 *  the key that submits it, either because none was asked for or because the
 *  screen stopped being free text between the two writes. */
export type TypingResult = AgentType

export class TypingClient {
  constructor(private readonly dispatcher: Dispatcher) {}

  /** Put text into the pane's input and press nothing. The person sees it in
   *  the input region and decides what to do with it. */
  type(sessionId: string, text: string): Promise<TypingResult> {
    return this.dispatcher.call<TypingResult>('agent.type', { sessionId, text })
  }

  /** Put text into the pane's input and press the key that submits it, as a
   *  separate write gated on its own frame. */
  submit(sessionId: string, text: string): Promise<TypingResult> {
    return this.dispatcher.call<TypingResult>('agent.type', { sessionId, text, submit: true })
  }
}

/**
 * Agent client — the assistant's control-plane surface (nocx-edio, design
 * §7). agent.status tells the ask surface whether an ask can succeed:
 * the chosen agent installed and configured. The wire
 * shapes are declared once, in contracts/agent.*.schema.json; this module's
 * types are generated from them.
 *
 * The ask transaction (agent.ask, agent.cancel, agent.approve) is nocx-f4s5.
 */
import { Dispatcher } from './dispatcher'
import type { AgentCancel } from './generated/agent.cancel'
import type { AgentStatusResult } from './generated/agent.status'
import type { AgentSteer } from './generated/agent.steer'

export class AgentClient {
  constructor(private readonly dispatcher: Dispatcher) {}

  status(sessionId?: string): Promise<AgentStatusResult> {
    return this.dispatcher.call<AgentStatusResult>('agent.status', sessionId ? { sessionId } : {})
  }

  cancel(runId: number): Promise<AgentCancel> {
    return this.dispatcher.call<AgentCancel>('agent.cancel', { runId })
  }

  /** Hand a person's message to the turn that is answering. It answers once
   *  the agent took the message — a turn that ended first refuses it. */
  steer(runId: number, text: string): Promise<AgentSteer> {
    return this.dispatcher.call<AgentSteer>('agent.steer', { runId, text })
  }
}

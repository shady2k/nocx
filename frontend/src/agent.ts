/**
 * Agent client — the assistant's control-plane surface (nocx-edio, design
 * §7). agent.status tells the ask surface whether an ask can succeed:
 * endpoint configured, credential resolvable, last probe result. The wire
 * shape is declared once, in contracts/agent.status.schema.json; this
 * module's types are generated from it.
 *
 * The ask transaction (agent.ask, agent.cancel, agent.approve) is nocx-f4s5
 * and deliberately not here.
 */
import { Dispatcher } from './dispatcher'
import type { AgentStatusResult } from './generated/agent.status'

export class AgentClient {
  constructor(private readonly dispatcher: Dispatcher) {}

  status(): Promise<AgentStatusResult> {
    return this.dispatcher.call<AgentStatusResult>('agent.status', {})
  }
}

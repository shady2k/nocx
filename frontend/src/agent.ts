/**
 * Agent client — the assistant's control-plane surface (nocx-edio, design
 * §7). agent.status tells the ask surface whether an ask can succeed:
 * endpoint configured, credential resolvable, last probe result. The wire
 * shapes are declared once, in contracts/agent.*.schema.json; this module's
 * types are generated from them.
 *
 * The ask transaction (agent.ask, agent.cancel, agent.approve) is nocx-f4s5.
 */
import { Dispatcher } from './dispatcher'
import type { AgentCancel } from './generated/agent.cancel'
import type { AgentStatusResult } from './generated/agent.status'

export class AgentClient {
  constructor(private readonly dispatcher: Dispatcher) {}

  status(): Promise<AgentStatusResult> {
    return this.dispatcher.call<AgentStatusResult>('agent.status', {})
  }

  cancel(runId: number): Promise<AgentCancel> {
    return this.dispatcher.call<AgentCancel>('agent.cancel', { runId })
  }
}

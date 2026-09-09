/**
 * THE ANSWERS A PERSON GAVE ABOUT WHICH AGENTS MAY USE NOCX'S TOOLS
 * (nocx-6jbad; contracts/agentAccess.list.schema.json).
 *
 * A pull, like the emitting client and for the same reason: the request is
 * the looking, so a page that has closed asks nothing and there is no
 * subscription to forget to close.
 *
 * The types are the generated ones and are never restated here: a
 * hand-written type can want a field the wire does not carry, a generated
 * one cannot.
 */
import type { Dispatcher } from './dispatcher'
import type { AgentAccessList } from './generated/agentAccess.list'
import type { AgentAccessForget } from './generated/agentAccess.forget'

/** One remembered answer: the three facts the person was shown, and what
 *  they said. */
export type AgentAccessAnswer = AgentAccessList['answers'][number]

export class AgentAccessClient {
  constructor(private readonly dispatcher: Dispatcher) {}

  /** Every answer this backend holds, in the backend's stable order. */
  list(): Promise<AgentAccessList> {
    return this.dispatcher.call<AgentAccessList>('agentAccess.list', {})
  }

  /**
   * Unmake one answer, addressed by the facts that identify it.
   *
   * `forgotten: false` is a success and not a failure — the answer was
   * already not there, which is what forgetting asked for. The caller
   * refreshes either way, because the list it holds is a read that may
   * predate somebody else's forget.
   */
  forget(answer: AgentAccessAnswer): Promise<AgentAccessForget> {
    return this.dispatcher.call<AgentAccessForget>('agentAccess.forget', {
      executable: answer.executable,
      digest: answer.digest,
      workspace: answer.workspace,
    })
  }
}

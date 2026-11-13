/**
 * GENERATED FILE — do not edit.
 *
 * Source: contracts/agent.approvalRetired.schema.json
 * Regenerate: cd frontend && npm run contracts
 *
 * Editing this file is editing the wrong end of the contract. If the renderer
 * needs a field the wire does not carry, the schema is what has to change, and
 * then the Go transport has to satisfy it.
 */

/**
 * Params of agent.approvalRetired: the exact question is no longer answerable, whether answered, expired or withdrawn. Retire only this binding and phase.
 */
export interface AgentApprovalRetired {
  /**
   * The backend-minted run id the question belongs to — the value agent.approve echoes back.
   */
  runId: string
  /**
   * The run's attempt — part of the binding; the answer echoes it back.
   */
  attempt: number
  /**
   * The tool the model proposed calling — one of the names internal/agenttools' declaration table declares, and nothing else. The set is CLOSED on purpose (nocx-69sew): the renderer keys two sentences on this value (the command block for the command carrier, the network row for fetch.url), and while it was a bare string a rename in the table — `run` became `session.run` — left the renderer comparing against a dead literal, which no test and no schema could see. Enumerated here, the generated renderer type is a union, so a comparison against a name the table no longer declares is a compile error rather than a branch that is quietly never taken; TestApprovalRequestedToolEnumMatchesTheTable is the other end, and fails the moment the table and this list disagree. Only a declared tool can reach this notification: the two names that are not declarations — the unknown-tool anchor and tools.search — are answered inside WrapInvokableToolCall and never reach the kernel that escalates.
   */
  tool:
    | 'files.read'
    | 'fetch.url'
    | 'session.list'
    | 'session.read'
    | 'session.keys'
    | 'session.message'
    | 'session.run'
    | 'session.wait'
    | 'files.edit'
    | 'files.create'
    | 'git.status'
    | 'notes.search'
    | 'notes.create'
    | 'notes.update'
    | 'notes.delete'
    | 'snippets.list'
    | 'snippets.create'
    | 'snippets.update'
    | 'snippets.delete'
    | 'snippets.reorder'
    | 'workers.holdings'
    | 'workers.spawn'
    | 'workers.say'
    | 'workers.inbox'
    | 'workers.report'
    | 'workers.close'
    | 'workers.removeCheckout'
  /**
   * The model's call id for the proposed call — part of the binding.
   */
  callId: string
  /**
   * The canonical-argument hash of the binding — what distinguishes one proposal from a changed one. The answer echoes it back; a changed argument never resumes under the old approval.
   */
  argHash: string
  /**
   * Which gate asked: the policy gate (an effect coming in) or the egress gate (a secret going out).
   */
  reason: 'policy' | 'egress'
}

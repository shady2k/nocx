// Recording ONE answer to ONE approval question (nocx-2019q).
//
// The composition root owns the QUEUE — which question is on screen, which
// wait behind it — and that is a different job from putting one answer on the
// wire and saying what came back. This is the second job, on its own, so a
// test can drive the real prompt's real button into the real call and watch
// what a person would see afterwards. Before it, the whole exchange lived in
// a closure inside main.tsx and the only way to test the button was to invent
// a second copy of the plumbing, which proves the copy works.
//
// THE WARNING IS WHY THIS EXISTS AT ALL. agent.approve answers with a
// sentence whenever the part of the answer that was meant to OUTLIVE the
// proposal could not be recorded — a store that would not write, a command
// that cannot be saved exactly as it was shown. That sentence was discarded
// here: the call resolved, the question closed, and a person who had just
// told nocx to stop asking was told nothing and asked again next time. A soft
// degrade must be visible in the product, not only in a log (AGENTS.md).

import type { Dispatcher } from './dispatcher'
import type { AgentApprovalRequested } from './generated/agent.approvalRequested'
import type { AgentApprove } from './generated/agent.approve'
import type { AgentApproveParams } from './generated/agent.approve.params'

/** How far the answer reaches — the wire's own vocabulary. */
type ApprovalScope = AgentApproveParams['scope']

export interface ApprovalDecisionSeams {
  dispatcher: Dispatcher
  /**
   * The standing half of the answer did not stick. Shown, never logged: the
   * decision itself stood, so the call is not refused, and the only place a
   * person can learn that "always" did not become always is on screen.
   */
  onWarning: (sentence: string) => void
  /** The call itself was refused — a stale binding, a closed transport. */
  onError: (message: string) => void
}

/**
 * Put one answer on the wire. Resolves true when the decision was RECORDED,
 * which is what lets the caller close the question and show the next one; a
 * refusal resolves false and leaves it open, so the person sees the honest
 * refusal and can answer anew or deny.
 *
 * The scope travels WITH the decision because the backend applies it: a
 * renderer that read the matrix, edited a row and wrote it back would be a
 * second owner of the policy document, racing the settings page (nocx-gycwo,
 * design §"Three wire changes"). The receipt for what the backend then wrote
 * arrives on its own notification, in the turn that asked the question — not
 * here, which has no scrollback and no business having one.
 */
export async function recordApprovalDecision(
  ask: AgentApprovalRequested,
  approved: boolean,
  scope: ApprovalScope,
  seams: ApprovalDecisionSeams,
): Promise<boolean> {
  try {
    const result = await seams.dispatcher.call<AgentApprove>('agent.approve', {
      runId: ask.runId,
      attempt: ask.attempt,
      tool: ask.tool,
      callId: ask.callId,
      argHash: ask.argHash,
      approved,
      scope,
    } satisfies AgentApproveParams)
    if (result?.warning) seams.onWarning(result.warning)
    return true
  } catch (err) {
    seams.onError(err instanceof Error ? err.message : String(err))
    return false
  }
}

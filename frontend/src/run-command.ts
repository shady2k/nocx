// The renderer half of the run tool (nocx-tjppv): the backend asks
// (agent.runRequest), the renderer submits the command through the SAME
// submit path a person uses — the terminal content's own orchestration
// (block, ledger entry, attempt, output artifact — all minted at submit,
// with the agent's author, design §3.1) — waits for the completion, and
// answers (agent.runResolved) with the entry id, the exit status and a
// window of the output. The backend never writes to the PTY (design §2.1):
// session.Write exists and is the shortest path, and it is deliberately
// not used, because a byte written straight to the pty would exist with no
// entry — a second input surface, and an invisible one.
//
// The session the request names has already been narrowed by the run's
// grant before it left the backend; the renderer's only job is to submit
// through its own lane's ordinary path or answer honestly why it cannot (a
// session it does not know, a submission refused). The renderer never
// learns the word "grant" (design §2.2) — it cannot widen anything, only
// answer.

import type { Dispatcher } from './dispatcher'
import type { AgentRunRequest } from './generated/agent.runRequest'
import type { AgentRunResolved } from './generated/agent.runResolved'

/** The renderer-side clamp on one run resolution's output text (the Go
 *  transport's maxRunOutputWindowChars is the same bound, stated on its
 *  side of the wire): the model reads this much output per command, and the
 *  window statement says how much more the block holds. The output budget
 *  of the lease (ADR-0020 decision 2) is its own bead; this is the wire
 *  bound that exists today. */
export const MAX_RUN_OUTPUT_WINDOW_CHARS = 64 << 10 // 64 KiB of output text

/** The completed run body a submitted command resolves with: the entry id
 *  the command was accepted under (minted at submit by the ordinary path),
 *  the exit status of the completed block (null when it froze without one —
 *  an entered environment), total (the block's output line count), the span
 *  of the window actually returned and its text. The window is clamped by
 *  the renderer: a long output is answered honestly, never truncated
 *  silently — the total tells the model how much more the block holds. */
export interface AgentRunCompletion {
  entryId: string
  exitCode: number | null
  status: 'success' | 'failure' | 'entered' | 'unknown'
  total: number
  start: number
  end: number
  text: string
}

/** The seam the handler submits through: the terminal content that owns the
 *  lane's session. submitAgentCommand runs the SAME submit orchestration a
 *  person's Enter runs — the ledger record, the running block, the
 *  lifecycle attempt, the paste+CR delivery — with the agent's author, and
 *  resolves when the block freezes. */
export interface RunCommandContent {
  sessionId(): string
  submitAgentCommand(command: string): Promise<AgentRunCompletion>
}

/** Mount the run pull handler on the app's dispatcher. Returns the
 *  unsubscribe function. findContent resolves a session id to the terminal
 *  content that owns its lane; null when no tab holds the session (a closed
 *  tab, a viewer tab), which is answered honestly as a failed submission. */
export function mountRunCommandHandler(
  dispatcher: Dispatcher,
  findContent: (sessionId: string) => RunCommandContent | null,
): () => void {
  return dispatcher.subscribe('agent.runRequest', (params) => {
    const p = params as AgentRunRequest
    if (!p || !p.requestId || !p.sessionId || !p.command) return
    void answerRun(dispatcher, findContent, p)
  })
}

async function answerRun(
  dispatcher: Dispatcher,
  findContent: (sessionId: string) => RunCommandContent | null,
  p: AgentRunRequest,
): Promise<void> {
  const content = findContent(p.sessionId)
  if (!content) {
    resolve(dispatcher, {
      requestId: p.requestId,
      outcome: 'failed',
      error: `no such session: ${p.sessionId}`,
    })
    return
  }
  try {
    const run = await content.submitAgentCommand(p.command)
    resolve(dispatcher, {
      requestId: p.requestId,
      outcome: 'completed',
      entryId: run.entryId,
      exitCode: run.exitCode,
      status: run.status,
      total: run.total,
      start: run.start,
      end: run.end,
      text: run.text,
    })
  } catch (err) {
    // An honest "failed": the submission could not be made or completed.
    // The backend answers the pending request with the sentence rather than
    // hanging the run.
    const reason = err instanceof Error ? err.message : String(err)
    resolve(dispatcher, { requestId: p.requestId, outcome: 'failed', error: reason })
  }
}

function resolve(dispatcher: Dispatcher, params: AgentRunResolved): void {
  dispatcher.call('agent.runResolved', params).catch(() => {
    // The broker refused the resolution (a stale request id — the request
    // timed out or was dropped while the command ran). That is the server's
    // honest answer to a request that is gone; nothing to do here but stop.
    console.warn('nocx: run resolution refused (stale request?)')
  })
}

// The renderer half of readScreen (nocx-ljfwz): the backend asks
// (agent.readScreenRequest), the renderer produces the frame — because it
// owns the grid (AD-6) — and answers (agent.readScreenResolved) with the
// same frame shape it pushes for agent.captureFrame, produced by the same
// code (the renderer's captureLiveFrame + the frame wire conversion).
//
// The session the request names has already been narrowed by the run's grant
// before it left the backend; the renderer's only job is to produce the
// frame or answer honestly why it cannot (a session it does not know, a
// capture aborted by disposal, a region past the end of the buffer). The
// renderer never learns the word "grant" (design §2.2) — it cannot widen
// anything, only answer.

import { liveFrameToWire } from './frame/wire'
import type { Dispatcher } from './dispatcher'
import type { AgentReadScreenRequest } from './generated/agent.readScreenRequest'
import type { AgentReadScreenResolved } from './generated/agent.readScreenResolved'
import type { CapturedFrame } from './frame/types'

/** The seam the handler captures through: the terminal content that owns the
 *  renderer for one session. */
export interface ReadScreenContent {
  sessionId(): string
  captureLiveFrame(region?: { start: number; end: number }): Promise<CapturedFrame>
}

/** Mount the readScreen pull handler on the app's dispatcher. Returns the
 *  unsubscribe function. findContent resolves a session id to the terminal
 *  content that owns its grid; null when no tab holds the session (a closed
 *  tab, a viewer tab), which is answered honestly as a failed capture. */
export function mountReadScreenHandler(
  dispatcher: Dispatcher,
  findContent: (sessionId: string) => ReadScreenContent | null,
): () => void {
  return dispatcher.subscribe('agent.readScreenRequest', (params) => {
    const p = params as AgentReadScreenRequest
    if (!p || !p.requestId || !p.sessionId) return
    void answerReadScreen(dispatcher, findContent, p)
  })
}

async function answerReadScreen(
  dispatcher: Dispatcher,
  findContent: (sessionId: string) => ReadScreenContent | null,
  p: AgentReadScreenRequest,
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
    const frame = await content.captureLiveFrame(p.region)
    resolve(dispatcher, { requestId: p.requestId, outcome: 'frame', ...liveFrameToWire(frame) })
  } catch (err) {
    // An honest "failed": the capture could not be produced (disposal, a
    // region past the end of the buffer). The backend answers the pending
    // request with the sentence rather than hanging the run.
    const reason = err instanceof Error ? err.message : String(err)
    resolve(dispatcher, { requestId: p.requestId, outcome: 'failed', error: reason })
  }
}

function resolve(dispatcher: Dispatcher, params: AgentReadScreenResolved): void {
  dispatcher.call('agent.readScreenResolved', params).catch(() => {
    // The broker refused the resolution (a stale request id — the request
    // timed out or was dropped while the frame was being minted). That is
    // the server's honest answer to a request that is gone; nothing to do
    // here but stop.
    console.warn('nocx: readScreen resolution refused (stale request?)')
  })
}

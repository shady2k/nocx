// The ask transaction's renderer half (nocx-x8s2.2): an AgentInputTarget
// routes a submitted question through InputTargetRegistry.active(). The
// renderer sends the question and the whole-block grants; the backend reads
// granted items through session.read. No output is copied into the ask.

import type { Extension } from '@codemirror/state'
import { Dispatcher } from './dispatcher'
import type { AgentAsk } from './generated/agent.ask'
import type { AgentCancel } from './generated/agent.cancel'
import type { AgentRunDelta } from './generated/agent.runDelta'
import type { AgentRunReasoning } from './generated/agent.runReasoning'
import type { AgentRunToolCall } from './generated/agent.runToolCall'
import type { AgentRunState } from './generated/agent.runState'
import type { AgentStatusResult } from './generated/agent.status'
import type { InputTarget } from './input-target'
import type { GrantBlock } from './ask-entry'
import type { AnswerBlockHandle, RunningBlockActions } from './scrollback/blocks'

export interface AgentAskSeams {
  dispatcher: Dispatcher
  cancel: (runId: number) => Promise<AgentCancel>
  sessionId: () => string
  grants: () => ReadonlyArray<GrantBlock>
  cwd: () => string
  openAnswer: (question: string, cwd: string, running?: RunningBlockActions) => AnswerBlockHandle
  onRefusal: (message: string) => void
  onNoEndpoint?: () => void
  status?: () => Promise<AgentStatusResult>
  editorExtensions?: () => Extension[]
}

interface AskParams {
  askId: string
  sessionId: string
  question: string
  cwd: string
  attachedContent: {
    itemId: string
    command: string
    state: GrantBlock['state']
    start?: number
    count?: number
  }[]
}

export class AgentInputTarget implements InputTarget {
  readonly id = 'agent'
  readonly label = 'Agent'
  readonly author = 'agent'
  readonly routesToShell = false
  private readonly runs = new Map<number, { handle: AnswerBlockHandle; settle: () => void }>()
  private subscribed = false

  constructor(private readonly seams: AgentAskSeams) {}

  private async offerEndpointRepair(): Promise<void> {
    if (!this.seams.status || !this.seams.onNoEndpoint) return
    try {
      const st = await this.seams.status()
      if (!st.endpointConfigured) this.seams.onNoEndpoint()
    } catch {
      // The refusal is already visible; only the repair offer is lost.
    }
  }

  editorExtensions(): Extension[] {
    return this.seams.editorExtensions?.() ?? []
  }

  async submit(doc: string): Promise<void> {
    this.ensureSubscribed()
    const sessionId = this.seams.sessionId()
    const cwd = this.seams.cwd()
    const attachedContent = this.seams.grants().map(({ itemId, command, state, start, count }) => ({
      itemId,
      command,
      state,
      ...(start !== undefined && count !== undefined ? { start, count } : {}),
    }))
    const askParams: AskParams = {
      askId: crypto.randomUUID(),
      sessionId,
      question: doc,
      cwd,
      attachedContent,
    }

    let runId: number | null = null
    let settled = false
    let cancellationRequested = false
    const running: RunningBlockActions = {
      stop: (): void => {
        if (runId === null || settled || cancellationRequested) return
        cancellationRequested = true
        void this.seams.cancel(runId).catch(() => undefined)
      },
      isActive: (): boolean => runId !== null && !settled && !cancellationRequested,
    }
    const handle = this.seams.openAnswer(doc, cwd, running)
    const ask = await this.seams.dispatcher
      .call<AgentAsk>('agent.ask', askParams)
      .catch((err: unknown) => {
        handle.el.remove()
        const message = err instanceof Error ? err.message : String(err)
        this.seams.onRefusal(message)
        void this.offerEndpointRepair()
        throw err
      })
    runId = ask.runId
    handle.el.dataset.entryId = ask.entryId
    handle.el.dataset.answeredBy = ask.model
    this.runs.set(ask.runId, { handle, settle: () => (settled = true) })
  }

  /** Subscribe once: deltas append to the run's block; the terminal state
   *  closes it. A runState with no prior delta (a failure before any text)
   *  still has a block — the ask result's entryId opened it. */
  private ensureSubscribed(): void {
    if (this.subscribed) return
    this.subscribed = true
    this.seams.dispatcher.subscribe('agent.runDelta', (params: unknown) => {
      const d = params as AgentRunDelta
      const run = this.runs.get(d.runId)
      if (!run) return
      const handle = run.handle
      // Both ids are on every delta on purpose (design §7): the run id
      // finds the ask, the entry id confirms the deltas land on the right
      // block — a mismatch is a stale or misrouted notification and must
      // not append to the wrong one.
      if (handle.el.dataset.entryId !== d.entryId) return
      // …and the block id says WHERE in the turn: the `text` child this run
      // of prose is, opened by the backend on the first delta after a call
      // and sealed when the next one arrives (ADR-0040). The renderer never
      // works the boundary out — that is what the anchor did, and the live
      // path and the restore each computed it and could drift.
      handle.append(d.text, d.blockId)
    })
    // A tool call is a CHILD of this turn (ADR-0040), so it is routed
    // exactly as a delta is — same two ids, same mismatch rule — and handed
    // to the same handle. That is the whole ordering fix: the call takes its
    // seat when it arrives, which is before the deltas the model writes from
    // its result, so it can no longer read as "answered first, ran the
    // command afterwards".
    //
    // For a call that OPENS A BLOCK the handle draws no child of its own:
    // the block the command opened is the account of that call, and it takes
    // the seat. The routing is identical either way — the turn decides what
    // the call becomes, from a fact the backend sent.
    this.seams.dispatcher.subscribe('agent.runToolCall', (params: unknown) => {
      const c = params as AgentRunToolCall
      const run = this.runs.get(c.runId)
      if (!run) return
      const handle = run.handle
      if (handle.el.dataset.entryId !== c.entryId) return
      handle.toolCall({
        callId: c.callId,
        tool: c.tool,
        effect: c.effect,
        // What the model asked for, as the tool's schema validated it: the
        // half that tells two calls of one tool apart, which the tool name
        // and the derived resource cannot (ADR-0040).
        args: c.args,
        // The wire says `null` for a tool that names no resource; the flow
        // wants "absent", and the two must not be confused into a resource
        // with an empty half.
        resource: c.resource ?? undefined,
        // Whether the call's work becomes a block of its own — the tool
        // table's fact, off the wire and never derived from the name here.
        // It is what tells the turn to let that block take the next seat
        // instead of drawing a child that restates it.
        opensBlock: c.opensBlock,
      })
    })
    // The model's thinking, into its own note and never into the answer
    // text (nocx-s92so).
    this.seams.dispatcher.subscribe('agent.runReasoning', (params: unknown) => {
      const r = params as AgentRunReasoning
      const run = this.runs.get(r.runId)
      if (!run) return
      const handle = run.handle
      if (handle.el.dataset.entryId !== r.entryId) return
      handle.reasoning(r.text)
    })
    this.seams.dispatcher.subscribe('agent.runState', (params: unknown) => {
      const s = params as AgentRunState
      const run = this.runs.get(s.runId)
      if (!run) return
      const handle = run.handle
      if (handle.el.dataset.entryId === undefined) {
        // A run that failed before its first delta never carried the entry
        // id on a delta — but the block was opened from the ask result, and
        // the entry id was recorded there. If it is still missing, the
        // block was never associated: nothing to close.
        return
      }
      // A dropped live delta is a visible bound (the bead's criterion 1):
      // the wire refused one or more agent.runDelta frames, so the block
      // must not read as a complete answer. The durable answer is whole —
      // every chunk was persisted before the notify — so the run still
      // closes with the state it earned; the gap is marked, never turned
      // into a failure (nocx-dw3.1).
      if ((s.droppedDeltas ?? 0) > 0) {
        handle.append(
          s.droppedDeltas === 1
            ? '— part of the answer was dropped while streaming; the full answer was saved —'
            : `— ${s.droppedDeltas} chunks of the answer were dropped while streaming; the full answer was saved —`,
        )
      }
      if (s.state === 'completed') {
        run.settle()
        handle.close('success', undefined, handle.el.dataset.answeredBy)
      } else if (s.state === 'cancelled') {
        run.settle()
        handle.close('cancelled')
      } else if (s.state === 'failed' || s.state === 'interrupted') {
        run.settle()
        handle.close('failure', s.error ?? s.state)
      } else if (s.state === 'awaiting_approval') {
        // A question is outstanding: the block stays OPEN (nothing is
        // closed — the person decides in the approval prompt), and the run
        // stays routable so the RESUME's deltas land on this same block
        // (nocx-z9hj4). The terminal close arrives when the question is
        // answered.
        return
      } else {
        // Unknown state: keep the block open and routable rather than
        // closing or forgetting it — a state this renderer does not know
        // may still produce deltas.
        return
      }
      this.runs.delete(s.runId)
    })
  }
}

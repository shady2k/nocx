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
  /** Called only after the backend accepts the turn, so a speculative
   *  overlay can change presentation without hiding itself on refusal. */
  onTurnAccepted?: (askId: string) => void
  onTurnStart?: (askId: string) => void
  onTurnEnd?: (askId: string) => void
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
    automatic?: true
  }[]
}

export class AgentInputTarget implements InputTarget {
  readonly id = 'agent'
  readonly label = 'Agent'
  readonly author = 'agent'
  readonly routesToShell = false
  private readonly runs = new Map<
    number,
    { handle: AnswerBlockHandle; settle: () => void; askId: string }
  >()
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
    const attachedContent = this.seams
      .grants()
      .map(({ itemId, command, state, start, count, automatic }) => ({
        itemId,
        command,
        state,
        ...(start !== undefined && count !== undefined ? { start, count } : {}),
        ...(automatic === true ? { automatic: true as const } : {}),
      }))
    const askId = crypto.randomUUID()
    const askParams: AskParams = {
      askId,
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
        const cancellingRunID = runId
        cancellationRequested = true
        // The response is reserved and arrives after durable terminalization;
        // runState is a notification and may be dropped under outbound pressure.
        // Whichever arrives first closes the same handle exactly once.
        void this.seams
          .cancel(cancellingRunID)
          .then((result) => this.settleRun(cancellingRunID, result))
          .catch(() => {
            // The backend refused the stop; keep the menu/key action retryable.
            cancellationRequested = false
          })
      },
      isActive: (): boolean => runId !== null && !settled && !cancellationRequested,
    }
    const handle = this.seams.openAnswer(doc, cwd, running)
    this.seams.onTurnStart?.(askId)
    const ask = await this.seams.dispatcher
      .call<AgentAsk>('agent.ask', askParams)
      .catch((err: unknown) => {
        this.seams.onTurnEnd?.(askId)
        handle.el.remove()
        const message = err instanceof Error ? err.message : String(err)
        this.seams.onRefusal(message)
        void this.offerEndpointRepair()
        throw err
      })
    this.seams.onTurnAccepted?.(askId)
    runId = ask.runId
    handle.el.dataset.entryId = ask.entryId
    handle.el.dataset.answeredBy = ask.model
    this.runs.set(ask.runId, { handle, askId, settle: () => (settled = true) })
  }

  private settleRun(
    runId: number,
    presentation: {
      state: AgentRunState['state']
      error?: string
      droppedDeltas?: number
      unarmedBounds?: string[]
    },
  ): void {
    const run = this.runs.get(runId)
    if (!run) return
    if (
      presentation.state !== 'completed' &&
      presentation.state !== 'cancelled' &&
      presentation.state !== 'failed' &&
      presentation.state !== 'interrupted'
    )
      return

    // Delete first: runState and the reserved cancel response may race, and
    // either may synchronously trigger presentation callbacks. The first
    // terminal fact owns every effect below; the second is a no-op.
    this.runs.delete(runId)
    run.settle()
    const dropped = presentation.droppedDeltas ?? 0
    if (dropped > 0) {
      run.handle.append(
        dropped === 1
          ? '— part of the answer was dropped while streaming; the full answer was saved —'
          : `— ${dropped} chunks of the answer were dropped while streaming; the full answer was saved —`,
      )
    }
    const unarmed = presentation.unarmedBounds ?? []
    if (unarmed.length > 0) {
      run.handle.append(`— ${unarmed.join('; ')}; only the wall-clock deadline remains active —`)
    }
    if (presentation.state === 'completed') {
      run.handle.close('success', undefined, run.handle.el.dataset.answeredBy)
    } else if (presentation.state === 'cancelled') {
      run.handle.close('cancelled', presentation.error)
    } else {
      run.handle.close('failure', presentation.error ?? presentation.state)
    }
    this.seams.onTurnEnd?.(run.askId)
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
        // The LEDGER action entry the attempt was recorded under — the
        // handle the call's expansion reaches its result through, rather
        // than a second copy of the bytes on this wire (nocx-hp8p2.13).
        actionEntryId: c.actionEntryId,
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
      const state = params as AgentRunState
      this.settleRun(state.runId, state)
    })
  }
}

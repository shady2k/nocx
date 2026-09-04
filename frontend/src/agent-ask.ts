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
import type { AgentStandingAnswerSaved } from './generated/agent.standingAnswerSaved'
import { standingAnswerReceipt } from './agent-approval-prompt'
import { EFFECT_LABEL } from './effect-labels'
import { PolicyClient } from './policy-client'
import type { BlockNotice, BlockNoticeAction } from './ui/block-notice'

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
  /** Open the page where standing answers are managed — the receipt's second
   *  action. Injected because a pane cannot know where Settings lives; absent
   *  in a bare-bones embedding, and then the receipt simply does not offer
   *  what nothing can do. */
  openPermissions?: () => void
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
      notices?: string[]
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
    // What the RUN says about ITSELF — today, only that it stopped asking a
    // person to widen a scope because it hit the bound on how often one
    // answer may ask (design §5.3). Its own line, never folded into the
    // unarmed-bounds sentence: a bound that could not be ARMED and a bound
    // the run stopped ASKING about are different facts, and one sentence
    // carrying both would state the wrong one of them. A run that stopped
    // asking and said nothing is exactly the soft degrade AGENTS.md forbids
    // — the person would be left inferring it from questions that never come.
    const notices = presentation.notices ?? []
    if (notices.length > 0) {
      run.handle.append(`— ${notices.join('; ')} —`)
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

  /**
   * The receipt for one saved standing answer, and the two things a person
   * can do about it.
   *
   * UNDO NAMES THE RULE, and that is the whole design. It forgets THAT id
   * through policy.forgetRule and never restores a snapshot of the policy:
   * an answer given between the save and the undo — by this person in another
   * pane, or by the settings page — would be silently discarded by a restore,
   * which is the same lost-write the backend seam was just fixed to prevent.
   *
   * `removed: false` is a SUCCESS. It means no rule wears that id, which is
   * exactly the state undoing asked for, and the line says so plainly rather
   * than raising an error at somebody about a thing they wanted.
   */
  private drawStandingAnswer(handle: AnswerBlockHandle, saved: AgentStandingAnswerSaved): void {
    const manage: BlockNoticeAction | null =
      this.seams.openPermissions === undefined
        ? null
        : { label: 'Manage permissions', onActivate: () => this.seams.openPermissions?.() }
    const withManage = (...before: BlockNoticeAction[]): BlockNoticeAction[] =>
      manage === null ? before : [...before, manage]

    let notice: BlockNotice | null = null
    let undoing = false
    const undo = (): void => {
      if (undoing || notice === null) return
      undoing = true
      const line = notice
      // policy.forgetRule has an owner (AD-8); this dials it through that
      // owner rather than growing a second spelling of the call beside it.
      new PolicyClient(this.seams.dispatcher)
        .forgetRule(saved.ruleId)
        .then((result) => {
          line.say({
            text: result.removed
              ? 'Undone — that answer is no longer saved.'
              : 'That answer was already gone.',
            actions: withManage(),
          })
        })
        .catch((err: unknown) => {
          undoing = false
          line.say({
            text: `It could not be undone: ${err instanceof Error ? err.message : String(err)}`,
            tone: 'warning',
            // Still offered: the rule is still there, so the act is still
            // available, and a receipt that dropped its own action after a
            // failed attempt would leave the person nowhere to try again.
            actions: withManage({ label: 'Undo', onActivate: undo }),
          })
        })
    }

    notice = handle.notice({
      text: standingAnswerReceipt(
        saved.approved,
        saved.scope,
        saved.rule,
        EFFECT_LABEL[saved.effect],
      ),
      // No id, no Undo: a session overlay and a matrix row are not
      // addressable by one, and an action that cannot name what it would
      // undo is a button that advertises what it cannot deliver.
      actions: saved.ruleId === '' ? withManage() : withManage({ label: 'Undo', onActivate: undo }),
    })
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
    // A STANDING ANSWER WAS WRITTEN (nocx-2019q). It is routed exactly as a
    // delta is — the run id finds the turn, the entry id confirms the block —
    // because it IS a fact about that run: the person answered the question
    // this turn raised, and the place that answer belongs is the turn that
    // raised it. Before this the rule went into the store and the terminal
    // said nothing, so the only way to find out you had configured something
    // was to stop being asked a question you had forgotten answering.
    this.seams.dispatcher.subscribe('agent.standingAnswerSaved', (params: unknown) => {
      const saved = params as AgentStandingAnswerSaved
      // The wire says the run id as a STRING here, as agent.approvalRequested
      // and agent.approve do — the whole approval exchange is in that
      // vocabulary — while the ask that opened this turn minted a number.
      // One conversion, at the one seam where the two meet.
      const run = this.runs.get(Number(saved.runId))
      if (!run) return
      const handle = run.handle
      if (saved.entryId !== '' && handle.el.dataset.entryId !== saved.entryId) return
      this.drawStandingAnswer(handle, saved)
    })
    this.seams.dispatcher.subscribe('agent.runState', (params: unknown) => {
      const state = params as AgentRunState
      this.settleRun(state.runId, state)
    })
  }
}

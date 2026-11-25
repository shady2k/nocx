// The ask transaction's renderer half (nocx-x8s2.2): an AgentInputTarget
// routes a submitted question through InputTargetRegistry.active(). The
// renderer sends the question and the whole-block grants; the backend reads
// granted items through session.read. No output is copied into the ask.

import type { Extension } from '@codemirror/state'
import { Dispatcher } from './dispatcher'
import type { AgentAsk } from './generated/agent.ask'
import type { AgentCancel } from './generated/agent.cancel'
import type { AgentSteer } from './generated/agent.steer'
import type { PersonMessage } from './ui/person-message'
import type { AgentRunDelta } from './generated/agent.runDelta'
import type { AgentRunReasoning } from './generated/agent.runReasoning'
import type { AgentRunToolCall } from './generated/agent.runToolCall'
import type { AgentRunState } from './generated/agent.runState'
import type { InputTarget } from './input-target'
import type { GrantBlock } from './ask-entry'
import type { AnswerBlockHandle, RunningBlockActions } from './scrollback/blocks'
import type { AgentStandingAnswerSaved } from './generated/agent.standingAnswerSaved'
import { standingAnswerReceipt } from './agent-approval-prompt'
import { EFFECT_LABEL } from './effect-labels'
import { PolicyClient } from './policy-client'
import type { BlockNotice, BlockNoticeAction } from './ui/block-notice'
import { t } from './i18n/locale-model'

export interface AgentAskSeams {
  dispatcher: Dispatcher
  cancel: (runId: number) => Promise<AgentCancel>
  sessionId: () => string
  grants: () => ReadonlyArray<GrantBlock>
  /** Take the marks the question carried. A message sent into a running turn
   *  carries none — it reaches an agent that already holds the conversation —
   *  so the marks stay for the next question rather than being dropped by a
   *  submit that never used them. */
  consumeGrants: () => void
  cwd: () => string
  openAnswer: (question: string, cwd: string, running?: RunningBlockActions) => AnswerBlockHandle
  /** Deliver a person's message into the turn that is answering, and answer
   *  once the agent took it. The message is delivered, not queued as a new
   *  question: the agent still holds the conversation. */
  steer: (runId: number, text: string) => Promise<AgentSteer>
  onRefusal: (message: string) => void
  /** Called only after the backend accepts the turn, so a speculative
   *  overlay can change presentation without hiding itself on refusal. */
  onTurnAccepted?: (askId: string) => void
  onTurnEnd?: (askId: string) => void
  ensureReady?: () => Promise<boolean>
  /** Open the page where standing answers are managed — the receipt's second
   *  action. Injected because a pane cannot know where Settings lives; absent
   *  in a bare-bones embedding, and then the receipt simply does not offer
   *  what nothing can do. */
  openPermissions?: () => void
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
    {
      handle: AnswerBlockHandle
      settle: () => void
      askId: string
      /** The messages this turn has been given, with the line each was drawn
       *  on: a cancel names the ones the agent dropped, and the block restates
       *  exactly those. */
      steered: { text: string; line: PersonMessage }[]
    }
  >()
  /** Messages a turn kept for later, in the order the person sent them: each is
   *  asked as the conversation's next question once that turn ends. */
  private nextQuestions: string[] = []
  /** Whether a kept message is already being asked. One question in flight per
   *  conversation: the backend can run one turn at a time, and two asks started
   *  in the same tick would both open a block and one would sit behind the
   *  other's whole answer. */
  private askingNext = false
  private subscribed = false

  constructor(private readonly seams: AgentAskSeams) {}

  private async offerAgentRepair(): Promise<void> {
    try {
      await this.seams.ensureReady?.()
    } catch {
      // The refusal is already visible; only the repair offer is lost.
    }
  }

  editorExtensions(): Extension[] {
    return this.seams.editorExtensions?.() ?? []
  }

  async submit(doc: string): Promise<void> {
    if (this.seams.ensureReady && !(await this.seams.ensureReady())) {
      throw new Error('The answering agent is not ready')
    }
    this.ensureSubscribed()
    // A turn is running in this pane's conversation: the person is speaking to
    // the agent that is answering, not asking a new question (ADR-anntnxdj
    // decision 4). One turn, one block — the message lands in the block being
    // written, at the point in the answer where it was sent.
    const answering = this.runningRun()
    if (answering !== null) {
      await this.steer(answering.runId, answering.handle, doc)
      return
    }
    await this.ask(doc)
  }

  /** Ask one question: a block of its own, a run of its own. */
  private async ask(doc: string): Promise<void> {
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
    const ask = await this.seams.dispatcher
      .call<AgentAsk>('agent.ask', askParams)
      .catch((err: unknown) => {
        this.seams.onTurnEnd?.(askId)
        handle.el.remove()
        const message = err instanceof Error ? err.message : String(err)
        this.seams.onRefusal(message)
        void this.offerAgentRepair()
        throw err
      })
    this.seams.onTurnAccepted?.(askId)
    // The question took the marks it rode with it; nothing else claims them.
    this.seams.consumeGrants()
    runId = ask.runId
    handle.el.dataset.entryId = ask.entryId
    handle.el.dataset.answeredBy = ask.model
    this.runs.set(ask.runId, {
      handle,
      askId,
      steered: [],
      settle: () => (settled = true),
    })
  }

  /** The conversation's live turn, if one is being answered. The pane's runs
   *  are the pane's conversation (§ conversation = the pane), and a run leaves
   *  this map at its terminal, so what is found here is a turn still running. */
  private runningRun(): { runId: number; handle: AnswerBlockHandle } | null {
    for (const [runId, run] of this.runs) {
      // A TURN IS ADDRESSABLE ONLY BY THE ID THE BACKEND MINTED. A run recorded
      // without one has no turn to steer — there is nothing to name in
      // agent.steer — so the person's next submit is a QUESTION again, which is
      // the only thing that can work (nocx-a62cl.6.1).
      if (!Number.isInteger(runId) || runId <= 0) continue
      return { runId, handle: run.handle }
    }
    return null
  }

  /** Hand the person's message to the running turn. It is drawn only after the
   *  agent took it: a message a turn ended under is refused by the backend,
   *  and a block that had already shown it would be claiming a delivery that
   *  never happened. */
  private async steer(runId: number, handle: AnswerBlockHandle, text: string): Promise<void> {
    let outcome: AgentSteer['outcome']
    try {
      outcome = (await this.seams.steer(runId, text)).outcome
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : String(err)
      this.seams.onRefusal(message)
      throw err
    }
    // WHAT BECAME OF IT, as the backend stated it (ADR-anntnxdj decision 4). The
    // message goes into the turn, or it is kept and asked once the turn ends —
    // because the agent cannot steer, or because the turn could not take it. The
    // block says which, and a kept message is asked as the conversation's next
    // question as soon as this turn terminalizes.
    const queued = outcome === 'next_question'
    const line = handle.steer(text, queued ? 'next_question' : 'steered')
    const run = this.runs.get(runId)
    if (run !== undefined) run.steered.push({ text, line })
    if (queued) {
      this.nextQuestions.push(text)
      // DRAIN ON ENQUEUE, not only at the terminal: the reply can arrive after
      // the turn's terminal (the backend answers a message it could not take
      // once it knows), and a queue drained only at the terminal would hold
      // that message for ever.
      this.askNextQuestions()
    }
  }

  private settleRun(
    runId: number,
    presentation: {
      state: AgentRunState['state']
      error?: string
      droppedDeltas?: number
      droppedSteers?: string[]
      notices?: string[]
      unarmedBounds?: string[]
      usage?: AgentRunState['usage']
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
      run.handle.close('success', undefined, run.handle.el.dataset.answeredBy, presentation.usage)
    } else if (presentation.state === 'cancelled') {
      run.handle.close('cancelled', presentation.error, undefined, presentation.usage)
    } else {
      run.handle.close(
        'failure',
        presentation.error ?? presentation.state,
        undefined,
        presentation.usage,
      )
    }
    // A CANCELLED TURN HANDS BACK WHAT ITS AGENT DROPPED (ADR-4khgzeyp decision
    // 3): the messages it had queued for steering and never used. The lines
    // already drawn for them are restated as waiting — the same words, marked —
    // and each is asked as the conversation's next question like any other kept
    // message.
    for (const dropped of presentation.droppedSteers ?? []) {
      const index = run.steered.findIndex((entry) => entry.text === dropped)
      if (index === -1) {
        // A message this surface did not send (an extension's, or one from a
        // previous window): it is still the person's, and it is still not lost.
        this.nextQuestions.push(dropped)
        continue
      }
      run.steered[index].line.markQueued()
      run.steered.splice(index, 1)
      this.nextQuestions.push(dropped)
    }
    this.askNextQuestions()
    this.seams.onTurnEnd?.(run.askId)
    // The messages this turn could not take are asked now, as its successors:
    // each opens its own block and is a question like any other (ADR-anntnxdj
    // decision 4 — a message is never lost to the turn that could not take it).
    this.askNextQuestions()
  }

  /**
   * Ask whatever the just-finished turn kept for later, oldest first.
   *
   * One at a time, deliberately: `ask` opens a block and starts a run, and the
   * question after it belongs to THAT run's conversation. This queue is the
   * same pane's, so the next one waits for the run it follows — which is what
   * "the conversation's next question" means.
   */
  private askNextQuestions(): void {
    if (this.askingNext || this.nextQuestions.length === 0 || this.runs.size > 0) return
    const [text, ...rest] = this.nextQuestions
    this.nextQuestions = rest
    this.askingNext = true
    void this.ask(text)
      .catch(() => {
        // The refusal is already on screen (onRefusal); the message itself was
        // the person's, and a turn that would not start must not swallow the
        // rest of the queue.
      })
      .finally(() => {
        this.askingNext = false
        // The turn that just started holds the queue (runs.size), and asks the
        // next one when it terminalizes; a turn that never started leaves the
        // queue to this call.
        this.askNextQuestions()
      })
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
        : { label: t('agentAsk.managePermissions', 'Manage permissions'), onActivate: () => this.seams.openPermissions?.() }
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
            actions: withManage({ label: t('agentAsk.undo', 'Undo'), onActivate: undo }),
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
      actions: saved.ruleId === '' ? withManage() : withManage({ label: t('agentAsk.undo', 'Undo'), onActivate: undo }),
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
      // Routed EXACTLY as a delta is, with no escape hatch. The empty
      // entryId this used to wave through was the schema's invention: it
      // promised "empty when the proposal was never bound to an entry", and
      // nothing on the backend can produce that (contracts/, and the trace
      // at agentStandingAnswerSaved.EntryID). A wildcard for a value nobody
      // sends is a second reading of routing that disagrees with every
      // sibling here, and it would draw a receipt on whatever block the run
      // id happened to find.
      if (handle.el.dataset.entryId !== saved.entryId) return
      this.drawStandingAnswer(handle, saved)
    })
    this.seams.dispatcher.subscribe('agent.runState', (params: unknown) => {
      const state = params as AgentRunState
      this.settleRun(state.runId, state)
    })
  }
}

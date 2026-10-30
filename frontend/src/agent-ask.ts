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
import { readTurnSnapshot, reconcileTurn, type RestoredCause } from './restore-client'

export interface AgentAskSeams {
  dispatcher: Dispatcher
  cancel: (runId: number) => Promise<AgentCancel>
  sessionId: () => string
  grants: () => ReadonlyArray<GrantBlock>
  cwd: () => string
  openAnswer: (question: string, cwd: string, running?: RunningBlockActions) => AnswerBlockHandle
  findAnswer: (entryId: string, running: RunningBlockActions) => AnswerBlockHandle | null
  onAnswerReused?: (existing: AnswerBlockHandle, speculative: AnswerBlockHandle) => void
  drawCommand: (cause: RestoredCause) => Promise<HTMLElement | null>
  onRefusal: (message: string) => void
  /** Called only after the backend accepts the turn, so a speculative
   *  overlay can change presentation without hiding itself on refusal. */
  onTurnAccepted?: (askId: string) => void
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

type RunPresentation = {
  state: AgentRunState['state']
  error?: string
  droppedDeltas?: number
  notices?: string[]
  unarmedBounds?: string[]
  durationMs?: number | null
}

type RunEvent =
  | { kind: 'delta'; value: AgentRunDelta }
  | { kind: 'tool'; value: AgentRunToolCall }
  | { kind: 'reasoning'; value: AgentRunReasoning }
  | { kind: 'standing'; value: AgentStandingAnswerSaved }
  | { kind: 'state'; value: AgentRunState }

interface RunView {
  handle: AnswerBlockHandle
  settle: () => void
  onTurnEnd: () => void
  hydrating: boolean
  queued: RunEvent[]
  hydration?: Promise<void>
  watermarks: Map<string, number>
  terminal?: RunPresentation
}

export class AgentInputTarget implements InputTarget {
  readonly id = 'agent'
  readonly label = 'Agent'
  readonly author = 'agent'
  readonly routesToShell = false
  private readonly runs = new Map<number, RunView>()
  private pendingAsks = 0
  private earlyEvents: RunEvent[] = []
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
    let handle = this.seams.openAnswer(doc, cwd, running)
    this.pendingAsks++
    const ask = await this.seams.dispatcher
      .call<AgentAsk>('agent.ask', askParams)
      .catch((err: unknown) => {
        this.seams.onTurnEnd?.(askId)
        this.pendingAsks--
        if (this.pendingAsks === 0) this.earlyEvents = []
        handle.el.remove()
        const message = err instanceof Error ? err.message : String(err)
        this.seams.onRefusal(message)
        void this.offerEndpointRepair()
        throw err
      })
    this.pendingAsks--
    this.seams.onTurnAccepted?.(askId)
    runId = ask.runId
    const active = this.runs.get(ask.runId)
    const existing = active?.handle ?? this.seams.findAnswer(ask.entryId, running)
    if (existing && existing !== handle) {
      this.seams.onAnswerReused?.(existing, handle)
      handle.el.remove()
      handle = existing
    }
    handle.el.dataset.entryId = ask.entryId
    handle.el.dataset.answeredBy = ask.model
    handle.provenance(ask.engine, ask.runtimeVersion)
    const finishSubmission = (): void => {
      settled = true
    }
    const endSubmission = (): void => {
      this.seams.onTurnEnd?.(askId)
    }
    const run: RunView = active ?? {
      handle,
      settle: finishSubmission,
      onTurnEnd: endSubmission,
      hydrating: false,
      queued: [],
      watermarks: new Map(),
    }
    if (active) {
      const previousSettle = active.settle
      const previousEnd = active.onTurnEnd
      active.settle = () => {
        previousSettle()
        finishSubmission()
      }
      active.onTurnEnd = () => {
        previousEnd()
        endSubmission()
      }
    }
    this.runs.set(ask.runId, run)
    run.hydrating ||= ask.replayed
    const early = this.earlyEvents.filter(
      (event) =>
        Number(event.value.runId) === ask.runId &&
        (event.kind === 'state' || event.value.entryId === ask.entryId),
    )
    this.earlyEvents =
      this.pendingAsks === 0
        ? []
        : this.earlyEvents.filter((event) => Number(event.value.runId) !== ask.runId)
    for (const event of early) this.receive(event)
    if (ask.replayed) {
      run.hydration ??= this.hydrate(ask, run).finally(() => {
        run.hydration = undefined
      })
      await run.hydration
    } else this.settleRun(ask.runId, ask)
  }

  private settleRun(runId: number, presentation: RunPresentation): void {
    const run = this.runs.get(runId)
    if (!run) return
    if (
      presentation.state !== 'completed' &&
      presentation.state !== 'cancelled' &&
      presentation.state !== 'failed' &&
      presentation.state !== 'interrupted'
    )
      return
    if (run.hydrating) {
      run.terminal = presentation
      return
    }

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
    const outcome =
      presentation.state === 'completed'
        ? 'success'
        : presentation.state === 'cancelled'
          ? 'cancelled'
          : 'failure'
    const error =
      outcome === 'success'
        ? undefined
        : outcome === 'cancelled'
          ? presentation.error
          : (presentation.error ?? presentation.state)
    if (presentation.durationMs !== undefined) {
      run.handle.close(outcome, error, run.handle.el.dataset.answeredBy, presentation.durationMs)
    } else if (outcome === 'success') {
      run.handle.close(outcome, error, run.handle.el.dataset.answeredBy)
    } else run.handle.close(outcome, error)
    run.onTurnEnd()
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

  private async hydrate(ask: AgentAsk, run: RunView): Promise<void> {
    let presentation: RunPresentation = ask
    try {
      let finalRead = false
      for (;;) {
        const snapshot = await readTurnSnapshot(this.seams.dispatcher, ask.entryId)
        const savedWatermarks = await reconcileTurn(run.handle, snapshot, this.seams.drawCommand)
        // Children absent from this snapshot were not redrawn. Keep their live
        // watermark, while redrawn children use the store's actual maximum seq.
        for (const [blockId, seq] of savedWatermarks) run.watermarks.set(blockId, seq)
        const saved = snapshot.agentRuns.find((candidate) => candidate.runId === ask.runId)
        if (saved) presentation = { state: saved.state, durationMs: snapshot.entry.durationMs }
        // A terminal notification proves its commit preceded this next read.
        // Reread metadata too, not only the children the first snapshot saw.
        if (run.terminal && !finalRead) {
          finalRead = true
          continue
        }
        break
      }
    } catch (err) {
      run.handle.notice({
        text: `The saved answer could not be read: ${err instanceof Error ? err.message : String(err)}`,
        tone: 'warning',
      })
    }
    run.hydrating = false
    const queued = run.queued
    run.queued = []
    for (const event of queued) {
      if (event.kind !== 'state') this.apply(run, event)
    }
    this.settleRun(ask.runId, run.terminal ? { ...presentation, ...run.terminal } : presentation)
  }

  private receive(event: RunEvent): void {
    const run = this.runs.get(Number(event.value.runId))
    if (!run) {
      if (this.pendingAsks > 0) this.earlyEvents.push(event)
      return
    }
    if (event.kind !== 'state' && run.handle.el.dataset.entryId !== event.value.entryId) return
    if (run.hydrating) {
      if (event.kind === 'state') this.settleRun(Number(event.value.runId), event.value)
      else run.queued.push(event)
      return
    }
    this.apply(run, event)
  }

  private apply(run: RunView, event: RunEvent): void {
    switch (event.kind) {
      case 'delta': {
        const delta = event.value
        if (delta.seq <= (run.watermarks.get(delta.blockId) ?? -1)) return
        run.watermarks.set(delta.blockId, delta.seq)
        run.handle.append(delta.text, delta.blockId)
        break
      }
      case 'tool': {
        const call = event.value
        run.handle.toolCall({
          callId: call.callId,
          actionEntryId: call.actionEntryId,
          tool: call.tool,
          effect: call.effect,
          args: call.args,
          resource: call.resource ?? undefined,
          opensBlock: call.opensBlock,
        })
        break
      }
      case 'reasoning':
        run.handle.reasoning(event.value.text)
        break
      case 'standing':
        this.drawStandingAnswer(run.handle, event.value)
        break
      case 'state':
        this.settleRun(event.value.runId, event.value)
        break
    }
  }

  /** Subscribe before sending asks, so no accepted event can outrun its handle. */
  private ensureSubscribed(): void {
    if (this.subscribed) return
    this.subscribed = true
    this.seams.dispatcher.subscribe('agent.runDelta', (value: unknown) =>
      this.receive({ kind: 'delta', value: value as AgentRunDelta }),
    )
    this.seams.dispatcher.subscribe('agent.runToolCall', (value: unknown) =>
      this.receive({ kind: 'tool', value: value as AgentRunToolCall }),
    )
    this.seams.dispatcher.subscribe('agent.runReasoning', (value: unknown) =>
      this.receive({ kind: 'reasoning', value: value as AgentRunReasoning }),
    )
    this.seams.dispatcher.subscribe('agent.standingAnswerSaved', (value: unknown) =>
      this.receive({ kind: 'standing', value: value as AgentStandingAnswerSaved }),
    )
    this.seams.dispatcher.subscribe('agent.runState', (value: unknown) =>
      this.receive({ kind: 'state', value: value as AgentRunState }),
    )
  }
}

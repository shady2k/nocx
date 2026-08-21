// The ask transaction's renderer half (nocx-x8s2.2): an AgentInputTarget
// routes a submitted question through InputTargetRegistry.active() — the
// ADR-0004 §3 seam (the editor stays passive; the target decides where a
// submitted document goes). Submitting mints the frozen frame from the
// selected block through mintFrozenFrame (the ONE derivation — a frozen
// block has no xterm cells left, and its text was already transformed by
// the serializer), ingests it with agent.captureFrame, asks with
// agent.ask, and streams the answer into an answer block in the flow.
//
// The wire shapes are declared once in contracts/agent.*.schema.json; the
// types here are generated from them.

import type { Extension } from '@codemirror/state'
import { Dispatcher } from './dispatcher'
import { frozenFrameSourceFromBlock, mintFrozenFrame } from './frame/frozen'
import type { AgentAsk } from './generated/agent.ask'
import type { AgentCaptureFrame } from './generated/agent.captureFrame'
import type { AgentRunDelta } from './generated/agent.runDelta'
import type { AgentRunState } from './generated/agent.runState'
import type { AgentStatusResult } from './generated/agent.status'
import type { InputTarget } from './input-target'
import type { ReferenceChip } from './ask-entry'
import type { AnswerBlockHandle } from './scrollback/blocks'
import { SERIALIZER_VERSION } from './scrollback/serializer'

export interface AgentAskSeams {
  /** The tab's JSON-RPC dispatcher (agent.* methods + notifications). */
  dispatcher: Dispatcher
  /** The tab's session id — backend-authoritative, never the renderer's own. */
  sessionId: () => string
  /** The reference chips currently in the input line (nocx-4wtlh): the
   *  frozen regions a question carries, resolved from the CHIPS — never
   *  re-derived from DOM selection at submit time (AD-8: selection is
   *  copy; the chip is what the person pointed at). A question carries
   *  exactly these and no others; an empty list is a general question
   *  with no pointed-at context. */
  chips: () => ReadonlyArray<ReferenceChip>
  cwd: () => string
  /** Render the answer block for one ask; the returned handle is the ONLY
   *  way the block's body and status change. */
  openAnswer: (question: string, cwd: string) => AnswerBlockHandle
  /** A refusal the surface must render (e.g. "no endpoint configured") —
   *  the product's rule: a soft degrade is visible, never only in a log.
   *  The ask surface raises it through the kit's one notification
   *  affordance. */
  onRefusal: (message: string) => void
  /** The refusal that has a REPAIR: no endpoint is configured, so there is
   *  a thing the person can do about it and the surface offers to do it
   *  (the host opens the endpoint editor). Raised in addition to
   *  onRefusal — the sentence is still shown; this only adds the way out.
   *  Optional: a host with nowhere to send them wires nothing and the
   *  refusal stays a sentence. */
  onNoEndpoint?: () => void
  /** The assistant's readiness facts (agent.status). WHY an ask was refused
   *  is read from this contract-declared fact, never sniffed out of the
   *  error text: the message is prose the backend may reword, while
   *  endpointConfigured is the schema's own answer to the same question.
   *  Absent, the refusal is reported and nothing else happens. */
  status?: () => Promise<AgentStatusResult>
  /** This target's editor layer (design §8.8), installed while Ask is
   *  where Enter goes. Deliberately NOT the shell's: a question is prose,
   *  so the shell highlighter and the command completion surface stay
   *  behind — `Привет!` is not an operator. The document-level surfaces
   *  that are language-agnostic (the vault chip and its candidate mark)
   *  are the caller's to pass, so a reference the person inserts is still
   *  rendered as a chip rather than raw text. */
  editorExtensions?: () => Extension[]
}

/** The wire params of agent.captureFrame for a frozen frame (design §2.2:
 *  text rows, no cursor, no identity/range — the backend's validation
 *  enforces exactly this shape). */
interface FrozenCaptureParams {
  captureId: string
  sessionId: string
  source: 'frozen'
  rows: { kind: 'text'; text: string }[]
  serializerVersion: number
  cwd: string
}

/** The wire params of agent.ask. */
interface AskParams {
  askId: string
  sessionId: string
  question: string
  cwd: string
  references: { frameId: string; region: { rowStart: number; rowEnd: number } }[]
}

/**
 * The agent input target: a submitted document is a QUESTION about the
 * selected block. Constructed with its seams (like ShellInputTarget); it
 * holds no store and no editor.
 */
export class AgentInputTarget implements InputTarget {
  readonly id = 'agent'
  readonly label = 'Agent'
  /** The assistant is the author of the questions it asks. A question
   *  never opens a ledger record (the shell orchestration skips it), but
   *  the target still declares its author — the same vocabulary a
   *  command-bearing agent target will submit with (design §3.1,
   *  nocx-iadtt). */
  readonly author = 'agent'
  /** A question is not a shell command: the composition root must not run
   *  the shell submit orchestration (keyboard handoff, ledger record,
   *  running block, attempt) for it (nocx-x8s2.2). */
  readonly routesToShell = false
  /** runId → the answer block the deltas append to. The renderer routes by
   *  runId AND entryId (both are on every delta) — "the current answer" is
   *  not an identity, and two overlapping asks land on their own blocks. */
  private readonly runs = new Map<number, AnswerBlockHandle>()
  private subscribed = false

  constructor(private readonly seams: AgentAskSeams) {}

  /** Ask agent.status why the refusal happened, and raise the repair seam
   *  when the answer is "no endpoint". Fire-and-forget and fail-quiet: the
   *  refusal sentence is already on screen, so a status call that fails
   *  costs the offer, never the report. */
  private async offerEndpointRepair(): Promise<void> {
    if (!this.seams.status || !this.seams.onNoEndpoint) return
    try {
      const st = await this.seams.status()
      if (!st.endpointConfigured) this.seams.onNoEndpoint()
    } catch {
      // The refusal is reported; the offer is what is lost.
    }
  }

  /** The editor layer for a question (design §8.8). Empty unless the host
   *  supplies one — the target never reaches into the editor itself. */
  editorExtensions(): Extension[] {
    return this.seams.editorExtensions?.() ?? []
  }

  /** Submit a question: ingest one frozen frame PER REFERENCED BLOCK (the
   *  backend mints the frame ids), then ask with one reference per chip —
   *  the chip's region into its block's frame. A general question (no
   *  chips) is just the ask with zero references.
   *
   *  CONTRACT: the chips are read from the seam SYNCHRONOUSLY — the
   *  grouping loop below runs before this async body's first await. The
   *  surface relies on that: it consumes the chips (clears the line) the
   *  moment submit returns, and an ask that deferred the read would find
   *  its payload already gone. Keep the read ahead of the first await. */
  async submit(doc: string): Promise<void> {
    this.ensureSubscribed()
    const sessionId = this.seams.sessionId()
    const cwd = this.seams.cwd()

    // The chips in the line ARE the payload: group them by block so one
    // frame per block is captured (design §2.2: reference = frame +
    // region; two chips into one block are two references into one
    // frame). The surface clears the chips when a `clear` takes their
    // blocks, so the captured frames always exist.
    const byBlock = new Map<HTMLElement, ReferenceChip[]>()
    for (const chip of this.seams.chips()) {
      const list = byBlock.get(chip.blockEl)
      if (list) list.push(chip)
      else byBlock.set(chip.blockEl, [chip])
    }

    const references: AskParams['references'] = []
    for (const [block, chips] of byBlock) {
      const frame = mintFrozenFrame(frozenFrameSourceFromBlock(block))
      // A frozen frame's rows are text BY CONSTRUCTION (the frozen mint
      // never emits cells — design §2.2). A cells row here means the
      // minting invariant broke; that is a loud failure, never
      // silently-empty text.
      const rows = frame.rows.map((r): { kind: 'text'; text: string } => {
        if (r.kind !== 'text') {
          throw new Error('agent-ask: a frozen frame minted a non-text row')
        }
        return { kind: 'text', text: r.text }
      })
      const capture = await this.seams.dispatcher.call<AgentCaptureFrame>('agent.captureFrame', {
        captureId: crypto.randomUUID(),
        sessionId,
        source: 'frozen',
        rows,
        serializerVersion: SERIALIZER_VERSION,
        cwd,
      } satisfies FrozenCaptureParams)
      for (const chip of chips) {
        // Clamped defensively: the block was frozen when the chip was
        // raised, but a re-freeze must never mint an out-of-bounds region
        // (the backend refuses it wholesale). A clamp that collapses to an
        // empty region (the block shrank past the chip) drops the chip
        // rather than failing the whole ask.
        const rowStart = Math.max(0, Math.min(chip.rowStart, rows.length))
        const rowEnd = Math.max(0, Math.min(chip.rowEnd, rows.length))
        if (rowEnd <= rowStart) continue
        references.push({ frameId: capture.frameId, region: { rowStart, rowEnd } })
      }
    }

    const askParams: AskParams = {
      askId: crypto.randomUUID(),
      sessionId,
      question: doc,
      cwd,
      references,
    }

    // The block opens BEFORE the ask resolves: the waiting state must
    // cover the whole submit → first-delta interval, including the RPC
    // that starts the run — a slow or hung ask is exactly the silence a
    // hung command had, and the block says what it is waiting for
    // (nocx-ex636). A refusal removes it: no run, no ledger entry, no
    // block — the refusal itself is the visible surface. The captures
    // above stay ahead of the block: a question whose payload cannot be
    // ingested is refused before any block exists.
    const handle = this.seams.openAnswer(doc, cwd)
    const ask = await this.seams.dispatcher
      .call<AgentAsk>('agent.ask', askParams)
      .catch((err: unknown) => {
        handle.el.remove()
        // A refusal (no endpoint configured) is a renderable condition, not a
        // server fault: the surface says so instead of leaving the editor
        // accepting questions nothing answers.
        const message = err instanceof Error ? err.message : String(err)
        this.seams.onRefusal(message)
        // …and when the reason is a missing endpoint, the surface offers
        // the repair rather than only naming the problem: a person told
        // "no endpoint configured" at the prompt has no way, from there,
        // to find where an endpoint is configured.
        void this.offerEndpointRepair()
        throw err
      })

    // The answer entry id is known once the ask resolves; the run's first
    // delta cannot arrive before it (the run starts inside agent.ask and
    // the response is on the wire before the first notification), so the
    // routing check below never sees a stale undefined id.
    handle.el.dataset.answerEntryId = ask.answerEntryId
    // Which model this run answers with — carried on the ask result and
    // kept on the block, so the terminal close can name it: the person
    // must be able to tell which model answered (nocx-e6kn2). The value is
    // the PINNED run fact, never a re-derivation.
    handle.el.dataset.answeredBy = ask.model
    this.runs.set(ask.runId, handle)
  }

  /** Subscribe once: deltas append to the run's block; the terminal state
   *  closes it. A runState with no prior delta (a failure before any text)
   *  still has a block — the ask result's answerEntryId opened it. */
  private ensureSubscribed(): void {
    if (this.subscribed) return
    this.subscribed = true
    this.seams.dispatcher.subscribe('agent.runDelta', (params: unknown) => {
      const d = params as AgentRunDelta
      const handle = this.runs.get(d.runId)
      if (!handle) return
      // Both ids are on every delta on purpose (design §7): the run id
      // finds the ask, the entry id confirms the deltas land on the right
      // answer block — a mismatch is a stale or misrouted notification and
      // must not append to the wrong block.
      if (handle.el.dataset.answerEntryId !== d.entryId) return
      handle.append(d.text)
    })
    this.seams.dispatcher.subscribe('agent.runState', (params: unknown) => {
      const s = params as AgentRunState
      const handle = this.runs.get(s.runId)
      if (!handle) return
      if (handle.el.dataset.answerEntryId === undefined) {
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
      if (s.state === 'completed' || s.state === 'cancelled') {
        handle.close('success', undefined, handle.el.dataset.answeredBy)
      } else if (s.state === 'failed' || s.state === 'interrupted') {
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

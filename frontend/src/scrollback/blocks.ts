// DOM scrollback block manager.
// Creates, freezes, and manages DOM command blocks in the scrollback area.
// Flat warp-style design (P0-1): no card borders, dividers between blocks,
// subtle background tint on hover/select.

import { serializeRange, serializeRangeSGR, serializeRangeText, fromITheme } from './serializer'
import type { CapturedBody } from '../capture-client'
import { getCurrentTheme } from '../renderers/theme-adapter'
import type { CommandSnapshotStore } from '../command-snapshot'
import type { IBufferLine } from '@xterm/xterm'
import { wordRangeIn } from '../word-selection'
import { createSecretChipUnresolved } from '../ui/secret-chip'
import type { AgentRunToolCall } from '../generated/agent.runToolCall'
import { createReasoningNote, type ReasoningNote } from '../ui/reasoning-note'
import { reasoningStartsExpanded } from '../reasoning-expanded'
import { showToast } from '../ui/toast'
import { clampMenuPosition } from '../ui/menu-geometry'
import { findReferences } from '../secret-reference'
import { commandFragment } from '../command-text'
import { KIND_LABELS, type SecretKind } from '../secret-kind'
import type { ExecutionAttempt } from '../lifecycle/state'
import type { CommandAuthor } from '../command-ledger'
import { createAnswerBody, type AnswerBody } from './answer-body'
import { toolCallTitle } from './tool-call-title'
import { paintShellInto } from './shell-paint'
// ── Clipboard helper ────────────────────────────────────────────────────────

async function copyToClipboardImpl(text: string): Promise<void> {
  if (typeof navigator !== 'undefined' && navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text)
      return
    } catch {
      // Fall through to the browser's legacy copy path.
    }
  }

  const ta = document.createElement('textarea')
  ta.value = text
  ta.style.position = 'fixed'
  ta.style.left = '-9999px'
  document.body.appendChild(ta)
  ta.select()
  try {
    if (!document.execCommand('copy')) throw new Error('clipboard refused')
  } finally {
    document.body.removeChild(ta)
  }
}

function clipboardFallback(text: string): void {
  void copyToClipboardImpl(text).catch(() => {})
}

/** Shared clipboard seam for the kit controls mounted in imperative blocks. */
export function copyToClipboard(text: string): Promise<void> {
  return copyToClipboardImpl(text)
}

// ── Render fence rendezvous (ADR-0024 §7 carve-out, bead nocx-u7uh.8) ──
// The lifecycle channel and the pty are independent streams, so an
// authenticated completion can reach nocx before the command's last output
// bytes do. The shell writes a 32-random-byte nonce (64 hex chars) to the
// pty AFTER the output and carries the same nonce in the `complete` event;
// the block's VISUAL freeze waits for both, while the LOGICAL completion
// (exit status, history) lands on the event alone.

/** How long a completed attempt's VISUAL boundary waits for its fence bytes
 *  before the visual freeze settles at the current output end. The LOGICAL
 *  freeze (status, exit code) lands on the event alone; the fence is printed
 *  by the shell immediately after the output on the same pty channel, so it
 *  lands within the same write burst — this window is generous for a slow
 *  link and only bounds how long a finished command keeps its running look
 *  when the fence never arrives. Named: the deferral is a policy, not a
 *  magic number, and the no-fence path is a degrade, never a truncation. */
export const FENCE_DEFER_MS = 500

/** Upper bound on remembered fence sightings (hex → line). Sightings are
 *  kept only so a completion that lands after its fence can match it; a
 *  crypto-random nonce makes collisions impossible, so a small ring is
 *  more than enough and bounds the memory of a hostile stream. */
const MAX_FENCE_SIGHTINGS = 8

/** Deferral-timer handle — named so the pending-fence contract never
 *  couples to setTimeout's implementation type. */
type FenceTimer = ReturnType<typeof setTimeout>

/** A block status that has left `running` — the terminal set the DOM
 *  freeze and the block record share. The LOGICAL freeze produces it and
 *  hands it to the VISUAL freeze, so serialization is typed to follow a
 *  terminalized record. */
export type FrozenStatus = 'success' | 'failure' | 'entered' | 'unknown'

/** Where a block is, as its HEADER reports it (nocx-hoeq3).
 *
 *  `settled` is the one that had to be named. It means: this block is
 *  finished and it has NO OUTCOME OF ITS OWN — a run of prose, a tool call
 *  announced but not judged here, a restored block whose outcome is stated
 *  elsewhere. It used to be spelled `success`, because nothing read the
 *  value and any settled-looking status would do. That stopped being true
 *  the moment the header derived a terminal chip from the status, which is
 *  what the group's one owner below does: a block spelled `success` would
 *  state an outcome it does not have. */
type HeaderStatus = 'running' | 'waiting' | 'settled' | 'cancelled' | FrozenStatus

/** The statuses a BUILT block can be handed — everything a header knows
 *  except `running`, which belongs to createRunningBlock's element and never
 *  to a block built with its rows already fixed. */
type BuiltStatus = Exclude<HeaderStatus, 'running'>

// ── Block kind ─────────────────────────────────────────────────────────────

/** A block's content grammar (nocx-ex636, ADR-0040). The FRAME — a header, a
 *  body, selection, the overflow menu — is shared by every block; the
 *  grammar is not. A question is prose and a command is a command line, and a
 *  fifth kind must declare itself in the rules table instead of inheriting
 *  the command's rules by accident.
 *
 *  `text` and `tool` are what a TURN is made of (ADR-0040): its children are
 *  the causal sequence, top to bottom, and each one is an ordinary block with
 *  its own id, its own selection and its own place in the order. A `text`
 *  block is one run of assistant prose, and a `tool` block is one call that
 *  opened no block of its own — the command a `run` call opened is not a
 *  third kind, it is a `command`, because that is what it is. */
export type BlockKind = 'command' | 'ask' | 'text' | 'tool'

/** The rules the owner named — highlighting, wrapping, the status
 *  vocabulary — read from ONE table, keyed by the kind the block declared.
 *  No call site checks "is this an answer", and no builder defaults to the
 *  command rules. */
export interface BlockKindRules {
  /** Whether the block draws a HEADER at all.
   *
   *  A run of prose does not, and that is the point of ADR-0040: there is
   *  nothing to name it — the intent was the question — and a header
   *  repeating the question is the `continued` badge this ADR deleted. The
   *  block is still a real block: it has an id, it selects, and it holds a
   *  seat in the turn's order. Only the header is not drawn.
   *
   *  A kind with no header also has no ⋮, because the ⋮ lives in it. */
  readonly header: boolean
  /** The header's text is a command line: shell-highlight it. A question,
   *  and the name of a tool call, are prose and never run through the lexer. */
  readonly highlightHeader: boolean
  /** The class the body element carries — the CSS owner of the wrap
   *  policy: `.cmd-output` freezes rows at the terminal grid width
   *  (nocx-juau), `.cmd-output-ask` wraps prose at the block's width.
   *
   *  NULL for a kind that has no body at all. A tool call is a header and
   *  nothing else: its result has an owner already (the ledger's attempt),
   *  and a body class declared for a body that never exists is a rule
   *  nobody can check. Handing such a block output is a loud failure, not a
   *  silent guess at which class to use. */
  readonly outputClass: string | null
  /** The header's status vocabulary. The command kind has none — its
   *  header states are the record's and render structurally (spinner,
   *  duration, exit chips). The ask kind names its lifecycle with words:
   *  in progress, then the terminal word from the close path. */
  readonly statusChips: {
    /** Shown while the work is in progress — the ask block says it is
     *  thinking until the first delta lands. Kept SHORT: it sits beside
     *  the live pulse, which is what carries "something is happening", so
     *  the word only has to name what the pulse is about. */
    readonly inProgress: string
    readonly done: string
    readonly failed: string
    readonly cancelled: string
  } | null
  /** WHAT THE HEADER'S RIGHT-HAND GROUP HOLDS when the block has settled,
   *  and in what order (nocx-hoeq3).
   *
   *  Here rather than at the call sites because there are two of them and
   *  they were hundreds of lines apart: the builder filled the group for a
   *  command, and the answer flow's close filled it again for a turn. They
   *  agreed on nothing except by accident — the turn's chip was missing the
   *  `-ok`/`-fail` modifier, and the turn had no duration chip at all
   *  because it was built with `durationMs = null` and never given one. The
   *  owner saw the result as two headers whose chips differ in number and
   *  placement, which is what they were.
   *
   *  A kind that declares nothing here does not get the command's group by
   *  default; it fails in blockKindRules like every other rule. */
  readonly headerRight: HeaderRightRules
}

/** The right-hand group's per-kind rules. */
interface HeaderRightRules {
  /** The slots the group holds, in DOM order. A slot renders nothing when
   *  the block has no such fact — no duration known, no outcome here — so
   *  the order is stable whether one chip is drawn or both. */
  readonly chips: readonly HeaderChipSlot[]
  /** The block's outcome as this kind SAYS it, or null when the block has
   *  none of its own: still running, still waiting, or settled with its
   *  outcome stated elsewhere.
   *
   *  The two kinds read different facts on purpose, and that is the whole
   *  of what is per-kind here (nocx-ex636). A command's outcome is the
   *  shell's exit code and its words are the shell's. A turn's outcome is
   *  the run's terminal status and its words are its own — an answer is not
   *  a command's output and does not borrow "ok". The CHIP the two produce
   *  is one chip, built once, below. */
  readonly terminal: (outcome: BlockOutcome) => TerminalChipSpec | null
}

/** One slot in the header's right-hand group. */
type HeaderChipSlot = 'duration' | 'terminal'

/** The facts a settled block's header decides its terminal chip from. */
interface BlockOutcome {
  readonly status: BuiltStatus
  /** The shell's code, and null for everything that is not a shell command
   *  — which is what the store sends for an assistant turn, because the
   *  exit code lives in the shell arm of an entry's payload and a turn has
   *  no shell arm (content.ShellExitCodeOf). */
  readonly exitCode: number | null
}

/** What a terminal chip says: its tone and its word. The tone is the
 *  block's outcome; the word is the kind's vocabulary. */
interface TerminalChipSpec {
  readonly ok: boolean
  readonly text: string
}

/** The ask kind's words, named once so the in-progress chip and the terminal
 *  chip cannot drift apart: they are one vocabulary. */
const ASK_STATUS_CHIPS = {
  inProgress: 'thinking',
  done: 'completed',
  failed: 'failed',
  cancelled: 'stopped',
} as const

const BLOCK_KIND_RULES: Record<BlockKind, BlockKindRules> = {
  command: {
    header: true,
    highlightHeader: true,
    outputClass: 'cmd-output',
    statusChips: null,
    headerRight: {
      chips: ['duration', 'terminal'],
      terminal: ({ status, exitCode }) => {
        // An 'entered' block froze on environment entry (N6): it carries no
        // exit code and must never paint success or failure, whatever code
        // the local D later delivers to the ledger.
        if (status === 'entered' || exitCode === null) return null
        return exitCode === 0 ? { ok: true, text: 'ok' } : { ok: false, text: `exit ${exitCode}` }
      },
    },
  },
  ask: {
    header: true,
    highlightHeader: false,
    outputClass: 'cmd-output cmd-output-ask',
    statusChips: ASK_STATUS_CHIPS,
    headerRight: {
      chips: ['duration', 'terminal'],
      // From the STATUS, never from the exit code. A turn's outcome is the
      // run's, and the store sends no exit code for one; deriving the chip
      // from the code left a restored turn saying nothing at all about
      // whether it finished, while the live one said `completed` from a
      // second construction (nocx-hoeq3).
      terminal: ({ status }) => {
        if (status === 'success') return { ok: true, text: ASK_STATUS_CHIPS.done }
        if (status === 'failure') return { ok: false, text: ASK_STATUS_CHIPS.failed }
        if (status === 'cancelled') return { ok: false, text: ASK_STATUS_CHIPS.cancelled }
        return null
      },
    },
  },
  // One run of assistant prose (ADR-0040). No header — see `header` above —
  // and the same wrapping body a whole answer used to have, because it IS
  // that body: what changed is that a turn now has several of them in order
  // rather than one string with offsets cut into it.
  text: {
    header: false,
    highlightHeader: false,
    outputClass: 'cmd-output cmd-output-ask',
    statusChips: null,
    headerRight: { chips: [], terminal: () => null },
  },
  // One tool call that opened no block of its own (ADR-0040). It is a HEADER
  // and nothing else: the header names the tool and the arguments it ran on,
  // which is what tells two calls of one tool apart, and the call's result
  // has an owner already (the ledger's attempt). The name is prose and never
  // goes through the shell lexer — `blocks.read sessionId=… blockId=…` is
  // not a command line and colouring it as one would say it is.
  //
  // No terminal chip either: the announcement is that the call HAPPENED, and
  // whether it succeeded belongs to the attempt. A chip here would be a
  // second, later-arriving owner of an outcome this surface never receives.
  tool: {
    header: true,
    highlightHeader: false,
    outputClass: null,
    statusChips: null,
    headerRight: { chips: [], terminal: () => null },
  },
}

/** A kind's rules, or a loud failure: a kind that declares nothing must
 *  never inherit the command rules by default (nocx-ex636). */
export function blockKindRules(kind: BlockKind): BlockKindRules {
  const rules = BLOCK_KIND_RULES[kind]
  if (!rules) throw new Error(`unknown block kind: ${String(kind)}`)
  return rules
}
// ── Block model ────────────────────────────────────────────────────────────

/** The handle the ask surface drives one answer block with (nocx-x8s2.2).
 *  The answer is NOT xterm output — it arrives as plain text over the
 *  control plane — so the body is rendered as escaped term-lines (the
 *  flow's one text vocabulary). The handle is the ONLY way the block's
 *  body and status change; the ask surface never touches the block DOM
 *  directly. */
export interface AnswerBlockHandle {
  readonly id: number
  readonly el: HTMLElement
  /** Append one streamed chunk (agent.runDelta text) to the turn's prose.
   *  `this: void` — the target holds the handle and calls the method
   *  detached from any receiver (unbound-method contract).
   *
   *  `blockId` is the `text` child the chunk belongs to — the store's own
   *  block, off agent.runDelta. A chunk naming a DIFFERENT block than the
   *  one being written opens the next run of prose, which is how the turn
   *  gets its several runs; the boundary is the BACKEND's and is never
   *  worked out here (ADR-0040: while the cut was the renderer's, the live
   *  path and the restore each computed it and could drift).
   *
   *  Omitted for a sentence the RENDERER writes into the flow — the dropped
   *  -deltas notice is the only one — which continues whatever run is open
   *  rather than declaring a boundary of its own. */
  append(this: void, text: string, blockId?: string): void
  /** Draw one tool call (agent.runToolCall) as a CHILD of the turn, in the
   *  seat it arrived at (ADR-0040). Never a top-level block: the call
   *  belongs to the turn that made it, and its position among the turn's
   *  children is what says when it happened.
   *
   *  A call that opened a block draws NO child of its own — the block the
   *  command opened is the account of it, and it takes the next seat.
   *
   *  Idempotent per `callId`: the backend announces a call once per
   *  EXECUTION, and an approved egress resume puts the same call through
   *  the pipeline a second time. One call, one child. */
  toolCall(this: void, call: AnswerToolCall): void
  /** Append one chunk of the model's thinking (agent.runReasoning) — into
   *  its own collapsed note, never into the answer text (nocx-s92so). The
   *  note is created at the FIRST chunk, so a model that returns no
   *  reasoning renders nothing at all. */
  reasoning(this: void, text: string): void
  /** Close the block: success, failure with a renderable reason, or the
   *  distinct cancelled outcome shown as "stopped". */
  close(
    this: void,
    status: 'success' | 'failure' | 'cancelled',
    error?: string,
    model?: string,
  ): void
}

/** The effect classes the ledger names (content.Effect), read off the wire
 *  type the schema generates rather than restated here — a second copy of a
 *  closed set is a set that disagrees with the wire the day one of them
 *  grows a member. */
type ToolCallEffect = AgentRunToolCall['effect']

/** One tool call as the turn draws it — the wire's facts
 *  (contracts/agent.runToolCall.schema.json), narrowed to what this surface
 *  needs. Deliberately no result: it has an owner already (the ledger's
 *  attempt, and for the run tool the block the command really opened). */
export interface AnswerToolCall {
  callId: string
  tool: string
  effect: ToolCallEffect
  /** What the model asked for, as the tool's schema validated it. This is
   *  what NAMES the call: the tool and the derived resource are the same for
   *  two calls of one session-scoped tool, and the arguments are what tell
   *  them apart (ADR-0040). */
  args?: Record<string, unknown>
  resource?: { kind: string; id: string }
  /** Whether this call's work becomes a BLOCK of its own — the tool
   *  declaration's fact, off the wire (ADR-0040). True and the turn draws
   *  NO child for the call: the block the command opened is the account of
   *  it and takes the next seat. False and the call's own child is the only
   *  thing that says it occurred.
   *
   *  Never derived here from `tool`: which tools open blocks is a fact of
   *  the tool table (internal/agenttools), and a renderer holding its own
   *  copy would disagree with it the day a tool is added — the same reason
   *  the effect beside it is sent rather than inferred (ADR-0028 decision
   *  4). */
  opensBlock: boolean
}

/** One answer block's bookkeeping (nocx-x8s2.2): the question it answers
 *  and its DOM element. Deliberately NOT a BlockRecord — no xterm lines,
 *  no freeze lifecycle; the command paths must never see it. */
interface AnswerBlockRecord {
  id: number
  question: string
  el: HTMLElement
}

export interface BlockRecord {
  id: number
  command: string
  cwd: string
  /** Who submitted the command — the minted author from the submitting
   *  target (design §3.1, nocx-iadtt), defaulting to the human's shell
   *  for a shell-originated block. The header renders the mark from this;
   *  the freeze path reuses it, so the mark survives the running → frozen
   *  replacement. */
  author: CommandAuthor
  /** Duration in ms: C marker to D marker. */
  durationMs: number | null
  exitCode: number | null
  /** Presentation state. 'entered' = frozen on environment entry (N6):
   *  neither success nor failure, no exit code — the block the ssh command
   *  froze into when the remote session began. 'unknown' = the bound
   *  attempt was abandoned (ADR-0024 §5): frozen, never successful, no
   *  reported exit code. */
  status: 'running' | 'success' | 'failure' | 'entered' | 'unknown'
  /** Run once, after the VISUAL freeze has replaced `el`.
   *
   *  The two freezes are separate moments (u7uh.8): the logical one lands on
   *  the authenticated completion and sets `status` above, while the visual
   *  one waits up to FENCE_DEFER_MS for the fence bytes and REPLACES `el`
   *  when it lands. Between them the block is finished but its element still
   *  reads `cmd-block-running`, and anything written onto that element is
   *  discarded by the replacement.
   *
   *  So a decoration arriving in that window parks here instead of being
   *  applied to an element about to be discarded, or dropped. The receipt is
   *  the case that needed it: the history.record ack raced the fence, was
   *  refused for looking unfinished, and was gone for good — a captured
   *  secret with nothing offering to save it (nocx-ggha). */
  afterVisualFreeze?: () => void
  /** What the VISUAL freeze produced for the store (nocx-2f0f): the block's
   *  rows as SGR and as characters, with the grid the serializer saw.
   *
   *  PARKED HERE rather than sent, because the artifact hangs on an ENTRY
   *  and the entry id arrives with the history.record ack — a different
   *  event that may land before or after this freeze. Whoever sends it
   *  clears the field, so a block cannot be captured twice.
   *
   *  Undefined until the visual freeze runs, and after the capture has been
   *  handed over. */
  captured?: CapturedBody
  /** The authenticated attempt this block is bound to (ADR-0024 §7
   *  projection): set when the running block binds to the published
   *  attempt, kept when the block freezes. Absent only for a block that
   *  never bound (cleared scrollback, never seen running). */
  attemptId?: string
  /** IMarker line for C boundary — the absolute buffer line where the
   *  block was CREATED: the prompt line at app-owned submit, or the cursor
   *  line when a shell-originated attempt's running fact landed. The
   *  published running fact binds to the block by this line's lifetime,
   *  never by its value (ADR-0024 §5 attachment semantics). */
  startLine: number
  /** The absolute buffer line where the block's OUTPUT begins — the first
   *  row serialized at freeze. Differs from `startLine` exactly when the
   *  creation line carries the shell's echo of the command: the app-owned
   *  submit opens the block BEFORE the bytes, and the echo lands on the
   *  creation line, so the output range starts one row later (nocx-4yhi).
   *  The range and the creation time are two different things, and this
   *  is the record of that; a shell-originated block opens after its echo
   *  and defaults to `startLine`. */
  outputStart: number
  /** IMarker line for D boundary (approx). */
  endLine: number
  /** Whether OSC 133 C was received for this command. False when the
   *  block was started from the app-owned submit (nocx-atyf.4). */
  cReceived: boolean
  el: HTMLElement
}

/** Line accessor function — matches xterm's IBufferLine.getLine(). */
export type GetLineFn = (y: number) => IBufferLine | undefined

// ── DOM helpers ────────────────────────────────────────────────────────────

function div(className: string, ...children: (string | HTMLElement)[]): HTMLElement {
  const el = document.createElement('div')
  el.className = className
  for (const c of children) {
    if (typeof c === 'string') {
      el.appendChild(document.createTextNode(c))
    } else {
      el.appendChild(c)
    }
  }
  return el
}

// ── Duration formatters ────────────────────────────────────────────────────

/**
 * The elapsed time of a command that is still running.
 *
 * Whole seconds, unlike the finished-command format. The ticker fires once a
 * second, so a tenths digit could only ever read `.0` — a decimal place that
 * never varies is not precision, it is noise that makes the number wider and
 * harder to read at a glance.
 */
function formatRunningDuration(ms: number): string {
  if (ms < 60000) return `${Math.floor(ms / 1000)}s`
  const min = Math.floor(ms / 60000)
  const sec = Math.floor((ms % 60000) / 1000)
  return `${min}m ${sec}s`
}

function formatDuration(ms: number): string {
  if (ms < 1000) return `${ms.toFixed(0)}ms`
  if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`
  const min = Math.floor(ms / 60000)
  const sec = ((ms % 60000) / 1000).toFixed(0)
  return `${min}m ${sec}s`
}

// ── The header's right-hand group: one owner (nocx-hoeq3) ──────────────────

/** THE construction of a header's duration chip, for every kind and for both
 *  of the states that show one.
 *
 *  A turn takes time and that is as worth knowing as `df` taking 27ms, so it
 *  is drawn with the same chip and the same identity class a command's is —
 *  which is also what makes the two headers line up, since the width floor
 *  lives on `.cmd-header-duration`.
 *
 *  The TEXT is the caller's, because the two formatters are deliberately
 *  different: a running command shows whole seconds (the ticker fires once a
 *  second, so a tenths digit could only read `.0`) and a finished one shows
 *  the precise figure. Two formatters, one chip. */
function durationChip(text: string): HTMLElement {
  const el = document.createElement('span')
  el.className = 'nocx-chip nocx-chip-muted cmd-header-duration'
  el.textContent = text
  return el
}

/** THE construction of a header's TERMINAL chip, for every kind.
 *
 *  There were two. The command's carried `cmd-header-exit-ok`/`-fail` and the
 *  turn's did not, which was invisible only because no stylesheet paints
 *  those modifiers — the two would have disagreed the day one did. The WORD
 *  still comes from the kind (nocx-ex636); the element does not. */
function terminalChip(spec: TerminalChipSpec): HTMLElement {
  const el = document.createElement('span')
  el.className = spec.ok
    ? 'nocx-chip nocx-chip-ok cmd-header-exit cmd-header-exit-ok'
    : 'nocx-chip nocx-chip-fail cmd-header-exit cmd-header-exit-fail'
  el.textContent = spec.text
  return el
}

/**
 * Fill a header's right-hand group with what a SETTLED block of this kind
 * shows, in the order the kind declared (nocx-hoeq3).
 *
 * Called from the two moments a block settles, so there is one answer for
 * both: at BUILD, for a block whose outcome was already known (a frozen
 * command, a restored anything), and at CLOSE, for a turn that was built
 * while it was still being written. Before this the close built its own chip
 * and never a duration, so a turn's header held one chip where a command's
 * held two — the difference in number and placement the owner reported.
 *
 * IDEMPOTENT: the settled chips are cleared first, so settling a header twice
 * re-states the group rather than growing a second copy of it. The ⋮ is not
 * ours — placeHeaderChip keeps it last, whether or not it exists yet.
 */
function settleHeaderRight(
  right: Element,
  kind: BlockKind,
  durationMs: number | null,
  outcome: BlockOutcome,
): void {
  for (const stale of right.querySelectorAll('.cmd-header-duration, .cmd-header-exit')) {
    stale.remove()
  }
  const rules = blockKindRules(kind).headerRight
  for (const slot of rules.chips) {
    if (slot === 'duration') {
      if (durationMs !== null) placeHeaderChip(right, durationChip(formatDuration(durationMs)))
      continue
    }
    const spec = rules.terminal(outcome)
    if (spec) placeHeaderChip(right, terminalChip(spec))
  }
}

// ── CWD display ────────────────────────────────────────────────────────────

function cwdLabel(cwd: string): string {
  const path = cwd.trim().replace(/\/+$/, '') || '~'
  const parts = path.split('/').filter(Boolean)
  if (path === '~' || parts.length === 0) return path
  return parts.slice(-2).join('/')
}

/**
 * Create the header row for a block — flat, warp-style (P0-1).
 * No card background, no pill/chip styling. Plain muted small text.
 * The grammar (highlighting, the status vocabulary) is the kind's
 * (nocx-ex636).
 */
function createHeader(
  kind: BlockKind,
  command: string,
  cwd: string,
  location: string,
  durationMs: number | null,
  exitCode: number | null,
  status: HeaderStatus,
  store: CommandSnapshotStore,
  author: CommandAuthor = 'shell',
): HTMLElement {
  const header = div('cmd-header')
  const rules = blockKindRules(kind)

  // ── Chips row (above command text): cwd left, duration+exit right ──
  const chipsRow = div('cmd-header-chips')

  // Who ran it, when it was not the human (design §3.1, nocx-iadtt): the
  // kit's badge in its info tone — the same "informational provenance"
  // register the secret chip speaks. A human's block carries no mark at
  // all; only a non-human author is worth saying out loud. Never a
  // hand-rolled chip: this is the kit's badge, placed like any other chip.
  if (author !== 'shell') {
    const mark = document.createElement('span')
    mark.className = 'ui-badge'
    mark.dataset.tone = 'info'
    mark.dataset.author = author
    mark.textContent = author
    chipsRow.appendChild(mark)
  }

  // Where the command ran, when it is somewhere other than this machine. Warp
  // puts `user@host` at the head of every block header and it is the attribute
  // ours was missing: a scrollback full of blocks with no host in them reads
  // the same whether you were on your laptop or three hops away (nocx-6w4z).
  if (location) {
    const loc = document.createElement('span')
    loc.className = 'nocx-chip nocx-chip-muted cmd-header-location'
    loc.textContent = location
    chipsRow.appendChild(loc)
  }

  // CWD — standard chip component
  if (cwd) {
    const cwdEl = document.createElement('span')
    cwdEl.className = 'nocx-chip cmd-header-cwd'
    cwdEl.textContent = `📁 ${cwdLabel(cwd)}`
    chipsRow.appendChild(cwdEl)
  }

  // Right: duration + exit status (or spinner while running)
  const right = div('cmd-header-right')

  if (status === 'running') {
    // The elapsed time, ticking. It used to appear only once the command had
    // finished, which is the one moment you no longer need it — the question
    // "how long has this been going" is asked WHILE it is going. Warp shows it
    // live and so does this (nocx-6w4z).
    const spinner = document.createElement('span')
    spinner.className = 'cmd-header-spinner'
    right.appendChild(spinner)
    right.appendChild(durationChip(formatRunningDuration(0)))
  } else if (status === 'waiting') {
    // The kind's own in-progress vocabulary: the ask block says it is
    // thinking until the first delta lands, and the answer
    // lifecycle removes it at exactly that moment (nocx-ex636). The
    // command kind has no in-progress WORD — its running state is the
    // spinner above — so a command handed this status shows nothing.
    if (rules.statusChips) {
      // The SAME pulse a running command's header carries, in the SAME
      // place: a bare dot in the chip row, left of the chip (AD-8 — one
      // owner for "this block is in progress", and one shape for it). A
      // static word is a label; a word beside a live pulse is a report
      // that something is happening right now. It sat INSIDE the chip for
      // one round and read as a different control from the command's,
      // which is two vocabularies for one concept.
      const pulse = document.createElement('span')
      // Its own identity class beside the shared appearance: the pulse is a
      // SIBLING of the chip now, so whoever ends the wait has to be able to
      // find it. Removing only the chip left a dot pulsing next to
      // `completed` — the report half that nobody owned.
      pulse.className = 'cmd-header-spinner cmd-answer-waiting-pulse'
      right.appendChild(pulse)
      const wait = document.createElement('span')
      wait.className = 'nocx-chip nocx-chip-muted cmd-answer-waiting'
      wait.textContent = rules.statusChips.inProgress
      right.appendChild(wait)
    }
  } else {
    // Settled: the group is the kind's, from its one owner. A block whose
    // outcome arrives LATER — a turn, which is written before it ends —
    // settles the same group through the same function at its close.
    settleHeaderRight(right, kind, durationMs, { status, exitCode })
  }

  chipsRow.appendChild(right)
  header.appendChild(chipsRow)
  // ── Header text (below chips) ──────────────────────────────────────
  // The grammar is the kind's (nocx-ex636): a command header carries the
  // same syntactic highlight pass as the live editor (same lexer, same
  // classes — see shell-highlight.ts); a question is prose and renders
  // plain, never through the lexer. A running header stays plain: the
  // command is still being executed, and the static pass is for reading a
  // finished command back. The frozen branch is innerHTML by design, but
  // the pass escapes every byte of the text, so command content can never
  // inject markup.
  const cmdSpan = document.createElement('span')
  cmdSpan.className = 'cmd-header-text'
  if (!rules.highlightHeader) {
    cmdSpan.textContent = command || '(empty)'
  } else {
    const refs = command ? findReferences(command) : []
    if (refs.length > 0) {
      // A vault reference reads as a chip here, exactly as it does in the
      // editor — it is the same fact about the same text, and showing
      // `{{secret:openrouter.ai}}` raw in the block made the block look like
      // a different thing from the line the user typed.
      //
      // Chips and shell highlighting do not compose: the highlighter emits
      // one HTML string for the whole command, and cutting chips into it
      // would mean tokenising the fragments between them, where a quote
      // opened before a reference closes after it. A command carrying a
      // reference therefore renders plain, the way a masked one already does
      // (renderRecordedCommand) — the chip is the emphasis.
      cmdSpan.replaceChildren(commandFragment(command))
    } else if (status === 'running') {
      cmdSpan.textContent = command || '(empty)'
    } else {
      if (command) paintShellInto(cmdSpan, command, store)
      else cmdSpan.textContent = '(empty)'
    }
  }
  header.appendChild(cmdSpan)

  return header
}

/**
 * Returns true when the serialized output HTML is effectively empty.
 */
function isOutputEmpty(html: string): boolean {
  const stripped = html.replace(/<[^>]*>/g, '').replace(/\s/g, '')
  return stripped.length === 0
}

/**
 * A block's output as text, with the line breaks put back.
 *
 * Asked of the BLOCK, because which element holds the output is the
 * block's own fact (nocx-ex636): a command block's output is the
 * `.cmd-output` its builder created, while an answer block's body is
 * appended after the frame — the overflow menu must resolve it from the
 * block at READ time, never hold a builder-time reference that was empty
 * or null. The extraction itself is kind-agnostic: every block's output
 * is `.term-line` rows or plain text.
 *
 * The serializer emits one `<span class="term-line">` per logical line and
 * nothing between them — the line breaks you see are `display: block` in
 * CSS, not characters in the DOM. So `outputEl.textContent` returned the
 * whole block as a single run, and "Copy output" pasted a hundred rows of
 * `top` onto one line (nocx-6w4z).
 *
 * Falls back to `textContent` when there are no line spans, which is what
 * a block with plain text content would give.
 */
export function blockOutputText(blockEl: HTMLElement | null): string {
  if (!blockEl) return ''
  const outputEl = blockEl.querySelector('.cmd-output')
  if (!outputEl) return ''
  const lines = outputEl.querySelectorAll('.term-line')
  if (lines.length === 0) return outputEl.textContent ?? ''
  return Array.from(lines)
    .map((line) => line.textContent ?? '')
    .join('\n')
}

/** The block's command as text, for a human label naming the block (the
 *  ask chip's value — nocx-x8s2.2). After history.record acks, the header
 *  renders the MASKED command and data-recorded-command holds the full
 *  stored text: the label reads the same source the block shows (ADR-0021),
 *  never a second derivation of the line. */
export function blockCommandText(blockEl: HTMLElement): string {
  const recorded = blockEl.getAttribute('data-recorded-command')
  if (recorded) return recorded
  return blockEl.querySelector('.cmd-header-text')?.textContent ?? ''
}

/** Place a chip in a header's right group, and keep the "⋮" last (nocx-kez4m).
 *
 *  The overflow button is appended when the block is BUILT, so a chip added
 *  by a later lifecycle step — the ask kind's terminal word is the only one
 *  today — lands to its RIGHT unless somebody says otherwise. The owner saw
 *  an answer block reading "⋮ failed" above a command block reading
 *  "50ms ok ⋮" and asked why one row runs backwards.
 *
 *  So the rule lives HERE rather than in each caller: chips go left of the
 *  button, whether or not the button exists yet. A kind added later inherits
 *  the order by using this, instead of learning the button's position by
 *  luck. */
function placeHeaderChip(right: Element, chip: Element): void {
  right.insertBefore(chip, right.querySelector('.cmd-overflow-btn'))
}

/**
 * Build the "⋮" overflow menu button + dropdown (P2-9, P1-6 fix).
 * The menu is rendered as a child of document.body with position:fixed
 * so it floats above ALL blocks and scroll containers. Position is
 * calculated from the button's bounding rect. Closes on outside click
 * and Escape key.
 */
/** Fetch the DURABLE text of one answer entry, or null when it is not
 *  stored any more. Injected, never constructed here: this module has no
 *  socket, and the one that does is wired at the composition root. */
export type AnswerTextSource = (entryId: string) => Promise<string | null>

/** What a RUNNING block's ⋮ menu can do about the command in it, beyond
 *  copying its text (nocx-92gfl, nocx-23rph).
 *
 *  Injected, never constructed here, for the same reason `answerText` is:
 *  this module owns block DOM and holds neither an editor nor a socket. Both
 *  entries also exist as a keystroke — ⌘/Ctrl+Enter summons, Ctrl+C
 *  interrupts — and the menu is deliberately a SECOND DOOR to the same
 *  handlers rather than a second implementation: a gesture nobody can see is
 *  a gesture nobody uses, and two implementations of one action are two
 *  behaviours waiting to diverge. */
export interface RunningBlockActions {
  /** Stop a live command through the backend's escalation ladder. */
  stop(): void
  /** Whether time-limited actions still belong to this specific block. */
  isActive(blockEl: HTMLElement): boolean
  /** Whether this block is currently granted to the next question. */
  isGranted?(blockEl: HTMLElement): boolean
  /** Toggle this whole block's grant, independent of liveness. */
  toggleGrant?(blockEl: HTMLElement): void
}

function buildOverflowMenu(
  blockEl: HTMLElement,
  command: string,
  answerText?: AnswerTextSource,
  running?: RunningBlockActions,
): HTMLElement {
  const btn = document.createElement('button')
  btn.className = 'cmd-overflow-btn'
  btn.textContent = '\u22EE' // ⋮ vertical ellipsis
  btn.setAttribute('aria-label', 'Block actions')

  let menu: HTMLElement | null = null
  let closeOnEscape: ((e: KeyboardEvent) => void) | null = null
  let closeOnClick: ((ev: MouseEvent) => void) | null = null

  const closeMenu = () => {
    if (menu) {
      menu.remove()
      menu = null
    }
    if (closeOnEscape) {
      document.removeEventListener('keydown', closeOnEscape)
      closeOnEscape = null
    }
    if (closeOnClick) {
      document.removeEventListener('click', closeOnClick)
      closeOnClick = null
    }
  }
  const onBlockSettled = (): void => {
    closeMenu()
    blockEl.removeEventListener('nocx:block-settled', onBlockSettled)
  }
  blockEl.addEventListener('nocx:block-settled', onBlockSettled)

  btn.addEventListener('click', (e) => {
    e.stopPropagation()
    e.preventDefault()

    // If menu is already open, close it.
    if (menu) {
      closeMenu()
      return
    }

    // Build the dropdown.
    menu = document.createElement('div')
    menu.className = 'cmd-overflow-menu'
    const copyCmd = document.createElement('button')
    copyCmd.className = 'cmd-overflow-menu-item'
    copyCmd.textContent = 'Copy command'
    copyCmd.addEventListener('click', (ev) => {
      ev.stopPropagation()
      // Once history.record acks, the block shows — and therefore copies —
      // the MASKED command: what you see is what went to the store, and the
      // renderer no longer holds the plaintext for that block (ADR-0021,
      // the receipt round's named trade). The full masked text lives in
      // data-recorded-command; the chips in the header are labels.
      const recorded = btn.closest('.cmd-block')?.getAttribute('data-recorded-command')
      clipboardFallback(recorded ?? command)
      closeMenu()
    })

    // WHERE A BLOCK'S OUTPUT COMES FROM, AND WHY THE TWO KINDS DIFFER
    // (nocx-v13pd).
    //
    // A COMMAND block copies what the terminal DREW. The rows in the DOM are
    // the artefact — the serializer put them there from the grid — so
    // scraping them is not a shortcut, it is reading the thing itself.
    //
    // An ANSWER block copies what was RECORDED. Since nocx-swoje the answer
    // flow RENDERS the model's markdown: `# ` becomes a heading and the
    // marker is consumed, `**bold**` becomes weight and the asterisks are
    // gone. The DOM is therefore a rendering of the answer and no longer the
    // answer, and a copy scraped from it would quietly differ from what the
    // model said. The durable text is right there — SubmitAgentAsk writes a
    // text/plain artifact for every answer — and the block already knows its
    // entry id, because the deltas were routed by it.
    //
    // Which makes copying an answer ASYNC, and that has two consequences the
    // menu has to honour: the item says it is working (a control that looks
    // clicked and does nothing reads as broken), and a fetch that comes back
    // empty REFUSES rather than falling back to the painted text. A copy
    // that quietly differs from the record is worse than one that did not
    // happen.
    const isAnswer = () => blockEl.dataset.blockKind === 'ask'

    /** The answer's stored text, or null — retention took it, the store is
     *  unreachable, or this window has no source wired. All three are the
     *  same fact to a person: it is not here. */
    const storedAnswer = async (): Promise<string | null> => {
      const entryId = blockEl.dataset.entryId
      if (!entryId || !answerText) return null
      return answerText(entryId)
    }

    const refuseCopy = (): void => {
      showToast({
        level: 'warning',
        message: 'The stored answer is not available, so nothing was copied.',
      })
    }

    /** Run an async menu action with the item reporting the work, and close
     *  the menu when it settles either way. */
    const whileFetching = async (item: HTMLButtonElement, work: () => Promise<void>) => {
      item.disabled = true
      item.dataset.busy = ''
      item.textContent = 'Copying…'
      try {
        await work()
      } finally {
        closeMenu()
      }
    }

    const copyOut = document.createElement('button')
    copyOut.className = 'cmd-overflow-menu-item'
    copyOut.textContent = 'Copy output'
    copyOut.addEventListener('click', (ev) => {
      ev.stopPropagation()
      if (!isAnswer()) {
        // The copyable text is asked of the BLOCK at read time (nocx-ex636):
        // an answer block's body is appended after the frame, so the
        // builder-time output reference is null — the block knows where its
        // output lives.
        clipboardFallback(blockOutputText(blockEl))
        closeMenu()
        return
      }
      void whileFetching(copyOut, async () => {
        const stored = await storedAnswer()
        if (stored === null) refuseCopy()
        else clipboardFallback(stored)
      })
    })
    const copyAll = document.createElement('button')
    copyAll.className = 'cmd-overflow-menu-item'
    copyAll.textContent = 'Copy all'
    copyAll.addEventListener('click', (ev) => {
      ev.stopPropagation()
      const intent = () =>
        btn.closest('.cmd-block')?.getAttribute('data-recorded-command') ?? command
      if (!isAnswer()) {
        clipboardFallback(`${intent()}\n${blockOutputText(blockEl)}`)
        closeMenu()
        return
      }
      // The same source as Copy output, deliberately: two items on one block
      // reading one thing from two places is how they start to disagree.
      void whileFetching(copyAll, async () => {
        const stored = await storedAnswer()
        if (stored === null) refuseCopy()
        else clipboardFallback(`${intent()}\n${stored}`)
      })
    })

    const isActive = running?.isActive(blockEl) ?? false
    if (running?.toggleGrant) {
      const grant = document.createElement('button')
      grant.className = 'cmd-overflow-menu-item'
      grant.dataset.action = 'grant'
      grant.textContent = running.isGranted?.(blockEl) ? 'unmark' : 'ask about this block'
      grant.addEventListener('click', (ev) => {
        ev.stopPropagation()
        running.toggleGrant?.(blockEl)
        closeMenu()
      })
      menu.appendChild(grant)
    }
    // Wrap is a per-block override of the kind's default, and it lives here
    // rather than as a control on the block because it is rare: the kind is
    // right nearly always (a command's grid must not re-wrap — nocx-juau —
    // and an answer's prose must). What it is for is the exception the kind
    // cannot know about: one wide table in otherwise ordinary output, or one
    // answer a person wants to read as it came. The override is the DOM
    // state `data-wrap` on the block, so the CSS reads one attribute and the
    // kind's own rule stays the default underneath it.
    //
    // The label names the EFFECTIVE state, not the attribute: with the
    // `terminal.wrapOutput` setting deciding untouched blocks, a block that
    // is already wrapping carries no attribute at all, and a menu offering
    // to "Wrap lines" on a wrapped block is a control you have to try in
    // order to understand. So the attribute answers when it is there, and
    // the rendered style answers when it is not — one question, asked of
    // whoever actually decided it.
    const wrapOn = (): boolean => {
      const attr = blockEl.getAttribute('data-wrap')
      if (attr === 'on') return true
      if (attr === 'off') return false
      const out = blockEl.querySelector<HTMLElement>('.cmd-output')
      return out ? getComputedStyle(out).whiteSpace.startsWith('pre-wrap') : false
    }
    const wrapItem = document.createElement('button')
    wrapItem.className = 'cmd-overflow-menu-item'
    wrapItem.textContent = wrapOn() ? 'Do not wrap' : 'Wrap lines'
    wrapItem.addEventListener('click', (ev) => {
      ev.stopPropagation()
      blockEl.setAttribute('data-wrap', wrapOn() ? 'off' : 'on')
      closeMenu()
    })

    // Stopping remains time-limited; granting the whole block does not.
    // Stopping is the only liveness-bound action; granting is not.
    if (running && isActive) {
      const stop = document.createElement('button')
      stop.className = 'cmd-overflow-menu-item'
      stop.dataset.action = 'stop'
      stop.textContent = 'Stop'
      stop.addEventListener('click', (ev) => {
        ev.stopPropagation()
        if (!running.isActive(blockEl)) {
          closeMenu()
          return
        }
        closeMenu()
        running.stop()
      })
      menu.append(stop)
    }
    menu.append(copyCmd, copyOut, copyAll, wrapItem)
    // Render at body level so it floats above all scroll containers (P1-6).
    document.body.appendChild(menu)

    // Position relative to the button using fixed coordinates — clamped by
    // the SAME geometry the kit's ContextMenu clamps through
    // (ui/menu-geometry.ts, nocx-vnirv.2). This is not a second clamp: a
    // running block sits at the bottom of the scrollback by construction, so
    // an unclamped menu opens past the window's bottom edge and "Ask about
    // this command" and "Stop" — the two items that exist ONLY while it runs
    // — are off-screen. Measured AFTER the menu is in the DOM, because the
    // clamp needs the laid-out size to keep the whole shell inside the
    // viewport. A menu taller than the viewport still fits: the shell's
    // `max-height` + `overflow-y` (style.css) scrolls within the menu.
    // TAKEN OUT OF FLOW BEFORE IT IS MEASURED, which is the whole of this
    // ordering and is not a tidy-up. A plain div appended to `body` is an
    // in-flow block box: it is as wide as the body, so measuring it there
    // reports the WINDOW's width as the menu's. `btnRect.right - width` then
    // goes negative and the clamp does exactly what it is asked to — pins
    // the menu against the left edge of the screen, nowhere near the ⋮ that
    // opened it (owner, 2026-08-24). Fixed positioning with no `left`/`top`
    // yet shrinks the box to its content, which is the size the clamp needs.
    menu.style.position = 'fixed'
    const btnRect = btn.getBoundingClientRect()
    const menuRect = menu.getBoundingClientRect()
    // Freeze the measured shell dimensions before assigning its final
    // coordinates. This keeps the clamp calculation stable in browsers whose
    // fixed-position box reports a different static rect after placement.
    menu.style.width = `${menuRect.width}px`
    menu.style.height = `${menuRect.height}px`
    // Right-aligned to the button, exactly where the fixed `right` it
    // replaces put it.
    const { left, top } = clampMenuPosition(
      { x: btnRect.right - menuRect.width, y: btnRect.bottom + 2 },
      { width: menuRect.width, height: menuRect.height },
      { width: window.innerWidth, height: window.innerHeight },
    )
    menu.style.left = `${left}px`
    menu.style.top = `${top}px`

    // Close on outside click (after this event finishes).
    closeOnClick = (ev: MouseEvent) => {
      if (!menu?.contains(ev.target as Node) && ev.target !== btn) {
        closeMenu()
      }
    }
    setTimeout(() => document.addEventListener('click', closeOnClick!), 0)

    // Close on Escape.
    closeOnEscape = (ev: KeyboardEvent) => {
      if (ev.key === 'Escape') {
        closeMenu()
      }
    }
    document.addEventListener('keydown', closeOnEscape)
  })

  return btn
}

// ── Selection helpers ──────────────────────────────────────────────────────

const SELECTED_CLASS = 'cmd-block-selected'

/**
 * Get the currently selected block's DOM element, if any.
 */
export function getSelectedBlock(container: HTMLElement): HTMLElement | null {
  return container.querySelector(`.${SELECTED_CLASS}`)
}

/**
 * Deselect all blocks inside the container. Returns true if a block was deselected.
 */
export function deselectAllBlocks(container: HTMLElement): boolean {
  const sel = getSelectedBlock(container)
  if (sel) {
    sel.classList.remove(SELECTED_CLASS)
    return true
  }
  return false
}

/**
 * Wire full-block click-to-select (P1-7).
 * Click (mousedown+up without significant movement) selects the block.
 * Drag (mousedown+move) starts text selection and does NOT select the block.
 * @param onSelect callback(id, selected) — notifies the manager of selection changes.
 *
 * THE INNERMOST BLOCK OWNS THE CLICK (ADR-0040). A turn CONTAINS the blocks
 * it caused now, so a click inside a child bubbles through the parent's
 * listener too, and both would claim it — two surfaces owning one input,
 * which AGENTS.md forbids whichever one wins by evaluation order. The nearest
 * `.cmd-block` ancestor of the target is the one that was clicked; every
 * other listener on the way up stands down.
 */
function wireBlockSelection(
  blockEl: HTMLElement,
  container: HTMLElement,
  blockId: number,
  onSelect: (id: number, selected: boolean) => void,
): void {
  let mouseMoved = false

  /** This block is the one the pointer is actually in — not an ancestor of
   *  it, and not the ⋮ or its menu, which own their own clicks. */
  const mine = (e: Event): boolean => {
    const target = e.target as HTMLElement
    if (target.closest('.cmd-overflow-btn, .cmd-overflow-menu')) return false
    return target.closest('.cmd-block') === blockEl
  }

  blockEl.addEventListener('mousedown', (e) => {
    if (!mine(e)) return
    mouseMoved = false
  })

  blockEl.addEventListener('mousemove', () => {
    mouseMoved = true
  })

  blockEl.addEventListener('mouseup', (e) => {
    if (!mine(e)) return
    if (mouseMoved) return

    // Toggle selection: if already selected, deselect; otherwise select
    const currentlySelected = blockEl.classList.contains(SELECTED_CLASS)
    if (currentlySelected) {
      blockEl.classList.remove(SELECTED_CLASS)
      onSelect(blockId, false)
    } else {
      // Deselect others first (single-select P1-8)
      const prev = getSelectedBlock(container)
      if (prev) prev.classList.remove(SELECTED_CLASS)
      blockEl.classList.add(SELECTED_CLASS)
      onSelect(blockId, true)
    }
    mouseMoved = false
  })
}

// ── Block builders ─────────────────────────────────────────────────────────

/**
 * Create a frozen command block DOM element with header + serialized output.
 * `status` 'entered' (N6) is the block the ssh command froze into when the
 * remote session began: painted as neither success nor failure, no exit code.
 * The block DECLARES its kind (nocx-ex636); the rendering rules —
 * highlighting, wrapping, the status vocabulary — are read from it.
 */
export function createCommandBlock(
  kind: BlockKind,
  id: number,
  command: string,
  cwd: string,
  location: string,
  outputHtml: string,
  durationMs: number | null,
  exitCode: number | null,
  status: BuiltStatus,
  getContainer: () => HTMLElement,
  onSelect: (id: number, selected: boolean) => void,
  store: CommandSnapshotStore,
  // REQUIRED, and deliberately not defaulted (nocx-4em1z). Who wrote a
  // block is a fact every caller holds and none may shrug off: the restore
  // path defaulted it by omission and every restored tab forgot that the
  // assistant had run the command. This is the shape that hid the close
  // wrapper's dropped model too — an arity the type system was happy to
  // accept. A call site that forgets it must not compile.
  author: CommandAuthor,
  answerText?: AnswerTextSource,
  menuActions?: RunningBlockActions,
  /** Durable ledger identity, when this block already has one. Renderer
   *  selection ids remain internal and never cross this DOM seam. */
  entryId?: string,
): HTMLElement {
  const wrapper = document.createElement('div')
  wrapper.className = 'cmd-block'
  // The block declares its kind once, in the DOM a person's tools can see:
  // the flow can tell a question from a command without reading the text
  // (nocx-ex636).
  wrapper.dataset.blockKind = kind
  const rules = blockKindRules(kind)
  // The entered block's own visual state (N6): frozen on environment entry,
  // neither success nor failure. The hook a stylesheet styles; the header
  // itself already refuses to paint an exit code or a failure for it.
  if (status === 'entered') wrapper.classList.add('cmd-block-entered')
  // A command carrying a vault reference renders its references as chips,
  // so the header's own text no longer spells the command. Copy reads the
  // full text from here — the reference intact, which is what the user
  // typed, what the store keeps, and what pastes usefully onto another
  // machine. renderRecordedCommand overwrites it with the masked text when
  // the ack lands, which is the same rule one step later.
  if (command && findReferences(command).length > 0) wrapper.dataset.recordedCommand = command
  if (entryId) wrapper.dataset.entryId = entryId

  let outputEl: HTMLElement | null = null
  if (outputHtml && !isOutputEmpty(outputHtml)) {
    if (!rules.outputClass) {
      // A kind that declares no body may not be given one. Loud, like
      // blockKindRules itself: silently choosing a class here would give the
      // block a wrap policy nobody declared for it (nocx-ex636).
      throw new Error(`block kind ${kind} has no body, but output was given`)
    }
    outputEl = document.createElement('div')
    outputEl.className = rules.outputClass
    outputEl.innerHTML = outputHtml
  }

  // A kind that draws no header draws no ⋮ either — the button lives in the
  // header — and a run of prose is the one that does not (ADR-0040): there
  // is nothing to name it, because the intent was the question.
  if (rules.header) {
    const header = createHeader(
      kind,
      command,
      cwd,
      location,
      durationMs,
      exitCode,
      status,
      store,
      author,
    )
    // Overflow menu (P2-9) — always the LAST element of the header-right
    // group (owner directive: ⋮ never shifts position). It reads the block's
    // copyable text from the BLOCK, at click time (nocx-ex636).
    const overflow = buildOverflowMenu(wrapper, command, answerText, menuActions)
    const right = header.querySelector('.cmd-header-right')
    if (right) right.appendChild(overflow)
    wrapper.appendChild(header)
  }
  if (outputEl) wrapper.appendChild(outputEl)

  // Full-block click-to-select with drag distinction (P1-7, P1-8).
  wireBlockSelection(wrapper, getContainer(), id, onSelect)

  // Double-click selects a whole token the way xterm does it (nocx-w7h.11,
  // spec v9 §2): xterm's SelectionService.handleMouseDown calls
  // preventDefault() FIRST — "Tell the browser not to start a regular
  // selection" — and only then branches on event.detail, computing the word
  // bounds from its own model and applying the selection once. The frozen
  // block mirrors that ordering. The browser's native word selection would
  // otherwise be created on the SECOND MOUSEDOWN (event.detail === 2),
  // before the dblclick event fires — observed by copy-on-select on mouseup
  // and copied, one word, before any later expansion could run. Intercepting
  // the mousedown means exactly one selection state exists, already correct,
  // and there is no race to order. A single mousedown (detail 1) is not
  // intercepted: drag selection and click-to-select keep working.
  wrapper.addEventListener('mousedown', (e: MouseEvent) => {
    if ((e.target as HTMLElement).closest('.cmd-overflow-btn, .cmd-overflow-menu')) return
    // The innermost block owns the gesture, for the reason selection does:
    // a turn contains the blocks it caused, so the same double-click reaches
    // every ancestor's listener (ADR-0040).
    if ((e.target as HTMLElement).closest('.cmd-block') !== wrapper) return
    if (e.detail !== 2) return
    e.preventDefault()
    const caret = document.caretRangeFromPoint?.(e.clientX, e.clientY)
    if (!caret || caret.startContainer.nodeType !== Node.TEXT_NODE) return
    const line = caret.startContainer.parentElement?.closest<HTMLElement>(
      '.term-line, .cmd-header-text',
    )
    if (!line) return
    const range = wordRangeIn(line, caret.startContainer as Text, caret.startOffset)
    if (!range) return
    const sel = window.getSelection()
    if (!sel) return
    sel.removeAllRanges()
    sel.addRange(range)
  })

  return wrapper
}

/**
 * ONE stand-in for "work is happening here and nothing has been written
 * yet" (nocx-vnirv.1): three dots where the first output line will land.
 * The same markup stands in a TURN's children while the model thinks or a
 * tool runs, and inside the LIVE REGION while a command runs — one class,
 * one builder, one owner (AD-8). A second indicator that merely looked
 * alike would be two owners of one fact, free to drift apart.
 *
 * `ariaLabel` is the caller's word for the work: the ask kind says
 * "thinking" (its statusChips vocabulary); a command has no in-progress
 * word of its own — its header reports with the spinner — so the
 * command's stand-in carries none.
 */
function createWorkingIndicator(ariaLabel?: string): HTMLElement {
  const typing = document.createElement('span')
  typing.className = 'cmd-answer-typing'
  if (ariaLabel) typing.setAttribute('aria-label', ariaLabel)
  for (let i = 0; i < 3; i++) typing.appendChild(document.createElement('i'))
  return typing
}

/**
 * Create a "running" block element — shows a spinner, no output area.
 */
export function createRunningBlock(
  id: number,
  command: string,
  cwd: string,
  location: string,
  getContainer: () => HTMLElement,
  onSelect: (id: number, selected: boolean) => void,
  store: CommandSnapshotStore,
  author: CommandAuthor = 'shell',
  /** What this block's ⋮ menu can do about the command while it runs. Absent
   *  in a bare-bones embedding, and then the menu is exactly what it was. */
  running?: RunningBlockActions,
): HTMLElement {
  const wrapper = document.createElement('div')
  wrapper.className = 'cmd-block cmd-block-running'
  // A running block is a command in flight; it declares the command kind
  // like the block it will freeze into (nocx-ex636).
  wrapper.dataset.blockKind = 'command'
  if (command && findReferences(command).length > 0) wrapper.dataset.recordedCommand = command

  const header = createHeader(
    'command',
    command,
    cwd,
    location,
    null,
    null,
    'running',
    store,
    author,
  )

  // Overflow menu — copying the command, plus what can be done ABOUT the
  // command while it is still running (nocx-92gfl, nocx-23rph).
  // Always the LAST element of header-right (owner directive).
  const overflow = buildOverflowMenu(wrapper, command, undefined, running)
  const right = header.querySelector('.cmd-header-right')
  if (right) right.appendChild(overflow)

  wrapper.appendChild(header)
  wireBlockSelection(wrapper, getContainer(), id, onSelect)

  return wrapper
}

/**
 * Freeze a running block: replace it with a frozen version.
 *
 * `status` is the presentation, never derived from the exit code: 'entered'
 * (N6) freezes on environment entry — neither success nor failure, no exit
 * code — and the old exitCode === null → 'failure' mapping is exactly the
 * bug this must not inherit. The D path passes 'success'/'failure' from the
 * real code; entry passes 'entered' with a null code.
 */
export function freezeBlock(
  el: HTMLElement,
  id: number,
  command: string,
  cwd: string,
  location: string,
  outputHtml: string,
  durationMs: number,
  exitCode: number | null,
  getContainer: () => HTMLElement,
  onSelect: (id: number, selected: boolean) => void,
  store: CommandSnapshotStore,
  status: 'success' | 'failure' | 'entered' | 'unknown',
  author: CommandAuthor = 'shell',
  menuActions?: RunningBlockActions,
  entryId?: string,
): HTMLElement {
  const newEl = createCommandBlock(
    'command',
    id,
    command,
    cwd,
    location,
    outputHtml,
    durationMs,
    exitCode,
    status,
    getContainer,
    onSelect,
    store,
    author,
    undefined,
    menuActions,
    entryId,
  )
  if (el.parentNode) {
    el.parentNode.replaceChild(newEl, el)
  }

  return newEl
}

/**
 * Re-render a frozen block's command line once history.record acks: the
 * MASKED command with an unresolved chip at every redaction span — what
 * you see in the block is what went to the store, and the receipt has
 * something to point at when a row is hovered. The chips carry their
 * redaction span (data-redaction-start/end) so the receipt's hover can
 * emphasise exactly one.
 *
 * Copying the block copies the MASKED text: the full masked command lives
 * in data-recorded-command (the chips in the header are labels, never the
 * stored text), and the overflow menu prefers it over the pre-ack line.
 * This is the round's named trade — after the ack the renderer no longer
 * holds the plaintext for this block, and neither does the clipboard.
 */
export function renderRecordedCommand(
  blockEl: HTMLElement,
  maskedCommand: string,
  redactions: ReadonlyArray<{ kind: SecretKind; start: number; end: number }>,
): void {
  blockEl.dataset.recordedCommand = maskedCommand
  const headerText = blockEl.querySelector<HTMLElement>('.cmd-header-text')
  if (!headerText) return
  // The segments are plain text (no shell highlighting): a mask breaks the
  // token the highlighter would colour anyway, and the chips are the
  // emphasis now. Offsets are UTF-16 units into maskedCommand, clamped so
  const frag = document.createDocumentFragment()
  let pos = 0
  redactions.forEach((r, i) => {
    const from = Math.max(pos, Math.min(r.start, maskedCommand.length))
    const to = Math.max(from, Math.min(r.end, maskedCommand.length))
    if (from > pos) frag.appendChild(document.createTextNode(maskedCommand.slice(pos, from)))
    if (to > from) {
      const chip = createSecretChipUnresolved(KIND_LABELS[r.kind])
      chip.dataset.redactionIndex = String(i)
      chip.dataset.redactionStart = String(r.start)
      chip.dataset.redactionEnd = String(r.end)
      frag.appendChild(chip)
    }
    pos = to
  })
  if (pos < maskedCommand.length) {
    frag.appendChild(document.createTextNode(maskedCommand.slice(pos)))
  }
  headerText.replaceChildren(frag)
}

// ── Block manager ──────────────────────────────────────────────────────────

export interface BlockManagerOpts {
  now?: () => number
  /** The tab's command-existence snapshot store (OSC 636), passed through to
   *  every frozen header this manager creates. */
  snapshotStore: CommandSnapshotStore
  /** Fired when a DEFERRED freeze lands — the fence arrived, or the
   *  FENCE_DEFER_MS window elapsed and the block settled at the current
   *  output end. The freeze originated inside the manager (sightFence /
   *  the deferral timer), so the caller learns to settle the live region. */
  onDeferredFreeze?: () => void
  /** Fired at the end of EVERY visual freeze — the moment the frozen
   *  element replaces the running one and the block's output rows are fixed
   *  in the DOM (nocx-tjppv: the run tool's completion wait reads the
   *  output window from the frozen block, so it must observe this exact
   *  moment, not the logical freeze, which may still be waiting on the
   *  render fence). Fires after afterVisualFreeze, so a waiter that sets
   *  that slot and an observer here never race. */
  onBlockFrozen?: (rec: BlockRecord) => void
  /** The terminal grid, read at freeze time. It is capture PROVENANCE
   *  (ADR-0019 §6): the same rows serialized at a different width are a
   *  different rendering, and a reader that cannot tell has to guess. The
   *  manager holds no renderer, so the caller that does supplies it. */
  dimensions?: () => { cols: number; rows: number }
  /** What a session is called TO A PERSON, for a tool block whose call named
   *  one (nocx-vnzek). The manager holds no pane list, so the caller that
   *  does supplies the tab strip's own derivation
   *  (PaneManager.sessionDisplayName); the paint rule built on it lives in
   *  scrollback/tool-call-title.ts. Absent in a bare-bones embedding, and
   *  then a session simply cannot be named — never rendered as its id. */
  sessionName?: (sessionId: string) => string | null
  /** The durable text of one ANSWER entry, for the copy path (nocx-v13pd).
   *  The manager holds no socket, so the caller that does supplies the
   *  reader (restore-client.answerTextForEntry). Absent in a bare-bones
   *  embedding, and then copying an answer refuses rather than falling back
   *  to the painted text. */
  answerText?: AnswerTextSource
  /** What a RUNNING block's ⋮ menu can do about the command in it
   *  (nocx-92gfl, nocx-23rph). Passed straight to every running block this
   *  manager opens; this manager neither summons nor signals anything.
   *  Absent in a bare-bones embedding, and then the menu is what it was. */
  runningActions?: RunningBlockActions
}

export class BlockManager {
  private _blocks: BlockRecord[] = []
  /** Answer blocks (nocx-x8s2.2): the assistant's streamed replies, kept
   *  OUT of _blocks because they have no xterm line range — the freeze,
   *  serialize and reconstruction paths iterate _blocks and must never see
   *  a record with sentinel lines. They share the id space and the DOM
   *  selection API; the ask surface drives them through AnswerBlockHandle
   *  only. */
  private _answerBlocks: AnswerBlockRecord[] = []
  /** EVERY element this manager has put into `.scrollback-inner`: live
   *  command blocks, answer blocks, the restored past and the boundary that
   *  labels it. The typed lists above exist for what each KIND of block
   *  needs (a freeze range, a question); this exists for the one thing they
   *  share — they are the scrollback, and `clear` empties the scrollback.
   *
   *  So `clearAll` walks this and nothing else. Before it, restored blocks
   *  were inserted straight into the container by the controller, past the
   *  manager, and `clear` left the whole previous session on screen under
   *  its "Previous session" line because `clearAll` had no list that named
   *  it (nocx-0zb1m). A second list to look in would have been the same
   *  defect with a third thing to keep in step. */
  private _owned = new Set<HTMLElement>()
  private _nextId = 1
  private _now: () => number
  private _onBlockFrozen?: (rec: BlockRecord) => void
  private _scrollbackInner: HTMLElement
  private _xtermContainer: HTMLElement
  private _runningBlock: BlockRecord | null = null
  private _cmdStartTime: number | null = null
  /** Currently selected block id, or null if none selected (P1-8). */
  private _selectedBlockId: number | null = null
  private _snapshotStore: CommandSnapshotStore
  private _onDeferredFreeze?: () => void
  private _dimensions?: () => { cols: number; rows: number }
  /** The tab strip's answer to "what is this session called to a person",
   *  handed to every tool block this manager draws (nocx-vnzek). */
  private _sessionName?: (sessionId: string) => string | null
  /** Reader for an answer's durable text — handed to the copy menu of every
   *  answer block this manager frames (nocx-v13pd). */
  private _answerText?: AnswerTextSource
  private _runningActions?: RunningBlockActions
  /** The attempt id the running block is bound to (ADR-0024 §7 projection).
   *  Set when the published running fact binds the block; cleared when the
   *  block freezes or the scrollback is cleared. */
  private _attemptId: string | null = null
  /** Recent fence sightings keyed by hex (the buffer line they landed on),
   *  bounded by MAX_FENCE_SIGHTINGS. A sighting already present is a replay
   *  and is ignored; an entry is consumed when a completion's fence matches.
   *  This is the render-only half of the rendezvous — a fence with no
   *  authenticated event behind it changes nothing (ADR-0024 §1). */
  private _fences = new Map<string, number>()
  /** A completion whose LOGICAL freeze has landed but whose output boundary
   *  (the VISUAL freeze) is still waiting on the render fence: the rows are
   *  serialized when the fence bytes are sighted (hex set), or when the
   *  FENCE_DEFER_MS window settles at the current output end. A completion
   *  that carried no fence at all (hex null — unreachable from the kernel,
   *  which requires the nonce on completed attempts) still defers by the
   *  window rather than truncating at the event-time end: the boundary is
   *  never cut on the event alone. Only the settle path fires
   *  onDeferredFreeze, and only while no newer command owns the running
   *  slot. */
  private _pendingFence: {
    hex: string | null
    /** The block whose boundary is pending — already logically frozen,
     *  still in `_blocks`, never the running block. */
    rec: BlockRecord
    /** The output end at completion time — the fallback boundary when a
     *  newer command owns the cursor and `getEndLine` would serialize
     *  the newer command's output into this block. */
    endLine: number
    /** The terminal status the logical freeze already applied — the
     *  visual freeze hands it to the DOM exactly as the event decided. */
    status: FrozenStatus
    getLine: GetLineFn
    getEndLine: () => number
    timer: FenceTimer
  } | null = null
  /** The fence hex consumed by the last freeze — a replay of it (one seen
   *  for an already-frozen block) does nothing. */
  private _consumedFence: string | null = null
  /** WHERE THE NEXT BLOCK THIS MANAGER OPENS BELONGS (ADR-0040).
   *
   *  A turn's `run` call is announced BEFORE the command is submitted, and
   *  the command is then submitted through the ordinary path — the same
   *  `startBlock` a person's line takes, because it is the same thing
   *  happening. So the turn claims the next seat when the call arrives, and
   *  the block lands in the turn's children instead of at the tail of the
   *  scrollback.
   *
   *  Claimed by exactly one call and released by it: the claim is cleared
   *  the moment it is used, and the turn drops it at its close, so a `run`
   *  that never reached a command cannot adopt an unrelated block later. */
  private _claimedBy: HTMLElement | null = null

  constructor(scrollbackInner: HTMLElement, xtermContainer: HTMLElement, opts: BlockManagerOpts) {
    this._scrollbackInner = scrollbackInner
    this._xtermContainer = xtermContainer
    this._now = opts.now ?? (() => performance.now())
    this._snapshotStore = opts.snapshotStore
    this._onDeferredFreeze = opts.onDeferredFreeze
    this._onBlockFrozen = opts.onBlockFrozen
    this._dimensions = opts.dimensions
    this._sessionName = opts.sessionName
    this._answerText = opts.answerText
    this._runningActions = opts.runningActions
  }

  /** THE ONE DOOR into `.scrollback-inner`. Everything this manager shows
   *  goes through here and is remembered, so a new kind of child cannot be
   *  added without `clearAll` already knowing how to take it away.
   *
   */
  private _own(el: HTMLElement, before: ChildNode | null): void {
    this._scrollbackInner.insertBefore(el, before)
    this._owned.add(el)
  }

  /** Put a COMMAND block in the next seat: inside the turn that claimed it
   *  (ADR-0040), or — nobody claimed it — at the tail of the scrollback,
   *  where every block a person opens goes.
   *
   *  A nested block is still remembered by `_owned`: `clearAll` walks one
   *  list, and removing an element that has already gone with its parent
   *  does nothing (nocx-0zb1m — a second list to look in would be the same
   *  defect with a third thing to keep in step). */
  private _ownNext(el: HTMLElement): void {
    const claim = this._claimedBy
    this._claimedBy = null
    if (!claim) {
      this._own(el, this._xtermContainer)
      return
    }
    claim.appendChild(el)
    this._owned.add(el)
    // A turn's working stand-in marks where the answer will continue: a
    // block that lands inside the turn's children goes ABOVE it, and the
    // stand-in returns to the tail — the next output's position
    // (nocx-vnirv.1).
    const indicator = claim.querySelector(':scope > .cmd-answer-typing')
    if (indicator) claim.appendChild(indicator)
  }

  /** The visual freeze REPLACES a block's element (`freezeBlock` swaps it in
   *  the DOM), so ownership moves with it. Without this the set would hold a
   *  detached element and miss the attached one — the exact shape of the
   *  defect this ownership was written to close. */
  private _reown(oldEl: HTMLElement, newEl: HTMLElement): void {
    this._owned.delete(oldEl)
    this._owned.add(newEl)
  }
  /** The "working, nothing written yet" stand-in for the RUNNING command,
   *  inside the live region, where the first output line will be written
   *  (nocx-vnirv.1). ONE element, idempotently rebuilt: a new command
   *  replaces the previous command's stand-in. */
  private _showCommandIndicator(): void {
    this._clearCommandIndicator()
    this._xtermContainer.appendChild(createWorkingIndicator())
  }

  private _clearCommandIndicator(): void {
    this._xtermContainer.querySelector(':scope > .cmd-answer-typing')?.remove()
  }

  /** THE SEAM for "the first byte of the command's output arrived"
   *  (nocx-vnirv.1): the renderer's writeParsed path calls this on the
   *  first parsed write after the block opens, and the stand-in stands
   *  down. Idempotent — every later chunk calls it again and nothing
   *  changes. The call site lives in terminal-content.ts (renderer
   *  onWriteParsed, beside scheduleLiveResize), which another worker owns;
   *  this is the seam the coordinator wires. */
  noteCommandOutput(): void {
    this._clearCommandIndicator()
  }

  /**
   * Draw blocks the STORE holds ABOVE everything the live session has, and
   * mark where the past ends (nocx-m3fqk).
   *
   * Inserted before the first child rather than appended, so restored blocks
   * keep the order they are given and a session that has already printed
   * something does not find its past underneath its present.
   *
   * The boundary is an element of its own rather than a class on the last
   * restored block: ADR-0019 §3 asks for the difference to be VISIBLE, and a
   * line saying where the previous session ended is what a person reads — a
   * block that merely looks a little different is not an answer to "is this
   * shell still running".
   *
   * It lives HERE, beside the live blocks, because one container may have
   * only one owner: the caller builds the elements, the manager is what puts
   * them on screen and what takes them off again (nocx-0zb1m). The caller
   * keeps the scroll decision, which is about the view and not about the
   * blocks.
   */
  restorePast(blocks: HTMLElement[]): void {
    if (blocks.length === 0) return
    const anchor = this._scrollbackInner.firstChild
    for (const el of blocks) this._own(el, anchor)
    const boundary = document.createElement('div')
    boundary.className = 'scrollback-restore-boundary'
    boundary.dataset.restoreBoundary = 'true'
    boundary.textContent = 'Previous session'
    this._own(boundary, anchor)
  }

  /** An id for a block this manager did not create: a RESTORED one, built
   *  from the store and handed back to `restorePast` (nocx-m3fqk).
   *
   *  The number is renderer-internal selection state. Restored blocks expose
   *  their durable ledger entry id separately as `data-entry-id`; keeping the
   *  same counter here only prevents selection collisions inside the renderer.
   */
  nextRestoredId(): number {
    return this._nextId++
  }

  get blocks(): readonly BlockRecord[] {
    return this._blocks
  }

  get runningBlock(): BlockRecord | null {
    return this._runningBlock
  }

  /** A completed attempt whose DOM output boundary still awaits its fence. */
  get visualFreezePending(): boolean {
    return this._pendingFence !== null
  }

  get cmdStartTime(): number | null {
    return this._cmdStartTime
  }

  /** The currently selected block id, or null (P1-8). */
  get selectedBlockId(): number | null {
    return this._selectedBlockId
  }

  /** Lazy container supplier bound to this manager's scrollback inner. */
  private _getContainer = (): HTMLElement => this._scrollbackInner

  /**
   * Deselect the currently selected block without clearing the block list.
   * Safe to call from keyboard handlers (P0-4: Escape deselects).
   */
  deselectAll(): void {
    if (this._selectedBlockId !== null) {
      const el = this._scrollbackInner.querySelector('.cmd-block-selected')
      if (el) el.classList.remove('cmd-block-selected')
      this._selectedBlockId = null
    }
  }

  /** Programmatic single-select, NON-toggle (the ask affordance's visual
   *  anchor — nocx-x8s2.2). The mouse path owns toggling; activation
   *  selects so the block the chip names reads as selected, but selection
   *  NEVER activates (AD-8: selection is copy). The single-select
   *  invariant (P1-8) holds: the id and the DOM class move together. */
  selectBlock(blockEl: HTMLElement): void {
    const prev = getSelectedBlock(this._scrollbackInner)
    if (prev && prev !== blockEl) prev.classList.remove(SELECTED_CLASS)
    if (!blockEl.classList.contains(SELECTED_CLASS)) blockEl.classList.add(SELECTED_CLASS)
    const rec = this._blocks.find((b) => b.el === blockEl)
    this._selectedBlockId = rec?.id ?? null
  }

  /**
   * Called by wireBlockSelection when a block's selection state changes.
   * Keeps _selectedBlockId in sync with single-select semantics (P1-8).
   */
  _onBlockSelected(blockId: number): void {
    if (this._selectedBlockId === blockId) {
      // Clicking the already-selected block deselects it
      this._selectedBlockId = null
      return
    }
    // Deselect previous
    if (this._selectedBlockId !== null) {
      for (const b of this._blocks) {
        if (b.id === this._selectedBlockId) {
          b.el.classList.remove('cmd-block-selected')
        }
      }
    }
    this._selectedBlockId = blockId
  }

  /**
   * Called by wireBlockSelection when a block is deselected.
   */
  _onBlockDeselected(blockId: number): void {
    if (this._selectedBlockId === blockId) {
      this._selectedBlockId = null
    }
  }
  /**
   * Bind the running block to an authenticated attempt (ADR-0024 §7
   *  projection): the block opened at app submit binds when the published
   *  running fact arrives, and the freeze/abandon paths require the match.
   */
  bindAttempt(attemptId: string): void {
    this._attemptId = attemptId
    if (this._runningBlock) {
      this._runningBlock.attemptId = attemptId
      this._runningBlock.el.dataset.entryId = attemptId
    }
  }

  /** The block bound to an attempt id — running or frozen. */
  blockForAttempt(attemptId: string): BlockRecord | null {
    return this._blocks.find((b) => b.attemptId === attemptId) ?? null
  }

  /**
   * Start a new running block. Called on OSC 133 C.
   */
  /** Where this session is — `user@host`, or empty for a local shell. */
  private _location = ''

  setLocation(location: string): void {
    this._location = location
  }

  startBlock(
    command: string,
    cwd: string,
    startLine: number,
    outputStart = startLine,
    /** Who submitted the command (design §3.1, nocx-iadtt): the app-owned
     *  submit passes the minted author; a shell-originated block is the
     *  human's shell and defaults to 'shell'. */
    author: CommandAuthor = 'shell',
  ): BlockRecord {
    if (this._runningBlock) {
      this._finalizeRunningUnsafe()
    }

    const id = this._nextId++
    this._cmdStartTime = this._now()

    const el = createRunningBlock(
      id,
      command,
      cwd,
      this._location,
      this._getContainer,
      (bid, sel) => {
        if (sel) this._onBlockSelected(bid)
        else this._onBlockDeselected(bid)
      },
      this._snapshotStore,
      author,
      this._runningActions,
    )
    this._ownNext(el)
    // The running command's BODY is the live region — a sibling of the
    // blocks, not a child of this one — so the "working, nothing written
    // yet" stand-in stands THERE, where the first output line will be
    // written (nocx-vnirv.1). The height constraint holds by construction:
    // the stand-in is absolutely positioned inside the live container
    // (style.css, .xterm-live-container.live-running > .cmd-answer-typing),
    // so it takes no flow height, and the box the controller measures and
    // sizes is exactly the box the frozen body replaces — nothing moves at
    // the swap. Removed by the first byte of output (noteCommandOutput) or
    // by the block's terminal freeze.
    this._showCommandIndicator()

    const rec: BlockRecord = {
      id,
      command,
      cwd,
      author,
      durationMs: null,
      exitCode: null,
      status: 'running',
      startLine,
      // The output range and the creation line are two different things
      // (nocx-4yhi): the app-owned submit opens the block before the bytes
      // and passes outputStart = startLine + 1, because the shell's echo
      // of the command lands on the creation line and the block's body
      // must not repeat the command its header already shows. A
      // shell-originated block opens after its echo and keeps the default.
      outputStart,
      endLine: startLine,
      cReceived: false,
      el,
    }
    this._blocks.push(rec)
    this._runningBlock = rec
    this._startTicker(el)

    return rec
  }

  /**
   * Tick the running block's duration chip once a second.
   *
   * One timer for the one running block, cleared the moment it stops running —
   * there is never more than one, so this cannot accumulate the way a per-block
   * timer would.
   */
  private _ticker: ReturnType<typeof setInterval> | null = null

  private _startTicker(el: HTMLElement): void {
    this._stopTicker()
    const chip = el.querySelector('.cmd-header-duration')
    const started = this._cmdStartTime
    if (!chip || started === null) return
    this._ticker = setInterval(() => {
      chip.textContent = formatRunningDuration(this._now() - started)
    }, 1000)
  }

  private _stopTicker(): void {
    if (this._ticker === null) return
    clearInterval(this._ticker)
    this._ticker = null
  }
  freezeBlock(getLine: GetLineFn, endLine: number, exitCode: number | null): BlockRecord | null {
    const rec = this._runningBlock
    if (!rec) return null
    const status = this._logicalFreeze(rec, exitCode, exitCode === 0 ? 'success' : 'failure')
    this._freezeVisual(rec, getLine, endLine, status)
    return rec
  }

  /**
   * Freeze the running block on environment entry (N6): the ssh block freezes
   * with NO exit code, painted as neither success nor failure, and the
   * manager's running slot is freed for the remote commands that follow. The
   * model-level completion (history.record) happens later, at the local D,
   * via the ledger's completeTransition — this only paints the block.
   */
  freezeEntered(getLine: GetLineFn, endLine: number): BlockRecord | null {
    const rec = this._runningBlock
    if (!rec) return null
    const status = this._logicalFreeze(rec, null, 'entered')
    this._freezeVisual(rec, getLine, endLine, status)
    return rec
  }

  /** The LOGICAL freeze (u7uh.8): flip the block's record to its terminal
   *  state — status, exit code and duration land on the authenticated event
   *  alone; the running slot is freed and the ticker stops. The DOM is
   *  untouched: which rows belong to the block is the VISUAL freeze's
   *  question, and it waits for the render fence or the deferral window. */
  private _logicalFreeze(
    rec: BlockRecord,
    exitCode: number | null,
    status: FrozenStatus,
  ): FrozenStatus {
    this._stopTicker()
    // The running command's stand-in stands down with the command itself:
    // a terminal freeze is a close, and no dots may type a command that
    // will not write any more (nocx-vnirv.1).
    this._clearCommandIndicator()
    rec.durationMs = this._cmdStartTime !== null ? this._now() - this._cmdStartTime : null
    this._cmdStartTime = null
    rec.exitCode = exitCode
    rec.status = status
    this._runningBlock = null
    return status
  }

  /** The VISUAL freeze: serialize the block's output region up to a boundary
   *  line and replace its running element with the frozen one. The boundary
   *  is the render fence's line when it was sighted, or the current output
   *  end when the deferral window settles; until this runs the block's rows
   *  are not yet fixed. */
  private _freezeVisual(
    rec: BlockRecord,
    getLine: GetLineFn,
    endLine: number,
    status: FrozenStatus,
  ): void {
    rec.endLine = endLine
    const snapshot = fromITheme(getCurrentTheme())
    const outputHtml = serializeRange(snapshot, getLine, rec.outputStart, endLine)
    // The DURABLE bodies, from the same rows and the same walk the frozen
    // block on screen is made of — so what comes back after a restart is
    // what was there, not a second reading of the buffer taken later.
    const dims = this._dimensions?.()
    if (dims) {
      rec.captured = {
        sgr: serializeRangeSGR(getLine, rec.outputStart, endLine),
        text: serializeRangeText(getLine, rec.outputStart, endLine),
        cols: dims.cols,
        rows: dims.rows,
      }
    }

    const newEl = freezeBlock(
      rec.el,
      rec.id,
      rec.command,
      rec.cwd,
      this._location,
      outputHtml,
      rec.durationMs ?? 0,
      rec.exitCode,
      this._getContainer,
      (bid, sel) => {
        if (sel) this._onBlockSelected(bid)
        else this._onBlockDeselected(bid)
      },
      this._snapshotStore,
      status,
      rec.author,
      this._runningActions,
      rec.attemptId,
    )

    this._reown(rec.el, newEl)
    rec.el = newEl
    // Anything that wanted to decorate this block had to wait for THIS
    // moment, because the line above threw the running element away. One
    // shot, cleared before it runs so a callback that re-enters cannot loop.
    const after = rec.afterVisualFreeze
    if (after !== undefined) {
      rec.afterVisualFreeze = undefined
      after()
    }
    // The visual freeze is complete: the frozen element is in the DOM with
    // its output rows fixed. Observers (the run tool's completion wait,
    // nocx-tjppv) read the block's output window from THIS element. Fires
    // after afterVisualFreeze, so a waiter that sets that slot and an
    // observer here never race.
    this._onBlockFrozen?.(rec)
  }

  /** Freeze the block bound to the attempt, from the attempt's authenticated
   *  completion (ADR-0024 §5, §7). Guards itself: only a COMPLETED attempt
   *  may freeze a block as success/failure, and only the block bound to that
   *  attempt — the kernel derivation freezeBlock() is the authority, and
   *  this keeps the DOM operation honest if a caller bypasses it.
   *
   *  Render fence (u7uh.8): the LOGICAL freeze — status, exit code,
   *  duration, freeing the running slot — lands on the authenticated event
   *  ALONE; the ledger and history have already landed (the projection
   *  order guarantees it). Only the VISUAL freeze — which rows belong to
   *  the block — waits for the fence bytes: when the fence was already
   *  sighted, this serializes at its line and returns the record; otherwise
   *  it defers (returns null) and `sightFence` resolves the boundary, or
   *  the FENCE_DEFER_MS window settles it at the current output end. The
   *  caller keeps the live region up while the boundary is pending, so the
   *  in-flight tail renders live instead of vanishing; `getEndLine`
   *  supplies the fresh output end for the no-fence settle. */
  freezeFromAttempt(
    attempt: ExecutionAttempt,
    getLine: GetLineFn,
    endLine: number,
    getEndLine: () => number,
  ): BlockRecord | null {
    if (attempt.state !== 'completed') return null
    if (this._attemptId !== attempt.id) return null
    const code = attempt.exitCode ?? null
    const status = code === 0 ? 'success' : 'failure'
    const fence = attempt.fence
    const sighted = fence !== undefined ? this._fences.get(fence) : undefined
    const rec = this._runningBlock
    if (!rec) return null

    if (this._pendingFence !== null) {
      // Another completion wants the slot while one is pending. The pty
      // order means the older fence should have landed already; if it has
      // not, settle the older block at its completion-time end (never at
      // the newer command's cursor) rather than stranding it, then defer
      // this completion the same way. The newer block is still running
      // here, so the settle does not touch the live region.
      this._settlePendingFence()
    }

    // LOGICAL freeze — the authenticated event alone flips the block's
    // status, exit code and duration and frees the running slot.
    const terminal = this._logicalFreeze(rec, code, status)
    this._attemptId = null

    if (fence !== undefined && sighted !== undefined) {
      // Rendezvous complete: the fence bytes landed before the completion.
      // Its line IS the output end — serialize now, boundary included.
      this._fences.delete(fence)
      this._consumedFence = fence
      this._freezeVisual(rec, getLine, sighted, terminal)
      return rec
    }

    // The fence bytes are still in flight — or the completion carried no
    // fence at all (hex null; unreachable from the kernel, which requires
    // the nonce on completed attempts). Either way the visual freeze
    // defers: a sighting resolves a non-null fence, and the FENCE_DEFER_MS
    // window settles both at the current output end. The boundary is never
    // cut on the event alone. Null tells the caller the live region stays
    // up until the boundary settles.
    this._pendingFence = {
      hex: fence ?? null,
      rec,
      endLine,
      status: terminal,
      getLine,
      getEndLine,
      timer: setTimeout(() => this._settlePendingFence(), FENCE_DEFER_MS),
    }
    return null
  }

  /** Report where a fence landed. A fence with no authenticated event behind
   *  it changes nothing at all (ADR-0024 §1): the sighting is remembered for
   *  a completion that arrives later, and consumed — never applied — when
   *  it matches. A replay (the same hex twice, or one for an already-frozen
   *  block) does nothing. */
  sightFence(hex: string, line: number): void {
    if (this._consumedFence === hex) return // already-frozen block's fence
    if (this._fences.has(hex)) return // same value seen twice — a replay

    const pending = this._pendingFence
    if (pending !== null && pending.hex === hex) {
      // The deferred boundary's fence landed: serialize the block at the
      // fence's line. The block's STATUS flipped on the completion event —
      // this settles only which rows belong to it. A fence for a block that
      // has since been cleared changes nothing.
      this._pendingFence = null
      clearTimeout(pending.timer)
      if (!this._blocks.includes(pending.rec)) return
      this._freezeVisual(pending.rec, pending.getLine, line, pending.status)
      this._consumedFence = hex
      // Settle the live region only if no newer command owns the running
      // slot — a new command's live region must stay up.
      if (this._runningBlock === null) this._onDeferredFreeze?.()
      return
    }

    this._fences.set(hex, line)
    if (this._fences.size > MAX_FENCE_SIGHTINGS) {
      const oldest = this._fences.keys().next().value
      if (oldest !== undefined) this._fences.delete(oldest)
    }
  }

  /** The FENCE_DEFER_MS window elapsed with no fence: settle the visual
   *  freeze. While no newer command owns the running slot, the boundary is
   *  the CURRENT output end — the tail that was in flight at the completion
   *  has had the window to arrive, so this defers the boundary rather than
   *  truncating it. If a newer command owns the cursor, the current end
   *  would serialize the newer command's output into this block, so the
   *  boundary falls back to the completion-time end. The cost of a fence
   *  that never arrived is that the boundary is approximate. */
  private _settlePendingFence(): void {
    const pending = this._pendingFence
    if (pending === null) return
    this._pendingFence = null
    if (!this._blocks.includes(pending.rec)) return // block moved on (cleared)
    const boundary = this._runningBlock === null ? pending.getEndLine() : pending.endLine
    this._freezeVisual(pending.rec, pending.getLine, boundary, pending.status)
    this._consumedFence = pending.hex
    if (this._runningBlock === null) this._onDeferredFreeze?.()
  }

  private _cancelPendingFence(): void {
    if (this._pendingFence === null) return
    clearTimeout(this._pendingFence.timer)
    this._pendingFence = null
  }

  /** Freeze the running block bound to the attempt as abandoned: the
   *  attempt went `unknown` (loss, closure, native escape) — frozen, never
   *  successful, no reported exit code (ADR-0024 §5). Abandonment carries
   *  no fence and waits for none. */
  abandonAttempt(
    attempt: ExecutionAttempt,
    getLine: GetLineFn,
    endLine: number,
  ): BlockRecord | null {
    if (attempt.state !== 'unknown') return null
    if (this._attemptId !== attempt.id) return null
    const rec = this._runningBlock
    if (!rec) return null
    // No pending-boundary cancel here: a pending fence belongs to an older,
    // already logically frozen block (a lost fence), never to the running
    // block being abandoned — its timer settles it independently.
    const status = this._logicalFreeze(rec, null, 'unknown')
    this._freezeVisual(rec, getLine, endLine, status)
    this._attemptId = null
    return rec
  }

  /** Freeze a running block that never bound to an attempt at all. The
   *  block opened at the app-owned submit and the domain it was submitted
   *  under has ended, so no start and no completion can ever name it: `exit`
   *  destroys the shell that would have sent both, and against a real sshd
   *  the start frame does not get out before the transport dies (nocx-mlyu).
   *  A BOUND block is not this method's business — its attempt goes unknown
   *  and abandonAttempt freezes it, with the attempt as the authority. */
  abandonUnbound(getLine: GetLineFn, endLine: number): BlockRecord | null {
    if (this._attemptId !== null) return null
    const rec = this._runningBlock
    if (!rec) return null
    const status = this._logicalFreeze(rec, null, 'unknown')
    this._freezeVisual(rec, getLine, endLine, status)
    return rec
  }

  /**
   * Open a TURN in the scrollback (nocx-x8s2.2, ADR-0040): the question as
   * its header, and the blocks it causes as its CHILDREN, in order.
   *
   * A TURN IS ONE BLOCK THAT CARRIES WHAT IT CAUSED. Its children, top to
   * bottom, are the causal sequence exactly as it happened: a run of prose, a
   * tool call, more prose, a command with its real output, more prose. There
   * is no second answer body at the bottom and no fixed arrangement — the
   * order is the store's, and vertical position in a terminal is a claim
   * about time.
   *
   * WHAT THIS REPLACES, AND WHY (ADR-0040). The turn used to be drawn as
   * SEVERAL top-level blocks: the answer was one stored string, a command it
   * ran landed at the tail of the scrollback, and the only way to read in
   * order was to CUT the prose at the offset the call happened at and open a
   * continuation below the block. Every continuation then repeated the
   * question in its header under a `continued` badge, and the cut existed
   * only because the unit that was DRAWN (a run of prose) and the unit that
   * was STORED (the whole answer) disagreed. They are the same unit now: the
   * store writes `text` children, so the renderer draws the list it is given
   * and there is nothing left to cut, nothing to repeat, and no threshold at
   * which a run of calls is compacted — five calls are five blocks.
   *
   * Returns the handle the ask surface writes the turn through; nothing else
   * touches the turn's DOM.
   */
  addAnswerBlock(question: string, cwd: string, running?: RunningBlockActions): AnswerBlockHandle {
    const id = this._nextId++
    const el = createCommandBlock(
      'ask',
      id,
      question,
      cwd,
      this._location,
      '',
      null,
      null,
      // The question is out and no answer has arrived: the header paints the
      // ask kind's in-progress word ("thinking") beside a live pulse, and the
      // children show the typing dots — both of which the first delta, or a
      // terminal close, removes.
      'waiting',
      this._getContainer,
      (bid, sel) => {
        if (sel) this._onBlockSelected(bid)
        else this._onBlockDeselected(bid)
      },
      this._snapshotStore,
      // The default author, named because the parameter after it is the one
      // that matters here: an answer block's copy reads the ledger.
      'shell',
      this._answerText,
      running,
    )
    // WHERE THE TURN'S CHILDREN GO. Its own element, under the header, so the
    // children are addressable as a list and the header stays exactly one
    // row of the block whatever the turn grows underneath it.
    const children = document.createElement('div')
    children.className = 'cmd-children'
    el.appendChild(children)
    this._own(el, this._xtermContainer)
    this._answerBlocks.push({ id, question, el })

    // The answer says it is being written, WHERE it will be written. The
    // header chip is in the corner a person checks; this is where they are
    // already looking, and an empty turn under a finished question is
    // indistinguishable from a product that did nothing. The ONE stand-in
    // (nocx-vnirv.1): gone the moment the first thing lands, and RETURNED
    // while a tool call is in flight and no prose is being written.
    children.appendChild(createWorkingIndicator(blockKindRules('ask').statusChips!.inProgress))

    // Captured here rather than read off `this` inside the returned handle:
    // the handle's methods are declared `this: void` and are called as bare
    // functions, so `this` is genuinely absent inside them. What they need is
    // named one seam at a time rather than aliased whole: an alias is a second
    // name for one receiver, which the lint refuses and which hides WHICH of
    // the manager's facts a closure actually reaches for.
    const mintId = (): number => this._nextId++
    const own = (el: HTMLElement): void => {
      this._owned.add(el)
    }
    /** Claim the next block the ordinary submit path opens for this turn's
     *  region — the seam the `run` tool's command block arrives through. */
    const claim = (region: HTMLElement | null): void => {
      this._claimedBy = region
    }
    const claimedBy = (): HTMLElement | null => this._claimedBy
    const store = this._snapshotStore
    const sessionName = this._sessionName
    const getContainer = this._getContainer
    const onSelect = (bid: number, sel: boolean): void => {
      if (sel) this._onBlockSelected(bid)
      else this._onBlockDeselected(bid)
    }
    // WHEN THE TURN STARTED. A turn takes time — the model thinks, the tools
    // run — and a person wants to know how long as much as they want to know
    // that `df` took 27ms (nocx-hoeq3). Measured on the RENDERER's clock,
    // from the question being submitted to the run terminalizing, which is
    // exactly how a command's duration is measured a few hundred lines up
    // (_cmdStartTime); a restored turn shows the store's own figure.
    const now = this._now
    const startedAt = now()

    /** Put the stand-in back, at the END of the children — where the next
     *  thing the turn writes will land (nocx-vnirv.1). Idempotent: a turn
     *  already showing it never stacks a second one. Shown while work is in
     *  flight and nothing has been written yet: from open, and again while a
     *  tool call runs. */
    const showTyping = (): void => {
      if (children.querySelector(':scope > .cmd-answer-typing')) return
      children.appendChild(createWorkingIndicator(blockKindRules('ask').statusChips!.inProgress))
    }
    const hideTyping = (): void => {
      children.querySelector(':scope > .cmd-answer-typing')?.remove()
    }
    const stopWaiting = (): void => {
      el.querySelector('.cmd-header-right .cmd-answer-waiting')?.remove()
      el.querySelector('.cmd-header-right .cmd-answer-waiting-pulse')?.remove()
      // Both ends of one fact: the corner stops reporting work and the
      // children stop standing in for text. A run that fails before any delta
      // clears both, or the dots would go on typing an answer that will never
      // arrive.
      hideTyping()
    }

    /** The run of prose being written, and the STORE's id for it. */
    let prose: { body: AnswerBody; blockId: string | null } | null = null
    let reasoningNote: ReasoningNote | null = null
    const seenCalls = new Set<string>()

    /** Finish the run of prose being written. The next chunk opens a new one,
     *  which is what a `text` child is: one run, its own seat. */
    const endProse = (): void => {
      if (!prose) return
      prose.body.finish()
      prose = null
    }

    /** The body the next chunk is written into — opening a new `text` child
     *  when the chunk names a block the open run is not.
     *
     *  The boundary is never decided here. The backend opens a `text` block
     *  on the first delta after a call and seals it when the next call
     *  arrives, and the id rides every delta; all this does is notice that
     *  the id changed (ADR-0040: while the cut was the renderer's, the live
     *  path and the restore each computed it and could drift). */
    const proseBody = (blockId: string | undefined): AnswerBody => {
      if (prose && (blockId === undefined || prose.blockId === blockId)) return prose.body
      endProse()
      const pid = mintId()
      const pel = createCommandBlock(
        'text',
        pid,
        '',
        '',
        '',
        '',
        null,
        null,
        // Printing text can neither run nor fail: a run of prose is born
        // finished, with no outcome of its own to state. It draws no header
        // at all, so nothing states one either.
        'settled',
        getContainer,
        onSelect,
        store,
        'shell',
      )
      if (blockId) pel.dataset.entryId = blockId
      const outputEl = document.createElement('div')
      // The class comes from the kind's rules — the wrap policy is owned
      // there, never a second copy (nocx-ex636).
      outputEl.className = blockKindRules('text').outputClass!
      outputEl.dataset.answerBody = ''
      pel.appendChild(outputEl)
      children.appendChild(pel)
      own(pel)
      // The body is drawn by its ONE owner (answer-body.ts) — the same
      // function a RESTORED answer draws through, so a turn that comes back
      // after a restart is painted by the code that painted it live
      // (nocx-4em1z).
      const body = createAnswerBody(outputEl, {
        store,
        onContent: hideTyping,
        copy: copyToClipboard,
      })
      prose = { body, blockId: blockId ?? null }
      return body
    }

    return {
      id,
      el,
      toolCall(call: AnswerToolCall): void {
        // The dedupe exists for ONE case: an approved egress resume puts the
        // same call through the pipeline a second time, and the backend
        // announces the same callId again. An EMPTY callId is not an
        // identity — a provider that omits the id is not malformed, and two
        // distinct id-less calls must not merge because their empty keys
        // collide (w-call-id-order). So the key is only consulted when there
        // is one.
        if (call.callId !== '' && seenCalls.has(call.callId)) return
        if (call.callId !== '') seenCalls.add(call.callId)
        // The run of prose above ends here whatever the call turns out to be:
        // the backend sealed its `text` block when this call arrived, and the
        // next delta names the next one.
        endProse()
        // No prose is being written while the call runs, so the stand-in
        // returns to the children — where the answer will continue — until
        // the next delta lands (nocx-vnirv.1). The corner keeps reporting
        // meanwhile: the waiting chip is removed only by a delta or a close.
        showTyping()
        if (call.opensBlock) {
          // THE BLOCK IS THE ACCOUNT OF THIS CALL (ADR-0040). The command is
          // submitted through the ordinary path, and the turn claims the seat
          // it lands in, so the command's own block — its header, its output,
          // its exit status, its ⋮ — stands exactly where the call happened.
          // A child beside it restating the command would be the empty half
          // of the two positions one command used to occupy.
          claim(children)
          return
        }
        // A call that opened no block: its own child, named by the tool and
        // the arguments it ran on. The arguments are the whole point — the
        // tool and the derived resource are identical for two calls of one
        // session-scoped tool, and `blocks.read blockId=3` and
        // `blocks.read blockId=4` must read differently (ADR-0040).
        const cid = mintId()
        const cel = createCommandBlock(
          'tool',
          cid,
          toolCallTitle(
            { tool: call.tool, args: call.args, resource: call.resource },
            {
              sessionName,
            },
          ),
          '',
          '',
          '',
          null,
          null,
          'settled',
          getContainer,
          onSelect,
          store,
          'shell',
        )
        // The effect the BACKEND decided, as a typed attribute: a destructive
        // call must not look like a read, and the renderer may never derive
        // an effect from a tool name (ADR-0028 decision 4).
        cel.dataset.effect = call.effect
        cel.dataset.tool = call.tool
        children.appendChild(cel)
        own(cel)
      },
      reasoning(text: string): void {
        if (text === '') return
        if (!reasoningNote) {
          // Open or shut as the setting says at the moment the model starts
          // thinking (nocx-y9e88). A note built while the setting was off and
          // then switched on is caught by the applier, which walks what is
          // already on screen — this is the other half of the same rule.
          reasoningNote = createReasoningNote({ expanded: reasoningStartsExpanded() })
          // INTO THE OPEN RUN OF PROSE when there is one, so the note lands
          // where it arrived: the same run goes on being written below it,
          // and a note parked outside the block would claim a position the
          // text then wrote past. With no run open it is a child of the turn,
          // which is where a model that thinks before it speaks belongs.
          if (prose) prose.body.insert(reasoningNote.el)
          else {
            hideTyping()
            children.appendChild(reasoningNote.el)
          }
        }
        reasoningNote.append(text)
      },
      append(text: string, blockId?: string): void {
        if (text === '') return
        stopWaiting()
        proseBody(blockId).append(text)
      },
      close(status: 'success' | 'failure' | 'cancelled', error?: string, model?: string): void {
        el.dispatchEvent(new Event('nocx:block-settled'))
        stopWaiting()
        endProse()
        // A `run` that was announced and never reached a command must not
        // adopt somebody else's block later: the claim dies with the turn.
        if (claimedBy() === children) claim(null)
        // The header's right-hand group, from its ONE owner (nocx-hoeq3):
        // how long the turn took and how it ended, as the ask kind's rules
        // say them. One header now, so the outcome lands where the question
        // is and nowhere else.
        const right = el.querySelector('.cmd-header-right')
        if (right) settleHeaderRight(right, 'ask', now() - startedAt, { status, exitCode: null })
        // The model that answered, on the answer itself (nocx-e6kn2): the
        // person must be able to tell which model answered without going to
        // look it up. The value is the ask result's pinned model — this run's
        // fact, never a re-derivation. Last among the children, because it is
        // a caption on the whole turn rather than a piece of it.
        if (status === 'success' && model) {
          const note = document.createElement('div')
          note.className = 'cmd-answer-provenance'
          note.textContent = `answered by ${model}`
          children.appendChild(note)
        }
        if (error) {
          const note = document.createElement('div')
          note.className = 'cmd-answer-error'
          note.textContent = error
          children.appendChild(note)
        }
      },
    }
  }

  clearAll(): void {
    this._stopTicker()
    this._cancelPendingFence()
    this._clearCommandIndicator()
    // ONE list, because there is one owner: whatever this manager put in
    // the container comes out, whether it was a live block, an answer or a
    // restored one under its boundary (nocx-0zb1m).
    for (const el of this._owned) el.remove()
    this._owned.clear()
    this._blocks = []
    this._answerBlocks = []
    this._runningBlock = null
    this._cmdStartTime = null
    this._selectedBlockId = null
    this._attemptId = null
    this._fences.clear()
    this._consumedFence = null
  }

  private _finalizeRunningUnsafe(): void {
    // Note: a pending render-fence boundary belongs to an ALREADY logically
    // frozen block, never to the running block this finalizes — its timer
    // settles it independently, guarded by the running slot.
    this._stopTicker()
    this._clearCommandIndicator()
    if (!this._runningBlock) return
    this._runningBlock.status = 'failure'
    this._runningBlock.exitCode = null
    this._runningBlock = null
    this._cmdStartTime = null
    this._attemptId = null
  }

  dispose(): void {
    this.clearAll()
  }
}

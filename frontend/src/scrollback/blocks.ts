// DOM scrollback block manager.
// Creates, freezes, and manages DOM command blocks in the scrollback area.
// Flat warp-style design (P0-1): no card borders, dividers between blocks,
// subtle background tint on hover/select.

import { serializeRange, fromITheme } from './serializer'
import { getCurrentTheme } from '../renderers/theme-adapter'
import { highlightShellText, onShellHighlightReady } from '../shell-highlight'
import type { CommandSnapshotStore } from '../command-snapshot'
import type { IBufferLine } from '@xterm/xterm'
import { wordRangeIn } from '../word-selection'
import { createSecretChipUnresolved } from '../ui/secret-chip'
import { findReferences } from '../secret-reference'
import { commandFragment } from '../command-text'
import { KIND_LABELS, type SecretKind } from '../secret-kind'
import type { ExecutionAttempt } from '../lifecycle/state'
// ── Clipboard helper ────────────────────────────────────────────────────────

function clipboardFallback(text: string): void {
  if (typeof navigator !== 'undefined' && navigator.clipboard?.writeText) {
    navigator.clipboard.writeText(text).catch(() => {
      const ta = document.createElement('textarea')
      ta.value = text
      ta.style.position = 'fixed'
      ta.style.left = '-9999px'
      document.body.appendChild(ta)
      ta.select()
      try {
        document.execCommand('copy')
      } catch {
        /* silent */
      }
      document.body.removeChild(ta)
    })
  }
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

// ── Block kind ─────────────────────────────────────────────────────────────

/** A block's content grammar (nocx-ex636). The FRAME — a header, a body,
 *  selection, the overflow menu — is shared by every block; the grammar is
 *  not. A question is prose and a command is a command line, and a fourth
 *  kind must declare itself in the rules table instead of inheriting the
 *  command's rules by accident. */
export type BlockKind = 'command' | 'ask'

/** The rules the owner named — highlighting, wrapping, the status
 *  vocabulary — read from ONE table, keyed by the kind the block declared.
 *  No call site checks "is this an answer", and no builder defaults to the
 *  command rules. */
export interface BlockKindRules {
  /** The header's text is a command line: shell-highlight it. A question
   *  is prose and never runs through the lexer. */
  readonly highlightHeader: boolean
  /** The class the body element carries — the CSS owner of the wrap
   *  policy: `.cmd-output` freezes rows at the terminal grid width
   *  (nocx-juau), `.cmd-output-ask` wraps prose at the block's width. */
  readonly outputClass: string
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
  } | null
}

const BLOCK_KIND_RULES: Record<BlockKind, BlockKindRules> = {
  command: {
    highlightHeader: true,
    outputClass: 'cmd-output',
    statusChips: null,
  },
  ask: {
    highlightHeader: false,
    outputClass: 'cmd-output cmd-output-ask',
    statusChips: {
      inProgress: 'thinking',
      done: 'completed',
      failed: 'failed',
    },
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
  /** Append one streamed chunk (agent.runDelta text) to the answer body.
   *  `this: void` — the target holds the handle and calls the method
   *  detached from any receiver (unbound-method contract). */
  append(this: void, text: string): void
  /** Close the block: success, or failure with the renderable reason. */
  close(this: void, status: 'success' | 'failure', error?: string): void
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

// ── CWD display ────────────────────────────────────────────────────────────

function cwdLabel(cwd: string): string {
  const path = cwd.trim().replace(/\/+$/, '') || '~'
  const parts = path.split('/').filter(Boolean)
  if (path === '~' || parts.length === 0) return path
  return parts.slice(-2).join('/')
}

// ── Frozen-header highlight readiness ────────────────────────────────────────
//
// The Shiki grammar loads asynchronously at module init. A header frozen in
// the few milliseconds before that resolves would stay plain forever, so
// spans rendered pre-ready are registered here and repainted by
// `highlightShellText` once the tokenizer exists. After that the registration
// is a no-op: the grammar is loaded and every later header is coloured at
// freeze time.

let tokenizerLoaded = false
/** Spans frozen before the grammar loaded, keyed by the tab's snapshot store
 *  so the repaint judges against the right tab's command set. */
const pendingHeaderSpans = new Map<HTMLElement, CommandSnapshotStore>()

function refreshPendingHeaderSpans(): void {
  for (const [el, store] of pendingHeaderSpans) {
    const text = el.textContent ?? ''
    if (text && text !== '(empty)') el.innerHTML = highlightShellText(text, store)
  }
  pendingHeaderSpans.clear()
}

onShellHighlightReady(() => {
  tokenizerLoaded = true
  refreshPendingHeaderSpans()
})
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
  status: 'running' | 'success' | 'failure' | 'entered' | 'unknown' | 'waiting',
  store: CommandSnapshotStore,
): HTMLElement {
  const header = div('cmd-header')
  const rules = blockKindRules(kind)

  // ── Chips row (above command text): cwd left, duration+exit right ──
  const chipsRow = div('cmd-header-chips')

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

    const dur = document.createElement('span')
    dur.className = 'nocx-chip nocx-chip-muted cmd-header-duration'
    dur.textContent = formatRunningDuration(0)
    right.appendChild(dur)
  } else {
    if (durationMs !== null) {
      const dur = document.createElement('span')
      dur.className = 'nocx-chip nocx-chip-muted cmd-header-duration'
      dur.textContent = formatDuration(durationMs)
      right.appendChild(dur)
    }

    // An 'entered' block froze on environment entry (N6): it carries no
    // exit code and must never paint success or failure, whatever code the
    // local D later delivers to the ledger.
    if (status !== 'entered' && exitCode !== null) {
      const exit = document.createElement('span')
      exit.className =
        exitCode === 0
          ? 'nocx-chip nocx-chip-ok cmd-header-exit cmd-header-exit-ok'
          : 'nocx-chip nocx-chip-fail cmd-header-exit cmd-header-exit-fail'
      exit.textContent = exitCode === 0 ? 'ok' : `exit ${exitCode}`
      right.appendChild(exit)
    }

    // The kind's own in-progress vocabulary: the ask block says it is
    // thinking until the first delta lands, and the answer
    // lifecycle removes it at exactly that moment (nocx-ex636). The
    // command kind has no in-progress WORD — its running state is the
    // spinner above.
    if (rules.statusChips && status === 'waiting') {
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
      cmdSpan.innerHTML = command ? highlightShellText(command, store) : '(empty)'
      if (!tokenizerLoaded) pendingHeaderSpans.set(cmdSpan, store)
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

/**
 * Build the "⋮" overflow menu button + dropdown (P2-9, P1-6 fix).
 * The menu is rendered as a child of document.body with position:fixed
 * so it floats above ALL blocks and scroll containers. Position is
 * calculated from the button's bounding rect. Closes on outside click
 * and Escape key.
 */
function buildOverflowMenu(blockEl: HTMLElement, command: string): HTMLElement {
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

    const copyOut = document.createElement('button')
    copyOut.className = 'cmd-overflow-menu-item'
    copyOut.textContent = 'Copy output'
    copyOut.addEventListener('click', (ev) => {
      ev.stopPropagation()
      // The copyable text is asked of the BLOCK at read time (nocx-ex636):
      // an answer block's body is appended after the frame, so the
      // builder-time output reference is null — the block knows where its
      // output lives.
      const text = blockOutputText(blockEl)
      clipboardFallback(text)
      closeMenu()
    })
    const copyAll = document.createElement('button')
    copyAll.className = 'cmd-overflow-menu-item'
    copyAll.textContent = 'Copy all'
    copyAll.addEventListener('click', (ev) => {
      ev.stopPropagation()
      const outText = blockOutputText(blockEl)
      const recorded = btn.closest('.cmd-block')?.getAttribute('data-recorded-command')
      clipboardFallback(`${recorded ?? command}\n${outText}`)
      closeMenu()
    })

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

    menu.append(copyCmd, copyOut, copyAll, wrapItem)

    // Render at body level so it floats above all scroll containers (P1-6).
    document.body.appendChild(menu)

    // Position relative to the button using fixed coordinates.
    const btnRect = btn.getBoundingClientRect()
    menu.style.position = 'fixed'
    menu.style.top = `${btnRect.bottom + 2}px`
    menu.style.right = `${window.innerWidth - btnRect.right}px`

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
 */
function wireBlockSelection(
  blockEl: HTMLElement,
  container: HTMLElement,
  overflowBtn: HTMLElement,
  blockId: number,
  onSelect: (id: number, selected: boolean) => void,
): void {
  let mouseMoved = false

  blockEl.addEventListener('mousedown', (e) => {
    if ((e.target as HTMLElement).closest('.cmd-overflow-btn, .cmd-overflow-menu')) return
    mouseMoved = false
  })

  blockEl.addEventListener('mousemove', () => {
    mouseMoved = true
  })

  blockEl.addEventListener('mouseup', (e) => {
    if ((e.target as HTMLElement).closest('.cmd-overflow-btn, .cmd-overflow-menu')) return
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
  status: 'success' | 'failure' | 'entered' | 'unknown' | 'waiting',
  getContainer: () => HTMLElement,
  onSelect: (id: number, selected: boolean) => void,
  store: CommandSnapshotStore,
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
  wrapper.setAttribute('data-block-id', String(id))

  const header = createHeader(kind, command, cwd, location, durationMs, exitCode, status, store)

  let outputEl: HTMLElement | null = null
  if (outputHtml && !isOutputEmpty(outputHtml)) {
    outputEl = document.createElement('div')
    outputEl.className = rules.outputClass
    outputEl.innerHTML = outputHtml
  }

  // Overflow menu (P2-9) — always the LAST element of the header-right
  // group (owner directive: ⋮ never shifts position). It reads the block's
  // copyable text from the BLOCK, at click time (nocx-ex636).
  const overflow = buildOverflowMenu(wrapper, command)
  const right = header.querySelector('.cmd-header-right')
  if (right) right.appendChild(overflow)
  wrapper.appendChild(header)
  if (outputEl) wrapper.appendChild(outputEl)

  // Full-block click-to-select with drag distinction (P1-7, P1-8).
  wireBlockSelection(wrapper, getContainer(), overflow, id, onSelect)

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
): HTMLElement {
  const wrapper = document.createElement('div')
  wrapper.className = 'cmd-block cmd-block-running'
  // A running block is a command in flight; it declares the command kind
  // like the block it will freeze into (nocx-ex636).
  wrapper.dataset.blockKind = 'command'
  if (command && findReferences(command).length > 0) wrapper.dataset.recordedCommand = command
  wrapper.setAttribute('data-block-id', String(id))

  const header = createHeader('command', command, cwd, location, null, null, 'running', store)

  // Overflow menu — minimal: copy command only while running.
  // Always the LAST element of header-right (owner directive).
  const overflow = buildOverflowMenu(wrapper, command)
  const right = header.querySelector('.cmd-header-right')
  if (right) right.appendChild(overflow)

  wrapper.appendChild(header)
  wireBlockSelection(wrapper, getContainer(), overflow, id, onSelect)

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
  private _nextId = 1
  private _now: () => number
  private _scrollbackInner: HTMLElement
  private _xtermContainer: HTMLElement
  private _runningBlock: BlockRecord | null = null
  private _cmdStartTime: number | null = null
  /** Currently selected block id, or null if none selected (P1-8). */
  private _selectedBlockId: number | null = null
  private _snapshotStore: CommandSnapshotStore
  private _onDeferredFreeze?: () => void
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

  constructor(scrollbackInner: HTMLElement, xtermContainer: HTMLElement, opts: BlockManagerOpts) {
    this._scrollbackInner = scrollbackInner
    this._xtermContainer = xtermContainer
    this._now = opts.now ?? (() => performance.now())
    this._snapshotStore = opts.snapshotStore
    this._onDeferredFreeze = opts.onDeferredFreeze
  }

  get blocks(): readonly BlockRecord[] {
    return this._blocks
  }

  get runningBlock(): BlockRecord | null {
    return this._runningBlock
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
    if (this._runningBlock) this._runningBlock.attemptId = attemptId
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
    )
    this._scrollbackInner.insertBefore(el, this._xtermContainer)

    const rec: BlockRecord = {
      id,
      command,
      cwd,
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
    )

    rec.el = newEl
    // Anything that wanted to decorate this block had to wait for THIS
    // moment, because the line above threw the running element away. One
    // shot, cleared before it runs so a callback that re-enters cannot loop.
    const after = rec.afterVisualFreeze
    if (after !== undefined) {
      rec.afterVisualFreeze = undefined
      after()
    }
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
   * Append an assistant answer block to the flow (nocx-x8s2.2): the
   * question as the header, the streamed answer text as the body. The
   * answer is plain text, NOT xterm output — it is rendered as escaped
   * term-lines at this boundary. The block declares the ask kind, so the
   * prose grammar (no shell highlight, wrapping body, its own status
   * words) follows from the kind's rules rather than from command rules
   * borrowed by accident (nocx-ex636). Returns the handle the ask surface
   * appends to and closes.
   */
  addAnswerBlock(question: string, cwd: string): AnswerBlockHandle {
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
      // The question is out and no answer has arrived: the header paints
      // the ask kind's in-progress word ("thinking") beside a live pulse,
      // and the body shows the typing dots — both of which
      // the first delta — or a terminal close — removes.
      'waiting',
      this._getContainer,
      (bid, sel) => {
        if (sel) this._onBlockSelected(bid)
        else this._onBlockDeselected(bid)
      },
      this._snapshotStore,
    )
    const outputEl = document.createElement('div')
    // The ask kind's body class comes from the kind's rules — the wrap
    // policy is owned there, never a second copy (nocx-ex636).
    outputEl.className = blockKindRules('ask').outputClass
    outputEl.dataset.answerBody = ''
    // The answer's body says it is being written, WHERE it will be written.
    // The header chip is in the corner a person checks; the body is where
    // they are already looking, and an empty body under a finished question
    // is indistinguishable from a product that did nothing. Removed by the
    // first delta, so the dots are replaced by the text they stood in for.
    const typing = document.createElement('span')
    typing.className = 'cmd-answer-typing'
    typing.setAttribute('aria-label', blockKindRules('ask').statusChips!.inProgress)
    for (let i = 0; i < 3; i++) typing.appendChild(document.createElement('i'))
    outputEl.appendChild(typing)
    el.appendChild(outputEl)
    this._scrollbackInner.insertBefore(el, this._xtermContainer)
    this._answerBlocks.push({ id, question, el })

    // The waiting chip says the model has not answered yet; it stops the
    // moment the first delta lands, and a run that fails before any text
    // must stop waiting too (the timeout sentence and the waiting state
    // are two ends of one fact, nocx-ex636).
    const stopWaiting = (): void => {
      el.querySelector('.cmd-answer-waiting')?.remove()
      el.querySelector('.cmd-answer-waiting-pulse')?.remove()
      // Both ends of one fact: the corner stops reporting work and the body
      // stops standing in for text. A run that fails before any delta
      // clears both, or the dots would go on typing an answer that will
      // never arrive.
      el.querySelector('.cmd-answer-typing')?.remove()
    }

    // The streamed chunks split MID-LINE, so the body keeps one persistent
    // partial row: a chunk's final segment stays on it and the next chunk
    // continues it. Every '\n' completes a row — including a chunk ending
    // in '\n', whose trailing empty segment starts a fresh (possibly
    // empty) partial, so "a\n" + "b" renders as two rows, never "ab".
    //
    // A fenced block the model returns (```…```) is the one place in an
    // answer where the command grammar is the right grammar: its rows land
    // in a `.cmd-output-code` container that stays monospace and unwrapped
    // (the nocx-juau rule, reachable through the kind, never by accident).
    // The fence toggles on the COMPLETED line, so a marker split across
    // chunks still works, and BOTH delimiters belong to the code region:
    // the opener moves into the container it opens, the closer stays in
    // the container it closes. A second fence after intervening prose gets
    // a fresh container, so the order fence → prose → fence survives.
    let partial: HTMLSpanElement | null = null
    let inFence = false
    let codeEl: HTMLElement | null = null
    const fenceMarker = /^\s*```/

    const codeContainer = (): HTMLElement => {
      if (!codeEl) {
        codeEl = document.createElement('div')
        codeEl.className = 'cmd-output-code'
        outputEl.appendChild(codeEl)
      }
      return codeEl
    }

    const makeRow = (): HTMLSpanElement => {
      const span = document.createElement('span')
      span.className = 'term-line'
      ;(inFence ? codeContainer() : outputEl).appendChild(span)
      return span
    }

    // Trim the trailing empty rows the serializer's contract leaves behind
    // (a stream that ended with '\n' finishes with an empty partial row),
    // whether they sit in the body or in a fence container.
    const trimEmptyTail = (): void => {
      for (;;) {
        const last = outputEl.lastElementChild
        if (!last) return
        if (last.classList.contains('term-line') && last.textContent === '') {
          last.remove()
          continue
        }
        if (last.classList.contains('cmd-output-code')) {
          const row = last.lastElementChild
          if (row?.classList.contains('term-line') && row.textContent === '') {
            row.remove()
            continue
          }
          if (!last.hasChildNodes()) {
            last.remove()
            continue
          }
        }
        return
      }
    }

    return {
      id,
      el,
      append(text: string): void {
        if (text === '') return
        stopWaiting()
        const parts = text.split('\n')
        for (let i = 0; i < parts.length; i++) {
          const part = parts[i]
          if (i < parts.length - 1) {
            // A complete line: finish the current partial (or open one) and
            // close the row.
            if (!partial) partial = makeRow()
            partial.textContent += part
            const row = partial
            const line = partial.textContent
            partial = null
            if (fenceMarker.test(line)) {
              const opening = !inFence
              inFence = !inFence
              if (opening) {
                // The opener belongs to the code region it opens: a fresh
                // container, with the marker as its first row.
                codeEl = null
                row.remove()
                codeContainer().appendChild(row)
              }
              // The closer was created inside the code region and stays
              // there; the rows after it go back to the prose body.
            }
          } else {
            // The final segment stays partial — the next chunk continues it.
            if (!partial) partial = makeRow()
            partial.textContent += part
          }
        }
      },
      close(status: 'success' | 'failure', error?: string): void {
        stopWaiting()
        trimEmptyTail()
        partial = null
        // The header's status chip, in the flow's own chip vocabulary —
        // the words come from the ask kind's rules (nocx-ex636).
        const chips = blockKindRules('ask').statusChips
        const right = el.querySelector('.cmd-header-right')
        if (right && chips) {
          const chip = document.createElement('span')
          chip.className =
            status === 'success'
              ? 'nocx-chip nocx-chip-ok cmd-header-exit'
              : 'nocx-chip nocx-chip-fail cmd-header-exit'
          chip.textContent = status === 'success' ? chips.done : chips.failed
          right.appendChild(chip)
        }
        if (error) {
          const note = document.createElement('div')
          note.className = 'cmd-answer-error'
          note.textContent = error
          outputEl.appendChild(note)
        }
      },
    }
  }

  clearAll(): void {
    this._stopTicker()
    this._cancelPendingFence()
    for (const b of this._blocks) {
      b.el.remove()
    }
    this._blocks = []
    for (const b of this._answerBlocks) {
      b.el.remove()
    }
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

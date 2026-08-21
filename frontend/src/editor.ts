// Passive DOM command editor (ADR-0004 §3, ADR-0010). Holds text + selection
// only; a registered action decides where a submit goes. Keyboard routing
// to/from the PTY is by FOCUS: while shown the editor captures keys; while
// hidden the xterm has focus and keys flow to the PTY as usual.
//
// The input surface is a CodeMirror 6 EditorView mounted inside the
// `.nocx-editor` card (ADR-0010 §1).
//
// Key handling deliberately stays a native capture-phase listener on `root`,
// NOT a CM6 keymap: the listener runs before CM6's own contentDOM handlers
// for whatever extension list the caller installs, so Enter/Escape/Ctrl-C
// decide exactly as they did on the textarea. Binding these keys as a CM6
// keymap at Prec.highest is W2's job; W1 only preserves today's behaviour.

import { Compartment, EditorState, Extension } from '@codemirror/state'
import { EditorView, keymap } from '@codemirror/view'
import { defaultKeymap, history, historyKeymap } from '@codemirror/commands'
import { setSecretCandidate } from './secret-candidate'
import {
  firstUnresolved,
  setUnresolvedSpans,
  unresolvedRedactionField,
  type UnresolvedSpan,
} from './unresolved-redactions'
import type { SubmitPlan } from './submit'
import type { ModelChipState } from './agent-readiness'

/**
 * The indent a pasted command arrives with, when it arrives at the very
 * start of the line — and only there, and only the first line.
 *
 * This is not cosmetics. A leading space is the shell's `HISTCONTROL=
 * ignorespace` flag: the command runs and the shell does not record it. Every
 * documentation site indents its examples, so pasting one silently sets a
 * flag the user never chose and cannot see, and the command goes missing from
 * their shell history with no explanation.
 *
 * Only the FIRST line, and only on a paste:
 *
 *  - Later lines are left exactly as they are. Inside quotes whitespace is
 *    data — a here-doc, a `python -c '…'`, an embedded JSON string — and the
 *    only generic transform that is safe there (drop the common prefix) does
 *    nothing for the shapes people actually paste, where the closing `}'`
 *    already sits in column zero.
 *  - A space TYPED at the start of a line is untouched, because there it is
 *    the deliberate gesture this flag exists for. Paste is not intent;
 *    typing is.
 *
 * And it is undoable: Ctrl+Z is wired now, so a paste we changed can be put
 * back exactly as it came.
 */
export function stripPastedIndent(text: string, atLineStart: boolean): string {
  if (!atLineStart) return text
  return text.replace(/^[ \t]+/, '')
}

/** The row count at which `resized` stops firing — it must match the CSS cap
 *  in style.css (`.nocx-editor .cm-editor`, 30 lines), because past the cap
 *  the box no longer grows and the scrollback has nothing to follow. Raise
 *  both or neither. */
const MAX_ROWS = 30

export interface EditorActions {
  submit: (doc: string, plan?: SubmitPlan) => void
  /** Enter on an empty (or whitespace-only) draft. It is not a command — no
   *  block, no attempt, no ledger record — but it IS a keystroke a shell
   *  answers with a fresh prompt, so the newline still has to reach the pty.
   *  Separate from submit() because the two differ in exactly that: one
   *  opens an execution, the other only echoes a line (nocx-292k). */
  submitEmpty?: () => void
  // cancel discards the composed line the way Ctrl-C does at a shell prompt:
  // the editor clears and the shell is interrupted so a fresh prompt returns.
  // Without it, Ctrl-C in the editor is a no-op and the stale text corrupts
  // the next command.
  cancel: () => void
  /** Fired on every user-driven document change with the current value.
   *  Use to drive external filter logic without coupling the data source
   *  to the editor. */
  onInputChange?: (text: string) => void
  /**
   * Fired when the editor's own height changes, because that changes how much
   * room the scrollback has. Optional: an editor with nothing above it — a test,
   * or a future host — has nobody to tell.
   */
  resized?: () => void
  /** Fired when the user presses Up with the caret already on the first line
   *  (or an empty draft): there is no further upward movement, so the caller
   *  may open recall instead of moving the caret (design §8.10 v6 — Up is
   *  caret movement first). */
  onUpAtTop?: () => void
  /** Fired when Tab is pressed with the completion dropdown closed. Tab opens
   *  the dropdown (design §8.7's decided option 1: with no candidates it
   *  sends nothing — never a raw `\t`, which would complete the shell's
   *  empty buffer). While the dropdown is open, the completion arbiter
   *  consumes Tab to CYCLE the selection (Shift+Tab back; accept stays
   *  Enter), so this fires only when closed. Tab never moves the focus
   *  either way (nocx-w7h.2/.3). */
  onTab?: () => void
  /**
   * Optional async planning seam, run BEFORE the atomic handoff. The host
   * resolves references (vault.resolveLine) and vetoes masked history rows
   * here: return a plan to submit the RESOLVED line while recording the
   * reference-intact one, or false to keep the draft (the host has already
   * reported why). While the verdict is in flight a second Enter is swallowed
   * and an edit to the draft drops the stale plan.
   */
  beforeSubmit?: (doc: string) => SubmitPlan | Promise<SubmitPlan | false> | false
  /** Fired when the user types `@` at a WORD START — the reference picker's
   *  trigger (the owner's decision). `triggerPos` is the caret position the
   *  '@' lands at: the picker's replacement range starts there. The '@'
   *  itself is NOT consumed — it lands in the document, and the picker
   *  replaces the trigger word with the chosen reference; dismissing leaves
   *  the literal '@' the user typed. */
  onSecretPicker?: (triggerPos: number) => void
  /** Fired after the document is cleared programmatically (submit, Esc,
   *  Ctrl-C) — the host drops its floating surfaces and offer memory. */
  onDocCleared?: () => void
  /**
   * Fired on the save chord (⌘S / Ctrl-S, ⇧⌘S with Shift) — the host's
   * save seam: a live after-submit receipt's primary action (Shift moves
   * focus into the receipt for review), or the composition-time secret
   * save. Returns true when something was triggered. The editor consumes
   * the chord either way — the browser's Save Page must never fire from
   * inside the prompt.
   */
  onSave?: (shift: boolean) => boolean
  /**
   * Whether a commit performs the SHELL handoff (ADR-0004 §2 step 1: hide
   * the DOM editor before anything is sent). The target declares what it
   * is (routesToShell) and the composition root wires this accessor to
   * InputTargetRegistry.active() — one authority reads it, never a mode
   * boolean, never a per-call parameter. Absent (or true): the editor's
   * original contract, every submit is a shell submit and hides. The
   * agent target's question is not a handoff — nothing is pasted into a
   * pty, the grid is not given the keys, and the editor stays on screen
   * for the next question (nocx-wmy4).
   */
  handoffToShell?: () => boolean
  /** Wrap a transition that changes the editor's own box, so the host can
   *  play the displacement back instead of letting it land in one frame.
   *  Absent in tests and in any host without a scrollback: the transition
   *  then simply happens, which is the same result without the animation. */
  settleAround?: (mutate: () => void) => void
  /** ⌘Enter / Ctrl+Enter: the explicit target switch ADR-0004 §3 requires
   *  — the indicator's keyboard twin, flipping Run ⇄ Ask. The host flips
   *  the registry's active target; the editor stays passive, the draft is
   *  untouched, and the next plain Enter goes wherever the person just
   *  put it. */
  onToggleTarget?: () => void
  /** A reference chip's drop control: the host removes that chip (the
   *  chip is data the host owns; this only reports the dismissal). */
  onDismissChip?: (id: string) => void
}

export class CommandEditor {
  readonly root: HTMLElement
  private view: EditorView
  private chrome: HTMLElement
  /** The reference chip strip (nocx-4wtlh): the chips a selection raises,
   *  rendered between the chrome row and the input. The chips are DATA the
   *  host owns; this container is their surface. Hidden while empty. */
  private referencesEl: HTMLElement
  /** Left chip group: the location + cwd chips sit together, the clock
   *  keeps the right edge of the chrome row. */
  private chromeLeft: HTMLElement
  private locationChip: HTMLElement
  private cwdChip: HTMLElement
  private timeChip: HTMLElement
  /** Recovery action chip — hidden in the healthy state, shows one action
   *  label in an exception state. The chip IS the action: one click, no
   *  popover (nocx-atyf.2). */
  private recoveryChip: HTMLButtonElement
  private _recoveryOnClick: (() => void) | null = null
  /** The model chip pair (nocx-rikz5): the endpoint that will answer and
   *  the model it will answer with, or — when the answering role does not
   *  resolve — one chip carrying the rung of the ladder the person is on.
   *  Buttons, because they are controls: a chip that navigates must be
   *  reachable by keyboard, and `recoveryChip` above is the precedent.
   *  Hidden until setModelChip is called with a state, exactly as
   *  locationChip is, so the row's height never moves (nocx-6c546). */
  private modelEndpointChip: HTMLButtonElement
  private modelChip: HTMLButtonElement
  /** Where each chip goes when clicked. STORED rather than captured in a
   *  closure: the chips' meaning changes with every state while the
   *  listeners are permanent. Re-adding a listener per state change is how
   *  one click ends up firing three times. */
  private _modelChipTargets: {
    endpoint: 'endpoints' | 'roles' | null
    model: 'endpoints' | 'roles' | null
  } = { endpoint: null, model: null }
  private _onModelChipClick: ((page: 'endpoints' | 'roles') => void) | null = null
  /** Where the pending command would land: the SAME string the block header
   *  shows (routed from locationLine, never derived a second way). Empty
   *  for a local session, where the absence of a chip is the information. */
  private _location = ''

  /** The row count (capped at MAX_ROWS) the host was last told about. */
  private _lastRowCount = 1
  /** True while a programmatic document edit is in flight: such edits set the
   *  value the way `textarea.value = …` did, which fired no input event, so
   *  they must not fire onInputChange either. */
  private _programmatic = false
  /** Input ownership is independent of layout: a submitted command leaves
   *  the empty composer's box reserved while the terminal owns keys. */
  private _inputActive = false
  /** Optional keyboard arbiter: called (capture phase) BEFORE the editor's
   *  own key handling. Return true to consume the key. One arbiter chain,
   *  composed at the root (terminal-content.ts): recall first, completion
   *  second, editor defaults last.
   *
   *  THE OWNERSHIP RULE (design §8.9.4 — two state machines, one keyboard):
   *  recall is the higher-priority surface. While it is open it owns every
   *  key, and the composition dismisses the completion dropdown the moment
   *  recall opens — the two never stack, so there is never a question of
   *  which surface Tab reaches while recall is open (recall's navigating
   *  state hands Tab to the editor via abandonToEdit, and the editor's Tab
   *  then opens completion on the restored draft). While the completion
   *  dropdown is open it owns ↑/↓ (navigate, wrapping), Tab (cycle the
   *  selection, Shift+Tab back — accept stays Enter), Escape (close exactly
   *  one surface per press) and Right/End (ghost accept when every §8.7
   *  precondition holds). Everything else falls through to this editor's own
   *  handling and then to CM6. */
  private keyArbiter: ((e: KeyboardEvent) => boolean) | null = null

  /** Register (or clear) the keyboard arbiter. Cleared on dispose. */
  setKeyArbiter(arbiter: ((e: KeyboardEvent) => boolean) | null): void {
    this.keyArbiter = arbiter
  }

  /**
   * The clock ticks only while the editor is on screen.
   *
   * It used to be stamped once, by the input-state transition that revealed the
   * editor, and then left alone — so the chip showed the second the prompt
   * appeared and stayed there. Sit at a prompt for ten minutes and it is ten
   * minutes wrong, which is worse than showing nothing: a wrong clock is still
   * read as a clock.
   *
   * A block in the scrollback is the opposite case and keeps its frozen stamp:
   * it records when that command ran. This chip is not a record, it is the
   * present, and the editor is where the present is (nocx-6w4z).
   */
  private clock: ReturnType<typeof setInterval> | null = null

  /**
   * The editor's own surface styling. Kept as a CM6 theme (not style.css)
   * because a theme extension deterministically overrides the base theme,
   * which is what these two rules must do: kill the base theme's dotted focus
   * outline (the textarea had `outline: none`) and paint the caret in the
   * app's text colour (the base theme uses black/white).
   */
  private static readonly editorTheme = EditorView.theme({
    '&.cm-focused': { outline: 'none' },
    '.cm-content': { caretColor: 'var(--color-text)' },
    '.cm-cursor': { borderLeftColor: 'var(--color-text)' },
  })

  /**
   * Bridge from CM6 transactions to the host's callbacks.
   *
   * - onInputChange mirrors the old textarea `input` event, but only for
   *   user-driven changes: programmatic edits are flagged and must not fire it
   *   (a paste never fired `input` on the textarea).
   * - resized is the _grow() port: the host is told when the capped row count
   *   (1..MAX_ROWS) changes. The box's real height is CSS (max-height:
   *   ten lines, overflow-y: auto), so the row count is the trigger, exactly
   *   as rows were before.
   *
   * Both callbacks are wrapped: an exception from a consumer must not corrupt
   * CM6's update cycle (fail-open).
   */
  private readonly onViewUpdate = EditorView.updateListener.of((update) => {
    if (!update.docChanged) return
    const text = update.state.doc.toString()
    if (!this._programmatic) {
      try {
        this.actions.onInputChange?.(text)
      } catch (err) {
        console.error('nocx: onInputChange failed', err)
      }
    }
    const rows = Math.min(MAX_ROWS, Math.max(1, text.split('\n').length))
    if (rows !== this._lastRowCount) {
      this._lastRowCount = rows
      try {
        this.actions.resized?.()
      } catch (err) {
        console.error('nocx: resized failed', err)
      }
    }
  })

  /** The ACTIVE target's extensions, in a compartment so they can be
   *  swapped when the person switches where Enter goes (ADR-0004 §3). The
   *  shell's highlighting is the shell's — prose typed at Ask is not a
   *  command and must not be painted as one. The editor still chooses
   *  nothing: it only holds the slot, and the host reconfigures it from
   *  the registry's active target (setTargetExtensions). */
  private readonly targetCompartment = new Compartment()

  constructor(
    private readonly actions: EditorActions,
    extensions: Extension[] = [],
  ) {
    this.root = document.createElement('div')
    this.root.className = 'nocx-editor'
    this.root.style.display = 'none'

    // ── Editor chrome (header row) ──────────────────────────────────────
    this.chrome = document.createElement('div')
    this.chrome.className = 'nocx-editor-chrome'

    // Left group: location + cwd together, the clock keeps the right edge.
    // Placement only — the chips carry their own appearance (ui/README).
    this.chromeLeft = document.createElement('div')
    this.chromeLeft.className = 'nocx-editor-chrome-left'

    // Where the pending command would land: the same chip the block header
    // shows (`nocx-chip nocx-chip-muted`), fed the same string. Hidden until
    // setLocation receives a value — a local session grows NO chip.
    this.locationChip = document.createElement('span')
    this.locationChip.className = 'nocx-chip nocx-chip-muted nocx-editor-location'
    this.locationChip.style.display = 'none'

    this.cwdChip = document.createElement('span')
    this.cwdChip.className = 'nocx-chip nocx-editor-cwd'
    this.cwdChip.textContent = '📁 ~'

    this.timeChip = document.createElement('span')
    this.timeChip.className = 'nocx-chip nocx-editor-time'

    this.recoveryChip = document.createElement('button')
    this.recoveryChip.type = 'button'
    this.recoveryChip.className = 'nocx-chip nocx-editor-recovery'
    this.recoveryChip.style.display = 'none'
    this.recoveryChip.addEventListener('click', () => this._recoveryOnClick?.())

    // The model that will answer, and the way to change it (nocx-rikz5).
    // The same .nocx-chip family as every other chip in this row: the row
    // has no ui-badge in it and must not grow one — two visual grammars in
    // one row is worse than one old grammar.
    this.modelEndpointChip = document.createElement('button')
    this.modelEndpointChip.type = 'button'
    this.modelEndpointChip.className = 'nocx-chip nocx-editor-model'
    this.modelEndpointChip.style.display = 'none'
    this.modelEndpointChip.addEventListener('click', () => {
      const page = this._modelChipTargets.endpoint
      if (page) this._onModelChipClick?.(page)
    })

    this.modelChip = document.createElement('button')
    this.modelChip.type = 'button'
    this.modelChip.className = 'nocx-chip nocx-editor-model'
    this.modelChip.style.display = 'none'
    this.modelChip.addEventListener('click', () => {
      const page = this._modelChipTargets.model
      if (page) this._onModelChipClick?.(page)
    })

    this.chromeLeft.append(
      this.recoveryChip,
      this.locationChip,
      this.cwdChip,
      this.modelEndpointChip,
      this.modelChip,
    )
    this.chrome.append(this.chromeLeft, this.timeChip)
    this.root.appendChild(this.chrome)

    // ── Reference chip strip (nocx-4wtlh) ─────────────────────────────
    // Between the chrome and the input: part of the input surface, never
    // floating over it. Rendered by setReferenceChips; the host owns the
    // chips' lifecycle (selection raises them, a question consumes them,
    // a cleared scrollback takes their blocks).
    this.referencesEl = document.createElement('div')
    this.referencesEl.className = 'nocx-editor-references'
    this.referencesEl.style.display = 'none'
    this.root.appendChild(this.referencesEl)

    // ── CodeMirror 6 surface (ADR-0010) ────────────────────────────────
    // The extension list is a constructor parameter: the editor must not
    // hard-code its language or decoration set (spec §Decision 4). What is
    // hard-coded here is the editor's own identity — line wrapping matches
    // the old pre-wrap textarea, and the surface theme above.
    this.view = new EditorView({
      state: EditorState.create({
        doc: '',
        extensions: [
          EditorView.lineWrapping,
          // Undo, and it belongs to the editor's own identity rather than to
          // whatever the caller installs: `@codemirror/commands` was a
          // dependency and `history()` was installed nowhere, so Ctrl+Z in
          // the prompt did nothing at all. In a one-line prompt that is a
          // nuisance; in a box that now holds thirty pasted lines it is the
          // difference between a slip and lost work — and it is the
          // precondition for ever transforming a paste, because a change we
          // cannot take back is one we must not make silently.
          history(),
          keymap.of(historyKeymap),
          // The standard editing keymap — the editor's baseline, not the
          // caller's: the vault chip's atomicity is a real behavior ("the
          // caret steps over it as one unit and Backspace removes the whole
          // reference"), and without these bindings the production prompt
          // had NO arrow-key caret movement and NO Backspace at all. Our
          // capture-phase listener still decides Enter/Escape/Tab/Ctrl-C
          // before this keymap ever sees them (tested: 'Enter still submits
          // even when a default-precedence keymap binds it').
          keymap.of(defaultKeymap),
          EditorView.domEventHandlers({
            paste: (event, view) => {
              const text = event.clipboardData?.getData('text/plain')
              if (!text) return false
              const sel = view.state.selection.main
              const atLineStart = sel.from === view.state.doc.lineAt(sel.from).from
              const cleaned = stripPastedIndent(text, atLineStart)
              if (cleaned === text) return false
              event.preventDefault()
              view.dispatch({
                changes: { from: sel.from, to: sel.to, insert: cleaned },
                selection: { anchor: sel.from + cleaned.length },
                userEvent: 'input.paste',
              })
              return true
            },
          }),
          CommandEditor.editorTheme,
          this.onViewUpdate,
          // The active target's layer sits where the shell's used to: the
          // caller's stable extensions (the target indicator) follow it,
          // so a swap never disturbs them.
          this.targetCompartment.of([]),
          ...extensions,
        ],
      }),
      parent: this.root,
    })
    this.view.contentDOM.classList.add('nocx-editor-input')
    this.view.contentDOM.spellcheck = false
    this.view.contentDOM.setAttribute('autocapitalize', 'off')

    // Key handling: capture on the card, so our decisions run before CM6's
    // own contentDOM handlers no matter what keymap the caller installs
    // (verified empirically: a capture-phase listener on an ancestor
    // preempts the defaultKeymap's Enter binding).
    this.root.addEventListener('keydown', this.onKeydown, true)
  }

  /** Install the extensions of the target Enter currently goes to. Called
   *  by the host on wire-up and on every switch — the editor never reads
   *  the registry itself (it stays passive) and never keeps a mode of its
   *  own: what is installed IS the mode. */
  setTargetExtensions(extensions: Extension[]): void {
    this.view.dispatch({ effects: this.targetCompartment.reconfigure(extensions) })
  }

  private startClock(): void {
    this.setTime(new Date())
    if (this.clock !== null) return
    this.clock = setInterval(() => this.setTime(new Date()), 1000)
  }

  private stopClock(): void {
    if (this.clock === null) return
    clearInterval(this.clock)
    this.clock = null
  }

  /** Update the time chip with date, weekday and time. */
  setTime(ts: Date): void {
    const datePart = ts.toLocaleDateString([], {
      weekday: 'short',
      month: 'short',
      day: 'numeric',
    })
    const timePart = ts.toLocaleTimeString([], {
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
    })
    this.timeChip.textContent = `${datePart} ${timePart}`
  }

  mount(container: HTMLElement): void {
    container.appendChild(this.root)
  }

  /** Set or clear the recovery action chip. `label` is the action text
   *  the user reads ("Enable command editor", "Retry integration",
   *  "Restore command editor"). Pass null to hide — the healthy state
   *  shows nothing (nocx-atyf.2). */
  setRecoveryAction(label: string | null, onClick: () => void): void {
    this._recoveryOnClick = label === null ? null : onClick
    if (label === null) {
      this.recoveryChip.style.display = 'none'
      this.recoveryChip.textContent = ''
      return
    }
    this.recoveryChip.style.display = ''
    this.recoveryChip.textContent = label
  }

  /** Where a model chip's click goes. Installed once by the host; the
   *  chips themselves carry no destination — they read the slot the last
   *  setModelChip wrote. */
  onModelChipClick(handler: (page: 'endpoints' | 'roles') => void): void {
    this._onModelChipClick = handler
  }

  /**
   * The model chip's ONE writer (nocx-rikz5). Null hides both chips — the
   * state a Run target is in, where no model answers anything and a chip
   * claiming one would be decoration.
   */
  setModelChip(state: ModelChipState | null): void {
    this._modelChipTargets = { endpoint: null, model: null }
    if (state === null) {
      this.modelEndpointChip.style.display = 'none'
      this.modelChip.style.display = 'none'
      return
    }
    if (state.kind === 'ready') {
      this._modelChipTargets = { endpoint: 'endpoints', model: 'roles' }
      this.modelEndpointChip.disabled = false
      this.modelEndpointChip.style.display = ''
      this.modelEndpointChip.textContent = state.endpoint
      this.modelEndpointChip.title = state.endpoint
      this.modelEndpointChip.setAttribute(
        'aria-label',
        `Answers with ${state.endpoint}. Open Endpoints.`,
      )
      // The id is long and must not wrap: a wrapped chip is the layout
      // shift the row's single height exists to prevent. The CSS truncates
      // it; the title and the accessible name carry the whole value, so
      // nothing a person needs is only in the ellipsis.
      this.modelChip.disabled = false
      this.modelChip.style.display = ''
      this.modelChip.textContent = state.model
      this.modelChip.title = state.model
      this.modelChip.setAttribute(
        'aria-label',
        `Answers with the model ${state.model}. Open Roles.`,
      )
      return
    }
    // An action rung. A rung with no destination ('unavailable') is not a
    // control: a button that leads nowhere invites a click that does
    // nothing, which reads as the app being broken rather than the store
    // being unreadable. `disabled` is reset on the way OUT of that state
    // too (the ready branch above) — a chip that stayed dead after the
    // store came back would be the same defect with a longer fuse.
    this._modelChipTargets = { endpoint: null, model: state.page }
    this.modelEndpointChip.style.display = 'none'
    this.modelChip.disabled = state.page === null
    this.modelChip.style.display = ''
    this.modelChip.textContent = state.text
    this.modelChip.title = state.text
    // No "Opens settings." when nothing opens: the accessible name may not
    // promise an action the chip does not have.
    this.modelChip.setAttribute(
      'aria-label',
      state.page === null ? state.text : `${state.text}. Opens settings.`,
    )
  }

  /** Update the cwd chip text. Uses the same short directoryLabel shape. */
  setCwd(cwd: string): void {
    const path = cwd.trim().replace(/\/+$/, '') || '~'
    const parts = path.split('/').filter(Boolean)
    const label = path === '~' || parts.length === 0 ? path : parts.slice(-2).join('/')
    this.cwdChip.textContent = `📁 ${label}`
  }

  /**
   * Where the pending command would land — the same string the block header
   * shows, routed from the one locationLine derivation rather than computed
   * a second way (two derivations of "which host" are how they start
   * disagreeing). Empty for a local session: no chip, and the absence is
   * the information.
   */
  setLocation(location: string): void {
    this._location = location
    this.renderLocation()
  }

  /** The chip is where the next Enter would land: hidden for a local
   *  session, the host string otherwise. The trust-gated display is deleted
   *  with the `trusted` boolean (ADR-0024 §6) — no stream sequence may
   *  promote or revoke the chip. */
  private renderLocation(): void {
    if (!this._location) {
      this.locationChip.style.display = 'none'
      this.locationChip.textContent = ''
      return
    }
    this.locationChip.style.display = ''
    this.locationChip.textContent = this._location
  }

  // ── keyboard ──────────────────────────────────────────────────────────

  /**
   * Replace the whole document without firing onInputChange (the textarea's
   * `value = ''` fired no input event).
   */
  private clearDoc(): void {
    this._programmatic = true
    try {
      this.view.dispatch({
        changes: { from: 0, to: this.view.state.doc.length },
      })
    } finally {
      this._programmatic = false
    }
    this._lastRowCount = 1
    // The host's floating surfaces (the secret offer, the picker) hold
    // stale findings after a clear; programmatic clears fire no input
    // events, so this is the one seam that tells the host.
    this.actions.onDocCleared?.()
  }

  /** Clear the document through the same seam a submit uses: programmatic,
   *  firing no input events, but announcing the clear (onDocCleared) so
   *  the host's floating surfaces hold no stale findings over the empty
   *  line. The per-target draft swap uses this for a target with no saved
   *  draft (nocx-4ff.7) — the incoming mode's line is genuinely empty. */
  clear(): void {
    this.clearDoc()
  }

  /** True while a beforeSubmit verdict is in flight: a second Enter in that
   *  window must not start a second resolve (each would commit a duplicate
   *  ledger record on success). Cleared when the verdict lands. */
  private _submitInFlight = false

  /** Submit the current document, then hide and clear (ADR-0004 §2). Also
   *  the overlay's execution path: RecallOverlay calls this so Enter in the
   *  palette runs the previewed command through exactly the same path a
   *  typed Enter takes. With a beforeSubmit hook registered, the atomic
   *  handoff waits for the verdict — a reference line resolves first, a
   *  veto keeps the draft with the host's report already on screen. */
  submit(): void {
    const doc = this.view.state.doc.toString()
    // An empty prompt is not a command, and this is the only place that can
    // say so before any state moves. CommandLedger.open already owns the rule
    // — it refuses an empty string — but it is downstream of commit(), which
    // clears and hides FIRST (the atomic handoff below). So an empty Enter
    // threw out of onKeydown with the editor already hidden and no input-state
    // transition to show it again: the prompt vanished for the rest of the
    // session. Asking the question here keeps one answer to "is this a
    // command" and keeps it on the side of the handoff that can still decline.
    // Whitespace alone counts as empty for the same reason — it would open a
    // block for a command nobody typed. Only the DECISION trims; what a real
    // command sends is still the document byte-for-byte, so a leading-space
    // line (` ls`, kept out of shell history on purpose) is untouched.
    // Not a command — but still a keystroke. The draft is cleared (it holds
    // only whitespace) and the bare newline goes to the pty, so the shell
    // answers with a fresh prompt exactly as it would in a plain terminal.
    // Neither the ledger nor the attempt path is entered, which is what
    // keeps the editor from being hidden by a handoff that then throws.
    // And the bare newline goes to the pty only when the line was going to
    // the SHELL. With Ask active there is nothing to ask and no reason to
    // poke the shell — an empty Enter in a mode the person chose must not
    // reach a program that is waiting on stdin. The seam is the same one
    // authority the handoff reads (the registry's active target), never a
    // second answer to "where does Enter go".
    if (doc.trim() === '') {
      this.clearDoc()
      if (this.actions.handoffToShell?.() ?? true) this.actions.submitEmpty?.()
      return
    }
    const hook = this.actions.beforeSubmit
    if (!hook) {
      this.commit(doc)
      return
    }
    if (this._submitInFlight) return
    let result: SubmitPlan | Promise<SubmitPlan | false> | false
    try {
      result = hook(doc)
    } catch {
      // Fail-open: a throwing planner keeps the draft; the host reports.
      return
    }
    if (result === false) return
    if (typeof (result as Promise<SubmitPlan | false>).then === 'function') {
      // The verdict is in flight (a line with references resolves over the
      // wire). A second Enter is swallowed; an edit to the draft drops the
      // stale plan — the user's new text is the draft.
      this._submitInFlight = true
      void Promise.resolve(result as Promise<SubmitPlan | false>)
        .then((plan) => {
          if (plan !== false && this.view.state.doc.toString() === doc)
            this.commit(plan.sendLine, plan)
        })
        .catch(() => {
          // Fail-open: the draft stays.
        })
        .finally(() => {
          this._submitInFlight = false
        })
      return
    }
    // A SYNCHRONOUS verdict (a plain line — no references, no wire call):
    // the atomic handoff runs NOW, with no microtask gap. A gap would let a
    // fast-typed key change the draft and drop the commit under the stale-
    // plan guard, and it would break the sync-after-Enter observers.
    const plan = result as SubmitPlan
    this.commit(plan.sendLine, plan)
  }

  /** The atomic handoff (ADR-0004 §2): clear + release input BEFORE sending,
   *  so the committed command is painted once by the shell, not echoed twice.
   *  `plan` is present only after a beforeSubmit planner succeeded:
   *  the resolved sendLine goes to the PTY, the reference-intact recordLine
   *  to the ledger.
   *
   *  The hide is the handoff's step 1 and belongs to it alone: a commit
   *  whose destination is not the shell (the agent target — routesToShell
   *  false, read through the handoffToShell seam) hides nothing, because
   *  there is nothing to hand off — the question stays in the editor for
   *  the next one (nocx-wmy4). The clear is unconditional: a submitted
   *  question, like a submitted command, leaves the editor empty. */
  private commit(sendLine: string, plan?: SubmitPlan): void {
    // The plan is present only after a beforeSubmit planner succeeded; the
    // plain path keeps the exact one-argument call (no resolution happened,
    // so there is nothing to resolve for the ledger either).
    const submit = (): void => {
      if (plan) this.actions.submit(sendLine, plan)
      else this.actions.submit(sendLine)
    }
    if (this.actions.handoffToShell?.() ?? true) {
      // ONE TRANSITION, ONE SETTLE. Emptying the draft, giving up the
      // composer's box and opening the running block all move the scrollback,
      // they all run in this task with no paint between them, and to a person
      // they are a single movement: the block takes the composer's place. Each
      // used to be animated separately, and the block's own glide then
      // cancelled the composer's mid-flight — which is what the jitter was.
      //
      // The handoff is unchanged and still atomic: hide() runs before
      // submit(), so the grid is writable before a byte can flow
      // (nocx-u7uh.23). What the wrapper adds is only how the displacement is
      // PAINTED. Absent a host that can settle, the mutations simply happen.
      const settle = this.actions.settleAround ?? ((m: () => void) => m())
      settle(() => {
        this.clearDoc()
        this.hide()
        submit()
      })
      return
    }
    // Nothing is handed off — the question stays in the editor and its box
    // never leaves the layout, so there is no displacement to play back.
    this.clearDoc()
    submit()
  }

  private onKeydown = (e: KeyboardEvent): void => {
    // IME in progress: the composition owns the key stream, and CM6 handles
    // composition itself. Interpreting a composing Enter as submit or a
    // composing Ctrl-C as interrupt would destroy the composition (spec
    // W1 check 3). keyCode 229 is the legacy WebKit composition sentinel.
    // The key is CONSUMED, not merely ignored: with the standard editing
    // keymap installed, an unguarded composing Enter would reach CM6's
    // insertNewline and corrupt the draft mid-composition.
    if (e.isComposing || e.keyCode === 229) {
      e.preventDefault()
      e.stopPropagation()
      return
    }

    // The keyboard arbiter (recall overlay) gets first refusal: while it is
    // open, Up/Down/Enter/Escape and the open shortcut belong to it, and
    // nothing the editor handles — submit, clear, interrupt — may fire.
    if (this.keyArbiter?.(e)) return

    // A key typed into a nested form control (the vault offer's name field
    // and buttons) belongs to that control, not to the prompt surface: the
    // editor must never interpret Enter inside the name field as submit, or
    // Escape as clearing the draft under it.
    const target = e.target as HTMLElement | null
    if (target && target.closest('input, textarea, select, button')) return

    // '@' at a WORD START opens the reference picker (the owner's trigger):
    // after whitespace or at line start. An '@' inside a word — user@host,
    // git@github.com, an email — is ordinary text and never fires. The word
    // start is the same rule the ghost text uses (ghostTail's prevChar check
    // in suggest/controller.ts): prevChar '' (line start) or whitespace.
    // The '@' itself is NOT consumed — it lands in the document, and the
    // picker replaces it with the chosen reference; dismissing leaves the
    // literal '@' the user typed.
    if (e.key === '@' && !e.ctrlKey && !e.metaKey && !e.altKey) {
      const head = this.view.state.selection.main.head
      const prevChar = head > 0 ? this.view.state.doc.sliceString(head - 1, head) : ''
      if (prevChar === '' || /\s/.test(prevChar)) {
        this.actions.onSecretPicker?.(head)
      }
    }

    // The save chord (⌘S / Ctrl-S — the same dual-modifier rule recall's
    // Ctrl/Cmd+R uses): the host's save seam. Handled BEFORE Enter/Escape,
    // and consumed even when nothing is saveable — the browser's Save Page
    // must never fire from inside the prompt.
    if ((e.key === 's' || e.key === 'S') && !e.altKey && (e.metaKey || e.ctrlKey)) {
      e.preventDefault()
      e.stopPropagation()
      this.actions.onSave?.(e.shiftKey)
      return
    }

    // Up is caret movement first (design §8.10 v6): recall opens only when
    // there is no further upward movement — caret on the first line or an
    // empty draft. Otherwise the key falls through to CM6's caret handling.
    //
    // And only from a SINGLE-LINE draft. Recall previews the selected command
    // over the draft, so on a multi-line draft one stray Up puts somebody
    // else's command where twenty pasted lines were. Esc restores it and Tab
    // is not destructive, but the next keystroke need not be either of those:
    // Enter runs the recalled command, and an edit keeps it as the new draft.
    // The risk is not symmetric — losing `git ` costs a retype, losing a
    // pasted curl costs the paste — and neither is the gesture: `git ` + Up
    // is how every shell works and must keep working, while a multi-line
    // draft is a block being edited, where Up plainly means "up". The
    // explicit shortcut still opens recall from anywhere.
    if (e.key === 'ArrowUp' && !e.shiftKey && !e.ctrlKey && !e.metaKey && !e.altKey) {
      if (this.caretAtTop() && this.view.state.doc.lines <= 1) {
        e.preventDefault()
        e.stopPropagation()
        this.actions.onUpAtTop?.()
      }
      return
    }

    // The ask entry gesture (nocx-4wtlh): ⌘/Ctrl+Enter is the explicit
    // switch ADR-0004 §3 requires — it flips the ACTIVE target, exactly as
    // clicking the caret indicator does, and sends nothing. The chord that
    // asked ONCE without moving the target is gone (owner's correction):
    // one chord that submits and one that submits somewhere else is two
    // send keys on one line, and the person could not see, before pressing
    // it, where the text was about to go. Now the chord only ever changes
    // the indicator, plain Enter is the only send, and the indicator says
    // where it goes. The draft is untouched by the flip. Unwired (no
    // onToggleTarget), the chord falls through to CM6.
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey) && !e.altKey) {
      if (!this.actions.onToggleTarget) return
      e.preventDefault()
      e.stopPropagation()
      this.actions.onToggleTarget()
      return
    }

    // Standard editor keys.
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      e.stopPropagation()
      this.submit()
      return
    }
    // Tab opens the completion dropdown (design §8.7's decided option 1: our
    // dropdown only; with no candidates it sends nothing — never a raw `\t`,
    // which would complete the shell's empty buffer, since ADR-0004 hands
    // the line over atomically at submit). The key stays SWALLOWED so it can
    // never move the focus out of the prompt (measured 2026-08-02:
    // document.activeElement went from cm-content to nothing). While the
    // dropdown is open, the completion arbiter consumes Tab to cycle the
    // selection (and Shift+Tab to go back), so this branch only fires when
    // the dropdown is closed.
    if (e.key === 'Tab' && !e.ctrlKey && !e.metaKey && !e.altKey && !e.shiftKey) {
      e.preventDefault()
      e.stopPropagation()
      this.actions.onTab?.()
      return
    }
    // Shift+Tab with the dropdown closed: Tab opens; Shift+Tab goes BACK,
    // which only means something once the dropdown is up. Closed, it is
    // swallowed all the same — never the browser's focus-move, which would
    // silently strand the next keystroke (the 2026-08-02 measurement above).
    if (e.key === 'Tab' && !e.ctrlKey && !e.metaKey && !e.altKey) {
      e.preventDefault()
      e.stopPropagation()
      return
    }
    // Escape clears the draft without interrupting the shell (Ctrl-C).
    if (e.key === 'Escape') {
      e.preventDefault()
      e.stopPropagation()
      this.escapeClear()
      return
    }
    // Ctrl-C cancels the composed line like a shell prompt. A real selection is
    // left alone so Ctrl-C still copies; with nothing selected, interrupt.
    if (e.ctrlKey && !e.metaKey && !e.altKey && (e.key === 'c' || e.key === 'C')) {
      const sel = this.view.state.selection.main
      if (sel.from !== sel.to) return
      e.preventDefault()
      e.stopPropagation()
      this.clearDoc()
      this.actions.cancel()
    }
  }

  /** True when the caret is on the first line (or the doc is empty): Up has
   *  no further upward movement, so recall may open (design §8.10 v6). */
  private caretAtTop(): boolean {
    const head = this.view.state.selection.main.head
    return this.view.state.doc.lineAt(head).number <= 1
  }

  /**
   * The Escape tail shared by the editor's own keydown and the host's
   * document rescue: the clear. The keyboard arbiter
   * is NOT consulted here — the internal path already consulted it for the
   * whole key, and the external path consults it before calling, so an open
   * recall overlay dismisses itself (restoring its captured draft) instead
   * of having the draft cleared under it.
   */
  private escapeClear(): void {
    this.clearDoc()
  }

  /**
   * Route an Escape that reached the HOST's document listener, not this
   * editor's own keydown: the editor is on screen but a click elsewhere
   * moved the focus out of its surface, so the key never traversed `root`
   * and onKeydown never saw it. The decision order mirrors the internal
   * one — the keyboard arbiter gets first refusal, then the clear — and
   * focus returns so the next keystroke lands in the
   * prompt, exactly as the typing rescue promises. Returns true when the
   * key was consumed (the caller preventDefaults).
   */
  handleExternalEscape(e: KeyboardEvent): boolean {
    if (e.isComposing || e.keyCode === 229) return false
    if (this.keyArbiter?.(e)) return true
    this.escapeClear()
    this.view.focus()
    return true
  }

  // ── visibility ────────────────────────────────────────────────────────

  /**
   * Show the input-owning composer.
   *
   * The composer rejoins the layout and takes its flex slot back, which moves
   * the scrollback by its own height — so the caller settles around this (see
   * `_syncLifecycleOwnership`), and holds the bottom while it does.
   */
  show(): void {
    this._inputActive = true
    this.root.style.display = ''
    this.root.removeAttribute('inert')

    // CLEARED, not set to 'visible'. An inactive pane is hidden with
    // `visibility: hidden` on purpose (base.css) so its renderer keeps measuring
    // a real size — and `visibility`, unlike `display`, is overridable by a
    // descendant. An inline `visible` here therefore re-painted the editor of a
    // tab the user had switched away from, on top of the active tab's editor at
    // the very same coordinates: you typed into the one below and watched the
    // empty one above. Clearing the property lets the pane decide, which is
    // where that decision belongs.
    this.root.style.visibility = ''
    this.startClock()
    // A view that was display:none can cache zero or stale geometry; ask CM6
    // to re-measure before it is painted and focused (spec W1 check 5).
    this.view.requestMeasure()
    this.view.focus()
  }

  /** The caret's x, in px relative to the editor root — the anchor for a
   *  floating surface that opens at the caret (the secret picker). Null when
   *  the view has no coordinates yet. */
  caretAnchorLeft(): number | null {
    const head = this.view.state.selection.main.head
    const coords = this.view.coordsAtPos(head)
    if (!coords) return null
    return coords.left - this.root.getBoundingClientRect().left
  }

  /** Focus the editor if it is visible. Safe to call when hidden. */
  focus(): void {
    if (this.isVisible) this.view.focus()
  }

  /** The current document text. */
  getDoc(): string {
    return this.view.state.doc.toString()
  }

  /** The current selection. */
  getSelection(): { from: number; to: number } {
    const sel = this.view.state.selection.main
    return { from: sel.from, to: sel.to }
  }

  /** Replace the whole document programmatically (fires no input events),
   *  placing the caret at `from` (default: the end of the text). The recall
   *  overlay uses this to preview a history row and to restore the draft. */
  replaceDoc(text: string, from?: number, to?: number): void {
    this._programmatic = true
    try {
      const anchor = from ?? text.length
      this.view.dispatch({
        changes: { from: 0, to: this.view.state.doc.length, insert: text },
        selection: { anchor, head: to ?? anchor },
      })
    } finally {
      this._programmatic = false
    }
  }

  /** The editor's vertical scroll offset — for exact draft restoration. */
  getScrollTop(): number {
    return this.view.scrollDOM.scrollTop
  }

  setScrollTop(top: number): void {
    this.view.scrollDOM.scrollTop = top
  }

  /**
   * Replace a document span programmatically — the completion controller
   * applies a candidate over its replacement range through this seam, and
   * the vault controller replaces a credential literal with its reference
   * (design §8.9: insertText is what is inserted; displayText is never).
   * Fires no input events, so onInputChange does not re-trigger (a
   * programmatic edit is exactly what the `_programmatic` flag exists
   * for). The caret lands after the inserted text.
   */
  applyReplacement(from: number, to: number, text: string): void {
    this._programmatic = true
    try {
      this.view.dispatch({
        changes: { from, to, insert: text },
        selection: { anchor: from + text.length },
      })
    } finally {
      this._programmatic = false
    }
  }
  /**
   * Insert text at the caret, replacing any selection, then focus.
   * Used by right-click/middle-click paste while the editor owns input: at the
   * prompt the terminal is read-only (setReadOnly), so a paste must land in the
   * composed command, not the (disabled) grid.
   */
  insertText(text: string): void {
    const sel = this.view.state.selection.main
    this._programmatic = true
    try {
      this.view.dispatch({
        changes: { from: sel.from, to: sel.to, insert: text },
        selection: { anchor: sel.from + text.length },
      })
    } finally {
      this._programmatic = false
    }
    this.view.focus()
  }

  /** Paint or clear the quiet composition-time candidate mark. The
   *  controller owns WHEN a candidate exists; this is the editor's half of
   *  the StateField the host's extension installed. */
  setCandidateDecoration(span: { from: number; to: number } | null): void {
    this.view.dispatch({ effects: setSecretCandidate.of(span) })
  }

  /** Replace the unresolved-redaction spans (recalled masked text). The
   *  spans map through the user's subsequent edits; the host refuses to
   *  submit while any remain. */
  setUnresolvedSpans(spans: ReadonlyArray<UnresolvedSpan>): void {
    this.view.dispatch({ effects: setUnresolvedSpans.of(spans) })
  }

  /** The first unresolved span still in the document, or null — the one
   *  Enter opens resolution on. Reads the field the host installed;
   *  undefined (not installed) reads as none. */
  firstUnresolvedSpan(): UnresolvedSpan | null {
    const spans = this.view.state.field(unresolvedRedactionField, false) ?? []
    return firstUnresolved(spans)
  }

  /** True while the document still carries any unresolved span. */
  hasUnresolvedSpans(): boolean {
    const spans = this.view.state.field(unresolvedRedactionField, false) ?? []
    return spans.some((s) => s.to > s.from)
  }
  /**
   * Remove the composer from presentation entirely — its box with it.
   *
   * The one way the composer leaves, and it leaves whole. It used to have a
   * second, `suspend()`, which kept the flex slot while hiding the chrome, so
   * that releasing 77px could not move a scrollback that hangs from the
   * scroller's bottom edge. That was traded away: the settle glide plays the
   * displacement back instead, and the reserved box was costing an inline TUI
   * on the normal buffer four rows of pane while `htop` on the alternate
   * buffer got all of them (nocx-g6hnk, reversing part of nocx-i4h04).
   *
   * The caller that changes layout owns the settle — see `commit`, where
   * emptying the draft, this call and opening the block are one glided
   * transition.
   */
  hide(): void {
    this._inputActive = false
    // Stopped, not left running. Every tab owns an editor, so a timer that
    // outlives visibility is one wakeup per second per tab for a chip nobody
    // can see — and they accumulate for the life of the window.
    this.stopClock()
    this.view.contentDOM.blur()
    this.root.removeAttribute('inert')
    this.root.style.display = 'none'
  }

  get isVisible(): boolean {
    return (
      this._inputActive &&
      this.root.style.display !== 'none' &&
      this.root.style.visibility !== 'hidden'
    )
  }

  /** Whether the editor's root element contains `el`. Used to scope the
   *  focus-bounce so clicks on the editor surface / cwd chip
   *  are not swallowed. CM6's contentDOM lives inside root, so the contract
   *  the focus-bounce tests against holds unchanged. */
  rootContains(el: Node | null): boolean {
    return this.root.contains(el)
  }

  /** Render the reference chips the host owns (nocx-4wtlh). Each chip is
   *  the kit's nocx-chip identity with a drop control; the strip hides
   *  itself when empty. The host re-renders on every add/remove — the
   *  chips are a short list and the strip is their only surface. */
  setReferenceChips(chips: ReadonlyArray<{ id: string; label: string }>): void {
    this.referencesEl.replaceChildren()
    if (chips.length === 0) {
      this.referencesEl.style.display = 'none'
      return
    }
    this.referencesEl.style.display = ''
    for (const chip of chips) {
      const el = document.createElement('span')
      el.className = 'nocx-chip nocx-editor-reference-chip'
      el.dataset.chipId = chip.id
      el.title = chip.label
      const name = document.createElement('span')
      name.className = 'nocx-editor-reference-chip__name'
      name.textContent = chip.label
      const drop = document.createElement('button')
      drop.type = 'button'
      drop.className = 'nocx-editor-reference-chip__drop'
      drop.textContent = '×'
      drop.setAttribute('aria-label', `remove reference ${chip.label}`)
      drop.addEventListener('click', () => this.actions.onDismissChip?.(chip.id))
      el.append(name, drop)
      this.referencesEl.appendChild(el)
    }
  }

  /** Drop every reference chip (the host consumed them — a question
   *  carried them, or a cleared scrollback took their blocks). */
  clearReferenceChips(): void {
    this.setReferenceChips([])
  }

  dispose(): void {
    // A tab can be closed while its editor is on screen, which is the common
    // case rather than the edge one — hide() would never run and the interval
    // would outlive everything it refers to.
    this.stopClock()
    // The arbiter outlives the overlay it points at otherwise; a closed tab
    // must not keep consuming keys through a dead closure.
    this.keyArbiter = null
    this.root.removeEventListener('keydown', this.onKeydown, true)
    this.view.destroy()
    this.root.remove()
  }
}

// The ask entry gesture (nocx-4wtlh): the caret indicator that renders
// InputTargetRegistry.active(), and the reference chips a selection raises.
//
// The rule the whole gesture stands on: NOTHING but the person changes where
// Enter goes. The indicator is the ADR-0004 §3 "UI chip" — the active
// target, rendered in the input line immediately left of the cursor. It is
// operable (click, and the ⌘/Ctrl+Enter chord) because the ADR requires an
// explicit switch, but in ordinary use nobody operates it: it is the
// confirmation that Enter goes to the shell.
//
// Selecting a region of a finished block's output FREEZES that region into a
// reference chip in the input line: "if you ask, this comes with you". It
// never arms ask — the active target does not move (the owner's Warp
// complaint: a selection that armed ask would send the next typed command
// to the model).

import { StateEffect, type Extension } from '@codemirror/state'
import { EditorView, GutterMarker, ViewPlugin, gutter } from '@codemirror/view'
import type { BadgeTone } from './ui/badge'
import { createModeIndicator } from './ui/mode-indicator'

// ── Reference chips ────────────────────────────────────────────────────────

export interface ReferenceChip {
  /** Stable identity for dismissal and exact-duplicate dedupe. */
  readonly id: string
  /** The finished block the selection landed in — the frame source and the
   *  chip's scope. Never re-derived from DOM selection at submit time
   *  (AD-8: selection is copy; the chip is the mode's record). */
  readonly blockEl: HTMLElement
  /** The chip's name: the block's command and the covered row range. */
  readonly label: string
  /** First covered term-line index, inclusive, 0-based. */
  readonly rowStart: number
  /** One past the last covered term-line index, exclusive. */
  readonly rowEnd: number
}

/** The block output whose term-line indices a chip's rows refer to. A chip
 *  may only point into ONE finished block's output: a running block's rows
 *  move, and a selection crossing two blocks has no single frame. */
function chipSourceOf(node: Node | null): HTMLElement | null {
  const el = node instanceof Element ? node : (node?.parentElement ?? null)
  const output = el?.closest<HTMLElement>('.cmd-output')
  if (!output) return null
  const block = output.closest<HTMLElement>('.cmd-block')
  if (!block || block.classList.contains('cmd-block-running')) return null
  return output
}

/** Map a live DOM selection to a frozen-region chip, or null when the
 *  selection cannot be one: collapsed, spanning two blocks, or anchored
 *  outside a finished block's output. Both ends must land inside the SAME
 *  output's term-lines; the covered rows are the inclusive span between
 *  them. */
export function chipFromSelection(
  sel: Selection | null,
): Omit<ReferenceChip, 'id' | 'label'> | null {
  if (!sel || sel.isCollapsed || sel.rangeCount === 0) return null
  const range = sel.getRangeAt(0)
  const startOutput = chipSourceOf(range.startContainer)
  const endOutput = chipSourceOf(range.endContainer)
  if (!startOutput || startOutput !== endOutput) return null
  const lines = Array.from(startOutput.querySelectorAll<HTMLElement>('.term-line'))
  if (lines.length === 0) return null
  const startLine =
    range.startContainer instanceof Element
      ? range.startContainer.closest<HTMLElement>('.term-line')
      : range.startContainer.parentElement?.closest<HTMLElement>('.term-line')
  const endLine =
    range.endContainer instanceof Element
      ? range.endContainer.closest<HTMLElement>('.term-line')
      : range.endContainer.parentElement?.closest<HTMLElement>('.term-line')
  if (!startLine || !endLine) return null
  const rowStart = lines.indexOf(startLine)
  const rowEnd = lines.indexOf(endLine)
  if (rowStart === -1 || rowEnd === -1) return null
  const first = Math.min(rowStart, rowEnd)
  const last = Math.max(rowStart, rowEnd)
  return {
    blockEl: startOutput.closest<HTMLElement>('.cmd-block')!,
    rowStart: first,
    rowEnd: last + 1,
  }
}

/** The exact-duplicate fingerprint: same block, same rows. Reselecting the
 *  identical region must not stack a second chip. */
export function chipFingerprint(
  chip: Pick<ReferenceChip, 'blockEl' | 'rowStart' | 'rowEnd'>,
): string {
  return `${chip.blockEl.dataset.blockId ?? 'block'}:${chip.rowStart}:${chip.rowEnd}`
}

// ── The line-start indicator (ADR-0004 §3's UI chip) ───────────────────────

/** Repaint signal: the registry's active target changed (or the host wants
 *  the chip re-read). */
const refreshIndicator = StateEffect.define<null>()

/** The indicator's OWN presentation of a target — the word the person
 *  reads and the badge tone the state wears, never the target's internal
 *  name. InputTarget.label stays the registry's word ('Shell'/'Agent' —
 *  other consumers may legitimately read it); this map is the indicator's
 *  vocabulary, keyed by target id, and the tone is the badge register the
 *  running block already uses for the same author (agent = info). Unknown
 *  ids fall back to the label and the neutral tone (a future target still
 *  gets an honest chip). */
const TARGET_PRESENTATION: Record<string, { word: string; tone: BadgeTone }> = {
  shell: { word: 'Run', tone: 'neutral' },
  agent: { word: 'Ask', tone: 'info' },
}

function targetPresentation(targetId: string, label: string): { word: string; tone: BadgeTone } {
  return TARGET_PRESENTATION[targetId] ?? { word: label, tone: 'neutral' }
}

class TargetMarker extends GutterMarker {
  constructor(
    private readonly word: string,
    private readonly tone: BadgeTone,
    private readonly targetId: string,
    private readonly onToggle: () => void,
  ) {
    super()
  }
  eq(other: TargetMarker): boolean {
    return other.word === this.word && other.tone === this.tone && other.targetId === this.targetId
  }
  toDOM(): HTMLElement {
    // The kit's ModeIndicator: the badge shape and tone are ui-badge's, the
    // operable variance is the indicator's own (ui/mode-indicator.ts). The
    // gutter PLACES it; the component owns its appearance.
    return createModeIndicator({
      word: this.word,
      tone: this.tone,
      targetId: this.targetId,
      onClick: this.onToggle,
    })
  }
}

/**
 * The line-start indicator: the token in the prompt rendering what the
 * ACTIVE target does — `Run` for the shell, `Ask` for the assistant — and
 * toggling the target on click. The host wires the registry's active target
 * and pushes every change through set(); the word and tone are this
 * module's own mapping (targetPresentation), never a rename of the target.
 * The editor stays passive; this is a decoration, never a second input owner.
 *
 * IT IS A GUTTER, NOT A WIDGET IN THE DOCUMENT, and that is the whole point
 * (nocx-4wtlh, after the widget shipped and was lived with). A widget at
 * position 0 sits INSIDE `.cm-content` — the element carrying
 * `role="textbox"` — so the control's label became part of the textbox's
 * text: a screen reader read "Run printf ..." as the line's content, and
 * every e2e assertion that reads the prompt got `Runprintf ...` back. The
 * same placement also cost three separate repairs, each a symptom of a
 * control pretending to be text: CM6's zero-width widget buffer painting a
 * broken-image square when it was given the trailing gap, a chip sitting
 * below a baseline it shares with larger text, and a second line that began
 * underneath the token because only the first line had one.
 *
 * A gutter answers all of it structurally. It lives beside the content, not
 * in it, so the textbox contains exactly what the person typed; it reserves
 * its width on EVERY line, so continuation lines and wrapped rows align
 * where the caret starts with no measurement and no hanging indent; and it
 * is where an editor is expected to put a prompt sigil.
 */
export class TargetIndicator {
  /** The word currently rendered, for the gutter's marker. */
  word = targetPresentation('shell', 'Shell').word
  /** The badge tone currently rendered — the active state's register. */
  tone: BadgeTone = targetPresentation('shell', 'Shell').tone
  /** The target id currently rendered (the data-target hook). */
  targetId = 'shell'
  /** The explicit switch (ADR-0004 §3): wired once by the host; the marker
   *  and the ⌘/Ctrl+Enter seam both end here. Reads the registry live at
   *  call time, so it never goes stale. */
  readonly toggle: () => void
  private view: EditorView | null = null

  constructor(toggle: () => void) {
    this.toggle = toggle
  }

  /** The CM6 extension the host feeds to the CommandEditor. */
  extension(): Extension {
    return indicatorGutter(this)
  }

  /** The gutter registers the live view so set() can repaint it. */
  attachView(view: EditorView): void {
    this.view = view
  }

  /** Repaint with the registry's active target — called by the host
   *  whenever the registry reports a change, never on any other signal.
   *  The WORD and TONE are derived here (targetPresentation); the
   *  indicator never shows the target's internal label. */
  set(targetId: string, label: string): void {
    const p = targetPresentation(targetId, label)
    if (this.word === p.word && this.tone === p.tone && this.targetId === targetId && this.view)
      return
    this.word = p.word
    this.tone = p.tone
    this.targetId = targetId
    this.view?.dispatch({ effects: refreshIndicator.of(null) })
  }
}

/** The gutter half of the indicator: one marker, on the FIRST line only,
 *  repainted when set() dispatches its effect. Later lines get the gutter's
 *  reserved width and no marker — the token is the prompt's sigil, not a
 *  per-line ornament — and `initialSpacer` keeps that width stable from the
 *  first frame instead of letting the text jump left before the marker
 *  renders. */
function indicatorGutter(indicator: TargetIndicator): Extension {
  const marker = (): TargetMarker =>
    new TargetMarker(indicator.word, indicator.tone, indicator.targetId, indicator.toggle)
  return [
    ViewPlugin.define((view) => {
      indicator.attachView(view)
      // CM6 marks its gutter container aria-hidden, which is right for the
      // thing gutters usually hold — line numbers are decoration, and a
      // screen reader gains nothing from them. Ours holds an operable
      // control: the one explicit switch ADR-0004 §3 requires. A focusable
      // button inside an aria-hidden subtree is exposed to nobody, so the
      // attribute is removed for THIS editor's gutters and no other's. The
      // measurement spacer CM6 keeps beside the real marker is
      // visibility:hidden, which assistive technology already ignores, so
      // removing the attribute exposes one control and not two.
      view.requestMeasure({
        read: () => null,
        write: (_, v) => v.dom.querySelector('.cm-gutters')?.removeAttribute('aria-hidden'),
      })
      return {}
    }),
    gutter({
      class: 'nocx-editor-target-gutter',
      lineMarker: (view, line) => (line.from === 0 ? marker() : null),
      initialSpacer: () => marker(),
      updateSpacer: () => marker(),
      lineMarkerChange: (update) =>
        update.transactions.some((t) => t.effects.some((e) => e.is(refreshIndicator))),
    }),
  ]
}
